// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package pacer

import (
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

func ms(n float64) time.Duration { return time.Duration(n * float64(time.Millisecond)) }

// send starts a frame, writes n bytes and ends it as id.
func send(t *testing.T, p *Pacer, now time.Time, id uint32, n int) {
	t.Helper()
	if ok, _ := p.StartFrame(now); !ok {
		t.Fatalf("frame %d could not start", id)
	}
	p.Wrote(now, n)
	p.EndFrame(now, id)
}

func TestWindow(t *testing.T) {
	p := New(Config{})
	send(t, p, t0, 1, 1000)
	send(t, p, t0, 2, 1000)
	if ok, retry := p.StartFrame(t0); ok || retry != 0 {
		t.Fatalf("third frame started with two in flight (retry %v)", retry)
	}
	if !p.Ack(t0.Add(ms(10)), 1, 0) {
		t.Fatal("ack of frame 1 rejected")
	}
	if ok, _ := p.StartFrame(t0.Add(ms(10))); !ok {
		t.Fatal("frame could not start after an ack")
	}
}

// TestHold checks that a frame which cannot be cut short waits for acks
// at the cap only while earlier frames are in flight.
func TestHold(t *testing.T) {
	p := New(Config{InitialCap: 10000})
	if ok, _ := p.StartFrame(t0); !ok {
		t.Fatal("frame 1 could not start")
	}
	p.Wrote(t0, 4000)
	if p.Hold(8000) {
		t.Error("a frame alone in flight was held")
	}
	p.EndFrame(t0, 1)
	if ok, _ := p.StartFrame(t0); !ok {
		t.Fatal("frame 2 could not start")
	}
	if p.Hold(8000) {
		t.Error("a frame's first write was held")
	}
	p.Wrote(t0, 100)
	if !p.Hold(6000) {
		t.Error("a write past the cap was not held with a frame in flight")
	}
	if _, more := p.Delay(t0, 6000); more {
		t.Error("Delay let a write past the cap go on")
	}
	p.Ack(t0.Add(ms(10)), 1, 0)
	if p.Hold(6000) {
		t.Error("a write was held with nothing else in flight")
	}
}

func TestInputGoesBeyondTheWindowOnce(t *testing.T) {
	p := New(Config{})
	send(t, p, t0, 1, 1000)
	send(t, p, t0, 2, 1000)
	p.Input()
	send(t, p, t0, 3, 1000)
	if frames, _ := p.InFlight(); frames != 3 {
		t.Fatalf("%d frames in flight, want 3", frames)
	}
	p.Input()
	if ok, _ := p.StartFrame(t0); ok {
		t.Fatal("a second input went two frames beyond the window")
	}
}

func TestInputBypassesTheInterval(t *testing.T) {
	p := New(Config{MinInterval: ms(20)})
	send(t, p, t0, 1, 1000)
	p.Ack(t0.Add(ms(1)), 1, 0)
	if ok, retry := p.StartFrame(t0.Add(ms(5))); ok || retry != ms(15) {
		t.Fatalf("frame inside the interval: ok %v, retry %v, want a retry in 15ms", ok, retry)
	}
	p.Input()
	if ok, _ := p.StartFrame(t0.Add(ms(5))); !ok {
		t.Fatal("the frame after an input waited for the interval")
	}
}

func TestCumulativeAck(t *testing.T) {
	p := New(Config{Window: 4})
	for id := uint32(1); id <= 3; id++ {
		send(t, p, t0, id, 1000)
	}
	if p.Ack(t0, 9, 0) {
		t.Error("ack of an unknown frame accepted")
	}
	if !p.Ack(t0.Add(ms(10)), 2, 0) {
		t.Fatal("ack of frame 2 rejected")
	}
	if frames, bytes := p.InFlight(); frames != 1 || bytes != 1000 {
		t.Errorf("in flight %d frames, %d bytes; want 1 and 1000", frames, bytes)
	}
	if p.Ack(t0.Add(ms(10)), 1, 0) {
		t.Error("ack of an already acknowledged frame accepted")
	}
}

func TestIntervalFollowsDecodeTime(t *testing.T) {
	p := New(Config{MinInterval: ms(4)})
	if got := p.Interval(); got != ms(4) {
		t.Fatalf("interval %v, want 4ms", got)
	}
	for id := uint32(1); id <= 20; id++ {
		now := t0.Add(time.Duration(id) * ms(50))
		send(t, p, now, id, 1000)
		p.Ack(now.Add(ms(30)), id, ms(25))
	}
	if got := p.Interval(); got < ms(24) || got > ms(25) {
		t.Errorf("interval %v with 25ms decodes, want about 25ms", got)
	}
}

// link is a simulated path from the server to a viewer: a bottleneck
// queue at the sender, a propagation delay, and a client that decodes one
// frame at a time.
type link struct {
	rate    float64 // bytes per second
	rtt     time.Duration
	decode  time.Duration
	busy    time.Time // when the bottleneck has drained
	decoded time.Time // when the client's decoder is free
}

// write queues n bytes at now and returns when their last byte reaches
// the client.
func (l *link) write(now time.Time, n int) time.Time {
	start := now
	if l.busy.After(start) {
		start = l.busy
	}
	l.busy = start.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	return l.busy.Add(l.rtt / 2)
}

// present returns when the client shows a frame whose last byte arrived
// at arrive, and the decode time it reports, including any wait for the
// decoder.
func (l *link) present(arrive time.Time) (shown time.Time, decode time.Duration) {
	start := arrive
	if l.decoded.After(start) {
		start = l.decoded
	}
	l.decoded = start.Add(l.decode)
	return l.decoded, l.decoded.Sub(arrive)
}

type ack struct {
	at     time.Time
	id     uint32
	decode time.Duration
}

// scene is full-screen motion: every period the whole screen changes, and
// encoding it costs bytes at the pacer's quality.
type scene struct {
	period time.Duration
	bytes  func(quality int) int
}

const (
	tileBytes   = 4 << 10
	swatchBytes = 1 << 10
)

// sim drives a Pacer the way a viewer's writer does: frames carry the
// current damage in tiles, and after an input the writer sends the swatch
// that shows the input's effect next and ends the frame.
type sim struct {
	p     *Pacer
	l     *link
	scene scene
	now   time.Time

	acks      []ack
	id        uint32
	open      bool
	arrive    time.Time
	dirty     int
	nextScene time.Time
	blocked   time.Time

	swatch    bool // an input's swatch awaits sending
	inputAt   time.Time
	inFrame   bool // the open frame carries the swatch
	latencies []time.Duration

	delivered map[int]int // bytes arriving per second of simulated time
	qualities []int
}

func newSim(p *Pacer, l *link, s scene) *sim {
	return &sim{p: p, l: l, scene: s, now: t0, nextScene: t0, delivered: map[int]int{}}
}

// input applies an input whose effect is the swatch: the writer sends it
// next, in the open frame if there is one, and ends that frame.
func (s *sim) input() {
	s.p.Input()
	s.swatch, s.inputAt = true, s.now
}

func (s *sim) endFrame() {
	s.id++
	s.p.EndFrame(s.now, s.id)
	shown, decode := s.l.present(s.arrive)
	s.acks = append(s.acks, ack{at: shown.Add(s.l.rtt / 2), id: s.id, decode: decode})
	if s.inFrame {
		s.latencies = append(s.latencies, shown.Sub(s.inputAt))
		s.inFrame = false
	}
	s.open = false
}

// step advances the simulation to now.
func (s *sim) step(now time.Time) {
	s.now = now
	for len(s.acks) > 0 && !s.acks[0].at.After(now) {
		a := s.acks[0]
		s.acks = s.acks[1:]
		s.p.Ack(a.at, a.id, a.decode)
	}
	if !now.Before(s.nextScene) {
		s.dirty = s.scene.bytes(s.p.Quality())
		s.nextScene = s.nextScene.Add(s.scene.period)
		s.qualities = append(s.qualities, s.p.Quality())
	}
	for !now.Before(s.blocked) {
		if !s.open {
			if !s.swatch && s.dirty == 0 {
				return
			}
			ok, retry := s.p.StartFrame(now)
			if !ok {
				if retry > 0 {
					s.blocked = now.Add(retry)
				}
				return
			}
			s.open = true
		}
		n := min(tileBytes, s.dirty)
		if s.swatch {
			n = swatchBytes
		}
		if n == 0 {
			s.endFrame()
			continue
		}
		wait, more := s.p.Delay(now, n)
		if !more {
			s.endFrame()
			continue
		}
		if wait > 0 {
			s.blocked = now.Add(wait)
			return
		}
		s.arrive = s.l.write(now, n)
		s.delivered[int(s.arrive.Sub(t0)/time.Second)] += n
		s.p.Wrote(now, n)
		if s.swatch {
			s.swatch, s.inFrame = false, true
			s.endFrame()
			continue
		}
		s.dirty -= n
	}
}

// run simulates d with an input every 97 ms, calling at for each step.
func (s *sim) run(d time.Duration, at func(now time.Time)) {
	const tick = 100 * time.Microsecond
	nextInput := t0.Add(ms(500))
	for now := t0; now.Before(t0.Add(d)); now = now.Add(tick) {
		if at != nil {
			at(now)
		}
		if !now.Before(nextInput) {
			s.step(now)
			s.input()
			nextInput = nextInput.Add(ms(97))
		}
		s.step(now)
	}
}

// p95 returns the 95th percentile of the latencies after the first n.
func p95(lat []time.Duration, skip int) time.Duration {
	l := slices.Clone(lat[min(skip, len(lat)):])
	slices.Sort(l)
	return l[len(l)*95/100]
}

// The benchmark's network conditions, and a full-screen motion that
// needs 12 MB/s at the highest quality: more than N2 carries.
var (
	n1     = link{rate: 100e6 / 8, rtt: ms(20), decode: ms(2)}
	n2     = link{rate: 20e6 / 8, rtt: ms(50), decode: ms(2)}
	motion = scene{period: ms(1000.0 / 30), bytes: func(q int) int { return 400 << 10 * q / 100 }}
)

func TestFullScreenMotion(t *testing.T) {
	for _, tc := range []struct {
		name string
		link link
	}{{"N1", n1}, {"N2", n2}} {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.link
			s := newSim(New(Config{}), &l, motion)
			s.run(10*time.Second, nil)

			// An input's effect waits behind little: beyond its floor, half
			// a round trip and a decode, a few target delays at most.
			bound := l.rtt/2 + l.decode + 4*defaultTarget
			if got := p95(s.latencies, 20); got > bound {
				t.Errorf("p95 input to display %v, want at most %v", got, bound)
			}
			// The link stays busy.
			var bytes int
			for sec := 5; sec < 10; sec++ {
				bytes += s.delivered[sec]
			}
			use := float64(bytes) / 5 / l.rate
			if demand := float64(motion.bytes(defaultMinQuality)) * 30 / l.rate; use < 0.8*min(1, demand) {
				t.Errorf("link %.0f%% used, want at least 80%% of %.0f%%", 100*use, 100*min(1, demand))
			}
			t.Logf("p95 %v, link %.0f%% used, quality %v, rate %.1f MB/s, min RTT %v",
				p95(s.latencies, 20), 100*use, s.qualities[len(s.qualities)-5:], s.p.Rate()/1e6, s.p.MinRTT())
		})
	}
}

func TestQualityAdapts(t *testing.T) {
	l := n2
	s := newSim(New(Config{}), &l, motion)
	var q []int
	s.run(15*time.Second, func(now time.Time) {
		if now.Equal(t0.Add(10 * time.Second)) {
			q = slices.Clone(s.qualities[len(s.qualities)-30:])
			// The motion stops; small updates remain.
			s.scene = scene{period: ms(100), bytes: func(int) int { return 8 << 10 }}
		}
	})
	if slices.Max(q) >= defaultMaxQuality {
		t.Errorf("quality %v stayed at the maximum on a link that cannot carry it", q)
	}
	if slices.Min(q) < defaultMinQuality {
		t.Errorf("quality %v fell below the minimum", q)
	}
	if got := s.p.Quality(); got != defaultMaxQuality {
		t.Errorf("quality %d after small updates, want %d", got, defaultMaxQuality)
	}
}

func TestBandwidthDrop(t *testing.T) {
	l := n1
	s := newSim(New(Config{}), &l, motion)
	var dropped int
	s.run(12*time.Second, func(now time.Time) {
		if now.Equal(t0.Add(6 * time.Second)) {
			l.rate = 10e6 / 8
			dropped = len(s.latencies)
		}
	})
	// Two seconds after the drop, inputs wait behind little again.
	after := s.latencies[dropped:]
	settled := after[len(after)*2/6:]
	bound := l.rtt/2 + l.decode + 4*defaultTarget
	if got := p95(settled, 0); got > bound {
		t.Errorf("p95 input to display %v after the drop, want at most %v", got, bound)
	}
	t.Logf("p95 %v before, %v after; rate %.1f MB/s", p95(s.latencies[:dropped], 20), p95(settled, 0), s.p.Rate()/1e6)
}

// TestUnpacedBaseline shows what the pacer prevents: the same motion with
// only a frame window, as a stream of frames without pacing, queues
// inputs behind whole frames.
func TestUnpacedBaseline(t *testing.T) {
	l := n2
	s := newSim(New(Config{InitialCap: 1 << 30}), &l, motion)
	s.p.phase = probe // never measures, so never paces
	s.p.samples = nil
	s.run(10*time.Second, func(time.Time) { s.p.rate = 0 })
	paced := n2
	ps := newSim(New(Config{}), &paced, motion)
	ps.run(10*time.Second, nil)
	if u, p := p95(s.latencies, 20), p95(ps.latencies, 20); u < 2*p {
		t.Errorf("unpaced p95 %v, paced %v: pacing should at least halve it", u, p)
	} else {
		t.Logf("unpaced p95 %v, paced %v", u, p)
	}
}
