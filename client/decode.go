// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"errors"
	"fmt"
	"runtime"
	"sync"

	"changkun.de/x/occamy/protocol"
)

// A Decoder decodes the tile and cursor payloads of one codec. The
// graphics pipeline's codecs plug in here; raw is built in. Decode is
// called from several goroutines at once.
type Decoder interface {
	// Codec is the codec the Decoder decodes.
	Codec() protocol.Codec
	// Decode decodes payload, an image of w×h pixels, into dst, which
	// holds w×h×4 bytes: rows from top to bottom without padding, four
	// bytes a pixel in raw's channel order. It returns an error if
	// payload is not such an image, which ends the session with
	// protocol-error. It must not keep payload or dst.
	Decode(dst []byte, w, h int, payload []byte) error
}

// errDecode wraps a decoder's error, which is the server's protocol
// error.
var errDecode = fmt.Errorf("%w: a payload that does not decode", protocol.ErrInvalid)

// raw is the raw codec: the payload is the pixels. Tiles never go
// through it, since applying a raw tile copies its payload directly; it
// decodes cursors.
type raw struct{}

func (raw) Codec() protocol.Codec { return protocol.CodecRaw }

func (raw) Decode(dst []byte, w, h int, payload []byte) error {
	if len(payload) != 4*w*h || len(dst) != 4*w*h {
		return errors.New("client: raw payload of the wrong size")
	}
	copy(dst, payload)
	return nil
}

// job is a tile to decode. done is closed once pix holds its pixels or
// err says why not.
type job struct {
	dec     Decoder
	payload []byte
	w, h    int
	pix     []byte
	err     error
	done    chan struct{}
}

// pool decodes tiles of plugged codecs as they arrive, in parallel, so
// that a frame is decoded by the time its frame-end arrives. Raw tiles
// do not go through it: copying them is all their decoding, and costs
// less than handing them to another goroutine.
type pool struct {
	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []*job
	closing bool
	// abort is closed when the caller closes the session: queued jobs
	// are then dropped rather than decoded. When the server ends it, the
	// queued jobs are decoded, since Next still delivers their frames.
	abort <-chan struct{}
}

func newPool(workers int, abort <-chan struct{}, wg *sync.WaitGroup) *pool {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	p := &pool{abort: abort}
	p.cond = sync.NewCond(&p.mu)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			p.work()
		}()
	}
	return p
}

// submit queues j.
func (p *pool) submit(j *job) {
	p.mu.Lock()
	p.jobs = append(p.jobs, j)
	p.mu.Unlock()
	p.cond.Signal()
}

// close lets the workers exit once the queued jobs are done.
func (p *pool) close() {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	p.cond.Broadcast()
}

func (p *pool) work() {
	for {
		p.mu.Lock()
		for len(p.jobs) == 0 && !p.closing {
			p.cond.Wait()
		}
		if len(p.jobs) == 0 {
			p.mu.Unlock()
			return
		}
		j := p.jobs[0]
		p.jobs[0] = nil
		p.jobs = p.jobs[1:]
		p.mu.Unlock()
		j.run(p.abort)
	}
}

func (j *job) run(abort <-chan struct{}) {
	defer close(j.done)
	select {
	case <-abort:
		j.err = ErrClosed
		return
	default:
	}
	j.pix = make([]byte, 4*j.w*j.h)
	if err := j.dec.Decode(j.pix, j.w, j.h, j.payload); err != nil {
		j.err = fmt.Errorf("%w: %v tile: %w", errDecode, j.dec.Codec(), err)
	}
	j.payload = nil
}
