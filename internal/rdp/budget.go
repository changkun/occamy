// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"runtime"
	"sync"
	"time"
)

// budgetSlice is what a client takes from its Budget at a time: about a
// millisecond of a core at decodeRate, or 18 RemoteFX tiles, so that
// sessions take turns many times a frame and each takes the Budget's
// lock about a thousand times a second at the most.
const budgetSlice = decodeRate / 1024

// Budget bounds the decoding of the clients that share it, as decodeRate
// bounds each of them, in the same units: a Budget of n cores has n
// times decodeRate. A session's one reader takes about a core at
// decodeRate, so without it one user's limits.sessions_per_user
// sessions to hostile servers would take that many cores.
//
// A client takes a Budget a slice at a time, budgetSlice units that it
// then spends on its charges, whatever their size. A token bucket like
// the client's own holds the slices: its debt is what clients took ahead
// of the rate, and a client waits until the debt as it stood at its take
// is paid for, less the time since its last slice was paid for
// (Client.spend). As a client takes no slice while it waits for one,
// the slices go round: a client gets one to any other's one, and a
// charge of hours delays no client but its own.
//
// What that guarantees, of n clients that want more of the Budget than
// it has, is that each gets at least a nth of it, or decodeRate if that
// is less. A client that wants less than that gets all it wants and,
// once it has its first slice, does not wait for the others; what it
// leaves goes to those that want it. The waits for a slice are of about
// n*budgetSlice/rate each, a millisecond over the Budget's cores for
// each client, or twice that as clients start. Shares are the clients',
// not their users': one user's ten sessions have ten of them.
//
// The units a Budget holds to are those of the charges, so what a
// client reads and ignores, as a fast-path update of a kind it does not
// ask for, it does not count. It bounds the rate of decoding, not how
// many clients decode at a time: a client may spend what it waited for
// at once, and over any time clients take at most its rate and burst
// and a slice each.
//
// Nothing is held across a wait, whose end depends on the clock alone:
// no client's stall can hold up another, and the debt of a client that
// ended is paid by time. Clients wait as they do for decodeRate
// (Client.pace), until their context ends.
type Budget struct {
	cores int
	rate  float64 // units a second

	mu     sync.Mutex // guards the fields below
	tokens float64    // the units left; below 0, the debt of the slices taken
	paced  time.Time  // when tokens was last brought up to date
}

// DefaultDecodeCores is the cores a Budget has unless it is told: half
// of GOMAXPROCS, at least one, so that hostile servers leave the other
// half to what a session's viewers need, encoding first.
func DefaultDecodeCores() int { return max(1, runtime.GOMAXPROCS(0)/2) }

// NewBudget returns a Budget of decoding for the given cores, or
// DefaultDecodeCores if that is not positive. It starts full and holds
// a second of its rate, so that a quiet process lets a session's first
// screen through without a wait, on the largest desktop too: 9 of the
// 14 desktops of its bucket (paceBurst), 0.56 of a second of a core.
func NewBudget(cores int) *Budget {
	if cores <= 0 {
		cores = DefaultDecodeCores()
	}
	rate := float64(cores) * decodeRate
	return &Budget{cores: cores, rate: rate, tokens: rate}
}

// Cores returns the cores the Budget has.
func (b *Budget) Cores() int { return b.cores }

// processBudget is what clients share whose Config names no Budget.
var processBudget = sync.OnceValue(func() *Budget { return NewBudget(0) })

// take takes a slice at the time now, and returns how long the client
// that took it waits for it. Clients read the clock before they lock,
// so now may lie before the last client's, and then adds nothing.
func (b *Budget) take(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := now.Sub(b.paced); d > 0 {
		b.tokens = min(b.rate, b.tokens+d.Seconds()*b.rate)
		b.paced = now
	}
	b.tokens -= budgetSlice
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}
