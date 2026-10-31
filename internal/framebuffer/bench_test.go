// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package framebuffer_test

import (
	"image"
	"testing"

	"changkun.de/x/occamy/internal/framebuffer"
)

// BenchmarkLockUnlock locks and unlocks a band of 1024×16 pixels, a row
// of hextile tiles, and draws nothing: the cost a decoder pays per strip
// on top of its pixels.
func BenchmarkLockUnlock(b *testing.B) {
	f := framebuffer.New(64)
	f.Resize(1024, 768)
	r := image.Rect(0, 64, 1024, 80)
	for b.Loop() {
		f.Lock(r)
		f.Unlock(r)
	}
}

// BenchmarkReadTile reads a tile of 64×64 pixels, as a viewer's writer
// does for each tile it sends.
func BenchmarkReadTile(b *testing.B) {
	f := framebuffer.New(64)
	f.Resize(1024, 768)
	dst := make([]byte, 4*64*64)
	for b.Loop() {
		f.ReadTile(17, dst)
	}
}
