// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package terminal

import (
	"testing"
	"time"
)

// TestWritePanicUnlocks checks that a panic inside Write, as a parser's
// bug would cause and the session recovers from, leaves the terminal
// unlocked: Snapshot, which viewers' writers and the session's owner
// goroutine call, returns rather than waiting forever.
func TestWritePanicUnlocks(t *testing.T) {
	term := New(20, 5, Options{})
	term.mu.Lock()
	term.cur.y = 1 << 20 // the bug: the cursor off the screen
	term.mu.Unlock()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Write did not panic")
			}
		}()
		term.Write([]byte("x"))
	}()
	done := make(chan *Screen, 1)
	go func() { done <- term.Snapshot() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Snapshot waits for the lock that Write's panic left")
	}
}
