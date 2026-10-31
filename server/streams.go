// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// streamLowWater is TCP_NOTSENT_LOWAT on a stream's socket: the unsent
// bytes the kernel may hold, about a raw 64-pixel tile, so that the frame
// the pacer picks next waits behind little.
const streamLowWater = 16 << 10

// setLowWater is stream.SetLowWater, a variable for tests.
var setLowWater = stream.SetLowWater

// streamCapabilities are what occamyd offers a stream: the codecs that
// internal/encoder sends, and an xterm-compatible terminal.
var streamCapabilities = protocol.Capabilities{Codecs: encoder.Codecs(), Terminal: 1}

// newEncoder is a viewer's encoder for the codecs it agreed to, or nil
// if it sends none of them, which stream.Serve closes with unsupported.
func newEncoder(codecs []protocol.Codec) stream.Encoder {
	if e := encoder.New(codecs); e != nil {
		return e
	}
	return nil
}

// Timeouts of a stream's end.
const (
	// closeWriteTimeout bounds the wait to send close to a client that
	// does not read, as stream.Serve bounds its own.
	closeWriteTimeout = time.Second
	// takeoverTimeout bounds the wait for an owner's older stream to end
	// when a newer one takes over.
	takeoverTimeout = 5 * time.Second
	// refuseTimeout bounds a refused stream, from its close to the end of
	// the closing handshake.
	refuseTimeout = time.Second
)

// registerStreams registers the stream routes. They authenticate on
// their own: a browser cannot set Authorization on a WebSocket, and a
// refusal must come as close after the upgrade, since a browser cannot
// read the status of a refused one.
func (s *server) registerStreams(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/connections/{id}/stream", s.connectionStream)
	mux.HandleFunc("GET /api/v1/sessions/{id}/stream", s.sessionStream)
}

// connectionStream implements GET /api/v1/connections/{id}/stream: it
// opens a session on the connection for the caller of an access token,
// and streams it to them as its owner, so that the first frame comes one
// round trip after the upgrade. The owner's hello sizes the session.
func (s *server) connectionStream(w http.ResponseWriter, r *http.Request) {
	s.streams.add()
	defer s.streams.done()
	req := s.upgrade(w, r)
	if req == nil {
		return
	}
	claims, err := s.verifier.VerifyAccess(req.token)
	if err != nil {
		req.c.refuse(refuseToken(err))
		return
	}
	p, err := s.ids.of(claims)
	if err != nil {
		req.c.refuse(refuseToken(err))
		return
	}
	c := s.connection(r.PathValue("id"), claims)
	if c == nil {
		req.c.refuse(protocol.Close{Code: protocol.CloseForbidden, Reason: "no such connection"})
		return
	}
	info, owner, err := s.open(connectionTarget(c), p, screenOf(req.hello))
	if errors.Is(err, errSessionLimit) || errors.Is(err, errSessionsFull) {
		req.c.refuse(protocol.Close{Code: protocol.CloseForbidden, Reason: err.Error()})
		return
	}
	if err != nil {
		req.c.refuse(protocol.Close{Code: protocol.CloseInternalError, Reason: "the session could not be opened"})
		return
	}
	s.serve(r.Context(), req.c, info, owner, req.hello)
}

// sessionStream implements GET /api/v1/sessions/{id}/stream: it joins a
// running session, as its owner with the owner's access token or a
// session token for the owner, or with the role of a session token or a
// share. The owner's hello resizes the session.
func (s *server) sessionStream(w http.ResponseWriter, r *http.Request) {
	s.streams.add()
	defer s.streams.done()
	req := s.upgrade(w, r)
	if req == nil {
		return
	}
	info, v, refuse := s.join(r.PathValue("id"), req.token)
	if refuse != nil {
		req.c.refuse(*refuse)
		return
	}
	if v.Role() == session.RoleOwner {
		if ev, ok := resizeFor(info.s.Class(), screenOf(req.hello)); ok {
			if err := v.Input(0, ev); err != nil {
				log.Printf("session %s: resizing to the owner's hello: %v", info.id, err)
			}
		}
	}
	s.serve(r.Context(), req.c, info, v, req.hello)
}

// streamRequest is an upgraded stream request whose Origin, token and
// hello passed the checks every stream route makes. Its read limit stays
// at protocol.MinMessageSize until serve, so that a client whose token
// is refused has a small one.
type streamRequest struct {
	c     *wsConn
	token string // not verified yet: what it may be depends on the route
	hello protocol.Hello
}

// upgrade checks what every stream request carries, and upgrades it. It
// returns nil once it has answered: with an HTTP error to a request that
// does not offer occamy.v1 or is no WebSocket upgrade, and with close
// after the upgrade to one whose Origin, token or hello is refused,
// since a browser cannot read the status of a refused upgrade. The token
// comes from the occamy.v1.token. subprotocol or, if there is none, from
// Authorization; never from the URL, which proxies log. The route
// verifies it after the upgrade, since what it may be depends on the
// route, and refuses it with close too.
func (s *server) upgrade(w http.ResponseWriter, r *http.Request) *streamRequest {
	if !offers(r, protocol.Subprotocol) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "a stream is a WebSocket with the subprotocol "+protocol.Subprotocol)
		return nil
	}
	var (
		refuse *protocol.Close
		token  string
		hello  protocol.Hello
		err    error
	)
	if !s.originAllowed(r) {
		refuse = &protocol.Close{Code: protocol.CloseForbidden, Reason: "origin not allowed"}
	} else if token, refuse = streamToken(r); refuse == nil {
		if hello, err = protocol.DecodeHelloQuery(r.URL.Query().Get(protocol.HelloParameter)); err != nil {
			refuse = &protocol.Close{Code: protocol.CloseCodeFor(err), Reason: "invalid hello"}
			if errors.Is(err, protocol.ErrUnsupported) {
				refuse.Reason = "unsupported protocol version"
			}
		}
	}
	// The WebSocket hides the socket, so its unsent bytes are limited
	// now.
	conn, ok := connFrom(r.Context())
	if ok {
		if err := setLowWater(conn, streamLowWater); err != nil {
			// #nosec G706 -- the remote address comes from net/http, not the client's data.
			log.Printf("stream from %s: setting TCP_NOTSENT_LOWAT: %v", r.RemoteAddr, err)
		}
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{protocol.Subprotocol},
		// Origin is checked above, and a refused one still gets the
		// upgrade, then close.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return nil // Accept has answered
	}
	ws.SetReadLimit(protocol.MinMessageSize)
	c := newWSConn(ws, conn)
	if refuse != nil {
		// #nosec G706 -- the reason is one of the constants above.
		log.Printf("stream refused from %s: %s", r.RemoteAddr, refuse.Reason)
		c.refuse(*refuse)
		return nil
	}
	return &streamRequest{c: c, token: token, hello: hello}
}

// offers reports whether the request offers the WebSocket subprotocol
// sub.
func offers(r *http.Request, sub string) bool {
	for _, line := range r.Header.Values("Sec-WebSocket-Protocol") {
		for p := range strings.SplitSeq(line, ",") {
			if strings.Trim(p, " \t") == sub {
				return true
			}
		}
	}
	return false
}

// originAllowed reports whether a page of the request's Origin may open
// a stream: its own, whose host is the request's, or one of origins. Its
// own is https when occamyd terminates TLS; behind a proxy that does,
// occamyd cannot tell the scheme, and takes either. A request without
// Origin comes from no browser, which is no page to guard against. An
// empty Origin, a repeated one and null are refused.
func (s *server) originAllowed(r *http.Request) bool {
	o, ok := r.Header["Origin"]
	if !ok {
		return true
	}
	if len(o) != 1 {
		return false
	}
	u, err := url.Parse(o[0])
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) && (r.TLS == nil || u.Scheme == "https") {
		return true
	}
	return s.origins[u.Scheme+"://"+strings.ToLower(u.Host)]
}

// streamToken returns the token of a stream request: the one in the
// occamy.v1.token. subprotocol, or else the one in Authorization.
func streamToken(r *http.Request) (string, *protocol.Close) {
	token, err := protocol.TokenFromSubprotocols(r.Header.Values("Sec-WebSocket-Protocol"))
	if err != nil {
		return "", &protocol.Close{Code: protocol.CloseUnauthorized, Reason: "invalid token subprotocol"}
	}
	if token != "" {
		return token, nil
	}
	if token, ok := bearerToken(r); ok {
		return token, nil
	}
	return "", &protocol.Close{Code: protocol.CloseUnauthorized, Reason: "a token is required"}
}

// refuseToken is the close for a token that failed verification, or
// whose subject identities refused.
func refuseToken(err error) protocol.Close {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return protocol.Close{Code: protocol.CloseUnauthorized, Reason: "token expired"}
	case errors.Is(err, errStaticUser):
		return protocol.Close{Code: protocol.CloseUnauthorized, Reason: "the token's subject is a static user's name"}
	}
	return protocol.Close{Code: protocol.CloseUnauthorized, Reason: "invalid token"}
}

// join joins session id with what token grants, or returns the close
// that refuses it:
//
//   - an access token of the session's owner joins as the owner;
//   - a session token for the session joins with its role, as the owner
//     only if it names the owner;
//   - a share of the registry for the session joins with its role.
//
// The owner is a principal: the same subject from another issuer is
// another user. A trusted issuer's session token, of any role, joins
// only its own users' sessions, or a static user's if it is linked.
//
// A session token's expiry, like an access token's, does not end a
// stream that has joined; the session's end does.
func (s *server) join(id, token string) (*sessionInfo, *session.Viewer, *protocol.Close) {
	forbidden := &protocol.Close{Code: protocol.CloseForbidden, Reason: "no such session"}
	if !strings.Contains(token, ".") {
		// A JWT has three parts; a share has none.
		v, err := s.registry.JoinShared(id, token)
		if errors.Is(err, session.ErrViewerLimit) {
			return nil, nil, &viewerLimit
		}
		if err != nil {
			return nil, nil, &protocol.Close{Code: protocol.CloseUnauthorized, Reason: "invalid token"}
		}
		info := s.session(id)
		if info == nil {
			v.Leave()
			return nil, nil, forbidden
		}
		return info, v, nil
	}
	claims, err := s.verifier.VerifyAccess(token)
	if errors.Is(err, auth.ErrKind) {
		claims, err = s.verifier.VerifySession(token)
	}
	var p principal
	if err == nil {
		p, err = s.ids.of(claims)
	}
	if err != nil {
		refuse := refuseToken(err)
		return nil, nil, &refuse
	}
	info := s.session(id)
	if info == nil {
		return nil, nil, forbidden
	}
	role := session.RoleOwner
	if claims.Kind == auth.KindSession {
		if claims.Session != id || !s.ids.grants(claims, info.owner) {
			return nil, nil, forbidden
		}
		switch claims.Role {
		case auth.RoleViewer:
			role = session.RoleViewer
		case auth.RoleReadOnly:
			role = session.RoleReadOnly
		}
	}
	if role == session.RoleOwner && p != info.owner {
		return nil, nil, forbidden
	}
	var v *session.Viewer
	if role == session.RoleOwner {
		v, err = s.joinOwner(info)
	} else {
		v, err = info.s.Join(role)
	}
	switch {
	case errors.Is(err, session.ErrOwnerPresent):
		return nil, nil, &protocol.Close{Code: protocol.CloseForbidden, Reason: "the owner is connected already"}
	case errors.Is(err, session.ErrViewerLimit):
		return nil, nil, &viewerLimit
	case err != nil:
		return nil, nil, forbidden
	}
	return info, v, nil
}

// viewerLimit refuses a viewer of a session that has as many as it takes.
var viewerLimit = protocol.Close{Code: protocol.CloseViewerLimit, Reason: "the session has as many viewers as it takes"}

// ownerStream is the stream an owner is connected through.
type ownerStream struct {
	conn   *wsConn
	cancel context.CancelFunc // ends the stream
	done   chan struct{}      // closed once it has left the session
}

// joinOwner joins info's session as its owner. A newer stream of the
// owner takes over from an older one, which gets close with normal: the
// older one may be on a connection that died without occamyd noticing,
// which would otherwise keep the owner out of their own session until
// TCP gives up.
func (s *server) joinOwner(info *sessionInfo) (*session.Viewer, error) {
	v, err := info.s.Join(session.RoleOwner)
	if !errors.Is(err, session.ErrOwnerPresent) {
		return v, err
	}
	info.mu.Lock()
	old := info.ownerStream
	info.mu.Unlock()
	if old == nil {
		return nil, err
	}
	old.conn.replace(protocol.Close{Code: protocol.CloseNormal, Reason: "the owner connected again"})
	old.cancel()
	t := time.NewTimer(takeoverTimeout)
	defer t.Stop()
	select {
	case <-old.done:
	case <-t.C:
		return nil, err
	}
	return info.s.Join(session.RoleOwner)
}

// serve serves v, a viewer of info's session, over c until the stream
// ends, and closes the WebSocket.
func (s *server) serve(ctx context.Context, c *wsConn, info *sessionInfo, v *session.Viewer, hello protocol.Hello) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var own *ownerStream
	if v.Role() == session.RoleOwner {
		own = &ownerStream{conn: c, cancel: cancel, done: make(chan struct{})}
		info.mu.Lock()
		info.ownerStream = own
		info.mu.Unlock()
	}
	// The client is let in: its messages may be as large as agreed.
	c.ws.SetReadLimit(int64(protocol.Agree(hello.Capabilities, streamCapabilities).Limits().MaxMessageSize))
	err := serveStream(ctx, c, v, hello, stream.Config{
		SessionID:     info.id,
		Capabilities:  streamCapabilities,
		NewEncoder:    newEncoder,
		ViewerTimeout: s.streamsCfg.ViewerTimeout,
	})
	if own != nil {
		info.mu.Lock()
		if info.ownerStream == own {
			info.ownerStream = nil
		}
		info.mu.Unlock()
		close(own.done)
	}
	var p panicked
	switch m := c.replacement(); {
	case errors.As(err, &p):
		// #nosec G706 -- the session's ID is occamyd's, and the panic is the stream's.
		log.Printf("session %s: viewer %d: the stream panicked: %v\n%s", info.id, v.ID(), p.value, p.stack)
		c.finish(protocol.Close{Code: protocol.CloseInternalError, Reason: "internal error"}, true)
	case m != nil:
		c.finish(*m, true)
	case err == nil:
		// The session ended, and Serve sent close, or the client left.
		c.finish(protocol.Close{Code: protocol.CloseNormal}, false)
	case errors.Is(err, errText):
		// Serve takes any failed read for a failed connection, after
		// which there is nobody to tell; after a text message there is.
		c.finish(protocol.Close{Code: protocol.CloseUnsupported, Reason: "a text message"}, true)
	case errors.Is(err, websocket.ErrMessageTooBig):
		// The client's mistake, which coder/websocket has answered with
		// 1009 already.
		c.finish(protocol.Close{Code: protocol.CloseTooLarge}, false)
	case errors.Is(err, stream.ErrStalled):
		// Serve has logged it and sent close, unless a write blocked,
		// which leaves nothing to tell.
		c.finish(protocol.Close{Code: protocol.CloseTooSlow}, false)
	default:
		// #nosec G706 -- the session's ID is occamyd's, and the error is the stream's.
		log.Printf("session %s: viewer %d: the stream ended: %v", info.id, v.ID(), err)
		c.finish(protocol.Close{Code: protocol.CloseInternalError, Reason: "internal error"}, true)
	}
}

// streamServe is stream.Serve, a variable for tests.
var streamServe = stream.Serve

// panicked is the error of a stream that panicked.
type panicked struct {
	value any
	stack []byte
}

func (p panicked) Error() string { return fmt.Sprintf("panic: %v", p.value) }

// serveStream runs stream.Serve and recovers from its panic: the viewer
// leaves the session, and serve still closes the WebSocket and lets a
// newer stream of the owner in. net/http would recover the panic too,
// but leave an upgraded connection open, with its reader running, until
// the client left.
func serveStream(ctx context.Context, c *wsConn, v *session.Viewer, hello protocol.Hello, cfg stream.Config) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicked{value: r, stack: debug.Stack()}
			v.Leave() // Serve's own deferred Leave has run, unless it panicked before
		}
	}()
	return streamServe(ctx, c, v, hello, cfg)
}

// screenOf is the size a hello asks for, within the bounds of a Resize.
func screenOf(h protocol.Hello) screen {
	return screen{
		width:  min(int(h.Width), backend.MaxResizeSide),
		height: min(int(h.Height), backend.MaxResizeSide),
		cols:   min(int(h.Cols), terminal.MaxCols),
		rows:   min(int(h.Rows), terminal.MaxRows),
	}
}

// wsConn is a stream's WebSocket as a stream.Conn.
//
// coder/websocket drops the connection, without a close frame, when the
// context of a read ends, and stream.Serve ends the context of its reads
// as soon as it is done. A goroutine therefore reads without a context,
// and ReadMessage waits for it or for its own context. Serve sends close
// but cannot close the WebSocket, so wsConn keeps the close it sent, and
// finish closes the WebSocket with the same code.
type wsConn struct {
	ws    *websocket.Conn
	raw   net.Conn // the socket below, or nil if the server does not keep it
	start sync.Once
	in    chan readResult
	quit  chan struct{} // closed by finish: nobody reads any more
	ended sync.Once

	mu       sync.Mutex
	sent     *protocol.Close // the close message the stream sent
	replaced *protocol.Close // the close for a stream that another took over
	failed   bool            // the connection failed
	tooLarge bool            // the client sent a message over the read limit
}

type readResult struct {
	p   []byte
	err error
}

// errText reports a text message, which protocol v1 does not allow.
var errText = fmt.Errorf("%w: a text message", protocol.ErrUnsupported)

func newWSConn(ws *websocket.Conn, raw net.Conn) *wsConn {
	return &wsConn{ws: ws, raw: raw, in: make(chan readResult), quit: make(chan struct{})}
}

// read reads messages for ReadMessage until the connection fails or
// closes, or finish stops it.
func (c *wsConn) read() {
	for {
		typ, p, err := c.ws.Read(context.Background())
		if err == nil && typ != websocket.MessageBinary {
			err = errText
		}
		select {
		case c.in <- readResult{p, err}:
		case <-c.quit:
			return
		}
		if err != nil {
			return
		}
	}
}

// ReadMessage implements stream.Conn.
func (c *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	c.start.Do(func() { go c.read() })
	select {
	case r := <-c.in:
		switch {
		case r.err == nil:
			return r.p, nil
		case errors.Is(r.err, errText):
			return nil, r.err
		case websocket.CloseStatus(r.err) != -1:
			return nil, io.EOF // the client closed the WebSocket
		case errors.Is(r.err, websocket.ErrMessageTooBig):
			c.mu.Lock()
			c.tooLarge = true
			c.mu.Unlock()
			return nil, r.err
		}
		c.fail()
		return nil, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// WriteMessage implements stream.Conn.
//
// coder/websocket drops the connection when the context of a write in
// progress ends. Serve ends its writer's context when the client breaks
// the protocol, and then sends close, as does a stream that another
// takes over; a frame being written at that moment would cut both off.
// A write that has started therefore gets closeWriteTimeout to finish
// after ctx ends. context.AfterFunc starts no goroutine unless ctx ends
// during the write.
func (c *wsConn) WriteMessage(ctx context.Context, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		t := time.NewTimer(closeWriteTimeout)
		defer t.Stop()
		select {
		case <-t.C:
			cancel()
		case <-wctx.Done():
		}
	})
	defer stop()
	if err := c.ws.Write(wctx, websocket.MessageBinary, p); err != nil {
		c.fail()
		return err
	}
	if m, ok := closeMessage(p); ok {
		c.mu.Lock()
		c.sent = &m
		c.mu.Unlock()
	}
	return nil
}

func (c *wsConn) fail() {
	c.mu.Lock()
	c.failed = true
	c.mu.Unlock()
}

// replace marks the stream as taken over: it ends with m.
func (c *wsConn) replace(m protocol.Close) {
	c.mu.Lock()
	c.replaced = &m
	c.mu.Unlock()
}

func (c *wsConn) replacement() *protocol.Close {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replaced
}

// refuse ends a stream before it starts: it sends m and closes the
// WebSocket with its code, as finish does, but drops the connection once
// refuseTimeout has passed, rather than wait up to 5 s each way for the
// closing handshake of a client that may not have authenticated.
func (c *wsConn) refuse(m protocol.Close) {
	if c.raw != nil {
		t := time.AfterFunc(refuseTimeout, func() { _ = c.raw.Close() })
		defer t.Stop()
	}
	c.finish(m, true)
}

// finish closes the WebSocket once the stream is over: with the code of
// the close message the stream sent, or else with m, which it sends
// first if tell is set. A connection that failed is dropped: nobody is
// left to tell. After a message over the read limit, coder/websocket
// has sent close 1009 itself; Close then sends nothing more, but reads
// the rest of the message, with no deadline, and the client's close, so
// it gets refuseTimeout, as a refused stream does. Dropping the
// connection at once would let a client still writing, such as acking
// a frame it read before the close, fail with a broken pipe, or lose
// the close to the reset. Only the first call does anything.
func (c *wsConn) finish(m protocol.Close, tell bool) {
	c.ended.Do(func() {
		close(c.quit)
		c.mu.Lock()
		sent, failed, tooLarge := c.sent, c.failed, c.tooLarge
		c.mu.Unlock()
		switch {
		case tooLarge && c.raw != nil:
			// Before failed: a write of the stream's after the 1009
			// fails and marks the connection failed, though the client
			// may still answer.
			t := time.AfterFunc(refuseTimeout, func() { _ = c.raw.Close() })
			defer t.Stop()
			_ = c.ws.Close(websocket.StatusMessageTooBig, "")
			return
		case failed || tooLarge:
			_ = c.ws.CloseNow()
			return
		case sent != nil:
			m = *sent
		case tell:
			ctx, cancel := context.WithTimeout(context.Background(), closeWriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageBinary, m.Append(nil))
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
		}
		// The closing handshake takes up to 5 s each way for a client
		// that does not answer.
		_ = c.ws.Close(websocket.StatusCode(m.Code), trimReason(m.Reason))
	})
}

// closeMessage decodes p if it is a close message.
func closeMessage(p []byte) (protocol.Close, bool) {
	var m protocol.Close
	if len(p) > 1024 {
		return m, false // a close message is small; this is a frame's
	}
	h, body, err := protocol.Split(p, protocol.Limits{})
	if err != nil || h.Type != protocol.TypeClose || m.Decode(body, protocol.Limits{}) != nil {
		return m, false
	}
	return m, true
}

// maxCloseReason is the longest reason a WebSocket close frame carries.
const maxCloseReason = 123

// trimReason cuts reason to fit a close frame, at a character boundary.
func trimReason(reason string) string {
	if len(reason) <= maxCloseReason {
		return reason
	}
	n := maxCloseReason
	for n > 0 && !utf8.RuneStart(reason[n]) {
		n--
	}
	return reason[:n]
}

// tracker counts the streams being served, so that a shutdown can wait
// for them.
type tracker struct {
	mu   sync.Mutex
	n    int
	zero chan struct{} // closed when n reaches zero, while someone waits
}

func (t *tracker) add() {
	t.mu.Lock()
	t.n++
	t.mu.Unlock()
}

func (t *tracker) done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n--; t.n == 0 && t.zero != nil {
		close(t.zero)
		t.zero = nil
	}
}

// wait waits up to d for every stream to end, and reports whether they
// did.
func (t *tracker) wait(d time.Duration) bool {
	t.mu.Lock()
	if t.n == 0 {
		t.mu.Unlock()
		return true
	}
	if t.zero == nil {
		t.zero = make(chan struct{})
	}
	zero := t.zero
	t.mu.Unlock()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-zero:
		return true
	case <-timer.C:
		return false
	}
}

// resizeFor is the resize the owner's hello asks of a session of class,
// or false if it asks for none.
func resizeFor(class backend.Class, sz screen) (backend.Resize, bool) {
	if class == backend.Terminal {
		return backend.Resize{Width: sz.cols, Height: sz.rows}, sz.cols > 0 && sz.rows > 0
	}
	return backend.Resize{Width: sz.width, Height: sz.height}, sz.width > 0 && sz.height > 0
}
