// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session

import (
	"image"
	"sync"

	"changkun.de/x/occamy/internal/backend"
)

// Viewer is one connection to a session. Its methods are safe for
// concurrent use: the connection's reader gives input and reports what the
// viewer can see, and its writer takes the small state to send.
type Viewer struct {
	s    *Session
	id   int
	role Role
	wake chan struct{} // holds a token while pending has news

	mu         sync.Mutex
	pending    Pending
	visible    bool
	viewport   image.Rectangle
	left       bool
	lastSeq    uint32
	inputClock uint64
}

// Pending is the small state that changed since the viewer's writer last
// took it. Each field holds the latest value, nil if it did not change,
// so nothing queues behind a slow writer.
type Pending struct {
	Resize    *image.Point
	Cursor    *Cursor
	CursorPos *image.Point
	Clipboard *Clipboard
	Title     *string
	Bells     int
	Joined    []ViewerInfo
	Left      []int
	Prompts   []PromptRequest
	// Input reports that the session applied an input from this viewer,
	// so the frame that carries its effect should go out first.
	Input bool
	// End is set once the session has ended; the writer sends close.
	End *End
}

// Cursor is the pointer's image, nil when hidden, and its hot spot.
type Cursor struct {
	Image *image.RGBA
	Hot   image.Point
}

// Clipboard is the remote clipboard's content.
type Clipboard struct {
	MIME string
	Data []byte
}

// ViewerInfo describes another viewer of the session.
type ViewerInfo struct {
	ID   int
	Role Role
}

// PromptRequest is a backend's question for the owner, answered with
// Viewer.Answer.
type PromptRequest struct {
	ID     uint32
	Prompt backend.Prompt
}

// ID identifies the viewer within its session.
func (v *Viewer) ID() int { return v.id }

// Role is what the viewer may do.
func (v *Viewer) Role() Role { return v.role }

// Session is the session the viewer belongs to.
func (v *Viewer) Session() *Session { return v.s }

// Wake receives a value when Take has news.
func (v *Viewer) Wake() <-chan struct{} { return v.wake }

// Take returns the small state that changed since the last call and
// clears it.
func (v *Viewer) Take() Pending {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.pending
	v.pending = Pending{}
	return p
}

// Input applies an input from the viewer, which the client numbered seq.
// A read-only viewer's input is dropped and counted.
func (v *Viewer) Input(seq uint32, ev backend.InputEvent) error {
	return v.s.do(func() error {
		v.mu.Lock()
		left := v.left
		v.mu.Unlock()
		if left {
			return ErrLeft
		}
		if v.role == RoleReadOnly {
			v.s.dropped.Add(1)
			return nil
		}
		err := v.s.backend.Input(ev)
		var clock uint64
		if v.s.fb != nil {
			clock = v.s.fb.Clock()
		}
		v.mu.Lock()
		v.lastSeq, v.inputClock = seq, clock
		v.mu.Unlock()
		v.update(func(p *Pending) { p.Input = true })
		v.s.idle.Reset(v.s.cfg.IdleTimeout)
		return err
	})
}

// LastInput reports the sequence number of the last input the session
// applied from the viewer, and the framebuffer's clock at that moment:
// tiles above it changed after the input.
func (v *Viewer) LastInput() (seq uint32, clock uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lastSeq, v.inputClock
}

// View reports whether the viewer's client can see the session, and the
// part of the screen its viewport shows; an empty viewport means the
// whole screen.
func (v *Viewer) View(visible bool, viewport image.Rectangle) error {
	v.mu.Lock()
	v.visible, v.viewport = visible, viewport
	v.mu.Unlock()
	return v.s.do(func() error {
		v.s.updateDemand()
		return nil
	})
}

// Visibility reports what View last set.
func (v *Viewer) Visibility() (visible bool, viewport image.Rectangle) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.visible, v.viewport
}

// Answer answers the backend's prompt id. Only the owner answers.
func (v *Viewer) Answer(id uint32, reply backend.PromptReply) error {
	s := v.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != v {
		return ErrNotOwner
	}
	if r, ok := s.prompts[id]; ok {
		select {
		case r.reply <- reply:
		default: // already answered
		}
	}
	return nil
}

// Leave removes the viewer from the session. The owner's leaving starts
// the grace period.
func (v *Viewer) Leave() {
	v.mu.Lock()
	already := v.left
	v.left = true
	v.visible = false
	v.mu.Unlock()
	if already {
		return
	}
	_ = v.s.do(func() error {
		v.s.removeViewer(v)
		return nil
	})
}

// update changes the pending state and wakes the writer.
func (v *Viewer) update(f func(p *Pending)) {
	v.mu.Lock()
	f(&v.pending)
	v.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
}
