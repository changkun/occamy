// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math/rand/v2"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// deflateDecoder decodes occamyd's deflate tiles, for the tests: the Go
// client has no decoder of its own but raw.
type deflateDecoder struct{}

func (deflateDecoder) Codec() protocol.Codec { return protocol.CodecDeflate }

func (deflateDecoder) Decode(dst []byte, w, h int, payload []byte) error {
	var in codec.Inflater
	return in.Decode(&image.RGBA{Pix: dst, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}, payload)
}

// The client's tile cache keeps in step with occamyd's mirror of it: it
// replays protocol/testdata/cache.json, which internal/stream records,
// and shows the server's screen after each frame.
func TestCacheGolden(t *testing.T) {
	b, err := os.ReadFile("../protocol/testdata/cache.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Welcome string
		Frames  []struct {
			Name          string
			Messages      []string
			Width, Height int
			Screen        string
		}
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		opts := client.Options{Decoders: []client.Decoder{deflateDecoder{}}, CacheSlots: 4}
		sc, err := startScripted(t, opts, rawMessage(mustHex(t, g.Welcome)))
		if err != nil {
			t.Fatal(err)
		}
		if c := sc.cl.Capabilities(); c.CacheSlots != 4 {
			t.Fatalf("agreed %+v", c)
		}
		for _, f := range g.Frames {
			for _, m := range f.Messages {
				sc.sendBytes(mustHex(t, m))
			}
			for {
				ev := next(t, sc.cl, time.Second)
				if ev == nil {
					t.Fatalf("%s: no frame", f.Name)
				}
				if e, ok := ev.(client.End); ok {
					t.Fatalf("%s: the session ended: %v", f.Name, e.Err)
				}
				if _, ok := ev.(client.Frame); ok {
					break
				}
			}
			img := sc.cl.Screenshot()
			sum := sha256.Sum256(img.Pix)
			if img.Rect != image.Rect(0, 0, f.Width, f.Height) || hex.EncodeToString(sum[:]) != f.Screen {
				t.Errorf("%s: the screen, %v, is not the server's", f.Name, img.Rect)
			}
		}
	})
}

// solid is a w×h tile of one colour.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

// A tile stored in a slot is drawn again from it: later in its frame,
// in later frames, decoded or raw, and after a resize that dropped the
// frame it came in. A cached tile of a slot that holds no tile of its
// size ends the session.
func TestCacheSlots(t *testing.T) {
	red, green, blue := color.RGBA{0xff, 0, 0, 0xff}, color.RGBA{0, 0xff, 0, 0xff}, color.RGBA{0, 0, 0xff, 0xff}
	w := graphical
	w.Capabilities.Codecs = []protocol.Codec{protocol.CodecPNG, protocol.CodecRaw, protocol.CodecCached}
	w.Capabilities.CacheSlots = 4
	codec := &pngCodec{}
	opts := client.Options{Decoders: []client.Decoder{codec}, CacheSlots: 4}
	pngTile := func(id uint32, x, y int, img *image.RGBA, slot uint16) *protocol.Tile {
		payload, _, _ := codec.Tile(nil, img.Pix, img.Rect.Dx(), img.Rect.Dy(), false)
		return &protocol.Tile{FrameID: id, X: uint16(x), Y: uint16(y), W: uint16(img.Rect.Dx()), H: uint16(img.Rect.Dy()), // #nosec G115 -- small
			Codec: protocol.CodecPNG, Slot: slot, Payload: payload}
	}
	raw := func(id uint32, x, y int, img *image.RGBA, slot uint16) *protocol.Tile {
		m := rawTile(id, img, img.Rect)
		m.X, m.Y, m.Slot = uint16(x), uint16(y), slot // #nosec G115 -- small
		return m
	}
	cached := func(id uint32, x, y, w, h int, slot uint16) *protocol.Tile {
		return &protocol.Tile{FrameID: id, X: uint16(x), Y: uint16(y), W: uint16(w), H: uint16(h), Codec: protocol.CodecCached, Slot: slot} // #nosec G115 -- small
	}
	frames := func(sc *scripted, n int) {
		t.Helper()
		for got := 0; got < n; {
			switch ev := next(t, sc.cl, time.Second).(type) {
			case nil:
				t.Fatalf("%d frames of %d", got, n)
			case client.End:
				t.Fatalf("the session ended: %v", ev.Err)
			case client.Frame:
				got++
			}
		}
	}

	synctest.Test(t, func(t *testing.T) {
		sc := newScripted(t, w, opts)
		want := image.NewRGBA(image.Rect(0, 0, 8, 8))
		put := func(x, y int, c color.RGBA, n int) {
			draw.Draw(want, image.Rect(x, y, x+n, y+n), image.NewUniform(c), image.Point{}, draw.Src)
		}
		// Stored, then drawn again in the same frame: a raw tile, and one
		// still being decoded.
		sc.frame(1, &protocol.Resize{Width: 8, Height: 8},
			raw(1, 0, 0, solid(4, 4, red), 1), pngTile(1, 4, 0, solid(4, 4, green), 2),
			cached(1, 0, 4, 4, 4, 1), cached(1, 4, 4, 4, 4, 2))
		put(0, 0, red, 4)
		put(4, 0, green, 4)
		put(0, 4, red, 4)
		put(4, 4, green, 4)
		frames(sc, 1)
		if got := sc.cl.Screenshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Fatal("frame 1 differs from the model")
		}
		// A frame that a resize drops still stores its tiles, and the
		// slots outlive the resize.
		sc.send(raw(2, 0, 0, solid(4, 4, blue), 3), &protocol.Resize{Width: 8, Height: 8})
		sc.frame(2, cached(2, 0, 0, 4, 4, 3), cached(2, 4, 0, 4, 4, 1), cached(2, 0, 4, 4, 4, 2), cached(2, 4, 4, 4, 4, 3))
		want = image.NewRGBA(want.Rect)
		put(0, 0, blue, 4)
		put(4, 0, red, 4)
		put(0, 4, green, 4)
		put(4, 4, blue, 4)
		frames(sc, 1)
		if got := sc.cl.Screenshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Fatal("frame 2 differs from the model")
		}
		// A slot stored again holds the newer tile.
		sc.frame(3, raw(3, 0, 0, solid(2, 2, green), 1), cached(3, 6, 6, 2, 2, 1))
		put(0, 0, green, 2)
		put(6, 6, green, 2)
		frames(sc, 1)
		if got := sc.cl.Screenshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Fatal("frame 3 differs from the model")
		}
	})

	for _, c := range []struct {
		name string
		tile *protocol.Tile
	}{
		{"an empty slot", cached(1, 0, 0, 4, 4, 2)},
		{"a slot of another size", cached(1, 0, 0, 4, 2, 1)},
		{"a slot of the transposed size", cached(1, 0, 0, 2, 8, 3)},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newScripted(t, w, opts)
				sc.send(&protocol.Resize{Width: 8, Height: 8}, raw(1, 0, 0, solid(4, 4, red), 1), raw(1, 0, 0, solid(8, 2, red), 3), c.tile)
				evs := drain(t, sc.cl, time.Second)
				e, ok := last(evs).(client.End)
				if !ok || !errors.Is(e.Err, protocol.ErrInvalid) || e.Code != protocol.CloseProtocolError {
					t.Fatalf("events %#v, want an End for a protocol error", evs)
				}
			})
		})
	}
}

// pattern paints tile pattern i at r: stripes of two of eight colours,
// the same wherever the tile is, so that content repeats.
func pattern(i int, r image.Rectangle) *image.RGBA {
	palette := []color.RGBA{
		{0xff, 0xff, 0xff, 0xff}, {0x20, 0x20, 0x20, 0xff}, {0xd0, 0x30, 0x30, 0xff}, {0x30, 0xa0, 0x40, 0xff},
		{0x30, 0x50, 0xd0, 0xff}, {0xe0, 0xc0, 0x20, 0xff}, {0x90, 0x40, 0xb0, 0xff}, {0x40, 0xb0, 0xc0, 0xff},
	}
	img := image.NewRGBA(r)
	a, b, width := palette[i%8], palette[(i+3)%8], 2+i%5
	for y := range r.Dy() {
		for x := range r.Dx() {
			c := a
			if i%2 == 0 && x/width%2 == 1 || i%2 == 1 && y/width%2 == 1 {
				c = b
			}
			img.SetRGBA(r.Min.X+x, r.Min.Y+y, c)
		}
	}
	return img
}

// counting counts the tiles occamyd stores in a slot and draws from one.
type counting struct {
	stream.Conn
	mu             sync.Mutex
	stored, cached int
}

func (c *counting) WriteMessage(ctx context.Context, p []byte) error {
	if m, err := protocol.Decode(p, protocol.ServerMessages(), protocol.Limits{MaxMessageSize: 1 << 30, CacheSlots: 1<<16 - 1}); err == nil {
		if m, ok := m.(*protocol.Tile); ok && m.Slot != 0 {
			c.mu.Lock()
			if m.Codec == protocol.CodecCached {
				c.cached++
			} else {
				c.stored++
			}
			c.mu.Unlock()
		}
	}
	return c.Conn.WriteMessage(ctx, p)
}

// The client's screen is occamyd's, byte for byte, after each change of
// tiles of a few patterns over a cache of four slots, through resizes
// and spells hidden.
func TestCacheConverges(t *testing.T) {
	sizes := []image.Point{{320, 192}, {256, 128}, {200, 150}}
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				b := tap(backendtest.NewScreen(320, 192, 0))
				s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: 64})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				p := newPipe(time.Millisecond)
				defer p.close()
				conn := &counting{Conn: p.server()}
				opts := client.Options{CacheSlots: 4}
				served := make(chan error, 1)
				go func() { served <- stream.Serve(ctx, conn, owner, opts.Hello(), stream.Config{SessionID: "s1"}) }()
				cl, err := client.New(ctx, p.client(), opts)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					cl.Close()
					cancel()
					<-served
				}()

				rng := rand.New(rand.NewPCG(seed, 5))
				size := sizes[0]
				paint := func(n int) {
					sink := b.Sink()
					for range n {
						x, y := 64*rng.IntN((size.X+63)/64), 64*rng.IntN((size.Y+63)/64)
						r := image.Rect(x, y, x+64, y+64).Intersect(image.Rectangle{Max: size})
						img := sink.Lock(r)
						draw.Draw(img, r, pattern(rng.IntN(6), r), r.Min, draw.Src)
						sink.Unlock(r)
					}
				}
				drain(t, cl, 100*time.Millisecond)
				for step := range 30 {
					switch rng.IntN(10) {
					case 0:
						size = sizes[rng.IntN(len(sizes))]
						b.Sink().Resize(size.X, size.Y)
						paint(12)
					case 1:
						if err := cl.View(ctx, false, image.Rectangle{}); err != nil {
							t.Fatal(err)
						}
						drain(t, cl, 20*time.Millisecond)
						paint(4)
						if err := cl.View(ctx, true, image.Rectangle{}); err != nil {
							t.Fatal(err)
						}
					default:
						paint(1 + rng.IntN(6))
					}
					if e, ok := last(drain(t, cl, 200*time.Millisecond)).(client.End); ok {
						t.Fatalf("step %d: the session ended: %v", step, e.Err)
					}
					want, got := serverScreen(s), cl.Screenshot()
					if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
						t.Fatalf("step %d: the client's screen %v differs from the server's %v", step, got.Rect, want.Rect)
					}
				}
				conn.mu.Lock()
				defer conn.mu.Unlock()
				t.Logf("%d tiles stored, %d drawn from a slot", conn.stored, conn.cached)
				if conn.cached == 0 || conn.stored <= 4 {
					t.Error("the cache was not put to the test")
				}
			})
		})
	}
}
