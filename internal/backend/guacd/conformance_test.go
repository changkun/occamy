// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacd_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	bridge "changkun.de/x/occamy/internal/backend/guacd"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

// corpus holds instruction streams that guacd 1.6 sent for real sessions
// on the bench desktop, with a manifest each. record_test.go records
// them; see its doc comment.
const corpus = "testdata/conformance"

// recording describes one stream of the corpus, in <name>.json beside
// <name>.guac.gz.
type recording struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	// Params are the guacd parameters of the session, without secrets.
	Params map[string]string `json:"params"`
	// Width and Height are the size the bridge asked for.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Steps are what the recorder did, in order.
	Steps []string `json:"steps"`
	// Checkpoints are frames that must show the scene's idle screen.
	Checkpoints []checkpoint `json:"checkpoints,omitempty"`
	// Instructions counts the stream's instructions by opcode, and its
	// images by type ("img image/png").
	Instructions map[string]int `json:"instructions"`
	// Size and SHA256 pin the last frame: its size and the SHA-256 of its
	// RGBA pixels.
	Size   image.Point `json:"size"`
	SHA256 string      `json:"sha256"`
}

// checkpoint is a frame, named by the timestamp of the sync that ends
// it, that shows bench/scene's idle screen with the swatch painted by
// Letter.
type checkpoint struct {
	Sync   string `json:"sync"`
	Letter string `json:"letter"`
}

// The latency swatch of bench/internal/probe, which this package cannot
// import: where the scene paints it, and in which colour for a letter.
var (
	swatch  = image.Rect(16, 16, 48, 48)
	palette = []color.RGBA{
		{0xff, 0x00, 0x00, 0xff},
		{0x00, 0xff, 0x00, 0xff},
		{0x00, 0x00, 0xff, 0xff},
		{0xff, 0xff, 0x00, 0xff},
		{0x00, 0xff, 0xff, 0xff},
		{0xff, 0x00, 0xff, 0xff},
		{0xff, 0x80, 0x00, 0xff},
		{0x80, 0x00, 0xff, 0xff},
	}
)

func swatchColour(k rune) color.RGBA { return palette[int(k)%len(palette)] }

// idleScreen is what bench/scene shows in its idle scene on a screen of
// the given size: the vertical gradient of its newWallpaper, and the
// swatch in the colour of letter k.
func idleScreen(size image.Point, k rune) *image.RGBA {
	img := image.NewRGBA(image.Rectangle{Max: size})
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	for y := range size.Y {
		t := float64(y) / float64(max(1, size.Y-1))
		f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
		c := color.RGBA{f(top.R, bottom.R), f(top.G, bottom.G), f(top.B, bottom.B), 0xff}
		for x := range size.X {
			img.SetRGBA(x, y, c)
		}
	}
	c := swatchColour(k)
	for y := swatch.Min.Y; y < swatch.Max.Y; y++ {
		for x := swatch.Min.X; x < swatch.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// tolerance is how far a channel may differ from what the scene drew:
// guacd encodes some updates as JPEG or WebP.
const tolerance = 24

// showsIdle reports whether got shows the idle screen with the swatch of
// letter k, and why not. A pixel may differ by up to tolerance in each
// channel, and one in a thousand by more, none of them in the swatch:
// guacd encodes a region that changes three or more times a second lossy,
// and never repaints it without loss while it stays the same, so a window
// dragged away can leave a few pixels of WebP ringing on the wallpaper,
// as it does in a browser.
func showsIdle(got *image.RGBA, k rune) error {
	want := idleScreen(got.Rect.Size(), k)
	var n int
	var first image.Point
	for y := want.Rect.Min.Y; y < want.Rect.Max.Y; y++ {
		for x := want.Rect.Min.X; x < want.Rect.Max.X; x++ {
			g, w := got.RGBAAt(x, y), want.RGBAAt(x, y)
			if !far(g.R, w.R) && !far(g.G, w.G) && !far(g.B, w.B) {
				continue
			}
			p := image.Pt(x, y)
			if p.In(swatch) {
				return fmt.Errorf("the swatch at %v is %v, want %v", p, g, w)
			}
			if n == 0 {
				first = p
			}
			n++
		}
	}
	if n > want.Rect.Dx()*want.Rect.Dy()/1000 {
		return fmt.Errorf("%d pixels differ from the idle screen, the first at %v: %v, want %v",
			n, first, got.RGBAAt(first.X, first.Y), want.RGBAAt(first.X, first.Y))
	}
	return nil
}

func far(a, b uint8) bool {
	d := int(a) - int(b)
	return d < -tolerance || d > tolerance
}

// replayed is what a replay of a stream through the bridge produced.
type replayed struct {
	err         error
	frame       *image.RGBA // nil if the screen never had a size
	checkpoints map[string]*image.RGBA
	unsupported map[string]int
	logs        string
	prompts     []backend.Prompt
}

// snapConn copies the sink's framebuffer when the bridge answers the
// sync of a checkpoint, from the bridge's goroutine, before it applies
// anything after that frame.
type snapConn struct {
	net.Conn
	sink *backendtest.Sink
	at   map[string]bool

	mu    sync.Mutex
	snaps map[string]*image.RGBA
}

func (c *snapConn) Write(p []byte) (int, error) {
	if ts, ok := strings.CutPrefix(string(p), "4.sync,"); ok {
		if _, ts, ok := strings.Cut(strings.TrimSuffix(ts, ";"), "."); ok && c.at[ts] {
			c.mu.Lock()
			c.snaps[ts] = c.sink.Pixels()
			c.mu.Unlock()
		}
	}
	return c.Conn.Write(p)
}

// replay runs a stream through a bridge, from a guacd that sends it at
// once and then disconnect, and answers every prompt.
func replay(stream []byte, rec *recording) *replayed {
	sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		answers := make([]string, len(p.Fields))
		for i := range answers {
			answers[i] = "answer"
		}
		return backend.PromptReply{Answers: answers}, nil
	}}
	cli, srv := net.Pipe()
	conn := &snapConn{Conn: cli, sink: sink, at: map[string]bool{}, snaps: map[string]*image.RGBA{}}
	for _, c := range rec.Checkpoints {
		conn.at[c.Sync] = true
	}
	var logs bytes.Buffer
	b := bridge.New("guacd:4822", guacd.Config{
		Protocol: rec.Protocol, Params: rec.Params, Width: rec.Width, Height: rec.Height,
	}, bridge.Options{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Dial:   func(context.Context, string, string) (net.Conn, error) { return conn, nil },
	})

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background(), sink) }()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		io.Copy(io.Discard, srv)
	}()
	var err error
	if _, werr := srv.Write(stream); werr != nil {
		err = fmt.Errorf("sending the stream: %w", werr)
	} else if _, werr := srv.Write([]byte("10.disconnect;")); werr != nil {
		err = fmt.Errorf("sending disconnect: %w", werr)
	}
	if rerr := <-done; rerr != nil {
		err = rerr
	}
	srv.Close()
	<-drained

	r := &replayed{
		err:         err,
		frame:       sink.Pixels(),
		checkpoints: conn.snaps,
		unsupported: b.Unsupported(),
		logs:        logs.String(),
		prompts:     sink.Prompts(),
	}
	return r
}

// instructions counts a stream's instructions by opcode, and its images
// by type.
func instructions(stream []byte) (map[string]int, error) {
	counts := map[string]int{}
	r := bufio.NewReader(bytes.NewReader(stream))
	for {
		raw, err := guacamole.ReadRaw(r)
		if errors.Is(err, io.EOF) {
			return counts, nil
		}
		if err != nil {
			return nil, err
		}
		ins, err := guacamole.ParseInstruction(raw)
		if err != nil {
			return nil, err
		}
		counts[ins.Opcode()]++
		if args := ins.Args(); ins.Opcode() == "img" && len(args) > 3 {
			counts["img "+args[3]]++
		}
	}
}

func frameHash(img *image.RGBA) string {
	sum := sha256.Sum256(img.Pix)
	return hex.EncodeToString(sum[:])
}

func readStream(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(z)
}

// checkReplay reports what in a replay breaks the corpus's rules.
func checkReplay(t *testing.T, rec *recording, r *replayed) {
	t.Helper()
	if r.err != nil {
		t.Errorf("Run = %v", r.err)
	}
	if len(r.unsupported) > 0 {
		t.Errorf("unsupported: %v", r.unsupported)
	}
	if r.logs != "" {
		t.Errorf("the bridge logged:\n%s", r.logs)
	}
	if r.frame == nil {
		t.Fatal("no frame")
	}
	for _, c := range rec.Checkpoints {
		got := r.checkpoints[c.Sync]
		if got == nil {
			t.Errorf("checkpoint at sync %s: no frame", c.Sync)
			continue
		}
		if len(c.Letter) != 1 {
			t.Errorf("checkpoint at sync %s: letter %q", c.Sync, c.Letter)
			continue
		}
		if err := showsIdle(got, rune(c.Letter[0])); err != nil {
			t.Errorf("checkpoint at sync %s, %q: %v", c.Sync, c.Letter, err)
		}
	}
}

// TestConformance replays the corpus: guacd 1.6's VNC, RDP and SSH
// support on the bench desktop, logging in, typing, scrolling, moving a
// window and resizing. Each must apply with nothing unsupported, nothing
// logged and no error, show the idle screen at its checkpoints, and end
// on the frame its manifest pins.
func TestConformance(t *testing.T) {
	manifests, err := filepath.Glob(filepath.Join(corpus, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) == 0 {
		t.Fatal("no recordings in " + corpus)
	}
	for _, path := range manifests {
		var rec recording
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(rec.Name, func(t *testing.T) {
			stream, err := readStream(strings.TrimSuffix(path, ".json") + ".guac.gz")
			if err != nil {
				t.Fatal(err)
			}
			counts, err := instructions(stream)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(counts, rec.Instructions) {
				t.Errorf("instructions = %v, the manifest says %v", counts, rec.Instructions)
			}
			// The bubble checks that no goroutine outlives the replay.
			synctest.Test(t, func(t *testing.T) {
				r := replay(stream, &rec)
				checkReplay(t, &rec, r)
				if got := r.frame.Rect.Size(); got != rec.Size {
					t.Errorf("last frame is %v, want %v", got, rec.Size)
				}
				if got := frameHash(r.frame); got != rec.SHA256 {
					t.Errorf("last frame's SHA-256 = %s, want %s", got, rec.SHA256)
				}
			})
		})
	}
}
