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
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"math/big"
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

	continuousMsg struct {
		enable bool
		r      image.Rectangle
	}
	fenceMsg struct {
		flags   uint32
		payload string
	}
	setSizeMsg struct {
		size    image.Point
		screens []screenMsg
	}
	screenMsg struct {
		id    uint32
		r     image.Rectangle
		flags uint32
	}
	qemuKeyMsg struct {
		down            bool
		keysym, keycode uint32
	}
	clipMsg struct { // Extended Clipboard
		flags uint32
		sizes []uint32 // of caps
		text  string   // of provide, as sent: CR LF line ends and a NUL
	}
)

// serverOptions configure a fake server.
type serverOptions struct {
	version  string      // the server's version, "003.008" if empty
	types    []byte      // offered security types, [None] if nil; 3.3 uses the first
	refuse   string      // a reason to refuse every connection with
	password string      // for VNC Authentication, VeNCrypt's Plain and Apple Remote Desktop
	size     image.Point // the screen, 64×48 if zero
	name     string

	// VeNCrypt, if types offers it.
	vencrypt  []uint32        // the subtypes offered
	vcVersion [2]byte         // VeNCrypt's version, 0.2 if zero
	cert      tls.Certificate // for the TLS and X.509 subtypes
	tlsMax    uint16          // the newest TLS version, and TLS 1.0 the oldest, if not zero
	user      string          // for Plain and Apple Remote Desktop
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
	subtypes []uint32 // the VeNCrypt subtypes the clients chose
	plains   []string // Plain's credentials, as "user:password"
	ards     []string // Apple Remote Desktop's credentials, as "user:password"
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
	authOK := true
	inner := uint32(t) // the type that authenticates, VeNCrypt's subtype within it
	switch t {
	case secVNCAuth:
		var ok bool
		if authOK, ok = s.vncAuth(nc, r); !ok {
			return
		}
	case secVeNCrypt:
		var ok bool
		if nc, r, inner, authOK, ok = s.veNCrypt(nc, r); !ok {
			return
		}
	case secARD:
		var ok bool
		if authOK, ok = s.ard(nc, r); !ok {
			return
		}
	}
	if !authOK {
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
	// As TigerVNC's SConnection::approveConnection: no SecurityResult
	// for None before 3.8, VeNCrypt's None subtype included.
	if inner != secNone || v == 8 {
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

// vncAuth runs the server's side of VNC Authentication. It returns
// whether the password was right, and false for ok if the connection
// failed.
func (s *fakeServer) vncAuth(nc net.Conn, r *bufio.Reader) (right, ok bool) {
	var challenge, response [16]byte
	rand.Read(challenge[:])
	nc.Write(challenge[:])
	if _, err := io.ReadFull(r, response[:]); err != nil {
		return false, false
	}
	want := vncAuthResponse(s.opts.password, challenge)
	return response == want, true
}

// veNCrypt runs the server's side of VeNCrypt: the version, the
// subtype, TLS for the TLS and X.509 subtypes, and the subtype's
// authentication. It returns the connection to go on with, the
// subtype, whether the credentials were right, and false for ok if the
// connection failed.
func (s *fakeServer) veNCrypt(nc net.Conn, r *bufio.Reader) (_ net.Conn, _ *bufio.Reader, _ uint32, right, ok bool) {
	version := s.opts.vcVersion
	if version == ([2]byte{}) {
		version = [2]byte{0, 2}
	}
	nc.Write(version[:])
	var b [8]byte
	if _, err := io.ReadFull(r, b[:2]); err != nil {
		return nil, nil, 0, false, false
	}
	if b[0] != 0 || b[1] != 2 {
		return nil, nil, 0, false, false // the client gave up
	}
	p := append([]byte{0, byte(len(s.opts.vencrypt))}, make([]byte, 0, 4*len(s.opts.vencrypt))...)
	for _, st := range s.opts.vencrypt {
		p = binary.BigEndian.AppendUint32(p, st)
	}
	nc.Write(p)
	if _, err := io.ReadFull(r, b[:4]); err != nil {
		return nil, nil, 0, false, false
	}
	st := binary.BigEndian.Uint32(b[:4])
	s.mu.Lock()
	s.subtypes = append(s.subtypes, st)
	s.mu.Unlock()
	if !slices.Contains(s.opts.vencrypt, st) {
		s.t.Errorf("fake server: client chose VeNCrypt subtype %d of %v", st, s.opts.vencrypt)
		return nil, nil, 0, false, false
	}
	if st >= 257 && st <= 262 {
		nc.Write([]byte{1})
		cfg := &tls.Config{Certificates: []tls.Certificate{s.opts.cert}, MinVersion: tls.VersionTLS12}
		if s.opts.tlsMax != 0 {
			cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS10, s.opts.tlsMax
		}
		tc := tls.Server(nc, cfg)
		if err := tc.Handshake(); err != nil {
			return nil, nil, 0, false, false
		}
		nc, r = tc, bufio.NewReader(tc)
	}
	switch st {
	case 2, 258, 261: // VNC Authentication
		right, ok = s.vncAuth(nc, r)
		return nc, r, st, right, ok
	case 256, 259, 262: // Plain
		if _, err := io.ReadFull(r, b[:8]); err != nil {
			return nil, nil, 0, false, false
		}
		creds := make([]byte, int(binary.BigEndian.Uint32(b[0:]))+int(binary.BigEndian.Uint32(b[4:])))
		if _, err := io.ReadFull(r, creds); err != nil {
			return nil, nil, 0, false, false
		}
		n := binary.BigEndian.Uint32(b[0:])
		user, password := string(creds[:n]), string(creds[n:])
		s.mu.Lock()
		s.plains = append(s.plains, user+":"+password)
		s.mu.Unlock()
		return nc, r, st, user == s.opts.user && password == s.opts.password, true
	}
	return nc, r, st, true, true // None
}

// Subtypes returns the VeNCrypt subtypes the clients chose.
func (s *fakeServer) Subtypes() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subtypes)
}

// Plains returns Plain's credentials, as "user:password".
func (s *fakeServer) Plains() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.plains)
}

// ard runs the server's side of Apple Remote Desktop authentication in
// RFC 2409's group 2 with the generator 2, as macOS sent it in 2017,
// and decrypts the credentials with code of its own. It returns whether
// they were right, and false for ok if the connection failed.
func (s *fakeServer) ard(nc net.Conn, r *bufio.Reader) (right, ok bool) {
	p := rfc2409Group2
	n := (p.BitLen() + 7) / 8
	x, _ := rand.Int(rand.Reader, new(big.Int).Sub(p, big.NewInt(3)))
	x.Add(x, big.NewInt(2))
	y := new(big.Int).Exp(big.NewInt(2), x, p)
	msg := binary.BigEndian.AppendUint16(nil, 2)
	msg = binary.BigEndian.AppendUint16(msg, uint16(n))
	msg = append(msg, p.FillBytes(make([]byte, n))...)
	nc.Write(append(msg, y.FillBytes(make([]byte, n))...))

	resp := make([]byte, 128+n)
	if _, err := io.ReadFull(r, resp); err != nil {
		return false, false
	}
	pub := new(big.Int).SetBytes(resp[128:])
	if pub.Cmp(big.NewInt(2)) < 0 || pub.Cmp(new(big.Int).Sub(p, big.NewInt(2))) > 0 {
		s.t.Errorf("fake server: the client's Apple Remote Desktop public key is not within [2, p-2]")
		return false, false
	}
	key := md5.Sum(new(big.Int).Exp(pub, x, p).FillBytes(make([]byte, n)))
	block, _ := aes.NewCipher(key[:])
	plain := make([]byte, 128)
	for i := 0; i < len(plain); i += block.BlockSize() {
		block.Decrypt(plain[i:], resp[i:])
	}
	u, _, uok := bytes.Cut(plain[:64], []byte{0})
	pw, _, pok := bytes.Cut(plain[64:], []byte{0})
	if !uok || !pok {
		s.t.Errorf("fake server: an Apple Remote Desktop field without its NUL: %q", plain)
		return false, false
	}
	s.mu.Lock()
	s.ards = append(s.ards, string(u)+":"+string(pw))
	s.mu.Unlock()
	return string(u) == s.opts.user && string(pw) == s.opts.password, true
}

// ARDs returns Apple Remote Desktop's credentials, as "user:password".
func (s *fakeServer) ARDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ards)
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
			n := int32(binary.BigEndian.Uint32(b[3:]))
			if n < 0 {
				var err error
				if m, err = readClipMsg(c.r, int(-n)); err != nil {
					t.Errorf("fake server: %v", err)
					c.nc.Close()
					return
				}
				break
			}
			text := make([]byte, n)
			if _, err := io.ReadFull(c.r, text); err != nil {
				return
			}
			m = cutTextMsg{string(text)}
		case msgEnableContinuousUpdates:
			if _, err := io.ReadFull(c.r, b[:9]); err != nil {
				return
			}
			x, y := int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
			w, h := int(binary.BigEndian.Uint16(b[5:])), int(binary.BigEndian.Uint16(b[7:]))
			m = continuousMsg{enable: b[0] != 0, r: image.Rect(x, y, x+w, y+h)}
		case msgClientFence:
			if _, err := io.ReadFull(c.r, b[:8]); err != nil {
				return
			}
			payload := make([]byte, b[7])
			if _, err := io.ReadFull(c.r, payload); err != nil {
				return
			}
			m = fenceMsg{flags: binary.BigEndian.Uint32(b[3:]), payload: string(payload)}
		case msgSetDesktopSize:
			if _, err := io.ReadFull(c.r, b[:7]); err != nil {
				return
			}
			msg := setSizeMsg{size: image.Pt(int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:])))}
			for range b[5] {
				var s [16]byte
				if _, err := io.ReadFull(c.r, s[:]); err != nil {
					return
				}
				x, y := int(binary.BigEndian.Uint16(s[4:])), int(binary.BigEndian.Uint16(s[6:]))
				w, h := int(binary.BigEndian.Uint16(s[8:])), int(binary.BigEndian.Uint16(s[10:]))
				msg.screens = append(msg.screens, screenMsg{
					id: binary.BigEndian.Uint32(s[0:]), r: image.Rect(x, y, x+w, y+h), flags: binary.BigEndian.Uint32(s[12:]),
				})
			}
			m = msg
		case msgQEMU:
			if _, err := io.ReadFull(c.r, b[:11]); err != nil {
				return
			}
			if b[0] != qemuExtKeyEvent {
				t.Errorf("fake server: QEMU submessage %d", b[0])
				c.nc.Close()
				return
			}
			m = qemuKeyMsg{down: binary.BigEndian.Uint16(b[1:]) != 0, keysym: binary.BigEndian.Uint32(b[3:]), keycode: binary.BigEndian.Uint32(b[7:])}
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

// The extensions' server messages and pseudo-rectangles.

func putFence(flags uint32, payload string) []byte {
	p := binary.BigEndian.AppendUint32([]byte{msgServerFence, 0, 0, 0}, flags)
	return append(append(p, byte(len(payload))), payload...)
}

func putEndOfContinuous() []byte { return []byte{msgEndOfContinuousUpdates} }

func putLastRect() []byte { return rectHeader(image.Rectangle{}, encLastRect) }

func putQEMUKeys() []byte { return rectHeader(image.Rect(0, 0, 64, 48), encQEMUExtKey) }

// putExtDesktopSize encodes an ExtendedDesktopSize rectangle of a screen
// of w×h with the given layout.
func putExtDesktopSize(reason, status, w, h int, screens ...screenMsg) []byte {
	p := rectHeader(image.Rect(reason, status, reason+w, status+h), encExtendedDesktopSize)
	p = append(p, byte(len(screens)), 0, 0, 0)
	for _, s := range screens {
		p = binary.BigEndian.AppendUint32(p, s.id)
		for _, v := range []int{s.r.Min.X, s.r.Min.Y, s.r.Dx(), s.r.Dy()} {
			p = binary.BigEndian.AppendUint16(p, uint16(v))
		}
		p = binary.BigEndian.AppendUint32(p, s.flags)
	}
	return p
}

// putClip encodes a ServerCutText of Extended Clipboard.
func putClip(flags uint32, payload []byte) []byte {
	p := []byte{msgServerCutText, 0, 0, 0}
	p = binary.BigEndian.AppendUint32(p, uint32(-int32(4+len(payload))))
	p = binary.BigEndian.AppendUint32(p, flags)
	return append(p, payload...)
}

// putClipCaps encodes Extended Clipboard caps with a size per format.
func putClipCaps(flags uint32, sizes ...uint32) []byte {
	var p []byte
	for _, s := range sizes {
		p = binary.BigEndian.AppendUint32(p, s)
	}
	return putClip(clipCaps|flags, p)
}

// putClipProvide encodes a provide action of text as it is, which
// should end with a NUL.
func putClipProvide(text string) []byte {
	return putClip(clipProvide|clipText, zlibOf(binary.BigEndian.AppendUint32(nil, uint32(len(text))), []byte(text)))
}

func zlibOf(parts ...[]byte) []byte {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	for _, p := range parts {
		zw.Write(p)
	}
	zw.Close()
	return b.Bytes()
}

// readClipMsg reads an Extended Clipboard message of n bytes after its
// length.
func readClipMsg(r io.Reader, n int) (clipMsg, error) {
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return clipMsg{}, err
	}
	if n < 4 {
		return clipMsg{}, fmt.Errorf("extended clipboard message of %d bytes", n)
	}
	m := clipMsg{flags: binary.BigEndian.Uint32(body)}
	body = body[4:]
	switch {
	case m.flags&clipCaps != 0:
		for len(body) >= 4 {
			m.sizes = append(m.sizes, binary.BigEndian.Uint32(body))
			body = body[4:]
		}
	case m.flags&clipProvide != 0:
		zr, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return m, err
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			return m, err
		}
		if len(data) < 4 || int(binary.BigEndian.Uint32(data)) != len(data)-4 {
			return m, fmt.Errorf("provide of %d bytes", len(data))
		}
		m.text = string(data[4:])
	}
	return m, nil
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
