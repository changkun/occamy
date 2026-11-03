// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package decoders holds the client core's decoders of the codecs that
// occamyd sends, in pure Go, for a client that plugs them into
// client.Options.Decoders: deflate with occamyd's own internal/codec,
// WebP with github.com/gen2brain/vpx/webp, which reads VP8's
// limited-range colours as a browser does (docs/protocol.md, Codecs),
// and JPEG with the standard library. Each checks the size a payload
// claims against its rectangle before it decodes anything.
package decoders

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"sync"

	"github.com/gen2brain/vpx/webp"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// All returns the decoders in the order occamy's web client offers the
// codecs where the browser decodes them all: deflate, WebP and JPEG.
// Raw is built into the client core.
func All() []client.Decoder {
	return []client.Decoder{Deflate{}, WebP{}, JPEG{}}
}

// Deflate decodes the deflate container (codec 2).
type Deflate struct{}

// inflaters are codec.Inflaters, which are not safe for concurrent use,
// for the decoding goroutines.
var inflaters = sync.Pool{New: func() any { return new(codec.Inflater) }}

// Codec returns protocol.CodecDeflate.
func (Deflate) Codec() protocol.Codec { return protocol.CodecDeflate }

// Decode decodes payload into dst, w×h pixels.
func (Deflate) Decode(dst []byte, w, h int, payload []byte) error {
	in := inflaters.Get().(*codec.Inflater)
	defer inflaters.Put(in)
	return in.Decode(rgba(dst, w, h), payload)
}

// WebP decodes WebP (codec 5): occamyd's lossy tiles, which are opaque.
// It premultiplies alpha, where the protocol's pixels have straight
// alpha; occamyd sends lossless WebP with alpha only as a cursor to a
// client that lists neither deflate nor raw, which a client offering
// All does not.
type WebP struct{}

// Codec returns protocol.CodecWebP.
func (WebP) Codec() protocol.Codec { return protocol.CodecWebP }

// Decode decodes payload into dst, w×h pixels.
func (WebP) Decode(dst []byte, w, h int, payload []byte) error {
	cfg, err := webp.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := sized("WebP", cfg, w, h); err != nil {
		return err
	}
	m, err := webp.Decode(bytes.NewReader(payload), webp.Options{ToRGBA: true, FrameSizeLimit: w * h})
	if err != nil {
		return err
	}
	img, ok := m.(*image.RGBA)
	if !ok || img.Rect.Dx() != w || img.Rect.Dy() != h {
		return fmt.Errorf("decoders: a WebP decoded to %T of %v for %d×%d", m, m.Bounds(), w, h)
	}
	for y := range h {
		copy(dst[4*w*y:4*w*(y+1)], img.Pix[img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y):])
	}
	return nil
}

// JPEG decodes JPEG (codec 4).
type JPEG struct{}

// Codec returns protocol.CodecJPEG.
func (JPEG) Codec() protocol.Codec { return protocol.CodecJPEG }

// Decode decodes payload into dst, w×h pixels.
func (JPEG) Decode(dst []byte, w, h int, payload []byte) error {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := sized("JPEG", cfg, w, h); err != nil {
		return err
	}
	m, err := jpeg.Decode(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	draw.Draw(rgba(dst, w, h), image.Rect(0, 0, w, h), m, m.Bounds().Min, draw.Src)
	return nil
}

// rgba views w×h pixels without padding as an image.
func rgba(pix []byte, w, h int) *image.RGBA {
	return &image.RGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
}

// sized checks the size a lossy payload's header claims against its
// rectangle before anything is decoded, as docs/protocol.md requires.
func sized(format string, cfg image.Config, w, h int) error {
	if cfg.Width != w || cfg.Height != h {
		return fmt.Errorf("decoders: a %s of %d×%d for %d×%d pixels", format, cfg.Width, cfg.Height, w, h)
	}
	return nil
}
