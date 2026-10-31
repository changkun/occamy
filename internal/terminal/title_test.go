// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTitleRoundTrip checks that every title the emulator keeps comes
// back whole through Diff's OSC 2, which the parser cuts at maxOSC: a
// title of invalid bytes, each of which becomes U+FFFD, three bytes, grew
// to three times what came in, and came back cut (FuzzWrite found it in
// the review of #117).
func TestTitleRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     int // the title's length
	}{
		{"invalid bytes", strings.Repeat("\xff", 2000), maxTitle - maxTitle%3},
		{"longest", strings.Repeat("a", maxTitle), maxTitle},
		{"longer", strings.Repeat("a", maxTitle+1), maxTitle},
		{"cut between characters", "a" + strings.Repeat("€", maxTitle/3+1), 1 + (maxTitle-1)/3*3},
		{"controls dropped", "a\x7fb\u0085c", 3},
	} {
		term := New(80, 24, Options{})
		term.Write([]byte("\x1b]2;" + tc.in + "\x07"))
		s := term.Snapshot()
		if len(s.Title) != tc.want || !utf8.ValidString(s.Title) {
			t.Errorf("%s: title of %d bytes, valid %v, want %d", tc.name, len(s.Title), utf8.ValidString(s.Title), tc.want)
		}
		ref := New(80, 24, Options{})
		ref.Write(Diff(nil, s))
		if got := ref.Snapshot().Title; got != s.Title {
			t.Errorf("%s: a title of %d bytes comes back through Diff as %d bytes", tc.name, len(s.Title), len(got))
		}
	}
}
