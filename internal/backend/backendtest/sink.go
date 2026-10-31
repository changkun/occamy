// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package backendtest provides a recording sink and fake backends, for
// testing backends and sessions.
package backendtest

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"slices"
	"sync"

	"changkun.de/x/occamy/internal/backend"
)

// ErrNoAnswer is returned by a Sink's Prompt when Sink.Answer is nil.
var ErrNoAnswer = errors.New("backendtest: no answer")

// Copy is a CopyRect that a Sink recorded.
type Copy struct {
	Dst image.Rectangle
	Src image.Point
}

// Sink is a backend.Sink that records what a backend sends and keeps a
// framebuffer of its own. It panics when a backend breaks the Sink
// contract, so that tests fail at the mistake. The zero value is ready to
// use.
type Sink struct {
	// Answer answers prompts. If nil, Prompt returns ErrNoAnswer.
	Answer func(backend.Prompt) (backend.PromptReply, error)

	draw sync.Mutex // held between Lock and Unlock
	mu   sync.Mutex // guards the fields below and fb's pointer

	fb        *image.RGBA
	size      image.Point
	locked    image.Rectangle
	damage    []image.Rectangle
	copies    []Copy
	cursor    *image.RGBA
	hot       image.Point
	pos       image.Point
	term      []byte
	clipMIME  string
	clipData  []byte
	bells     int
	title     string
	prompts   []backend.Prompt
	resizes   int
	lockCount int
}

var _ backend.Sink = (*Sink)(nil)

// Resize implements backend.Sink. A graphical size replaces the
// framebuffer with a black one.
func (s *Sink) Resize(width, height int) {
	if width <= 0 || height <= 0 {
		panic(fmt.Sprintf("backendtest: Resize(%d, %d)", width, height))
	}
	s.draw.Lock()
	defer s.draw.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = image.Pt(width, height)
	s.fb = image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(s.fb, s.fb.Rect, image.Black, image.Point{}, draw.Src)
	s.resizes++
}

// Lock implements backend.Sink.
func (s *Sink) Lock(r image.Rectangle) *image.RGBA {
	s.draw.Lock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fb == nil || r.Empty() || !r.In(s.fb.Rect) {
		s.draw.Unlock()
		panic(fmt.Sprintf("backendtest: Lock(%v) outside the screen %v", r, s.screen()))
	}
	s.locked = r
	s.lockCount++
	return s.fb.SubImage(r).(*image.RGBA)
}

// Unlock implements backend.Sink.
func (s *Sink) Unlock(r image.Rectangle) {
	s.mu.Lock()
	if r != s.locked {
		s.mu.Unlock()
		panic(fmt.Sprintf("backendtest: Unlock(%v) after Lock(%v)", r, s.locked))
	}
	s.locked = image.Rectangle{}
	s.damage = append(s.damage, r)
	s.mu.Unlock()
	s.draw.Unlock()
}

// CopyRect implements backend.Sink.
func (s *Sink) CopyRect(dst image.Rectangle, src image.Point) {
	s.draw.Lock()
	defer s.draw.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	from := dst.Sub(dst.Min).Add(src)
	if s.fb == nil || dst.Empty() || !dst.In(s.fb.Rect) || !from.In(s.fb.Rect) {
		panic(fmt.Sprintf("backendtest: CopyRect(%v, %v) outside the screen %v", dst, src, s.screen()))
	}
	// draw.Draw handles overlapping source and destination in one image.
	draw.Draw(s.fb, dst, s.fb, src, draw.Src)
	s.copies = append(s.copies, Copy{Dst: dst, Src: src})
}

// Cursor implements backend.Sink.
func (s *Sink) Cursor(img *image.RGBA, hot image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor, s.hot = nil, hot
	if img != nil {
		s.cursor = image.NewRGBA(img.Rect)
		draw.Draw(s.cursor, img.Rect, img, img.Rect.Min, draw.Src)
	}
}

// CursorMove implements backend.Sink.
func (s *Sink) CursorMove(p image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pos = p
}

// TermData implements backend.Sink.
func (s *Sink) TermData(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.term = append(s.term, p...)
}

// Clipboard implements backend.Sink.
func (s *Sink) Clipboard(mime string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipMIME, s.clipData = mime, slices.Clone(data)
}

// Bell implements backend.Sink.
func (s *Sink) Bell() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bells++
}

// Title implements backend.Sink.
func (s *Sink) Title(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.title = title
}

// Prompt implements backend.Sink.
func (s *Sink) Prompt(ctx context.Context, p backend.Prompt) (backend.PromptReply, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, p)
	answer := s.Answer
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return backend.PromptReply{}, err
	}
	if answer == nil {
		return backend.PromptReply{}, ErrNoAnswer
	}
	return answer(p)
}

// screen is the framebuffer's bounds, for messages. s.mu must be held.
func (s *Sink) screen() image.Rectangle {
	if s.fb == nil {
		return image.Rectangle{}
	}
	return s.fb.Rect
}

// Size is the size last reported with Resize.
func (s *Sink) Size() image.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Resizes is the number of Resize calls.
func (s *Sink) Resizes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resizes
}

// Pixels returns a copy of the framebuffer, or nil before Resize.
func (s *Sink) Pixels() *image.RGBA {
	s.draw.Lock()
	defer s.draw.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fb == nil {
		return nil
	}
	fb := image.NewRGBA(s.fb.Rect)
	copy(fb.Pix, s.fb.Pix)
	return fb
}

// TakeDamage returns the rectangles unlocked since the last call.
func (s *Sink) TakeDamage() []image.Rectangle {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.damage
	s.damage = nil
	return d
}

// Locks is the number of Lock calls.
func (s *Sink) Locks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockCount
}

// Locked returns the rectangle a backend holds locked, empty if none, as
// after a panic that left it locked.
func (s *Sink) Locked() image.Rectangle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked
}

// Copies returns the recorded CopyRect calls.
func (s *Sink) Copies() []Copy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.copies)
}

// CursorImage returns the pointer's image, nil if hidden, and its hot
// spot. The image is the Sink's copy and must not be modified.
func (s *Sink) CursorImage() (*image.RGBA, image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, s.hot
}

// CursorPos is the position last reported with CursorMove.
func (s *Sink) CursorPos() image.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos
}

// Term returns all terminal output so far.
func (s *Sink) Term() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.term)
}

// ClipboardContent returns the clipboard last reported.
func (s *Sink) ClipboardContent() (mime string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipMIME, slices.Clone(s.clipData)
}

// Bells is the number of Bell calls.
func (s *Sink) Bells() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bells
}

// TitleText is the title last reported.
func (s *Sink) TitleText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// Prompts returns the prompts asked so far.
func (s *Sink) Prompts() []backend.Prompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.prompts)
}
