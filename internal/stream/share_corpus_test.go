// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// shareMode is how the viewers of a session encode: each alone, or
// sharing the session's encode cache, and its workers if any.
type shareMode struct {
	name    string
	share   bool
	workers int
}

var shareModes = []shareMode{
	{"alone", false, 0},
	{"cache", true, 0},
	{"workers", true, DefaultEncodeWorkers()},
}

// viewers are n viewers of one session, each with a writer as
// newRefineDirect makes it and a tile cache of the default slots, which
// the test drives frame by frame: each viewer's frame on its own
// goroutine, as each viewer's writer runs. sh is what they share, nil
// if nothing.
type viewers struct {
	ds []*direct
	sh *shared
}

func newViewers(tb testing.TB, n int, m shareMode) *viewers {
	tb.Helper()
	d := newRefineDirect(tb, false)
	d.w.cache = newTileCache(DefaultCacheSlots)
	vs := &viewers{ds: []*direct{d}}
	if m.share {
		fb := d.w.fb
		vs.sh = share(fb, m.workers, DefaultEncodeCacheBytes, slog.New(slog.DiscardHandler))
		tb.Cleanup(func() { vs.sh.release(fb) })
	}
	for range n - 1 {
		v, err := d.s.Join(session.RoleViewer)
		if err != nil {
			tb.Fatal(err)
		}
		if err := v.View(true, image.Rectangle{}); err != nil {
			tb.Fatal(err)
		}
		c := &recorder{reg: protocol.ServerMessages()}
		p := pacer.New(pacer.Config{Window: 1 << 20, InitialCap: 1 << 30})
		enc := newEncoder([]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate})
		w := newWriter(c, v, p, enc, protocol.Limits{}, slog.New(slog.DiscardHandler))
		w.sc = &scroller{}
		w.cache = newTileCache(DefaultCacheSlots)
		vs.ds = append(vs.ds, &direct{tb: tb, ctx: d.ctx, b: d.b, s: d.s, w: w, c: c})
	}
	if vs.sh != nil {
		for _, d := range vs.ds {
			p, ok := profileOf(d.w.enc)
			if !ok {
				tb.Fatal("the encoder is not internal/encoder's")
			}
			d.w.sh, d.w.prof = vs.sh, p
		}
	}
	return vs
}

// send sends what changed as one frame, as frame does, and returns an
// error rather than failing the test, since it runs on a goroutine of
// its own.
func (d *direct) send() error {
	w := d.w
	if err := w.syncSize(d.ctx); err != nil {
		return err
	}
	d.aging()
	if _, ok := w.collect(image.Rectangle{}); !ok {
		return errors.New("resized")
	}
	if ok, _ := w.p.StartFrame(time.Now()); !ok {
		return errors.New("the pacer holds the frame")
	}
	d.c.bytes, d.c.tiles, d.c.copies, d.c.rects = 0, 0, 0, nil
	if err := w.sendFrame(d.ctx, w.order, false); err != nil {
		return err
	}
	if len(d.c.errs) > 0 {
		return d.c.errs[0]
	}
	return nil
}

// refine refines what the writer would once the screen has been still,
// as refineAll does, and returns the bytes or an error.
func (d *direct) refine() (int, error) {
	w := d.w
	w.refineDelay = 0
	defer func() { w.refineDelay = DefaultRefineDelay }()
	w.versions = w.fb.Versions(w.versions[:0])
	w.candidates(image.Rectangle{}, time.Now())
	size := 0
	for i, t := range w.refine {
		send, ok := w.refineTile(1, t)
		if !ok {
			return 0, errors.New("resized")
		}
		if !send {
			continue
		}
		if err := d.c.WriteMessage(d.ctx, w.buf); err != nil {
			return 0, err
		}
		w.drawn(w.refine[i:i+1], w.msgQuality, w.msgGoal)
		size += len(w.buf)
	}
	if len(d.c.errs) > 0 {
		return 0, d.c.errs[0]
	}
	return size, nil
}

// each runs f for every viewer at once, and reports the time until all
// are done and the process's CPU time meanwhile.
func (vs *viewers) each(f func(d *direct) error) (wall, cpu time.Duration, err error) {
	errs := make([]error, len(vs.ds))
	start, cpu0 := time.Now(), cpuTime()
	var wg sync.WaitGroup
	for k, d := range vs.ds {
		wg.Go(func() { errs[k] = f(d) })
	}
	wg.Wait()
	return time.Since(start), cpuTime() - cpu0, errors.Join(errs...)
}

// shareRun is a scene sent to n viewers: the first viewer's bytes a
// frame, and for each measured frame the time until every viewer sent
// it and the CPU time they took, then the same of refinement once the
// scene is still, and what the encode cache held and found, if shared.
type shareRun struct {
	bytes           []int
	walls, cpus     []time.Duration
	refBytes        int
	refWall, refCPU time.Duration
	stats           cacheStats
}

func runShareScene(tb testing.TB, n int, m shareMode, first string, frames []string, warm int, moving bool) shareRun {
	tb.Helper()
	var out shareRun
	vs := newViewers(tb, n, m)
	d0 := vs.ds[0]
	d0.show(corpusFrame(tb, first))
	if _, _, err := vs.each((*direct).send); err != nil {
		tb.Fatal(err)
	}
	for k, name := range frames {
		if moving {
			for _, d := range vs.ds {
				d.age()
			}
		}
		d0.show(corpusFrame(tb, name))
		wall, cpu, err := vs.each((*direct).send)
		if err != nil {
			tb.Fatal(err)
		}
		if k < warm {
			continue
		}
		out.bytes = append(out.bytes, d0.c.bytes)
		out.walls = append(out.walls, wall)
		out.cpus = append(out.cpus, cpu)
	}
	var err error
	out.refWall, out.refCPU, err = vs.each(func(d *direct) error {
		b, err := d.refine()
		if d == d0 {
			out.refBytes = b
		}
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	if vs.sh != nil {
		out.stats = vs.sh.cache.stats()
	}
	return out
}

func mean(ds []time.Duration) time.Duration {
	var t time.Duration
	for _, d := range ds {
		t += d
	}
	return t / time.Duration(len(ds))
}

func median(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

var shareRuns = flag.Int("share.runs", 0, "time each corpus scene this many times for 1, 2 and 4 viewers of one session and print the medians")

// TestShareTimes prints, for each corpus scene sent to 1, 2 and 4 viewers
// of one session, each encoding alone, sharing the encode cache, and
// sharing it and the workers: the bytes a frame, the time until every
// viewer has sent a frame and the process's CPU time for it, the same of
// refinement once the scene is still, and the most the encode cache held
// and the share of lookups it answered; the median over -share.runs
// passes, which go through the modes in turn so that the load falls on
// each alike.
func TestShareTimes(t *testing.T) {
	if *shareRuns == 0 {
		t.Skip("set -share.runs to time the corpus")
	}
	t.Logf("load average at the start: %s", loadAverage())
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Viewers | Mode | KiB a frame | ms a frame: wall · CPU | refinement: KiB · wall ms · CPU ms | cache: KiB held at most · hits |\n|---|---|---|---|---|---|---|\n")
	for _, sc := range cacheScenes {
		for _, n := range []int{1, 2, 4} {
			type acc struct {
				walls, cpus, refWalls, refCPUs []time.Duration
				r0                             shareRun
			}
			accs := make([]acc, len(shareModes))
			for pass := range *shareRuns {
				for k, m := range shareModes {
					r := runShareScene(t, n, m, sc.first, sc.frames, sc.warm, sc.moving)
					a := &accs[k]
					if pass == 0 {
						a.r0 = r
					}
					a.walls = append(a.walls, mean(r.walls))
					a.cpus = append(a.cpus, mean(r.cpus))
					a.refWalls = append(a.refWalls, r.refWall)
					a.refCPUs = append(a.refCPUs, r.refCPU)
				}
			}
			for k, m := range shareModes {
				a := &accs[k]
				cache := "–"
				if st := a.r0.stats; m.share {
					cache = fmt.Sprintf("%.0f · %.0f%%", float64(st.high)/1024, 100*float64(st.hits)/float64(max(1, st.hits+st.misses+st.stale)))
				}
				fmt.Fprintf(&out, "| %s | %d | %s | %.1f | %.2f · %.2f | %.1f · %.2f · %.2f | %s |\n", sc.name, n, m.name,
					float64(sum(a.r0.bytes))/1024/float64(len(a.r0.bytes)), ms(median(a.walls)), ms(median(a.cpus)),
					float64(a.r0.refBytes)/1024, ms(median(a.refWalls)), ms(median(a.refCPUs)), cache)
			}
		}
	}
	t.Logf("load average at the end: %s", loadAverage())
	t.Log("\n" + out.String())
}
