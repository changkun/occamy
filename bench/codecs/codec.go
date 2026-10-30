// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codecs

import (
	"bytes"
	"compress/flate"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"

	dtwebp "github.com/deepteams/webp"
	"github.com/gen2brain/jpegn"
	vpxwebp "github.com/gen2brain/vpx/webp"
	kflate "github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
)

// Codec is a candidate tile codec. A Codec keeps buffers between calls,
// so it is not safe for concurrent use.
type Codec struct {
	// Name identifies the candidate in the results.
	Name string
	// Family groups a lossy codec's qualities into one curve; it is
	// empty for lossless codecs.
	Family string
	// Quality is a lossy codec's quality, 1 to 100.
	Quality int
	// Encode returns the tile encoded. The result is valid until the
	// next call. p holds the tile's palette if it has at most 256
	// colours, and is nil otherwise.
	Encode func(t *image.RGBA, p *Palette) ([]byte, error)
	// Decode decodes what Encode returned into opaque RGBA pixels, as a
	// client draws them. The image is valid until the next call.
	Decode func(b []byte) (*image.RGBA, error)
}

// Lossless reports whether c must reproduce the tile exactly.
func (c *Codec) Lossless() bool { return c.Family == "" }

// Qualities are the lossy qualities measured.
var Qualities = []int{30, 40, 50, 60, 70, 80, 90}

// Candidates returns every codec the measurement compares, for tiles of
// the given side.
func Candidates(side int) []*Codec {
	cs := []*Codec{
		container("pal+flate1", side, stdFlate(flate.BestSpeed)),
		container("pal+flate6", side, stdFlate(flate.DefaultCompression)),
		container("pal+kflate1", side, kFlate(1)),
		container("pal+zstd1", side, newZstd(zstd.SpeedFastest)),
		container("pal+zstd3", side, newZstd(zstd.SpeedDefault)),
		pngCodec("png1", png.BestSpeed),
		pngCodec("png6", png.DefaultCompression),
		qoiCodec(side),
		deepteams("webpll-dt0", true, 0, 0),
		deepteams("webpll-dt4", true, 75, 4),
		vpx("webpll-vpx0", true, 0, 0),
		vpx("webpll-vpx4", true, 0, 4),
	}
	for _, q := range Qualities {
		cs = append(cs,
			stdJPEG(q),
			jpegnCodec(q),
			deepteams(fmt.Sprintf("webp-dt0 q%d", q), false, q, 0),
			deepteams(fmt.Sprintf("webp-dt4 q%d", q), false, q, 4),
			vpx(fmt.Sprintf("webp-vpx0 q%d", q), false, q, 0),
			vpx(fmt.Sprintf("webp-vpx4 q%d", q), false, q, 4),
		)
	}
	return cs
}

// compressor is a general-purpose compressor for the palette container.
type compressor struct {
	compress   func(dst, src []byte) ([]byte, error)
	decompress func(dst, src []byte) ([]byte, error)
}

// container is lossless option A: the palette container, compressed.
func container(name string, side int, z compressor) *Codec {
	var raw, packed, plain []byte
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	return &Codec{
		Name: name,
		Encode: func(t *image.RGBA, p *Palette) ([]byte, error) {
			raw = AppendContainer(raw[:0], t, p, p != nil)
			var err error
			packed, err = z.compress(packed[:0], raw)
			return packed, err
		},
		Decode: func(b []byte) (*image.RGBA, error) {
			var err error
			if plain, err = z.decompress(plain[:0], b); err != nil {
				return nil, err
			}
			return out, DecodeContainer(out, plain)
		},
	}
}

// stdFlate is raw deflate from the standard library, which browsers
// inflate with DecompressionStream("deflate-raw").
func stdFlate(level int) compressor {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, level)
	if err != nil {
		panic(err)
	}
	r := flate.NewReader(nil)
	var src bytes.Reader
	return compressor{
		compress: func(dst, b []byte) ([]byte, error) {
			buf.Reset()
			w.Reset(&buf)
			if _, err := w.Write(b); err != nil {
				return nil, err
			}
			if err := w.Close(); err != nil {
				return nil, err
			}
			return append(dst, buf.Bytes()...), nil
		},
		decompress: func(dst, b []byte) ([]byte, error) {
			src.Reset(b)
			if err := r.(flate.Resetter).Reset(&src, nil); err != nil {
				return nil, err
			}
			return readAll(dst, r)
		},
	}
}

// kFlate is raw deflate from klauspost/compress, the same format.
func kFlate(level int) compressor {
	var buf bytes.Buffer
	w, err := kflate.NewWriter(&buf, level)
	if err != nil {
		panic(err)
	}
	r := kflate.NewReader(nil)
	var src bytes.Reader
	return compressor{
		compress: func(dst, b []byte) ([]byte, error) {
			buf.Reset()
			w.Reset(&buf)
			if _, err := w.Write(b); err != nil {
				return nil, err
			}
			if err := w.Close(); err != nil {
				return nil, err
			}
			return append(dst, buf.Bytes()...), nil
		},
		decompress: func(dst, b []byte) ([]byte, error) {
			src.Reset(b)
			if err := r.(kflate.Resetter).Reset(&src, nil); err != nil {
				return nil, err
			}
			return readAll(dst, r)
		},
	}
}

// newZstd is zstd from klauspost/compress. No browser decodes zstd
// natively.
func newZstd(level zstd.EncoderLevel) compressor {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	return compressor{
		compress:   func(dst, b []byte) ([]byte, error) { return enc.EncodeAll(b, dst), nil },
		decompress: func(dst, b []byte) ([]byte, error) { return dec.DecodeAll(b, dst) },
	}
}

func readAll(dst []byte, r io.Reader) ([]byte, error) {
	for {
		if len(dst) == cap(dst) {
			dst = append(dst, 0)[:len(dst)]
		}
		n, err := r.Read(dst[len(dst):cap(dst)])
		dst = dst[:len(dst)+n]
		if err == io.EOF {
			return dst, nil
		}
		if err != nil {
			return dst, err
		}
	}
}

// toRGBA converts what a decoder returned to RGBA pixels at the origin,
// through image/draw's fast paths for the standard image types.
func toRGBA(m image.Image, err error) (*image.RGBA, error) {
	if err != nil {
		return nil, err
	}
	if r, ok := m.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r, nil
	}
	r := image.NewRGBA(image.Rect(0, 0, m.Bounds().Dx(), m.Bounds().Dy()))
	draw.Draw(r, r.Rect, m, m.Bounds().Min, draw.Src)
	return r, nil
}

// pngPool lets png.Encoder reuse its buffers between tiles.
type pngPool struct{ b *png.EncoderBuffer }

func (p *pngPool) Get() *png.EncoderBuffer  { return p.b }
func (p *pngPool) Put(b *png.EncoderBuffer) { p.b = b }

// pngCodec is lossless option B. Tiles of up to 256 colours are written
// with a palette, which the encoder packs to 1, 2, 4 or 8 bits.
func pngCodec(name string, level png.CompressionLevel) *Codec {
	enc := png.Encoder{CompressionLevel: level, BufferPool: &pngPool{}}
	var buf bytes.Buffer
	var pal color.Palette
	return &Codec{
		Name: name,
		Encode: func(t *image.RGBA, p *Palette) ([]byte, error) {
			buf.Reset()
			var m image.Image = t
			if p != nil {
				pal = pal[:0]
				for _, c := range p.Colours {
					pal = append(pal, color.RGBA{byte(c >> 16), byte(c >> 8), byte(c), 0xff}) // #nosec G115 -- bytes of 0xRRGGBB
				}
				m = &image.Paletted{Pix: p.Index, Stride: t.Rect.Dx(), Rect: t.Rect, Palette: pal}
			}
			err := enc.Encode(&buf, m)
			return buf.Bytes(), err
		},
		Decode: func(b []byte) (*image.RGBA, error) { return toRGBA(png.Decode(bytes.NewReader(b))) },
	}
}

// qoiCodec is lossless option C.
func qoiCodec(side int) *Codec {
	var buf []byte
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	return &Codec{
		Name: "qoi",
		Encode: func(t *image.RGBA, _ *Palette) ([]byte, error) {
			buf = AppendQOI(buf[:0], t)
			return buf, nil
		},
		Decode: func(b []byte) (*image.RGBA, error) { return out, DecodeQOI(out, b) },
	}
}

// deepteams is WebP from github.com/deepteams/webp: lossless option D,
// or lossy option B. For lossless, quality is the compression effort.
func deepteams(name string, lossless bool, quality, method int) *Codec {
	o := dtwebp.DefaultOptions()
	o.Lossless, o.Quality, o.Method = lossless, float32(quality), method
	var buf bytes.Buffer
	c := &Codec{
		Name: name,
		Encode: func(t *image.RGBA, _ *Palette) ([]byte, error) {
			buf.Reset()
			err := dtwebp.Encode(&buf, t, o)
			return buf.Bytes(), err
		},
		Decode: func(b []byte) (*image.RGBA, error) { return toRGBA(dtwebp.Decode(bytes.NewReader(b))) },
	}
	if !lossless {
		c.Family, c.Quality = fmt.Sprintf("webp-dt%d", method), quality
	}
	return c
}

// vpx is WebP from github.com/gen2brain/vpx/webp, coded on one
// goroutine. Lossless ignores quality.
func vpx(name string, lossless bool, quality, method int) *Codec {
	o := vpxwebp.EncodeOptions{Lossless: lossless, Quality: quality, Method: method, Threads: 1}
	var buf bytes.Buffer
	c := &Codec{
		Name: name,
		Encode: func(t *image.RGBA, _ *Palette) ([]byte, error) {
			buf.Reset()
			err := vpxwebp.Encode(&buf, t, o)
			return buf.Bytes(), err
		},
		Decode: func(b []byte) (*image.RGBA, error) {
			return toRGBA(vpxwebp.Decode(bytes.NewReader(b), vpxwebp.Options{ToRGBA: true, Threads: 1}))
		},
	}
	if !lossless {
		c.Family, c.Quality = fmt.Sprintf("webp-vpx%d", method), quality
	}
	return c
}

// stdJPEG is lossy option A, image/jpeg at 4:2:0.
func stdJPEG(q int) *Codec {
	var buf bytes.Buffer
	o := &jpeg.Options{Quality: q}
	return &Codec{
		Name: fmt.Sprintf("jpeg q%d", q), Family: "jpeg", Quality: q,
		Encode: func(t *image.RGBA, _ *Palette) ([]byte, error) {
			buf.Reset()
			err := jpeg.Encode(&buf, t, o)
			return buf.Bytes(), err
		},
		Decode: func(b []byte) (*image.RGBA, error) { return toRGBA(jpeg.Decode(bytes.NewReader(b))) },
	}
}

// jpegnCodec is JPEG from github.com/gen2brain/jpegn, baseline at 4:2:0
// with the standard tables, as image/jpeg writes.
func jpegnCodec(q int) *Codec {
	var buf bytes.Buffer
	o := &jpegn.EncodeOptions{Quality: q, Subsampling: jpegn.Subsample420}
	return &Codec{
		Name: fmt.Sprintf("jpegn q%d", q), Family: "jpegn", Quality: q,
		Encode: func(t *image.RGBA, _ *Palette) ([]byte, error) {
			buf.Reset()
			err := jpegn.Encode(&buf, t, o)
			return buf.Bytes(), err
		},
		Decode: func(b []byte) (*image.RGBA, error) {
			return toRGBA(jpegn.Decode(bytes.NewReader(b), &jpegn.Options{ToRGBA: true}))
		},
	}
}
