// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of the graphics pipeline: against rdptest's server, as the
// dynamic channels' are, and on a client fed GFX messages directly.

const gfxID = 9 // the graphics channel's id in the tests

var (
	red   = color.RGBA{R: 0xff, A: 0xff}
	green = color.RGBA{G: 0xff, A: 0xff}
	blue  = color.RGBA{B: 0xff, A: 0xff}
	black = color.RGBA{A: 0xff}
)

// gradient is an image of w×h whose channels differ from each other and
// across it.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{byte(x * 255 / max(1, w-1)), byte(y * 255 / max(1, h-1)), byte((x*7 + y*3) % 256), 0xff})
		}
	}
	return img
}

// gfxTest is a client whose graphics pipeline is open and confirmed,
// which a test feeds messages as the channel would.
type gfxTest struct {
	c     *Client
	clock *fakeClock
	out   *screen
	enc   rdptest.ZGFX
}

func newGFXTest(t testing.TB, w, h int) *gfxTest {
	t.Helper()
	c, clock := testClock(w, h)
	c.cfg.GFX = true
	c.chio.kick = make(chan struct{}, 1)
	c.dynCh = &vchannel{name: "drdynvc", id: 1005}
	for _, p := range [][]byte{rdptest.DVCCapsPDU(3), rdptest.DVCCreateRequest(gfxID, rdptest.Graphics)} {
		if err := c.drdynvcMessage(nil, p, false); err != nil {
			t.Fatal(err)
		}
	}
	g := &gfxTest{c: c, clock: clock, out: &screen{}}
	g.out.Resize(w, h)
	g.must(t, rdptest.CapsConfirm(rdptest.CapVersion107, 0))
	return g
}

// send applies the PDUs as one message, stored.
func (g *gfxTest) send(pdus ...[]byte) error {
	msg := slices.Concat(pdus...)
	if len(msg) <= zgfxSegment {
		return g.c.gfxMessage(g.out, g.enc.Single(msg, rdptest.SegStored))
	}
	return g.c.gfxMessage(g.out, g.enc.Multipart(msg, zgfxSegment, rdptest.SegStored))
}

func (g *gfxTest) must(t testing.TB, pdus ...[]byte) {
	t.Helper()
	if err := g.send(pdus...); err != nil {
		t.Fatal(err)
	}
}

// acks returns the frame acknowledgements that wait for the writer, and
// takes them.
func (g *gfxTest) acks(t testing.TB) []rdptest.FrameAck {
	t.Helper()
	g.c.chio.mu.Lock()
	pdus := g.c.dvc.acks
	g.c.dvc.acks = nil
	g.c.chio.mu.Unlock()
	var acks []rdptest.FrameAck
	for _, p := range pdus {
		acks = append(acks, parseAck(t, p))
	}
	return acks
}

// parseAck parses a DRDYNVC Data PDU of a frame acknowledgement.
func parseAck(t testing.TB, p []byte) rdptest.FrameAck {
	t.Helper()
	d, err := rdptest.ParseDVC(p)
	if err != nil || d.Cmd != rdptest.DVCData || d.Channel != gfxID {
		t.Fatalf("%+v, %v: want data on the graphics channel", d, err)
	}
	return ackOf(t, d.Data)
}

// ackOf parses a message of a frame acknowledgement.
func ackOf(t testing.TB, msg []byte) rdptest.FrameAck {
	t.Helper()
	gp, err := rdptest.ParseGFX(msg)
	if err != nil || len(gp) != 1 || gp[0].Cmd != rdptest.GFXFrameAcknowledge {
		t.Fatalf("%+v, %v: want a frame acknowledgement", gp, err)
	}
	a, err := rdptest.ParseFrameAck(gp[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// surface creates the surface id of w×h and maps it at at.
func surfacePDUs(id int, w, h int, at image.Point) [][]byte {
	return [][]byte{rdptest.CreateSurface(id, w, h, rdptest.PixelXRGB), rdptest.MapSurface(id, at)}
}

// gfxFrame wraps PDUs in the frame id.
func gfxFrame(id uint32, pdus ...[]byte) []byte {
	return slices.Concat(append(append([][]byte{rdptest.StartFrame(id)}, pdus...), rdptest.EndFrame(id))...)
}

// wantRect checks that r of the screen is c.
func wantRect(t testing.TB, img *image.RGBA, r image.Rectangle, c color.RGBA) {
	t.Helper()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if got := img.RGBAAt(x, y); got != c {
				t.Fatalf("pixel (%d, %d) is %v, want %v", x, y, got, c)
			}
		}
	}
}

// wantImage checks that the screen at p shows img.
func wantImage(t testing.TB, scr *image.RGBA, p image.Point, img *image.RGBA) {
	t.Helper()
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			want := img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			want.A = 0xff
			if got := scr.RGBAAt(p.X+x, p.Y+y); got != want {
				t.Fatalf("pixel (%d, %d) is %v, want %v", p.X+x, p.Y+y, got, want)
			}
		}
	}
}

// A frame's fills and bitmaps reach the output at its end, and the
// client acknowledges it after.
func TestGFXFrame(t *testing.T) {
	g := newGFXTest(t, 256, 128)
	img := gradient(40, 30)
	g.must(t, surfacePDUs(1, 256, 128, image.Point{})...)
	g.must(t, rdptest.StartFrame(7),
		rdptest.SolidFill(1, red, image.Rect(0, 0, 100, 100), image.Rect(200, 100, 300, 300)),
		rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, image.Rect(100, 10, 140, 40), rdptest.Uncompressed(img)),
		rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(150, 50, 190, 80), rdptest.Planar(img)))
	if a := g.acks(t); len(a) != 0 {
		t.Fatalf("acknowledged %+v before the frame ended", a)
	}
	if scr := g.out.snapshot(); scr.RGBAAt(5, 5) != black {
		t.Fatalf("the frame reached the output before its end: %v", scr.RGBAAt(5, 5))
	}
	g.must(t, rdptest.EndFrame(7))
	if a := g.acks(t); len(a) != 1 || a[0] != (rdptest.FrameAck{QueueDepth: 0, FrameID: 7, TotalFramesDecoded: 1}) {
		t.Fatalf("acknowledgements %+v", a)
	}
	scr := g.out.snapshot()
	wantRect(t, scr, image.Rect(0, 0, 100, 100), red)
	wantRect(t, scr, image.Rect(200, 100, 256, 128), red) // clipped to the surface
	wantRect(t, scr, image.Rect(0, 100, 200, 128), black)
	wantImage(t, scr, image.Pt(100, 10), img)
	wantImage(t, scr, image.Pt(150, 50), img)
	st := g.c.Stats()
	if st.Bitmaps["gfx uncompressed"] != 1 || st.Bitmaps["gfx planar"] != 1 || st.GFX["end frame"] != 1 || st.GFX["solid fill"] != 1 {
		t.Errorf("stats %+v", st)
	}
	// The next frame counts on.
	g.must(t, gfxFrame(8, rdptest.SolidFill(1, blue, image.Rect(0, 0, 1, 1))))
	if a := g.acks(t); len(a) != 1 || a[0].FrameID != 8 || a[0].TotalFramesDecoded != 2 {
		t.Fatalf("acknowledgements %+v", a)
	}
}

// Outside a frame, what changed reaches the output with the message.
func TestGFXOutsideFrame(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.must(t, append(surfacePDUs(1, 64, 64, image.Point{}), rdptest.SolidFill(1, green, image.Rect(0, 0, 8, 8)))...)
	wantRect(t, g.out.snapshot(), image.Rect(0, 0, 8, 8), green)
	if a := g.acks(t); len(a) != 0 {
		t.Fatalf("acknowledgements %+v outside a frame", a)
	}
}

// Only the cells that changed go to the output, and an unmapped
// surface's changes not at all, until it is mapped.
func TestGFXFlushCells(t *testing.T) {
	g := newGFXTest(t, 256, 256)
	g.must(t, surfacePDUs(1, 256, 256, image.Point{})...)
	g.must(t, rdptest.CreateSurface(2, 64, 64, rdptest.PixelXRGB), rdptest.SolidFill(2, blue, image.Rect(0, 0, 64, 64)))
	draws := g.out.draws
	g.must(t, gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(70, 70, 71, 71), image.Rect(130, 0, 140, 1))))
	if n := g.out.draws - draws; n != 2 {
		t.Errorf("%d draws for two cells", n)
	}
	scr := g.out.snapshot()
	wantRect(t, scr, image.Rect(70, 70, 71, 71), red)
	wantRect(t, scr, image.Rect(0, 0, 64, 64), black) // surface 2 is not mapped
	g.must(t, rdptest.MapSurface(2, image.Pt(192, 192)))
	wantRect(t, g.out.snapshot(), image.Rect(192, 192, 256, 256), blue)
}

// A surface goes to the output in strips of at most drawStrip pixels.
func TestGFXFlushStrips(t *testing.T) {
	g := newGFXTest(t, 2048, 512)
	rec := &lockRecorder{}
	g.out = nil
	g.c.gfxMessage(nil, g.enc.Single(slices.Concat(surfacePDUs(1, 2048, 512, image.Point{})...), rdptest.SegStored))
	g.c.gfxFlush(rec)
	n := 0
	for _, r := range rec.locks {
		if r.Dx()*r.Dy() > drawStrip {
			t.Errorf("a lock of %v", r)
		}
		n += r.Dx() * r.Dy()
	}
	if n != 2048*512 {
		t.Errorf("%d pixels drawn of %d", n, 2048*512)
	}
}

// lockRecorder is an Output that records its locks.
type lockRecorder struct {
	sizeRecorder
	locks  []image.Rectangle
	copies [][2]image.Rectangle
}

func (l *lockRecorder) Lock(r image.Rectangle) *image.RGBA {
	l.locks = append(l.locks, r)
	return l.sizeRecorder.Lock(r)
}

func (l *lockRecorder) CopyRect(dst image.Rectangle, src image.Point) {
	l.copies = append(l.copies, [2]image.Rectangle{dst, {Min: src, Max: src.Add(dst.Size())}})
}

// Without an output, during the connection sequence, the changes wait
// for the next message.
func TestGFXWithoutOutput(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	pdus := append(surfacePDUs(1, 64, 64, image.Point{}), rdptest.SolidFill(1, green, image.Rect(0, 0, 8, 8)))
	if err := g.c.gfxMessage(nil, g.enc.Single(slices.Concat(pdus...), rdptest.SegStored)); err != nil {
		t.Fatal(err)
	}
	g.must(t, rdptest.SolidFill(1, red, image.Rect(60, 60, 64, 64)))
	scr := g.out.snapshot()
	wantRect(t, scr, image.Rect(0, 0, 8, 8), green)
	wantRect(t, scr, image.Rect(60, 60, 64, 64), red)
}

// copyRef copies src of img to dst through a copy of the pixels, as a
// reference for overlapping copies.
func copyRef(img *image.RGBA, src image.Rectangle, dst image.Point) {
	tmp := image.NewRGBA(src)
	for y := src.Min.Y; y < src.Max.Y; y++ {
		copy(tmp.Pix[tmp.PixOffset(src.Min.X, y):][:4*src.Dx()], img.Pix[img.PixOffset(src.Min.X, y):])
	}
	for y := range src.Dy() {
		copy(img.Pix[img.PixOffset(dst.X, dst.Y+y):][:4*src.Dx()], tmp.Pix[tmp.PixOffset(src.Min.X, src.Min.Y+y):])
	}
}

// Copies within a surface that overlap themselves, each way, copy what
// was there before the copy; on the output they go as copies.
func TestGFXSurfaceToSurfaceOverlap(t *testing.T) {
	src := image.Rect(20, 20, 100, 90)
	for _, d := range []image.Point{{0, 7}, {0, -7}, {7, 0}, {-7, 0}, {5, 5}, {-5, -5}, {5, -5}, {-5, 5}} {
		t.Run(fmt.Sprint(d), func(t *testing.T) {
			g := newGFXTest(t, 128, 128)
			img := gradient(128, 128)
			g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
			g.must(t, rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, img.Rect, rdptest.Uncompressed(img)))
			copies := g.out.copies
			g.must(t, gfxFrame(1, rdptest.SurfaceToSurface(1, 1, src, src.Min.Add(d))))
			copyRef(img, src, src.Min.Add(d))
			wantImage(t, g.out.snapshot(), image.Point{}, img)
			s := g.c.gfx.surfaces[1]
			mirror := &image.RGBA{Pix: s.pix, Stride: 4 * s.w, Rect: s.bounds()}
			wantImage(t, mirror, image.Point{}, img)
			if g.out.copies != copies+1 {
				t.Errorf("%d copies on the output, want 1", g.out.copies-copies)
			}
		})
	}
}

// copyPixels within one surface copies rows bottom-up when it copies
// down, top-down otherwise.
func TestCopyPixelsDirection(t *testing.T) {
	for _, dy := range []int{1, -1, 0} {
		s := newSurface(1, 4, 8)
		for y := range 8 {
			fill(s, image.Rect(0, y, 4, y+1), [4]byte{byte(y), 0, 0, 0xff})
		}
		copyPixels(s, image.Pt(0, 2+dy), s, image.Rect(0, 2, 4, 6))
		for y := range 4 {
			if got := s.pix[s.offset(1, 2+dy+y)]; int(got) != 2+y {
				t.Errorf("copy by %d: row %d is %d, want %d", dy, 2+dy+y, got, 2+y)
			}
		}
	}
}

// A copy goes to the output as a copy only when the output has what it
// copies: within one mapped surface, on the desktop, where no other
// surface is mapped. Otherwise it is drawn.
func TestGFXCopyRectWhen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup [][]byte
		copy  []byte
		rect  bool
	}{
		{"within a surface", surfacePDUs(1, 128, 128, image.Point{}),
			rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(64, 64)), true},
		{"between surfaces", append(surfacePDUs(1, 64, 128, image.Point{}), surfacePDUs(2, 64, 128, image.Pt(64, 0))...),
			rdptest.SurfaceToSurface(1, 2, image.Rect(0, 0, 32, 32), image.Pt(10, 10)), false},
		{"off the desktop", surfacePDUs(1, 128, 128, image.Pt(64, 0)),
			rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(90, 0)), false},
		{"under another surface", append(surfacePDUs(1, 128, 128, image.Point{}), surfacePDUs(2, 16, 16, image.Pt(100, 100))...),
			rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(90, 90)), false},
		{"under another surface where it is mapped", append(surfacePDUs(1, 128, 128, image.Point{}), surfacePDUs(2, 16, 16, image.Pt(100, 100))...),
			rdptest.SurfaceToSurface(1, 1, image.Rect(64, 64, 80, 80), image.Pt(96, 96)), false},
		{"unmapped", [][]byte{rdptest.CreateSurface(1, 128, 128, rdptest.PixelXRGB)},
			rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(64, 64)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGFXTest(t, 128, 128)
			g.must(t, tc.setup...)
			g.must(t, gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(0, 0, 32, 32))))
			copies := g.out.copies
			g.must(t, gfxFrame(2, tc.copy))
			if got := g.out.copies > copies; got != tc.rect {
				t.Errorf("copied on the output: %v, want %v", got, tc.rect)
			}
			if tc.rect {
				wantRect(t, g.out.snapshot(), image.Rect(64, 64, 96, 96), red)
			}
		})
	}
}

// Where a surface leaves the output, deleted, replaced or mapped
// elsewhere, or where a bitmap update draws under a mapped surface, the
// surfaces under it draw again: the output under surface 1 is its
// pixels once more, black, and a copy within it sent as CopyRect copies
// black, not what was drawn over it. Surface 1 lies a cell from the
// output's origin, so that where it draws again is found from where it
// is mapped.
func TestGFXCopyRectExposed(t *testing.T) {
	at := image.Pt(64, 0)
	over := image.Rect(0, 0, 32, 32).Add(at) // surface 1's (0,0)-(32,32) on the output
	for _, tc := range []struct {
		name  string
		cover [][]byte // red over the output's over
		leave func(g *gfxTest) error
	}{
		{"deleted", surfacePDUs(2, 32, 32, at), func(g *gfxTest) error {
			return g.send(rdptest.DeleteSurface(2))
		}},
		{"replaced", surfacePDUs(2, 32, 32, at), func(g *gfxTest) error {
			return g.send(rdptest.CreateSurface(2, 16, 16, rdptest.PixelXRGB))
		}},
		{"mapped elsewhere", surfacePDUs(2, 32, 32, at), func(g *gfxTest) error {
			return g.send(rdptest.MapSurface(2, image.Pt(200, 0)))
		}},
		{"a bitmap update", nil, func(g *gfxTest) error {
			b := &bitmapData{dest: over, width: 32, height: 32, bpp: 32, data: bytes.Repeat([]byte{0, 0, 0xff, 0xff}, 32*32)}
			if err := g.c.drawBitmap(g.out, b); err != nil {
				return err
			}
			wantRect(t, g.out.snapshot(), over, red)
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGFXTest(t, 256, 128)
			g.must(t, surfacePDUs(1, 128, 128, at)...)
			if tc.cover != nil {
				g.must(t, tc.cover...)
				g.must(t, gfxFrame(1, rdptest.SolidFill(2, red, image.Rect(0, 0, 32, 32))))
				wantRect(t, g.out.snapshot(), over, red)
			}
			if err := tc.leave(g); err != nil {
				t.Fatal(err)
			}
			copies := g.out.copies
			g.must(t, gfxFrame(2, rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(64, 64))))
			if g.out.copies == copies {
				t.Error("the copy was not sent as CopyRect")
			}
			scr := g.out.snapshot()
			wantRect(t, scr, over, black)
			wantRect(t, scr, image.Rect(64, 64, 96, 96).Add(at), black)
		})
	}
}

// A copy reads what the frame drew before it: the output gets what the
// surface changed before the copy.
func TestGFXCopyAfterFill(t *testing.T) {
	g := newGFXTest(t, 128, 128)
	g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
	g.must(t, gfxFrame(1,
		rdptest.SolidFill(1, red, image.Rect(0, 0, 16, 16)),
		rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 16, 16), image.Pt(100, 100), image.Pt(50, 0)),
		rdptest.SolidFill(1, blue, image.Rect(0, 0, 8, 8))))
	scr := g.out.snapshot()
	wantRect(t, scr, image.Rect(100, 100, 116, 116), red)
	wantRect(t, scr, image.Rect(50, 0, 66, 16), red)
	wantRect(t, scr, image.Rect(0, 0, 8, 8), blue)
	wantRect(t, scr, image.Rect(8, 8, 16, 16), red)
}

// Copies between surfaces and to an unmapped surface reach the output
// when mapped.
func TestGFXCopyBetweenSurfaces(t *testing.T) {
	g := newGFXTest(t, 128, 128)
	g.must(t, rdptest.CreateSurface(3, 32, 32, rdptest.PixelARGB), rdptest.SolidFill(3, green, image.Rect(0, 0, 32, 32)))
	g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
	g.must(t, gfxFrame(1, rdptest.SurfaceToSurface(3, 1, image.Rect(0, 0, 32, 32), image.Pt(10, 10), image.Pt(90, 90))))
	scr := g.out.snapshot()
	wantRect(t, scr, image.Rect(10, 10, 42, 42), green)
	wantRect(t, scr, image.Rect(90, 90, 122, 122), green)
}

// A resize of the output comes once, however many ways it is told, and
// the mapped surfaces go to the new output whole.
func TestGFXResetGraphics(t *testing.T) {
	g := newGFXTest(t, 256, 256)
	g.must(t, surfacePDUs(1, 512, 300, image.Point{})...)
	g.must(t, gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(0, 0, 512, 300))))
	resizes := g.out.resizes
	g.must(t, rdptest.ResetGraphics(256, 256, image.Rect(0, 0, 256, 256)))
	if g.out.resizes != resizes {
		t.Fatal("a reset to the size the desktop has resized it")
	}
	g.must(t, rdptest.ResetGraphics(512, 300))
	if g.out.resizes != resizes+1 {
		t.Fatalf("%d resizes, want 1", g.out.resizes-resizes)
	}
	if w, h := g.c.Size(); w != 512 || h != 300 {
		t.Fatalf("size %d×%d", w, h)
	}
	wantRect(t, g.out.snapshot(), image.Rect(0, 0, 512, 300), red)
	// A reactivation to the same size does not resize again.
	if g.c.activated(g.c.share(), serverCaps{width: 512, height: 300}) {
		t.Error("the reactivation after the reset resizes")
	}
	// Resizes come at most every resizeEvery, as a reactivation's do,
	// whatever else the client waited for between them (the redraw's
	// charge here).
	first := g.c.lastResize
	g.must(t, rdptest.ResetGraphics(256, 256))
	if d := g.c.lastResize.Sub(first); d != resizeEvery {
		t.Errorf("a second resize at once came %v after the first, want %v", d, resizeEvery)
	}
}

// Before the server confirms the capabilities, its PDUs are ignored and
// its frames not acknowledged.
func TestGFXBeforeConfirm(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.c.gfx.confirmed = false
	g.must(t, append(surfacePDUs(1, 64, 64, image.Point{}), gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(0, 0, 64, 64))))...)
	if a := g.acks(t); len(a) != 0 {
		t.Fatalf("acknowledgements %+v", a)
	}
	if n := g.c.Stats().Ignored["GFX PDUs before the capabilities"]; n != 5 {
		t.Errorf("%d PDUs ignored, want 5", n)
	}
	// A capability set of an unknown version is ignored too.
	g.must(t, rdptest.CapsConfirm(0x000b0000, 0))
	if g.c.gfx.confirmed {
		t.Error("an unknown capability set confirmed")
	}
	g.must(t, rdptest.CapsConfirm(rdptest.CapVersion8, 2), rdptest.CreateSurface(1, 8, 8, rdptest.PixelXRGB))
	if gf := &g.c.gfx; !gf.confirmed || gf.version != rdptest.CapVersion8 || gf.flags != 2 || gf.surfaces[1] == nil {
		t.Errorf("confirmed %v, version %#x, flags %#x after the confirm", gf.confirmed, gf.version, gf.flags)
	}
	// The set of version 10.1 holds no flags, only reserved bytes.
	g.must(t, rdptest.CapsConfirm(rdptest.CapVersion101, 0xff))
	if gf := &g.c.gfx; gf.version != rdptest.CapVersion101 || gf.flags != 0 {
		t.Errorf("version %#x, flags %#x after a confirm of 10.1", gf.version, gf.flags)
	}
}

// What the client does not take yet is counted, and its frame still
// acknowledged.
func TestGFXIgnored(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
	r := image.Rect(0, 0, 8, 8)
	g.must(t, gfxFrame(3,
		rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, rdptest.CodecClearCodec, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, rdptest.CodecAVC420, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, rdptest.CodecAlpha, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, rdptest.CodecProgressive, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, 0x77, rdptest.PixelXRGB, r, []byte{1}),
		rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(4, 4, 4, 8), nil),
		rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, []byte{1}),
		rdptest.GFXPDU(rdptest.GFXDeleteEncodingCtx, make([]byte, 6)),
		rdptest.GFXPDU(rdptest.GFXCacheToSurface, make([]byte, 6)),
		rdptest.GFXPDU(rdptest.GFXMapSurfaceToScaled, make([]byte, 20)),
		rdptest.GFXPDU(0x77, nil),
	))
	if a := g.acks(t); len(a) != 1 || a[0].FrameID != 3 {
		t.Fatalf("acknowledgements %+v", a)
	}
	ign := g.c.Stats().Ignored
	for _, k := range []string{"GFX RemoteFX bitmaps", "GFX ClearCodec bitmaps", "GFX AVC420 bitmaps", "GFX alpha bitmaps",
		"GFX bitmaps of unknown codecs", "empty bitmaps", "GFX codec contexts", "GFX cache PDUs"} {
		if ign[k] != 1 {
			t.Errorf("Ignored[%q] = %d", k, ign[k])
		}
	}
	// One in wire-to-surface 2, as servers send it, and one in 1.
	if ign["GFX progressive bitmaps"] != 2 {
		t.Errorf("%d progressive bitmaps ignored, want 2", ign["GFX progressive bitmaps"])
	}
	if ign["GFX PDUs"] != 2 {
		t.Errorf("%d unknown PDUs ignored, want 2", ign["GFX PDUs"])
	}
}

// A frame that starts before the last ended is counted, and the frames
// go on; one that ends without a start is acknowledged.
func TestGFXFrameOrder(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.must(t, rdptest.StartFrame(1), rdptest.StartFrame(2), rdptest.EndFrame(2), rdptest.EndFrame(5))
	a := g.acks(t)
	if len(a) != 2 || a[0].FrameID != 2 || a[1].FrameID != 5 || a[1].TotalFramesDecoded != 2 {
		t.Fatalf("acknowledgements %+v", a)
	}
	if n := g.c.Stats().Ignored["GFX frames without their end"]; n != 1 {
		t.Errorf("%d frames without their end", n)
	}
}

// Surfaces replace and delete as they should, and the pixels they hold
// are bounded.
func TestGFXSurfaces(t *testing.T) {
	g := newGFXTest(t, 1024, 768)
	g.must(t, rdptest.CreateSurface(1, 1024, 768, rdptest.PixelXRGB), rdptest.CreateSurface(1, 64, 64, rdptest.PixelXRGB))
	if g.c.gfx.pixels != 64*64 || len(g.c.gfx.surfaces) != 1 {
		t.Fatalf("%d pixels in %d surfaces", g.c.gfx.pixels, len(g.c.gfx.surfaces))
	}
	g.must(t, rdptest.MapSurface(1, image.Point{}), rdptest.DeleteSurface(1), rdptest.DeleteSurface(1))
	if g.c.gfx.pixels != 0 || len(g.c.gfx.mapped) != 0 || g.c.Stats().Ignored["GFX deletes of no surface"] != 1 {
		t.Fatalf("%d pixels, %d mapped after the delete", g.c.gfx.pixels, len(g.c.gfx.mapped))
	}
	// Four desktops of 1024×768 come to minSurfacePixels.
	var pdus [][]byte
	for id := range 4 {
		pdus = append(pdus, rdptest.CreateSurface(id, 2048, 2048, rdptest.PixelXRGB))
	}
	g.must(t, pdus...)
	if err := g.send(rdptest.CreateSurface(9, 1, 1, rdptest.PixelXRGB)); err == nil {
		t.Error("a surface past the bound: no error")
	}
	// A surface that replaces another at the bound gives its pixels back.
	g.must(t, rdptest.CreateSurface(0, 2048, 2048, rdptest.PixelXRGB))
	// A surface mapped again moves to the top, once.
	g.must(t, rdptest.MapSurface(1, image.Point{}), rdptest.MapSurface(2, image.Point{}), rdptest.MapSurface(1, image.Pt(8, 0)))
	if m := g.c.gfx.mapped; len(m) != 2 || m[0].id != 2 || m[1].id != 1 || m[1].at != image.Pt(8, 0) {
		t.Errorf("%d surfaces mapped", len(m))
	}
	if got := surfaceLimit(8192, 4096); got != 2*maxPixels {
		t.Errorf("the bound on the largest desktop is %d", got)
	}
	if got := surfaceLimit(3840, 2160); got != 4*3840*2160 {
		t.Errorf("the bound on 4K is %d", got)
	}
}

// planarBlack is a planar bitmap of w×h, black, in RLE planes without
// alpha: each row a raw zero, then runs of zeros.
func planarBlack(w, h int) []byte {
	var row []byte
	row = append(row, 0x10, 0) // one raw byte
	left := w - 1
	for ; left >= 47; left -= 47 {
		row = append(row, 0xf2) // a run of 32+15
	}
	if left > 0 {
		row = append(row, byte(left)<<4)
		row = append(row, make([]byte, left)...)
	}
	plane := bytes.Repeat(row, h)
	return slices.Concat([]byte{planarRLE | planarNA}, plane, plane, plane)
}

// A planar bitmap of the graphics pipeline is bounded as a bitmap
// update's is, since the decoder keeps planes of its largest.
func TestGFXPlanarBound(t *testing.T) {
	g := newGFXTest(t, 1024, 768)
	g.must(t, rdptest.CreateSurface(1, 4096, 2049, rdptest.PixelXRGB))
	g.must(t, rdptest.SolidFill(1, red, image.Rect(0, 0, 4096, 2049)))
	at := image.Rect(0, 0, 4096, 2048)
	g.must(t, rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, at, planarBlack(4096, 2048)))
	if s := g.c.gfx.surfaces[1]; !bytes.Equal(s.pix[s.offset(4095, 2047):][:4], []byte{0, 0, 0, 0xff}) {
		t.Error("the bitmap at the bound was not decoded")
	}
	err := g.send(rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(0, 0, 4096, 2049), planarBlack(4096, 2049)))
	if err == nil || !strings.Contains(err.Error(), "over the bound of 8388608 pixels") {
		t.Errorf("a planar bitmap past the bound: %v", err)
	}
	for i, p := range g.c.dec.planes {
		if cap(p) > maxBitmapPixels {
			t.Errorf("plane %d of %d bytes kept", i, cap(p))
		}
	}
}

// The surfaces mapped at once are bounded, since a copy, a flush, a
// delete or a mapping looks at each; one mapped again counts once.
func TestGFXMappedBound(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	var pdus [][]byte
	for id := range maxGFXMapped + 1 {
		pdus = append(pdus, rdptest.CreateSurface(id, 1, 1, rdptest.PixelXRGB))
		if id < maxGFXMapped {
			pdus = append(pdus, rdptest.MapSurface(id, image.Pt(id, 0)))
		}
	}
	g.must(t, pdus...)
	g.must(t, rdptest.MapSurface(0, image.Pt(0, 1)))
	if err := g.send(rdptest.MapSurface(maxGFXMapped, image.Point{})); err == nil || !strings.Contains(err.Error(), "more than 64 surfaces mapped") {
		t.Errorf("a surface mapped past the bound: %v", err)
	}
}

// What a malformed PDU or message holds ends the session.
func TestGFXMalformed(t *testing.T) {
	r := image.Rect(0, 0, 8, 8)
	img := gradient(8, 8)
	for _, tc := range []struct {
		name string
		pdus [][]byte
		want string // in the error
	}{
		{"short header", [][]byte{{0x04, 0, 0, 0}}, "PDU of 0 bytes in 4"},
		{"length below the header", [][]byte{{0x04, 0, 0, 0, 7, 0, 0, 0}}, "PDU of 7 bytes in 8"},
		{"length past the message", [][]byte{{0x04, 0, 0, 0, 9, 0, 0, 0}}, "PDU of 9 bytes in 8"},
		{"length past the message of a PDU ignored", [][]byte{{0x77, 0, 0, 0, 9, 0, 0, 0}}, "PDU of 9 bytes in 8"},
		{"short body", [][]byte{rdptest.GFXPDU(rdptest.GFXCreateSurface, []byte{1, 0, 8})}, "truncated"},
		{"surface of a pixel format", [][]byte{rdptest.CreateSurface(2, 8, 8, 0x22)}, "pixel format 0x22"},
		{"empty surface", [][]byte{rdptest.CreateSurface(2, 0, 8, rdptest.PixelXRGB)}, "surface of 0×8"},
		{"surface too wide", [][]byte{rdptest.CreateSurface(2, maxSide+1, 8, rdptest.PixelXRGB)}, "surface of 8193×8"},
		{"fill of no surface", [][]byte{rdptest.SolidFill(5, red, r)}, "no surface 5"},
		{"fill of an inverted rectangle", [][]byte{rdptest.SolidFill(1, red, image.Rectangle{Min: image.Pt(8, 0), Max: image.Pt(4, 8)})}, "inverted rectangle"},
		{"fill short of its rectangles", [][]byte{rdptest.GFXPDU(rdptest.GFXSolidFill, []byte{1, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 1, 0, 1, 0})}, "2 rectangles in 8 bytes"},
		{"map of no surface", [][]byte{rdptest.MapSurface(5, image.Point{})}, "no surface 5"},
		{"map right of the largest output", [][]byte{rdptest.MapSurface(1, image.Pt(maxGFXOrigin+1, 0))}, "mapped at 32767,0"},
		{"map below the largest output", [][]byte{rdptest.MapSurface(1, image.Pt(0, maxGFXOrigin+1))}, "mapped at 0,32767"},
		{"copy from no surface", [][]byte{rdptest.SurfaceToSurface(5, 1, r, image.Point{})}, "no surface 5"},
		{"copy to no surface", [][]byte{rdptest.SurfaceToSurface(1, 5, r, image.Point{})}, "no surface 5"},
		{"copy from outside the surface", [][]byte{rdptest.SurfaceToSurface(1, 1, image.Rect(60, 60, 70, 70), image.Point{})}, "copy of (60,60)-(70,70) outside"},
		{"copy to outside the surface", [][]byte{rdptest.SurfaceToSurface(1, 1, r, image.Pt(60, 0))}, "copy to (60,0)-(68,8) outside"},
		{"copy short of its points", [][]byte{rdptest.GFXPDU(rdptest.GFXSurfaceToSurface, []byte{1, 0, 1, 0, 0, 0, 0, 0, 8, 0, 8, 0, 1, 0})}, "1 points in 0 bytes"},
		{"bitmap on no surface", [][]byte{rdptest.WireToSurface1(5, rdptest.CodecUncompressed, rdptest.PixelXRGB, r, rdptest.Uncompressed(img))}, "no surface 5"},
		{"bitmap outside the surface", [][]byte{rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, r.Add(image.Pt(60, 0)), rdptest.Uncompressed(img))}, "bitmap at (60,0)-(68,8) outside"},
		{"bitmap of an inverted rectangle", [][]byte{rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rectangle{Min: image.Pt(0, 8), Max: image.Pt(8, 0)}, rdptest.Planar(img))}, "inverted rectangle"},
		{"bitmap past its PDU", [][]byte{rdptest.GFXPDU(rdptest.GFXWireToSurface1, []byte{1, 0, 0, 0, 0x20, 0, 0, 0, 0, 8, 0, 8, 0, 9, 0, 0, 0, 1})}, "bitmap of 9 bytes in 1"},
		{"short uncompressed bitmap", [][]byte{rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, r, rdptest.Uncompressed(img)[1:])}, "8×8 in 255 bytes"},
		{"short planar bitmap", [][]byte{rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, r, rdptest.Planar(img)[:100])}, "truncated"},
		{"planar subsampled without colour loss", [][]byte{rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, r, []byte{planarNA | planarCS})}, "chroma subsampling and no colour loss"},
		{"reset of 17 monitors", [][]byte{rdptest.ResetGraphics(64, 64, slices.Repeat([]image.Rectangle{image.Rect(0, 0, 64, 64)}, 17)...)}, "17 monitors"},
		{"reset short of its monitors", [][]byte{rdptest.GFXPDU(rdptest.GFXResetGraphics, []byte{64, 0, 0, 0, 64, 0, 0, 0, 2, 0, 0, 0})}, "2 monitors in 0 bytes"},
		{"reset past the largest desktop", [][]byte{rdptest.ResetGraphics(maxSide+1, 64)}, "desktop of 8193×64"},
		{"reset past the largest area", [][]byte{rdptest.ResetGraphics(maxSide, maxSide)}, "desktop of 8192×8192"},
		{"reset to nothing", [][]byte{rdptest.ResetGraphics(0, 64)}, "desktop of 0×64"},
		{"confirm short of its set", [][]byte{rdptest.GFXPDU(rdptest.GFXCapsConfirm, []byte{4, 0, 8, 0, 4, 0, 0, 0})}, "truncated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGFXTest(t, 64, 64)
			g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
			if err := g.send(tc.pdus...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%v, want an error of %q", err, tc.want)
			}
		})
	}
}

// The frame acknowledgements that wait for the writer are bounded: a
// server that does not read them ends the session.
func TestGFXAcksBounded(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	var err error
	for i := range maxGFXAcks + 1 {
		if err = g.send(gfxFrame(uint32(i))); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("no error")
	}
	g.c.chio.mu.Lock()
	n := len(g.c.dvc.acks)
	g.c.chio.mu.Unlock()
	if n != maxGFXAcks {
		t.Errorf("%d acknowledgements wait", n)
	}
}

// planarYCoCg encodes img in the planar codec in AYCoCg with hdr's
// colour loss level, chroma subsampling, RLE and alpha plane, and
// returns the image the client should decode: the transform of
// [MS-RDPEGDI] 3.1.9.1.2 applied to the reduced planes.
func planarYCoCg(img *image.RGBA, hdr byte) []byte {
	b, _ := planarYCoCgWant(img, hdr)
	return b
}

func planarYCoCgWant(img *image.RGBA, hdr byte) ([]byte, *image.RGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	cll, cs := uint(hdr&planarCLLMask), hdr&planarCS != 0
	cw, ch := w, h
	if cs {
		cw, ch = (w+1)/2, (h+1)/2
	}
	lum := make([]byte, w*h)
	co, cg := make([]int, w*h), make([]int, w*h)
	for y := range h {
		for x := range w {
			c := img.RGBAAt(x, y)
			r, g, b := int(c.R), int(c.G), int(c.B)
			lum[y*w+x] = byte((r + 2*g + b) / 4)
			co[y*w+x], cg[y*w+x] = r-b, g-(r+b)/2
		}
	}
	cop, cgp := make([]byte, cw*ch), make([]byte, cw*ch)
	for y := range ch {
		for x := range cw {
			var so, sg, n int
			for dy := range 2 {
				for dx := range 2 {
					sx, sy := x, y
					if cs {
						sx, sy = 2*x+dx, 2*y+dy
					} else if dx+dy > 0 {
						continue
					}
					if sx < w && sy < h {
						so, sg, n = so+co[sy*w+sx], sg+cg[sy*w+sx], n+1
					}
				}
			}
			cop[y*cw+x], cgp[y*cw+x] = byte((so/n)>>cll), byte((sg/n)>>cll)
		}
	}
	planes := [][]byte{lum, cop, cgp}
	sizes := [][2]int{{w, h}, {cw, ch}, {cw, ch}}
	if hdr&planarNA == 0 {
		planes = append([][]byte{bytes.Repeat([]byte{0xff}, w*h)}, planes...)
		sizes = append([][2]int{{w, h}}, sizes...)
	}
	out := []byte{hdr}
	for i, p := range planes {
		if hdr&planarRLE != 0 {
			out = append(out, planarEncode([][]byte{p}, sizes[i][0], sizes[i][1], true)[1:]...)
		} else {
			out = append(out, p...)
		}
	}
	if hdr&planarRLE == 0 {
		out = append(out, 0) // pad
	}
	// The decoded image, by the transform's matrix on 9-bit chroma.
	want := image.NewRGBA(img.Rect)
	sign9 := func(v byte) int {
		n := int(v) << cll & 0x1ff
		if n&0x100 != 0 {
			n -= 0x200
		}
		return n
	}
	for y := range h {
		for x := range w {
			i := y*cw + x
			if cs {
				i = y/2*cw + x/2
			}
			yy, o, g := int(lum[y*w+x]), sign9(cop[i]), sign9(cgp[i])
			want.SetRGBA(x, y, color.RGBA{clamp8(yy + o/2 - g/2), clamp8(yy + g/2), clamp8(yy - o/2 - g/2), 0xff})
		}
	}
	return out, want
}

// Planar bitmaps in AYCoCg, with each colour loss level, subsampled or
// not, run-length encoded or raw, with an alpha plane or without.
func TestPlanarYCoCg(t *testing.T) {
	img := gradient(13, 7)
	for cll := byte(1); cll <= 7; cll++ {
		for _, flags := range []byte{0, planarCS, planarRLE, planarRLE | planarCS, planarNA, planarNA | planarCS | planarRLE} {
			hdr := cll | flags
			src, want := planarYCoCgWant(img, hdr)
			var d decoder
			stride := 4 * 20 // within a wider surface
			dst := make([]byte, stride*7)
			if err := d.planarDecode(src, 13, 7, dst, stride, planarGFX); err != nil {
				t.Fatalf("header %#x: %v", hdr, err)
			}
			got := &image.RGBA{Pix: dst, Stride: stride, Rect: image.Rect(0, 0, 13, 7)}
			for y := range 7 {
				for x := range 13 {
					if g, w := got.RGBAAt(x, y), want.RGBAAt(x, y); g != w {
						t.Fatalf("header %#x: pixel (%d, %d) is %v, want %v", hdr, x, y, g, w)
					}
				}
				if p := dst[y*stride+4*13:][:4*7]; !bytes.Equal(p, make([]byte, len(p))) {
					t.Fatalf("header %#x: row %d written past the bitmap", hdr, y)
				}
			}
		}
	}
	// At colour loss level 1, without subsampling, the colours come back
	// within the rounding: red and blue are where they should be.
	src, _ := planarYCoCgWant(img, 1|planarNA)
	var d decoder
	dst := make([]byte, 4*13*7)
	if err := d.planarDecode(src, 13, 7, dst, 4*13, planarGFX); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(dst); i += 4 {
		for k := range 3 {
			if !near(dst[i+k], img.Pix[i+k], 2) {
				t.Fatalf("pixel %d: %v, want %v", i/4, dst[i:i+4], img.Pix[i:i+4])
			}
		}
	}
}

// openGFX opens the graphics pipeline as id after the DRDYNVC
// capabilities, and returns the capability sets the client advertises.
func (l *live) openGFX(t *testing.T, id uint32) []rdptest.GFXCaps {
	t.Helper()
	l.sendDVC(t, rdptest.DVCCapsPDU(3))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCaps || d.Version != 2 {
		t.Fatalf("%+v, want capabilities of version 2", d)
	}
	l.sendDVC(t, rdptest.DVCCreateRequest(id, rdptest.Graphics))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Channel != id || d.Status != 0 {
		t.Fatalf("%+v, want the channel accepted", d)
	}
	return l.capsAdvertise(t, id)
}

// capsAdvertise reads the client's capabilities on the channel id.
func (l *live) capsAdvertise(t *testing.T, id uint32) []rdptest.GFXCaps {
	t.Helper()
	d := l.dvc(t)
	if d.Cmd != rdptest.DVCData || d.Channel != id {
		t.Fatalf("%+v, want the capabilities", d)
	}
	pdus, err := rdptest.ParseGFX(d.Data)
	if err != nil || len(pdus) != 1 || pdus[0].Cmd != rdptest.GFXCapsAdvertise {
		t.Fatalf("%+v, %v, want the capabilities", pdus, err)
	}
	sets, err := rdptest.ParseCapsAdvertise(pdus[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

// sendGFX sends PDUs as one message on the channel id, compressed.
func (l *live) sendGFX(t *testing.T, enc *rdptest.ZGFX, id uint32, pdus ...[]byte) {
	t.Helper()
	msg := slices.Concat(pdus...)
	if err := l.conn.SendGFX(id, enc.Multipart(msg, zgfxSegment, rdptest.SegCompressed, rdptest.SegStored)); err != nil {
		t.Fatal(err)
	}
}

// ack returns the client's next frame acknowledgement on the channel id.
func (l *live) ack(t *testing.T, id uint32) rdptest.FrameAck {
	t.Helper()
	d := l.dvc(t)
	if d.Cmd != rdptest.DVCData || d.Channel != id {
		t.Fatalf("%+v, want a frame acknowledgement", d)
	}
	return ackOf(t, d.Data)
}

// The client asks for the graphics pipeline only with Config.GFX.
func TestGFXAsked(t *testing.T) {
	for _, cfg := range []Config{{}, {Resize: true}, {GFX: true}} {
		l := runLive(t, rdptest.Server{}, cfg)
		_, dynvc := l.conn.Channels["drdynvc"]
		if gfx := l.conn.EarlyFlags&rnsUDCSSupportDynvcGFX != 0; gfx != cfg.GFX || dynvc != (cfg.GFX || cfg.Resize) {
			t.Errorf("%+v: GFX flag %v, drdynvc %v", cfg, gfx, dynvc)
		}
		if !cfg.Resize && !cfg.GFX {
			continue
		}
		l.sendDVC(t, rdptest.DVCCapsPDU(3))
		l.dvc(t)
		l.sendDVC(t, rdptest.DVCCreateRequest(gfxID, rdptest.Graphics))
		if d := l.dvc(t); (d.Status == 0) != cfg.GFX {
			t.Errorf("%+v: the graphics channel's status %#x", cfg, d.Status)
		}
	}
}

// The capability sets the client advertises, newest first.
func TestGFXCapsAdvertise(t *testing.T) {
	l := runLive(t, rdptest.Server{}, Config{GFX: true})
	sets := l.openGFX(t, gfxID)
	want := []rdptest.GFXCaps{
		{Version: rdptest.CapVersion107, Flags: 0xa2},
		{Version: rdptest.CapVersion104, Flags: 0x22},
		{Version: rdptest.CapVersion103, Flags: 0x20},
		{Version: rdptest.CapVersion102, Flags: 0x22},
		{Version: rdptest.CapVersion10, Flags: 0x22},
		{Version: rdptest.CapVersion81, Flags: 0x02},
		{Version: rdptest.CapVersion8, Flags: 0x02},
	}
	if len(sets) != len(want) {
		t.Fatalf("%d sets, want %d", len(sets), len(want))
	}
	for i, s := range sets {
		if s.Version != want[i].Version || s.Flags != want[i].Flags || len(s.Data) != 4 {
			t.Errorf("set %d: %#x flags %#x of %d bytes, want %#x flags %#x", i, s.Version, s.Flags, len(s.Data), want[i].Version, want[i].Flags)
		}
	}
}

// A session as xrdp runs one: the capabilities confirmed, the output
// reset, a surface mapped, and frames drawn and acknowledged, over the
// channel in compressed messages and DRDYNVC chunks.
func TestGFXSession(t *testing.T) {
	l := runLive(t, rdptest.Server{Width: 320, Height: 200}, Config{GFX: true, Width: 320, Height: 200})
	l.openGFX(t, gfxID)
	var enc rdptest.ZGFX
	img := gradient(200, 150)
	l.sendGFX(t, &enc, gfxID, rdptest.CapsConfirm(rdptest.CapVersion107, 0xa2), rdptest.ResetGraphics(320, 200, image.Rect(0, 0, 320, 200)))
	l.sendGFX(t, &enc, gfxID, surfacePDUs(0, 320, 200, image.Point{})...)
	l.sendGFX(t, &enc, gfxID, gfxFrame(1,
		rdptest.SolidFill(0, blue, image.Rect(0, 0, 320, 200)),
		rdptest.WireToSurface1(0, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(10, 20, 210, 170), rdptest.Planar(img))))
	if a := l.ack(t, gfxID); a.FrameID != 1 || a.TotalFramesDecoded != 1 {
		t.Fatalf("acknowledgement %+v", a)
	}
	scr := l.out.snapshot()
	wantImage(t, scr, image.Pt(10, 20), img)
	wantRect(t, scr, image.Rect(220, 0, 320, 200), blue)
	l.sendGFX(t, &enc, gfxID, gfxFrame(2, rdptest.SurfaceToSurface(0, 0, image.Rect(10, 20, 110, 70), image.Pt(200, 100))))
	if a := l.ack(t, gfxID); a.FrameID != 2 || a.TotalFramesDecoded != 2 {
		t.Fatalf("acknowledgement %+v", a)
	}
	wantImage(t, l.out.snapshot(), image.Pt(200, 100), img.SubImage(image.Rect(0, 0, 100, 50)).(*image.RGBA))
	if n := l.out.copyCount(); n != 1 {
		t.Errorf("%d copies on the output", n)
	}
}

// A message over the channel's bound ends the session, rather than be
// dropped: the next would depend on it.
func TestGFXMessageBound(t *testing.T) {
	l := runLive(t, rdptest.Server{}, Config{GFX: true})
	l.openGFX(t, gfxID)
	l.sendDVC(t, rdptest.DVCDataFirstPDU(gfxID, maxGFXMessage+1, []byte{0xe0}))
	select {
	case err := <-l.done:
		if err == nil || !strings.Contains(err.Error(), "over the bound") {
			t.Fatalf("Run: %v", err)
		}
		l.done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("the session went on")
	}
}

// The server may close the channel, as xrdp does to resize, and open it
// again: the pipeline starts anew. A second channel while one is open is
// refused.
func TestGFXReopen(t *testing.T) {
	l := runLive(t, rdptest.Server{}, Config{GFX: true})
	l.openGFX(t, gfxID)
	var enc rdptest.ZGFX
	l.sendGFX(t, &enc, gfxID, append([][]byte{rdptest.CapsConfirm(rdptest.CapVersion107, 0)}, surfacePDUs(1, 64, 64, image.Point{})...)...)
	l.sendDVC(t, rdptest.DVCCreateRequest(gfxID+1, rdptest.Graphics))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Status == 0 {
		t.Fatalf("%+v, want a second channel refused", d)
	}
	l.sendDVC(t, rdptest.DVCClosePDU(gfxID))
	if d := l.dvc(t); d.Cmd != rdptest.DVCClose || d.Channel != gfxID {
		t.Fatalf("%+v, want the close answered", d)
	}
	l.sendDVC(t, rdptest.DVCCreateRequest(gfxID+1, rdptest.Graphics))
	if d := l.dvc(t); d.Cmd != rdptest.DVCCreate || d.Status != 0 {
		t.Fatalf("%+v, want the channel accepted", d)
	}
	l.capsAdvertise(t, gfxID+1)
	// The new channel has a history and surfaces of its own.
	enc = rdptest.ZGFX{}
	l.sendGFX(t, &enc, gfxID+1, rdptest.CapsConfirm(rdptest.CapVersion8, 0), rdptest.SolidFill(1, red, image.Rect(0, 0, 1, 1)))
	select {
	case err := <-l.done:
		if err == nil || !strings.Contains(err.Error(), "no surface 1") {
			t.Fatalf("Run: %v", err)
		}
		l.done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("a fill of a surface of the closed channel went on")
	}
}

// Each PDU that changes pixels is charged as bitmaps of the pixels it
// changes, after its clipping.
func TestGFXPace(t *testing.T) {
	g := newGFXTest(t, 256, 256)
	if err := g.c.pace(0); err != nil {
		t.Fatal(err)
	}
	img := gradient(8, 8)
	ignored := rdptest.GFXPDU(rdptest.GFXCacheImportReply, make([]byte, 40000))
	for _, tc := range []struct {
		name string
		pdu  []byte
		n    int // -1: what compression added, in pixels of 4 bytes
		zip  bool
	}{
		{"create", rdptest.CreateSurface(1, 64, 32, rdptest.PixelXRGB), 64 * 32, false},
		{"replace", rdptest.CreateSurface(1, 128, 64, rdptest.PixelXRGB), 128 * 64, false},
		{"map", rdptest.MapSurface(1, image.Point{}), 128 * 64, false},
		{"fill", rdptest.SolidFill(1, red, image.Rect(0, 0, 8, 8), image.Rect(32, 32, 100, 100)), 8*8 + 68*32, false},
		{"small copy", rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 8, 4), image.Pt(16, 16), image.Pt(32, 0)), 2 * pointerCost, false},
		{"large copy", rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 128, 40), image.Pt(0, 8)), 128 * 40, false},
		// Copies that cannot reach the output cost their pixels alone.
		{"offscreen", rdptest.CreateSurface(2, 16, 16, rdptest.PixelXRGB), 16 * 16, false},
		{"offscreen copy", rdptest.SurfaceToSurface(2, 2, image.Rect(0, 0, 8, 4), image.Pt(8, 8), image.Pt(0, 8)), 2 * 8 * 4, false},
		{"copy to offscreen", rdptest.SurfaceToSurface(1, 2, image.Rect(0, 0, 8, 4), image.Pt(4, 4)), 8 * 4, false},
		{"copy from offscreen", rdptest.SurfaceToSurface(2, 1, image.Rect(0, 0, 8, 4), image.Pt(64, 0)), 8 * 4, false},
		{"uncompressed", rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, image.Rect(0, 0, 8, 8), rdptest.Uncompressed(img)), 8 * 8, false},
		{"planar", rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(8, 8, 16, 16), rdptest.Planar(img)), 8 * 8, false},
		{"stored", ignored, 0, false},
		{"compressed", ignored, -1, true},
		// The mapped surface drawn again; a larger desktop, so that its
		// larger bucket does not clamp what is left.
		{"reset", rdptest.ResetGraphics(300, 300), 128 * 64, false},
	} {
		before := g.c.tokens
		want := tc.n
		if tc.zip {
			msg := g.enc.Single(tc.pdu, rdptest.SegCompressed)
			want = (len(tc.pdu) - len(msg)) / 4
			if err := g.c.gfxMessage(g.out, msg); err != nil {
				t.Fatal(err)
			}
		} else {
			g.must(t, tc.pdu)
		}
		if got := before - g.c.tokens; got != float64(want) {
			t.Errorf("%s: charged %v pixels, want %d", tc.name, got, want)
		}
	}
}

// Where a surface leaves the output, or a bitmap update draws, the
// mapped surfaces under it draw again in whole cells, which may be far
// more than its own pixels: each is charged for the cells it has drawn
// again that were not to be drawn already, within its surface.
func TestGFXExposeCharged(t *testing.T) {
	const stack = 63 // surfaces of the whole desktop, under the others
	cell := gfxCell * gfxCell
	var mapEach [][]byte // the 1×1 surface mapped to each cell in turn
	for cy := range 8 {
		for cx := range 8 {
			mapEach = append(mapEach, rdptest.MapSurface(100, image.Pt(cx*gfxCell+1, cy*gfxCell+1)))
		}
	}
	tiny := surfacePDUs(100, 1, 1, image.Point{})
	for _, tc := range []struct {
		name  string
		setup [][]byte
		msg   [][]byte // one message
		bmp   bool     // a bitmap update of 1×1 at 0,0 in place of msg
		want  int
	}{
		// The first mapping uncovers the cell at 0,0, the second the same
		// cell, which waits to be drawn already, and the others one cell
		// each.
		{"mapped over each cell", tiny, mapEach, false, 64 + 63*stack*cell},
		{"created, mapped and deleted", nil, append(slices.Clone(tiny), rdptest.DeleteSurface(100)), false, 1 + 1 + stack*cell},
		{"replaced", tiny, [][]byte{rdptest.CreateSurface(100, 1, 1, rdptest.PixelXRGB)}, false, 1 + stack*cell},
		{"bitmap update", nil, nil, true, 1 + stack*cell},
		// The bottom surface's cell is to be drawn already for its fill.
		{"cells to be drawn already", tiny, [][]byte{rdptest.SolidFill(0, red, image.Rect(0, 0, 8, 8)), rdptest.DeleteSurface(100)}, false, 8*8 + (stack-1)*cell},
		// The top of the stack replaced by a surface of 70×70, which has
		// 6×6 pixels in its cell at 64,64.
		{"cell at a surface's edge", append(surfacePDUs(stack-1, 70, 70, image.Point{}), surfacePDUs(100, 1, 1, image.Pt(65, 65))...), [][]byte{rdptest.DeleteSurface(100)}, false, (stack-1)*cell + 6*6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGFXTest(t, 512, 512)
			var setup [][]byte
			for id := range stack {
				setup = append(setup, surfacePDUs(id, 512, 512, image.Point{})...)
			}
			g.must(t, append(setup, tc.setup...)...)
			// Settle what the setup owes, then count what the message
			// takes from the bucket and the time it waits for it.
			settle := func() (float64, time.Duration) {
				if err := g.c.pace(0); err != nil {
					t.Fatal(err)
				}
				g.clock.mu.Lock()
				defer g.clock.mu.Unlock()
				return g.c.tokens, g.clock.slept
			}
			tokens, slept := settle()
			if tc.bmp {
				b := &bitmapData{dest: image.Rect(0, 0, 1, 1), width: 1, height: 1, bpp: 32, data: []byte{0, 0, 0xff, 0xff}}
				if err := g.c.drawBitmap(g.out, b); err != nil {
					t.Fatal(err)
				}
			} else {
				g.must(t, tc.msg...)
			}
			tokens2, slept2 := settle()
			got := tokens - tokens2 + (slept2-slept).Seconds()*decodeRate
			if math.Abs(got-float64(tc.want)) > 8 {
				t.Errorf("charged %.0f pixels, want %d", got, tc.want)
			}
		})
	}
}

// The channel keeps a message's buffers for the next, up to retainGFX,
// and lets a larger one go.
func TestGFXRetain(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		keep bool
	}{{1024, 512, true}, {1280, 1024, false}} {
		g := newGFXTest(t, tc.w, tc.h)
		g.must(t, surfacePDUs(1, tc.w, tc.h, image.Point{})...)
		img := gradient(tc.w, tc.h)
		bmp := rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, img.Rect, rdptest.Uncompressed(img))
		for _, p := range rdptest.DVCMessage(gfxID, g.enc.Multipart(bmp, zgfxSegment, rdptest.SegStored), 1590) {
			if err := g.c.drdynvcMessage(g.out, p, false); err != nil {
				t.Fatal(err)
			}
		}
		wantImage(t, g.out.snapshot(), image.Point{}, img)
		dc := g.c.dvc.open[gfxID]
		if cap(dc.buf) > 0 != tc.keep || cap(g.c.gfx.z.out) > 0 != tc.keep {
			t.Errorf("a message of %d bytes: buffers of %d and %d bytes kept, want them kept: %v", len(bmp), cap(dc.buf), cap(g.c.gfx.z.out), tc.keep)
		}
	}
}

// A message that the server leaves unfinished ends the session: the
// next would depend on it.
func TestGFXUnfinished(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	for _, p := range [][]byte{rdptest.DVCDataFirstPDU(gfxID, 100, make([]byte, 10)), rdptest.DVCDataFirstPDU(gfxID, 100, make([]byte, 10))} {
		if err := g.c.drdynvcMessage(g.out, p, false); err != nil {
			if !strings.Contains(err.Error(), "without its end") {
				t.Fatal(err)
			}
			return
		}
	}
	t.Error("a message after one unfinished: no error")
}

// A channel that closes takes the pipeline's state with it: the
// surfaces and the acknowledgements that wait. The history's ring stays
// for the next channel (TestGFXReopenHistory).
func TestGFXClosed(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
	g.must(t, gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(0, 0, 8, 8))))
	if err := g.c.drdynvcMessage(nil, rdptest.DVCClosePDU(gfxID), false); err != nil {
		t.Fatal(err)
	}
	gf := &g.c.gfx
	if gf.ch != nil || gf.confirmed || gf.surfaces != nil || gf.mapped != nil || gf.pixels != 0 {
		t.Errorf("channel %v, confirmed %v, %d surfaces, %d mapped, %d pixels after the close",
			gf.ch, gf.confirmed, len(gf.surfaces), len(gf.mapped), gf.pixels)
	}
	if n := len(g.c.dvc.acks); n != 0 {
		t.Errorf("%d acknowledgements wait after the close", n)
	}
	wantRect(t, g.out.snapshot(), image.Rect(0, 0, 8, 8), red)
}

// A channel opened again starts with an empty history in the ring the
// last one used, zeroed rather than allocated again, and counts its
// frames on from the last channel's: totalFramesDecoded counts them
// since the connection began.
func TestGFXReopenHistory(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
	g.must(t, gfxFrame(1, rdptest.SolidFill(1, red, image.Rect(0, 0, 8, 8))))
	g.acks(t)
	hist := g.c.gfx.z.hist
	if g.c.gfx.z.filled == 0 {
		t.Fatal("nothing in the history")
	}
	for _, p := range [][]byte{rdptest.DVCClosePDU(gfxID), rdptest.DVCCreateRequest(gfxID, rdptest.Graphics)} {
		if err := g.c.drdynvcMessage(nil, p, false); err != nil {
			t.Fatal(err)
		}
	}
	z := &g.c.gfx.z
	if &z.hist[0] != &hist[0] || z.pos != 0 || z.filled != 0 {
		t.Errorf("the history reallocated: %v, or at %d with %d written", &z.hist[0] != &hist[0], z.pos, z.filled)
	}
	if i := slices.IndexFunc(z.hist, func(b byte) bool { return b != 0 }); i >= 0 {
		t.Errorf("the last channel's history at %d", i)
	}
	g.enc = rdptest.ZGFX{}
	g.must(t, rdptest.CapsConfirm(rdptest.CapVersion107, 0))
	g.must(t, gfxFrame(7))
	if acks := g.acks(t); len(acks) != 1 || acks[0].TotalFramesDecoded != 2 {
		t.Errorf("acknowledgements %+v, want a second frame decoded", acks)
	}
}

// A server that does not confirm, or closes the channel, draws with
// bitmap updates.
func TestGFXFallback(t *testing.T) {
	for _, close := range []bool{false, true} {
		l := runLive(t, rdptest.Server{Width: 256, Height: 200}, Config{GFX: true, Width: 256, Height: 200})
		l.openGFX(t, gfxID)
		if close {
			l.sendDVC(t, rdptest.DVCClosePDU(gfxID))
		}
		bmp := rdptest.FastPath(rdptest.Update(rdptest.FPBitmap, rdptest.BitmapUpdate(rdptest.RawBitmap(image.Pt(0, 0), solid(32, 16, green)))))
		if err := l.conn.Write(bmp); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(10 * time.Second); l.out.at(31, 15) != green; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("close %v: the bitmap update was not drawn", close)
			}
		}
	}
}

// FuzzGFX applies GFX messages to a client whose pipeline is open, on a
// screen that panics where the client breaks the output's contract.
func FuzzGFX(f *testing.F) {
	seed := func(msgs ...[]byte) []byte {
		var enc rdptest.ZGFX
		var b []byte
		for i, m := range msgs {
			kind := rdptest.SegStored
			if i%2 == 1 {
				kind = rdptest.SegCompressed
			}
			p := enc.Multipart(m, 4000, kind)
			b = append(b, byte(len(p)), byte(len(p)>>8))
			b = append(b, p...)
		}
		return b
	}
	img := gradient(16, 16)
	setup := slices.Concat(surfacePDUs(1, 96, 64, image.Point{})...)
	f.Add(seed(setup, gfxFrame(1,
		rdptest.SolidFill(1, red, image.Rect(0, 0, 50, 50)),
		rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, image.Rect(0, 0, 16, 16), rdptest.Uncompressed(img)),
		rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(16, 16, 32, 32), rdptest.Planar(img)),
		rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(32, 16, 48, 32), planarYCoCg(img, planarRLE|planarCS|planarNA|3)),
		rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 32, 32), image.Pt(40, 20), image.Pt(4, 4)))))
	f.Add(seed(setup, slices.Concat(rdptest.ResetGraphics(200, 100), rdptest.CreateSurface(2, 50, 50, rdptest.PixelARGB),
		rdptest.MapSurface(2, image.Pt(180, 90)), rdptest.SurfaceToSurface(1, 2, image.Rect(0, 0, 50, 50), image.Point{}), rdptest.DeleteSurface(1))))
	f.Fuzz(func(t *testing.T, data []byte) {
		g := newGFXTest(t, 128, 64)
		for len(data) >= 2 {
			n := min(int(data[0])|int(data[1])<<8, len(data)-2)
			msg := data[2 : 2+n]
			data = data[2+n:]
			if err := g.c.gfxMessage(g.out, msg); err != nil {
				return
			}
			if g.c.gfx.pixels > surfaceLimit(g.c.Size()) {
				t.Fatalf("%d pixels of surfaces", g.c.gfx.pixels)
			}
		}
	})
}

// fuzzGFXSeed is FuzzRun's seed of a graphics pipeline: the channel
// opened, a session drawn in compressed messages, a reactivation, and
// the channel closed.
func fuzzGFXSeed() [][]byte {
	var enc rdptest.ZGFX
	img := gradient(32, 32)
	var frames [][]byte
	dvc := func(pdus ...[]byte) {
		for _, p := range pdus {
			frames = append(frames, rdptest.ChannelChunks(fuzzDynvc, p, rdptest.ChunkSize)...)
		}
	}
	gfx := func(pdus ...[]byte) {
		dvc(rdptest.DVCMessage(gfxID, enc.Multipart(slices.Concat(pdus...), 3000, rdptest.SegCompressed, rdptest.SegStored), 1500)...)
	}
	dvc(rdptest.DVCCapsPDU(3), rdptest.DVCCreateRequest(gfxID, rdptest.Graphics))
	gfx(rdptest.CapsConfirm(rdptest.CapVersion107, 0), rdptest.ResetGraphics(fuzzWidth, fuzzHeight))
	gfx(surfacePDUs(1, fuzzWidth, fuzzHeight, image.Point{})...)
	gfx(gfxFrame(1,
		rdptest.SolidFill(1, red, image.Rect(0, 0, 100, 64)),
		rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, image.Rect(100, 0, 132, 32), rdptest.Planar(img)),
		rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, image.Rect(200, 0, 232, 32), rdptest.Uncompressed(img)),
		rdptest.SurfaceToSurface(1, 1, image.Rect(0, 0, 200, 32), image.Pt(300, 30))))
	frames = append(frames, rdptest.DeactivateAll(), rdptest.DemandActive(1280, 720, rdptest.DefaultInputFlags))
	frames = append(frames, rdptest.Finalization()...)
	gfx(rdptest.ResetGraphics(1280, 720), gfxFrame(2, rdptest.SolidFill(1, blue, image.Rect(0, 0, 10, 10))))
	dvc(rdptest.DVCClosePDU(gfxID))
	return frames
}

// FuzzRun's seed of a graphics pipeline draws what it should through
// Run, and the reactivation and the reset to its size resize once.
func TestFuzzGFXSeed(t *testing.T) {
	c, _ := testClock(fuzzWidth, fuzzHeight)
	c.cfg.Resize, c.cfg.GFX = true, true
	c.chio.kick = make(chan struct{}, 1)
	c.channels = c.newChannels()
	c.dynCh = c.channels[0]
	c.dynCh.id = fuzzDynvc
	sink := &backendtest.Sink{}
	if err := runFrames(c, sink, fuzzGFXSeed()...); !HungUp(err) {
		t.Fatalf("Run: %v", err)
	}
	st := c.Stats()
	if st.GFX["end frame"] != 2 || st.Bitmaps["gfx planar"] != 1 || st.Bitmaps["gfx uncompressed"] != 1 || st.GFX["surface to surface"] != 1 {
		t.Errorf("stats %+v", st)
	}
	if n := sink.Resizes(); n != 2 {
		t.Errorf("%d resizes, want the first and the reactivation's", n)
	}
	// The reactivation's resize cleared the output, and the surface went
	// to it again whole, not only the cells frame 2 changed.
	px := sink.Pixels()
	img := gradient(32, 32)
	for _, w := range []struct {
		p    image.Point
		want color.RGBA
	}{
		{image.Pt(5, 5), blue},
		{image.Pt(40, 40), red},
		{image.Pt(80, 20), red},
		{image.Pt(110, 10), img.RGBAAt(10, 10)},
		{image.Pt(210, 20), img.RGBAAt(10, 20)},
		{image.Pt(350, 40), red}, // copied from (50,10)
		{image.Pt(520, 50), black},
		{image.Pt(10, 100), black}, // below the surface
	} {
		if got := px.RGBAAt(w.p.X, w.p.Y); got != w.want {
			t.Errorf("output at %v is %v, want %v", w.p, got, w.want)
		}
	}
}

// BenchmarkGFXPlanar is a frame of 64×64 planar tiles over a surface of
// 1024×768, as xrdp sends its own screens, through the channel's
// decompression to the output.
func BenchmarkGFXPlanar(b *testing.B) {
	benchFrame(b, func(r image.Rectangle, img *image.RGBA) []byte {
		return rdptest.WireToSurface1(1, rdptest.CodecPlanar, rdptest.PixelXRGB, r, planarEncodeImage(img))
	})
}

// BenchmarkGFXUncompressed is the same frame of uncompressed tiles.
func BenchmarkGFXUncompressed(b *testing.B) {
	benchFrame(b, func(r image.Rectangle, img *image.RGBA) []byte {
		return rdptest.WireToSurface1(1, rdptest.CodecUncompressed, rdptest.PixelXRGB, r, rdptest.Uncompressed(img))
	})
}

// BenchmarkGFXSolidFill is a frame of a fill per tile.
func BenchmarkGFXSolidFill(b *testing.B) {
	benchFrame(b, func(r image.Rectangle, _ *image.RGBA) []byte {
		return rdptest.SolidFill(1, red, r)
	})
}

// BenchmarkGFXScroll is a frame that scrolls the surface by a line, as
// a copy on the output.
func BenchmarkGFXScroll(b *testing.B) {
	g := newGFXTest(b, 1024, 768)
	g.must(b, surfacePDUs(1, 1024, 768, image.Point{})...)
	msg := g.enc.Single(gfxFrame(1, rdptest.SurfaceToSurface(1, 1, image.Rect(0, 16, 1024, 768), image.Point{})), rdptest.SegStored)
	out := &sizeRecorder{img: image.NewRGBA(image.Rect(0, 0, 1024, 768))}
	b.ReportAllocs()
	for b.Loop() {
		if err := g.c.gfxMessage(out, msg); err != nil {
			b.Fatal(err)
		}
		g.c.chio.mu.Lock()
		g.c.dvc.acks = g.c.dvc.acks[:0]
		g.c.chio.mu.Unlock()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(1024*752), "ns/pixel")
}

// benchFrame benchmarks a frame of a PDU per 64×64 tile of a 1024×768
// surface, which tile makes, applied and drawn on an output.
func benchFrame(b *testing.B, tile func(image.Rectangle, *image.RGBA) []byte) {
	g := newGFXTest(b, 1024, 768)
	g.must(b, surfacePDUs(1, 1024, 768, image.Point{})...)
	img := gradient(64, 64)
	pdus := [][]byte{rdptest.StartFrame(1)}
	for y := 0; y < 768; y += 64 {
		for x := 0; x < 1024; x += 64 {
			pdus = append(pdus, tile(image.Rect(x, y, x+64, y+64), img))
		}
	}
	pdus = append(pdus, rdptest.EndFrame(1))
	msg := slices.Concat(pdus...)
	wire := g.enc.Multipart(msg, zgfxSegment, rdptest.SegStored)
	out := &sizeRecorder{img: image.NewRGBA(image.Rect(0, 0, 1024, 768))}
	b.SetBytes(int64(len(msg)))
	b.ReportAllocs()
	for b.Loop() {
		if err := g.c.gfxMessage(out, wire); err != nil {
			b.Fatal(err)
		}
		g.c.chio.mu.Lock()
		g.c.dvc.acks = g.c.dvc.acks[:0]
		g.c.chio.mu.Unlock()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(1024*768), "ns/pixel")
}

// planarEncodeImage is img in the planar codec, run-length encoded, no
// alpha plane, top-down.
func planarEncodeImage(img *image.RGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	planes := make([][]byte, 4)
	for c := range 3 {
		planes[c+1] = make([]byte, w*h)
		for i := range w * h {
			planes[c+1][i] = img.Pix[4*i+c]
		}
	}
	return planarEncode(planes, w, h, false)
}
