// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zeebo/xxh3"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// testLimits are what the tests' clients decode under: any message
// size, and any slot, which testSlots checks against what it holds.
var testLimits = protocol.Limits{MaxMessageSize: 1 << 30, CacheSlots: 1<<16 - 1}

// testSlots is a client's tile cache as the protocol has it, written
// apart from the server's mirror: a tile that names a slot is stored in
// it as it is read, and a cached tile draws what its slot holds. It
// counts the tiles stored, those stored in a slot that held one, and
// those drawn from a slot.
type testSlots struct {
	slots                    map[uint16]*image.RGBA
	stored, replaced, cached int
	// sameFrame counts cached tiles of a slot stored earlier in their
	// frame, which frame holds the slots stored in the frame so far.
	sameFrame int
	frame     map[uint16]bool
}

// endFrame ends a frame.
func (c *testSlots) endFrame() { clear(c.frame) }

// draw draws tile m onto img, through the cache.
func (c *testSlots) draw(img *image.RGBA, m *protocol.Tile) error {
	r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
	if m.Codec == protocol.CodecCached {
		src := c.slots[m.Slot]
		if src == nil || src.Rect.Size() != r.Size() {
			return fmt.Errorf("a cached %v tile from slot %d, which holds %v", r.Size(), m.Slot, src)
		}
		draw.Draw(img, r, src, image.Point{}, draw.Src)
		c.cached++
		if c.frame[m.Slot] {
			c.sameFrame++
		}
		return nil
	}
	if err := decodeTile(img.SubImage(r).(*image.RGBA), m.Codec, m.Payload); err != nil {
		return err
	}
	if m.Slot != 0 {
		t := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(t, t.Rect, img, r.Min, draw.Src)
		if c.slots == nil {
			c.slots, c.frame = map[uint16]*image.RGBA{}, map[uint16]bool{}
		}
		c.frame[m.Slot] = true
		if c.slots[m.Slot] != nil {
			c.replaced++
		}
		c.slots[m.Slot] = t
		c.stored++
	}
	return nil
}

// lruModel is a client's tile cache with the server's policy, written
// the slow and obvious way: the slots' content, and the slots from the
// least to the most recently used.
type lruModel struct {
	content map[uint16]cacheKey
	order   []uint16
	n       int
}

func (m *lruModel) use(s uint16) {
	m.order = slices.DeleteFunc(m.order, func(o uint16) bool { return o == s })
	m.order = append(m.order, s)
}

func (m *lruModel) lookup(k cacheKey) (uint16, bool) {
	for s, c := range m.content {
		if c == k {
			m.use(s)
			return s, true
		}
	}
	return 0, false
}

func (m *lruModel) victim() uint16 {
	if len(m.content) < m.n {
		return uint16(len(m.content) + 1) // #nosec G115 -- a test's few slots
	}
	return m.order[0]
}

// store stores k in s. Were k in another slot too, the newer serves it,
// and the older is never found again: its content is a key that no
// lookup asks for.
func (m *lruModel) store(s uint16, k cacheKey) {
	for p, c := range m.content {
		if c == k {
			m.content[p] = cacheKey{h: p}
		}
	}
	m.content[s] = k
	m.use(s)
}

func testKey(i int) cacheKey {
	return cacheKey{sum: xxh3.Uint128{Hi: uint64(i), Lo: uint64(i) * 7}, w: 64, h: 64} // #nosec G115 -- small
}

// The mirror picks the slots that a plain LRU picks, and finds what it
// holds, for the server's pattern of use: a lookup, then a store on a
// miss, and now and then a store of what a slot holds already.
func TestTileCacheModel(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 16} {
		for seed := range uint64(20) {
			rng := rand.New(rand.NewPCG(seed, uint64(n))) // #nosec G115 -- small
			c := newTileCache(n)
			m := &lruModel{content: map[uint16]cacheKey{}, n: n}
			universe := 2*n + 1
			for step := range 2000 {
				k := testKey(rng.IntN(universe))
				got, ok := c.lookup(k)
				want, wok := m.lookup(k)
				if got != want || ok != wok {
					t.Fatalf("n %d seed %d step %d: lookup %v = %d, %v; want %d, %v", n, seed, step, k.sum, got, ok, want, wok)
				}
				if ok && rng.IntN(20) != 0 {
					continue
				}
				// A miss is stored, and so, now and then, is a hit: the
				// same content in a second slot.
				s, ms := c.victim(), m.victim()
				if s != ms {
					t.Fatalf("n %d seed %d step %d: victim %d, want %d", n, seed, step, s, ms)
				}
				c.store(s, k)
				m.store(s, k)
			}
		}
	}
}

func TestTileCache(t *testing.T) {
	if newTileCache(0) != nil || newTileCache(-1) != nil {
		t.Error("a cache of no slots")
	}
	var none *tileCache
	if _, ok := none.key(make([]byte, 4), image.Rect(0, 0, 1, 1)); ok {
		t.Error("a key without a cache")
	}
	if c := newTileCache(1 << 20); len(c.slots) != 1<<16-1 {
		t.Errorf("%d slots, want at most 65535", len(c.slots))
	}

	c := newTileCache(3)
	pix := make([]byte, 4*protocol.MaxCachedPixels)
	for i := range pix {
		pix[i] = byte(i % 251)
	}
	// The same bytes at another size are another tile.
	a, ok := c.key(pix, image.Rect(0, 0, 32, 64))
	b, _ := c.key(pix, image.Rect(64, 0, 128, 32))
	if !ok || a.sum != b.sum || a == b {
		t.Errorf("keys %v and %v of the same 8 KiB", a, b)
	}
	if _, ok := c.key(make([]byte, 4*65*64), image.Rect(0, 0, 65, 64)); ok {
		t.Error("a key of more than MaxCachedPixels")
	}
	if k, ok := c.key(pix, image.Rect(0, 0, 64, 64)); !ok || k.w != 64 || k.h != 64 {
		t.Errorf("key of a whole tile: %v, %v", k, ok)
	}

	// Slots fill in order, then the least recently used goes; has does
	// not count as a use.
	for i, want := range []uint16{1, 2, 3} {
		if s := c.victim(); s != want {
			t.Fatalf("victim %d, want %d", s, want)
		}
		c.store(want, testKey(i))
	}
	if !c.has(testKey(0)) || c.has(testKey(9)) {
		t.Error("has")
	}
	if s := c.victim(); s != 1 {
		t.Errorf("victim %d after has, want 1", s)
	}
	if s, ok := c.lookup(testKey(0)); !ok || s != 1 {
		t.Errorf("lookup = %d, %v", s, ok)
	}
	if s := c.victim(); s != 2 {
		t.Errorf("victim %d after a lookup of slot 1, want 2", s)
	}
	c.store(2, testKey(3))
	if c.has(testKey(1)) {
		t.Error("slot 2's old tile is still found")
	}
	if s, ok := c.lookup(testKey(3)); !ok || s != 2 {
		t.Errorf("lookup of the new tile = %d, %v", s, ok)
	}
	if s := c.victim(); s != 3 {
		t.Errorf("victim %d, want 3", s)
	}
}

// TestTileCacheMemory measures the mirror of a client with the default
// slots, full.
func TestTileCacheMemory(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c := newTileCache(DefaultCacheSlots)
	for i := range DefaultCacheSlots {
		c.store(c.victim(), testKey(i))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("a full mirror of %d slots holds %.1f KiB", DefaultCacheSlots, float64(after.HeapAlloc-before.HeapAlloc)/1024)
	runtime.KeepAlive(c)
}

func BenchmarkTileKey(b *testing.B) {
	c := newTileCache(1)
	pix := make([]byte, 4*64*64)
	r := image.Rect(0, 0, 64, 64)
	b.SetBytes(int64(len(pix)))
	for b.Loop() {
		c.key(pix, r)
	}
}

// pattern returns tile pattern i at origin: stripes of two of eight
// colours, the same wherever the tile is, so that content repeats.
func pattern(i int, origin image.Point) *image.RGBA {
	palette := []color.RGBA{
		{0xff, 0xff, 0xff, 0xff}, {0x20, 0x20, 0x20, 0xff}, {0xd0, 0x30, 0x30, 0xff}, {0x30, 0xa0, 0x40, 0xff},
		{0x30, 0x50, 0xd0, 0xff}, {0xe0, 0xc0, 0x20, 0xff}, {0x90, 0x40, 0xb0, 0xff}, {0x40, 0xb0, 0xc0, 0xff},
	}
	img := image.NewRGBA(image.Rectangle{Min: origin, Max: origin.Add(image.Pt(64, 64))})
	a, b, width := palette[i%8], palette[(i+3)%8], 2+i%5
	for y := range 64 {
		for x := range 64 {
			c := a
			if i%2 == 0 && x/width%2 == 1 || i%2 == 1 && y/width%2 == 1 {
				c = b
			}
			img.SetRGBA(origin.X+x, origin.Y+y, c)
		}
	}
	return img
}

// The client's screen is the server's, byte for byte, after each change
// of tiles of a few patterns over a cache of four slots: through tiles
// stored and drawn again in one frame and across frames, slots replaced
// many times over, resizes to sizes whose edge tiles are smaller, and
// spells hidden.
func TestCacheConverges(t *testing.T) {
	sizes := []image.Point{{320, 192}, {256, 128}, {200, 150}, {96, 96}}
	for seed := range uint64(8) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rng := rand.New(rand.NewPCG(seed, 9))
				b := &tapped{Backend: backendtest.NewScreen(320, 192, 0)}
				hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{
					Codecs:     []protocol.Codec{protocol.CodecDeflate, protocol.CodecRaw, protocol.CodecCached},
					CacheSlots: 4,
				}}
				r := newRig(t, b, 50e6, time.Millisecond, hello)
				time.Sleep(100 * time.Millisecond)
				size := sizes[0]
				tiles := func(n int) {
					cols, rows := (size.X+63)/64, (size.Y+63)/64
					for range n {
						x, y := 64*rng.IntN(cols), 64*rng.IntN(rows)
						tr := image.Rect(x, y, x+64, y+64).Intersect(image.Rectangle{Max: size})
						paint(b, tr, pattern(rng.IntN(6), tr.Min))
					}
				}
				hiddenSpells := 0
				for step := range 40 {
					switch rng.IntN(10) {
					case 0:
						size = sizes[rng.IntN(len(sizes))]
						b.Sink().Resize(size.X, size.Y)
						tiles(12)
					case 1:
						r.cl.send(t, protocol.View{})
						time.Sleep(20 * time.Millisecond)
						tiles(4)
						time.Sleep(20 * time.Millisecond)
						r.cl.send(t, protocol.View{Visible: true})
						hiddenSpells++
					default:
						tiles(1 + rng.IntN(6))
					}
					time.Sleep(200 * time.Millisecond)
					synctest.Wait()
					want, got := serverScreen(r.s), r.cl.screen()
					if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
						t.Fatalf("step %d: the client's screen %v differs from the server's %v", step, got.Rect, want.Rect)
					}
				}
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				if len(r.cl.errs) > 0 {
					t.Fatal(r.cl.errs[0])
				}
				c := r.cl.cache
				t.Logf("%d tiles, %d stored, %d in a slot that held one, %d cached, %d of a slot stored in their frame; %d hidden spells",
					r.cl.tiles, c.stored, c.replaced, c.cached, c.sameFrame, hiddenSpells)
				if c.cached == 0 || c.replaced == 0 || c.sameFrame == 0 {
					t.Error("the cache was not put to the test")
				}
				if r.cl.codecs[protocol.CodecCached] != c.cached {
					t.Errorf("%d cached tiles counted, %d drawn", r.cl.codecs[protocol.CodecCached], c.cached)
				}
			})
		})
	}
}
