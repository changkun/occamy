// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// confAdapter runs the conformance table against Terminal.
type confAdapter struct {
	t       *Terminal
	replies []byte
}

func newConfAdapter(cols, rows int) *confAdapter {
	a := &confAdapter{}
	a.t = New(cols, rows, Options{Reply: func(p []byte) { a.replies = append(a.replies, p...) }})
	return a
}

func (a *confAdapter) Write(s string)     { a.t.Write([]byte(s)) }
func (a *confAdapter) Line(y int) string  { return a.t.Snapshot().Line(y) }
func (a *confAdapter) Cursor() (int, int) { c := a.t.Snapshot().Cursor; return c.X, c.Y }

func (a *confAdapter) Cell(x, y int) confCell {
	c := a.t.Snapshot().Cell(x, y)
	color := func(c Color) string {
		if i, ok := c.Basic(); ok {
			return strconv.Itoa(int(i))
		}
		if i, ok := c.Indexed(); ok {
			return strconv.Itoa(int(i))
		}
		if r, g, b, ok := c.RGB(); ok {
			return fmt.Sprintf("#%02x%02x%02x", r, g, b)
		}
		return ""
	}
	var attrs strings.Builder
	for _, f := range []struct {
		a Attr
		c byte
	}{{Bold, 'b'}, {Faint, 'f'}, {Italic, 'i'}, {Underline, 'u'}, {Blink, 'k'}, {Inverse, 'r'}, {Invisible, 'h'}, {Strikethrough, 's'}} {
		if c.Style.Attrs&f.a != 0 {
			attrs.WriteByte(f.c)
		}
	}
	return confCell{Content: c.Content, Width: c.Width, Fg: color(c.Style.Fg), Bg: color(c.Style.Bg), Attrs: attrs.String()}
}

func (a *confAdapter) Get(name string) (string, bool) {
	s := a.t.Snapshot()
	b := func(v bool) string { return strconv.FormatBool(v) }
	switch name {
	case "DECCKM":
		return b(s.Modes&ModeCursorKeys != 0), true
	case "DECKPAM":
		return b(s.Modes&ModeKeypad != 0), true
	case "DECAWM":
		return b(s.Modes&ModeAutoWrap != 0), true
	case "bracketed":
		return b(s.Modes&ModeBracketedPaste != 0), true
	case "focus":
		return b(s.Modes&ModeFocus != 0), true
	case "mouse":
		return [...]string{"none", "x10", "normal", "button", "any"}[s.Mouse], true
	case "mouseenc":
		return [...]string{"default", "utf8", "sgr", "urxvt", "sgrpixels"}[s.MouseEnc], true
	case "alt":
		return b(s.AltScreen), true
	case "cursorvisible":
		return b(s.Cursor.Visible), true
	case "cursorshape":
		return strconv.Itoa(s.Cursor.Shape), true
	case "title":
		return s.Title, true
	case "reply":
		return string(a.replies), true
	}
	return "", false
}

// checkConformance runs one case and returns its failures.
func checkConformance(c confCase, term confTerm) []string {
	var fails []string
	term.Write(c.in)
	if c.lines != nil {
		for y := range c.rows {
			want := ""
			if y < len(c.lines) {
				want = c.lines[y]
			}
			if got := term.Line(y); got != want {
				fails = append(fails, fmt.Sprintf("row %d: got %q, want %q", y, got, want))
			}
		}
	}
	if c.cursor != nil {
		if x, y := term.Cursor(); x != c.cursor[0] || y != c.cursor[1] {
			fails = append(fails, fmt.Sprintf("cursor: got (%d,%d), want (%d,%d)", x, y, c.cursor[0], c.cursor[1]))
		}
	}
	for _, cc := range c.cells {
		got := term.Cell(cc.x, cc.y)
		want := cc.want
		if want.Content == "" {
			got.Content = ""
		}
		if want.Width == 0 {
			got.Width = 0
		}
		if got != want {
			fails = append(fails, fmt.Sprintf("cell (%d,%d): got %+v, want %+v", cc.x, cc.y, got, want))
		}
	}
	for k, want := range c.state {
		got, ok := term.Get(k)
		if !ok {
			fails = append(fails, k+": not exposed")
		} else if got != want {
			fails = append(fails, fmt.Sprintf("%s: got %q, want %q", k, got, want))
		}
	}
	return fails
}

func TestConformance(t *testing.T) {
	for _, c := range conformance {
		t.Run(c.cat+"/"+c.name, func(t *testing.T) {
			for _, f := range checkConformance(c, newConfAdapter(c.cols, c.rows)) {
				t.Error(f)
			}
		})
	}
}
