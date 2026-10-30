// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// The security protocols of [MS-RDPBCGR] 2.2.1.1.1, as requested in the
// X.224 Connection Request and selected in the Connection Confirm.
const (
	ProtocolRDP      uint32 = 0x0 // standard RDP security, which Client refuses
	ProtocolSSL      uint32 = 0x1 // TLS
	ProtocolHybrid   uint32 = 0x2 // CredSSP (NLA) over TLS
	ProtocolHybridEx uint32 = 0x8 // CredSSP with the Early User Authorization Result
)

// RDP_NEG_* types of [MS-RDPBCGR] 2.2.1.1.1 and 2.2.1.2.
const (
	negReq     = 0x01
	negRsp     = 0x02
	negFailure = 0x03
)

// maxFrame bounds the frames the transport reads: a TPKT's length is 16
// bits and a fast-path PDU's 15.
const maxFrame = 0xffff

// Direction tells a Trace function which way a frame went.
type Direction int

// The directions of a frame.
const (
	FromServer Direction = iota
	ToServer
)

// transport frames TPKT (slow-path) and fast-path PDUs over a connection,
// which TLS may replace after the X.224 exchange.
type transport struct {
	conn  net.Conn
	r     *bufio.Reader
	trace func(Direction, []byte)

	wmu sync.Mutex // serializes writes
}

func newTransport(conn net.Conn, trace func(Direction, []byte)) *transport {
	return &transport{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), trace: trace}
}

// upgrade replaces the connection, as when TLS starts over it.
func (t *transport) upgrade(conn net.Conn) {
	t.conn = conn
	t.r = bufio.NewReaderSize(conn, 64<<10)
}

// frame is one PDU read from the server.
type frame struct {
	// fastPath is set for a fast-path PDU, whose header byte is hdr.
	fastPath bool
	hdr      byte
	// data is the PDU after the TPKT and X.224 Data headers for a slow-path
	// PDU, and after the length for a fast-path PDU. It is valid until the
	// next read.
	data []byte
}

// readFrame reads the next PDU.
func (t *transport) readFrame(buf []byte) (frame, []byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(t.r, h[:2]); err != nil {
		return frame{}, buf, err
	}
	var f frame
	var n, hdrLen int
	if h[0] == 0x03 { // TPKT, [T.123] 8
		if _, err := io.ReadFull(t.r, h[2:4]); err != nil {
			return frame{}, buf, err
		}
		n, hdrLen = int(binary.BigEndian.Uint16(h[2:4])), 4
	} else if h[0]&0x3 == 0 { // fast-path, [MS-RDPBCGR] 2.2.9.1.2
		f.fastPath, f.hdr = true, h[0]
		n, hdrLen = int(h[1]), 2
		if n&0x80 != 0 {
			if _, err := io.ReadFull(t.r, h[2:3]); err != nil {
				return frame{}, buf, err
			}
			n, hdrLen = (n&0x7f)<<8|int(h[2]), 3
		}
	} else {
		return frame{}, buf, fmt.Errorf("rdp: unknown PDU header %#02x", h[0])
	}
	if n < hdrLen || n > maxFrame {
		return frame{}, buf, fmt.Errorf("rdp: bad PDU length %d", n)
	}
	if cap(buf) < n {
		buf = make([]byte, n, max(n, 2*cap(buf)))
	}
	buf = buf[:n]
	copy(buf, h[:hdrLen])
	if _, err := io.ReadFull(t.r, buf[hdrLen:]); err != nil {
		return frame{}, buf, err
	}
	if t.trace != nil {
		t.trace(FromServer, buf)
	}
	f.data = buf[hdrLen:]
	if !f.fastPath {
		// Every TPKT after the connection confirm carries an X.224 Data
		// TPDU, [X.224] 13.7: length indicator 2, DT with EOT.
		if len(f.data) >= 3 && f.data[0] == 0x02 && f.data[1] == 0xf0 {
			f.data = f.data[3:]
		} else if len(f.data) < 2 || f.data[1]&0xf0 != 0xd0 {
			return frame{}, buf, errors.New("rdp: TPKT without X.224 data")
		}
	}
	return f, buf, nil
}

// write sends one complete frame.
func (t *transport) write(p []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	if t.trace != nil {
		t.trace(ToServer, p)
	}
	_, err := t.conn.Write(p)
	return err
}

// tpkt wraps an X.224 Data TPDU's payload in its headers.
func tpkt(payload []byte) []byte {
	n := 7 + len(payload)
	b := make([]byte, 7, n)
	b[0] = 0x03
	binary.BigEndian.PutUint16(b[2:], uint16(n)) // #nosec G115 -- MCS PDUs stay below 64 KiB
	b[4], b[5], b[6] = 0x02, 0xf0, 0x80
	return append(b, payload...)
}

// connectionRequest is the X.224 Connection Request PDU of [MS-RDPBCGR]
// 2.2.1.1, with a routing cookie and an RDP_NEG_REQ.
func connectionRequest(cookie string, protocols uint32) []byte {
	var w writer
	w.bytes([]byte{0x03, 0x00, 0, 0}) // TPKT, length below
	w.u8(0)                           // length indicator, below
	w.u8(0xe0)                        // CR TPDU
	w.u16be(0)                        // DST-REF
	w.u16be(0)                        // SRC-REF
	w.u8(0)                           // class 0
	if cookie != "" {
		w.bytes([]byte("Cookie: mstshash=" + cookie + "\r\n"))
	}
	w.u8(negReq)
	w.u8(0) // flags
	w.u16(8)
	w.u32(protocols)
	binary.BigEndian.PutUint16(w.b[2:], uint16(len(w.b))) // #nosec G115 -- the cookie is short
	w.b[4] = byte(len(w.b) - 5)                           // #nosec G115 -- the cookie is short
	return w.b
}

// parseConnectionConfirm returns the protocol the server selected from an
// X.224 Connection Confirm, the data of a TPKT.
func parseConnectionConfirm(p []byte) (protocol uint32, err error) {
	// LI, CC TPDU, DST-REF, SRC-REF, class.
	if len(p) < 7 || p[1]&0xf0 != 0xd0 {
		return 0, errors.New("rdp: no X.224 connection confirm")
	}
	r := reader{b: p[7:]}
	if r.len() == 0 {
		// A server without negotiation supports standard RDP security
		// only, [MS-RDPBCGR] 3.2.5.3.2.
		return ProtocolRDP, nil
	}
	typ := r.u8()
	r.u8() // flags
	if r.u16() != 8 {
		return 0, errors.New("rdp: bad negotiation response length")
	}
	v := r.u32()
	if r.err != nil {
		return 0, r.err
	}
	switch typ {
	case negRsp:
		return v, nil
	case negFailure:
		return 0, &NegotiationError{Code: v}
	}
	return 0, fmt.Errorf("rdp: unknown negotiation response type %d", typ)
}

// NegotiationError is the RDP_NEG_FAILURE a server answers when it
// supports none of the requested security protocols.
type NegotiationError struct {
	Code uint32
}

func (e *NegotiationError) Error() string {
	reason := map[uint32]string{
		1: "SSL required by server",
		2: "SSL not allowed by server",
		3: "SSL certificate not on server",
		4: "inconsistent flags",
		5: "hybrid (NLA) required by server",
		6: "SSL with user authentication required by server",
	}[e.Code]
	if reason == "" {
		reason = fmt.Sprintf("code %d", e.Code)
	}
	return "rdp: security negotiation failed: " + reason
}
