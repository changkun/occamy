// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

// Every input message starts with a sequence number. A client numbers
// its input messages from 1, one more for each, and FrameEnd reports the
// last one a frame reflects, so the client can measure the time from an
// input to its effect.

// Key is a key press or release.
type Key struct {
	Seq  uint32
	Down bool
	// Keysym is the X11 keysym that the key produced.
	Keysym uint32
	// Code is the physical key as a USB HID usage from the keyboard page
	// (0x07), or 0 if the client does not know it.
	Code uint16
}

// Type returns TypeKey.
func (Key) Type() Type { return TypeKey }

// Append appends the message to b.
func (m Key) Append(b []byte) []byte {
	b = appendType(b, TypeKey)
	b = AppendUint32(b, m.Seq)
	b = AppendBool(b, m.Down)
	b = AppendUint32(b, m.Keysym)
	return AppendUint16(b, m.Code)
}

// Decode decodes body into m.
func (m *Key) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.Down = d.ReadBool()
	m.Keysym = d.ReadUint32()
	m.Code = d.ReadUint16()
	return d.Finish()
}

// Text is text that an input method composed.
type Text struct {
	Seq  uint32
	Text string
}

// Type returns TypeText.
func (Text) Type() Type { return TypeText }

// Append appends the message to b.
func (m Text) Append(b []byte) []byte {
	b = appendType(b, TypeText)
	b = AppendUint32(b, m.Seq)
	return AppendString(b, m.Text)
}

// Decode decodes body into m.
func (m *Text) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.Text = d.ReadString()
	return d.Finish()
}

// TermInput is the bytes that the client's terminal emulator produced,
// such as xterm.js's onData.
type TermInput struct {
	Seq  uint32
	Data []byte
}

// Type returns TypeTermInput.
func (TermInput) Type() Type { return TypeTermInput }

// Append appends the message to b.
func (m TermInput) Append(b []byte) []byte {
	b = appendType(b, TypeTermInput)
	b = AppendUint32(b, m.Seq)
	return AppendBytes(b, m.Data)
}

// Decode decodes body into m.
func (m *TermInput) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.Data = d.ReadBytes()
	return d.Finish()
}

// Buttons is a set of pointer buttons. The bits above ButtonForward are
// reserved: senders leave them zero and receivers ignore them.
type Buttons uint8

// The pointer buttons.
const (
	ButtonLeft Buttons = 1 << iota
	ButtonMiddle
	ButtonRight
	ButtonBack
	ButtonForward
)

// Pointer is the pointer's position on the screen, in pixels, and the
// buttons held.
type Pointer struct {
	Seq     uint32
	X, Y    uint16
	Buttons Buttons
}

// Type returns TypePointer.
func (Pointer) Type() Type { return TypePointer }

// Append appends the message to b.
func (m Pointer) Append(b []byte) []byte {
	b = appendType(b, TypePointer)
	b = AppendUint32(b, m.Seq)
	b = AppendUint16(b, m.X)
	b = AppendUint16(b, m.Y)
	return AppendUint8(b, uint8(m.Buttons))
}

// Decode decodes body into m.
func (m *Pointer) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.X = d.ReadUint16()
	m.Y = d.ReadUint16()
	m.Buttons = Buttons(d.ReadUint8())
	return d.Finish()
}

// WheelMode is the unit of a Wheel's deltas.
type WheelMode uint8

// The wheel modes.
const (
	WheelPixels WheelMode = 0
	WheelLines  WheelMode = 1
)

// Wheel is a scroll: positive DX scrolls right and positive DY down.
type Wheel struct {
	Seq    uint32
	DX, DY int32
	Mode   WheelMode
}

// Type returns TypeWheel.
func (Wheel) Type() Type { return TypeWheel }

// Append appends the message to b.
func (m Wheel) Append(b []byte) []byte {
	b = appendType(b, TypeWheel)
	b = AppendUint32(b, m.Seq)
	b = AppendInt32(b, m.DX)
	b = AppendInt32(b, m.DY)
	return AppendUint8(b, uint8(m.Mode))
}

// Decode decodes body into m.
func (m *Wheel) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.DX = d.ReadInt32()
	m.DY = d.ReadInt32()
	if m.Mode = WheelMode(d.ReadUint8()); m.Mode > WheelLines {
		d.failf(ErrInvalid, "wheel mode %d", m.Mode)
	}
	return d.Finish()
}

// ResizeRequest asks the remote to change the screen's size, in pixels
// for a graphical session and in character cells for a terminal.
type ResizeRequest struct {
	Seq           uint32
	Width, Height uint16
}

// Type returns TypeResizeRequest.
func (ResizeRequest) Type() Type { return TypeResizeRequest }

// Append appends the message to b.
func (m ResizeRequest) Append(b []byte) []byte {
	b = appendType(b, TypeResizeRequest)
	b = AppendUint32(b, m.Seq)
	b = AppendUint16(b, m.Width)
	return AppendUint16(b, m.Height)
}

// Decode decodes body into m.
func (m *ResizeRequest) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.Width = d.ReadUint16()
	m.Height = d.ReadUint16()
	return d.Finish()
}

// ClientClipboard is the client's clipboard content, for the remote's
// clipboard.
type ClientClipboard struct {
	Seq  uint32
	MIME string
	Data []byte
}

// Type returns TypeClientClipboard.
func (ClientClipboard) Type() Type { return TypeClientClipboard }

// Append appends the message to b.
func (m ClientClipboard) Append(b []byte) []byte {
	b = appendType(b, TypeClientClipboard)
	b = AppendUint32(b, m.Seq)
	b = AppendString(b, m.MIME)
	return AppendBytes(b, m.Data)
}

// Decode decodes body into m.
func (m *ClientClipboard) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Seq = d.ReadUint32()
	m.MIME = d.ReadString()
	m.Data = d.ReadBytes()
	return d.Finish()
}

// View reports what the client shows. A hidden client gets no frames, and
// tiles outside the viewport wait until they come into view.
type View struct {
	// Visible is false while the client cannot be seen, such as in a
	// hidden browser tab or a minimized window.
	Visible bool
	// X, Y, W and H are the part of the screen that the viewport shows,
	// in the session's units. A zero W or H means the whole screen.
	X, Y, W, H uint16
}

// Type returns TypeView.
func (View) Type() Type { return TypeView }

// Append appends the message to b.
func (m View) Append(b []byte) []byte {
	b = appendType(b, TypeView)
	b = AppendBool(b, m.Visible)
	b = AppendUint16(b, m.X)
	b = AppendUint16(b, m.Y)
	b = AppendUint16(b, m.W)
	return AppendUint16(b, m.H)
}

// Decode decodes body into m.
func (m *View) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.Visible = d.ReadBool()
	m.X = d.ReadUint16()
	m.Y = d.ReadUint16()
	m.W = d.ReadUint16()
	m.H = d.ReadUint16()
	return d.Finish()
}

// FrameAck acknowledges that the client drew a frame and every frame
// before it.
type FrameAck struct {
	FrameID uint32
	// DecodeTime is how long the client took to decode and draw the
	// frame, in microseconds.
	DecodeTime uint32
}

// Type returns TypeFrameAck.
func (FrameAck) Type() Type { return TypeFrameAck }

// Append appends the message to b.
func (m FrameAck) Append(b []byte) []byte {
	b = appendType(b, TypeFrameAck)
	b = AppendUint32(b, m.FrameID)
	return AppendUint32(b, m.DecodeTime)
}

// Decode decodes body into m.
func (m *FrameAck) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.FrameID = d.ReadUint32()
	m.DecodeTime = d.ReadUint32()
	return d.Finish()
}

// PromptReply answers a Prompt. Only the owner sends it.
type PromptReply struct {
	// ID is the ID of the prompt it answers.
	ID uint32
	// Answers holds one answer per field of the prompt, in order. They
	// may be secrets, such as passwords, and are never logged.
	Answers []string
}

// Type returns TypePromptReply.
func (PromptReply) Type() Type { return TypePromptReply }

// Append appends the message to b.
func (m PromptReply) Append(b []byte) []byte {
	b = appendType(b, TypePromptReply)
	b = AppendUint32(b, m.ID)
	b = AppendCount(b, len(m.Answers))
	for _, a := range m.Answers {
		b = AppendString(b, a)
	}
	return b
}

// Decode decodes body into m.
func (m *PromptReply) Decode(body []byte, _ Limits) error {
	d := NewDecoder(body)
	m.ID = d.ReadUint32()
	m.Answers = nil
	if n := d.ReadCount(lengthSize); n > 0 {
		m.Answers = make([]string, n)
		for i := range m.Answers {
			m.Answers[i] = d.ReadString()
		}
	}
	return d.Finish()
}
