// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Version is the protocol's major version, which also names the
// subprotocol. It is the first field of a hello, so that a server can
// tell a hello of another version before it reads the rest.
const Version = 1

// DefaultFrameWindow is the number of unacknowledged frames a server
// sends a viewer unless the handshake agrees on another window.
const DefaultFrameWindow = 2

// HelloParameter is the query parameter of the WebSocket upgrade request
// that carries the client's hello (see EncodeHelloQuery).
const HelloParameter = "hello"

// MaxHelloSize is the largest hello, header included, in bytes. Encoded
// in the query, it takes at most 2731 characters, so that the request
// line stays well within the 8 KiB that common proxies allow.
const MaxHelloSize = 2048

// tokenPrefix starts the subprotocol that carries a token.
const tokenPrefix = Subprotocol + ".token."

// base64url is the encoding of the hello query and of tokens: the URL
// and filename safe alphabet without padding, with zero trailing bits, so
// that each value has one encoding.
var base64url = base64.RawURLEncoding.Strict()

// Role is a viewer's share role.
type Role uint8

// The roles. The owner and viewers may send input; read-only viewers may
// not, and the server drops their input.
const (
	RoleOwner    Role = 1
	RoleViewer   Role = 2
	RoleReadOnly Role = 3
)

func (r Role) valid() bool { return r >= RoleOwner && r <= RoleReadOnly }

func (r Role) String() string {
	switch r {
	case RoleOwner:
		return "owner"
	case RoleViewer:
		return "viewer"
	case RoleReadOnly:
		return "read-only"
	}
	return "Role(" + strconv.Itoa(int(r)) + ")"
}

// Class is the kind of content a session shows.
type Class uint8

// The classes.
const (
	// ClassTerminal sessions send term-data and term-snapshot, in
	// character cells.
	ClassTerminal Class = 1
	// ClassGraphical sessions send tiles, in pixels.
	ClassGraphical Class = 2
)

func (c Class) valid() bool { return c == ClassTerminal || c == ClassGraphical }

func (c Class) String() string {
	switch c {
	case ClassTerminal:
		return "terminal"
	case ClassGraphical:
		return "graphical"
	}
	return "Class(" + strconv.Itoa(int(c)) + ")"
}

// Codec identifies the encoding of a tile's or a cursor's pixels. Codec
// 0 is not a codec, and 0x80 to 0xFF are reserved for a video tier after
// 1.0.
type Codec uint8

// The codecs. Raw's channel order and Deflate's container are
// provisional until the graphics pipeline's measurement fixes the pixel
// layout and the lossless container.
const (
	// CodecRaw is 4 bytes per pixel, row by row without padding, in RGBA
	// order with straight alpha.
	CodecRaw Codec = 1
	// CodecDeflate is a raw DEFLATE stream (RFC 1951) of a palette or raw
	// pixel container.
	CodecDeflate Codec = 2
	// CodecPNG is a PNG file.
	CodecPNG Codec = 3
	// CodecJPEG is a baseline or progressive JFIF file.
	CodecJPEG Codec = 4
	// CodecWebP is a WebP file. occamyd does not send it in beta, since
	// no pure-Go encoder exists.
	CodecWebP Codec = 5
	// CodecCached draws the tile stored in a client cache slot and has no
	// payload. It is only for tiles.
	CodecCached Codec = 6
)

func (c Codec) valid() bool { return c >= CodecRaw && c <= CodecCached }

func (c Codec) String() string {
	switch c {
	case CodecRaw:
		return "raw"
	case CodecDeflate:
		return "deflate"
	case CodecPNG:
		return "png"
	case CodecJPEG:
		return "jpeg"
	case CodecWebP:
		return "webp"
	case CodecCached:
		return "cached"
	}
	return "Codec(" + strconv.Itoa(int(c)) + ")"
}

// CapabilityID identifies a capability. ID 0 is reserved.
type CapabilityID uint16

// The capabilities this package knows, with the encoding of each value.
const (
	// CapCodecs is a list of codecs, one byte each, in order of
	// preference.
	CapCodecs CapabilityID = 1
	// CapMaxTextureSize is a u16, the largest tile or cursor width or
	// height in pixels.
	CapMaxTextureSize CapabilityID = 2
	// CapTerminal is a u8, the terminal level: 1 is an xterm-compatible
	// emulator.
	CapTerminal CapabilityID = 3
	// CapPixelRatio is a u16, device pixels per CSS pixel in hundredths.
	CapPixelRatio CapabilityID = 4
	// CapCacheSlots is a u16, the number of tile cache slots.
	CapCacheSlots CapabilityID = 5
	// CapFrameWindow is a u8, the number of unacknowledged frames.
	CapFrameWindow CapabilityID = 6
	// CapMaxMessageSize is a u32, the largest message in bytes.
	CapMaxMessageSize CapabilityID = 7
)

// capabilityMinSize is the smallest encoded capability: an ID and an
// empty value.
const capabilityMinSize = 2 + lengthSize

// A Capability is an entry of the capability list whose ID this package
// does not know.
type Capability struct {
	ID CapabilityID
	// Value aliases the message it was decoded from.
	Value []byte
}

// Capabilities are what a client offers in its hello, and what the
// server agrees to in its welcome.
//
// On the wire they are a list of entries, each an ID and a byte string
// value, in ascending order of ID and with each ID at most once. A
// capability that is zero (or an empty list) is not sent, and a receiver
// rejects an entry with a zero value, so that each set of capabilities
// has one encoding. A capability that is not sent takes its default.
type Capabilities struct {
	// Codecs are the codecs the client decodes, in order of preference,
	// each at most once.
	Codecs []Codec
	// MaxTextureSize is the largest tile or cursor width or height in
	// pixels that the client decodes. Zero means DefaultMaxTextureSize.
	MaxTextureSize uint16
	// Terminal is the terminal level the client renders: 1 is an
	// xterm-compatible emulator, and zero means no terminal.
	Terminal uint8
	// PixelRatio is the client's device pixels per CSS pixel in
	// hundredths, such as 200 for a display at twice the density. Zero
	// means 100.
	PixelRatio uint16
	// CacheSlots is the number of tile cache slots the client keeps.
	// Zero means none.
	CacheSlots uint16
	// FrameWindow is the number of unacknowledged frames the server sends
	// a viewer. Zero means DefaultFrameWindow.
	FrameWindow uint8
	// MaxMessageSize is the largest message, header included, in bytes.
	// Zero means DefaultMaxMessageSize.
	MaxMessageSize uint32
	// Unknown holds the entries whose IDs this package does not know, as
	// a receiver decoded them, so that the capabilities encode again as
	// they arrived. A receiver otherwise ignores them. Their IDs ascend,
	// and are larger than every known ID.
	Unknown []Capability
}

// Limits returns the limits that c sets: the maximum message size, the
// maximum texture size and the cache slots.
func (c Capabilities) Limits() Limits {
	return Limits{
		MaxMessageSize: int(min(c.MaxMessageSize, math.MaxInt32)),
		MaxTextureSize: int(c.MaxTextureSize),
		CacheSlots:     int(c.CacheSlots),
	}
}

// Agree returns the capabilities that a server whose own capabilities
// are server agrees to with a client that offered client:
//
//   - the client's codecs that the server also has, in the client's
//     order;
//   - the client's maximum texture size, lowered to the server's if the
//     server states one;
//   - the lower terminal level and the fewer cache slots;
//   - the client's pixel ratio;
//   - the smaller frame window and maximum message size, each counting
//     an unstated value as its default.
//
// Entries with unknown IDs are dropped.
func Agree(client, server Capabilities) Capabilities {
	var a Capabilities
	for _, c := range client.Codecs {
		if slices.Contains(server.Codecs, c) && !slices.Contains(a.Codecs, c) {
			a.Codecs = append(a.Codecs, c)
		}
	}
	a.MaxTextureSize = client.MaxTextureSize
	if server.MaxTextureSize != 0 && (a.MaxTextureSize == 0 || server.MaxTextureSize < a.MaxTextureSize) {
		a.MaxTextureSize = server.MaxTextureSize
	}
	a.Terminal = min(client.Terminal, server.Terminal)
	a.PixelRatio = client.PixelRatio
	a.CacheSlots = min(client.CacheSlots, server.CacheSlots)
	a.FrameWindow = min(orDefault(client.FrameWindow, DefaultFrameWindow), orDefault(server.FrameWindow, DefaultFrameWindow))
	a.MaxMessageSize = min(orDefault(client.MaxMessageSize, DefaultMaxMessageSize), orDefault(server.MaxMessageSize, DefaultMaxMessageSize))
	return a
}

func orDefault[T uint8 | uint32](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

// count returns the number of entries c encodes to.
func (c Capabilities) count() int {
	n := len(c.Unknown)
	for _, set := range [...]bool{
		len(c.Codecs) > 0, c.MaxTextureSize != 0, c.Terminal != 0, c.PixelRatio != 0,
		c.CacheSlots != 0, c.FrameWindow != 0, c.MaxMessageSize != 0,
	} {
		if set {
			n++
		}
	}
	return n
}

// appendCapabilities appends c to b as a list.
func appendCapabilities(b []byte, c Capabilities) []byte {
	b = AppendCount(b, c.count())
	if len(c.Codecs) > 0 {
		b = AppendUint16(b, uint16(CapCodecs))
		b = appendLength(b, len(c.Codecs))
		for _, codec := range c.Codecs {
			b = append(b, byte(codec))
		}
	}
	if c.MaxTextureSize != 0 {
		b = appendCapUint16(b, CapMaxTextureSize, c.MaxTextureSize)
	}
	if c.Terminal != 0 {
		b = appendCapUint8(b, CapTerminal, c.Terminal)
	}
	if c.PixelRatio != 0 {
		b = appendCapUint16(b, CapPixelRatio, c.PixelRatio)
	}
	if c.CacheSlots != 0 {
		b = appendCapUint16(b, CapCacheSlots, c.CacheSlots)
	}
	if c.FrameWindow != 0 {
		b = appendCapUint8(b, CapFrameWindow, c.FrameWindow)
	}
	if c.MaxMessageSize != 0 {
		b = AppendUint16(b, uint16(CapMaxMessageSize))
		b = appendLength(b, 4)
		b = AppendUint32(b, c.MaxMessageSize)
	}
	for _, u := range c.Unknown {
		b = AppendUint16(b, uint16(u.ID))
		b = AppendBytes(b, u.Value)
	}
	return b
}

func appendCapUint8(b []byte, id CapabilityID, v uint8) []byte {
	b = AppendUint16(b, uint16(id))
	b = appendLength(b, 1)
	return AppendUint8(b, v)
}

func appendCapUint16(b []byte, id CapabilityID, v uint16) []byte {
	b = AppendUint16(b, uint16(id))
	b = appendLength(b, 2)
	return AppendUint16(b, v)
}

// readCapabilities reads a capability list.
func (d *Decoder) readCapabilities() Capabilities {
	var c Capabilities
	n := d.ReadCount(capabilityMinSize)
	var last CapabilityID
	for range n {
		id := CapabilityID(d.ReadUint16())
		v := d.ReadBytes()
		if d.err != nil {
			return Capabilities{}
		}
		if id <= last {
			d.failf(ErrInvalid, "capability %d after %d", id, last)
			return Capabilities{}
		}
		last = id
		switch id {
		case CapCodecs:
			c.Codecs = d.readCodecList(v)
		case CapMaxTextureSize:
			if capValue(d, id, v, 2) {
				c.MaxTextureSize = binary.LittleEndian.Uint16(v)
			}
		case CapTerminal:
			if capValue(d, id, v, 1) {
				c.Terminal = v[0]
			}
		case CapPixelRatio:
			if capValue(d, id, v, 2) {
				c.PixelRatio = binary.LittleEndian.Uint16(v)
			}
		case CapCacheSlots:
			if capValue(d, id, v, 2) {
				c.CacheSlots = binary.LittleEndian.Uint16(v)
			}
		case CapFrameWindow:
			if capValue(d, id, v, 1) {
				c.FrameWindow = v[0]
			}
		case CapMaxMessageSize:
			if capValue(d, id, v, 4) {
				c.MaxMessageSize = binary.LittleEndian.Uint32(v)
			}
		default:
			c.Unknown = append(c.Unknown, Capability{ID: id, Value: v})
		}
	}
	if d.err != nil {
		return Capabilities{}
	}
	return c
}

// capValue reports whether v, the value of a capability that is an
// integer of size bytes, has that size and is not zero, and fails
// otherwise.
func capValue(d *Decoder, id CapabilityID, v []byte, size int) bool {
	if len(v) != size {
		d.failf(ErrInvalid, "capability %d has %d bytes, want %d", id, len(v), size)
		return false
	}
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	d.failf(ErrInvalid, "capability %d is zero", id)
	return false
}

// readCodecList reads the value of the codecs capability: at least one
// codec, none of them 0 and none twice.
func (d *Decoder) readCodecList(v []byte) []Codec {
	if len(v) == 0 {
		d.failf(ErrInvalid, "empty codec list")
		return nil
	}
	var seen [256]bool
	for _, c := range v {
		if c == 0 || seen[c] {
			d.failf(ErrInvalid, "codec %d listed twice or zero", c)
			return nil
		}
		seen[c] = true
	}
	codecs := make([]Codec, len(v))
	for i, c := range v {
		codecs[i] = Codec(c)
	}
	return codecs
}

// Hello opens a session. A client sends it in the upgrade request's
// query (see EncodeHelloQuery), never as a WebSocket message. Its body
// starts with Version, which Append writes and Decode checks.
type Hello struct {
	// Width and Height are the screen size the client asks for, in device
	// pixels, if the session is graphical.
	Width, Height uint16
	// Cols and Rows are the terminal size the client asks for, in
	// character cells, if the session is a terminal. Zero means the
	// server's default.
	Cols, Rows uint16
	// Visible is whether the client can be seen, as in View.
	Visible bool
	// Name names the client and its version, such as "occamy-web/1.0".
	Name         string
	Capabilities Capabilities
}

// Type returns TypeHello.
func (Hello) Type() Type { return TypeHello }

// Append appends the message to b.
func (m Hello) Append(b []byte) []byte {
	b = appendType(b, TypeHello)
	b = AppendUint16(b, Version)
	b = AppendUint16(b, m.Width)
	b = AppendUint16(b, m.Height)
	b = AppendUint16(b, m.Cols)
	b = AppendUint16(b, m.Rows)
	b = AppendBool(b, m.Visible)
	b = AppendString(b, m.Name)
	return appendCapabilities(b, m.Capabilities)
}

// Decode decodes body into m. It fails with an error wrapping
// ErrUnsupported if the hello is of another protocol version, without
// reading the rest.
func (m *Hello) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	if v := d.ReadUint16(); d.err == nil && v != Version {
		return fmt.Errorf("%w: protocol version %d, want %d", ErrUnsupported, v, Version)
	}
	m.Width = d.ReadUint16()
	m.Height = d.ReadUint16()
	m.Cols = d.ReadUint16()
	m.Rows = d.ReadUint16()
	m.Visible = d.ReadBool()
	m.Name = d.ReadString()
	m.Capabilities = d.readCapabilities()
	return d.Finish()
}

// Welcome answers a hello. The server sends it as its first message, and
// the session's first frame follows it.
type Welcome struct {
	// ViewerID identifies this viewer in ViewerJoined and ViewerLeft.
	ViewerID uint32
	Role     Role
	Class    Class
	// SessionID identifies the session, for joining and sharing it.
	SessionID string
	// Capabilities are the agreed capabilities, which both peers use from
	// now on.
	Capabilities Capabilities
}

// Type returns TypeWelcome.
func (Welcome) Type() Type { return TypeWelcome }

// Append appends the message to b.
func (m Welcome) Append(b []byte) []byte {
	b = appendType(b, TypeWelcome)
	b = AppendUint32(b, m.ViewerID)
	b = AppendUint8(b, uint8(m.Role))
	b = AppendUint8(b, uint8(m.Class))
	b = AppendString(b, m.SessionID)
	return appendCapabilities(b, m.Capabilities)
}

// Decode decodes body into m.
func (m *Welcome) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.ViewerID = d.ReadUint32()
	if m.Role = Role(d.ReadUint8()); !m.Role.valid() {
		d.failf(ErrInvalid, "role %d", m.Role)
	}
	if m.Class = Class(d.ReadUint8()); !m.Class.valid() {
		d.failf(ErrInvalid, "class %d", m.Class)
	}
	m.SessionID = d.ReadString()
	m.Capabilities = d.readCapabilities()
	return d.Finish()
}

// EncodeHelloQuery encodes h, header included, in unpadded base64url for
// the hello query parameter. It fails with an error wrapping ErrTooLarge
// if the hello is larger than MaxHelloSize.
func EncodeHelloQuery(h Hello) (string, error) {
	msg := h.Append(nil)
	if len(msg) > MaxHelloSize {
		return "", fmt.Errorf("%w: hello of %d bytes, limit %d", ErrTooLarge, len(msg), MaxHelloSize)
	}
	return base64url.EncodeToString(msg), nil
}

// DecodeHelloQuery decodes the value of the hello query parameter. It
// checks the size before it decodes anything, and fails with an error
// wrapping ErrTooLarge if the hello would be larger than MaxHelloSize. It
// fails with an error wrapping ErrInvalid if s is not unpadded base64url
// with zero trailing bits, or does not hold a hello.
func DecodeHelloQuery(s string) (Hello, error) {
	if n := base64url.DecodedLen(len(s)); n > MaxHelloSize {
		return Hello{}, fmt.Errorf("%w: hello of %d bytes, limit %d", ErrTooLarge, n, MaxHelloSize)
	}
	msg, err := decodeBase64URL(s)
	if err != nil {
		return Hello{}, fmt.Errorf("%w: hello is not base64url", ErrInvalid)
	}
	h, body, err := Split(msg, Limits{MaxMessageSize: MaxHelloSize})
	if err != nil {
		return Hello{}, err
	}
	if h.Type != TypeHello {
		return Hello{}, fmt.Errorf("%w: the hello query holds a %v", ErrInvalid, h.Type)
	}
	var hello Hello
	if err := hello.Decode(body, Limits{}); err != nil {
		return Hello{}, err
	}
	return hello, nil
}

// decodeBase64URL decodes s, which must use only the base64url alphabet:
// the standard decoder skips line breaks, so that s would otherwise have
// more than one encoding.
func decodeBase64URL(s string) ([]byte, error) {
	for i := range len(s) {
		if !inBase64URL(s[i]) {
			return nil, ErrInvalid
		}
	}
	return base64url.DecodeString(s)
}

func inBase64URL(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}

// TokenSubprotocol returns the subprotocol that carries token, which a
// client offers in Sec-WebSocket-Protocol beside Subprotocol:
// "occamy.v1.token." followed by the token in unpadded base64url. The
// token thus never appears in the URL, which proxies log.
func TokenSubprotocol(token string) string {
	return tokenPrefix + base64url.EncodeToString([]byte(token))
}

// TokenFromSubprotocols returns the token that a client offered with
// TokenSubprotocol. Offered holds the values of the request's
// Sec-WebSocket-Protocol header lines, each a comma-separated list, such
// as r.Header.Values("Sec-WebSocket-Protocol"). It returns an empty token
// and no error if the client offered none, as a native client that sends
// an Authorization header does.
//
// It fails with an error wrapping ErrInvalid if the client did not offer
// Subprotocol, offered more than one token, or offered one that is empty
// or not unpadded base64url. The error never contains the token.
func TokenFromSubprotocols(offered []string) (string, error) {
	var v1 bool
	var tokens int
	var encoded string
	for _, line := range offered {
		for p := range strings.SplitSeq(line, ",") {
			p = strings.Trim(p, " \t")
			switch {
			case p == Subprotocol:
				v1 = true
			case strings.HasPrefix(p, tokenPrefix):
				tokens++
				encoded = p[len(tokenPrefix):]
			}
		}
	}
	switch {
	case !v1:
		return "", fmt.Errorf("%w: the client did not offer %s", ErrInvalid, Subprotocol)
	case tokens > 1:
		return "", fmt.Errorf("%w: the client offered %d tokens", ErrInvalid, tokens)
	case tokens == 0:
		return "", nil
	case encoded == "":
		return "", fmt.Errorf("%w: the client offered an empty token", ErrInvalid)
	}
	token, err := decodeBase64URL(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: the offered token is not base64url", ErrInvalid)
	}
	return string(token), nil
}
