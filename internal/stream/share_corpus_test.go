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

// viewers are n viewers of one session, each with a writer as
// newRefineDirect makes it and a tile cache of the default slots, which
// the test drives frame by frame: each viewer's frame on its own
// goroutine, as each viewer's writer runs.
type viewers struct {
	ds []*direct
}

func newViewers(tb testing.TB, n int) *viewers {
	tb.Helper()
	d := newRefineDirect(tb, false)
	d.w.cache = newTileCache(DefaultCacheSlots)
	vs := &viewers{ds: []*direct{d}}
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
// scene is still.
type shareRun struct {
	bytes           []int
	walls, cpus     []time.Duration
	refBytes        int
	refWall, refCPU time.Duration
}

func runShareScene(tb testing.TB, n int, first string, frames []string, warm int, moving bool) shareRun {
	tb.Helper()
	var out shareRun
	vs := newViewers(tb, n)
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
// of one session, the bytes a frame, the time until every viewer has sent
// a frame and the process's CPU time for it, and the same of refinement
// once the scene is still: the median over -share.runs passes.
func TestShareTimes(t *testing.T) {
	if *shareRuns == 0 {
		t.Skip("set -share.runs to time the corpus")
	}
	t.Logf("load average at the start: %s", loadAverage())
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Viewers | KiB a frame | ms a frame: wall · CPU | refinement: KiB · wall ms · CPU ms |\n|---|---|---|---|---|\n")
	for _, sc := range cacheScenes {
		for _, n := range []int{1, 2, 4} {
			var walls, cpus, refWalls, refCPUs []time.Duration
			var r0 shareRun
			for pass := range *shareRuns {
				r := runShareScene(t, n, sc.first, sc.frames, sc.warm, sc.moving)
				if pass == 0 {
					r0 = r
				}
				walls = append(walls, mean(r.walls))
				cpus = append(cpus, mean(r.cpus))
				refWalls = append(refWalls, r.refWall)
				refCPUs = append(refCPUs, r.refCPU)
			}
			fmt.Fprintf(&out, "| %s | %d | %.1f | %.2f · %.2f | %.1f · %.2f · %.2f |\n", sc.name, n,
				float64(sum(r0.bytes))/1024/float64(len(r0.bytes)), ms(median(walls)), ms(median(cpus)),
				float64(r0.refBytes)/1024, ms(median(refWalls)), ms(median(refCPUs)))
		}
	}
	t.Logf("load average at the end: %s", loadAverage())
	t.Log("\n" + out.String())
}
