// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// A property test of the writer's shadow against the messages it writes:
// random screens, scrolls, resizes, viewports, stalls, wheels and viewers
// joining, served over links of random rates to viewers of random codecs,
// each message applied to a model of the client's screen as it is
// written, and the invariants checked at every frame-end.
//
// It runs for OCCAMY_SCROLL_PROPERTY_BUDGET (3s by default), seed after
// seed from OCCAMY_SCROLL_PROPERTY_SEED (0); OCCAMY_SCROLL_PROPERTY_ONE
// runs that seed alone, and OCCAMY_SCROLL_PROPERTY_STACKS logs the
// goroutines of a writer that did not catch up.
func TestScrollShadowProperty(t *testing.T) {
	budget := 3 * time.Second
	if s := os.Getenv("OCCAMY_SCROLL_PROPERTY_BUDGET"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		budget = d
	}
	var first uint64
	if s := os.Getenv("OCCAMY_SCROLL_PROPERTY_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first = n
	}
	start := time.Now()
	var tot propStats
	seeds := 0
	for seed := first; seeds == 0 || time.Since(start) < budget; seed++ {
		tot.add(propRun(t, seed))
		seeds++
		if t.Failed() {
			t.Logf("failed at seed %d", seed)
			break
		}
		if os.Getenv("OCCAMY_SCROLL_PROPERTY_ONE") != "" {
			break
		}
	}
	t.Logf("seeds %d, frames %d, frames with copies %d, copies %d, copies after a tile %d, frames cut after a copy %d, tile checks %d, lossy tiles %d, largest mean difference %.1f",
		seeds, tot.frames, tot.copyFrames, tot.copies, tot.late, tot.cut, tot.checked, tot.lossyTiles, tot.maxMeanDiff)
}

// propStats counts what the tees saw.
type propStats struct {
	frames, copyFrames, copies, late, cut, checked, lossyTiles int
	maxMeanDiff                                                float64
}

func (s *propStats) add(o propStats) {
	s.frames += o.frames
	s.copyFrames += o.copyFrames
	s.copies += o.copies
	s.late += o.late
	s.cut += o.cut
	s.checked += o.checked
	s.lossyTiles += o.lossyTiles
	s.maxMeanDiff = max(s.maxMeanDiff, o.maxMeanDiff)
}

// propTee sits between a writer and its link. It applies every message
// the writer writes to a model of the client's screen, from the exact
// bytes, and at each frame-end checks, on the writer's goroutine:
//
//	I1  a tile the writer believes current (version == sent) shows on
//	    the model as the framebuffer has it: exactly when the shadow
//	    holds it lossless, or the viewer takes no lossy codec;
//	I1s such a tile, if valid in the shadow, is the framebuffer's,
//	    exactly, in the shadow (whatever the codec);
//	I2  every tile valid in the shadow is on the model as the shadow
//	    holds it: exactly when lossless, alpha exactly always;
//	I3  copies stay on the screen and move vertically only;
//	I4  every tile marked sent is valid in the shadow.
type propTee struct {
	Conn
	w     *writer
	reg   *protocol.Registry
	lossy bool

	mu  sync.Mutex
	img *image.RGBA

	fails                   []string
	stats                   propStats
	frameCopies, frameTiles int
	copyCols                map[int]bool
}

func (c *propTee) fail(format string, args ...any) {
	if len(c.fails) < 20 {
		c.fails = append(c.fails, fmt.Sprintf(format, args...))
	}
}

func (c *propTee) WriteMessage(ctx context.Context, p []byte) error {
	c.mu.Lock()
	c.observe(p)
	c.mu.Unlock()
	return c.Conn.WriteMessage(ctx, p)
}

func (c *propTee) observe(p []byte) {
	m, err := protocol.Decode(p, c.reg, protocol.Limits{MaxMessageSize: 1 << 30})
	if err != nil {
		c.fail("decode: %v", err)
		return
	}
	switch m := m.(type) {
	case *protocol.Resize:
		c.img = image.NewRGBA(image.Rect(0, 0, int(m.Width), int(m.Height)))
	case *protocol.Tile:
		r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
		if !r.In(c.img.Rect) {
			c.fail("tile %v outside %v", r, c.img.Rect)
			return
		}
		if err := decodeTile(c.img.SubImage(r).(*image.RGBA), m.Codec, m.Payload); err != nil {
			c.fail("tile: %v", err)
		}
		if m.Codec == protocol.CodecWebP || m.Codec == protocol.CodecJPEG {
			c.stats.lossyTiles++
		}
		c.frameTiles++
	case *protocol.CopyRect:
		dst := image.Rect(int(m.DstX), int(m.DstY), int(m.DstX)+int(m.W), int(m.DstY)+int(m.H))
		src := dst.Sub(dst.Min).Add(image.Pt(int(m.SrcX), int(m.SrcY)))
		if m.SrcX != m.DstX || m.SrcY == m.DstY || dst.Empty() {
			c.fail("I3: copy %+v", *m)
		}
		if !dst.In(c.img.Rect) || !src.In(c.img.Rect) {
			c.fail("I3: copy %+v outside %v", *m, c.img.Rect)
			return
		}
		draw.Draw(c.img, dst, c.img, src.Min, draw.Src)
		c.frameCopies++
		if c.frameTiles > 0 {
			c.stats.late++
		}
		if c.copyCols == nil {
			c.copyCols = map[int]bool{}
		}
		if c.w != nil && c.w.g.tile > 0 {
			for x := dst.Min.X / c.w.g.tile; x <= (dst.Max.X-1)/c.w.g.tile; x++ {
				c.copyCols[x] = true
			}
		}
	case *protocol.FrameEnd:
		c.check(m.FrameID)
		c.frameCopies, c.frameTiles = 0, 0
		clear(c.copyCols)
	}
}

// diff compares the pixels of r (row by row at r's width, in a) with the
// model's; it reports whether they match (exactly, or else within a
// lossy codec's tolerance with alpha exact) and the mean difference.
func (c *propTee) diff(a []byte, r image.Rectangle, exact bool) (bool, float64) {
	w := 4 * r.Dx()
	var sum, n float64
	same := true
	for y := range r.Dy() {
		o := c.img.PixOffset(r.Min.X, r.Min.Y+y)
		ra, rb := a[y*w:(y+1)*w], c.img.Pix[o:o+w]
		for i := range ra {
			d := int(ra[i]) - int(rb[i])
			if d != 0 && (exact || i%4 == 3) {
				same = false
			}
			if i%4 != 3 {
				sum += float64(max(d, -d))
				n++
			}
		}
	}
	mean := sum / max(n, 1)
	if !exact && mean > 65 {
		same = false
	}
	return same, mean
}

func (c *propTee) check(id uint32) {
	w := c.w
	if w == nil || w.sc == nil {
		return
	}
	sc := w.sc
	c.stats.frames++
	if c.frameCopies > 0 {
		c.stats.copyFrames++
		c.stats.copies += c.frameCopies
	}
	g := w.g
	if sc.g != g {
		c.fail("frame %d: shadow grid %+v, writer's %+v", id, sc.g, g)
		return
	}
	if g.cols == 0 {
		return
	}
	if c.img.Rect != g.bounds {
		c.fail("frame %d: model %v, writer's %v", id, c.img.Rect, g.bounds)
		return
	}
	buf := make([]byte, 4*g.tile*g.tile)
	leftover := false
	for t := range w.sent {
		r := g.rect(t)
		got, v := w.fb.ReadTile(t, buf)
		if got != r {
			return // resized meanwhile: the next frame-end checks
		}
		if v > w.sent[t] && c.copyCols[t%g.cols] {
			leftover = true
		}
		if w.sent[t] != 0 && !sc.valid[t] {
			c.fail("I4 frame %d: tile %d %v sent at %d, invalid in the shadow", id, t, r, w.sent[t])
		}
		exact := !c.lossy || (sc.valid[t] && sc.qual[t] == lossless)
		if sc.valid[t] {
			ok, mean := c.diff(sc.pix[t], r, exact)
			if !ok {
				c.fail("I2 frame %d: tile %d %v: the shadow differs from the client (mean %.1f, quality %d)", id, t, r, mean, sc.qual[t])
			}
			if !exact {
				c.stats.maxMeanDiff = max(c.stats.maxMeanDiff, mean)
			}
		}
		if v == w.sent[t] && v != 0 {
			c.stats.checked++
			if ok, mean := c.diff(buf, r, exact); !ok {
				c.fail("I1 frame %d: tile %d %v at version %d: the client differs from the server (mean %.1f)", id, t, r, v, mean)
			}
			if sc.valid[t] {
				w4 := 4 * r.Dx()
				for k := range r.Dy() * w4 {
					if sc.pix[t][k] != buf[k] {
						c.fail("I1s frame %d: tile %d %v at version %d: the shadow differs from the server at byte %d", id, t, r, v, k)
						break
					}
				}
			}
		}
	}
	if c.frameCopies > 0 && leftover {
		c.stats.cut++
	}
}

// propViewer is a viewer served by a writer built as Serve builds it,
// over a link, with a tee in front of the link and a test client at its
// far end.
type propViewer struct {
	v      *session.Viewer
	cl     *client
	tee    *propTee
	link   *link
	done   chan error
	seq    uint32
	hidden bool
	vp     bool
	stall  bool
	lossy  bool
}

func propServe(t *testing.T, ctx context.Context, v *session.Viewer, hello protocol.Hello, rate float64, delay time.Duration, lossy bool) *propViewer {
	l := newLink(rate, delay)
	cl := newClient(l.client())
	go cl.run(ctx)
	agreed := protocol.Agree(hello.Capabilities, protocol.Capabilities{Codecs: encoder.Codecs(), Terminal: 1})
	limits := agreed.Limits()
	enc := newEncoder(agreed.Codecs)
	if enc == nil {
		t.Fatal("no encoder")
	}
	if err := v.View(hello.Visible, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	tee := &propTee{Conn: l.server(), reg: protocol.ServerMessages(), img: &image.RGBA{}, lossy: lossy}
	welcome := protocol.Welcome{ViewerID: uint32(v.ID()), Role: role(v.Role()), Class: protocol.ClassGraphical, SessionID: "s", Capabilities: agreed} // #nosec G115 -- test
	if err := tee.WriteMessage(ctx, welcome.Append(nil)); err != nil {
		t.Fatal(err)
	}
	wr := newWriter(tee, v, pacer.New(pacer.Config{Window: int(agreed.FrameWindow)}), enc, limits, slog.New(slog.DiscardHandler))
	wr.sc = &scroller{}
	tee.w = wr
	pv := &propViewer{v: v, cl: cl, tee: tee, link: l, done: make(chan error, 1), hidden: !hello.Visible, lossy: lossy}
	cctx, cancel := context.WithCancel(ctx)
	read := make(chan error, 1)
	go func() {
		err := wr.read(cctx, limits)
		if !errors.Is(err, session.ErrEnded) {
			cancel()
		}
		read <- err
	}()
	go func() {
		err := wr.run(cctx)
		cancel()
		<-read
		pv.done <- err
	}()
	return pv
}

func (pv *propViewer) send(t *testing.T, m interface{ Append([]byte) []byte }) {
	t.Helper()
	pv.cl.send(t, m)
}

func (pv *propViewer) setStall(t *testing.T, on bool) {
	pv.cl.mu.Lock()
	pv.cl.noAcks = on
	var last uint32
	if n := len(pv.cl.frames); n > 0 {
		last = pv.cl.frames[n-1].id
	}
	pv.cl.mu.Unlock()
	pv.stall = on
	if !on && last != 0 {
		pv.send(t, protocol.FrameAck{FrameID: last, DecodeTime: 500})
	}
}

// propShift paints r shifted by dy as a remote that sends damage does:
// the rows that stay move by dy and the exposed rows are new content,
// with a copy-rect or as one drawing operation.
func propShift(b *tapped, s *session.Session, r image.Rectangle, dy int, fresh func(image.Rectangle), useCopy bool) {
	if dy == 0 || r.Empty() {
		return
	}
	if abs(dy) >= r.Dy() {
		fresh(r)
		return
	}
	var stay, exposed image.Rectangle
	var src image.Point
	if dy > 0 { // content moves up: rows dy below come to r's top
		stay = image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y-dy)
		src = image.Pt(r.Min.X, r.Min.Y+dy)
		exposed = image.Rect(r.Min.X, r.Max.Y-dy, r.Max.X, r.Max.Y)
	} else {
		stay = image.Rect(r.Min.X, r.Min.Y-dy, r.Max.X, r.Max.Y)
		src = r.Min
		exposed = image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y-dy)
	}
	if useCopy {
		b.Sink().CopyRect(stay, src)
		fresh(exposed)
		return
	}
	cur := serverScreen(s)
	next := image.NewRGBA(r)
	draw.Draw(next, stay, cur, src, draw.Src)
	img := b.Sink().Lock(r)
	draw.Draw(img, stay, next, stay.Min, draw.Src)
	b.Sink().Unlock(r)
	fresh(exposed)
}

// propRun runs one seed's session.
func propRun(t *testing.T, seed uint64) (stats propStats) {
	synctest.Test(t, func(t *testing.T) {
		rng := rand.New(rand.NewPCG(seed, 115))
		w, h := 48+rng.IntN(300), 48+rng.IntN(300)
		tile := []int{16, 32, 64, 64}[rng.IntN(4)]
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		ctx, cancel := context.WithCancel(context.Background())
		s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: tile, Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		ph := photo(t)
		rate := func() float64 { return []float64{0.2e6, 1e6, 5e6, 50e6}[rng.IntN(4)] }
		delay := func() time.Duration { return time.Duration(1+rng.IntN(30)) * time.Millisecond }
		var vs []*propViewer
		for i := range 1 + rng.IntN(3) {
			v := owner
			if i > 0 {
				v, err = s.Join(session.RoleViewer)
				if err != nil {
					t.Fatal(err)
				}
			}
			caps := rawOnly
			lossy := false
			switch rng.IntN(4) {
			case 0:
				caps = protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecDeflate}}
			case 1:
				caps = protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}}
				lossy = true
			}
			vs = append(vs, propServe(t, ctx, v, protocol.Hello{Visible: true, Capabilities: caps}, rate(), delay(), lossy))
		}
		defer func() {
			cancel()
			s.Close()
			for _, pv := range vs {
				pv.link.close()
				<-pv.done
			}
		}()
		time.Sleep(100 * time.Millisecond)
		for b.Sink() == nil {
			time.Sleep(time.Millisecond)
		}
		screen := func() image.Rectangle { return image.Rect(0, 0, w, h) }
		randRect := func() image.Rectangle {
			x0, y0 := rng.IntN(w), rng.IntN(h)
			r := image.Rect(x0, y0, x0+1+rng.IntN(w), y0+1+rng.IntN(h)).Intersect(screen())
			if rng.IntN(3) == 0 { // aligned to tiles
				r = image.Rect(r.Min.X/tile*tile, r.Min.Y/tile*tile, min(w, (r.Max.X+tile-1)/tile*tile), min(h, (r.Max.Y+tile-1)/tile*tile))
			}
			return r
		}
		pageSeed := 1
		fresh := func(r image.Rectangle) {
			if r.Empty() {
				return
			}
			switch rng.IntN(6) {
			case 0:
				paint(b, r, image.NewUniform(color.RGBA{byte(rng.IntN(256)), byte(rng.IntN(256)), 9, 0xff}))
			case 1:
				off := image.Pt(rng.IntN(200), rng.IntN(200))
				img := b.Sink().Lock(r)
				draw.Draw(img, r, ph, off, draw.Src)
				b.Sink().Unlock(r)
			default:
				pageSeed++
				pageSink(b, r, pageSeed, rng.IntN(1000))
			}
		}
		randDY := func(n int) int {
			var dy int
			if rng.IntN(3) == 0 {
				dy = 1 + rng.IntN(max(1, n))
			} else {
				dy = 1 + rng.IntN(40)
			}
			if rng.IntN(2) == 0 {
				dy = -dy
			}
			return dy
		}
		// Keeps resized screens above the test backend's swatch.
		const swatchMin = backendtest.SwatchSize + 1
		fresh(screen())
		time.Sleep(50 * time.Millisecond)
		for range 40 + rng.IntN(60) {
			switch k := rng.IntN(23); {
			case k < 2:
				fresh(randRect())
			case k < 3: // stripes and blank rows, which must not vote
				r := randRect()
				period := 1 + rng.IntN(8)
				img := b.Sink().Lock(r)
				for y := r.Min.Y; y < r.Max.Y; y++ {
					c := color.RGBA{0xfa, 0xfa, 0xf5, 0xff}
					if (y/period)%2 == 0 {
						c = color.RGBA{0x20, 0x20, byte(y % 3), 0xff}
					}
					for x := r.Min.X; x < r.Max.X; x++ {
						img.SetRGBA(x, y, c)
					}
				}
				b.Sink().Unlock(r)
			case k < 6:
				propShift(b, s, screen(), randDY(h), fresh, rng.IntN(2) == 0)
			case k < 10:
				r := randRect()
				propShift(b, s, r, randDY(r.Dy()), fresh, rng.IntN(2) == 0)
			case k < 11:
				switch rng.IntN(3) {
				case 0: // the same size
				case 1: // the same grid
					nw := max(48, (w+tile-1)/tile*tile-rng.IntN(min(tile, w-swatchMin)))
					nh := max(48, (h+tile-1)/tile*tile-rng.IntN(min(tile, h-swatchMin)))
					w, h = nw, nh
				default:
					w, h = 48+rng.IntN(300), 48+rng.IntN(300)
				}
				b.Sink().Resize(w, h)
				fresh(screen())
			case k < 12:
				pv := vs[rng.IntN(len(vs))]
				pv.hidden = !pv.hidden
				pv.vp = false
				pv.send(t, protocol.View{Visible: !pv.hidden})
			case k < 13:
				pv := vs[rng.IntN(len(vs))]
				pv.vp = !pv.vp
				m := protocol.View{Visible: !pv.hidden}
				if pv.vp {
					m.X, m.Y = uint16(rng.IntN(w/2)), uint16(rng.IntN(h/2))     // #nosec G115 -- test
					m.W, m.H = uint16(1+rng.IntN(w/2)), uint16(1+rng.IntN(h/2)) // #nosec G115 -- test
				}
				pv.send(t, m)
			case k < 14:
				pv := vs[rng.IntN(len(vs))]
				pv.setStall(t, !pv.stall)
			case k < 15: // resume acks and draw nothing: the strip must come
				for _, pv := range vs {
					if pv.stall {
						pv.setStall(t, false)
					}
				}
				time.Sleep(time.Duration(rng.IntN(200)) * time.Millisecond)
			case k < 18: // a wheel, whose effect is a scroll a little later
				pv := vs[rng.IntN(len(vs))]
				pv.seq++
				pv.send(t, protocol.Wheel{Seq: pv.seq, DY: 1})
				time.Sleep(time.Duration(rng.IntN(12)) * time.Millisecond)
				if rng.IntN(2) == 0 {
					propShift(b, s, screen(), randDY(h), fresh, rng.IntN(2) == 0)
				} else {
					r := randRect()
					propShift(b, s, r, randDY(r.Dy()), fresh, rng.IntN(2) == 0)
				}
			case k < 20: // a burst of small scrolls landing mid-frame
				r := screen()
				if rng.IntN(2) == 0 {
					r = randRect()
				}
				for range 2 + rng.IntN(6) {
					propShift(b, s, r, randDY(8), fresh, rng.IntN(2) == 0)
					time.Sleep(time.Duration(rng.IntN(4)) * time.Millisecond)
				}
			case k < 21: // a viewer joins mid-scroll
				if len(vs) < 4 {
					v, err := s.Join(session.RoleViewer)
					if err != nil {
						t.Fatal(err)
					}
					caps, lossy := rawOnly, false
					if rng.IntN(3) == 0 {
						caps, lossy = protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}}, true
					}
					vs = append(vs, propServe(t, ctx, v, protocol.Hello{Visible: true, Capabilities: caps}, rate(), delay(), lossy))
				}
				propShift(b, s, screen(), randDY(h), fresh, rng.IntN(2) == 0)
			default:
			}
			time.Sleep(time.Duration(rng.IntN(60)) * time.Millisecond)
		}
		// Catch up: every viewer visible, whole screen, acking.
		for _, pv := range vs {
			if pv.stall {
				pv.setStall(t, false)
			}
			pv.send(t, protocol.View{Visible: true})
			pv.hidden, pv.vp = false, false
		}
		want := serverScreen(s)
		caught := func() bool {
			for _, pv := range vs {
				got := pv.cl.screen()
				if got.Rect != want.Rect || (!pv.lossy && string(got.Pix) != string(want.Pix)) {
					return false
				}
				pv.tee.mu.Lock()
				same := string(pv.tee.img.Pix) == string(got.Pix)
				pv.tee.mu.Unlock()
				if !same {
					return false
				}
			}
			return true
		}
		waited := 0
		for ; waited < 60 && !caught(); waited++ {
			time.Sleep(time.Second)
			synctest.Wait()
		}
		if waited > 3 {
			t.Logf("seed %d: caught up after %d s", seed, waited)
		}
		for i, pv := range vs {
			pv.tee.mu.Lock()
			fails := append([]string(nil), pv.tee.fails...)
			model := pv.tee.img
			stats.add(pv.tee.stats)
			pv.tee.mu.Unlock()
			for _, f := range fails {
				t.Errorf("seed %d viewer %d (tile %d, lossy %v): %s", seed, i, tile, pv.lossy, f)
			}
			got := pv.cl.screen()
			if got.Rect != model.Rect || string(got.Pix) != string(model.Pix) {
				t.Errorf("seed %d viewer %d: the test client differs from the tee's model", seed, i)
			}
			if !pv.lossy && (got.Rect != want.Rect || string(got.Pix) != string(want.Pix)) {
				t.Errorf("seed %d viewer %d: after catching up the client differs from the server (client %v, server %v)", seed, i, got.Rect, want.Rect)
				propDump(t, pv, got, want)
			}
			pv.cl.mu.Lock()
			if len(pv.cl.errs) > 0 || pv.cl.outside {
				t.Errorf("seed %d viewer %d: client errors %v, outside %v", seed, i, pv.cl.errs, pv.cl.outside)
			}
			pv.cl.mu.Unlock()
		}
	})
	return stats
}

// propDump logs the state of a writer whose client did not catch up.
func propDump(t *testing.T, pv *propViewer, got, want *image.RGBA) {
	wr := pv.tee.w
	vis, vp := wr.v.Visibility()
	select {
	case err := <-pv.done:
		t.Logf("  writer ended: %v", err)
		pv.done <- err
	default:
		t.Logf("  writer running")
		if os.Getenv("OCCAMY_SCROLL_PROPERTY_STACKS") != "" {
			sb := make([]byte, 1<<20)
			n := runtime.Stack(sb, true)
			t.Logf("%s", sb[:n])
		}
	}
	t.Logf("  writer: g %+v, visible %v vp %v, in %+v, frame %d, stall %v", wr.g, vis, vp, wr.in, wr.frame, pv.stall)
	buf := make([]byte, 4*wr.g.tile*wr.g.tile)
	for tt := range wr.sent {
		r, v := wr.fb.ReadTile(tt, buf)
		if got.Rect == want.Rect && !sameRect(got, want, r) {
			t.Logf("  tile %d %v: sent %d version %d valid %v quality %d", tt, r, wr.sent[tt], v, wr.sc.valid[tt], wr.sc.qual[tt])
		}
	}
}
