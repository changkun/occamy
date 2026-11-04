// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build darwin && cgo

// Command core is the C export layer of the Go client core for the
// macOS app (specs/010-native-swift-client.md). Built with
// -buildmode=c-archive it is liboccamy.a, whose interface is occamy.h:
// a session is a handle, events are pulled with occamy_next, and the
// framebuffer is copied out by rectangle, so that no Go pointer reaches
// C. The app plugs in the decoders of every codec occamyd sends.
//
// occamyd does not link it: cgo is the client's boundary alone, and the
// server builds with CGO_ENABLED=0.
package main

/*
#define OCCAMY_CGO
#include <stdlib.h>
#include <string.h>
#include "occamy.h"
*/
import "C"

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"
	"unsafe"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/client/decoders"
	"changkun.de/x/occamy/protocol"
)

func main() {}

// defaultName names the app in hello when the caller names nothing.
const defaultName = "occamy-macos"

// session is a handle's session, with the error of its last failed
// call.
type session struct {
	s *client.Session

	mu  sync.Mutex
	err string
}

// fail records err as the session's last error and returns OCCAMY_ERR,
// or the result that a client error of its own maps to.
func (ss *session) fail(err error) C.int {
	switch {
	case errors.Is(err, client.ErrClosed):
		return C.OCCAMY_ERR_CLOSED
	case errors.Is(err, client.ErrReadOnly):
		return C.OCCAMY_ERR_READ_ONLY
	case errors.Is(err, client.ErrNotOwner):
		return C.OCCAMY_ERR_NOT_OWNER
	}
	ss.mu.Lock()
	ss.err = err.Error()
	ss.mu.Unlock()
	return C.OCCAMY_ERR
}

// The handles. A handle is a number that the core gives out once, never
// a pointer, so that an unknown or released one is refused rather than
// followed.
var (
	mu       sync.Mutex
	sessions = map[C.occamy_session]*session{}
	last     C.occamy_session
)

func register(s *client.Session) C.occamy_session {
	mu.Lock()
	defer mu.Unlock()
	last++
	sessions[last] = &session{s: s}
	return last
}

func lookup(h C.occamy_session) *session {
	mu.Lock()
	defer mu.Unlock()
	return sessions[h]
}

// within returns a context that ends after ms milliseconds, or never if
// ms is negative.
func within(ms C.int32_t) (context.Context, context.CancelFunc) {
	if ms < 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(ms)*time.Millisecond)
}

// cString returns s in C memory, or NULL if it is empty.
func cString(s string) *C.char {
	if s == "" {
		return nil
	}
	return C.CString(s)
}

// cBytes returns b in C memory, or NULL if it is empty.
func cBytes(b []byte) *C.uint8_t {
	if len(b) == 0 {
		return nil
	}
	return (*C.uint8_t)(C.CBytes(b))
}

// goBytes returns a copy of n bytes at p.
func goBytes(p *C.uint8_t, n C.size_t) []byte {
	if p == nil || n == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(p), C.int(n))
}

//export occamy_dial
func occamy_dial(url, token *C.char, opts *C.occamy_options, timeoutMS C.int32_t, out *C.occamy_session, cerr *C.occamy_dial_error) C.int {
	fail := func(err error) C.int {
		if cerr != nil {
			*cerr = C.occamy_dial_error{message: cString(err.Error())}
			var ce *client.CloseError
			if errors.As(err, &ce) {
				cerr.close_code = C.int32_t(ce.Code)
				cerr.close_reason = cString(ce.Reason)
			}
			var re *client.RefusedError
			if errors.As(err, &re) {
				cerr.session_id = cString(re.SessionID)
			}
		}
		return C.OCCAMY_ERR
	}
	if url == nil || out == nil {
		return fail(errors.New("occamy_dial: url and out are required"))
	}
	o := client.Options{Name: defaultName, Decoders: decoders.All()}
	if opts != nil {
		if opts.name != nil {
			o.Name = C.GoString(opts.name)
		}
		o.Width, o.Height = uint16(opts.width), uint16(opts.height)
		o.Cols, o.Rows = uint16(opts.cols), uint16(opts.rows)
		o.PixelRatio = uint16(opts.pixel_ratio)
		o.Hidden = bool(opts.hidden)
	}
	var tok string
	if token != nil {
		tok = C.GoString(token)
	}
	ctx, cancel := within(timeoutMS)
	defer cancel()
	s, err := client.Dial(ctx, C.GoString(url), tok, o)
	if err != nil {
		return fail(err)
	}
	*out = register(s)
	return C.OCCAMY_OK
}

//export occamy_dial_error_free
func occamy_dial_error_free(e *C.occamy_dial_error) {
	if e == nil {
		return
	}
	C.free(unsafe.Pointer(e.close_reason))
	C.free(unsafe.Pointer(e.message))
	C.free(unsafe.Pointer(e.session_id))
	*e = C.occamy_dial_error{}
}

//export occamy_next
func occamy_next(h C.occamy_session, timeoutMS C.int32_t, ev *C.occamy_event) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	if ev == nil {
		return ss.fail(errors.New("occamy_next: ev is NULL"))
	}
	ctx, cancel := within(timeoutMS)
	defer cancel()
	e, err := ss.s.Next(ctx)
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		return C.OCCAMY_TIMEOUT
	default:
		return ss.fail(err)
	}
	*ev = event(e)
	return C.OCCAMY_OK
}

// event is e in C memory.
func event(e client.Event) C.occamy_event {
	var c C.occamy_event
	switch e := e.(type) {
	case client.Frame:
		c.kind = C.OCCAMY_EVENT_FRAME
		c.id = C.uint32_t(e.ID)
		c.last_seq = C.uint32_t(e.LastSeq)
		c.server_time_us = C.int64_t(e.ServerTime.Microseconds())
		if n := len(e.Dirty); n > 0 {
			c.rects = (*C.occamy_rect)(C.malloc(C.size_t(n) * C.size_t(unsafe.Sizeof(C.occamy_rect{}))))
			rs := unsafe.Slice(c.rects, n)
			for i, r := range e.Dirty {
				rs[i] = rect(r)
			}
			c.rect_count = C.size_t(n)
		}
	case client.Resize:
		c.kind = C.OCCAMY_EVENT_RESIZE
		c.width, c.height = C.int32_t(e.Width), C.int32_t(e.Height)
	case client.TermSnapshot:
		c.kind = C.OCCAMY_EVENT_TERM_SNAPSHOT
		c.data, c.data_len = cBytes(e.Data), C.size_t(len(e.Data))
	case client.TermData:
		c.kind = C.OCCAMY_EVENT_TERM_DATA
		c.data, c.data_len = cBytes(e.Data), C.size_t(len(e.Data))
	case client.Cursor:
		c.kind = C.OCCAMY_EVENT_CURSOR
		if e.Image != nil {
			b := e.Image.Rect
			pix := make([]byte, 0, 4*b.Dx()*b.Dy())
			for y := b.Min.Y; y < b.Max.Y; y++ {
				i := e.Image.PixOffset(b.Min.X, y)
				pix = append(pix, e.Image.Pix[i:i+4*b.Dx()]...)
			}
			c.width, c.height = C.int32_t(b.Dx()), C.int32_t(b.Dy())
			c.x, c.y = C.int32_t(e.Hot.X), C.int32_t(e.Hot.Y)
			c.data, c.data_len = cBytes(pix), C.size_t(len(pix))
		}
	case client.CursorMove:
		c.kind = C.OCCAMY_EVENT_CURSOR_MOVE
		c.x, c.y = C.int32_t(e.X), C.int32_t(e.Y)
	case client.Clipboard:
		c.kind = C.OCCAMY_EVENT_CLIPBOARD
		c.text = cString(e.MIME)
		c.data, c.data_len = cBytes(e.Data), C.size_t(len(e.Data))
	case client.ClipboardStatus:
		c.kind = C.OCCAMY_EVENT_CLIPBOARD_STATUS
		if e.Answering {
			c.code = 1
		}
	case client.Bell:
		c.kind = C.OCCAMY_EVENT_BELL
	case client.Title:
		c.kind = C.OCCAMY_EVENT_TITLE
		c.text = cString(e.Title)
	case client.Prompt:
		c.kind = C.OCCAMY_EVENT_PROMPT
		c.id = C.uint32_t(e.ID)
		c.text, c.detail = cString(e.Name), cString(e.Instruction)
		if n := len(e.Fields); n > 0 {
			c.fields = (*C.occamy_prompt_field)(C.malloc(C.size_t(n) * C.size_t(unsafe.Sizeof(C.occamy_prompt_field{}))))
			fs := unsafe.Slice(c.fields, n)
			for i, f := range e.Fields {
				fs[i] = C.occamy_prompt_field{label: cString(f.Label), secret: C.bool(f.Secret)}
			}
			c.field_count = C.size_t(n)
		}
	case client.ViewerJoined:
		c.kind = C.OCCAMY_EVENT_VIEWER_JOINED
		c.id = C.uint32_t(e.ViewerID)
		c.code = C.int32_t(e.Role)
		c.text = cString(e.Name)
	case client.ViewerLeft:
		c.kind = C.OCCAMY_EVENT_VIEWER_LEFT
		c.id = C.uint32_t(e.ViewerID)
	case client.End:
		c.kind = C.OCCAMY_EVENT_END
		c.code = C.int32_t(e.Code)
		c.text = cString(e.Reason)
		if e.Err != nil {
			c.detail = cString(e.Err.Error())
		}
	}
	return c
}

func rect(r image.Rectangle) C.occamy_rect {
	return C.occamy_rect{x: C.int32_t(r.Min.X), y: C.int32_t(r.Min.Y), width: C.int32_t(r.Dx()), height: C.int32_t(r.Dy())}
}

//export occamy_event_free
func occamy_event_free(ev *C.occamy_event) {
	if ev == nil {
		return
	}
	C.free(unsafe.Pointer(ev.rects))
	C.free(unsafe.Pointer(ev.data))
	C.free(unsafe.Pointer(ev.text))
	C.free(unsafe.Pointer(ev.detail))
	if ev.fields != nil {
		for _, f := range unsafe.Slice(ev.fields, int(ev.field_count)) {
			C.free(unsafe.Pointer(f.label))
		}
		C.free(unsafe.Pointer(ev.fields))
	}
	*ev = C.occamy_event{}
}

//export occamy_info
func occamy_info(h C.occamy_session, info *C.occamy_session_info) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	if info == nil {
		return ss.fail(errors.New("occamy_info: info is NULL"))
	}
	*info = C.occamy_session_info{
		viewer_id:     C.uint32_t(ss.s.ViewerID()),
		role:          C.int32_t(ss.s.Role()),
		session_class: C.int32_t(ss.s.Class()),
		session_id:    cString(ss.s.SessionID()),
	}
	return C.OCCAMY_OK
}

//export occamy_framebuffer_read
func occamy_framebuffer_read(h C.occamy_session, dst *C.uint8_t, stride C.size_t, width, height C.int32_t, r C.occamy_rect) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	w, ht := int(width), int(height)
	area := image.Rect(int(r.x), int(r.y), int(r.x)+int(r.width), int(r.y)+int(r.height))
	switch {
	case dst == nil:
		return ss.fail(errors.New("occamy_framebuffer_read: dst is NULL"))
	case w <= 0 || ht <= 0 || int(stride) < 4*w:
		return ss.fail(errors.New("occamy_framebuffer_read: a stride shorter than a row"))
	case !area.In(image.Rect(0, 0, w, ht)):
		return ss.fail(errors.New("occamy_framebuffer_read: the rectangle is not within the screen"))
	}
	var err error
	ss.s.Framebuffer(func(fb *image.RGBA) {
		if fb.Rect.Dx() != w || fb.Rect.Dy() != ht {
			err = errors.New("occamy_framebuffer_read: the screen is not width×height")
			return
		}
		out := unsafe.Slice((*byte)(unsafe.Pointer(dst)), int(stride)*ht)
		n := 4 * area.Dx()
		for y := area.Min.Y; y < area.Max.Y; y++ {
			o := y*int(stride) + 4*area.Min.X
			copy(out[o:o+n], fb.Pix[fb.PixOffset(fb.Rect.Min.X+area.Min.X, fb.Rect.Min.Y+y):])
		}
	})
	if err != nil {
		return ss.fail(err)
	}
	return C.OCCAMY_OK
}

// input sends an input of ss built by send, and stores its sequence
// number in *seq if seq is not NULL.
func input(h C.occamy_session, seq *C.uint32_t, send func(s *client.Session) (uint32, error)) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	n, err := send(ss.s)
	if err != nil {
		return ss.fail(err)
	}
	if seq != nil {
		*seq = C.uint32_t(n)
	}
	return C.OCCAMY_OK
}

//export occamy_key
func occamy_key(h C.occamy_session, down C.bool, keysym C.uint32_t, usage C.uint16_t, seq *C.uint32_t) C.int {
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Key(context.Background(), bool(down), uint32(keysym), uint16(usage))
	})
}

//export occamy_text
func occamy_text(h C.occamy_session, text *C.char, seq *C.uint32_t) C.int {
	t := ""
	if text != nil {
		t = C.GoString(text)
	}
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Text(context.Background(), t)
	})
}

//export occamy_term_input
func occamy_term_input(h C.occamy_session, p *C.uint8_t, n C.size_t, seq *C.uint32_t) C.int {
	b := goBytes(p, n)
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.TermInput(context.Background(), b)
	})
}

//export occamy_pointer
func occamy_pointer(h C.occamy_session, x, y C.uint16_t, buttons C.uint8_t, seq *C.uint32_t) C.int {
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Pointer(context.Background(), uint16(x), uint16(y), protocol.Buttons(buttons))
	})
}

//export occamy_wheel
func occamy_wheel(h C.occamy_session, dx, dy, mode C.int32_t, seq *C.uint32_t) C.int {
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Wheel(context.Background(), int32(dx), int32(dy), protocol.WheelMode(mode))
	})
}

//export occamy_resize
func occamy_resize(h C.occamy_session, width, height C.uint16_t, seq *C.uint32_t) C.int {
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Resize(context.Background(), uint16(width), uint16(height))
	})
}

//export occamy_clipboard
func occamy_clipboard(h C.occamy_session, mime *C.char, p *C.uint8_t, n C.size_t, seq *C.uint32_t) C.int {
	m := "text/plain"
	if mime != nil {
		m = C.GoString(mime)
	}
	b := goBytes(p, n)
	return input(h, seq, func(s *client.Session) (uint32, error) {
		return s.Clipboard(context.Background(), m, b)
	})
}

//export occamy_view
func occamy_view(h C.occamy_session, visible C.bool, viewport C.occamy_rect) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	r := image.Rect(int(viewport.x), int(viewport.y), int(viewport.x)+int(viewport.width), int(viewport.y)+int(viewport.height))
	if err := ss.s.View(context.Background(), bool(visible), r); err != nil {
		return ss.fail(err)
	}
	return C.OCCAMY_OK
}

//export occamy_prompt_reply
func occamy_prompt_reply(h C.occamy_session, id C.uint32_t, answers **C.char, n C.size_t) C.int {
	ss := lookup(h)
	if ss == nil {
		return C.OCCAMY_ERR_CLOSED
	}
	if answers == nil && n > 0 {
		return ss.fail(errors.New("occamy_prompt_reply: answers is NULL"))
	}
	var as []string
	if n > 0 {
		for _, a := range unsafe.Slice(answers, int(n)) {
			if a == nil {
				return ss.fail(errors.New("occamy_prompt_reply: an answer is NULL"))
			}
			as = append(as, C.GoString(a))
		}
	}
	if err := ss.s.PromptReply(context.Background(), uint32(id), as); err != nil {
		return ss.fail(err)
	}
	return C.OCCAMY_OK
}

//export occamy_close
func occamy_close(h C.occamy_session) {
	if ss := lookup(h); ss != nil {
		_ = ss.s.Close()
	}
}

//export occamy_release
func occamy_release(h C.occamy_session) {
	mu.Lock()
	ss := sessions[h]
	delete(sessions, h)
	mu.Unlock()
	if ss != nil {
		_ = ss.s.Close()
	}
}

//export occamy_error
func occamy_error(h C.occamy_session) *C.char {
	ss := lookup(h)
	if ss == nil {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return cString(ss.err)
}

//export occamy_free
func occamy_free(p unsafe.Pointer) {
	C.free(p)
}
