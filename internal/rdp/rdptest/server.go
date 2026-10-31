// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// ErrCredSSP is returned by Accept when the server selected NLA, after
// it read the client's first TSRequest: CredSSP is not implemented.
var ErrCredSSP = errors.New("rdptest: the client started CredSSP, which the fake server does not implement")

// ErrStandardSecurity is returned by Accept for a Server.Standard, after
// it selected standard RDP security, which the fake server does not
// implement either.
var ErrStandardSecurity = errors.New("rdptest: the server selected standard RDP security")

// Certificate returns the servers' certificate, a self-signed ECDSA
// certificate for the name "rdp.test", and its SHA-256 fingerprint as
// openssl prints it, which a client's TLS pins take.
func Certificate() (tls.Certificate, string, error) {
	c, err := certificate()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(c.Certificate[0])
	hex := make([]string, len(sum))
	for i, b := range sum {
		hex[i] = fmt.Sprintf("%02X", b)
	}
	return c, strings.Join(hex, ":"), nil
}

var certificate = sync.OnceValues(func() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rdp.test"},
		DNSNames:     []string{"rdp.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
})

// Server is the server's side of RDP connections. The zero value
// selects TLS without NLA and gives the client the desktop it asks for.
type Server struct {
	// Protocol is the security protocol the server selects: ProtocolSSL
	// if zero.
	Protocol uint32
	// Width and Height are the desktop's size in the Demand Active; the
	// size the client asked for if zero.
	Width, Height int
	// InputFlags are the server's input capabilities; DefaultInputFlags
	// if zero.
	InputFlags uint16
	// ChannelIDs, if not nil, are the MCS channels the server lists for
	// the client's static channels, instead of one for each.
	ChannelIDs []uint16

	// Account, if set, makes a server that selects NLA run CredSSP
	// (credssp.go): it takes this account's NTLMv2 logon, binds its
	// certificate's public key, takes the credentials the client
	// delegates, and goes on with the connection sequence. Without it,
	// a server that selects NLA stops at the client's first TSRequest.
	Account *Account
	// CredSSPVersion is the TSRequest version the server speaks; 6 if
	// zero.
	CredSSPVersion int
	// NTLMFlags, if not zero, are the flags of the server's NTLM
	// challenge instead of those the client asked for.
	NTLMFlags uint32
	// WrongProof makes the server prove a public key other than its
	// certificate's, as a man in the middle would.
	WrongProof bool
	// StallNLA makes the server stop answering after the client's first
	// TSRequest (1) or its second, with the NTLM authenticate (2), until
	// the client hangs up.
	StallNLA int
	// Standard makes the server select standard RDP security, as xrdp
	// does when offered CredSSP alone; Accept then returns
	// ErrStandardSecurity.
	Standard bool
}

// Credentials are the logon fields of a Client Info PDU.
type Credentials struct {
	Domain, Username, Password string
	// AutoLogon is INFO_AUTOLOGON: the client sent a password.
	AutoLogon bool
}

// Conn is a connection after the connection sequence.
type Conn struct {
	// Cookie is the routing cookie of the client's connection request.
	Cookie string
	// Requested are the security protocols the client offered.
	Requested uint32
	// Size is the desktop size the client asked for.
	Size image.Point
	// EarlyFlags is the earlyCapabilityFlags of the client's core data.
	EarlyFlags uint16
	// TSRequest is the client's first TSRequest, when the server
	// selected NLA.
	TSRequest []byte
	// Authenticates counts the NTLM AUTHENTICATE_MESSAGEs the client
	// sent in CredSSP, and Delegated are the credentials it delegated.
	Authenticates int
	Delegated     Credentials
	// Info is what the client's Client Info PDU carried.
	Info Credentials
	// Channels are the static channels the client named, with the MCS
	// channels the server gave them.
	Channels map[string]uint16
	// Joined are the MCS channels the client joined, in order.
	Joined []uint16

	conn   net.Conn
	tls    *tls.Conn
	r      *bufio.Reader
	wmu    sync.Mutex
	chunks map[string]*reassembly // owned by Next
}

// Accept runs the server's side of the connection sequence over conn,
// up to the Font Map PDU that ends it. It returns ErrCredSSP, and the
// connection with TSRequest set, when the server selects NLA.
func (s *Server) Accept(conn net.Conn) (*Conn, error) {
	c := &Conn{conn: conn, r: bufio.NewReader(conn)}
	f, err := readFrame(c.r)
	if err != nil {
		return nil, fmt.Errorf("connection request: %w", err)
	}
	if err := c.parseConnectionRequest(f.data); err != nil {
		return nil, err
	}
	protocol := s.Protocol
	if protocol == 0 {
		protocol = ProtocolSSL
	}
	if s.Standard {
		protocol = 0
	}
	if _, err := conn.Write(ConnectionConfirm(protocol)); err != nil {
		return nil, err
	}
	if s.Standard {
		return nil, ErrStandardSecurity
	}
	cert, _, err := Certificate()
	if err != nil {
		return nil, err
	}
	if c.r.Buffered() > 0 {
		return nil, errors.New("rdptest: the client sent data before the TLS handshake")
	}
	c.tls = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := c.tls.Handshake(); err != nil {
		return nil, fmt.Errorf("rdptest: TLS: %w", err)
	}
	c.r = bufio.NewReader(c.tls)
	if protocol&(ProtocolHybrid|ProtocolHybridEx) != 0 {
		if s.Account == nil {
			c.TSRequest, err = readTSRequest(c.r)
			if err != nil {
				return c, err
			}
			return c, ErrCredSSP
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, err
		}
		if err := s.credSSP(c, leaf); err != nil {
			return c, err
		}
		if protocol&ProtocolHybridEx != 0 {
			if err := c.Write(earlyUserAuthResult()); err != nil {
				return nil, err
			}
		}
	}

	// Basic settings exchange and channel connection.
	if f, err = readFrame(c.r); err != nil {
		return nil, fmt.Errorf("connect initial: %w", err)
	}
	c.Size = clientDesktop(f.data)
	ids, err := c.channels(f.data, s.ChannelIDs)
	if err != nil {
		return nil, err
	}
	if err := c.Write(ConnectResponseChannels(c.Requested, ids)); err != nil {
		return nil, err
	}
	for range 2 { // Erect Domain Request, Attach User Request
		if _, err := readFrame(c.r); err != nil {
			return nil, err
		}
	}
	if err := c.Write(AttachUserConfirm()); err != nil {
		return nil, err
	}
	// Channel Join Requests, as many as the client sends, until the
	// Client Info PDU.
	for {
		if f, err = readFrame(c.r); err != nil {
			return nil, err
		}
		if len(f.data) < 1 || f.data[0]&0xfc != 14<<2 {
			break
		}
		if len(f.data) < 5 {
			return nil, errors.New("rdptest: short channel join request")
		}
		ch := binary.BigEndian.Uint16(f.data[3:])
		c.Joined = append(c.Joined, ch)
		if err := c.Write(ChannelJoinConfirm(ch)); err != nil {
			return nil, err
		}
	}

	// Client Info, licensing and capabilities.
	_, data, err := parseRequest(f.data)
	if err != nil {
		return nil, err
	}
	if c.Info, err = parseClientInfo(data); err != nil {
		return nil, err
	}
	width, height := s.Width, s.Height
	if width == 0 {
		width, height = c.Size.X, c.Size.Y
	}
	flags := s.InputFlags
	if flags == 0 {
		flags = DefaultInputFlags
	}
	if err := c.Write(License(), DemandActive(width, height, flags)); err != nil {
		return nil, err
	}
	// Confirm Active, Synchronize, two Controls and Font List.
	for {
		m, err := c.Next()
		if err != nil {
			return nil, fmt.Errorf("rdptest: before the Font List: %w", err)
		}
		if m.Type2 == PDU2FontList {
			break
		}
	}
	if err := c.Write(Finalization()...); err != nil {
		return nil, err
	}
	return c, nil
}

// parseConnectionRequest reads the routing cookie and the offered
// protocols of an X.224 Connection Request, [MS-RDPBCGR] 2.2.1.1.
func (c *Conn) parseConnectionRequest(p []byte) error {
	if len(p) < 7+8 || p[1] != 0xe0 {
		return errors.New("rdptest: no X.224 connection request")
	}
	rest := p[7:]
	if cookie, ok := bytes.CutPrefix(rest, []byte("Cookie: mstshash=")); ok {
		i := bytes.Index(cookie, []byte("\r\n"))
		if i < 0 {
			return errors.New("rdptest: routing cookie without CR LF")
		}
		c.Cookie, rest = string(cookie[:i]), cookie[i+2:]
	}
	if len(rest) != 8 || rest[0] != 0x01 {
		return errors.New("rdptest: no RDP_NEG_REQ")
	}
	c.Requested = binary.LittleEndian.Uint32(rest[4:])
	return nil
}

// channels records the static channels the client names in its MCS
// Connect Initial p, with the MCS channels the server gives them: ids if
// not nil, else one for each, from the one after the I/O channel on,
// around User.
func (c *Conn) channels(p []byte, ids []uint16) ([]uint16, error) {
	blocks, err := clientBlocks(p)
	if err != nil {
		return nil, err
	}
	names, err := channelNames(blocks[0xc003])
	if err != nil {
		return nil, err
	}
	// TS_UD_CS_CORE's earlyCapabilityFlags, 140 bytes after its header.
	if core := blocks[0xc001]; len(core) >= 142 {
		c.EarlyFlags = binary.LittleEndian.Uint16(core[140:])
	}
	if ids == nil {
		ch := firstChannel
		for range names {
			if ch == User {
				ch++
			}
			ids = append(ids, ch)
			ch++
		}
	}
	c.Channels = map[string]uint16{}
	for i, name := range names {
		if i < len(ids) && ids[i] != 0 {
			c.Channels[name] = ids[i]
		}
	}
	return ids, nil
}

// clientDesktop returns the desktop size in the client core data of an
// MCS Connect Initial, or zero.
func clientDesktop(p []byte) image.Point {
	// TS_UD_CS_CORE: type 0xc001, its length, version 0x00080004, then
	// the size.
	for i := 0; i+12 <= len(p); i++ {
		b := p[i:]
		if binary.LittleEndian.Uint16(b) == 0xc001 && binary.LittleEndian.Uint32(b[4:]) == 0x00080004 {
			return image.Pt(int(binary.LittleEndian.Uint16(b[8:])), int(binary.LittleEndian.Uint16(b[10:])))
		}
	}
	return image.Point{}
}

// Write writes server frames.
func (c *Conn) Write(frames ...[]byte) error {
	var b []byte
	for _, f := range frames {
		b = append(b, f...)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var w io.Writer = c.conn
	if c.tls != nil {
		w = c.tls
	}
	_, err := w.Write(b)
	return err
}

// SetReadDeadline bounds the wait of Next.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close closes the connection.
func (c *Conn) Close() error { return c.conn.Close() }

// Reset closes the connection with a TCP reset, as a server's kernel
// does when the server closes with input unread.
func (c *Conn) Reset() error {
	if tc, ok := c.conn.(*net.TCPConn); ok {
		if err := tc.SetLinger(0); err != nil {
			return err
		}
	}
	return c.conn.Close()
}

// EventKind is the kind of an input event.
type EventKind int

// The kinds of input events, [MS-RDPBCGR] 2.2.8.1.1.3.1.1.
const (
	Scancode EventKind = iota + 1
	Unicode
	Mouse
	MouseX
	Sync
)

// Event is an input event a client sent, by fast-path or slow-path.
type Event struct {
	Kind EventKind
	// Code is a scancode or a UTF-16 code unit.
	Code uint16
	// Release, Extended and Extended1 are a key's flags.
	Release, Extended, Extended1 bool
	// Flags are a pointer event's flags, or a Sync's toggles.
	Flags uint16
	X, Y  uint16
}

// Message is what a client sent: input events, or another share data
// PDU.
type Message struct {
	// Events are the events of fast-path input or of a slow-path Input
	// Event PDU.
	Events []Event
	// FastPath reports fast-path input.
	FastPath bool
	// Type2 is a slow-path share data PDU's type, 0 for one of other
	// PDUs, and Body its data after the share data header.
	Type2 uint8
	Body  []byte
	// Channel is the name of the static channel of a message on one,
	// Data the message reassembled, and Chunks the number of its chunks.
	Channel string
	Data    []byte
	Chunks  int
}

// Empty reports whether m holds nothing a client sent whole: neither
// input, a share data PDU, nor a channel message.
func (m Message) Empty() bool {
	return len(m.Events) == 0 && m.Type2 == 0 && m.Channel == ""
}

// Next reads what the client sends next. A chunk of a message on a
// static channel other than its last is an empty Message.
func (c *Conn) Next() (Message, error) {
	f, err := readFrame(c.r)
	if err != nil {
		return Message{}, err
	}
	if f.fastPath {
		events, err := parseFastPathInput(f.hdr, f.data)
		return Message{Events: events, FastPath: true}, err
	}
	ch, data, err := parseRequest(f.data)
	if err != nil {
		return Message{}, err
	}
	if name := c.channelName(ch); name != "" {
		return c.channelChunk(name, data)
	}
	if ch != IOChannel || len(data) < 6 {
		return Message{}, nil
	}
	typ := binary.LittleEndian.Uint16(data[2:]) & 0xf
	if typ != pduData {
		return Message{}, nil
	}
	if len(data) < 6+12 {
		return Message{}, errors.New("rdptest: short share data PDU")
	}
	m := Message{Type2: data[6+8], Body: data[6+12:]}
	if m.Type2 == PDU2Input {
		m.Events, err = parseSlowPathInput(m.Body)
	}
	return m, err
}

// SuppressOutput reads a Suppress Output PDU's body: whether it allows
// display updates.
func SuppressOutput(body []byte) (allow bool, err error) {
	if len(body) < 1 {
		return false, errors.New("rdptest: short Suppress Output PDU")
	}
	return body[0] == 1, nil
}

// frame is a client frame.
type frame struct {
	fastPath bool
	hdr      byte
	data     []byte // after the TPKT and X.224 headers, or the fast-path length
}

// readFrame reads a TPKT or a fast-path PDU.
func readFrame(r *bufio.Reader) (frame, error) {
	var h [3]byte
	if _, err := io.ReadFull(r, h[:2]); err != nil {
		return frame{}, err
	}
	if h[0] == 0x03 {
		if _, err := io.ReadFull(r, h[2:3]); err != nil {
			return frame{}, err
		}
		lo, err := r.ReadByte()
		if err != nil {
			return frame{}, err
		}
		n := int(h[2])<<8 | int(lo)
		if n < 7 {
			return frame{}, fmt.Errorf("rdptest: TPKT of %d bytes", n)
		}
		b := make([]byte, n-4)
		if _, err := io.ReadFull(r, b); err != nil {
			return frame{}, err
		}
		if b[1] == 0xf0 { // X.224 Data
			return frame{data: b[3:]}, nil
		}
		return frame{data: b}, nil // the connection request keeps its TPDU
	}
	f := frame{fastPath: true, hdr: h[0]}
	n, hdrLen := int(h[1]), 2
	if n&0x80 != 0 {
		if _, err := io.ReadFull(r, h[2:3]); err != nil {
			return frame{}, err
		}
		n, hdrLen = (n&0x7f)<<8|int(h[2]), 3
	}
	if n < hdrLen {
		return frame{}, fmt.Errorf("rdptest: fast-path PDU of %d bytes", n)
	}
	f.data = make([]byte, n-hdrLen)
	_, err := io.ReadFull(r, f.data)
	return f, err
}

// parseRequest returns the channel and data of an MCS Send Data Request.
func parseRequest(p []byte) (uint16, []byte, error) {
	if len(p) < 7 || p[0]&0xfc != 25<<2 {
		return 0, nil, fmt.Errorf("rdptest: not a Send Data Request: % x", p[:min(len(p), 8)])
	}
	ch := binary.BigEndian.Uint16(p[3:])
	n, rest := int(p[6]), p[7:]
	if n&0x80 != 0 {
		if len(rest) < 1 {
			return 0, nil, errors.New("rdptest: short Send Data Request")
		}
		n, rest = (n&0x7f)<<8|int(rest[0]), rest[1:]
	}
	if n > len(rest) {
		return 0, nil, errors.New("rdptest: short Send Data Request")
	}
	return ch, rest[:n], nil
}

// parseClientInfo reads the credentials of a Client Info PDU,
// [MS-RDPBCGR] 2.2.1.11.
func parseClientInfo(p []byte) (Credentials, error) {
	if len(p) < 4+18 || binary.LittleEndian.Uint16(p)&0x0040 == 0 {
		return Credentials{}, errors.New("rdptest: no Client Info PDU")
	}
	p = p[4:]
	flags := binary.LittleEndian.Uint32(p[4:])
	lens := []int{int(binary.LittleEndian.Uint16(p[8:])), int(binary.LittleEndian.Uint16(p[10:])), int(binary.LittleEndian.Uint16(p[12:]))}
	p = p[18:]
	var fields [3]string
	for i, n := range lens {
		if len(p) < n+2 {
			return Credentials{}, errors.New("rdptest: short Client Info PDU")
		}
		u := make([]uint16, n/2)
		for j := range u {
			u[j] = binary.LittleEndian.Uint16(p[2*j:])
		}
		fields[i], p = string(utf16.Decode(u)), p[n+2:]
	}
	return Credentials{Domain: fields[0], Username: fields[1], Password: fields[2], AutoLogon: flags&0x8 != 0}, nil
}

// parseFastPathInput reads a fast-path input PDU, [MS-RDPBCGR]
// 2.2.8.1.2, after its length.
func parseFastPathInput(hdr byte, p []byte) ([]Event, error) {
	n := int(hdr >> 2 & 0xf)
	if n == 0 {
		if len(p) < 1 {
			return nil, errors.New("rdptest: fast-path input without its count")
		}
		n, p = int(p[0]), p[1:]
	}
	short := errors.New("rdptest: short fast-path input")
	events := make([]Event, 0, n)
	for range n {
		if len(p) < 1 {
			return nil, short
		}
		h := p[0]
		code, flags := h>>5, h&0x1f
		p = p[1:]
		var e Event
		switch code {
		case 0x0: // scancode
			if len(p) < 1 {
				return nil, short
			}
			e = Event{Kind: Scancode, Code: uint16(p[0]), Release: flags&1 != 0, Extended: flags&2 != 0, Extended1: flags&4 != 0}
			p = p[1:]
		case 0x1, 0x2: // mouse, extended mouse
			if len(p) < 6 {
				return nil, short
			}
			e = Event{Kind: Mouse, Flags: binary.LittleEndian.Uint16(p), X: binary.LittleEndian.Uint16(p[2:]), Y: binary.LittleEndian.Uint16(p[4:])}
			if code == 0x2 {
				e.Kind = MouseX
			}
			p = p[6:]
		case 0x3: // synchronize
			e = Event{Kind: Sync, Flags: uint16(flags)}
		case 0x4: // unicode
			if len(p) < 2 {
				return nil, short
			}
			e = Event{Kind: Unicode, Code: binary.LittleEndian.Uint16(p), Release: flags&1 != 0}
			p = p[2:]
		default:
			return nil, fmt.Errorf("rdptest: fast-path input event %d", code)
		}
		events = append(events, e)
	}
	if len(p) != 0 {
		return nil, fmt.Errorf("rdptest: %d bytes after %d fast-path input events", len(p), n)
	}
	return events, nil
}

// parseSlowPathInput reads an Input Event PDU's body, [MS-RDPBCGR]
// 2.2.8.1.1.3.
func parseSlowPathInput(p []byte) ([]Event, error) {
	if len(p) < 4 {
		return nil, errors.New("rdptest: short Input Event PDU")
	}
	n := int(binary.LittleEndian.Uint16(p))
	p = p[4:]
	if len(p) != 12*n {
		return nil, fmt.Errorf("rdptest: %d bytes for %d input events", len(p), n)
	}
	events := make([]Event, n)
	for i := range events {
		ev := p[12*i+4 : 12*i+12]
		typ := binary.LittleEndian.Uint16(ev)
		a, b, c := binary.LittleEndian.Uint16(ev[2:]), binary.LittleEndian.Uint16(ev[4:]), binary.LittleEndian.Uint16(ev[6:])
		switch typ {
		case 0x0004:
			events[i] = Event{Kind: Scancode, Code: b, Release: a&0x8000 != 0, Extended: a&0x0100 != 0, Extended1: a&0x0200 != 0}
		case 0x0005:
			events[i] = Event{Kind: Unicode, Code: b, Release: a&0x8000 != 0}
		case 0x8001, 0x8002:
			events[i] = Event{Kind: Mouse, Flags: a, X: b, Y: c}
			if typ == 0x8002 {
				events[i].Kind = MouseX
			}
		case 0x0000:
			events[i] = Event{Kind: Sync, Flags: b}
		default:
			return nil, fmt.Errorf("rdptest: input event type %#04x", typ)
		}
	}
	return events, nil
}

// readTSRequest reads a TSRequest, [MS-CSSP] 2.2.1: a DER SEQUENCE.
func readTSRequest(r *bufio.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if h[0] != 0x30 {
		return nil, fmt.Errorf("rdptest: not a TSRequest: %#02x", h[0])
	}
	b := h[:]
	n := int(h[1])
	if n&0x80 != 0 {
		k := n & 0x7f
		if k == 0 || k > 2 {
			return nil, errors.New("rdptest: TSRequest length")
		}
		ext := make([]byte, k)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		b = append(b, ext...)
		n = 0
		for _, x := range ext {
			n = n<<8 | int(x)
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return append(b, body...), nil
}
