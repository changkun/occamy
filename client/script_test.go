// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// scripted is a client of a server that the test plays message by
// message, for what occamyd does not send, such as copy-rect, and for
// servers that break the protocol.
type scripted struct {
	t    *testing.T
	pipe *pipe
	srv  interface {
		ReadMessage(context.Context) ([]byte, error)
		WriteMessage(context.Context, []byte) error
	}
	cl *client.Session
}

// graphical is the welcome of a graphical session with raw tiles.
var graphical = protocol.Welcome{
	ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical, SessionID: "s",
	Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}, FrameWindow: 2, MaxMessageSize: protocol.DefaultMaxMessageSize},
}

func newScripted(t *testing.T, w protocol.Welcome, opts client.Options) *scripted {
	t.Helper()
	sc, err := startScripted(t, opts, &w)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// startScripted starts a client of a server that sends first.
func startScripted(t *testing.T, opts client.Options, first ...protocol.Message) (*scripted, error) {
	t.Helper()
	p := newPipe(0)
	sc := &scripted{t: t, pipe: p, srv: p.server()}
	for _, m := range first {
		sc.send(m)
	}
	cl, err := client.New(context.Background(), p.client(), opts)
	sc.cl = cl
	t.Cleanup(func() {
		if sc.cl != nil {
			sc.cl.Close()
		}
		p.close()
	})
	return sc, err
}

func (sc *scripted) send(ms ...protocol.Message) {
	sc.t.Helper()
	for _, m := range ms {
		sc.sendBytes(m.Append(nil))
	}
}

func (sc *scripted) sendBytes(b []byte) {
	sc.t.Helper()
	if err := sc.srv.WriteMessage(context.Background(), b); err != nil {
		sc.t.Fatal(err)
	}
}

// recv returns the client's next message, or nil if none comes within
// a second or the client closed the pipe.
func (sc *scripted) recv() protocol.Message {
	sc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err := sc.srv.ReadMessage(ctx)
	if err != nil {
		return nil
	}
	m, err := protocol.Decode(b, protocol.ClientMessages(), protocol.Limits{})
	if err != nil {
		sc.t.Fatalf("the client sent % x: %v", b, err)
	}
	return m
}

// sync waits until the client has read everything sent so far: it
// answers a ping only after the messages before it.
func (sc *scripted) sync() {
	sc.t.Helper()
	sc.send(&protocol.Ping{Nonce: 77, Time: 1})
	for {
		switch m := sc.recv().(type) {
		case nil:
			sc.t.Fatal("no pong")
		case *protocol.Pong:
			if m.Nonce == 77 {
				return
			}
		}
	}
}

// frame sends a frame of ms with ID id.
func (sc *scripted) frame(id uint32, ms ...protocol.Message) {
	sc.t.Helper()
	sc.send(ms...)
	sc.send(&protocol.FrameEnd{FrameID: id, ServerTime: uint64(id) * 1000, LastSeq: id})
}

// rawTile is a tile of frame id at r with pixels from img.
func rawTile(id uint32, img *image.RGBA, r image.Rectangle) *protocol.Tile {
	sub := image.NewRGBA(image.Rectangle{Max: r.Size()})
	draw.Draw(sub, sub.Rect, img, r.Min, draw.Src)
	return &protocol.Tile{
		FrameID: id, X: uint16(r.Min.X), Y: uint16(r.Min.Y), W: uint16(r.Dx()), H: uint16(r.Dy()), // #nosec G115 -- small
		Codec: protocol.CodecRaw, Payload: sub.Pix,
	}
}

// noise is an image of w×h distinct pixels.
func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	return img
}

// copyModel copies as copy-rect does, through a copy of the source.
func copyModel(img *image.RGBA, dst image.Rectangle, src image.Point) {
	tmp := image.NewRGBA(image.Rectangle{Max: dst.Size()})
	draw.Draw(tmp, tmp.Rect, img, src, draw.Src)
	draw.Draw(img, dst, tmp, image.Point{}, draw.Src)
}

// copy-rect copies from the screen as the messages before it left it,
// overlapping or not, and in order with the tiles around it.
func TestCopyRect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		const w, h = 40, 30
		want := noise(w, h)
		sc.frame(1, &protocol.Resize{Width: w, Height: h}, rawTile(1, want, want.Rect))
		cp := func(id uint32, dst image.Rectangle, src image.Point) *protocol.CopyRect {
			copyModel(want, dst, src)
			return &protocol.CopyRect{
				FrameID: id, SrcX: uint16(src.X), SrcY: uint16(src.Y), // #nosec G115 -- small
				DstX: uint16(dst.Min.X), DstY: uint16(dst.Min.Y), W: uint16(dst.Dx()), H: uint16(dst.Dy()), // #nosec G115 -- small
			}
		}
		// Scrolling down and up, right and left, onto itself.
		sc.frame(2,
			cp(2, image.Rect(0, 5, w, h), image.Pt(0, 0)),
			cp(2, image.Rect(0, 0, w, h-3), image.Pt(0, 3)),
			cp(2, image.Rect(4, 2, 30, 20), image.Pt(2, 1)),
			cp(2, image.Rect(1, 1, 20, 9), image.Pt(3, 4)),
		)
		// A tile after a copy draws over it; a copy after a tile copies it.
		patch := image.NewRGBA(want.Rect)
		draw.Draw(patch, patch.Rect, image.NewUniform(color.RGBA{1, 2, 3, 255}), image.Point{}, draw.Src)
		c1 := cp(3, image.Rect(10, 10, 20, 20), image.Pt(0, 0))
		draw.Draw(want, image.Rect(12, 12, 16, 16), patch, image.Pt(12, 12), draw.Src)
		t3 := rawTile(3, want, image.Rect(12, 12, 16, 16))
		c2 := cp(3, image.Rect(30, 0, 34, 4), image.Pt(12, 12))
		sc.frame(3, c1, t3, c2)

		var fs []client.Frame
		for len(fs) < 3 {
			ev := next(t, sc.cl, time.Second)
			if ev == nil {
				t.Fatal("no frame")
			}
			if f, ok := ev.(client.Frame); ok {
				fs = append(fs, f)
			}
		}
		if got := sc.cl.Screenshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Error("the screen differs from the model")
		}
		wantDirty := []image.Rectangle{image.Rect(10, 10, 20, 20), image.Rect(12, 12, 16, 16), image.Rect(30, 0, 34, 4)}
		if !slices.Equal(fs[2].Dirty, wantDirty) {
			t.Errorf("frame 3's dirty rectangles %v, want %v", fs[2].Dirty, wantDirty)
		}
		if !slices.Equal(fs[0].Dirty, []image.Rectangle{want.Rect}) {
			t.Errorf("the resizing frame's dirty rectangles %v, want the screen", fs[0].Dirty)
		}
		if fs[2].LastSeq != 3 || fs[2].ServerTime != 3*time.Millisecond {
			t.Errorf("frame 3: last_seq %d, server time %v", fs[2].LastSeq, fs[2].ServerTime)
		}
	})
}

// The small state reaches the caller in order, the latest value of each
// kind once, and pings are answered.
func TestSmallState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		cur := []byte{255, 0, 0, 128, 0, 255, 0, 255, 0, 0, 255, 0, 1, 2, 3, 4}
		sc.send(
			&protocol.Cursor{W: 1, H: 1, Codec: protocol.CodecRaw, Payload: []byte{9, 9, 9, 9}},
			&protocol.CursorMove{X: 1, Y: 2},
			&protocol.Title{Title: "one"},
			&protocol.ViewerJoined{ViewerID: 7, Role: protocol.RoleReadOnly, Name: "ann"},
			&protocol.Cursor{HotX: 1, HotY: 0, W: 2, H: 2, Codec: protocol.CodecRaw, Payload: cur},
			&protocol.ServerClipboard{MIME: "text/plain", Data: []byte("a")},
			&protocol.Bell{}, &protocol.Bell{}, &protocol.Bell{}, &protocol.Bell{}, &protocol.Bell{},
			&protocol.ViewerLeft{ViewerID: 7},
			&protocol.ViewerJoined{ViewerID: 8, Role: protocol.RoleReadOnly},
			&protocol.Title{Title: "two"},
			&protocol.CursorMove{X: 3, Y: 4},
			&protocol.ServerClipboard{MIME: "text/plain", Data: []byte("b")},
			&protocol.Prompt{ID: 5, Name: "ssh", Fields: []protocol.PromptField{{Label: "Password:", Secret: true}}},
		)
		sc.sync()
		evs := drain(t, sc.cl, 10*time.Millisecond)
		var kinds []string
		for _, ev := range evs {
			kinds = append(kinds, typeName(ev))
		}
		want := []string{"Cursor", "CursorMove", "Title", "Clipboard", "Bell", "Bell", "Bell", "ViewerJoined", "Prompt"}
		if !slices.Equal(kinds, want) {
			t.Fatalf("events %v, want %v", kinds, want)
		}
		c := evs[0].(client.Cursor)
		if c.Hot != image.Pt(1, 0) || c.Image.Rect != image.Rect(0, 0, 2, 2) || !bytes.Equal(c.Image.Pix, cur) {
			t.Errorf("cursor %v at %v, %v", c.Image.Rect, c.Hot, c.Image.Pix)
		}
		if c.Image.NRGBAAt(0, 0) != (color.NRGBA{255, 0, 0, 128}) {
			t.Errorf("the cursor's pixels are not straight alpha: %v", c.Image.NRGBAAt(0, 0))
		}
		if m := evs[1].(client.CursorMove); m.X != 3 || m.Y != 4 {
			t.Errorf("cursor move %+v", m)
		}
		if ti := evs[2].(client.Title); ti.Title != "two" {
			t.Errorf("title %q", ti.Title)
		}
		if cb := evs[3].(client.Clipboard); string(cb.Data) != "b" || cb.MIME != "text/plain" {
			t.Errorf("clipboard %+v", cb)
		}
		if j := evs[7].(client.ViewerJoined); j.ViewerID != 8 {
			t.Errorf("joined %+v, want viewer 8 alone", j)
		}
		if p := evs[8].(client.Prompt); p.ID != 5 || p.Name != "ssh" || len(p.Fields) != 1 || !p.Fields[0].Secret {
			t.Errorf("prompt %+v", p)
		}

		// Hiding the cursor reports no image.
		sc.send(&protocol.Cursor{})
		sc.sync()
		if c, ok := next(t, sc.cl, time.Second).(client.Cursor); !ok || c.Image != nil {
			t.Errorf("hidden cursor: %#v", c)
		}

		// The owner answers the prompt.
		if err := sc.cl.PromptReply(context.Background(), 5, []string{"hunter2"}); err != nil {
			t.Fatal(err)
		}
		if m, ok := sc.recv().(*protocol.PromptReply); !ok || m.ID != 5 || !slices.Equal(m.Answers, []string{"hunter2"}) {
			t.Errorf("prompt reply %#v", m)
		}
	})
}

func typeName(ev client.Event) string {
	switch ev.(type) {
	case client.Cursor:
		return "Cursor"
	case client.CursorMove:
		return "CursorMove"
	case client.Title:
		return "Title"
	case client.Clipboard:
		return "Clipboard"
	case client.Bell:
		return "Bell"
	case client.ViewerJoined:
		return "ViewerJoined"
	case client.ViewerLeft:
		return "ViewerLeft"
	case client.Prompt:
		return "Prompt"
	case client.Frame:
		return "Frame"
	case client.Resize:
		return "Resize"
	case client.End:
		return "End"
	}
	return "?"
}

// Small state that arrives within a frame reaches the caller after the
// frames before it, and before the frame it arrived in.
func TestEventOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		img := noise(8, 8)
		sc.send(&protocol.Resize{Width: 8, Height: 8}, &protocol.Title{Title: "in frame 1"}, rawTile(1, img, img.Rect))
		sc.send(&protocol.FrameEnd{FrameID: 1})
		sc.send(&protocol.Bell{})
		sc.frame(2, rawTile(2, img, image.Rect(0, 0, 4, 4)))
		sc.sync()
		var kinds []string
		for _, ev := range drain(t, sc.cl, 10*time.Millisecond) {
			kinds = append(kinds, typeName(ev))
		}
		want := []string{"Title", "Resize", "Frame", "Bell", "Frame"}
		if !slices.Equal(kinds, want) {
			t.Errorf("events %v, want %v", kinds, want)
		}
	})
}

// A server that breaks the protocol ends the session with the close code
// for what it broke, which the client sends it and reports.
func TestProtocolErrors(t *testing.T) {
	img := noise(8, 8)
	resize := &protocol.Resize{Width: 8, Height: 8}
	for _, c := range []struct {
		name string
		opts client.Options
		msgs []protocol.Message
		raw  []byte
		want protocol.CloseCode
	}{
		{"tile before resize", client.Options{}, []protocol.Message{rawTile(1, img, image.Rect(0, 0, 4, 4))}, nil, protocol.CloseProtocolError},
		{"tile outside the screen", client.Options{}, []protocol.Message{&protocol.Resize{Width: 4, Height: 4}, rawTile(1, img, image.Rect(2, 2, 6, 6))}, nil, protocol.CloseProtocolError},
		{"copy from outside the screen", client.Options{}, []protocol.Message{resize, &protocol.CopyRect{FrameID: 1, SrcX: 6, W: 4, H: 4}}, nil, protocol.CloseProtocolError},
		{"tile of another frame", client.Options{}, []protocol.Message{resize, rawTile(2, img, img.Rect)}, nil, protocol.CloseProtocolError},
		{"frame-end out of order", client.Options{}, []protocol.Message{&protocol.FrameEnd{FrameID: 2}}, nil, protocol.CloseProtocolError},
		{"codec not agreed", client.Options{}, []protocol.Message{resize, &protocol.Tile{FrameID: 1, W: 1, H: 1, Codec: protocol.CodecPNG, Payload: []byte{1}}}, nil, protocol.CloseProtocolError},
		{"terminal data in a graphical session", client.Options{}, []protocol.Message{&protocol.TermData{Data: []byte("x")}}, nil, protocol.CloseProtocolError},
		{"second welcome", client.Options{}, []protocol.Message{&graphical}, nil, protocol.CloseProtocolError},
		{"client message", client.Options{}, []protocol.Message{&protocol.Key{Seq: 1, Down: true, Keysym: 'a'}}, nil, protocol.CloseUnsupported},
		{"screen too large", client.Options{MaxPixels: 63}, []protocol.Message{resize, rawTile(1, img, img.Rect)}, nil, protocol.CloseUnsupported},
		{"frames beyond the window", client.Options{}, []protocol.Message{
			&protocol.FrameEnd{FrameID: 1}, &protocol.FrameEnd{FrameID: 2}, &protocol.FrameEnd{FrameID: 3}, &protocol.FrameEnd{FrameID: 4},
		}, nil, protocol.CloseProtocolError},
		{"truncated", client.Options{}, nil, []byte{0x05, 0x02, 0, 0, 1}, protocol.CloseProtocolError},
		{"too large", client.Options{}, nil, append([]byte{0x07, 0x02, 0, 0}, make([]byte, protocol.DefaultMaxMessageSize)...), protocol.CloseTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newScripted(t, graphical, c.opts)
				sc.send(c.msgs...)
				if c.raw != nil {
					sc.sendBytes(c.raw)
				}
				// The client tells the server, then closes.
				var got *protocol.Close
				for m := sc.recv(); m != nil; m = sc.recv() {
					if cm, ok := m.(*protocol.Close); ok {
						got = cm
					}
				}
				if got == nil || got.Code != c.want {
					t.Errorf("the client sent close %+v, want code %v", got, c.want)
				}
				if code, ok := sc.pipe.clientCode(); !ok || code != c.want {
					t.Errorf("the client closed the connection with %v, want %v", code, c.want)
				}
				evs := drain(t, sc.cl, time.Second)
				e, ok := last(evs).(client.End)
				if !ok || e.Code != c.want || e.Err == nil {
					t.Fatalf("last event %#v, want End %v with the error", last(evs), c.want)
				}
				t.Log(e.Err)
				if _, err := sc.cl.Key(context.Background(), true, 'a', 0); !errors.Is(err, client.ErrClosed) {
					t.Errorf("Key after the end: %v", err)
				}
			})
		})
	}
}

// The server's close ends the session with its code and reason, and
// the client sends no close of its own.
func TestServerClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		sc.send(&protocol.Title{Title: "before"}, &protocol.Close{Code: protocol.CloseIdle, Reason: "idle"})
		evs := drain(t, sc.cl, time.Second)
		if len(evs) != 2 {
			t.Fatalf("events %#v", evs)
		}
		if e, ok := evs[1].(client.End); !ok || e.Code != protocol.CloseIdle || e.Reason != "idle" || e.Err != nil {
			t.Errorf("End %#v", evs[1])
		}
		for m := sc.recv(); m != nil; m = sc.recv() {
			if _, ok := m.(*protocol.Close); ok {
				t.Error("the client answered close with close")
			}
		}
		if code, _ := sc.pipe.clientCode(); code != protocol.CloseIdle {
			t.Errorf("the client closed the connection with %v, want the server's code", code)
		}
	})
}

// The server's refusal before welcome is New's error.
func TestRefusedBeforeWelcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc, err := startScripted(t, client.Options{}, &protocol.Close{Code: protocol.CloseUnauthorized, Reason: "token expired"})
		var ce *client.CloseError
		if !errors.As(err, &ce) || ce.Code != protocol.CloseUnauthorized || ce.Reason != "token expired" {
			t.Fatalf("New returned %v", err)
		}
		if code, _ := sc.pipe.clientCode(); code != protocol.CloseUnauthorized {
			t.Errorf("closed with %v", code)
		}
	})
}

// A welcome the client cannot use is refused with unsupported; one
// that is not a welcome, with a protocol error.
func TestBadWelcome(t *testing.T) {
	for _, c := range []struct {
		name  string
		first protocol.Message
		want  protocol.CloseCode
	}{
		{"no codec in common", &protocol.Welcome{Role: protocol.RoleOwner, Class: protocol.ClassGraphical, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG}}}, protocol.CloseUnsupported},
		{"only cached in common", &protocol.Welcome{Role: protocol.RoleOwner, Class: protocol.ClassGraphical, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG, protocol.CodecCached}, CacheSlots: 4}}, protocol.CloseUnsupported},
		{"no terminal", &protocol.Welcome{Role: protocol.RoleOwner, Class: protocol.ClassTerminal}, protocol.CloseUnsupported},
		{"title first", &protocol.Title{Title: "x"}, protocol.CloseProtocolError},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc, err := startScripted(t, client.Options{}, c.first)
				if err == nil || protocol.CloseCodeFor(err) != c.want {
					t.Errorf("New returned %v, want code %v", err, c.want)
				}
				if m, ok := sc.recv().(*protocol.Close); !ok || m.Code != c.want {
					t.Errorf("the client sent %#v, want close %v", m, c.want)
				}
				if code, _ := sc.pipe.clientCode(); code != c.want {
					t.Errorf("closed with %v", code)
				}
			})
		})
	}
}

// What the client agrees to never exceeds what it offered, whatever the
// server states.
func TestAgreed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := graphical
		w.Capabilities = protocol.Capabilities{
			Codecs:         []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw},
			MaxTextureSize: 4096, Terminal: 1, FrameWindow: 9, MaxMessageSize: 1 << 30, CacheSlots: 64,
		}
		sc := newScripted(t, w, client.Options{FrameWindow: 3, MaxMessageSize: 1 << 20, MaxTextureSize: 512, CacheSlots: 16})
		got := sc.cl.Capabilities()
		want := protocol.Capabilities{
			Codecs: []protocol.Codec{protocol.CodecRaw}, MaxTextureSize: 512, Terminal: 1,
			FrameWindow: 3, MaxMessageSize: 1 << 20, CacheSlots: 16,
		}
		if !slices.Equal(got.Codecs, want.Codecs) || got.MaxTextureSize != want.MaxTextureSize || got.Terminal != want.Terminal ||
			got.FrameWindow != want.FrameWindow || got.MaxMessageSize != want.MaxMessageSize || got.CacheSlots != want.CacheSlots {
			t.Errorf("agreed %+v, want %+v", got, want)
		}
		// A client without a cache offers neither the cached codec nor
		// slots, and agrees to neither, whatever the server states.
		if h := (client.Options{NoCache: true}).Hello(); h.Capabilities.CacheSlots != 0 || slices.Contains(h.Capabilities.Codecs, protocol.CodecCached) {
			t.Errorf("offered %+v without a cache", h.Capabilities)
		}
		w.Capabilities.Codecs = append(w.Capabilities.Codecs, protocol.CodecCached)
		sc = newScripted(t, w, client.Options{NoCache: true})
		if got := sc.cl.Capabilities(); got.CacheSlots != 0 || slices.Contains(got.Codecs, protocol.CodecCached) {
			t.Errorf("agreed %+v without a cache", got)
		}
	})
}

// Input waits once more than a message's worth is queued and the
// network takes nothing, until its context is done.
func TestInputWaitsForNetwork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPipe(0)
		srv := p.server()
		_ = srv.WriteMessage(context.Background(), graphical.Append(nil))
		stall := &stalled{Conn: p.client(), release: make(chan struct{})}
		cl, err := client.New(context.Background(), stall, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(stall.release)
			cl.Close()
			p.close()
		}()
		big := make([]byte, protocol.MinMessageSize)
		for range 3 { // one in the writer, one queued, one within the limit
			if _, err := cl.TermInput(context.Background(), big); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var err2 error
		for range protocol.DefaultMaxMessageSize / protocol.MinMessageSize {
			if _, err2 = cl.TermInput(ctx, big); err2 != nil {
				break
			}
		}
		if !errors.Is(err2, context.DeadlineExceeded) {
			t.Errorf("input to a stalled network: %v, want the context's error", err2)
		}
	})
}

// stalled is a client connection whose writes wait for release.
type stalled struct {
	client.Conn
	release chan struct{}
}

func (s *stalled) WriteMessage(ctx context.Context, p []byte) error {
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Conn.WriteMessage(ctx, p)
}
