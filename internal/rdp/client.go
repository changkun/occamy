// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package rdp is a pure-Go RDP client: the connection sequence of
// [MS-RDPBCGR] over TLS, NLA through CredSSP with NTLMv2 ([MS-CSSP],
// [MS-NLMP]), bitmap updates in the interleaved RLE and planar codecs,
// pointer updates, and keyboard and mouse input. It is the protocol half
// of a native RDP backend (specs/008-native-architecture/rdp-strategy.md).
//
// The client asks for bitmap updates only: it advertises no drawing
// orders, caches, bulk compression, bitmap codecs or virtual channels.
// Standard RDP security, the licensing exchange, Kerberos and the GFX
// pipeline are not implemented.
package rdp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a connection. The zero value of a field selects the
// default its comment gives.
type Config struct {
	// Width and Height are the desktop size to ask for; 1024×768 by
	// default. The server may choose another.
	Width, Height int
	// ColorDepth is 15, 16, 24 or 32 bits per pixel; 32 by default.
	ColorDepth int
	// Domain, Username and Password log on, through NLA when the server
	// selects it and through the Client Info PDU in any case.
	Domain, Username, Password string
	// ClientName is the client's name the server sees; "occamy" by
	// default.
	ClientName string
	// KeyboardLayout is the input locale identifier of the layout; US
	// English (0x409) by default.
	KeyboardLayout uint32
	// Protocols are the security protocols to offer, of ProtocolSSL,
	// ProtocolHybrid and ProtocolHybridEx; all three by default. Standard
	// RDP security is never offered.
	Protocols uint32
	// TLSConfig configures TLS. Nil verifies the server's certificate
	// against the system roots for the host the connection is to.
	TLSConfig *tls.Config
	// Trace, if set, receives every frame the client writes and reads
	// outside TLS records and CredSSP, for recording sessions. The slice
	// is borrowed for the call.
	Trace func(Direction, []byte)
}

// Output receives a client's screen. backend.Sink satisfies it.
type Output interface {
	// Resize reports the desktop's size; the server draws all of it after.
	Resize(width, height int)
	// Lock returns the pixels of r, which lies on the desktop, for
	// drawing, and Unlock releases them.
	Lock(r image.Rectangle) *image.RGBA
	Unlock(r image.Rectangle)
	// Cursor sets the pointer's shape; nil hides it.
	Cursor(img *image.RGBA, hot image.Point)
	// CursorMove reports that the server moved the pointer.
	CursorMove(p image.Point)
}

// Stats counts what a client received, by kind.
type Stats struct {
	// Bitmaps counts bitmap rectangles by codec: "planar", "rle8",
	// "rle15", "rle16", "rle24", "raw16", "raw32" and so on.
	Bitmaps map[string]int
	// FastPath and SlowPath count update PDUs by path.
	FastPath, SlowPath int
	// Ignored counts what the client received and did not apply, such as
	// "orders" and "surface commands", which it does not ask for.
	Ignored map[string]int
	// Bytes counts the bytes of all frames read.
	Bytes int64
	// Pointers counts pointer shape updates.
	Pointers int
}

// Client is a connection to an RDP server.
type Client struct {
	cfg      Config
	conn     net.Conn
	t        *transport
	protocol uint32 // the security protocol the server selected

	userID     uint16
	ioChannel  uint16
	msgChannel uint16
	shareID    uint32
	caps       serverCaps

	fastPathInput atomic.Bool

	mu        sync.Mutex // guards the fields below
	width     int
	height    int
	stats     Stats
	finalized bool

	// Owned by Connect, then Run.
	ctx        context.Context // ends a wait for resizeEvery; Background if nil
	dec        decoder
	cursors    [maxPointerCache]cursor
	frag       []byte
	fragging   bool // a first fragment came, and its last has not
	budget     int  // the pixels the current PDU may still decode
	lastResize time.Time
	rbuf       []byte
	errInfo    ErrorInfo
}

// Limits on what the server makes the client do.
const (
	// maxSide and maxPixels bound the desktop, which the server sets in
	// its Demand Active, as they bound the size the client asks for:
	// 8192 pixels a side, the most a client may ask for ([MS-RDPBCGR]
	// 2.2.1.3.2), and 128 MiB of RGBA in all.
	maxSide   = 8192
	maxPixels = 1 << 25
	// resizeEvery is the least time between two resizes. A Demand Active
	// that resizes the desktop sooner waits, so that a server cannot make
	// the session reallocate its screen faster than ten times a second.
	resizeEvery = 100 * time.Millisecond
)

// checkDesktop returns an error if a desktop of w×h is empty or exceeds
// the limits.
func checkDesktop(w, h int) error {
	if w <= 0 || h <= 0 || w > maxSide || h > maxSide || w*h > maxPixels {
		return fmt.Errorf("rdp: desktop of %d×%d is empty or exceeds the limits of %d pixels a side and %d in all", w, h, maxSide, maxPixels)
	}
	return nil
}

// Connect runs the RDP connection sequence over conn, which is connected
// to the server's RDP port, up to the client's part of the connection
// finalization. Run then serves the connection. If ctx is done first,
// Connect closes conn and returns ctx.Err().
func Connect(ctx context.Context, conn net.Conn, cfg Config) (*Client, error) {
	if cfg.Width == 0 {
		cfg.Width, cfg.Height = 1024, 768
	}
	if cfg.Width < 200 || cfg.Height < 200 || checkDesktop(cfg.Width, cfg.Height) != nil {
		return nil, fmt.Errorf("rdp: desktop size %d×%d out of range", cfg.Width, cfg.Height)
	}
	switch cfg.ColorDepth {
	case 0:
		cfg.ColorDepth = 32
	case 15, 16, 24, 32:
	default:
		return nil, fmt.Errorf("rdp: colour depth %d", cfg.ColorDepth)
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "occamy"
	}
	if cfg.KeyboardLayout == 0 {
		cfg.KeyboardLayout = 0x409
	}
	if cfg.Protocols == 0 {
		cfg.Protocols = ProtocolSSL | ProtocolHybrid | ProtocolHybridEx
	}
	c := &Client{
		cfg:    cfg,
		conn:   conn,
		t:      newTransport(conn, cfg.Trace),
		width:  cfg.Width,
		height: cfg.Height,
		stats:  Stats{Bitmaps: map[string]int{}, Ignored: map[string]int{}},
		ctx:    ctx,
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	err := c.connect(ctx)
	if !stop() || ctx.Err() != nil {
		conn.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) connect(ctx context.Context) error {
	// Connection initiation, [MS-RDPBCGR] 1.3.1.1 phase 1.
	if err := c.t.write(connectionRequest(c.cfg.Username, c.cfg.Protocols)); err != nil {
		return err
	}
	f, err := c.read()
	if err != nil {
		return err
	}
	if c.protocol, err = parseConnectionConfirm(f.data); err != nil {
		return err
	}
	if c.protocol&c.cfg.Protocols == 0 || c.protocol&^(ProtocolSSL|ProtocolHybrid|ProtocolHybridEx) != 0 {
		// xrdp selects standard RDP security when asked for CredSSP alone.
		return fmt.Errorf("rdp: the server selected security protocol %#x, which the client did not offer or does not support", c.protocol)
	}

	tcfg := c.cfg.TLSConfig.Clone()
	if tcfg == nil {
		tcfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if tcfg.ServerName == "" && !tcfg.InsecureSkipVerify {
		host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String())
		if err != nil {
			return err
		}
		tcfg.ServerName = host
	}
	tc := tls.Client(c.conn, tcfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("rdp: TLS: %w", err)
	}
	c.t.upgrade(tc)
	if c.protocol&(ProtocolHybrid|ProtocolHybridEx) != 0 {
		certs := tc.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			return errors.New("rdp: TLS server without certificate")
		}
		if err := credSSP(c.t.r, tc, certs[0], c.cfg.Domain, c.cfg.Username, c.cfg.Password); err != nil {
			return err
		}
		if c.protocol&ProtocolHybridEx != 0 {
			if err := readEarlyUserAuth(c.t.r); err != nil {
				return err
			}
		}
	}

	// Basic settings exchange, phase 2.
	if err := c.t.write(tpkt(connectInitial(c.clientData()))); err != nil {
		return err
	}
	if f, err = c.read(); err != nil {
		return err
	}
	data, err := parseConnectResponse(f.data)
	if err != nil {
		return err
	}
	sd, err := parseServerData(data)
	if err != nil {
		return err
	}
	if sd.encryption != 0 {
		return errors.New("rdp: the server wants standard RDP security encryption")
	}
	c.ioChannel, c.msgChannel = sd.ioChannel, sd.msgChannel

	// Channel connection, phase 3.
	if err := c.t.write(tpkt(erectDomainRequest())); err != nil {
		return err
	}
	if err := c.t.write(tpkt(attachUserRequest())); err != nil {
		return err
	}
	if f, err = c.read(); err != nil {
		return err
	}
	if c.userID, err = parseAttachUserConfirm(f.data); err != nil {
		return err
	}
	join := []uint16{c.userID, c.ioChannel}
	if c.msgChannel != 0 {
		join = append(join, c.msgChannel)
	}
	join = append(join, sd.channels...)
	for _, ch := range join {
		if err := c.t.write(tpkt(channelJoinRequest(c.userID, ch))); err != nil {
			return err
		}
		if f, err = c.read(); err != nil {
			return err
		}
		if err := parseChannelJoinConfirm(f.data, ch); err != nil {
			return err
		}
	}

	// Secure settings exchange and licensing, phases 5 and 6. Under TLS
	// there is no security exchange.
	if err := c.sendClientInfo(); err != nil {
		return err
	}
	for licensed := false; !licensed; {
		data, err := c.readChannelData()
		if err != nil {
			return err
		}
		r := reader{b: data}
		flags := r.u16()
		r.u16() // flagsHi
		if r.err != nil {
			return r.err
		}
		if flags&secLicensePkt == 0 {
			// Auto-detect and multitransport requests come here too.
			continue
		}
		if licensed, err = parseLicense(r.b); err != nil {
			return err
		}
	}

	// Capabilities exchange, phase 7. A server that refuses the logon
	// here says why in a Set Error Info PDU before it disconnects.
	for {
		data, err := c.readIOData()
		if err != nil {
			if c.errInfo != 0 {
				return fmt.Errorf("%w (%w)", c.errInfo, err)
			}
			return err
		}
		done, err := c.handleShareControl(nil, data)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// read reads a slow-path frame during the connection sequence.
func (c *Client) read() (frame, error) {
	f, buf, err := c.t.readFrame(c.rbuf)
	c.rbuf = buf
	if err != nil {
		return frame{}, err
	}
	if f.fastPath {
		return frame{}, errors.New("rdp: fast-path PDU during the connection sequence")
	}
	return f, nil
}

// readChannelData reads the data of the next Send Data Indication.
func (c *Client) readChannelData() ([]byte, error) {
	_, data, err := c.readChannel()
	return data, err
}

func (c *Client) readChannel() (uint16, []byte, error) {
	f, err := c.read()
	if err != nil {
		return 0, nil, err
	}
	return parseDomainPDU(f.data)
}

// readIOData reads the data of the next Send Data Indication on the I/O
// channel, skipping the other channels'.
func (c *Client) readIOData() ([]byte, error) {
	for {
		ch, data, err := c.readChannel()
		if err != nil || ch == c.ioChannel {
			return data, err
		}
		c.ignoreChannel(ch)
	}
}

// ignoreChannel counts a PDU on a channel other than the I/O channel: the
// message channel's auto-detect and multitransport requests
// ([MS-RDPBCGR] 2.2.14, 2.2.15), which start with a security header and
// which the client does not answer, or a virtual channel's.
func (c *Client) ignoreChannel(ch uint16) {
	kind := "virtual channel"
	if ch == c.msgChannel {
		kind = "message channel"
	}
	c.count(func(s *Stats) { s.Ignored[kind]++ })
}

// Size returns the desktop's size.
func (c *Client) Size() (width, height int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.width, c.height
}

// Protocol returns the security protocol the server selected.
func (c *Client) Protocol() uint32 { return c.protocol }

// Stats returns what the client received so far.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Bitmaps = make(map[string]int, len(c.stats.Bitmaps))
	for k, v := range c.stats.Bitmaps {
		s.Bitmaps[k] = v
	}
	s.Ignored = make(map[string]int, len(c.stats.Ignored))
	for k, v := range c.stats.Ignored {
		s.Ignored[k] = v
	}
	return s
}

// Finalized reports whether the server completed the connection
// finalization with its Font Map PDU.
func (c *Client) Finalized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finalized
}

// SuppressOutput tells the server to stop or resume sending screen
// updates, [MS-RDPBCGR] 2.2.11.3. On resuming, the server redraws the
// desktop.
func (c *Client) SuppressOutput(suppress bool) error {
	c.mu.Lock()
	body := c.suppressOutput(!suppress)
	c.mu.Unlock()
	return c.sendData(pdu2SuppressOutput, body)
}

// Refresh asks the server to redraw r.
func (c *Client) Refresh(r image.Rectangle) error {
	return c.sendData(pdu2RefreshRect, refreshRect(r))
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Run serves the connection, drawing the server's screen on out, until
// the server disconnects, ctx is done or the connection fails. It returns
// nil when the server ends the session normally, ctx.Err() when ctx is
// done first, and another error otherwise. It closes the connection
// before it returns.
func (c *Client) Run(ctx context.Context, out Output) error {
	defer c.conn.Close()
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stop()
	c.ctx = ctx
	w, h := c.Size()
	out.Resize(w, h)
	c.lastResize = time.Now()
	for {
		f, buf, err := c.t.readFrame(c.rbuf)
		c.rbuf = buf
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		c.mu.Lock()
		c.stats.Bytes += int64(len(buf))
		c.mu.Unlock()
		if f.fastPath {
			err = c.handleFastPath(out, f)
		} else {
			var ch uint16
			var data []byte
			ch, data, err = parseDomainPDU(f.data)
			if errors.Is(err, errDisconnected) {
				return c.disconnected()
			}
			if err == nil {
				if ch == c.ioChannel {
					_, err = c.handleShareControl(out, data)
				} else {
					c.ignoreChannel(ch)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

// disconnected returns Run's result for a server that disconnected.
func (c *Client) disconnected() error {
	switch c.errInfo {
	case 0, 0x0000000b, 0x0000000c: // none, or the user logged off or disconnected
		return nil
	}
	return c.errInfo
}

// handleShareControl handles the share control PDUs in data, the data of
// a Send Data Indication on the I/O channel. out is nil during the
// connection sequence, which ends with the client's finalization PDUs:
// done reports that. A server sends a Demand Active in a PDU of its own,
// and a second in the same PDU is refused, so that a few bytes cannot
// make the client reallocate its desktop again and again.
func (c *Client) handleShareControl(out Output, data []byte) (done bool, err error) {
	c.budget = c.pixelBudget()
	for len(data) >= 2 {
		r := reader{b: data}
		total := int(r.u16())
		if total == 0x8000 {
			// A flow control PDU, [MS-RDPBCGR] 2.2.8.1.1.1.1 note: 8 bytes.
			data = data[min(len(data), 8):]
			continue
		}
		typ := int(r.u16() & 0xf)
		r.u16() // pduSource
		if r.err != nil || total < 6 || total > len(data) {
			return false, fmt.Errorf("rdp: share control PDU of length %d in %d bytes", total, len(data))
		}
		body := data[6:total]
		data = data[total:]
		switch typ {
		case pduDemandActive:
			if done {
				return false, errors.New("rdp: a second Demand Active in one PDU")
			}
			if err := c.activate(out, body); err != nil {
				return false, err
			}
			done = true
		case pduDeactivateAll:
			// A Demand Active follows, [MS-RDPBCGR] 1.3.1.3.
		case pduData:
			if err := c.handleShareData(out, body); err != nil {
				return false, err
			}
		case pduServerRedir:
			return false, errors.New("rdp: server redirection is not implemented")
		}
	}
	return done, nil
}

// activate answers a Demand Active, [MS-RDPBCGR] 1.3.1.1 phases 7 to 10,
// both in the connection sequence and in a deactivation-reactivation
// sequence.
func (c *Client) activate(out Output, body []byte) error {
	shareID, caps, err := parseDemandActive(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.shareID, c.caps = shareID, caps
	resized := c.width != caps.width || c.height != caps.height
	c.width, c.height = caps.width, caps.height
	c.finalized = false
	c.mu.Unlock()
	c.fastPathInput.Store(caps.inputFlags&(inputFlagFastPathInput|inputFlagFastPathInput2) != 0)
	if err := c.sendIO(c.confirmActive()); err != nil {
		return err
	}
	if err := c.finalize(); err != nil {
		return err
	}
	if out != nil && resized {
		if err := c.waitResize(); err != nil {
			return err
		}
		out.Resize(caps.width, caps.height)
	}
	return nil
}

// waitResize waits until resizeEvery has passed since the last resize.
func (c *Client) waitResize() error {
	if wait := time.Until(c.lastResize.Add(resizeEvery)); wait > 0 {
		ctx := c.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	c.lastResize = time.Now()
	return nil
}

// handleShareData handles a share data PDU after its share control
// header. During the connection sequence, when out is nil, it takes only
// the reason a server gives before it disconnects.
func (c *Client) handleShareData(out Output, p []byte) error {
	r := reader{b: p}
	r.u32() // shareId
	r.u8()  // pad1
	r.u8()  // streamId
	r.u16() // uncompressedLength
	typ2 := r.u8()
	compressed := r.u8()
	r.u16() // compressedLength
	if r.err != nil {
		return fmt.Errorf("rdp: share data header: %w", r.err)
	}
	if compressed&0x20 != 0 {
		return errors.New("rdp: compressed PDU, though the client offered no compression")
	}
	body := r.b
	if out == nil && typ2 != pdu2SetErrorInfo {
		return nil
	}
	switch typ2 {
	case pdu2Update:
		c.count(func(s *Stats) { s.SlowPath++ })
		u := reader{b: body}
		typ := u.u16()
		if u.err != nil {
			return u.err
		}
		return c.update(out, int(typ), u.b)
	case pdu2Pointer:
		u := reader{b: body}
		typ := u.u16()
		u.u16() // pad2Octets
		if u.err != nil {
			return u.err
		}
		return c.slowPathPointer(out, int(typ), u.b)
	case pdu2SetErrorInfo:
		u := reader{b: body}
		c.errInfo = ErrorInfo(u.u32())
		return u.err
	case pdu2FontMap:
		c.mu.Lock()
		c.finalized = true
		c.mu.Unlock()
	}
	return nil
}

// Update types of slow-path updates, [MS-RDPBCGR] 2.2.9.1.1.3.1.
const (
	updateOrders  = 0x0
	updateBitmap  = 0x1
	updatePalette = 0x2
	updateSync    = 0x3
)

// update applies a slow-path update, or a fast-path update of the same
// form, after its updateType.
func (c *Client) update(out Output, typ int, p []byte) error {
	switch typ {
	case updateBitmap:
		return parseBitmapUpdate(p, func(b *bitmapData) error { return c.drawBitmap(out, b) })
	case updatePalette:
		return c.dec.parsePalette(p)
	case updateOrders:
		c.count(func(s *Stats) { s.Ignored["orders"]++ })
	}
	return nil
}

// Fast-path update codes, [MS-RDPBCGR] 2.2.9.1.2.1.
const (
	fpOrders       = 0x0
	fpBitmap       = 0x1
	fpPalette      = 0x2
	fpSync         = 0x3
	fpSurfCmds     = 0x4
	fpPtrNull      = 0x5
	fpPtrDefault   = 0x6
	fpPtrPosition  = 0x8
	fpColorPointer = 0x9
	fpCachedPtr    = 0xa
	fpPointer      = 0xb
	fpLargePointer = 0xc
)

// Fast-path fragmentation, [MS-RDPBCGR] 2.2.9.1.2.1.
const (
	fragSingle = 0x0
	fragLast   = 0x1
	fragFirst  = 0x2
	fragNext   = 0x3
)

// maxReassembly bounds a fragmented fast-path update, and the client
// advertises it as the largest it accepts (capMultifragment).
const maxReassembly = 32 << 20

// handleFastPath applies the updates of a fast-path update PDU,
// [MS-RDPBCGR] 2.2.9.1.2.
func (c *Client) handleFastPath(out Output, f frame) error {
	if f.hdr&0xc0 != 0 {
		return errors.New("rdp: encrypted fast-path PDU under TLS")
	}
	c.budget = c.pixelBudget()
	r := reader{b: f.data}
	for r.len() > 0 {
		hdr := r.u8()
		code, frag, comp := int(hdr&0xf), int(hdr>>4&0x3), hdr>>6&0x3
		if comp&0x2 != 0 && r.u8()&0x20 != 0 {
			return errors.New("rdp: compressed fast-path update, though the client offered no compression")
		}
		p := r.take(int(r.u16()))
		if r.err != nil {
			return fmt.Errorf("rdp: fast-path update: %w", r.err)
		}
		c.count(func(s *Stats) { s.FastPath++ })
		switch frag {
		case fragSingle:
		case fragFirst:
			c.frag = append(c.frag[:0], p...)
			c.fragging = true
			continue
		case fragNext, fragLast:
			if !c.fragging {
				// A fragment without its first, which a server that
				// follows [MS-RDPBCGR] 2.2.9.1.2.1 does not send: dropped,
				// not added to the last update.
				c.count(func(s *Stats) { s.Ignored["orphan fragments"]++ })
				continue
			}
			if len(c.frag)+len(p) > maxReassembly {
				return errors.New("rdp: fragmented fast-path update too large")
			}
			c.frag = append(c.frag, p...)
			if frag == fragNext {
				continue
			}
			c.fragging = false
			p = c.frag
		}
		if err := c.fastPathUpdate(out, code, p); err != nil {
			return err
		}
		if frag == fragLast && cap(c.frag) > retainFrag {
			c.frag = nil
		}
	}
	return nil
}

// retainFrag bounds the reassembly buffer the client keeps between
// fragmented updates.
const retainFrag = 1 << 20

func (c *Client) fastPathUpdate(out Output, code int, p []byte) error {
	switch code {
	case fpBitmap, fpPalette:
		// TS_UPDATE_BITMAP_DATA and TS_UPDATE_PALETTE_DATA start with
		// their updateType.
		if len(p) < 2 {
			return errShort
		}
		typ := updateBitmap
		if code == fpPalette {
			typ = updatePalette
		}
		return c.update(out, typ, p[2:])
	case fpOrders:
		c.count(func(s *Stats) { s.Ignored["orders"]++ })
	case fpSurfCmds:
		c.count(func(s *Stats) { s.Ignored["surface commands"]++ })
	case fpPtrNull:
		out.Cursor(nil, image.Point{})
	case fpPtrDefault:
		out.Cursor(defaultCursor.img, defaultCursor.hot)
	case fpPtrPosition:
		r := reader{b: p}
		pt := image.Pt(int(r.u16()), int(r.u16()))
		if r.err != nil {
			return r.err
		}
		out.CursorMove(pt)
	case fpColorPointer, fpPointer, fpLargePointer:
		return c.pointer(out, p, code != fpColorPointer, code == fpLargePointer)
	case fpCachedPtr:
		return c.cachedPointer(out, p)
	}
	return nil
}

// Slow-path pointer message types, [MS-RDPBCGR] 2.2.9.1.1.4.
const (
	ptrMsgSystem   = 0x0001
	ptrMsgPosition = 0x0003
	ptrMsgColor    = 0x0006
	ptrMsgCached   = 0x0007
	ptrMsgPointer  = 0x0008
	ptrMsgLarge    = 0x0009
)

func (c *Client) slowPathPointer(out Output, typ int, p []byte) error {
	switch typ {
	case ptrMsgSystem:
		r := reader{b: p}
		if r.u32() == 0 { // SYSPTR_NULL
			out.Cursor(nil, image.Point{})
		} else {
			out.Cursor(defaultCursor.img, defaultCursor.hot)
		}
		return r.err
	case ptrMsgPosition:
		return c.fastPathUpdate(out, fpPtrPosition, p)
	case ptrMsgColor, ptrMsgPointer, ptrMsgLarge:
		return c.pointer(out, p, typ != ptrMsgColor, typ == ptrMsgLarge)
	case ptrMsgCached:
		return c.cachedPointer(out, p)
	}
	return nil
}

func (c *Client) pointer(out Output, p []byte, withBpp, large bool) error {
	i, cur, err := c.dec.parsePointerAttribute(p, withBpp, large)
	if err != nil {
		return err
	}
	c.cursors[i] = cur
	c.count(func(s *Stats) { s.Pointers++ })
	out.Cursor(cur.img, cur.hot)
	return nil
}

func (c *Client) cachedPointer(out Output, p []byte) error {
	r := reader{b: p}
	i := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if i >= maxPointerCache || c.cursors[i].img == nil {
		return fmt.Errorf("rdp: cached pointer %d not in the cache", i)
	}
	out.Cursor(c.cursors[i].img, c.cursors[i].hot)
	return nil
}

// bitmapPad is how much wider and taller than its destination, or the
// desktop, a bitmap may be: servers pad bitmaps to whole tiles of 64
// pixels at most, and xrdp not at all.
const bitmapPad = 63

// pixelBudget is the number of pixels the bitmaps of one PDU may decode
// in all: twice the desktop, since a server draws each pixel of an
// update once. A few bytes of compressed data describe a large bitmap,
// so without it a 64 KiB PDU could cost seconds of CPU.
func (c *Client) pixelBudget() int {
	w, h := c.Size()
	return 2*w*h + (bitmapPad+1)*(bitmapPad+1)
}

// drawBitmap decodes b and draws it. It decodes only bitmaps that fit
// their destination and the desktop, that show on the desktop and that
// have data, within the PDU's budget of pixels.
func (c *Client) drawBitmap(out Output, b *bitmapData) error {
	w, h := c.Size()
	if b.width <= 0 || b.height <= 0 ||
		b.width > min(b.dest.Dx(), w)+bitmapPad || b.height > min(b.dest.Dy(), h)+bitmapPad {
		return fmt.Errorf("rdp: bitmap of %d×%d for %v on a desktop of %d×%d", b.width, b.height, b.dest, w, h)
	}
	r := b.dest.Intersect(image.Rectangle{Min: b.dest.Min, Max: b.dest.Min.Add(image.Pt(b.width, b.height))})
	r = r.Intersect(image.Rect(0, 0, w, h))
	if r.Empty() || len(b.data) == 0 {
		c.count(func(s *Stats) { s.Ignored["empty bitmaps"]++ })
		return nil
	}
	n := b.width * b.height
	if n > c.budget {
		return fmt.Errorf("rdp: an update decodes more than %d pixels", c.pixelBudget())
	}
	c.budget -= n
	rgba, err := c.dec.decode(b)
	if err != nil {
		return err
	}
	kind := fmt.Sprintf("raw%d", b.bpp)
	switch {
	case b.compressed && b.bpp == 32:
		kind = "planar"
	case b.compressed:
		kind = fmt.Sprintf("rle%d", b.bpp)
	}
	c.count(func(s *Stats) { s.Bitmaps[kind]++ })
	img := out.Lock(r)
	sx := r.Min.X - b.dest.Min.X
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := 4 * ((y-b.dest.Min.Y)*b.width + sx)
		copy(img.Pix[img.PixOffset(r.Min.X, y):], rgba[o:o+4*r.Dx()])
	}
	out.Unlock(r)
	return nil
}

func (c *Client) count(f func(*Stats)) {
	c.mu.Lock()
	f(&c.stats)
	c.mu.Unlock()
}
