// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"image"
	"time"

	"changkun.de/x/occamy/protocol"
)

// An Event is what Next delivers: one of Frame, Resize, TermSnapshot,
// TermData, Cursor, CursorMove, Clipboard, Bell, Title, Prompt,
// ViewerJoined, ViewerLeft and End.
type Event interface{ event() }

// Frame closes a frame. For a graphical session, the framebuffer holds
// the frame once Next returns it, and Dirty lists the rectangles it
// changed: tiles and copy-rect destinations in the order they came, or
// the whole screen after a Resize. For a terminal session, the frame's
// Resize, TermSnapshot and TermData came just before it. Next
// acknowledges the frame as it delivers it.
type Frame struct {
	// ID is the frame's ID, from 1 on each connection, wrapping from
	// 2³²−1 to 0.
	ID uint32
	// LastSeq is the sequence number of the last input of this client
	// that the server applied before it finished reading the frame's
	// content, or 0. The frame shows the effect of no later input. An
	// input's effect may show only in a later frame when the remote end
	// answers late, as a remote shell's echo does.
	LastSeq uint32
	// ServerTime is when the server read the frame's content, since the
	// session started.
	ServerTime time.Duration
	// Dirty are the rectangles a graphical frame changed.
	Dirty []image.Rectangle

	arrived time.Time // when frame-end arrived
}

// Resize reports the screen's new size, in pixels for a graphical
// session and in character cells for a terminal. It starts a frame,
// which redraws the whole screen. For a graphical session the
// framebuffer already holds that frame, whose Frame comes next.
type Resize struct {
	Width, Height int
}

// TermSnapshot replaces the terminal: the caller resets its terminal
// emulator and writes Data to it, as the server sent it. A terminal
// session's first frame carries one. Data is the caller's.
type TermSnapshot struct {
	Data []byte
}

// TermData is escape sequences that update the terminal, as the server
// sent them, for the caller to write to its terminal emulator. A frame
// larger than a message comes as several, each ending at a character
// boundary. Data is the caller's.
type TermData struct {
	Data []byte
}

// Cursor sets the pointer's image, which the caller draws at the local
// pointer with its hot spot there. A nil Image hides the pointer. The
// image has straight alpha, as raw pixels do.
type Cursor struct {
	Image *image.NRGBA
	Hot   image.Point
}

// CursorMove reports that the remote moved the pointer, in pixels.
type CursorMove struct {
	X, Y int
}

// Clipboard is the remote clipboard's new content. Data is the caller's.
type Clipboard struct {
	MIME string
	Data []byte
}

// Bell rings the bell.
type Bell struct{}

// Title is the remote's desktop or window title.
type Title struct {
	Title string
}

// Prompt asks the owner for answers, such as a password the connection
// lacks; PromptReply answers it once, with one answer per field.
type Prompt struct {
	ID          uint32
	Name        string
	Instruction string
	Fields      []protocol.PromptField
}

// ViewerJoined reports a viewer in the session: one that joined, and,
// right after welcome, each viewer already there.
type ViewerJoined struct {
	ViewerID uint32
	Role     protocol.Role
	Name     string
}

// ViewerLeft reports that a viewer left.
type ViewerLeft struct {
	ViewerID uint32
}

// End is the last event: the session ended.
type End struct {
	// Code is why: the server's close code, CloseNormal after Close, or
	// the code the client closed with when the server broke the
	// protocol. It is zero if the connection failed without one.
	Code protocol.CloseCode
	// Reason describes the code, for people and logs.
	Reason string
	// Err is the error that ended the session on the client's side: the
	// server's protocol error, or the connection's failure.
	Err error
}

func (Frame) event()        {}
func (Resize) event()       {}
func (TermSnapshot) event() {}
func (TermData) event()     {}
func (Cursor) event()       {}
func (CursorMove) event()   {}
func (Clipboard) event()    {}
func (Bell) event()         {}
func (Title) event()        {}
func (Prompt) event()       {}
func (ViewerJoined) event() {}
func (ViewerLeft) event()   {}
func (End) event()          {}
