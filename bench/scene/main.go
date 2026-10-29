// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command scene draws the benchmark scenes of
// specs/008-native-architecture/benchmark.md on an X display. It covers the
// screen with one window and takes the input focus, since the bench desktop
// has no window manager. Function keys select the scene, and letters paint
// the latency probe's swatch in every scene:
//
//	F1      idle
//	F2      probe (the same as idle)
//	F3      scroll a page of text one line every 16 ms
//	F4      drag a 400×300 textured window at 60 Hz
//	F5      full-screen motion at 30 fps
//	F6      a static photographic image
//	Escape  back to idle
//
// Everything it draws is generated from fixed seeds, so every run shows
// the same pixels.
package main

import (
	"image"
	"image/color"
	"image/draw"
	"log"
	"math"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"changkun.de/x/occamy/bench/internal/probe"
)

// Keysyms the scene reacts to.
const (
	keyEscape = 0xff1b
	keyF1     = 0xffbe
	keyF6     = 0xffc3
)

// The scenes, numbered as their function keys.
const (
	sceneIdle = iota + 1
	sceneProbe
	sceneScroll
	sceneDrag
	sceneMotion
	scenePhoto
)

// Animation periods.
const (
	scrollPeriod = 16 * time.Millisecond
	dragPeriod   = time.Second / 60
	motionPeriod = time.Second / 30
)

// margin is the strip on the left that no scene animates, so that the
// swatch inside it stays still.
const margin = 64

// noProbe is the swatch's colour until a letter paints it. Every scene
// starts with it, so that the first probe key always changes the swatch.
var noProbe = color.RGBA{0x40, 0x40, 0x40, 0xff}

func main() {
	log.SetFlags(0)
	log.SetPrefix("scene: ")
	x, err := xgb.NewConn()
	if err != nil {
		log.Fatal(err)
	}
	d, err := newDisplay(x)
	if err != nil {
		log.Fatal(err)
	}
	d.run()
}

// display is the scene program's window and what it shows.
type display struct {
	x      *xgb.Conn
	screen *xproto.ScreenInfo
	win    xproto.Window
	drag   xproto.Window // the dragged window, a child of win
	gc     xproto.Gcontext
	w, h   int
	// maxRows is how many rows of the screen's width one PutImage request
	// carries.
	maxRows int

	keymap  []xproto.Keysym
	minCode xproto.Keycode
	perCode int

	scene  int
	swatch color.RGBA
	frame  int // frames drawn in the current scene
	start  time.Time

	text   *textPage
	motion *plasma
	photo  *image.RGBA
}

func newDisplay(x *xgb.Conn) (*display, error) {
	setup := xproto.Setup(x)
	screen := setup.DefaultScreen(x)
	d := &display{
		x: x, screen: screen,
		w: int(screen.WidthInPixels), h: int(screen.HeightInPixels),
		scene:  sceneIdle,
		swatch: noProbe,
	}
	// The request length is in units of four bytes, and PutImage has a
	// 24-byte header.
	d.maxRows = max(1, (int(setup.MaximumRequestLength)*4-24)/(d.w*4))

	var err error
	if d.win, err = xproto.NewWindowId(x); err != nil {
		return nil, err
	}
	if d.drag, err = xproto.NewWindowId(x); err != nil {
		return nil, err
	}
	if d.gc, err = xproto.NewGcontextId(x); err != nil {
		return nil, err
	}
	wallpaper, err := d.pixmap(newWallpaper(d.w, d.h))
	if err != nil {
		return nil, err
	}
	texture, err := d.pixmap(newPhoto(400, 300, 2))
	if err != nil {
		return nil, err
	}
	xproto.CreateWindow(x, screen.RootDepth, d.win, screen.Root,
		0, 0, u16(d.w), u16(d.h), 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixmap|xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{
			uint32(wallpaper), 1,
			xproto.EventMaskExposure | xproto.EventMaskKeyPress | xproto.EventMaskStructureNotify,
		})
	xproto.CreateWindow(x, screen.RootDepth, d.drag, d.win,
		margin, 0, 400, 300, 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixmap, []uint32{uint32(texture)})
	xproto.CreateGC(x, d.gc, xproto.Drawable(d.win), 0, nil)
	if err := d.loadKeymap(); err != nil {
		return nil, err
	}
	xproto.MapWindow(x, d.win)

	d.text = newTextPage(d.w-margin, d.h)
	d.motion = newPlasma(d.w, d.h)
	d.photo = newPhoto(d.w, d.h, 1)
	return d, nil
}

// pixmap uploads img to a new pixmap.
func (d *display) pixmap(img *image.RGBA) (xproto.Pixmap, error) {
	p, err := xproto.NewPixmapId(d.x)
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	xproto.CreatePixmap(d.x, d.screen.RootDepth, p, xproto.Drawable(d.screen.Root),
		u16(b.Dx()), u16(b.Dy()))
	gc, err := xproto.NewGcontextId(d.x)
	if err != nil {
		return 0, err
	}
	xproto.CreateGC(d.x, gc, xproto.Drawable(p), 0, nil)
	d.putImage(xproto.Drawable(p), gc, img, image.Point{})
	xproto.FreeGC(d.x, gc)
	return p, nil
}

// putImage draws img at at, in requests that fit the request length.
func (d *display) putImage(dst xproto.Drawable, gc xproto.Gcontext, img *image.RGBA, at image.Point) {
	b := img.Bounds()
	rows := max(1, d.maxRows*d.w/max(1, b.Dx()))
	buf := make([]byte, 0, b.Dx()*rows*4)
	for y0 := b.Min.Y; y0 < b.Max.Y; y0 += rows {
		y1 := min(y0+rows, b.Max.Y)
		buf = buf[:0]
		for y := y0; y < y1; y++ {
			row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
			for i := 0; i < len(row); i += 4 {
				// ZPixmap at depth 24 is 32 bits per pixel, B, G, R and
				// padding in the byte order of little-endian servers.
				buf = append(buf, row[i+2], row[i+1], row[i], 0)
			}
		}
		xproto.PutImage(d.x, xproto.ImageFormatZPixmap, dst, gc,
			u16(b.Dx()), u16(y1-y0),
			i16(at.X), i16(at.Y+y0-b.Min.Y), 0, d.screen.RootDepth, buf)
	}
}

func (d *display) fill(r image.Rectangle, c color.RGBA) {
	pixel := uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
	xproto.ChangeGC(d.x, d.gc, xproto.GcForeground, []uint32{pixel})
	xproto.PolyFillRectangle(d.x, xproto.Drawable(d.win), d.gc, []xproto.Rectangle{{
		X: i16(r.Min.X), Y: i16(r.Min.Y), Width: u16(r.Dx()), Height: u16(r.Dy()),
	}})
}

func (d *display) loadKeymap() error {
	setup := xproto.Setup(d.x)
	d.minCode = setup.MinKeycode
	m, err := xproto.GetKeyboardMapping(d.x, setup.MinKeycode,
		byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		return err
	}
	d.keymap, d.perCode = m.Keysyms, int(m.KeysymsPerKeycode)
	return nil
}

func (d *display) keysym(code xproto.Keycode) uint32 {
	i := (int(code) - int(d.minCode)) * d.perCode
	if i < 0 || i >= len(d.keymap) {
		return 0
	}
	return uint32(d.keymap[i])
}

func (d *display) run() {
	events := make(chan xgb.Event)
	go func() {
		for {
			ev, err := d.x.WaitForEvent()
			if ev == nil && err == nil {
				log.Fatal("connection closed")
			}
			if err != nil {
				log.Print(err)
				continue
			}
			events <- ev
		}
	}()
	var tick <-chan time.Time
	var ticker *time.Ticker
	setPeriod := func(p time.Duration) {
		if ticker != nil {
			ticker.Stop()
			ticker, tick = nil, nil
		}
		if p > 0 {
			ticker = time.NewTicker(p)
			tick = ticker.C
		}
	}
	for {
		select {
		case ev := <-events:
			switch ev := ev.(type) {
			case xproto.MapNotifyEvent:
				// Focus can only go to a viewable window.
				xproto.SetInputFocus(d.x, xproto.InputFocusPointerRoot, d.win, xproto.TimeCurrentTime)
			case xproto.ExposeEvent:
				if ev.Count == 0 {
					d.redraw()
				}
			case xproto.MappingNotifyEvent:
				if err := d.loadKeymap(); err != nil {
					log.Print(err)
				}
			case xproto.KeyPressEvent:
				if p, ok := d.key(d.keysym(ev.Detail)); ok {
					setPeriod(p)
				}
			}
		case <-tick:
			d.animate()
		}
	}
}

// key handles a key press. When the key changes the scene, it returns
// the new scene's animation period, which is zero for a still scene.
func (d *display) key(k uint32) (time.Duration, bool) {
	switch {
	case 'a' <= k && k <= 'z':
		d.swatch = probe.Colour(rune(k))
		d.fill(probe.Swatch, d.swatch)
		return 0, false
	case k == keyEscape:
		return d.setScene(sceneIdle), true
	case keyF1 <= k && k <= keyF6:
		return d.setScene(int(k-keyF1) + 1), true
	}
	return 0, false
}

func (d *display) setScene(s int) time.Duration {
	if d.scene == sceneDrag && s != sceneDrag {
		xproto.UnmapWindow(d.x, d.drag)
	}
	d.scene, d.frame, d.start = s, 0, time.Now()
	d.swatch = noProbe
	d.redraw()
	switch s {
	case sceneScroll:
		return scrollPeriod
	case sceneDrag:
		xproto.MapWindow(d.x, d.drag)
		return dragPeriod
	case sceneMotion:
		return motionPeriod
	}
	return 0
}

// redraw draws the current scene in full.
func (d *display) redraw() {
	switch d.scene {
	case sceneScroll:
		xproto.ClearArea(d.x, false, d.win, 0, 0, margin, u16(d.h))
		d.putImage(xproto.Drawable(d.win), d.gc, d.text.page(), image.Pt(margin, 0))
	case sceneMotion:
		d.putImage(xproto.Drawable(d.win), d.gc, d.withSwatch(d.motion.frame(d.frame)), image.Point{})
		return
	case scenePhoto:
		d.putImage(xproto.Drawable(d.win), d.gc, d.withSwatch(d.photo), image.Point{})
		return
	default:
		xproto.ClearArea(d.x, false, d.win, 0, 0, 0, 0)
	}
	d.fill(probe.Swatch, d.swatch)
}

// withSwatch draws the swatch into img, so that a full-screen image does
// not cover it.
func (d *display) withSwatch(img *image.RGBA) *image.RGBA {
	draw.Draw(img, probe.Swatch, image.NewUniform(d.swatch), image.Point{}, draw.Src)
	return img
}

// animate draws the next frame of an animated scene.
func (d *display) animate() {
	d.frame++
	switch d.scene {
	case sceneScroll:
		lh := d.text.lineHeight
		xproto.CopyArea(d.x, xproto.Drawable(d.win), xproto.Drawable(d.win), d.gc,
			margin, i16(lh), margin, 0, u16(d.w-margin), u16(d.h-lh))
		d.putImage(xproto.Drawable(d.win), d.gc, d.text.next(), image.Pt(margin, d.h-lh))
	case sceneDrag:
		p := dragPosition(time.Since(d.start), d.w, d.h)
		xproto.ConfigureWindow(d.x, d.drag, xproto.ConfigWindowX|xproto.ConfigWindowY,
			[]uint32{uint32(int32(p.X)), uint32(int32(p.Y))}) // #nosec G115 -- X takes signed positions as 32-bit values
	case sceneMotion:
		d.putImage(xproto.Drawable(d.win), d.gc, d.withSwatch(d.motion.frame(d.frame)), image.Point{})
	}
}

// X takes coordinates and sizes as 16-bit numbers. The scene draws within
// a screen whose size X reports in 16 bits, so these only clamp.

func u16(v int) uint16 {
	switch {
	case v < 0:
		return 0
	case v > math.MaxUint16:
		return math.MaxUint16
	}
	return uint16(v)
}

func i16(v int) int16 {
	switch {
	case v < math.MinInt16:
		return math.MinInt16
	case v > math.MaxInt16:
		return math.MaxInt16
	}
	return int16(v)
}
