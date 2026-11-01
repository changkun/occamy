// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"slices"
	"testing"
)

// FuzzEncodeCache runs the encode cache against a model of it: lookups
// of a few tiles and runs at versions the input picks, encodings made,
// failed or panicking in any order, and a budget that evicts. Every
// lookup must find what the model finds, and the cache must hold the
// model's entries and bytes, within its budget.
func FuzzEncodeCache(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0, 4, 2, 1, 0x49, 2, 1, 0, 2, 6, 0, 3, 1}, uint16(2000))
	f.Add(bytes.Repeat([]byte{0x10, 0x31, 0x52, 0x83, 0x02, 0x40}, 20), uint16(600))
	f.Add(bytes.Repeat([]byte{0x01, 0x1b, 0x05, 0x2a, 0x82, 0x00, 0x22, 0x01}, 30), uint16(1500))
	f.Fuzz(func(t *testing.T, ops []byte, budget uint16) {
		c := newEncodeCache(int(budget))

		// The model: for each key, the versions of its encoding and the
		// entry the cache gave for it, and its size once made; the
		// encodings made, oldest first.
		type made struct {
			versions []uint64
			e        *entry
			size     int
		}
		model := map[any]*made{}
		var order []*made
		var pending []*entry
		bytesHeld, high, hits, misses, stale := 0, 0, 0, 0, 0
		keyOf := func(e *entry) any {
			if e.run {
				return e.rk
			}
			return e.tk
		}
		drop := func(m *made) {
			bytesHeld -= m.size
			m.size = -1 // never counts again
		}

		for i := 0; i+1 < len(ops); i += 2 {
			a, b := ops[i], ops[i+1]
			switch a % 4 {
			case 0, 1: // a lookup
				var key any
				var versions []uint64
				if a%4 == 0 {
					key = tileKey{tile: int(a>>2) % 3, moving: a>>4&1 == 1}
					versions = []uint64{uint64(b % 5)}
				} else {
					n := 1 + int(b>>6)%2
					key = runKey{tile: int(a>>2) % 3, n: n, still: a>>4&1 == 1, q: 90, lo: 30}
					versions = []uint64{uint64(b % 5), uint64(b >> 3 % 5)}[:n]
				}

				// What the model finds: the encoding of these versions, or
				// news that the pixels are older, or else one to make.
				var want *entry
				wantMine, wantStale := false, false
				if m := model[key]; m != nil {
					switch {
					case slices.Equal(m.versions, versions):
						want = m.e
					case olderAny(m.versions, versions):
						wantStale = true
					default:
						if m.size > 0 {
							drop(m)
						}
						delete(model, key)
					}
				}
				if want == nil && !wantStale {
					wantMine = true
				}

				var e *entry
				var mine bool
				if tk, ok := key.(tileKey); ok {
					e, mine = c.tile(tk, versions[0])
				} else {
					e, mine = c.run(key.(runKey), versions)
				}
				switch {
				case wantStale:
					stale++
					if e != nil {
						t.Fatalf("op %d: %v at %v, older than the cache's %v, got an encoding", i, key, versions, model[key].versions)
					}
				case want != nil:
					hits++
					if e != want || mine {
						t.Fatalf("op %d: %v at %v did not find its encoding", i, key, versions)
					}
				case wantMine:
					misses++
					if e == nil || !mine {
						t.Fatalf("op %d: %v at %v is not made", i, key, versions)
					}
					model[key] = &made{versions: slices.Clone(versions), e: e}
					pending = append(pending, e)
				}

			case 2, 3: // an encoding made, failed or panicking
				if len(pending) == 0 {
					continue
				}
				k := int(b) % len(pending)
				e := pending[k]
				pending = slices.Delete(pending, k, k+1)
				size := int(a>>3) * 16
				ok := a>>2&1 == 0
				panics := a%4 == 3 && !ok
				func() {
					defer func() {
						if r := recover(); r != nil && !panics {
							panic(r)
						}
					}()
					c.make(e, func(e *entry) bool {
						if panics {
							panic("encoder")
						}
						e.payload = make([]byte, size)
						return ok
					})
				}()
				if !e.isDone() || e.ok != ok {
					t.Fatalf("op %d: an encoding made %v is published %v", i, ok, e.ok)
				}

				m := model[keyOf(e)]
				switch {
				case m == nil || m.e != e:
					// Replaced while it was made: it never counts.
				case !ok:
					delete(model, keyOf(e))
				default:
					m.size = size + entryBytes
					bytesHeld += m.size
					order = append(order, m)
					for bytesHeld > int(budget) && len(order) > 0 {
						old := order[0]
						order = order[1:]
						if old.size > 0 {
							drop(old)
							delete(model, keyOf(old.e))
						}
					}
					high = max(high, bytesHeld)
				}
			}

			s := c.stats()
			live := 0
			for _, m := range model {
				if m.size > 0 {
					live++
				}
			}
			want := cacheStats{bytesHeld, high, live, hits, misses, stale}
			if s != want {
				t.Fatalf("op %d: the cache holds %+v, the model %+v", i, s, want)
			}
			if s.bytes > int(budget) {
				t.Fatalf("op %d: %d bytes held over a budget of %d", i, s.bytes, budget)
			}
			c.mu.Lock()
			mapped := len(c.tiles) + len(c.runs)
			for key, m := range model {
				var e *entry
				if tk, ok := key.(tileKey); ok {
					e = c.tiles[tk]
				} else {
					e = c.runs[key.(runKey)]
				}
				if e != m.e {
					t.Errorf("op %d: %v is not the model's encoding", i, key)
				}
			}
			c.mu.Unlock()
			if mapped != len(model) {
				t.Fatalf("op %d: the cache maps %d keys, the model %d", i, mapped, len(model))
			}
		}

		// Whoever waits for an encoding gets an answer.
		for _, e := range pending {
			c.make(e, func(*entry) bool { return false })
			if !e.isDone() {
				t.Fatal("an encoding made is not published")
			}
		}
	})
}

// olderAny reports whether any of versions is older than have's.
func olderAny(have, versions []uint64) bool {
	for k, v := range versions {
		if have[k] > v {
			return true
		}
	}
	return false
}
