// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"unicode"

	"golang.org/x/text/width"
)

// runeWidth returns the number of cells r takes: 0 for a code point that
// joins the previous cell (combining marks, joiners, variation
// selectors), 2 for East Asian wide and fullwidth characters, which since
// Unicode 9 include emoji presented as emoji, and 1 otherwise. It returns
// -1 for C1 control characters, which are not drawn.
//
// This is the wcwidth model that xterm, tmux and mosh use, and that
// xterm.js uses with its Unicode 11 addon: each code point of nonzero
// width starts a cell, so an emoji sequence joined by U+200D takes the
// cells of its parts. Clients that cluster graphemes draw such a sequence
// narrower; Diff positions the cursor absolutely after one, so the cells
// after it stay in place.
func runeWidth(r rune) int {
	switch {
	case r < 0x7f:
		return 1
	case r < 0xa0:
		return -1
	case r < 0x300:
		return 1
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff:
		return 0
	case r >= 0x1160 && r <= 0x11ff:
		// Hangul medial vowels and final consonants join the syllable.
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// risky reports whether clients may disagree on the width of a cell whose
// first code point is r: emoji and regional indicators, the symbol blocks
// whose characters have both a text and an emoji presentation, and any
// cell with joined code points. Box drawing and block elements are not
// risky.
func risky(r rune, hasComb bool) bool {
	return hasComb || r >= 0x1f000 ||
		(r >= 0x2300 && r < 0x2400) || (r >= 0x2600 && r < 0x2800) || (r >= 0x2b00 && r < 0x2c00)
}
