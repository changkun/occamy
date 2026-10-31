// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// blankCell is an erased cell in the default style.
var blankCell = cell{r: ' ', w: 1}

// lines returns the lines of the screen shown.
func (t *Terminal) lines() []*line {
	if t.altActive {
		return t.alt
	}
	return t.main
}

// line returns row y of the screen shown, ready to be written: a line
// that a snapshot holds is copied first.
func (t *Terminal) line(y int) *line {
	ls := t.lines()
	l := ls[y]
	if l.frozen {
		l = l.clone()
		ls[y] = l
	}
	return l
}

// erased is a cell erased with the current pen: xterm.js and SwiftTerm
// fill erased cells with the pen's background colour and nothing else.
func (t *Terminal) erased() cell {
	return cell{r: ' ', w: 1, st: Style{Bg: t.cur.pen.Bg}}
}

// blankLine returns a line of erased cells, reusing one that left the
// screen when it can.
func (t *Terminal) blankLine() *line {
	if l := t.spare; l != nil {
		t.spare = nil
		l.reset(t.erased())
		return l
	}
	return newLine(t.cols, t.erased())
}

// discard offers a line that left the screen for reuse.
func (t *Terminal) discard(l *line) {
	if !l.frozen {
		t.spare = l
	}
}

// execute performs a C0 control.
func (t *Terminal) execute(c byte) {
	switch c {
	case 0x08: // BS
		t.cur.wrap = false
		if t.cur.x > 0 {
			t.cur.x--
		}
	case 0x09: // HT
		t.tab(1)
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF
		t.index()
		if t.newline {
			t.cur.x = 0
		}
	case 0x0d: // CR
		t.cur.x = 0
		t.cur.wrap = false
	case 0x0e: // SO
		t.cur.gl = 1
	case 0x0f: // SI
		t.cur.gl = 0
	default:
		return
	}
	t.dirty = true
}

// printASCII draws a run of printable ASCII.
func (t *Terminal) printASCII(b []byte) {
	t.dirty = true
	if t.cur.charsets[t.cur.gl] != charsetASCII || t.insert {
		for _, c := range b {
			t.print(rune(c))
		}
		return
	}
	pen := t.cur.pen
	for len(b) > 0 {
		if t.cur.wrap {
			if t.modes&ModeAutoWrap == 0 {
				// Without auto-wrap, the rest overwrites the last column.
				l := t.line(t.cur.y)
				l.splitWide(t.cols-1, t.erased())
				l.cells[t.cols-1] = cell{r: rune(b[len(b)-1]), st: pen, w: 1}
				t.last = rune(b[len(b)-1])
				return
			}
			t.wrapLine()
		}
		l := t.line(t.cur.y)
		x := t.cur.x
		n := min(len(b), t.cols-x)
		l.splitWide(x, t.erased())
		l.splitWide(x+n, t.erased())
		cells := l.cells[x : x+n]
		for i := range cells {
			cells[i] = cell{r: rune(b[i]), st: pen, w: 1}
		}
		t.last = rune(b[n-1])
		b = b[n:]
		if x+n < t.cols {
			t.cur.x = x + n
			continue
		}
		t.cur.x = t.cols - 1
		t.cur.wrap = true
	}
}

// wrapLine moves the cursor from a pending wrap to the start of the next
// line. Without auto-wrap, it only ends the pending wrap: the cursor stays
// in the last column, which the next character replaces.
func (t *Terminal) wrapLine() {
	if t.modes&ModeAutoWrap == 0 {
		t.cur.wrap = false
		return
	}
	t.line(t.cur.y).wrapped = true
	t.cur.wrap = false
	t.cur.x = 0
	t.index()
}

// print draws one code point.
func (t *Terminal) print(r rune) {
	t.dirty = true
	if r < 0x7f {
		switch t.cur.charsets[t.cur.gl] {
		case charsetDECLine:
			if r >= 0x5f && r < 0x7f {
				r = decLine[r-0x5f]
			}
		case charsetUK:
			if r == '#' {
				r = '£'
			}
		}
	}
	w := runeWidth(r)
	switch {
	case w < 0:
		return
	case w == 0:
		t.combine(r)
		return
	}
	if t.cur.wrap {
		t.wrapLine()
	}
	if w == 2 && t.cur.x == t.cols-1 {
		if t.modes&ModeAutoWrap == 0 || t.cols < 2 {
			t.cur.wrap = false
			// It does not fit, and xterm.js drops it.
			return
		}
		l := t.line(t.cur.y)
		l.splitWide(t.cur.x, t.erased())
		l.cells[t.cur.x] = t.erased()
		t.wrapLine()
	}
	l := t.line(t.cur.y)
	x := t.cur.x
	if t.insert {
		t.insertCells(l, x, w)
	}
	l.splitWide(x, t.erased())
	l.splitWide(x+w, t.erased())
	l.cells[x] = cell{r: r, st: t.cur.pen, w: uint8(w)} // #nosec G115 -- w is 1 or 2
	if w == 2 {
		l.cells[x+1] = cell{st: t.cur.pen}
	}
	t.last = r
	if x+w < t.cols {
		t.cur.x = x + w
	} else {
		t.cur.x = t.cols - 1
		t.cur.wrap = true
	}
}

// combine joins a zero-width code point to the character before the
// cursor.
func (t *Terminal) combine(r rune) {
	x := t.cur.x - 1
	if t.cur.wrap {
		x = t.cur.x
	}
	if x < 0 {
		return
	}
	l := t.line(t.cur.y)
	if l.cells[x].w == 0 && x > 0 {
		x--
	}
	if l.cells[x].w == 0 {
		return
	}
	l.addComb(x, r)
}

// splitWide blanks both halves of a wide character that column x would
// cut, before cells from x on are changed.
func (l *line) splitWide(x int, blank cell) {
	if x <= 0 || x >= len(l.cells) || l.cells[x].w != 0 {
		return
	}
	l.cells[x-1] = blank
	l.cells[x] = blank
}

// index moves the cursor down, scrolling at the bottom of the scroll
// region (IND, and LF).
func (t *Terminal) index() {
	t.cur.wrap = false
	switch {
	case t.cur.y == t.bot:
		t.scrollUp(1)
	case t.cur.y < t.rows-1:
		t.cur.y++
	}
}

// reverseIndex moves the cursor up, scrolling at the top of the scroll
// region (RI).
func (t *Terminal) reverseIndex() {
	t.cur.wrap = false
	switch {
	case t.cur.y == t.top:
		t.scrollDown(1)
	case t.cur.y > 0:
		t.cur.y--
	}
}

// scrollUp moves the lines of the scroll region up by n.
func (t *Terminal) scrollUp(n int) {
	ls := t.lines()
	h := t.bot - t.top + 1
	n = min(n, h)
	if !t.altActive && t.top == 0 && t.bot == t.rows-1 {
		t.scrolled += uint64(n) // #nosec G115 -- n is positive
	}
	for range n {
		l := ls[t.top]
		copy(ls[t.top:t.bot], ls[t.top+1:t.bot+1])
		t.discard(l)
		ls[t.bot] = t.blankLine()
	}
}

// scrollDown moves the lines of the scroll region down by n.
func (t *Terminal) scrollDown(n int) {
	ls := t.lines()
	h := t.bot - t.top + 1
	n = min(n, h)
	for range n {
		l := ls[t.bot]
		copy(ls[t.top+1:t.bot+1], ls[t.top:t.bot])
		t.discard(l)
		ls[t.top] = t.blankLine()
	}
}

// insertLines inserts n blank lines at the cursor (IL).
func (t *Terminal) insertLines(n int) {
	if t.cur.y < t.top || t.cur.y > t.bot {
		return
	}
	top := t.top
	t.top = t.cur.y
	t.scrollDown(n)
	t.top = top
	t.cur.x, t.cur.wrap = 0, false
}

// deleteLines deletes n lines at the cursor (DL).
func (t *Terminal) deleteLines(n int) {
	if t.cur.y < t.top || t.cur.y > t.bot {
		return
	}
	top, scrolled := t.top, t.scrolled
	t.top = t.cur.y
	t.scrollUp(n)
	t.top, t.scrolled = top, scrolled
	t.cur.x, t.cur.wrap = 0, false
}

// insertCells shifts the cells from x on right by n, inserting blanks
// (ICH, and IRM).
func (t *Terminal) insertCells(l *line, x, n int) {
	blank := t.erased()
	l.splitWide(x, blank)
	n = min(n, t.cols-x)
	copy(l.cells[x+n:], l.cells[x:t.cols-n])
	for i := x; i < x+n; i++ {
		l.cells[i] = blank
	}
	if l.cells[t.cols-1].w == 2 {
		l.cells[t.cols-1] = blank
	}
}

// deleteCells removes n cells at x, shifting the rest left (DCH).
func (t *Terminal) deleteCells(l *line, x, n int) {
	blank := t.erased()
	l.splitWide(x, blank)
	n = min(n, t.cols-x)
	l.splitWide(x+n, blank)
	copy(l.cells[x:], l.cells[x+n:])
	for i := t.cols - n; i < t.cols; i++ {
		l.cells[i] = blank
	}
}

// eraseCells erases the cells [x0, x1) of row y.
func (t *Terminal) eraseCells(y, x0, x1 int) {
	if x0 >= x1 {
		return
	}
	l := t.line(y)
	blank := t.erased()
	l.splitWide(x0, blank)
	l.splitWide(x1, blank)
	for x := x0; x < x1; x++ {
		l.cells[x] = blank
	}
	if x0 == 0 && x1 == t.cols {
		l.wrapped = false
	}
}

// eraseLines erases the rows [y0, y1) completely.
func (t *Terminal) eraseLines(y0, y1 int) {
	ls := t.lines()
	blank := t.erased()
	for y := y0; y < y1; y++ {
		if ls[y].frozen {
			ls[y] = newLine(t.cols, blank)
		} else {
			ls[y].reset(blank)
		}
	}
}

// tab moves the cursor to the n-th next tab stop (HT, CHT).
func (t *Terminal) tab(n int) {
	t.cur.wrap = false
	for ; n > 0 && t.cur.x < t.cols-1; n-- {
		t.cur.x++
		for t.cur.x < t.cols-1 && !t.tabs[t.cur.x] {
			t.cur.x++
		}
	}
}

// backTab moves the cursor to the n-th previous tab stop (CBT).
func (t *Terminal) backTab(n int) {
	t.cur.wrap = false
	for ; n > 0 && t.cur.x > 0; n-- {
		t.cur.x--
		for t.cur.x > 0 && !t.tabs[t.cur.x] {
			t.cur.x--
		}
	}
}

// moveTo moves the cursor to column x and row y, which origin mode makes
// relative to the scroll region (CUP).
func (t *Terminal) moveTo(x, y int) {
	minY, maxY := 0, t.rows-1
	if t.origin {
		y += t.top
		minY, maxY = t.top, t.bot
	}
	t.cur.x = min(max(x, 0), t.cols-1)
	t.cur.y = min(max(y, minY), maxY)
	t.cur.wrap = false
}

// moveUp and moveDown move the cursor vertically, stopping at the scroll
// region's edge when the cursor is inside it (CUU, CUD).
func (t *Terminal) moveUp(n int) {
	limit := 0
	if t.cur.y >= t.top {
		limit = t.top
	}
	t.cur.y = max(t.cur.y-n, limit)
	t.cur.wrap = false
}

func (t *Terminal) moveDown(n int) {
	limit := t.rows - 1
	if t.cur.y <= t.bot {
		limit = t.bot
	}
	t.cur.y = min(t.cur.y+n, limit)
	t.cur.wrap = false
}

// saveCursor and restoreCursor implement DECSC and DECRC, per screen.
func (t *Terminal) saveCursor() {
	t.cur.origin = t.origin
	t.saved[t.screenIndex()] = t.cur
}

func (t *Terminal) restoreCursor() {
	t.cur = t.saved[t.screenIndex()]
	t.origin = t.cur.origin
	t.cur.x = min(t.cur.x, t.cols-1)
	t.cur.y = min(t.cur.y, t.rows-1)
}

func (t *Terminal) screenIndex() int {
	if t.altActive {
		return 1
	}
	return 0
}

// setAltScreen switches between the primary and the alternate screen.
func (t *Terminal) setAltScreen(on, clear bool) {
	if on == t.altActive {
		return
	}
	t.altActive = on
	if on && clear {
		t.eraseLines(0, t.rows)
	}
	t.cur.wrap = false
}

// escDispatch performs an escape sequence.
func (t *Terminal) escDispatch(final, inter byte) {
	if final == '\\' {
		return // ST, which ends a string
	}
	t.dirty = true
	switch inter {
	case 0:
		switch final {
		case '7': // DECSC
			t.saveCursor()
		case '8': // DECRC
			t.restoreCursor()
		case 'D': // IND
			t.index()
		case 'E': // NEL
			t.index()
			t.cur.x = 0
		case 'H': // HTS
			t.tabs[t.cur.x] = true
		case 'M': // RI
			t.reverseIndex()
		case 'c': // RIS
			t.reset()
		case '=': // DECKPAM
			t.modes |= ModeKeypad
		case '>': // DECKPNM
			t.modes &^= ModeKeypad
		}
	case '(', ')':
		cs := charsetASCII
		switch final {
		case '0':
			cs = charsetDECLine
		case 'A':
			cs = charsetUK
		}
		t.cur.charsets[inter-'('] = cs
	case '#':
		if final == '8' { // DECALN
			t.top, t.bot = 0, t.rows-1
			e := cell{r: 'E', w: 1}
			for y := range t.rows {
				l := t.line(y)
				l.reset(e)
			}
			t.moveTo(0, 0)
		}
	}
}

// csiDispatch performs a control sequence.
func (t *Terminal) csiDispatch(final byte) {
	p := &t.p
	if p.marker != 0 && p.marker != '?' && p.marker != '>' {
		return
	}
	switch p.inter {
	case 0:
	case ' ':
		if final == 'q' && p.marker == 0 { // DECSCUSR
			if s := p.param(0, 0); s <= 6 {
				t.cursorShape = s
				t.dirty = true
			}
		}
		return
	case '$':
		if final == 'p' { // DECRQM
			t.reportMode()
		}
		return
	case '!':
		if final == 'p' { // DECSTR
			t.softReset()
		}
		return
	default:
		return
	}
	if p.marker == '>' {
		switch final {
		case 'c': // DA2
			t.answer("\x1b[>0;276;0c")
		case 'q': // XTVERSION
			t.answer("\x1bP>|occamy\x1b\\")
		}
		return
	}
	if p.marker == '?' {
		switch final {
		case 'h', 'l':
			for i := range p.n {
				t.setPrivateMode(p.param(i, 0), final == 'h')
			}
			t.dirty = true
		}
		return
	}
	switch final {
	case 'c': // DA1
		if p.param(0, 0) == 0 {
			t.answer("\x1b[?1;2c")
		}
		return
	case 'n': // DSR
		switch p.param(0, 0) {
		case 5:
			t.answer("\x1b[0n")
		case 6:
			y := t.cur.y
			if t.origin {
				y -= t.top
			}
			t.answer("\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(t.cur.x+1) + "R")
		}
		return
	case 't': // XTWINOPS
		t.windowOp()
		return
	}
	t.dirty = true
	switch final {
	case '@': // ICH
		t.cur.wrap = false
		t.insertCells(t.line(t.cur.y), t.cur.x, p.count(0))
	case 'A': // CUU
		t.moveUp(p.count(0))
	case 'B', 'e': // CUD, VPR
		t.moveDown(p.count(0))
	case 'C', 'a': // CUF, HPR
		t.cur.x = min(t.cur.x+p.count(0), t.cols-1)
		t.cur.wrap = false
	case 'D': // CUB
		t.cur.x = max(t.cur.x-p.count(0), 0)
		t.cur.wrap = false
	case 'E': // CNL
		t.moveDown(p.count(0))
		t.cur.x = 0
	case 'F': // CPL
		t.moveUp(p.count(0))
		t.cur.x = 0
	case 'G', '`': // CHA, HPA
		t.cur.x = min(p.count(0)-1, t.cols-1)
		t.cur.wrap = false
	case 'H', 'f': // CUP, HVP
		t.moveTo(p.count(1)-1, p.count(0)-1)
	case 'I': // CHT
		t.tab(p.count(0))
	case 'J': // ED
		t.cur.wrap = false
		switch p.param(0, 0) {
		case 0:
			t.eraseCells(t.cur.y, t.cur.x, t.cols)
			t.eraseLines(t.cur.y+1, t.rows)
		case 1:
			t.eraseLines(0, t.cur.y)
			t.eraseCells(t.cur.y, 0, t.cur.x+1)
		case 2:
			t.eraseLines(0, t.rows)
		}
	case 'K': // EL
		t.cur.wrap = false
		switch p.param(0, 0) {
		case 0:
			t.eraseCells(t.cur.y, t.cur.x, t.cols)
		case 1:
			t.eraseCells(t.cur.y, 0, t.cur.x+1)
		case 2:
			t.eraseCells(t.cur.y, 0, t.cols)
		}
	case 'L': // IL
		t.insertLines(p.count(0))
	case 'M': // DL
		t.deleteLines(p.count(0))
	case 'P': // DCH
		t.cur.wrap = false
		t.deleteCells(t.line(t.cur.y), t.cur.x, p.count(0))
	case 'S': // SU
		t.scrollUp(p.count(0))
	case 'T': // SD
		if p.n <= 1 {
			t.scrollDown(p.count(0))
		}
	case 'X': // ECH
		t.cur.wrap = false
		t.eraseCells(t.cur.y, t.cur.x, min(t.cur.x+p.count(0), t.cols))
	case 'Z': // CBT
		t.backTab(p.count(0))
	case 'b': // REP
		if t.last != 0 {
			for range min(p.count(0), t.cols*t.rows) {
				t.print(t.last)
			}
		}
	case 'd': // VPA
		t.moveTo(t.cur.x, p.count(0)-1)
	case 'g': // TBC
		switch p.param(0, 0) {
		case 0:
			t.tabs[t.cur.x] = false
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l': // SM, RM
		for i := range p.n {
			switch p.param(i, 0) {
			case 4:
				t.insert = final == 'h'
			case 20:
				t.newline = final == 'h'
			}
		}
	case 'm': // SGR
		t.sgr()
	case 'r': // DECSTBM
		top, bot := p.count(0)-1, p.param(1, t.rows)
		if bot == 0 {
			bot = t.rows
		}
		bot = min(bot, t.rows) - 1
		if top < bot {
			t.top, t.bot = top, bot
			t.moveTo(0, 0)
		}
	case 's': // SCOSC
		t.saveCursor()
	case 'u': // SCORC
		t.restoreCursor()
	}
}

// setPrivateMode performs DECSET and DECRST for mode m.
func (t *Terminal) setPrivateMode(m int, on bool) {
	set := func(f Mode) {
		if on {
			t.modes |= f
		} else {
			t.modes &^= f
		}
	}
	switch m {
	case 1:
		set(ModeCursorKeys)
	case 6:
		t.origin = on
		t.moveTo(0, 0)
	case 7:
		set(ModeAutoWrap)
	case 9, 1000, 1002, 1003:
		t.mouse = MouseNone
		if on {
			t.mouse = mouseTracking(m)
		}
	case 1005, 1006, 1015, 1016:
		enc := mouseEncoding(m)
		switch {
		case on:
			t.mouseEnc = enc
		case t.mouseEnc == enc:
			t.mouseEnc = MouseEncodingDefault
		}
	case 12:
		// Blinking toggles between the blinking and steady shapes.
		s := t.cursorShape
		if s == 0 {
			s = 1
		}
		if on {
			s = (s+1)/2*2 - 1
		} else {
			s = (s + 1) / 2 * 2
		}
		t.cursorShape = s
	case 25:
		t.cursorVisible = on
	case 66:
		set(ModeKeypad)
	case 1004:
		set(ModeFocus)
	case 2004:
		set(ModeBracketedPaste)
	case 47, 1047:
		// xterm keeps the alternate screen's content for 47; xterm.js and
		// SwiftTerm clear it on every switch, and so does this terminal.
		t.setAltScreen(on, true)
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		if on {
			if !t.altActive {
				t.saveCursor()
				t.setAltScreen(true, true)
			}
		} else if t.altActive {
			t.setAltScreen(false, false)
			t.restoreCursor()
		}
	}
}

func mouseTracking(m int) MouseTracking {
	switch m {
	case 9:
		return MouseX10
	case 1000:
		return MouseNormal
	case 1002:
		return MouseButton
	}
	return MouseAny
}

func mouseEncoding(m int) MouseEncoding {
	switch m {
	case 1005:
		return MouseEncodingUTF8
	case 1006:
		return MouseEncodingSGR
	case 1015:
		return MouseEncodingURXVT
	}
	return MouseEncodingSGRPixels
}

// privateModeState returns the DECRQM state of mode m: 1 set, 2 reset, 0
// not recognized.
func (t *Terminal) privateModeState(m int) int {
	is := func(on bool) int {
		if on {
			return 1
		}
		return 2
	}
	switch m {
	case 1:
		return is(t.modes&ModeCursorKeys != 0)
	case 6:
		return is(t.origin)
	case 7:
		return is(t.modes&ModeAutoWrap != 0)
	case 9:
		return is(t.mouse == MouseX10)
	case 1000:
		return is(t.mouse == MouseNormal)
	case 1002:
		return is(t.mouse == MouseButton)
	case 1003:
		return is(t.mouse == MouseAny)
	case 1004:
		return is(t.modes&ModeFocus != 0)
	case 1005:
		return is(t.mouseEnc == MouseEncodingUTF8)
	case 1006:
		return is(t.mouseEnc == MouseEncodingSGR)
	case 1015:
		return is(t.mouseEnc == MouseEncodingURXVT)
	case 1016:
		return is(t.mouseEnc == MouseEncodingSGRPixels)
	case 25:
		return is(t.cursorVisible)
	case 66:
		return is(t.modes&ModeKeypad != 0)
	case 47, 1047, 1049:
		return is(t.altActive)
	case 2004:
		return is(t.modes&ModeBracketedPaste != 0)
	}
	return 0
}

func (t *Terminal) reportMode() {
	p := &t.p
	m := p.param(0, 0)
	if p.marker == '?' {
		t.answer("\x1b[?" + strconv.Itoa(m) + ";" + strconv.Itoa(t.privateModeState(m)) + "$y")
		return
	}
	state := 0
	switch m {
	case 4:
		state = 2
		if t.insert {
			state = 1
		}
	case 20:
		state = 2
		if t.newline {
			state = 1
		}
	}
	t.answer("\x1b[" + strconv.Itoa(m) + ";" + strconv.Itoa(state) + "$y")
}

// softReset performs DECSTR, as xterm.js does.
func (t *Terminal) softReset() {
	t.dirty = true
	t.cursorVisible = true
	t.insert, t.origin = false, false
	t.modes = t.modes&^(ModeCursorKeys|ModeKeypad) | ModeAutoWrap
	t.top, t.bot = 0, t.rows-1
	t.cur.pen = Style{}
	t.cur.charsets = [2]charset{}
	t.cur.gl = 0
	t.cur.wrap = false
	t.saved = [2]cursor{}
}

// windowOp performs the parts of XTWINOPS that concern text: the title
// stack and the size report.
func (t *Terminal) windowOp() {
	p := &t.p
	switch p.param(0, 0) {
	case 18:
		t.answer("\x1b[8;" + strconv.Itoa(t.rows) + ";" + strconv.Itoa(t.cols) + "t")
	case 22:
		if p.param(1, 0) != 1 && len(t.titles) < 16 {
			t.titles = append(t.titles, t.title)
		}
	case 23:
		if p.param(1, 0) != 1 && len(t.titles) > 0 {
			t.title = t.titles[len(t.titles)-1]
			t.titles = t.titles[:len(t.titles)-1]
			t.dirty = true
		}
	}
}

// oscDispatch performs an operating system command. Only the title is
// kept.
func (t *Terminal) oscDispatch() {
	s := t.p.osc
	t.p.osc = t.p.osc[:0]
	cmd, rest, ok := strings.Cut(string(s), ";")
	if !ok || (cmd != "0" && cmd != "2") {
		return
	}
	t.title = sanitizeTitle(rest)
	t.dirty = true
}

// maxTitle is the longest title, in bytes, that an OSC 2 of at most
// maxOSC bytes carries whole, as Diff sends it.
const maxTitle = maxOSC - len("2;")

// sanitizeTitle makes a title safe to send in OSC 2: valid UTF-8 without
// C0 or C1 controls, which would end the sequence early, and at most
// maxTitle bytes, cut between characters, which the parser would cut
// anywhere. An invalid byte becomes U+FFFD, three bytes, so a title that
// came in whole may need the cut.
func sanitizeTitle(s string) string {
	ok := len(s) <= maxTitle && utf8.ValidString(s)
	if ok {
		for _, r := range s {
			if r < 0x20 || (r >= 0x7f && r < 0xa0) {
				ok = false
				break
			}
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxTitle {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// answer queues a reply to the application.
func (t *Terminal) answer(s string) {
	if t.reply != nil {
		t.replies = append(t.replies, s...)
	}
}

// sgr performs SGR.
func (t *Terminal) sgr() {
	p := &t.p
	pen := &t.cur.pen
	for i := 0; i < p.n; i++ {
		v := p.param(i, 0)
		// Subparameters belong to the parameter before them.
		next := i + 1
		for next < p.n && p.sub&(1<<next) != 0 {
			next++
		}
		switch {
		case v == 0:
			*pen = Style{}
		case v == 1:
			pen.Attrs |= Bold
		case v == 2:
			pen.Attrs |= Faint
		case v == 3:
			pen.Attrs |= Italic
		case v == 4:
			if next > i+1 && p.param(i+1, 1) == 0 {
				pen.Attrs &^= Underline
			} else {
				pen.Attrs |= Underline
			}
		case v == 5 || v == 6:
			pen.Attrs |= Blink
		case v == 7:
			pen.Attrs |= Inverse
		case v == 8:
			pen.Attrs |= Invisible
		case v == 9:
			pen.Attrs |= Strikethrough
		case v == 21:
			pen.Attrs |= Underline
		case v == 22:
			pen.Attrs &^= Bold | Faint
		case v == 23:
			pen.Attrs &^= Italic
		case v == 24:
			pen.Attrs &^= Underline
		case v == 25:
			pen.Attrs &^= Blink
		case v == 27:
			pen.Attrs &^= Inverse
		case v == 28:
			pen.Attrs &^= Invisible
		case v == 29:
			pen.Attrs &^= Strikethrough
		case v >= 30 && v <= 37:
			pen.Fg = BasicColor(uint8(v - 30)) // #nosec G115 -- 0 to 7
		case v == 38, v == 48, v == 58:
			c, n, ok := p.extendedColor(i, next)
			if ok && v == 38 {
				pen.Fg = c
			} else if ok && v == 48 {
				pen.Bg = c
			}
			if next == i+1 {
				next = i + 1 + n
			}
		case v == 39:
			pen.Fg = DefaultColor
		case v >= 40 && v <= 47:
			pen.Bg = BasicColor(uint8(v - 40)) // #nosec G115 -- 0 to 7
		case v == 49:
			pen.Bg = DefaultColor
		case v == 53:
			pen.Attrs |= Overline
		case v == 55:
			pen.Attrs &^= Overline
		case v >= 90 && v <= 97:
			pen.Fg = BasicColor(uint8(v - 90 + 8)) // #nosec G115 -- 8 to 15
		case v >= 100 && v <= 107:
			pen.Bg = BasicColor(uint8(v - 100 + 8)) // #nosec G115 -- 8 to 15
		}
		i = next - 1
	}
}

// extendedColor reads the colour of SGR 38, 48 or 58 at parameter i. In
// the colon form its parts are the subparameters up to next; in the
// semicolon form they are the parameters after i, and n is how many it
// used.
func (p *parser) extendedColor(i, next int) (c Color, n int, ok bool) {
	comp := func(j int) uint8 { return uint8(min(max(p.param(j, 0), 0), 255)) } // #nosec G115 -- clamped
	if next > i+1 {
		// 38:5:n, 38:2:cs:r:g:b, or 38:2:r:g:b.
		switch p.param(i+1, -1) {
		case 5:
			if next > i+2 {
				return IndexedColor(comp(i + 2)), 0, true
			}
		case 2:
			switch next - i {
			case 6:
				return RGBColor(comp(i+3), comp(i+4), comp(i+5)), 0, true
			case 5:
				return RGBColor(comp(i+2), comp(i+3), comp(i+4)), 0, true
			}
		}
		return 0, 0, false
	}
	switch p.param(i+1, -1) {
	case 5:
		if i+2 < p.n {
			return IndexedColor(comp(i + 2)), 2, true
		}
		return 0, p.n - i - 1, false
	case 2:
		if i+4 < p.n {
			return RGBColor(comp(i+2), comp(i+3), comp(i+4)), 4, true
		}
		return 0, p.n - i - 1, false
	}
	return 0, 0, false
}

// decLine maps 0x5f to 0x7e in the DEC Special Graphics set.
var decLine = [32]rune{
	' ', '◆', '▒', '␉', '␌', '␍', '␊', '°', '±', '␤', '␋', '┘', '┐', '┌', '└', '┼',
	'⎺', '⎻', '─', '⎼', '⎽', '├', '┤', '┴', '┬', '│', '≤', '≥', 'π', '≠', '£', '·',
}
