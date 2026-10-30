// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"strconv"
)

// Diff returns the bytes that turn a client terminal showing from into
// one showing to. A nil from stands for a fresh terminal of to's size,
// which makes the result a snapshot for a late joiner.
//
// When the sizes differ, the client resizes its terminal to to's size
// before it writes the result (the protocol carries the size), and the
// result repaints both screens, so whatever the client did to its content
// on resize (reflow or truncation) does not matter.
//
// The result uses only sequences that xterm.js and SwiftTerm both
// implement:
//
//   - text as UTF-8, CR, LF and BS;
//   - CUP (CSI r;c H), CUU, CUD, CUF, CUB (CSI n A, B, C, D), CHA
//     (CSI n G) and VPA (CSI n d), chosen by their length;
//   - EL (CSI K) and ED (CSI J), which fill with the background colour of
//     the current SGR, as both clients do;
//   - SU and SD (CSI n S, CSI n T), within DECSTBM (CSI t;b r) for part of
//     the screen, and LF at the bottom row to scroll the whole primary
//     screen, which is the only scroll that both clients add to their
//     scrollback;
//   - SGR 0 to 9, 21 to 29, 53, 55, 30 to 37, 39, 40 to 47, 49, 90 to 97,
//     100 to 107, and 38 and 48 with ;5;n or ;2;r;g;b;
//   - DECSET and DECRST 1, 7, 25, 1004, 2004, 1049, the mouse modes 9,
//     1000, 1002 and 1003, and the mouse encodings 1005, 1006, 1015 and
//     1016 (xterm.js ignores 1005 and 1015);
//   - DECKPAM and DECKPNM (ESC = and ESC >), DECSCUSR (CSI n SP q), and
//     OSC 2 ended by BEL for the title.
//
// The client keeps the defaults for everything else: no scroll region
// between diffs, origin and insert modes off, ASCII character sets, and
// SGR 0 when a diff ends.
func Diff(from, to *Screen) []byte {
	if from == to || to == nil {
		return nil
	}
	e := &encoder{cols: to.Cols, rows: to.Rows}
	e.diff(from, to)
	return e.b
}

// encoder writes a diff and models what the client shows and where its
// cursor is while it does.
type encoder struct {
	b          []byte
	cols, rows int

	x, y     int
	xOK, yOK bool // whether the client's cursor column and row are known
	// pending is set when the client is known to be in a pending wrap at
	// column cols-1 of row y.
	pending bool
	pen     Style

	blanks map[Color]*line
}

// client is the state of the client that is not painted: modes, title
// and cursor appearance.
type clientState struct {
	modes    Mode
	mouse    MouseTracking
	mouseEnc MouseEncoding
	title    string
	visible  bool
	shape    int
}

func (e *encoder) diff(from, to *Screen) {
	var st clientState
	var fromMain, fromAlt []*line
	fromIsAlt := false
	var hint uint64
	switch {
	case from == nil:
		// A fresh terminal: blank, cursor at home, default modes.
		st = clientState{modes: defaultModes, visible: true}
		e.xOK, e.yOK = true, true
		fromMain = e.blankScreen()
	case from.Cols != to.Cols || from.Rows != to.Rows:
		// The client resized: its content is unknown, its modes are not.
		st = stateOf(from)
		if from.AltScreen {
			e.str("\x1b[?1049l")
		}
		e.str("\x1b[H\x1b[2J")
		e.x, e.y, e.xOK, e.yOK = 0, 0, true, true
		fromMain = e.blankScreen()
	default:
		st = stateOf(from)
		e.x, e.y = from.Cursor.X, from.Cursor.Y
		e.xOK, e.yOK = !from.Cursor.PendingWrap, true
		e.pending = from.Cursor.PendingWrap
		fromMain, fromAlt, fromIsAlt = from.main, from.alt, from.AltScreen
		if to.scrolled > from.scrolled {
			hint = to.scrolled - from.scrolled
		}
	}

	switch {
	case fromIsAlt && to.AltScreen && linesEqual(fromMain, to.main):
		e.paint(fromAlt, to.alt, 0, false)
	case fromIsAlt:
		e.setPen(Style{})
		e.str("\x1b[?1049l")
		e.lost()
		e.paint(fromMain, to.main, hint, true)
		if to.AltScreen {
			e.enterAlt(to)
		}
	default:
		e.paint(fromMain, to.main, hint, true)
		if to.AltScreen {
			e.enterAlt(to)
		}
	}

	if st.title != to.Title {
		e.str("\x1b]2;")
		e.str(to.Title)
		e.b = append(e.b, 0x07)
	}
	e.modes(st, to)
	if st.shape != to.Cursor.Shape {
		e.str("\x1b[")
		e.num(to.Cursor.Shape)
		e.str(" q")
	}
	c := to.Cursor
	if c.PendingWrap {
		if !e.pending || !e.yOK || e.y != c.Y {
			// Only writing into the last column puts the client in a
			// pending wrap.
			l := to.lines()[c.Y]
			x := e.cols - 1
			if x > 0 && l.cells[x].w == 0 {
				x--
			}
			e.moveTo(x, c.Y)
			e.writeCells(l, x, e.cols, c.Y)
		}
	} else {
		e.moveTo(c.X, c.Y)
	}
	e.setPen(Style{})
	if st.visible != c.Visible {
		if c.Visible {
			e.str("\x1b[?25h")
		} else {
			e.str("\x1b[?25l")
		}
	}
}

func stateOf(s *Screen) clientState {
	return clientState{
		modes:    s.Modes,
		mouse:    s.Mouse,
		mouseEnc: s.MouseEnc,
		title:    s.Title,
		visible:  s.Cursor.Visible,
		shape:    s.Cursor.Shape,
	}
}

// enterAlt switches the client to its alternate screen, which DECSET 1049
// clears, and paints to's.
func (e *encoder) enterAlt(to *Screen) {
	e.setPen(Style{})
	e.str("\x1b[?1049h")
	e.lost()
	e.paint(e.blankScreen(), to.alt, 0, false)
}

// lost forgets the client's cursor position.
func (e *encoder) lost() {
	e.xOK, e.yOK, e.pending = false, false, false
}

// modes sets the client's modes to to's.
func (e *encoder) modes(st clientState, to *Screen) {
	flag := func(m Mode, n int) {
		if st.modes&m == to.Modes&m {
			return
		}
		h := "l"
		if to.Modes&m != 0 {
			h = "h"
		}
		e.str("\x1b[?")
		e.num(n)
		e.str(h)
	}
	flag(ModeCursorKeys, 1)
	flag(ModeAutoWrap, 7)
	flag(ModeFocus, 1004)
	flag(ModeBracketedPaste, 2004)
	if st.modes&ModeKeypad != to.Modes&ModeKeypad {
		if to.Modes&ModeKeypad != 0 {
			e.str("\x1b=")
		} else {
			e.str("\x1b>")
		}
	}
	if st.mouse != to.Mouse {
		if to.Mouse == MouseNone {
			e.str("\x1b[?")
			e.num(mouseTrackingMode[st.mouse])
			e.str("l")
		} else {
			e.str("\x1b[?")
			e.num(mouseTrackingMode[to.Mouse])
			e.str("h")
		}
	}
	if st.mouseEnc != to.MouseEnc {
		if to.MouseEnc == MouseEncodingDefault {
			e.str("\x1b[?")
			e.num(mouseEncodingMode[st.mouseEnc])
			e.str("l")
		} else {
			e.str("\x1b[?")
			e.num(mouseEncodingMode[to.MouseEnc])
			e.str("h")
		}
	}
}

var (
	mouseTrackingMode = [...]int{MouseX10: 9, MouseNormal: 1000, MouseButton: 1002, MouseAny: 1003}
	mouseEncodingMode = [...]int{
		MouseEncodingUTF8: 1005, MouseEncodingSGR: 1006,
		MouseEncodingURXVT: 1015, MouseEncodingSGRPixels: 1016,
	}
)

// paint turns the client's screen, showing old, into one showing new. On
// the primary screen, hint is how many lines left its top in between.
func (e *encoder) paint(old, new []*line, hint uint64, primary bool) {
	e.setPen(Style{})
	v := make([]*line, len(old))
	copy(v, old)
	if primary && hint > 0 {
		// Lines left the top: scroll them into the client's scrollback.
		e.scrollOut(v, new, int(min(hint, uint64(e.rows)))) // #nosec G115 -- at most rows
		e.paintRows(v, new)
		return
	}
	k, top, bot, ok := detectScroll(v, new)
	if !ok {
		e.paintRows(v, new)
		return
	}
	// Try the scroll for the region found, for the whole screen, and no
	// scroll, and keep the shortest.
	type plan struct{ k, top, bot int }
	plans := []plan{{0, 0, 0}, {k, top, bot}}
	if top != 0 || bot != e.rows-1 {
		plans = append(plans, plan{k, 0, e.rows - 1})
	}
	start := e.save()
	var best []byte
	var bestState encoderState
	w := make([]*line, len(v))
	for i, p := range plans {
		e.restore(start)
		copy(w, v)
		if p.k != 0 {
			e.scroll(w, p.top, p.bot, p.k)
		}
		e.paintRows(w, new)
		if i == 0 || len(e.b)-start.n < len(best) {
			best = append(best[:0], e.b[start.n:]...)
			bestState = e.save()
		}
	}
	e.restore(start)
	e.b = append(e.b, best...)
	n := len(e.b)
	e.restore(bestState)
	e.b = e.b[:n]
}

// encoderState is what save keeps to try a plan and undo it.
type encoderState struct {
	n             int
	x, y          int
	xOK, yOK, pnd bool
	pen           Style
}

func (e *encoder) save() encoderState {
	return encoderState{len(e.b), e.x, e.y, e.xOK, e.yOK, e.pending, e.pen}
}

func (e *encoder) restore(s encoderState) {
	e.b = e.b[:s.n]
	e.x, e.y, e.xOK, e.yOK, e.pending, e.pen = s.x, s.y, s.xOK, s.yOK, s.pnd, s.pen
}

// detectScroll finds the shift k and the rows [top, bot] such that
// scrolling v up by k (down for a negative k) within them moves the most
// lines of new into place.
func detectScroll(v, new []*line) (k, top, bot int, ok bool) {
	rows := len(v)
	best := 0
	for d := 1; d < rows; d++ {
		for _, s := range [2]int{d, -d} {
			score, first, last := 0, -1, -1
			for i := max(0, -s); i < rows && i+s < rows; i++ {
				n := new[i]
				if n.blank || !sameLine(n, v[i+s]) {
					continue
				}
				if !sameLine(n, v[i]) {
					score++
				}
				if first < 0 {
					first = i
				}
				last = i
			}
			if score > best {
				best, k = score, s
				if s > 0 {
					top, bot = first, min(last+s, rows-1)
				} else {
					top, bot = max(first+s, 0), last
				}
			}
		}
	}
	return k, top, bot, best > 0
}

// sameLine compares two frozen lines by their hashes. A false match only
// costs bytes: painting compares every cell.
func sameLine(a, b *line) bool {
	return a == b || (a.hash == b.hash && a.blank == b.blank)
}

// scroll scrolls the client's rows [top, bot] up by k, or down by -k.
func (e *encoder) scroll(v []*line, top, bot, k int) {
	full := top == 0 && bot == e.rows-1
	if !full {
		e.csi2(top+1, bot+1, "r")
	}
	if k > 0 {
		e.csi(k, "S")
	} else {
		e.csi(-k, "T")
	}
	if !full {
		// DECSTBM moves the cursor home.
		e.str("\x1b[r")
		e.x, e.y, e.xOK, e.yOK, e.pending = 0, 0, true, true, false
	}
	blank := e.blank(e.pen.Bg)
	if k > 0 {
		copy(v[top:bot+1-k], v[top+k:bot+1])
		for y := bot + 1 - k; y <= bot; y++ {
			v[y] = blank
		}
	} else {
		k = -k
		copy(v[top+k:bot+1], v[top:bot+1-k])
		for y := top; y < top+k; y++ {
			v[y] = blank
		}
	}
}

// scrollOut scrolls the whole primary screen up by k with line feeds at
// its bottom row, so that the client adds the lines to its scrollback,
// and draws each new line at the bottom as it comes in.
func (e *encoder) scrollOut(v, new []*line, k int) {
	bottom := e.rows - 1
	x := 0
	if e.xOK {
		x = e.x
	}
	if !e.yOK || e.y != bottom {
		e.moveTo(x, bottom)
	}
	for j := range k {
		e.b = append(e.b, '\n')
		e.pending = false
		copy(v, v[1:])
		v[bottom] = e.blank(e.pen.Bg)
		target := e.rows - k + j
		e.paintRow(bottom, v[bottom], new[target])
		v[bottom] = new[target]
	}
}

// paintRows paints every row of the client that differs from new.
func (e *encoder) paintRows(v, new []*line) {
	e.eraseBelow(v, new)
	for y := range v {
		e.paintRow(y, v[y], new[y])
	}
}

// eraseBelow erases the bottom rows of the client with one ED when new
// has them all erased and two or more of them differ.
func (e *encoder) eraseBelow(v, new []*line) {
	rows := len(v)
	yb := rows
	var bg Color
	for yb > 0 {
		b, ok := erasedLine(new[yb-1])
		if !ok || (yb < rows && b != bg) {
			break
		}
		bg = b
		yb--
	}
	if yb == rows {
		return
	}
	differ := 0
	for y := yb; y < rows; y++ {
		if !v[y].equal(new[y]) {
			differ++
		}
	}
	if differ < 2 {
		return
	}
	e.moveTo(0, yb)
	e.setBg(bg)
	e.str("\x1b[J")
	blank := e.blank(bg)
	for y := yb; y < rows; y++ {
		v[y] = blank
	}
}

// erasedLine reports whether every cell of l is erased with one
// background colour, and which.
func erasedLine(l *line) (Color, bool) {
	if l.blank {
		return DefaultColor, true
	}
	bg := l.cells[0].st.Bg
	for i := range l.cells {
		if !isErased(&l.cells[i], bg) {
			return 0, false
		}
	}
	return bg, true
}

func isErased(c *cell, bg Color) bool {
	return c.r == ' ' && c.w == 1 && c.comb == 0 && c.st == Style{Bg: bg}
}

// maxGap is the longest run of unchanged cells that paintRow writes again
// rather than moving over.
const maxGap = 3

// paintRow paints row y of the client, showing o, to show n.
func (e *encoder) paintRow(y int, o, n *line) {
	if o.equal(n) {
		return
	}
	cols := e.cols
	// From tail on, n is erased with one background, which EL draws.
	tail := cols
	bg := n.cells[cols-1].st.Bg
	for tail > 0 && isErased(&n.cells[tail-1], bg) {
		tail--
	}
	for x := 0; x < cols; {
		if o.cellEqual(x, n, x) {
			x++
			continue
		}
		if x >= tail {
			e.moveTo(x, y)
			e.setBg(bg)
			e.str("\x1b[K")
			return
		}
		// Never start or end inside a wide character, old or new.
		for x > 0 && (n.cells[x].w == 0 || o.cells[x].w == 0) {
			x--
		}
		end := x + 1
		for end < tail {
			if !o.cellEqual(end, n, end) {
				end++
				continue
			}
			gap := end
			for gap < tail && o.cellEqual(gap, n, gap) {
				gap++
			}
			if gap == tail || gap-end > maxGap {
				break
			}
			end = gap
		}
		for end < cols && (n.cells[end].w == 0 || o.cells[end].w == 0) {
			end++
		}
		e.moveTo(x, y)
		e.writeCells(n, x, end, y)
		x = end
	}
}

// writeCells writes the cells [x0, x1) of n into row y of the client,
// starting with the cursor at x0.
func (e *encoder) writeCells(n *line, x0, x1, y int) {
	for x := x0; x < x1; x++ {
		c := &n.cells[x]
		if c.w == 0 {
			continue
		}
		if !e.xOK {
			e.moveTo(x, y)
		}
		e.setPen(c.st)
		e.b = n.appendContent(e.b, x)
		e.x += int(c.w)
		e.pending = false
		switch {
		case e.x >= e.cols:
			// The client is in a pending wrap, where only an absolute
			// move is safe next.
			e.x = e.cols - 1
			e.xOK = false
			e.pending = true
		case risky(c.r, c.comb != 0):
			e.xOK = false
		}
	}
}

// blankScreen returns the lines of a blank screen.
func (e *encoder) blankScreen() []*line {
	ls := make([]*line, e.rows)
	b := e.blank(DefaultColor)
	for i := range ls {
		ls[i] = b
	}
	return ls
}

// blank returns a frozen line of cells erased with background bg.
func (e *encoder) blank(bg Color) *line {
	if l, ok := e.blanks[bg]; ok {
		return l
	}
	if e.blanks == nil {
		e.blanks = make(map[Color]*line)
	}
	l := newLine(e.cols, cell{r: ' ', w: 1, st: Style{Bg: bg}})
	l.freeze()
	e.blanks[bg] = l
	return l
}

// moveTo moves the client's cursor to column x of row y with the shortest
// sequence.
func (e *encoder) moveTo(x, y int) {
	if e.xOK && e.yOK && e.x == x && e.y == y {
		return
	}
	// The vertical move, which keeps the column.
	const (
		vNone = iota
		vRel  // CUU or CUD
		vLF   // line feeds
		vAbs  // VPA
	)
	vKind, vCost := vAbs, 3+digits(y+1)
	if e.yOK {
		switch d := y - e.y; {
		case d == 0:
			vKind, vCost = vNone, 0
		case d > 0 && d <= 3:
			vKind, vCost = vLF, d
		default:
			if c := relCost(abs(d)); c < vCost {
				vKind, vCost = vRel, c
			}
		}
	}
	// The horizontal move.
	const (
		hNone = iota
		hCR
		hRel // CUF or CUB
		hBS
		hAbs // CHA
	)
	hKind, hCost := hAbs, 3
	if x > 0 {
		hCost += digits(x + 1)
	}
	switch {
	case e.xOK && e.x == x:
		hKind, hCost = hNone, 0
	case x == 0:
		hKind, hCost = hCR, 1
	case e.xOK:
		d := x - e.x
		if d < 0 && d >= -3 && -d < hCost {
			hKind, hCost = hBS, -d
		} else if c := relCost(abs(d)); c < hCost {
			hKind, hCost = hRel, c
		}
	}
	cup := 3 + digits(y+1)
	if x > 0 {
		cup += 1 + digits(x+1)
	}
	if cup <= vCost+hCost {
		e.b = append(e.b, 0x1b, '[')
		if y > 0 || x > 0 {
			e.num(y + 1)
		}
		if x > 0 {
			e.b = append(e.b, ';')
			e.num(x + 1)
		}
		e.b = append(e.b, 'H')
	} else {
		switch vKind {
		case vRel:
			if y < e.y {
				e.csi(e.y-y, "A")
			} else {
				e.csi(y-e.y, "B")
			}
		case vLF:
			for range y - e.y {
				e.b = append(e.b, '\n')
			}
		case vAbs:
			e.csi(y+1, "d")
		}
		switch hKind {
		case hCR:
			e.b = append(e.b, '\r')
		case hRel:
			if x < e.x {
				e.csi(e.x-x, "D")
			} else {
				e.csi(x-e.x, "C")
			}
		case hBS:
			for range e.x - x {
				e.b = append(e.b, '\b')
			}
		case hAbs:
			e.csi(x+1, "G")
		}
	}
	e.x, e.y, e.xOK, e.yOK, e.pending = x, y, true, true, false
}

// relCost is the length of CSI n A and its relatives.
func relCost(n int) int {
	if n == 1 {
		return 3
	}
	return 3 + digits(n)
}

func digits(n int) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// setBg changes only the background of the client's SGR, which is all
// that erasing uses.
func (e *encoder) setBg(bg Color) {
	if e.pen.Bg != bg {
		p := e.pen
		p.Bg = bg
		e.setPen(p)
	}
}

// setPen changes the client's SGR to st with the shorter of a change from
// the current SGR and a reset followed by st.
func (e *encoder) setPen(st Style) {
	if e.pen == st {
		return
	}
	start := len(e.b)
	e.b = append(e.b, 0x1b, '[')
	params := len(e.b)
	e.b = appendSGRChange(e.b, e.pen, st)
	change := len(e.b) - params
	e.b = appendSGRReset(e.b, st)
	reset := len(e.b) - params - change
	if reset < change {
		copy(e.b[params:], e.b[params+change:])
		e.b = e.b[:params+reset]
	} else {
		e.b = e.b[:params+change]
	}
	if len(e.b) == params+1 && e.b[params] == '0' {
		e.b = e.b[:params]
	}
	if len(e.b) > start {
		e.b = append(e.b, 'm')
	}
	e.pen = st
}

// sgrAttrs lists the attributes with the SGR parameters that set and
// reset them. Bold and faint share their reset, 22.
var sgrAttrs = [...]struct {
	a       Attr
	on, off int
}{
	{Bold, 1, 22}, {Faint, 2, 22}, {Italic, 3, 23}, {Underline, 4, 24},
	{Blink, 5, 25}, {Inverse, 7, 27}, {Invisible, 8, 28},
	{Strikethrough, 9, 29}, {Overline, 53, 55},
}

// appendSGRChange appends the SGR parameters that change from to to.
func appendSGRChange(b []byte, from, to Style) []byte {
	off := from.Attrs &^ to.Attrs
	on := to.Attrs &^ from.Attrs
	if off&(Bold|Faint) != 0 {
		b = appendParam(b, 22)
		on |= to.Attrs & (Bold | Faint)
		off &^= Bold | Faint
	}
	for _, s := range sgrAttrs {
		if off&s.a != 0 {
			b = appendParam(b, s.off)
		}
	}
	for _, s := range sgrAttrs {
		if on&s.a != 0 {
			b = appendParam(b, s.on)
		}
	}
	if from.Fg != to.Fg {
		b = appendColor(b, to.Fg, 30)
	}
	if from.Bg != to.Bg {
		b = appendColor(b, to.Bg, 40)
	}
	return b
}

// appendSGRReset appends SGR parameters that reset and then set st.
func appendSGRReset(b []byte, st Style) []byte {
	b = append(b, '0')
	for _, s := range sgrAttrs {
		if st.Attrs&s.a != 0 {
			b = appendParam(b, s.on)
		}
	}
	if !st.Fg.IsDefault() {
		b = appendColor(b, st.Fg, 30)
	}
	if !st.Bg.IsDefault() {
		b = appendColor(b, st.Bg, 40)
	}
	return b
}

// appendColor appends the SGR parameters of c as a foreground (base 30)
// or background (base 40) colour.
func appendColor(b []byte, c Color, base int) []byte {
	if i, ok := c.Basic(); ok {
		if i < 8 {
			return appendParam(b, base+int(i))
		}
		return appendParam(b, base+60+int(i)-8)
	}
	if i, ok := c.Indexed(); ok {
		b = appendParam(b, base+8)
		b = append(b, ";5;"...)
		return strconv.AppendInt(b, int64(i), 10)
	}
	if r, g, bl, ok := c.RGB(); ok {
		b = appendParam(b, base+8)
		b = append(b, ";2;"...)
		b = strconv.AppendInt(b, int64(r), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(g), 10)
		b = append(b, ';')
		return strconv.AppendInt(b, int64(bl), 10)
	}
	return appendParam(b, base+9)
}

// appendParam appends one SGR parameter, after a separator unless it is
// the first.
func appendParam(b []byte, n int) []byte {
	if len(b) > 0 && b[len(b)-1] != '[' {
		b = append(b, ';')
	}
	return strconv.AppendInt(b, int64(n), 10)
}

func (e *encoder) str(s string) { e.b = append(e.b, s...) }

func (e *encoder) num(n int) { e.b = strconv.AppendInt(e.b, int64(n), 10) }

// csi appends CSI n final, leaving out n when it is 1 or less.
func (e *encoder) csi(n int, final string) {
	e.b = append(e.b, 0x1b, '[')
	if n > 1 {
		e.num(n)
	}
	e.str(final)
}

func (e *encoder) csi2(a, b int, final string) {
	e.b = append(e.b, 0x1b, '[')
	e.num(a)
	e.b = append(e.b, ';')
	e.num(b)
	e.str(final)
}
