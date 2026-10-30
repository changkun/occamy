// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/protocol"
)

// blockDecoder decodes a tile once the test releases it, and counts
// what it decoded.
type blockDecoder struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func newBlockDecoder() *blockDecoder {
	return &blockDecoder{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (*blockDecoder) Codec() protocol.Codec { return protocol.CodecPNG }

func (d *blockDecoder) Decode([]byte, int, int, []byte) error {
	d.started <- struct{}{}
	<-d.release
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
	return nil
}

func (d *blockDecoder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func newJob(d Decoder) *job {
	return &job{dec: d, payload: []byte{1}, w: 1, h: 1, done: make(chan struct{})}
}

// Close drops the tiles queued for decoding rather than decode them.
func TestPoolAbort(t *testing.T) {
	abort := make(chan struct{})
	var wg sync.WaitGroup
	p := newPool(1, abort, &wg)
	d := newBlockDecoder()
	jobs := []*job{newJob(d), newJob(d), newJob(d)}
	for _, j := range jobs {
		p.submit(j)
	}
	<-d.started // the worker decodes the first
	close(abort)
	p.close()
	close(d.release)
	wg.Wait()
	if n := d.count(); n != 1 {
		t.Errorf("%d tiles decoded after Close, want only the one being decoded", n)
	}
	for _, j := range jobs[1:] {
		<-j.done
		if !errors.Is(j.err, ErrClosed) {
			t.Errorf("a dropped tile's error is %v", j.err)
		}
	}
}

// A tile submitted once the pool is closing, whose workers may be gone,
// fails at once rather than wait forever.
func TestPoolSubmitAfterClose(t *testing.T) {
	var wg sync.WaitGroup
	p := newPool(1, make(chan struct{}), &wg)
	p.close()
	wg.Wait()
	j := newJob(fuzzDecoder{})
	p.submit(j)
	select {
	case <-j.done:
	default:
		t.Fatal("a tile submitted after close is never decoded")
	}
	if !errors.Is(j.err, ErrClosed) {
		t.Errorf("error %v, want ErrClosed", j.err)
	}
}

// pngWelcome is the welcome of a graphical session with the plugged
// codec and raw.
func pngWelcome(window uint8) *protocol.Welcome {
	return &protocol.Welcome{
		ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical,
		Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw}, FrameWindow: window},
	}
}

func encode(ms ...protocol.Message) [][]byte {
	var b [][]byte
	for _, m := range ms {
		b = append(b, m.Append(nil))
	}
	return b
}

// Next waits for a frame's tiles to decode only until its context is
// done or the session is closed.
func TestDecodeWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newBlockDecoder()
		c := &fuzzConn{msgs: encode(
			pngWelcome(2),
			&protocol.Resize{Width: 8, Height: 8},
			&protocol.Tile{FrameID: 1, W: 8, H: 8, Codec: protocol.CodecPNG, Payload: []byte{1}},
			&protocol.FrameEnd{FrameID: 1},
		)}
		s, err := New(context.Background(), c, Options{Decoders: []Decoder{d}, DecodeWorkers: 1})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if ev, err := s.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Next returned %#v, %v while a tile decodes, want the context's error", ev, err)
		}
		got := make(chan Event)
		go func() {
			ev, _ := s.Next(context.Background())
			got <- ev
		}()
		synctest.Wait()
		closed := make(chan struct{})
		go func() {
			s.Close()
			close(closed)
		}()
		// Next returns while the decoder still holds the tile.
		if e, ok := (<-got).(End); !ok || e.Code != protocol.CloseNormal {
			t.Errorf("Next returned %#v after Close, want End", e)
		}
		close(d.release)
		<-closed
	})
}

// Once End was delivered and the decoders exited, Next returns
// ErrClosed at once, whatever the server sent meanwhile.
func TestNextAfterPoolClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		late := make(chan struct{})
		c := &gatedConn{
			first: encode(
				pngWelcome(2),
				&protocol.Resize{Width: 8, Height: 8},
				&protocol.Tile{FrameID: 1, W: 4, H: 4, Codec: protocol.CodecPNG}, // fuzzDecoder fails on empty
				&protocol.FrameEnd{FrameID: 1},
			),
			late: encode(
				&protocol.Tile{FrameID: 2, W: 4, H: 4, Codec: protocol.CodecPNG, Payload: []byte{7}},
				&protocol.FrameEnd{FrameID: 2},
			),
			gate: late,
		}
		s, err := New(context.Background(), c, Options{Decoders: []Decoder{fuzzDecoder{}}, DecodeWorkers: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if ev, err := s.Next(ctx); err != nil {
			t.Fatal(err)
		} else if e, ok := ev.(End); !ok || e.Code != protocol.CloseProtocolError {
			t.Fatalf("event %#v, want End protocol-error", ev)
		}
		synctest.Wait() // the session's end closed the pool, and its worker exited
		close(late)
		synctest.Wait()
		if ev, err := s.Next(ctx); !errors.Is(err, ErrClosed) {
			t.Errorf("after End, Next returned %#v, %v", ev, err)
		}
	})
}

// gatedConn returns first at once, then late once gate is closed,
// whatever its context, as a connection returns what it had received.
type gatedConn struct {
	mu          sync.Mutex
	first, late [][]byte
	gate        chan struct{}
}

func (c *gatedConn) ReadMessage(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if len(c.first) > 0 {
		m := c.first[0]
		c.first = c.first[1:]
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()
	<-c.gate
	c.mu.Lock()
	if len(c.late) > 0 {
		m := c.late[0]
		c.late = c.late[1:]
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *gatedConn) WriteMessage(context.Context, []byte) error { return nil }

// Sequence numbers wrap from 2³²−1 to 1, so that 0 always means no
// input, and frame IDs from 2³²−1 to 0.
func TestWrap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &fuzzConn{msgs: encode(
			&protocol.Resize{Width: 8, Height: 8},
			&protocol.FrameEnd{FrameID: 0},
			&protocol.FrameEnd{FrameID: 1},
		)}
		s := newSession(c, Options{}, *pngWelcome(2))
		s.lastEnd = math.MaxUint32
		s.out.seq = math.MaxUint32 - 1
		s.start()
		defer s.Close()
		ctx := context.Background()
		for _, want := range []uint32{math.MaxUint32, 1, 2} {
			if seq, err := s.Key(ctx, true, 'a', 0); err != nil || seq != want {
				t.Errorf("Key returned %d, %v, want %d", seq, err, want)
			}
		}
		var ids []uint32
		for len(ids) < 2 {
			ev, err := s.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch ev := ev.(type) {
			case Frame:
				ids = append(ids, ev.ID)
			case End:
				t.Fatalf("frames %v, then %+v", ids, ev)
			}
		}
		if ids[0] != 0 || ids[1] != 1 {
			t.Errorf("frames %v after frame 2³²−1, want 0 and 1", ids)
		}
	})
}

// lowBacklog lowers minBacklog for the test, so that the limits that
// follow from the screen's size apply to small screens.
func lowBacklog(t *testing.T) {
	old := minBacklog
	minBacklog = 0
	t.Cleanup(func() { minBacklog = old })
}

// endOf reads s's events until End, and returns it.
func endOf(t *testing.T, s *Session) End {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := ev.(End); ok {
			return e
		}
	}
}

// A tile of a plugged codec holds its decoded pixels until it is
// delivered, whatever its payload's size: a server that sends small
// payloads of large tiles beyond the frame window is cut off.
func TestDecodedBacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lowBacklog(t)
		const side = 256
		ms := []protocol.Message{pngWelcome(1), &protocol.Resize{Width: side, Height: side}}
		for id := uint32(1); id <= 2; id++ {
			for range 7 {
				ms = append(ms, &protocol.Tile{FrameID: id, W: side, H: side, Codec: protocol.CodecPNG, Payload: []byte{7}})
			}
			ms = append(ms, &protocol.FrameEnd{FrameID: id})
		}
		c := &fuzzConn{msgs: encode(ms...)}
		s, err := New(context.Background(), c, Options{Decoders: []Decoder{fuzzDecoder{}}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait() // read everything before Next delivers anything
		e := endOf(t, s)
		if e.Code != protocol.CloseProtocolError || e.Err == nil || !strings.Contains(e.Err.Error(), "undelivered") {
			t.Errorf("End %+v, want a protocol error for the backlog", e)
		}
	})
}

// A cursor of a plugged codec is checked against the backlog before its
// pixels are allocated: a few bytes may declare 4096×4096 of them.
func TestCursorBacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lowBacklog(t)
		c := &fuzzConn{msgs: encode(
			pngWelcome(2),
			&protocol.Resize{Width: 64, Height: 64},
			&protocol.Cursor{W: 32, H: 32, Codec: protocol.CodecPNG, Payload: []byte{7}},
			&protocol.Cursor{W: 4096, H: 304, Codec: protocol.CodecPNG, Payload: []byte{7}},
		)}
		s, err := New(context.Background(), c, Options{Decoders: []Decoder{fuzzDecoder{}}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait() // read everything before Next delivers anything
		s.mu.Lock()
		backlog, limit := s.backlog, s.maxBacklog()
		s.mu.Unlock()
		if backlog > limit {
			t.Errorf("backlog %d, limit %d", backlog, limit)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var evs []Event
		for {
			ev, err := s.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, ev)
			if _, ok := ev.(End); ok {
				break
			}
		}
		if cur, ok := evs[0].(Cursor); !ok || cur.Image.Rect.Dx() != 32 {
			t.Errorf("first event %#v, want the small cursor", evs[0])
		}
		if e := evs[len(evs)-1].(End); e.Code != protocol.CloseProtocolError || !strings.Contains(e.Err.Error(), "cursor") {
			t.Errorf("End %+v, want a protocol error for the cursor", e)
		}
	})
}

// A frame that draws a large screen and then shrinks it still holds
// what it decodes for the large one, and is measured against it.
func TestShrinkWithinFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lowBacklog(t)
		ms := []protocol.Message{pngWelcome(2), &protocol.Resize{Width: 256, Height: 256}}
		for range 3 {
			ms = append(ms, &protocol.Tile{FrameID: 1, W: 256, H: 256, Codec: protocol.CodecPNG, Payload: []byte{7}})
		}
		ms = append(ms, &protocol.Resize{Width: 16, Height: 16}, &protocol.FrameEnd{FrameID: 1})
		s, err := New(context.Background(), &fuzzConn{msgs: encode(ms...), eof: true}, Options{Decoders: []Decoder{fuzzDecoder{}}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		if e := endOf(t, s); e.Code == protocol.CloseProtocolError {
			t.Errorf("End %+v, want the connection's end", e)
		}
	})
}

// A frame may resize, redraw, scroll and redraw again, and no more than
// eight screens' worth.
func TestFrameWorkLimit(t *testing.T) {
	const side = 256
	full := func() protocol.Message {
		return &protocol.Tile{FrameID: 1, W: side, H: side, Codec: protocol.CodecRaw, Payload: make([]byte, 4*side*side)}
	}
	heavy := []protocol.Message{
		&protocol.Resize{Width: side, Height: side}, full(),
		&protocol.CopyRect{FrameID: 1, SrcY: 16, W: side, H: side - 16},
		&protocol.Tile{FrameID: 1, Y: side - 16, W: side, H: 16, Codec: protocol.CodecRaw, Payload: make([]byte, 4*side*16)},
		full(), full(),
	}
	tooMuch := []protocol.Message{&protocol.Resize{Width: side, Height: side}}
	for range 8 {
		tooMuch = append(tooMuch, full())
	}
	for _, c := range []struct {
		name string
		ms   []protocol.Message
		ok   bool
	}{{"heavy", heavy, true}, {"nine screens", tooMuch, false}} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lowBacklog(t)
				ms := append([]protocol.Message{pngWelcome(2)}, c.ms...)
				ms = append(ms, &protocol.FrameEnd{FrameID: 1})
				s, err := New(context.Background(), &fuzzConn{msgs: encode(ms...), eof: true}, Options{Decoders: []Decoder{fuzzDecoder{}}})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				var evs []Event
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				for {
					ev, err := s.Next(ctx)
					if err != nil {
						t.Fatal(err)
					}
					evs = append(evs, ev)
					if _, ok := ev.(End); ok {
						break
					}
				}
				e := evs[len(evs)-1].(End)
				if c.ok && (len(evs) != 3 || e.Code == protocol.CloseProtocolError) {
					t.Errorf("events %#v, want the frame and the connection's end", evs)
				}
				if !c.ok && (e.Code != protocol.CloseProtocolError || !strings.Contains(e.Err.Error(), "writes")) {
					t.Errorf("End %+v, want a protocol error for the frame's work", e)
				}
			})
		})
	}
}
