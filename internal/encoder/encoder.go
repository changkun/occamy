// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package encoder encodes a viewer's tiles and cursor in the codecs it
// agreed to, as the graphics pipeline's Codecs and Refinement tasks
// describe (specs/008-native-architecture/graphics-pipeline.md): it
// classifies a tile by its colours, sends tiles of up to 16 colours
// lossless as the deflate container (internal/codec) or raw pixels, tiles
// of 17 to 256 colours the same way unless they move, and the rest lossy
// as WebP from github.com/gen2brain/vpx at method 0, or as JPEG from
// github.com/gen2brain/jpegn for a viewer that lists JPEG but not WebP.
// The stream writer merges adjacent lossy tiles of a frame into one
// rectangle before it asks for their encoding, so that their headers are
// paid once. A still lossy rectangle goes at the lowest quality whose
// SSIM against its pixels reaches StillSSIM.
//
// Every encoder in this package is pure Go and codes on the calling
// goroutine alone, so a viewer's encoding takes at most one core.
package encoder

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"slices"

	"github.com/gen2brain/jpegn"
	"github.com/gen2brain/vpx/webp"

	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// Codecs returns the codecs an Encoder sends, which the server offers in
// the handshake, and cached, which the server sends for a tile that the
// client's tile cache holds.
func Codecs() []protocol.Codec {
	return []protocol.Codec{protocol.CodecWebP, protocol.CodecJPEG, protocol.CodecDeflate, protocol.CodecRaw, protocol.CodecCached}
}

// Class is a tile's class by its colours, which picks its codec.
type Class int

// The classes.
const (
	// Few is up to 16 colours: text, UI and flat gradients, which are
	// always lossless, since lossy is 5 to 10 times larger on them.
	Few Class = iota
	// Many is 17 to 256 colours: antialiased text and smooth gradients,
	// lossless while still and lossy while they move, since lossless
	// costs 15 times lossy on the motion scene's plasma.
	Many
	// Photo is over 256 colours, which is lossy.
	Photo
)

// FewColours is the most colours of a tile in class Few.
const FewColours = 16

// StillSSIM is the SSIM, on luma as the codec measurement scores it,
// that Still's quality reaches on each tile of a still rectangle. On the
// corpus's photograph, rows of WebP reach it at quality 87 (median) in
// 121.5 KiB, under G6's 139 (docs/benchmarks/refinement.md); 0.92 takes
// 141.9 KiB, 0.95 is beyond quality 90, the pacer's highest, on two
// fifths of its tiles, and the guacd baseline's JPEG at quality 70
// scores 0.871 over the whole photograph.
const StillSSIM = 0.91

// QualityStep is the distance between the qualities Still tries: WebP's
// bytes on the photograph grow by about a tenth a quality point near 87.
const QualityStep = 3

// firstHint is where an encoder's first search starts.
const firstHint = 80

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
	// pix holds pixels converted before they are encoded: a cursor's to
	// straight alpha, a lossy rectangle's to opaque.
	pix []byte

	// What Still works in: the encoding tried last and the best that
	// reached StillSSIM, a JPEG decoded to RGBA, the scorer, and the
	// quality the last search chose, where the next starts.
	cand, best []byte
	dec        *image.RGBA
	ssim       ssimer
	hint       int
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
		hint: firstHint,
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
// merge it with adjacent lossy tiles and encode them with Lossy or Still.
// A tile goes lossy if it has more than 256 colours, or more than 16 and
// moving is set: content picks the codec, and motion too for tiles of 17
// to 256 colours, which are lossless only while still.
func (e *Encoder) Tile(dst, pix []byte, w, h int, moving bool) (b []byte, c protocol.Codec, lossy bool) {
	if e.lossless == 0 {
		return dst, 0, true
	}
	n := codec.Colours(pix, w, h, &e.pal)
	if class := Classify(n); e.lossy != 0 && (class == Photo || class == Many && moving) {
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
// ones merged into a rectangle. It is encoded opaque, whatever its alpha,
// since a screen has none: a pixel that is not opaque keeps its red,
// green and blue, and WebP gets no alpha channel. For a viewer with no
// lossy codec, or if the lossy encoder fails, as it does for a rectangle
// wider or higher than WebP allows (16383 pixels), the encoding is
// lossless; it returns codec 0 only if there is none.
func (e *Encoder) Lossy(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec) {
	pix = e.opaque(pix[:4*w*h])
	if b, ok := e.lossyAt(dst, pix, w, h, quality); ok {
		return b, e.lossy
	}
	return e.fallback(dst, pix, w, h)
}

// lossyAt appends the lossy encoding of pix, opaque, at quality, and
// reports false if the viewer has no lossy codec or the encoder failed.
func (e *Encoder) lossyAt(dst, pix []byte, w, h, quality int) ([]byte, bool) {
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
	return b, e.lossy != 0 && err == nil
}

// fallback appends pix, opaque, in the lossless codec, for a viewer with
// no lossy codec or a rectangle the lossy encoder refused; it returns
// codec 0 if the viewer has no lossless codec either.
func (e *Encoder) fallback(dst, pix []byte, w, h int) ([]byte, protocol.Codec) {
	if e.lossless == 0 {
		return dst, 0
	}
	var p *codec.Palette
	if e.pal.Build(pix, w, h) {
		p = &e.pal
	}
	return e.appendLossless(dst, pix, w, h, p), e.lossless
}

// Still appends the lossy encoding of pix, as Lossy does, at the lowest
// quality from lo to hi at which the SSIM of each of its tiles, tile
// pixels wide side by side, the last maybe narrower, reaches StillSSIM,
// and returns it with its codec and quality: in a rectangle of tiles a
// writer merged, each tile reaches the score, not only their mean. If no
// quality up to hi reaches it, the encoding is at hi and reached is
// false. It tries qualities QualityStep apart, and hi, starting where its
// last search ended, since a screen's photographic tiles tend to need
// alike: on most rectangles it encodes and decodes twice. A rectangle
// smaller than SSIM's 8×8 window has no score: it goes at hi, and
// reached is false, so that a writer raises it as hi rises. For a
// viewer with no lossy
// codec, or a rectangle the lossy encoder refuses, the encoding is
// lossless, at quality 100.
func (e *Encoder) Still(dst, pix []byte, w, h, tile, lo, hi int) (b []byte, c protocol.Codec, quality int, reached bool) {
	hi = min(max(hi, 1), 100)
	lo = min(max(lo, 1), hi)
	pix = e.opaque(pix[:4*w*h])
	if w < ssimWindow || h < ssimWindow {
		if b, ok := e.lossyAt(dst, pix, w, h, hi); ok {
			return b, e.lossy, hi, false
		}
		b, c := e.fallback(dst, pix, w, h)
		return b, c, 100, true
	}
	// The ladder is lo, lo+QualityStep, ... below hi, then hi.
	steps := (hi-lo+QualityStep-1)/QualityStep + 1
	at := func(i int) int { return min(lo+i*QualityStep, hi) }
	pass := func(i int) (ok, failed bool) {
		var enc bool
		e.cand, enc = e.lossyAt(e.cand[:0], pix, w, h, at(i))
		if !enc {
			return false, true
		}
		got, err := e.decode(e.cand, w, h)
		if err != nil {
			return false, true
		}
		return e.ssim.score(pix, got, w, h, tile) >= StillSSIM, false
	}
	i := min(max((e.hint-lo+QualityStep/2)/QualityStep, 0), steps-1)
	ok, failed := pass(i)
	switch {
	case failed:
		b, c := e.fallback(dst, pix, w, h)
		return b, c, 100, true
	case ok:
		// Down while the next lower quality still reaches the score.
		e.best = append(e.best[:0], e.cand...)
		for i > 0 {
			if ok, _ := pass(i - 1); !ok {
				break
			}
			i--
			e.best = append(e.best[:0], e.cand...)
		}
		reached = true
	default:
		// Up until one reaches it, or hi.
		for i+1 < steps {
			i++
			ok, failed := pass(i)
			if failed {
				b, c := e.fallback(dst, pix, w, h)
				return b, c, 100, true
			}
			if ok {
				reached = true
				break
			}
		}
		e.best = append(e.best[:0], e.cand...)
	}
	e.hint = at(i)
	return append(dst, e.best...), e.lossy, at(i), reached
}

// errDecoded reports an encoding that decodes to something other than
// what was encoded.
var errDecoded = errors.New("encoder: a lossy encoding decodes to another size")

// decode decodes a lossy encoding of w by h pixels to RGBA, as a client
// shows it: WebP as libwebp decodes it, JPEG as image/jpeg does.
func (e *Encoder) decode(b []byte, w, h int) ([]byte, error) {
	if e.lossy == protocol.CodecWebP {
		m, err := webp.Decode(bytes.NewReader(b), webp.Options{ToRGBA: true, Threads: 1, FrameSizeLimit: w * h})
		if err != nil {
			return nil, err
		}
		img, ok := m.(*image.RGBA)
		if !ok || img.Rect.Dx() != w || img.Rect.Dy() != h {
			return nil, errDecoded
		}
		return img.Pix, nil
	}
	m, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if m.Bounds().Dx() != w || m.Bounds().Dy() != h {
		return nil, errDecoded
	}
	if e.dec == nil || e.dec.Rect.Dx() != w || e.dec.Rect.Dy() != h {
		e.dec = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	draw.Draw(e.dec, e.dec.Rect, m, m.Bounds().Min, draw.Src)
	return e.dec.Pix, nil
}

// Cursor appends the encoding of a cursor's image to dst, in a codec
// that keeps it exact, and returns it with its codec. pix is w by h
// pixels as an image.RGBA holds them, row by row with alpha
// premultiplied; the payload carries straight alpha, as every codec of
// the protocol does, converted as color.NRGBAModel converts. It reports
// false, with dst unchanged, for a viewer that agreed none: JPEG has no
// alpha.
func (e *Encoder) Cursor(dst, pix []byte, w, h int) (b []byte, c protocol.Codec, ok bool) {
	if e.lossless == 0 && !e.webp {
		return dst, 0, false
	}
	pix = e.straight(pix[:4*w*h])
	switch {
	case e.lossless == protocol.CodecDeflate:
		return e.def.Append(dst, pix, w, h), protocol.CodecDeflate, true
	case e.lossless == protocol.CodecRaw:
		return codec.AppendRaw(dst, pix, w, h), protocol.CodecRaw, true
	case e.webp:
		e.out.b = dst
		img := &image.NRGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
		// Without Exact, vpx may rewrite the colour of transparent
		// pixels, which straight leaves zero, and the cursor would not
		// decode as sent.
		err := webp.Encode(&e.out, img, webp.EncodeOptions{Lossless: true, Exact: true, Method: 0, Threads: 1})
		b, e.out.b = e.out.b, nil
		if err == nil {
			return b, protocol.CodecWebP, true
		}
	}
	return dst, 0, false
}

// straight returns pix, premultiplied as in an image.RGBA, with straight
// alpha, in e.pix: each colour channel times 0xffff over alpha, in 16
// bits, as color.NRGBAModel converts, and transparent pixels zero.
func (e *Encoder) straight(pix []byte) []byte {
	e.pix = slices.Grow(e.pix[:0], len(pix))[:len(pix)]
	out := e.pix
	for i := 0; i < len(pix); i += 4 {
		switch a := pix[i+3]; a {
		case 0xff:
			copy(out[i:i+4], pix[i:i+4])
		case 0:
			out[i], out[i+1], out[i+2], out[i+3] = 0, 0, 0, 0
		default:
			a16 := uint32(a) * 0x101
			for k := range 3 {
				// A channel above alpha is not premultiplied; it saturates.
				c := min(uint32(pix[i+k])*0x101*0xffff/a16, 0xffff)
				out[i+k] = uint8(c >> 8)
			}
			out[i+3] = a
		}
	}
	return out
}

// opaque returns pix with every alpha 0xff: pix itself if it is opaque
// already, as a screen's pixels are, and else a copy in e.pix.
func (e *Encoder) opaque(pix []byte) []byte {
	i := 3
	for i < len(pix) && pix[i] == 0xff {
		i += 4
	}
	if i >= len(pix) {
		return pix
	}
	e.pix = append(e.pix[:0], pix...)
	for ; i < len(e.pix); i += 4 {
		e.pix[i] = 0xff
	}
	return e.pix
}

// sliceWriter appends what is written to a slice.
type sliceWriter struct{ b []byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
