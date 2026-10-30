// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"sync"
	"time"
)

// maxAcks bounds the acks waiting for the writer. A client acks only
// frames it was sent, a window's worth and one more; a client that sends
// more loses the excess, which the pacer ignores anyway.
const maxAcks = 64

// ack is a client's frame-ack, stamped when the reader received it, so
// that a busy writer does not lengthen the round trip it measures.
type ack struct {
	id     uint32
	decode time.Duration
	at     time.Time
}

// acks carries frame acks from the reader to the writer.
type acks struct {
	mu   sync.Mutex
	list []ack
	wake chan struct{} // holds a token while list is not empty
}

func newAcks() *acks { return &acks{wake: make(chan struct{}, 1)} }

func (a *acks) put(x ack) {
	a.mu.Lock()
	if len(a.list) < maxAcks {
		a.list = append(a.list, x)
	}
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// take appends the waiting acks to dst, oldest first, and removes them.
func (a *acks) take(dst []ack) []ack {
	a.mu.Lock()
	defer a.mu.Unlock()
	dst = append(dst, a.list...)
	a.list = a.list[:0]
	return dst
}
