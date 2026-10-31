// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/protocol"
)

// fuzzConn plays a hostile server: the messages it was given, then the
// end of the connection or silence.
type fuzzConn struct {
	msgs [][]byte
	eof  bool
}

func (c *fuzzConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if len(c.msgs) > 0 {
		m := c.msgs[0]
		c.msgs = c.msgs[1:]
		return m, nil
	}
	if c.eof {
		return nil, io.EOF
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *fuzzConn) WriteMessage(context.Context, []byte) error { return nil }

// fuzzDecoder decodes any payload but an empty one into its first byte.
type fuzzDecoder struct{}

func (fuzzDecoder) Codec() protocol.Codec { return protocol.CodecPNG }

func (fuzzDecoder) Decode(dst []byte, _, _ int, payload []byte) error {
	if len(payload) == 0 {
		return errors.New("empty")
	}
	for i := range dst {
		dst[i] = payload[0]
	}
	return nil
}

// fuzzMessages splits data into messages, each after its u16 length.
func fuzzMessages(data []byte) [][]byte {
	var msgs [][]byte
	for len(data) >= 2 {
		n := int(binary.LittleEndian.Uint16(data))
		data = data[2:]
		n = min(n, len(data))
		msgs = append(msgs, data[:n])
		data = data[n:]
	}
	return msgs
}

func fuzzSeed(ms ...protocol.Message) []byte {
	var b []byte
	for _, m := range ms {
		e := m.Append(nil)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(e))) // #nosec G115 -- small seeds
		b = append(b, e...)
	}
	return b
}

// fuzzPixels are the largest screens a fuzzed client takes, by mode.
var fuzzPixels = [4]int{16 * 16, 64 * 64, 256 * 256, 640 * 480}

// A server that sends anything at all neither panics the client nor
// grows its memory past its bounds, Next delivers nothing after End,
// and the session's goroutines exit.
func FuzzSession(f *testing.F) {
	// The limits that follow from the screen's size, not their floor.
	old := minBacklog
	minBacklog = 1 << 16
	f.Cleanup(func() { minBacklog = old })
	pix := make([]byte, 4*4*4)
	f.Add(byte(0), fuzzSeed(
		&protocol.Resize{Width: 16, Height: 16},
		&protocol.Tile{FrameID: 1, W: 4, H: 4, Codec: protocol.CodecRaw, Payload: pix},
		&protocol.Tile{FrameID: 1, X: 4, W: 4, H: 4, Codec: protocol.CodecPNG, Payload: []byte{7}},
		&protocol.CopyRect{FrameID: 1, SrcX: 0, DstX: 2, DstY: 2, W: 8, H: 8},
		&protocol.FrameEnd{FrameID: 1, LastSeq: 1},
		&protocol.Cursor{W: 2, H: 2, HotX: 1, Codec: protocol.CodecRaw, Payload: pix[:16]},
		&protocol.Cursor{W: 2, H: 2, Codec: protocol.CodecPNG, Payload: []byte{1}},
		&protocol.Title{Title: "t"}, &protocol.Bell{}, &protocol.Bell{}, &protocol.Bell{}, &protocol.Bell{},
		&protocol.ViewerJoined{ViewerID: 2, Role: protocol.RoleViewer}, &protocol.ViewerLeft{ViewerID: 2},
		&protocol.Ping{Nonce: 1}, &protocol.FrameEnd{FrameID: 2}, &protocol.FrameEnd{FrameID: 3},
		&protocol.Tile{FrameID: 4, W: 4, H: 4, Codec: protocol.CodecPNG},
		&protocol.FrameEnd{FrameID: 4},
	))
	f.Add(byte(1), fuzzSeed(
		&protocol.Resize{Width: 80, Height: 24},
		&protocol.TermSnapshot{Data: []byte("\x1b[H\x1b[2Jhi")},
		&protocol.TermData{Data: []byte("x")},
		&protocol.FrameEnd{FrameID: 1},
		&protocol.Prompt{ID: 1, Fields: []protocol.PromptField{{Label: "p", Secret: true}}},
		&protocol.Close{Code: protocol.CloseIdle},
	))
	f.Add(byte(2), fuzzSeed(&protocol.Resize{Width: 300, Height: 300}, &protocol.Welcome{Role: protocol.RoleOwner, Class: protocol.ClassGraphical}))
	f.Add(byte(8), fuzzSeed( // a frame of a large screen queued, then a small one
		&protocol.Resize{Width: 256, Height: 256},
		&protocol.Tile{FrameID: 1, W: 256, H: 256, Codec: protocol.CodecPNG, Payload: []byte{7}},
		&protocol.Tile{FrameID: 1, W: 256, H: 256, Codec: protocol.CodecPNG, Payload: []byte{8}},
		&protocol.FrameEnd{FrameID: 1},
		&protocol.Resize{Width: 16, Height: 16},
		&protocol.Resize{Width: 256, Height: 256},
		&protocol.CopyRect{FrameID: 2, SrcY: 1, W: 256, H: 255},
		&protocol.CopyRect{FrameID: 2, SrcY: 1, W: 256, H: 255},
		&protocol.FrameEnd{FrameID: 2},
		&protocol.Title{Title: "after"},
	))
	f.Fuzz(func(t *testing.T, mode byte, data []byte) {
		synctest.Test(t, func(t *testing.T) {
			w := protocol.Welcome{
				ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical,
				Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw}, Terminal: 1},
			}
			if mode&1 != 0 {
				w.Class = protocol.ClassTerminal
			}
			c := &fuzzConn{msgs: append([][]byte{w.Append(nil)}, fuzzMessages(data)...), eof: mode&2 != 0}
			maxPixels := fuzzPixels[mode>>2&3]
			s, err := New(context.Background(), c, Options{Decoders: []Decoder{fuzzDecoder{}}, MaxPixels: maxPixels, DecodeWorkers: 2})
			if err != nil {
				t.Fatal(err)
			}
			// Every screen is at most maxPixels, which bounds maxBacklog,
			// and the backlog passes it by one message at most: its
			// bytes, its op, and a tile's decoded pixels.
			limit := minBacklog
			if w.Class == protocol.ClassGraphical {
				limit = max(min((s.window+2)*3*4*maxPixels, 1<<30), minBacklog)
			}
			limit += s.limits.MaxMessageSize + opCost + 4*maxPixels
			check := func() {
				s.mu.Lock()
				backlog := s.backlog
				s.mu.Unlock()
				if backlog > limit {
					t.Fatalf("backlog %d, limit %d", backlog, limit)
				}
				s.Framebuffer(func(img *image.RGBA) {
					if len(img.Pix) > 4*maxPixels {
						t.Fatalf("a framebuffer of %v", img.Rect)
					}
				})
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ended := false
			for i := 0; !ended; i++ {
				check()
				ev, err := s.Next(ctx)
				if err != nil {
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("Next: %v", err)
					}
					break
				}
				_, ended = ev.(End)
				if i%3 == 0 {
					_, _ = s.Key(ctx, true, 'a', 0)
				}
			}
			check()
			s.Close()
			if !ended {
				if ev, err := s.Next(context.Background()); err != nil {
					t.Fatalf("after Close, Next: %v", err)
				} else if _, ok := ev.(End); !ok {
					t.Fatalf("after Close, Next returned %#v, want End", ev)
				}
			}
			for range 3 {
				if ev, err := s.Next(context.Background()); !errors.Is(err, ErrClosed) {
					t.Fatalf("after End, Next returned %#v, %v", ev, err)
				}
			}
			s.mu.Lock()
			frames, queued := s.frames, len(s.queue)
			s.mu.Unlock()
			if frames != 0 || queued != 0 {
				t.Fatalf("after End, %d frames and %d items queued", frames, queued)
			}
		})
	})
}
