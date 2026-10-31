// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"errors"
	"image"
	"net"
	"testing"
)

// discardConn is a connection whose writes go nowhere.
type discardConn struct{ net.Conn }

func (discardConn) Write(p []byte) (int, error) { return len(p), nil }

// recordConn records what is written to it.
type recordConn struct {
	net.Conn
	buf bytes.Buffer
}

func (c *recordConn) Write(p []byte) (int, error) { return c.buf.Write(p) }

func TestConnectionRequest(t *testing.T) {
	// As probed against xrdp: TLS, CredSSP and CredSSP with early user
	// authorization, no cookie.
	got := connectionRequest("", ProtocolSSL|ProtocolHybrid|ProtocolHybridEx)
	want := unhex(t, "03000013"+"0ee000000000"+"00"+"01000800"+"0b000000")
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
	got = connectionRequest("bob", ProtocolSSL)
	if !bytes.Contains(got, []byte("Cookie: mstshash=bob\r\n")) || int(got[3]) != len(got) || int(got[4]) != len(got)-5 {
		t.Errorf("with cookie: %x", got)
	}
}

func TestConnectionConfirm(t *testing.T) {
	// xrdp 0.10.1's answers, after the TPKT header.
	p, err := parseConnectionConfirm(unhex(t, "0ed000001234000201080001000000"))
	if err != nil || p != ProtocolSSL {
		t.Errorf("TLS: %#x, %v", p, err)
	}
	p, err = parseConnectionConfirm(unhex(t, "0ed000001234000201080000000000"))
	if err != nil || p != ProtocolRDP {
		t.Errorf("standard security: %#x, %v", p, err)
	}
	_, err = parseConnectionConfirm(unhex(t, "0ed000001234000301080005000000"))
	var ne *NegotiationError
	if !errors.As(err, &ne) || ne.Code != 5 {
		t.Errorf("failure: %v", err)
	}
	if p, err = parseConnectionConfirm(unhex(t, "06d00000123400")); err != nil || p != ProtocolRDP {
		t.Errorf("no negotiation: %#x, %v", p, err)
	}
}

// The MCS PDUs against the examples of [MS-RDPBCGR] 4.1.
func TestMCSExamples(t *testing.T) {
	// 4.1.3: the GCC Conference Create Request around 0x11c bytes of
	// client data.
	gcc := conferenceCreateRequest(make([]byte, 0x11c))
	if want := unhex(t, "000500147c0001812a000800100001c00044756361811c"); !bytes.HasPrefix(gcc, want) {
		t.Errorf("GCC header %x, want %x", gcc[:len(want)], want)
	}
	if got := erectDomainRequest(); !bytes.Equal(got, unhex(t, "0401000100")) {
		t.Errorf("erect domain %x", got)
	}
	if got := attachUserRequest(); !bytes.Equal(got, []byte{0x28}) {
		t.Errorf("attach user %x", got)
	}
	user, err := parseAttachUserConfirm(unhex(t, "2e000006"))
	if err != nil || user != 1007 {
		t.Errorf("attach user confirm: %d, %v", user, err)
	}
	if got := channelJoinRequest(1007, 1003); !bytes.Equal(got, unhex(t, "38000603eb")) {
		t.Errorf("channel join %x", got)
	}
	if err := parseChannelJoinConfirm(unhex(t, "3e000006"+"03eb03eb"), 1003); err != nil {
		t.Error(err)
	}
	if err := parseChannelJoinConfirm(unhex(t, "3e000006"+"03eb03eb"), 1004); err == nil {
		t.Error("join confirm of another channel accepted")
	}
	if err := parseChannelJoinConfirm(unhex(t, "3e010006"+"03eb03eb"), 1003); err == nil {
		t.Error("refused join accepted")
	}
	ch, data, err := parseDomainPDU(append(unhex(t, "68"+"0001"+"03eb"+"70"+"03"), 1, 2, 3))
	if err != nil || ch != 1003 || !bytes.Equal(data, []byte{1, 2, 3}) {
		t.Errorf("send data indication: %d %x %v", ch, data, err)
	}
	if _, _, err := parseDomainPDU([]byte{0x21, 0x80}); !errors.Is(err, errDisconnected) {
		t.Errorf("disconnect ultimatum: %v", err)
	}
	big := sendDataRequest(1007, 1003, make([]byte, 300))
	if !bytes.Equal(big[:8], unhex(t, "64000603eb70812c")) {
		t.Errorf("send data request header %x", big[:8])
	}
}

func TestConnectInitialRoundTrip(t *testing.T) {
	c := &Client{cfg: Config{Width: 1280, Height: 800, ColorDepth: 32, ClientName: "occamy", KeyboardLayout: 0x409}, protocol: ProtocolSSL}
	ud := c.clientData()
	// TS_UD_CS_CORE is 216 bytes.
	if typ, n := int(ud[0])|int(ud[1])<<8, int(ud[2])|int(ud[3])<<8; typ != csCore || n != 216 {
		t.Errorf("core block %#x of %d bytes", typ, n)
	}
	ci := connectInitial(ud)
	if !bytes.HasPrefix(ci, []byte{0x7f, 0x65, 0x82}) || !bytes.Contains(ci, ud) {
		t.Errorf("connect initial %x", ci[:16])
	}

	// A connect response with server core, security and network blocks.
	var sd writer
	sd.u16(scCore)
	sd.u16(16)
	sd.u32(0x00080004)
	sd.u32(ProtocolSSL)
	sd.u32(0)
	sd.u16(scSecurity)
	sd.u16(12)
	sd.u32(0)
	sd.u32(0)
	sd.u16(scNet)
	sd.u16(10)
	sd.u16(1003)
	sd.u16(1)
	sd.u16(1004)
	gccResp := append(unhex(t, "000500147c00012a14760a01010001c0004d63446e"), perLen(nil, len(sd.b))...)
	gccResp = append(gccResp, sd.b...)
	var body []byte
	body = berTLV(body, berTagEnumerated, []byte{0})
	body = berTLV(body, berTagInteger, []byte{0})
	body = berTLV(body, berTagSequence, domainParameters(34, 3, 0, 0xfff8)[2:])
	body = berTLV(body, berTagOctetString, gccResp)
	resp := berLen([]byte{0x7f, 0x66}, len(body))
	resp = append(resp, body...)
	data, err := parseConnectResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseServerData(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ioChannel != 1003 || len(got.channels) != 1 || got.channels[0] != 1004 || got.version != 0x00080004 {
		t.Errorf("server data %+v", got)
	}
	resp[len(resp)-len(body)+2] = 1 // result: refused
	if _, err := parseConnectResponse(resp); err == nil {
		t.Error("refused connect response accepted")
	}
}

func TestCapabilities(t *testing.T) {
	c := &Client{cfg: Config{ColorDepth: 24, KeyboardLayout: 0x409}, width: 1024, height: 768, userID: 1007, shareID: 0x103ea}
	ca := c.confirmActive()
	r := reader{b: ca}
	if total := int(r.u16()); total != len(ca) {
		t.Errorf("total length %d of %d", total, len(ca))
	}
	if typ := r.u16(); typ != pduConfirmActive|0x10 {
		t.Errorf("type %#x", typ)
	}
	r.u16() // source
	if r.u32() != 0x103ea {
		t.Error("share ID")
	}
	r.u16() // originator
	srcLen, capsLen := int(r.u16()), int(r.u16())
	r.skip(srcLen)
	n := int(r.u16())
	r.u16()
	if r.len() != capsLen-4 {
		t.Errorf("capabilities of %d bytes, length says %d", r.len(), capsLen-4)
	}
	seen := map[uint16]bool{}
	for range n {
		typ, l := r.u16(), int(r.u16())
		body := r.take(l - 4)
		seen[typ] = true
		if typ == capOrder && !bytes.Equal(body[32:64], make([]byte, 32)) {
			t.Error("the client supports drawing orders")
		}
		if typ == capBitmap && body[0] != 24 {
			t.Errorf("preferred depth %d", body[0])
		}
	}
	if r.err != nil || r.len() != 0 {
		t.Errorf("capability sets: %v, %d bytes left", r.err, r.len())
	}
	for _, typ := range []uint16{capGeneral, capBitmap, capOrder, capBitmapCache, capPointer, capInput,
		capBrush, capGlyphCache, capOffscreenCache, capVirtualChannel, capSound} {
		if !seen[typ] {
			t.Errorf("no capability set %d, which [MS-RDPBCGR] 2.2.1.13.2.1 requires", typ)
		}
	}

	// The client reads the size and input flags of a Demand Active.
	var da writer
	da.u32(0x103ea)
	da.u16(4)
	da.u16(0)
	da.bytes([]byte("RDP\x00"))
	da.u16(2)
	da.u16(0)
	da.u16(capBitmap)
	da.u16(28)
	da.u16(32)
	da.zero(6)
	da.u16(1280)
	da.u16(720)
	da.zero(12)
	da.u16(capInput)
	da.u16(8)
	da.u16(inputFlagScancodes | inputFlagFastPathInput2)
	da.u16(0)
	id, caps, err := parseDemandActive(da.b)
	if err != nil || id != 0x103ea || caps.width != 1280 || caps.height != 720 || caps.bpp != 32 || caps.inputFlags != inputFlagScancodes|inputFlagFastPathInput2 {
		t.Errorf("demand active: %#x %+v %v", id, caps, err)
	}
}

func TestFastPathInput(t *testing.T) {
	conn := &recordConn{}
	c := &Client{width: 1024, height: 768, t: &transport{conn: conn}}
	c.fastPathInput.Store(true)
	for _, tc := range []struct {
		name string
		send func() error
		want string
	}{
		{"key down", func() error { return c.Key(0x1e, true) }, "0404001e"},
		{"key up", func() error { return c.Key(0x1e, false) }, "0404011e"},
		{"extended key", func() error { return c.Key(0xe048, true) }, "04040248"},
		{"unicode", func() error { return c.Unicode(0x263a, true) }, "0405803a26"},
		{"move", func() error { return c.MouseMove(image.Pt(100, 200)) }, "040920" + "0008" + "6400c800"},
		{"move clamped", func() error { return c.MouseMove(image.Pt(5000, -3)) }, "040920" + "0008" + "ff030000"},
		{"left down", func() error { return c.MouseButton(ButtonLeft, true, image.Pt(1, 2)) }, "040920" + "0090" + "01000200"},
		{"right up", func() error { return c.MouseButton(ButtonRight, false, image.Pt(1, 2)) }, "040920" + "0020" + "01000200"},
		{"back down", func() error { return c.MouseButton(ButtonBack, true, image.Pt(1, 2)) }, "040940" + "0180" + "01000200"},
		{"wheel down", func() error { return c.MouseWheel(-120, false, image.Pt(1, 2)) }, "040920" + "8803" + "01000200"},
		{"wheel up", func() error { return c.MouseWheel(120, false, image.Pt(1, 2)) }, "040920" + "7802" + "01000200"},
	} {
		conn.buf.Reset()
		if err := tc.send(); err != nil {
			t.Fatal(err)
		}
		if got := conn.buf.Bytes(); !bytes.Equal(got, unhex(t, tc.want)) {
			t.Errorf("%s: %x, want %s", tc.name, got, tc.want)
		}
	}
	// Many events take a two-byte length and a count byte.
	events := make([]inputEvent, 100)
	b := encodeFastPath(events)
	if b[0] != 0 || b[1]&0x80 == 0 || int(b[1]&0x7f)<<8|int(b[2]) != len(b) || b[3] != 100 {
		t.Errorf("100 events: header %x, %d bytes", b[:4], len(b))
	}
}

func TestSlowPathInput(t *testing.T) {
	b := encodeSlowPath([]inputEvent{{kind: evScancode, flags: keyRelease | keyExtended, a: 0x48}})
	if want := unhex(t, "0100"+"0000"+"00000000"+"0400"+"0081"+"4800"+"0000"); !bytes.Equal(b, want) {
		t.Errorf("got %x, want %x", b, want)
	}
}

func TestScancodes(t *testing.T) {
	for _, tc := range []struct {
		keysym uint32
		hid    uint16
		want   uint16
	}{
		{'a', 0x04, 0x1e}, {'Z', 0x1d, 0x2c}, {'1', 0x1e, 0x02}, {'!', 0x1e, 0x02},
		{0xff0d, 0x28, 0x1c}, {0xff1b, 0x29, 0x01}, {0xffbe, 0x3a, 0x3b}, {0xffc9, 0x45, 0x58},
		{0xff52, 0x52, 0xe048}, {0xffe1, 0xe1, 0x2a}, {0xffea, 0xe6, 0xe038}, {'/', 0x38, 0x35},
	} {
		if got, ok := ScancodeForKeysym(tc.keysym); !ok || got != tc.want {
			t.Errorf("keysym %#x: %#x, %v, want %#x", tc.keysym, got, ok, tc.want)
		}
		if got, ok := ScancodeForHID(tc.hid); !ok || got != tc.want {
			t.Errorf("HID %#x: %#x, %v, want %#x", tc.hid, got, ok, tc.want)
		}
	}
	if _, ok := ScancodeForKeysym(0x20ac); ok { // EuroSign
		t.Error("a keysym off the US layout has a scancode")
	}
	if _, ok := ScancodeForHID(0x48); ok { // Pause
		t.Error("Pause has a scancode")
	}
	if got, ok := ScancodeForKeysym(0xff6b); !ok || got != 0xe046 { // Break
		t.Errorf("Break: %#x, %v, want E0 46", got, ok)
	}
}
