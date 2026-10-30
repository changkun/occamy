// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package rfb is the part of RFB 3.8 that the bench tools speak to the
// bench desktop's VNC server: VNC authentication, raw encoding, 32-bit
// true-colour pixels, the cursor pseudo-encoding, update requests and key
// events.
package rfb

import (
	"bufio"
	"crypto/des" // #nosec G502 -- VNC authentication is DES by definition
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"slices"
)

// Format is the byte order of the pixels the server sends, 32 bits each
// with the fourth byte unused.
type Format int

const (
	// BGRX is blue, green, red and padding in memory: red at bit 16 of a
	// little-endian pixel, which is the X server's own order at depth 24.
	BGRX Format = iota
	// RGBX is red, green, blue and padding in memory, the order of Go's
	// image.RGBA and a browser's ImageData.
	RGBX
)

// Options are what a client asks the server for.
type Options struct {
	// Format is the pixels' byte order.
	Format Format
	// CursorShape asks for the cursor's shape apart from the screen, so
	// that the server does not paint the pointer into FB. The shape is
	// read and discarded.
	CursorShape bool
}

// encodingCursor is the cursor pseudo-encoding, -239.
const encodingCursor = 0xffffff11

// Conn is a VNC session and the screen it has received.
type Conn struct {
	nc   net.Conn
	r    *bufio.Reader
	size []byte // the width and height as the server sent them

	// W and H are the screen's size in pixels.
	W, H int
	// FB is the screen, four bytes a pixel in the Format of Dial, rows
	// from the top.
	FB []byte
}

// Dial connects to the VNC server at addr, authenticates with password,
// and asks for raw encoding with the given options.
func Dial(addr, password string, o Options) (*Conn, error) {
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Conn{nc: nc, r: bufio.NewReaderSize(nc, 1<<20)}
	if err := c.handshake(password, o); err != nil {
		nc.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	return c, nil
}

// Close closes the connection.
func (c *Conn) Close() error { return c.nc.Close() }

func (c *Conn) read(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(c.r, b)
	return b, err
}

func (c *Conn) handshake(password string, o Options) error {
	if _, err := c.read(12); err != nil { // the server's version
		return err
	}
	if _, err := c.nc.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	count, err := c.read(1)
	if err != nil {
		return err
	}
	types, err := c.read(int(count[0]))
	if err != nil {
		return err
	}
	if !slices.Contains(types, 2) {
		return fmt.Errorf("no VNC authentication among %v", types)
	}
	if _, err := c.nc.Write([]byte{2}); err != nil {
		return err
	}
	challenge, err := c.read(16)
	if err != nil {
		return err
	}
	// The key is the password's first 8 bytes, each with its bits
	// reversed.
	key := make([]byte, 8)
	copy(key, password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key) // #nosec G405 -- see the import
	if err != nil {
		return err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	if _, err := c.nc.Write(response); err != nil {
		return err
	}
	result, err := c.read(4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result) != 0 {
		return errors.New("authentication failed")
	}
	if _, err := c.nc.Write([]byte{1}); err != nil { // shared
		return err
	}
	init, err := c.read(24)
	if err != nil {
		return err
	}
	c.size = init[0:4]
	c.W, c.H = int(binary.BigEndian.Uint16(init[0:])), int(binary.BigEndian.Uint16(init[2:]))
	if _, err := c.read(int(binary.BigEndian.Uint32(init[20:]))); err != nil { // the name
		return err
	}
	c.FB = make([]byte, c.W*c.H*4)
	// SetPixelFormat: 32 bits per pixel, depth 24, little endian, true
	// colour, 255 per channel, and the shifts of red, green and blue.
	// Then SetEncodings with raw encoding, and the cursor's shape if
	// asked for.
	redShift, blueShift := byte(16), byte(0)
	if o.Format == RGBX {
		redShift, blueShift = 0, 16
	}
	msg := []byte{
		0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, redShift, 8, blueShift, 0, 0, 0,
		2, 0, 0, 1, 0, 0, 0, 0,
	}
	if o.CursorShape {
		msg[23] = 2
		msg = binary.BigEndian.AppendUint32(msg, encodingCursor)
	}
	_, err = c.nc.Write(msg)
	return err
}

// Request asks for an update of the whole screen: only what changed if
// incremental, everything otherwise.
func (c *Conn) Request(incremental bool) error {
	b := []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if incremental {
		b[1] = 1
	}
	copy(b[6:], c.size)
	_, err := c.nc.Write(b)
	return err
}

// Key presses and releases the key with the given keysym.
func (c *Conn) Key(keysym uint32) error {
	b := []byte{4, 1, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[4:], keysym)
	binary.BigEndian.PutUint32(b[12:], keysym)
	_, err := c.nc.Write(b)
	return err
}

// ReadUpdate reads server messages until a framebuffer update has been
// applied to FB. It skips bells and clipboard text.
func (c *Conn) ReadUpdate() error {
	for {
		t, err := c.read(1)
		if err != nil {
			return err
		}
		switch t[0] {
		case 0: // FramebufferUpdate
			h, err := c.read(3)
			if err != nil {
				return err
			}
			for range binary.BigEndian.Uint16(h[1:]) {
				if err := c.rect(); err != nil {
					return err
				}
			}
			return nil
		case 2: // Bell
		case 3: // ServerCutText
			h, err := c.read(7)
			if err != nil {
				return err
			}
			if _, err := c.read(int(binary.BigEndian.Uint32(h[3:]))); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected message %d", t[0])
		}
	}
}

func (c *Conn) rect() error {
	h, err := c.read(12)
	if err != nil {
		return err
	}
	x, y := int(binary.BigEndian.Uint16(h[0:])), int(binary.BigEndian.Uint16(h[2:]))
	w, ht := int(binary.BigEndian.Uint16(h[4:])), int(binary.BigEndian.Uint16(h[6:]))
	switch enc := binary.BigEndian.Uint32(h[8:]); enc {
	case 0:
	case encodingCursor:
		// The shape's pixels, then a bit mask with rows padded to bytes.
		_, err := c.r.Discard(w*ht*4 + (w+7)/8*ht)
		return err
	default:
		return fmt.Errorf("encoding %d, not raw", int32(enc)) // #nosec G115 -- encodings are signed
	}
	if x+w > c.W || y+ht > c.H {
		return fmt.Errorf("rectangle %d×%d at (%d, %d) is off the screen", w, ht, x, y)
	}
	for row := y; row < y+ht; row++ {
		if _, err := io.ReadFull(c.r, c.FB[(row*c.W+x)*4:(row*c.W+x+w)*4]); err != nil {
			return err
		}
	}
	return nil
}
