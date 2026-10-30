// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole

import (
	"image"
	"io"

	"golang.org/x/image/webp"
)

// decodeWebP decodes a WebP image to the pixels libwebp, and so a
// browser, would show. golang.org/x/image/webp returns a lossy image as
// an image.YCbCr, whose colour model is JPEG's full-range YCbCr, while
// VP8 stores BT.601 limited-range samples: drawn as it is, dark colours
// come out too bright and bright ones too dark, by up to 38 levels a
// channel on guacd's frames. It also leaves chroma at half resolution,
// where libwebp interpolates it ("fancy upsampling"). decodeWebP
// converts lossy images itself, with libwebp's fixed-point arithmetic,
// so they match dwebp's output exactly. Lossless images come back as
// image.NRGBA and need nothing.
func decodeWebP(r io.Reader) (image.Image, error) {
	img, err := webp.Decode(r)
	if err != nil {
		return nil, err
	}
	switch m := img.(type) {
	case *image.YCbCr:
		if m.SubsampleRatio == image.YCbCrSubsampleRatio420 && m.Rect.Min == (image.Point{}) {
			return upsample(m, nil), nil
		}
	case *image.NYCbCrA:
		if m.SubsampleRatio == image.YCbCrSubsampleRatio420 && m.Rect.Min == (image.Point{}) {
			return upsample(&m.YCbCr, m), nil
		}
	}
	return img, nil
}

// upsample converts a 4:2:0 image whose bounds start at the origin to
// non-premultiplied RGBA, taking alpha from a when it is not nil. It
// follows libwebp's EmitFancyRGB and UpsampleRgbLinePair: each output
// pixel weighs the four nearest chroma samples 9:3:3:1, and the first
// row, the last row of an even height and the edge columns mirror the
// samples beyond the border.
func upsample(m *image.YCbCr, a *image.NYCbCrA) *image.NRGBA {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	dst := image.NewNRGBA(m.Rect)
	rows := (h + 1) / 2
	for y := range h {
		// near is the chroma row closest to y, far the next closest:
		// row k sits between luma rows 2k and 2k+1.
		near, far := (y+1)/2-1, (y+1)/2
		if y%2 == 0 {
			near, far = far, near
		}
		near, far = max(near, 0), min(max(far, 0), rows-1)
		ys := m.Y[y*m.YStride : y*m.YStride+w]
		nu := m.Cb[near*m.CStride:]
		nv := m.Cr[near*m.CStride:]
		fu := m.Cb[far*m.CStride:]
		fv := m.Cr[far*m.CStride:]
		out := dst.Pix[y*dst.Stride : y*dst.Stride+4*w]

		put := func(x int, u, v uint32) {
			yuvToRGB(ys[x], u, v, out[4*x:4*x+3])
		}
		// The first pixel and, for an even width, the last one only
		// blend vertically.
		put(0, (3*uint32(nu[0])+uint32(fu[0])+2)>>2, (3*uint32(nv[0])+uint32(fv[0])+2)>>2)
		for x := 1; x <= (w-1)/2; x++ {
			put(2*x-1, between(nu[x-1], nu[x], fu[x-1], fu[x]), between(nv[x-1], nv[x], fv[x-1], fv[x]))
			put(2*x, between(nu[x], nu[x-1], fu[x], fu[x-1]), between(nv[x], nv[x-1], fv[x], fv[x-1]))
		}
		if w%2 == 0 {
			x := w/2 - 1
			put(w-1, (3*uint32(nu[x])+uint32(fu[x])+2)>>2, (3*uint32(nv[x])+uint32(fv[x])+2)>>2)
		}

		for x := range w {
			out[4*x+3] = 0xff
			if a != nil {
				out[4*x+3] = a.A[y*a.AStride+x]
			}
		}
	}
	return dst
}

// between interpolates the chroma of a pixel whose nearest sample is
// nn, with nf beside it in the same row and fn, ff the samples under
// them in the next nearest row, in libwebp's two rounding steps.
func between(nn, nf, fn, ff uint8) uint32 {
	d := (uint32(nn) + 3*uint32(nf) + 3*uint32(fn) + uint32(ff) + 8) >> 3
	return (d + uint32(nn)) >> 1
}

// yuvToRGB writes the RGB of one limited-range BT.601 sample to rgb,
// with libwebp's VP8YuvToRgb arithmetic: 14-bit coefficients, a 6-bit
// fraction and clipping.
func yuvToRGB(y uint8, u, v uint32, rgb []byte) {
	yy := mulHi(uint32(y), 19077)
	rgb[0] = clip8(yy + mulHi(v, 26149) - 14234)
	rgb[1] = clip8(yy - mulHi(u, 6419) - mulHi(v, 13320) + 8708)
	rgb[2] = clip8(yy + mulHi(u, 33050) - 17685)
}

func mulHi(v, coeff uint32) int { return int(v * coeff >> 8) }

func clip8(v int) uint8 {
	switch {
	case v < 0:
		return 0
	case v >= 256<<6:
		return 0xff
	}
	return uint8(v >> 6) // #nosec G115 -- v is in [0, 1<<14).
}
