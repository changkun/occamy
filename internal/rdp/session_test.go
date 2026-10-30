// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of what internal/backend/rdp relies on: batched input, the
// pace of pointer updates, and how a session ends. They run the client
// on frames that rdptest builds.

// runFrames runs c, in the active state, on frames as a server sends
// them, until they end.
func runFrames(c *Client, out Output, frames ...[]byte) error {
	conn := readConn{bytes.NewReader(slices.Concat(frames...))}
	c.conn, c.t = conn, newTransport(conn, nil)
	c.ioChannel = rdptest.IOChannel
	return c.Run(context.Background(), out)
}

// countConn records writes.
type countConn struct {
	net.Conn
	writes int
	buf    bytes.Buffer
}

func (c *countConn) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

// Many events go out in PDUs of at most 255 events, written at once.
func TestSendInputBatches(t *testing.T) {
	keys := make([]InputEvent, 600)
	for i := range keys {
		keys[i] = KeyEvent(0x1e, i%2 == 0)
	}
	for _, fastPath := range []bool{true, false} {
		conn := &countConn{}
		c := &Client{width: 1024, height: 768, t: &transport{conn: conn}, ioChannel: 1003}
		c.fastPathInput.Store(fastPath)
		if err := c.SendInput(keys...); err != nil {
			t.Fatal(err)
		}
		var counts []int
		for b := conn.buf.Bytes(); len(b) > 4; {
			var n, events int
			if fastPath {
				// A count byte after a two-byte length.
				n, events = int(b[1]&0x7f)<<8|int(b[2]), int(b[3])
			} else {
				n = int(binary.BigEndian.Uint16(b[2:]))
				events = slowPathEvents(b[:n])
			}
			counts = append(counts, events)
			b = b[n:]
		}
		if conn.writes != 1 || !slices.Equal(counts, []int{255, 255, 90}) {
			t.Errorf("fast-path %v: %d writes of PDUs of %v events, want one of 255, 255 and 90", fastPath, conn.writes, counts)
		}
	}
	conn := &countConn{}
	c := &Client{width: 1024, height: 768, t: &transport{conn: conn}}
	if err := c.SendInput(); err != nil || conn.writes != 0 {
		t.Errorf("no events: %v, %d writes", err, conn.writes)
	}
}

// slowPathEvents returns the number of events in a TPKT of a slow-path
// Input Event PDU, which ends it: its numEvents and a pad, then the
// events of 12 bytes each.
func slowPathEvents(tpkt []byte) int {
	for n := maxInputBatch; n > 0; n-- {
		i := len(tpkt) - 12*n - 4
		if i >= 0 && int(binary.LittleEndian.Uint16(tpkt[i:])) == n {
			return n
		}
	}
	return 0
}

// Pause is Control after the E1 prefix and Num Lock, pressed and
// released.
func TestPauseEvents(t *testing.T) {
	conn := &recordConn{}
	c := &Client{width: 1024, height: 768, t: &transport{conn: conn}}
	c.fastPathInput.Store(true)
	if err := c.SendInput(PauseEvents()...); err != nil {
		t.Fatal(err)
	}
	if got, want := conn.buf.Bytes(), unhex(t, "100a"+"041d"+"0045"+"051d"+"0145"); !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestWheelEventClamps(t *testing.T) {
	for delta, want := range map[int]uint16{
		1000: 0x0200 | 0xff, -1000: 0x0200 | 0x0100, 255: 0x0200 | 0xff, -256: 0x0200 | 0x0100, -1: 0x0200 | 0x01ff,
	} {
		if got := WheelEvent(delta, false, image.Point{}).e.flags; got != want {
			t.Errorf("delta %d: flags %#x, want %#x", delta, got, want)
		}
	}
}

// cursorCount counts the shapes and moves the client gives its output.
type cursorCount struct {
	screen
	shapes, moves int
}

func (s *cursorCount) Cursor(img *image.RGBA, hot image.Point) {
	s.shapes++
	s.screen.Cursor(img, hot)
}

func (s *cursorCount) CursorMove(p image.Point) {
	s.moves++
	s.screen.CursorMove(p)
}

// Pointer updates are charged against the decoding budget: a move or a
// small shape as a 64×64 tile, a large shape as its pixels. A server
// cannot make the client hand shapes and moves to every viewer faster
// than it could hand them bitmaps.
func TestPointerPaced(t *testing.T) {
	const w, h = 1024, 768
	burst := 2*w*h + (bitmapPad+1)*(bitmapPad+1)
	wait := func(pixels int) time.Duration {
		return time.Duration(float64(pixels-burst) / decodeRate * float64(time.Second))
	}
	near := func(got, want time.Duration) bool { return got > want-time.Millisecond && got < want+time.Millisecond }

	const moves = 1000
	var frames [][]byte
	for i := range moves {
		frames = append(frames, rdptest.FastPath(rdptest.Update(rdptest.FPPtrPosition, rdptest.PointerPosition(image.Pt(i%w, i%h)))))
	}
	c, clock := testClock(w, h)
	out := &cursorCount{}
	if err := runFrames(c, out, frames...); !errors.Is(err, io.EOF) || out.moves != moves {
		t.Fatalf("moves: Run returned %v after %d moves", err, out.moves)
	}
	if want := wait(moves * pointerCost); !near(clock.slept, want) {
		t.Errorf("%d moves waited %v, want %v", moves, clock.slept, want)
	}

	const shapes = 300
	frames = frames[:0]
	small, large := solid(16, 16, color.RGBA{A: 0xff}), solid(80, 80, color.RGBA{R: 0xff, A: 0xff})
	for i := range shapes {
		frames = append(frames,
			rdptest.FastPath(rdptest.Update(rdptest.FPPointer, rdptest.Pointer(i%8, image.Point{}, small))),
			rdptest.FastPath(rdptest.Update(rdptest.FPPointer, rdptest.Pointer(i%8, image.Point{}, large))))
	}
	c, clock = testClock(w, h)
	out = &cursorCount{}
	if err := runFrames(c, out, frames...); !errors.Is(err, io.EOF) || out.shapes != 2*shapes {
		t.Fatalf("shapes: Run returned %v after %d shapes", err, out.shapes)
	}
	if want := wait(shapes * (pointerCost + 80*80)); !near(clock.slept, want) {
		t.Errorf("%d shapes waited %v, want %v", 2*shapes, clock.slept, want)
	}
}

// A shape that is shown already is not shown again: the cached pointer
// a server repeats, the hidden pointer, the default one.
func TestPointerShownOnce(t *testing.T) {
	frames := [][]byte{rdptest.FastPath(rdptest.Update(rdptest.FPPointer, rdptest.Pointer(3, image.Pt(1, 1), solid(8, 8, color.RGBA{A: 0xff}))))}
	for _, u := range [][]byte{
		rdptest.Update(rdptest.FPCachedPtr, rdptest.CachedPointer(3)),
		rdptest.Update(rdptest.FPPtrNull, nil),
		rdptest.Update(rdptest.FPPtrDefault, nil),
		rdptest.Update(rdptest.FPCachedPtr, rdptest.CachedPointer(3)),
	} {
		for range 1000 {
			frames = append(frames, rdptest.FastPath(u))
		}
	}
	c := testClient(1024, 768)
	out := &cursorCount{}
	if err := runFrames(c, out, frames...); !errors.Is(err, io.EOF) || out.shapes != 4 {
		t.Errorf("Run returned %v after %d shapes, want 4", err, out.shapes)
	}
	if out.cursor == nil || out.cursor.Rect.Dx() != 8 {
		t.Errorf("the last shape is %v, want the cached one", out.cursor)
	}
}

// A reason the server gives before it hangs up, without a Disconnect
// Provider Ultimatum, is Run's result.
func TestRunErrorInfoThenHangUp(t *testing.T) {
	for code, want := range map[uint32]error{0xc: nil, 0xb: nil, 0x9: ErrorInfo(0x9), 0x3: ErrorInfo(0x3)} {
		c := testClient(1024, 768)
		err := runFrames(c, &screen{}, rdptest.SetErrorInfo(code))
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("error info %#x: Run returned %v, want %v", code, err, want)
		}
	}
	if err := runFrames(testClient(1024, 768), &screen{}); !errors.Is(err, io.EOF) {
		t.Errorf("no reason: Run returned %v, want io.EOF", err)
	}
}

func TestHungUp(t *testing.T) {
	reset := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	for err, want := range map[error]bool{
		io.EOF:                       true,
		io.ErrUnexpectedEOF:          false,
		reset:                        true,
		errors.Join(errPaced, reset): true,
		errPaced:                     false,
	} {
		if HungUp(err) != want {
			t.Errorf("HungUp(%v) = %v", err, !want)
		}
	}
}

func TestTLSCheck(t *testing.T) {
	pin := "SHA256:" + strings.Repeat("A", 43)
	for name, tc := range map[string]struct {
		tls  TLS
		host string
		ok   bool
	}{
		"skip":           {TLS{InsecureSkipVerify: true}, "", true},
		"pin":            {TLS{Pins: []string{pin}}, "", true},
		"bad pin":        {TLS{Pins: []string{"zz"}}, "rdp.test", false},
		"system roots":   {TLS{}, "rdp.test", true},
		"no name":        {TLS{}, "", false},
		"server name":    {TLS{ServerName: "rdp.test"}, "", true},
		"roots and pins": {TLS{RootCAs: x509.NewCertPool(), Pins: []string{pin}}, "rdp.test", false},
	} {
		if err := tc.tls.Check(tc.host); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// solid returns a w×h image of one colour.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// FuzzRun runs a client in the active state on what a server sends, as
// the RDP backend runs it, into the sink of internal/backend's tests,
// which panics where the client breaks the session's contract: a lock
// off the screen or inside another, an unlock of another rectangle, a
// shape or a move out of turn. The seeds are the updates of the xrdp
// recordings and the PDUs of rdptest.
func FuzzRun(f *testing.F) {
	names, err := filepath.Glob("testdata/xrdp-*.trace.gz")
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range names {
		frames := loadTrace(f, name)
		// The updates follow the connection sequence and the four PDUs
		// of the finalization.
		f.Add(slices.Concat(frames[min(11, len(frames)):]...))
	}
	red := color.RGBA{R: 0xff, A: 0xff}
	for _, seed := range [][][]byte{
		{rdptest.FastPath(rdptest.Update(rdptest.FPBitmap, rdptest.BitmapUpdate(rdptest.RawBitmap(image.Pt(1000, 60), solid(30, 10, red)))))},
		{rdptest.SlowPathUpdate(rdptest.BitmapUpdate(rdptest.FillBitmap(image.Rect(0, 0, 1024, 64), red)))},
		rdptest.Fragments(rdptest.FPBitmap, rdptest.BitmapUpdate(rdptest.FillBitmap(image.Rect(10, 10, 300, 60), red)), 8),
		{
			rdptest.FastPath(
				rdptest.Update(rdptest.FPPointer, rdptest.Pointer(1, image.Pt(2, 2), solid(32, 32, red))),
				rdptest.Update(rdptest.FPCachedPtr, rdptest.CachedPointer(1)),
				rdptest.Update(rdptest.FPPtrNull, nil),
				rdptest.Update(rdptest.FPPtrDefault, nil),
				rdptest.Update(rdptest.FPPtrPosition, rdptest.PointerPosition(image.Pt(5000, 7))),
			),
		},
		{rdptest.SetErrorInfo(0x9), rdptest.DisconnectUltimatum()},
	} {
		f.Add(slices.Concat(seed...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		runFrames(testClient(fuzzWidth, fuzzHeight), &backendtest.Sink{}, data)
	})
}
