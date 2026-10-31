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
// protocol. Native RDP is not registered in occamyd yet, and SSH has no
// guacd backend in v1, so neither series serves every protocol.
var v1Connections = map[string]map[string]string{
	"v1-native": {"ssh": "ssh", "vnc": "vnc-native"},
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

// openV1 opens v1 sessions on the connections conns names per protocol.
func openV1(conns map[string]string) func(context.Context, string, string) (*session, error) {
	return func(ctx context.Context, addr, proto string) (*session, error) {
		id, ok := conns[proto]
		if !ok {
			return nil, fmt.Errorf("no v1 connection for %s", proto)
		}
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
			return nil, err
		}
		s.startV1(cl, func() {
			// The owner's leaving keeps the session, and the remote
			// connected, for the owner grace of 60 s, during which it
			// would count against the user's sessions and load the
			// remote.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := apiV1(ctx, addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(cl.SessionID()), token, nil, nil)
			if err != nil {
				log.Printf("  ending session %s: %v", cl.SessionID(), err)
			}
		})
		return s, nil
	}
}

// startV1 runs s on cl. end, if set, ends the session on the server once
// the client left it.
func (s *session) startV1(cl *client.Session, end func()) {
	c := &v1Conn{cl: cl, end: end}
	s.conn = c
	go s.readV1(c)
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
func (c *v1Conn) key(keysym int) error {
	ctx := context.Background()
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
	ks := uint32(keysym) // #nosec G115 -- checked above
	if _, err := c.cl.Key(ctx, true, ks, 0); err != nil {
		return err
	}
	_, err := c.cl.Key(ctx, false, ks, 0)
	return err
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
// client core's framebuffer as Next delivers them, and acknowledged at
// the next call, so the loop takes each frame and comes back at once, as
// a client that draws without delay does. A terminal's bytes go to a
// terminal emulator, as a viewer's xterm.js takes them; that is the
// client's decoding of a terminal, and marks look at its cells.
func (s *session) readV1(c *v1Conn) {
	defer close(s.done)
	defer close(s.frames)
	var (
		term *terminal.Terminal
		size image.Point
		drew bool // the terminal frame being read carried bytes
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
			drew = true
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
			now := time.Now()
			m := s.marker.Load()
			if c.cl.Class() == protocol.ClassTerminal {
				if !drew {
					continue
				}
				drew = false
				s.emit(now, image.Rectangle{}, m != nil && m.cells != nil && m.cells(term.Snapshot()))
				continue
			}
			if len(ev.Dirty) == 0 {
				continue
			}
			var damage image.Rectangle
			for _, r := range ev.Dirty {
				damage = damage.Union(r)
			}
			marked := false
			if m != nil && m.pixels != nil {
				c.cl.Framebuffer(func(img *image.RGBA) { marked = m.pixels(img, damage) })
			}
			s.emit(now, damage, marked)
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

// decoders are the codecs that occamy's web client offers where the
// browser decodes them all (client/occamy-web, LocalDecoder.codecs), in
// its order: deflate, WebP, JPEG, and raw, which the client core has
// built in. Each adds the time it takes to spent. The client core
// decodes tiles in parallel as they arrive, so spent is CPU time, not
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
