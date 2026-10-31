// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

// Unit tests of the parity matrix's machinery (parity_test.go), which
// need no demo stack.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// TestParityCell drives a cell with fake checks: when it tries again,
// what it shows, and when it fails the test.
func TestParityCell(t *testing.T) {
	unavailable := fmt.Errorf("%w: waiting for the screen: no such event in 20s", errUnavailable)
	differs := errors.New(`typed "zy", want "yz"`)
	latin1 := parityKnownFailure{"x11vnc: Latin-1 only", `"? ? ??"`}
	garbled := errors.New(`the server took "? ? ??", want "€ ✓ 日本"`)
	tests := []struct {
		name   string
		guacd  bool
		known  *parityKnownFailure
		checks []error // what each try of the check returns
		twins  []error // what each run of the twin returns
		cell   string
		fails  bool // whether the cell fails the test
		pauses int
	}{
		{name: "passes", checks: []error{nil}, cell: "pass"},
		{name: "a native difference stands", checks: []error{differs, nil}, cell: "FAIL: " + differs.Error(), fails: true},
		{
			name:   "native, unavailable, and guacd too: the server's fault",
			checks: []error{unavailable, nil}, twins: []error{unavailable},
			cell: "pass (try 2)", pauses: 1,
		},
		{
			name:   "native, unavailable, but guacd reaches the desktop",
			checks: []error{unavailable, nil}, twins: []error{nil},
			cell: "FAIL: " + unavailable.Error(), fails: true,
		},
		{
			name:   "native, unavailable, but guacd reaches the desktop and fails otherwise",
			checks: []error{unavailable, nil}, twins: []error{differs},
			cell: "FAIL: " + unavailable.Error(), fails: true,
		},
		{
			name:   "native, unavailable at every try",
			checks: []error{unavailable, unavailable, unavailable, nil}, twins: []error{unavailable, unavailable, unavailable},
			cell: "FAIL (try 3): " + unavailable.Error(), fails: true, pauses: 2,
		},
		{
			name:   "native, unavailable, then a difference",
			checks: []error{unavailable, differs, nil}, twins: []error{unavailable},
			cell: "FAIL (try 2): " + differs.Error(), fails: true, pauses: 1,
		},
		{name: "guacd tries again after any failure", guacd: true, checks: []error{differs, differs, nil}, cell: "pass (try 3)", pauses: 2},
		{
			name: "guacd fails at every try", guacd: true,
			checks: []error{differs, differs, differs, nil},
			cell:   "FAIL (try 3): " + differs.Error(), fails: true, pauses: 2,
		},
		{name: "fails as listed", known: &latin1, checks: []error{garbled}, cell: "fail: x11vnc: Latin-1 only"},
		{
			name: "guacd fails as listed after a server fault", guacd: true, known: &latin1,
			checks: []error{unavailable, garbled}, cell: "fail: x11vnc: Latin-1 only (try 2)", pauses: 1,
		},
		{
			name: "listed, but fails otherwise", known: &latin1,
			checks: []error{differs, garbled}, cell: "FAIL: " + differs.Error(), fails: true,
		},
		{
			name: "listed, but unavailable, and guacd too", known: &latin1,
			checks: []error{unavailable, garbled}, twins: []error{unavailable},
			cell: "fail: x11vnc: Latin-1 only (try 2)", pauses: 1,
		},
		{name: "listed, but passes", known: &latin1, checks: []error{nil}, cell: "pass, listed as failing", fails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checks, twins, pauses int
			r := parityCell{
				known: tt.known,
				guacd: tt.guacd,
				check: func() error { checks++; return tt.checks[checks-1] },
				pause: func() { pauses++ },
				logf:  t.Logf,
			}
			if !tt.guacd {
				r.twin = func() error { twins++; return tt.twins[twins-1] }
			}
			cell, err := r.run()
			if cell != tt.cell {
				t.Errorf("the cell is %q, want %q", cell, tt.cell)
			}
			if (err != nil) != tt.fails {
				t.Errorf("the cell's error is %v, want one: %v", err, tt.fails)
			}
			if pauses != tt.pauses || checks != tt.pauses+1 {
				t.Errorf("checked %d times with %d pauses, want %d and %d", checks, pauses, tt.pauses+1, tt.pauses)
			}
			if tt.guacd && twins != 0 {
				t.Errorf("a guacd cell ran a twin %d times", twins)
			}
			if !tt.guacd && twins > tt.pauses+1 {
				t.Errorf("ran the twin %d times for %d pauses", twins, tt.pauses)
			}
		})
	}
}

// TestParityKnown checks that each entry of parityKnown names a cell
// that runs, and says why and how the cell fails.
func TestParityKnown(t *testing.T) {
	cells := map[string]bool{}
	for _, f := range parityFeatures {
		for _, c := range parityColumns {
			cells[f.name+"|"+c.String()] = f.runsOn(c)
		}
	}
	for cell, k := range parityKnown {
		if !cells[cell] {
			t.Errorf("parityKnown lists %q, which is no cell that runs", cell)
		}
		if k.why == "" || k.want == "" {
			t.Errorf("parityKnown lists %q without why or want: %+v", cell, k)
		}
	}
}

// TestParityMatrixDoc checks that the matrix in guacd-bridge.md is the
// one that parityFeatures and parityKnown give, which is what a run of
// TestVNCParity gives when every cell passes or fails as listed at its
// first try.
func TestParityMatrixDoc(t *testing.T) {
	const doc = "../specs/008-native-architecture/guacd-bridge.md"
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The table, indented in its list item.
	var table []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if len(table) == 0 && !strings.HasPrefix(line, "  | Feature |") {
			continue
		}
		if !strings.HasPrefix(line, "  |") {
			break
		}
		table = append(table, strings.TrimPrefix(line, "  "))
	}
	if len(table) == 0 {
		t.Fatalf("no matrix in %s", doc)
	}
	got := strings.Join(table, "\n") + "\n"
	if want := parityTable(parityExpected); got != want {
		t.Errorf("the matrix in %s differs from parityFeatures and parityKnown; it should read\n%s", doc, want)
	}
}

// TestParityToken checks that the demo user's token is renewed at
// parityTokenAge.
func TestParityToken(t *testing.T) {
	now := time.Unix(1e9, 0)
	signIns := 0
	p := &parity{
		addr: "occamy-v1",
		signIn: func(addr string) (string, error) {
			signIns++
			if signIns == 3 {
				return "", errors.New("refused")
			}
			return fmt.Sprintf("token %d of %s", signIns, addr), nil
		},
		now: func() time.Time { return now },
	}
	for _, step := range []struct {
		after time.Duration
		token string
		err   bool
	}{
		{0, "token 1 of occamy-v1", false},
		{parityTokenAge - time.Second, "token 1 of occamy-v1", false},
		{time.Second, "token 2 of occamy-v1", false},
		{parityTokenAge, "", true},
		{0, "token 4 of occamy-v1", false},
	} {
		now = now.Add(step.after)
		tok, err := p.token()
		if tok != step.token || (err != nil) != step.err {
			t.Fatalf("after %v: the token is %q, %v; want %q, an error: %v", step.after, tok, err, step.token, step.err)
		}
	}
}

// TestParitySend checks that an input call's error says how the session
// ended.
func TestParitySend(t *testing.T) {
	ended := &paritySession{more: make(chan struct{}), events: []client.Event{
		client.Frame{},
		client.End{Code: protocol.CloseBackendFailed, Reason: "the server closed the connection"},
	}}
	err := ended.send(7, client.ErrClosed)
	if !errors.Is(err, client.ErrClosed) || !strings.Contains(err.Error(), `backend-failed "the server closed the connection"`) {
		t.Errorf("send after the end returned %v, want client.ErrClosed with the end", err)
	}
	if err := ended.send(7, nil); err != nil {
		t.Errorf("send without an error returned %v", err)
	}
	failed := &paritySession{more: make(chan struct{}), err: errors.New("the stream broke")}
	err = failed.send(7, client.ErrClosed)
	if !errors.Is(err, client.ErrClosed) || !strings.Contains(err.Error(), "the stream broke") {
		t.Errorf("send after the stream broke returned %v, want client.ErrClosed with why", err)
	}
}
