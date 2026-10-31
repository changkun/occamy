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
	"io"
	"log/slog"
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
	// selects it and through the Client Info PDU in any case. Without
	// NLA, the password goes to whichever server holds a certificate TLS
	// accepts. The client clears the copies it encodes once they are
	// sent, but the Config's strings live as long as the Client.
	Domain, Username, Password string
	// ClientName is the client's name the server sees; "occamy" by
	// default.
	ClientName string
	// RoutingCookie sends the first nine characters of Username as the
	// routing cookie of the connection request ("Cookie: mstshash="), as
	// mstsc and FreeRDP do, for a load balancer that routes by user. It
	// goes in the clear, before TLS, so it is off by default: then the
	// connection request carries only the protocols offered.
	RoutingCookie bool
	// KeyboardLayout is the input locale identifier of the layout; US
	// English (0x409) by default.
	KeyboardLayout uint32
	// Protocols are the security protocols to offer, of ProtocolSSL,
	// ProtocolHybrid and ProtocolHybridEx; NLA, ProtocolHybrid and
	// ProtocolHybridEx, by default. ProtocolSSL, TLS without NLA, logs on
	// in the Client Info PDU after the connection is set up, and is
	// offered only when asked for: xrdp supports nothing else. Standard
	// RDP security is never offered.
	Protocols uint32
	// TLS is the policy for the server's certificate. The VNC backend's
	// VeNCrypt has the same; it has no Require, since an attacker who
	// strips NLA from the offer is noticed by the protocols the server
	// repeats under TLS.
	TLS TLS
	// Logger receives warnings; nil means slog.Default().
	Logger *slog.Logger

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
	tls      tlsPolicy
	addr     string // the server's address, for warnings
	log      *slog.Logger

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
	ctx        context.Context // ends a wait; Background if nil
	dec        decoder
	cursors    [maxPointerCache]cursor
	shown      shownCursor
	frag       []byte
	fragging   bool // a first fragment came, and its last has not
	lastResize time.Time
	tokens     float64   // the pixels the client may decode; below 0, its debt
	paced      time.Time // when tokens was last brought up to date
	rbuf       []byte
	errInfo    ErrorInfo

	// The tests replace these: now, if set, is the time, and sleep, if
	// set, waits.
	now   func() time.Time
	sleep func(time.Duration) error
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
	// decodeRate bounds the pixels of bitmaps the client decodes, a few
	// bytes of which can describe a whole desktop: 32 Mi pixels a second,
	// 42 desktops of 1024×768, 16 of 1920×1080, 4 of 3840×2160 or 1 of
	// the largest. Real content costs about two bytes a pixel as bitmap
	// updates, so a server needs over 500 Mbit/s to reach it. A token
	// bucket holds two desktops and a tile; while it is empty, the client
	// waits, and the server's updates queue behind it.
	decodeRate = 1 << 25
)

// checkDesktop returns an error if a desktop of w×h is empty or exceeds
// the limits.
func checkDesktop(w, h int) error {
	if w <= 0 || h <= 0 || w > maxSide || h > maxSide || w*h > maxPixels {
		return fmt.Errorf("rdp: desktop of %d×%d is empty or exceeds the limits of %d pixels a side and %d in all", w, h, maxSide, maxPixels)
	}
	return nil
}

// Dial connects to the RDP server at addr, a host and port, and runs
// Connect. Unless cfg.TLS.ServerName is set, the server's certificate
// must name the host, or be pinned, or cfg.TLS.InsecureSkipVerify set.
func Dial(ctx context.Context, addr string, cfg Config) (*Client, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("rdp: %w", err)
	}
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName = host
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rdp: %w", err)
	}
	return Connect(ctx, conn, cfg)
}

// Connect runs the RDP connection sequence over conn, which is connected
// to the server's RDP port, up to the client's part of the connection
// finalization. Run then serves the connection. Unless cfg.TLS.ServerName
// is set, the server's certificate must name the host of conn's remote
// address, or be pinned, or cfg.TLS.InsecureSkipVerify set. On an error
// Connect closes conn; if ctx is done first, the error is ctx.Err().
func Connect(ctx context.Context, conn net.Conn, cfg Config) (*Client, error) {
	if cfg.Width == 0 {
		cfg.Width, cfg.Height = 1024, 768
	}
	if cfg.Width < 200 || cfg.Height < 200 || checkDesktop(cfg.Width, cfg.Height) != nil {
		conn.Close()
		return nil, fmt.Errorf("rdp: desktop size %d×%d out of range", cfg.Width, cfg.Height)
	}
	switch cfg.ColorDepth {
	case 0:
		cfg.ColorDepth = 32
	case 15, 16, 24, 32:
	default:
		conn.Close()
		return nil, fmt.Errorf("rdp: colour depth %d", cfg.ColorDepth)
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "occamy"
	}
	if cfg.KeyboardLayout == 0 {
		cfg.KeyboardLayout = 0x409
	}
	if cfg.Protocols == 0 {
		cfg.Protocols = ProtocolHybrid | ProtocolHybridEx
	}
	var addr, host string
	if ra := conn.RemoteAddr(); ra != nil {
		addr = ra.String()
		host, _, _ = net.SplitHostPort(addr) // empty if not IP, as for net.Pipe
	}
	policy, err := newTLSPolicy(cfg.TLS, host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Client{
		tls:    policy,
		addr:   addr,
		log:    log,
		cfg:    cfg,
		conn:   conn,
		t:      newTransport(conn, cfg.Trace),
		width:  cfg.Width,
		height: cfg.Height,
		stats:  Stats{Bitmaps: map[string]int{}, Ignored: map[string]int{}},
		ctx:    ctx,
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	err = c.connect(ctx)
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
	cookie := ""
	if c.cfg.RoutingCookie {
		cookie = c.cfg.Username
	}
	if err := c.t.write(connectionRequest(cookie, c.cfg.Protocols)); err != nil {
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

	tc := tls.Client(c.conn, c.tls.config(c.addr, c.log))
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
	// The server repeats, under TLS, the protocols it was offered in the
	// clear (clientRequestedProtocols of TS_UD_SC_CORE, [MS-RDPBCGR]
	// 2.2.1.4.2): a difference means someone changed the offer to make
	// the server pick a weaker protocol. A server that leaves the field
	// out, as only RDP 4.0 and 5.0 servers may, cannot be checked.
	if !sd.echoed {
		return errors.New("rdp: the server did not say which security protocols it was offered")
	}
	if sd.requestedProtocols != c.cfg.Protocols {
		return fmt.Errorf("rdp: the server was offered security protocols %#x, not the %#x the client sent", sd.requestedProtocols, c.cfg.Protocols)
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
	c.lastResize = c.clock()
	for {
		f, buf, err := c.t.readFrame(c.rbuf)
		c.rbuf = buf
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c.errInfo != 0 && HungUp(err) {
				// The server said why in a Set Error Info PDU, and closed
				// the connection without a Disconnect Provider Ultimatum.
				return c.disconnected()
			}
			return err
		}
		c.mu.Lock()
		c.stats.Bytes += int64(len(buf))
		c.mu.Unlock()
		if f.fastPath {
			c.errInfo = 0 // the session went on after a reason
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

// HungUp reports whether err is the server closing the connection: the
// end of the stream, or the reset a server's kernel sends instead when
// the server closes with the client's input unread.
func HungUp(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, errReset)
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
			c.errInfo = 0 // the session went on after a reason
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
		c.dec.drop()
		out.Resize(caps.width, caps.height)
	}
	return nil
}

// waitResize waits until resizeEvery has passed since the last resize.
func (c *Client) waitResize() error {
	if d := c.lastResize.Add(resizeEvery).Sub(c.clock()); d > 0 {
		if err := c.wait(d); err != nil {
			return err
		}
	}
	c.lastResize = c.clock()
	return nil
}

// clock returns the time.
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// wait waits for d, or until c.ctx ends.
func (c *Client) wait(d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(d)
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pace charges n decoded pixels to the token bucket of decodeRate, and
// waits while the bucket is in debt. The wait follows from the debt, so
// a bitmap larger than the bucket waits its share too.
func (c *Client) pace(n int) error {
	now := c.clock()
	w, h := c.Size()
	burst := float64(2*w*h + (bitmapPad+1)*(bitmapPad+1))
	if c.paced.IsZero() {
		c.tokens = burst
	} else {
		c.tokens = min(burst, c.tokens+now.Sub(c.paced).Seconds()*decodeRate)
	}
	c.paced = now
	c.tokens -= float64(n)
	if c.tokens >= 0 {
		return nil
	}
	return c.wait(time.Duration(-c.tokens / decodeRate * float64(time.Second)))
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
	if typ2 != pdu2SetErrorInfo {
		// A reason holds only for the end that follows it: a server ends
		// a session with Set Error Info, then optionally Deactivate All
		// and a Disconnect Provider Ultimatum, and closes ([MS-RDPBCGR]
		// 1.3.1.4.2). A share data PDU after the reason means the session
		// went on, so a later hang-up is not the end the reason gave.
		c.errInfo = 0
		if out == nil {
			return nil
		}
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
		return c.setCursor(out, nil, image.Point{})
	case fpPtrDefault:
		return c.setCursor(out, defaultCursor.img, defaultCursor.hot)
	case fpPtrPosition:
		r := reader{b: p}
		pt := image.Pt(int(r.u16()), int(r.u16()))
		if r.err != nil {
			return r.err
		}
		return c.moveCursor(out, pt)
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
		sys := r.u32()
		if r.err != nil {
			return r.err
		}
		if sys == 0 { // SYSPTR_NULL
			return c.setCursor(out, nil, image.Point{})
		}
		return c.setCursor(out, defaultCursor.img, defaultCursor.hot)
	case ptrMsgPosition:
		return c.fastPathUpdate(out, fpPtrPosition, p)
	case ptrMsgColor, ptrMsgPointer, ptrMsgLarge:
		return c.pointer(out, p, typ != ptrMsgColor, typ == ptrMsgLarge)
	case ptrMsgCached:
		return c.cachedPointer(out, p)
	}
	return nil
}

// pointer decodes a new pointer shape, caches it and shows it. The shape
// is charged by its size before it is decoded, as a bitmap is.
func (c *Client) pointer(out Output, p []byte, withBpp, large bool) error {
	a, err := parsePointerAttribute(p, withBpp, large)
	if err != nil {
		return err
	}
	if err := c.pace(cursorCost(a.w, a.h)); err != nil {
		return err
	}
	img, err := c.dec.pointerImage(a.w, a.h, a.bpp, a.xorMask, a.andMask)
	if err != nil {
		return err
	}
	c.cursors[a.index] = cursor{img: img, hot: a.hot}
	c.count(func(s *Stats) { s.Pointers++ })
	c.showCursor(out, img, a.hot)
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
	return c.setCursor(out, c.cursors[i].img, c.cursors[i].hot)
}

// bitmapPad is how much wider and taller than its destination, or the
// desktop, a bitmap may be: servers pad bitmaps to whole tiles of 64
// pixels at most, and xrdp not at all.
const bitmapPad = 63

// drawBitmap decodes b and draws it. It decodes only bitmaps that fit
// their destination and the desktop, that show on the desktop and that
// have data, at the pace of decodeRate.
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
	if err := c.pace(b.width * b.height); err != nil {
		return err
	}
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
	// The bitmap is decoded whole before anything is locked, and drawn in
	// strips of whole rows, so that the output's lock is held for at most
	// drawStrip pixels at a time and never while the client reads.
	rows := max(1, drawStrip/r.Dx())
	sx := r.Min.X - b.dest.Min.X
	for y0 := r.Min.Y; y0 < r.Max.Y; y0 += rows {
		drawRows(out, image.Rect(r.Min.X, y0, r.Max.X, min(y0+rows, r.Max.Y)), b, sx, rgba)
	}
	return nil
}

// drawRows copies the strip s of the decoded bitmap b, whose pixels are
// rgba and whose column sx is the strip's first, to out under one lock.
// The Unlock is deferred, so that a panic while drawing, which the
// session recovers from, releases the pixels that viewers wait for.
func drawRows(out Output, s image.Rectangle, b *bitmapData, sx int, rgba []byte) {
	img := out.Lock(s)
	defer out.Unlock(s)
	for y := s.Min.Y; y < s.Max.Y; y++ {
		o := 4 * ((y-b.dest.Min.Y)*b.width + sx)
		copy(img.Pix[img.PixOffset(s.Min.X, y):], rgba[o:o+4*s.Dx()])
	}
}

// drawStrip bounds the pixels drawn under one Lock: 256 KiB of RGBA, the
// VNC backend's strip.
const drawStrip = 64 << 10

// pointerCost is the least that a pointer update, a shape or a move, is
// charged against decodeRate, in pixels: a tile of 64×64. A cached
// pointer is 2 bytes of a PDU, and the output copies its shape, up to
// 384×384 pixels, and shows it to every viewer; a move is 4 bytes. The
// charge bounds them to 8,192 a second at most, and a shape to its
// pixels' share of decodeRate. A new shape is charged by the size it
// declares before it is decoded.
const pointerCost = 64 * 64

// shownCursor is the pointer shape the client last gave its output.
type shownCursor struct {
	img *image.RGBA
	hot image.Point
	set bool
}

// cursorCost is what showing a pointer shape of w×h pixels is charged.
func cursorCost(w, h int) int { return max(pointerCost, w*h) }

// setCursor gives out a pointer shape the client has, a cached, hidden
// or default one, unless it is the one shown, at the pace of decodeRate.
func (c *Client) setCursor(out Output, img *image.RGBA, hot image.Point) error {
	if c.shown.set && c.shown.img == img && c.shown.hot == hot {
		return nil
	}
	n := pointerCost
	if img != nil {
		n = cursorCost(img.Rect.Dx(), img.Rect.Dy())
	}
	if err := c.pace(n); err != nil {
		return err
	}
	c.showCursor(out, img, hot)
	return nil
}

// showCursor gives out a pointer shape that has been charged.
func (c *Client) showCursor(out Output, img *image.RGBA, hot image.Point) {
	c.shown = shownCursor{img: img, hot: hot, set: true}
	out.Cursor(img, hot)
}

// moveCursor reports the server's move of the pointer to out, at the
// pace of decodeRate.
func (c *Client) moveCursor(out Output, p image.Point) error {
	if err := c.pace(pointerCost); err != nil {
		return err
	}
	out.CursorMove(p)
	return nil
}

func (c *Client) count(f func(*Stats)) {
	c.mu.Lock()
	f(&c.stats)
	c.mu.Unlock()
}
