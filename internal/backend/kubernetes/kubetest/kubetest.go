// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package kubetest is a fake Kubernetes API server on the loopback
// interface, for tests of the Kubernetes backend and of occamyd. It
// serves pod exec and attach over HTTPS with the WebSocket channel
// subprotocols v5.channel.k8s.io and v4.channel.k8s.io, as the API
// server and the kubelet frame them: one binary message per write, its
// first byte the channel, an empty message on stdout once the stream is
// ready, and a metav1.Status on the error channel before the end. Every
// request that reaches it is recorded, and every message a client sends
// on a stream is handed to the test.
package kubetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The subprotocols.
const (
	ProtocolV5 = "v5.channel.k8s.io"
	ProtocolV4 = "v4.channel.k8s.io"
)

// The channels.
const (
	Stdin  = 0
	Stdout = 1
	Stderr = 2
	Error  = 3
	Resize = 4
	// CloseSignal is v5's: [255, channel] closes a channel.
	CloseSignal = 255
)

// Options configure a Server.
type Options struct {
	// Protocols are the subprotocols the server picks from, in its order
	// of preference. Nil means v5, then v4. A list the client offers
	// none of leaves the stream without one.
	Protocols []string
	// Token, if set, is the only bearer token accepted: a request
	// without it is answered 401.
	Token string
	// ClientCAs, if set, makes the TLS handshake require a client
	// certificate that they sign.
	ClientCAs *x509.CertPool
	// Refuse, if set, answers every request with this status code and a
	// metav1.Status whose message is RefuseMessage.
	Refuse        int
	RefuseMessage string
	// Handler, if set, answers every request in the server's place.
	Handler http.Handler
	// Hold, if set, keeps each upgrade waiting until it is closed.
	Hold <-chan struct{}
	// Certificate, if set, is the server's in place of httptest's, which
	// every Server shares.
	Certificate *tls.Certificate
}

// Request is a request that reached the server's handler.
type Request struct {
	Method string
	// Path is the request's path as it was sent, escaped.
	Path   string
	Query  url.Values
	Header http.Header
	// ClientCertificate is the client's certificate, if it sent one.
	ClientCertificate *x509.Certificate
}

// Server is a fake API server.
type Server struct {
	srv   *httptest.Server
	opts  Options
	execs chan *Exec
	quit  chan struct{} // closed by Close

	mu       sync.Mutex
	requests []Request
	open     []*Exec
	closed   bool
}

// NewServer starts a server on the loopback interface, with httptest's
// certificate for 127.0.0.1. Close stops it.
func NewServer(opts Options) *Server {
	if opts.Protocols == nil {
		opts.Protocols = []string{ProtocolV5, ProtocolV4}
	}
	s := &Server{opts: opts, execs: make(chan *Exec, 16), quit: make(chan struct{})}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	// Refused certificates are what tests are about, not news.
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.TLS = &tls.Config{}
	if opts.ClientCAs != nil {
		s.srv.TLS.ClientAuth, s.srv.TLS.ClientCAs = tls.RequireAndVerifyClientCert, opts.ClientCAs
	}
	if opts.Certificate != nil {
		s.srv.TLS.Certificates = []tls.Certificate{*opts.Certificate}
	}
	s.srv.StartTLS()
	return s
}

// SelfSigned returns a self-signed certificate for the IP addresses or
// DNS names in hosts, for Options.Certificate.
func SelfSigned(hosts ...string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// Addr is the server's host and port.
func (s *Server) Addr() string { return s.srv.Listener.Addr().String() }

// Certificate is the server's certificate.
func (s *Server) Certificate() *x509.Certificate { return s.srv.Certificate() }

// CertificatePEM is the server's certificate in PEM, for a CA file.
func (s *Server) CertificatePEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.srv.Certificate().Raw})
}

// Fingerprint is the SHA-256 fingerprint of the server's certificate, as
// openssl prints it, for a pin.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	parts := make([]string, len(sum))
	for i, c := range sum {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

// Requests returns the requests that reached the handler so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Accept returns the next stream a client opened.
func (s *Server) Accept(ctx context.Context) (*Exec, error) {
	select {
	case e := <-s.execs:
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close ends every stream and stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	if !s.closed {
		close(s.quit)
	}
	s.closed = true
	open := s.open
	s.open = nil
	s.mu.Unlock()
	for _, e := range open {
		e.Abort()
	}
	s.srv.Close()
}

// path is the exec or attach subresource of a pod.
var path = regexp.MustCompile(`^/api/v1/namespaces/([^/]+)/pods/([^/]+)/(exec|attach)$`)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	req := Request{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone()}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		req.ClientCertificate = r.TLS.PeerCertificates[0]
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	switch {
	case s.opts.Handler != nil:
		s.opts.Handler.ServeHTTP(w, r)
		return
	case s.opts.Refuse != 0:
		WriteStatus(w, s.opts.Refuse, s.opts.RefuseMessage)
		return
	case s.opts.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.opts.Token:
		WriteStatus(w, http.StatusUnauthorized, "Unauthorized")
		return
	case r.Method != http.MethodGet:
		WriteStatus(w, http.StatusMethodNotAllowed, "the channel protocols upgrade a GET")
		return
	case !path.MatchString(req.Path):
		WriteStatus(w, http.StatusNotFound, "the server could not find the requested resource")
		return
	}
	e := &Exec{Request: req, msgs: make(chan Message, 256), done: make(chan struct{}), quit: make(chan struct{})}
	if s.opts.Hold != nil {
		select {
		case <-s.opts.Hold:
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: s.opts.Protocols,
		OnPingReceived: func(context.Context, []byte) bool {
			e.pings.Add(1)
			return true
		},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	e.conn, e.Protocol = conn, conn.Subprotocol()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.CloseNow()
		return
	}
	s.open = append(s.open, e)
	s.mu.Unlock()
	// The kubelet's empty message on the lowest output channel says the
	// stream is ready.
	if err := e.Send([]byte{Stdout}); err != nil {
		e.Abort()
		return
	}
	go e.read() //nolint:gosec // G118: the stream outlives the upgrade's request; Abort and Close end it
	select {
	case s.execs <- e:
	case <-s.quit:
	}
}

// WriteStatus answers a request with code and a metav1.Status, as the
// API server refuses one.
func WriteStatus(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{},
		"status": "Failure", "message": message, "reason": strings.ReplaceAll(http.StatusText(code), " ", ""), "code": code,
	})
}

// Exec is a stream that a client opened.
type Exec struct {
	Request
	// Protocol is the subprotocol chosen, empty if none.
	Protocol string

	conn  *websocket.Conn
	msgs  chan Message
	done  chan struct{} // closed when read returns
	err   error         // why the stream ended, set before done is closed
	quit  chan struct{} // closed by Abort
	once  sync.Once
	pings atomic.Int64
}

// Message is a message from the client: Stdin, ResizeMessage,
// CloseMessage or Other.
type Message interface{ message() }

// StdinMessage is input on stdin.
type StdinMessage struct{ Data []byte }

// ResizeMessage is a terminal size on the resize channel.
type ResizeMessage struct{ Width, Height int }

// CloseMessage is v5's close signal for Channel.
type CloseMessage struct{ Channel byte }

// Other is any other message, as it came, or a text message.
type Other struct {
	Text bool
	Data []byte
}

func (StdinMessage) message()  {}
func (ResizeMessage) message() {}
func (CloseMessage) message()  {}
func (Other) message()         {}

// read hands the client's messages to Next until the stream ends.
func (e *Exec) read() {
	defer close(e.done)
	for {
		typ, p, err := e.conn.Read(context.Background())
		if err != nil {
			e.err = err
			return
		}
		select {
		case e.msgs <- parse(typ, p):
		case <-e.quit:
			e.err = errors.New("kubetest: aborted")
			return
		}
	}
}

func parse(typ websocket.MessageType, p []byte) Message {
	if typ != websocket.MessageBinary || len(p) == 0 {
		return Other{Text: typ != websocket.MessageBinary, Data: p}
	}
	switch p[0] {
	case Stdin:
		return StdinMessage{Data: p[1:]}
	case Resize:
		var sz struct{ Width, Height int }
		if json.Unmarshal(p[1:], &sz) == nil {
			return ResizeMessage{Width: sz.Width, Height: sz.Height}
		}
	case CloseSignal:
		if len(p) == 2 {
			return CloseMessage{Channel: p[1]}
		}
	}
	return Other{Data: p}
}

// Next returns the client's next message, or the error that ended the
// stream once every message has been taken.
func (e *Exec) Next(ctx context.Context) (Message, error) {
	select {
	case m := <-e.msgs:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.done:
		select {
		case m := <-e.msgs:
			return m, nil
		default:
		}
		return nil, e.err
	}
}

// Pings is the number of pings the client has sent.
func (e *Exec) Pings() int64 { return e.pings.Load() }

// Done is closed when the stream has ended.
func (e *Exec) Done() <-chan struct{} { return e.done }

// Send sends a message as it is: its first byte is the channel.
func (e *Exec) Send(p []byte) error {
	return e.conn.Write(context.Background(), websocket.MessageBinary, p)
}

// SendText sends a text message, which no channel protocol uses.
func (e *Exec) SendText(p []byte) error {
	return e.conn.Write(context.Background(), websocket.MessageText, p)
}

// Stdout sends p on stdout.
func (e *Exec) Stdout(p []byte) error { return e.Send(append([]byte{Stdout}, p...)) }

// Stderr sends p on stderr.
func (e *Exec) Stderr(p []byte) error { return e.Send(append([]byte{Stderr}, p...)) }

// Status sends a metav1.Status, in JSON, on the error channel, and ends
// the stream, as the kubelet does when the command exits.
func (e *Exec) Status(status []byte) error {
	if err := e.Send(append([]byte{Error}, status...)); err != nil {
		return err
	}
	return e.Close()
}

// Exit ends the stream with the status of a command that exited with
// code, as the kubelet sends it.
func (e *Exec) Exit(code int) error { return e.Status(ExitStatus(code)) }

// ExitStatus is the metav1.Status of a command that exited with code.
func ExitStatus(code int) []byte {
	if code == 0 {
		return []byte(`{"metadata":{},"status":"Success"}`)
	}
	return fmt.Appendf(nil, `{"metadata":{},"status":"Failure","message":"command terminated with non-zero exit code: error executing command [sh], exit code %d","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"%d"}]}}`, code, code)
}

// Close ends the stream with a WebSocket close, and no status unless one
// was sent. The close handshake's error is dropped: the client may drop
// the connection as soon as it has answered, before the server reads
// the answer.
func (e *Exec) Close() error {
	_ = e.conn.Close(websocket.StatusNormalClosure, "")
	return nil
}

// Abort drops the connection without a close.
func (e *Exec) Abort() {
	e.once.Do(func() { close(e.quit) })
	_ = e.conn.CloseNow()
}
