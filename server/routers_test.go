// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"testing"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// A join that fails before the user is prepared must release the proxy
// lock, otherwise every later connection blocks forever.
func TestRouteConnReleasesLockOnJoinError(t *testing.T) {
	p := &proxy{sessions: make(map[string]*Session)}
	jwt := &config.JWT{
		Protocol: "vnc",
		Host:     "127.0.0.1", // no port: lib.NewUser rejects it
		Password: "occamy",
	}

	for i := 0; i < 2; i++ {
		done := make(chan error, 1)
		go func() { done <- p.routeConn(nil, jwt) }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("routeConn accepted a host without port")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("routeConn deadlocked on the proxy lock")
		}
		if !p.mu.TryLock() {
			t.Fatal("proxy lock still held after a failed join")
		}
		p.mu.Unlock()
	}
	if len(p.sessions) != 0 {
		t.Fatalf("failed sessions left in the table: %d", len(p.sessions))
	}
}
