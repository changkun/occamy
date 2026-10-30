// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build measure

package codecs

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
	"changkun.de/x/occamy/internal/framebuffer"
)

// The measurement runs with
//
//	go test -tags measure -run TestMeasure -timeout 2h ./bench/codecs -args -out DIR
//
// and writes DIR/codecs.json and DIR/codecs-tables.md.
var (
	corpusDir = flag.String("corpus", "../testdata/corpus", "corpus directory")
	outDir    = flag.String("out", "", "output directory; the measurement is skipped without it")
	runs      = flag.Int("runs", 3, "passes over the corpus; times are the median per tile")
	sizesFlag = flag.String("sizes", "64,128", "tile sides")
	only      = flag.String("codecs", "", "regexp restricting the codecs, for a quick look")
	pipeLL    = flag.String("pipeline-lossless", "pal+flate1", "lossless codec of the pipeline mix")
	pipeLossy = flag.String("pipeline-lossy", "jpeg q70", "lossy codec of the pipeline mix")
)

// frame is one corpus file.
type frame struct {
	name, scene string
	img         *image.RGBA
}

// tile is one tile of a frame at one tile size, with what each codec
// made of it.
type tile struct {
	scene, frame string
	size, index  int
	img          *image.RGBA
	colours      int
	pal          *Palette // nil above 256 colours
	classifyNs   []int64
	swizzleNs    []int64
	res          []result // by codec
	raw          map[string]result
}

type result struct {
	bytes    int
	enc, dec []int64 // per pass, nanoseconds
	ssim     float64
	mismatch bool
}

func median(v []int64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return float64(s[len(s)/2])
	}
	return float64(s[len(s)/2-1]+s[len(s)/2]) / 2
}

func loadCorpus(t *testing.T) []frame {
	files, err := filepath.Glob(filepath.Join(*corpusDir, "*.png"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus in %s: %v", *corpusDir, err)
	}
	sort.Strings(files)
	seq := regexp.MustCompile(`-\d+$`)
	var frames []frame
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		img := image.NewRGBA(m.Bounds())
		draw.Draw(img, img.Rect, m, m.Bounds().Min, draw.Src)
		name := strings.TrimSuffix(filepath.Base(f), ".png")
		frames = append(frames, frame{name: name, scene: seq.ReplaceAllString(name, ""), img: img})
	}
	return frames
}

// tilesOf splits f through the framebuffer the pipeline uses.
func tilesOf(f frame, side int) []*tile {
	fb := framebuffer.New(side)
	b := f.img.Rect
	fb.Resize(b.Dx(), b.Dy())
	draw.Draw(fb.Lock(b), b, f.img, b.Min, draw.Src)
	fb.Unlock(b)
	cols, rows := fb.Grid()
	var ts []*tile
	for i := range cols * rows {
		buf := make([]byte, side*side*4)
		r, _ := fb.ReadTile(i, buf)
		img := &image.RGBA{Pix: buf[:r.Dx()*r.Dy()*4], Stride: r.Dx() * 4, Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
		ts = append(ts, &tile{scene: f.scene, frame: f.name, size: side, index: i, img: img})
	}
	return ts
}

// rawLayouts are the raw-pixel forms the pixel layout question compares,
// each deflated at level 1: three or four bytes a pixel, red or blue
// first.
var rawLayouts = []string{"RGB", "BGR", "RGBX", "BGRX"}

func appendRaw(dst []byte, img *image.RGBA, layout string) []byte {
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
		switch layout {
		case "RGB":
			dst = append(dst, r, g, b)
		case "BGR":
			dst = append(dst, b, g, r)
		case "RGBX":
			dst = append(dst, r, g, b, 0xff)
		case "BGRX":
			dst = append(dst, b, g, r, 0)
		}
	}
	return dst
}

func TestMeasure(t *testing.T) {
	if *outDir == "" {
		t.Skip("no -out directory")
	}
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	frames := loadCorpus(t)
	var sizes []int
	for _, s := range strings.Split(*sizesFlag, ",") {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, n)
	}
	var filter *regexp.Regexp
	if *only != "" {
		filter = regexp.MustCompile(*only)
	}

	rep := &report{Runs: *runs, Started: time.Now().UTC().Format(time.RFC3339), GoVersion: runtime.Version(),
		GOARCH: runtime.GOARCH, Sizes: sizes, Frames: map[string]int{}}
	for _, f := range frames {
		rep.Frames[f.scene]++
	}
	all := map[int][]*tile{}
	for _, side := range sizes {
		var codecs []*Codec
		for _, c := range Candidates(side) {
			if filter == nil || filter.MatchString(c.Name) {
				codecs = append(codecs, c)
			}
		}
		names := make([]string, len(codecs))
		for i, c := range codecs {
			names[i] = c.Name
		}
		rep.Codecs = names
		var tiles []*tile
		for _, f := range frames {
			tiles = append(tiles, tilesOf(f, side)...)
		}
		all[side] = tiles
		raw := stdFlate(1)
		var buf, packed []byte
		swz := make([]byte, side*side*4)
		start := time.Now()
		for pass := range *runs {
			for _, tl := range tiles {
				// Classification: the colour count with its early exit,
				// which also builds the palette.
				var p Palette
				t0 := time.Now()
				n := Colours(tl.img, &p)
				tl.classifyNs = append(tl.classifyNs, time.Since(t0).Nanoseconds())
				t0 = time.Now()
				Swizzle(swz, tl.img.Pix)
				tl.swizzleNs = append(tl.swizzleNs, time.Since(t0).Nanoseconds())
				if pass == 0 {
					tl.colours = n
					if n <= 256 {
						tl.pal = &Palette{Colours: slices.Clone(p.Colours), Index: slices.Clone(p.Index)}
					}
					tl.res = make([]result, len(codecs))
					tl.raw = map[string]result{}
				}
				for _, layout := range rawLayouts {
					buf = appendRaw(buf[:0], tl.img, layout)
					t0 := time.Now()
					var err error
					packed, err = raw.compress(packed[:0], buf)
					d := time.Since(t0).Nanoseconds()
					if err != nil {
						t.Fatal(err)
					}
					r := tl.raw[layout]
					r.bytes = len(packed)
					r.enc = append(r.enc, d)
					tl.raw[layout] = r
				}
				for ci, c := range codecs {
					t0 := time.Now()
					b, err := c.Encode(tl.img, tl.pal)
					enc := time.Since(t0).Nanoseconds()
					if err != nil {
						t.Fatalf("%s on %s tile %d: %v", c.Name, tl.frame, tl.index, err)
					}
					size := len(b)
					t0 = time.Now()
					got, err := c.Decode(b)
					dec := time.Since(t0).Nanoseconds()
					if err != nil {
						t.Fatalf("%s decoding %s tile %d: %v", c.Name, tl.frame, tl.index, err)
					}
					r := &tl.res[ci]
					r.enc = append(r.enc, enc)
					r.dec = append(r.dec, dec)
					if pass > 0 {
						continue
					}
					r.bytes = size
					if c.Lossless() {
						if !bytes.Equal(got.Pix, tl.img.Pix) {
							r.mismatch = true
							t.Errorf("%s: %s tile %d differs after decoding", c.Name, tl.frame, tl.index)
						}
						r.ssim = 1
					} else {
						r.ssim = SSIM(tl.img, got)
					}
				}
			}
			t.Logf("tile %d, pass %d: %v", side, pass, time.Since(start).Round(time.Second))
		}
		rep.add(side, tiles, codecs)
	}
	rep.Damage = damage(t, frames, sizes, all, rep.Codecs)
	rep.Whole = wholeFrames(t, frames, filter)
	rep.Finished = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(*outDir, 0o750); err != nil {
		t.Fatal(err)
	}
	j, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "codecs.json"), append(j, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "codecs-tables.md"), []byte(rep.markdown()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// report is what the measurement writes: sums of medians per tile, per
// tile size, scene, class and codec.
type report struct {
	Started, Finished string
	GoVersion, GOARCH string
	Runs              int
	Sizes             []int
	Frames            map[string]int // frames per scene
	Codecs            []string
	Rows              []row
	Raw               []row // the raw layouts, deflated at level 1
	Tiles             []tileCount
	Damage            []damageRow
	Whole             []wholeRow
}

// row sums one codec's results over a group of tiles.
type row struct {
	Size     int
	Scene    string
	Class    string
	Codec    string
	Tiles    int
	Pixels   int
	Bytes    int
	EncodeNs float64 // the sum of each tile's median
	DecodeNs float64
	SSIMSum  float64 // the sum of each tile's SSIM
	Mismatch int     `json:",omitempty"`
}

type tileCount struct {
	Size        int
	Scene       string
	Class       string
	Tiles       int
	ClassifyNs  float64
	SwizzleNs   float64
	MeanColours float64
}

func (rep *report) add(side int, tiles []*tile, codecs []*Codec) {
	type key struct{ scene, class, codec string }
	rows := map[key]*row{}
	counts := map[key]*tileCount{}
	for _, tl := range tiles {
		class := Class(tl.colours)
		k := key{tl.scene, class, ""}
		tc := counts[k]
		if tc == nil {
			tc = &tileCount{Size: side, Scene: tl.scene, Class: class}
			counts[k] = tc
		}
		tc.Tiles++
		tc.ClassifyNs += median(tl.classifyNs)
		tc.SwizzleNs += median(tl.swizzleNs)
		tc.MeanColours += float64(tl.colours)
		px := tl.img.Rect.Dx() * tl.img.Rect.Dy()
		for ci, c := range codecs {
			r := tl.res[ci]
			k := key{tl.scene, class, c.Name}
			rw := rows[k]
			if rw == nil {
				rw = &row{Size: side, Scene: tl.scene, Class: class, Codec: c.Name}
				rows[k] = rw
			}
			rw.Tiles++
			rw.Pixels += px
			rw.Bytes += r.bytes
			rw.EncodeNs += median(r.enc)
			rw.DecodeNs += median(r.dec)
			rw.SSIMSum += r.ssim
			if r.mismatch {
				rw.Mismatch++
			}
		}
		for _, layout := range rawLayouts {
			r := tl.raw[layout]
			k := key{tl.scene, class, "raw " + layout}
			rw := rows[k]
			if rw == nil {
				rw = &row{Size: side, Scene: tl.scene, Class: class, Codec: "raw " + layout}
				rows[k] = rw
			}
			rw.Tiles++
			rw.Pixels += px
			rw.Bytes += r.bytes
			rw.EncodeNs += median(r.enc)
			rw.SSIMSum++
		}
	}
	for _, tc := range counts {
		tc.MeanColours /= float64(tc.Tiles)
		rep.Tiles = append(rep.Tiles, *tc)
	}
	for k, rw := range rows {
		if strings.HasPrefix(k.codec, "raw ") {
			rep.Raw = append(rep.Raw, *rw)
		} else {
			rep.Rows = append(rep.Rows, *rw)
		}
	}
	less := func(a, b row) int {
		return cmpAll(a.Size-b.Size, strings.Compare(a.Scene, b.Scene), strings.Compare(a.Class, b.Class), strings.Compare(a.Codec, b.Codec))
	}
	slices.SortFunc(rep.Rows, less)
	slices.SortFunc(rep.Raw, less)
	slices.SortFunc(rep.Tiles, func(a, b tileCount) int {
		return cmpAll(a.Size-b.Size, strings.Compare(a.Scene, b.Scene), strings.Compare(a.Class, b.Class))
	})
}

func cmpAll(c ...int) int {
	for _, v := range c {
		if v != 0 {
			return v
		}
	}
	return 0
}

// wholeRow is a lossy codec on whole frames of a scene, or on their
// rows of 64-pixel tiles, as if the pipeline merged lossy tiles into
// larger rectangles.
type wholeRow struct {
	Scene    string
	Unit     string // "frame" or "row of 64"
	Codec    string
	Frames   int
	Bytes    int
	EncodeNs float64
	DecodeNs float64
	SSIMSum  float64
}

// wholeFamilies are the lossy codecs measured on whole frames.
var wholeFamilies = []string{"jpeg", "jpegn", "webp-vpx0", "webp-vpx4"}

// wholeFrames encodes every frame as one image, and the frames of the
// photograph and the motion as rows of 64-pixel tiles, with the lossy
// codecs at four qualities: what the per-tile headers cost, and what
// merging a frame's lossy tiles would reach.
func wholeFrames(t *testing.T, frames []frame, filter *regexp.Regexp) []wholeRow {
	var codecs []*Codec
	for _, c := range Candidates(1) {
		if slices.Contains(wholeFamilies, c.Family) && slices.Contains([]int{30, 50, 70, 90}, c.Quality) &&
			(filter == nil || filter.MatchString(c.Name)) {
			codecs = append(codecs, c)
		}
	}
	rows := map[[3]string]*wholeRow{}
	var order [][3]string
	// measure encodes the parts of a frame and sums them; the SSIM is
	// the parts' mean, weighted by their pixels.
	measure := func(f frame, unit string, parts []*image.RGBA) {
		for _, c := range codecs {
			var size int
			var encSum, decSum, ssim float64
			for _, part := range parts {
				var enc, dec []int64
				for pass := range *runs {
					t0 := time.Now()
					b, err := c.Encode(part, nil)
					enc = append(enc, time.Since(t0).Nanoseconds())
					if err != nil {
						t.Fatalf("%s on %s: %v", c.Name, f.name, err)
					}
					if pass == 0 {
						size += len(b)
					}
					t0 = time.Now()
					got, err := c.Decode(b)
					dec = append(dec, time.Since(t0).Nanoseconds())
					if err != nil {
						t.Fatalf("%s decoding %s: %v", c.Name, f.name, err)
					}
					if pass == 0 {
						ssim += SSIM(part, got) * float64(len(part.Pix)) / float64(len(f.img.Pix))
					}
				}
				encSum += median(enc)
				decSum += median(dec)
			}
			k := [3]string{f.scene, unit, c.Name}
			r := rows[k]
			if r == nil {
				r = &wholeRow{Scene: f.scene, Unit: unit, Codec: c.Name}
				rows[k] = r
				order = append(order, k)
			}
			r.Frames++
			r.Bytes += size
			r.EncodeNs += encSum
			r.DecodeNs += decSum
			r.SSIMSum += ssim
		}
	}
	for _, f := range frames {
		measure(f, "frame", []*image.RGBA{f.img})
		if f.scene != "photo" && f.scene != "motion" {
			continue
		}
		var strips []*image.RGBA
		for y := 0; y < f.img.Rect.Dy(); y += 64 {
			s := image.NewRGBA(image.Rect(0, 0, f.img.Rect.Dx(), min(64, f.img.Rect.Dy()-y)))
			draw.Draw(s, s.Rect, f.img, image.Pt(0, y), draw.Src)
			strips = append(strips, s)
		}
		measure(f, "row of 64", strips)
	}
	var out []wholeRow
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out
}

// damageRow is what one change costs to send at one tile size: the
// tiles it touches, their pixels, and their bytes in the pipeline's mix
// of codecs.
type damageRow struct {
	Case   string
	Size   int
	Tiles  int
	Pixels int
	Bytes  int
}

// damage measures what small and partial changes cost at each tile
// size: consecutive frames of the animated scenes as they are, the
// scroll after an ideal copy of the scrolled area, and single keys.
func damage(t *testing.T, frames []frame, sizes []int, all map[int][]*tile, names []string) []damageRow {
	ll, lossy := slices.Index(names, *pipeLL), slices.Index(names, *pipeLossy)
	if ll < 0 || lossy < 0 {
		t.Logf("no damage figures: %s or %s was not measured", *pipeLL, *pipeLossy)
		return nil
	}
	byName := map[string]*image.RGBA{}
	for _, f := range frames {
		byName[f.name] = f.img
	}
	// cost is the pipeline's bytes for tile i of frame name.
	cost := func(side int, name string, i int) int {
		for _, tl := range all[side] {
			if tl.frame == name && tl.index == i {
				if tl.colours <= 256 {
					return tl.res[ll].bytes
				}
				return tl.res[lossy].bytes
			}
		}
		t.Fatalf("no tile %d of %s", i, name)
		return 0
	}
	var rows []damageRow
	addDirty := func(label string, side int, after string, dirty []bool) {
		cols := (byName[after].Rect.Dx() + side - 1) / side
		r := damageRow{Case: label, Size: side}
		for i, d := range dirty {
			if !d {
				continue
			}
			r.Tiles++
			tr := image.Rect(i%cols*side, i/cols*side, (i%cols+1)*side, (i/cols+1)*side).Intersect(byName[after].Rect)
			r.Pixels += tr.Dx() * tr.Dy()
			r.Bytes += cost(side, after, i)
		}
		rows = append(rows, r)
	}
	pairs := [][2]string{{"drag-0", "drag-1"}, {"drag-1", "drag-2"}, {"scroll-0", "scroll-1"}, {"scroll-1", "scroll-2"}, {"motion-0", "motion-1"}}
	for _, side := range sizes {
		for _, p := range pairs {
			a, b := byName[p[0]], byName[p[1]]
			if a == nil || b == nil {
				continue
			}
			addDirty(p[0]+" to "+p[1], side, p[1], dirtyTiles(a, b, side, 0))
			if strings.HasPrefix(p[0], "scroll") {
				dy := bestScroll(a, b)
				addDirty(fmt.Sprintf("%s to %s after a copy of %d rows", p[0], p[1], dy), side, p[1], dirtyTiles(a, b, side, dy))
			}
		}
	}
	// A key: the probe swatch on the desktop, and a cell where the
	// terminals' cursors and the browser's text are, with the tile that
	// holds it sent again whole.
	keys := []struct {
		label string
		frame string
		at    image.Point
	}{
		{"a probe key on the desktop", "desktop", probe.Centre},
		{"a key in the terminal", "terminal", image.Pt(3, 515)},
		{"a key in the antialiased terminal", "terminal-aa", image.Pt(102, 655)},
		{"a key in the browser's text", "browser", image.Pt(480, 400)},
	}
	for _, side := range sizes {
		for _, k := range keys {
			img := byName[k.frame]
			if img == nil {
				continue
			}
			cols := (img.Rect.Dx() + side - 1) / side
			i := k.at.Y/side*cols + k.at.X/side
			dirty := make([]bool, cols*((img.Rect.Dy()+side-1)/side))
			dirty[i] = true
			addDirty(k.label, side, k.frame, dirty)
		}
	}
	return rows
}

// dirtyTiles marks the tiles of b that differ from a, where a's area
// right of the scene's 64-pixel margin is first moved up by dy rows, as
// a copy-rect would.
func dirtyTiles(a, b *image.RGBA, side, dy int) []bool {
	w, h := b.Rect.Dx(), b.Rect.Dy()
	cols, rows := (w+side-1)/side, (h+side-1)/side
	dirty := make([]bool, cols*rows)
	for y := range h {
		for x := range w {
			sy := y
			if x >= 64 {
				sy = y + dy
			}
			same := sy < h && bytes.Equal(a.Pix[a.PixOffset(x, sy):a.PixOffset(x, sy)+3], b.Pix[b.PixOffset(x, y):b.PixOffset(x, y)+3])
			if !same {
				dirty[y/side*cols+x/side] = true
			}
		}
	}
	return dirty
}

// bestScroll is the upward move of the area right of the margin that
// leaves the fewest rows of b unexplained by a.
func bestScroll(a, b *image.RGBA) int {
	best, bestMiss := 0, math.MaxInt
	w, h := b.Rect.Dx(), b.Rect.Dy()
	for dy := 0; dy < h/2; dy++ {
		miss := 0
		for y := range h {
			if y+dy >= h || !bytes.Equal(a.Pix[a.PixOffset(64, y+dy):a.PixOffset(w, y+dy)], b.Pix[b.PixOffset(64, y):b.PixOffset(w, y)]) {
				miss++
			}
		}
		if miss < bestMiss {
			best, bestMiss = dy, miss
		}
	}
	return best
}
