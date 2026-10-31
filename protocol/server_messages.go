// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

// Resize reports the size of the remote screen, in pixels for a
// graphical session and in character cells for a terminal. The frame it
// starts redraws the whole screen.
type Resize struct {
	Width, Height uint16
}

// Type returns TypeResize.
func (Resize) Type() Type { return TypeResize }

// Append appends the message to b.
func (m Resize) Append(b []byte) []byte {
	b = appendType(b, TypeResize)
	b = AppendUint16(b, m.Width)
	return AppendUint16(b, m.Height)
}

// Decode decodes body into m.
func (m *Resize) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Width = d.ReadUint16()
	m.Height = d.ReadUint16()
	return d.Finish()
}

// Tile draws a rectangle of pixels at X, Y.
//
// Decode checks that W and H are at least 1 and at most the maximum
// texture size, that Slot is within the cache slots, that a tile with a
// slot covers at most MaxCachedPixels, that a cached tile names a slot
// and has no payload, and that a raw tile has 4 bytes per pixel.
type Tile struct {
	FrameID    uint32
	X, Y, W, H uint16
	Codec      Codec
	// Slot is the client cache slot, from 1, that the client stores the
	// decoded tile in, or 0 to store nothing. For CodecCached, it is the
	// slot to draw.
	Slot uint16
	// Payload is the encoded pixels, empty for CodecCached. It aliases
	// the message.
	Payload []byte
}

// Type returns TypeTile.
func (Tile) Type() Type { return TypeTile }

// Append appends the message to b.
func (m Tile) Append(b []byte) []byte {
	b = appendType(b, TypeTile)
	b = AppendUint32(b, m.FrameID)
	b = AppendUint16(b, m.X)
	b = AppendUint16(b, m.Y)
	b = AppendUint16(b, m.W)
	b = AppendUint16(b, m.H)
	b = AppendUint8(b, uint8(m.Codec))
	b = AppendUint16(b, m.Slot)
	return AppendBytes(b, m.Payload)
}

// Decode decodes body into m.
func (m *Tile) Decode(body []byte, l Limits) error {
	d := NewDecoder(body)
	m.FrameID = d.ReadUint32()
	m.X = d.ReadUint16()
	m.Y = d.ReadUint16()
	m.W = d.readSide("tile width", l)
	m.H = d.readSide("tile height", l)
	if m.Codec = Codec(d.ReadUint8()); !m.Codec.valid() {
		d.failf(ErrInvalid, "tile codec %d", m.Codec)
	}
	if m.Slot = d.ReadUint16(); int(m.Slot) > max(l.CacheSlots, 0) {
		d.failf(ErrInvalid, "cache slot %d of %d", m.Slot, max(l.CacheSlots, 0))
	}
	m.Payload = d.ReadBytes()
	if err := d.Finish(); err != nil {
		return err
	}
	if m.Slot != 0 && int(m.W)*int(m.H) > MaxCachedPixels {
		d.failf(ErrInvalid, "%dx%d tile in cache slot %d, more than %d pixels", m.W, m.H, m.Slot, MaxCachedPixels)
	}
	switch m.Codec {
	case CodecCached:
		if m.Slot == 0 || len(m.Payload) != 0 {
			d.failf(ErrInvalid, "cached tile with slot %d and a %d-byte payload", m.Slot, len(m.Payload))
		}
	case CodecRaw:
		if len(m.Payload) != rawSize(m.W, m.H) {
			d.failf(ErrInvalid, "raw %dx%d tile with %d bytes", m.W, m.H, len(m.Payload))
		}
	}
	return d.Err()
}

// readSide reads the width or height of a tile: at least 1, and at most
// the maximum texture size.
func (d *Decoder) readSide(what string, l Limits) uint16 {
	v := d.ReadUint16()
	switch {
	case d.err != nil:
	case v == 0:
		d.failf(ErrInvalid, "%s 0", what)
	case int(v) > l.maxTextureSize():
		d.failf(ErrTooLarge, "%s %d, limit %d", what, v, l.maxTextureSize())
	}
	return v
}

// readCursorSide reads the width or height of a cursor: at most the
// maximum texture size, and zero for a hidden cursor.
func (d *Decoder) readCursorSide(what string, l Limits) uint16 {
	v := d.ReadUint16()
	if d.err == nil && int(v) > l.maxTextureSize() {
		d.failf(ErrTooLarge, "cursor %s %d, limit %d", what, v, l.maxTextureSize())
	}
	return v
}

// rawSize is the size of a raw image of w×h pixels.
func rawSize(w, h uint16) int { return int(w) * int(h) * 4 }

// CopyRect copies the W×H pixels at SrcX, SrcY to DstX, DstY, as the
// client's screen is after the messages before it, as when the remote
// scrolls or moves a window.
type CopyRect struct {
	FrameID          uint32
	SrcX, SrcY       uint16
	DstX, DstY, W, H uint16
}

// Type returns TypeCopyRect.
func (CopyRect) Type() Type { return TypeCopyRect }

// Append appends the message to b.
func (m CopyRect) Append(b []byte) []byte {
	b = appendType(b, TypeCopyRect)
	b = AppendUint32(b, m.FrameID)
	b = AppendUint16(b, m.SrcX)
	b = AppendUint16(b, m.SrcY)
	b = AppendUint16(b, m.DstX)
	b = AppendUint16(b, m.DstY)
	b = AppendUint16(b, m.W)
	return AppendUint16(b, m.H)
}

// Decode decodes body into m.
func (m *CopyRect) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.FrameID = d.ReadUint32()
	m.SrcX = d.ReadUint16()
	m.SrcY = d.ReadUint16()
	m.DstX = d.ReadUint16()
	m.DstY = d.ReadUint16()
	m.W = d.ReadUint16()
	m.H = d.ReadUint16()
	return d.Finish()
}

// FrameEnd closes a frame: the client draws what the frame's messages
// changed, and acknowledges it with FrameAck.
type FrameEnd struct {
	// FrameID is the frame's ID. Frame IDs start at 1 on each connection
	// and grow by one for each frame.
	FrameID uint32
	// ServerTime is when the server read the frame's content, in
	// microseconds since the session started.
	ServerTime uint64
	// LastSeq is the sequence number of the last input from this client
	// that the frame reflects, or 0 if none.
	LastSeq uint32
}

// Type returns TypeFrameEnd.
func (FrameEnd) Type() Type { return TypeFrameEnd }

// Append appends the message to b.
func (m FrameEnd) Append(b []byte) []byte {
	b = appendType(b, TypeFrameEnd)
	b = AppendUint32(b, m.FrameID)
	b = AppendUint64(b, m.ServerTime)
	return AppendUint32(b, m.LastSeq)
}

// Decode decodes body into m.
func (m *FrameEnd) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.FrameID = d.ReadUint32()
	m.ServerTime = d.ReadUint64()
	m.LastSeq = d.ReadUint32()
	return d.Finish()
}

// Cursor sets the pointer's image, which the client draws itself. The
// zero Cursor hides the pointer.
//
// Decode checks that W and H are at most the maximum texture size, that
// a hidden cursor (W and H zero) has no hot spot, codec or payload, and
// that a shown one has a codec other than CodecCached, its hot spot
// inside the image, and 4 bytes per pixel if it is raw.
type Cursor struct {
	// HotX and HotY are the hot spot within the image.
	HotX, HotY uint16
	W, H       uint16
	Codec      Codec
	// Payload is the encoded image. It aliases the message.
	Payload []byte
}

// Type returns TypeCursor.
func (Cursor) Type() Type { return TypeCursor }

// Append appends the message to b.
func (m Cursor) Append(b []byte) []byte {
	b = appendType(b, TypeCursor)
	b = AppendUint16(b, m.HotX)
	b = AppendUint16(b, m.HotY)
	b = AppendUint16(b, m.W)
	b = AppendUint16(b, m.H)
	b = AppendUint8(b, uint8(m.Codec))
	return AppendBytes(b, m.Payload)
}

// Decode decodes body into m.
func (m *Cursor) Decode(body []byte, l Limits) error {
	d := NewDecoder(body)
	m.HotX = d.ReadUint16()
	m.HotY = d.ReadUint16()
	m.W = d.readCursorSide("width", l)
	m.H = d.readCursorSide("height", l)
	m.Codec = Codec(d.ReadUint8())
	m.Payload = d.ReadBytes()
	if err := d.Finish(); err != nil {
		return err
	}
	switch {
	case m.W == 0 && m.H == 0:
		if m.HotX != 0 || m.HotY != 0 || m.Codec != 0 || len(m.Payload) != 0 {
			d.failf(ErrInvalid, "hidden cursor with a hot spot, codec or payload")
		}
	case m.W == 0 || m.H == 0:
		d.failf(ErrInvalid, "cursor of %dx%d", m.W, m.H)
	case !m.Codec.valid() || m.Codec == CodecCached:
		d.failf(ErrInvalid, "cursor codec %d", m.Codec)
	case m.HotX >= m.W || m.HotY >= m.H:
		d.failf(ErrInvalid, "hot spot %d,%d outside a %dx%d cursor", m.HotX, m.HotY, m.W, m.H)
	case m.Codec == CodecRaw && len(m.Payload) != rawSize(m.W, m.H):
		d.failf(ErrInvalid, "raw %dx%d cursor with %d bytes", m.W, m.H, len(m.Payload))
	}
	return d.Err()
}

// CursorMove reports that the remote moved the pointer, in pixels.
type CursorMove struct {
	X, Y uint16
}

// Type returns TypeCursorMove.
func (CursorMove) Type() Type { return TypeCursorMove }

// Append appends the message to b.
func (m CursorMove) Append(b []byte) []byte {
	b = appendType(b, TypeCursorMove)
	b = AppendUint16(b, m.X)
	return AppendUint16(b, m.Y)
}

// Decode decodes body into m.
func (m *CursorMove) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.X = d.ReadUint16()
	m.Y = d.ReadUint16()
	return d.Finish()
}

// TermData is the escape sequences that turn the client's terminal screen
// into the server's.
type TermData struct {
	// Data aliases the message.
	Data []byte
}

// Type returns TypeTermData.
func (TermData) Type() Type { return TypeTermData }

// Append appends the message to b.
func (m TermData) Append(b []byte) []byte {
	b = appendType(b, TypeTermData)
	return AppendBytes(b, m.Data)
}

// Decode decodes body into m.
func (m *TermData) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Data = d.ReadBytes()
	return d.Finish()
}

// TermSnapshot replaces the client's terminal: the client resets its
// terminal and feeds it Data, which reproduces the server's screen,
// cursor, attributes and modes.
type TermSnapshot struct {
	// Data aliases the message.
	Data []byte
}

// Type returns TypeTermSnapshot.
func (TermSnapshot) Type() Type { return TypeTermSnapshot }

// Append appends the message to b.
func (m TermSnapshot) Append(b []byte) []byte {
	b = appendType(b, TypeTermSnapshot)
	return AppendBytes(b, m.Data)
}

// Decode decodes body into m.
func (m *TermSnapshot) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Data = d.ReadBytes()
	return d.Finish()
}

// ServerClipboard is the remote clipboard's new content.
type ServerClipboard struct {
	MIME string
	// Data aliases the message.
	Data []byte
}

// Type returns TypeServerClipboard.
func (ServerClipboard) Type() Type { return TypeServerClipboard }

// Append appends the message to b.
func (m ServerClipboard) Append(b []byte) []byte {
	b = appendType(b, TypeServerClipboard)
	b = AppendString(b, m.MIME)
	return AppendBytes(b, m.Data)
}

// Decode decodes body into m.
func (m *ServerClipboard) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.MIME = d.ReadString()
	m.Data = d.ReadBytes()
	return d.Finish()
}

// Bell rings the bell. It has no fields.
type Bell struct{}

// Type returns TypeBell.
func (Bell) Type() Type { return TypeBell }

// Append appends the message to b.
func (Bell) Append(b []byte) []byte { return appendType(b, TypeBell) }

// Decode decodes body into m.
func (*Bell) Decode(body []byte, _ Limits) error { return NewDecoder(body).Finish() }

// Title is the remote's desktop or window title.
type Title struct {
	Title string
}

// Type returns TypeTitle.
func (Title) Type() Type { return TypeTitle }

// Append appends the message to b.
func (m Title) Append(b []byte) []byte {
	b = appendType(b, TypeTitle)
	return AppendString(b, m.Title)
}

// Decode decodes body into m.
func (m *Title) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Title = d.ReadString()
	return d.Finish()
}

// Prompt asks the owner for answers, such as an SSH keyboard-interactive
// challenge or a password the connection lacks. Only the owner gets it,
// and answers with a PromptReply of the same ID.
type Prompt struct {
	ID uint32
	// Name and Instruction describe the request; either may be empty.
	Name, Instruction string
	Fields            []PromptField
}

// PromptField is one answer that a prompt asks for.
type PromptField struct {
	Label string
	// Secret answers, such as passwords, are not echoed.
	Secret bool
}

// promptFieldMinSize is the size of a prompt field with an empty label.
const promptFieldMinSize = lengthSize + 1

// Type returns TypePrompt.
func (Prompt) Type() Type { return TypePrompt }

// Append appends the message to b.
func (m Prompt) Append(b []byte) []byte {
	b = appendType(b, TypePrompt)
	b = AppendUint32(b, m.ID)
	b = AppendString(b, m.Name)
	b = AppendString(b, m.Instruction)
	b = AppendCount(b, len(m.Fields))
	for _, f := range m.Fields {
		b = AppendString(b, f.Label)
		b = AppendBool(b, f.Secret)
	}
	return b
}

// Decode decodes body into m.
func (m *Prompt) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.ID = d.ReadUint32()
	m.Name = d.ReadString()
	m.Instruction = d.ReadString()
	m.Fields = nil
	if n := d.ReadCount(promptFieldMinSize); n > 0 {
		m.Fields = make([]PromptField, n)
		for i := range m.Fields {
			m.Fields[i].Label = d.ReadString()
			m.Fields[i].Secret = d.ReadBool()
		}
	}
	return d.Finish()
}

// ViewerJoined reports a viewer in the session: one that joins, and on
// joining, each viewer already there.
type ViewerJoined struct {
	ViewerID uint32
	Role     Role
	// Name is the viewer's user name, or empty if its token names none.
	Name string
}

// Type returns TypeViewerJoined.
func (ViewerJoined) Type() Type { return TypeViewerJoined }

// Append appends the message to b.
func (m ViewerJoined) Append(b []byte) []byte {
	b = appendType(b, TypeViewerJoined)
	b = AppendUint32(b, m.ViewerID)
	b = AppendUint8(b, uint8(m.Role))
	return AppendString(b, m.Name)
}

// Decode decodes body into m.
func (m *ViewerJoined) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.ViewerID = d.ReadUint32()
	if m.Role = Role(d.ReadUint8()); !m.Role.valid() {
		d.failf(ErrInvalid, "role %d", m.Role)
	}
	m.Name = d.ReadString()
	return d.Finish()
}

// ViewerLeft reports that a viewer left the session.
type ViewerLeft struct {
	ViewerID uint32
}

// Type returns TypeViewerLeft.
func (ViewerLeft) Type() Type { return TypeViewerLeft }

// Append appends the message to b.
func (m ViewerLeft) Append(b []byte) []byte {
	b = appendType(b, TypeViewerLeft)
	return AppendUint32(b, m.ViewerID)
}

// Decode decodes body into m.
func (m *ViewerLeft) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.ViewerID = d.ReadUint32()
	return d.Finish()
}
