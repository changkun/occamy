// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Dynamic virtual channels ([MS-RDPEDYC]) on the static channel
// "drdynvc", and the Display Control channel ([MS-RDPEDISP]) on them: the
// server's PDUs, and a parser for the client's.

// DRDYNVC commands, the high nibble of a PDU's first byte.
const (
	DVCCreate          = 0x01
	DVCDataFirst       = 0x02
	DVCData            = 0x03
	DVCClose           = 0x04
	DVCCaps            = 0x05
	DVCDataFirstZ      = 0x06
	DVCDataZ           = 0x07
	DVCSoftSyncRequest = 0x08
)

// DisplayControl is the Display Control channel's name.
const DisplayControl = "Microsoft::Windows::RDS::DisplayControl"

// dvcSize returns the size code of the smallest field that holds v.
func dvcSize(v uint32) int {
	switch {
	case v <= 0xff:
		return 0
	case v <= 0xffff:
		return 1
	}
	return 2
}

// appendSized appends v in a field of size code size: 1, 2 or 4 bytes.
func appendSized(p []byte, size int, v uint32) []byte {
	switch size {
	case 0:
		return append(p, byte(v)) // #nosec G115 -- of size code 0
	case 1:
		return binary.LittleEndian.AppendUint16(p, uint16(v)) // #nosec G115 -- of size code 1
	}
	return binary.LittleEndian.AppendUint32(p, v)
}

// DVCPDU is a DRDYNVC PDU of cmd for the channel id, its middle bits sp,
// and body after the id.
func DVCPDU(cmd, sp int, id uint32, body []byte) []byte {
	size := dvcSize(id)
	p := []byte{byte(cmd<<4 | sp<<2 | size)} // #nosec G115 -- tests keep the fields in range
	p = appendSized(p, size, id)
	return append(p, body...)
}

// DVCCapsPDU is DYNVC_CAPS_VERSION1, 2 or 3 of version.
func DVCCapsPDU(version int) []byte {
	p := []byte{DVCCaps << 4, 0}
	p = binary.LittleEndian.AppendUint16(p, uint16(version)) // #nosec G115 -- tests keep it in range
	if version >= 2 {
		p = append(p, make([]byte, 8)...) // the priority charges
	}
	return p
}

// DVCCreateRequest is DYNVC_CREATE_REQ for the channel id named name.
func DVCCreateRequest(id uint32, name string) []byte {
	return DVCPDU(DVCCreate, 0, id, append([]byte(name), 0))
}

// DVCDataPDU is DYNVC_DATA of data on the channel id.
func DVCDataPDU(id uint32, data []byte) []byte {
	return DVCPDU(DVCData, 0, id, data)
}

// DVCDataFirstPDU is DYNVC_DATA_FIRST of data on the channel id, of a
// message of total bytes.
func DVCDataFirstPDU(id uint32, total uint32, data []byte) []byte {
	size := dvcSize(total)
	return DVCPDU(DVCDataFirst, size, id, append(appendSized(nil, size, total), data...))
}

// DVCMessage is msg on the channel id as DRDYNVC PDUs whose data is at
// most size bytes: one Data PDU if it fits, or a Data First and Data
// PDUs.
func DVCMessage(id uint32, msg []byte, size int) [][]byte {
	if len(msg) <= size {
		return [][]byte{DVCDataPDU(id, msg)}
	}
	pdus := [][]byte{DVCDataFirstPDU(id, uint32(len(msg)), msg[:size])} // #nosec G115 -- tests send less than 4 GiB
	for i := size; i < len(msg); i += size {
		pdus = append(pdus, DVCDataPDU(id, msg[i:min(i+size, len(msg))]))
	}
	return pdus
}

// DVCClosePDU is DYNVC_CLOSE of the channel id.
func DVCClosePDU(id uint32) []byte { return DVCPDU(DVCClose, 0, id, nil) }

// DVC is a DRDYNVC PDU a client sent.
type DVC struct {
	Cmd, Sp int
	// Channel is the channel's id; Version a capabilities response's
	// version; Status a create response's status; Data the data of a
	// Data PDU.
	Channel uint32
	Version int
	Status  uint32
	Data    []byte
}

// ParseDVC parses a DRDYNVC PDU a client sent: a capabilities response,
// a create response, a close, or a Data PDU, which is all a client that
// opens no channel of its own sends.
func ParseDVC(p []byte) (DVC, error) {
	if len(p) < 1 {
		return DVC{}, errors.New("rdptest: empty DRDYNVC PDU")
	}
	d := DVC{Cmd: int(p[0] >> 4), Sp: int(p[0] >> 2 & 3)}
	size := int(p[0] & 3)
	p = p[1:]
	if d.Cmd == DVCCaps {
		if len(p) != 3 {
			return DVC{}, fmt.Errorf("rdptest: DRDYNVC capabilities response of %d bytes", len(p))
		}
		d.Version = int(binary.LittleEndian.Uint16(p[1:]))
		return d, nil
	}
	n := []int{1, 2, 4, 4}[size]
	if len(p) < n {
		return DVC{}, errors.New("rdptest: short DRDYNVC PDU")
	}
	switch n {
	case 1:
		d.Channel = uint32(p[0])
	case 2:
		d.Channel = uint32(binary.LittleEndian.Uint16(p))
	default:
		d.Channel = binary.LittleEndian.Uint32(p)
	}
	p = p[n:]
	switch d.Cmd {
	case DVCCreate:
		if len(p) != 4 {
			return DVC{}, fmt.Errorf("rdptest: DRDYNVC create response of %d bytes", len(p))
		}
		d.Status = binary.LittleEndian.Uint32(p)
	case DVCClose:
		if len(p) != 0 {
			return DVC{}, errors.New("rdptest: DRDYNVC close with data")
		}
	case DVCData:
		d.Data = p
	default:
		return DVC{}, fmt.Errorf("rdptest: DRDYNVC command %d from a client", d.Cmd)
	}
	return d, nil
}

// DisplayCaps is DISPLAYCONTROL_CAPS_PDU ([MS-RDPEDISP] 2.2.2.1).
func DisplayCaps(maxMonitors, factorA, factorB uint32) []byte {
	var p []byte
	for _, v := range []uint32{5, 20, maxMonitors, factorA, factorB} {
		p = binary.LittleEndian.AppendUint32(p, v)
	}
	return p
}

// Monitor is a DISPLAYCONTROL_MONITOR_LAYOUT.
type Monitor struct {
	Flags                                 uint32
	Left, Top                             int32
	Width, Height                         uint32
	PhysicalWidth, PhysicalHeight         uint32
	Orientation                           uint32
	DesktopScaleFactor, DeviceScaleFactor uint32
}

// ParseMonitorLayout parses a DISPLAYCONTROL_MONITOR_LAYOUT_PDU
// ([MS-RDPEDISP] 2.2.2.2) and checks its header.
func ParseMonitorLayout(p []byte) ([]Monitor, error) {
	if len(p) < 16 {
		return nil, errors.New("rdptest: short monitor layout")
	}
	typ, n := binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:])
	size, count := binary.LittleEndian.Uint32(p[8:]), binary.LittleEndian.Uint32(p[12:])
	if typ != 2 || int64(n) != int64(len(p)) || size != 40 || int64(len(p)-16) != int64(count)*40 {
		return nil, fmt.Errorf("rdptest: monitor layout of type %d, %d bytes saying %d, %d monitors of %d bytes", typ, len(p), n, count, size)
	}
	var ms []Monitor
	for p = p[16:]; len(p) > 0; p = p[40:] {
		var m Monitor
		if err := binary.Read(bytes.NewReader(p[:40]), binary.LittleEndian, &m); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, nil
}

// SendDVC sends DRDYNVC PDUs to the client on its drdynvc channel.
func (c *Conn) SendDVC(pdus ...[]byte) error {
	for _, p := range pdus {
		if err := c.SendChannel("drdynvc", p); err != nil {
			return err
		}
	}
	return nil
}
