// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command webinterop serves occamyd's stream code over WebSocket to the
// browser client's interop test (client/occamy-web/src/protocol/
// interop.test.ts), which runs it, and to the protocol demo page, which
// runs in a browser by hand. It listens on 127.0.0.1, or another
// loopback address, prints its URL on the first line of stdout, and
// exits when stdin closes, so that it never outlives the test that
// started it; run by hand, it runs in the foreground.
//
// Routes, each a stream route of occamyd in miniature that takes the
// token "interop" in Sec-WebSocket-Protocol and hello in the URL:
//
//	/graphical  a scripted desktop (desk)
//	/terminal   an echoing terminal (backendtest.Terminal)
//
// and, for the test to compare with what the client shows, JSON of the
// last desktop started:
//
//	/screen   its framebuffer, RGBA
//	/cursor   its pointer image, RGBA with straight alpha
//	/inputs   the input it got
//	/log      in the order they happened: "shown" and "hidden" as
//	          demands came, and "frame N" as its viewer was sent the
//	          frame-end of frame N
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// token is the access token the routes take.
const token = "interop"

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "the address to listen on")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	// The routes take a fixed token, any Origin and any page's fetches.
	if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() {
		log.Fatalf("%v is not a loopback address", ln.Addr())
	}
	// The parent closes stdin, or dies, when it is done with the server.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
	fmt.Printf("http://%s\n", ln.Addr())
	srv := &http.Server{Handler: newServer(slog.New(slog.DiscardHandler)), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

type server struct {
	mux *http.ServeMux
	log *slog.Logger

	mu   sync.Mutex
	desk *desk // the last desktop started
}

func newServer(logger *slog.Logger) *server {
	s := &server{mux: http.NewServeMux(), log: logger}
	s.mux.HandleFunc("/graphical", func(w http.ResponseWriter, r *http.Request) {
		s.serve(w, r, newDesk())
	})
	s.mux.HandleFunc("/terminal", func(w http.ResponseWriter, r *http.Request) {
		s.serve(w, r, backendtest.NewTerminal(80, 24, 0))
	})
	s.mux.HandleFunc("/screen", s.state(func(d *desk) any { return d.screen() }))
	s.mux.HandleFunc("/cursor", s.state(func(d *desk) any { return d.cursor() }))
	s.mux.HandleFunc("/inputs", s.state(func(d *desk) any { return d.recorded() }))
	s.mux.HandleFunc("/log", s.state(func(d *desk) any { return d.logged() }))
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The demo page fetches the JSON routes from the Vite dev server's
	// origin.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	s.mux.ServeHTTP(w, r)
}

// state serves the JSON of f's view of the last desktop started.
func (s *server) state(f func(d *desk) any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		d := s.desk
		s.mu.Unlock()
		if d == nil {
			http.Error(w, "no desktop started", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(f(d)); err != nil {
			s.log.Debug("state not sent", "err", err)
		}
	}
}

// maxMessage is the largest message the routes read: the protocol's
// default max_message_size.
const maxMessage = protocol.DefaultMaxMessageSize

// serve is occamyd's stream route in miniature: it authenticates the
// token, decodes hello, and serves a session of b to its owner.
func (s *server) serve(w http.ResponseWriter, r *http.Request, b backend.Backend) {
	tok, terr := protocol.TokenFromSubprotocols(r.Header.Values("Sec-WebSocket-Protocol"))
	h, herr := protocol.DecodeHelloQuery(r.URL.Query().Get(protocol.HelloParameter))
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{protocol.Subprotocol},
		// Node's WebSocket sends no Origin; the server listens on
		// loopback only.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	c.SetReadLimit(maxMessage)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if terr != nil || herr != nil || tok != token {
		m := protocol.Close{Code: protocol.CloseUnauthorized, Reason: "token refused"}
		_ = c.Write(ctx, websocket.MessageBinary, m.Append(nil))
		_ = c.Close(websocket.StatusCode(m.Code), m.Reason)
		return
	}
	cn := conn{c: c}
	if d, ok := b.(*desk); ok {
		// The JSON routes show the desktop of the last client let in.
		s.mu.Lock()
		s.desk = d
		s.mu.Unlock()
		cn.sent = d.sent
	}
	sess, owner, err := session.Start(ctx, session.Config{Backend: b, Logger: s.log})
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, "")
		return
	}
	defer sess.Close()
	if err := stream.Serve(ctx, cn, owner, h, stream.Config{SessionID: "interop", Logger: s.log}); err != nil {
		s.log.Debug("stream ended", "err", err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// conn adapts a WebSocket to stream.Conn, as occamyd's handler does: a
// close from the client ends the reads.
type conn struct {
	c    *websocket.Conn
	sent func(p []byte) // if set, sees each message before it is written
}

func (w conn) ReadMessage(ctx context.Context) ([]byte, error) {
	typ, p, err := w.c.Read(ctx)
	if websocket.CloseStatus(err) != -1 {
		return nil, io.EOF
	}
	if err == nil && typ != websocket.MessageBinary {
		err = errors.New("a text message")
	}
	return p, err
}

func (w conn) WriteMessage(ctx context.Context, p []byte) error {
	if w.sent != nil {
		w.sent(p)
	}
	return w.c.Write(ctx, websocket.MessageBinary, p)
}

// The desktop's first size, and the sizes a resize may ask for.
const (
	deskWidth, deskHeight = 320, 200
	maxSide               = 2048
)

// desk is a scripted graphical backend. It paints a pattern of many
// colours, which goes as raw pixels in deflate, above a band of few,
// which goes as a palette; shows the last key pressed in
// backendtest.Swatch in backendtest.SwatchColor; takes a resize; echoes
// the clipboard back; and takes text as commands: "bell" rings the bell,
// "title t" sets the title to t, and "end" ends the session as the
// remote would.
type desk struct {
	in chan backend.InputEvent

	mu     sync.Mutex
	img    *image.RGBA // the screen as painted
	inputs []inputJSON
	log    []string // what /log reports
}

var _ backend.Backend = (*desk)(nil)

func newDesk() *desk {
	return &desk{in: make(chan backend.InputEvent, 256)}
}

func (d *desk) Class() backend.Class { return backend.Graphical }

func (d *desk) Input(ev backend.InputEvent) error {
	d.mu.Lock()
	d.inputs = append(d.inputs, toJSON(ev))
	d.mu.Unlock()
	select {
	case d.in <- ev:
		return nil
	default:
		return backendtest.ErrInputFull
	}
}

func (d *desk) Demand(dm backend.Demand) {
	d.mu.Lock()
	if dm.Visible {
		d.log = append(d.log, "shown")
	} else {
		d.log = append(d.log, "hidden")
	}
	d.mu.Unlock()
}

// sent logs a frame-end its viewer is sent, in order with the demands:
// the session records a viewer's hiding before the stream's writer can
// see it, so a frame logged after "hidden" was started after it.
func (d *desk) sent(p []byte) {
	if m, err := protocol.Decode(p, protocol.ServerMessages(), protocol.Limits{}); err == nil {
		if fe, ok := m.(*protocol.FrameEnd); ok {
			d.mu.Lock()
			d.log = append(d.log, fmt.Sprintf("frame %d", fe.FrameID))
			d.mu.Unlock()
		}
	}
}

func (d *desk) Run(ctx context.Context, sink backend.Sink) error {
	d.resize(sink, deskWidth, deskHeight)
	sink.Cursor(cursorImage(), cursorHot)
	sink.Title("interop")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-d.in:
			switch ev := ev.(type) {
			case backend.Key:
				if ev.Down {
					d.fill(sink, backendtest.Swatch, backendtest.SwatchColor(ev.Keysym))
				}
			case backend.Resize:
				if ev.Width > 0 && ev.Height > 0 && ev.Width <= maxSide && ev.Height <= maxSide {
					d.resize(sink, ev.Width, ev.Height)
				}
			case backend.Clipboard:
				sink.Clipboard(ev.MIME, ev.Data)
			case backend.Text:
				switch cmd, arg, _ := strings.Cut(ev.Text, " "); cmd {
				case "bell":
					sink.Bell()
				case "title":
					sink.Title(arg)
				case "end":
					return nil
				}
			}
		}
	}
}

// resize starts a screen of w×h and paints it: smooth gradients, as of
// a photograph, which a browser gets lossy, above stripes of two
// colours. The stripes start at a tile's edge where they can: a tile of
// both goes lossy, which smears the stripes' colours.
func (d *desk) resize(sink backend.Sink, w, h int) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	band := (h - h/4) / 64 * 64
	if band == 0 {
		band = h - h/4
	}
	for y := range h {
		for x := range w {
			c := color.RGBA{
				R: uint8(255 * x / max(w-1, 1)),         // #nosec G115 -- at most 255
				G: uint8(255 * y / max(h-1, 1)),         // #nosec G115 -- at most 255
				B: uint8(255 * (x + y) / max(w+h-2, 1)), // #nosec G115 -- at most 255
				A: 0xff,
			}
			if y >= band {
				// Stripes of two colours.
				c = color.RGBA{R: 0x20, G: 0x40, B: 0x80, A: 0xff}
				if (x/7+y/3)%2 == 0 {
					c = color.RGBA{R: 0xf0, G: 0xe0, B: 0x10, A: 0xff}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	d.mu.Lock()
	d.img = img
	d.mu.Unlock()
	sink.Resize(w, h)
	d.paint(sink, img.Rect)
}

// fill paints r in c.
func (d *desk) fill(sink backend.Sink, r image.Rectangle, c color.RGBA) {
	d.mu.Lock()
	r = r.Intersect(d.img.Rect)
	draw.Draw(d.img, r, image.NewUniform(c), image.Point{}, draw.Src)
	d.mu.Unlock()
	d.paint(sink, r)
}

// paint copies r of the painted screen to the session's framebuffer.
func (d *desk) paint(sink backend.Sink, r image.Rectangle) {
	fb := sink.Lock(r)
	d.mu.Lock()
	draw.Draw(fb, r, d.img, r.Min, draw.Src)
	d.mu.Unlock()
	sink.Unlock(r)
}

// cursorHot is the pointer's hot spot.
var cursorHot = image.Pt(3, 4)

// cursorImage is a 16×16 pointer of opaque, translucent and transparent
// pixels, premultiplied as an image.RGBA holds them.
func cursorImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			var c color.NRGBA
			switch {
			case x <= y && y < 12:
				c = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			case x <= y+2 && y < 14:
				c = color.NRGBA{R: 0x10, G: 0x20, B: 0x30, A: uint8(16 * x)} // #nosec G115 -- x < 16
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// imageJSON is an image as the JSON routes send it.
type imageJSON struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	HotX   int    `json:"hotX,omitempty"`
	HotY   int    `json:"hotY,omitempty"`
	Pixels []byte `json:"pixels"` // base64 by encoding/json
}

func (d *desk) screen() imageJSON {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.img == nil {
		return imageJSON{}
	}
	b := d.img.Rect
	return imageJSON{Width: b.Dx(), Height: b.Dy(), Pixels: append([]byte(nil), d.img.Pix...)}
}

// cursor is the pointer's image with straight alpha, as
// color.NRGBAModel converts it, which the encoder follows.
func (d *desk) cursor() imageJSON {
	img := cursorImage()
	b := img.Rect
	out := image.NewNRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	return imageJSON{Width: b.Dx(), Height: b.Dy(), HotX: cursorHot.X, HotY: cursorHot.Y, Pixels: out.Pix}
}

// inputJSON is an input as /inputs reports it.
type inputJSON struct {
	Type    string `json:"type"`
	Down    bool   `json:"down,omitempty"`
	Keysym  uint32 `json:"keysym,omitempty"`
	Code    uint16 `json:"code,omitempty"`
	X       int    `json:"x,omitempty"`
	Y       int    `json:"y,omitempty"`
	Buttons uint8  `json:"buttons,omitempty"`
	DX      int    `json:"dx,omitempty"`
	DY      int    `json:"dy,omitempty"`
	Mode    uint8  `json:"mode,omitempty"`
	Text    string `json:"text,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Data    string `json:"data,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

func toJSON(ev backend.InputEvent) inputJSON {
	switch ev := ev.(type) {
	case backend.Key:
		return inputJSON{Type: "key", Down: ev.Down, Keysym: ev.Keysym, Code: ev.Code}
	case backend.Pointer:
		return inputJSON{Type: "pointer", X: ev.X, Y: ev.Y, Buttons: uint8(ev.Buttons)}
	case backend.Wheel:
		return inputJSON{Type: "wheel", DX: ev.DX, DY: ev.DY, Mode: uint8(ev.Mode)}
	case backend.Text:
		return inputJSON{Type: "text", Text: ev.Text}
	case backend.TermInput:
		return inputJSON{Type: "term-input", Data: string(ev.Data)}
	case backend.Clipboard:
		return inputJSON{Type: "clipboard", MIME: ev.MIME, Data: string(ev.Data)}
	case backend.Resize:
		return inputJSON{Type: "resize", Width: ev.Width, Height: ev.Height}
	}
	return inputJSON{Type: fmt.Sprintf("%T", ev)}
}

func (d *desk) recorded() []inputJSON {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]inputJSON{}, d.inputs...)
}

func (d *desk) logged() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.log...)
}
