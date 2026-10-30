// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"image"
	"slices"
	"testing"
)

// putUncounted encodes a FramebufferUpdate of 65535 rectangles, as
// servers that end their updates with LastRect count them.
func putUncounted(rects ...[]byte) []byte {
	return slices.Concat(append([][]byte{{msgFramebufferUpdate, 0, 0xff, 0xff}}, rects...)...)
}

func TestLastRect(t *testing.T) {
	img := pattern(8, 8, 0)
	sink, err := decode(t, image.Pt(8, 8), defaultLimits,
		putUncounted(putRaw(wantPixelFormat, img, img.Rect), putLastRect()), putBell())
	if err != nil || sink.Bells() != 1 {
		t.Fatalf("decode = %v with %d bells, want the bell after LastRect", err, sink.Bells())
	}
	equalImages(t, sink.Pixels(), img)
	// One update's LastRect does not end the next, which counts its
	// rectangles.
	next := pattern(8, 8, 2)
	sink, err = decode(t, image.Pt(8, 8), defaultLimits,
		putUncounted(putLastRect()), putUpdate(putRaw(wantPixelFormat, img, img.Rect), putRaw(wantPixelFormat, next, img.Rect)), putBell())
	if err != nil || sink.Bells() != 1 {
		t.Fatalf("decode = %v with %d bells, want the bell after the second update", err, sink.Bells())
	}
	equalImages(t, sink.Pixels(), next)

	// In a session, LastRect ends the update, which a request follows.
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	req := expect[requestMsg](t, s.c)
	img = pattern(64, 48, 1)
	s.c.send(t, putUncounted(putRaw(s.c.pf, img, req.r), putLastRect()))
	if req := expect[requestMsg](t, s.c); !req.incremental {
		t.Errorf("request %+v, want changes", req)
	}
	equalImages(t, s.sink.Pixels(), img)
}
