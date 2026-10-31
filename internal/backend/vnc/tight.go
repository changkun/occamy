// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"slices"
)

// encTight is the Tight encoding (rfbproto, "Tight Encoding").
const encTight int32 = 7

// Tight's compression control, after its four reset bits, and filters.
const (
	tightFill           = 8
	tightJPEG           = 9
	tightExplicitFilter = 4
	tightFilterCopy     = 0
	tightFilterPalette  = 1
	tightFilterGradient = 2
)

const (
	// tightMinToCompress is the size below which a basic rectangle's data
	// is sent as it is, without zlib.
	tightMinToCompress = 12
	// tightMaxJPEGWidth bounds the width of a JPEG rectangle, as
	// TigerVNC's viewer bounds every Tight rectangle but fills; servers
	// split larger areas.
	tightMaxJPEGWidth = 2048
	// tightMaxJPEGPixels bounds a JPEG rectangle's area, since image/jpeg
	// allocates the whole image and its decoding time grows with it.
	// TigerVNC, libvncserver and QEMU send JPEG rectangles of at most
	// 65536 pixels.
	tightMaxJPEGPixels = 65536
	// jpegChunk is the most JPEG data read before the buffer grows, so
	// that the buffer grows with the data that arrives, not with the
	// length the server claims.
	jpegChunk = 64 << 10
)

// decodeJPEG decodes a Tight JPEG image once its markers and size have
// passed jpegFrame. It is a variable so that tests can count the images
// that reach image/jpeg.
var decodeJPEG = jpeg.Decode

// tight draws a Tight rectangle. Its compression control resets any of
// the connection's four zlib streams first, then says how the rectangle
// is sent: filled with one colour, as a JPEG image, or with the basic
// compression, whose pixels go through a filter (copy, palette or
// gradient) and are compressed with one of the four streams. The
// backend's pixel format makes Tight's compact pixels 3 bytes: red,
// green and blue.
func (d *decoder) tight(r image.Rectangle) error {
	var b [3]byte
	if err := d.read(b[:1]); err != nil {
		return err
	}
	ctl := b[0]
	for i := range d.enc.tight {
		if ctl&(1<<i) != 0 {
			d.enc.tight[i].reset()
		}
	}
	ctl >>= 4
	switch {
	case ctl == tightFill:
		if err := d.read(b[:3]); err != nil {
			return err
		}
		if r.Empty() {
			return nil
		}
		if err := d.onScreen("tight rectangle", r); err != nil {
			return err
		}
		d.fillRect(r, rgb(b[:]))
		d.enc.counts[tightFillRect]++
		return nil
	case ctl == tightJPEG:
		return d.tightJPEG(r)
	case ctl > tightJPEG:
		// 10 is TightPNG's PNG, which the backend does not ask for.
		return fmt.Errorf("tight compression type %d", ctl)
	}
	return d.tightBasic(r, int(ctl&3), ctl&tightExplicitFilter != 0)
}

// tightBasic draws a rectangle of Tight's basic compression, whose data is
// inflated by stream id, in strips of whole rows.
func (d *decoder) tightBasic(r image.Rectangle, id int, explicit bool) error {
	filter := byte(tightFilterCopy)
	if explicit {
		var b [1]byte
		if err := d.read(b[:]); err != nil {
			return err
		}
		filter = b[0]
	}
	w := r.Dx()
	var (
		pal  [256]uint32
		npal int
		row  int // bytes a row of data
	)
	switch filter {
	case tightFilterCopy, tightFilterGradient:
		row = 3 * w
	case tightFilterPalette:
		var b [1]byte
		if err := d.read(b[:]); err != nil {
			return err
		}
		npal = int(b[0]) + 1
		buf := d.enc.tile[:3*npal]
		if err := d.read(buf); err != nil {
			return err
		}
		for i := range pal[:npal] {
			pal[i] = rgb(buf[3*i:])
		}
		// Two colours or one, as TigerVNC reads it, are bits.
		row = w
		if npal <= 2 {
			row = (w + 7) / 8
		}
	default:
		return fmt.Errorf("tight filter %d", filter)
	}
	size := row * r.Dy()
	if size == 0 {
		return nil
	}
	if err := d.onScreen("tight rectangle", r); err != nil {
		return err
	}
	var z *zstream
	if size >= tightMinToCompress {
		n, err := d.compactLength()
		if err != nil {
			return err
		}
		if bound := deflateBound(size); n > bound {
			return fmt.Errorf("tight data of %d bytes compressed to %d, more than %d", size, n, bound)
		}
		z = &d.enc.tight[id]
		if err := z.begin(d.r, int64(n)); err != nil {
			return fmt.Errorf("tight stream %d: %w", id, err)
		}
		d.enc.counts[tightZlibData]++
	} else {
		d.enc.counts[tightRawData]++
	}

	if filter == tightFilterGradient {
		d.enc.prev = slices.Grow(d.enc.prev[:0], row)[:row]
		clear(d.enc.prev)
	}
	rows := max(1, len(d.strip)/row)
	for y := r.Min.Y; y < r.Max.Y; y += rows {
		strip := image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y))
		data := d.strip[:row*strip.Dy()]
		var err error
		if z != nil {
			err = z.read(data)
		} else {
			err = d.read(data)
		}
		if err != nil {
			return err
		}
		switch filter {
		case tightFilterPalette:
			if err := checkIndices(data, row, w, npal); err != nil {
				return err
			}
			d.drawPalette(strip, data, row, pal[:], npal <= 2)
		case tightFilterGradient:
			gradient(data, row, d.enc.prev)
			d.drawCompact(strip, data)
		default:
			d.drawCompact(strip, data)
		}
	}
	switch filter {
	case tightFilterPalette:
		d.enc.counts[tightPaletteRect]++
	case tightFilterGradient:
		d.enc.counts[tightGradientRect]++
	default:
		d.enc.counts[tightCopyRect]++
	}
	if z != nil {
		if err := z.end(); err != nil {
			return fmt.Errorf("tight stream %d: %w", id, err)
		}
	}
	return nil
}

// compactLength reads Tight's compact length: 7 bits in each of the first
// two bytes, which have the top bit set if another byte follows, and 8 in
// the third, so at most 4 MiB.
func (d *decoder) compactLength() (int, error) {
	var b [1]byte
	n := 0
	for i := range 3 {
		if err := d.read(b[:]); err != nil {
			return 0, err
		}
		if i == 2 {
			return n | int(b[0])<<14, nil
		}
		n |= int(b[0]&0x7f) << (7 * i)
		if b[0]&0x80 == 0 {
			break
		}
	}
	return n, nil
}

// drawCompact draws rows of compact pixels.
func (d *decoder) drawCompact(r image.Rectangle, data []byte) {
	row := 3 * r.Dx()
	img := d.sink.Lock(r)
	defer d.sink.Unlock(r)
	for j := range r.Dy() {
		expand(img.Pix[j*img.Stride:j*img.Stride+4*r.Dx()], data[j*row:])
	}
}

// checkIndices returns an error if a row of palette data, row bytes for w
// pixels, has an index beyond the palette's n colours: with bits for two
// colours or one, a set bit when there is only one.
func checkIndices(data []byte, row, w, n int) error {
	switch {
	case n == 1:
		for j := 0; j < len(data); j += row {
			for i := range w {
				if data[j+i/8]&(0x80>>(i%8)) != 0 {
					return fmt.Errorf("palette index 1 of 1 colour")
				}
			}
		}
	case n > 2 && n < 256:
		for _, c := range data {
			if int(c) >= n {
				return fmt.Errorf("palette index %d of %d colours", c, n)
			}
		}
	}
	return nil
}

// drawPalette draws rows of palette indices into pal, which has 256
// colours: if bits, a bit a pixel, the most significant first, and
// otherwise a byte.
func (d *decoder) drawPalette(r image.Rectangle, data []byte, row int, pal []uint32, bits bool) {
	w := r.Dx()
	img := d.sink.Lock(r)
	defer d.sink.Unlock(r)
	for j := range r.Dy() {
		src, dst := data[j*row:(j+1)*row], img.Pix[j*img.Stride:]
		for i := range w {
			var idx byte
			if bits {
				idx = src[i/8] >> (7 - i%8) & 1
			} else {
				idx = src[i]
			}
			binary.LittleEndian.PutUint32(dst[4*i:], pal[idx])
		}
	}
}

// gradient reverses the gradient filter in place, row by row: each
// component was sent as its difference from a prediction, the left
// neighbour plus the one above minus the one above and left, clamped to
// [0, 255], with zero beyond the rectangle's top and left edges. prev is
// the row above the first, and is left holding the last.
func gradient(data []byte, row int, prev []byte) {
	for j := 0; j < len(data); j += row {
		cur := data[j : j+row]
		for c := range 3 {
			cur[c] += prev[c]
		}
		for i := 3; i < row; i++ {
			est := int(prev[i]) + int(cur[i-3]) - int(prev[i-3])
			cur[i] += byte(min(max(est, 0), 255))
		}
		copy(prev, cur)
	}
}

// tightJPEG draws a Tight JPEG rectangle: a compact length, then a JPEG
// image of the rectangle's size, which is opaque. The image's markers,
// and its size, are checked before it is decoded (jpegFrame).
func (d *decoder) tightJPEG(r image.Rectangle) error {
	n, err := d.compactLength()
	if err != nil {
		return err
	}
	if r.Empty() {
		return d.discard(int64(n))
	}
	if r.Dx() > tightMaxJPEGWidth || r.Dx()*r.Dy() > tightMaxJPEGPixels {
		return fmt.Errorf("a tight jpeg rectangle of %d×%d exceeds %d pixels wide or %d in all", r.Dx(), r.Dy(), tightMaxJPEGWidth, tightMaxJPEGPixels)
	}
	if err := d.onScreen("tight rectangle", r); err != nil {
		return err
	}
	// The buffer grows as the data arrives, doubling up to n.
	buf := d.enc.jpeg[:0]
	for len(buf) < n {
		k := min(n-len(buf), jpegChunk)
		if cap(buf)-len(buf) < k {
			grown := make([]byte, len(buf), len(buf)+min(n-len(buf), max(k, len(buf))))
			copy(grown, buf)
			buf = grown
		}
		if err := d.read(buf[len(buf) : len(buf)+k]); err != nil {
			d.enc.jpeg = buf
			return err
		}
		buf = buf[:len(buf)+k]
	}
	d.enc.jpeg = buf
	size, err := jpegFrame(buf)
	if err != nil {
		return fmt.Errorf("tight jpeg: %w", err)
	}
	if size != r.Size() {
		return fmt.Errorf("a tight jpeg image of %d×%d for a rectangle of %d×%d", size.X, size.Y, r.Dx(), r.Dy())
	}
	img, err := decodeJPEG(bytes.NewReader(buf))
	if errors.Is(err, io.ErrUnexpectedEOF) {
		// The data arrived whole; it is the image that is cut short, which
		// is not the connection's io.ErrUnexpectedEOF.
		return errors.New("tight jpeg: the image ends before its pixels")
	}
	if err != nil {
		return fmt.Errorf("tight jpeg: %w", err)
	}
	// image/jpeg's images start at (0, 0); drawing them with draw.Src
	// makes the pixels opaque.
	rows := max(1, stripSize/(4*r.Dx()))
	for y := r.Min.Y; y < r.Max.Y; y += rows {
		strip := image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y))
		d.drawImage(strip, img, image.Pt(0, y-r.Min.Y))
	}
	d.enc.counts[tightJPEGRect]++
	return nil
}

// drawImage draws src from sp to strip under one lock.
func (d *decoder) drawImage(strip image.Rectangle, src image.Image, sp image.Point) {
	dst := d.sink.Lock(strip)
	defer d.sink.Unlock(strip)
	draw.Draw(dst, strip, src, sp, draw.Src)
}

// jpegFrame checks a JPEG image's markers, and returns the size its frame
// header gives. image/jpeg decodes every scan of an image, and a
// progressive image can have thousands of scans of a few bytes, each a
// pass over every block: 16,000 scans in 300 KB took 13 s at 2048×2048.
// Only baseline and extended sequential images (SOF0 and SOF1), which
// TigerVNC, libvncserver and QEMU send, are accepted, with at most a
// scan per component, as sequential JPEG has, so that decoding costs no
// more than a pass over the image's blocks for each component.
func jpegFrame(data []byte) (image.Point, error) {
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		return image.Point{}, errors.New("no start of image")
	}
	var size image.Point
	comps, scans := 0, 0
	for i := 2; ; {
		// A marker: 0xFF, any fill bytes of 0xFF, and its code.
		if i >= len(data) || data[i] != 0xff {
			return image.Point{}, fmt.Errorf("no marker at byte %d", i)
		}
		for i < len(data) && data[i] == 0xff {
			i++
		}
		if i >= len(data) {
			return image.Point{}, errors.New("the image ends within a marker")
		}
		m := data[i]
		i++
		if m == 0xd9 { // EOI
			if scans == 0 {
				return image.Point{}, errors.New("no scan")
			}
			return size, nil
		}
		if i+2 > len(data) {
			return image.Point{}, errors.New("the image ends within a marker")
		}
		n := int(binary.BigEndian.Uint16(data[i:]))
		if n < 2 || i+n > len(data) {
			return image.Point{}, fmt.Errorf("a segment of %d bytes at byte %d, beyond the image's %d", n, i, len(data))
		}
		seg := data[i+2 : i+n]
		i += n
		switch {
		case m == 0xc0 || m == 0xc1: // SOF0, SOF1
			if comps != 0 {
				return image.Point{}, errors.New("a second frame")
			}
			if len(seg) < 6 || seg[5] == 0 {
				return image.Point{}, errors.New("an invalid frame header")
			}
			size = image.Pt(int(binary.BigEndian.Uint16(seg[3:])), int(binary.BigEndian.Uint16(seg[1:])))
			comps = int(seg[5])
		case m == 0xda: // SOS
			if comps == 0 {
				return image.Point{}, errors.New("a scan before the frame header")
			}
			if scans++; scans > comps {
				return image.Point{}, fmt.Errorf("more scans than components: the frame has %d", comps)
			}
			// The scan's entropy-coded data runs to the next marker, other
			// than stuffed bytes (0xFF 0x00) and restart markers.
			for {
				k := bytes.IndexByte(data[i:], 0xff)
				if k < 0 || i+k+1 >= len(data) {
					return image.Point{}, errors.New("the image ends within a scan")
				}
				i += k
				if c := data[i+1]; c != 0 && (c < 0xd0 || c > 0xd7) {
					break
				}
				i += 2
			}
		case m == 0xc4, m == 0xdb, m == 0xdd, m == 0xfe, 0xe0 <= m && m <= 0xef:
			// DHT, DQT, DRI, COM and APPn.
		case 0xc0 <= m && m <= 0xcf && m != 0xc8 && m != 0xcc:
			return image.Point{}, fmt.Errorf("a frame of type SOF%d; only baseline and extended sequential JPEG (SOF0, SOF1) is accepted", m-0xc0)
		default:
			return image.Point{}, fmt.Errorf("marker %#x", m)
		}
	}
}
