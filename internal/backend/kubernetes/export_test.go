// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"testing"
	"time"
)

// SetKeepalive sets how often the backend pings, for the test.
func SetKeepalive(t *testing.T, d time.Duration) {
	old := keepalive
	keepalive = d
	t.Cleanup(func() { keepalive = old })
}

// The bounds, for tests.
const (
	MaxMessage = maxMessage
	MaxStatus  = maxStatus
	ReadSize   = readSize
)
