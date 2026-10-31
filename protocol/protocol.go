// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package protocol implements Occamy protocol v1, the wire protocol
// between clients and occamyd.
//
// Each protocol message travels as one WebSocket binary message, so the
// transport delimits messages and this package works on whole messages as
// byte slices. A message is a Header, the message type and flags as two
// little-endian u16, followed by a body of fixed-size little-endian fields
// and length-prefixed variable fields. Bodies are written with the Append
// functions and read with a Decoder.
//
// A receiver splits a message with Split and asks a Registry whether it
// knows the type. A message of an unknown type is skipped if it is marked
// ignorable, and otherwise ends the connection with the unsupported close
// code.
//
// Each v1 message is a type of this package, such as Hello, Key or Tile,
// whose Append method encodes it and whose Decode method decodes its
// body. Decode reads a whole message as a client or a server receives
// it, with the registry from ServerMessages or ClientMessages. A session
// opens in one round trip: the client sends its Hello in the upgrade
// request (EncodeHelloQuery) with its token in Sec-WebSocket-Protocol
// (TokenSubprotocol), and the server answers with a Welcome whose
// capabilities come from Agree.
//
// docs/protocol.md describes the protocol and is normative; this package
// is its reference implementation.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Subprotocol is the WebSocket subprotocol of protocol v1. The major
// version lives in its name; minor versions evolve through capabilities
// and ignorable messages.
const Subprotocol = "occamy.v1"

// HeaderSize is the size of a message header in bytes.
const HeaderSize = 4

// DefaultMaxMessageSize is the largest message, header included, that a
// peer accepts unless the handshake agrees on another limit.
const DefaultMaxMessageSize = 4 << 20

// MinMessageSize is the smallest max_message_size a peer may state, so
// that a message is never too small to make progress: a receiver refuses
// a hello or welcome that states less.
const MinMessageSize = 64 << 10

// DefaultMaxTextureSize is the largest width or height, in pixels, of a
// tile or a cursor image that a client accepts unless it states another
// limit in its hello.
const DefaultMaxTextureSize = 4096

// MaxCachedPixels is the most pixels of a tile stored in a cache slot: a
// tile with a slot is at most 64 by 64 pixels in area, so that a client
// bounds its cache at 16 KiB a slot.
const MaxCachedPixels = 4096

// Errors while decoding a message.
var (
	// ErrTruncated reports a message that ends inside its header or inside
	// a field, including a length prefix larger than the rest of the
	// message.
	ErrTruncated = errors.New("protocol: message truncated")
	// ErrTooLarge reports a message larger than the maximum message size,
	// or a tile or cursor image wider or taller than the maximum texture
	// size.
	ErrTooLarge = errors.New("protocol: message too large")
	// ErrInvalidUTF8 reports a string field that is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("protocol: string is not valid UTF-8")
	// ErrTrailingData reports bytes left in a body after its last field.
	ErrTrailingData = errors.New("protocol: data after the last field")
	// ErrInvalid reports a field whose value the protocol does not allow,
	// such as a boolean other than 0 or 1, an unknown role or codec, or
	// capabilities out of order. It also reports a malformed hello query
	// or Sec-WebSocket-Protocol header.
	ErrInvalid = errors.New("protocol: invalid value")
	// ErrUnsupported reports a message of an unknown type that is not
	// ignorable, or a hello of another protocol version. The receiver
	// closes the connection with the unsupported close code.
	ErrUnsupported = errors.New("protocol: unsupported message type")
)

// Type identifies a message type. A type number names one message for
// good: the number of a retired message is never given to another, and
// type 0 is never used.
type Type uint16

// Flags are the flags of a message header.
type Flags uint16

// FlagIgnorable marks a message that a receiver may skip if it does not
// know its type. The other bits are reserved: senders leave them zero and
// receivers ignore them.
const FlagIgnorable Flags = 1 << 0

// Header starts every message.
type Header struct {
	Type  Type
	Flags Flags
}

// Ignorable reports whether a receiver that does not know h.Type may skip
// the message.
func (h Header) Ignorable() bool { return h.Flags&FlagIgnorable != 0 }

// AppendHeader appends h to b, which starts a message.
func AppendHeader(b []byte, h Header) []byte {
	b = binary.LittleEndian.AppendUint16(b, uint16(h.Type))
	return binary.LittleEndian.AppendUint16(b, uint16(h.Flags))
}

// Split splits msg into its header and its body, which aliases msg. It
// returns an error wrapping ErrTooLarge if msg exceeds l, and
// ErrTruncated if msg is shorter than a header.
func Split(msg []byte, l Limits) (Header, []byte, error) {
	if err := l.CheckSize(len(msg)); err != nil {
		return Header{}, nil, err
	}
	if len(msg) < HeaderSize {
		return Header{}, nil, ErrTruncated
	}
	h := Header{
		Type:  Type(binary.LittleEndian.Uint16(msg)),
		Flags: Flags(binary.LittleEndian.Uint16(msg[2:])),
	}
	return h, msg[HeaderSize:], nil
}

// Limits are the sizes a peer accepts. Each is checked before anything is
// allocated for the data it bounds. After the handshake, both peers use
// the limits of the agreed capabilities (see Capabilities.Limits).
type Limits struct {
	// MaxMessageSize is the largest message in bytes, header included.
	// Zero or less means DefaultMaxMessageSize.
	MaxMessageSize int
	// MaxTextureSize is the largest width or height of a tile or a cursor
	// image in pixels. Zero or less means DefaultMaxTextureSize.
	MaxTextureSize int
	// CacheSlots is the number of tile cache slots the client keeps,
	// numbered from 1. Zero or less means none.
	CacheSlots int
}

func (l Limits) maxTextureSize() int {
	if l.MaxTextureSize <= 0 {
		return DefaultMaxTextureSize
	}
	return l.MaxTextureSize
}

// CheckSize returns an error wrapping ErrTooLarge if a message of n bytes
// exceeds l. A sender checks a message before sending it. A receiver
// checks it before reading it: with a WebSocket library, set the
// connection's read limit to the same size, so that a larger message is
// refused before it is buffered, and Split checks again.
func (l Limits) CheckSize(n int) error {
	limit := l.MaxMessageSize
	if limit <= 0 {
		limit = DefaultMaxMessageSize
	}
	if n > limit {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, n, limit)
	}
	return nil
}
