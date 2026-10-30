// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// The fake RFB server that the tests run against. It speaks the server's
// side of the handshake, encodes pixels in the pixel format the client
// asked for, as a real server translates them, and parses the client's
// messages.

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

// serverPixelFormat is the fake's own pixel format, as x11vnc's on a
// 24-bit screen: blue, green, red and padding in memory. The client must
// replace it.
var serverPixelFormat = pixFormat{
	bpp: 32, depth: 24, trueColour: true,
	rmax: 255, gmax: 255, bmax: 255, rshift: 16, gshift: 8, bshift: 0,
}

// pixFormat is a parsed PIXEL_FORMAT.
type pixFormat struct {
	bpp, depth             uint8
	bigEndian, trueColour  bool
	rmax, gmax, bmax       uint16
	rshift, gshift, bshift uint8
}

func parsePixFormat(b []byte) pixFormat {
	return pixFormat{
		bpp: b[0], depth: b[1], bigEndian: b[2] != 0, trueColour: b[3] != 0,
		rmax: binary.BigEndian.Uint16(b[4:]), gmax: binary.BigEndian.Uint16(b[6:]), bmax: binary.BigEndian.Uint16(b[8:]),
		rshift: b[10], gshift: b[11], bshift: b[12],
	}
}

func (pf pixFormat) bytes() []byte {
	b := []byte{pf.bpp, pf.depth, 0, 0, 0, 0, 0, 0, 0, 0, pf.rshift, pf.gshift, pf.bshift, 0, 0, 0}
	if pf.bigEndian {
		b[2] = 1
	}
	if pf.trueColour {
		b[3] = 1
	}
	binary.BigEndian.PutUint16(b[4:], pf.rmax)
	binary.BigEndian.PutUint16(b[6:], pf.gmax)
	binary.BigEndian.PutUint16(b[8:], pf.bmax)
	return b
}

// appendPixel appends c in pf, with the padding bits set, as some
// servers leave them, so that a client that keeps them shows it.
func (pf pixFormat) appendPixel(p []byte, c color.RGBA) []byte {
	v := uint32(c.R)*uint32(pf.rmax)/255<<pf.rshift |
		uint32(c.G)*uint32(pf.gmax)/255<<pf.gshift |
		uint32(c.B)*uint32(pf.bmax)/255<<pf.bshift
	used := uint32(pf.rmax)<<pf.rshift | uint32(pf.gmax)<<pf.gshift | uint32(pf.bmax)<<pf.bshift
	v |= 0x5a5a5a5a &^ used
	switch pf.bpp {
	case 8:
		return append(p, byte(v))
	case 16:
		if pf.bigEndian {
			return binary.BigEndian.AppendUint16(p, uint16(v))
		}
		return binary.LittleEndian.AppendUint16(p, uint16(v))
	}
	if pf.bigEndian {
		return binary.BigEndian.AppendUint32(p, v)
	}
	return binary.LittleEndian.AppendUint32(p, v)
}

// The client's messages, as the fake parses them.
type (
	pixelFormatMsg struct{ pf pixFormat }
	encodingsMsg   struct{ encodings []int32 }
	requestMsg     struct {
		incremental bool
		r           image.Rectangle
	}
	keyMsg struct {
		down   bool
		keysym uint32
	}
	pointerMsg struct {
		buttons uint8
		at      image.Point
	}
	cutTextMsg struct{ text string } // Latin-1, as sent
)

// serverOptions configure a fake server.
type serverOptions struct {
	version  string      // the server's version, "003.008" if empty
	types    []byte      // offered security types, [None] if nil; 3.3 uses the first
	refuse   string      // a reason to refuse every connection with
	password string      // for VNC Authentication
	size     image.Point // the screen, 64×48 if zero
	name     string
}

// fakeServer is an RFB server on the loopback interface.
type fakeServer struct {
	t     *testing.T
	addr  string
	opts  serverOptions
	conns chan *serverConn

	mu       sync.Mutex
	replies  []string // the clients' versions
	failures int      // failed authentications
}

// newServer starts a fake server that serves until the test ends.
func newServer(t *testing.T, opts serverOptions) *fakeServer {
	t.Helper()
	if opts.version == "" {
		opts.version = "003.008"
	}
	if opts.types == nil {
		opts.types = []byte{secNone}
	}
	if opts.size == (image.Point{}) {
		opts.size = image.Pt(64, 48)
	}
	s := &fakeServer{t: t, opts: opts, conns: make(chan *serverConn, 8)}
	s.addr = listen(t, s.serve)
	return s
}

// listen accepts connections on the loopback interface and serves each
// with serve in its own goroutine, until the test ends. Every connection
// is closed, and every goroutine has returned, when its cleanup has run.
func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns []net.Conn
	)
	wg.Go(func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			wg.Go(func() { serve(nc) })
		}
	})
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, nc := range conns {
			nc.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// Replies returns the versions the clients replied with.
func (s *fakeServer) Replies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.replies)
}

// Failures is the number of failed authentications.
func (s *fakeServer) Failures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// accept waits for a client to finish the handshake.
func (s *fakeServer) accept(t *testing.T) *serverConn {
	t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(timeout):
		t.Fatal("no client finished the handshake")
	}
	return nil
}

// serve runs the handshake with a client, and hands the connection to
// accept once the client has set its pixel format and encodings.
func (s *fakeServer) serve(nc net.Conn) {
	r := bufio.NewReader(nc)
	fail := func(format string, args ...any) {
		s.t.Errorf("fake server: "+format, args...)
		nc.Close()
	}
	if _, err := fmt.Fprintf(nc, "RFB %s\n", s.opts.version); err != nil {
		return
	}
	var b [24]byte
	if _, err := io.ReadFull(r, b[:12]); err != nil {
		return
	}
	reply := string(b[:12])
	s.mu.Lock()
	s.replies = append(s.replies, reply)
	s.mu.Unlock()
	v, err := parseVersion(b[:12])
	if err != nil {
		fail("client version %q: %v", reply, err)
		return
	}
	if s.opts.refuse != "" {
		if v == 3 {
			nc.Write(binary.BigEndian.AppendUint32(nil, secInvalid))
		} else {
			nc.Write([]byte{0})
		}
		nc.Write(appendString(nil, s.opts.refuse))
		nc.Close()
		return
	}
	var t byte
	if v == 3 {
		t = s.opts.types[0]
		nc.Write(binary.BigEndian.AppendUint32(nil, uint32(t)))
	} else {
		nc.Write(append([]byte{byte(len(s.opts.types))}, s.opts.types...))
		if _, err := io.ReadFull(r, b[:1]); err != nil {
			return
		}
		if t = b[0]; !slices.Contains(s.opts.types, t) {
			fail("client chose type %d of %v", t, s.opts.types)
			return
		}
	}
	if t == secVNCAuth {
		var challenge [16]byte
		rand.Read(challenge[:])
		nc.Write(challenge[:])
		if _, err := io.ReadFull(r, b[:16]); err != nil {
			return
		}
		if want := vncAuthResponse(s.opts.password, challenge); string(b[:16]) != string(want[:]) {
			s.mu.Lock()
			s.failures++
			s.mu.Unlock()
			nc.Write(binary.BigEndian.AppendUint32(nil, 1))
			if v == 8 {
				nc.Write(appendString(nil, "Authentication failed"))
			}
			nc.Close()
			return
		}
	}
	if t == secVNCAuth || v == 8 {
		nc.Write(binary.BigEndian.AppendUint32(nil, 0))
	}
	if _, err := io.ReadFull(r, b[:1]); err != nil {
		return
	}
	shared := b[0] != 0
	init := binary.BigEndian.AppendUint16(nil, uint16(s.opts.size.X))
	init = binary.BigEndian.AppendUint16(init, uint16(s.opts.size.Y))
	init = append(init, serverPixelFormat.bytes()...)
	nc.Write(appendString(init, s.opts.name))

	// The client sets its pixel format and encodings first.
	if _, err := io.ReadFull(r, b[:20]); err != nil {
		return
	}
	if b[0] != msgSetPixelFormat {
		fail("first message type %d, want SetPixelFormat", b[0])
		return
	}
	pf := parsePixFormat(b[4:20])
	if _, err := io.ReadFull(r, b[:4]); err != nil {
		return
	}
	if b[0] != msgSetEncodings {
		fail("second message type %d, want SetEncodings", b[0])
		return
	}
	encs := make([]int32, binary.BigEndian.Uint16(b[2:]))
	if err := binary.Read(r, binary.BigEndian, encs); err != nil {
		return
	}
	c := &serverConn{
		nc: nc, r: r, version: v, security: t, shared: shared,
		pf: pf, encodings: encs, size: s.opts.size,
		msgs: make(chan any, 1024), stopped: make(chan struct{}),
	}
	go c.readLoop(s.t)
	s.conns <- c
	<-c.stopped
}

// appendString appends a string with its U32 length.
func appendString(p []byte, s string) []byte {
	p = binary.BigEndian.AppendUint32(p, uint32(len(s)))
	return append(p, s...)
}

// serverConn is the server's side of a connection after the handshake.
type serverConn struct {
	nc        net.Conn
	r         *bufio.Reader
	version   int
	security  byte
	shared    bool
	pf        pixFormat // as the client set it
	encodings []int32   // as the client set them
	size      image.Point
	msgs      chan any      // the client's messages
	stopped   chan struct{} // closed when readLoop returns
}

// readLoop parses the client's messages until the connection ends.
func (c *serverConn) readLoop(t *testing.T) {
	defer close(c.stopped)
	defer close(c.msgs)
	var b [20]byte
	for {
		typ, err := c.r.ReadByte()
		if err != nil {
			return
		}
		var m any
		switch typ {
		case msgSetPixelFormat:
			if _, err := io.ReadFull(c.r, b[:19]); err != nil {
				return
			}
			m = pixelFormatMsg{parsePixFormat(b[3:19])}
		case msgSetEncodings:
			if _, err := io.ReadFull(c.r, b[:3]); err != nil {
				return
			}
			e := make([]int32, binary.BigEndian.Uint16(b[1:]))
			if err := binary.Read(c.r, binary.BigEndian, e); err != nil {
				return
			}
			m = encodingsMsg{e}
		case msgFramebufferUpdateRequest:
			if _, err := io.ReadFull(c.r, b[:9]); err != nil {
				return
			}
			x, y := int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
			w, h := int(binary.BigEndian.Uint16(b[5:])), int(binary.BigEndian.Uint16(b[7:]))
			m = requestMsg{incremental: b[0] != 0, r: image.Rect(x, y, x+w, y+h)}
		case msgKeyEvent:
			if _, err := io.ReadFull(c.r, b[:7]); err != nil {
				return
			}
			m = keyMsg{down: b[0] != 0, keysym: binary.BigEndian.Uint32(b[3:])}
		case msgPointerEvent:
			if _, err := io.ReadFull(c.r, b[:5]); err != nil {
				return
			}
			m = pointerMsg{buttons: b[0], at: image.Pt(int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:])))}
		case msgClientCutText:
			if _, err := io.ReadFull(c.r, b[:7]); err != nil {
				return
			}
			text := make([]byte, binary.BigEndian.Uint32(b[3:]))
			if _, err := io.ReadFull(c.r, text); err != nil {
				return
			}
			m = cutTextMsg{string(text)}
		default:
			t.Errorf("fake server: unknown client message type %d", typ)
			c.nc.Close()
			return
		}
		select {
		case c.msgs <- m:
		default:
			t.Errorf("fake server: more than %d messages not taken", cap(c.msgs))
			c.nc.Close()
			return
		}
	}
}

// send writes p to the client.
func (c *serverConn) send(t *testing.T, p ...[]byte) {
	t.Helper()
	if _, err := c.nc.Write(slices.Concat(p...)); err != nil {
		t.Fatalf("fake server: %v", err)
	}
}

// next returns the client's next message.
func (c *serverConn) next(t *testing.T) any {
	t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			t.Fatal("the client closed the connection")
		}
		return m
	case <-time.After(timeout):
		t.Fatal("no message from the client")
	}
	return nil
}

// expect returns the client's next message, which must be a T.
func expect[T any](t *testing.T, c *serverConn) T {
	t.Helper()
	m := c.next(t)
	v, ok := m.(T)
	if !ok {
		var want T
		t.Fatalf("client sent %T %+v, want %T", m, m, want)
	}
	return v
}

// quiet fails the test if the client sends a message within d.
func (c *serverConn) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m, ok := <-c.msgs:
		if ok {
			t.Fatalf("client sent %T %+v, want nothing", m, m)
		}
	case <-time.After(d):
	}
}

// The server's messages, as the fake encodes them.

// encUpdate encodes a FramebufferUpdate of the given rectangles, each
// already encoded.
func putUpdate(rects ...[]byte) []byte {
	p := []byte{msgFramebufferUpdate, 0}
	p = binary.BigEndian.AppendUint16(p, uint16(len(rects)))
	return slices.Concat(append([][]byte{p}, rects...)...)
}

func rectHeader(r image.Rectangle, enc int32) []byte {
	p := binary.BigEndian.AppendUint16(nil, uint16(r.Min.X))
	p = binary.BigEndian.AppendUint16(p, uint16(r.Min.Y))
	p = binary.BigEndian.AppendUint16(p, uint16(r.Dx()))
	p = binary.BigEndian.AppendUint16(p, uint16(r.Dy()))
	return binary.BigEndian.AppendUint32(p, uint32(enc))
}

// encRaw encodes img's pixels in r as a Raw rectangle in pf.
func putRaw(pf pixFormat, img *image.RGBA, r image.Rectangle) []byte {
	p := rectHeader(r, encRaw)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p = pf.appendPixel(p, img.RGBAAt(x, y))
		}
	}
	return p
}

func putCopy(dst image.Rectangle, src image.Point) []byte {
	p := rectHeader(dst, encCopyRect)
	p = binary.BigEndian.AppendUint16(p, uint16(src.X))
	return binary.BigEndian.AppendUint16(p, uint16(src.Y))
}

func putDesktopSize(w, h int) []byte {
	return rectHeader(image.Rect(0, 0, w, h), encDesktopSize)
}

// encCursor encodes img as a Cursor pseudo-rectangle in pf, opaque where
// img's alpha is not zero.
func putCursor(pf pixFormat, img *image.RGBA, hot image.Point) []byte {
	b := img.Rect
	p := rectHeader(image.Rectangle{Min: hot, Max: hot.Add(b.Size())}, encCursor)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p = pf.appendPixel(p, img.RGBAAt(x, y))
		}
	}
	stride := (b.Dx() + 7) / 8
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := make([]byte, stride)
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.RGBAAt(x, y).A != 0 {
				row[(x-b.Min.X)/8] |= 0x80 >> ((x - b.Min.X) % 8)
			}
		}
		p = append(p, row...)
	}
	return p
}

func putBell() []byte { return []byte{msgBell} }

func putCutText(latin1 string) []byte {
	return appendString([]byte{msgServerCutText, 0, 0, 0}, latin1)
}

func putColourMap(n int) []byte {
	p := []byte{msgSetColourMapEntries, 0, 0, 0}
	p = binary.BigEndian.AppendUint16(p, uint16(n))
	return append(p, make([]byte, 6*n)...)
}

// pattern returns an opaque image whose every pixel differs from its
// neighbours, offset by seed.
func pattern(w, h, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*7 + seed), G: uint8(y*13 + seed), B: uint8((x + y) * 3), A: 0xff})
		}
	}
	return img
}
