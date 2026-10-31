// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package backend defines how a session drives a connection to a remote
// screen or terminal. A backend connects to the remote server, draws into
// the framebuffer or feeds the terminal that the session owns, and applies
// the viewers' input. It never encodes for viewers.
package backend

import (
	"context"
	"image"
	"strconv"
)

// Class is the kind of content a backend produces.
type Class int

const (
	// Terminal backends produce output for the session's terminal emulator.
	Terminal Class = iota + 1
	// Graphical backends draw pixels into the session's framebuffer.
	Graphical
)

func (c Class) String() string {
	switch c {
	case Terminal:
		return "terminal"
	case Graphical:
		return "graphical"
	}
	return "Class(" + strconv.Itoa(int(c)) + ")"
}

// Backend is one connection to a remote server, owned by one session.
//
// The session calls Run once. It calls Input and Demand from its owner
// goroutine, one call at a time, concurrently with Run. They may still
// come for a moment after Run returns, until the session notices, and
// must then return without blocking or panicking; Input returns an
// error.
type Backend interface {
	// Class reports the kind of content the backend produces. It never
	// changes.
	Class() Class
	// Run connects to the remote server and serves it until ctx is done
	// or the remote ends the session. It returns nil when the remote ends
	// the session normally, ctx.Err() when ctx is done first, and another
	// error when the connection fails.
	Run(ctx context.Context, sink Sink) error
	// Input applies a viewer's input. It must return quickly, since the
	// session's owner goroutine waits for it: a backend queues what it
	// cannot send at once, and returns an error when its queue is full.
	Input(ev InputEvent) error
	// Demand tells the backend what the viewers ask of the remote server.
	// A backend asks for or accepts screen updates only while d.Visible,
	// so that the remote server is paced as the viewers are. It must
	// return quickly.
	Demand(d Demand)
}

// Demand is what the session's viewers ask of the remote server.
type Demand struct {
	// Visible is whether at least one viewer can see the screen.
	Visible bool
}

// Sink receives a backend's output. The session implements it.
//
// Its methods are safe for concurrent use, never wait for viewers except
// in Prompt, and must not be called after Run returns. Slices and images
// passed to them are borrowed for the call only.
type Sink interface {
	// Resize reports the size of the remote screen, in pixels for a
	// graphical backend and in character cells for a terminal. A graphical
	// backend draws the whole screen after it.
	Resize(width, height int)
	// Lock returns the framebuffer's pixels in r, which must lie within the
	// screen, locked for drawing. The backend draws and then calls Unlock
	// with the same r, which releases the pixels and records r as damage.
	// A goroutine that holds a lock calls no other Sink method until it
	// unlocks. Graphical backends only.
	Lock(r image.Rectangle) *image.RGBA
	Unlock(r image.Rectangle)
	// CopyRect copies the pixels of dst's size at src to dst, as when the
	// remote scrolls or moves a window. Graphical backends only.
	CopyRect(dst image.Rectangle, src image.Point)
	// Cursor sets the pointer's image and hot spot. A nil img hides the
	// pointer.
	Cursor(img *image.RGBA, hot image.Point)
	// CursorMove reports that the remote moved the pointer to p.
	CursorMove(p image.Point)
	// TermData feeds the remote's output to the session's terminal
	// emulator. Terminal backends only.
	TermData(p []byte)
	// Clipboard reports the remote clipboard's new content.
	Clipboard(mime string, data []byte)
	// Bell rings the bell.
	Bell()
	// Title reports the remote's desktop or window title.
	Title(title string)
	// Prompt asks the session's owner for answers, such as an SSH
	// keyboard-interactive challenge or a VNC password, and waits until
	// the owner answers, ctx is done, or the session ends.
	Prompt(ctx context.Context, p Prompt) (PromptReply, error)
}

// Prompt is a request for answers from the session's owner.
type Prompt struct {
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

// PromptReply holds the answers in the order of the prompt's fields.
type PromptReply struct {
	Answers []string
}

// InputEvent is a viewer's input: a Key, Pointer, Wheel, Text, TermInput,
// Clipboard or Resize.
type InputEvent interface{ input() }

// Key is a key press or release.
type Key struct {
	Down bool
	// Keysym is the X11 keysym that the key produced.
	Keysym uint32
	// Code is the physical key as a USB HID usage from the keyboard page
	// (0x07), or 0 if the client does not know it.
	Code uint16
}

// Buttons is a set of pointer buttons.
type Buttons uint8

// The pointer buttons.
const (
	ButtonLeft Buttons = 1 << iota
	ButtonMiddle
	ButtonRight
	ButtonBack
	ButtonForward
)

// Pointer is the pointer's position on the screen and the buttons held.
type Pointer struct {
	X, Y    int
	Buttons Buttons
}

// WheelMode is the unit of a Wheel's deltas.
type WheelMode uint8

// The wheel modes.
const (
	WheelPixels WheelMode = iota
	WheelLines
)

// Wheel is a scroll: positive DX scrolls right and positive DY down.
type Wheel struct {
	DX, DY int
	Mode   WheelMode
}

// Text is text from an input method, in UTF-8.
type Text struct {
	Text string
}

// TermInput is the bytes that a viewer's terminal emulator produced, such
// as xterm.js's onData.
type TermInput struct {
	Data []byte
}

// Clipboard is a viewer's clipboard content.
type Clipboard struct {
	MIME string
	Data []byte
}

// Resize asks the remote to change the screen's size, in pixels for a
// graphical backend and in character cells for a terminal. Clients ask
// for sizes, so occamyd clamps what they ask for before a backend sees
// it: a graphical screen's sides to MaxResizeSide, and a terminal's to
// the terminal emulator's MaxCols and MaxRows.
type Resize struct {
	Width, Height int
}

// MaxResizeSide is the largest width and height, in pixels, that a
// Resize asks of a graphical backend.
const MaxResizeSide = 8192

func (Key) input()       {}
func (Pointer) input()   {}
func (Wheel) input()     {}
func (Text) input()      {}
func (TermInput) input() {}
func (Clipboard) input() {}
func (Resize) input()    {}
