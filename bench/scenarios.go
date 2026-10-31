// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"time"

	"changkun.de/x/occamy/internal/terminal"
)

// scenario is one of the measurements in specs/008-native-architecture/
// benchmark.md.
type scenario struct {
	ID       string
	Protocol string
	run      func(ctx context.Context, r *runner, s *session, res *result) error
}

var scenarios = slices.Concat(
	[]scenario{
		{ID: "T1", Protocol: "ssh", run: runTypeEcho},
		{ID: "T2", Protocol: "ssh", run: runBulkOutput},
		{ID: "T3", Protocol: "ssh", run: runTop},
	},
	desktopScenarios("vnc"),
	desktopScenarios("rdp"),
)

// desktopScenarios are the scenarios on the bench desktop over proto.
func desktopScenarios(proto string) []scenario {
	return []scenario{
		{ID: "G1", Protocol: proto, run: runIdle},
		{ID: "G2", Protocol: proto, run: runProbe},
		{ID: "G3", Protocol: proto, run: animated(keyF3, scrollFPS, 10*time.Second)},
		{ID: "G4", Protocol: proto, run: animated(keyF4, dragFPS, 10*time.Second)},
		{ID: "G5", Protocol: proto, run: runMotion},
		{ID: "G6", Protocol: proto, run: runPhoto},
	}
}

// promptQuiet is how long a fresh terminal must stay still before a
// scenario starts typing.
const promptQuiet = 1500 * time.Millisecond

// runTypeEcho (T1) types into an idle shell and measures the time from
// each key until the first frame that draws after it.
func runTypeEcho(ctx context.Context, r *runner, s *session, res *result) error {
	if err := s.settle(ctx, promptQuiet); err != nil {
		return err
	}
	n := r.samples(200)
	var lat []time.Duration
	next := time.Now()
	for i := range n {
		next = next.Add(100 * time.Millisecond) // 10 keys per second
		t0 := time.Now()
		if err := s.key('a' + i%26); err != nil {
			return err
		}
		f, err := s.wait(ctx, t0, func(frame) bool { return true })
		if err != nil {
			return err
		}
		lat = append(lat, f.at.Sub(t0))
		time.Sleep(time.Until(next))
	}
	res.Latency = summarize(lat)
	return nil
}

// magenta marks T2's end: a line in colour 201 of the xterm palette.
// Nothing else on the demo shell is this colour. A v0 client sees it as
// pixels, which guacd's terminal draws, and a v1 client as cells of the
// terminal it emulates.
var magenta = marker{pixels: isMagenta, cells: hasMagenta}

// isMagenta reports magenta pixels in r.
func isMagenta(img *image.RGBA, r image.Rectangle) bool {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.R > 0xf0 && c.G < 0x10 && c.B > 0xf0 {
				return true
			}
		}
	}
	return false
}

// magentaCell is the background of the marker's cells, as the shell's
// printf sets it (SGR 48;5;201).
var magentaCell = terminal.IndexedColor(201)

// hasMagenta reports cells with a magenta background on scr.
func hasMagenta(scr *terminal.Screen) bool {
	for y := range scr.Rows {
		for x := range scr.Cols {
			if scr.Cell(x, y).Style.Bg == magentaCell {
				return true
			}
		}
	}
	return false
}

// runBulkOutput (T2) prints 100000 lines and measures the time until the
// magenta end marker after them is visible.
func runBulkOutput(ctx context.Context, r *runner, s *session, res *result) error {
	if err := s.settle(ctx, promptQuiet); err != nil {
		return err
	}
	lines := r.samples(100000)
	s.setMarker(magenta)
	cmd := fmt.Sprintf(`seq 1 %d; printf '\033[48;5;201m%%40s\033[0m\n' ''`, lines)
	if err := s.typeText(cmd); err != nil {
		return err
	}
	if err := s.settle(ctx, 300*time.Millisecond); err != nil { // the echo
		return err
	}
	m := r.measure(s)
	t0 := time.Now()
	if err := s.key(keyReturn); err != nil {
		return err
	}
	f, err := s.wait(ctx, t0, func(f frame) bool { return f.marked })
	if err != nil {
		return err
	}
	res.Completion = ms(f.at.Sub(t0))
	m.finish(res, f.at)
	return nil
}

// runTop (T3) runs top, refreshing every half second, and measures the
// traffic it causes.
func runTop(ctx context.Context, r *runner, s *session, res *result) error {
	if err := s.settle(ctx, promptQuiet); err != nil {
		return err
	}
	if err := s.typeText("top -d 0.5"); err != nil {
		return err
	}
	if err := s.key(keyReturn); err != nil {
		return err
	}
	// Skip top's first screen, then measure its refreshes.
	if err := s.drain(ctx, time.Second); err != nil {
		return err
	}
	m := r.measure(s)
	if err := s.drain(ctx, r.duration(30*time.Second)); err != nil {
		return err
	}
	m.finish(res, time.Now())
	return s.key('q')
}
