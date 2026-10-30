// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

// The conformance table. It depends on nothing in this package but
// confTerm, so that it can be run against other emulators: that is how
// the emulator was chosen (see the native SSH backend spec). The expected
// screens follow xterm, and xterm.js where they differ; the text and
// cursor of every case were checked against tmux.

// confTerm is what the table needs of an emulator.
type confTerm interface {
	Write(s string)
	Line(y int) string // trailing blanks removed
	Cursor() (x, y int)
	Cell(x, y int) confCell
	// Get reports a mode or other state by name: "DECCKM", "DECKPAM",
	// "DECAWM", "bracketed", "focus", "mouse" (none, x10, normal,
	// button, any), "mouseenc" (default, utf8, sgr, urxvt), "alt",
	// "cursorvisible", "cursorshape", "title", "reply". ok is false when
	// the emulator does not expose it.
	Get(name string) (v string, ok bool)
}

// confCell is a cell as the table sees it. Colours are "" for the
// default, the palette index for basic and indexed colours, and #rrggbb.
type confCell struct {
	Content string
	Width   int
	Fg, Bg  string
	Attrs   string // letters: b bold, f faint, i italic, u underline, k blink, r inverse, h invisible, s strikethrough
}

type confCase struct {
	cat        string
	name       string
	cols, rows int
	in         string
	// lines are the expected rows; rows past the end must be blank. nil
	// skips the check.
	lines []string
	// cursor is the expected position, unless nil.
	cursor *[2]int
	cells  []confCellCheck
	state  map[string]string
}

type confCellCheck struct {
	x, y int
	want confCell // Content and Width are checked when set
}

func at(x, y int) *[2]int { return &[2]int{x, y} }

const fill4 = "\x1b[1;1Ha\x1b[2;1Hb\x1b[3;1Hc\x1b[4;1Hd"

var conformance = []confCase{
	// Cursor movement.
	{cat: "cursor", name: "CUP", cols: 10, rows: 5, in: "\x1b[3;4HX", lines: []string{"", "", "   X"}, cursor: at(4, 2)},
	{cat: "cursor", name: "CUP without parameters", cols: 10, rows: 5, in: "abc\x1b[HX", lines: []string{"Xbc"}, cursor: at(1, 0)},
	{cat: "cursor", name: "CUP clamps", cols: 10, rows: 5, in: "\x1b[99;99HX", lines: []string{"", "", "", "", "         X"}, cursor: at(9, 4)},
	{cat: "cursor", name: "CUU CUD CUF CUB", cols: 10, rows: 5, in: "\x1b[3;3H\x1b[AU\x1b[2BD\x1b[3CF\x1b[5DB", lines: []string{"", "  U", "", "   B   F"}, cursor: at(4, 3)},
	{cat: "cursor", name: "CUB stops at column 0", cols: 10, rows: 5, in: "ab\x1b[9DX", lines: []string{"Xb"}, cursor: at(1, 0)},
	{cat: "cursor", name: "CHA VPA", cols: 10, rows: 5, in: "\x1b[5GX\x1b[3dY", lines: []string{"    X", "", "     Y"}, cursor: at(6, 2)},
	{cat: "cursor", name: "CNL CPL", cols: 10, rows: 5, in: "ab\x1b[2Ec\x1b[Fd", lines: []string{"ab", "d", "c"}, cursor: at(1, 1)},
	{cat: "cursor", name: "CR LF BS", cols: 10, rows: 5, in: "abc\rX\nY\bZ", lines: []string{"Xbc", " Z"}, cursor: at(2, 1)},
	{cat: "cursor", name: "BS at column 0", cols: 10, rows: 5, in: "\bX", lines: []string{"X"}, cursor: at(1, 0)},

	// Erasing.
	{cat: "erase", name: "ED 0", cols: 10, rows: 3, in: "\x1b[1;1Haaaaa\x1b[2;1Hbbbbb\x1b[3;1Hccccc\x1b[2;3H\x1b[J", lines: []string{"aaaaa", "bb", ""}, cursor: at(2, 1)},
	{cat: "erase", name: "ED 1", cols: 10, rows: 3, in: "\x1b[1;1Haaaaa\x1b[2;1Hbbbbb\x1b[3;1Hccccc\x1b[2;3H\x1b[1J", lines: []string{"", "   bb", "ccccc"}, cursor: at(2, 1)},
	{cat: "erase", name: "ED 2", cols: 10, rows: 3, in: "\x1b[1;1Haaaaa\x1b[2;1Hbbbbb\x1b[3;1Hccccc\x1b[2J", lines: []string{"", "", ""}, cursor: at(5, 2)},
	{cat: "erase", name: "EL 0", cols: 10, rows: 3, in: "abcdef\x1b[1;3H\x1b[K", lines: []string{"ab"}, cursor: at(2, 0)},
	{cat: "erase", name: "EL 1", cols: 10, rows: 3, in: "abcdef\x1b[1;3H\x1b[1K", lines: []string{"   def"}, cursor: at(2, 0)},
	{cat: "erase", name: "EL 2", cols: 10, rows: 3, in: "abcdef\x1b[1;3H\x1b[2K", lines: []string{""}, cursor: at(2, 0)},
	{cat: "erase", name: "ECH", cols: 10, rows: 3, in: "abcdef\x1b[1;2H\x1b[3X", lines: []string{"a   ef"}, cursor: at(1, 0)},
	{cat: "erase", name: "erase fills with the background colour", cols: 10, rows: 3, in: "\x1b[44m\x1b[2K\x1b[m", cells: []confCellCheck{{0, 0, confCell{Bg: "4"}}, {9, 0, confCell{Bg: "4"}}}},

	// Scroll regions.
	{cat: "scroll", name: "LF at the bottom scrolls", cols: 10, rows: 4, in: "1\r\n2\r\n3\r\n4\r\n5", lines: []string{"2", "3", "4", "5"}, cursor: at(1, 3)},
	{cat: "scroll", name: "DECSTBM LF at the bottom margin", cols: 10, rows: 4, in: fill4 + "\x1b[2;3r\x1b[3;1H\nX", lines: []string{"a", "c", "X", "d"}, cursor: at(1, 2)},
	{cat: "scroll", name: "DECSTBM RI at the top margin", cols: 10, rows: 4, in: fill4 + "\x1b[2;3r\x1b[2;1H\x1bMX", lines: []string{"a", "X", "b", "d"}, cursor: at(1, 1)},
	{cat: "scroll", name: "DECSTBM SU", cols: 10, rows: 4, in: fill4 + "\x1b[2;4r\x1b[S", lines: []string{"a", "c", "d", ""}, cursor: at(0, 0)},
	{cat: "scroll", name: "DECSTBM SD", cols: 10, rows: 4, in: fill4 + "\x1b[2;4r\x1b[2T", lines: []string{"a", "", "", "b"}, cursor: at(0, 0)},
	{cat: "scroll", name: "DECSTBM homes the cursor", cols: 10, rows: 4, in: "abc\x1b[2;3r", lines: []string{"abc"}, cursor: at(0, 0)},
	{cat: "scroll", name: "LF below the region does not scroll", cols: 10, rows: 4, in: "\x1b[1;2r\x1b[4;1Hx\n\ny", lines: []string{"", "", "", "xy"}, cursor: at(2, 3)},

	// Inserting and deleting lines and characters.
	{cat: "insdel", name: "IL", cols: 10, rows: 4, in: fill4 + "\x1b[2;1H\x1b[L", lines: []string{"a", "", "b", "c"}, cursor: at(0, 1)},
	{cat: "insdel", name: "DL", cols: 10, rows: 4, in: fill4 + "\x1b[2;1H\x1b[2M", lines: []string{"a", "d", "", ""}, cursor: at(0, 1)},
	{cat: "insdel", name: "IL within the region", cols: 10, rows: 4, in: fill4 + "\x1b[1;3r\x1b[2;1H\x1b[L", lines: []string{"a", "", "b", "d"}, cursor: at(0, 1)},
	{cat: "insdel", name: "IL outside the region", cols: 10, rows: 4, in: fill4 + "\x1b[1;2r\x1b[4;1H\x1b[L", lines: []string{"a", "b", "c", "d"}, cursor: at(0, 3)},
	{cat: "insdel", name: "ICH", cols: 10, rows: 4, in: "abcdef\x1b[1;3H\x1b[2@", lines: []string{"ab  cdef"}, cursor: at(2, 0)},
	{cat: "insdel", name: "DCH", cols: 10, rows: 4, in: "abcdef\x1b[1;2H\x1b[2P", lines: []string{"adef"}, cursor: at(1, 0)},
	{cat: "insdel", name: "ICH pushes off the edge", cols: 10, rows: 4, in: "0123456789\x1b[1;1H\x1b[3@", lines: []string{"   0123456"}, cursor: at(0, 0)},

	// Insert mode.
	{cat: "irm", name: "IRM inserts", cols: 10, rows: 2, in: "abcdef\x1b[1;3H\x1b[4hXY\x1b[4lZ", lines: []string{"abXYZdef"}, cursor: at(5, 0)},
	{cat: "irm", name: "IRM as nano uses it", cols: 20, rows: 2, in: "The quick brown\x1b[1;5H\x1b[4hN\x1b[4l\x1b[1;6H\x1b[4hE\x1b[4l", lines: []string{"The NEquick brown"}, cursor: at(6, 0)},

	// Origin mode.
	{cat: "origin", name: "DECOM makes CUP relative", cols: 10, rows: 5, in: "\x1b[2;4r\x1b[?6h\x1b[1;1HX", lines: []string{"", "X"}, cursor: at(1, 1)},
	{cat: "origin", name: "DECOM keeps the cursor in the region", cols: 10, rows: 5, in: "\x1b[2;4r\x1b[?6h\x1b[9;1HX", lines: []string{"", "", "", "X"}, cursor: at(1, 3)},
	{cat: "origin", name: "DECOM reset homes the cursor", cols: 10, rows: 5, in: "\x1b[2;4r\x1b[?6h\x1b[?6lX", lines: []string{"X"}, cursor: at(1, 0)},

	// Wrapping.
	{cat: "wrap", name: "auto-wrap", cols: 10, rows: 3, in: "0123456789AB", lines: []string{"0123456789", "AB"}, cursor: at(2, 1)},
	{cat: "wrap", name: "pending wrap stays in the last column", cols: 10, rows: 3, in: "0123456789", lines: []string{"0123456789"}, cursor: at(9, 0)},
	{cat: "wrap", name: "CR cancels the pending wrap", cols: 10, rows: 3, in: "0123456789\rX", lines: []string{"X123456789"}, cursor: at(1, 0)},
	{cat: "wrap", name: "LF cancels the pending wrap", cols: 10, rows: 3, in: "0123456789\nX", lines: []string{"0123456789", "         X"}, cursor: at(9, 1)},
	{cat: "wrap", name: "DECAWM off overwrites the last column", cols: 10, rows: 3, in: "\x1b[?7l0123456789AB", lines: []string{"012345678B"}, cursor: at(9, 0)},
	{cat: "wrap", name: "a wide character wraps from the last column", cols: 10, rows: 3, in: "012345678漢", lines: []string{"012345678", "漢"}, cursor: at(2, 1)},

	// Tabs.
	{cat: "tabs", name: "HT", cols: 20, rows: 2, in: "a\tb", lines: []string{"a       b"}, cursor: at(9, 0)},
	{cat: "tabs", name: "HTS TBC", cols: 20, rows: 2, in: "\x1b[3g\x1b[1;5H\x1bH\x1b[1;1H\tX", lines: []string{"    X"}, cursor: at(5, 0)},
	{cat: "tabs", name: "HT stops at the last column", cols: 20, rows: 2, in: "\t\t\t\tX", lines: []string{"                   X"}, cursor: at(19, 0)},
	{cat: "tabs", name: "CHT CBT", cols: 20, rows: 2, in: "\x1b[2IX\x1b[ZY", lines: []string{"                Y"}, cursor: at(17, 0)},

	// SGR.
	{cat: "sgr", name: "bold italic underline inverse", cols: 10, rows: 2, in: "\x1b[1;3;4;7mA\x1b[mB", cells: []confCellCheck{{0, 0, confCell{Content: "A", Attrs: "biur"}}, {1, 0, confCell{Content: "B"}}}},
	{cat: "sgr", name: "faint blink invisible strikethrough", cols: 10, rows: 2, in: "\x1b[2;5;8;9mA", cells: []confCellCheck{{0, 0, confCell{Attrs: "fkhs"}}}},
	{cat: "sgr", name: "basic colours", cols: 10, rows: 2, in: "\x1b[31;42mA", cells: []confCellCheck{{0, 0, confCell{Fg: "1", Bg: "2"}}}},
	{cat: "sgr", name: "bright colours", cols: 10, rows: 2, in: "\x1b[91;102mA", cells: []confCellCheck{{0, 0, confCell{Fg: "9", Bg: "10"}}}},
	{cat: "sgr", name: "256 colours", cols: 10, rows: 2, in: "\x1b[38;5;196;48;5;21mA", cells: []confCellCheck{{0, 0, confCell{Fg: "196", Bg: "21"}}}},
	{cat: "sgr", name: "truecolor", cols: 10, rows: 2, in: "\x1b[38;2;1;2;3;48;2;4;5;6mA", cells: []confCellCheck{{0, 0, confCell{Fg: "#010203", Bg: "#040506"}}}},
	{cat: "sgr", name: "truecolor with colons", cols: 10, rows: 2, in: "\x1b[38:2::10:20:30mA", cells: []confCellCheck{{0, 0, confCell{Fg: "#0a141e"}}}},
	{cat: "sgr", name: "SGR 22 resets bold and faint", cols: 10, rows: 2, in: "\x1b[1;2m\x1b[22mA", cells: []confCellCheck{{0, 0, confCell{Content: "A"}}}},
	{cat: "sgr", name: "SGR 39 and 49", cols: 10, rows: 2, in: "\x1b[31;41m\x1b[39;49mA", cells: []confCellCheck{{0, 0, confCell{Content: "A"}}}},

	// Wide characters and combining marks.
	{cat: "wide", name: "CJK takes two cells", cols: 10, rows: 2, in: "漢字x", lines: []string{"漢字x"}, cursor: at(5, 0), cells: []confCellCheck{{0, 0, confCell{Content: "漢", Width: 2}}, {2, 0, confCell{Content: "字", Width: 2}}, {4, 0, confCell{Content: "x", Width: 1}}}},
	{cat: "wide", name: "writing the right half blanks the left", cols: 10, rows: 2, in: "漢\x1b[1;2HX", lines: []string{" X"}, cursor: at(2, 0)},
	{cat: "wide", name: "writing the left half blanks the right", cols: 10, rows: 2, in: "漢\x1b[1;1HX", lines: []string{"X"}, cursor: at(1, 0)},
	{cat: "wide", name: "a combining mark joins its cell", cols: 10, rows: 2, in: "éx", lines: []string{"éx"}, cursor: at(2, 0), cells: []confCellCheck{{0, 0, confCell{Content: "é", Width: 1}}}},
	{cat: "wide", name: "an emoji takes two cells", cols: 10, rows: 2, in: "😀x", lines: []string{"😀x"}, cursor: at(3, 0), cells: []confCellCheck{{0, 0, confCell{Content: "😀", Width: 2}}}},

	// The alternate screen.
	{cat: "alt", name: "1049 restores the primary screen and cursor", cols: 10, rows: 3, in: "main\x1b[?1049halt\x1b[?1049l", lines: []string{"main"}, cursor: at(4, 0), state: map[string]string{"alt": "false"}},
	{cat: "alt", name: "1049 clears the alternate screen", cols: 10, rows: 3, in: "abc\x1b[?1049h", lines: []string{""}, cursor: at(3, 0), state: map[string]string{"alt": "true"}},
	{cat: "alt", name: "1049 keeps the alternate screen apart", cols: 10, rows: 3, in: "abc\x1b[?1049h\x1b[Hxyz", lines: []string{"xyz"}, cursor: at(3, 0), state: map[string]string{"alt": "true"}},

	// Saving the cursor.
	{cat: "decsc", name: "DECSC DECRC", cols: 10, rows: 3, in: "\x1b[2;3H\x1b[1;31m\x1b7\x1b[m\x1b[1;1HA\x1b8B", lines: []string{"A", "  B"}, cursor: at(3, 1), cells: []confCellCheck{{2, 1, confCell{Content: "B", Fg: "1", Attrs: "b"}}, {0, 0, confCell{Content: "A"}}}},
	{cat: "decsc", name: "DECRC without DECSC homes the cursor", cols: 10, rows: 3, in: "abc\x1b8X", lines: []string{"Xbc"}, cursor: at(1, 0)},

	// Modes a client mirrors.
	{cat: "modes", name: "bracketed paste", cols: 10, rows: 2, in: "\x1b[?2004h", state: map[string]string{"bracketed": "true"}},
	{cat: "modes", name: "mouse 1000 with SGR encoding", cols: 10, rows: 2, in: "\x1b[?1000h\x1b[?1006h", state: map[string]string{"mouse": "normal", "mouseenc": "sgr"}},
	{cat: "modes", name: "mouse 1002 replaces 1000", cols: 10, rows: 2, in: "\x1b[?1000h\x1b[?1002h", state: map[string]string{"mouse": "button"}},
	{cat: "modes", name: "mouse 1003", cols: 10, rows: 2, in: "\x1b[?1003h", state: map[string]string{"mouse": "any"}},
	{cat: "modes", name: "resetting any mouse mode stops reports", cols: 10, rows: 2, in: "\x1b[?1003h\x1b[?1000l", state: map[string]string{"mouse": "none"}},
	{cat: "modes", name: "focus reports", cols: 10, rows: 2, in: "\x1b[?1004h", state: map[string]string{"focus": "true"}},
	{cat: "modes", name: "DECCKM", cols: 10, rows: 2, in: "\x1b[?1h", state: map[string]string{"DECCKM": "true"}},
	{cat: "modes", name: "DECKPAM", cols: 10, rows: 2, in: "\x1b=", state: map[string]string{"DECKPAM": "true"}},
	{cat: "modes", name: "DECAWM reset", cols: 10, rows: 2, in: "\x1b[?7l", state: map[string]string{"DECAWM": "false"}},
	{cat: "modes", name: "DECTCEM hides the cursor", cols: 10, rows: 2, in: "\x1b[?25l", state: map[string]string{"cursorvisible": "false"}},
	{cat: "modes", name: "DECSCUSR", cols: 10, rows: 2, in: "\x1b[4 q", state: map[string]string{"cursorshape": "4"}},

	// Titles.
	{cat: "title", name: "OSC 0", cols: 10, rows: 2, in: "\x1b]0;hello\x07", state: map[string]string{"title": "hello"}},
	{cat: "title", name: "OSC 2 ended by ST", cols: 10, rows: 2, in: "\x1b]2;world\x1b\\", state: map[string]string{"title": "world"}},
	{cat: "title", name: "OSC 2 in UTF-8", cols: 10, rows: 2, in: "\x1b]2;漢字\x07", state: map[string]string{"title": "漢字"}},

	// Character sets, repetition and replies.
	{cat: "misc", name: "DEC special graphics", cols: 10, rows: 2, in: "\x1b(0lqk\x1b(Bx", lines: []string{"┌─┐x"}, cursor: at(4, 0)},
	{cat: "misc", name: "SO and SI", cols: 10, rows: 2, in: "\x1b)0\x0eq\x0fq", lines: []string{"─q"}, cursor: at(2, 0)},
	{cat: "misc", name: "REP", cols: 10, rows: 2, in: "a\x1b[3b", lines: []string{"aaaa"}, cursor: at(4, 0)},
	{cat: "misc", name: "CPR", cols: 10, rows: 5, in: "\x1b[3;5H\x1b[6n", state: map[string]string{"reply": "\x1b[3;5R"}},
}
