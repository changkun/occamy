// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"runtime"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// budgetClient is a client of the Budget b, as testClient's but for its
// clock and waits, which are real: in a bubble, the bubble's.
func budgetClient(ctx context.Context, b *Budget) *Client {
	c := testClient(1024, 768)
	c.now, c.sleep = nil, nil
	c.ctx, c.cfg.Budget = ctx, b
	return c
}

// drained is a Budget of the given cores with nothing left of its
// burst, so that a test starts at its rate.
func drained(cores int) *Budget {
	b := NewBudget(cores)
	b.tokens, b.paced = 0, time.Now()
	return b
}

// units is how long n units take at rate.
func units(n int, rate float64) time.Duration {
	return time.Duration(float64(n) / rate * float64(time.Second))
}

// flood charges the client n units at a time until ctx ends, spending on
// each charge the time it takes at decodeRate if decode says so. It
// returns the units that went through.
func flood(ctx context.Context, c *Client, n int, decode bool) int {
	sum := 0
	for ctx.Err() == nil && c.pace(n) == nil {
		sum += n
		if decode {
			time.Sleep(units(n, decodeRate))
		}
	}
	return sum
}

// A Budget has its cores' rate, starts with a second of it, and has no
// more than that however long it rests. A slice is free while they last
// and then waits for all that were taken before it. A clock that lies
// behind the last adds nothing.
func TestBudgetTake(t *testing.T) {
	const cores = 2
	const rate = cores * decodeRate
	near := func(got, want time.Duration) bool { return got > want-time.Microsecond && got < want+time.Microsecond }
	start := time.Unix(1<<30, 0)
	b := NewBudget(cores)
	if b.Cores() != cores || b.rate != rate || b.tokens != rate {
		t.Fatalf("a Budget of %d cores: %d, rate %v, %v tokens", cores, b.Cores(), b.rate, b.tokens)
	}
	for i := range rate / budgetSlice {
		if d := b.take(start); d != 0 {
			t.Fatalf("slice %d of the burst waits %v", i, d)
		}
	}
	for i := range 3 {
		if got, want := b.take(start), units((i+1)*budgetSlice, rate); !near(got, want) {
			t.Errorf("slice %d after the burst waits %v, want %v", i, got, want)
		}
	}
	// Three slices of debt, one paid for by the time of a slice.
	later := start.Add(units(budgetSlice, rate))
	if got, want := b.take(later), units(3*budgetSlice, rate); !near(got, want) {
		t.Errorf("a slice of time later: waits %v, want %v", got, want)
	}
	before := b.tokens
	if got, want := b.take(start), units(4*budgetSlice, rate); !near(got, want) {
		t.Errorf("an earlier clock: waits %v, want %v", got, want)
	}
	if b.tokens != before-budgetSlice || !b.paced.Equal(later) {
		t.Errorf("an earlier clock: %v tokens at %v, want %v at %v", b.tokens, b.paced, before-budgetSlice, later)
	}
	b.take(start.Add(time.Hour))
	if want := rate - float64(budgetSlice); b.tokens != want {
		t.Errorf("after an hour: %v tokens, want %v", b.tokens, want)
	}
}

// A Budget is half of GOMAXPROCS by default, at least one core, and any
// other number of cores asked for.
func TestDecodeCores(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, tc := range []struct{ procs, want int }{{1, 1}, {2, 1}, {3, 1}, {4, 2}, {18, 9}, {64, 32}} {
		runtime.GOMAXPROCS(tc.procs)
		if got := DefaultDecodeCores(); got != tc.want {
			t.Errorf("DefaultDecodeCores at GOMAXPROCS %d = %d, want %d", tc.procs, got, tc.want)
		}
		if got := NewBudget(0).Cores(); got != tc.want {
			t.Errorf("NewBudget(0) at GOMAXPROCS %d: %d cores, want %d", tc.procs, got, tc.want)
		}
		if got := NewBudget(-1).Cores(); got != tc.want {
			t.Errorf("NewBudget(-1) at GOMAXPROCS %d: %d cores, want %d", tc.procs, got, tc.want)
		}
		if got := NewBudget(5).Cores(); got != 5 {
			t.Errorf("NewBudget(5) at GOMAXPROCS %d: %d cores", tc.procs, got)
		}
	}
}

// Clients connected without a Budget share one, and a Config's stands
// and is charged.
func TestConnectBudget(t *testing.T) {
	frames := loadTrace(t, "testdata/xrdp-32bpp.trace.gz")
	budget := NewBudget(1)
	for _, tc := range []struct {
		cfg  Config
		want *Budget
	}{{Config{}, processBudget()}, {Config{}, processBudget()}, {Config{Budget: budget}, budget}} {
		c, _ := replay(t, &fakeServer{frames: frames}, tc.cfg)
		if c.cfg.Budget != tc.want {
			t.Errorf("a client's budget: %p, want %p", c.cfg.Budget, tc.want)
		}
	}
	if budget.tokens >= budget.rate {
		t.Error("a recording of xrdp's left the Budget of its client full")
	}
}

// A client takes slices for what it spends and no more: small charges
// share one, and a large one takes as many as it needs.
func TestBudgetSlices(t *testing.T) {
	c, _ := testClock(1024, 768)
	b := NewBudget(1)
	c.cfg.Budget = b
	for _, tc := range []struct{ n, slices int }{
		{0, 0},
		{1, 1},
		{budgetSlice - 1, 1},
		{1, 2},
		{budgetSlice - 1, 2},
		{budgetSlice, 3},
		{3*budgetSlice + 5, 7},
	} {
		if err := c.pace(tc.n); err != nil {
			t.Fatal(err)
		}
		if got := (b.rate - b.tokens) / budgetSlice; got != float64(tc.slices) {
			t.Errorf("after a charge of %d: %v slices taken, want %d", tc.n, got, tc.slices)
		}
	}
	if want := budgetSlice - 5; c.credit != want {
		t.Errorf("a credit of %d, want %d", c.credit, want)
	}
}

// Sessions over a Budget take its rate together: ten of a user's on two
// cores take two cores' worth, not the ten their own rates allow, and
// no less, and they stop as their context ends.
func TestBudgetBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sessions, cores = 10, 2
		const span = 2 * time.Second
		b := drained(cores)
		ctx, cancel := context.WithTimeout(t.Context(), span)
		defer cancel()
		deadline, _ := ctx.Deadline()
		sums := make([]int, sessions)
		var wg sync.WaitGroup
		for i := range sessions {
			c := budgetClient(ctx, b)
			wg.Go(func() { sums[i] = flood(ctx, c, rfxTileCost, false) })
		}
		wg.Wait()
		sum := 0
		for _, n := range sums {
			sum += n
		}
		want := float64(cores*decodeRate) * span.Seconds()
		if float64(sum) > want+sessions*budgetSlice || float64(sum) < 0.99*want {
			t.Errorf("%d sessions on %d cores took %d units in %v, want about %.0f", sessions, cores, sum, span, want)
		}
		if d := time.Since(deadline); d != 0 {
			t.Errorf("the sessions outlived their context by %v", d)
		}
	})
}

// An honest session keeps a fair share however hostile ones charge: by
// a little at a time, by the tile or the slice, or a charge of minutes.
// It wants all it can get, tiles it decodes in the time they take, and
// gets its nth of the Budget: neither a flood of small charges nor one
// large one holds up the Budget as a queue of charges would.
func TestBudgetShare(t *testing.T) {
	const hostile = 10
	const span = time.Second
	for _, cores := range []int{1, 4} {
		for _, tc := range []struct {
			name   string
			charge int
			decode bool
		}{
			{"small charges", 8192, false},
			{"tiles", rfxTileCost, true},
			{"tiles of no cost", rfxTileCost, false},
			{"slices", budgetSlice, false},
			{"twice a slice", 2 * budgetSlice, false},
			{"minutes", 1 << 37, false},
		} {
			t.Run(fmt.Sprintf("%s on %d cores", tc.name, cores), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					b := drained(cores)
					ctx, cancel := context.WithTimeout(t.Context(), span)
					defer cancel()
					var wg sync.WaitGroup
					for range hostile {
						c := budgetClient(ctx, b)
						wg.Go(func() { flood(ctx, c, tc.charge, tc.decode) })
					}
					honest := flood(ctx, budgetClient(ctx, b), rfxTileCost, true)
					wg.Wait()
					share := float64(cores*decodeRate) * span.Seconds() / (hostile + 1)
					if got := float64(honest) / share; got < 0.98 || got > 1.02 {
						t.Errorf("an honest session got %d units, %.1f%% of its share of %.0f", honest, 100*got, share)
					}
				})
			})
		}
	}
}

// An honest session that wants less than its share gets all it wants:
// it waits up to a turn of the sessions that flood the Budget for its
// first slice, and for the others up to another turn in all, as a
// first slice that came late leaves the second less time, where each of
// its 46 slices would wait a turn without the time since the last.
func TestBudgetUnderShare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const hostile, cores = 10, 1
		const span = time.Second
		const charges = 500
		b := drained(cores)
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		for range hostile {
			c := budgetClient(ctx, b)
			wg.Go(func() { flood(ctx, c, 1<<37, false) })
		}
		// Half of its share of the second, in charges of a fifth of a
		// slice or so.
		const n = decodeRate / (hostile + 1) / 2 / charges
		every := span / charges
		turn := units((hostile+1)*budgetSlice, cores*decodeRate)
		c := budgetClient(ctx, b)
		var waited, slowest time.Duration
		start := time.Now()
		for i := range charges {
			time.Sleep(time.Until(start.Add(time.Duration(i) * every)))
			begin := time.Now()
			if err := c.pace(n); err != nil {
				t.Fatal(err)
			}
			d := time.Since(begin)
			waited += d
			slowest = max(slowest, d)
		}
		if d := time.Since(start); d > span+turn {
			t.Errorf("%d charges took %v, want %v", charges, d, span)
		}
		if slowest > turn || waited > 2*turn {
			t.Errorf("%d charges waited %v, and %v at the most, want at most two turns of %v", charges, waited, slowest, turn)
		}
		cancel()
		wg.Wait()
	})
}

// A charge beyond the bucket waits for the time it takes at decodeRate,
// on a Budget of the same rate and on one of twice it: what a session
// waits for the Budget also pays what it owes its bucket, rather than
// being waited a second time. The bucket's burst does not help where
// the Budget has none.
func TestBudgetAlone(t *testing.T) {
	const n = 8 * decodeRate
	for _, tc := range []struct {
		cores int
		want  time.Duration
	}{
		{1, units(n, decodeRate)},
		{2, units(n-int(paceBurst(1024, 768)), decodeRate)},
	} {
		synctest.Test(t, func(t *testing.T) {
			c := budgetClient(t.Context(), drained(tc.cores))
			if err := c.pace(0); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := c.pace(n); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < tc.want-time.Millisecond || d > tc.want+time.Millisecond {
				t.Errorf("%d cores: a charge of %d units waited %v, want %v", tc.cores, n, d, tc.want)
			}
		})
	}
}

// On a Budget of more cores than it needs, a session is still held to
// decodeRate: the Budget adds no rate of its own.
func TestBudgetKeepsDecodeRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const span = time.Second
		ctx, cancel := context.WithTimeout(t.Context(), span)
		defer cancel()
		sum := flood(ctx, budgetClient(ctx, NewBudget(4)), rfxTileCost, false)
		want := decodeRate*span.Seconds() + paceBurst(1024, 768)
		if float64(sum) > want+rfxTileCost || float64(sum) < 0.99*want {
			t.Errorf("a session took %d units in %v of a large Budget, want about %.0f", sum, span, want)
		}
	})
}

// A quiet process lets the first screen of the largest desktop through
// without a wait, on a Budget of one core.
func TestBudgetFirstScreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := budgetClient(t.Context(), NewBudget(1))
		c.width, c.height = 8192, 4096
		start := time.Now()
		if err := c.pace(9 * c.width * c.height); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("the first screen waited %v", d)
		}
	})
}

// A wait for a slice ends with the context, with its error, at once.
// The debt that the client leaves is paid by time alone: the next client
// waits for that and no more, and depends on nobody.
func TestBudgetContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const debt = time.Hour
		b := drained(1)
		b.tokens = -debt.Seconds() * b.rate
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c := budgetClient(ctx, b)
		errc := make(chan error, 1)
		go func() { errc <- c.pace(budgetSlice) }()
		synctest.Wait()
		select {
		case err := <-errc:
			t.Fatalf("a client over the Budget did not wait: %v", err)
		default:
		}
		start := time.Now()
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Errorf("a wait whose context ended: %v", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("a wait took %v to see its context end", d)
		}
		c = budgetClient(t.Context(), b)
		start = time.Now()
		if err := c.pace(budgetSlice); err != nil {
			t.Fatal(err)
		}
		want := debt + units(2*budgetSlice, b.rate)
		if d := time.Since(start); d < want-time.Millisecond || d > want+time.Millisecond {
			t.Errorf("the next client waited %v, want %v", d, want)
		}
	})
}

// Sessions that the Budget holds back apply all that their servers send:
// each draws every rectangle and ends on the last, in the time the
// Budget gives them, where their own rates alone would take a quarter of
// it. A session whose context ends in the wait stops there.
func TestBudgetRun(t *testing.T) {
	const sessions, w, h, rects = 4, 200, 200, 200
	// A mega-mega colour run of the whole rectangle at 16 bits a pixel,
	// each rectangle its colour.
	last := color.RGBA{0xff, 0, 0xff, 0xff} // 0xf81f
	rect := func(i int) []byte {
		px := 0xf81f - (rects-1-i)*0x20
		data := []byte{megaMegaColorRun, byte((w * h) & 0xff), byte((w * h) >> 8), byte(px), byte(px >> 8)}
		return rdptest.FastPath(fastPathBitmaps(1, w, h, 16, data))
	}
	var frames [][]byte
	for i := range rects {
		frames = append(frames, rect(i))
	}
	synctest.Test(t, func(t *testing.T) {
		b := drained(1)
		screens := make([]*screen, sessions)
		clients := make([]*Client, sessions)
		errs := make([]error, sessions)
		start := time.Now()
		var wg sync.WaitGroup
		for i := range sessions {
			clients[i] = budgetClient(t.Context(), b)
			clients[i].width, clients[i].height = w, h
			screens[i] = &screen{}
			wg.Go(func() { errs[i] = runFrames(clients[i], screens[i], frames...) })
		}
		wg.Wait()
		elapsed := time.Since(start)
		for i, c := range clients {
			if !HungUp(errs[i]) || c.Stats().Bitmaps["rle16"] != rects {
				t.Errorf("session %d: %v after %d bitmaps, want %d", i, errs[i], c.Stats().Bitmaps["rle16"], rects)
			}
			if got := screens[i].snapshot().RGBAAt(w-1, h-1); got != last {
				t.Errorf("session %d ended on %v, want %v", i, got, last)
			}
		}
		want, slack := units(sessions*rects*w*h, b.rate), units(sessions*budgetSlice, b.rate)
		if elapsed < want-slack || elapsed > want+slack {
			t.Errorf("%d sessions took %v, want %v", sessions, elapsed, want)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		b := drained(1)
		b.tokens = -b.rate * 3600
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		c := budgetClient(ctx, b)
		c.width, c.height = w, h
		conn := readConn{bytes.NewReader(slices.Concat(frames...))}
		c.conn, c.t, c.ioChannel = conn, newTransport(conn, nil), rdptest.IOChannel
		if err := c.Run(ctx, &screen{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run with its context ended in a wait: %v", err)
		}
	})
}
