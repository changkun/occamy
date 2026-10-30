// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// slowClose is a client connection whose Close takes a while, as
// coder/websocket's close handshake does, while what the server sent
// before it saw the client's close still reaches a read: first at once,
// late once the test lets it.
type slowClose struct {
	mu          sync.Mutex
	first, late [][]byte
	lateGo      chan struct{}
	closeGo     chan struct{}
	done        chan struct{}
	once        sync.Once
}

func (c *slowClose) pop(q *[][]byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(*q) == 0 {
		return nil
	}
	m := (*q)[0]
	*q = (*q)[1:]
	return m
}

func (c *slowClose) ReadMessage(ctx context.Context) ([]byte, error) {
	if m := c.pop(&c.first); m != nil {
		return m, nil
	}
	select {
	case <-c.lateGo:
		if m := c.pop(&c.late); m != nil {
			return m, nil
		}
	case <-ctx.Done():
	case <-c.done:
	}
	select {
	case <-ctx.Done():
	case <-c.done:
	}
	return nil, errors.New("closed")
}

func (c *slowClose) WriteMessage(context.Context, []byte) error { return nil }

func (c *slowClose) Close(protocol.CloseCode, string) error {
	<-c.closeGo // the close handshake
	c.once.Do(func() { close(c.done) })
	return nil
}

// Once Next delivered End it returns ErrClosed, even for what the
// server sent before it read the client's close.
func TestNothingAfterEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := graphical
		w.Capabilities.Codecs = []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw}
		c := &slowClose{
			first: [][]byte{
				w.Append(nil),
				(&protocol.Resize{Width: 8, Height: 8}).Append(nil),
				(&protocol.Tile{FrameID: 1, W: 8, H: 8, Codec: protocol.CodecPNG, Payload: []byte("not a png")}).Append(nil),
				(&protocol.FrameEnd{FrameID: 1}).Append(nil),
			},
			late: [][]byte{
				(&protocol.Title{Title: "late"}).Append(nil),
				(&protocol.Resize{Width: 4, Height: 4}).Append(nil),
				(&protocol.FrameEnd{FrameID: 2}).Append(nil),
			},
			lateGo: make(chan struct{}), closeGo: make(chan struct{}), done: make(chan struct{}),
		}
		cl, err := client.New(context.Background(), c, client.Options{Decoders: []client.Decoder{&pngCodec{}}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(c.closeGo)
			cl.Close()
		}()
		evs := drain(t, cl, time.Second) // the tile does not decode
		if e, ok := last(evs).(client.End); !ok || e.Code != protocol.CloseProtocolError {
			t.Fatalf("events %#v, want End protocol-error last", evs)
		}
		close(c.lateGo) // what the server sent before it saw the close
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if ev, err := cl.Next(ctx); !errors.Is(err, client.ErrClosed) {
			t.Errorf("after End, Next returned %#v, %v, want ErrClosed", ev, err)
		}
	})
}

// Close sends the input queued so far before close, even when the
// network takes it only after Close was called.
func TestCloseFlushesInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPipe(0)
		srv := p.server()
		_ = srv.WriteMessage(context.Background(), graphical.Append(nil))
		stall := &stalled{Conn: p.client(), release: make(chan struct{})}
		cl, err := client.New(context.Background(), stall, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer p.close()
		ctx := context.Background()
		for i := range 3 {
			if _, err := cl.Key(ctx, true, uint32('a'+i), 0); err != nil {
				t.Fatal(err)
			}
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			close(stall.release)
		}()
		cl.Close()
		var got []protocol.Message
		for len(got) < 10 {
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			b, err := srv.ReadMessage(ctx)
			cancel()
			if err != nil {
				break
			}
			m, err := protocol.Decode(b, protocol.ClientMessages(), protocol.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, m)
		}
		want := []protocol.Message{
			&protocol.Key{Seq: 1, Down: true, Keysym: 'a'},
			&protocol.Key{Seq: 2, Down: true, Keysym: 'b'},
			&protocol.Key{Seq: 3, Down: true, Keysym: 'c'},
			&protocol.Close{Code: protocol.CloseNormal},
		}
		if len(got) != len(want) {
			t.Fatalf("the server got %#v, want %#v", got, want)
		}
		for i := range want {
			if string(got[i].Append(nil)) != string(want[i].Append(nil)) {
				t.Errorf("message %d: %#v, want %#v", i, got[i], want[i])
			}
		}
	})
}
