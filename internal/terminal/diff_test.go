// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// check feeds from's diff to a reference holding from and reports
// whether it ends showing to.
func check(t *testing.T, from, to *Screen) []byte {
	t.Helper()
	var snaps []*Screen
	if from != nil {
		snaps = append(snaps, from)
	}
	replay(t, append(snaps, to))
	return Diff(from, to)
}

func TestSnapshotVersions(t *testing.T) {
	term := New(10, 3, Options{})
	a := term.Snapshot()
	if b := term.Snapshot(); a != b {
		t.Error("an unchanged terminal returned a new snapshot")
	}
	term.Write([]byte("\x1b[c\x1b[6n\x1b[18t")) // queries change nothing
	if b := term.Snapshot(); a != b {
		t.Error("queries changed the snapshot")
	}
	term.Write([]byte("x"))
	b := term.Snapshot()
	if b == a || b.Version <= a.Version {
		t.Errorf("after a write: version %d, then %d", a.Version, b.Version)
	}
	if a.Line(0) != "" || b.Line(0) != "x" {
		t.Errorf("snapshots are not immutable: %q, %q", a.Line(0), b.Line(0))
	}
	term.Resize(12, 4)
	if c := term.Snapshot(); c.Version <= b.Version || c.Cols != 12 || c.Rows != 4 {
		t.Errorf("after a resize: version %d, size %dx%d", c.Version, c.Cols, c.Rows)
	}
}

func TestSnapshotsShareLines(t *testing.T) {
	term := textScreen(80, 24)
	a := term.Snapshot()
	term.Write([]byte("\x1b[3;1Hchanged"))
	b := term.Snapshot()
	for y := range 24 {
		if same := a.main[y] == b.main[y]; same != (y != 2) {
			t.Errorf("row %d shared %v", y, same)
		}
	}
}

func TestDiffScrollPrimary(t *testing.T) {
	// Bulk output: the screen scrolls by k lines between frames.
	term := New(80, 24, Options{})
	for i := range 30 {
		fmt.Fprintf(writerOf(term), "line %d of the log\r\n", i)
	}
	from := term.Snapshot()
	for i := 30; i < 35; i++ {
		fmt.Fprintf(writerOf(term), "line %d of the log\r\n", i)
	}
	to := term.Snapshot()
	d := check(t, from, to)
	// Five line feeds at the bottom push five lines into the client's
	// scrollback; then only the five new lines are drawn.
	if n := bytes.Count(d, []byte("\n")); n != 5 {
		t.Errorf("%d line feeds, want 5: %q", n, d)
	}
	if bytes.Contains(d, []byte("line 29 ")) || !bytes.Contains(d, []byte("line 34")) {
		t.Errorf("repainted more than the new lines: %q", d)
	}
	if len(d) > 5*25 {
		t.Errorf("%d bytes for a scroll of 5 lines: %q", len(d), d)
	}
}

func TestDiffScrollRegion(t *testing.T) {
	// A pager on the alternate screen scrolls the text above a status
	// line with a scroll region.
	term := New(40, 10, Options{})
	w := writerOf(term)
	fmt.Fprint(w, "\x1b[?1049h")
	for i := range 9 {
		fmt.Fprintf(w, "\x1b[%d;1Htext line %d", i+1, i)
	}
	fmt.Fprint(w, "\x1b[10;1H\x1b[7mstatus\x1b[m")
	from := term.Snapshot()
	fmt.Fprint(w, "\x1b[1;9r\x1b[9;1H")
	for i := 9; i < 12; i++ {
		fmt.Fprintf(w, "\ntext line %d", i)
	}
	fmt.Fprint(w, "\x1b[r\x1b[10;7H")
	to := term.Snapshot()
	d := check(t, from, to)
	if !bytes.Contains(d, []byte("\x1b[1;9r\x1b[3S\x1b[r")) {
		t.Errorf("no region scroll in %q", d)
	}
	if bytes.Contains(d, []byte("text line 8")) || len(d) > 60 {
		t.Errorf("%d bytes, repainting more than the new lines: %q", len(d), d)
	}

	// Scrolling back down uses SD.
	fmt.Fprint(w, "\x1b[1;9r\x1b[1;1H\x1bM\x1bMtop\x1b[r")
	d = check(t, to, term.Snapshot())
	if !bytes.Contains(d, []byte("\x1b[2T")) {
		t.Errorf("no SD in %q", d)
	}
}

func TestDiffScrolledAway(t *testing.T) {
	// More lines than the screen holds arrived between frames: the old
	// lines are scrolled into the client's scrollback and the screen is
	// drawn line by line as it scrolls.
	term := New(20, 5, Options{})
	fmt.Fprint(writerOf(term), "a\r\nb\r\nc\r\nd\r\ne")
	from := term.Snapshot()
	for i := range 12 {
		fmt.Fprintf(writerOf(term), "\r\n%d", i)
	}
	d := check(t, from, term.Snapshot())
	if want := "\n\r7\n\r8\n\r9\n\r10\n\r11"; !bytes.HasSuffix(d, []byte(want)) || len(d) > len(want)+6 {
		t.Errorf("got %q, want a move to the bottom and %q", d, want)
	}
}

func TestDiffSGRMinimal(t *testing.T) {
	cases := []struct {
		from, to Style
		want     string
	}{
		{Style{Attrs: Bold, Fg: BasicColor(1)}, Style{Attrs: Bold, Fg: BasicColor(2)}, "\x1b[32m"},
		{Style{Attrs: Bold | Underline}, Style{Attrs: Underline}, "\x1b[22m"},
		{Style{Attrs: Bold | Faint}, Style{Attrs: Faint}, "\x1b[0;2m"},
		{Style{Attrs: Bold | Faint, Fg: RGBColor(1, 2, 3)}, Style{Attrs: Faint, Fg: RGBColor(1, 2, 3)}, "\x1b[22;2m"},
		{Style{Attrs: Bold | Italic | Underline, Fg: RGBColor(1, 2, 3)}, Style{}, "\x1b[m"},
		{Style{Attrs: Bold | Italic | Underline | Inverse}, Style{Attrs: Blink}, "\x1b[0;5m"},
		{Style{}, Style{Fg: IndexedColor(196), Bg: RGBColor(10, 20, 30)}, "\x1b[38;5;196;48;2;10;20;30m"},
		{Style{Fg: BasicColor(1)}, Style{Fg: IndexedColor(1)}, "\x1b[38;5;1m"},
		{Style{}, Style{Fg: BasicColor(9), Bg: BasicColor(12)}, "\x1b[91;104m"},
		{Style{Fg: BasicColor(3)}, Style{Fg: BasicColor(3), Bg: BasicColor(4)}, "\x1b[44m"},
		{Style{Bg: BasicColor(4)}, Style{}, "\x1b[m"},
		{Style{Attrs: Overline | Strikethrough}, Style{Attrs: Overline}, "\x1b[29m"},
	}
	for _, c := range cases {
		e := &encoder{pen: c.from}
		e.setPen(c.to)
		if string(e.b) != c.want {
			t.Errorf("%+v to %+v: got %q, want %q", c.from, c.to, e.b, c.want)
		}
	}
}

func TestDiffSGRRuns(t *testing.T) {
	// One SGR per run of cells in the same style.
	term := New(40, 2, Options{})
	fmt.Fprint(writerOf(term), "\x1b[1;31mred bold\x1b[0m plain \x1b[44mblue bg\x1b[m")
	d := string(check(t, nil, term.Snapshot()))
	if want := "\x1b[1;31mred bold\x1b[m plain \x1b[44mblue bg\x1b[m"; d != want {
		t.Errorf("got %q, want %q", d, want)
	}
}

func TestDiffCursorMoves(t *testing.T) {
	term := New(80, 24, Options{})
	w := writerOf(term)
	fmt.Fprint(w, "\x1b[10;20Habc")
	from := term.Snapshot()
	cases := []struct{ in, want string }{
		{"\x1b[10;20H", "\b\b\b"},      // three to the left
		{"\x1b[11;1H", "\n\r"},         // next line
		{"\x1b[10;21H", "\b\b"},        // two to the left
		{"\x1b[10;3H", "\x1b[3G"},      // CHA is shorter than CUB 20
		{"\x1b[1;1H", "\x1b[H"},        // home
		{"\x1b[24;80H", "\x1b[24;80H"}, // far
		{"\x1b[12;23H", "\n\n"},        // straight down
		{"\x1b[9;23H", "\x1b[A"},       // one up
		{"\x1b[3;23H", "\x1b[3d"},      // VPA as long as CUU 7
		{"\x1b[10;30H", "\x1b[7C"},     // right on the row
		{"\x1b[20;5H", "\x1b[20;5H"},   // both
		{"\x1b[11;23H", "\n"},          // one down
		{"\x1b[10;1H", "\r"},           // start of the row
		{"\x1b[13;1H", "\n\n\n\r"},     // three down
		{"\x1b[14;1H", "\x1b[14H"},     // four down
	}
	for _, c := range cases {
		term.Write([]byte(c.in))
		got := string(check(t, from, term.Snapshot()))
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
		term.Write([]byte("\x1b[10;23H"))
	}
}

func TestDiffEraseRuns(t *testing.T) {
	term := New(40, 6, Options{})
	w := writerOf(term)
	for y := range 6 {
		fmt.Fprintf(w, "\x1b[%d;1H%s", y+1, strings.Repeat("x", 40))
	}
	from := term.Snapshot()
	fmt.Fprint(w, "\x1b[1;5H\x1b[K\x1b[3;1H\x1b[J")
	d := check(t, from, term.Snapshot())
	if want := "\x1b[3H\x1b[J\x1b[1;5H\x1b[K\n\n\r"; string(d) != want {
		t.Errorf("got %q, want %q", d, want)
	}
	// Erasing in a colour sets only the background first.
	from = term.Snapshot()
	fmt.Fprint(w, "\x1b[1;31m\x1b[2;1Hab\x1b[44m\x1b[K\x1b[m")
	d = check(t, from, term.Snapshot())
	if !bytes.Contains(d, []byte("\x1b[44m\x1b[K")) {
		t.Errorf("no erase in blue in %q", d)
	}
}

func TestDiffWideAtLineEnd(t *testing.T) {
	term := New(10, 4, Options{})
	w := writerOf(term)
	// A wide character that does not fit in the last column wraps.
	fmt.Fprint(w, "012345678漢")
	s := term.Snapshot()
	if s.Line(0) != "012345678" || s.Line(1) != "漢" {
		t.Fatalf("rows %q %q", s.Line(0), s.Line(1))
	}
	check(t, nil, s)
	// A wide character in the last two columns leaves a pending wrap,
	// which the diff reproduces by writing the character again.
	from := term.Snapshot()
	fmt.Fprint(w, "\x1b[3;1H01234567字")
	to := term.Snapshot()
	if !to.Cursor.PendingWrap || to.Cursor.X != 9 {
		t.Fatalf("cursor %+v", to.Cursor)
	}
	d := check(t, from, to)
	if bytes.Contains(d, []byte("\x1b[3;10H")) {
		t.Errorf("moved into the right half of a wide character: %q", d)
	}
	// Narrow characters over wide ones, shifted by one column, rewrite
	// whole wide characters only.
	from = term.Snapshot()
	fmt.Fprint(w, "\x1b[4;1H漢字漢字\x1b[4;2Habcdefg")
	check(t, from, term.Snapshot())
	from = term.Snapshot()
	fmt.Fprint(w, "\x1b[4;1Ha漢字漢字")
	check(t, from, term.Snapshot())
}

func TestDiffRiskyGraphemes(t *testing.T) {
	// After a character clients may draw at another width, the next
	// cell is placed with an absolute move.
	term := New(20, 2, Options{})
	fmt.Fprint(writerOf(term), "a😀b👨‍👩‍👧c漢d")
	d := string(check(t, nil, term.Snapshot()))
	if want := "a😀\x1b[4Gb👨‍\x1b[7G👩‍\x1b[9G👧\x1b[11Gc漢d"; d != want {
		t.Errorf("got %q, want %q", d, want)
	}
}

func TestDiffResize(t *testing.T) {
	term := textScreen(80, 24)
	from := term.Snapshot()
	term.Resize(100, 30)
	fmt.Fprint(writerOf(term), "\x1b[30;1Hbottom")
	to := term.Snapshot()
	d := check(t, from, to)
	if !bytes.HasPrefix(d, []byte("\x1b[H\x1b[2J")) {
		t.Errorf("a resize does not start from a cleared screen: %q", d)
	}
	// On the alternate screen, both screens are drawn again.
	fmt.Fprint(writerOf(term), "\x1b[?1049h\x1b[Hin vim")
	from = term.Snapshot()
	term.Resize(60, 20)
	d = check(t, from, term.Snapshot())
	if !bytes.HasPrefix(d, []byte("\x1b[?1049l\x1b[H\x1b[2J")) || !bytes.Contains(d, []byte("\x1b[?1049h")) {
		t.Errorf("got %q", d)
	}
	// Shrinking keeps the cursor's line on screen.
	term = New(10, 5, Options{})
	fmt.Fprint(writerOf(term), "1\r\n2\r\n3\r\n4\r\n5")
	term.Resize(10, 3)
	if s := term.Snapshot(); s.String() != "3\n4\n5" || s.Cursor.Y != 2 {
		t.Errorf("after shrinking: %q, cursor %+v", s.String(), s.Cursor)
	}
}

func TestDiffAltScreen(t *testing.T) {
	term := New(20, 4, Options{})
	w := writerOf(term)
	fmt.Fprint(w, "$ vim\r\n")
	s0 := term.Snapshot()
	fmt.Fprint(w, "\x1b[?1049h\x1b[?1h\x1b=\x1b[Hfile content\x1b[4;1H\x1b[7m-- INSERT --\x1b[m")
	s1 := term.Snapshot()
	d := check(t, s0, s1)
	if !bytes.HasPrefix(d, []byte("\x1b[?1049h")) {
		t.Errorf("entering: %q", d)
	}
	fmt.Fprint(w, "\x1b[?1049l\x1b[?1l\x1b>$ ")
	s2 := term.Snapshot()
	d = check(t, s1, s2)
	// The client's primary screen comes back as it was; only the prompt
	// is drawn.
	if want := "\x1b[?1049l\x1b[2;3H\x1b[?1l\x1b>"; !bytes.Contains(d, []byte("\x1b[?1049l")) || bytes.Contains(d, []byte("vim")) {
		t.Errorf("leaving: got %q, want about %q", d, want)
	}
	// A late joiner while the alternate screen is shown gets the primary
	// screen first, so that leaving restores it.
	check(t, nil, s1)
	replay(t, []*Screen{s1, s2})
}

func TestDiffModesAndTitle(t *testing.T) {
	term := New(20, 4, Options{})
	from := term.Snapshot()
	fmt.Fprint(writerOf(term), "\x1b]2;remote shell\x07\x1b[?2004h\x1b[?1002h\x1b[?1006h\x1b[?1004h\x1b[?25l\x1b[5 q\x1b[?7l")
	to := term.Snapshot()
	d := string(check(t, from, to))
	for _, want := range []string{"\x1b]2;remote shell\x07", "\x1b[?2004h", "\x1b[?1002h", "\x1b[?1006h", "\x1b[?1004h", "\x1b[?25l", "\x1b[5 q", "\x1b[?7l"} {
		if !strings.Contains(d, want) {
			t.Errorf("%q lacks %q", d, want)
		}
	}
	fmt.Fprint(writerOf(term), "\x1b]2;\x07\x1b[?1000l\x1b[?1006l\x1b[?25h")
	d = string(check(t, to, term.Snapshot()))
	for _, want := range []string{"\x1b]2;\x07", "\x1b[?1002l", "\x1b[?1006l", "\x1b[?25h"} {
		if !strings.Contains(d, want) {
			t.Errorf("%q lacks %q", d, want)
		}
	}
}

func TestReplies(t *testing.T) {
	var term *Terminal
	var got []byte
	term = New(20, 5, Options{Reply: func(p []byte) {
		got = append(got, p...)
		term.Snapshot() // the lock is released before Reply is called
	}})
	term.Write([]byte("\x1b[3;5H\x1b[6n\x1b[c\x1b[>c\x1b[5n\x1b[?2004h\x1b[?2004$p\x1b[18t"))
	want := "\x1b[3;5R\x1b[?1;2c\x1b[>0;276;0c\x1b[0n\x1b[?2004;1$y\x1b[8;5;20t"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestConcurrentSnapshots(t *testing.T) {
	term := New(80, 24, Options{})
	stream := sgrStream()[:200000]
	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			// A viewer's writer: diff from the last screen it sent.
			ref := New(80, 24, Options{})
			var last *Screen
			send := func(s *Screen) {
				if last == nil || last.Cols != s.Cols || last.Rows != s.Rows {
					ref.Resize(s.Cols, s.Rows)
				}
				ref.Write(Diff(last, s))
				last = s
			}
			for {
				send(term.Snapshot())
				select {
				case <-done:
					s := term.Snapshot()
					send(s)
					if !ref.Snapshot().Equal(s) {
						t.Error("a viewer's terminal differs from the last snapshot")
					}
					return
				default:
				}
			}
		})
	}
	for i := 0; i < len(stream); i += 1000 {
		term.Write(stream[i:min(i+1000, len(stream))])
		if i%50000 == 0 {
			term.Resize(80+i%7, 24)
		}
	}
	close(done)
	wg.Wait()
}
