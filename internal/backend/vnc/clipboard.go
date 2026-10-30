// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// Extended Clipboard (rfbproto). A server that supports it sends its
// caps, a cut text message with a negative length, in answer to
// SetEncodings, and from then on both sides speak UTF-8: text with CR LF
// line ends and a terminating NUL, compressed with zlib. The backend
// answers with caps of its own, for text only, which it takes unasked up
// to the size it takes cut text (4 MiB). It asks for the server's text
// whenever the server notifies it of new text, gives the owner's text
// when the server asks for it, and notifies the server of the owner's
// text when the owner copies, or gives it unasked to a server that takes
// no notifications. Until a server has sent caps, and with servers that
// never do, the owner's text goes as Latin-1 cut text, as RFC 6143 has
// it.
//
// The notification, or the text given unasked, is queued with the other
// input, so that it reaches the server before the keys that paste it.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"unicode/utf8"
)

// encExtendedClipboard is the Extended Clipboard pseudo-encoding,
// 0xc0a1e5ce as a signed number.
const encExtendedClipboard int32 = -1063131698

// Extended Clipboard flags: the formats, of which the backend speaks
// text only, and the actions.
const (
	clipText    uint32 = 1 << 0
	clipFormats uint32 = 0xffff
	clipCaps    uint32 = 1 << 24
	clipRequest uint32 = 1 << 25
	clipPeek    uint32 = 1 << 26
	clipNotify  uint32 = 1 << 27
	clipProvide uint32 = 1 << 28
	clipActions uint32 = 0xff << 24
)

// clipboard is the state of Extended Clipboard, under run.mu.
type clipboard struct {
	flags uint32 // the formats and actions the server takes, from its caps; 0 before them
	// What is due to be sent.
	caps, request, notify, provide bool
	// text is the owner's latest clipboard in the extended format, nil
	// before the first.
	text []byte
}

// extendedClipboard applies an Extended Clipboard message of n bytes
// after its length.
func (d *decoder) extendedClipboard(n int64) error {
	if n < 4 {
		return fmt.Errorf("vnc: extended clipboard message of %d bytes", n)
	}
	if err := d.read(d.hdr[:4]); err != nil {
		return fmt.Errorf("vnc: extended clipboard: %w", err)
	}
	flags := binary.BigEndian.Uint32(d.hdr[:4])
	n -= 4
	switch action := flags & clipActions; {
	case action&clipCaps != 0:
		// A size for each format, of which text, if there, is first.
		var sizes [4 * 16]byte
		k := int64(4 * bits.OnesCount32(flags&clipFormats))
		if n < k {
			return fmt.Errorf("vnc: extended clipboard caps of %d bytes for %d formats", n, k/4)
		}
		if err := d.read(sizes[:k]); err != nil {
			return fmt.Errorf("vnc: extended clipboard: %w", err)
		}
		n -= k
		d.tell().clipboardCaps(flags)
	case action == clipProvide:
		return d.clipboardProvide(flags, n)
	case action == clipRequest, action == clipPeek, action == clipNotify:
		d.tell().clipboardAction(flags)
	default:
		d.log.Debug("vnc: extended clipboard message without one action", "flags", flags)
	}
	if err := d.discard(n); err != nil {
		return fmt.Errorf("vnc: extended clipboard: %w", err)
	}
	return nil
}

// clipboardProvide applies a provide action of n bytes after its flags:
// a zlib stream of a size and data for each format. Of the text, the
// first maxClipboard bytes are read; the rest of the stream is skipped
// without decompressing it. A stream that does not decompress is
// skipped with a warning, since the message's length still frames it.
func (d *decoder) clipboardProvide(flags uint32, n int64) error {
	lr := &limitedReader{r: d.r, n: n}
	text, truncated, err := readProvide(lr, flags)
	if lr.err != nil {
		return fmt.Errorf("vnc: extended clipboard: %w", lr.err)
	}
	switch {
	case err != nil:
		d.log.Warn("vnc: extended clipboard: the server's text does not decompress", "err", err)
	case text != nil:
		if truncated {
			d.log.Warn("vnc: extended clipboard text truncated", "kept", len(text))
		}
		d.sink.Clipboard("text/plain", fromExtendedText(text, truncated))
	}
	if err := d.discard(lr.n); err != nil {
		return fmt.Errorf("vnc: extended clipboard: %w", err)
	}
	return nil
}

// readProvide reads the text from a provide action's zlib stream: at
// most maxClipboard bytes of it, and whether there was more. It returns
// nil if the action provides no text.
func readProvide(r io.Reader, flags uint32) (text []byte, truncated bool, err error) {
	if flags&clipText == 0 {
		return nil, false, nil
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, false, err
	}
	defer zr.Close()
	var b [4]byte
	if _, err := io.ReadFull(zr, b[:]); err != nil {
		return nil, false, err
	}
	size := int64(binary.BigEndian.Uint32(b[:]))
	keep := int(min(size, maxClipboard))
	// The buffer grows as the text arrives, not to the size claimed, and
	// not beyond what is kept.
	text = make([]byte, 0, min(keep, 4<<10))
	for len(text) < keep {
		if len(text) == cap(text) {
			text = slices.Grow(text, min(len(text), keep-len(text)))
		}
		n, err := zr.Read(text[len(text):min(cap(text), keep)])
		text = text[:len(text)+n]
		if err != nil && len(text) < keep {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, false, err
		}
	}
	return text, size > int64(keep), nil
}

// limitedReader reads at most n bytes from r, and keeps r's error apart
// from the end of the n bytes.
type limitedReader struct {
	r   io.Reader
	n   int64
	err error // r's error; io.EOF is io.ErrUnexpectedEOF
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	k, err := l.r.Read(p)
	l.n -= int64(k)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		l.err = err
	}
	return k, err
}

// clipboardCaps implements peer: the backend's caps answer the server's.
func (r *run) clipboardCaps(flags uint32) {
	r.mu.Lock()
	r.clip.flags = flags
	r.clip.caps = true
	r.mu.Unlock()
	r.poke()
}

// clipboardAction implements peer.
func (r *run) clipboardAction(flags uint32) {
	r.mu.Lock()
	c := &r.clip
	switch flags & clipActions {
	case clipRequest:
		c.provide = c.provide || flags&clipText != 0
	case clipPeek:
		c.notify = true
	case clipNotify:
		c.request = c.request || flags&clipText != 0
	}
	r.mu.Unlock()
	r.poke()
}

// clipboard returns the message for the owner's clipboard text: Latin-1
// cut text for a server without Extended Clipboard; otherwise a
// notification, with the text kept for the server to ask for, or the
// text itself for a server that takes no notifications.
func (r *run) clipboard(data []byte) []byte {
	r.mu.Lock()
	flags := r.clip.flags
	r.mu.Unlock()
	if flags&clipText == 0 || flags&(clipNotify|clipProvide) == 0 {
		return appendCutText(nil, data)
	}
	text := toExtendedText(data)
	r.mu.Lock()
	r.clip.text = text
	r.mu.Unlock()
	if flags&clipNotify != 0 {
		return appendClipboard(nil, clipNotify|clipText, nil)
	}
	return appendClipboardProvide(nil, text)
}

// clipboardDue appends the Extended Clipboard messages that are due,
// except a provide action, whose text it returns to be compressed after
// r.mu is released. Actions that the server's caps do not take are
// dropped. r.mu must be held.
func (r *run) clipboardDue(p []byte) ([]byte, []byte) {
	c := &r.clip
	if c.caps {
		c.caps = false
		p = appendClipboard(p, clipCaps|clipText|clipRequest|clipPeek|clipNotify|clipProvide,
			binary.BigEndian.AppendUint32(nil, maxClipboard))
	}
	if c.request {
		c.request = false
		if c.flags&clipRequest != 0 {
			p = appendClipboard(p, clipRequest|clipText, nil)
		}
	}
	if c.notify {
		c.notify = false
		if c.flags&clipNotify != 0 {
			flags := clipNotify
			if c.text != nil {
				flags |= clipText
			}
			p = appendClipboard(p, flags, nil)
		}
	}
	var text []byte
	if c.provide {
		c.provide = false
		if c.flags&clipProvide != 0 {
			text = c.text
		}
	}
	return p, text
}

// appendClipboard appends an Extended Clipboard message with flags and
// the payload that follows them.
func appendClipboard(p []byte, flags uint32, payload []byte) []byte {
	p = append(p, msgClientCutText, 0, 0, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(-int32(4+len(payload)))) // #nosec G115 -- at most maxClipboard plus zlib's overhead
	p = binary.BigEndian.AppendUint32(p, flags)
	return append(p, payload...)
}

// appendClipboardProvide appends a provide action of text, which is in
// the extended format.
func appendClipboardProvide(p []byte, text []byte) []byte {
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)                       // the level is valid
	_, _ = zw.Write(binary.BigEndian.AppendUint32(nil, uint32(len(text)))) // #nosec G115 -- at most maxClipboard
	_, _ = zw.Write(text)
	_ = zw.Close() // writes to memory
	return appendClipboard(p, clipProvide|clipText, z.Bytes())
}

// toExtendedText converts the owner's text, UTF-8, to the extended
// format: valid UTF-8 with CR LF line ends and a terminating NUL, in at
// most maxClipboard bytes. Invalid UTF-8 becomes U+FFFD.
func toExtendedText(data []byte) []byte {
	out := make([]byte, 0, min(len(data)+len(data)/16+1, maxClipboard))
	for len(data) > 0 {
		c, n := utf8.DecodeRune(data)
		seq := data[:n]
		switch {
		case c == '\r' && len(data) > 1 && data[1] == '\n':
			seq, n = []byte("\r\n"), 2
		case c == '\r', c == '\n':
			seq = []byte("\r\n")
		case c == utf8.RuneError && n == 1:
			seq = []byte(string(utf8.RuneError))
		}
		if len(out)+len(seq) > maxClipboard-1 {
			break
		}
		out = append(out, seq...)
		data = data[n:]
	}
	return append(out, 0)
}

// fromExtendedText converts text in the extended format to the sink's:
// UTF-8 up to the first NUL, with LF line ends. A character that a
// truncation cut in two is dropped, and invalid UTF-8 becomes U+FFFD.
// It converts b in place, unless b is not valid UTF-8.
func fromExtendedText(b []byte, truncated bool) []byte {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b, truncated = b[:i], false
	}
	if truncated {
		b = trimPartialRune(b)
	}
	if !utf8.Valid(b) {
		b = bytes.ToValidUTF8(b, []byte(string(utf8.RuneError)))
	}
	// CR LF and a lone CR become LF; the text only shrinks.
	out := b[:0]
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == '\r' {
			c = '\n'
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
		}
		out = append(out, c)
	}
	return out
}
