// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build darwin && cgo

package main

/*
#define OCCAMY_CGO
#include <stdlib.h>
#include "occamy.h"
*/
import "C"

import (
	"image"
	"unsafe"

	"changkun.de/x/occamy/protocol"
)

// The tests call the exported functions through these, as the app does
// through occamy.h, since a test file cannot use cgo. Each converts its
// arguments to C memory, frees what it allocated, and returns what C got
// back as Go values.

// goEvent is an occamy_event read back.
type goEvent struct {
	Kind                int
	ID, LastSeq         uint32
	ServerTimeUS        int64
	X, Y, Width, Height int
	Code                int
	Rects               []image.Rectangle
	Data                []byte
	Text, Detail        string
	Fields              []protocol.PromptField
	rectsNil, dataNil   bool
	zeroed              bool // occamy_event_free zeroed the event
}

type goOptions struct {
	Name                      string
	Width, Height, Cols, Rows uint16
	PixelRatio                uint16
	Hidden                    bool
}

// handle is a session's handle.
type handle = C.occamy_session

type dialResult struct {
	Handle      handle
	Result      int
	CloseCode   int
	CloseReason string
	Message     string
	SessionID   string
}

func callDial(url, token string, o *goOptions, timeoutMS int) dialResult {
	cu, ct := C.CString(url), C.CString(token)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(ct))
	var opts *C.occamy_options
	if o != nil {
		opts = (*C.occamy_options)(C.calloc(1, C.size_t(unsafe.Sizeof(C.occamy_options{}))))
		defer C.free(unsafe.Pointer(opts))
		if o.Name != "" {
			opts.name = C.CString(o.Name)
			defer C.free(unsafe.Pointer(opts.name))
		}
		opts.width, opts.height = C.uint16_t(o.Width), C.uint16_t(o.Height)
		opts.cols, opts.rows = C.uint16_t(o.Cols), C.uint16_t(o.Rows)
		opts.pixel_ratio = C.uint16_t(o.PixelRatio)
		opts.hidden = C.bool(o.Hidden)
	}
	out := (*C.occamy_session)(C.calloc(1, C.size_t(unsafe.Sizeof(C.occamy_session(0)))))
	defer C.free(unsafe.Pointer(out))
	cerr := (*C.occamy_dial_error)(C.calloc(1, C.size_t(unsafe.Sizeof(C.occamy_dial_error{}))))
	defer C.free(unsafe.Pointer(cerr))
	r := dialResult{Result: int(occamy_dial(cu, ct, opts, C.int32_t(timeoutMS), out, cerr))}
	r.Handle = *out
	r.CloseCode = int(cerr.close_code)
	r.CloseReason = C.GoString(cerr.close_reason)
	r.Message = C.GoString(cerr.message)
	r.SessionID = C.GoString(cerr.session_id)
	occamy_dial_error_free(cerr)
	return r
}

// callDialNil calls occamy_dial without a URL.
func callDialNil() dialResult {
	cerr := (*C.occamy_dial_error)(C.calloc(1, C.size_t(unsafe.Sizeof(C.occamy_dial_error{}))))
	defer C.free(unsafe.Pointer(cerr))
	var out C.occamy_session
	r := dialResult{Result: int(occamy_dial(nil, nil, nil, 0, &out, cerr))}
	r.Message = C.GoString(cerr.message)
	occamy_dial_error_free(cerr)
	return r
}

func callNext(h handle, timeoutMS int) (int, goEvent) {
	ev := (*C.occamy_event)(C.calloc(1, C.size_t(unsafe.Sizeof(C.occamy_event{}))))
	defer C.free(unsafe.Pointer(ev))
	res := int(occamy_next(h, C.int32_t(timeoutMS), ev))
	g := goEvent{
		Kind: int(ev.kind), ID: uint32(ev.id), LastSeq: uint32(ev.last_seq),
		ServerTimeUS: int64(ev.server_time_us),
		X:            int(ev.x), Y: int(ev.y), Width: int(ev.width), Height: int(ev.height),
		Code: int(ev.code),
		Text: C.GoString(ev.text), Detail: C.GoString(ev.detail),
		rectsNil: ev.rects == nil, dataNil: ev.data == nil,
	}
	for _, r := range unsafe.Slice(ev.rects, int(ev.rect_count)) {
		g.Rects = append(g.Rects, image.Rect(int(r.x), int(r.y), int(r.x+r.width), int(r.y+r.height)))
	}
	g.Data = goBytes(ev.data, ev.data_len)
	for _, f := range unsafe.Slice(ev.fields, int(ev.field_count)) {
		g.Fields = append(g.Fields, protocol.PromptField{Label: C.GoString(f.label), Secret: bool(f.secret)})
	}
	occamy_event_free(ev)
	g.zeroed = ev.kind == 0 && ev.rects == nil && ev.data == nil && ev.text == nil && ev.detail == nil && ev.fields == nil
	return res, g
}

// callNextNil calls occamy_next without an event to fill.
func callNextNil(h handle) int { return int(occamy_next(h, 0, nil)) }

func callInfo(h handle) (res int, viewer uint32, role, class int, sessionID string) {
	var info C.occamy_session_info
	res = int(occamy_info(h, &info))
	sessionID = C.GoString(info.session_id)
	occamy_free(unsafe.Pointer(info.session_id))
	return res, uint32(info.viewer_id), int(info.role), int(info.session_class), sessionID
}

// callFramebuffer reads r of a screen of w×h, at stride bytes a row,
// into a buffer that starts as fill, and returns the buffer.
func callFramebuffer(h handle, w, ht, stride int, r image.Rectangle, fill byte) (int, []byte) {
	n := max(stride*ht, 1)
	buf := (*C.uint8_t)(C.malloc(C.size_t(n)))
	defer C.free(unsafe.Pointer(buf))
	b := unsafe.Slice((*byte)(unsafe.Pointer(buf)), n)
	for i := range b {
		b[i] = fill
	}
	cr := C.occamy_rect{x: C.int32_t(r.Min.X), y: C.int32_t(r.Min.Y), width: C.int32_t(r.Dx()), height: C.int32_t(r.Dy())}
	res := int(occamy_framebuffer_read(h, buf, C.size_t(stride), C.int32_t(w), C.int32_t(ht), cr))
	return res, append([]byte(nil), b...)
}

func callKey(h handle, down bool, keysym uint32, usage uint16) (int, uint32) {
	var seq C.uint32_t
	return int(occamy_key(h, C.bool(down), C.uint32_t(keysym), C.uint16_t(usage), &seq)), uint32(seq)
}

func callText(h handle, text string) (int, uint32) {
	ct := C.CString(text)
	defer C.free(unsafe.Pointer(ct))
	var seq C.uint32_t
	return int(occamy_text(h, ct, &seq)), uint32(seq)
}

func callTermInput(h handle, data []byte) (int, uint32) {
	p := cBytes(data)
	defer C.free(unsafe.Pointer(p))
	var seq C.uint32_t
	return int(occamy_term_input(h, p, C.size_t(len(data)), &seq)), uint32(seq)
}

func callPointer(h handle, x, y uint16, buttons uint8) (int, uint32) {
	// No seq: the core takes NULL.
	return int(occamy_pointer(h, C.uint16_t(x), C.uint16_t(y), C.uint8_t(buttons), nil)), 0
}

func callWheel(h handle, dx, dy, mode int32) (int, uint32) {
	var seq C.uint32_t
	return int(occamy_wheel(h, C.int32_t(dx), C.int32_t(dy), C.int32_t(mode), &seq)), uint32(seq)
}

func callResize(h handle, w, ht uint16) (int, uint32) {
	var seq C.uint32_t
	return int(occamy_resize(h, C.uint16_t(w), C.uint16_t(ht), &seq)), uint32(seq)
}

func callClipboard(h handle, mime string, data []byte) (int, uint32) {
	cm := C.CString(mime)
	defer C.free(unsafe.Pointer(cm))
	p := cBytes(data)
	defer C.free(unsafe.Pointer(p))
	var seq C.uint32_t
	return int(occamy_clipboard(h, cm, p, C.size_t(len(data)), &seq)), uint32(seq)
}

func callView(h handle, visible bool, r image.Rectangle) int {
	cr := C.occamy_rect{x: C.int32_t(r.Min.X), y: C.int32_t(r.Min.Y), width: C.int32_t(r.Dx()), height: C.int32_t(r.Dy())}
	return int(occamy_view(h, C.bool(visible), cr))
}

func callPromptReply(h handle, id uint32, answers []string) int {
	if len(answers) == 0 {
		return int(occamy_prompt_reply(h, C.uint32_t(id), nil, 0))
	}
	arr := (**C.char)(C.malloc(C.size_t(len(answers)) * C.size_t(unsafe.Sizeof((*C.char)(nil)))))
	defer C.free(unsafe.Pointer(arr))
	as := unsafe.Slice(arr, len(answers))
	for i, a := range answers {
		as[i] = C.CString(a)
		defer C.free(unsafe.Pointer(as[i]))
	}
	return int(occamy_prompt_reply(h, C.uint32_t(id), arr, C.size_t(len(answers))))
}

func callClose(h handle)   { occamy_close(h) }
func callRelease(h handle) { occamy_release(h) }

func callError(h handle) string {
	p := occamy_error(h)
	defer occamy_free(unsafe.Pointer(p))
	return C.GoString(p)
}

// Results, for the tests.
const (
	resOK       = int(C.OCCAMY_OK)
	resTimeout  = int(C.OCCAMY_TIMEOUT)
	resErr      = int(C.OCCAMY_ERR)
	resClosed   = int(C.OCCAMY_ERR_CLOSED)
	resReadOnly = int(C.OCCAMY_ERR_READ_ONLY)
	resNotOwner = int(C.OCCAMY_ERR_NOT_OWNER)

	evFrame          = int(C.OCCAMY_EVENT_FRAME)
	evResize         = int(C.OCCAMY_EVENT_RESIZE)
	evTermSnapshot   = int(C.OCCAMY_EVENT_TERM_SNAPSHOT)
	evTermData       = int(C.OCCAMY_EVENT_TERM_DATA)
	evCursor         = int(C.OCCAMY_EVENT_CURSOR)
	evCursorMove     = int(C.OCCAMY_EVENT_CURSOR_MOVE)
	evClipboard      = int(C.OCCAMY_EVENT_CLIPBOARD)
	evClipboardState = int(C.OCCAMY_EVENT_CLIPBOARD_STATUS)
	evBell           = int(C.OCCAMY_EVENT_BELL)
	evTitle          = int(C.OCCAMY_EVENT_TITLE)
	evPrompt         = int(C.OCCAMY_EVENT_PROMPT)
	evViewerJoined   = int(C.OCCAMY_EVENT_VIEWER_JOINED)
	evViewerLeft     = int(C.OCCAMY_EVENT_VIEWER_LEFT)
	evEnd            = int(C.OCCAMY_EVENT_END)
)
