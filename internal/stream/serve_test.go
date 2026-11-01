// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"errors"
	"fmt"
	"testing"

	"changkun.de/x/occamy/internal/backend"
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
