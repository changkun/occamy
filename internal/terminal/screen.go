// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"strings"
	"unicode/utf8"
)

// Color is the colour of a cell's text or background: the terminal's
// default, one of the 16 basic colours (SGR 30 to 37 and 90 to 97), one of
// the 256 indexed colours (SGR 38;5), or 24-bit RGB (SGR 38;2). The basic
// and indexed forms stay distinct even for the same index, because clients
// render them differently: xterm.js draws bold text in a basic colour
// brighter, but not the same index given as an indexed colour.
type Color uint32

// The kinds of Color, in its top byte.
const (
	colorDefault Color = 0
	colorBasic   Color = 1 << 24
	colorIndexed Color = 2 << 24
	colorRGB     Color = 3 << 24
	colorKind    Color = 0xff << 24
)

// DefaultColor is the terminal's default colour.
const DefaultColor Color = colorDefault

// BasicColor returns one of the 16 basic colours, 0 to 15.
func BasicColor(i uint8) Color { return colorBasic | Color(i&15) }

// IndexedColor returns one of the 256 indexed colours.
func IndexedColor(i uint8) Color { return colorIndexed | Color(i) }

// RGBColor returns a 24-bit colour.
func RGBColor(r, g, b uint8) Color {
	return colorRGB | Color(r)<<16 | Color(g)<<8 | Color(b)
}

// IsDefault reports whether c is the terminal's default colour.
func (c Color) IsDefault() bool { return c == colorDefault }

// Basic returns the index of a basic colour.
func (c Color) Basic() (uint8, bool) {
	return uint8(c & 15), c&colorKind == colorBasic // #nosec G115 -- masked to 4 bits
}

// Indexed returns the index of an indexed colour.
func (c Color) Indexed() (uint8, bool) {
	return uint8(c & 0xff), c&colorKind == colorIndexed // #nosec G115 -- masked to 8 bits
}

// RGB returns the components of a 24-bit colour.
func (c Color) RGB() (r, g, b uint8, ok bool) {
	// #nosec G115 -- each component is masked to 8 bits.
	return uint8(c >> 16 & 0xff), uint8(c >> 8 & 0xff), uint8(c & 0xff), c&colorKind == colorRGB
}

// Attr is a set of text attributes.
type Attr uint16

// Text attributes, set by SGR.
const (
	Bold          Attr = 1 << iota // SGR 1
	Faint                          // SGR 2
	Italic                         // SGR 3
	Underline                      // SGR 4 and 21; underline styles are not kept
	Blink                          // SGR 5 and 6
	Inverse                        // SGR 7
	Invisible                      // SGR 8
	Strikethrough                  // SGR 9
	Overline                       // SGR 53
)

// Style is how a cell is drawn.
type Style struct {
	Fg, Bg Color
	Attrs  Attr
}

// Cell is one cell of a screen.
type Cell struct {
	// Content is the grapheme drawn in the cell: one code point and the
	// zero-width code points (combining marks, joiners, variation
	// selectors) that followed it. A blank cell holds a space, and the
	// right half of a wide character holds "".
	Content string
	// Width is 1, or 2 for a wide character, whose right half is a cell of
	// width 0.
	Width int
	Style Style
}

// MouseTracking is the mouse reporting mode an application asked for.
type MouseTracking uint8

// Mouse reporting modes. Setting one replaces the other, and resetting
// any of them turns reporting off, as in xterm.
const (
	MouseNone   MouseTracking = iota
	MouseX10                  // DECSET 9: presses only
	MouseNormal               // DECSET 1000: presses and releases
	MouseButton               // DECSET 1002: and motion while a button is down
	MouseAny                  // DECSET 1003: and all motion
)

// MouseEncoding is how mouse reports are encoded.
type MouseEncoding uint8

// Mouse report encodings. Setting one replaces the other.
const (
	MouseEncodingDefault   MouseEncoding = iota // X10 bytes
	MouseEncodingUTF8                           // DECSET 1005
	MouseEncodingSGR                            // DECSET 1006
	MouseEncodingURXVT                          // DECSET 1015
	MouseEncodingSGRPixels                      // DECSET 1016
)

// Mode is a set of terminal modes that a client mirrors to encode input
// the way the application expects.
type Mode uint16

// Modes a client mirrors. Mouse reporting, the alternate screen and the
// cursor's visibility are kept separately, in Screen.
const (
	ModeCursorKeys     Mode = 1 << iota // DECCKM, DECSET 1: application cursor keys
	ModeKeypad                          // DECKPAM (ESC =) or DECSET 66: application keypad
	ModeAutoWrap                        // DECAWM, DECSET 7
	ModeFocus                           // DECSET 1004: focus in and out reports
	ModeBracketedPaste                  // DECSET 2004
)

// defaultModes are the modes of a fresh terminal.
const defaultModes = ModeAutoWrap

// Cursor is the text cursor.
type Cursor struct {
	X, Y    int
	Visible bool
	// Shape is the DECSCUSR parameter: 0 is the client's default, 1 and 2
	// a blinking and a steady block, 3 and 4 an underline, 5 and 6 a bar.
	Shape int
	// PendingWrap is set after a character was written into the last
	// column: the cursor stays in that column, and the next character
	// goes to the start of the next line, or replaces the last column
	// when auto-wrap is off. Clients track the same state (xterm.js as a
	// cursor one past the last column).
	PendingWrap bool
}

// Screen is an immutable copy of a terminal's state: what a viewer sees,
// and the modes it mirrors to encode input. Screens are taken with
// Terminal.Snapshot and compared with Diff.
type Screen struct {
	// Version increases with every change to the terminal. Two snapshots
	// of one terminal with the same version are the same pointer.
	Version uint64

	Cols, Rows int
	Cursor     Cursor
	Modes      Mode
	Mouse      MouseTracking
	MouseEnc   MouseEncoding
	Title      string
	// AltScreen is set while the alternate screen is shown (DECSET 47,
	// 1047 or 1049). The primary screen is kept underneath.
	AltScreen bool

	main, alt []*line // alt is nil unless AltScreen is set
	// rowVersions holds, for each row shown, the version at which it last
	// came to look as it does, and cursorVersion that of the cursor.
	rowVersions   []uint64
	cursorVersion uint64
	// scrolled counts the lines that have left the top of the primary
	// screen, scrolled out of a full-screen scroll region. Diff uses the
	// difference between two screens to scroll the client the same way.
	scrolled uint64
}

// rows returns the lines of the screen that is shown.
func (s *Screen) lines() []*line {
	if s.AltScreen {
		return s.alt
	}
	return s.main
}

// Cell returns the cell at column x of row y of the screen that is shown.
func (s *Screen) Cell(x, y int) Cell {
	if x < 0 || y < 0 || x >= s.Cols || y >= s.Rows {
		return Cell{}
	}
	l := s.lines()[y]
	c := &l.cells[x]
	return Cell{Content: l.content(x), Width: int(c.w), Style: c.st}
}

// Line returns the text of row y, with trailing blanks removed.
func (s *Screen) Line(y int) string {
	if y < 0 || y >= s.Rows {
		return ""
	}
	return s.lines()[y].text()
}

// String returns the text of the screen that is shown, one row per line,
// with trailing blanks removed from each.
func (s *Screen) String() string {
	var b strings.Builder
	for y := range s.Rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s.Line(y))
	}
	return b.String()
}

// Versions appends to dst, for each row shown from the top, the version
// at which the row last came to look as it does, then the version at
// which the cursor last moved or changed. A row redrawn the same, or a
// line that scrolled into a row that showed the same, keeps its version,
// so a row's version is not raised by output that changed nothing on
// it. The versions are those of snapshots: changes between two snapshots
// carry the later one's version.
func (s *Screen) Versions(dst []uint64) []uint64 {
	dst = append(dst, s.rowVersions...)
	return append(dst, s.cursorVersion)
}

// Equal reports whether s and o look the same to a viewer and set the same
// modes: size, cells, cursor, modes, title, and which screen is shown,
// including the primary screen kept under the alternate one. Versions are
// not compared.
func (s *Screen) Equal(o *Screen) bool {
	if s == o {
		return true
	}
	if s == nil || o == nil {
		return false
	}
	if s.Cols != o.Cols || s.Rows != o.Rows || s.Cursor != o.Cursor ||
		s.Modes != o.Modes || s.Mouse != o.Mouse || s.MouseEnc != o.MouseEnc ||
		s.Title != o.Title || s.AltScreen != o.AltScreen {
		return false
	}
	if !linesEqual(s.main, o.main) {
		return false
	}
	return !s.AltScreen || linesEqual(s.alt, o.alt)
}

func linesEqual(a, b []*line) bool {
	for i := range a {
		if !a[i].equal(b[i]) {
			return false
		}
	}
	return true
}

// cell is the stored form of a Cell.
type cell struct {
	r  rune // the first code point; 0 in the right half of a wide character
	st Style
	// comb is the 1-based index of the cell's full grapheme in its line's
	// comb, when zero-width code points follow r, and 0 otherwise.
	comb uint16
	w    uint8
}

// line is one row of cells. Once a snapshot holds a line it is frozen and
// never written again: the terminal copies it before the next change, so
// snapshots share every line that did not change.
type line struct {
	cells []cell
	comb  []string
	// hash is set when the line is frozen. Diff compares it to find lines
	// that are equal, and lines that moved.
	hash   uint64
	blank  bool // set when frozen: every cell is a space in the default style
	frozen bool
	// wrapped is set when the text continues on the next line because it
	// reached the last column.
	wrapped bool
}

// maxComb bounds the bytes of one cell's grapheme, so that a stream of
// combining marks cannot grow a line without bound.
const maxComb = 32

func newLine(cols int, blank cell) *line {
	l := &line{cells: make([]cell, cols)}
	for i := range l.cells {
		l.cells[i] = blank
	}
	return l
}

func (l *line) clone() *line {
	n := &line{cells: make([]cell, len(l.cells)), wrapped: l.wrapped}
	copy(n.cells, l.cells)
	if len(l.comb) > 0 {
		// Copy only the graphemes still in use, renumbered.
		for i := range n.cells {
			if c := &n.cells[i]; c.comb != 0 {
				n.comb = append(n.comb, l.comb[c.comb-1])
				c.comb = uint16(len(n.comb)) // #nosec G115 -- at most one per cell, and lines are far shorter than 65536
			}
		}
	}
	return n
}

// reset blanks the whole line for reuse.
func (l *line) reset(blank cell) {
	for i := range l.cells {
		l.cells[i] = blank
	}
	l.comb = l.comb[:0]
	l.wrapped = false
}

// content returns the grapheme of cell x.
func (l *line) content(x int) string {
	c := &l.cells[x]
	switch {
	case c.comb != 0:
		return l.comb[c.comb-1]
	case c.w == 0:
		return ""
	case c.r < utf8.RuneSelf:
		return asciiStrings[c.r : c.r+1]
	}
	return string(c.r)
}

// appendContent appends the grapheme of cell x to b.
func (l *line) appendContent(b []byte, x int) []byte {
	c := &l.cells[x]
	switch {
	case c.comb != 0:
		return append(b, l.comb[c.comb-1]...)
	case c.w == 0:
		return b
	case c.r < utf8.RuneSelf:
		return append(b, byte(c.r)) // #nosec G115 -- ASCII
	}
	return utf8.AppendRune(b, c.r)
}

// addComb appends the zero-width code point r to the grapheme of cell x.
func (l *line) addComb(x int, r rune) {
	c := &l.cells[x]
	var g string
	if c.comb != 0 {
		g = l.comb[c.comb-1]
	} else {
		g = string(c.r)
	}
	if len(g)+utf8.RuneLen(r) > maxComb {
		return
	}
	g += string(r)
	if c.comb != 0 {
		l.comb[c.comb-1] = g
		return
	}
	if len(l.comb) >= len(l.cells) {
		l.compactComb()
	}
	l.comb = append(l.comb, g)
	c.comb = uint16(len(l.comb)) // #nosec G115 -- compactComb keeps it below the line's width
}

// compactComb drops the graphemes of cells that were overwritten.
func (l *line) compactComb() {
	old := l.comb
	l.comb = make([]string, 0, len(l.cells))
	for i := range l.cells {
		if c := &l.cells[i]; c.comb != 0 {
			l.comb = append(l.comb, old[c.comb-1])
			c.comb = uint16(len(l.comb)) // #nosec G115 -- at most one per cell
		}
	}
}

func (l *line) text() string {
	var b []byte
	end := 0
	for x := range l.cells {
		if l.cells[x].w == 0 {
			continue
		}
		b = l.appendContent(b, x)
		if l.cells[x].r != ' ' || l.cells[x].comb != 0 {
			end = len(b)
		}
	}
	return string(b[:end])
}

// cellEqual reports whether cell x of l and cell y of o look the same.
func (l *line) cellEqual(x int, o *line, y int) bool {
	a, b := &l.cells[x], &o.cells[y]
	if a.r != b.r || a.w != b.w || a.st != b.st || (a.comb == 0) != (b.comb == 0) {
		return false
	}
	return a.comb == 0 || l.comb[a.comb-1] == o.comb[b.comb-1]
}

// equal reports whether two lines of the same width look the same. The
// wrapped flag is not compared: it changes no cell.
func (l *line) equal(o *line) bool {
	if l == o {
		return true
	}
	if l.frozen && o.frozen && (l.hash != o.hash || l.blank != o.blank) {
		return false
	}
	for x := range l.cells {
		if !l.cellEqual(x, o, x) {
			return false
		}
	}
	return true
}

// freeze marks the line as shared with a snapshot, and computes its hash.
func (l *line) freeze() {
	if l.frozen {
		return
	}
	l.frozen = true
	const prime = 1099511628211
	h := uint64(14695981039346656037)
	blank := true
	for x := range l.cells {
		c := &l.cells[x]
		h = (h ^ uint64(c.r) ^ uint64(c.w)<<32) * prime            // #nosec G115 -- r is a valid rune, never negative
		h = (h ^ uint64(c.st.Fg) ^ uint64(c.st.Attrs)<<32) * prime // #nosec G115 -- mixing bits only
		h = (h ^ uint64(c.st.Bg)) * prime
		if c.comb != 0 {
			for _, b := range []byte(l.comb[c.comb-1]) {
				h = (h ^ uint64(b)) * prime
			}
		}
		if c.r != ' ' || c.comb != 0 || c.st != (Style{}) {
			blank = false
		}
	}
	l.hash, l.blank = h, blank
}

// asciiStrings holds every ASCII character, so that a cell's content can
// be sliced from it without allocating.
var asciiStrings = func() string {
	b := make([]byte, utf8.RuneSelf)
	for i := range b {
		b[i] = byte(i)
	}
	return string(b)
}()
