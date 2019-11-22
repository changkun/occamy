// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

// CompositeMode used by Occamy draw instructions. Each
// composite mode maps to a unique channel mask integer.
type CompositeMode int

const (
	// A: Source where destination transparent = S n D'
	// B: Source where destination opaque      = S n D
	// C: Destination where source transparent = D n S'
	// D: Destination where source opaque      = D n S

	// 0 = Active, 1 = Inactive

	// CompROUT 0010 - Clears destination where source opaque
	CompROUT CompositeMode = 0x2
	// CompATOP 0110 - Fill where destination opaque only
	CompATOP CompositeMode = 0x6
	// CompXOR 1010 - XOR
	CompXOR CompositeMode = 0xA
	// CompROVER 1011 - Fill where destination transparent only
	CompROVER CompositeMode = 0xB
	// CompOVER 1110 - Draw normally
	CompOVER CompositeMode = 0xE
	// CompPLUS 1111 - Add
	CompPLUS CompositeMode = 0xF

	// Unimplemented in client:
	// NOT IMPLEMENTED:       0000 - Clear
	// NOT IMPLEMENTED:       0011 - No operation
	// NOT IMPLEMENTED:       0101 - Additive IN
	// NOT IMPLEMENTED:       0111 - Additive ATOP
	// NOT IMPLEMENTED:       1101 - Additive RATOP

	// Buggy in webkit browsers, as they keep channel C on in all cases:

	// CompRIN 0001
	CompRIN CompositeMode = 0x1
	// CompIN 0100
	CompIN CompositeMode = 0x4
	// CompOUT 1000
	CompOUT CompositeMode = 0x8
	// CompRATOP 1001
	CompRATOP CompositeMode = 0x9
	// CompSRC 1100
	CompSRC CompositeMode = 0xC

	// Bitwise composite operations (binary)

	// A: S' & D'
	// B: S' & D
	// C: S  & D'
	// D: S  & D

	// 0 = Active, 1 = Inactive
)

// ProtocolStatus represents all possible status codes returned by
// protocol operations. These codes relate to Guacamole server/client
// communication, and not to internal communication of errors within
// libguac and linked software.
// In general:
//     0x0000 - 0x00FF: Successful operations.
//     0x0100 - 0x01FF: Operations that failed due to implementation status.
//     0x0200 - 0x02FF: Operations that failed due to remote state/environment.
//     0x0300 - 0x03FF: Operations that failed due to user/client action.
// There is a general correspondence of these status codes with HTTP response
// codes.
type ProtocolStatus int

const (

	// ProtocolStatusSuccess The operation succeeded.
	ProtocolStatusSuccess ProtocolStatus = 0x0000
	// ProtocolStatusUnsupported The requested operation is unsupported.
	ProtocolStatusUnsupported ProtocolStatus = 0x0100
	// ProtocolStatusServerError The operation could not be performed
	// due to an internal failure.
	ProtocolStatusServerError ProtocolStatus = 0x0200
	// ProtocolStatusServerBusy The operation could not be performed due
	// as the server is busy.
	ProtocolStatusServerBusy ProtocolStatus = 0x0201
	// ProtocolStatusUpstreamTimeout The operation could not be
	// performed because the upstream server
	// is not responding.
	ProtocolStatusUpstreamTimeout ProtocolStatus = 0x0202
	// ProtocolStatusUpstreamError The operation was unsuccessful due to
	// an error or otherwise unexpected condition of the upstream server.
	ProtocolStatusUpstreamError ProtocolStatus = 0x0203
	// ProtocolStatusResourceNotFound The operation could not be
	// performed as the requested resource does not exist.
	ProtocolStatusResourceNotFound ProtocolStatus = 0x0204
	// ProtocolStatusResourceConflict The operation could not be
	// performed as the requested resource is already in use.
	ProtocolStatusResourceConflict ProtocolStatus = 0x0205
	// ProtocolStatusResourceClosed The operation could not be performed
	// as the requested resource is now closed.
	ProtocolStatusResourceClosed ProtocolStatus = 0x0206
	// ProtocolStatusUpstreamNotFound The operation could not be
	// performed because the upstream server does not appear to exist.
	ProtocolStatusUpstreamNotFound ProtocolStatus = 0x0207
	// ProtocolStatusUpstreamUnavailable The operation could not be
	// performed because the upstream server is not available to service
	// the request.
	ProtocolStatusUpstreamUnavailable ProtocolStatus = 0x0208
	// ProtocolStatusSessionConflict The session within the upstream
	// server has ended because it conflicted with another session.
	ProtocolStatusSessionConflict ProtocolStatus = 0x0209
	// ProtocolStatusSessionTimeout The session within the upstream
	// server has ended because it appeared to be inactive.
	ProtocolStatusSessionTimeout ProtocolStatus = 0x020A
	// ProtocolStatusSessionClosed The session within the upstream
	// server has been forcibly terminated.
	ProtocolStatusSessionClosed ProtocolStatus = 0x020B
	// ProtocolStatusClientBadRequest The operation could not be
	// performed because bad parameters were given.
	ProtocolStatusClientBadRequest ProtocolStatus = 0x300
	// ProtocolStatusClientUnauthorized Permission was denied to perform
	// the operation, as the user is not yet authorized (not yet logged
	// in, for example).
	ProtocolStatusClientUnauthorized ProtocolStatus = 0x0301
	// ProtocolStatusClientForbidden Permission was denied to perform
	// the operation, and this permission will not be granted even if
	// the user is authorized.
	ProtocolStatusClientForbidden ProtocolStatus = 0x0303
	// ProtocolStatusClientTimeout The client took too long to respond.
	ProtocolStatusClientTimeout ProtocolStatus = 0x308
	// ProtocolStatusClientOverrun The client sent too much data.
	ProtocolStatusClientOverrun ProtocolStatus = 0x30D
	// ProtocolStatusClientBadType The client sent data of an
	// unsupported or unexpected type.
	ProtocolStatusClientBadType ProtocolStatus = 0x30F
	// ProtocolStatusClientTooMany The operation failed because the
	// current client is already using too many resources.
	ProtocolStatusClientTooMany ProtocolStatus = 0x31D
)

// TransferFunc represents default transfer functions. There is no
// current facility in the Occamy protocol to define custom
// transfer functions.
type TransferFunc int

// Constant functions
const (
	TransferBinaryBLACK TransferFunc = 0x0 // 0000
	TransferBinaryWHITE TransferFunc = 0xF // 1111

	// Copy functions
	TransferBinarySRC   TransferFunc = 0x3 // 0011
	TransferBinaryDEST  TransferFunc = 0x5 // 0101
	TransferBinaryNSRC  TransferFunc = 0xC // 1100
	TransferBinaryNDEST TransferFunc = 0xA // 1010

	// AND / NAND
	TransferBinaryAND  TransferFunc = 0x1 // 0001
	TransferBinaryNAND TransferFunc = 0xE // 1110

	// OR / NOR
	TransferBinaryOR  TransferFunc = 0x7 // 0111
	TransferBinaryNOR TransferFunc = 0x8 // 1000

	// XOR / XNOR
	TransferBinaryXOR  TransferFunc = 0x6 // 0110
	TransferBinaryXNOR TransferFunc = 0x9 // 1001

	// AND / NAND with inverted source
	TransferBinaryNSRCAND  TransferFunc = 0x4 // 0100
	TransferBinaryNSRCNAND TransferFunc = 0xB // 1011

	// OR / NOR with inverted source
	TransferBinaryNSRCOR  TransferFunc = 0xD // 1101
	TransferBinaryNSRCNOR TransferFunc = 0x2 // 0010

	// AND / NAND with inverted destination
	TransferBinaryNDESTAND  TransferFunc = 0x2 // 0010
	TransferBinaryNDESTNAND TransferFunc = 0xD // 1101

	// OR / NOR with inverted destination
	TransferBinaryNDESTOR  TransferFunc = 0xB // 1011
	TransferBinaryNDESTNOR TransferFunc = 0x4 // 0100
)
