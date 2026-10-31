// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package vnctest is a fake RFB server (RFC 6143) on the loopback
// interface, for the tests of packages that run the native VNC backend,
// such as occamyd's server. It knows nothing of the backend: it speaks
// RFB 3.8 with the security types None, VNC Authentication and
// VeNCrypt's X.509 subtypes, sends Raw rectangles in the pixel format
// the client asks for, the Cursor, DesktopSize and ExtendedDesktopSize
// pseudo-encodings and cut text, and answers update requests and
// SetDesktopSize on its own. Every message the client sends is handed to
// the test, in order, by Conn.Next.
//
// The backend's own tests use a fake of their own, in package vnc,
// which reaches into the backend's state and scripts every byte.
package vnctest

import (
	"bufio"
	"context"
	"crypto/des" // #nosec G502 -- VNC Authentication is DES by definition (RFC 6143, section 7.2.2)
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math/bits"
	"net"
	"slices"
	"sync"
	"unicode/utf8"
)

// Security types and VeNCrypt subtypes.
const (
	SecNone     = 1
	SecVNCAuth  = 2
	SecVeNCrypt = 19

	SubX509None  = 260
	SubX509Vnc   = 261
	SubX509Plain = 262
)

// Encodings and pseudo-encodings the server sends, when the client asks
// for them.
const (
	EncRaw                 int32 = 0
	EncCursor              int32 = -239
	EncDesktopSize         int32 = -223
	EncExtendedDesktopSize int32 = -308
)

// Client message types.
const (
	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5
	msgClientCutText            = 6
	msgSetDesktopSize           = 251
)

// Server message types.
const (
	msgFramebufferUpdate = 0
	msgBell              = 2
	msgServerCutText     = 3
)

// ExtendedDesktopSize reasons.
const (
	reasonServer = 0
	reasonClient = 1
)

// maxSide bounds the screen the server takes from SetDesktopSize.
const maxSide = 8192

// Options configure a Server.
type Options struct {
	// Size is the screen's size, 64×48 if zero.
	Size image.Point
	// Name is the desktop's name.
	Name string
	// Password is asked for with VNC Authentication, or with VeNCrypt's
	// X509Vnc or X509Plain. Without one, the server takes None, or
	// X509None.
	Password string
	// Username, with Password and Certificate, offers X509Plain, which
	// checks both.
	Username string
	// Certificate, if set, makes the server offer VeNCrypt alone, with
	// X.509 subtypes over TLS with this certificate.
	Certificate *tls.Certificate
	// ExtendedDesktopSize makes the server announce ExtendedDesktopSize
	// with its first full update, and resize the screen when the client
	// asks with SetDesktopSize, up to 8192 a side.
	ExtendedDesktopSize bool
}

// Server is a fake RFB server.
type Server struct {
	opts  Options
	ln    net.Listener
	conns chan *Conn
	wg    sync.WaitGroup

	mu          sync.Mutex
	open        []net.Conn
	failures    int
	credentials int
	closed      bool
}

// NewServer starts a server on the loopback interface. Close stops it.
func NewServer(opts Options) (*Server, error) {
	if opts.Size == (image.Point{}) {
		opts.Size = image.Pt(64, 48)
	}
	if opts.Size.X <= 0 || opts.Size.Y <= 0 || opts.Size.X > maxSide || opts.Size.Y > maxSide {
		return nil, fmt.Errorf("vnctest: invalid size %v", opts.Size)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, ln: ln, conns: make(chan *Conn, 16)}
	s.wg.Go(s.acceptLoop)
	return s, nil
}

// Addr is the server's host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Failures is the number of failed authentications.
func (s *Server) Failures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// Credentials is the number of credentials that clients have sent, right
// or wrong: VNC Authentication's responses and X509Plain's usernames and
// passwords. A client that refuses the server's certificate sends none.
func (s *Server) Credentials() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentials
}

// sentCredentials counts credentials a client has sent.
func (s *Server) sentCredentials() {
	s.mu.Lock()
	s.credentials++
	s.mu.Unlock()
}

// Close stops the server: it closes the listener and every connection,
// and returns once every goroutine of the server has ended.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	s.closed = true
	for _, nc := range s.open {
		nc.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

// Accept returns the next client that has finished the handshake and
// set its pixel format and encodings.
func (s *Server) Accept(ctx context.Context) (*Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) acceptLoop() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.open = append(s.open, nc)
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(nc) })
	}
}

// serve runs the handshake, then hands the connection to Accept and
// reads the client's messages until the connection ends.
func (s *Server) serve(nc net.Conn) {
	defer nc.Close()
	c, err := s.handshake(nc)
	if err != nil {
		return
	}
	select {
	case s.conns <- c:
	default:
		return // nobody accepts so many
	}
	c.readLoop()
}

// handshake runs RFB 3.8's handshake, and reads the client's pixel
// format and encodings.
func (s *Server) handshake(nc net.Conn) (*Conn, error) {
	conn := nc
	r := bufio.NewReader(nc)
	if _, err := io.WriteString(nc, "RFB 003.008\n"); err != nil {
		return nil, err
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[:12]); err != nil {
		return nil, err
	}
	if string(b[:12]) != "RFB 003.008\n" {
		return nil, fmt.Errorf("vnctest: the client speaks %q", b[:12])
	}
	types := []byte{SecNone}
	switch {
	case s.opts.Certificate != nil:
		types = []byte{SecVeNCrypt}
	case s.opts.Password != "":
		types = []byte{SecVNCAuth}
	}
	if _, err := nc.Write(append([]byte{byte(len(types))}, types...)); err != nil { // #nosec G115 -- one type
		return nil, err
	}
	if _, err := io.ReadFull(r, b[:1]); err != nil {
		return nil, err
	}
	if !slices.Contains(types, b[0]) {
		return nil, fmt.Errorf("vnctest: the client chose security type %d", b[0])
	}
	ok := true
	var err error
	switch b[0] {
	case SecVNCAuth:
		ok, err = s.vncAuth(nc, r)
	case SecVeNCrypt:
		conn, r, ok, err = s.veNCrypt(nc, r)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		s.mu.Lock()
		s.failures++
		s.mu.Unlock()
		p := binary.BigEndian.AppendUint32(nil, 1)
		_, _ = conn.Write(appendString(p, "Authentication failed"))
		return nil, errors.New("vnctest: authentication failed")
	}
	if _, err := conn.Write(binary.BigEndian.AppendUint32(nil, 0)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, b[:1]); err != nil { // ClientInit
		return nil, err
	}
	c := &Conn{
		nc: conn, r: r, opts: s.opts,
		fb:     image.NewRGBA(image.Rectangle{Max: s.opts.Size}),
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	draw.Draw(c.fb, c.fb.Rect, image.Black, image.Point{}, draw.Src)
	init := binary.BigEndian.AppendUint16(nil, uint16(s.opts.Size.X)) // #nosec G115 -- at most maxSide
	init = binary.BigEndian.AppendUint16(init, uint16(s.opts.Size.Y)) // #nosec G115 -- at most maxSide
	init = append(init, serverFormat.bytes()...)
	if _, err := conn.Write(appendString(init, s.opts.Name)); err != nil {
		return nil, err
	}
	// The client sets its pixel format and encodings first.
	for range 2 {
		m, err := c.read()
		if err != nil {
			return nil, err
		}
		c.apply(m)
		c.queue(m)
	}
	if !c.pfSet || c.encodings == nil {
		return nil, errors.New("vnctest: the client did not set its pixel format and encodings first")
	}
	return c, nil
}

// vncAuth runs VNC Authentication with the server's password, and
// reports whether the client knew it.
func (s *Server) vncAuth(w io.Writer, r io.Reader) (bool, error) {
	var challenge, response [16]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return false, err
	}
	if _, err := w.Write(challenge[:]); err != nil {
		return false, err
	}
	if _, err := io.ReadFull(r, response[:]); err != nil {
		return false, err
	}
	s.sentCredentials()
	return response == authResponse(s.opts.Password, challenge), nil
}

// authResponse is the response to challenge with password: each half of
// the challenge encrypted with DES, with the password's first 8 bytes,
// each bit-reversed, as the key, as every implementation does.
func authResponse(password string, challenge [16]byte) [16]byte {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:]) // #nosec G405 -- see the import
	if err != nil {
		panic(err) // the key has the right size
	}
	var out [16]byte
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:])
	return out
}

// veNCrypt runs VeNCrypt 0.2 with X.509 subtypes: X509Plain with a
// username, X509Vnc with a password, X509None otherwise. It returns the
// TLS connection and whether the client's credentials were right.
func (s *Server) veNCrypt(nc net.Conn, r *bufio.Reader) (net.Conn, *bufio.Reader, bool, error) {
	if _, err := nc.Write([]byte{0, 2}); err != nil {
		return nil, nil, false, err
	}
	var b [8]byte
	if _, err := io.ReadFull(r, b[:2]); err != nil {
		return nil, nil, false, err
	}
	if b[0] != 0 || b[1] != 2 {
		return nil, nil, false, fmt.Errorf("vnctest: VeNCrypt %d.%d", b[0], b[1])
	}
	sub := uint32(SubX509None)
	switch {
	case s.opts.Username != "" && s.opts.Password != "":
		sub = SubX509Plain
	case s.opts.Password != "":
		sub = SubX509Vnc
	}
	if _, err := nc.Write(binary.BigEndian.AppendUint32([]byte{0, 1}, sub)); err != nil {
		return nil, nil, false, err
	}
	if _, err := io.ReadFull(r, b[:4]); err != nil {
		return nil, nil, false, err
	}
	if got := binary.BigEndian.Uint32(b[:4]); got != sub {
		return nil, nil, false, fmt.Errorf("vnctest: the client chose VeNCrypt subtype %d", got)
	}
	if _, err := nc.Write([]byte{1}); err != nil {
		return nil, nil, false, err
	}
	if r.Buffered() > 0 {
		return nil, nil, false, errors.New("vnctest: the client sent data before the TLS handshake")
	}
	tc := tls.Server(nc, &tls.Config{Certificates: []tls.Certificate{*s.opts.Certificate}, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		return nil, nil, false, err // the client refused the certificate
	}
	tr := bufio.NewReader(tc)
	switch sub {
	case SubX509Vnc:
		ok, err := s.vncAuth(tc, tr)
		return tc, tr, ok, err
	case SubX509Plain:
		if _, err := io.ReadFull(tr, b[:8]); err != nil {
			return nil, nil, false, err
		}
		nu, np := binary.BigEndian.Uint32(b[:4]), binary.BigEndian.Uint32(b[4:])
		if nu > 1024 || np > 1024 {
			return nil, nil, false, errors.New("vnctest: credentials too long")
		}
		creds := make([]byte, nu+np)
		if _, err := io.ReadFull(tr, creds); err != nil {
			return nil, nil, false, err
		}
		s.sentCredentials()
		return tc, tr, string(creds[:nu]) == s.opts.Username && string(creds[nu:]) == s.opts.Password, nil
	}
	return tc, tr, true, nil
}

// Messages from the client, as Conn.Next returns them.
type (
	// Message is one of the types below.
	Message interface{ message() }

	// SetPixelFormat sets the pixel format of the server's pixels.
	SetPixelFormat struct{ Format PixelFormat }
	// SetEncodings lists the encodings the client takes.
	SetEncodings struct{ Encodings []int32 }
	// UpdateRequest asks for an update of Rect: its changes if
	// Incremental, all of it otherwise.
	UpdateRequest struct {
		Incremental bool
		Rect        image.Rectangle
	}
	// KeyEvent presses or releases the key of Keysym.
	KeyEvent struct {
		Down   bool
		Keysym uint32
	}
	// PointerEvent moves the pointer to At with Buttons held, bit 0 the
	// left button.
	PointerEvent struct {
		Buttons uint8
		At      image.Point
	}
	// CutText is the client's clipboard, in UTF-8; it travels in
	// Latin-1.
	CutText struct{ Text string }
	// SetDesktopSize asks for a screen of Size.
	SetDesktopSize struct{ Size image.Point }
)

func (SetPixelFormat) message() {}
func (SetEncodings) message()   {}
func (UpdateRequest) message()  {}
func (KeyEvent) message()       {}
func (PointerEvent) message()   {}
func (CutText) message()        {}
func (SetDesktopSize) message() {}

// PixelFormat is RFB's PIXEL_FORMAT.
type PixelFormat struct {
	BitsPerPixel, Depth   uint8
	BigEndian, TrueColour bool
	RedMax, GreenMax      uint16
	BlueMax               uint16
	RedShift, GreenShift  uint8
	BlueShift             uint8
}

// serverFormat is the server's own pixel format, which the client must
// replace: blue, green, red and padding in memory, as x11vnc's.
var serverFormat = PixelFormat{
	BitsPerPixel: 32, Depth: 24, TrueColour: true,
	RedMax: 255, GreenMax: 255, BlueMax: 255, RedShift: 16, GreenShift: 8,
}

func parseFormat(b []byte) PixelFormat {
	return PixelFormat{
		BitsPerPixel: b[0], Depth: b[1], BigEndian: b[2] != 0, TrueColour: b[3] != 0,
		RedMax: binary.BigEndian.Uint16(b[4:]), GreenMax: binary.BigEndian.Uint16(b[6:]), BlueMax: binary.BigEndian.Uint16(b[8:]),
		RedShift: b[10], GreenShift: b[11], BlueShift: b[12],
	}
}

func (pf PixelFormat) bytes() []byte {
	b := []byte{pf.BitsPerPixel, pf.Depth, 0, 0, 0, 0, 0, 0, 0, 0, pf.RedShift, pf.GreenShift, pf.BlueShift, 0, 0, 0}
	if pf.BigEndian {
		b[2] = 1
	}
	if pf.TrueColour {
		b[3] = 1
	}
	binary.BigEndian.PutUint16(b[4:], pf.RedMax)
	binary.BigEndian.PutUint16(b[6:], pf.GreenMax)
	binary.BigEndian.PutUint16(b[8:], pf.BlueMax)
	return b
}

// appendPixel appends c in pf, with the padding bits set, as some
// servers leave them, so that a client that keeps them shows it.
func (pf PixelFormat) appendPixel(p []byte, c color.RGBA) []byte {
	v := uint32(c.R)*uint32(pf.RedMax)/255<<pf.RedShift |
		uint32(c.G)*uint32(pf.GreenMax)/255<<pf.GreenShift |
		uint32(c.B)*uint32(pf.BlueMax)/255<<pf.BlueShift
	used := uint32(pf.RedMax)<<pf.RedShift | uint32(pf.GreenMax)<<pf.GreenShift | uint32(pf.BlueMax)<<pf.BlueShift
	v |= 0x5a5a5a5a &^ used
	switch pf.BitsPerPixel {
	case 8:
		return append(p, byte(v)) // #nosec G115 -- an 8-bit pixel
	case 16:
		if pf.BigEndian {
			return binary.BigEndian.AppendUint16(p, uint16(v)) // #nosec G115 -- a 16-bit pixel
		}
		return binary.LittleEndian.AppendUint16(p, uint16(v)) // #nosec G115 -- a 16-bit pixel
	}
	if pf.BigEndian {
		return binary.BigEndian.AppendUint32(p, v)
	}
	return binary.LittleEndian.AppendUint32(p, v)
}

// Conn is the server's side of one client after the handshake. It
// answers the client's update requests with what changed on its screen
// since the last update, the whole screen for a request that is not
// incremental, and the client's SetDesktopSize as Options say.
type Conn struct {
	nc   net.Conn
	r    *bufio.Reader
	opts Options

	notify chan struct{} // a message was queued
	done   chan struct{} // closed when the client's messages end

	mu        sync.Mutex // guards what follows, and writes to nc
	pf        PixelFormat
	pfSet     bool
	encodings []int32
	msgs      []Message
	err       error // why the messages ended
	fb        *image.RGBA
	damage    image.Rectangle
	request   *UpdateRequest // the request not yet answered
	cursor    *cursor        // the cursor to send with the next update
	resized   bool           // the next update tells the client the screen's size
	reason    int            // why: reasonServer or reasonClient
	announced bool           // ExtendedDesktopSize has been sent
}

type cursor struct {
	img *image.RGBA
	hot image.Point
}

// Format returns the pixel format the client set.
func (c *Conn) Format() PixelFormat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pf
}

// Encodings returns the encodings the client set.
func (c *Conn) Encodings() []int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.encodings)
}

// Size returns the screen's size.
func (c *Conn) Size() image.Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fb.Rect.Size()
}

// Next returns the client's next message, or the reason there is none:
// io.EOF once the client has closed the connection, or ctx's error.
func (c *Conn) Next(ctx context.Context) (Message, error) {
	for {
		c.mu.Lock()
		if len(c.msgs) > 0 {
			m := c.msgs[0]
			c.msgs = c.msgs[1:]
			c.mu.Unlock()
			return m, nil
		}
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-c.notify:
		case <-c.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Draw draws img over the screen at img's bounds, and sends the area to
// the client with the next update.
func (c *Conn) Draw(img image.Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := img.Bounds().Intersect(c.fb.Rect)
	draw.Draw(c.fb, r, img, r.Min, draw.Src)
	c.damage = c.damage.Union(r)
	return c.flush()
}

// Screen returns a copy of the screen.
func (c *Conn) Screen() *image.RGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	img := image.NewRGBA(c.fb.Rect)
	copy(img.Pix, c.fb.Pix)
	return img
}

// Cursor sends img, with its hot spot at hot, as the pointer's shape
// with the next update, opaque where img's alpha is not zero.
func (c *Conn) Cursor(img *image.RGBA, hot image.Point) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = &cursor{img: img, hot: hot}
	return c.flush()
}

// Resize resizes the screen, as the server's own decision, keeping what
// fits, and tells the client with the next update.
func (c *Conn) Resize(size image.Point) error {
	if size.X <= 0 || size.Y <= 0 || size.X > maxSide || size.Y > maxSide {
		return fmt.Errorf("vnctest: invalid size %v", size)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resize(size, reasonServer)
	return c.flush()
}

// CutText sends text as the server's clipboard, in Latin-1, with '?'
// for what Latin-1 lacks.
func (c *Conn) CutText(text string) error {
	latin1 := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 0xff || r == utf8.RuneError {
			r = '?'
		}
		latin1 = append(latin1, byte(r))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(appendString([]byte{msgServerCutText, 0, 0, 0}, string(latin1)))
}

// Bell rings the client's bell.
func (c *Conn) Bell() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write([]byte{msgBell})
}

// Close closes the connection between messages, which ends an RFB
// session normally.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nc.Close()
}

// write writes p to the client. c.mu must be held.
func (c *Conn) write(p []byte) error {
	_, err := c.nc.Write(p)
	return err
}

// takes reports whether the client takes encoding e. c.mu must be held.
func (c *Conn) takes(e int32) bool { return slices.Contains(c.encodings, e) }

// resize resizes the screen for reason, keeping what fits. c.mu must be
// held.
func (c *Conn) resize(size image.Point, reason int) {
	fb := image.NewRGBA(image.Rectangle{Max: size})
	draw.Draw(fb, fb.Rect, image.Black, image.Point{}, draw.Src)
	draw.Draw(fb, fb.Rect, c.fb, image.Point{}, draw.Src)
	c.fb = fb
	c.damage = fb.Rect
	c.resized, c.reason = true, reason
}

// flush answers the pending update request if there is anything to
// send: a size, a cursor, or changes, or anything for a request that is
// not incremental. c.mu must be held.
func (c *Conn) flush() error {
	req := c.request
	if req == nil {
		return nil
	}
	announce := c.opts.ExtendedDesktopSize && !c.announced && !req.Incremental && c.takes(EncExtendedDesktopSize)
	size := c.resized && (c.takes(EncExtendedDesktopSize) && c.announced || c.takes(EncDesktopSize))
	area := c.damage
	if !req.Incremental {
		area = c.fb.Rect
	}
	area = area.Intersect(req.Rect).Intersect(c.fb.Rect)
	if size {
		area = c.fb.Rect // after a resize the client has nothing
	}
	sendCursor := c.cursor != nil && c.takes(EncCursor)
	if !announce && !size && !sendCursor && area.Empty() {
		return nil
	}
	var rects [][]byte
	switch {
	case size && c.takes(EncExtendedDesktopSize) && c.announced:
		rects = append(rects, c.extendedDesktopSize(c.reason))
	case size:
		rects = append(rects, rectHeader(c.fb.Rect, EncDesktopSize))
	case announce:
		rects = append(rects, c.extendedDesktopSize(reasonServer))
		c.announced = true
	}
	if sendCursor {
		rects = append(rects, c.encodeCursor())
		c.cursor = nil
	}
	if !area.Empty() {
		rects = append(rects, c.raw(area))
	}
	p := binary.BigEndian.AppendUint16([]byte{msgFramebufferUpdate, 0}, uint16(len(rects))) // #nosec G115 -- at most three
	for _, r := range rects {
		p = append(p, r...)
	}
	c.request = nil
	c.damage = image.Rectangle{}
	c.resized = false
	return c.write(p)
}

func rectHeader(r image.Rectangle, enc int32) []byte {
	p := make([]byte, 0, 12)
	for _, v := range []int{r.Min.X, r.Min.Y, r.Dx(), r.Dy()} {
		p = binary.BigEndian.AppendUint16(p, uint16(v)) // #nosec G115 -- within the screen
	}
	return binary.BigEndian.AppendUint32(p, uint32(enc)) // #nosec G115 -- the encoding's bits
}

// extendedDesktopSize encodes an ExtendedDesktopSize rectangle of the
// screen for reason, with status 0 and one screen covering it.
func (c *Conn) extendedDesktopSize(reason int) []byte {
	size := c.fb.Rect.Size()
	p := rectHeader(image.Rect(reason, 0, reason+size.X, size.Y), EncExtendedDesktopSize)
	p = append(p, 1, 0, 0, 0)
	p = binary.BigEndian.AppendUint32(p, 1) // the screen's id
	p = append(p, rectHeader(c.fb.Rect, 0)[:8]...)
	return binary.BigEndian.AppendUint32(p, 0) // its flags
}

// raw encodes r of the screen as a Raw rectangle in the client's format.
func (c *Conn) raw(r image.Rectangle) []byte {
	p := rectHeader(r, EncRaw)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p = c.pf.appendPixel(p, c.fb.RGBAAt(x, y))
		}
	}
	return p
}

// encodeCursor encodes the pending cursor as a Cursor pseudo-rectangle.
func (c *Conn) encodeCursor() []byte {
	img, b := c.cursor.img, c.cursor.img.Rect
	p := rectHeader(image.Rectangle{Min: c.cursor.hot, Max: c.cursor.hot.Add(b.Size())}, EncCursor)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			p = c.pf.appendPixel(p, img.RGBAAt(x, y))
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

// readLoop reads the client's messages, answers what the server answers
// itself, and queues each for Next, until the connection ends.
func (c *Conn) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
			err = io.EOF
		}
		c.err = err
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		var m Message
		if m, err = c.read(); err != nil {
			return
		}
		c.mu.Lock()
		c.queue(m)
		c.apply(m)
		err = c.flush()
		c.mu.Unlock()
		if err != nil {
			return
		}
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

// queue adds m to the messages for Next. c.mu must be held, but for the
// handshake's.
func (c *Conn) queue(m Message) { c.msgs = append(c.msgs, m) }

// apply does what m asks of the server. c.mu must be held, but for the
// handshake's.
func (c *Conn) apply(m Message) {
	switch m := m.(type) {
	case SetPixelFormat:
		c.pf, c.pfSet = m.Format, true
	case SetEncodings:
		c.encodings = m.Encodings
	case UpdateRequest:
		c.request = &m
	case SetDesktopSize:
		if !c.opts.ExtendedDesktopSize || m.Size.X <= 0 || m.Size.Y <= 0 || m.Size.X > maxSide || m.Size.Y > maxSide {
			return
		}
		c.resize(m.Size, reasonClient)
		// The answer goes out at once, whether or not a request is
		// pending, as the rfbproto asks of a server.
		if c.request == nil {
			c.request = &UpdateRequest{Incremental: true, Rect: c.fb.Rect}
		}
	}
}

// read reads one client message.
func (c *Conn) read() (Message, error) {
	typ, err := c.r.ReadByte()
	if err != nil {
		return nil, err
	}
	var b [20]byte
	switch typ {
	case msgSetPixelFormat:
		if _, err := io.ReadFull(c.r, b[:19]); err != nil {
			return nil, err
		}
		return SetPixelFormat{parseFormat(b[3:19])}, nil
	case msgSetEncodings:
		if _, err := io.ReadFull(c.r, b[:3]); err != nil {
			return nil, err
		}
		e := make([]int32, binary.BigEndian.Uint16(b[1:]))
		if err := binary.Read(c.r, binary.BigEndian, e); err != nil {
			return nil, err
		}
		return SetEncodings{e}, nil
	case msgFramebufferUpdateRequest:
		if _, err := io.ReadFull(c.r, b[:9]); err != nil {
			return nil, err
		}
		x, y := int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:]))
		w, h := int(binary.BigEndian.Uint16(b[5:])), int(binary.BigEndian.Uint16(b[7:]))
		return UpdateRequest{Incremental: b[0] != 0, Rect: image.Rect(x, y, x+w, y+h)}, nil
	case msgKeyEvent:
		if _, err := io.ReadFull(c.r, b[:7]); err != nil {
			return nil, err
		}
		return KeyEvent{Down: b[0] != 0, Keysym: binary.BigEndian.Uint32(b[3:])}, nil
	case msgPointerEvent:
		if _, err := io.ReadFull(c.r, b[:5]); err != nil {
			return nil, err
		}
		return PointerEvent{Buttons: b[0], At: image.Pt(int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:])))}, nil
	case msgClientCutText:
		if _, err := io.ReadFull(c.r, b[:7]); err != nil {
			return nil, err
		}
		n := int32(binary.BigEndian.Uint32(b[3:])) // #nosec G115 -- RFB's S32
		if n < 0 || n > 4<<20 {
			return nil, fmt.Errorf("vnctest: cut text of %d bytes", n)
		}
		latin1 := make([]byte, n)
		if _, err := io.ReadFull(c.r, latin1); err != nil {
			return nil, err
		}
		text := make([]rune, len(latin1))
		for i, ch := range latin1 {
			text[i] = rune(ch)
		}
		return CutText{string(text)}, nil
	case msgSetDesktopSize:
		if _, err := io.ReadFull(c.r, b[:7]); err != nil {
			return nil, err
		}
		size := image.Pt(int(binary.BigEndian.Uint16(b[1:])), int(binary.BigEndian.Uint16(b[3:])))
		if _, err := io.CopyN(io.Discard, c.r, 16*int64(b[5])); err != nil {
			return nil, err
		}
		return SetDesktopSize{size}, nil
	}
	return nil, fmt.Errorf("vnctest: client message type %d", typ)
}

// appendString appends s with its U32 length.
func appendString(p []byte, s string) []byte {
	p = binary.BigEndian.AppendUint32(p, uint32(len(s))) // #nosec G115 -- short
	return append(p, s...)
}
