// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backendtest

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/internal/backend"
)

// ErrInputFull is returned by Input when a fake's input queue is full.
var ErrInputFull = errors.New("backendtest: input queue full")

// inputQueue is the number of inputs a fake holds before Input fails.
const inputQueue = 64

// SwatchSize is the side of the Screen's swatch, in pixels.
const SwatchSize = 32

// Swatch is where a Screen shows the last key pressed.
var Swatch = image.Rect(0, 0, SwatchSize, SwatchSize)

// SwatchColor is the colour a Screen paints its swatch for a key press of
// keysym, and for keysym 0 at the start.
func SwatchColor(keysym uint32) color.RGBA {
	return color.RGBA{R: uint8(keysym), G: uint8(keysym >> 8), B: 0x40 | uint8(keysym>>16), A: 0xff} // #nosec G115 -- truncation picks the colour
}

// MotionColor is the colour of a Screen's nth motion frame.
func MotionColor(n int) color.RGBA {
	return color.RGBA{R: 0x80, G: uint8(n), B: uint8(n >> 8), A: 0xff} // #nosec G115 -- truncation picks the colour
}

// recorder is the input and demand bookkeeping that both fakes share.
type recorder struct {
	input chan backend.InputEvent
	end   chan error

	mu      sync.Mutex
	inputs  []backend.InputEvent
	demands []backend.Demand
	visible atomic.Bool
}

func newRecorder() recorder {
	return recorder{
		input: make(chan backend.InputEvent, inputQueue),
		end:   make(chan error, 1),
	}
}

// Input implements backend.Backend.
func (r *recorder) Input(ev backend.InputEvent) error {
	r.mu.Lock()
	r.inputs = append(r.inputs, ev)
	r.mu.Unlock()
	select {
	case r.input <- ev:
		return nil
	default:
		return ErrInputFull
	}
}

// Demand implements backend.Backend.
func (r *recorder) Demand(d backend.Demand) {
	r.mu.Lock()
	r.demands = append(r.demands, d)
	r.mu.Unlock()
	r.visible.Store(d.Visible)
}

// Inputs returns every input passed to Input, including those it
// rejected.
func (r *recorder) Inputs() []backend.InputEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.inputs)
}

// Demands returns every demand passed to Demand.
func (r *recorder) Demands() []backend.Demand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.demands)
}

// End makes Run return err, as when the remote ends the session; nil is
// a normal end. Only the first call has an effect.
func (r *recorder) End(err error) {
	select {
	case r.end <- err:
	default:
	}
}

// Screen is a fake graphical backend. It paints a swatch in the top-left
// corner in the colour of the last key pressed, as the benchmark's
// desktop does, and, if Motion is set, repaints the area below the swatch
// every Motion with the next motion colour while a viewer can see it, as
// a paced backend would.
type Screen struct {
	recorder
	width, height int
	motion        time.Duration
	frames        atomic.Int64
}

var _ backend.Backend = (*Screen)(nil)

// NewScreen returns a Screen of the given size; a zero motion leaves the
// screen still apart from the swatch.
func NewScreen(width, height int, motion time.Duration) *Screen {
	if height <= SwatchSize || width < SwatchSize {
		panic("backendtest: screen smaller than the swatch")
	}
	return &Screen{recorder: newRecorder(), width: width, height: height, motion: motion}
}

// Class implements backend.Backend.
func (s *Screen) Class() backend.Class { return backend.Graphical }

// Frames is the number of motion frames painted.
func (s *Screen) Frames() int { return int(s.frames.Load()) }

// MotionArea is the area that motion frames repaint.
func (s *Screen) MotionArea() image.Rectangle {
	return image.Rect(0, SwatchSize, s.width, s.height)
}

// Run implements backend.Backend.
func (s *Screen) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(s.width, s.height)
	fill(sink, image.Rect(0, 0, s.width, s.height), color.RGBA{A: 0xff})
	fill(sink, Swatch, SwatchColor(0))

	var tick <-chan time.Time
	if s.motion > 0 {
		t := time.NewTicker(s.motion)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.end:
			return err
		case ev := <-s.input:
			if k, ok := ev.(backend.Key); ok && k.Down {
				fill(sink, Swatch, SwatchColor(k.Keysym))
			}
		case <-tick:
			if s.visible.Load() {
				n := s.frames.Add(1)
				fill(sink, s.MotionArea(), MotionColor(int(n)))
			}
		}
	}
}

func fill(sink backend.Sink, r image.Rectangle, c color.RGBA) {
	img := sink.Lock(r)
	defer sink.Unlock(r)
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// Terminal is a fake terminal backend. It echoes TermInput back as a
// shell does, with a carriage return echoed as CR LF, applies Resize, and,
// if Flood is set, prints a numbered line every Flood, as `seq` does.
type Terminal struct {
	recorder
	cols, rows int
	flood      time.Duration
	lines      atomic.Int64
}

var _ backend.Backend = (*Terminal)(nil)

// NewTerminal returns a Terminal of the given size; a zero flood prints
// nothing on its own.
func NewTerminal(cols, rows int, flood time.Duration) *Terminal {
	return &Terminal{recorder: newRecorder(), cols: cols, rows: rows, flood: flood}
}

// Class implements backend.Backend.
func (t *Terminal) Class() backend.Class { return backend.Terminal }

// Lines is the number of numbered lines printed.
func (t *Terminal) Lines() int { return int(t.lines.Load()) }

// Run implements backend.Backend.
func (t *Terminal) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(t.cols, t.rows)

	var tick <-chan time.Time
	if t.flood > 0 {
		tk := time.NewTicker(t.flood)
		defer tk.Stop()
		tick = tk.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-t.end:
			return err
		case ev := <-t.input:
			switch ev := ev.(type) {
			case backend.TermInput:
				sink.TermData(bytes.ReplaceAll(ev.Data, []byte("\r"), []byte("\r\n")))
			case backend.Resize:
				sink.Resize(ev.Width, ev.Height)
			}
		case <-tick:
			n := t.lines.Add(1)
			sink.TermData(append(strconv.AppendInt(nil, n, 10), '\r', '\n'))
		}
	}
}
