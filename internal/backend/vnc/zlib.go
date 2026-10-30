// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
)

const (
	// maxZlibTail bounds the compressed bytes of a rectangle that are left
	// once its pixels are decoded. A server flushes its stream at the end
	// of each rectangle (Z_SYNC_FLUSH), which ends the data with an empty
	// stored block, and Go's inflater hands out what it has inflated at
	// the end of a stored block, when its 32 KiB window fills, and at the
	// stream's end. So the inflater normally uses all of a rectangle's
	// data, flush included. When the window fills just as the pixels end,
	// or a server flushes twice, the end of the last block or an empty
	// stored block, a few bytes, are left for the next rectangle.
	maxZlibTail = 64
	// zlibBuffer is the size of the buffer that a stream's output is read
	// through: the inflater's window, the most it hands out at once, so
	// that each time the buffer is filled it takes all that the inflater
	// has handed out, and inflated data that a rectangle's pixels do not
	// take is left in the buffer, where end sees it.
	zlibBuffer = 32 << 10
)

var (
	// errZlibShort is the inflater's error when a rectangle's compressed
	// data ends before its pixels do.
	errZlibShort = errors.New("the compressed data ends before the rectangle's pixels")
	// errZlibEnd is returned when the server ends a stream, which RFB
	// keeps open for the whole connection.
	errZlibEnd = errors.New("the server ended its zlib stream")
)

// deflateBound is the most compressed data a deflate encoder sends for n
// bytes of data and the flush after them: an eighth more than n, as fixed
// Huffman codes spend up to 9 bits on a byte, and 1 KiB for headers.
// A length beyond it is refused before anything is read.
func deflateBound(n int) int { return n + n/8 + 1024 }

// zstream is one of the server's zlib streams: ZRLE's, or one of Tight's
// four. A server keeps each stream for the whole connection and flushes it
// at the end of each rectangle, so a rectangle's data continues the
// stream of the rectangles before it, and back-references reach into
// them. The stream's first rectangle, and the first after Tight resets
// it, starts with the zlib header.
//
// The inflater reads the connection itself, through zinput, which hands
// it the rectangle's compressed bytes and no more, and the decoder reads
// as much inflated data as the rectangle's pixels need, through out, so
// the compressed data is never buffered and the inflated data at most a
// window at a time. Since Go's inflater hands out data at the end of a
// stored block, a stream that a server flushes in another way, such as
// Z_PARTIAL_FLUSH, fails with errZlibShort; TigerVNC, libvncserver and
// QEMU flush with Z_SYNC_FLUSH. A stream lives as long as the decoder,
// which is Run's.
type zstream struct {
	in      zinput
	started bool          // the zlib header has been read
	fr      io.ReadCloser // the inflater, nil before the first rectangle
	out     *bufio.Reader // fr's output
}

// begin starts a rectangle whose n bytes of compressed data follow on r.
func (z *zstream) begin(r io.Reader, n int64) error {
	z.in.r = r
	z.in.br, _ = r.(io.ByteReader)
	z.in.n = n
	if z.started {
		return nil
	}
	// RFC 1950: CM 8 (deflate) with a window of at most 32 KiB, no preset
	// dictionary, and a check value that makes the header a multiple of
	// 31.
	var h [2]byte
	if _, err := io.ReadFull(&z.in, h[:]); err != nil {
		return fmt.Errorf("zlib header: %w", err)
	}
	if h[0]&0x0f != 8 || h[0]>>4 > 7 || h[1]&0x20 != 0 || (uint(h[0])<<8|uint(h[1]))%31 != 0 {
		return fmt.Errorf("invalid zlib header %#x", h)
	}
	if z.fr == nil {
		z.fr = flate.NewReader(&z.in)
		z.out = bufio.NewReaderSize(z.fr, zlibBuffer)
	} else {
		if err := z.fr.(flate.Resetter).Reset(&z.in, nil); err != nil {
			return err
		}
		z.out.Reset(z.fr)
	}
	z.started = true
	return nil
}

// end finishes a rectangle: its pixels have been read. What is left of its
// compressed data must be the end of the server's flush, which is kept
// for the next rectangle; inflated data that no pixel took is an error.
// The inflater holds back what it inflates after its window last filled,
// until the next stored block, so data beyond the pixels goes unseen when
// the window fills exactly where they end: it then starts the stream's
// next rectangle, and maxZlibTail bounds what it can inflate to.
func (z *zstream) end() error {
	if n := z.out.Buffered(); n > 0 {
		return fmt.Errorf("the compressed data holds %d bytes more than the rectangle's pixels", n)
	}
	return z.in.keepTail()
}

// reset discards the stream, as Tight's compression control asks: the
// next rectangle starts a new one.
func (z *zstream) reset() {
	z.started = false
	z.in.tail = nil
}

// read fills p with inflated data. It reads less than the buffer at a
// time, since bufio reads larger requests past its buffer.
func (z *zstream) read(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), zlibBuffer-1)
		if _, err := io.ReadFull(z.out, p[:n]); err != nil {
			return zlibError(err)
		}
		p = p[n:]
	}
	return nil
}

// byte reads one byte of inflated data.
func (z *zstream) byte() (byte, error) {
	c, err := z.out.ReadByte()
	return c, zlibError(err)
}

// zlibError maps the inflater's io.EOF, which it returns after a final
// block, to errZlibEnd, so that it cannot pass for the end of the
// connection.
func zlibError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		return errZlibEnd
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, errZlibShort):
		return err
	}
	return fmt.Errorf("zlib: %w", err)
}

// zinput is a zlib stream's input: the compressed bytes of earlier
// rectangles that the inflater has not used yet, then the current
// rectangle's on the connection. It reports errZlibShort, not io.EOF,
// when the rectangle's bytes run out, since the inflater keeps any error
// it sees and the stream's state is lost with it; a well-formed rectangle
// never runs out. It implements io.ByteReader, so that the inflater does
// not read ahead through a buffer of its own.
type zinput struct {
	r    io.Reader
	br   io.ByteReader // r, if it is one
	n    int64         // the rectangle's bytes not yet read from r
	tail []byte        // bytes of earlier rectangles not yet used, in buf
	buf  [maxZlibTail]byte
	one  [1]byte
}

func (z *zinput) ReadByte() (byte, error) {
	if len(z.tail) > 0 {
		c := z.tail[0]
		z.tail = z.tail[1:]
		return c, nil
	}
	if z.n <= 0 {
		return 0, errZlibShort
	}
	z.n--
	if z.br != nil {
		c, err := z.br.ReadByte()
		if err != nil && errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return c, err
	}
	err := read(z.r, z.one[:])
	return z.one[0], err
}

func (z *zinput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(z.tail) > 0 {
		n := copy(p, z.tail)
		z.tail = z.tail[n:]
		return n, nil
	}
	if z.n <= 0 {
		return 0, errZlibShort
	}
	if int64(len(p)) > z.n {
		p = p[:z.n]
	}
	n, err := z.r.Read(p)
	z.n -= int64(n)
	if errors.Is(err, io.EOF) {
		err = nil
		if n == 0 {
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}

// keepTail reads what is left of the rectangle's compressed bytes, which
// may not be more than maxZlibTail with those left from before, and keeps
// them for the stream's next rectangle.
func (z *zinput) keepTail() error {
	if z.n > int64(maxZlibTail-len(z.tail)) {
		return fmt.Errorf("%d bytes of compressed data are left after the rectangle's pixels", z.n+int64(len(z.tail)))
	}
	k := copy(z.buf[:], z.tail) // tail may lie in buf; copy moves it to the front
	n := int(z.n)
	if err := read(z.r, z.buf[k:k+n]); err != nil {
		return err
	}
	z.tail = z.buf[:k+n]
	z.n = 0
	return nil
}
