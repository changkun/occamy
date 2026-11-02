// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
)

// Keysyms of the scene program's function keys (bench/scene).
const (
	keyF2 = keyF1 + 1 + iota
	keyF3
	keyF4
	keyF5
	keyF6
)

// Scene rates, as bench/scene animates them.
const (
	scrollFPS = 62.5
	dragFPS   = 60
	motionFPS = 30
)

// wallpaper reports a frame whose screen shows the wallpaper, which every
// scenario leaves showing when it ends, in the middle of the screen, where
// nothing else of the idle scene draws: not a frame that sizes the screen
// before the desktop's first image.
var wallpaper = marker{pixels: func(img *image.RGBA, _ image.Rectangle) bool {
	b := img.Bounds()
	y := b.Dy() / 2
	return probe.Near(img.RGBAAt(b.Dx()/2, y), probe.Wallpaper(y, b.Dy()))
}}

// probeMarker reports frames that show the colour key k paints the
// swatch.
func probeMarker(k rune) marker {
	return marker{pixels: func(img *image.RGBA, r image.Rectangle) bool {
		return probe.Centre.In(r) && probe.Match(img.RGBAAt(probe.Centre.X, probe.Centre.Y), k)
	}}
}

// ready waits until the scene program answers, which takes a while on
// the first RDP connection, since xrdp starts the session then. It leaves
// the probe scene showing with the swatch painted by z, which differs
// from the first probe key. Showing the probe scene also stops a scene
// that a previous session left running.
func ready(ctx context.Context, s *session) error {
	for i := range 30 {
		// F2 resets the swatch and the letter paints it. Both may arrive
		// in one update, which changes nothing if the swatch already had
		// the letter's colour, so the attempts alternate between two
		// letters of different colours.
		k := 'y' + rune(i%2)
		err := paint(ctx, s, k, keyF2, int(k))
		if err == nil && k == 'y' {
			err = paint(ctx, s, 'z', 'z')
		}
		if err == nil {
			return nil
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return errors.New("the scene program does not answer")
}

// paint presses keys and waits up to a second for the swatch to show the
// colour of k.
func paint(ctx context.Context, s *session, k rune, keys ...int) error {
	s.setMarker(probeMarker(k))
	t0 := time.Now()
	for _, key := range keys {
		if err := s.key(key); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := s.wait(ctx, t0, func(f frame) bool { return f.marked })
	return err
}

// startScene shows the scene for key, from a ready probe scene.
func startScene(ctx context.Context, s *session, key int) error {
	if err := ready(ctx, s); err != nil {
		return err
	}
	return s.key(key)
}

// endScene ends the scene and waits until the screen shows the wallpaper.
// The session must outlive the key: a client that closes at once, while
// the scene draws, loses it on a slow network, as the proxy closes the
// connection on the write that fails and the key is still in its delay.
func endScene(ctx context.Context, s *session) error {
	s.setMarker(wallpaper)
	t0 := time.Now()
	if err := s.key(keyEscape); err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, s.patient(keyWait))
	defer cancel()
	_, err := s.wait(wctx, t0, func(f frame) bool { return f.marked })
	return err
}

// runIdle (G1) measures a still desktop.
func runIdle(ctx context.Context, r *runner, s *session, res *result) error {
	if err := startScene(ctx, s, keyF1); err != nil {
		return err
	}
	if err := s.drain(ctx, time.Second); err != nil {
		return err
	}
	m := r.measure(s)
	if err := s.drain(ctx, r.duration(30*time.Second)); err != nil {
		return err
	}
	m.finish(res, time.Now())
	return nil
}

// The probe's keys: ten a second on average, each up to keyJitter either
// way from its slot. Keys 100 ms apart are 6 periods of a 60 Hz screen and
// 3 of the 30 fps scene, so without the jitter every key of a run would
// meet both at the phase the run started at, and one run's percentiles
// would not cover the spread of phases.
const (
	keyPeriod = 100 * time.Millisecond
	keyJitter = time.Second / 60
)

// keyOffsets returns the offsets of n probe keys from their slots, in
// (-keyJitter, keyJitter). They depend only on the run, so that every
// series of a run presses its keys at the same times.
func keyOffsets(run, n int) []time.Duration {
	rng := rand.New(rand.NewPCG(uint64(run), 0x6f63616d79)) // #nosec G115,G404 -- a run's index, for a fair spread of phases
	offsets := make([]time.Duration, n)
	for i := range offsets {
		offsets[i] = time.Duration(rng.Int64N(int64(2*keyJitter)-1)) - keyJitter + 1
	}
	return offsets
}

// probeLatency presses n probe keys, ten per second, jittered by run, and
// returns the time from each key until the swatch shows its colour.
func probeLatency(ctx context.Context, s *session, run, n int) ([]time.Duration, error) {
	var lat []time.Duration
	// The first slot is far enough off for a key to come before it.
	start := time.Now().Add(keyJitter)
	for i, offset := range keyOffsets(run, n) {
		time.Sleep(time.Until(start.Add(time.Duration(i)*keyPeriod + offset)))
		k := 'a' + rune(i%26)
		s.setMarker(probeMarker(k))
		t0 := time.Now()
		if err := s.key(int(k)); err != nil {
			return nil, err
		}
		wctx, cancel := context.WithTimeout(ctx, s.patient(keyWait))
		f, err := s.wait(wctx, t0, func(f frame) bool { return f.marked })
		cancel()
		if err != nil {
			return nil, fmt.Errorf("probe key %d: %w", i, err)
		}
		lat = append(lat, f.at.Sub(t0))
	}
	return lat, nil
}

// runProbe (G2) measures input latency on a still desktop.
func runProbe(ctx context.Context, r *runner, s *session, res *result) error {
	if err := ready(ctx, s); err != nil {
		return err
	}
	lat, err := probeLatency(ctx, s, r.runIndex, r.samples(200))
	if err != nil {
		return err
	}
	res.Latency = summarize(lat)
	return nil
}

// animated returns a scenario that measures the scene for key for d.
func animated(key int, fps float64, d time.Duration) func(context.Context, *runner, *session, *result) error {
	return func(ctx context.Context, r *runner, s *session, res *result) error {
		if err := startScene(ctx, s, key); err != nil {
			return err
		}
		if err := s.drain(ctx, time.Second); err != nil {
			return err
		}
		m := r.measure(s)
		if err := s.drain(ctx, r.duration(d)); err != nil {
			return err
		}
		m.finish(res, time.Now())
		res.SceneFPS = fps
		return endScene(ctx, s)
	}
}

// runMotion (G5) measures full-screen motion and the latency of probe
// keys during it.
func runMotion(ctx context.Context, r *runner, s *session, res *result) error {
	if err := startScene(ctx, s, keyF5); err != nil {
		return err
	}
	if err := s.drain(ctx, time.Second); err != nil {
		return err
	}
	m := r.measure(s)
	lat, err := probeLatency(ctx, s, r.runIndex, r.samples(200))
	if err != nil {
		return err
	}
	m.finish(res, time.Now())
	res.Latency = summarize(lat)
	res.SceneFPS = motionFPS
	return endScene(ctx, s)
}

// runPhoto (G6) shows a photograph and measures the time until the last
// update of it, and its bytes.
func runPhoto(ctx context.Context, r *runner, s *session, res *result) error {
	if err := ready(ctx, s); err != nil {
		return err
	}
	if err := s.drain(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	m := r.measure(s)
	t0 := time.Now()
	if err := s.key(keyF6); err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	last, err := s.last(lctx, t0, 2*time.Second)
	if err != nil {
		return err
	}
	res.Completion = ms(last.Sub(t0))
	m.finish(res, last)
	return endScene(ctx, s)
}
