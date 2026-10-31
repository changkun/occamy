// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package framebuffer_test

import (
	"image"
	"image/color"
	"runtime"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/framebuffer"
)

// waitIn waits until a goroutine's stack holds both functions, as one
// waiting on a lock inside a framebuffer method does.
func waitIn(t *testing.T, fn, wait string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		for g := range strings.SplitSeq(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			if strings.Contains(g, fn) && strings.Contains(g, wait) {
				return
			}
		}
	}
	t.Fatalf("no goroutine waits in %s", fn)
}

// TestCloseReleasesLocks checks that Close releases a Lock that is never
// unlocked, as a backend that panicked while it drew leaves one: a writer
// waiting in ReadTile for its rows returns with nothing read, a Resize
// waiting behind it goes ahead, and the late Unlock does nothing, where
// unlocking the rows a second time would be a fatal error.
func TestCloseReleasesLocks(t *testing.T) {
	f := framebuffer.New(64)
	f.Resize(256, 256)
	fill(f, image.Rect(0, 0, 256, 256), color.RGBA{R: 0x80, A: 0xff})
	held := image.Rect(0, 0, 256, 64)
	f.Lock(held) // and never unlocked before Close

	read := make(chan image.Rectangle, 1)
	go func() {
		got, _ := f.ReadTile(1, make([]byte, 4*64*64))
		read <- got
	}()
	waitIn(t, "(*Framebuffer).ReadTile", "(*RWMutex).RLock")
	resized := make(chan struct{})
	go func() {
		f.Resize(128, 128)
		close(resized)
	}()
	waitIn(t, "(*Framebuffer).Resize", "(*RWMutex).Lock")
	changed := f.Changed()

	f.Close()
	select {
	case got := <-read:
		if !got.Empty() {
			t.Errorf("ReadTile after Close read %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadTile still waits for the rows after Close")
	}
	select {
	case <-resized:
	case <-time.After(5 * time.Second):
		t.Fatal("Resize still waits for the Lock after Close")
	}
	if !closed(changed) {
		t.Error("Close did not wake the writers")
	}
	f.Unlock(held) // late, as a backend still drawing would: nothing

	// Closed, it reads nothing and draws nowhere, and does not wait.
	if got, v := f.ReadTile(0, make([]byte, 4*64*64)); !got.Empty() || v != 0 {
		t.Errorf("ReadTile of a closed framebuffer read %v at %d", got, v)
	}
	clock := f.Clock()
	r := image.Rect(0, 0, 64, 64)
	img := f.Lock(r)
	img.Pix[0] = 1
	f.Unlock(r)
	if f.Clock() != clock {
		t.Error("a Lock after Close changed the screen")
	}
	f.Close() // twice is fine
}

// TestCloseWhileDrawing checks that a Lock held when Close comes, by a
// backend's goroutine that has not stopped, is released once: writers
// read nothing, so they do not race with its drawing, and its Unlock
// after Close does nothing.
func TestCloseWhileDrawing(t *testing.T) {
	f := framebuffer.New(64)
	f.Resize(256, 256)
	r := image.Rect(0, 64, 256, 128)
	img := f.Lock(r)
	f.Close()
	if got, _ := f.ReadTile(5, make([]byte, 4*64*64)); !got.Empty() {
		t.Errorf("ReadTile read %v that a Lock still draws", got)
	}
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	f.Unlock(r)
	// The rows are free: a Lock of them returns at once.
	done := make(chan struct{})
	go func() {
		f.Unlock(f.Lock(r).Rect)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Lock after Close waits for rows")
	}
}
