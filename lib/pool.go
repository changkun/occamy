// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package lib

import "sync"

// BufferPoolInitialSize is the minimum number of buffers to create
// before allowing free'd buffers to be reclaimed. In the case a
// protocol rapidly creates, uses, and destroys buffers, this can
// prevent unnecessary reuse of the same buffer (which would make draw
// operations unnecessarily synchronous).
const BufferPoolInitialSize = 1024

// poolInt represents a single integer within a larger pool of integers.
type poolInt struct {
	value int
	next  *poolInt
}

func newPoolInt(v int) poolInt {
	return poolInt{value: v}
}

// A Pool of integers. Integers can be removed from and later free'd back
// into the pool. New integers are returned when the pool is exhausted,
// or when the pool has not met some minimum size. Old, free'd integers
// are returned otherwise.
type Pool struct {
	minSize    int
	active     int
	nextValue  int
	head, tail *poolInt
	mu         sync.Mutex
}

// NewPool allocates a new guac_pool having the given minimum size.
func NewPool(size int) *Pool {
	return &Pool{minSize: size}
}

// Next returns the next available integer from the given guac_pool.
// All integers returned are non-negative, and are returned in sequences,
// starting from 0. This operation is threadsafe.
func (p *Pool) Next() (v int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.active++

	// If more integers are needed, return a new one.
	if p.head == nil || p.nextValue < p.minSize {
		v = p.nextValue
		p.nextValue++
		return
	}

	// Otherwise, remove first integer.
	v = p.head.value
	if p.tail == p.head {
		p.head = nil
		p.tail = nil
	} else {
		// Otherwise, advance head.
		h := p.head
		p.head = h.next
	}
	return
}

// Free frees the given integer back into the given guac_pool.
// The integer given will be available for future calls to p.Next.
// This operation is threadsafe.
func (p *Pool) Free(v int) {
	pInt := newPoolInt(v)

	p.mu.Lock()
	p.active--
	if p.tail == nil {
		p.tail = &pInt
		p.head = p.tail
	} else {
		p.tail.next = &pInt
		p.tail = &pInt
	}
	p.mu.Unlock()
}
