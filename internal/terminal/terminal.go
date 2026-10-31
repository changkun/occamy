// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package terminal is the headless terminal emulator of a terminal
// session. All output of the remote shell is written to one Terminal,
// which keeps the screen, the cursor, the modes a client mirrors, and the
// title. Viewers are never sent the shell's bytes: each takes snapshots
// and is sent the Diff between the screen it last received and the
// current one, escape sequences that turn one into the other, as mosh
// does. A late joiner is sent the Diff from nothing.
//
// # Writers
//
// The session's owner calls Write with the backend's output and Resize
// with the owner's size, one call at a time. Any number of viewer writers
// read at the same time, each from its own goroutine, without further
// locking:
//
//	var last *terminal.Screen // what this viewer was last sent; nil at first
//	for {
//		changed := sess.Changed() // before reading, so no change is missed
//		s := term.Snapshot()
//		if s != last {
//			if last != nil && (last.Cols != s.Cols || last.Rows != s.Rows) {
//				// send the size first; the client resizes its terminal
//			}
//			send(terminal.Diff(last, s)) // as term-data, then frame-end
//			last = s
//		}
//		<-changed // or the pacer's next frame
//	}
//
// Snapshot holds the terminal's lock only to copy one pointer per row and
// hash the rows that changed, about a microsecond for 200x50, so it is
// cheap at any frame rate and Write never waits on a diff. Screens are
// immutable: a writer may keep its last one as long as it likes, diff it
// from any goroutine, and share it with other writers. While nothing
// changes, Snapshot returns the same pointer, and Diff of a screen with
// itself is empty, so a writer can compare pointers to skip a frame.
// Intermediate screens a writer never took are skipped, which is what
// makes bulk output cheap: a frame costs what changed on the screen, not
// what the shell printed.
//
// The emulator follows xterm, and where xterm.js and SwiftTerm, the two
// clients, differ from xterm, it follows them. It keeps no scrollback:
// lines that leave the top of the screen are sent to clients as scrolled
// lines, and the clients keep them.
package terminal

import (
	"sync"
)

// MaxCols and MaxRows bound a terminal's size. A screen's cells take
// memory in proportion to its area, and each viewer keeps the screens it
// was sent, so a size that a client asks for is clamped to them, here and
// before it reaches a backend.
const (
	MaxCols = 1000
	MaxRows = 500
)

// Options configure a Terminal.
type Options struct {
	// Reply receives the terminal's answers to the application's queries
	// (device attributes, cursor position, mode reports, window size),
	// which belong on the application's input. It is called from Write
	// after the terminal's lock is released; p is not reused.
	Reply func(p []byte)
}

// Terminal is a headless terminal emulator. Write and Resize are called
// from one goroutine; Snapshot may be called from any goroutine at the
// same time.
type Terminal struct {
	mu sync.Mutex

	cols, rows int
	main, alt  []*line
	altActive  bool

	cur   cursor
	saved [2]cursor // DECSC, for the primary and the alternate screen
	// top and bot are the scroll region's first and last row.
	top, bot int

	modes         Mode
	insert        bool // IRM
	origin        bool // DECOM
	newline       bool // LNM
	mouse         MouseTracking
	mouseEnc      MouseEncoding
	cursorVisible bool
	cursorShape   int

	tabs   []bool
	title  string
	titles []string // XTWINOPS 22 and 23
	last   rune     // the last character drawn, for REP

	p parser

	reply   func([]byte)
	replies []byte

	scrolled uint64
	version  uint64
	dirty    bool
	snap     *Screen
	spare    *line // a line that left the screen and can be reused
}

// clampSize clamps a size to at least 1 by 1 and at most MaxCols by
// MaxRows.
func clampSize(cols, rows int) (int, int) {
	return min(max(cols, 1), MaxCols), min(max(rows, 1), MaxRows)
}

// cursor is the cursor's position and the state DECSC saves with it.
type cursor struct {
	x, y     int
	wrap     bool // pending wrap
	pen      Style
	origin   bool
	charsets [2]charset // G0 and G1
	gl       int        // the charset in use, 0 or 1
}

// charset is a character set designated by SCS.
type charset uint8

const (
	charsetASCII   charset = iota
	charsetDECLine         // DEC Special Graphics, ESC ( 0
	charsetUK              // ESC ( A, where # is £
)

// New returns a terminal of cols columns and rows rows, at least 1 each
// and at most MaxCols and MaxRows.
func New(cols, rows int, opts Options) *Terminal {
	cols, rows = clampSize(cols, rows)
	t := &Terminal{cols: cols, rows: rows, reply: opts.Reply}
	t.reset()
	t.version, t.dirty = 1, false
	return t
}

// reset puts the terminal in its initial state, as RIS does. The title
// stays, as in xterm.
func (t *Terminal) reset() {
	t.main = t.newScreen()
	t.alt = t.newScreen()
	t.altActive = false
	t.cur = cursor{}
	t.saved = [2]cursor{}
	t.top, t.bot = 0, t.rows-1
	t.modes = defaultModes
	t.insert, t.origin, t.newline = false, false, false
	t.mouse, t.mouseEnc = MouseNone, MouseEncodingDefault
	t.cursorVisible, t.cursorShape = true, 0
	t.resetTabs()
	t.titles = nil
	t.last = 0
	t.dirty = true
}

func (t *Terminal) newScreen() []*line {
	ls := make([]*line, t.rows)
	for i := range ls {
		ls[i] = newLine(t.cols, blankCell)
	}
	return ls
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for x := 8; x < t.cols; x += 8 {
		t.tabs[x] = true
	}
}

// Write feeds output of the application to the terminal.
func (t *Terminal) Write(p []byte) {
	if replies := t.feed(p); replies != nil {
		t.reply(replies)
	}
}

// feed applies p under the terminal's lock and returns the replies to
// send once it is released. A panic releases the lock too, so that
// Snapshot, which viewers' writers and the session's owner goroutine
// call, does not wait forever after the session recovers from it.
func (t *Terminal) feed(p []byte) (replies []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.write(p)
	if t.dirty {
		t.version++
		t.dirty = false
	}
	if len(t.replies) > 0 {
		if t.reply != nil {
			replies, t.replies = t.replies, nil
		} else {
			t.replies = t.replies[:0]
		}
	}
	return replies
}

// Resize changes the terminal's size to cols columns and rows rows, at
// least 1 each and at most MaxCols and MaxRows. Lines are cut or padded, not reflowed; when the screen
// gets shorter, lines leave the top as far as needed to keep the cursor on
// it, as in xterm. The scroll region is reset.
func (t *Terminal) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}
	for i := range t.saved {
		t.saved[i].x = min(t.saved[i].x, cols-1)
		t.saved[i].y = min(t.saved[i].y, rows-1)
		t.saved[i].wrap = false
	}
	shift := 0
	if t.cur.y >= rows {
		shift = t.cur.y - rows + 1
	}
	// The screen shown keeps its cursor; the other keeps its top lines.
	mainShift, altShift := shift, 0
	if t.altActive {
		mainShift, altShift = 0, shift
	}
	t.main = resizeLines(t.main, cols, rows, mainShift)
	t.alt = resizeLines(t.alt, cols, rows, altShift)
	oldCols := t.cols
	t.cols, t.rows = cols, rows
	t.cur.y -= shift
	t.cur.x = min(t.cur.x, cols-1)
	t.cur.wrap = false
	t.top, t.bot = 0, rows-1
	tabs := make([]bool, cols)
	copy(tabs, t.tabs)
	for x := oldCols; x < cols; x++ {
		tabs[x] = x%8 == 0 && x > 0
	}
	t.tabs = tabs
	t.spare = nil
	t.version++
}

func resizeLines(ls []*line, cols, rows, shift int) []*line {
	ls = ls[shift:]
	out := make([]*line, rows)
	for y := range out {
		if y >= len(ls) {
			out[y] = newLine(cols, blankCell)
			continue
		}
		out[y] = resizeLine(ls[y], cols)
	}
	return out
}

func resizeLine(l *line, cols int) *line {
	if cols == len(l.cells) {
		return l
	}
	n := &line{cells: make([]cell, cols)}
	for x := range n.cells {
		if x >= len(l.cells) {
			n.cells[x] = blankCell
			continue
		}
		c := l.cells[x]
		if c.comb != 0 {
			n.comb = append(n.comb, l.comb[c.comb-1])
			c.comb = uint16(len(n.comb)) // #nosec G115 -- at most one per cell
		}
		n.cells[x] = c
	}
	// A wide character cut in half by the new edge is blanked.
	if cols < len(l.cells) && n.cells[cols-1].w == 2 {
		n.cells[cols-1] = blankCell
	}
	return n
}

// Snapshot returns the terminal's current state. The screen is immutable
// and shares unchanged lines with earlier snapshots; while nothing
// changes, Snapshot returns the same pointer.
func (t *Terminal) Snapshot() *Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snap != nil && t.snap.Version == t.version {
		return t.snap
	}
	s := &Screen{
		Version: t.version,
		Cols:    t.cols,
		Rows:    t.rows,
		Cursor: Cursor{
			X:           t.cur.x,
			Y:           t.cur.y,
			Visible:     t.cursorVisible,
			Shape:       t.cursorShape,
			PendingWrap: t.cur.wrap,
		},
		Modes:     t.modes,
		Mouse:     t.mouse,
		MouseEnc:  t.mouseEnc,
		Title:     t.title,
		AltScreen: t.altActive,
		main:      freezeLines(t.main),
		scrolled:  t.scrolled,
	}
	if t.altActive {
		s.alt = freezeLines(t.alt)
	}
	s.stamp(t.snap)
	t.snap = s
	return s
}

// stamp gives each row of s, and its cursor, the version at which they
// came to look as they do, from those of the snapshot before.
func (s *Screen) stamp(prev *Screen) {
	ls := s.lines()
	s.rowVersions = make([]uint64, len(ls))
	same := prev != nil && prev.Cols == s.Cols && prev.Rows == s.Rows
	var old []*line
	if same {
		old = prev.lines()
	}
	for y, l := range ls {
		if same && l.equal(old[y]) {
			s.rowVersions[y] = prev.rowVersions[y]
		} else {
			s.rowVersions[y] = s.Version
		}
	}
	s.cursorVersion = s.Version
	if same && prev.Cursor == s.Cursor {
		s.cursorVersion = prev.cursorVersion
	}
}

func freezeLines(ls []*line) []*line {
	out := make([]*line, len(ls))
	for i, l := range ls {
		l.freeze()
		out[i] = l
	}
	return out
}
