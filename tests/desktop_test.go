// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"net/url"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
)

// TestDemoDesktop opens the demo stack's real desktop through the native
// backends, the connections a first-time viewer sees first
// (docker/demo/desktop): XFCE must draw its panel and its wallpaper, and
// when the screen grows the panel must grow with it. The first RDP
// connection starts its X session, so the deadline leaves XFCE time to
// come up on a slow machine.
func TestDemoDesktop(t *testing.T) {
	addr := endpointV1(t)
	for _, c := range []struct{ id, backend string }{
		{"desktop-vnc", "vnc"},
		{"desktop-rdp", "rdp"},
	} {
		t.Run(c.id, func(t *testing.T) {
			token, err := signIn(addr)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cl := openDesktopV1(t, ctx, addr, "/api/v1/connections/"+url.PathEscape(c.id)+"/stream", token,
				client.Options{Width: 1024, Height: 768})
			if state := sessionV1(t, addr, token, cl.SessionID()); state.Backend != c.backend {
				t.Errorf("the session's backend is %q, want %q", state.Backend, c.backend)
			}
			awaitPanelV1(t, ctx, cl, image.Pt(1024, 768))
			if _, err := cl.Resize(ctx, 1280, 800); err != nil {
				t.Fatal(err)
			}
			awaitPanelV1(t, ctx, cl, image.Pt(1280, 800))
			closeSessionV1(t, addr, token, cl.SessionID())
		})
	}
}

// TestPanelShown checks panelShown, which needs no stack, on what the
// screen can show instead of the desktop.
func TestPanelShown(t *testing.T) {
	const w, h = 200, 100
	// fill draws a wallpaper of many colours under a panel that covers
	// the first panel columns of the top rows.
	fill := func(wallpaper bool, panel int) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				c := color.RGBA{A: 0xff}
				if wallpaper {
					c.G, c.B = byte(x), byte(y)
				}
				if y < 26 && x < panel {
					c = color.RGBA{0xf6, 0xf5, 0xf4, 0xff}
				}
				img.SetRGBA(x, y, c)
			}
		}
		return img
	}
	// band repaints rows from and to of img, by what colour gives a point.
	band := func(img *image.RGBA, from, to int, colour func(x, y int) color.RGBA) *image.RGBA {
		for y := from; y < to; y++ {
			for x := range w {
				img.SetRGBA(x, y, colour(x, y))
			}
		}
		return img
	}
	icons := func(x, y int) color.RGBA { return color.RGBA{byte(x), byte(y), 0x80, 0xff} }
	white := func(int, int) color.RGBA { return color.RGBA{0xff, 0xff, 0xff, 0xff} }
	for _, tt := range []struct {
		name string
		img  *image.RGBA
		want bool
	}{
		{"the desktop", fill(true, w), true},
		{"a white window left open", band(fill(true, w), 26, 90, white), true},
		{"many-coloured icons on black", band(fill(false, w), 80, h, icons), false},
		{"nothing drawn", image.NewRGBA(image.Rect(0, 0, w, h)), false},
		{"a black screen", fill(false, 0), false},
		{"a panel with no wallpaper", fill(false, w), false},
		{"no panel", fill(true, 0), false},
		{"a panel behind a grown screen", fill(true, w*3/4), false},
		{"no screen", image.NewRGBA(image.Rectangle{}), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if shown, ok := panelShown(tt.img); ok != tt.want {
				t.Errorf("panelShown = %t (%s), want %t", ok, shown, tt.want)
			}
		})
	}
}

// awaitPanelV1 reads cl's frames, which also acknowledges them, until the
// screen has the size and shows the desktop with its panel across it.
func awaitPanelV1(t *testing.T, ctx context.Context, cl *client.Session, size image.Point) {
	t.Helper()
	var shown string
	defer func() { t.Logf("the screen is %v and shows %s", cl.Size(), shown) }()
	awaitV1(t, ctx, cl, fmt.Sprintf("XFCE's panel across %v", size), func(ev client.Event) bool {
		if _, ok := ev.(client.Frame); !ok || !cl.Size().Eq(size) {
			return false
		}
		var ok bool
		shown, ok = panelShown(cl.Screenshot())
		return ok
	})
}

// panelShown says whether img shows XFCE's default session, and what it
// shows if not. The panel is a light bar along the top, whose second row
// is free of its text and reaches the right edge: a panel that stayed
// behind a growing screen leaves the end of that row dark. The wallpaper
// gives the screen at least minColours colours, but the panel and the
// dock's icons can too on a black screen, so no dark colour may cover
// more than maxShare of it. A light one may: a window left open in the
// session, such as Firefox on a white page, covers most of the screen.
func panelShown(img *image.RGBA) (string, bool) {
	const (
		row        = 1
		minColours = 256
		minBright  = 0.9 // the workspace switcher's cells are darker
		maxShare   = 0.5 // of the screen, in one dark colour
	)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w == 0 || h <= row {
		return "nothing", false
	}
	bright := func(px []byte) bool {
		return px[3] == 0xff && (int(px[0])+int(px[1])+int(px[2]))/3 >= 0x80
	}
	lit := 0
	for x := range w {
		if bright(img.Pix[row*img.Stride+4*x:]) {
			lit++
		}
	}
	colours := map[[3]byte]int{}
	var common [3]byte
	for y := range h {
		line := img.Pix[y*img.Stride : y*img.Stride+4*w]
		for x := 0; x < len(line); x += 4 {
			if line[x+3] == 0xff {
				c := [3]byte(line[x : x+3])
				colours[c]++
				if colours[c] > colours[common] {
					common = c
				}
			}
		}
	}
	share := float64(colours[common]) / float64(w*h)
	end := bright(img.Pix[row*img.Stride+4*(w-1):])
	shown := fmt.Sprintf("%d colours, %.1f%% of the screen in %v, with %d of %d pixels of the panel's row bright, its end bright: %t",
		len(colours), 100*share, common, lit, w, end)
	empty := share > maxShare && !bright(append(common[:], 0xff))
	return shown, len(colours) >= minColours && !empty && end && float64(lit)/float64(w) >= minBright
}
