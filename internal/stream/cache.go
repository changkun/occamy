// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"

	"github.com/zeebo/xxh3"

	"changkun.de/x/occamy/protocol"
)

// The tile cache (specs/008-native-architecture/graphics-pipeline.md,
// Implementation notes: Tile cache). A client that agreed to the cached
// codec and to cache slots keeps the pixels of each tile message that
// names a slot, in that slot, until another tile is stored there. The
// server decides what goes where: it mirrors each client's slots, with
// the content each holds, and sends a tile whose content a slot holds
// as a cached tile of that slot, a few bytes instead of its pixels. It
// stores only single tiles that it sends exactly, so that a slot holds
// what the server's screen held.

// DefaultCacheSlots is the cache slots the server offers unless
// Config.Capabilities states another number: up to 16 MiB of 64-pixel
// tiles on the client, and 70 KiB of mirror a viewer on the server.
const DefaultCacheSlots = 1024

// cacheKey is a tile's content: the xxh3-128 hash of its pixels and its
// size, since the same bytes at another size are another tile.
type cacheKey struct {
	sum  xxh3.Uint128
	w, h uint16
}

// tileCache mirrors a client's tile cache: the content of each slot, and
// the order in which they were last used, so that the slot used least
// recently is the next to be replaced. Slots are numbered from 1; 0
// links nothing. A tile is stored only when no slot holds its content,
// so each content is in one slot; were it in two, keys names the newer.
type tileCache struct {
	keys       map[cacheKey]uint16
	slots      []cacheSlot // slot s at s-1
	head, tail uint16      // the most and the least recently used
	used       int         // slots 1 to used hold a tile
}

type cacheSlot struct {
	key        cacheKey
	prev, next uint16 // more and less recently used
}

// newTileCache returns the mirror of a client with n slots, or nil for
// none.
func newTileCache(n int) *tileCache {
	if n <= 0 {
		return nil
	}
	n = min(n, 1<<16-1)
	return &tileCache{keys: make(map[cacheKey]uint16), slots: make([]cacheSlot, n)}
}

// key returns the key of pix, the pixels of r in RGBA order row by row,
// and false if r is too large for a slot.
func (c *tileCache) key(pix []byte, r image.Rectangle) (cacheKey, bool) {
	w, h := r.Dx(), r.Dy()
	if c == nil || w*h > protocol.MaxCachedPixels {
		return cacheKey{}, false
	}
	return cacheKey{sum: xxh3.Hash128(pix[:4*w*h]), w: uint16(w), h: uint16(h)}, true // #nosec G115 -- at most MaxCachedPixels
}

// has reports whether a slot holds k.
func (c *tileCache) has(k cacheKey) bool {
	_, ok := c.keys[k]
	return ok
}

// lookup returns the slot that holds k, and marks it used.
func (c *tileCache) lookup(k cacheKey) (uint16, bool) {
	s, ok := c.keys[k]
	if ok {
		c.touch(s)
	}
	return s, ok
}

// victim returns the slot to store the next tile in: one never used, or
// the one used least recently.
func (c *tileCache) victim() uint16 {
	if c.used < len(c.slots) {
		return uint16(c.used + 1) // #nosec G115 -- at most 65535 slots
	}
	return c.tail
}

// store records that the client stored k in slot s, which victim
// returned.
func (c *tileCache) store(s uint16, k cacheKey) {
	if int(s) > c.used {
		c.used = int(s)
	} else if old := c.slots[s-1].key; c.keys[old] == s {
		delete(c.keys, old)
	}
	c.slots[s-1].key = k
	c.keys[k] = s
	c.touch(s)
}

// touch makes s the most recently used slot.
func (c *tileCache) touch(s uint16) {
	if c.head == s {
		return
	}
	e := &c.slots[s-1]
	// Unlink it, if it is linked.
	if e.prev != 0 {
		c.slots[e.prev-1].next = e.next
	}
	if e.next != 0 {
		c.slots[e.next-1].prev = e.prev
	}
	if c.tail == s {
		c.tail = e.prev
	}
	// Link it first.
	e.prev, e.next = 0, c.head
	if c.head != 0 {
		c.slots[c.head-1].prev = s
	}
	c.head = s
	if c.tail == 0 {
		c.tail = s
	}
}
