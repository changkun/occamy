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
	for _, tt := range []struct {
		name string
		img  *image.RGBA
		want bool
	}{
		{"the desktop", fill(true, w), true},
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
	defer func() {
		if t.Failed() {
			t.Logf("the screen is %v and shows %s", cl.Size(), shown)
		}
	}()
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
// behind a growing screen leaves the end of that row dark. Thousands of
// colours, in the wallpaper's gradient, tell a desktop from a bar on a
// plain screen.
func panelShown(img *image.RGBA) (string, bool) {
	const (
		row        = 1
		minColours = 256
		minBright  = 0.9 // the workspace switcher's cells are darker
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
	colours := map[[3]byte]bool{}
	for y := range h {
		line := img.Pix[y*img.Stride : y*img.Stride+4*w]
		for x := 0; x < len(line); x += 4 {
			if line[x+3] == 0xff {
				colours[[3]byte(line[x:x+3])] = true
			}
		}
	}
	end := bright(img.Pix[row*img.Stride+4*(w-1):])
	shown := fmt.Sprintf("%d colours, with %d of %d pixels of the panel's row bright, its end bright: %t", len(colours), lit, w, end)
	return shown, len(colours) >= minColours && end && float64(lit)/float64(w) >= minBright
}
