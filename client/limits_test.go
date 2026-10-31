// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// A frame of a large screen still undelivered does not count against
// the smaller screen the next frame resizes to: stream sends the resize
// that starts a frame at once, whatever the window.
func TestShrinkWithFrameQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		const w, h, side = 4096, 4096, 512
		sc.send(&protocol.Resize{Width: w, Height: h})
		pix := make([]byte, 4*side*side)
		for y := 0; y < h; y += side {
			for x := 0; x < w; x += side {
				sc.send(&protocol.Tile{FrameID: 1, X: uint16(x), Y: uint16(y), W: side, H: side, Codec: protocol.CodecRaw, Payload: pix}) // #nosec G115 -- small
			}
		}
		sc.send(&protocol.FrameEnd{FrameID: 1})
		sc.send(&protocol.Resize{Width: 1280, Height: 720})
		sc.sync()
		if code, closed := sc.pipe.clientCode(); closed {
			t.Fatalf("the client closed a server within its window with %v", code)
		}
		evs := drain(t, sc.cl, time.Millisecond)
		if fs := frames(evs); len(fs) != 1 || last(evs) == nil {
			t.Errorf("events %d, frames %d", len(evs), len(fs))
		}
	})
}

// A frame's work is bounded: copy-rects and resizes that cost a few
// bytes each and a screen each to apply end the session before Next
// does the work.
func TestFrameWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		const w, h = 8192, 8192
		var ms []protocol.Message
		for range 16 {
			ms = append(ms, &protocol.Resize{Width: w, Height: h})
		}
		for range 64 {
			ms = append(ms, &protocol.CopyRect{FrameID: 1, SrcY: 1, W: w, H: h - 1})
		}
		ms = append(ms, &protocol.FrameEnd{FrameID: 1})
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for _, m := range ms {
			// The client may close the pipe before the frame is all sent.
			_ = sc.srv.WriteMessage(context.Background(), m.Append(nil))
		}
		evs := drain(t, sc.cl, time.Second)
		runtime.ReadMemStats(&after)
		e, ok := last(evs).(client.End)
		if !ok || e.Code != protocol.CloseProtocolError || !errors.Is(e.Err, protocol.ErrInvalid) {
			t.Fatalf("events %#v, want End protocol-error", evs)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 64<<20 {
			t.Errorf("1.4 KB from the server made the client allocate %d MiB", n>>20)
		}
	})
}

// The frame Next delivered counts against the frame window until the
// next call acknowledges it: with it, the server may hold the window
// and one more frame unacknowledged, and no more.
func TestWindowCountsShown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		sc.send(&protocol.Resize{Width: 1, Height: 1}, &protocol.FrameEnd{FrameID: 1})
		for range 2 { // the resize and frame 1, which is now shown
			if _, err := sc.cl.Next(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		sc.send(&protocol.FrameEnd{FrameID: 2}, &protocol.FrameEnd{FrameID: 3})
		synctest.Wait()
		if code, closed := sc.pipe.clientCode(); closed {
			t.Fatalf("the client closed a server within its window with %v", code)
		}
		sc.send(&protocol.FrameEnd{FrameID: 4})
		synctest.Wait()
		evs := drain(t, sc.cl, time.Second)
		e, ok := last(evs).(client.End)
		if !ok || e.Code != protocol.CloseProtocolError || e.Err == nil || !strings.Contains(e.Err.Error(), "more than 3 frames unacknowledged") {
			t.Fatalf("events %#v, want End protocol-error for the window", evs)
		}
		if fs := frames(evs); len(fs) != 2 || fs[0].ID != 2 || fs[1].ID != 3 {
			t.Errorf("frames %+v, want 2 and 3", fs)
		}
	})
}

// Resizes in a row cost one screen, and a resize forgets what its frame
// drew before it, so the frame is the tiles after it.
func TestResizesInARow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		img := noise(64, 48)
		var ms []protocol.Message
		ms = append(ms, &protocol.Resize{Width: 8192, Height: 8192}, rawTile(1, noise(8, 8), image.Rect(0, 0, 8, 8)))
		for range 40 { // 40 screens of 8192², if each cost one
			ms = append(ms, &protocol.Resize{Width: 8192, Height: 8192})
		}
		ms = append(ms, &protocol.Resize{Width: 64, Height: 48}, rawTile(1, img, img.Rect))
		sc.frame(1, ms...)
		evs := drain(t, sc.cl, time.Second)
		if len(evs) != 2 {
			t.Fatalf("events %#v", evs)
		}
		if r, ok := evs[0].(client.Resize); !ok || r.Width != 64 || r.Height != 48 {
			t.Errorf("resize %#v", evs[0])
		}
		if got := sc.cl.Screenshot(); !bytes.Equal(got.Pix, img.Pix) {
			t.Error("the screen is not the frame's last tiles")
		}
	})
}

// A viewport is clamped to the protocol's coordinates, and one that
// shows nothing is refused rather than sent as the whole screen.
func TestViewViewport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, graphical, client.Options{})
		ctx := context.Background()
		if err := sc.cl.View(ctx, true, image.Rect(-100, -100, -10, -10)); err == nil {
			t.Error("a viewport at negative coordinates only was taken")
		}
		if err := sc.cl.View(ctx, true, image.Rect(-10, -20, 50, 60)); err != nil {
			t.Fatal(err)
		}
		m, ok := sc.recv().(*protocol.View)
		if !ok || *m != (protocol.View{Visible: true, W: 50, H: 60}) {
			t.Errorf("view %#v, want 0,0 50x60", m)
		}
	})
}
