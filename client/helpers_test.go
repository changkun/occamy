// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// pipe joins a client and a server in process, for tests in a synctest
// bubble. Each direction is a queue of messages that the reader takes
// after delay, so that neither end's writes wait.
type pipe struct {
	delay  time.Duration
	toCli  *queue
	toSrv  *queue
	once   sync.Once
	closed chan struct{}

	mu       sync.Mutex
	closedBy *protocol.CloseCode // the code the client closed with
}

func newPipe(delay time.Duration) *pipe {
	return &pipe{delay: delay, toCli: newQueue(), toSrv: newQueue(), closed: make(chan struct{})}
}

func (p *pipe) close() {
	p.once.Do(func() {
		close(p.closed)
		p.toCli.close()
		p.toSrv.close()
	})
}

// clientCode reports the code the client closed the pipe with.
func (p *pipe) clientCode() (protocol.CloseCode, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closedBy == nil {
		return 0, false
	}
	return *p.closedBy, true
}

func (p *pipe) server() stream.Conn { return pipeEnd{p, p.toSrv, p.toCli, false} }
func (p *pipe) client() client.Conn { return pipeEnd{p, p.toCli, p.toSrv, true} }

type pipeEnd struct {
	p        *pipe
	in, out  *queue
	isClient bool
}

func (e pipeEnd) ReadMessage(ctx context.Context) ([]byte, error) { return e.in.pop(ctx) }

func (e pipeEnd) WriteMessage(ctx context.Context, b []byte) error {
	select {
	case <-e.p.closed:
		return io.ErrClosedPipe
	default:
	}
	e.out.push(slices.Clone(b), time.Now().Add(e.p.delay))
	return nil
}

// Close closes the pipe for both ends; the client's end records its
// code.
func (e pipeEnd) Close(code protocol.CloseCode, _ string) error {
	if e.isClient {
		e.p.mu.Lock()
		if e.p.closedBy == nil {
			e.p.closedBy = &code
		}
		e.p.mu.Unlock()
	}
	e.p.close()
	return nil
}

// queue delivers messages in order, each no earlier than its time.
type queue struct {
	mu     sync.Mutex
	q      []timed
	notify chan struct{}
	done   bool
}

type timed struct {
	b  []byte
	at time.Time
}

func newQueue() *queue { return &queue{notify: make(chan struct{}, 1)} }

func (q *queue) push(b []byte, at time.Time) {
	q.mu.Lock()
	q.q = append(q.q, timed{b, at})
	q.mu.Unlock()
	q.poke()
}

func (q *queue) poke() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *queue) close() {
	q.mu.Lock()
	q.done = true
	q.mu.Unlock()
	q.poke()
}

func (q *queue) pop(ctx context.Context) ([]byte, error) {
	for {
		q.mu.Lock()
		var wait time.Duration
		if len(q.q) > 0 {
			if wait = time.Until(q.q[0].at); wait <= 0 {
				b := q.q[0].b
				q.q = q.q[1:]
				q.mu.Unlock()
				return b, nil
			}
		} else if q.done {
			q.mu.Unlock()
			return nil, io.EOF
		}
		q.mu.Unlock()
		var timer <-chan time.Time
		t := time.NewTimer(wait)
		if wait > 0 {
			timer = t.C
		}
		select {
		case <-q.notify:
		case <-timer:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
		t.Stop()
	}
}

// tapped exposes the sink a backend runs with.
type tapped struct {
	backend.Backend
	mu   sync.Mutex
	sink backend.Sink
	ran  chan struct{}
}

func tap(b backend.Backend) *tapped { return &tapped{Backend: b, ran: make(chan struct{})} }

func (b *tapped) Run(ctx context.Context, sink backend.Sink) error {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
	close(b.ran)
	return b.Backend.Run(ctx, sink)
}

func (b *tapped) Sink() backend.Sink {
	<-b.ran
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sink
}

// rig is a real session served by stream.Serve to a client over a pipe.
type rig struct {
	s      *session.Session
	owner  *session.Viewer
	pipe   *pipe
	cl     *client.Session
	served chan error
}

func newRig(t *testing.T, b backend.Backend, opts client.Options, cfg stream.Config) *rig {
	t.Helper()
	r, err := startRig(t, b, opts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// startRig is newRig that returns New's error.
func startRig(t *testing.T, b backend.Backend, opts client.Options, cfg stream.Config) (*rig, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: 64})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	r := &rig{s: s, owner: owner, pipe: newPipe(time.Millisecond), served: make(chan error, 1)}
	if cfg.SessionID == "" {
		cfg.SessionID = "s1"
	}
	go func() { r.served <- stream.Serve(ctx, r.pipe.server(), owner, opts.Hello(), cfg) }()
	t.Cleanup(func() {
		if r.cl != nil {
			r.cl.Close()
		}
		cancel()
		s.Close()
		r.pipe.close()
		<-r.served
	})
	r.cl, err = client.New(ctx, r.pipe.client(), opts)
	return r, err
}

// serverScreen reads a session's framebuffer whole.
func serverScreen(s *session.Session) *image.RGBA {
	fb := s.Framebuffer()
	img := image.NewRGBA(fb.Bounds())
	cols, rows := fb.Grid()
	pix := make([]byte, 4*fb.TileSize()*fb.TileSize())
	for i := range cols * rows {
		r, _ := fb.ReadTile(i, pix)
		src := &image.RGBA{Pix: pix, Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
		draw.Draw(img, r, src, image.Point{}, draw.Src)
	}
	return img
}

// next returns the next event within d, or nil if none came.
func next(t *testing.T, cl *client.Session, d time.Duration) client.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	ev, err := cl.Next(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil
	case err != nil:
		t.Fatalf("Next: %v", err)
	}
	return ev
}

// drain delivers events until none comes within d, and returns them.
func drain(t *testing.T, cl *client.Session, d time.Duration) []client.Event {
	t.Helper()
	var evs []client.Event
	for {
		ev := next(t, cl, d)
		if ev == nil {
			return evs
		}
		evs = append(evs, ev)
		if _, ok := ev.(client.End); ok {
			return evs
		}
	}
}

// collect delivers events for d, however many come, and returns them.
func collect(t *testing.T, cl *client.Session, d time.Duration) []client.Event {
	t.Helper()
	var evs []client.Event
	for end := time.Now().Add(d); time.Until(end) > 0; {
		ev := next(t, cl, time.Until(end))
		if ev == nil {
			break
		}
		evs = append(evs, ev)
		if _, ok := ev.(client.End); ok {
			break
		}
	}
	return evs
}

// last returns the last of evs, or nil if there is none.
func last(evs []client.Event) client.Event {
	if len(evs) == 0 {
		return nil
	}
	return evs[len(evs)-1]
}

func frames(evs []client.Event) []client.Frame {
	var fs []client.Frame
	for _, ev := range evs {
		if f, ok := ev.(client.Frame); ok {
			fs = append(fs, f)
		}
	}
	return fs
}

// pngCodec encodes and decodes tiles as PNG files, a plugged codec for
// the tests: the server's Encoder and the client's Decoder.
type pngCodec struct {
	mu      sync.Mutex
	decoded int
}

func (*pngCodec) Codec() protocol.Codec { return protocol.CodecPNG }

// Tile encodes every tile as PNG, none lossy.
func (*pngCodec) Tile(dst, pix []byte, w, h int, _ bool) ([]byte, protocol.Codec, bool) {
	var b bytes.Buffer
	img := &image.RGBA{Pix: pix[:4*w*h], Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return append(dst, b.Bytes()...), protocol.CodecPNG, false
}

// Lossy is never asked for, since Tile sends no tile lossy.
func (*pngCodec) Lossy(dst, _ []byte, _, _, _ int) ([]byte, protocol.Codec) { return dst, 0 }

// Still is never asked for either.
func (*pngCodec) Still(dst, _ []byte, _, _, _, _, _ int) ([]byte, protocol.Codec, int, bool) {
	return dst, 0, 0, false
}

// Cursor sends the cursor raw.
func (*pngCodec) Cursor(dst, pix []byte, w, h int) ([]byte, protocol.Codec, bool) {
	return append(dst, pix[:4*w*h]...), protocol.CodecRaw, true
}

// Decode checks the size the PNG declares before it decodes, as the
// Decoder contract asks, so that a header cannot make it allocate.
func (c *pngCodec) Decode(dst []byte, w, h int, payload []byte) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if cfg.Width != w || cfg.Height != h {
		return errors.New("png of another size")
	}
	img, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	draw.Draw(&image.RGBA{Pix: dst, Stride: 4 * w, Rect: img.Bounds()}, img.Bounds(), img, image.Point{}, draw.Src)
	c.mu.Lock()
	c.decoded++
	c.mu.Unlock()
	return nil
}

func (c *pngCodec) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decoded
}
