// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"errors"
	"fmt"
	"testing"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// Viewers learn what a backend says they may, and of any other error only
// that the session failed: it may name a host, or a user's password.
func TestCloseForFailure(t *testing.T) {
	const why = backend.Failure("the cause")
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a failure", why, "the cause"},
		{"a wrapped failure", fmt.Errorf("backend: %w", why), "the cause"},
		{"another error", errors.New("dial 10.0.0.1: refused"), "failed"},
		{"no error", nil, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CloseFor(session.End{Reason: session.Failed, Err: tc.err})
			if want := (protocol.Close{Code: protocol.CloseBackendFailed, Reason: tc.want}); got != want {
				t.Errorf("CloseFor = %+v, want %+v", got, want)
			}
		})
	}
}

// A graphical viewer's pacer keeps the frame window agreed, a terminal's
// at most two, so that bulk output coalesces as it did.
func TestViewerPacerWindow(t *testing.T) {
	agreed := protocol.Capabilities{FrameWindow: protocol.MaxFrameWindow}
	cfg := pacer.Config{MinQuality: 40}
	if got := viewerPacer(cfg, agreed, protocol.ClassGraphical); got.Window != protocol.MaxFrameWindow || got.MinQuality != 40 {
		t.Errorf("graphical: %+v, want window %d and the rest kept", got, protocol.MaxFrameWindow)
	}
	if got := viewerPacer(cfg, agreed, protocol.ClassTerminal); got.Window != protocol.DefaultFrameWindow {
		t.Errorf("terminal: window %d, want %d", got.Window, protocol.DefaultFrameWindow)
	}
	agreed.FrameWindow = 1
	if got := viewerPacer(cfg, agreed, protocol.ClassTerminal); got.Window != 1 {
		t.Errorf("terminal that agreed to 1: window %d", got.Window)
	}
}
