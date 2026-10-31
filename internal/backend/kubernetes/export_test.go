// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

// SetKeepalive sets how often the backend pings, for the test.
func SetKeepalive(t *testing.T, d time.Duration) {
	old := keepalive
	keepalive = d
	t.Cleanup(func() { keepalive = old })
}

// OnDialed sets f to run in Run once the stream is open, before
// anything is written to it, for the test.
func OnDialed(t *testing.T, f func()) {
	old := testHookDialed
	testHookDialed = f
	t.Cleanup(func() { testHookDialed = old })
}

// OnServing sets f to run in serve once its goroutines run, before it
// waits for them, with the stream and the channel of the reader's
// result, for the test.
func OnServing(t *testing.T, f func(conn *websocket.Conn, ended chan error)) {
	old := testHookServing
	testHookServing = f
	t.Cleanup(func() { testHookServing = old })
}

// HasProxy reports whether b's requests may go through a proxy.
func HasProxy(b *Backend) bool { return b.transport.Proxy != nil }

// The bounds, for tests.
const (
	MaxMessage = maxMessage
	MaxStatus  = maxStatus
	ReadSize   = readSize
)
