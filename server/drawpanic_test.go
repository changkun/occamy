// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"image"
	"runtime"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/protocol"
)

// lockPanics paints its screen, then locks the top row of tiles and,
// once release is closed, panics without unlocking it, as a decoder that
// indexes past its data while it holds the rows would without a
// deferred Unlock.
type lockPanics struct {
	locked  chan struct{}
	release chan struct{}
}

func (lockPanics) Class() backend.Class           { return backend.Graphical }
func (lockPanics) Input(backend.InputEvent) error { return nil }
func (lockPanics) Demand(backend.Demand)          {}

func (b lockPanics) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(256, 256)
	r := image.Rect(0, 0, 256, 256)
	img := sink.Lock(r)
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	sink.Unlock(r)
	img = sink.Lock(image.Rect(0, 0, 256, 64))
	close(b.locked)
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	past := len(img.Pix)
	img.Pix[past] = 0 // the decoder's bug
	return nil
}

// waiting reports whether a goroutine's stack holds both functions.
func waiting(fn, wait string) bool {
	buf := make([]byte, 1<<20)
	for g := range strings.SplitSeq(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
		if strings.Contains(g, fn) && strings.Contains(g, wait) {
			return true
		}
	}
	return false
}

// TestPanicWhileDrawing checks that a backend that panics while it holds
// rows of the framebuffer ends its session with internal-error for a
// viewer whose writer waits for those rows, and that the writer, its
// stream and its connection go: the session releases the rows once the
// backend has stopped. Before, the writer waited in ReadTile forever.
func TestPanicWhileDrawing(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	b := lockPanics{locked: make(chan struct{}), release: make(chan struct{})}
	ts.s.backends.register("fake", "", "fake", func(target, screen) (backend.Backend, error) { return b, nil })
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	<-b.locked
	graphical := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}}}
	c := ts.stream(t, "/api/v1/sessions/"+o.ID+"/stream", o.StreamToken, graphical)
	waitFor(t, "the writer to wait for the locked rows", func() bool {
		return waiting("(*Framebuffer).ReadTile", "(*RWMutex).RLock")
	})

	close(b.release)
	if cl := c.closed(t, protocol.CloseInternalError); cl.Reason != "internal error" {
		t.Errorf("close %q", cl.Reason)
	}
	waitFor(t, "the session to go", func() bool { return ts.s.session(o.ID) == nil })
	if !ts.s.streams.wait(5 * time.Second) {
		t.Error("the stream's handler still runs after the session ended")
	}
	waitFor(t, "no goroutine to wait in ReadTile", func() bool {
		return !waiting("(*Framebuffer).ReadTile", "(*RWMutex).RLock")
	})
}
