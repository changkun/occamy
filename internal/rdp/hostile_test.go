// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"image"
	"net"
	"strings"
	"testing"
	"time"
)

// What a hostile server could send, from the review of the spike: each
// test fails if the guard it covers is removed.

// sizeRecorder is an Output that records resizes without allocating.
type sizeRecorder struct {
	resizes []image.Point
	img     *image.RGBA
}

func (s *sizeRecorder) Resize(w, h int) { s.resizes = append(s.resizes, image.Pt(w, h)) }
func (s *sizeRecorder) Lock(r image.Rectangle) *image.RGBA {
	if s.img == nil || !r.In(s.img.Rect) {
		s.img = image.NewRGBA(r)
	}
	return s.img
}
func (s *sizeRecorder) Unlock(image.Rectangle)          {}
func (s *sizeRecorder) Cursor(*image.RGBA, image.Point) {}
func (s *sizeRecorder) CursorMove(image.Point)          {}

// demandActive builds a share control Demand Active PDU with one bitmap
// capability set for a w×h desktop.
func demandActive(w, h int) []byte {
	var caps writer
	caps.u16(capBitmap)
	caps.u16(28)
	caps.u16(32) // preferredBitsPerPixel
	caps.u16(1)
	caps.u16(1)
	caps.u16(1)
	caps.u16(w)
	caps.u16(h)
	caps.zero(12)
	var b writer
	b.u32(0x1234) // shareId
	b.u16(0)      // lengthSourceDescriptor
	b.u16(len(caps.b) + 4)
	b.u16(1) // numberCapabilities
	b.u16(0)
	b.bytes(caps.b)
	return shareControl(pduDemandActive, serverChannelID, b.b)
}

// A Demand Active may announce up to 65535×65535 pixels, which a sink
// would allocate 16 GiB for.
func TestDemandActiveTooLarge(t *testing.T) {
	for _, size := range []image.Point{{0xffff, 0xffff}, {maxSide + 1, 100}, {maxSide, maxSide}} {
		c := testClient(1024, 768)
		out := &sizeRecorder{}
		if _, err := c.handleShareControl(out, demandActive(size.X, size.Y)); err == nil {
			t.Errorf("%v: accepted", size)
		}
		if len(out.resizes) != 0 {
			t.Errorf("%v: resized to %v", size, out.resizes)
		}
	}
}

// One PDU of 60 KB holds over a thousand Demand Actives, each a resize
// of up to 128 MiB: the second is refused.
func TestDemandActiveRepeated(t *testing.T) {
	c := testClient(1024, 768)
	out := &sizeRecorder{}
	var data []byte
	for i := 0; len(data) < 60000; i++ {
		data = append(data, demandActive(4096-i%2, 4096)...)
	}
	_, err := c.handleShareControl(out, data)
	if err == nil || len(out.resizes) > 1 {
		t.Errorf("one %d-byte PDU: %v, %d resizes", len(data), err, len(out.resizes))
	}
}

// Demand Actives in PDUs of their own resize at most every resizeEvery,
// and the wait ends with the context.
func TestDemandActiveResizeRate(t *testing.T) {
	c := testClient(1024, 768)
	out := &sizeRecorder{}
	c.lastResize = time.Now()
	start := time.Now()
	if _, err := c.handleShareControl(out, demandActive(800, 600)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < resizeEvery/2 || len(out.resizes) != 1 {
		t.Errorf("resized %v after %v", out.resizes, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ctx = ctx
	if _, err := c.handleShareControl(out, demandActive(1024, 768)); !errors.Is(err, context.Canceled) {
		t.Errorf("a resize after the context ended: %v", err)
	}
}

// fastPathBitmaps builds the data of a fast-path update PDU with n
// compressed w×h rectangles at bpp that cover the desktop from the
// origin, each with data and no compression header.
func fastPathBitmaps(n, w, h, bpp int, data []byte) []byte {
	var u writer
	u.u16(updateBitmap)
	u.u16(n)
	for range n {
		u.u16(0)
		u.u16(0)
		u.u16(w - 1)
		u.u16(h - 1)
		u.u16(w)
		u.u16(h)
		u.u16(bpp)
		u.u16(bitmapCompression | noBitmapCompressionHDR)
		u.u16(len(data))
		u.bytes(data)
	}
	var f writer
	f.u8(fpBitmap)
	f.u16(len(u.b))
	f.bytes(u.b)
	return f.b
}

// A few bytes of RLE describe a whole desktop. A PDU full of such
// rectangles decodes at most its budget of pixels, and the decoder does
// not keep the large buffers.
func TestBitmapPixelBudget(t *testing.T) {
	c := testClient(1024, 768)
	scr := &screen{}
	scr.Resize(1024, 768)
	// A mega-mega background run of 65535 pixels, 3 bytes for a whole
	// desktop: 3000 of them fit a fast-path PDU.
	start := time.Now()
	err := c.handleFastPath(scr, frame{fastPath: true, data: fastPathBitmaps(3000, 1024, 768, 16, []byte{0xf0, 0xff, 0xff})})
	if d := time.Since(start); err == nil || !strings.Contains(err.Error(), "decodes more than") || d > time.Second {
		t.Errorf("a PDU of 3000 desktop-sized rectangles: %v after %v", err, d)
	}
	if n := c.Stats().Bitmaps["rle16"]; n != 2 {
		t.Errorf("decoded %d rectangles, want 2 within the budget", n)
	}
	if cap(c.dec.rgba) > 4*retainPixels || cap(c.dec.raw) > retainPixels {
		t.Errorf("the decoder keeps %d and %d bytes", cap(c.dec.rgba), 4*cap(c.dec.raw))
	}
	// Rectangles with no data are not decoded at all.
	c = testClient(1024, 768)
	start = time.Now()
	if err := c.handleFastPath(scr, frame{fastPath: true, data: fastPathBitmaps(3600, 1024, 768, 16, nil)}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond || c.Stats().Ignored["empty bitmaps"] != 3600 {
		t.Errorf("3600 empty rectangles took %v, ignored %v", d, c.Stats().Ignored)
	}
}

// A bitmap may be larger than its destination and the desktop by a tile's
// padding, no more.
func TestBitmapBounds(t *testing.T) {
	scr := &screen{}
	scr.Resize(256, 256)
	data := []byte{0x60, 0, 1, 0} // a colour run of 32 pixels at 16 bits per pixel
	for _, tc := range []struct {
		name string
		dest image.Rectangle
		w, h int
		ok   bool
	}{
		{"padded to a tile", image.Rect(0, 0, 1, 1), 64, 64, true},
		{"wider than its destination", image.Rect(0, 0, 1, 1), 65, 1, false},
		{"taller than the desktop", image.Rect(0, 0, 256, 1024), 1, 320, false},
	} {
		c := testClient(256, 256)
		err := parseBitmapUpdate(bitmapUpdate(tc.dest, tc.w, tc.h, 16, bitmapCompression|noBitmapCompressionHDR, data)[2:],
			func(b *bitmapData) error { return c.drawBitmap(scr, b) })
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// The decoder refuses a bitmap of more than maxBitmapPixels on its own,
// though an empty stream would decode.
func TestMaxBitmapPixels(t *testing.T) {
	var d decoder
	if _, err := d.decode(&bitmapData{width: 64, height: 64, bpp: 16, compressed: true}); err != nil {
		t.Fatalf("an empty stream: %v", err)
	}
	if _, err := d.decode(&bitmapData{width: 4097, height: 2048, bpp: 16, compressed: true}); err == nil {
		t.Error("decoded a bitmap of 4097×2048")
	}
}

// pointerAttribute builds a 2×2 TS_POINTERATTRIBUTE at 24 bits per pixel
// for a cache index.
func pointerAttribute(index int) []byte {
	var w writer
	w.u16(24)
	w.u16(index)
	w.u16(1)
	w.u16(1)
	w.u16(2)
	w.u16(2)
	w.u16(4)
	w.u16(12)
	w.zero(16)
	return w.b
}

// A pointer's cache index must lie in the cache, both when the server
// sets it and when it uses it.
func TestPointerCacheIndex(t *testing.T) {
	var d decoder
	if _, _, err := d.parsePointerAttribute(pointerAttribute(maxPointerCache-1), true, false); err != nil {
		t.Fatalf("index %d: %v", maxPointerCache-1, err)
	}
	if _, _, err := d.parsePointerAttribute(pointerAttribute(maxPointerCache), true, false); err == nil {
		t.Errorf("index %d accepted", maxPointerCache)
	}
	c := testClient(64, 64)
	for _, i := range []int{maxPointerCache, 0xffff} {
		if err := c.cachedPointer(&sizeRecorder{}, []byte{byte(i), byte(i >> 8)}); err == nil {
			t.Errorf("cached pointer %d accepted", i)
		}
	}
}

// The routing cookie fits the TPDU's one-byte length and ends at the
// client's CR LF, whatever the user name.
func TestRoutingCookie(t *testing.T) {
	for _, user := range []string{strings.Repeat("a", 250), "bob\r\nCookie: mstshash=eve", "\x00\x7f\xff"} {
		b := connectionRequest(user, ProtocolSSL)
		if int(b[4])+5 != len(b) {
			t.Errorf("%q: length indicator %d for a %d-byte TPDU", user, b[4], len(b)-5)
		}
		if n := bytes.Count(b, []byte("\r\n")); n > 1 || n == 1 && !bytes.Contains(b, []byte("Cookie: mstshash=")) {
			t.Errorf("%q: routing token %q", user, b[11:len(b)-8])
		}
	}
	if got := routingCookie("occamy-user"); got != "occamy-us" {
		t.Errorf("cookie %q, want the first nine characters", got)
	}
}

// Config.Trace sees the Client Info PDU with the password zeroed.
func TestTraceRedactsPassword(t *testing.T) {
	frames := loadTrace(t, "testdata/xrdp-32bpp.trace.gz")
	var sent [][]byte
	replay(t, &fakeServer{frames: frames}, Config{
		Username: "occamy", Password: "hunter2-secret",
		Trace: func(d Direction, p []byte) {
			if d == ToServer {
				sent = append(sent, bytes.Clone(p))
			}
		},
	})
	info := false
	for _, p := range sent {
		if bytes.Contains(p, utf16le("hunter2-secret")) {
			t.Errorf("Trace received the password in a %d-byte frame", len(p))
		}
		if bytes.Contains(p, utf16le("occamy")) {
			info = true
		}
	}
	if !info {
		t.Error("Trace did not see the Client Info PDU")
	}
}

// A last fragment without a first is dropped, not appended to the
// previous update.
func TestOrphanFragment(t *testing.T) {
	c := testClient(64, 64)
	out := &sizeRecorder{}
	pos := func(frag int, p []byte) []byte {
		var f writer
		f.u8(fpPtrPosition | frag<<4)
		f.u16(len(p))
		f.bytes(p)
		return f.b
	}
	data := append(pos(fragFirst, []byte{1, 0}), pos(fragLast, []byte{2, 0})...)
	if err := c.handleFastPath(out, frame{fastPath: true, data: data}); err != nil {
		t.Fatal(err)
	}
	if err := c.handleFastPath(out, frame{fastPath: true, data: pos(fragLast, []byte{9, 9})}); err != nil {
		t.Fatal(err)
	}
	if len(c.frag) == 6 || c.Stats().Ignored["orphan fragments"] != 1 {
		t.Errorf("a lone last fragment was appended to the previous update: %x", c.frag)
	}
}

// indication wraps share control data in a Send Data Indication on the
// I/O channel of the recordings, 1003.
func indication(data []byte) []byte {
	ind := []byte{mcsSendDataIndication, 0x00, 0x06, 0x03, 0xeb, 0x70}
	if len(data) >= 0x80 {
		ind = append(ind, 0x80|byte(len(data)>>8))
	}
	return tpkt(append(append(ind, byte(len(data))), data...))
}

// A server that refuses the logon during the connection sequence says
// why in a Set Error Info PDU, and Connect reports it.
func TestConnectSetErrorInfo(t *testing.T) {
	frames := loadTrace(t, "testdata/xrdp-32bpp.trace.gz")
	// Keep the frames up to the Demand Active, then refuse.
	n := 0
	for i, fr := range frames {
		if len(fr) > 7 && fr[0] == 0x03 && fr[5] == 0xf0 {
			if _, data, err := parseDomainPDU(fr[7:]); err == nil && len(data) >= 4 && data[2]&0xf == pduDemandActive {
				n = i
				break
			}
		}
	}
	if n == 0 {
		t.Fatal("no Demand Active in the recording")
	}
	var w writer
	w.u32(0x103ea) // shareId
	w.u8(0)
	w.u8(1)
	w.u16(8)
	w.u8(pdu2SetErrorInfo)
	w.u8(0)
	w.u16(0)
	w.u32(7) // ERRINFO_SERVER_DENIED_CONNECTION
	frames = append(frames[:n:n], indication(shareControl(pduData, serverChannelID, w.b)), tpkt([]byte{0x21, 0x80}))

	cconn, sconn := net.Pipe()
	go (&fakeServer{frames: frames}).serve(sconn)
	_, err := Connect(context.Background(), cconn, Config{
		Username: "occamy", Password: "occamy", Protocols: ProtocolSSL,
		InsecureSkipVerify: true, Logger: quiet,
	})
	var ei ErrorInfo
	if !errors.As(err, &ei) || ei != 7 {
		t.Errorf("Connect returned %v, want the server's error info 7", err)
	}
}

// FuzzConnectionSequence covers the parsers of the connection sequence
// that FuzzFastPath and FuzzSlowPath do not reach.
func FuzzConnectionSequence(f *testing.F) {
	frames := loadTrace(f, "testdata/xrdp-32bpp.trace.gz")
	for _, fr := range frames[:min(12, len(frames))] {
		for k := range 7 {
			f.Add(uint8(k), fr)
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		switch kind % 7 {
		case 0:
			tr := newTransport(readConn{bytes.NewReader(data)}, nil)
			var buf []byte
			for range 4 {
				var err error
				if _, buf, err = tr.readFrame(buf); err != nil {
					break
				}
			}
		case 1:
			parseConnectionConfirm(data)
		case 2:
			if d, err := parseConnectResponse(data); err == nil {
				parseServerData(d)
			}
			parseServerData(data)
		case 3:
			parseAttachUserConfirm(data)
			parseChannelJoinConfirm(data, 1003)
			parseDomainPDU(data)
		case 4:
			parseLicense(data)
		case 5:
			parseDemandActive(data)
		case 6:
			readTSRequest(bufio.NewReader(bytes.NewReader(data)))
		}
	})
}

// readConn is a connection that reads from r and discards writes.
type readConn struct{ r *bytes.Reader }

func (c readConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c readConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c readConn) Close() error                     { return nil }
func (c readConn) LocalAddr() net.Addr              { return nil }
func (c readConn) RemoteAddr() net.Addr             { return nil }
func (c readConn) SetDeadline(time.Time) error      { return nil }
func (c readConn) SetReadDeadline(time.Time) error  { return nil }
func (c readConn) SetWriteDeadline(time.Time) error { return nil }
