// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"flag"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// cacheScenes are the refinement measurement's scenes, and windows
// switched back and forth over the desktop, as alt-tab does: the first
// switch there and back is unmeasured, and fills the cache.
var cacheScenes = append(slices.Clone(refineScenes), []struct {
	name   string
	first  string
	frames []string
	warm   int
	moving bool
}{
	{"switch, browser", "desktop", repeat([]string{"browser", "desktop"}, 3), 2, false},
	{"switch, terminal", "desktop", repeat([]string{"terminal", "desktop"}, 3), 2, false},
	{"switch, photo", "desktop", repeat([]string{"photo", "desktop"}, 3), 2, false},
}...)

// cacheRun is a scene sent to a viewer with or without a tile cache of
// the default slots: each measured frame's bytes and encode time, the
// tiles drawn from the cache in them, what refinement sends once the
// scene is still, and whether the client then shows the server's screen
// exactly.
type cacheRun struct {
	bytes                 []int
	times                 []time.Duration
	cached                int
	refBytes, refMessages int
	exact                 bool
}

func runCacheScene(tb testing.TB, cache bool, first string, frames []string, warm int, moving bool) cacheRun {
	var out cacheRun
	d := newRefineDirect(tb, false)
	if cache {
		d.w.cache = newTileCache(DefaultCacheSlots)
	}
	d.show(corpusFrame(tb, first))
	d.frame()
	for k, name := range frames {
		if moving {
			d.age()
		}
		d.show(corpusFrame(tb, name))
		cached := d.c.cache.cached
		b, _, _, enc, _ := d.frame()
		if k < warm {
			continue
		}
		out.bytes = append(out.bytes, b)
		out.times = append(out.times, enc)
		out.cached += d.c.cache.cached - cached
	}
	out.refBytes, out.refMessages, _, _ = d.refineAll()
	out.exact = bytes.Equal(d.c.img.Pix, serverScreen(d.s).Pix)
	return out
}

func sum(b []int) int {
	n := 0
	for _, v := range b {
		n += v
	}
	return n
}

// The tile cache on the corpus. No scene takes more bytes with it, the
// client shows what it showed without it, and a window switched back
// and forth over the desktop takes a fraction of the bytes once the
// cache holds both: the desktop and the window's exact tiles come from
// it, though not a photograph's, which go lossy and are not stored.
func TestCacheCorpus(t *testing.T) {
	for _, sc := range cacheScenes {
		var r [2]cacheRun
		for k, cache := range []bool{false, true} {
			r[k] = runCacheScene(t, cache, sc.first, sc.frames, sc.warm, sc.moving)
		}
		n := float64(len(r[1].bytes))
		without, with := sum(r[0].bytes), sum(r[1].bytes)
		t.Logf("%-18s %7.1f KiB a frame without the cache, %7.1f with; %5.1f tiles a frame cached; refinement %7.1f KiB, %7.1f with",
			sc.name, float64(without)/1024/n, float64(with)/1024/n, float64(r[1].cached)/n,
			float64(r[0].refBytes)/1024, float64(r[1].refBytes)/1024)
		if with > without || r[1].refBytes > r[0].refBytes {
			t.Errorf("%s: %d bytes and %d of refinement with the cache, %d and %d without", sc.name, with, r[1].refBytes, without, r[0].refBytes)
		}
		if r[1].exact != r[0].exact {
			t.Errorf("%s: exact %v with the cache, %v without", sc.name, r[1].exact, r[0].exact)
		}
		switch sc.name {
		case "switch, browser", "switch, terminal":
			if 2*with > without {
				t.Errorf("%s: %d bytes with the cache, %d without: want half at most", sc.name, with, without)
			}
		case "switch, photo":
			// The photograph goes lossy each time, and only the
			// desktop comes from the cache.
			if with >= without {
				t.Errorf("%s: %d bytes with the cache, %d without: want fewer", sc.name, with, without)
			}
		}
	}
}

var cacheRuns = flag.Int("cache.runs", 0, "time each corpus scene this many times on one core, with the tile cache and without, and print the medians")

// TestCacheTimes prints each scene's bytes and encode time a frame, with
// the tile cache and without: the median over -cache.runs passes.
func TestCacheTimes(t *testing.T) {
	if *cacheRuns == 0 {
		t.Skip("set -cache.runs to time the corpus")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	t.Logf("load average at the start: %s", loadAverage())
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Cache | KiB a frame | cached tiles a frame | encode ms a frame | refinement KiB | messages |\n|---|---|---|---|---|---|---|\n")
	for _, sc := range cacheScenes {
		for _, cache := range []bool{false, true} {
			var frames []time.Duration
			var r0 cacheRun
			for pass := range *cacheRuns {
				r := runCacheScene(t, cache, sc.first, sc.frames, sc.warm, sc.moving)
				if pass == 0 {
					r0 = r
				}
				var total time.Duration
				for _, d := range r.times {
					total += d
				}
				frames = append(frames, total/time.Duration(len(r.times)))
			}
			slices.Sort(frames)
			n := float64(len(r0.bytes))
			mode := "without"
			if cache {
				mode = "with"
			}
			fmt.Fprintf(&out, "| %s | %s | %.1f | %.1f | %.2f | %.1f | %d |\n", sc.name, mode,
				float64(sum(r0.bytes))/1024/n, float64(r0.cached)/n, float64(frames[len(frames)/2].Microseconds())/1000,
				float64(r0.refBytes)/1024, r0.refMessages)
		}
	}
	t.Logf("load average at the end: %s", loadAverage())
	t.Log("\n" + out.String())
}
