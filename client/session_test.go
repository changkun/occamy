// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// scribble draws n random rectangles through sink, some by copying, as
// a desktop that changes in many places does.
func scribble(sink backend.Sink, size image.Point, n int, rng *rand.Rand, gap time.Duration) {
	for range n {
		x0, y0 := rng.IntN(size.X), rng.IntN(size.Y)
		r := image.Rect(x0, y0, x0+1+rng.IntN(80), y0+1+rng.IntN(60)).Intersect(image.Rectangle{Max: size})
		if rng.IntN(4) == 0 {
			src := image.Pt(rng.IntN(size.X-r.Dx()+1), rng.IntN(size.Y-r.Dy()+1))
			sink.CopyRect(r, src)
		} else {
			c := color.RGBA{uint8(rng.Uint32()), uint8(rng.Uint32()), uint8(rng.Uint32()), 0xff}
			img := sink.Lock(r)
			draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
			sink.Unlock(r)
		}
		time.Sleep(gap)
	}
}

// A graphical session's framebuffer ends up as the server's, byte for
// byte, through raw tiles and through a plugged codec decoded in
// parallel.
func TestGraphicalConverges(t *testing.T) {
	for _, plugged := range []bool{false, true} {
		t.Run(fmt.Sprintf("plugged=%v", plugged), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				codec := &pngCodec{}
				var opts client.Options
				var cfg stream.Config
				if plugged {
					opts.Decoders = []client.Decoder{codec}
					opts.DecodeWorkers = 4
					cfg.Encoder = codec
					cfg.Capabilities.Codecs = []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw}
				}
				b := tap(backendtest.NewScreen(300, 200, 0))
				r := newRig(t, b, opts, cfg)
				if r.cl.Class() != protocol.ClassGraphical || r.cl.Role() != protocol.RoleOwner || r.cl.SessionID() != "s1" {
					t.Errorf("welcome: class %v, role %v, session %q", r.cl.Class(), r.cl.Role(), r.cl.SessionID())
				}
				if plugged && r.cl.Capabilities().Codecs[0] != protocol.CodecPNG {
					t.Errorf("agreed codecs %v, want png first", r.cl.Capabilities().Codecs)
				}
				var evs []client.Event
				done := make(chan struct{})
				go func() {
					defer close(done)
					scribble(b.Sink(), image.Pt(300, 200), 200, rand.New(rand.NewPCG(1, 2)), 3*time.Millisecond)
				}()
				for {
					select {
					case <-done:
					default:
						evs = append(evs, next(t, r.cl, time.Second))
						continue
					}
					break
				}
				evs = append(evs, drain(t, r.cl, 100*time.Millisecond)...)
				want, got := serverScreen(r.s), r.cl.Screenshot()
				if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
					t.Fatalf("client screen %v differs from the server's %v", got.Rect, want.Rect)
				}
				if r.cl.Size() != image.Pt(300, 200) {
					t.Errorf("size %v", r.cl.Size())
				}
				fs := frames(evs)
				if len(fs) < 10 {
					t.Errorf("%d frames for 200 changes; the test shows little", len(fs))
				}
				for i, f := range fs {
					if f.ID != uint32(i+1) { // #nosec G115 -- a small count
						t.Fatalf("frame %d has ID %d", i, f.ID)
					}
				}
				if plugged && codec.count() == 0 {
					t.Error("no tile went through the plugged decoder")
				}
			})
		})
	}
}

// A terminal session's bytes, written to a terminal emulator as they
// come, give the server's screen.
func TestTerminalConverges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := tap(backendtest.NewTerminal(80, 24, 0))
		r := newRig(t, b, client.Options{}, stream.Config{})
		if r.cl.Class() != protocol.ClassTerminal {
			t.Fatalf("class %v", r.cl.Class())
		}
		var term *terminal.Terminal
		var size image.Point
		snapshots := 0
		feed := func(evs []client.Event) {
			for _, ev := range evs {
				switch ev := ev.(type) {
				case client.Resize:
					size = image.Pt(ev.Width, ev.Height)
					if term != nil {
						term.Resize(size.X, size.Y)
					}
				case client.TermSnapshot:
					term = terminal.New(size.X, size.Y, terminal.Options{})
					term.Write(ev.Data)
					snapshots++
				case client.TermData:
					if term == nil {
						t.Fatal("term-data before term-snapshot")
					}
					term.Write(ev.Data)
				}
			}
		}
		feed(drain(t, r.cl, 50*time.Millisecond))
		sink := b.Sink()
		var out strings.Builder
		for i := range 3000 {
			fmt.Fprintf(&out, "\x1b[3%dmline %d 日本\x1b[m\r\n", i%8, i)
		}
		sink.TermData([]byte(out.String()))
		feed(drain(t, r.cl, 50*time.Millisecond))
		sink.Resize(100, 30)
		sink.TermData([]byte("\x1b]2;shell\x07after resize $ "))
		evs := drain(t, r.cl, 100*time.Millisecond)
		feed(evs)
		if term == nil {
			t.Fatal("no term-snapshot")
		}
		if want, got := r.s.Screen(), term.Snapshot(); !got.Equal(want) {
			t.Errorf("client screen\n%s\nserver screen\n%s", got, want)
		}
		if snapshots != 1 {
			t.Errorf("%d term-snapshots", snapshots)
		}
		titled := false
		for _, ev := range evs {
			if ti, ok := ev.(client.Title); ok && ti.Title == "shell" {
				titled = true
			}
		}
		if !titled {
			t.Error("no title event for the shell's title")
		}
		if r.cl.Size() != image.Pt(100, 30) {
			t.Errorf("size %v, want 100x30", r.cl.Size())
		}
	})
}

// Inputs are numbered from 1 in the order they are sent, and the frame
// that first shows a key's effect reports it in last_seq.
func TestInputLastSeq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := backendtest.NewScreen(200, 150, 10*time.Millisecond)
		r := newRig(t, b, client.Options{}, stream.Config{})
		collect(t, r.cl, 50*time.Millisecond)
		ctx := context.Background()
		var seqs []uint32
		for _, k := range []uint32{'a', 'b', 'k'} {
			seq, err := r.cl.Key(ctx, true, k, 0)
			if err != nil {
				t.Fatal(err)
			}
			seqs = append(seqs, seq)
		}
		if seq, err := r.cl.Pointer(ctx, 10, 20, 1); err != nil || seq != 4 {
			t.Fatalf("pointer seq %d, %v", seq, err)
		}
		if seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
			t.Fatalf("key seqs %v, want 1, 2, 3", seqs)
		}
		want := backendtest.SwatchColor('k')
		var carrier *client.Frame
		for i := 0; i < 100 && carrier == nil; i++ {
			ev := next(t, r.cl, time.Second)
			f, ok := ev.(client.Frame)
			if !ok {
				continue
			}
			var px color.RGBA
			r.cl.Framebuffer(func(img *image.RGBA) { px = img.RGBAAt(1, 1) })
			if px == want {
				carrier = &f
			}
		}
		if carrier == nil {
			t.Fatal("the key's effect never arrived")
		}
		if carrier.LastSeq < 3 {
			t.Errorf("frame %d shows key 3's effect but reports last_seq %d", carrier.ID, carrier.LastSeq)
		}
		var keys []uint32
		for _, ev := range b.Inputs() {
			if k, ok := ev.(backend.Key); ok {
				keys = append(keys, k.Keysym)
			}
		}
		if len(keys) != 3 || keys[0] != 'a' || keys[2] != 'k' {
			t.Errorf("the backend got keysyms %v", keys)
		}
	})
}

// A terminal's echo arrives in a frame that reports the input.
func TestTerminalInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewTerminal(80, 24, 0), client.Options{}, stream.Config{})
		drain(t, r.cl, 50*time.Millisecond)
		seq, err := r.cl.TermInput(context.Background(), []byte("typed\r"))
		if err != nil || seq != 1 {
			t.Fatalf("seq %d, %v", seq, err)
		}
		var echoed bool
		for _, ev := range drain(t, r.cl, 50*time.Millisecond) {
			switch ev := ev.(type) {
			case client.TermData:
				echoed = echoed || bytes.Contains(ev.Data, []byte("typed"))
			case client.Frame:
				if echoed && ev.LastSeq != seq {
					t.Errorf("the echo's frame reports last_seq %d, want %d", ev.LastSeq, seq)
				}
				if echoed {
					return
				}
			}
		}
		t.Error("no echo")
	})
}

// A hidden client gets no frames; shown, it gets the screen at once, and
// a viewport limits the tiles.
func TestVisibility(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(256, 256, 0), client.Options{Hidden: true}, stream.Config{})
		if fs := frames(drain(t, r.cl, time.Second)); len(fs) != 0 {
			t.Fatalf("%d frames for a hidden client", len(fs))
		}
		ctx := context.Background()
		if err := r.cl.View(ctx, true, image.Rect(0, 0, 100, 60)); err != nil {
			t.Fatal(err)
		}
		var dirty []image.Rectangle
		for _, f := range frames(drain(t, r.cl, 100*time.Millisecond)) {
			dirty = append(dirty, f.Dirty...)
		}
		// The viewport covers two of the 64-pixel tiles, but the frame
		// that resizes the screen marks the whole screen.
		if r.cl.Size() != image.Pt(256, 256) {
			t.Fatalf("size %v after the first frame", r.cl.Size())
		}
		got := r.cl.Screenshot()
		want := serverScreen(r.s)
		for _, p := range []image.Point{{1, 1}, {99, 59}, {127, 63}} {
			if got.RGBAAt(p.X, p.Y) != want.RGBAAt(p.X, p.Y) {
				t.Errorf("pixel %v in the viewport differs", p)
			}
		}
		if got.RGBAAt(200, 200) == want.RGBAAt(200, 200) {
			t.Errorf("pixel 200,200 outside the viewport arrived")
		}
		if err := r.cl.View(ctx, true, image.Rectangle{}); err != nil {
			t.Fatal(err)
		}
		fs := frames(drain(t, r.cl, 100*time.Millisecond))
		if len(fs) == 0 {
			t.Fatal("no frame once the viewport grew")
		}
		if got := r.cl.Screenshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Error("screen differs after the viewport grew to the screen")
		}
	})
}

// A caller that does not call Next holds the server back at its frame
// window, and frames flow again once it does.
func TestAcksHoldServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), client.Options{}, stream.Config{})
		window := int(r.cl.Capabilities().FrameWindow)
		if n := len(frames(collect(t, r.cl, 500*time.Millisecond))); n < 40 {
			t.Fatalf("%d frames in half a second of 10 ms motion", n)
		}
		time.Sleep(time.Second) // a hundred motion frames
		held := frames(drain(t, r.cl, time.Millisecond))
		if len(held) == 0 || len(held) > window {
			t.Errorf("%d frames waiting after a second without Next, want 1 to %d", len(held), window)
		}
		// Taking them acknowledges them, and the server goes on. The
		// held frames report the second as their decode time, which the
		// server's frame interval follows and then forgets frame by
		// frame. (Decoding takes no time on the bubble's clock, so the
		// server's average starts from the held frames alone.)
		if fs := frames(collect(t, r.cl, time.Second)); len(fs) < 2 {
			t.Errorf("%d frames in the second after the caller came back", len(fs))
		}
		collect(t, r.cl, 10*time.Second)
		if n := len(frames(collect(t, r.cl, time.Second))); n < 50 {
			t.Errorf("%d frames a second, want about 100 again", n)
		}
	})
}

// A slow caller neither blocks input nor deadlocks: the server sends it
// fewer frames, and every goroutine exits after Close.
func TestSlowConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := backendtest.NewScreen(200, 150, 5*time.Millisecond)
		r := newRig(t, b, client.Options{}, stream.Config{})
		ctx := context.Background()
		var wg sync.WaitGroup
		wg.Add(1)
		var blocked time.Duration
		go func() {
			defer wg.Done()
			for i := range 400 {
				start := time.Now()
				if _, err := r.cl.Key(ctx, i%2 == 0, 'x', 0); err != nil {
					t.Error(err)
					return
				}
				blocked = max(blocked, time.Since(start))
				time.Sleep(5 * time.Millisecond)
			}
		}()
		got := 0
		for range 20 {
			if _, ok := next(t, r.cl, time.Second).(client.Frame); ok {
				got++
			}
			time.Sleep(100 * time.Millisecond) // a slow renderer
		}
		wg.Wait()
		if blocked > 0 {
			t.Errorf("an input waited %v", blocked)
		}
		if n := len(b.Inputs()); n != 400 {
			t.Errorf("the backend got %d of 400 inputs", n)
		}
		if got == 0 {
			t.Error("no frames")
		}
		if err := r.cl.Close(); err != nil {
			t.Error(err)
		}
		ev := next(t, r.cl, time.Second)
		if e, ok := ev.(client.End); !ok || e.Code != protocol.CloseNormal {
			t.Errorf("after Close, %#v, want End normal", ev)
		}
		if _, err := r.cl.Next(ctx); !errors.Is(err, client.ErrClosed) {
			t.Errorf("Next after End: %v", err)
		}
		if _, err := r.cl.Key(ctx, true, 'y', 0); !errors.Is(err, client.ErrClosed) {
			t.Errorf("Key after Close: %v", err)
		}
		if err := <-r.served; err != nil {
			t.Errorf("Serve returned %v after the client closed", err)
		}
		r.served <- nil
	})
}

// Next returns when its context is done, and the session goes on.
func TestNextContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), client.Options{Hidden: true}, stream.Config{})
		drain(t, r.cl, 50*time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if ev, err := r.cl.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Next returned %#v, %v, want the context's error", ev, err)
		}
		if err := r.cl.View(context.Background(), true, image.Rectangle{}); err != nil {
			t.Fatal(err)
		}
		if _, ok := next(t, r.cl, time.Second).(client.Resize); !ok {
			t.Error("no frame after a cancelled Next")
		}
	})
}

// The session's end reaches the caller with its close code.
func TestCloseCodes(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(r *rig, b *backendtest.Screen)
		want protocol.CloseCode
	}{
		{"owner closed", func(r *rig, _ *backendtest.Screen) { r.s.Close() }, protocol.CloseSessionClosed},
		{"remote ended", func(_ *rig, b *backendtest.Screen) { b.End(nil) }, protocol.CloseRemoteEnded},
		{"backend failed", func(_ *rig, b *backendtest.Screen) { b.End(errors.New("lost")) }, protocol.CloseBackendFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := backendtest.NewScreen(200, 150, 0)
				r := newRig(t, b, client.Options{}, stream.Config{})
				drain(t, r.cl, 50*time.Millisecond)
				c.end(r, b)
				evs := drain(t, r.cl, time.Second)
				e, ok := evs[len(evs)-1].(client.End)
				if !ok || e.Code != c.want || e.Err != nil {
					t.Fatalf("last event %#v, want End %v", evs[len(evs)-1], c.want)
				}
				if _, err := r.cl.Next(context.Background()); !errors.Is(err, client.ErrClosed) {
					t.Errorf("Next after End: %v", err)
				}
			})
		})
	}
}

// A server that cannot serve the client refuses it with a close code,
// which New returns.
func TestRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := stream.Config{Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG}}}
		_, err := startRig(t, backendtest.NewScreen(200, 150, 0), client.Options{}, cfg)
		var ce *client.CloseError
		if !errors.As(err, &ce) || ce.Code != protocol.CloseUnsupported {
			t.Errorf("New returned %v, want unsupported", err)
		}
	})
}

// A read-only viewer's input is refused before it is sent.
func TestReadOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), client.Options{}, stream.Config{})
		v, err := r.s.Join(session.RoleReadOnly)
		if err != nil {
			t.Fatal(err)
		}
		p := newPipe(time.Millisecond)
		served := make(chan error, 1)
		go func() {
			served <- stream.Serve(context.Background(), p.server(), v, client.Options{}.Hello(), stream.Config{})
		}()
		ro, err := client.New(context.Background(), p.client(), client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			ro.Close()
			<-served
		}()
		if ro.Role() != protocol.RoleReadOnly {
			t.Fatalf("role %v", ro.Role())
		}
		if _, err := ro.Key(context.Background(), true, 'a', 0); !errors.Is(err, client.ErrReadOnly) {
			t.Errorf("Key from a read-only viewer: %v", err)
		}
		if err := ro.PromptReply(context.Background(), 1, nil); !errors.Is(err, client.ErrNotOwner) {
			t.Errorf("PromptReply from a read-only viewer: %v", err)
		}
		var joined bool
		for _, ev := range drain(t, r.cl, 50*time.Millisecond) {
			if j, ok := ev.(client.ViewerJoined); ok && j.ViewerID == ro.ViewerID() && j.Role == protocol.RoleReadOnly {
				joined = true
			}
		}
		if !joined {
			t.Error("the owner was not told of the read-only viewer")
		}
	})
}
