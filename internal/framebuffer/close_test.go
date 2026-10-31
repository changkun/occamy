// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package framebuffer_test

import (
	"image"
	"image/color"
	"runtime"
	"strings"
	"sync"
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

// paint writes every pixel of r in img, which Lock(r) returned, and no
// other: img's Pix runs on to the end of the screen.
func paint(img *image.RGBA, r image.Rectangle, v byte) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[img.PixOffset(r.Min.X, y):][:4*r.Dx()]
		for i := range row {
			row[i] = v
		}
	}
}

// TestCloseWhileDrawing checks that a Lock held when Close comes, by a
// backend's goroutine that has not stopped and goes on drawing, is
// released once, and that nothing reads or writes its pixels meanwhile:
// a writer that waited for its rows reads nothing, and a CopyRect onto
// them copies nothing. With -race, it fails if Close lets the writer in
// before it marks the framebuffer closed, or if CopyRect draws after
// Close (from the review of #117).
func TestCloseWhileDrawing(t *testing.T) {
	f := framebuffer.New(64)
	f.Resize(256, 256)
	r := image.Rect(0, 64, 256, 128)
	locked, draw := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		img := f.Lock(r)
		close(locked)
		<-draw
		// Nothing orders this drawing with Close and what follows it.
		for v := range byte(8) {
			paint(img, r, v)
		}
		f.Unlock(r)
	})
	<-locked
	read := make(chan image.Rectangle, 1)
	go func() {
		got, _ := f.ReadTile(5, make([]byte, 4*64*64))
		read <- got
	}()
	waitIn(t, "(*Framebuffer).ReadTile", "(*RWMutex).RLock")
	close(draw)
	f.Close()
	if got := <-read; !got.Empty() {
		t.Errorf("ReadTile read %v that a Lock still draws", got)
	}
	clock := f.Clock()
	f.CopyRect(image.Rect(0, 64, 64, 128), image.Pt(64, 128))
	if f.Clock() != clock {
		t.Error("CopyRect after Close changed the screen")
	}
	wg.Wait()
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

// TestCloseStraggler checks Close against a goroutine that locks, draws,
// resizes and unlocks with no handoff to the goroutine that closes,
// which learns of the Lock only from the framebuffer's record of it: a
// backend's goroutine that has not stopped. The straggler's Resize waits
// for Close to release its Lock's hold on the screen, and its Unlock
// after Close does nothing. With -race, it fails if Lock reads the
// screen after it has recorded the Lock, which Close may release and a
// Resize replace (from the review of #117).
func TestCloseStraggler(t *testing.T) {
	for range 20 {
		f := framebuffer.New(64)
		f.Resize(256, 256)
		r := image.Rect(0, 0, 256, 64)
		var wg sync.WaitGroup
		wg.Go(func() {
			img := f.Lock(r)
			paint(img, r, 0xff)
			f.Resize(128, 128)
			paint(img, r, 0x80)
			f.Unlock(r)
		})
		for framebuffer.Held(f) == 0 {
			runtime.Gosched()
		}
		f.Close()
		f.Resize(256, 256)
		wg.Wait()
		if n := framebuffer.Held(f); n != 0 {
			t.Fatalf("%d Locks held after Close", n)
		}
	}
}

// TestUnlockWithoutLock checks that Unlock of a rectangle that no Lock
// holds panics, which a session recovers from, rather than unlocking
// rows that are not locked, a fatal error, or doing nothing, which would
// leave the Lock meant held and the next Lock of its rows waiting
// forever. Once the framebuffer is closed, it does nothing.
func TestUnlockWithoutLock(t *testing.T) {
	f := framebuffer.New(64)
	f.Resize(256, 256)
	unlock := func(r image.Rectangle) (v any) {
		defer func() { v = recover() }()
		f.Unlock(r)
		return nil
	}
	r := image.Rect(0, 0, 64, 64)
	if v := unlock(r); v == nil {
		t.Error("Unlock without a Lock did not panic")
	}
	f.Lock(r)
	if v := unlock(image.Rect(0, 0, 32, 32)); v == nil {
		t.Error("Unlock of another rectangle than the Lock's did not panic")
	}
	if v := unlock(r); v != nil {
		t.Errorf("Unlock of the Lock panicked: %v", v)
	}
	done := make(chan struct{})
	go func() {
		f.Unlock(f.Lock(r).Rect)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a Lock waits after the Unlocks")
	}
	f.Close()
	if v := unlock(r); v != nil {
		t.Errorf("Unlock after Close panicked: %v", v)
	}
}
