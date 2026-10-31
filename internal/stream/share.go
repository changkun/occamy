// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"
	"maps"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/framebuffer"
	"changkun.de/x/occamy/protocol"
)

// The encode cache and its workers (specs/008-native-architecture/
// graphics-pipeline.md, Implementation notes: Encode cache and workers).
//
// Viewers whose encoders send tiles in the same codecs get the same bytes
// for the same pixels, so the encoding of a tile, and of a run of tiles
// at a quality, is made once for all the viewers of a session. A tile's
// version names its pixels, since the framebuffer's versions count up
// for all its tiles and a resize changes every one, so an encoding is
// known by the versions of the tiles it holds; an encoding of newer
// versions replaces the older. A session's workers encode the rows of a
// frame ahead of the writers, who send them in order as they are done
// and encode themselves what no worker has begun. The cache holds at
// most a budget of bytes, the oldest going first.

// DefaultEncodeCacheBytes bounds the encodings a session keeps unless
// Config.EncodeCacheBytes says otherwise.
const DefaultEncodeCacheBytes = 32 << 20

// DefaultEncodeWorkers is the workers a session has unless
// Config.EncodeWorkers says otherwise: half the cores, so that a busy
// session leaves the other half to the rest.
func DefaultEncodeWorkers() int { return max(1, runtime.GOMAXPROCS(0)/2) }

// profile is what an encoder of internal/encoder sends tiles in, its
// lossless and lossy codecs: encoders of one profile encode the same
// pixels alike.
type profile struct{ lossless, lossy protocol.Codec }

// profileOf returns enc's profile, and false unless enc is
// internal/encoder's, the only one whose encodings are known to be the
// same for every viewer of the profile.
func profileOf(enc Encoder) (profile, bool) {
	e, ok := enc.(*encoder.Encoder)
	if !ok || e == nil {
		return profile{}, false
	}
	lossless, lossy := e.Kinds()
	return profile{lossless, lossy}, true
}

// shared is what the viewers of a session share: the encode cache and
// the workers.
type shared struct {
	refs  int // under sharedMu
	cache *encodeCache
	jobs  chan func(*worker) // nil without workers
	wg    sync.WaitGroup
}

// jobsPerWorker is how many jobs may wait for each worker; a frame's
// rows that find the queue full are left to the writer.
const jobsPerWorker = 64

var (
	sharedMu  sync.Mutex
	sharedFor = map[*framebuffer.Framebuffer]*shared{}
)

// share returns what the viewers of fb share, which the first makes with
// the given workers and cache budget. Each call is paired with release.
func share(fb *framebuffer.Framebuffer, workers, budget int) *shared {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	sh := sharedFor[fb]
	if sh == nil {
		sh = &shared{cache: newEncodeCache(budget)}
		if workers > 0 {
			sh.jobs = make(chan func(*worker), jobsPerWorker*workers)
			for range workers {
				sh.wg.Go(func() {
					wk := &worker{encs: map[profile]*encoder.Encoder{}}
					for job := range sh.jobs {
						job(wk)
					}
				})
			}
		}
		sharedFor[fb] = sh
	}
	sh.refs++
	return sh
}

// release lets go of what share returned for fb. The last to let go
// stops the workers and waits for them to finish the jobs they have.
func (sh *shared) release(fb *framebuffer.Framebuffer) {
	sharedMu.Lock()
	sh.refs--
	last := sh.refs == 0
	if last {
		delete(sharedFor, fb)
	}
	sharedMu.Unlock()
	if last && sh.jobs != nil {
		close(sh.jobs)
		sh.wg.Wait()
	}
}

// submit queues job for a worker, and reports false if the queue is
// full.
func (sh *shared) submit(job func(*worker)) bool {
	select {
	case sh.jobs <- job:
		return true
	default:
		return false
	}
}

// worker is what a worker encodes with: an encoder of each profile, and
// buffers like the writer's.
type worker struct {
	encs     map[profile]*encoder.Encoder
	tiles    [encoder.MaxRun][]byte
	rects    [encoder.MaxRun]image.Rectangle
	versions []uint64
	pix      []byte
	payload  []byte
}

// encoder returns the worker's encoder of profile p.
func (wk *worker) encoder(p profile) *encoder.Encoder {
	e := wk.encs[p]
	if e == nil {
		var codecs []protocol.Codec
		for _, c := range []protocol.Codec{p.lossless, p.lossy} {
			if c != 0 {
				codecs = append(codecs, c)
			}
		}
		e = encoder.New(codecs)
		wk.encs[p] = e
	}
	return e
}

// tile returns the buffer for the pixels of a run's k-th tile.
func (wk *worker) tile(k, size int) []byte {
	if len(wk.tiles[k]) < 4*size*size {
		wk.tiles[k] = make([]byte, 4*size*size)
	}
	return wk.tiles[k]
}

// tileKey names a tile's encoding by Tile; the entry has its version.
type tileKey struct {
	p      profile
	tile   int
	moving bool
}

// runKey names the lossy encoding of the run of n tiles from tile: by
// Still from lo to q, or else by Lossy at q. The entry has the tiles'
// versions.
type runKey struct {
	p       profile
	tile, n int
	still   bool
	q, lo   int
}

// runMode is how a run of lossy tiles is encoded when the pacer's
// quality is q and its lowest lo: tiles in motion by Lossy at the lowest
// quality, an input's effect by Lossy at q, and other tiles by Still
// from lo to q.
func runMode(q, lo int, moving, input bool) (still bool, hi, from int) {
	switch {
	case moving:
		return false, min(q, lo), 0
	case input:
		return false, q, 0
	}
	return true, q, lo
}

// entry is an encoding in the cache, or one being made. done is closed
// once it is made, ok false if it could not be; nothing of it changes
// after.
type entry struct {
	versions []uint64
	done     chan struct{}
	ok       bool

	payload []byte
	codec   protocol.Codec
	lossy   bool // a tile's: it goes in a run
	quality int  // a run's: the quality Still chose
	reached bool // and whether its SSIM reached the score

	// Under the cache's lock: the key it is under, whether it is a run's,
	// the bytes it counts, and whether it was replaced or evicted.
	tk   tileKey
	rk   runKey
	run  bool
	size int
	dead bool
}

// entryBytes is what an entry costs beside its payload: the entry, its
// versions and its place in a map and in the order of eviction.
const entryBytes = 256

// encodeCache holds the encodings of a session's tiles and runs, within
// a budget of bytes; the oldest go first once it is full.
type encodeCache struct {
	mu     sync.Mutex
	tiles  map[tileKey]*entry
	runs   map[runKey]*entry
	made   []*entry // in the order they were made, for eviction
	live   int      // of made, those not dead
	bytes  int
	high   int // the most bytes it held
	budget int

	// For the measurement: lookups that found an encoding made or being
	// made, those that made one, and those whose pixels were older than
	// the cache's.
	hits, misses, stale int
}

func newEncodeCache(budget int) *encodeCache {
	return &encodeCache{tiles: map[tileKey]*entry{}, runs: map[runKey]*entry{}, budget: budget}
}

// tile returns the encoding of tile k at version: one made or being
// made, whose done the caller waits for, or else a new one, mine, which
// the caller makes with make. It returns nil if the cache has a newer
// version: the caller's pixels are out of date, and it encodes them on
// its own.
func (c *encodeCache) tile(k tileKey, version uint64) (e *entry, mine bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.tiles[k]; old != nil {
		switch v := old.versions[0]; {
		case v == version:
			c.hits++
			return old, false
		case v > version:
			c.stale++
			return nil, false
		}
		c.drop(old)
	}
	e = &entry{versions: []uint64{version}, done: make(chan struct{}), tk: k}
	c.tiles[k] = e
	c.misses++
	return e, true
}

// run returns the encoding of run k whose tiles have versions, as tile
// does. It returns nil if the cache has a newer version of any of them.
func (c *encodeCache) run(k runKey, versions []uint64) (e *entry, mine bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.runs[k]; old != nil {
		if slices.Equal(old.versions, versions) {
			c.hits++
			return old, false
		}
		for i, v := range old.versions {
			if v > versions[i] {
				c.stale++
				return nil, false
			}
		}
		c.drop(old)
	}
	e = &entry{versions: slices.Clone(versions), done: make(chan struct{}), rk: k, run: true}
	c.runs[k] = e
	c.misses++
	return e, true
}

// make makes e, which tile or run gave the caller, with f, which
// reports whether it could, and publishes it: if not, the next to ask
// makes it again. A panic in f publishes it failed, so that nobody waits
// for it forever.
func (c *encodeCache) make(e *entry, f func(e *entry) bool) {
	ok := false
	defer func() { c.finish(e, ok) }()
	ok = f(e)
}

func (c *encodeCache) finish(e *entry, ok bool) {
	c.mu.Lock()
	e.ok = ok
	switch {
	case e.dead:
	case !ok:
		c.unmap(e)
		e.dead = true
	default:
		e.size = len(e.payload) + entryBytes
		c.bytes += e.size
		c.made = append(c.made, e)
		c.live++
		for c.bytes > c.budget && c.evict() {
		}
		c.high = max(c.high, c.bytes)
		if len(c.made) > 2*c.live+64 {
			c.made = slices.DeleteFunc(c.made, func(e *entry) bool { return e.dead })
		}
	}
	c.mu.Unlock()
	close(e.done)
}

// drop takes e, made or not, out of the bytes held, as replaced; the
// caller maps its key to another.
func (c *encodeCache) drop(e *entry) {
	if e.size > 0 {
		c.bytes -= e.size
		c.live--
		e.size = 0
	}
	e.dead = true
}

// unmap removes e's key, if it is still e's.
func (c *encodeCache) unmap(e *entry) {
	if e.run {
		if c.runs[e.rk] == e {
			delete(c.runs, e.rk)
		}
	} else if c.tiles[e.tk] == e {
		delete(c.tiles, e.tk)
	}
}

// evict drops the oldest entry, and reports false if there is none.
func (c *encodeCache) evict() bool {
	for len(c.made) > 0 {
		e := c.made[0]
		c.made[0] = nil
		c.made = c.made[1:]
		if e.dead {
			continue
		}
		c.unmap(e)
		c.drop(e)
		return true
	}
	return false
}

// cacheStats is what an encode cache held and found.
type cacheStats struct {
	bytes, high, entries int
	hits, misses, stale  int
}

func (c *encodeCache) stats() cacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cacheStats{c.bytes, c.high, c.live, c.hits, c.misses, c.stale}
}

// encodeTile encodes tile t, whose pixels at version are in w.tiles[k]
// and make r, as w.enc.Tile does, appending to dst: once for all the
// viewers that share the writer's cache.
func (w *writer) encodeTile(dst []byte, t, k int, version uint64, r image.Rectangle, moving bool) ([]byte, protocol.Codec, bool) {
	var e *entry
	if w.sh != nil {
		var mine bool
		e, mine = w.sh.cache.tile(tileKey{w.prof, t, moving}, version)
		if e != nil && !mine {
			if <-e.done; e.ok {
				return append(dst, e.payload...), e.codec, e.lossy
			}
			e = nil
		}
	}
	if e == nil {
		return w.enc.Tile(dst, w.tiles[k], r.Dx(), r.Dy(), moving)
	}
	var b []byte
	w.sh.cache.make(e, func(e *entry) bool {
		b, e.codec, e.lossy = w.enc.Tile(dst, w.tiles[k], r.Dx(), r.Dy(), moving)
		e.payload = slices.Clone(b[len(dst):])
		return true
	})
	return b, e.codec, e.lossy
}

// encodeRun encodes the run of n tiles from t, whose pixels are pix and
// make rect and whose versions are w.runVersion, into w.payload, by
// Still from lo to q or else by Lossy at q: once for all the viewers that
// share the writer's cache. It returns the codec, and Still's quality and
// whether it reached the score.
func (w *writer) encodeRun(t, n int, pix []byte, rect image.Rectangle, still bool, q, lo int) (codec protocol.Codec, got int, reached bool) {
	var e *entry
	if w.sh != nil {
		var mine bool
		e, mine = w.sh.cache.run(runKey{w.prof, t, n, still, q, lo}, w.runVersion[:n])
		if e != nil && !mine {
			if <-e.done; e.ok {
				w.payload = append(w.payload[:0], e.payload...)
				return e.codec, e.quality, e.reached
			}
			e = nil
		}
	}
	enc := func() {
		if still {
			w.payload, codec, got, reached = w.enc.Still(w.payload[:0], pix, rect.Dx(), rect.Dy(), w.g.tile, lo, q)
		} else {
			w.payload, codec = w.enc.Lossy(w.payload[:0], pix, rect.Dx(), rect.Dy(), q)
		}
	}
	if e == nil {
		enc()
		return codec, got, reached
	}
	w.sh.cache.make(e, func(e *entry) bool {
		enc()
		e.payload, e.codec, e.quality, e.reached = slices.Clone(w.payload), codec, got, reached
		return codec != 0
	})
	return codec, got, reached
}

// prefetch is a frame's work for the workers: what of the writer's
// state they need, as of when the writer handed the rows over.
type prefetch struct {
	fb       *framebuffer.Framebuffer
	g        grid
	p        profile
	cache    *encodeCache
	tc       *tileCache // a copy of the client's tile cache, nil without one
	q, lo    int        // the pacer's quality and lowest
	maxWidth int
	// over is set once the frame is sent: what is left is not wanted.
	over atomic.Bool
}

// prefetch hands the rows of order but the first, which the writer
// encodes meanwhile, to the workers, a job a row, and returns the
// frame's work, nil if none. The tiles go to the cache in the messages
// encode makes of them, so that the writer finds them there.
func (w *writer) prefetch(order []int) *prefetch {
	if w.sh == nil || w.sh.jobs == nil || len(order) < 2 ||
		order[0]/w.g.cols == order[len(order)-1]/w.g.cols {
		return nil
	}
	f := &prefetch{
		fb: w.fb, g: w.g, p: w.prof, cache: w.sh.cache,
		q: w.p.Quality(), lo: w.p.MinQuality(), maxWidth: w.maxWidth(),
	}
	if w.cache != nil {
		f.tc = &tileCache{keys: maps.Clone(w.cache.keys)}
	}
	ts := make([]int, 0, len(order))
	for _, t := range order {
		if t/w.g.cols != order[0]/w.g.cols && w.sent[t] < w.versions[t] {
			ts = append(ts, t)
		}
	}
	moving := make([]bool, len(ts))
	for i, t := range ts {
		moving[i] = w.moving(t, false)
	}
	for i := 0; i < len(ts); {
		j := i + 1
		for j < len(ts) && ts[j]/w.g.cols == ts[i]/w.g.cols {
			j++
		}
		row, mv := ts[i:j], moving[i:j]
		if !w.sh.submit(func(wk *worker) { f.row(wk, row, mv) }) {
			break
		}
		i = j
	}
	return f
}

// maxWidth is the widest a run may be: the client's maximum texture
// size.
func (w *writer) maxWidth() int {
	if w.limits.MaxTextureSize > 0 {
		return w.limits.MaxTextureSize
	}
	return protocol.DefaultMaxTextureSize
}

// row encodes tiles ts of one row, whose motion is moving, into the
// cache as encode would: each tile, and each run of lossy tiles.
func (f *prefetch) row(wk *worker, ts []int, moving []bool) {
	enc := wk.encoder(f.p)
	for i := 0; i < len(ts) && !f.over.Load(); {
		n := f.message(wk, enc, ts[i:], moving[i:])
		if n == 0 {
			return
		}
		i += n
	}
}

// message encodes the message from ts[0] on as encode would, and
// reports how many tiles it holds: 0 if the screen was resized.
func (f *prefetch) message(wk *worker, enc *encoder.Encoder, ts []int, moving []bool) int {
	t := ts[0]
	r, v := f.fb.ReadTile(t, wk.tile(0, f.g.tile))
	if r != f.g.rect(t) || f.fb.Bounds() != f.g.bounds {
		return 0
	}
	if f.cached(wk.tiles[0], r) {
		return 1
	}
	if lossy, ok := f.tile(wk, enc, t, v, 0, r, moving[0]); !ok || !lossy {
		return 1
	}
	wk.rects[0] = r
	wk.versions = append(wk.versions[:0], v)
	n, width := 1, r.Dx()
	for n < len(ts) && n < encoder.MaxRun {
		u := ts[n]
		ru := f.g.rect(u)
		if u != ts[n-1]+1 || width+ru.Dx() > f.maxWidth || moving[n] != moving[0] {
			break
		}
		got, vu := f.fb.ReadTile(u, wk.tile(n, f.g.tile))
		if got != ru || f.fb.Bounds() != f.g.bounds || f.cached(wk.tiles[n], ru) {
			break
		}
		if lossy, ok := f.tile(wk, enc, u, vu, n, ru, moving[0]); !ok || !lossy {
			break
		}
		wk.rects[n] = ru
		wk.versions = append(wk.versions, vu)
		width += ru.Dx()
		n++
	}
	f.run(wk, enc, t, n, moving[0])
	return n
}

// cached reports whether the client's tile cache holds pix, the pixels
// of r, which encode then sends as a cached tile.
func (f *prefetch) cached(pix []byte, r image.Rectangle) bool {
	if f.tc == nil {
		return false
	}
	k, ok := f.tc.key(pix, r)
	return ok && f.tc.has(k)
}

// tile encodes the k-th tile of a run, t at version v, into the cache,
// or waits for whoever does, and reports whether it goes lossy; ok is
// false if the cache has a newer version.
func (f *prefetch) tile(wk *worker, enc *encoder.Encoder, t int, v uint64, k int, r image.Rectangle, moving bool) (lossy, ok bool) {
	e, mine := f.cache.tile(tileKey{f.p, t, moving}, v)
	switch {
	case e == nil:
		return false, false
	case !mine:
		<-e.done
		return e.lossy, e.ok
	}
	f.cache.make(e, func(e *entry) bool {
		wk.payload, e.codec, e.lossy = enc.Tile(wk.payload[:0], wk.tiles[k], r.Dx(), r.Dy(), moving)
		e.payload = slices.Clone(wk.payload)
		return true
	})
	return e.lossy, true
}

// run encodes the run of n tiles from t, whose pixels and versions the
// worker holds, into the cache, unless someone else does.
func (f *prefetch) run(wk *worker, enc *encoder.Encoder, t, n int, moving bool) {
	still, q, lo := runMode(f.q, f.lo, moving, false)
	e, mine := f.cache.run(runKey{f.p, t, n, still, q, lo}, wk.versions[:n])
	if !mine {
		return
	}
	rect := wk.rects[0].Union(wk.rects[n-1])
	wk.pix = assemble(wk.pix, wk.tiles[:n], wk.rects[:n], rect)
	f.cache.make(e, func(e *entry) bool {
		if still {
			wk.payload, e.codec, e.quality, e.reached = enc.Still(wk.payload[:0], wk.pix, rect.Dx(), rect.Dy(), f.g.tile, lo, q)
		} else {
			wk.payload, e.codec = enc.Lossy(wk.payload[:0], wk.pix, rect.Dx(), rect.Dy(), q)
		}
		e.payload = slices.Clone(wk.payload)
		return e.codec != 0
	})
}

// assemble puts the pixels of tiles, which make up rect, side by side in
// pix, row by row, and returns it.
func assemble(pix []byte, tiles [][]byte, rects []image.Rectangle, rect image.Rectangle) []byte {
	stride := 4 * rect.Dx()
	size := stride * rect.Dy()
	if cap(pix) < size {
		pix = make([]byte, size)
	}
	pix = pix[:size]
	for k, r := range rects {
		tw := 4 * r.Dx()
		x := 4 * (r.Min.X - rect.Min.X)
		for y := range r.Dy() {
			copy(pix[y*stride+x:y*stride+x+tw], tiles[k][y*tw:(y+1)*tw])
		}
	}
	return pix
}

// stopFetch tells the workers that the rows of the frame's work are no
// longer wanted.
func (w *writer) stopFetch() {
	if w.fetch != nil {
		w.fetch.over.Store(true)
		w.fetch = nil
	}
}
