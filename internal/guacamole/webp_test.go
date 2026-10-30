// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole_test

import (
	"encoding/base64"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strconv"
	"testing"

	"changkun.de/x/occamy/internal/guacamole"
)

// TestDisplayWebPMatchesLibwebp draws lossy WebP images and compares
// them with what libwebp makes of them. The fixtures are sharp colour
// edges on dark and bright backgrounds, encoded with libwebp 1.6:
//
//	cwebp -q 50 opaque.png -o lossy.webp              # 37×21
//	cwebp -q 50 -exact alpha.png -o lossy-alpha.webp  # 24×16, alpha ramp
//	dwebp lossy.webp -o lossy.png
//	dwebp lossy-alpha.webp -o lossy-alpha.png
//
// golang.org/x/image/webp alone reads VP8's limited-range samples as
// full-range and does not interpolate chroma, which put guacd's frames
// up to 38 levels a channel off what a browser shows.
func TestDisplayWebPMatchesLibwebp(t *testing.T) {
	for _, name := range []string{"lossy", "lossy-alpha"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name + ".webp")
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open("testdata/" + name + ".png")
			if err != nil {
				t.Fatal(err)
			}
			ref, err := png.Decode(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := image.NewRGBA(ref.Bounds())
			draw.Draw(want, want.Rect, ref, image.Point{}, draw.Src)

			w, h := strconv.Itoa(want.Rect.Dx()), strconv.Itoa(want.Rect.Dy())
			d := guacamole.NewDisplay()
			apply(t, d, "size", "0", w, h)
			apply(t, d, "img", "1", "12", "0", "image/webp", "0", "0")
			apply(t, d, "blob", "1", base64.StdEncoding.EncodeToString(data))
			apply(t, d, "end", "1")
			got := d.Image()
			if got.Rect != want.Rect {
				t.Fatalf("bounds %v, want %v", got.Rect, want.Rect)
			}
			n := 0
			for y := range want.Rect.Dy() {
				for x := range want.Rect.Dx() {
					if g, w := got.RGBAAt(x, y), want.RGBAAt(x, y); g != w {
						if n < 5 {
							t.Errorf("pixel (%d, %d) = %v, libwebp %v", x, y, g, w)
						}
						n++
					}
				}
			}
			if n > 0 {
				t.Errorf("%d pixels differ from libwebp", n)
			}
		})
	}
}
