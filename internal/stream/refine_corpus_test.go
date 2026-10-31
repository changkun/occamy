// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/protocol"
)

// fixedEncoder is internal/encoder's encoder as it was before
// refinement: tiles of 17 to 256 colours lossless whatever their motion,
// and lossy tiles at the pacer's quality, with no search.
type fixedEncoder struct{ *encoder.Encoder }

func (e fixedEncoder) Tile(dst, pix []byte, w, h int, _ bool) ([]byte, protocol.Codec, bool) {
	return e.Encoder.Tile(dst, pix, w, h, false)
}

func (e fixedEncoder) Still(dst, pix []byte, w, h, _, _, hi int) ([]byte, protocol.Codec, int, bool) {
	b, c := e.Lossy(dst, pix, w, h, hi)
	return b, c, hi, true
}

// newRefineDirect is newDirect with the pacer's defaults, quality 30 to
// 90 from 90, where a viewer starts, and with refinement or, for before,
// with the encoder as it was before it.
func newRefineDirect(tb testing.TB, before bool) *direct {
	d := newDirect(tb, true)
	d.w.p = pacer.New(pacer.Config{Window: 1 << 20, InitialCap: 1 << 30})
	if before {
		d.w.enc = fixedEncoder{encoder.New([]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate})}
		d.w.refineDelay = -1
	}
	return d
}

// age makes the next frame take a snapshot of the versions, as frames at
// least activeWindow apart do: motion at 30 frames a second changes a
// tile in every snapshot.
func (d *direct) age() {
	d.w.snapAt = d.w.snapAt.Add(-activeWindow)
}

// refineAll refines what the writer would once the screen has been still
// for the refine delay, every tile at once, and reports the bytes, the
// messages, the encode time, and the longest encode of one message,
// which is the most an input waits for the writer.
func (d *direct) refineAll() (size, messages int, total, longest time.Duration) {
	w := d.w
	if w.refineDelay < 0 {
		return 0, 0, 0, 0
	}
	w.refineDelay = 0
	w.versions = w.fb.Versions(w.versions[:0])
	w.candidates(image.Rectangle{}, time.Now())
	for i, t := range w.refine {
		w.encodeTime = 0
		send, ok := w.refineTile(1, t)
		if !ok {
			d.tb.Fatal("resized")
		}
		total += w.encodeTime
		longest = max(longest, w.encodeTime)
		if !send {
			continue
		}
		if err := d.c.WriteMessage(d.ctx, w.buf); err != nil {
			d.tb.Fatal(err)
		}
		w.drawn(w.refine[i:i+1], w.msgQuality, w.msgGoal)
		size += len(w.buf)
		messages++
	}
	if len(d.c.errs) > 0 {
		d.tb.Fatal(d.c.errs[0])
	}
	w.refineDelay = DefaultRefineDelay
	return size, messages, total, longest
}

// refineScenes are the corpus's scenes as refinement sees them: first is
// sent whole, then frames in turn, warm of them unmeasured. moving makes
// the frames at least activeWindow apart, as 30 frames a second are.
var refineScenes = []struct {
	name   string
	first  string
	frames []string
	warm   int
	moving bool
}{
	// G5: the plasma, 17 to 256 colours a tile.
	{"motion", "motion-0", repeat([]string{"motion-1", "motion-0"}, 4), 2, true},
	// G4: the dragged window.
	{"drag", "drag-0", repeat([]string{"drag-1", "drag-2", "drag-1", "drag-0"}, 2), 2, true},
	// G3: the scrolled text.
	{"scroll", "scroll-0", []string{"scroll-1", "scroll-2"}, 0, true},
	// G6: the photograph, once.
	{"photo, once", "desktop", []string{"photo"}, 0, false},
	{"browser, once", "desktop", []string{"browser"}, 0, false},
	{"terminal-aa, once", "desktop", []string{"terminal-aa"}, 0, false},
}

func repeat(s []string, n int) []string {
	var out []string
	for range n {
		out = append(out, s...)
	}
	return out
}

// refineRun sends a scene and returns, for each measured frame, its bytes
// and encode time, then what refinement sends once it is still, whether
// the client then shows the server's screen exactly, and the SSIM of its
// worst tile.
type refineRun struct {
	bytes                 []int
	times                 []time.Duration
	lossy                 int // measured frames with a lossy tile
	refBytes, refMessages int
	refTime, refLongest   time.Duration
	exact                 bool
	worst                 float64
}

func runRefineScene(tb testing.TB, before bool, first string, frames []string, warm int, moving bool) refineRun {
	var out refineRun
	d := newRefineDirect(tb, before)
	d.show(corpusFrame(tb, first))
	d.frame()
	for k, name := range frames {
		if moving {
			d.age()
		}
		d.show(corpusFrame(tb, name))
		d.c.lossy = false
		b, _, _, enc, _ := d.frame()
		if k < warm {
			continue
		}
		out.bytes = append(out.bytes, b)
		out.times = append(out.times, enc)
		if d.c.lossy {
			out.lossy++
		}
	}
	out.refBytes, out.refMessages, out.refTime, out.refLongest = d.refineAll()
	server := serverScreen(d.s)
	out.exact = bytes.Equal(d.c.img.Pix, server.Pix)
	out.worst = slices.Min(tileScores(d.c.img, server, server.Rect))
	return out
}

// Refinement on the corpus. Motion, the plasma's and the dragged
// window's, goes in a quarter of the bytes at most, and once still is
// refined: the plasma to exact, the photograph in the window to the
// quality its SSIM calls for. A photograph shown once goes under G6's
// 139 KiB and needs no refinement, nor does the browser, whose pictures
// go in fewer bytes than before; the scroll and still text send what they
// sent before, and the client shows them exactly. No tile ends worse than
// the encoder's SSIM score, or than before if that was lower.
func TestRefineCorpus(t *testing.T) {
	for _, sc := range refineScenes {
		var size [2]int
		var r [2]refineRun
		for k, before := range []bool{true, false} {
			r[k] = runRefineScene(t, before, sc.first, sc.frames, sc.warm, sc.moving)
			for _, b := range r[k].bytes {
				size[k] += b
			}
		}
		after := r[1]
		n := len(after.bytes)
		t.Logf("%-18s %7.1f KiB a frame before, %7.1f after; refinement %7.1f KiB in %d messages; worst tile %.3f, %.3f before",
			sc.name, float64(size[0])/1024/float64(n), float64(size[1])/1024/float64(n),
			float64(after.refBytes)/1024, after.refMessages, after.worst, r[0].worst)
		if floor := min(encoder.StillSSIM, r[0].worst); after.worst < floor-1e-9 {
			t.Errorf("%s: a tile scores %.4f after refinement, want %.4f at least", sc.name, after.worst, floor)
		}
		switch sc.name {
		case "motion", "drag":
			if 4*size[1] > size[0] {
				t.Errorf("%s: %d bytes moving, %d before: want a quarter at most", sc.name, size[1], size[0])
			}
			if after.refMessages == 0 {
				t.Errorf("%s: nothing refined once still", sc.name)
			}
			if sc.name == "motion" && !after.exact {
				t.Errorf("%s: the client is not exact after refinement", sc.name)
			}
		case "photo, once", "browser, once":
			if size[1] >= size[0] || sc.name == "photo, once" && size[1] > 139<<10 {
				t.Errorf("%s: %d bytes, %d before: want fewer, and a photograph under G6's 139 KiB", sc.name, size[1], size[0])
			}
			if after.refMessages != 0 {
				t.Errorf("%s: %d messages of refinement after a still picture", sc.name, after.refMessages)
			}
		default:
			if size[1] != size[0] || after.refMessages != 0 || !after.exact {
				t.Errorf("%s: %d bytes, %d before, %d refined, exact %v", sc.name, size[1], size[0], after.refMessages, after.exact)
			}
		}
	}
}

// loadAverage is the host's load average, for the measurement's log.
func loadAverage() string {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return "unknown"
		}
		out = b
	}
	return strings.TrimSpace(string(out))
}

var refineRuns = flag.Int("refine.runs", 0, "time each corpus scene this many times on one core and print the medians")

// TestRefineTimes prints each scene's bytes and encode time a frame,
// before and after refinement, and what refinement costs once the scene
// is still: the median over -refine.runs passes.
func TestRefineTimes(t *testing.T) {
	if *refineRuns == 0 {
		t.Skip("set -refine.runs to time the corpus")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	t.Logf("load average at the start: %s", loadAverage())
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Encoder | KiB a frame | share of frames lossy | encode ms a frame | refinement KiB | messages | encode ms | longest message ms |\n|---|---|---|---|---|---|---|---|---|\n")
	median := func(d []time.Duration) time.Duration {
		slices.Sort(d)
		return d[len(d)/2]
	}
	for _, sc := range refineScenes {
		for _, before := range []bool{true, false} {
			var frames, refTimes, longest []time.Duration
			var r0 refineRun
			for pass := range *refineRuns {
				r := runRefineScene(t, before, sc.first, sc.frames, sc.warm, sc.moving)
				if pass == 0 {
					r0 = r
				}
				var sum time.Duration
				for _, d := range r.times {
					sum += d
				}
				frames = append(frames, sum/time.Duration(len(r.times)))
				refTimes = append(refTimes, r.refTime)
				longest = append(longest, r.refLongest)
			}
			size := 0
			for _, b := range r0.bytes {
				size += b
			}
			n := float64(len(r0.bytes))
			mode := "after"
			if before {
				mode = "before"
			}
			fmt.Fprintf(&out, "| %s | %s | %.1f | %.1f | %.2f | %.1f | %d | %.2f | %.3f |\n", sc.name, mode,
				float64(size)/1024/n, float64(r0.lossy)/n, float64(median(frames).Microseconds())/1000,
				float64(r0.refBytes)/1024, r0.refMessages, float64(median(refTimes).Microseconds())/1000,
				float64(median(longest).Microseconds())/1000)
		}
	}
	t.Logf("load average at the end: %s", loadAverage())
	t.Log("\n" + out.String())
}
