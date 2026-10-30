// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package pacer decides when a session writes a viewer's next frame and
// bytes, and at what lossy quality, so that the viewer sees the effect of
// its input soon after the input (specs/008-native-architecture/
// session-core.md, Input to display).
//
// A Pacer models the viewer's path from its frame acks, as BBR does for
// TCP: the delivery rate is the windowed maximum of rate samples, and the
// propagation delay the windowed minimum of round trips. Writes are paced
// at about the delivery rate, so that data does not queue in the network,
// and the bytes in flight are capped at about twice the bandwidth-delay
// product. The frame that carries an input's effect goes out first: an
// open frame takes the input's tiles next and ends, or a new frame starts
// even one beyond the frame window, and their bytes go out unpaced. The
// quality steps down while the writer spends most of its time waiting for
// the path and back up while it does not, so that motion loses detail
// before input loses responsiveness.
//
// A Pacer does no I/O and reads no clock: every method takes the current
// time, so that tests can drive it on a simulated link. It is not safe for
// concurrent use; each viewer's writer owns one.
package pacer

import (
	"math"
	"time"
)

// Config holds a Pacer's settings. Zero fields take the defaults.
type Config struct {
	// Window is the number of frames that may await an ack, 2 if zero.
	Window int
	// MinInterval is the shortest time between the starts of two frames.
	// Zero leaves the rate of frames to the window and the pacing.
	MinInterval time.Duration
	// MinQuality and MaxQuality bound the lossy quality, 30 and 90 if
	// zero. The quality starts at MaxQuality.
	MinQuality, MaxQuality int
	// Target is the queueing delay the pacer tolerates before it backs
	// off, 5 ms if zero.
	Target time.Duration
	// InitialCap is the number of bytes that may be in flight before the
	// first measurement, 128 KiB if zero.
	InitialCap int
}

const (
	defaultWindow     = 2
	defaultMinQuality = 30
	defaultMaxQuality = 90
	defaultTarget     = 5 * time.Millisecond
	defaultInitialCap = 128 << 10

	// minCap is the smallest cap on bytes in flight.
	minCap = 32 << 10
	// minBurst is the fewest bytes written unpaced after an idle period,
	// and by the frame that carries an input.
	minBurst = 16 << 10
	// minSample is the fewest bytes an ack must acknowledge to give a
	// rate sample: fewer arrive too quickly to time.
	minSample = 4 << 10
	// capGain multiplies the bandwidth-delay product into the cap.
	capGain = 2
	// startupGain is the pacing gain while the delivery rate is unknown
	// or still growing.
	startupGain = 2
	// rateRounds is how many rounds a rate sample stays in the filter.
	rateRounds = 10
	// minRTTWindow is how long a minimum round trip stays valid.
	minRTTWindow = 10 * time.Second
	// qualityStep is the additive quality increase per round.
	qualityStep = 5
	// qualityDecrease multiplies the quality in a round the path limited.
	qualityDecrease = 0.85
	// busyHigh and busyLow bound the share of a round the writer may
	// spend waiting for the path: above busyHigh the quality steps down,
	// below busyLow it steps up. The headroom keeps room for inputs.
	busyHigh = 0.85
	busyLow  = 0.6
)

// probeGains is the pacing gain per round once the rate is known: one
// round probes for more bandwidth, the next drains what that queued. The
// gains are gentler than BBR's 1.25 and 0.75, trading how fast the pacer
// finds new bandwidth for less queueing ahead of inputs.
var probeGains = [...]float64{1.1, 0.9, 1, 1, 1, 1, 1, 1}

type phase int

const (
	startup phase = iota
	drain
	probe
)

type frame struct {
	seq        int64
	id         uint32
	bytes      int
	start, end time.Time
	extra      bool // started beyond the window for an input
}

type sample struct {
	round int64
	rate  float64 // bytes per second
}

// Pacer paces one viewer's frames. Create one with New.
type Pacer struct {
	cfg Config

	inflight      []frame
	inflightBytes int
	extras        int // extra frames in flight, including the open one
	open          bool
	cur           frame
	seq           int64
	lastStart     time.Time

	pendingInput bool
	burst        int // bytes the open frame may still write unpaced

	tokens   float64
	tokensAt time.Time

	minRTT   time.Duration
	minRTTAt time.Time
	srtt     time.Duration
	decode   time.Duration
	lastRecv time.Time

	samples []sample
	rate    float64 // bytes per second, 0 while unknown

	phase        phase
	round        int64
	roundEnd     int64
	roundStart   time.Time
	fullRate     float64
	fullRounds   int
	cycle        int
	blocked      time.Duration // time the writer waited for the path this round
	blockedUntil time.Time     // end of the last pacing wait
	waitingSince time.Time     // when StartFrame began refusing, or zero

	quality     int
	lastBackoff time.Time
}

// New returns a Pacer with the given settings.
func New(cfg Config) *Pacer {
	if cfg.Window <= 0 {
		cfg.Window = defaultWindow
	}
	if cfg.MinQuality <= 0 {
		cfg.MinQuality = defaultMinQuality
	}
	if cfg.MaxQuality <= 0 {
		cfg.MaxQuality = defaultMaxQuality
	}
	cfg.MaxQuality = max(cfg.MaxQuality, cfg.MinQuality)
	if cfg.Target <= 0 {
		cfg.Target = defaultTarget
	}
	if cfg.InitialCap <= 0 {
		cfg.InitialCap = defaultInitialCap
	}
	return &Pacer{cfg: cfg, quality: cfg.MaxQuality}
}

// Input records that the session applied an input from this viewer, and
// lets the frame that carries its effect go out first. If a frame is
// open, the writer writes the input's tiles next, unpaced, and ends it.
// Otherwise the next frame may start at once, one frame beyond the window
// if the window is full, and its first bytes go out unpaced.
func (p *Pacer) Input() {
	if p.open {
		p.burst = max(p.burst, p.inputBurst())
		return
	}
	p.pendingInput = true
}

// StartFrame reports whether a frame may start at now. If not, retry is
// how long until the interval allows one, or zero if it must wait for an
// ack.
func (p *Pacer) StartFrame(now time.Time) (ok bool, retry time.Duration) {
	if p.open {
		return true, 0
	}
	regular := len(p.inflight) - p.extras
	extra := false
	switch {
	case p.pendingInput && (regular < p.cfg.Window || p.extras == 0):
		extra = regular >= p.cfg.Window
		p.pendingInput = false
		p.burst = p.inputBurst()
	case regular >= p.cfg.Window || p.inflightBytes >= p.cap():
		if p.waitingSince.IsZero() {
			p.waitingSince = now
		}
		return false, 0
	default:
		if wait := p.Interval() - now.Sub(p.lastStart); !p.lastStart.IsZero() && wait > 0 {
			return false, wait
		}
		p.burst = 0
	}
	if !p.waitingSince.IsZero() {
		p.blocked += now.Sub(p.waitingSince)
		p.waitingSince = time.Time{}
	}
	if extra {
		p.extras++
	}
	p.open = true
	p.seq++
	p.cur = frame{seq: p.seq, start: now, extra: extra}
	p.lastStart = now
	return true, 0
}

// Delay reports how long the writer should wait before it writes n more
// bytes of the open frame. If more is false, the frame has reached the
// cap on bytes in flight: the writer ends it and leaves the rest of its
// damage for a later frame. The first write of a frame is always allowed.
func (p *Pacer) Delay(now time.Time, n int) (wait time.Duration, more bool) {
	if !p.open || p.cur.bytes == 0 || p.burst > 0 {
		return 0, true
	}
	if p.inflightBytes+p.cur.bytes+n > p.cap() {
		return 0, false
	}
	rate := p.pacingRate()
	if rate == 0 {
		return 0, true
	}
	p.refill(now, rate)
	if p.tokens > 0 {
		return 0, true
	}
	wait = time.Duration(-p.tokens / rate * float64(time.Second))
	// Count the wait once, however often the writer asks.
	from := now
	if p.blockedUntil.After(from) {
		from = p.blockedUntil
	}
	if until := now.Add(wait); until.After(from) {
		p.blocked += until.Sub(from)
		p.blockedUntil = until
	}
	return wait, true
}

// Wrote records that the writer wrote n bytes of the open frame at now.
func (p *Pacer) Wrote(now time.Time, n int) {
	if !p.open {
		return
	}
	p.cur.bytes += n
	p.cur.end = now
	if p.burst > 0 {
		used := min(p.burst, n)
		p.burst -= used
		n -= used
	}
	if rate := p.pacingRate(); rate > 0 {
		p.refill(now, rate)
		p.tokens -= float64(n)
	}
}

// EndFrame records that the writer ended the open frame as id at now.
func (p *Pacer) EndFrame(now time.Time, id uint32) {
	if !p.open {
		return
	}
	p.open = false
	p.cur.id = id
	if p.cur.end.IsZero() {
		p.cur.end = now
	}
	p.inflight = append(p.inflight, p.cur)
	p.inflightBytes += p.cur.bytes
	p.burst = 0
}

// Ack records the viewer's ack of frame id at now, with the time it took
// to decode the frame. It acknowledges every earlier frame too, and
// reports false if id is not in flight.
func (p *Pacer) Ack(now time.Time, id uint32, decode time.Duration) bool {
	i := -1
	for j, f := range p.inflight {
		if f.id == id {
			i = j
			break
		}
	}
	if i < 0 {
		return false
	}
	acked := p.inflight[:i+1]
	f := acked[len(acked)-1]
	bytes := 0
	for _, a := range acked {
		bytes += a.bytes
		if a.extra {
			p.extras--
		}
	}
	first := acked[0].start
	p.inflight = append(p.inflight[:0], p.inflight[i+1:]...)
	p.inflightBytes -= bytes

	decode = max(decode, 0)
	p.decode = ewma(p.decode, decode)
	rtt := max(now.Sub(f.end)-decode, time.Microsecond)
	if p.minRTT == 0 || rtt <= p.minRTT || now.Sub(p.minRTTAt) > minRTTWindow {
		p.minRTT, p.minRTTAt = rtt, now
	}
	p.srtt = ewma(p.srtt, rtt)

	// The client received the frame decode before it acked, and the
	// frame's bytes could not arrive before both the previous frame was
	// received and one round trip after the frame started.
	recv := now.Add(-decode)
	from := first.Add(p.minRTT)
	if p.lastRecv.After(from) {
		from = p.lastRecv
	}
	p.lastRecv = recv
	if d := recv.Sub(from); d > 0 && bytes >= minSample {
		s := float64(bytes) / max(d.Seconds(), 50e-6)
		p.samples = append(p.samples, sample{round: p.round, rate: s})
		p.rate = max(p.rate, s)

		// The delivery rate fell and data is queueing: trust the
		// latest sample rather than wait for the filter to forget.
		if p.phase != startup && rtt-p.minRTT > max(3*p.cfg.Target, p.minRTT/2) &&
			now.Sub(p.lastBackoff) > p.srtt && s < p.rate {
			p.samples = append(p.samples[:0], sample{round: p.round, rate: s})
			p.rate = s
			p.lastBackoff = now
		}
	}

	if f.seq >= p.roundEnd {
		p.endRound(now)
	}
	return true
}

// endRound advances the round at now: it ages the rate filter, moves
// through startup, drain and probing, and steps the quality.
func (p *Pacer) endRound(now time.Time) {
	if !p.waitingSince.IsZero() {
		p.blocked += now.Sub(p.waitingSince)
		p.waitingSince = now
	}
	busy := 0.0
	if !p.roundStart.IsZero() && now.After(p.roundStart) {
		busy = p.blocked.Seconds() / now.Sub(p.roundStart).Seconds()
	}
	limited := p.blocked > 0
	p.blocked, p.roundStart = 0, now
	p.round++
	p.roundEnd = p.seq + 1

	keep := p.samples[:0]
	p.rate = 0
	for _, s := range p.samples {
		if p.round-s.round <= rateRounds {
			keep = append(keep, s)
			p.rate = max(p.rate, s.rate)
		}
	}
	if len(keep) == 0 && len(p.samples) > 0 {
		keep = append(keep, p.samples[len(p.samples)-1])
		p.rate = keep[0].rate
	}
	p.samples = keep

	switch p.phase {
	case startup:
		// Leave startup once three rounds that the path limited did
		// not raise the rate by a quarter.
		if limited && p.rate > 0 {
			if p.rate >= 1.25*p.fullRate {
				p.fullRate, p.fullRounds = p.rate, 0
			} else if p.fullRounds++; p.fullRounds >= 3 {
				p.phase = drain
			}
		}
	case drain:
		p.phase, p.cycle = probe, 0
	case probe:
		p.cycle = (p.cycle + 1) % len(probeGains)
	}

	switch {
	case busy > busyHigh:
		p.quality = max(p.cfg.MinQuality, int(math.Round(float64(p.quality)*qualityDecrease)))
	case busy < busyLow:
		p.quality = min(p.cfg.MaxQuality, p.quality+qualityStep)
	}
}

// Quality is the lossy quality for the viewer's next tiles.
func (p *Pacer) Quality() int { return p.quality }

// Interval is the shortest time between the starts of two frames: the
// configured minimum, or the viewer's decode time if that is longer.
func (p *Pacer) Interval() time.Duration { return max(p.cfg.MinInterval, p.decode) }

// InFlight reports the frames and bytes awaiting an ack, including the
// open frame.
func (p *Pacer) InFlight() (frames, bytes int) {
	frames, bytes = len(p.inflight), p.inflightBytes
	if p.open {
		frames++
		bytes += p.cur.bytes
	}
	return frames, bytes
}

// Rate is the estimated delivery rate in bytes per second, 0 while
// unknown.
func (p *Pacer) Rate() float64 { return p.rate }

// MinRTT is the smallest round trip seen recently, without decoding.
func (p *Pacer) MinRTT() time.Duration { return p.minRTT }

// inputBurst is how many bytes the frame that carries an input may write
// unpaced and beyond the cap.
func (p *Pacer) inputBurst() int { return max(minBurst, int(p.bdp())) }

// bdp is the bandwidth-delay product in bytes, including the viewer's
// decode time, during which its ack cannot arrive.
func (p *Pacer) bdp() float64 {
	return p.rate * (p.minRTT + p.decode).Seconds()
}

// cap is the most bytes that may await an ack.
func (p *Pacer) cap() int {
	if p.rate == 0 {
		return p.cfg.InitialCap
	}
	c := max(minCap, int(capGain*p.bdp()))
	if p.phase == startup {
		c = max(c, p.cfg.InitialCap)
	}
	return c
}

// pacingRate is the rate the writer is paced at, 0 for unpaced.
func (p *Pacer) pacingRate() float64 {
	switch p.phase {
	case startup:
		return startupGain * p.rate
	case drain:
		return 0.75 * p.rate
	}
	return probeGains[p.cycle] * p.rate
}

// refill adds the tokens that accrued at rate since the last refill, up
// to the burst allowed after an idle period.
func (p *Pacer) refill(now time.Time, rate float64) {
	if !p.tokensAt.IsZero() {
		p.tokens += now.Sub(p.tokensAt).Seconds() * rate
	}
	p.tokensAt = now
	p.tokens = min(p.tokens, max(minBurst, rate*p.cfg.Target.Seconds()))
}

func ewma(avg, x time.Duration) time.Duration {
	if avg == 0 {
		return x
	}
	return avg + (x-avg)/8
}
