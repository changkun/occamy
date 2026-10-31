// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// FuzzWrite feeds arbitrary bytes, which anyone with a shell can print,
// through Write in pieces of arbitrary size, at varied sizes and with
// resizes in between, and checks after each step that the terminal holds
// together: it does not panic, the cursor and the scroll region are on
// the screen, a snapshot's shape, versions and cells agree, snapshots
// never change once taken, and the diffs between them rebuild them.
//
// The script drives it two bytes at a time: an operation and its
// argument. Operation 0 writes the next argument+1 bytes of data, 1
// resizes to the argument's columns and the next byte's rows, 2 takes a
// snapshot, and 3 writes the next argument+1 bytes one at a time. What
// data is left is written at the end.
func FuzzWrite(f *testing.F) {
	for _, c := range conformance {
		f.Add(uint16(c.cols), uint16(c.rows), []byte(c.in), []byte{2, 0})
	}
	traces, _ := filepath.Glob("testdata/*.raw")
	for _, name := range traces {
		data, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(uint16(80), uint16(24), data, []byte{0, 200, 2, 0, 1, 40, 10, 0, 255, 2, 0, 1, 132, 50, 3, 30, 2, 0})
	}
	f.Add(uint16(1), uint16(1), []byte("世界́\x1b[?1049h\x1b[5;10r\x1b[10;1H\n\n\x1b[2@\x1b[P\x1b[X"), []byte{3, 9, 2, 0, 1, 2, 2, 0, 255})
	f.Add(uint16(0xffff), uint16(0xffff), []byte("\x1b[999;999Hx\x1b#8"), []byte{2, 0, 1, 255, 255, 2, 0})
	f.Fuzz(fuzzWrite)
}

// fuzzSize is a size from the fuzzer's number: mostly small, so that
// most inputs run fast, and from 0xfff0 on, beyond the largest, which
// New clamps.
func fuzzSize(v uint16) int {
	if v >= 0xfff0 {
		return int(v)
	}
	return int(v % 160)
}

// byteSize is a size from one byte of the script: 255 is beyond the
// largest, which Resize clamps, and 254 the largest.
func byteSize(b byte, largest int) int {
	switch b {
	case 255:
		return largest + 1
	case 254:
		return largest
	}
	return int(b % 100)
}

// taken is a snapshot and what it showed when it was taken.
type taken struct {
	s    *Screen
	text string
	cur  Cursor
}

func fuzzWrite(t *testing.T, cols, rows uint16, data, script []byte) {
	term := New(fuzzSize(cols), fuzzSize(rows), Options{Reply: func([]byte) {}})
	var snaps []taken
	var last uint64
	snapshot := func() {
		s := term.Snapshot()
		if s.Version < last {
			t.Fatalf("version %d after %d", s.Version, last)
		}
		last = s.Version
		if again := term.Snapshot(); again != s {
			t.Fatal("a second snapshot without a change is another screen")
		}
		checkScreen(t, s)
		if len(snaps) < 16 {
			snaps = append(snaps, taken{s, s.String(), s.Cursor})
		}
	}
	write := func(n int) {
		n = min(n, len(data))
		term.Write(data[:n])
		data = data[n:]
	}
	for len(script) >= 2 {
		op, arg := script[0], script[1]
		script = script[2:]
		switch op % 4 {
		case 0:
			write(int(arg) + 1)
		case 1:
			var r byte
			if len(script) > 0 {
				r, script = script[0], script[1:]
			}
			term.Resize(byteSize(arg, MaxCols), byteSize(r, MaxRows))
		case 2:
			snapshot()
		case 3:
			for range int(arg) + 1 {
				write(1)
			}
		}
		checkTerminal(t, term)
	}
	write(len(data))
	checkTerminal(t, term)
	snapshot()
	for i, k := range snaps {
		if k.s.String() != k.text || k.s.Cursor != k.cur {
			t.Fatalf("snapshot %d changed after it was taken", i)
		}
	}
	screens := make([]*Screen, len(snaps))
	for i, k := range snaps {
		screens[i] = k.s
	}
	replay(t, screens)
}

// checkTerminal checks the terminal's own state: its size, the cursor,
// the saved cursors and the scroll region on the screen, and both
// screens and the tab stops of its size.
func checkTerminal(t *testing.T, term *Terminal) {
	t.Helper()
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cols < 1 || term.cols > MaxCols || term.rows < 1 || term.rows > MaxRows {
		t.Fatalf("size %dx%d", term.cols, term.rows)
	}
	in := func(c cursor) bool { return c.x >= 0 && c.x < term.cols && c.y >= 0 && c.y < term.rows }
	if !in(term.cur) {
		t.Fatalf("cursor (%d, %d) off the screen of %dx%d", term.cur.x, term.cur.y, term.cols, term.rows)
	}
	for i, c := range term.saved {
		if !in(c) {
			t.Fatalf("saved cursor %d (%d, %d) off the screen of %dx%d", i, c.x, c.y, term.cols, term.rows)
		}
	}
	if term.top < 0 || term.top > term.bot || term.bot >= term.rows {
		t.Fatalf("scroll region %d to %d of %d rows", term.top, term.bot, term.rows)
	}
	if len(term.tabs) != term.cols {
		t.Fatalf("%d tab stops for %d columns", len(term.tabs), term.cols)
	}
	for _, ls := range [][]*line{term.main, term.alt} {
		if len(ls) != term.rows {
			t.Fatalf("%d lines for %d rows", len(ls), term.rows)
		}
		for y, l := range ls {
			if len(l.cells) != term.cols {
				t.Fatalf("line %d has %d cells for %d columns", y, len(l.cells), term.cols)
			}
		}
	}
}

// checkScreen checks that a snapshot agrees with itself: its size and
// cursor, its versions, and its cells, where a wide character's right
// half follows it and nothing else.
func checkScreen(t *testing.T, s *Screen) {
	t.Helper()
	if s.Cursor.X < 0 || s.Cursor.X >= s.Cols || s.Cursor.Y < 0 || s.Cursor.Y >= s.Rows {
		t.Fatalf("cursor (%d, %d) off the screen of %dx%d", s.Cursor.X, s.Cursor.Y, s.Cols, s.Rows)
	}
	vs := s.Versions(nil)
	if len(vs) != s.Rows+1 {
		t.Fatalf("%d versions for %d rows", len(vs), s.Rows)
	}
	for i, v := range vs {
		if v > s.Version {
			t.Fatalf("version %d of row %d after the screen's %d", v, i, s.Version)
		}
	}
	screens := [][]*line{s.main}
	if s.AltScreen {
		screens = append(screens, s.alt)
	}
	for _, ls := range screens {
		if len(ls) != s.Rows {
			t.Fatalf("%d lines for %d rows", len(ls), s.Rows)
		}
		for y, l := range ls {
			if !l.frozen || len(l.cells) != s.Cols {
				t.Fatalf("line %d: frozen %v, %d cells for %d columns", y, l.frozen, len(l.cells), s.Cols)
			}
			for x := range l.cells {
				c := &l.cells[x]
				switch {
				case c.comb > uint16(len(l.comb)):
					t.Fatalf("cell (%d, %d) has grapheme %d of %d", x, y, c.comb, len(l.comb))
				case c.w > 2:
					t.Fatalf("cell (%d, %d) is %d wide", x, y, c.w)
				case c.w == 2 && (x+1 >= len(l.cells) || l.cells[x+1].w != 0):
					t.Fatalf("cell (%d, %d) is wide without its right half: %q", x, y, s.Line(y))
				case c.w == 0 && (x == 0 || l.cells[x-1].w != 2):
					t.Fatalf("cell (%d, %d) is a right half without its character: %q", x, y, s.Line(y))
				}
				if g := l.content(x); !utf8.ValidString(g) || len(g) > maxComb {
					t.Fatalf("cell (%d, %d) holds %q", x, y, g)
				}
			}
		}
	}
	for y := range s.Rows {
		_ = s.Line(y)
	}
}
