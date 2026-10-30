// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/protocol"
)

// message is one tile message of a frame: a tile, or a lossy rectangle of
// adjacent tiles.
type message struct {
	r       image.Rectangle
	tiles   int
	codec   protocol.Codec
	payload []byte
}

// encodeFrame encodes a whole frame in 64-pixel tiles, row by row, as the
// stream writer sends a full screen: each tile through Tile, and each run
// of adjacent lossy tiles in a row, up to MaxRun, through Lossy, or each
// lossy tile alone if merge is false. Like the writer, it keeps the
// lossless tile that ends a run for the next message. It returns the
// messages and how long encoding took, classification included.
func encodeFrame(e *Encoder, img *image.RGBA, quality int, merge bool) ([]message, time.Duration) {
	const side = 64
	b := img.Rect
	cols, rows := (b.Dx()+side-1)/side, (b.Dy()+side-1)/side
	tile := func(c, r int) image.Rectangle {
		return image.Rect(c*side, r*side, (c+1)*side, (r+1)*side).Intersect(b)
	}
	var msgs []message
	var took time.Duration
	var ahead *message // the lossless tile that ended a run
	run := make([]byte, 0, 4*MaxRun*side*side)
	for r := range rows {
		for c := 0; c < cols; {
			t := tile(c, r)
			if ahead != nil {
				msgs, ahead = append(msgs, *ahead), nil
				c++
				continue
			}
			pix := pixels(img, t)
			start := time.Now()
			payload, codec, lossy := e.Tile(nil, pix, t.Dx(), t.Dy())
			took += time.Since(start)
			if !lossy {
				msgs = append(msgs, message{t, 1, codec, payload})
				c++
				continue
			}
			// The run's pixels, the tiles side by side.
			rect, n := t, 1
			for merge && c+n < cols && n < MaxRun {
				u := tile(c+n, r)
				start := time.Now()
				payload, codec, lossy := e.Tile(nil, pixels(img, u), u.Dx(), u.Dy())
				took += time.Since(start)
				if !lossy {
					ahead = &message{u, 1, codec, payload}
					break
				}
				rect, n = rect.Union(u), n+1
			}
			run = append(run[:0], pixels(img, rect)...)
			start = time.Now()
			payload, codec = e.Lossy(nil, run, rect.Dx(), rect.Dy(), quality)
			took += time.Since(start)
			msgs = append(msgs, message{rect, n, codec, payload})
			c += n
		}
	}
	return msgs, took
}

// Every corpus frame encodes to messages that cover it tile by tile, its
// tiles of up to 256 colours exactly and its lossy rectangles above an
// SSIM floor. The test logs each frame's bytes and encode time on one
// pass; BenchmarkFrame and -corpus.runs time them properly.
func TestCorpus(t *testing.T) {
	for _, s := range scenes {
		for _, name := range s.frames {
			img := loadFrame(t, name)
			for _, codecs := range [][]protocol.Codec{{webpC, deflate}, {jpegC, deflate}} {
				e := New(codecs)
				msgs, took := encodeFrame(e, img, 70, true)
				covered := image.NewRGBA(img.Rect) // alpha 0xff where a message drew
				size, lossyTiles := 0, 0
				for _, m := range msgs {
					size += len(m.payload)
					out, err := decode(m.codec, m.payload, m.r.Dx(), m.r.Dy())
					if err != nil {
						t.Fatalf("%s, %v: %v", name, m.r, err)
					}
					src := img.SubImage(m.r).(*image.RGBA)
					want := pixels(img, m.r)
					switch {
					case m.codec == deflate:
						if !bytes.Equal(out.Pix, want) || m.tiles != 1 {
							t.Errorf("%s: the lossless tile at %v differs", name, m.r)
						}
					case m.r.Dy() >= 8:
						lossyTiles += m.tiles
						// The grain of the photograph scores lowest:
						// 0.843 at q70 in the measurement's rows.
						if score := SSIM(src, out); score < 0.8 {
							t.Errorf("%s: the lossy rectangle at %v has SSIM %.3f", name, m.r, score)
						}
					}
					for y := m.r.Min.Y; y < m.r.Max.Y; y++ {
						for x := m.r.Min.X; x < m.r.Max.X; x++ {
							i := covered.PixOffset(x, y) + 3
							if covered.Pix[i] != 0 {
								t.Fatalf("%s: %v drawn twice", name, image.Pt(x, y))
							}
							covered.Pix[i] = 0xff
						}
					}
				}
				for i := 3; i < len(covered.Pix); i += 4 {
					if covered.Pix[i] == 0 {
						t.Fatalf("%s: pixel %d not drawn", name, i/4)
					}
				}
				t.Logf("%-11s %-6v %6.1f KiB in %3d messages, %3d tiles lossy, encoded in %v",
					name, codecs[0], float64(size)/1024, len(msgs), lossyTiles, took.Round(10*time.Microsecond))
			}
		}
	}
}

// BenchmarkFrame encodes each scene's frames whole, as the stream writer
// sends a full screen, with WebP for photographic tiles merged into rows
// at quality 70, and reports the time and bytes per frame. Run it with
// -cpu 1 to compare with docs/benchmarks/codecs.md, which timed one core.
func BenchmarkFrame(b *testing.B) {
	for _, lossy := range []protocol.Codec{webpC, jpegC} {
		for _, s := range scenes {
			var frames []*image.RGBA
			for _, name := range s.frames {
				frames = append(frames, loadFrame(b, name))
			}
			b.Run(fmt.Sprintf("%v/%s", lossy, s.name), func(b *testing.B) {
				e := New([]protocol.Codec{lossy, deflate})
				size := 0
				for i := 0; b.Loop(); i++ {
					msgs, _ := encodeFrame(e, frames[i%len(frames)], 70, true)
					if i < len(frames) {
						for _, m := range msgs {
							size += len(m.payload)
						}
					}
				}
				b.ReportMetric(float64(size)/float64(len(frames))/1024, "KiB/frame")
			})
		}
	}
}

var corpusRuns = flag.Int("corpus.runs", 0, "time every corpus frame this many times on one core and print the medians")

// TestCorpusTimes prints each scene's encode time a frame, the median of
// -corpus.runs passes over its frames on one core, with WebP and with JPEG
// for photographic tiles, merged into rows or sent tile by tile. The
// codec measurement summed medians the same way.
func TestCorpusTimes(t *testing.T) {
	if *corpusRuns == 0 {
		t.Skip("set -corpus.runs to time the corpus")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Codecs | KiB a frame | ms a frame | messages a frame |\n|---|---|---|---|---|\n")
	for _, s := range scenes {
		var frames []*image.RGBA
		for _, name := range s.frames {
			frames = append(frames, loadFrame(t, name))
		}
		for _, mode := range []struct {
			name  string
			lossy protocol.Codec
			merge bool
		}{
			{"webp rows", webpC, true},
			{"webp tiles", webpC, false},
			{"jpeg rows", jpegC, true},
		} {
			e := New([]protocol.Codec{mode.lossy, deflate})
			var size, msgs int
			var sum time.Duration
			for _, f := range frames {
				var times []time.Duration
				for pass := range *corpusRuns {
					m, took := encodeFrame(e, f, 70, mode.merge)
					times = append(times, took)
					if pass == 0 {
						msgs += len(m)
						for _, x := range m {
							size += len(x.payload)
						}
					}
				}
				slices.Sort(times)
				sum += times[len(times)/2]
			}
			n := float64(len(frames))
			fmt.Fprintf(&out, "| %s | %s | %.1f | %.2f | %.0f |\n", s.name, mode.name,
				float64(size)/1024/n, float64(sum.Microseconds())/1000/n, float64(msgs)/n)
		}
	}
	t.Log("\n" + out.String())
}
