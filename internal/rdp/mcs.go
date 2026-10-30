// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"errors"
	"fmt"
)

// MCS ([T.125]) and GCC ([T.124]) as RDP uses them: the Connect-Initial
// and Connect-Response in BER, the conference create PDUs inside them in
// ALIGNED PER, and the domain PDUs in ALIGNED PER.

// The MCS channel of the server, which the share control PDUs name as
// their source, [MS-RDPBCGR] 2.2.1.13.2.1 (originatorId).
const serverChannelID = 1002

// mcsBaseUserID is subtracted from user IDs in PER, [T.125] 7
// (UserId ::= DynamicChannelId (1001..65535)).
const mcsBaseUserID = 1001

// DomainMCSPDU choices, [T.125] 11, shifted into the top six bits.
const (
	mcsErectDomainRequest   = 1 << 2
	mcsDisconnectUltimatum  = 8 << 2
	mcsAttachUserRequest    = 10 << 2
	mcsAttachUserConfirm    = 11 << 2
	mcsChannelJoinRequest   = 14 << 2
	mcsChannelJoinConfirm   = 15 << 2
	mcsSendDataRequest      = 25 << 2
	mcsSendDataIndication   = 26 << 2
	mcsDomainPDUChoiceMask  = 0xfc
	berConnectInitialTag    = 101
	berConnectResponseTag   = 102
	berTagInteger           = 0x02
	berTagOctetString       = 0x04
	berTagBoolean           = 0x01
	berTagEnumerated        = 0x0a
	berTagSequence          = 0x30
	berApplicationConstruct = 0x7f
)

// berLen appends a BER length.
func berLen(b []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(b, byte(n))
	case n < 0x100:
		return append(b, 0x81, byte(n))
	}
	return append(b, 0x82, byte(n>>8), byte(n)) // #nosec G115 -- MCS PDUs stay below 64 KiB
}

// berTLV appends a BER element.
func berTLV(b []byte, tag byte, v []byte) []byte {
	b = append(b, tag)
	b = berLen(b, len(v))
	return append(b, v...)
}

// berInt encodes a non-negative INTEGER.
func berInt(v int) []byte {
	switch {
	case v < 0x80:
		return berTLV(nil, berTagInteger, []byte{byte(v)})
	case v < 0x8000:
		return berTLV(nil, berTagInteger, []byte{byte(v >> 8), byte(v)}) // #nosec G115 -- in range
	}
	return berTLV(nil, berTagInteger, []byte{byte(v >> 16), byte(v >> 8), byte(v)}) // #nosec G115 -- MCS parameters fit in 24 bits
}

// domainParameters encodes [T.125] DomainParameters.
func domainParameters(maxChannels, maxUsers, maxTokens, maxPDU int) []byte {
	var v []byte
	for _, n := range []int{maxChannels, maxUsers, maxTokens, 1, 0, 1, maxPDU, 2} {
		v = append(v, berInt(n)...)
	}
	return berTLV(nil, berTagSequence, v)
}

// connectInitial is the MCS Connect Initial PDU with GCC Conference Create
// Request of [MS-RDPBCGR] 2.2.1.3, carrying the client data blocks.
func connectInitial(userData []byte) []byte {
	var v []byte
	v = berTLV(v, berTagOctetString, []byte{1}) // callingDomainSelector
	v = berTLV(v, berTagOctetString, []byte{1}) // calledDomainSelector
	v = berTLV(v, berTagBoolean, []byte{0xff})  // upwardFlag
	// The parameters mstsc and FreeRDP send.
	v = append(v, domainParameters(34, 2, 0, 0xffff)...)
	v = append(v, domainParameters(1, 1, 1, 0x420)...)
	v = append(v, domainParameters(0xffff, 0xfc17, 0xffff, 0xffff)...)
	v = berTLV(v, berTagOctetString, conferenceCreateRequest(userData))
	b := []byte{berApplicationConstruct, berConnectInitialTag}
	b = berLen(b, len(v))
	return append(b, v...)
}

// perLen appends an ALIGNED PER length determinant.
func perLen(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	return append(b, 0x80|byte(n>>8), byte(n)) // #nosec G115 -- lengths stay below 16 KiB
}

// conferenceCreateRequest is the GCC ConnectData wrapping a
// ConferenceCreateRequest, [MS-RDPBCGR] 2.2.1.3.1 and its example in
// 4.1.3: the T.124 object identifier, a conference named "1", and the
// client data as user data keyed "Duca".
func conferenceCreateRequest(userData []byte) []byte {
	var ccr []byte
	ccr = append(ccr, 0x00)       // ConnectGCCPDU choice: conferenceCreateRequest
	ccr = append(ccr, 0x08)       // optional fields: userData present
	ccr = append(ccr, 0x00, 0x10) // conferenceName: numeric string "1"
	ccr = append(ccr, 0x00)       // padding
	ccr = append(ccr, 0x01)       // one user data set
	ccr = append(ccr, 0xc0)       // key choice: h221NonStandard
	ccr = append(ccr, 0x00)       // octet string length 4 - 4
	ccr = append(ccr, "Duca"...)
	ccr = perLen(ccr, len(userData))
	ccr = append(ccr, userData...)

	b := []byte{
		0x00,                               // ConnectData key choice: object
		0x05, 0x00, 0x14, 0x7c, 0x00, 0x01, // t124Identifier 0.0.20.124.0.1
	}
	b = perLen(b, len(ccr))
	return append(b, ccr...)
}

// berReadHeader reads a BER identifier and length, and returns the
// contents.
func berRead(r *reader, tag byte) []byte {
	if r.u8() != tag {
		if r.err == nil {
			r.err = fmt.Errorf("rdp: BER tag is not %#02x", tag)
		}
		return nil
	}
	return r.take(berReadLen(r))
}

func berReadLen(r *reader) int {
	n := int(r.u8())
	if n&0x80 == 0 {
		return n
	}
	k := n & 0x7f
	if k == 0 || k > 2 {
		if r.err == nil {
			r.err = errors.New("rdp: unsupported BER length")
		}
		return 0
	}
	n = 0
	for range k {
		n = n<<8 | int(r.u8())
	}
	return n
}

// parseConnectResponse returns the server data blocks of an MCS Connect
// Response PDU with GCC Conference Create Response, [MS-RDPBCGR] 2.2.1.4.
func parseConnectResponse(p []byte) ([]byte, error) {
	r := reader{b: p}
	if r.u8() != berApplicationConstruct || r.u8() != berConnectResponseTag {
		return nil, errors.New("rdp: no MCS connect response")
	}
	r = reader{b: r.take(berReadLen(&r))}
	result := berRead(&r, berTagEnumerated)
	berRead(&r, berTagInteger)  // calledConnectId
	berRead(&r, berTagSequence) // domainParameters
	gcc := berRead(&r, berTagOctetString)
	if r.err != nil {
		return nil, r.err
	}
	if len(result) != 1 || result[0] != 0 {
		return nil, fmt.Errorf("rdp: MCS connect refused: result %v", result)
	}
	// The ConferenceCreateResponse ends with the user data set keyed
	// "McDn", the server data, after a PER length.
	i := bytes.Index(gcc, []byte("McDn"))
	if i < 0 {
		return nil, errors.New("rdp: GCC response without server data")
	}
	r = reader{b: gcc[i+4:]}
	n := int(r.u8())
	if n&0x80 != 0 {
		n = (n&0x7f)<<8 | int(r.u8())
	}
	data := r.take(n)
	if r.err != nil {
		return nil, r.err
	}
	return data, nil
}

// erectDomainRequest is [MS-RDPBCGR] 2.2.1.5: subHeight and subInterval
// both 0.
func erectDomainRequest() []byte {
	return []byte{mcsErectDomainRequest, 0x01, 0x00, 0x01, 0x00}
}

// attachUserRequest is [MS-RDPBCGR] 2.2.1.6.
func attachUserRequest() []byte { return []byte{mcsAttachUserRequest} }

// parseAttachUserConfirm returns the user channel of an Attach User
// Confirm, [MS-RDPBCGR] 2.2.1.7.
func parseAttachUserConfirm(p []byte) (uint16, error) {
	r := reader{b: p}
	choice := r.u8()
	result := r.u8()
	initiator := r.u16be()
	if r.err != nil || choice&mcsDomainPDUChoiceMask != mcsAttachUserConfirm || choice&0x02 == 0 {
		return 0, errors.New("rdp: no MCS attach user confirm")
	}
	if result != 0 {
		return 0, fmt.Errorf("rdp: MCS attach user refused: result %d", result)
	}
	return initiator + mcsBaseUserID, nil
}

// channelJoinRequest is [MS-RDPBCGR] 2.2.1.8.
func channelJoinRequest(user, channel uint16) []byte {
	return []byte{mcsChannelJoinRequest,
		byte((user - mcsBaseUserID) >> 8), byte(user - mcsBaseUserID),
		byte(channel >> 8), byte(channel)}
}

// parseChannelJoinConfirm checks a Channel Join Confirm, [MS-RDPBCGR]
// 2.2.1.9, for channel.
func parseChannelJoinConfirm(p []byte, channel uint16) error {
	r := reader{b: p}
	choice := r.u8()
	result := r.u8()
	r.u16be() // initiator
	requested := r.u16be()
	if r.err != nil || choice&mcsDomainPDUChoiceMask != mcsChannelJoinConfirm {
		return errors.New("rdp: no MCS channel join confirm")
	}
	if result != 0 {
		return fmt.Errorf("rdp: MCS join of channel %d refused: result %d", channel, result)
	}
	if requested != channel {
		return fmt.Errorf("rdp: MCS join confirms channel %d, want %d", requested, channel)
	}
	return nil
}

// sendDataRequest wraps data for channel in an MCS Send Data Request,
// [T.125] 11.32, with high priority and the data whole.
func sendDataRequest(user, channel uint16, data []byte) []byte {
	b := make([]byte, 0, 8+len(data))
	b = append(b, mcsSendDataRequest,
		byte((user-mcsBaseUserID)>>8), byte(user-mcsBaseUserID),
		byte(channel>>8), byte(channel),
		0x70) // dataPriority high, segmentation begin and end
	b = perLen(b, len(data))
	return append(b, data...)
}

// parseDomainPDU returns the channel and data of a Send Data Indication,
// or reports a Disconnect Provider Ultimatum.
func parseDomainPDU(p []byte) (channel uint16, data []byte, err error) {
	r := reader{b: p}
	choice := r.u8()
	switch choice & mcsDomainPDUChoiceMask {
	case mcsSendDataIndication:
	case mcsDisconnectUltimatum:
		return 0, nil, errDisconnected
	default:
		return 0, nil, fmt.Errorf("rdp: unexpected MCS PDU %#02x", choice)
	}
	r.u16be() // initiator
	channel = r.u16be()
	r.u8() // dataPriority and segmentation
	n := int(r.u8())
	if n&0x80 != 0 {
		n = (n&0x7f)<<8 | int(r.u8())
	}
	data = r.take(n)
	if r.err != nil {
		return 0, nil, r.err
	}
	return channel, data, nil
}

// errDisconnected reports the server's MCS Disconnect Provider Ultimatum.
var errDisconnected = errors.New("rdp: server disconnected")
