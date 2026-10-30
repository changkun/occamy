// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The traces in testdata are the output of nano, vim, less and top
// (macOS builds, TERM=xterm-256color) recorded in a tmux pane with
// pipe-pane while a script typed into them. For each, NAME.screen is the
// pane's text as tmux captured it at the end, and NAME.state is
// tmux's cursor_x, cursor_y, cursor_flag, alternate_on, insert_flag,
// keypad_cursor_flag, keypad_flag, mouse_any_flag and the pane size.

type trace struct {
	name       string
	raw        []byte
	screen     []string
	cols, rows int
	cursor     [2]int
	visible    bool
	alt        bool
	ckm        bool
	mouse      bool
}

func loadTraces(t testing.TB) []trace {
	t.Helper()
	var out []trace
	for _, name := range []string{"nano", "vim", "less", "top"} {
		read := func(ext string) []byte {
			b, err := os.ReadFile(filepath.Join("testdata", name+ext))
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		tr := trace{name: name, raw: read(".raw")}
		var vis, alt, ins, ckm, kp, mouse int
		if _, err := fmt.Sscanf(string(read(".state")), "%d,%d,%d,%d,%d,%d,%d,%d,%dx%d",
			&tr.cursor[0], &tr.cursor[1], &vis, &alt, &ins, &ckm, &kp, &mouse, &tr.cols, &tr.rows); err != nil {
			t.Fatal(err)
		}
		tr.visible, tr.alt, tr.ckm, tr.mouse = vis == 1, alt == 1, ckm == 1, mouse == 1
		for _, l := range strings.Split(strings.TrimSuffix(string(read(".screen")), "\n"), "\n") {
			tr.screen = append(tr.screen, strings.TrimRight(l, " "))
		}
		out = append(out, tr)
	}
	return out
}

// TestTraces checks that the terminal ends where tmux did.
func TestTraces(t *testing.T) {
	for _, tr := range loadTraces(t) {
		t.Run(tr.name, func(t *testing.T) {
			term := New(tr.cols, tr.rows, Options{})
			term.Write(tr.raw)
			s := term.Snapshot()
			for y := range tr.rows {
				if got := s.Line(y); got != tr.screen[y] {
					t.Errorf("row %d:\n got  %q\n want %q", y, got, tr.screen[y])
				}
			}
			// tmux reports a pending wrap as one column past the edge.
			want := tr.cursor
			if want[0] == tr.cols {
				want[0] = tr.cols - 1
			}
			if s.Cursor.X != want[0] || s.Cursor.Y != want[1] {
				t.Errorf("cursor: got (%d,%d), want (%d,%d)", s.Cursor.X, s.Cursor.Y, want[0], want[1])
			}
			if s.Cursor.Visible != tr.visible || s.AltScreen != tr.alt ||
				(s.Modes&ModeCursorKeys != 0) != tr.ckm || (s.Mouse != MouseNone) != tr.mouse {
				t.Errorf("state: visible %v alt %v DECCKM %v mouse %v, want %v %v %v %v",
					s.Cursor.Visible, s.AltScreen, s.Modes&ModeCursorKeys != 0, s.Mouse != MouseNone,
					tr.visible, tr.alt, tr.ckm, tr.mouse)
			}
		})
	}
}

// TestDiffReproducesTraces checks the diff property on the traces, with
// snapshots every few bytes.
func TestDiffReproducesTraces(t *testing.T) {
	for _, tr := range loadTraces(t) {
		for _, every := range []int{1, 7, 64, 500} {
			t.Run(fmt.Sprintf("%s/%d", tr.name, every), func(t *testing.T) {
				term := New(tr.cols, tr.rows, Options{})
				var snaps []*Screen
				for i := 0; i < len(tr.raw); i += every {
					term.Write(tr.raw[i:min(i+every, len(tr.raw))])
					snaps = append(snaps, term.Snapshot())
				}
				replay(t, snaps)
			})
		}
	}
}
