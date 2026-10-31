// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

// A client's close with a code other than normal is logged at info, its
// reason clipped to what a close frame holds, since a browser closes the
// WebSocket itself with 1000 for codes below 3000. A normal close is not.
func TestClientCloseLogged(t *testing.T) {
	var buf bytes.Buffer
	w := &writer{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	if err := w.apply(&protocol.Close{Code: protocol.CloseNormal, Reason: "bye"}); !errors.Is(err, errClientClosed) {
		t.Fatalf("normal close: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a normal close logged %q", buf.String())
	}

	reason := "a tile that does not decode: " + strings.Repeat("é", 100)
	if err := w.apply(&protocol.Close{Code: protocol.CloseProtocolError, Reason: reason}); !errors.Is(err, errClientClosed) {
		t.Fatalf("protocol-error close: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "level=INFO") || !strings.Contains(got, `msg="client closed"`) || !strings.Contains(got, "code=protocol-error") {
		t.Errorf("a protocol-error close logged %q", got)
	}
	want := clip(reason, maxCloseReason)
	if len(want) > maxCloseReason || len(want) < maxCloseReason-1 || !strings.HasPrefix(reason, want) || !strings.Contains(got, want) || strings.Contains(got, reason) {
		t.Errorf("the reason was not clipped to %d bytes at a character: %q", maxCloseReason, got)
	}
}

func TestClip(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abc", 2, "ab"},
		{"aé", 2, "a"}, // é is 2 bytes
		{"aé", 3, "aé"},
		{"é", 1, ""},
		{"", 0, ""},
	} {
		if got := clip(c.s, c.n); got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}
