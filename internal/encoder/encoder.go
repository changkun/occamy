// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package encoder encodes a viewer's tiles and cursor in the codecs it
// agreed to, as the graphics pipeline's Codecs task describes
// (specs/008-native-architecture/graphics-pipeline.md): it classifies a
// tile by its colours, sends tiles of up to 256 colours lossless as the
// deflate container (internal/codec) or raw pixels, and photographic
// tiles lossy as WebP from github.com/gen2brain/vpx at method 0, or as
// JPEG from github.com/gen2brain/jpegn for a viewer that lists JPEG but
// not WebP. The stream writer merges adjacent lossy tiles of a frame into
// one rectangle before it asks for their encoding, so that their headers
// are paid once.
//
// Every encoder in this package is pure Go and codes on the calling
// goroutine alone, so a viewer's encoding takes at most one core.
package encoder

import (
	"image"
	"slices"

	"github.com/gen2brain/jpegn"
	"github.com/gen2brain/vpx/webp"

	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// Codecs returns the codecs an Encoder sends, which the server offers in
// the handshake.
func Codecs() []protocol.Codec {
	return []protocol.Codec{protocol.CodecWebP, protocol.CodecJPEG, protocol.CodecDeflate, protocol.CodecRaw}
}

// Class is a tile's class by its colours, which picks its codec.
type Class int

// The classes.
const (
	// Few is up to 16 colours: text, UI and flat gradients, which are
	// always lossless, since lossy is 5 to 10 times larger on them.
	Few Class = iota
	// Many is 17 to 256 colours: antialiased text and smooth gradients,
	// lossless while still and lossy while they move. Until the
	// pipeline's Refinement task tells the encoder a tile's motion,
	// they are lossless.
	Many
	// Photo is over 256 colours, which is lossy.
	Photo
)

// FewColours is the most colours of a tile in class Few.
const FewColours = 16

// MaxRun is the most adjacent lossy tiles a writer merges into one
// rectangle: a row of the 1024-pixel frames that the codec measurement
// merged, which keeps the message near the size of one raw tile.
const MaxRun = 16

// Classify returns the class of a tile with n colours, as codec.Colours
// counts them.
func Classify(n int) Class {
	switch {
	case n <= FewColours:
		return Few
	case n <= codec.MaxColours:
		return Many
	}
	return Photo
}

func (c Class) String() string {
	switch c {
	case Few:
		return "few"
	case Many:
		return "many"
	case Photo:
		return "photo"
	}
	return "Class(?)"
}

// Encoder encodes one viewer's tiles and cursor. It keeps its buffers
// and compressors between calls, so it is not safe for concurrent use:
// each writer has its own.
type Encoder struct {
	// lossless and lossy are the codecs tiles of each kind go in, 0 for
	// none. A viewer with no lossless codec gets every tile lossy, and
	// one with no lossy codec gets every tile lossless.
	lossless, lossy protocol.Codec
	// webp reports that the viewer decodes WebP, which carries a cursor
	// exactly when it agreed no lossless codec.
	webp bool

	pal codec.Palette
	def codec.Deflater
	out sliceWriter
	jo  jpegn.EncodeOptions
}

// New returns an encoder for a viewer that agreed to codecs, in any
// order:
//
//   - tiles of up to 256 colours as deflate, or else raw pixels;
//   - tiles of more as WebP, or else JPEG, or else as tiles of fewer;
//   - every tile lossy for a viewer that agreed only lossy codecs;
//   - the cursor as deflate, or else raw pixels, or else lossless WebP.
//
// The measurement ranks the codecs of each kind, so the viewer's order
// of preference does not change the choice. New returns nil if codecs has
// none of the codecs of Codecs.
func New(codecs []protocol.Codec) *Encoder {
	e := &Encoder{
		webp: slices.Contains(codecs, protocol.CodecWebP),
		jo:   jpegn.EncodeOptions{Subsampling: jpegn.Subsample420},
	}
	switch {
	case slices.Contains(codecs, protocol.CodecDeflate):
		e.lossless = protocol.CodecDeflate
	case slices.Contains(codecs, protocol.CodecRaw):
		e.lossless = protocol.CodecRaw
	}
	switch {
	case e.webp:
		e.lossy = protocol.CodecWebP
	case slices.Contains(codecs, protocol.CodecJPEG):
		e.lossy = protocol.CodecJPEG
	}
	if e.lossless == 0 && e.lossy == 0 {
		return nil
	}
	return e
}

// Tile appends the encoding of a tile's pixels, w by h in RGBA order row
// by row, to dst and returns it with its codec; or, if the tile goes
// lossy, returns dst unchanged and lossy set, so that the caller can
// merge it with adjacent lossy tiles and encode them with Lossy.
func (e *Encoder) Tile(dst, pix []byte, w, h int) (b []byte, c protocol.Codec, lossy bool) {
	if e.lossless == 0 {
		return dst, 0, true
	}
	n := codec.Colours(pix, w, h, &e.pal)
	if Classify(n) == Photo && e.lossy != 0 {
		return dst, 0, true
	}
	var p *codec.Palette
	if n <= codec.MaxColours {
		p = &e.pal
	}
	return e.appendLossless(dst, pix, w, h, p), e.lossless, false
}

// appendLossless appends pix in the lossless codec, with p its palette
// or nil if it has more than 256 colours.
func (e *Encoder) appendLossless(dst, pix []byte, w, h int, p *codec.Palette) []byte {
	if e.lossless == protocol.CodecDeflate {
		return e.def.AppendPalette(dst, pix, w, h, p)
	}
	return codec.AppendRaw(dst, pix, w, h)
}

// Lossy appends the lossy encoding of pix, w by h pixels in RGBA order
// row by row, to dst at quality, from 1 to 100, and returns it with its
// codec. pix is one tile that Tile reported lossy, or several adjacent
// ones merged into a rectangle. Its alpha is taken as opaque. For a
// viewer with no lossy codec, or if the lossy encoder fails, as it does
// for a rectangle wider or higher than WebP allows (16383 pixels), the
// encoding is lossless; it returns codec 0 only if there is none.
func (e *Encoder) Lossy(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec) {
	quality = min(max(quality, 1), 100)
	e.out.b = dst
	var err error
	switch e.lossy {
	case protocol.CodecWebP:
		// The NRGBA path packs pixels without the division that RGBA's
		// premultiplied alpha costs; opaque pixels are the same in both.
		img := &image.NRGBA{Pix: pix[:4*w*h], Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
		err = webp.Encode(&e.out, img, webp.EncodeOptions{Quality: quality, Method: 0, Threads: 1})
	case protocol.CodecJPEG:
		img := &image.RGBA{Pix: pix[:4*w*h], Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
		e.jo.Quality = quality
		err = jpegn.Encode(&e.out, img, &e.jo)
	}
	b := e.out.b
	e.out.b = nil
	if e.lossy != 0 && err == nil {
		return b, e.lossy
	}
	if e.lossless == 0 {
		return dst, 0
	}
	var p *codec.Palette
	if e.pal.Build(pix, w, h) {
		p = &e.pal
	}
	return e.appendLossless(dst, pix, w, h, p), e.lossless
}

// Cursor appends the encoding of a cursor's image, pix w by h pixels in
// RGBA order row by row with straight alpha, to dst, in a codec that
// keeps it exact, and returns it with its codec. It reports false, with
// dst unchanged, for a viewer that agreed none: JPEG has no alpha.
func (e *Encoder) Cursor(dst, pix []byte, w, h int) (b []byte, c protocol.Codec, ok bool) {
	switch {
	case e.lossless == protocol.CodecDeflate:
		return e.def.Append(dst, pix, w, h), protocol.CodecDeflate, true
	case e.lossless == protocol.CodecRaw:
		return codec.AppendRaw(dst, pix, w, h), protocol.CodecRaw, true
	case e.webp:
		e.out.b = dst
		img := &image.NRGBA{Pix: pix[:4*w*h], Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
		err := webp.Encode(&e.out, img, webp.EncodeOptions{Lossless: true, Exact: true, Method: 0, Threads: 1})
		b, e.out.b = e.out.b, nil
		if err == nil {
			return b, protocol.CodecWebP, true
		}
	}
	return dst, 0, false
}

// sliceWriter appends what is written to a slice.
type sliceWriter struct{ b []byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
