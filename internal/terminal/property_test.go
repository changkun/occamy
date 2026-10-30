// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// replay feeds the diffs between successive snapshots into a fresh
// reference terminal and checks after each one that it shows the
// snapshot. It returns the bytes the diffs took.
func replay(t testing.TB, snaps []*Screen) int {
	t.Helper()
	if len(snaps) == 0 {
		return 0
	}
	ref := New(snaps[0].Cols, snaps[0].Rows, Options{})
	var prev *Screen
	total := 0
	for i, s := range snaps {
		if prev != nil && (prev.Cols != s.Cols || prev.Rows != s.Rows) {
			ref.Resize(s.Cols, s.Rows)
		}
		d := Diff(prev, s)
		total += len(d)
		ref.Write(d)
		if got := ref.Snapshot(); !got.Equal(s) {
			t.Fatalf("snapshot %d: the reference differs after the diff %q:\n%s", i, d, describe(got, s))
		}
		prev = s
	}
	return total
}

// describe reports how got differs from want.
func describe(got, want *Screen) string {
	var b strings.Builder
	field := func(name string, g, w any) {
		if fmt.Sprint(g) != fmt.Sprint(w) {
			fmt.Fprintf(&b, "%s: got %v, want %v\n", name, g, w)
		}
	}
	field("size", [2]int{got.Cols, got.Rows}, [2]int{want.Cols, want.Rows})
	field("cursor", got.Cursor, want.Cursor)
	field("modes", got.Modes, want.Modes)
	field("mouse", [2]any{got.Mouse, got.MouseEnc}, [2]any{want.Mouse, want.MouseEnc})
	field("title", got.Title, want.Title)
	field("alt screen", got.AltScreen, want.AltScreen)
	if got.Cols != want.Cols || got.Rows != want.Rows {
		return b.String()
	}
	cmp := func(name string, g, w []*line) {
		for y := range w {
			for x := range w[y].cells {
				if !g[y].cellEqual(x, w[y], x) {
					fmt.Fprintf(&b, "%s cell (%d,%d): got %q %+v, want %q %+v\n", name, x, y,
						g[y].content(x), g[y].cells[x], w[y].content(x), w[y].cells[x])
					fmt.Fprintf(&b, "  got row:  %q\n  want row: %q\n", g[y].text(), w[y].text())
					return
				}
			}
		}
	}
	cmp("primary", got.main, want.main)
	if got.AltScreen && want.AltScreen {
		cmp("alternate", got.alt, want.alt)
	}
	return b.String()
}

// streamGen generates random terminal output.
type streamGen struct {
	r          *rand.Rand
	cols, rows int
}

var genText = []string{
	"hello", "world", "a", " ", "  ", "0123456789", "the quick brown fox",
	"漢字", "한국어", "ｱｲｳ", "é", "e\u0301", "a\u0308\u0323", "😀", "👍🏽", "👨\u200d👩\u200d👧",
	"🇩🇪", "─│┌┐└┘", "\u2764\ufe0f", "x\u200by", "\t", "\uFFFD",
}

// chunk returns one random piece of output.
func (g *streamGen) chunk() string {
	r := g.r
	n := func(k int) int { return r.IntN(k) }
	switch n(30) {
	case 0, 1, 2, 3, 4:
		return genText[n(len(genText))]
	case 5:
		return strings.Repeat(string(rune('A'+n(26))), n(2*g.cols))
	case 6, 7:
		return "\r\n"
	case 8:
		return []string{"\n", "\r", "\b", "\x1bD", "\x1bE", "\x1bM", "\v"}[n(7)]
	case 9:
		return fmt.Sprintf("\x1b[%d;%dH", n(g.rows+2), n(g.cols+2))
	case 10:
		return fmt.Sprintf("\x1b[%d%c", n(g.cols), "ABCDEFGd`aeIZ"[n(13)])
	case 11, 12:
		return g.sgr()
	case 13:
		return fmt.Sprintf("\x1b[%dJ", n(4))
	case 14:
		return fmt.Sprintf("\x1b[%dK", n(3))
	case 15:
		return fmt.Sprintf("\x1b[%d%c", n(5), "XLMP@ST"[n(7)])
	case 16:
		top := n(g.rows)
		return fmt.Sprintf("\x1b[%d;%dr", top+1, top+1+n(g.rows-top+1))
	case 17:
		return "\x1b[r"
	case 18:
		return []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?47h", "\x1b[?47l", "\x1b[?1047h", "\x1b[?1047l"}[n(6)]
	case 19:
		modes := []int{1, 7, 25, 1004, 2004, 9, 1000, 1002, 1003, 1005, 1006, 1015, 1016, 12, 66, 6, 1048}
		hl := "hl"[n(2)]
		return fmt.Sprintf("\x1b[?%d%c", modes[n(len(modes))], hl)
	case 20:
		return []string{"\x1b[4h", "\x1b[4l", "\x1b7", "\x1b8", "\x1b[s", "\x1b[u", "\x1b=", "\x1b>"}[n(8)]
	case 21:
		return fmt.Sprintf("\x1b]%d;title %d\x07", []int{0, 2, 1}[n(3)], n(100))
	case 22:
		return fmt.Sprintf("\x1b[%d q", n(7))
	case 23:
		return []string{"\x1b(0", "\x1b(B", "\x0e", "\x0f", "\x1b)0", "lqqk", "\x1bH", "\x1b[3g", "\x1b[g"}[n(9)]
	case 24:
		return fmt.Sprintf("\x1b[%db", n(10))
	case 25:
		// Fill the rest of the line, to reach the last column.
		return strings.Repeat("=", n(g.cols+1))
	case 26:
		return []string{"\x1b[22t", "\x1b[23t", "\x1b]2;\x1b\\", "\x1b[!p", "\x1b#8", "\x1bc"}[n(6)]
	case 27:
		return "\x1b[?7l"
	case 28:
		return "\x1b[?7h"
	}
	// Bulk output, as from seq.
	var b strings.Builder
	for range n(3 * g.rows) {
		fmt.Fprintf(&b, "%d\r\n", n(100000))
	}
	return b.String()
}

func (g *streamGen) sgr() string {
	r := g.r
	parts := []string{}
	for range 1 + r.IntN(3) {
		switch r.IntN(8) {
		case 0:
			parts = append(parts, "0")
		case 1:
			parts = append(parts, fmt.Sprint([]int{1, 2, 3, 4, 5, 7, 8, 9, 21, 22, 23, 24, 25, 27, 28, 29, 53, 55}[r.IntN(18)]))
		case 2:
			parts = append(parts, fmt.Sprint(30+r.IntN(10)), fmt.Sprint(40+r.IntN(10)))
		case 3:
			parts = append(parts, fmt.Sprint(90+r.IntN(8)), fmt.Sprint(100+r.IntN(8)))
		case 4:
			parts = append(parts, fmt.Sprintf("38;5;%d", r.IntN(256)))
		case 5:
			parts = append(parts, fmt.Sprintf("48;2;%d;%d;%d", r.IntN(256), r.IntN(256), r.IntN(256)))
		case 6:
			parts = append(parts, fmt.Sprintf("38:2::%d:%d:%d", r.IntN(256), r.IntN(256), r.IntN(256)))
		case 7:
			parts = append(parts, "4:3", "58;5;1")
		}
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

// run feeds steps chunks and takes a snapshot after each with probability
// p, resizing now and then when resize is set.
func (g *streamGen) run(term *Terminal, steps int, p float64, resize bool) []*Screen {
	var snaps []*Screen
	for range steps {
		if resize && g.r.IntN(60) == 0 {
			g.cols, g.rows = 1+g.r.IntN(100), 1+g.r.IntN(40)
			term.Resize(g.cols, g.rows)
		}
		term.Write([]byte(g.chunk()))
		if g.r.Float64() < p {
			snaps = append(snaps, term.Snapshot())
		}
	}
	return append(snaps, term.Snapshot())
}

func TestDiffReproducesSnapshots(t *testing.T) {
	sizes := [][2]int{{80, 24}, {20, 5}, {1, 1}, {2, 3}, {132, 50}, {7, 30}}
	for seed := range 300 {
		size := sizes[seed%len(sizes)]
		g := &streamGen{r: rand.New(rand.NewPCG(uint64(seed), 1)), cols: size[0], rows: size[1]}
		term := New(g.cols, g.rows, Options{})
		snaps := g.run(term, 400, 0.3, seed%3 == 0)
		t.Run(fmt.Sprint(seed), func(t *testing.T) { replay(t, snaps) })
		if t.Failed() {
			return
		}
	}
}

func TestDiffReproducesConformanceCases(t *testing.T) {
	for _, c := range conformance {
		t.Run(c.name, func(t *testing.T) {
			term := New(c.cols, c.rows, Options{})
			var snaps []*Screen
			// Snapshot after every byte, then after every chunk of the
			// case, so that diffs start and end everywhere.
			for i := range len(c.in) {
				term.Write([]byte{c.in[i]})
				snaps = append(snaps, term.Snapshot())
			}
			replay(t, snaps)
		})
	}
}

// FuzzDiff checks the property on arbitrary output. In the input, 0xff
// takes a snapshot, and 0xfe followed by two bytes resizes.
func FuzzDiff(f *testing.F) {
	for _, c := range conformance {
		f.Add([]byte(c.in + "\xff"))
	}
	f.Add([]byte("hello\xff\r\nworld\xff\x1b[?1049hvim\xff\x1b[?1049l\xff"))
	f.Add([]byte("1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n\xff7\r\n8\r\n\xff\xfe\x0a\x03abc\xff"))
	f.Fuzz(fuzzDiff)
}

// fuzzDiff is the body of FuzzDiff.
func fuzzDiff(t *testing.T, data []byte) {
	term := New(20, 6, Options{})
	var snaps []*Screen
	start := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case 0xff:
			term.Write(data[start:i])
			snaps = append(snaps, term.Snapshot())
			start = i + 1
		case 0xfe:
			term.Write(data[start:i])
			if i+2 < len(data) {
				term.Resize(int(data[i+1]%90)+1, int(data[i+2]%40)+1)
			}
			i += 2
			start = i + 1
		}
	}
	if start < len(data) {
		term.Write(data[start:])
	}
	replay(t, append(snaps, term.Snapshot()))
}
