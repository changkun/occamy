// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

var updateCache = flag.Bool("update", false, "rewrite protocol/testdata/cache.json")

// cacheFile holds what occamyd sends a client with a tile cache of four
// slots, frame by frame, and the screen the client shows after each:
// the Go client and the web client's protocol core replay it, so that
// their caches are seen to keep in step with the server's mirror.
var cacheFile = filepath.Join("..", "..", "protocol", "testdata", "cache.json")

type cacheGolden struct {
	Description string `json:"description"`
	// Welcome is the welcome the client reads first, hex.
	Welcome string       `json:"welcome"`
	Frames  []cacheFrame `json:"frames"`
}

type cacheFrame struct {
	Name string `json:"name"`
	// Messages are the frame's messages in order, hex, frame-end last,
	// and Notes say what each is.
	Messages []string `json:"messages"`
	Notes    []string `json:"notes"`
	// Width and Height are the screen's size after the frame, and Screen
	// the SHA-256 of its pixels, RGBA row by row.
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Screen string `json:"screen"`
}

// tape records the bytes of each message it passes on, with a
// frame-end's server time made the frame's number times 16,667 µs, as
// at 60 frames a second, so that the record is the same each time.
type tape struct {
	*recorder
	msgs [][]byte
}

func (c *tape) WriteMessage(ctx context.Context, p []byte) error {
	m, err := protocol.Decode(p, protocol.ServerMessages(), testLimits)
	if end, ok := m.(*protocol.FrameEnd); err == nil && ok {
		end.ServerTime = uint64(end.FrameID) * 16667
		p = end.Append(nil)
	}
	c.msgs = append(c.msgs, slices.Clone(p))
	return c.recorder.WriteMessage(ctx, p)
}

// solidTile returns pattern i's opposite: one colour, i's.
func solidTile(i int, r image.Rectangle) *image.RGBA {
	img := image.NewRGBA(r)
	c := color.RGBA{uint8(40 * i), 0x80, uint8(255 - 30*i), 0xff} // #nosec G115 -- small
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// note says what message p is.
func note(p []byte) string {
	m, err := protocol.Decode(p, protocol.ServerMessages(), testLimits)
	if err != nil {
		return err.Error()
	}
	switch m := m.(type) {
	case *protocol.Resize:
		return fmt.Sprintf("resize %dx%d", m.Width, m.Height)
	case *protocol.Tile:
		s := fmt.Sprintf("tile %d,%d %dx%d %v", m.X, m.Y, m.W, m.H, m.Codec)
		switch {
		case m.Codec == protocol.CodecCached:
			s += fmt.Sprintf(" from slot %d", m.Slot)
		case m.Slot != 0:
			s += fmt.Sprintf(", stored in slot %d", m.Slot)
		}
		return s
	case *protocol.FrameEnd:
		return fmt.Sprintf("frame-end %d", m.FrameID)
	}
	return m.Type().String()
}

// cacheScript plays the frames of cache.json on a writer with a mirror
// of four slots, painting each frame's tiles by pattern: a, b, c, d and
// e are patterns 0 to 4, and S is one colour.
func cacheScript(t *testing.T) cacheGolden {
	d := newDirect(t, false)
	d.w.enc = newEncoder([]protocol.Codec{protocol.CodecDeflate, protocol.CodecRaw})
	d.w.cache = newTileCache(4)
	c := &tape{recorder: d.c}
	d.w.c = c

	g := cacheGolden{
		Description: "What occamyd sends a client whose tile cache has 4 slots, frame by frame, and the SHA-256 of " +
			"the client's screen after each frame, RGBA row by row: a client replays the messages and checks " +
			"each screen, which is the server's. Rewrite with `go test ./internal/stream -run TestCacheGolden " +
			"-update`. The frames store tiles and draw them again in one frame and across frames, replace the " +
			"slot used least recently when all are full, keep the slots across resizes, and tell the same bytes " +
			"at two sizes apart.",
		Welcome: hex.EncodeToString(protocol.Welcome{
			ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical, SessionID: "cache",
			Capabilities: protocol.Capabilities{
				Codecs:      []protocol.Codec{protocol.CodecDeflate, protocol.CodecRaw, protocol.CodecCached},
				CacheSlots:  4,
				FrameWindow: 8,
			},
		}.Append(nil)),
	}
	shown := map[image.Point]string{} // each tile's pattern
	frame := func(name string, size image.Point, rows ...string) {
		t.Helper()
		if d.s.Framebuffer().Bounds().Size() != size {
			d.b.Sink().Resize(size.X, size.Y)
			clear(shown)
		}
		for y, row := range rows {
			for x, p := range strings.Fields(row) {
				r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(image.Rectangle{Max: size})
				if shown[r.Min] == p {
					continue // unchanged, so not damaged
				}
				shown[r.Min] = p
				if p == "S" {
					paint(d.b, r, solidTile(5, r))
				} else {
					paint(d.b, r, pattern(int(p[0]-'a'), r.Min))
				}
			}
		}
		c.msgs = nil
		d.frame()
		d.exact()
		screen := serverScreen(d.s)
		sum := sha256.Sum256(screen.Pix)
		f := cacheFrame{Name: name, Width: size.X, Height: size.Y, Screen: hex.EncodeToString(sum[:])}
		for _, p := range c.msgs {
			f.Messages = append(f.Messages, hex.EncodeToString(p))
			f.Notes = append(f.Notes, note(p))
		}
		g.Frames = append(g.Frames, f)
	}

	frame("eight tiles of five patterns: a and b drawn again from the slots they were stored in a moment before; "+
		"e replaces c, the slot used least recently",
		image.Pt(256, 128), "a b a c", "d a b e")
	frame("c and d again, which were replaced: each replaces the slot used least recently, d's, then a's",
		image.Pt(256, 128), "c d a c", "d a b e")
	frame("a smaller screen: the slots stay, so e, c, d and b come from them, and a and e replace e and c",
		image.Pt(192, 128), "e c d", "b a e")
	frame("one colour over a screen of 96x96, whose tiles are 64x64, 32x64, 64x32 and 32x32: "+
		"32x64 and 64x32 are the same bytes, and each is stored",
		image.Pt(96, 96), "S S", "S S")
	frame("one colour over 160x64: its 64x64 and 32x64 tiles come from the slots of the screen before",
		image.Pt(160, 64), "S S S")
	frame("the first screen again, all of it to be stored afresh", image.Pt(256, 128), "a b a c", "d a b e")
	return g
}

// The tile cache plays out as cache.json records, and the client's
// screen is the server's after each frame.
func TestCacheGolden(t *testing.T) {
	g := cacheScript(t)

	// What each frame holds, as the mirror's least recently used policy
	// has it.
	want := [][]string{
		{"resize 256x128", "cursor-move", "title",
			"tile 0,0 64x64 deflate, stored in slot 1", "tile 64,0 64x64 deflate, stored in slot 2",
			"tile 128,0 64x64 cached from slot 1", "tile 192,0 64x64 deflate, stored in slot 3",
			"tile 0,64 64x64 deflate, stored in slot 4", "tile 64,64 64x64 cached from slot 1",
			"tile 128,64 64x64 cached from slot 2", "tile 192,64 64x64 deflate, stored in slot 3", "frame-end 1"},
		{"tile 0,0 64x64 deflate, stored in slot 4", "tile 64,0 64x64 deflate, stored in slot 1", "frame-end 2"},
		{"resize 192x128",
			"tile 0,0 64x64 cached from slot 3", "tile 64,0 64x64 cached from slot 4", "tile 128,0 64x64 cached from slot 1",
			"tile 0,64 64x64 cached from slot 2", "tile 64,64 64x64 deflate, stored in slot 3",
			"tile 128,64 64x64 deflate, stored in slot 4", "frame-end 3"},
		{"resize 96x96",
			"tile 0,0 64x64 deflate, stored in slot 1", "tile 64,0 32x64 deflate, stored in slot 2",
			"tile 0,64 64x32 deflate, stored in slot 3", "tile 64,64 32x32 deflate, stored in slot 4", "frame-end 4"},
		{"resize 160x64",
			"tile 0,0 64x64 cached from slot 1", "tile 64,0 64x64 cached from slot 1",
			"tile 128,0 32x64 cached from slot 2", "frame-end 5"},
	}
	for i, w := range want {
		if !slices.Equal(g.Frames[i].Notes, w) {
			t.Errorf("frame %d:\n%s\nwant\n%s", i+1, strings.Join(g.Frames[i].Notes, "\n"), strings.Join(w, "\n"))
		}
	}

	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *updateCache {
		if err := os.WriteFile(cacheFile, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/stream -run TestCacheGolden -update)", err)
	}
	var old cacheGolden
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	// The payloads are deflate's, which another version of Go may
	// compress otherwise: the file is out of date if it says or shows
	// something else.
	same := old.Description == g.Description && old.Welcome == g.Welcome && len(old.Frames) == len(g.Frames)
	for i := 0; same && i < len(g.Frames); i++ {
		o, n := old.Frames[i], g.Frames[i]
		same = o.Name == n.Name && slices.Equal(o.Notes, n.Notes) && o.Width == n.Width && o.Height == n.Height && o.Screen == n.Screen
	}
	if !same {
		t.Errorf("%s is out of date (run go test ./internal/stream -run TestCacheGolden -update)", cacheFile)
	}
}
