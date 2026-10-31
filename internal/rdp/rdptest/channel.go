// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// Static virtual channels, [MS-RDPBCGR] 2.2.6: the server's chunks, and
// the client's reassembled.

// CHANNEL_PDU_HEADER flags, [MS-RDPBCGR] 2.2.6.1.1.
const (
	ChannelFirst      uint32 = 0x00000001
	ChannelLast       uint32 = 0x00000002
	ChannelCompressed uint32 = 0x00200000
)

// ChunkSize is CHANNEL_CHUNK_LENGTH: the largest chunk a client sends,
// and the size of the chunks SendChannel sends by default.
const ChunkSize = 1600

// firstChannel is the MCS channel of the first static channel a client
// asks for; the others follow it, around User.
const firstChannel = IOChannel + 1

// clientBlocks returns the client data blocks of an MCS Connect Initial,
// [MS-RDPBCGR] 2.2.1.3, by type: the user data keyed "Duca" after a PER
// length.
func clientBlocks(p []byte) (map[uint16][]byte, error) {
	i := bytes.Index(p, []byte("Duca"))
	if i < 0 || i+5 > len(p) {
		return nil, errors.New("rdptest: connect initial without client data")
	}
	p = p[i+4:]
	n := int(p[0])
	p = p[1:]
	if n&0x80 != 0 {
		if len(p) < 1 {
			return nil, errors.New("rdptest: short client data")
		}
		n, p = (n&0x7f)<<8|int(p[0]), p[1:]
	}
	if n > len(p) {
		return nil, errors.New("rdptest: short client data")
	}
	p = p[:n]
	blocks := map[uint16][]byte{}
	for len(p) >= 4 {
		typ, l := binary.LittleEndian.Uint16(p), int(binary.LittleEndian.Uint16(p[2:]))
		if l < 4 || l > len(p) {
			return nil, fmt.Errorf("rdptest: client data block %#04x of %d bytes", typ, l)
		}
		blocks[typ] = p[4:l]
		p = p[l:]
	}
	return blocks, nil
}

// channelNames returns the names of the static channels in a client
// network data block, TS_UD_CS_NET, [MS-RDPBCGR] 2.2.1.3.4.
func channelNames(b []byte) ([]string, error) {
	if len(b) < 4 {
		return nil, errors.New("rdptest: short client network data")
	}
	n := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	if len(b) != 12*n {
		return nil, fmt.Errorf("rdptest: %d bytes for %d channel definitions", len(b), n)
	}
	names := make([]string, n)
	for i := range names {
		name := b[12*i : 12*i+8]
		if j := bytes.IndexByte(name, 0); j >= 0 {
			name = name[:j]
		}
		names[i] = string(name)
	}
	return names, nil
}

// ChannelChunk is a chunk of a message of total bytes on the MCS
// channel id, with flags, as a server sends it: a Send Data Indication
// of a CHANNEL_PDU_HEADER and data.
func ChannelChunk(id uint16, total, flags uint32, data []byte) []byte {
	var b w
	b.u32(total)
	b.u32(flags)
	b.bytes(data)
	return Indication(id, b)
}

// ChannelChunks splits msg into the chunks of at most size bytes that
// send it on the MCS channel id.
func ChannelChunks(id uint16, msg []byte, size int) [][]byte {
	var chunks [][]byte
	for i := 0; ; i += size {
		end := min(i+size, len(msg))
		var flags uint32
		if i == 0 {
			flags |= ChannelFirst
		}
		if end == len(msg) {
			flags |= ChannelLast
		}
		chunks = append(chunks, ChannelChunk(id, uint32(len(msg)), flags, msg[i:end])) // #nosec G115 -- tests send less than 4 GiB
		if end == len(msg) {
			return chunks
		}
	}
}

// SendChannel sends msg on the static channel the client named name, in
// chunks of ChunkSize.
func (c *Conn) SendChannel(name string, msg []byte) error {
	id, ok := c.Channels[name]
	if !ok {
		return fmt.Errorf("rdptest: the client did not ask for channel %q", name)
	}
	return c.Write(ChannelChunks(id, msg, ChunkSize)...)
}

// channelName returns the name of the static channel with MCS channel
// id, or "".
func (c *Conn) channelName(id uint16) string {
	for name, ch := range c.Channels {
		if ch == id {
			return name
		}
	}
	return ""
}

// reassembly is a client's message on a static channel being
// reassembled.
type reassembly struct {
	total  uint32
	data   []byte
	chunks int
}

// channelChunk reassembles a chunk the client sent on the static channel
// name, and returns the message it completes, if any. It fails on a
// chunk larger than ChunkSize and on chunks that do not add up to the
// length their first declared.
func (c *Conn) channelChunk(name string, p []byte) (Message, error) {
	if len(p) < 8 {
		return Message{}, errors.New("rdptest: short channel PDU")
	}
	total, flags, data := binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:]), p[8:]
	if len(data) > ChunkSize {
		return Message{}, fmt.Errorf("rdptest: a chunk of %d bytes on %s", len(data), name)
	}
	if c.chunks == nil {
		c.chunks = map[string]*reassembly{}
	}
	m := c.chunks[name]
	if flags&ChannelFirst != 0 {
		if m != nil {
			return Message{}, fmt.Errorf("rdptest: a first chunk on %s before the last of the message before", name)
		}
		m = &reassembly{total: total}
		c.chunks[name] = m
	}
	if m == nil || m.total != total {
		return Message{}, fmt.Errorf("rdptest: a chunk on %s without its first, or of another message", name)
	}
	m.data = append(m.data, data...)
	m.chunks++
	if uint64(len(m.data)) > uint64(total) {
		return Message{}, fmt.Errorf("rdptest: %d bytes on %s of a message of %d", len(m.data), name, total)
	}
	if flags&ChannelLast == 0 {
		return Message{}, nil
	}
	delete(c.chunks, name)
	if uint64(len(m.data)) != uint64(total) {
		return Message{}, fmt.Errorf("rdptest: %d bytes on %s of a message of %d", len(m.data), name, total)
	}
	return Message{Channel: name, Data: m.data, Chunks: m.chunks}, nil
}

// CLIPRDR message types, [MS-RDPECLIP] 2.2.1.
const (
	CBMonitorReady       uint16 = 0x0001
	CBFormatList         uint16 = 0x0002
	CBFormatListResponse uint16 = 0x0003
	CBFormatDataRequest  uint16 = 0x0004
	CBFormatDataResponse uint16 = 0x0005
	CBClipCaps           uint16 = 0x0007
	CBResponseOK         uint16 = 0x0001
	CBResponseFail       uint16 = 0x0002
)

// The clipboard formats of text.
const (
	CFText        uint32 = 1
	CFUnicodeText uint32 = 13
)

// Cliprdr is a CLIPRDR message: its header's type and flags, and data.
type Cliprdr struct {
	Type, Flags uint16
	Data        []byte
}

// ParseCliprdr parses a CLIPRDR message a client sent, whose length must
// match its header's.
func ParseCliprdr(msg []byte) (Cliprdr, error) {
	if len(msg) < 8 {
		return Cliprdr{}, errors.New("rdptest: short CLIPRDR message")
	}
	n := binary.LittleEndian.Uint32(msg[4:])
	if uint64(n) != uint64(len(msg)-8) {
		return Cliprdr{}, fmt.Errorf("rdptest: CLIPRDR message of %d bytes says %d", len(msg)-8, n)
	}
	return Cliprdr{Type: binary.LittleEndian.Uint16(msg), Flags: binary.LittleEndian.Uint16(msg[2:]), Data: msg[8:]}, nil
}

// CliprdrPDU is a CLIPRDR message of type typ with flags and data.
func CliprdrPDU(typ, flags uint16, data []byte) []byte {
	var b w
	b.u16(typ)
	b.u16(flags)
	b.u32(uint32(len(data))) // #nosec G115 -- tests send less than 4 GiB
	b.bytes(data)
	return b
}

// ClipCaps is the server's Clipboard Capabilities PDU, version 2, with
// long format names if long.
func ClipCaps(long bool) []byte {
	var b w
	b.u16(1)
	b.u16(0)
	b.u16(1) // CB_CAPSTYPE_GENERAL
	b.u16(12)
	b.u32(2)
	var flags uint32
	if long {
		flags = 0x2 // CB_USE_LONG_FORMAT_NAMES
	}
	b.u32(flags)
	return CliprdrPDU(CBClipCaps, 0, b)
}

// LongNames reports whether a client's Clipboard Capabilities PDU's data
// takes long format names.
func LongNames(data []byte) (bool, error) {
	if len(data) != 16 || binary.LittleEndian.Uint16(data) != 1 || binary.LittleEndian.Uint16(data[4:]) != 1 {
		return false, fmt.Errorf("rdptest: clipboard capabilities % x", data)
	}
	return binary.LittleEndian.Uint32(data[12:])&0x2 != 0, nil
}

// MonitorReady is the server's Monitor Ready PDU.
func MonitorReady() []byte { return CliprdrPDU(CBMonitorReady, 0, nil) }

// FormatList is a Format List PDU of formats with empty names, in long
// or short format names.
func FormatList(long bool, formats ...uint32) []byte {
	var b w
	for _, f := range formats {
		b.u32(f)
		if long {
			b.u16(0)
		} else {
			b.bytes(make([]byte, 32))
		}
	}
	return CliprdrPDU(CBFormatList, 0, b)
}

// FormatIDs returns the format IDs of a Format List's data.
func FormatIDs(data []byte, long bool) ([]uint32, error) {
	var ids []uint32
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("rdptest: short format list")
		}
		ids = append(ids, binary.LittleEndian.Uint32(data))
		data = data[4:]
		if !long {
			if len(data) < 32 {
				return nil, errors.New("rdptest: short format list")
			}
			data = data[32:]
			continue
		}
		end := -1
		for i := 0; i+1 < len(data); i += 2 {
			if data[i] == 0 && data[i+1] == 0 {
				end = i + 2
				break
			}
		}
		if end < 0 {
			return nil, errors.New("rdptest: format name without its terminator")
		}
		data = data[end:]
	}
	return ids, nil
}

// FormatDataRequest is a Format Data Request PDU for format.
func FormatDataRequest(format uint32) []byte {
	var b w
	b.u32(format)
	return CliprdrPDU(CBFormatDataRequest, 0, b)
}

// FormatDataResponse is a Format Data Response PDU with data, or a
// failure.
func FormatDataResponse(ok bool, data []byte) []byte {
	if !ok {
		return CliprdrPDU(CBFormatDataResponse, CBResponseFail, nil)
	}
	return CliprdrPDU(CBFormatDataResponse, CBResponseOK, data)
}

// UnicodeText encodes s as CF_UNICODETEXT: UTF-16LE and a NUL.
func UnicodeText(s string) []byte {
	var b w
	for _, u := range utf16.Encode([]rune(s)) {
		b.u16(u)
	}
	b.u16(0)
	return b
}

// FromUnicodeText decodes CF_UNICODETEXT up to its NUL, which it must
// have.
func FromUnicodeText(b []byte) (string, error) {
	if len(b)%2 != 0 {
		return "", errors.New("rdptest: odd CF_UNICODETEXT")
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	for i, c := range u {
		if c == 0 {
			return string(utf16.Decode(u[:i])), nil
		}
	}
	return "", errors.New("rdptest: CF_UNICODETEXT without its NUL")
}
