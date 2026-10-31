// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/protocol"
)

// An encoding is made once for its tile's version and asked for by
// everyone else: older pixels are told to encode alone, newer ones
// replace it, and one that could not be made, or whose maker panicked,
// is made again by the next to ask.
func TestEncodeCacheEntries(t *testing.T) {
	c := newEncodeCache(1 << 20)
	k := tileKey{tile: 3}
	made := func(e *entry, payload string) {
		t.Helper()
		c.make(e, func(e *entry) bool {
			e.payload, e.codec = []byte(payload), protocol.CodecDeflate
			return true
		})
	}

	e, mine := c.tile(k, 5)
	if e == nil || !mine {
		t.Fatal("the first to ask for a tile does not make it")
	}
	w, mine := c.tile(k, 5)
	if w != e || mine {
		t.Fatal("the second to ask for a tile being made does not wait for it")
	}
	made(e, "v5")
	<-w.done
	if !w.ok || string(w.payload) != "v5" {
		t.Fatalf("the waiter got %q, ok %v", w.payload, w.ok)
	}
	if e, _ := c.tile(k, 4); e != nil {
		t.Error("older pixels got the encoding of newer ones")
	}
	if e, _ := c.tile(tileKey{tile: 3, moving: true}, 5); e == w {
		t.Error("a moving tile got the encoding of a still one")
	}
	if e, _ := c.tile(tileKey{p: profile{lossless: protocol.CodecPNG}, tile: 3}, 5); e == w {
		t.Error("another profile got the encoding of this one")
	}

	n, mine := c.tile(k, 6)
	if n == nil || n == w || !mine {
		t.Fatal("newer pixels did not replace the encoding")
	}
	c.make(n, func(*entry) bool { return false })
	if !n.isDone() || n.ok {
		t.Fatal("an encoding that could not be made is not published as failed")
	}
	again, mine := c.tile(k, 6)
	if again == n || !mine {
		t.Fatal("an encoding that could not be made is not made again")
	}
	func() {
		defer func() { _ = recover() }()
		c.make(again, func(*entry) bool { panic("encoder") })
	}()
	if !again.isDone() || again.ok {
		t.Fatal("an encoding whose maker panicked is not published as failed")
	}
	if e, mine := c.tile(k, 6); e == again || !mine {
		t.Fatal("an encoding whose maker panicked is not made again")
	}
}

func (e *entry) isDone() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// A run's encoding is known by the versions of all its tiles: any older
// is told to encode alone, any newer replaces it.
func TestEncodeCacheRuns(t *testing.T) {
	c := newEncodeCache(1 << 20)
	k := runKey{tile: 4, n: 3, still: true, q: 90, lo: 30}
	e, mine := c.run(k, []uint64{7, 8, 9})
	if !mine {
		t.Fatal("the first to ask for a run does not make it")
	}
	c.make(e, func(e *entry) bool { e.payload, e.codec = []byte("run"), protocol.CodecWebP; return true })
	if w, mine := c.run(k, []uint64{7, 8, 9}); w != e || mine {
		t.Error("the same versions did not find the run")
	}
	if w, _ := c.run(k, []uint64{7, 8, 6}); w != nil {
		t.Error("a run with an older tile got the encoding")
	}
	if w, _ := c.run(runKey{tile: 4, n: 3, still: true, q: 80, lo: 30}, []uint64{7, 8, 9}); w == e {
		t.Error("another quality got the encoding")
	}
	if w, mine := c.run(k, []uint64{7, 10, 9}); w == e || !mine {
		t.Error("a run with a newer tile did not replace the encoding")
	}
	if s := c.stats(); s.entries != 0 || s.bytes != 0 {
		t.Errorf("the replaced run still counts: %+v", s)
	}
}

// The cache holds no more than its budget, the oldest encodings going
// first; one replaced while it was made never counts.
func TestEncodeCacheBudget(t *testing.T) {
	const size = 1000
	c := newEncodeCache(3 * (size + entryBytes))
	make1 := func(tile int, v uint64) *entry {
		t.Helper()
		e, mine := c.tile(tileKey{tile: tile}, v)
		if !mine {
			t.Fatalf("tile %d at %d is not made", tile, v)
		}
		c.make(e, func(e *entry) bool { e.payload = make([]byte, size); return true })
		return e
	}
	for tile := range 5 {
		make1(tile, 1)
	}
	if s := c.stats(); s.bytes != 3*(size+entryBytes) || s.entries != 3 {
		t.Errorf("five encodings in a budget of three hold %d bytes in %d entries", s.bytes, s.entries)
	}
	for tile := range 5 {
		_, mine := c.tile(tileKey{tile: tile}, 1)
		if want := tile < 2; mine != want {
			t.Errorf("tile %d is made again: %v, want %v", tile, mine, want)
		}
	}

	c = newEncodeCache(1 << 20)
	old, _ := c.tile(tileKey{tile: 1}, 1)
	newer, _ := c.tile(tileKey{tile: 1}, 2)
	c.make(old, func(e *entry) bool { e.payload = make([]byte, size); return true })
	if s := c.stats(); s.bytes != 0 || s.entries != 0 {
		t.Errorf("an encoding replaced while it was made counts: %+v", s)
	}
	c.make(newer, func(e *entry) bool { e.payload = make([]byte, size); return true })
	if s := c.stats(); s.bytes != size+entryBytes || s.entries != 1 {
		t.Errorf("one encoding holds %d bytes in %d entries", s.bytes, s.entries)
	}
}

// The viewers of a session share its encode cache and workers, which
// the last to leave stops; viewers of another session share another.
func TestShareRelease(t *testing.T) {
	a, b := newDirect(t, false), newDirect(t, false)
	fa, fb := a.w.fb, b.w.fb
	s1 := share(fa, 2, 1<<20, nil)
	s2 := share(fa, 2, 1<<20, nil)
	if s1 != s2 {
		t.Fatal("two viewers of a session do not share")
	}
	if s3 := share(fb, 2, 1<<20, nil); s3 == s1 {
		t.Fatal("viewers of two sessions share")
	} else {
		s3.release(fb)
	}
	s1.release(fa)
	if !s2.submit(func(*worker) {}) {
		t.Fatal("the workers stopped while a viewer was left")
	}
	s2.release(fa)
	sharedMu.Lock()
	_, left := sharedFor[fa]
	sharedMu.Unlock()
	if left {
		t.Error("the session's share outlives its viewers")
	}
	if s4 := share(fa, 2, 1<<20, nil); s4 == s1 {
		t.Error("a viewer after the last left found the old share")
	} else {
		s4.release(fa)
	}
}

// A job still running when the last viewer leaves may yet ask for
// another worker, as it begins an encoding: it gets none, and nothing is
// sent on the closed queue.
func TestShareReleaseRecruit(t *testing.T) {
	fb := newDirect(t, false).w.fb
	sh := share(fb, 1, 1<<20, nil)
	f := &prefetch{sh: sh, most: 2, rows: []int{0, 1, 2}}
	f.busy.Store(1)
	running, recruit := make(chan struct{}), make(chan struct{})
	recovered := make(chan any, 1)
	if !sh.submit(func(*worker) {
		close(running)
		<-recruit
		defer func() { recovered <- recover() }()
		f.recruit()
	}) {
		t.Fatal("no worker took the job")
	}
	<-running
	released := make(chan struct{})
	go func() {
		sh.release(fb)
		close(released)
	}()
	// release closes the queue, then waits for the job.
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case _, ok := <-sh.jobs:
			if ok {
				t.Fatal("a job in the queue")
			}
		default:
			if time.Now().After(deadline) {
				t.Fatal("the last viewer left, and the queue is open")
			}
			runtime.Gosched()
			continue
		}
		break
	}
	close(recruit)
	if r := <-recovered; r != nil {
		t.Fatalf("a job asked for a worker once the last viewer left: %v", r)
	}
	<-released
	if n := f.busy.Load(); n != 1 {
		t.Errorf("%d workers on the frame, want the one it had", n)
	}
}

// Workers that ask for more while the last viewer leaves get none once
// it has, and send nothing on the closed queue.
func TestShareReleaseRace(t *testing.T) {
	fb := newDirect(t, false).w.fb
	for range 200 {
		sh := share(fb, 2, 1<<20, nil)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
						sh.submit(func(*worker) {})
					}
				}
			})
		}
		sh.release(fb)
		close(stop)
		wg.Wait()
	}
}

// A job that panics ends neither occamyd nor its worker. The worker logs
// the panic and drops its encoders, which may be broken; the encoding
// the job was making is published failed, and the next to ask makes it.
func TestShareWorkerPanics(t *testing.T) {
	var logged bytes.Buffer
	fb := newDirect(t, false).w.fb
	sh := share(fb, 1, 1<<20, slog.New(slog.NewTextHandler(&logged, nil)))
	p := profile{protocol.CodecDeflate, protocol.CodecWebP}
	k := tileKey{p, 3, false}
	e, mine := sh.cache.tile(k, 1)
	if !mine {
		t.Fatal("a new tile's encoding is not the asker's to make")
	}
	encs := make(chan *encoder.Encoder, 2)
	done := make(chan struct{})
	for i, job := range []func(*worker){
		func(wk *worker) {
			encs <- wk.encoder(p)
			sh.cache.make(e, func(*entry) bool { panic("an encoder broke") })
		},
		func(wk *worker) {
			encs <- wk.encoder(p)
			close(done)
		},
	} {
		if !sh.submit(job) {
			t.Fatalf("no worker took job %d", i)
		}
	}
	<-done
	sh.release(fb)
	if a, b := <-encs, <-encs; a == b {
		t.Error("the worker kept the encoders of a job that panicked")
	}
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the encoding whose maker panicked is never published")
	}
	if e.ok {
		t.Error("the encoding whose maker panicked is published made")
	}
	if again, mine := sh.cache.tile(k, 1); !mine || again == e {
		t.Error("the next to ask does not make the encoding again")
	}
	if !strings.Contains(logged.String(), "an encoder broke") {
		t.Errorf("the panic is not logged: %q", logged.String())
	}
}

// A panic after the cache hands an encoding out and before it is made,
// as a worker asks for another, gathers a run's pixels, or as a writer
// hands its rows over, publishes it failed: the next to ask is handed it
// to make, rather than waiting for it forever.
func TestShareHandedOutPanics(t *testing.T) {
	p := profile{protocol.CodecDeflate, protocol.CodecWebP}
	r := image.Rect(0, 0, 64, 64)
	panics := func(t *testing.T, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatal("nothing panicked")
			}
		}()
		f()
	}
	// writer is a writer whose handing over of its rows panics, at a
	// tile past the screen.
	writer := func(t *testing.T) (*writer, *shared) {
		d := newDirect(t, false)
		sh := sharedWriter(t, d)
		sh.jobs = make(chan func(*worker), 64)
		w := d.w
		w.pending = []int{0, len(w.sent) + 16}
		w.runVersion = append(w.runVersion[:0], 1)
		return w, sh
	}
	t.Run("a worker's tile, asking for another worker", func(t *testing.T) {
		c := newEncodeCache(1 << 20)
		// No share to ask: asking panics.
		f := &prefetch{cache: c, p: p, rows: []int{0, 1, 2}, most: 2}
		f.busy.Store(1)
		wk := newWorker()
		wk.tile(0, 64)
		panics(t, func() { f.tile(wk, wk.encoder(p), 3, 1, 0, r, false, cacheKey{}, false, 0) })
		if _, mine := c.tile(tileKey{p, 3, false}, 1); !mine {
			t.Error("the tile's encoding is still being made")
		}
	})
	t.Run("a worker's run, asking for another worker", func(t *testing.T) {
		c := newEncodeCache(1 << 20)
		f := &prefetch{cache: c, p: p, q: 70, lo: 30, g: grid{tile: 64}, rows: []int{0, 1, 2}, most: 2}
		f.busy.Store(1)
		wk := newWorker()
		wk.tile(0, 64)
		wk.tile(1, 64)
		wk.versions = append(wk.versions[:0], 1, 1)
		wk.rects[0], wk.rects[1] = r, r.Add(image.Pt(64, 0))
		panics(t, func() { f.run(wk, wk.encoder(p), 3, 2, false) })
		still, q, lo := runMode(f.q, f.lo, false, false)
		if _, mine := c.run(runKey{p, 3, 2, still, q, lo}, []uint64{1, 1}); !mine {
			t.Error("the run's encoding is still being made")
		}
	})
	t.Run("a worker's run, gathering its pixels", func(t *testing.T) {
		c := newEncodeCache(1 << 20)
		f := &prefetch{cache: c, p: p, q: 70, lo: 30, g: grid{tile: 64}}
		wk := newWorker()
		// Two tiles' places, and no pixels: gathering them panics.
		wk.versions = append(wk.versions[:0], 1, 1)
		wk.rects[0], wk.rects[1] = r, r.Add(image.Pt(64, 0))
		panics(t, func() { f.run(wk, wk.encoder(p), 3, 2, false) })
		still, q, lo := runMode(f.q, f.lo, false, false)
		if _, mine := c.run(runKey{p, 3, 2, still, q, lo}, []uint64{1, 1}); !mine {
			t.Error("the run's encoding is still being made")
		}
	})
	t.Run("a writer's tile, handing its rows over", func(t *testing.T) {
		w, sh := writer(t)
		panics(t, func() { w.encodeTile(nil, 0, 0, 1, r, false) })
		if _, mine := sh.cache.tile(tileKey{w.prof, 0, false}, 1); !mine {
			t.Error("the tile's encoding is still being made")
		}
	})
	t.Run("a writer's run, handing its rows over", func(t *testing.T) {
		w, sh := writer(t)
		pix := make([]byte, 4*64*64)
		panics(t, func() { w.encodeRun(0, 1, pix, r, false, 70, 0) })
		if _, mine := sh.cache.run(runKey{w.prof, 0, 1, false, 70, 0}, []uint64{1}); !mine {
			t.Error("the run's encoding is still being made")
		}
	})
}

// Only internal/encoder's encoders share, by the codecs they send in.
func TestProfile(t *testing.T) {
	if _, ok := profileOf(&markEncoder{}); ok {
		t.Error("another encoder shares")
	}
	if _, ok := profileOf(nil); ok {
		t.Error("no encoder shares")
	}
	webp, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}))
	jpeg, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecJPEG, protocol.CodecDeflate}))
	again, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecDeflate, protocol.CodecWebP}))
	if webp == jpeg || webp != again {
		t.Errorf("profiles: webp %v, jpeg %v, webp again %v", webp, jpeg, again)
	}
}

// The workers skip the tiles that the client's tile cache holds, as the
// writer sends them cached: they look tiles up under the mirror's seed.
// The one worker that reads them asks for no other.
func TestPrefetchSkipsCached(t *testing.T) {
	d := newDirect(t, false)
	d.w.cache = newTileCache(16)
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y)%4, image.Pt(64*x, 64*y)))
		}
	}
	d.show(s.shot())
	d.frame()

	// The same tiles, elsewhere: every one is in a slot.
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y+1)%4, image.Pt(64*x, 64*y)))
		}
	}
	d.show(s.shot())
	sh := &shared{cache: newEncodeCache(DefaultEncodeCacheBytes), jobs: make(chan func(*worker), 64), workers: 4}
	p, _ := profileOf(d.w.enc)
	d.w.sh, d.w.prof = sh, p
	if _, ok := d.w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	if f := d.w.prefetch(d.w.order); f == nil {
		t.Fatal("no rows for the workers")
	}
	wk := newWorker()
	jobs := 0
	for len(sh.jobs) > 0 {
		(<-sh.jobs)(wk)
		jobs++
	}
	if s := sh.cache.stats(); jobs != 1 || s.misses != 0 {
		t.Errorf("%d workers encoded %d tiles or runs of tiles the client has", jobs, s.misses)
	}
}

// sharedWriter is a writer of d that shares a cache with no workers.
func sharedWriter(t *testing.T, d *direct) *shared {
	t.Helper()
	sh := &shared{cache: newEncodeCache(DefaultEncodeCacheBytes)}
	p, ok := profileOf(d.w.enc)
	if !ok {
		t.Fatal("the encoder is not internal/encoder's")
	}
	d.w.sh, d.w.prof = sh, p
	return sh
}

// A tile's encoding while it moves is not its encoding still: a tile of
// 17 to 256 colours goes lossy while it moves, and exact otherwise.
func TestShareMoving(t *testing.T) {
	d := newDirect(t, false)
	sharedWriter(t, d)
	w := d.w
	r := image.Rect(0, 0, 64, 64)
	pix := w.tile(0)
	for i := range 64 * 64 {
		pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = uint8(i%100), uint8(2*(i%100)), 0x40, 0xff // #nosec G115 -- small
	}
	_, _, moving := w.encodeTile(nil, 0, 0, 1, r, true)
	_, codec, still := w.encodeTile(nil, 0, 0, 1, r, false)
	if !moving || still || codec != protocol.CodecDeflate {
		t.Errorf("a tile of 100 colours: lossy %v moving, lossy %v still in %v", moving, still, codec)
	}
}

// A run's encoding at one quality, or searched from one lowest, is not
// one at another.
func TestShareRunQuality(t *testing.T) {
	d := newDirect(t, false)
	sharedWriter(t, d)
	w := d.w
	photo := corpusFrame(t, "photo")
	r := image.Rect(0, 0, 64, 64)
	pix := pixels(photo, r.Add(image.Pt(256, 192)))
	w.runVersion = append(w.runVersion[:0], 1)
	encode := func(still bool, q, lo int) ([]byte, int) {
		_, got, _ := w.encodeRun(0, 1, pix, r, still, q, lo)
		return slices.Clone(w.payload), got
	}
	low, _ := encode(false, 30, 0)
	high, _ := encode(false, 90, 0)
	if bytes.Equal(low, high) {
		t.Error("a run at 90 got the encoding at 30")
	}
	if again, _ := encode(false, 30, 0); !bytes.Equal(again, low) {
		t.Error("the encoding at 30 changed in the cache as the writer encoded at 90")
	}
	_, from30 := encode(true, 90, 30)
	_, from85 := encode(true, 90, 85)
	if from30 >= 85 || from85 < 85 {
		t.Errorf("Still from 30 chose %d, from 85 chose %d", from30, from85)
	}
}

// The workers encode a frame's rows but the writer's first as the
// writer then sends them: each tile, and each run of lossy tiles between
// tiles that are clean, exact or in the client's tile cache, so that the
// writer finds every encoding they made. A run also ends where motion
// starts or stops, at the client's widest texture, and at a tile a copy
// completed, which neither encodes.
func TestPrefetchMatchesWriter(t *testing.T) {
	photo := corpusFrame(t, "photo")
	first := image.NewRGBA(photo.Rect)
	copy(first.Pix, photo.Pix)
	// The last row's patterns go to the tile cache.
	for x := range 16 {
		r := image.Rect(64*x, 64*11, 64*x+64, 64*12)
		draw.Draw(first, r, pattern(x+11, r.Min), r.Min, draw.Src)
	}
	// Every other run of tiles changes, some of them to a pattern, which
	// from the eighth row on the tile cache holds.
	next := image.NewRGBA(photo.Rect)
	draw.Draw(next, next.Rect, photo, image.Pt(0, 32), draw.Src)
	img := image.NewRGBA(photo.Rect)
	copy(img.Pix, first.Pix)
	for y := range 12 {
		for x := range 16 {
			r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64)
			switch {
			case (x/3+y)%2 == 1:
			case x%5 == 4:
				draw.Draw(img, r, pattern(x+y, r.Min), r.Min, draw.Src)
			default:
				draw.Draw(img, r, next, r.Min, draw.Src)
			}
		}
	}
	for _, c := range []struct {
		name string
		// set changes the writer once it has the frame's tiles.
		set func(t *testing.T, w *writer)
	}{
		{"tiles, runs and cached tiles", func(*testing.T, *writer) {}},
		{"motion inside runs", func(t *testing.T, w *writer) {
			// The changed tiles of each even row come in runs of three,
			// from columns 0, 6 and 12; one tile of each run moves.
			for y := 0; y < 12; y += 2 {
				for _, x := range []int{1, 7, 12} {
					w.heat[16*y+x] = motionHeat
				}
			}
			if !w.moving(16*2+7, false) {
				t.Fatal("no tile moves")
			}
		}},
		{"a texture two tiles wide", func(_ *testing.T, w *writer) {
			w.limits.MaxTextureSize = 2 * 64
		}},
		{"tiles a copy completed", func(_ *testing.T, w *writer) {
			for _, u := range w.order {
				if x := u % 16; u/16%3 == 1 && x >= 1 && x < 7 {
					w.sent[u] = w.versions[u]
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDirect(t, false)
			d.w.cache = newTileCache(DefaultCacheSlots)
			d.show(first)
			d.frame()
			d.show(img)
			sh := sharedWriter(t, d)
			sh.jobs = make(chan func(*worker), 64)
			w := d.w
			if _, ok := w.collect(image.Rectangle{}); !ok {
				t.Fatal("resized")
			}
			c.set(t, w)
			if f := w.prefetch(w.order); f == nil {
				t.Fatal("no rows for the workers")
			}
			wk := newWorker()
			for len(sh.jobs) > 0 {
				(<-sh.jobs)(wk)
			}
			for k := range sh.cache.tiles {
				if k.tile/16 == w.order[0]/16 {
					t.Fatalf("a worker encoded tile %d, of the writer's row", k.tile)
				}
			}
			// A worker asks again for an exact tile that ended a run, as
			// the first of the next message.
			before := sh.cache.stats()
			made := before.misses
			sh.jobs = nil // the writer encodes the rest alone
			if ok, _ := w.p.StartFrame(time.Now()); !ok {
				t.Fatal("the pacer holds the frame")
			}
			if err := w.sendFrame(d.ctx, w.order, false); err != nil {
				t.Fatal(err)
			}
			if s := sh.cache.stats(); made == 0 || s.hits-before.hits != made {
				t.Errorf("the workers made %d encodings, and the writer found %d", made, s.hits-before.hits)
			}
		})
	}
}

// hooked is a connection that calls hook after each write, with the
// number of writes before it.
type hooked struct {
	Conn
	writes int
	hook   func(n int)
}

func (c *hooked) WriteMessage(ctx context.Context, p []byte) error {
	err := c.Conn.WriteMessage(ctx, p)
	c.hook(c.writes)
	c.writes++
	return err
}

// A frame's workers leave its rows once it is sent, and once it turns to
// an input's effect, which goes first; a frame of an input's effect wakes
// none, as its tiles are few and wanted at once.
func TestFetchEndsWithFrame(t *testing.T) {
	desktop, photo := corpusFrame(t, "desktop"), corpusFrame(t, "photo")
	start := func(t *testing.T) (*direct, *shared) {
		d := newDirect(t, false)
		d.show(desktop)
		d.frame()
		sh := sharedWriter(t, d)
		sh.jobs, sh.workers = make(chan func(*worker), 64), 2
		d.show(photo)
		if _, ok := d.w.collect(image.Rectangle{}); !ok {
			t.Fatal("resized")
		}
		if ok, _ := d.w.p.StartFrame(time.Now()); !ok {
			t.Fatal("the pacer holds the frame")
		}
		return d, sh
	}
	// lookups runs the queued jobs and reports the lookups they made.
	lookups := func(sh *shared) int {
		before := sh.cache.stats()
		wk := newWorker()
		for len(sh.jobs) > 0 {
			(<-sh.jobs)(wk)
		}
		s := sh.cache.stats()
		return s.hits + s.misses + s.stale - before.hits - before.misses - before.stale
	}
	t.Run("sent", func(t *testing.T) {
		d, sh := start(t)
		if err := d.w.sendFrame(d.ctx, d.w.order, false); err != nil {
			t.Fatal(err)
		}
		if n := len(sh.jobs); n != 1 {
			t.Fatalf("%d jobs for the workers, want 1", n)
		}
		if d.w.fetch != nil {
			t.Error("the writer holds the work of a frame it sent")
		}
		if n := lookups(sh); n != 0 {
			t.Errorf("the workers looked up %d encodings of a frame already sent", n)
		}
	})
	t.Run("of an input's effect", func(t *testing.T) {
		d, sh := start(t)
		if err := d.w.sendFrame(d.ctx, d.w.order, true); err != nil {
			t.Fatal(err)
		}
		if n := len(sh.jobs); n != 0 {
			t.Errorf("a frame of an input's effect: %d jobs for the workers", n)
		}
	})
	t.Run("turned to an input's effect", func(t *testing.T) {
		d, sh := start(t)
		w := d.w
		// An input whose effect is any change from now on.
		w.in = &pending{first: 1, seq: 1, clock: w.fb.Clock(), at: time.Now()}
		r := image.Rect(0, 704, 64, 768)
		during := -1
		w.c = &hooked{Conn: w.c, hook: func(n int) {
			switch n {
			case 0:
				// The input's effect shows as the frame's first message
				// goes.
				sink := d.b.Sink()
				draw.Draw(sink.Lock(r), r, image.White, image.Point{}, draw.Src)
				sink.Unlock(r)
			case 1:
				if len(sh.jobs) != 1 {
					t.Errorf("%d jobs for the workers, want 1", len(sh.jobs))
				}
				during = lookups(sh)
			}
		}}
		d.c.rects = nil
		if err := w.sendFrame(d.ctx, w.order, false); err != nil {
			t.Fatal(err)
		}
		switch {
		case len(d.c.rects) < 2 || d.c.rects[1] != r:
			t.Fatalf("the frame's second tile message is not the input's effect: %v", d.c.rects[:min(2, len(d.c.rects))])
		case during > 0:
			t.Errorf("the workers looked up %d encodings of rows the frame left for an input's effect", during)
		}
	})
}

// Once its frame is sent, the workers leave the rest of its rows.
func TestPrefetchStops(t *testing.T) {
	d := newDirect(t, false)
	d.show(corpusFrame(t, "desktop"))
	d.frame()
	d.show(corpusFrame(t, "photo"))
	sh := sharedWriter(t, d)
	sh.jobs = make(chan func(*worker), 64)
	w := d.w
	if _, ok := w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	f := w.prefetch(w.order)
	if w.fetch = f; f == nil {
		t.Fatal("no rows for the workers")
	}
	w.stopFetch()
	wk := newWorker()
	jobs := 0
	for len(sh.jobs) > 0 {
		(<-sh.jobs)(wk)
		jobs++
	}
	// And a worker on a row when the frame is sent leaves the rest of it.
	f.row(wk, wk.encoder(f.p), f.rows[0], f.rows[1])
	if s := sh.cache.stats(); jobs != 1 || s.misses != 0 {
		t.Errorf("%d workers encoded %d tiles or runs of a frame already sent", jobs, s.misses)
	}
}

// A frame's rows go to one worker, which asks for one more as it begins
// an encoding, of a tile or of a run, while rows are left for it, up to
// the session's workers.
func TestPrefetchRecruits(t *testing.T) {
	desktop, photo := corpusFrame(t, "desktop"), corpusFrame(t, "photo")
	patterns := image.NewRGBA(photo.Rect)
	for y := range 12 {
		for x := range 16 {
			r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64)
			draw.Draw(patterns, r, pattern(x+16*y, r.Min), r.Min, draw.Src)
		}
	}
	twoRows := image.NewRGBA(photo.Rect)
	copy(twoRows.Pix, desktop.Pix)
	draw.Draw(twoRows, image.Rect(0, 0, 1024, 128), photo, image.Point{}, draw.Src)
	for _, c := range []struct {
		name    string
		next    *image.RGBA
		workers int
		runs    bool // the cache has the frame's tiles, not its runs
		want    int
	}{
		{"a photograph", photo, 1, false, 1},
		{"a photograph", photo, 3, false, 3},
		{"exact tiles", patterns, 3, false, 3},
		{"runs alone", photo, 3, true, 3},
		{"one row after the writer's", twoRows, 3, false, 1},
	} {
		d := newDirect(t, false)
		d.show(desktop)
		d.frame()
		d.show(c.next)
		sh := sharedWriter(t, d)
		sh.jobs, sh.workers = make(chan func(*worker), 64), 1
		w := d.w
		if _, ok := w.collect(image.Rectangle{}); !ok {
			t.Fatal("resized")
		}
		wk := newWorker()
		if c.runs {
			// A worker encodes the frame at the pacer's quality; the
			// next wants its runs at another.
			if w.prefetch(w.order) == nil {
				t.Fatal("no rows for the workers")
			}
			(<-sh.jobs)(wk)
		}
		sh.workers = c.workers
		f := w.prefetch(w.order)
		if f == nil {
			t.Fatal("no rows for the workers")
		}
		if c.runs {
			f.q--
		}
		before := sh.cache.stats().misses
		jobs := 0
		for len(sh.jobs) > 0 {
			(<-sh.jobs)(wk)
			jobs++
		}
		if made := sh.cache.stats().misses - before; jobs != c.want || made == 0 {
			t.Errorf("%s, %d workers at most: %d took the frame, and made %d encodings; want %d", c.name, c.workers, jobs, made, c.want)
		}
	}
}

// The writer stores a content that goes exact at the first place in the
// frame's order a worker found it, whichever found it first, and draws
// it cached after.
func TestPrefetchSeen(t *testing.T) {
	f := &prefetch{tc: newTileCache(16).contents(), seen: map[cacheKey]int{}}
	k := cacheKey{w: 64, h: 64}
	f.see(k, 10)
	f.see(k, 5)
	f.see(k, 8)
	for i, want := range map[int]bool{0: false, 5: false, 6: true, 9: true, 11: true} {
		if got := f.cached(k, true, i); got != want {
			t.Errorf("seen at 10, 5 and 8: cached at %d %v, want %v", i, got, want)
		}
	}
	if f.cached(k, false, 11) {
		t.Error("a tile too large for a slot is cached")
	}
}

// The workers encode a content that repeats exact in a frame once, at
// its first place in the frame's order, which the writer stores in the
// client's tile cache and then draws again cached; and every place of a
// content that repeats lossy, which the writer never stores. Whichever
// rows the workers take first, the writer finds every encoding of the
// rows after its own.
func TestPrefetchRepeats(t *testing.T) {
	photo := corpusFrame(t, "photo")
	for _, reversed := range []bool{false, true} {
		d := newDirect(t, false)
		d.w.cache = newTileCache(DefaultCacheSlots)
		s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
		d.show(s.shot())
		d.frame()

		// The writer's row of patterns of its own; below it, four
		// patterns that repeat, and one tile of the photograph, the same
		// in every row.
		lossy := photo.SubImage(image.Rect(320, 384, 384, 448)).(*image.RGBA)
		for y := range 12 {
			for x := range 16 {
				r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64)
				switch {
				case y == 0:
					s.set(x, y, pattern(8+x, r.Min))
				case x == 15:
					tile := image.NewRGBA(r)
					draw.Draw(tile, r, lossy, lossy.Rect.Min, draw.Src)
					s.set(x, y, tile)
				default:
					s.set(x, y, pattern(1+(x+y)%4, r.Min))
				}
			}
		}
		d.show(s.shot())
		sh := sharedWriter(t, d)
		sh.jobs = make(chan func(*worker), 64)
		w := d.w
		if _, ok := w.collect(image.Rectangle{}); !ok {
			t.Fatal("resized")
		}
		if w.order[0]/w.g.cols != 0 {
			t.Fatalf("the writer's row is %d", w.order[0]/w.g.cols)
		}
		f := w.prefetch(w.order)
		if f == nil {
			t.Fatal("no rows for the workers")
		}
		<-sh.jobs // the rows go by hand instead, in the order of the case
		wk := newWorker()
		enc := wk.encoder(f.p)
		rows := len(f.rows) - 1
		for k := range rows {
			if reversed {
				k = rows - 1 - k
			}
			f.row(wk, enc, f.rows[k], f.rows[k+1])
		}
		made := sh.cache.stats().misses
		if !reversed && made != 4+2*rows {
			// Each pattern once, and the photograph's tile in each row,
			// as a tile and as a run of one.
			t.Errorf("in order: %d encodings, want %d", made, 4+2*rows)
		}

		sh.jobs = nil // the writer encodes the rest alone
		if ok, _ := w.p.StartFrame(time.Now()); !ok {
			t.Fatal("the pacer holds the frame")
		}
		if err := w.sendFrame(d.ctx, w.order, false); err != nil {
			t.Fatal(err)
		}
		if got := sh.cache.stats().misses - made; got != 16 {
			t.Errorf("reversed %v: the writer made %d encodings, want the 16 of its row", reversed, got)
		}
	}
}

// The writer hands a frame's rows to the workers once it has a tile to
// encode itself: not for a frame whose tiles all go cached, nor for one
// whose encodings another viewer made; and to one worker.
func TestFetchStartsWithWork(t *testing.T) {
	vs := newViewers(t, 2, shareModes[1])
	a, b := vs.ds[0], vs.ds[1]
	sh := vs.sh
	sh.jobs, sh.workers = make(chan func(*worker), 64), 2
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y)%4, image.Pt(64*x, 64*y)))
		}
	}
	jobs := func() int {
		n := len(sh.jobs)
		for len(sh.jobs) > 0 {
			<-sh.jobs
		}
		return n
	}
	a.show(s.shot())
	if err := a.send(); err != nil {
		t.Fatal(err)
	}
	if n := jobs(); n != 1 {
		t.Fatalf("a frame to encode: %d jobs for the workers, want 1", n)
	}
	if err := b.send(); err != nil {
		t.Fatal(err)
	}
	if n := jobs(); n != 0 {
		t.Errorf("a frame another viewer encoded: %d jobs for the workers", n)
	}

	// The same tiles, elsewhere: every one is in a slot.
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y+1)%4, image.Pt(64*x, 64*y)))
		}
	}
	a.show(s.shot())
	if err := a.send(); err != nil {
		t.Fatal(err)
	}
	if n := jobs(); n != 0 {
		t.Errorf("a frame of cached tiles: %d jobs for the workers", n)
	}
}

// Serve shares a session's encodings among the viewers whose encoders
// are internal/encoder's, with DefaultEncodeWorkers unless EncodeWorkers
// says otherwise, none if negative, and not at all with NoShare.
func TestShareConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		shared  bool
		workers int
	}{
		{"by default", Config{}, true, DefaultEncodeWorkers()},
		{"with three workers", Config{EncodeWorkers: 3}, true, 3},
		{"without workers", Config{EncodeWorkers: -1}, true, 0},
		{"NoShare", Config{NoShare: true}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tc.cfg.SessionID = "s1"
				r := newRigConfig(t, backendtest.NewScreen(320, 192, 0), 64, 50e6, time.Millisecond, webpDeflate, tc.cfg)
				time.Sleep(time.Second)
				sharedMu.Lock()
				sh := sharedFor[r.s.Framebuffer()]
				sharedMu.Unlock()
				workers := 0
				if sh != nil && sh.jobs != nil {
					workers = sh.workers
				}
				if (sh != nil) != tc.shared || workers != tc.workers {
					t.Errorf("shared %v, with %d workers; want %v, %d", sh != nil, workers, tc.shared, tc.workers)
				}
			})
		})
	}
}

// Viewers that share the encode cache, and its workers, send what each
// would alone: random screens of tiles of a few patterns and of a
// photograph, moving and still, then refined, with resizes. Every
// viewer that shares sends the same messages, byte for byte, as the
// others; where the screens are only patterns, as a viewer alone; and
// each client ends with the server's pattern tiles exact and its
// photographic tiles at the score refinement seeks.
func TestShareConverges(t *testing.T) {
	photo := corpusFrame(t, "photo")
	modes := []shareMode{{"cache", true, 0}, {"workers", true, 2}}
	for _, m := range modes {
		for seed := range uint64(4) {
			patterns := seed < 2
			t.Run(fmt.Sprintf("%s/seed%d", m.name, seed), func(t *testing.T) {
				alone := runShareSeed(t, 1, shareModes[0], seed, patterns, photo)
				shared := runShareSeed(t, 3, m, seed, patterns, photo)
				for k := 1; k < len(shared); k++ {
					if i := firstDiff(shared[0], shared[k]); i >= 0 {
						t.Errorf("viewer %d's message %d differs from viewer 0's: %s, not %s", k, i, noteAt(shared[k], i), noteAt(shared[0], i))
					}
				}
				if patterns {
					if i := firstDiff(alone[0], shared[0]); i >= 0 {
						t.Errorf("message %d differs from a viewer's alone", i)
					}
				}
			})
		}
	}
}

// noteAt is a note of message i of msgs.
func noteAt(msgs [][]byte, i int) string {
	if i >= len(msgs) {
		return "none"
	}
	m, err := protocol.Decode(msgs[i], protocol.ServerMessages(), testLimits)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%T %+v", m, m)
}

// drawing keeps of msgs those that draw: what viewers of a session are
// sent alike, unlike the session's state.
func drawing(msgs [][]byte) [][]byte {
	var out [][]byte
	for _, p := range msgs {
		m, err := protocol.Decode(p, protocol.ServerMessages(), testLimits)
		if err != nil {
			out = append(out, p)
			continue
		}
		switch m.(type) {
		case *protocol.Tile, *protocol.CopyRect, *protocol.Resize, *protocol.FrameEnd:
			out = append(out, p)
		}
	}
	return out
}

// firstDiff returns the index of the first message that differs in a
// and b, -1 if none.
func firstDiff(a, b [][]byte) int {
	for i := range max(len(a), len(b)) {
		if i >= len(a) || i >= len(b) || !bytes.Equal(a[i], b[i]) {
			return i
		}
	}
	return -1
}

// runShareSeed sends n viewers of mode m the screens of seed, and returns
// each viewer's messages, after checking what each client shows.
func runShareSeed(t *testing.T, n int, m shareMode, seed uint64, patterns bool, photo *image.RGBA) [][][]byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 2026))
	vs := newViewers(t, n, m)
	tapes := make([]*tape, n)
	for k, d := range vs.ds {
		tapes[k] = &tape{recorder: d.c}
		d.w.c = tapes[k]
	}
	d0 := vs.ds[0]
	size := image.Pt(1024, 768)
	img := image.NewRGBA(image.Rectangle{Max: size})
	isPhoto := map[image.Point]bool{}
	paintTile := func(x, y int) {
		r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(img.Rect)
		at := image.Pt(x, y)
		if !patterns && rng.IntN(3) == 0 {
			off := image.Pt(rng.IntN(photo.Rect.Dx()-64), rng.IntN(photo.Rect.Dy()-64))
			draw.Draw(img, r, photo, off, draw.Src)
			isPhoto[at] = true
			return
		}
		draw.Draw(img, r, pattern(rng.IntN(6), r.Min), r.Min, draw.Src)
		delete(isPhoto, at)
	}
	cols, rows := (size.X+63)/64, (size.Y+63)/64
	for y := range rows {
		for x := range cols {
			paintTile(x, y)
		}
	}
	show := func() {
		// show keeps the image, which paintTile goes on changing.
		next := image.NewRGBA(img.Rect)
		copy(next.Pix, img.Pix)
		d0.show(next)
	}
	show()
	send := func() {
		t.Helper()
		for _, d := range vs.ds {
			d.age()
		}
		if _, _, err := vs.each((*direct).send); err != nil {
			t.Fatal(err)
		}
	}
	send()
	for frame := range 8 {
		if frame == 5 {
			// A resize to a size whose edge tiles are smaller.
			size = image.Pt(1000, 700)
			img = image.NewRGBA(image.Rectangle{Max: size})
			cols, rows = (size.X+63)/64, (size.Y+63)/64
			clear(isPhoto)
			for y := range rows {
				for x := range cols {
					paintTile(x, y)
				}
			}
			d0.b.Sink().Resize(size.X, size.Y)
			d0.prev = nil
			show()
			send()
			continue
		}
		for range 1 + rng.IntN(12) {
			paintTile(rng.IntN(cols), rng.IntN(rows))
		}
		show()
		send()
	}
	if _, _, err := vs.each(func(d *direct) error { _, err := d.refine(); return err }); err != nil {
		t.Fatal(err)
	}

	if vs.sh != nil {
		if s := vs.sh.cache.stats(); s.hits == 0 {
			t.Errorf("the viewers shared no encoding: %+v", s)
		}
	}
	if !patterns && !d0.c.lossy {
		t.Error("no tile went lossy")
	}

	server := serverScreen(d0.s)
	for k, d := range vs.ds {
		client := d.c.img
		for y := range rows {
			for x := range cols {
				r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(server.Rect)
				if !isPhoto[image.Pt(x, y)] {
					if !bytes.Equal(pixels(client, r), pixels(server, r)) {
						t.Fatalf("%s, %d viewers: viewer %d's pattern tile %v differs from the server's; client lossy %v, SSIM %.3f", m.name, n, k, r.Min, d.c.lossy, tileScores(client, server, r)[0])
					}
					continue
				}
				if s := tileScores(client, server, r)[0]; s < shareMinSSIM {
					t.Errorf("viewer %d's photographic tile %v has an SSIM of %.3f", k, r.Min, s)
				}
			}
		}
	}
	out := make([][][]byte, n)
	for k, c := range tapes {
		out[k] = drawing(c.msgs)
	}
	return out
}

// shareMinSSIM is the least SSIM a refined photographic tile has: the
// score refinement seeks, less what Still's estimate leaves.
const shareMinSSIM = 0.85

// A search by Still starts where the last of its profile in the session
// ended, whichever encoder made it, or else where the encoder's own last
// ended, and leaves where it ended for the next.
func TestEncodeCacheHint(t *testing.T) {
	codecs := []protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}
	p := profile{protocol.CodecDeflate, protocol.CodecWebP}
	photo := corpusFrame(t, "photo")
	r := image.Rect(0, 128, 1024, 192)
	pix := make([]byte, 0, 4*r.Dx()*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := photo.PixOffset(r.Min.X, y)
		pix = append(pix, photo.Pix[i:i+4*r.Dx()]...)
	}
	still := func(c *encodeCache, e *encoder.Encoder) (q, tries int) {
		before := e.Tries()
		_, _, q, _ = c.still(p, e, nil, pix, r.Dx(), r.Dy(), 64, 30, 90)
		return q, e.Tries() - before
	}

	c := newEncodeCache(DefaultEncodeCacheBytes)
	q, _ := still(c, encoder.New(codecs))
	if c.hints[p] != q {
		t.Fatalf("the session's hint is %d after a search for %d", c.hints[p], q)
	}
	fresh := encoder.New(codecs)
	if start := fresh.Hint(); start > q-2*encoder.QualityStep && start < q+2*encoder.QualityStep {
		t.Fatalf("a new encoder starts at %d, too near %d to tell", start, q)
	}
	// A new encoder starts where the other ended: a try, and one more
	// below, unless at the top.
	if got, tries := still(c, fresh); got != q || tries > 2 {
		t.Errorf("a new encoder, after another's search: %d in %d tries, want %d in 2 at most", got, tries, q)
	}

	// With no search of the session yet, an encoder starts where its
	// own last ended.
	own := encoder.New(codecs)
	own.SetHint(q)
	c = newEncodeCache(DefaultEncodeCacheBytes)
	if got, tries := still(c, own); got != q || tries > 2 {
		t.Errorf("an encoder's own hint: %d in %d tries, want %d in 2 at most", got, tries, q)
	}
}

// A session has two workers by default, or half the cores where there
// are fewer than four, and none on one.
func TestDefaultEncodeWorkers(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, c := range []struct{ procs, want int }{{1, 0}, {2, 1}, {3, 1}, {4, 2}, {5, 2}, {18, 2}} {
		runtime.GOMAXPROCS(c.procs)
		if got := DefaultEncodeWorkers(); got != c.want {
			t.Errorf("on %d cores: %d workers, want %d", c.procs, got, c.want)
		}
	}
}
