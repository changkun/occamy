// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

// The guacd bridge's parity matrix for VNC
// (specs/008-native-architecture/guacd-bridge.md, the parity gate's step
// 2): each feature on each demo VNC server, x11vnc and TigerVNC, through
// occamyd's native backend and through guacd, both behind occamy-v1 and
// driven by the Go client. The servers' desktops
// (docker/demo/vnc/desktop.sh) report what reached them over the session
// itself: a line typed into the typing probe comes back on the clipboard
// as "typed: " and its bytes in hex, clipboard text "owner: …" comes back
// as "echo: …", and "hex: …" as "hexed: " and its bytes in hex. Each
// server has a screen with the us keyboard layout, one with de and one
// with fr.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// parityTimeout bounds each wait of a check.
const parityTimeout = 20 * time.Second

// A parityColumn of the matrix: a server and a backend.
type parityColumn struct {
	server  string // x11vnc or tigervnc
	backend string // native or guacd
}

var parityColumns = []parityColumn{
	{"x11vnc", "native"}, {"x11vnc", "guacd"},
	{"tigervnc", "native"}, {"tigervnc", "guacd"},
}

func (c parityColumn) String() string { return c.server + "/" + c.backend }

// conn returns the ID of the column's connection of a kind in
// docker/occamy-v1.yaml: "" for VNC Authentication with a stored
// password, or none, prompt or plain, or de or fr for the screen with
// that keyboard layout.
func (c parityColumn) conn(kind string) string {
	id := "vnc"
	if c.server == "tigervnc" {
		id = "tigervnc"
	}
	if kind != "" {
		id += "-" + kind
	}
	return id + "-" + c.backend
}

// state returns the backend that the session's state names.
func (c parityColumn) state() string {
	if c.backend == "native" {
		return "vnc"
	}
	return "guacd"
}

// A parityFeature is a row of the matrix.
type parityFeature struct {
	name string
	// servers it applies to; nil for both.
	servers []string
	check   func(p *parity, c parityColumn) error
}

var tigerOnly = []string{"tigervnc"}

// parityKnown holds the cells expected to fail, by "row|column", with
// why. A cell that fails without an entry fails the test, and so does
// one that passes with an entry, so that the matrix in guacd-bridge.md
// stays what the suite finds.
var parityKnown = map[string]string{
	// RFB's cut text is Latin-1. x11vnc has no Extended Clipboard, and
	// guacd 1.6 does not ask for it.
	"clipboard to the server, beyond Latin-1|x11vnc/native":  "x11vnc: Latin-1 only",
	"clipboard to the server, beyond Latin-1|x11vnc/guacd":   "x11vnc: Latin-1 only",
	"clipboard to the server, beyond Latin-1|tigervnc/guacd": "guacd: Latin-1 only",
	"clipboard both ways, beyond Latin-1|x11vnc/native":      "x11vnc: Latin-1 only",
	"clipboard both ways, beyond Latin-1|x11vnc/guacd":       "x11vnc: Latin-1 only",
	"clipboard both ways, beyond Latin-1|tigervnc/guacd":     "guacd: Latin-1 only",
	// libvncclient asks for X509 credentials, which guacd 1.6 does not
	// give ("Unsupported credential type requested").
	"auth: VeNCrypt X509Plain|tigervnc/guacd": "guacd: no X509 credentials",
}

// TestVNCParity runs the matrix against occamy-v1, column by column,
// since the columns of a server share its screens. It logs the matrix
// as a Markdown table, and writes it to the file that
// OCCAMY_PARITY_MATRIX names, if set.
func TestVNCParity(t *testing.T) {
	addr := endpointV1(t)
	token, err := signIn(addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &parity{addr: addr, token: token}
	cells := map[string]string{}
	for _, c := range parityColumns {
		t.Run(c.String(), func(t *testing.T) {
			for _, f := range parityFeatures {
				cell := f.name + "|" + c.String()
				if f.servers != nil && !slices.Contains(f.servers, c.server) {
					cells[cell] = "—"
					continue
				}
				t.Run(f.name, func(t *testing.T) {
					start := time.Now()
					err := f.check(p, c)
					known, isKnown := parityKnown[cell]
					switch {
					case err == nil && !isKnown:
						cells[cell] = "pass"
					case err == nil:
						cells[cell] = "pass (listed as failing)"
						t.Errorf("passes, but parityKnown lists it as failing (%s): update the matrix", known)
					case isKnown:
						cells[cell] = "fail: " + known
						t.Logf("fails as known (%s): %v", known, err)
					default:
						cells[cell] = "FAIL: " + err.Error()
						t.Error(err)
					}
					t.Logf("%v", time.Since(start).Round(time.Millisecond))
				})
			}
		})
	}
	var b strings.Builder
	b.WriteString("| Feature |")
	for _, c := range parityColumns {
		fmt.Fprintf(&b, " %s |", c)
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(parityColumns)) + "\n")
	for _, f := range parityFeatures {
		fmt.Fprintf(&b, "| %s |", f.name)
		for _, c := range parityColumns {
			fmt.Fprintf(&b, " %s |", strings.ReplaceAll(cells[f.name+"|"+c.String()], "|", "\\|"))
		}
		b.WriteString("\n")
	}
	t.Logf("the matrix:\n%s", b.String())
	if path := os.Getenv("OCCAMY_PARITY_MATRIX"); path != "" {
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Error(err)
		}
	}
}

var parityFeatures = []parityFeature{
	{name: "auth: None", check: checkAuthNone},
	{name: "auth: VNC Authentication, stored password", check: checkAuthStored},
	{name: "auth: VNC Authentication, asked of the owner", servers: tigerOnly, check: checkAuthPrompt},
	{name: "auth: VNC Authentication, wrong answer refused", servers: tigerOnly, check: checkAuthWrong},
	{name: "auth: VeNCrypt X509Plain", servers: tigerOnly, check: checkAuthPlain},
	{name: "screen and input acknowledged", check: checkScreen},
	{name: "resize: by the owner's hello", servers: tigerOnly, check: checkResizeHello},
	{name: "resize: by a request", servers: tigerOnly, check: checkResizeRequest},
	{name: "resize: ignored without ExtendedDesktopSize", servers: []string{"x11vnc"}, check: checkResizeIgnored},
	{name: "clipboard to the server, Latin-1", check: clipboardIn("naïve café, ½ °C")},
	{name: "clipboard to the server, beyond Latin-1", check: clipboardIn("€ ✓ 日本")},
	{name: "clipboard both ways, Latin-1", check: clipboardEcho("naïve café\nzweite Zeile")},
	{name: "clipboard both ways, beyond Latin-1", check: clipboardEcho("€ ✓ 日本")},
	{name: "cursor", check: checkCursor},
	{name: "keyboard: us", check: keyboard("us", "us", false)},
	{name: "keyboard: de, keysyms and codes", check: keyboard("de", "de", false)},
	{name: "keyboard: de, keysyms only", check: keyboard("de", "de", true)},
	{name: "keyboard: fr, keysyms and codes", check: keyboard("fr", "fr", false)},
	{name: "keyboard: fr, keysyms only", check: keyboard("fr", "fr", true)},
	{name: "keyboard: de client on a us server", check: keyboard("de", "us", false)},
	{name: "text input", check: checkText},
	{name: "sharing: a viewer's input", check: checkViewer},
	{name: "sharing: a read-only viewer's input dropped", check: checkReadOnly},
	{name: "reconnect: the owner rejoins", check: checkReconnect},
}

// parity holds what the checks share: occamy-v1's address and the demo
// user's access token.
type parity struct {
	addr, token string
}

// A paritySession is a stream with its events recorded, so that a check
// can find an event that arrived while it waited for another.
type paritySession struct {
	p  *parity
	cl *client.Session
	ws *websocket.Conn

	mu     sync.Mutex
	events []client.Event
	more   chan struct{} // closed when an event arrives or Next fails
	err    error         // why Next failed
}

// open streams a connection as the demo user, and waits for the screen,
// answering a prompt with answers. It returns the stream and a function
// that closes the session.
func (p *parity) open(conn string, opts client.Options, answers ...string) (*paritySession, func(), error) {
	s, err := p.dial("/api/v1/connections/"+url.PathEscape(conn)+"/stream", p.token, opts)
	if err != nil {
		return nil, nil, err
	}
	closeSession := func() {
		_ = apiV1(p.addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(s.cl.SessionID()), p.token, nil, nil)
		s.close()
	}
	if err := s.screen(answers...); err != nil {
		closeSession()
		return nil, nil, err
	}
	return s, closeSession, nil
}

// dial opens a stream at path with token. It dials the WebSocket itself
// rather than through client.Dial, so that inject can send what the
// client would refuse to.
func (p *parity) dial(path, token string, opts client.Options) (*paritySession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), parityTimeout)
	defer cancel()
	if opts.Name == "" {
		opts.Name = "occamy-parity/1"
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 1024, 768
	}
	q, err := protocol.EncodeHelloQuery(opts.Hello())
	if err != nil {
		return nil, err
	}
	u := "ws://" + p.addr + path + "?" + url.Values{protocol.HelloParameter: {q}}.Encode()
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{ //nolint:bodyclose // websocket.Dial closes it
		Subprotocols: []string{protocol.Subprotocol, protocol.TokenSubprotocol(token)},
	})
	if err != nil {
		return nil, fmt.Errorf("dialling %s: %w", path, err)
	}
	cl, err := client.New(ctx, parityConn{ws}, opts)
	if err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("joining %s: %w", path, err)
	}
	s := &paritySession{p: p, cl: cl, ws: ws, more: make(chan struct{})}
	go s.pump()
	return s, nil
}

// parityConn is client.Dial's WebSocket connection.
type parityConn struct{ c *websocket.Conn }

func (w parityConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, p, err := w.c.Read(ctx)
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return nil, &client.CloseError{Code: protocol.CloseCode(ce.Code), Reason: ce.Reason} // #nosec G115 -- a close status is a u16 on the wire
	}
	return p, err
}

func (w parityConn) WriteMessage(ctx context.Context, p []byte) error {
	return w.c.Write(ctx, websocket.MessageBinary, p)
}

func (w parityConn) Close(code protocol.CloseCode, reason string) error {
	return w.c.Close(websocket.StatusCode(code), reason)
}

func (w parityConn) SetReadLimit(n int64) { w.c.SetReadLimit(n) }

func (s *paritySession) pump() {
	for {
		ev, err := s.cl.Next(context.Background())
		s.mu.Lock()
		if err != nil {
			s.err = err
			close(s.more)
			s.mu.Unlock()
			return
		}
		s.events = append(s.events, ev)
		close(s.more)
		s.more = make(chan struct{})
		s.mu.Unlock()
	}
}

func (s *paritySession) close() { _ = s.cl.Close() }

// endError describes the end of a session as an error.
func endError(e client.End) error {
	err := fmt.Errorf("the session ended: %v %q", e.Code, e.Reason)
	if e.Err != nil {
		err = fmt.Errorf("%w: %w", err, e.Err)
	}
	return err
}

// mark returns the index of the next event.
func (s *paritySession) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// wait returns the first event from index from on that ok accepts, and
// the index after it. The session's end is an error unless ok accepts
// it.
func (s *paritySession) wait(from int, what string, ok func(client.Event) bool) (client.Event, int, error) {
	return s.waitFor(parityTimeout, from, what, ok)
}

// waitFor is wait with a timeout of its own.
func (s *paritySession) waitFor(timeout time.Duration, from int, what string, ok func(client.Event) bool) (client.Event, int, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		for ; from < len(s.events); from++ {
			ev := s.events[from]
			if ok(ev) {
				s.mu.Unlock()
				return ev, from + 1, nil
			}
			if e, end := ev.(client.End); end {
				s.mu.Unlock()
				return nil, from, fmt.Errorf("waiting for %s: %w", what, endError(e))
			}
		}
		more, err := s.more, s.err
		s.mu.Unlock()
		if err != nil {
			return nil, from, fmt.Errorf("waiting for %s: %w", what, err)
		}
		select {
		case <-more:
		case <-timer.C:
			return nil, from, fmt.Errorf("waiting for %s: no such event in %v", what, timeout)
		}
	}
}

// screen waits for a frame of a screen, answering a prompt with
// answers; a prompt without answers is an error.
func (s *paritySession) screen(answers ...string) error {
	from := 0
	for {
		ev, next, err := s.wait(from, "the screen", func(ev client.Event) bool {
			switch ev.(type) {
			case client.Prompt:
				return true
			case client.Frame:
				return !s.cl.Size().Eq(image.Point{})
			}
			return false
		})
		if err != nil {
			return err
		}
		from = next
		pr, ok := ev.(client.Prompt)
		if !ok {
			return nil
		}
		if len(answers) == 0 {
			return fmt.Errorf("asked for %d fields (%q) where nothing should be asked", len(pr.Fields), pr.Name)
		}
		if err := s.cl.PromptReply(context.Background(), pr.ID, answers[:min(len(answers), len(pr.Fields))]); err != nil {
			return err
		}
		answers = nil
	}
}

// size waits for a frame of the screen at size.
func (s *paritySession) size(from int, size image.Point) error {
	_, _, err := s.wait(from, fmt.Sprintf("a screen of %v", size), func(ev client.Event) bool {
		_, ok := ev.(client.Frame)
		return ok && s.cl.Size() == size
	})
	if err != nil {
		return fmt.Errorf("%w; the screen is %v", err, s.cl.Size())
	}
	return nil
}

// clipboard sends text as the client's clipboard and returns the text
// of the first clipboard from the server, from then on, that starts
// with prefix.
func (s *paritySession) clipboard(text, prefix string) (string, int, error) {
	from := s.mark()
	if _, err := s.cl.Clipboard(context.Background(), "text/plain", []byte(text)); err != nil {
		return "", from, err
	}
	return s.clipboardFrom(from, prefix)
}

func (s *paritySession) clipboardFrom(from int, prefix string) (string, int, error) {
	ev, next, err := s.wait(from, "a clipboard of "+prefix+"…", func(ev client.Event) bool {
		c, ok := ev.(client.Clipboard)
		return ok && bytes.HasPrefix(c.Data, []byte(prefix))
	})
	if err != nil {
		return "", next, err
	}
	return strings.TrimPrefix(string(ev.(client.Clipboard).Data), prefix), next, nil
}

// barrier makes sure that the desktop's probe has answered everything
// sent before, and returns the index of the event after its answer. It
// tries four times, five seconds each: on an x11vnc that has just
// started, the first cut text of each of its ports did not come back,
// through either backend.
func (s *paritySession) barrier() (int, error) {
	var err error
	for range 4 {
		nonce := fmt.Sprintf("barrier %d", time.Now().UnixNano())
		from := s.mark()
		if _, err := s.cl.Clipboard(context.Background(), "text/plain", []byte("owner: "+nonce)); err != nil {
			return from, err
		}
		var next int
		_, next, err = s.waitFor(5*time.Second, from, "the echo of "+nonce, func(ev client.Event) bool {
			c, ok := ev.(client.Clipboard)
			return ok && string(c.Data) == "echo: "+nonce
		})
		if err == nil {
			return next, nil
		}
	}
	return 0, err
}

// A key is a key press as a client sends it: the keysym it produced, or
// 0, and the physical key as a HID usage, or 0, with the modifiers held
// around it.
type key struct {
	keysym uint32
	code   uint16
	mods   []key
}

var (
	keyShift  = key{keysym: 0xffe1, code: 0xe1} // Shift_L, Left Shift
	keyAltGr  = key{keysym: 0xfe03, code: 0xe6} // ISO_Level3_Shift, Right Alt
	keyReturn = key{keysym: 0xff0d, code: 0x28}
)

// k is a key that produced keysym.
func k(keysym uint32, code uint16, mods ...key) key {
	return key{keysym: keysym, code: code, mods: mods}
}

// dead is a dead key as a browser reports it: no keysym, only the code.
func dead(code uint16, mods ...key) key { return key{code: code, mods: mods} }

// withoutCodes returns keys as a client that knows no physical keys
// sends them.
func withoutCodes(keys []key) []key {
	out := make([]key, len(keys))
	for i, k := range keys {
		out[i] = key{keysym: k.keysym, mods: withoutCodes(k.mods)}
	}
	return out
}

func (s *paritySession) press(k key) error {
	ctx := context.Background()
	for _, m := range k.mods {
		if _, err := s.cl.Key(ctx, true, m.keysym, m.code); err != nil {
			return err
		}
	}
	if _, err := s.cl.Key(ctx, true, k.keysym, k.code); err != nil {
		return err
	}
	if _, err := s.cl.Key(ctx, false, k.keysym, k.code); err != nil {
		return err
	}
	for _, m := range slices.Backward(k.mods) {
		if _, err := s.cl.Key(ctx, false, m.keysym, m.code); err != nil {
			return err
		}
	}
	return nil
}

// typeLine points at the typing probe, sends input there with send,
// presses Return, and returns the line that the probe read.
func (s *paritySession) typeLine(send func() error) (string, error) {
	if _, err := s.cl.Pointer(context.Background(), 20, 20, 0); err != nil {
		return "", err
	}
	from, err := s.barrier()
	if err != nil {
		return "", err
	}
	if err := send(); err != nil {
		return "", err
	}
	if err := s.press(keyReturn); err != nil {
		return "", err
	}
	h, _, err := s.clipboardFrom(from, "typed: ")
	if err != nil {
		return "", err
	}
	line, err := hex.DecodeString(h)
	if err != nil {
		return "", fmt.Errorf("the typed line %q: %w", h, err)
	}
	return string(line), nil
}

// typeKeys types keys into the typing probe and returns the line read.
func (s *paritySession) typeKeys(keys []key) (string, error) {
	return s.typeLine(func() error {
		for _, k := range keys {
			if err := s.press(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// expectLine types keys and checks the line read.
func (s *paritySession) expectLine(keys []key, want string) error {
	got, err := s.typeKeys(keys)
	if err == nil && got != want {
		err = fmt.Errorf("typed %q, want %q", got, want)
	}
	return err
}

// lowercase returns the keys that type ASCII lowercase letters, digits
// and spaces on a us layout.
func lowercase(text string) []key {
	var keys []key
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z':
			keys = append(keys, k(uint32(r), uint16(0x04+r-'a')))
		case r >= '1' && r <= '9':
			keys = append(keys, k(uint32(r), uint16(0x1e+r-'1')))
		case r == '0':
			keys = append(keys, k('0', 0x27))
		case r == ' ':
			keys = append(keys, k(' ', 0x2c))
		default:
			panic("lowercase: " + string(r))
		}
	}
	return keys
}

// state returns the session's state.
func (p *parity) state(id string) (sessionState, error) {
	var st sessionState
	err := apiV1(p.addr, http.MethodGet, "/api/v1/sessions/"+url.PathEscape(id), p.token, nil, &st)
	return st, err
}

type sessionState struct {
	Backend string `json:"backend"`
	Viewers []struct {
		Role string `json:"role"`
	} `json:"viewers"`
}

// connects opens conn and checks that its session reaches the screen
// with the column's backend and takes input.
func (p *parity) connects(c parityColumn, conn string, answers ...string) error {
	s, closeSession, err := p.open(conn, client.Options{}, answers...)
	if err != nil {
		return err
	}
	defer closeSession()
	st, err := p.state(s.cl.SessionID())
	if err != nil {
		return err
	}
	if st.Backend != c.state() {
		return fmt.Errorf("the session's backend is %q, want %q", st.Backend, c.state())
	}
	return s.expectLine(lowercase("auth"), "auth")
}

func checkAuthNone(p *parity, c parityColumn) error   { return p.connects(c, c.conn("none")) }
func checkAuthStored(p *parity, c parityColumn) error { return p.connects(c, c.conn("")) }
func checkAuthPlain(p *parity, c parityColumn) error  { return p.connects(c, c.conn("plain")) }

func checkAuthPrompt(p *parity, c parityColumn) error {
	return p.connects(c, c.conn("prompt"), "vncpassword")
}

// checkAuthWrong answers the prompt wrong: the session ends with
// backend-failed.
func checkAuthWrong(p *parity, c parityColumn) error {
	s, err := p.dial("/api/v1/connections/"+url.PathEscape(c.conn("prompt"))+"/stream", p.token, client.Options{})
	if err != nil {
		return err
	}
	defer s.close()
	defer apiV1(p.addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(s.cl.SessionID()), p.token, nil, nil) //nolint:errcheck // the session should have ended
	ev, _, err := s.wait(0, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
	if err != nil {
		return err
	}
	pr := ev.(client.Prompt)
	if len(pr.Fields) != 1 || !pr.Fields[0].Secret {
		return fmt.Errorf("the prompt %q asks %+v, want one secret field", pr.Name, pr.Fields)
	}
	if err := s.cl.PromptReply(context.Background(), pr.ID, []string{"wrong"}); err != nil {
		return err
	}
	ev, _, err = s.wait(0, "the end", func(ev client.Event) bool { _, ok := ev.(client.End); return ok })
	if err != nil {
		return err
	}
	if e := ev.(client.End); e.Code != protocol.CloseBackendFailed {
		return fmt.Errorf("the session ended with %v %q, want backend-failed", e.Code, e.Reason)
	}
	return nil
}

// checkScreen checks the screen's size and that it shows something, and
// that a frame acknowledges input.
func checkScreen(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	if got := s.cl.Size(); got != image.Pt(1024, 768) {
		return fmt.Errorf("the screen is %v, want 1024×768", got)
	}
	ctx := context.Background()
	if _, err := s.cl.Pointer(ctx, 100, 100, 0); err != nil {
		return err
	}
	seq, err := s.cl.Key(ctx, true, 0xffe1, 0xe1) // Shift, which types nothing
	if err != nil {
		return err
	}
	if _, err := s.cl.Key(ctx, false, 0xffe1, 0xe1); err != nil {
		return err
	}
	if _, _, err := s.wait(0, "a frame after the input", func(ev client.Event) bool {
		f, ok := ev.(client.Frame)
		return ok && f.LastSeq >= seq
	}); err != nil {
		return err
	}
	img := s.cl.Screenshot()
	first := img.RGBAAt(0, 0)
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			if img.RGBAAt(x, y) != first {
				return nil
			}
		}
	}
	return fmt.Errorf("the screen is all %v", first)
}

func checkResizeHello(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{Width: 900, Height: 700})
	if err != nil {
		return err
	}
	defer closeSession()
	defer s.restoreSize()
	return s.size(0, image.Pt(900, 700))
}

func checkResizeRequest(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	defer s.restoreSize()
	for _, size := range []image.Point{{800, 600}, {1024, 768}} {
		from := s.mark()
		if _, err := s.cl.Resize(context.Background(), uint16(size.X), uint16(size.Y)); err != nil {
			return err
		}
		if err := s.size(from, size); err != nil {
			return err
		}
	}
	return nil
}

// restoreSize asks for the screen's size of 1024×768 again, which the
// other checks expect.
func (s *paritySession) restoreSize() {
	from := s.mark()
	if _, err := s.cl.Resize(context.Background(), 1024, 768); err == nil {
		_ = s.size(from, image.Pt(1024, 768))
	}
}

// checkResizeIgnored asks a server without ExtendedDesktopSize for
// another size: the screen stays, and the session goes on.
func checkResizeIgnored(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	if _, err := s.cl.Resize(context.Background(), 800, 600); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	if got := s.cl.Size(); got != image.Pt(1024, 768) {
		return fmt.Errorf("the screen is %v, want 1024×768", got)
	}
	return s.expectLine(lowercase("resize"), "resize")
}

// clipboardIn sends text to the server's clipboard and checks what the
// server took, from the probe's hex.
func clipboardIn(text string) func(p *parity, c parityColumn) error {
	return func(p *parity, c parityColumn) error {
		s, closeSession, err := p.open(c.conn(""), client.Options{})
		if err != nil {
			return err
		}
		defer closeSession()
		if _, err := s.barrier(); err != nil {
			return err
		}
		h, _, err := s.clipboard("hex: "+text, "hexed: ")
		if err != nil {
			return err
		}
		got, err := hex.DecodeString(h)
		if err == nil && string(got) != text {
			err = fmt.Errorf("the server took %q, want %q", got, text)
		}
		return err
	}
}

// clipboardEcho sends text to the server's clipboard and checks that it
// comes back.
func clipboardEcho(text string) func(p *parity, c parityColumn) error {
	return func(p *parity, c parityColumn) error {
		s, closeSession, err := p.open(c.conn(""), client.Options{})
		if err != nil {
			return err
		}
		defer closeSession()
		if _, err := s.barrier(); err != nil {
			return err
		}
		got, _, err := s.clipboard("owner: "+text, "echo: ")
		if err == nil && got != text {
			err = fmt.Errorf("the client got %q back, want %q", got, text)
		}
		return err
	}
}

// checkCursor moves the pointer between the root window and an xterm,
// whose cursors differ, and checks that each change reaches the client.
func checkCursor(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	isCursor := func(ev client.Event) bool {
		cur, ok := ev.(client.Cursor)
		return ok && cur.Image != nil
	}
	var last *client.Cursor
	for i, at := range []image.Point{{1000, 740}, {20, 20}, {1000, 740}} {
		from := s.mark()
		if _, err := s.cl.Pointer(context.Background(), uint16(at.X), uint16(at.Y), 0); err != nil {
			return err
		}
		if i == 0 {
			// The root window's cursor, which may have come with the
			// screen.
			time.Sleep(time.Second)
			for _, ev := range s.recorded() {
				if isCursor(ev) {
					cur := ev.(client.Cursor)
					last = &cur
				}
			}
			if last == nil {
				return errors.New("no cursor arrived")
			}
			continue
		}
		ev, _, err := s.wait(from, fmt.Sprintf("another cursor at %v", at), func(ev client.Event) bool {
			return isCursor(ev) && !sameCursor(ev.(client.Cursor), *last)
		})
		if err != nil {
			return err
		}
		cur := ev.(client.Cursor)
		if !cur.Hot.In(cur.Image.Rect) {
			return fmt.Errorf("the hot spot %v lies outside the cursor %v", cur.Hot, cur.Image.Rect)
		}
		last = &cur
	}
	return nil
}

func (s *paritySession) recorded() []client.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func sameCursor(a, b client.Cursor) bool {
	return a.Hot == b.Hot && a.Image.Rect == b.Image.Rect && bytes.Equal(a.Image.Pix, b.Image.Pix)
}

// The layouts' keys, as a browser on that layout reports them: the
// keysym each key produced, with the HID usage of the key pressed, and
// AltGr and Shift held where the layout needs them. A dead key produces
// nothing; the next key produces the composed character.
var layoutKeys = map[string]struct {
	keys []key
	want string
}{
	"us": {
		keys: []key{
			k('H', 0x0b, keyShift), k('e', 0x08), k('l', 0x0f), k('l', 0x0f), k('o', 0x12),
			k(',', 0x36), k(' ', 0x2c), k('W', 0x1a, keyShift), k('o', 0x12), k('r', 0x15),
			k('l', 0x0f), k('d', 0x07), k('!', 0x1e, keyShift), k(' ', 0x2c), k('@', 0x1f, keyShift),
			k('~', 0x35, keyShift), k('1', 0x1e), k('2', 0x1f), k('3', 0x20),
		},
		want: "Hello, World! @~123",
	},
	"de": {
		keys: []key{
			k('z', 0x1c), k('y', 0x1d), // QWERTZ
			k('@', 0x14, keyAltGr), k(0x20ac, 0x08, keyAltGr), k('{', 0x24, keyAltGr), // AltGr+Q, E, 7
			k(0xfc, 0x2f), k(0xf6, 0x33), k(0xe4, 0x34), k(0xdf, 0x2d), // ü ö ä ß
			k('"', 0x1f, keyShift),
			dead(0x35), k(0xf4, 0x12), // dead circumflex, o: ô
			dead(0x2e), k(0xe9, 0x08), // dead acute, e: é
			dead(0x35), k('^', 0x2c), // dead circumflex, space: ^
		},
		want: `zy@€{üöäß"ôé^`,
	},
	"fr": {
		keys: []key{
			k('a', 0x14), k('q', 0x04), k('z', 0x1a), k('w', 0x1d), // AZERTY
			k(0xe9, 0x1f), k(0xe8, 0x24), k(0xe7, 0x26), k(0xe0, 0x27), // é è ç à
			k('@', 0x27, keyAltGr), k(0x20ac, 0x08, keyAltGr), k('#', 0x20, keyAltGr), // AltGr+0, E, 3
			dead(0x2f), k(0xea, 0x08), // dead circumflex, e: ê
			dead(0x2f, keyShift), k(0xeb, 0x08), // dead diaeresis, e: ë
		},
		want: "aqzwéèçà@€#êë",
	},
}

// keyboard types the client layout's keys on the server's screen with
// a layout, with their codes or without. The screens keep their layouts:
// x11vnc adds a keysym missing from the layout to an unused key, and
// after a switch of layouts, still presses that key
// (docker/demo/vnc/desktop.sh).
func keyboard(clientLayout, serverLayout string, noCodes bool) func(p *parity, c parityColumn) error {
	return func(p *parity, c parityColumn) error {
		kind := serverLayout
		if kind == "us" {
			kind = ""
		}
		s, closeSession, err := p.open(c.conn(kind), client.Options{})
		if err != nil {
			return err
		}
		defer closeSession()
		l := layoutKeys[clientLayout]
		keys := l.keys
		if noCodes {
			keys = withoutCodes(keys)
		}
		return s.expectLine(keys, l.want)
	}
}

// checkText sends text, as an input method composes it.
func checkText(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	const text = "日本 ✓ naïve"
	got, err := s.typeLine(func() error {
		_, err := s.cl.Text(context.Background(), text)
		return err
	})
	if err == nil && got != text {
		err = fmt.Errorf("typed %q, want %q", got, text)
	}
	return err
}

// share mints a share of the session for role.
func (p *parity) share(id, role string) (string, error) {
	body, err := json.Marshal(map[string]string{"role": role})
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	err = apiV1(p.addr, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(id)+"/shares", p.token, body, &out)
	return out.Token, err
}

// join joins session id with a share of role, and waits for the screen.
func (p *parity) join(id, role string) (*paritySession, error) {
	tok, err := p.share(id, role)
	if err != nil {
		return nil, err
	}
	s, err := p.dial("/api/v1/sessions/"+url.PathEscape(id)+"/stream", tok, client.Options{Name: "occamy-parity-" + role + "/1"})
	if err != nil {
		return nil, err
	}
	if err := s.screen(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// checkViewer has a viewer type into the owner's session.
func checkViewer(p *parity, c parityColumn) error {
	owner, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	v, err := p.join(owner.cl.SessionID(), "viewer")
	if err != nil {
		return err
	}
	defer v.close()
	if v.cl.Role() != protocol.RoleViewer {
		return fmt.Errorf("joined as %v, want viewer", v.cl.Role())
	}
	if err := v.expectLine(lowercase("viewer"), "viewer"); err != nil {
		return fmt.Errorf("the viewer: %w", err)
	}
	st, err := p.state(owner.cl.SessionID())
	if err != nil {
		return err
	}
	var roles []string
	for _, v := range st.Viewers {
		roles = append(roles, v.Role)
	}
	if !slices.Equal(roles, []string{"owner", "viewer"}) {
		return fmt.Errorf("the session's viewers are %v, want owner and viewer", roles)
	}
	return nil
}

// checkReadOnly has a read-only viewer send input anyway, past the
// client, which refuses it: nothing reaches the server, and the owner's
// line comes through alone.
func checkReadOnly(p *parity, c parityColumn) error {
	owner, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	defer closeSession()
	ro, err := p.join(owner.cl.SessionID(), "read-only")
	if err != nil {
		return err
	}
	defer ro.close()
	if ro.cl.Role() != protocol.RoleReadOnly {
		return fmt.Errorf("joined as %v, want read-only", ro.cl.Role())
	}
	ctx := context.Background()
	if _, err := ro.cl.Key(ctx, true, 'x', 0x1b); !errors.Is(err, client.ErrReadOnly) {
		return fmt.Errorf("a read-only viewer's key: the client returned %q, want %w", fmt.Sprint(err), client.ErrReadOnly)
	}
	if _, err := owner.cl.Pointer(ctx, 20, 20, 0); err != nil {
		return err
	}
	from, err := owner.barrier()
	if err != nil {
		return err
	}
	var raw []byte
	seq := uint32(0)
	for _, m := range []protocol.Message{
		&protocol.Pointer{X: 20, Y: 20},
		&protocol.Key{Down: true, Keysym: 'x', Code: 0x1b}, &protocol.Key{Keysym: 'x', Code: 0x1b},
		&protocol.Key{Down: true, Keysym: 0xff0d, Code: 0x28}, &protocol.Key{Keysym: 0xff0d, Code: 0x28},
		&protocol.ClientClipboard{MIME: "text/plain", Data: []byte("owner: read-only")},
	} {
		seq++
		switch m := m.(type) {
		case *protocol.Pointer:
			m.Seq = seq
		case *protocol.Key:
			m.Seq = seq
		case *protocol.ClientClipboard:
			m.Seq = seq
		}
		raw = m.Append(raw[:0])
		if err := ro.ws.Write(ctx, websocket.MessageBinary, raw); err != nil {
			return err
		}
	}
	time.Sleep(2 * time.Second)
	for _, ev := range owner.recorded()[from:] {
		if cb, ok := ev.(client.Clipboard); ok {
			return fmt.Errorf("the read-only viewer's input reached the server: the clipboard became %q", cb.Data)
		}
	}
	if err := owner.expectLine(lowercase("owner"), "owner"); err != nil {
		return fmt.Errorf("the owner: %w", err)
	}
	return nil
}

// checkReconnect has the owner leave and come back within the owner
// grace: the same session, with its screen, and input still reaching
// the server.
func checkReconnect(p *parity, c parityColumn) error {
	s, closeSession, err := p.open(c.conn(""), client.Options{})
	if err != nil {
		return err
	}
	id := s.cl.SessionID()
	defer closeSession()
	if err := s.expectLine(lowercase("before"), "before"); err != nil {
		return err
	}
	s.close()
	time.Sleep(time.Second)
	back, err := p.dial("/api/v1/sessions/"+url.PathEscape(id)+"/stream", p.token, client.Options{})
	if err != nil {
		return err
	}
	defer back.close()
	if err := back.screen(); err != nil {
		return err
	}
	if back.cl.SessionID() != id || back.cl.Role() != protocol.RoleOwner {
		return fmt.Errorf("rejoined session %s as %v, want %s as owner", back.cl.SessionID(), back.cl.Role(), id)
	}
	if got := back.cl.Size(); got != image.Pt(1024, 768) {
		return fmt.Errorf("the screen is %v, want 1024×768", got)
	}
	return back.expectLine(lowercase("after"), "after")
}
