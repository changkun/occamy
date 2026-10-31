// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// corpusFrame reads a frame of the codec corpus (bench/testdata/corpus)
// as RGBA.
func corpusFrame(tb testing.TB, name string) *image.RGBA {
	tb.Helper()
	f, err := os.Open(filepath.Join("..", "..", "bench", "testdata", "corpus", name+".png"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		tb.Fatal(err)
	}
	img := image.NewRGBA(m.Bounds())
	draw.Draw(img, img.Rect, m, m.Bounds().Min, draw.Src)
	return img
}

// recorder is a connection that draws what the writer sends into a
// client's screen and counts the frame's messages and bytes.
type recorder struct {
	img                  *image.RGBA
	bytes, tiles, copies int
	lossy                bool              // a tile went lossy, so the screen is not exact
	rects                []image.Rectangle // of the tile messages, in order
	errs                 []error
	reg                  *protocol.Registry
}

func (c *recorder) WriteMessage(_ context.Context, p []byte) error {
	m, err := protocol.Decode(p, c.reg, protocol.Limits{MaxMessageSize: 1 << 30})
	if err != nil {
		c.errs = append(c.errs, err)
		return nil
	}
	switch m := m.(type) {
	case *protocol.Resize:
		c.img = image.NewRGBA(image.Rect(0, 0, int(m.Width), int(m.Height)))
	case *protocol.Tile:
		r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
		if err := decodeTile(c.img.SubImage(r).(*image.RGBA), m.Codec, m.Payload); err != nil {
			c.errs = append(c.errs, err)
		}
		c.lossy = c.lossy || m.Codec == protocol.CodecWebP || m.Codec == protocol.CodecJPEG
		c.bytes += len(p)
		c.tiles++
		c.rects = append(c.rects, r)
	case *protocol.CopyRect:
		dst := image.Rect(int(m.DstX), int(m.DstY), int(m.DstX)+int(m.W), int(m.DstY)+int(m.H))
		draw.Draw(c.img, dst, c.img, image.Pt(int(m.SrcX), int(m.SrcY)), draw.Src)
		c.bytes += len(p)
		c.copies++
	}
	return nil
}

func (c *recorder) ReadMessage(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// direct is a writer that the test drives frame by frame on its own
// goroutine, for real times: frames encoded in a synctest bubble take no
// time.
type direct struct {
	tb   testing.TB
	ctx  context.Context
	b    *tapped
	s    *session.Session
	w    *writer
	c    *recorder
	prev *image.RGBA
}

// newDirect returns a writer of a 1024×768 session for a viewer of WebP
// and deflate at quality 70, as the codec measurement encoded, with
// scroll detection if scroll is set. No frame is ever acknowledged, so
// the pacer measures no rate: it neither cuts a frame nor paces it, and
// each frame goes out whole and at once, however slowly it is encoded.
func newDirect(tb testing.TB, scroll bool) *direct {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)
	b := &tapped{Backend: backendtest.NewScreen(1024, 768, 0)}
	s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: 64})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(s.Close)
	// The backend resizes the screen, fills it, then paints its swatch:
	// once the swatch shows, nothing else draws but the test.
	swatch := backendtest.SwatchColor(0)
	pix := make([]byte, 4*64*64)
	for {
		if s.Framebuffer().Bounds().Dx() == 1024 && b.Sink() != nil {
			s.Framebuffer().ReadTile(0, pix)
			if pix[0] == swatch.R && pix[1] == swatch.G && pix[2] == swatch.B {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if err := owner.View(true, image.Rectangle{}); err != nil {
		tb.Fatal(err)
	}
	c := &recorder{reg: protocol.ServerMessages()}
	p := pacer.New(pacer.Config{Window: 1 << 20, MinQuality: 70, MaxQuality: 70, InitialCap: 1 << 30})
	enc := newEncoder([]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate})
	w := newWriter(c, owner, p, enc, protocol.Limits{}, slog.New(slog.DiscardHandler))
	if scroll {
		w.sc = &scroller{}
	}
	return &direct{tb: tb, ctx: ctx, b: b, s: s, w: w, c: c}
}

// show paints img as a backend reports damage: each tile that differs
// from the last image shown, or the whole screen the first time.
func (d *direct) show(img *image.RGBA) {
	sink := d.b.Sink()
	if d.prev == nil {
		paint(d.b, img.Rect, img)
		d.prev = img
		return
	}
	for y := 0; y < img.Rect.Dy(); y += 64 {
		for x := 0; x < img.Rect.Dx(); x += 64 {
			r := image.Rect(x, y, x+64, y+64).Intersect(img.Rect)
			if !sameRect(img, d.prev, r) {
				dst := sink.Lock(r)
				draw.Draw(dst, r, img, r.Min, draw.Src)
				sink.Unlock(r)
			}
		}
	}
	d.prev = img
}

// frame sends what changed as one frame and reports its bytes, tiles and
// copies, its encode time, and the part of it the scroll search took. It
// fails if the frame left any of the screen's changes unsent, or if the
// writer's shadow is not what the client shows.
func (d *direct) frame() (bytes, tiles, copies int, encode, search time.Duration) {
	d.tb.Helper()
	w := d.w
	if err := w.syncSize(d.ctx); err != nil {
		d.tb.Fatal(err)
	}
	if _, ok := w.collect(image.Rectangle{}); !ok {
		d.tb.Fatal("resized")
	}
	if ok, _ := w.p.StartFrame(time.Now()); !ok {
		d.tb.Fatal("the pacer holds the frame")
	}
	d.c.bytes, d.c.tiles, d.c.copies, d.c.rects = 0, 0, 0, nil
	if err := w.sendFrame(d.ctx, w.order, false); err != nil {
		d.tb.Fatal(err)
	}
	if len(d.c.errs) > 0 {
		d.tb.Fatal(d.c.errs[0])
	}
	for t, v := range w.fb.Versions(nil) {
		if v > w.sent[t] {
			d.tb.Fatalf("tile %d left unsent by the frame", t)
		}
	}
	d.shadowed()
	return d.c.bytes, d.c.tiles, d.c.copies, w.encodeTime, w.scrollTime
}

// shadowed fails unless every tile the writer's shadow holds lossless is,
// pixel for pixel, what the client shows. Lossy tiles the client decoded
// for itself.
func (d *direct) shadowed() {
	d.tb.Helper()
	sc := d.w.sc
	if sc == nil {
		return
	}
	for t, ok := range sc.valid {
		if !ok || sc.qual[t] != lossless {
			continue
		}
		r := sc.g.rect(t)
		w := 4 * r.Dx()
		for y := range r.Dy() {
			o := d.c.img.PixOffset(r.Min.X, r.Min.Y+y)
			if !bytes.Equal(sc.pix[t][y*w:(y+1)*w], d.c.img.Pix[o:o+w]) {
				d.tb.Fatalf("tile %d at %v: the shadow's row %d is not the client's", t, r, y)
			}
		}
	}
}

// exact fails unless the client shows the server's screen.
func (d *direct) exact() {
	d.tb.Helper()
	if want := serverScreen(d.s); !bytes.Equal(d.c.img.Pix, want.Pix) {
		d.tb.Fatal("the client's screen differs from the server's")
	}
}

// scrollScenes are the corpus's updates the measurement sends: a viewer
// that has the first frame is sent each of the others in turn.
var scrollScenes = []struct {
	name   string
	frames []string
}{
	{"scroll", []string{"scroll-0", "scroll-1", "scroll-2"}},
	{"scroll, skipping one", []string{"scroll-0", "scroll-2"}},
	{"drag", []string{"drag-0", "drag-1", "drag-2"}},
	{"motion", []string{"motion-0", "motion-1"}},
	{"browser, whole", []string{"desktop", "browser"}},
	{"photo, whole", []string{"desktop", "photo"}},
}

// The corpus's scrolled text costs a copy-rect and the exposed line's
// tiles, and the client draws the screen exactly; the other scenes send
// what they sent without scroll detection.
func TestScrollCorpus(t *testing.T) {
	for _, sc := range scrollScenes {
		var frames []*image.RGBA
		for _, name := range sc.frames {
			frames = append(frames, corpusFrame(t, name))
		}
		var size [2]int
		for k, on := range []bool{false, true} {
			d := newDirect(t, on)
			d.show(frames[0])
			d.frame()
			for _, f := range frames[1:] {
				d.show(f)
				b, _, _, _, _ := d.frame()
				size[k] += b
				if !d.c.lossy {
					d.exact()
				}
			}
		}
		t.Logf("%-22s %7.1f KiB a frame without scroll detection, %7.1f with", sc.name,
			float64(size[0])/1024/float64(len(frames)-1), float64(size[1])/1024/float64(len(frames)-1))
		if strings.HasPrefix(sc.name, "scroll") {
			if 8*size[1] > size[0] {
				t.Errorf("%s: %d bytes with scroll detection, %d without: want an eighth at most", sc.name, size[1], size[0])
			}
		} else if size[1] != size[0] {
			t.Errorf("%s: %d bytes with scroll detection, %d without", sc.name, size[1], size[0])
		}
	}
}

var scrollRuns = flag.Int("scroll.runs", 0, "time each corpus update this many times on one core and print the medians")

// TestScrollTimes prints each scene's bytes, messages and encode time a
// frame, with and without scroll detection: the median over
// -scroll.runs passes of each update's time, summed over the scene's
// updates and divided by their number.
func TestScrollTimes(t *testing.T) {
	if *scrollRuns == 0 {
		t.Skip("set -scroll.runs to time the corpus")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	load := func() string {
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
	t.Logf("load average at the start: %s", load())
	var out strings.Builder
	fmt.Fprintf(&out, "| Scene | Scroll detection | KiB a frame | tiles a frame | copies a frame | encode ms a frame | of which search ms |\n|---|---|---|---|---|---|---|\n")
	for _, sc := range scrollScenes {
		var frames []*image.RGBA
		for _, name := range sc.frames {
			frames = append(frames, corpusFrame(t, name))
		}
		updates := len(frames) - 1
		for _, on := range []bool{false, true} {
			times := make([][]time.Duration, updates)
			searches := make([][]time.Duration, updates)
			var size, tiles, copies int
			for pass := range *scrollRuns {
				d := newDirect(t, on)
				d.show(frames[0])
				d.frame()
				for k, f := range frames[1:] {
					d.show(f)
					b, n, c, enc, search := d.frame()
					times[k] = append(times[k], enc)
					searches[k] = append(searches[k], search)
					if pass == 0 {
						size, tiles, copies = size+b, tiles+n, copies+c
					}
				}
			}
			var sum, ssum time.Duration
			for k := range updates {
				slices.Sort(times[k])
				slices.Sort(searches[k])
				sum += times[k][len(times[k])/2]
				ssum += searches[k][len(searches[k])/2]
			}
			n := float64(updates)
			mode := "off"
			if on {
				mode = "on"
			}
			fmt.Fprintf(&out, "| %s | %s | %.1f | %.0f | %.0f | %.2f | %.3f |\n", sc.name, mode,
				float64(size)/1024/n, float64(tiles)/n, float64(copies)/n,
				float64(sum.Microseconds())/1000/n, float64(ssum.Microseconds())/1000/n)
		}
	}
	t.Logf("load average at the end: %s", load())
	t.Log("\n" + out.String())
}
