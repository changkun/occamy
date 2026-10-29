// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// newWallpaper is a vertical gradient, as plain as a desktop background.
func newWallpaper(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	for y := range h {
		c := lerp(top, bottom, float64(y)/float64(max(1, h-1)))
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// newPhoto renders a landscape from fractal noise: smooth gradients with
// fine detail and grain, which compress as a photograph does. The seed
// selects the landscape.
func newPhoto(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 0x6f63616d79)) // #nosec G404 -- deterministic content
	n := newNoise(rng)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	skyTop, skyLow := color.RGBA{0x3a, 0x6f, 0xb5, 0xff}, color.RGBA{0xc8, 0xdc, 0xf0, 0xff}
	ramp := []color.RGBA{
		{0x2b, 0x4f, 0x2a, 0xff}, // forest
		{0x5d, 0x7a, 0x3a, 0xff}, // meadow
		{0x8a, 0x7f, 0x6a, 0xff}, // rock
		{0xee, 0xee, 0xf2, 0xff}, // snow
	}
	for y := range h {
		for x := range w {
			u, v := float64(x)/float64(w), float64(y)/float64(h)
			horizon := 0.42 + 0.25*(n.fbm(u*3, 0.5, 5)-0.5)
			var c color.RGBA
			if v < horizon {
				c = lerp(skyTop, skyLow, v/horizon)
				cloud := n.fbm(u*6, v*12, 6)
				c = lerp(c, color.RGBA{0xff, 0xff, 0xff, 0xff}, clamp01((cloud-0.55)*3))
			} else {
				height := n.fbm(u*8+10, v*8+10, 7)
				c = rampAt(ramp, clamp01(height*1.4-0.2-(v-horizon)))
				shade := 0.6 + 0.6*n.fbm(u*40, v*40, 3)
				c = scale(c, shade)
			}
			grain := rng.IntN(13) - 6
			img.SetRGBA(x, y, color.RGBA{add(c.R, grain), add(c.G, grain), add(c.B, grain), 0xff})
		}
	}
	return img
}

// noise is value noise on a 256×256 lattice.
type noise struct{ lattice [256 * 256]float64 }

func newNoise(rng *rand.Rand) *noise {
	n := &noise{}
	for i := range n.lattice {
		n.lattice[i] = rng.Float64()
	}
	return n
}

func (n *noise) at(x, y int) float64 { return n.lattice[(y&255)*256+(x&255)] }

func (n *noise) value(x, y float64) float64 {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := smooth(x-x0), smooth(y-y0)
	ix, iy := int(x0), int(y0)
	a := n.at(ix, iy) + (n.at(ix+1, iy)-n.at(ix, iy))*fx
	b := n.at(ix, iy+1) + (n.at(ix+1, iy+1)-n.at(ix, iy+1))*fx
	return a + (b-a)*fy
}

// fbm sums octaves of noise, each at twice the frequency and half the
// amplitude of the one before, normalized to [0, 1].
func (n *noise) fbm(x, y float64, octaves int) float64 {
	var sum, amp, total float64 = 0, 1, 0
	for range octaves {
		sum += amp * n.value(x, y)
		total += amp
		x, y, amp = x*2, y*2, amp/2
	}
	return sum / total
}

func smooth(t float64) float64 { return t * t * (3 - 2*t) }

func clamp01(t float64) float64 { return min(1, max(0, t)) }

func lerp(a, b color.RGBA, t float64) color.RGBA {
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 0xff}
}

func rampAt(ramp []color.RGBA, t float64) color.RGBA {
	t *= float64(len(ramp) - 1)
	i := min(int(t), len(ramp)-2)
	return lerp(ramp[i], ramp[i+1], t-float64(i))
}

func scale(c color.RGBA, s float64) color.RGBA {
	f := func(v uint8) uint8 { return uint8(min(255, float64(v)*s)) }
	return color.RGBA{f(c.R), f(c.G), f(c.B), 0xff}
}

func add(v uint8, d int) uint8 { return uint8(min(255, max(0, int(v)+d))) }

// textPage is an endless page of text, like a log scrolling in an
// editor.
type textPage struct {
	w, h       int
	lineHeight int
	rng        *rand.Rand
	line       *image.RGBA
}

var (
	paper = color.RGBA{0xfa, 0xfa, 0xf5, 0xff}
	ink   = color.RGBA{0x20, 0x20, 0x28, 0xff}
	words = strings.Fields(`the quick brown fox jumps over lazy dog remote desktop
		protocol frame latency server client session window input stream
		pixel tile cursor keyboard network buffer encode decode render`)
)

func newTextPage(w, h int) *textPage {
	return &textPage{
		w: w, h: h, lineHeight: basicfont.Face7x13.Height,
		rng:  rand.New(rand.NewPCG(3, 0x74657874)), // #nosec G404 -- deterministic content
		line: image.NewRGBA(image.Rect(0, 0, w, basicfont.Face7x13.Height)),
	}
}

// page renders a screenful of lines.
func (p *textPage) page() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, p.w, p.h))
	for y := 0; y < p.h; y += p.lineHeight {
		draw.Draw(img, image.Rect(0, y, p.w, y+p.lineHeight), p.next(), image.Point{}, draw.Src)
	}
	return img
}

// next renders the next line. The image is reused by the next call.
func (p *textPage) next() *image.RGBA {
	draw.Draw(p.line, p.line.Bounds(), image.NewUniform(paper), image.Point{}, draw.Src)
	var b strings.Builder
	for b.Len() < p.w/7 {
		b.WriteString(words[p.rng.IntN(len(words))])
		b.WriteByte(' ')
	}
	d := font.Drawer{
		Dst: p.line, Src: image.NewUniform(ink), Face: basicfont.Face7x13,
		Dot: fixed.P(4, basicfont.Face7x13.Ascent),
	}
	d.DrawString(b.String())
	return p.line
}

// plasma is full-screen procedural motion: every pixel changes in every
// frame.
type plasma struct {
	img     *image.RGBA
	sine    [1024]int
	palette [256]color.RGBA
}

func newPlasma(w, h int) *plasma {
	p := &plasma{img: image.NewRGBA(image.Rect(0, 0, w, h))}
	for i := range p.sine {
		p.sine[i] = int(63.5 + 63.5*math.Sin(2*math.Pi*float64(i)/float64(len(p.sine))))
	}
	for i := range p.palette {
		t := 2 * math.Pi * float64(i) / 256
		p.palette[i] = color.RGBA{
			uint8(127.5 + 127.5*math.Sin(t)),
			uint8(127.5 + 127.5*math.Sin(t+2*math.Pi/3)),
			uint8(127.5 + 127.5*math.Sin(t+4*math.Pi/3)),
			0xff,
		}
	}
	return p
}

// frame renders frame n. The image is reused by the next call.
func (p *plasma) frame(n int) *image.RGBA {
	b := p.img.Bounds()
	for y := range b.Dy() {
		row := p.img.Pix[y*p.img.Stride:]
		sy := p.sine[(y*3+n*2)&1023]
		for x := range b.Dx() {
			v := p.sine[(x*2+n*3)&1023] + sy + p.sine[(x+y+n*5)&1023]
			c := p.palette[(v+n)&255]
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.R, c.G, c.B, 0xff
		}
	}
	return p.img
}

// dragPosition is where the dragged window is after t: it sweeps across
// the screen right of the margin and back every 8 seconds, and swings up
// and down every 3.
func dragPosition(t time.Duration, w, h int) image.Point {
	s := t.Seconds()
	sweep := math.Abs(math.Mod(s/4, 2) - 1) // 1 → 0 → 1
	x := margin + int(float64(w-margin-400)*(1-sweep))
	y := (h-300)/2 + int(0.8*float64(h-300)/2*math.Sin(2*math.Pi*s/3))
	return image.Pt(x, y)
}
