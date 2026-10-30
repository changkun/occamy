// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import (
	"errors"
	"fmt"
	"strconv"
)

// A Message is one protocol message: a pointer to one of the message
// types of this package, such as *Tile.
type Message interface {
	// Type returns the message's type.
	Type() Type
	// Append appends the whole message, header included, to b and returns
	// the extended buffer. The header's flags are zero.
	Append(b []byte) []byte
	// Decode decodes body, the body of a message of this type, into the
	// message. It checks every field as it reads it, then that no bytes
	// are left, then the rules between fields, and returns the first
	// error. Byte strings alias body and are not copied. If Decode fails,
	// the message's fields are unspecified.
	Decode(body []byte, l Limits) error
}

// The message types of protocol v1. Types 0x0001 to 0x00FF go in both
// directions, 0x0100 to 0x01FF from client to server, and 0x0200 to
// 0x02FF from server to client, so that a type number alone tells a
// message sent the wrong way.
const (
	TypePing  Type = 0x0001
	TypePong  Type = 0x0002
	TypeClose Type = 0x0003

	TypeHello           Type = 0x0101
	TypeKey             Type = 0x0102
	TypeText            Type = 0x0103
	TypeTermInput       Type = 0x0104
	TypePointer         Type = 0x0105
	TypeWheel           Type = 0x0106
	TypeResizeRequest   Type = 0x0107
	TypeClientClipboard Type = 0x0108
	TypeView            Type = 0x0109
	TypeFrameAck        Type = 0x010a
	TypePromptReply     Type = 0x010b

	TypeWelcome         Type = 0x0201
	TypeResize          Type = 0x0202
	TypeTile            Type = 0x0203
	TypeCopyRect        Type = 0x0204
	TypeFrameEnd        Type = 0x0205
	TypeCursor          Type = 0x0206
	TypeCursorMove      Type = 0x0207
	TypeTermData        Type = 0x0208
	TypeTermSnapshot    Type = 0x0209
	TypeServerClipboard Type = 0x020a
	TypeBell            Type = 0x020b
	TypeTitle           Type = 0x020c
	TypePrompt          Type = 0x020d
	TypeViewerJoined    Type = 0x020e
	TypeViewerLeft      Type = 0x020f
)

// sender is the peer that sends a message type.
type sender uint8

const (
	eitherPeer sender = iota
	clientPeer
	serverPeer
)

// catalogue lists every message type of protocol v1. New, the
// registries and Type.String all come from it.
var catalogue = [...]struct {
	t    Type
	name string
	from sender
	new  func() Message
}{
	{TypePing, "ping", eitherPeer, func() Message { return new(Ping) }},
	{TypePong, "pong", eitherPeer, func() Message { return new(Pong) }},
	{TypeClose, "close", eitherPeer, func() Message { return new(Close) }},

	{TypeHello, "hello", clientPeer, func() Message { return new(Hello) }},
	{TypeKey, "key", clientPeer, func() Message { return new(Key) }},
	{TypeText, "text", clientPeer, func() Message { return new(Text) }},
	{TypeTermInput, "term-input", clientPeer, func() Message { return new(TermInput) }},
	{TypePointer, "pointer", clientPeer, func() Message { return new(Pointer) }},
	{TypeWheel, "wheel", clientPeer, func() Message { return new(Wheel) }},
	{TypeResizeRequest, "resize-request", clientPeer, func() Message { return new(ResizeRequest) }},
	{TypeClientClipboard, "clipboard", clientPeer, func() Message { return new(ClientClipboard) }},
	{TypeView, "view", clientPeer, func() Message { return new(View) }},
	{TypeFrameAck, "frame-ack", clientPeer, func() Message { return new(FrameAck) }},
	{TypePromptReply, "prompt-reply", clientPeer, func() Message { return new(PromptReply) }},

	{TypeWelcome, "welcome", serverPeer, func() Message { return new(Welcome) }},
	{TypeResize, "resize", serverPeer, func() Message { return new(Resize) }},
	{TypeTile, "tile", serverPeer, func() Message { return new(Tile) }},
	{TypeCopyRect, "copy-rect", serverPeer, func() Message { return new(CopyRect) }},
	{TypeFrameEnd, "frame-end", serverPeer, func() Message { return new(FrameEnd) }},
	{TypeCursor, "cursor", serverPeer, func() Message { return new(Cursor) }},
	{TypeCursorMove, "cursor-move", serverPeer, func() Message { return new(CursorMove) }},
	{TypeTermData, "term-data", serverPeer, func() Message { return new(TermData) }},
	{TypeTermSnapshot, "term-snapshot", serverPeer, func() Message { return new(TermSnapshot) }},
	{TypeServerClipboard, "clipboard", serverPeer, func() Message { return new(ServerClipboard) }},
	{TypeBell, "bell", serverPeer, func() Message { return new(Bell) }},
	{TypeTitle, "title", serverPeer, func() Message { return new(Title) }},
	{TypePrompt, "prompt", serverPeer, func() Message { return new(Prompt) }},
	{TypeViewerJoined, "viewer-joined", serverPeer, func() Message { return new(ViewerJoined) }},
	{TypeViewerLeft, "viewer-left", serverPeer, func() Message { return new(ViewerLeft) }},
}

// catalogueIndex maps a message type to its index in catalogue.
var catalogueIndex = func() map[Type]int {
	m := make(map[Type]int, len(catalogue))
	for i, e := range catalogue {
		m[e.t] = i
	}
	return m
}()

// String returns the name of a v1 message type, such as "tile", or the
// number of another type.
func (t Type) String() string {
	if i, ok := catalogueIndex[t]; ok {
		return catalogue[i].name
	}
	return fmt.Sprintf("Type(%#04x)", uint16(t))
}

// New returns a new, zero message of type t, or nil if t is not a v1
// message type.
func New(t Type) Message {
	if i, ok := catalogueIndex[t]; ok {
		return catalogue[i].new()
	}
	return nil
}

// ClientMessages returns a new Registry of the message types that
// clients send: those from 0x0100 to 0x01FF and those for both
// directions. A server accepts messages with it.
func ClientMessages() *Registry { return registry(clientPeer) }

// ServerMessages returns a new Registry of the message types that
// servers send: those from 0x0200 to 0x02FF and those for both
// directions. A client accepts messages with it.
func ServerMessages() *Registry { return registry(serverPeer) }

func registry(from sender) *Registry {
	r := new(Registry)
	for _, e := range catalogue {
		if e.from == from || e.from == eitherPeer {
			r.Register(e.t, e.name)
		}
	}
	return r
}

// Decode decodes msg, a whole message received by a peer that accepts
// the types in r, such as a registry from ClientMessages or
// ServerMessages. It returns nil and no error for a message whose type r
// does not know but which is marked ignorable; the receiver skips it.
//
// Decode allocates the message it returns. A receiver that decodes many
// tiles can avoid that by switching on the header from Split and
// decoding into a Tile it reuses.
func Decode(msg []byte, r *Registry, l Limits) (Message, error) {
	h, body, err := Split(msg, l)
	if err != nil {
		return nil, err
	}
	ok, err := r.Accept(h)
	if err != nil || !ok {
		return nil, err
	}
	m := New(h.Type)
	if m == nil {
		return nil, fmt.Errorf("%w: type %#04x is not a v1 message", ErrUnsupported, uint16(h.Type))
	}
	if err := m.Decode(body, l); err != nil {
		return nil, fmt.Errorf("%v: %w", h.Type, err)
	}
	return m, nil
}

// appendType appends the header of a message of type t, whose flags are
// zero.
func appendType(b []byte, t Type) []byte { return AppendHeader(b, Header{Type: t}) }

// failf records an error wrapping sentinel, unless a read failed before.
func (d *Decoder) failf(sentinel error, format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
	}
}

// Ping asks the other peer for a Pong with the same nonce and time, to
// measure the round trip. Either peer may send it.
type Ping struct {
	Nonce uint32
	// Time is the sender's clock in microseconds, from an epoch of its
	// choice.
	Time uint64
}

// Pong answers a Ping with its nonce and time.
type Pong struct {
	Nonce uint32
	Time  uint64
}

// Type returns TypePing.
func (Ping) Type() Type { return TypePing }

// Append appends the message to b.
func (m Ping) Append(b []byte) []byte {
	b = appendType(b, TypePing)
	b = AppendUint32(b, m.Nonce)
	return AppendUint64(b, m.Time)
}

// Decode decodes body into m.
func (m *Ping) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Nonce = d.ReadUint32()
	m.Time = d.ReadUint64()
	return d.Finish()
}

// Type returns TypePong.
func (Pong) Type() Type { return TypePong }

// Append appends the message to b.
func (m Pong) Append(b []byte) []byte {
	b = appendType(b, TypePong)
	b = AppendUint32(b, m.Nonce)
	return AppendUint64(b, m.Time)
}

// Decode decodes body into m.
func (m *Pong) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Nonce = d.ReadUint32()
	m.Time = d.ReadUint64()
	return d.Finish()
}

// A CloseCode says why a connection ends. Close codes are WebSocket close
// status codes, so a peer that sends Close then closes the WebSocket with
// the same code, and a client that sees only the close frame still knows
// why.
type CloseCode uint16

// The close codes. A receiver treats a code it does not know as an error
// described by the reason.
const (
	// CloseNormal ends a connection that has done its work, such as a
	// viewer that leaves.
	CloseNormal CloseCode = 1000
	// CloseServerShutdown says that the server is shutting down.
	CloseServerShutdown CloseCode = 1001
	// CloseProtocolError says that the peer broke the protocol, such as
	// with a truncated message or an invalid value.
	CloseProtocolError CloseCode = 1002
	// CloseUnsupported says that the peer sent a message of an unknown
	// type that is not ignorable, a hello of another protocol version, or
	// asked for something the receiver cannot do.
	CloseUnsupported CloseCode = 1003
	// CloseTooLarge says that the peer sent a message larger than the
	// maximum message size or a tile larger than the maximum texture size.
	CloseTooLarge CloseCode = 1009
	// CloseInternalError says that the server failed on its own.
	CloseInternalError CloseCode = 1011
	// CloseUnauthorized says that the token is missing, invalid or
	// expired.
	CloseUnauthorized CloseCode = 4000
	// CloseForbidden says that the token does not grant access to the
	// connection or the session.
	CloseForbidden CloseCode = 4001
	// CloseSessionClosed says that the session's owner closed it.
	CloseSessionClosed CloseCode = 4002
	// CloseOwnerLeft says that the owner left and did not return within
	// the grace period.
	CloseOwnerLeft CloseCode = 4003
	// CloseIdle says that the session ended after no input for the idle
	// timeout.
	CloseIdle CloseCode = 4004
	// CloseRemoteEnded says that the remote host ended the session, such
	// as a shell that exits.
	CloseRemoteEnded CloseCode = 4005
	// CloseBackendFailed says that the connection to the remote host
	// failed.
	CloseBackendFailed CloseCode = 4006
	// CloseViewerLimit says that the session has as many viewers as it
	// allows.
	CloseViewerLimit CloseCode = 4007
)

var closeCodeNames = map[CloseCode]string{
	CloseNormal:         "normal",
	CloseServerShutdown: "server-shutdown",
	CloseProtocolError:  "protocol-error",
	CloseUnsupported:    "unsupported",
	CloseTooLarge:       "too-large",
	CloseInternalError:  "internal-error",
	CloseUnauthorized:   "unauthorized",
	CloseForbidden:      "forbidden",
	CloseSessionClosed:  "session-closed",
	CloseOwnerLeft:      "owner-left",
	CloseIdle:           "idle",
	CloseRemoteEnded:    "remote-ended",
	CloseBackendFailed:  "backend-failed",
	CloseViewerLimit:    "viewer-limit",
}

// String returns the name of c, such as "protocol-error", or its number
// if it is not a v1 close code.
func (c CloseCode) String() string {
	if name, ok := closeCodeNames[c]; ok {
		return name
	}
	return "CloseCode(" + strconv.Itoa(int(c)) + ")"
}

// CloseCodeFor returns the code that closes a connection over err, an
// error from this package: unsupported for ErrUnsupported, too-large for
// ErrTooLarge, and protocol-error for other decoding errors. It returns
// CloseNormal for nil and CloseInternalError for errors of other
// packages.
func CloseCodeFor(err error) CloseCode {
	switch {
	case err == nil:
		return CloseNormal
	case errors.Is(err, ErrUnsupported):
		return CloseUnsupported
	case errors.Is(err, ErrTooLarge):
		return CloseTooLarge
	case errors.Is(err, ErrTruncated), errors.Is(err, ErrInvalidUTF8),
		errors.Is(err, ErrTrailingData), errors.Is(err, ErrInvalid):
		return CloseProtocolError
	}
	return CloseInternalError
}

// Close ends the connection. The sender then closes the WebSocket with
// the same code and sends nothing more.
type Close struct {
	Code CloseCode
	// Reason describes the cause for people and logs. It never holds a
	// secret, and it is short enough to fit a WebSocket close frame (123
	// bytes) where possible.
	Reason string
}

// Type returns TypeClose.
func (Close) Type() Type { return TypeClose }

// Append appends the message to b.
func (m Close) Append(b []byte) []byte {
	b = appendType(b, TypeClose)
	b = AppendUint16(b, uint16(m.Code))
	return AppendString(b, m.Reason)
}

// Decode decodes body into m.
func (m *Close) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Code = CloseCode(d.ReadUint16())
	m.Reason = d.ReadString()
	return d.Finish()
}
