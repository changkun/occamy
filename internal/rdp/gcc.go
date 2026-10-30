// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
)

// User data block types, [MS-RDPBCGR] 2.2.1.3.1.
const (
	csCore     = 0xc001
	csSecurity = 0xc002
	csNet      = 0xc003
	csCluster  = 0xc004
	scCore     = 0x0c01
	scSecurity = 0x0c02
	scNet      = 0x0c03
	scMsgChan  = 0x0c04
)

// Colour depth fields of TS_UD_CS_CORE, [MS-RDPBCGR] 2.2.1.3.2.
const (
	rnsUDColor8BPP = 0xca01

	rnsUD24BPPSupport = 0x0001
	rnsUD16BPPSupport = 0x0002
	rnsUD15BPPSupport = 0x0004
	rnsUD32BPPSupport = 0x0008

	rnsUDCSSupportErrInfoPDU = 0x0001
	rnsUDCSWant32BPPSession  = 0x0002
)

// clientData encodes the client data blocks of the Connect Initial.
func (c *Client) clientData() []byte {
	var w writer

	// TS_UD_CS_CORE, [MS-RDPBCGR] 2.2.1.3.2.
	start := len(w.b)
	w.u16(csCore)
	w.u16(0) // length, below
	w.u32(0x00080004)
	w.u16(c.cfg.Width)
	w.u16(c.cfg.Height)
	w.u16(rnsUDColor8BPP) // colorDepth, superseded below
	w.u16(0xaa03)         // SASSequence: RNS_UD_SAS_DEL
	w.u32(c.cfg.KeyboardLayout)
	w.u32(2600) // clientBuild
	w.bytes(fixedUTF16(c.cfg.ClientName, 32))
	w.u32(4)  // keyboardType: IBM enhanced (101- or 102-key)
	w.u32(0)  // keyboardSubType
	w.u32(12) // keyboardFunctionKey
	w.zero(64)
	w.u16(rnsUDColor8BPP) // postBeta2ColorDepth, superseded below
	w.u16(1)              // clientProductId
	w.u32(0)              // serialNumber
	high, early := c.cfg.ColorDepth, rnsUDCSSupportErrInfoPDU
	if high == 32 {
		high, early = 24, early|rnsUDCSWant32BPPSession
	}
	w.u16(high)
	w.u16(rnsUD24BPPSupport | rnsUD16BPPSupport | rnsUD15BPPSupport | rnsUD32BPPSupport)
	w.u16(early)
	w.zero(64) // clientDigProductId
	w.u8(0)    // connectionType
	w.u8(0)    // pad1octet
	w.u32(c.protocol)
	w.putU16(start+2, len(w.b)-start)

	// TS_UD_CS_SEC, [MS-RDPBCGR] 2.2.1.3.3. Under TLS no encryption method
	// is negotiated.
	w.u16(csSecurity)
	w.u16(12)
	w.u32(0) // encryptionMethods
	w.u32(0) // extEncryptionMethods

	// TS_UD_CS_NET, [MS-RDPBCGR] 2.2.1.3.4: no static virtual channels.
	w.u16(csNet)
	w.u16(8)
	w.u32(0)

	// TS_UD_CS_CLUSTER, [MS-RDPBCGR] 2.2.1.3.5: redirection supported,
	// version 4, no session to return to.
	w.u16(csCluster)
	w.u16(12)
	w.u32(0x0d)
	w.u32(0)
	return w.b
}

// serverData is what the server data blocks tell the client.
type serverData struct {
	version            uint32
	requestedProtocols uint32 // the protocols the server was offered
	echoed             bool   // the server sent requestedProtocols
	ioChannel          uint16
	channels           []uint16
	msgChannel         uint16
	encryption         uint32 // encryptionMethod of TS_UD_SC_SEC1
	encryptionLvl      uint32
	earlyCapsFlags     uint32
}

// parseServerData parses the server data blocks of the Connect Response,
// [MS-RDPBCGR] 2.2.1.4.
func parseServerData(p []byte) (*serverData, error) {
	sd := &serverData{}
	r := reader{b: p}
	for r.len() >= 4 && r.err == nil {
		typ := r.u16()
		n := int(r.u16())
		if n < 4 {
			return nil, fmt.Errorf("rdp: server data block %#04x of length %d", typ, n)
		}
		b := reader{b: r.take(n - 4)}
		switch typ {
		case scCore: // [MS-RDPBCGR] 2.2.1.4.2; xrdp ends it after the protocols
			sd.version = b.u32()
			if b.len() >= 4 {
				sd.requestedProtocols, sd.echoed = b.u32(), true
			}
			if b.len() >= 4 {
				sd.earlyCapsFlags = b.u32()
			}
		case scSecurity:
			sd.encryption = b.u32()
			sd.encryptionLvl = b.u32()
		case scNet:
			sd.ioChannel = b.u16()
			count := int(b.u16())
			for range count {
				sd.channels = append(sd.channels, b.u16())
			}
		case scMsgChan:
			sd.msgChannel = b.u16()
		}
		if b.err != nil {
			return nil, fmt.Errorf("rdp: server data block %#04x: %w", typ, b.err)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if sd.ioChannel == 0 {
		return nil, errors.New("rdp: server data without network block")
	}
	return sd, nil
}
