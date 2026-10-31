// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
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

// probeLatency presses n probe keys, ten per second, and returns the time
// from each key until the swatch shows its colour.
func probeLatency(ctx context.Context, s *session, n int) ([]time.Duration, error) {
	var lat []time.Duration
	next := time.Now()
	for i := range n {
		next = next.Add(100 * time.Millisecond)
		k := 'a' + rune(i%26)
		s.setMarker(probeMarker(k))
		t0 := time.Now()
		if err := s.key(int(k)); err != nil {
			return nil, err
		}
		wctx, cancel := context.WithTimeout(ctx, keyWait)
		f, err := s.wait(wctx, t0, func(f frame) bool { return f.marked })
		cancel()
		if err != nil {
			return nil, fmt.Errorf("probe key %d: %w", i, err)
		}
		lat = append(lat, f.at.Sub(t0))
		time.Sleep(time.Until(next))
	}
	return lat, nil
}

// runProbe (G2) measures input latency on a still desktop.
func runProbe(ctx context.Context, r *runner, s *session, res *result) error {
	if err := ready(ctx, s); err != nil {
		return err
	}
	lat, err := probeLatency(ctx, s, r.samples(200))
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
		return s.key(keyEscape)
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
	lat, err := probeLatency(ctx, s, r.samples(200))
	if err != nil {
		return err
	}
	m.finish(res, time.Now())
	res.Latency = summarize(lat)
	res.SceneFPS = motionFPS
	return s.key(keyEscape)
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
	return s.key(keyEscape)
}
