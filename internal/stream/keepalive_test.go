// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// quietLimit is the longest a stream may carry no message: half of
// nginx's default proxy_read_timeout, after which it drops the stream.
const quietLimit = 30 * time.Second

// A stream with nothing to send, as of a still screen, a hidden viewer
// or a quiet terminal, still carries a message well within a proxy's
// idle timeout: a ping, which the client answers and which keeps the
// stream. One that sends frames sends no ping.
func TestKeepalive(t *testing.T) {
	still := func() backend.Backend { return backendtest.NewScreen(200, 150, 0) }
	for _, tc := range []struct {
		name  string
		b     func() backend.Backend
		hello protocol.Hello
		pings bool
	}{
		{"still screen", still, visible, true},
		{"hidden viewer", still, hidden, true},
		{"quiet terminal", func() backend.Backend { return backendtest.NewTerminal(80, 24, 0) }, termHello, true},
		{"moving screen", func() backend.Backend { return backendtest.NewScreen(200, 150, 100*time.Millisecond) }, visible, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(t, tc.b(), 50e6, time.Millisecond, tc.hello)
				const d = 2 * time.Minute
				time.Sleep(d)
				synctest.Wait()
				if err, done := r.servedErr(); done {
					t.Fatalf("Serve returned %v", err)
				}
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				last := time.Duration(0)
				for i, at := range append(r.cl.at, d) {
					if at-last > quietLimit {
						t.Errorf("no message for %v before message %d, want at most %v", at-last, i, quietLimit)
						break
					}
					last = at
				}
				if !tc.pings {
					if len(r.cl.pings) > 0 {
						t.Errorf("%d pings to a viewer sent frames, want none", len(r.cl.pings))
					}
					return
				}
				if n, want := len(r.cl.pings), int(d/quietLimit); n < want {
					t.Errorf("%d pings in %v, want at least %d", n, d, want)
				}
				for i, nonce := range r.cl.pings {
					if nonce != uint32(i+1) { // #nosec G115 -- a small count
						t.Errorf("ping %d has nonce %d, want %d", i, nonce, i+1)
						break
					}
				}
			})
		})
	}
}
