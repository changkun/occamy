// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// The clipboard channel, CLIPRDR ([MS-RDPECLIP]), for text. The server
// sends its capabilities and Monitor Ready; the client answers with its
// own capabilities and a Format List of what its clipboard holds. From
// then on either side announces a new clipboard with a Format List,
// which the other acknowledges, and asks for the data of a format with a
// Format Data Request, which the other answers.
//
// The client asks for the server's text as soon as the server announces
// it, as guacd does, preferring CF_UNICODETEXT to CF_TEXT, and gives it
// to its Output. It announces the owner's text as both, and sends it
// when the server asks. Text is UTF-8 with LF line ends on the client's
// side, and CR LF on the server's, as the VNC backend converts it, and
// at most MaxClipboard bytes of UTF-8 either way. One request at a time
// awaits the server's answer; a newer announcement makes the answer to
// an older one stale. The server's text reaches the Output at the pace
// of the decoding rate, like a pointer shape, so that a server that
// announces text again and again cannot hand every viewer a clipboard
// at the rate of the link.

// MaxClipboard bounds clipboard text, in bytes of UTF-8, in either
// direction: the largest message protocol v1 carries by default, as in
// the VNC backend and the guacd bridge.
const MaxClipboard = 4 << 20

// maxCliprdrMessage bounds the CLIPRDR message the client keeps: its
// header and two bytes of UTF-16 for each byte of the most text it gives
// its Output. The rest of a longer message is dropped, which truncates
// the text.
const maxCliprdrMessage = 8 + 2*MaxClipboard

// maxDataRequests bounds the server's Format Data Requests that await
// the client's answer. A server asks again only once it has its answer,
// so more mean a server that does not read.
const maxDataRequests = 8

// maxListResponses bounds the Format List Responses the client owes the
// server. A server sends a Format List when its clipboard changes, and
// the channel writer answers at once, so more mean a server that floods
// lists while it does not read, or faster than the client writes.
const maxListResponses = 64

// ClipboardMode selects which ways clipboard text goes. With neither
// direction the client does not ask for the clipboard channel.
type ClipboardMode uint8

// The clipboard's directions.
const (
	// ClipboardFromServer gives the server's text to the Output.
	ClipboardFromServer ClipboardMode = 1 << iota
	// ClipboardToServer offers the text of SetClipboard to the server.
	ClipboardToServer
)

// CLIPRDR message types, [MS-RDPECLIP] 2.2.1.
const (
	cbMonitorReady       = 0x0001
	cbFormatList         = 0x0002
	cbFormatListResponse = 0x0003
	cbFormatDataRequest  = 0x0004
	cbFormatDataResponse = 0x0005
	cbClipCaps           = 0x0007
)

// CLIPRDR message flags, [MS-RDPECLIP] 2.2.1.
const (
	cbResponseOK   = 0x0001
	cbResponseFail = 0x0002
)

// The general capability set, [MS-RDPECLIP] 2.2.2.1.1.1.
const (
	cbCapsTypeGeneral    = 0x0001
	cbCapsVersion2       = 0x00000002
	cbUseLongFormatNames = 0x00000002
)

// The clipboard formats of text, [MS-RDPECLIP] 1.3.1.2.
const (
	cfText        = 1
	cfUnicodeText = 13
)

// shortFormatName is the size of a CLIPRDR_SHORT_FORMAT_NAME,
// [MS-RDPECLIP] 2.2.3.1.1.1.
const shortFormatName = 36

// cliprdr is the state of the clipboard channel, under chio.mu.
type cliprdr struct {
	longNames bool // the server takes long format names
	ready     bool // the server sent Monitor Ready

	// What is due to be sent.
	initDue   bool   // the client's capabilities and first Format List
	listDue   bool   // a Format List for new text
	listOK    int    // Format List Responses that accept a list
	listFail  int    // and those that refuse one that does not parse
	request   uint32 // a format of the server's to ask for, 0 if none
	requested uint32 // the format asked for and not answered, 0 if none
	provide   []uint32
	text      []byte // the owner's text, nil if none
	owned     bool   // the server's latest Format List is the client's
	stale     bool   // the answer awaited is for an older announcement
}

// cliprdrMessage applies a CLIPRDR message from the server, of which
// the client may have kept only the first maxCliprdrMessage bytes.
func (c *Client) cliprdrMessage(out Output, msg []byte, truncated bool) error {
	r := reader{b: msg}
	typ := r.u16()
	flags := r.u16()
	n := int64(r.u32())
	if r.err != nil {
		return fmt.Errorf("rdp: clipboard PDU: %w", r.err)
	}
	body := r.b
	switch {
	case n <= int64(len(body)):
		body = body[:n]
	case !truncated || typ != cbFormatDataResponse:
		// Only text may be cut; a message whose length is wrong is
		// dropped.
		c.count(func(s *Stats) { s.Ignored["clipboard PDUs"]++ })
		return nil
	}
	cl := &c.clip
	switch typ {
	case cbClipCaps:
		long, err := parseClipCaps(body)
		if err != nil {
			return err
		}
		c.chio.mu.Lock()
		cl.longNames = long
		c.chio.mu.Unlock()
	case cbMonitorReady:
		c.chio.mu.Lock()
		cl.ready, cl.initDue = true, true
		c.chio.mu.Unlock()
		c.due()
	case cbFormatList:
		// The reader alone changes longNames, so it reads it unlocked.
		format, ok := textFormat(body, cl.longNames)
		c.chio.mu.Lock()
		if cl.listOK+cl.listFail == maxListResponses {
			c.chio.mu.Unlock()
			return fmt.Errorf("rdp: more than %d clipboard Format List Responses await writing", maxListResponses)
		}
		cl.owned = false
		if ok {
			cl.listOK++
		} else {
			cl.listFail++
		}
		// The server's clipboard changed: an answer awaited is stale, and
		// a request not yet sent is replaced.
		cl.request = 0
		if c.cfg.Clipboard&ClipboardFromServer != 0 {
			cl.request = format
		}
		cl.stale = cl.requested != 0
		c.chio.mu.Unlock()
		c.due()
	case cbFormatDataRequest:
		f := reader{b: body}
		format := f.u32()
		if f.err != nil {
			return fmt.Errorf("rdp: clipboard data request: %w", f.err)
		}
		c.chio.mu.Lock()
		if len(cl.provide) == maxDataRequests {
			c.chio.mu.Unlock()
			return fmt.Errorf("rdp: more than %d clipboard data requests await their answers", maxDataRequests)
		}
		cl.provide = append(cl.provide, format)
		c.chio.mu.Unlock()
		c.due()
	case cbFormatDataResponse:
		c.chio.mu.Lock()
		format, stale := cl.requested, cl.stale
		cl.requested, cl.stale = 0, false
		c.chio.mu.Unlock()
		c.due() // the request for a newer announcement, if any
		if format == 0 || stale || flags&cbResponseOK == 0 {
			c.count(func(s *Stats) { s.Ignored["clipboard answers"]++ })
			return nil
		}
		var text []byte
		if format == cfText {
			text = fromANSIText(body)
		} else {
			text = fromUnicodeText(body)
		}
		if out == nil {
			return nil
		}
		if err := c.pace(max(pointerCost, len(text))); err != nil {
			return err
		}
		out.Clipboard("text/plain", text)
	default:
		// Temporary directories, file contents and locks are for files,
		// which the client does not offer.
		c.count(func(s *Stats) { s.Ignored["clipboard PDUs"]++ })
	}
	return nil
}

// cliprdrDue appends the clipboard messages that are due, in the order
// [MS-RDPECLIP] 1.3.2 has them. chio.mu must be held.
func (c *Client) cliprdrDue(msgs []dueMessage) []dueMessage {
	vc, cl := c.clipCh, &c.clip
	if vc == nil {
		return msgs
	}
	add := func(encode func() []byte) { msgs = append(msgs, dueMessage{vc, encode}) }
	if cl.initDue {
		cl.initDue = false
		add(clipCaps)
		cl.listDue = true
	}
	if cl.listDue && cl.ready {
		cl.listDue = false
		long, text := cl.longNames, cl.text != nil
		cl.owned = text
		add(func() []byte { return formatList(long, text) })
	}
	for ; cl.listOK > 0; cl.listOK-- {
		add(func() []byte { return cliprdrPDU(cbFormatListResponse, cbResponseOK, nil) })
	}
	for ; cl.listFail > 0; cl.listFail-- {
		add(func() []byte { return cliprdrPDU(cbFormatListResponse, cbResponseFail, nil) })
	}
	if cl.request != 0 && cl.requested == 0 {
		format := cl.request
		cl.requested, cl.request = format, 0
		add(func() []byte { return formatDataRequest(format) })
	}
	for _, format := range cl.provide {
		text := cl.text
		add(func() []byte { return formatDataResponse(format, text) })
	}
	clear(cl.provide)
	cl.provide = cl.provide[:0]
	return msgs
}

// SetClipboard offers text, which is UTF-8, to the server as its
// clipboard: it sends a Format List of text, so that a key sent after
// SetClipboard returns pastes it, and the text when the server asks for
// it. Text beyond MaxClipboard bytes is dropped, and invalid UTF-8
// becomes U+FFFD. Before the server's clipboard channel is ready, the
// text waits for it. Without ClipboardToServer, or without the channel,
// SetClipboard does nothing.
func (c *Client) SetClipboard(text []byte) error {
	if c.cfg.Clipboard&ClipboardToServer == 0 || c.clipCh == nil {
		return nil
	}
	text = clipboardText(text)
	c.chio.mu.Lock()
	cl := &c.clip
	if cl.owned && bytes.Equal(cl.text, text) {
		c.chio.mu.Unlock()
		return nil
	}
	cl.text = text
	cl.listDue = true
	c.chio.mu.Unlock()
	return c.writeDue()
}

// clipboardText returns a copy of text cut to at most MaxClipboard bytes
// at a character's start, or nil for none.
func clipboardText(text []byte) []byte {
	if len(text) == 0 {
		return nil
	}
	if len(text) > MaxClipboard {
		n := MaxClipboard
		for n > 0 && !utf8.RuneStart(text[n]) { // #nosec G602 -- text has more than MaxClipboard bytes
			n--
		}
		text = text[:n]
	}
	return bytes.Clone(text)
}

// cliprdrPDU is a CLIPRDR message: the header of [MS-RDPECLIP] 2.2.1,
// then data.
func cliprdrPDU(typ, flags int, data []byte) []byte {
	b := make([]byte, 0, 8+len(data))
	w := writer{b: b}
	w.u16(typ)
	w.u16(flags)
	w.u32(uint32(len(data))) // #nosec G115 -- bounded by MaxClipboard
	w.bytes(data)
	return w.b
}

// clipCaps is the client's Clipboard Capabilities PDU, [MS-RDPECLIP]
// 2.2.2.1: version 2 with long format names, and nothing for files.
func clipCaps() []byte {
	var w writer
	w.u16(1) // cCapabilitiesSets
	w.u16(0) // pad1
	w.u16(cbCapsTypeGeneral)
	w.u16(12)
	w.u32(cbCapsVersion2)
	w.u32(cbUseLongFormatNames)
	return cliprdrPDU(cbClipCaps, 0, w.b)
}

// parseClipCaps returns whether the server's capabilities take long
// format names.
func parseClipCaps(p []byte) (long bool, err error) {
	r := reader{b: p}
	n := int(r.u16())
	r.u16() // pad1
	for range n {
		typ := r.u16()
		l := int(r.u16())
		if r.err == nil && l < 4 {
			return false, fmt.Errorf("rdp: clipboard capability set %d of length %d", typ, l)
		}
		set := reader{b: r.take(l - 4)}
		if typ == cbCapsTypeGeneral {
			set.u32() // version
			long = set.u32()&cbUseLongFormatNames != 0
		}
		if set.err != nil {
			return false, fmt.Errorf("rdp: clipboard capability set %d: %w", typ, set.err)
		}
	}
	if r.err != nil {
		return false, fmt.Errorf("rdp: clipboard capabilities: %w", r.err)
	}
	return long, nil
}

// formatList is the client's Format List PDU, [MS-RDPECLIP] 2.2.3.1:
// CF_UNICODETEXT and CF_TEXT if it has text, or no formats, in long or
// short format names. Their names are empty.
func formatList(long, text bool) []byte {
	var w writer
	if text {
		for _, f := range []uint32{cfUnicodeText, cfText} {
			w.u32(f)
			if long {
				w.u16(0) // an empty name's terminator
			} else {
				w.zero(shortFormatName - 4)
			}
		}
	}
	return cliprdrPDU(cbFormatList, 0, w.b)
}

// textFormat returns the text format a Format List offers, CF_UNICODETEXT
// rather than CF_TEXT, or 0 if it offers neither, and whether the list
// parses. It looks at the formats' IDs only.
func textFormat(p []byte, long bool) (format uint32, ok bool) {
	take := func(f uint32) {
		if f == cfUnicodeText || f == cfText && format == 0 {
			format = f
		}
	}
	if !long {
		if len(p)%shortFormatName != 0 {
			return 0, false
		}
		for i := 0; i < len(p); i += shortFormatName {
			take(uint32(p[i]) | uint32(p[i+1])<<8 | uint32(p[i+2])<<16 | uint32(p[i+3])<<24)
		}
		return format, true
	}
	for len(p) > 0 {
		if len(p) < 6 {
			return 0, false
		}
		take(uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24)
		end := -1
		for i := 4; i+1 < len(p); i += 2 {
			if p[i] == 0 && p[i+1] == 0 {
				end = i + 2
				break
			}
		}
		if end < 0 {
			return 0, false
		}
		p = p[end:]
	}
	return format, true
}

// formatDataRequest is the Format Data Request PDU, [MS-RDPECLIP]
// 2.2.5.1, for format.
func formatDataRequest(format uint32) []byte {
	var w writer
	w.u32(format)
	return cliprdrPDU(cbFormatDataRequest, 0, w.b)
}

// formatDataResponse is the Format Data Response PDU, [MS-RDPECLIP]
// 2.2.5.2, of text in format, or a failure if there is no text or the
// format is not text.
func formatDataResponse(format uint32, text []byte) []byte {
	switch {
	case text == nil:
	case format == cfUnicodeText:
		return cliprdrPDU(cbFormatDataResponse, cbResponseOK, unicodeText(text))
	case format == cfText:
		return cliprdrPDU(cbFormatDataResponse, cbResponseOK, ansiText(text))
	}
	return cliprdrPDU(cbFormatDataResponse, cbResponseFail, nil)
}

// crlf calls f for each character of UTF-8 text with CR LF line ends:
// a lone CR or LF becomes CR LF, and invalid UTF-8 U+FFFD.
func crlf(text []byte, f func(rune)) {
	for len(text) > 0 {
		r, n := utf8.DecodeRune(text)
		text = text[n:]
		switch r {
		case '\r':
			if len(text) > 0 && text[0] == '\n' {
				text = text[1:]
			}
			f('\r')
			f('\n')
		case '\n':
			f('\r')
			f('\n')
		default:
			f(r)
		}
	}
}

// unicodeText encodes text as CF_UNICODETEXT: UTF-16LE with CR LF line
// ends and a terminating NUL.
func unicodeText(text []byte) []byte {
	b := make([]byte, 0, 2*len(text)+2)
	var units [2]uint16
	crlf(text, func(r rune) {
		for _, u := range utf16.AppendRune(units[:0], r) {
			b = binary.LittleEndian.AppendUint16(b, u)
		}
	})
	return append(b, 0, 0)
}

// ansiText encodes text as CF_TEXT: Latin-1 with CR LF line ends and a
// terminating NUL, a character outside Latin-1 as '?'.
func ansiText(text []byte) []byte {
	b := make([]byte, 0, len(text)+1)
	crlf(text, func(r rune) {
		if r > 0xff {
			r = '?'
		}
		b = append(b, byte(r))
	})
	return append(b, 0)
}

// lineEnds converts CR LF and a lone CR to LF, for each character r of
// text in turn, and reports whether r is kept.
type lineEnds struct{ cr bool }

func (l *lineEnds) next(r rune) (rune, bool) {
	if r == '\n' && l.cr {
		l.cr = false
		return 0, false
	}
	l.cr = r == '\r'
	if l.cr {
		return '\n', true
	}
	return r, true
}

// fromUnicodeText decodes CF_UNICODETEXT to UTF-8 with LF line ends: up
// to its first NUL, at most MaxClipboard bytes. An unpaired surrogate
// becomes U+FFFD; one cut off at the end, as by truncation, is dropped.
func fromUnicodeText(b []byte) []byte {
	out := make([]byte, 0, min(len(b), MaxClipboard))
	var ends lineEnds
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(b[i]) | rune(b[i+1])<<8
		if r == 0 {
			break
		}
		if utf16.IsSurrogate(r) {
			if i+3 >= len(b) {
				break
			}
			r = utf16.DecodeRune(r, rune(b[i+2])|rune(b[i+3])<<8)
			if r != utf8.RuneError {
				i += 2
			}
		}
		r, ok := ends.next(r)
		if !ok {
			continue
		}
		if len(out)+utf8.RuneLen(r) > MaxClipboard {
			break
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

// fromANSIText decodes CF_TEXT, as Latin-1, to UTF-8 with LF line ends:
// up to its first NUL, at most MaxClipboard bytes.
func fromANSIText(b []byte) []byte {
	out := make([]byte, 0, min(len(b), MaxClipboard))
	var ends lineEnds
	for _, c := range b {
		if c == 0 {
			break
		}
		r, ok := ends.next(rune(c))
		if !ok {
			continue
		}
		if len(out)+utf8.RuneLen(r) > MaxClipboard {
			break
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}
