// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
	if s.img == nil {
		s.img = image.NewRGBA(r)
	} else if !r.In(s.img.Rect) {
		// Grown to all that was locked, so that strips of one bitmap
		// allocate only the first time.
		s.img = image.NewRGBA(r.Union(s.img.Rect))
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
	c.now, c.sleep = nil, nil // the clock and its timers
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

// A few bytes of RLE describe a whole desktop: a mega-mega background
// run of 65535 pixels is 3 bytes. The client decodes such rectangles at
// decodeRate, whether they come in one PDU or one per PDU, and a bitmap
// larger than the bucket waits its share.
func TestDecodeRate(t *testing.T) {
	const w, h, n = 1024, 768, 50
	desktop := fastPathBitmaps(1, w, h, 16, []byte{0xf0, 0xff, 0xff})
	burst := 2*w*h + (bitmapPad+1)*(bitmapPad+1)
	want := time.Duration(float64(n*w*h-burst) / decodeRate * float64(time.Second))
	near := func(d time.Duration) bool { return d > want-time.Millisecond && d < want+time.Millisecond }
	scr := &screen{}
	scr.Resize(w, h)

	// One PDU of n desktops, and n PDUs of one.
	c, clock := testClock(w, h)
	if err := c.handleFastPath(scr, frame{fastPath: true, data: fastPathBitmaps(n, w, h, 16, []byte{0xf0, 0xff, 0xff})}); err != nil {
		t.Fatal(err)
	}
	if c.Stats().Bitmaps["rle16"] != n || !near(clock.slept) {
		t.Errorf("one PDU of %d desktops: decoded %d, waited %v, want %v", n, c.Stats().Bitmaps["rle16"], clock.slept, want)
	}
	c, clock = testClock(w, h)
	for range n {
		if err := c.handleFastPath(scr, frame{fastPath: true, data: desktop}); err != nil {
			t.Fatal(err)
		}
	}
	if !near(clock.slept) {
		t.Errorf("%d PDUs of one desktop: waited %v, want %v", n, clock.slept, want)
	}

	// Without a wait, the bucket holds two desktops and a tile.
	c = testClient(w, h)
	err := c.handleFastPath(scr, frame{fastPath: true, data: fastPathBitmaps(n, w, h, 16, []byte{0xf0, 0xff, 0xff})})
	if !errors.Is(err, errPaced) || c.Stats().Bitmaps["rle16"] != 2 {
		t.Errorf("an empty bucket: %v after %d desktops, want 2", err, c.Stats().Bitmaps["rle16"])
	}

	// A bitmap larger than the bucket, as a desktop of 1×8192 allows,
	// waits for its share, not for ever.
	c, clock = testClock(1, 8192)
	bw, bh := 1+bitmapPad, 8192+bitmapPad
	big := fastPathBitmaps(1, bw, bh, 16, []byte{0xf0, 0xff, 0xff})
	wantBig := time.Duration(float64(bw*bh-(2*8192+(bitmapPad+1)*(bitmapPad+1))) / decodeRate * float64(time.Second))
	if err := c.handleFastPath(&sizeRecorder{}, frame{fastPath: true, data: big}); err != nil || clock.slept != wantBig {
		t.Errorf("a bitmap of %d×%d on a desktop of 1×8192: %v, waited %v, want %v", bw, bh, err, clock.slept, wantBig)
	}

	// Real traffic never waits: the motion scene's 30 desktops of
	// 1024×768 a second, or 15 of 1920×1080.
	for _, tc := range []struct{ w, h, fps int }{{1024, 768, 30}, {1920, 1080, 15}} {
		c, clock = testClock(tc.w, tc.h)
		scr.Resize(tc.w, tc.h)
		pdu := fastPathBitmaps(1, tc.w, tc.h, 16, []byte{0xf0, 0xff, 0xff})
		for range 2 * tc.fps {
			if err := c.handleFastPath(scr, frame{fastPath: true, data: pdu}); err != nil {
				t.Fatal(err)
			}
			clock.t = clock.t.Add(time.Second / time.Duration(tc.fps))
		}
		if clock.slept != 0 {
			t.Errorf("%d desktops of %d×%d a second waited %v", tc.fps, tc.w, tc.h, clock.slept)
		}
	}

	// Rectangles with no data are not decoded or charged at all.
	c = testClient(w, h)
	start := time.Now()
	if err := c.handleFastPath(scr, frame{fastPath: true, data: fastPathBitmaps(3600, w, h, 16, nil)}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond || c.Stats().Ignored["empty bitmaps"] != 3600 {
		t.Errorf("3600 empty rectangles took %v, ignored %v", d, c.Stats().Ignored)
	}
}

// The decoder keeps its buffers from bitmap to bitmap, however large,
// and drops them when the desktop changes.
func TestDecoderKeepsScratch(t *testing.T) {
	c, _ := testClock(1024, 768)
	out := &sizeRecorder{}
	pdu := fastPathBitmaps(1, 1024, 768, 16, []byte{0xf0, 0xff, 0xff})
	if err := c.handleFastPath(out, frame{fastPath: true, data: pdu}); err != nil {
		t.Fatal(err)
	}
	raw, rgba := &c.dec.raw[0], &c.dec.rgba[0]
	a := testing.AllocsPerRun(10, func() {
		if err := c.handleFastPath(out, frame{fastPath: true, data: pdu}); err != nil {
			t.Fatal(err)
		}
	})
	if &c.dec.raw[0] != raw || &c.dec.rgba[0] != rgba {
		t.Error("the decoder's buffers moved between desktop-sized bitmaps")
	}
	// The one allocation is the PDU's bitmapData, which its rectangles
	// share: the bitmap itself allocates nothing, with or without -race.
	if a > 1 {
		t.Errorf("a PDU of a desktop-sized bitmap allocated %v times, want at most 1", a)
	}
	if _, err := c.handleShareControl(out, demandActive(800, 600)); err != nil {
		t.Fatal(err)
	}
	if c.dec.raw != nil || c.dec.rgba != nil {
		t.Errorf("the decoder kept %d and %d bytes after a resize", 4*cap(c.dec.raw), cap(c.dec.rgba))
	}
}

// BenchmarkHostileBitmap is the cheapest PDU for the most decoding: one
// rectangle of 3 bytes of RLE. Its ns/pixel times decodeRate is the CPU
// a hostile server can make the client spend in a second.
func BenchmarkHostileBitmap(b *testing.B) {
	for _, size := range []image.Point{{1024, 768}, {4096, 2048}} {
		b.Run(fmt.Sprintf("%dx%d", size.X, size.Y), func(b *testing.B) {
			c, _ := testClock(size.X, size.Y)
			out := &sizeRecorder{}
			pdu := fastPathBitmaps(1, size.X, size.Y, 16, []byte{0xf0, 0xff, 0xff})
			b.ReportAllocs()
			for b.Loop() {
				if err := c.handleFastPath(out, frame{fastPath: true, data: pdu}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(size.X*size.Y), "ns/pixel")
		})
	}
}

// BenchmarkTileBitmaps is one PDU of 64 bitmaps of 64×64 pixels, the
// tiles Windows sends, so that what each bitmap costs beside its pixels
// shows.
func BenchmarkTileBitmaps(b *testing.B) {
	c, _ := testClock(1024, 768)
	out := &sizeRecorder{}
	// RLE of a background run of the tile's 4096 pixels.
	pdu := fastPathBitmaps(64, 64, 64, 16, []byte{0xf0, 0x00, 0x10})
	b.ReportAllocs()
	for b.Loop() {
		if err := c.handleFastPath(out, frame{fastPath: true, data: pdu}); err != nil {
			b.Fatal(err)
		}
	}
	if n := c.Stats().Bitmaps["rle16"]; n == 0 {
		b.Fatal("no bitmap decoded")
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

// pointerAttributePDU builds a 2×2 TS_POINTERATTRIBUTE at 24 bits per pixel
// for a cache index.
func pointerAttributePDU(index int) []byte {
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
	if _, err := parsePointerAttribute(pointerAttributePDU(maxPointerCache-1), true, false); err != nil {
		t.Fatalf("index %d: %v", maxPointerCache-1, err)
	}
	if _, err := parsePointerAttribute(pointerAttributePDU(maxPointerCache), true, false); err == nil {
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

// Config.Trace sees the Client Info PDU without the password: neither its
// bytes nor its length, which cbPassword and every length around it would
// tell.
func TestTraceRedactsPassword(t *testing.T) {
	frames := loadTrace(t, "testdata/xrdp-32bpp.trace.gz")
	info := func(password string) []byte {
		var info []byte
		replay(t, &fakeServer{frames: frames}, Config{
			Username: "trace-user", Password: password,
			Trace: func(d Direction, p []byte) {
				if d != ToServer {
					return
				}
				if bytes.Contains(p, utf16le(password)) {
					t.Errorf("Trace received the password in a %d-byte frame", len(p))
				}
				if bytes.Contains(p, utf16le("trace-user")) {
					info = bytes.Clone(p)
				}
			},
		})
		if info == nil {
			t.Fatal("Trace did not see the Client Info PDU")
		}
		return info
	}
	if short, long := info("hunter2"), info("hunter2-a-much-longer-secret"); !bytes.Equal(short, long) {
		t.Errorf("Trace saw Client Info PDUs of %d and %d bytes for passwords of two lengths", len(short), len(long))
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
		TLS: TLS{InsecureSkipVerify: true}, Logger: quiet,
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
