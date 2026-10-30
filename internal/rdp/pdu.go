// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
	"image"
)

// Share control PDU types, [MS-RDPBCGR] 2.2.8.1.1.1.1.
const (
	pduDemandActive  = 0x1
	pduConfirmActive = 0x3
	pduDeactivateAll = 0x6
	pduData          = 0x7
	pduServerRedir   = 0xa
)

// Share data PDU types, [MS-RDPBCGR] 2.2.8.1.1.1.2.
const (
	pdu2Update         = 0x02
	pdu2Control        = 0x14
	pdu2Pointer        = 0x1b
	pdu2Input          = 0x1c
	pdu2Synchronize    = 0x1f
	pdu2RefreshRect    = 0x21
	pdu2SuppressOutput = 0x23
	pdu2ShutdownDenied = 0x25
	pdu2SaveSessInfo   = 0x26
	pdu2FontList       = 0x27
	pdu2FontMap        = 0x28
	pdu2SetErrorInfo   = 0x2f
)

// Control actions, [MS-RDPBCGR] 2.2.1.15.1.
const (
	ctrlRequestControl = 0x0001
	ctrlGrantedControl = 0x0002
	ctrlCooperate      = 0x0004
)

// Security header flags, [MS-RDPBCGR] 2.2.8.1.1.2.1.
const (
	secInfoPkt    = 0x0040
	secLicensePkt = 0x0080
)

// shareControl wraps a PDU in a share control header, [MS-RDPBCGR]
// 2.2.8.1.1.1.1.
func shareControl(typ int, source uint16, body []byte) []byte {
	var w writer
	w.u16(6 + len(body))
	w.u16(typ | 0x10) // version 1
	w.u16(int(source))
	w.bytes(body)
	return w.b
}

// shareData wraps a PDU in share control and share data headers,
// [MS-RDPBCGR] 2.2.8.1.1.1.2.
func (c *Client) shareData(typ2 int, body []byte) []byte {
	c.mu.Lock()
	shareID := c.shareID
	c.mu.Unlock()
	var w writer
	w.u32(shareID)
	w.u8(0)              // pad1
	w.u8(1)              // streamId: STREAM_LOW
	w.u16(len(body) + 4) // uncompressedLength, as IronRDP counts it
	w.u8(typ2)
	w.u8(0)  // compressedType
	w.u16(0) // compressedLength
	w.bytes(body)
	return shareControl(pduData, c.userID, w.b)
}

// sendIO sends a slow-path PDU on the I/O channel.
func (c *Client) sendIO(pdu []byte) error {
	return c.t.write(tpkt(sendDataRequest(c.userID, c.ioChannel, pdu)))
}

// sendData sends a share data PDU.
func (c *Client) sendData(typ2 int, body []byte) error {
	return c.sendIO(c.shareData(typ2, body))
}

// sendClientInfo sends the Client Info PDU, then clears the copies of
// the password it made. A trace sees the PDU as it would be without the
// password, so that neither the password nor its length reaches it.
func (c *Client) sendClientInfo() error {
	pdu := c.clientInfo(c.cfg.Password)
	data := sendDataRequest(c.userID, c.ioChannel, pdu)
	frame := tpkt(data)
	defer func() {
		clear(pdu)
		clear(data)
		clear(frame)
	}()
	var traced []byte
	if c.t.trace != nil {
		traced = tpkt(sendDataRequest(c.userID, c.ioChannel, c.clientInfo("")))
	}
	return c.t.writeTraced(frame, traced)
}

// clientInfo is the Client Info PDU of [MS-RDPBCGR] 2.2.1.11, with the
// credentials for automatic logon: the configuration's, with password.
func (c *Client) clientInfo(password string) []byte {
	const (
		infoMouse              = 0x00000001
		infoDisableCtrlAltDel  = 0x00000002
		infoAutoLogon          = 0x00000008
		infoUnicode            = 0x00000010
		infoMaximizeShell      = 0x00000020
		infoLogonNotify        = 0x00000040
		infoEnableWindowsKey   = 0x00000100
		infoLogonErrors        = 0x00010000
		infoNoAudioPlayback    = 0x00080000
		perfDisableWallpaper   = 0x00000001
		perfDisableFullWindow  = 0x00000002
		perfDisableMenuAnims   = 0x00000004
		perfDisableTheming     = 0x00000008
		perfDisableCursorShdw  = 0x00000020
		perfDisableCursorBlink = 0x00000040
	)
	flags := uint32(infoMouse | infoDisableCtrlAltDel | infoUnicode | infoMaximizeShell |
		infoLogonNotify | infoEnableWindowsKey | infoLogonErrors | infoNoAudioPlayback)
	if c.cfg.Password != "" {
		flags |= infoAutoLogon
	}
	domain, user, pass := utf16le(c.cfg.Domain), utf16le(c.cfg.Username), utf16le(password)
	defer clear(pass)

	var w writer
	w.u16(secInfoPkt) // basic security header, [MS-RDPBCGR] 2.2.8.1.1.2.1
	w.u16(0)
	w.u32(0) // CodePage
	w.u32(flags)
	w.u16(len(domain))
	w.u16(len(user))
	w.u16(len(pass))
	w.u16(0) // cbAlternateShell
	w.u16(0) // cbWorkingDir
	for _, s := range [][]byte{domain, user, pass, nil, nil} {
		w.bytes(s)
		w.u16(0)
	}
	// TS_EXTENDED_INFO_PACKET, [MS-RDPBCGR] 2.2.1.11.1.1.1.
	addr := utf16le("0.0.0.0")
	w.u16(0x0002) // clientAddressFamily: AF_INET
	w.u16(len(addr) + 2)
	w.bytes(addr)
	w.u16(0)
	dir := utf16le("occamy")
	w.u16(len(dir) + 2)
	w.bytes(dir)
	w.u16(0)
	w.zero(172) // clientTimeZone: UTC
	w.u32(0)    // clientSessionId
	w.u32(perfDisableWallpaper | perfDisableFullWindow | perfDisableMenuAnims |
		perfDisableTheming | perfDisableCursorShdw | perfDisableCursorBlink)
	w.u16(0) // cbAutoReconnectCookie
	return w.b
}

// Licensing, [MS-RDPELE] and [MS-RDPBCGR] 2.2.1.12.
const (
	licenseErrorAlert   = 0xff
	statusValidClient   = 0x00000007
	licenseRequest      = 0x01
	licenseNewLicense   = 0x03
	licenseUpgrade      = 0x04
	licensePlatformChal = 0x02
)

// parseLicense handles a licensing PDU after its security header. It
// returns done when licensing is over. Only the server's shortcut, a
// License Error PDU with STATUS_VALID_CLIENT, is implemented; a server
// that runs the full licensing exchange is refused.
func parseLicense(p []byte) (done bool, err error) {
	r := reader{b: p}
	typ := r.u8()
	r.u8()  // flags
	r.u16() // wMsgSize
	switch typ {
	case licenseErrorAlert:
		code := r.u32()
		r.u32() // dwStateTransition
		if r.err != nil {
			return false, r.err
		}
		if code != statusValidClient {
			return false, fmt.Errorf("rdp: licensing error %#x", code)
		}
		return true, nil
	case licenseNewLicense, licenseUpgrade:
		return true, nil
	case licenseRequest, licensePlatformChal:
		return false, errors.New("rdp: the server requires the licensing exchange, which is not implemented")
	}
	return false, fmt.Errorf("rdp: unknown licensing message %#02x", typ)
}

// confirmActive is the Confirm Active PDU of [MS-RDPBCGR] 2.2.1.13.2.
func (c *Client) confirmActive() []byte {
	n, caps := c.capabilities()
	src := []byte("MSTSC\x00")
	var w writer
	w.u32(c.shareID)
	w.u16(serverChannelID) // originatorId
	w.u16(len(src))
	w.u16(len(caps) + 4)
	w.bytes(src)
	w.u16(n)
	w.u16(0) // pad2Octets
	w.bytes(caps)
	return shareControl(pduConfirmActive, c.userID, w.b)
}

// finalize sends the client's connection finalization PDUs,
// [MS-RDPBCGR] 1.3.1.1 phase 10.
func (c *Client) finalize() error {
	var w writer
	w.u16(1) // messageType: SYNCMSGTYPE_SYNC
	w.u16(serverChannelID)
	if err := c.sendData(pdu2Synchronize, w.b); err != nil {
		return err
	}
	for _, action := range []int{ctrlCooperate, ctrlRequestControl} {
		w = writer{}
		w.u16(action)
		w.u16(0) // grantId
		w.u32(0) // controlId
		if err := c.sendData(pdu2Control, w.b); err != nil {
			return err
		}
	}
	w = writer{}
	w.u16(0)      // numberFonts
	w.u16(0)      // totalNumFonts
	w.u16(0x0003) // listFlags: FONTLIST_FIRST | FONTLIST_LAST
	w.u16(0x0032) // entrySize
	return c.sendData(pdu2FontList, w.b)
}

// suppressOutput is the Suppress Output PDU of [MS-RDPBCGR] 2.2.11.3.1.
func (c *Client) suppressOutput(allow bool) []byte {
	var w writer
	if !allow {
		w.u8(0)
		w.zero(3)
		return w.b
	}
	w.u8(1)
	w.zero(3)
	w.u16(0)
	w.u16(0)
	w.u16(c.width - 1)
	w.u16(c.height - 1)
	return w.b
}

// refreshRect is the Refresh Rect PDU of [MS-RDPBCGR] 2.2.11.2.1 for one
// area.
func refreshRect(r image.Rectangle) []byte {
	var w writer
	w.u8(1)
	w.zero(3)
	w.u16(r.Min.X)
	w.u16(r.Min.Y)
	w.u16(r.Max.X - 1)
	w.u16(r.Max.Y - 1)
	return w.b
}

// ErrorInfo is the reason the server gave for ending the session, from
// its Set Error Info PDU, [MS-RDPBCGR] 2.2.5.1.1.
type ErrorInfo uint32

func (e ErrorInfo) Error() string {
	reasons := map[ErrorInfo]string{
		0x00000001: "disconnected by an administrative tool",
		0x00000002: "logged off by an administrative tool",
		0x00000003: "idle timeout",
		0x00000004: "logon timeout",
		0x00000005: "replaced by another connection",
		0x00000006: "out of memory",
		0x00000007: "server denied the connection",
		0x00000009: "insufficient privileges",
		0x0000000a: "fresh credentials required",
		0x0000000b: "disconnected by the user",
		0x0000000c: "logged off by the user",
	}
	if s, ok := reasons[e]; ok {
		return "rdp: " + s
	}
	return fmt.Sprintf("rdp: server error info %#08x", uint32(e))
}
