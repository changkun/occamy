// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"time"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
)

// Server-to-client message types (RFC 6143, section 7.6).
const (
	msgFramebufferUpdate   = 0
	msgSetColourMapEntries = 1
	msgBell                = 2
	msgServerCutText       = 3
)

const (
	// stripSize bounds the pixels read before they are drawn. A Raw
	// rectangle is read and drawn in strips of whole rows, so that the
	// framebuffer is never locked while the backend waits for the network
	// and memory does not grow with the rectangle. A row of MaxSide
	// pixels fits.
	stripSize = 256 << 10
	// maxCursor is the largest width or height of a cursor, as in
	// TigerVNC's viewer.
	maxCursor = 256
	// resizeEvery is the least time between two resizes. A DesktopSize
	// that comes sooner waits, so that a server cannot make the session
	// reallocate its screen faster than ten times a second; an
	// interactive resize still follows at that rate. TigerVNC's viewer
	// waits 500 ms before it asks for a resize of its own.
	resizeEvery = 100 * time.Millisecond
)

// decoder reads the server's messages and applies them to a sink. Every
// length is checked before anything is read or allocated for it, and
// every rectangle against the screen before it is drawn, so a server
// cannot make it allocate without bound or break the sink's contract.
type decoder struct {
	ctx  context.Context // ends the decoding between messages and rectangles
	r    io.Reader
	sink backend.Sink
	lim  limits
	log  *slog.Logger

	size        image.Point   // the screen
	resized     bool          // the current or last update resized the screen
	resizeEvery time.Duration // the least time between two resizes
	lastResize  time.Time

	hdr    [12]byte
	strip  []byte // stripSize bytes
	cursor []byte // a cursor's pixels and mask, grown up to maxCursor
	img    image.RGBA
}

func newDecoder(ctx context.Context, r io.Reader, sink backend.Sink, size image.Point, lim limits, log *slog.Logger) *decoder {
	return &decoder{
		ctx: ctx, r: r, sink: sink, lim: lim, log: log,
		size: size, resizeEvery: resizeEvery, strip: make([]byte, stripSize),
	}
}

// read fills b. A connection that ends within a message is
// io.ErrUnexpectedEOF.
func (d *decoder) read(b []byte) error { return read(d.r, b) }

// discard skips n bytes.
func (d *decoder) discard(n int64) error {
	_, err := io.CopyN(io.Discard, d.r, n)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// next reads one message and applies it, and returns its type. It
// returns io.EOF, unwrapped, only when the connection ends before the
// message's first byte, and the context's error once it is done: what
// is left in the read buffer is not applied.
func (d *decoder) next() (byte, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	var t [1]byte
	if _, err := io.ReadFull(d.r, t[:]); err != nil {
		return 0, err
	}
	var err error
	switch t[0] {
	case msgFramebufferUpdate:
		err = d.update()
	case msgSetColourMapEntries:
		err = d.colourMap()
	case msgBell:
		d.sink.Bell()
	case msgServerCutText:
		err = d.cutText()
	default:
		err = fmt.Errorf("vnc: unknown message type %d", t[0])
	}
	return t[0], err
}

// update applies a FramebufferUpdate.
func (d *decoder) update() error {
	d.resized = false
	if err := d.read(d.hdr[:3]); err != nil {
		return fmt.Errorf("vnc: framebuffer update: %w", err)
	}
	n := int(binary.BigEndian.Uint16(d.hdr[1:]))
	for i := range n {
		err := d.ctx.Err()
		if err == nil {
			err = d.rect()
		}
		if err != nil {
			return fmt.Errorf("vnc: framebuffer update, rectangle %d of %d: %w", i+1, n, err)
		}
	}
	return nil
}

// rect applies one rectangle of a FramebufferUpdate.
func (d *decoder) rect() error {
	if err := d.read(d.hdr[:12]); err != nil {
		return err
	}
	x, y := int(binary.BigEndian.Uint16(d.hdr[0:])), int(binary.BigEndian.Uint16(d.hdr[2:]))
	w, h := int(binary.BigEndian.Uint16(d.hdr[4:])), int(binary.BigEndian.Uint16(d.hdr[6:]))
	enc := int32(binary.BigEndian.Uint32(d.hdr[8:])) // #nosec G115 -- encodings are signed on the wire
	r := image.Rect(x, y, x+w, y+h)
	switch enc {
	case encRaw:
		return d.raw(r)
	case encCopyRect:
		return d.copyRect(r)
	case encDesktopSize:
		return d.desktopSize(w, h)
	case encCursor:
		return d.setCursor(image.Pt(x, y), w, h)
	}
	return fmt.Errorf("unsupported encoding %d", enc)
}

// onScreen returns an error if r, which is not empty, is not within the
// screen.
func (d *decoder) onScreen(what string, r image.Rectangle) error {
	if !r.In(image.Rectangle{Max: d.size}) {
		return fmt.Errorf("%s %v is outside the screen of %d×%d", what, r, d.size.X, d.size.Y)
	}
	return nil
}

// raw draws a Raw rectangle: its pixels row by row, in the backend's
// pixel format.
func (d *decoder) raw(r image.Rectangle) error {
	if r.Empty() {
		return nil
	}
	if err := d.onScreen("raw rectangle", r); err != nil {
		return err
	}
	row := 4 * r.Dx()
	rows := len(d.strip) / row
	for y := r.Min.Y; y < r.Max.Y; y += rows {
		strip := image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y))
		buf := d.strip[:row*strip.Dy()]
		if err := d.read(buf); err != nil {
			return err
		}
		img := d.sink.Lock(strip)
		for i := range strip.Dy() {
			copyOpaque(img.Pix[i*img.Stride:i*img.Stride+row], buf[i*row:])
		}
		d.sink.Unlock(strip)
	}
	return nil
}

// copyOpaque copies pixels from src to dst, which are in the same layout,
// and makes them opaque: the fourth byte is padding on the wire and alpha
// in the framebuffer.
func copyOpaque(dst, src []byte) {
	copy(dst, src)
	for i := 3; i < len(dst); i += 4 {
		dst[i] = 0xff
	}
}

// copyRect applies a CopyRect rectangle: the source's position follows.
func (d *decoder) copyRect(r image.Rectangle) error {
	var b [4]byte
	if err := d.read(b[:]); err != nil {
		return err
	}
	if r.Empty() {
		return nil
	}
	src := image.Pt(int(binary.BigEndian.Uint16(b[0:])), int(binary.BigEndian.Uint16(b[2:])))
	if err := d.onScreen("copy-rect destination", r); err != nil {
		return err
	}
	if err := d.onScreen("copy-rect source", r.Sub(r.Min).Add(src)); err != nil {
		return err
	}
	d.sink.CopyRect(r, src)
	return nil
}

// desktopSize applies a DesktopSize pseudo-rectangle: the screen is now
// w×h, and its pixels are lost. Rectangles after it in the same update
// are drawn on the new screen. An update resizes the screen at most
// once, as servers send it (RFC 6143, section 7.8.2), so that a few
// bytes cannot reallocate the screen thousands of times; and resizes
// come at most every resizeEvery.
func (d *decoder) desktopSize(w, h int) error {
	if d.resized {
		return errors.New("a second DesktopSize in one update")
	}
	if err := d.lim.check(w, h); err != nil {
		return err
	}
	if wait := time.Until(d.lastResize.Add(d.resizeEvery)); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-d.ctx.Done():
			t.Stop()
			return d.ctx.Err()
		case <-t.C:
		}
	}
	d.lastResize = time.Now()
	d.size = image.Pt(w, h)
	d.resized = true
	d.sink.Resize(w, h)
	return nil
}

// setCursor applies a Cursor pseudo-rectangle: w×h pixels in the
// backend's pixel format, then a bitmask with a row of whole bytes per
// row of pixels, the most significant bit leftmost, set where the cursor
// is opaque. Its position is the hot spot. A cursor of no pixels hides
// the pointer.
func (d *decoder) setCursor(hot image.Point, w, h int) error {
	if w > maxCursor || h > maxCursor {
		return fmt.Errorf("a cursor of %d×%d exceeds %d×%d", w, h, maxCursor, maxCursor)
	}
	if w == 0 || h == 0 {
		d.sink.Cursor(nil, image.Point{})
		return nil
	}
	stride := (w + 7) / 8
	n := 4*w*h + stride*h
	if cap(d.cursor) < n {
		d.cursor = make([]byte, n)
	}
	buf := d.cursor[:n]
	if err := d.read(buf); err != nil {
		return err
	}
	pix, mask := buf[:4*w*h], buf[4*w*h:]
	// The pixels are converted in place: buf is the image's too.
	for y := range h {
		for x := range w {
			p := pix[4*(y*w+x):][:4]
			if mask[y*stride+x/8]&(0x80>>(x%8)) != 0 {
				p[3] = 0xff
			} else {
				// image.RGBA is premultiplied, so transparent is zero.
				p[0], p[1], p[2], p[3] = 0, 0, 0, 0
			}
		}
	}
	d.img = image.RGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	// Some servers put the hot spot on the cursor's far edge.
	hot = image.Pt(min(hot.X, w-1), min(hot.Y, h-1))
	d.sink.Cursor(&d.img, hot)
	return nil
}

// colourMap skips a SetColourMapEntries, which a server sends only for a
// pixel format with a colour map, which the backend never sets.
func (d *decoder) colourMap() error {
	if err := d.read(d.hdr[:5]); err != nil {
		return fmt.Errorf("vnc: colour map: %w", err)
	}
	n := int64(binary.BigEndian.Uint16(d.hdr[3:]))
	if err := d.discard(6 * n); err != nil {
		return fmt.Errorf("vnc: colour map: %w", err)
	}
	return nil
}

// cutText applies a ServerCutText, of which the first maxClipboard bytes
// are kept. RFC 6143 says the text is Latin-1, but many servers send
// UTF-8, so it is read as text, as the desktop name is: UTF-8 if it is
// valid UTF-8, Latin-1 otherwise. A character that the truncation cut in
// two is dropped first.
func (d *decoder) cutText() error {
	if err := d.read(d.hdr[:7]); err != nil {
		return fmt.Errorf("vnc: cut text: %w", err)
	}
	n := binary.BigEndian.Uint32(d.hdr[3:])
	if n&(1<<31) != 0 {
		// A negative length is Extended Clipboard's, which the
		// backend does not ask for.
		return fmt.Errorf("vnc: cut text of %d bytes", int32(n)) // #nosec G115 -- shown as the signed length it is
	}
	keep := min(int64(n), maxClipboard)
	// The buffer grows as the text arrives, not to the length claimed.
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, d.r, keep); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("vnc: cut text: %w", err)
	}
	if rest := int64(n) - keep; rest > 0 {
		d.log.Warn("vnc: cut text truncated", "bytes", n, "kept", keep)
		if err := d.discard(rest); err != nil {
			return fmt.Errorf("vnc: cut text: %w", err)
		}
	}
	b := buf.Bytes()
	if t := trimPartialRune(b); int64(n) > keep && len(t) < len(b) && utf8.Valid(t) {
		b = t // UTF-8 cut short; Latin-1 is kept whole
	}
	d.sink.Clipboard("text/plain", []byte(text(b)))
	return nil
}

// trimPartialRune drops an incomplete UTF-8 sequence from the end of b,
// as truncation leaves one.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}
