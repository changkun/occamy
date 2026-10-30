// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ssh_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"image"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// serverOptions configure a testServer. Each authentication method is
// offered only when its field is set.
type serverOptions struct {
	password  string          // the password that password auth accepts
	clientKey gossh.PublicKey // the key that public key auth accepts
	questions []question      // what keyboard-interactive auth asks
	// retries is how often keyboard-interactive asks again after wrong
	// answers, as PAM may.
	retries int
	// secondFactor makes password auth a partial success that
	// keyboard-interactive (questions) must complete, instead of
	// offering keyboard-interactive on its own.
	secondFactor bool
	// hostKeys are the server's host keys; nil means one Ed25519 key.
	hostKeys []gossh.Signer
	// ignoreInput leaves the shell's stdin unread instead of echoing it.
	ignoreInput bool
	// stderr is written to the shell's stderr when it starts.
	stderr string
}

// question is one keyboard-interactive question and the answer the
// server accepts.
type question struct {
	prompt string
	echo   bool
	answer string
}

// ending tells a shell how to end.
type ending struct {
	output   string // written to stdout first
	status   uint32
	noStatus bool // close the channel without an exit status
}

// testServer is an SSH server on the loopback interface. Its shell
// echoes stdin to stdout, records the terminal's sizes, and ends when
// told.
type testServer struct {
	addr     string
	hostKeys []gossh.Signer

	opts   serverOptions
	config *gossh.ServerConfig
	ln     net.Listener
	ends   chan ending
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	conns   []net.Conn
	term    string
	sizes   []image.Point // the pty-req's size, then each window-change's
	shells  int
	answers [][]string // each keyboard-interactive round's answers
}

// newServer starts a server that the test's cleanup stops.
func newServer(t *testing.T, opts serverOptions) *testServer {
	t.Helper()
	if opts.hostKeys == nil {
		opts.hostKeys = []gossh.Signer{newSigner(t, newEd25519(t))}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{
		addr:     ln.Addr().String(),
		hostKeys: opts.hostKeys,
		opts:     opts,
		config:   &gossh.ServerConfig{},
		ln:       ln,
		ends:     make(chan ending, 1),
	}
	for _, k := range opts.hostKeys {
		s.config.AddHostKey(k)
	}
	if opts.password != "" {
		s.config.PasswordCallback = func(_ gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			if string(pw) != opts.password {
				return nil, errors.New("wrong password")
			}
			if opts.secondFactor {
				return nil, &gossh.PartialSuccessError{Next: gossh.ServerAuthCallbacks{
					KeyboardInteractiveCallback: s.keyboardInteractive,
				}}
			}
			return nil, nil
		}
	}
	if opts.clientKey != nil {
		want := opts.clientKey.Marshal()
		s.config.PublicKeyCallback = func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), want) {
				return nil, errors.New("unknown key")
			}
			return nil, nil
		}
	}
	if opts.questions != nil && !opts.secondFactor {
		s.config.KeyboardInteractiveCallback = s.keyboardInteractive
	}
	s.wg.Go(s.accept)
	t.Cleanup(s.close)
	return s
}

// keyboardInteractive asks the configured questions, and again after
// wrong answers as often as retries allows.
func (s *testServer) keyboardInteractive(_ gossh.ConnMetadata, client gossh.KeyboardInteractiveChallenge) (*gossh.Permissions, error) {
	var prompts, want []string
	var echos []bool
	for _, q := range s.opts.questions {
		prompts = append(prompts, q.prompt)
		echos = append(echos, q.echo)
		want = append(want, q.answer)
	}
	// A round without questions first, as OpenSSH sends.
	if _, err := client("", "Welcome.", nil, nil); err != nil {
		return nil, err
	}
	for try := 0; ; try++ {
		answers, err := client("Login", "Answer the questions.", prompts, echos)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.answers = append(s.answers, answers)
		s.mu.Unlock()
		if slices.Equal(answers, want) {
			return nil, nil
		}
		if try >= s.opts.retries {
			return nil, errors.New("wrong answers")
		}
	}
}

func (s *testServer) accept() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.conns = append(s.conns, nc)
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(nc) })
	}
}

func (s *testServer) serve(nc net.Conn) {
	defer nc.Close()
	conn, chans, reqs, err := gossh.NewServerConn(nc, s.config)
	if err != nil {
		return
	}
	defer conn.Close()
	s.wg.Go(func() { gossh.DiscardRequests(reqs) })
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(gossh.UnknownChannelType, "sessions only")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		s.wg.Go(func() { s.session(ch, creqs) })
	}
}

func (s *testServer) session(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for {
		select {
		case req, ok := <-reqs:
			if !ok {
				return
			}
			s.request(ch, req)
		case e := <-s.ends:
			io.WriteString(ch, e.output)
			if !e.noStatus {
				ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{e.status}))
			}
			return
		}
	}
}

func (s *testServer) request(ch gossh.Channel, req *gossh.Request) {
	switch req.Type {
	case "pty-req":
		var m struct {
			Term                      string
			Cols, Rows, Width, Height uint32
			Modes                     string
		}
		if err := gossh.Unmarshal(req.Payload, &m); err != nil {
			req.Reply(false, nil)
			return
		}
		s.mu.Lock()
		s.term = m.Term
		s.sizes = append(s.sizes, image.Pt(int(m.Cols), int(m.Rows)))
		s.mu.Unlock()
		req.Reply(true, nil)
	case "window-change":
		var m struct{ Cols, Rows, Width, Height uint32 }
		if err := gossh.Unmarshal(req.Payload, &m); err != nil {
			return
		}
		s.mu.Lock()
		s.sizes = append(s.sizes, image.Pt(int(m.Cols), int(m.Rows)))
		s.mu.Unlock()
	case "shell":
		req.Reply(true, nil)
		if s.opts.stderr != "" {
			io.WriteString(ch.Stderr(), s.opts.stderr)
		}
		if !s.opts.ignoreInput {
			s.wg.Go(func() { io.Copy(ch, ch) })
		}
		s.mu.Lock()
		s.shells++
		s.mu.Unlock()
	default:
		req.Reply(false, nil)
	}
}

// end makes the shell end as e says.
func (s *testServer) end(e ending) { s.ends <- e }

// drop closes every connection without a word, as a lost network does.
func (s *testServer) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *testServer) close() {
	s.ln.Close()
	s.mu.Lock()
	s.closed = true
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Shells is the number of shells started.
func (s *testServer) Shells() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shells
}

// Term is the TERM of the last pty-req.
func (s *testServer) Term() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.term
}

// Answers returns the answers of each keyboard-interactive round with
// questions.
func (s *testServer) Answers() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.answers)
}

// Sizes returns the pty-req's size and then each window-change's.
func (s *testServer) Sizes() []image.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sizes)
}

func newEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func newECDSA(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func newSigner(t *testing.T, priv crypto.Signer) gossh.Signer {
	t.Helper()
	s, err := gossh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// authorizedKey formats k as a line of authorized_keys, newline included.
func authorizedKey(k gossh.PublicKey) string {
	return string(gossh.MarshalAuthorizedKey(k))
}

// writeKeyFile writes priv in OpenSSH format to a file and returns its
// path. A non-empty passphrase encrypts it.
func writeKeyFile(t *testing.T, priv crypto.PrivateKey, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = gossh.MarshalPrivateKey(priv, "test")
	} else {
		block, err = gossh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
