// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/vpx/webp"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// The v1 series drive protocol v1 sessions with the Go client core
// (client/) through occamyd's API v1, as a client of
// docs/api.md does: sign in, open a connection's stream, and end the
// session with DELETE. The connections are those of the bench stack's
// occamy-v1 service (docker/bench/occamy-v1.yaml), per series and
// protocol. SSH has no guacd backend in v1, so v1-guacd serves only the
// desktop.
var v1Connections = map[string]map[string]string{
	"v1-native": {"ssh": "ssh", "vnc": "vnc-native", "rdp": "rdp-native"},
	"v1-guacd":  {"vnc": "vnc-guacd", "rdp": "rdp-guacd"},
}

// v1Account is the bench stack's API v1 user.
var v1Account = struct{ name, secret string }{"demo", "occamy"}

// The terminal the v1 series asks for, in cells: the size of the
// terminal guacd draws in the v0 series' 1024×768 pixels at 96 DPI, as
// stty reported it in the shell, so that both protocols' shells print
// the same screens.
const (
	termCols, termRows = 99, 39
)

// openV1 opens v1 sessions on the connections conns names per protocol,
// which show their frames on d.
func openV1(conns map[string]string, d display) func(context.Context, string, string) (*session, error) {
	return func(ctx context.Context, addr, proto string) (*session, error) {
		id, ok := conns[proto]
		if !ok {
			return nil, fmt.Errorf("no v1 connection for %s", proto)
		}
		ctx, cancel := context.WithTimeout(ctx, openTimeout)
		defer cancel()
		t0 := time.Now()
		token, err := signIn(ctx, addr)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		s := newSession(start)
		s.signIn = start.Sub(t0)
		cl, err := client.Dial(ctx, "ws://"+addr+"/api/v1/connections/"+url.PathEscape(id)+"/stream", token, client.Options{
			Name:     "occamy-bench/1",
			Width:    width,
			Height:   height,
			Cols:     termCols,
			Rows:     termRows,
			Decoders: decoders(&s.decode),
		})
		if err != nil {
			// The server made the session before it welcomed the client,
			// which then refused it, as when they have no codec in common.
			if re := (*client.RefusedError)(nil); errors.As(err, &re) && re.SessionID != "" {
				endSession(addr, token, re.SessionID)
			}
			return nil, err
		}
		s.startV1(cl, d, func() { endSession(addr, token, cl.SessionID()) })
		return s, nil
	}
}

// endSession ends session sid on the server. The owner's leaving keeps
// a session, and the remote connected, for the owner grace of 60 s,
// during which it would count against the user's sessions and load the
// remote.
func endSession(addr, token, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := apiV1(ctx, addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(sid), token, nil, nil); err != nil {
		log.Printf("  ending session %s: %v", sid, err)
	}
}

// startV1 runs s on cl, showing its frames on d. end, if set, ends the
// session on the server once the client left it.
func (s *session) startV1(cl *client.Session, d display, end func()) {
	c := &v1Conn{cl: cl, end: end}
	s.conn = c
	go s.readV1(c, d)
}

// display is the screen on which a client presents a graphical frame
// that Next delivered, and so acknowledges it by asking for the next
// event: at the next tick of a screen that refreshes every period, as
// occamy's web client presents a frame at the next animation frame and
// acknowledges it then (client/occamy-web/src/protocol/display.ts,
// Display.run), or at once without a period. Acknowledgements pace the
// server's frames, by the frame window.
type display struct{ period time.Duration }

// displayOrigin is the phase of every display's ticks: a session starts
// at any point between two of them, as it does on a real screen.
var displayOrigin = time.Now()

// newDisplay returns a display that refreshes hz times a second, or at
// once for 0.
func newDisplay(hz float64) display {
	if hz <= 0 {
		return display{}
	}
	return display{period: time.Duration(float64(time.Second) / hz)}
}

// next returns when a frame that Next delivered at now is presented:
// the first tick after now, or now without a period.
func (d display) next(now time.Time) time.Time {
	if d.period <= 0 {
		return now
	}
	n := now.Sub(displayOrigin)/d.period + 1
	return displayOrigin.Add(n * d.period)
}

// present waits until a frame that Next delivered at now is presented.
func (d display) present(now time.Time) {
	time.Sleep(time.Until(d.next(now)))
}

// signIn returns an access token for the bench user. A v1 client signs
// in once for many sessions, and occamyd checks the password with
// bcrypt, so openV1 times it apart from the session.
func signIn(ctx context.Context, addr string) (string, error) {
	body, err := json.Marshal(map[string]string{"username": v1Account.name, "password": v1Account.secret})
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := apiV1(ctx, addr, http.MethodPost, "/api/v1/auth/token", "", body, &out); err != nil {
		return "", fmt.Errorf("sign-in: %w", err)
	}
	return out.AccessToken, nil
}

// apiV1 sends a request to API v1 and decodes a 2xx answer's JSON into
// out, unless out is nil.
func apiV1(ctx context.Context, addr, method, path, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, b)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// v1Conn sends a v1 session's input through the client core.
type v1Conn struct {
	cl      *client.Session
	end     func()
	closing atomic.Bool // close was called: the session's end is ours
}

// key presses and releases a key. The SSH backend takes no keys, only
// the bytes a terminal emulator makes of them, so a terminal gets those.
// A desktop's key carries its physical key, as the web client's does,
// which the native RDP backend sends as a scancode.
func (c *v1Conn) key(keysym int) error {
	ctx, cancel := context.WithTimeout(context.Background(), inputTimeout)
	defer cancel()
	if c.cl.Class() == protocol.ClassTerminal {
		b, ok := termBytes(keysym)
		if !ok {
			return fmt.Errorf("no terminal input for keysym %#x", keysym)
		}
		_, err := c.cl.TermInput(ctx, b)
		return err
	}
	if keysym < 0 || keysym > 0x1fffffff { // X11 keysyms have 29 bits
		return fmt.Errorf("keysym %d", keysym)
	}
	ks, code := uint32(keysym), hidUsage(keysym) // #nosec G115 -- checked above
	if _, err := c.cl.Key(ctx, true, ks, code); err != nil {
		return err
	}
	_, err := c.cl.Key(ctx, false, ks, code)
	return err
}

// hidUsage returns the USB HID usage on the keyboard page of the key on
// a US layout that types keysym, as the web client takes it from
// KeyboardEvent.code (client/occamy-web/src/protocol/keyboard.ts), for
// the keys the scenarios press on a desktop, or 0 for another.
func hidUsage(keysym int) uint16 {
	switch {
	case keysym >= 'a' && keysym <= 'z':
		return uint16(0x04 + keysym - 'a') // #nosec G115 -- at most 0x1d
	case keysym >= '1' && keysym <= '9':
		return uint16(0x1e + keysym - '1') // #nosec G115 -- at most 0x26
	case keysym == '0':
		return 0x27
	case keysym == ' ':
		return 0x2c
	case keysym == keyReturn:
		return 0x28
	case keysym == keyEscape:
		return 0x29
	case keysym >= keyF1 && keysym < keyF1+12:
		return uint16(0x3a + keysym - keyF1) // #nosec G115 -- at most 0x45
	}
	return 0
}

// termBytes returns what xterm.js sends for the keys the scenarios press
// in a terminal.
func termBytes(keysym int) ([]byte, bool) {
	switch {
	case keysym == keyReturn:
		return []byte{'\r'}, true
	case keysym == keyEscape:
		return []byte{0x1b}, true
	case keysym >= 0x20 && keysym < 0x7f:
		return []byte{byte(keysym)}, true
	}
	return nil, false
}

// close leaves the session and ends it.
func (c *v1Conn) close() {
	c.closing.Store(true)
	c.cl.Close()
	if c.end != nil {
		c.end()
	}
}

// readV1 delivers the session's events. Frames are applied to the
// client core's framebuffer as Next delivers them, and count from then.
// The next call acknowledges a frame: a graphical one's at d's next
// refresh, a terminal's at once, as occamy's web client does. A
// terminal's bytes go to a terminal emulator, as a viewer's xterm.js
// takes them; that is the client's decoding of a terminal, and marks
// look at its cells.
func (s *session) readV1(c *v1Conn, d display) {
	defer close(s.done)
	defer close(s.frames)
	var (
		term  *terminal.Terminal
		size  image.Point
		drew  bool // the terminal frame being read carried bytes
		shown bool // the terminal showed a cell that is not blank
	)
	for {
		ev, err := c.cl.Next(context.Background())
		if err != nil { // after End
			return
		}
		switch ev := ev.(type) {
		case client.Resize:
			size = image.Pt(ev.Width, ev.Height)
			if term != nil {
				term.Resize(size.X, size.Y)
			}
		case client.TermSnapshot:
			t := time.Now()
			term = terminal.New(size.X, size.Y, terminal.Options{})
			term.Write(ev.Data)
			s.decode.Add(int64(time.Since(t)))
			// The session core serves a blank terminal before the
			// backend has connected, whose snapshot is empty: that
			// shows nothing of the remote.
			drew = len(ev.Data) > 0
		case client.TermData:
			if term == nil {
				s.err = errors.New("term-data before term-snapshot")
				return
			}
			t := time.Now()
			term.Write(ev.Data)
			s.decode.Add(int64(time.Since(t)))
			drew = drew || len(ev.Data) > 0
		case client.Frame:
			// A frame counts from when Next delivers it applied, as v0's
			// from when the client applied it: each client's canvas then
			// reaches the screen at its next refresh.
			now := time.Now()
			m := s.marker.Load()
			var (
				draws, marked, content bool
				damage                 image.Rectangle
			)
			if c.cl.Class() == protocol.ClassTerminal {
				// A terminal shows the remote from its first cell that is
				// not blank: the SSH backend resizes the blank terminal
				// before the shell prints, which sends a clear screen.
				draws, drew = drew, false
				cells := m != nil && m.cells != nil
				if draws && (!shown || cells) {
					scr := term.Snapshot()
					shown = shown || showsCells(scr)
					marked = cells && m.cells(scr)
				}
				content = shown
			} else if len(ev.Dirty) > 0 {
				draws, content = true, true
				for _, r := range ev.Dirty {
					damage = damage.Union(r)
				}
				if m != nil && m.pixels != nil {
					c.cl.Framebuffer(func(img *image.RGBA) { marked = m.pixels(img, damage) })
				}
			}
			if draws {
				s.emit(frame{at: now, damage: damage, marked: marked, content: content})
			}
			// The web client presents a graphical frame, drawing or
			// not, at the screen's next refresh, and acknowledges it by
			// asking for the next event then; a terminal's at once.
			if c.cl.Class() != protocol.ClassTerminal {
				d.present(now)
			}
		case client.End:
			if !c.closing.Load() {
				s.err = fmt.Errorf("the server ended the session: %v %q", ev.Code, ev.Reason)
				if ev.Err != nil {
					s.err = fmt.Errorf("%w: %w", s.err, ev.Err)
				}
			}
			return
		}
	}
}

// showsCells reports a cell of scr that is not blank: one with a
// character, or a background other than the default.
func showsCells(scr *terminal.Screen) bool {
	for y := range scr.Rows {
		for x := range scr.Cols {
			c := scr.Cell(x, y)
			if c.Content != " " && c.Content != "" || c.Style.Bg != terminal.DefaultColor {
				return true
			}
		}
	}
	return false
}

// decoders are the codecs that occamy's web client offers where the
// browser decodes them all (client/occamy-web, LocalDecoder.codecs), in
// its order: deflate, WebP, JPEG, and raw, which the client core has
// built in. Each adds the wall time of each Decode call to spent. The
// client core decodes tiles in parallel as they arrive, so spent sums
// the time of its decoding goroutines, which is neither CPU time nor
// the time a frame waits; copying the decoded tiles into the
// framebuffer is not in it.
func decoders(spent *atomic.Int64) []client.Decoder {
	return []client.Decoder{
		timed{deflateDecoder{}, spent},
		timed{webpDecoder{}, spent},
		timed{jpegDecoder{}, spent},
	}
}

// timed is a Decoder that counts the time it takes.
type timed struct {
	client.Decoder
	spent *atomic.Int64
}

func (d timed) Decode(dst []byte, w, h int, payload []byte) error {
	t := time.Now()
	err := d.Decoder.Decode(dst, w, h, payload)
	d.spent.Add(int64(time.Since(t)))
	return err
}

// rgba views w×h pixels without padding as an image.
func rgba(pix []byte, w, h int) *image.RGBA {
	return &image.RGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
}

// inflaters are codec.Inflaters, which are not safe for concurrent use,
// for the decoding goroutines.
var inflaters = sync.Pool{New: func() any { return new(codec.Inflater) }}

// deflateDecoder decodes the deflate container (codec 2) with occamyd's
// own internal/codec.
type deflateDecoder struct{}

func (deflateDecoder) Codec() protocol.Codec { return protocol.CodecDeflate }

func (deflateDecoder) Decode(dst []byte, w, h int, payload []byte) error {
	in := inflaters.Get().(*codec.Inflater)
	defer inflaters.Put(in)
	return in.Decode(rgba(dst, w, h), payload)
}

// webpDecoder decodes WebP with github.com/gen2brain/vpx/webp, which
// reads VP8's limited-range colours as a browser does
// (docs/protocol.md, Codecs).
type webpDecoder struct{}

func (webpDecoder) Codec() protocol.Codec { return protocol.CodecWebP }

func (webpDecoder) Decode(dst []byte, w, h int, payload []byte) error {
	cfg, err := webp.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := sized("WebP", cfg, w, h); err != nil {
		return err
	}
	m, err := webp.Decode(bytes.NewReader(payload), webp.Options{ToRGBA: true, FrameSizeLimit: w * h})
	if err != nil {
		return err
	}
	img, ok := m.(*image.RGBA)
	if !ok || img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Errorf("a WebP decoded to %T of %v for %d×%d", m, m.Bounds(), w, h)
	}
	// ToRGBA premultiplies alpha, and the protocol's pixels have straight
	// alpha. occamyd's lossy tiles are opaque, where the two agree, and
	// it sends lossless WebP only for cursors to a client that lists
	// neither deflate nor raw, which this one does.
	for y := range h {
		copy(dst[4*w*y:4*w*(y+1)], img.Pix[img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y):])
	}
	return nil
}

// jpegDecoder decodes JPEG with the standard library.
type jpegDecoder struct{}

func (jpegDecoder) Codec() protocol.Codec { return protocol.CodecJPEG }

func (jpegDecoder) Decode(dst []byte, w, h int, payload []byte) error {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := sized("JPEG", cfg, w, h); err != nil {
		return err
	}
	m, err := jpeg.Decode(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	draw.Draw(rgba(dst, w, h), image.Rect(0, 0, w, h), m, m.Bounds().Min, draw.Src)
	return nil
}

// sized checks the size a lossy payload's header claims against its
// rectangle before anything is decoded, as docs/protocol.md requires.
func sized(format string, cfg image.Config, w, h int) error {
	if cfg.Width != w || cfg.Height != h {
		return fmt.Errorf("a %s of %d×%d for %d×%d pixels", format, cfg.Width, cfg.Height, w, h)
	}
	return nil
}
