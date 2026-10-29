// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import "testing"

// TestE2E opens a session to each demo server and checks that the remote
// desktop reaches the client. VNC runs twice in a row: tearing a session
// down used to crash occamyd (the libvncclient double frees fixed during
// the revival), and only the next session notices.
func TestE2E(t *testing.T) {
	addr := endpoint(t)
	for _, tt := range []struct{ name, proto string }{
		{"vnc", "vnc"},
		{"vnc-again", "vnc"},
		{"ssh", "ssh"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkSession(t, addr, credentials[tt.proto])
		})
	}
}
