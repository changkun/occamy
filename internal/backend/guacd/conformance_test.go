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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// dumpEnv names a directory to write the frames of failed checks to, as
// PNG files.
const dumpEnv = "OCCAMY_CONFORMANCE_DUMP"

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
	// Regions are areas of frames pinned pixel by pixel.
	Regions []region `json:"regions,omitempty"`
	// Prompts are what the bridge asked the session's owner, in order.
	Prompts []prompt `json:"prompts,omitempty"`
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

// region is an area of the frame that the sync Sync ends, whose pixels
// are in the PNG file of that name beside the manifest. Why says what
// drew it.
type region struct {
	Sync string          `json:"sync"`
	Rect image.Rectangle `json:"rect"`
	Why  string          `json:"why"`
	PNG  string          `json:"png"`
}

// prompt is a backend.Prompt as the manifest holds it.
type prompt struct {
	Name   string        `json:"name"`
	Fields []promptField `json:"fields"`
}

type promptField struct {
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

func prompts(ps []backend.Prompt) []prompt {
	var out []prompt
	for _, p := range ps {
		q := prompt{Name: p.Name}
		for _, f := range p.Fields {
			q.Fields = append(q.Fields, promptField{Label: f.Label, Secret: f.Secret})
		}
		out = append(out, q)
	}
	return out
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

// tolerance is how far a channel may differ from what the scene drew.
// The desktops send the wallpaper without loss (x11vnc, and xrdp without
// the graphics pipeline at 24 bits), and guacd sends it as PNG or, where
// it changed three or more times a second, as WebP, which keeps the
// smooth gradient within 4 levels when decoded as libwebp does.
// golang.org/x/image/webp's own colours were more than 6 off on 26,328
// pixels of the VNC recording's second checkpoint and 43,817 of the
// RDP recording's.
const tolerance = 6

// residue is how many pixels may differ by more than tolerance: a line
// around the 400×300 window that bench/scene drags. Where a WebP update
// held the window and the wallpaper beside it, its 4:2:0 chroma bleeds
// across the window's edge into the wallpaper's first row or column,
// by up to 47 levels. guacd never resends those pixels, since they were
// the wallpaper all along, so the line stays after the window moves on,
// as it does in a browser: 269 pixels at the VNC recording's second
// checkpoint and 865 at the RDP recording's.
const residue = 2 * (400 + 300)

// showsIdle reports whether got shows the idle screen with the swatch of
// letter k, and why not. A pixel may differ by up to tolerance in each
// channel, and residue pixels by more, none of them in the swatch.
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
	if n > residue {
		return fmt.Errorf("%d pixels differ from the idle screen by more than %d, the first at %v: %v, want %v",
			n, tolerance, first, got.RGBAAt(first.X, first.Y), want.RGBAAt(first.X, first.Y))
	}
	return nil
}

func far(a, b uint8) bool {
	d := int(a) - int(b)
	return d < -tolerance || d > tolerance
}

// sameRegion reports the first pixel of r in got that differs from want,
// which holds r's pixels from its origin.
func sameRegion(got *image.RGBA, want image.Image, r image.Rectangle) error {
	if !r.In(got.Rect) {
		return fmt.Errorf("%v is not within the frame %v", r, got.Rect)
	}
	if want.Bounds().Size() != r.Size() {
		return fmt.Errorf("the pinned pixels are %v, want %v", want.Bounds().Size(), r.Size())
	}
	o := want.Bounds().Min.Sub(r.Min)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			g := got.RGBAAt(x, y)
			w := color.RGBAModel.Convert(want.At(x+o.X, y+o.Y)).(color.RGBA)
			if g != w {
				return fmt.Errorf("pixel %v is %v, want %v", image.Pt(x, y), g, w)
			}
		}
	}
	return nil
}

// keep writes img to the directory dumpEnv names, so that a failed frame
// can be looked at.
func keep(t *testing.T, name string, img image.Image) {
	t.Helper()
	dir := os.Getenv(dumpEnv)
	if dir == "" {
		t.Logf("set %s to a directory to keep the frame", dumpEnv)
		return
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Error(err)
		return
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Error(err)
		return
	}
	t.Logf("wrote the frame to %s", path)
}

// replayed is what a replay of a stream through the bridge produced.
type replayed struct {
	err         error
	frame       *image.RGBA // nil if the screen never had a size
	snaps       map[string]*image.RGBA
	unsupported map[string]int
	logs        string
	prompts     []backend.Prompt
	sent        []byte // what the bridge sent guacd
}

// snapConn copies the sink's framebuffer when the bridge answers the
// sync of a frame of interest, from the bridge's goroutine, before it
// applies anything after that frame.
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
// once and then disconnect, and answers every prompt with "answer". It
// keeps the frames that the checkpoints and regions of rec name, and
// those that the syncs in also end.
func replay(stream []byte, rec *recording, also ...string) *replayed {
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
	for _, r := range rec.Regions {
		conn.at[r.Sync] = true
	}
	for _, ts := range also {
		conn.at[ts] = true
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
	var sent bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		io.Copy(&sent, srv)
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

	return &replayed{
		err:         err,
		frame:       sink.Pixels(),
		snaps:       conn.snaps,
		unsupported: b.Unsupported(),
		logs:        logs.String(),
		prompts:     sink.Prompts(),
		sent:        sent.Bytes(),
	}
}

// parse splits a stream into instructions.
func parse(stream []byte) ([]*guacamole.Instruction, error) {
	var out []*guacamole.Instruction
	r := bufio.NewReader(bytes.NewReader(stream))
	for {
		raw, err := guacamole.ReadRaw(r)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		ins, err := guacamole.ParseInstruction(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, ins)
	}
}

// instructions counts a stream's instructions by opcode, and its images
// by type.
func instructions(stream []byte) (map[string]int, error) {
	all, err := parse(stream)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, ins := range all {
		counts[ins.Opcode()]++
		if args := ins.Args(); ins.Opcode() == "img" && len(args) > 3 {
			counts["img "+args[3]]++
		}
	}
	return counts, nil
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

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// checkReplay reports what in a replay breaks the corpus's rules.
func checkReplay(t *testing.T, rec *recording, stream []byte, r *replayed) {
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
	if got := prompts(r.prompts); !slices.EqualFunc(got, rec.Prompts, func(a, b prompt) bool {
		return a.Name == b.Name && slices.Equal(a.Fields, b.Fields)
	}) {
		t.Errorf("prompts = %+v, want %+v", got, rec.Prompts)
	}
	checkSent(t, rec, stream, r.sent)
	if r.frame == nil {
		t.Fatal("no frame")
	}
	for _, c := range rec.Checkpoints {
		got := r.snaps[c.Sync]
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
			keep(t, rec.Name+"-"+c.Sync, got)
		}
	}
	for _, g := range rec.Regions {
		got := r.snaps[g.Sync]
		if got == nil {
			t.Errorf("region %v at sync %s: no frame", g.Rect, g.Sync)
			continue
		}
		want, err := readPNG(filepath.Join(corpus, g.PNG))
		if err != nil {
			t.Error(err)
			continue
		}
		if err := sameRegion(got, want, g.Rect); err != nil {
			t.Errorf("region %v at sync %s (%s): %v", g.Rect, g.Sync, g.Why, err)
			keep(t, rec.Name+"-"+g.Sync, got)
		}
	}
}

// checkSent checks what the bridge sent guacd in a replay: the
// handshake for rec, an argv stream answering each parameter that guacd
// required, an ack refusing each argv stream that guacd opened, and one
// answer to each frame.
func checkSent(t *testing.T, rec *recording, stream, sent []byte) {
	t.Helper()
	in, err := parse(stream)
	if err != nil {
		t.Fatal(err)
	}
	out, err := parse(sent)
	if err != nil {
		t.Fatalf("what the bridge sent: %v", err)
	}
	if len(in) == 0 || in[0].Opcode() != "args" || len(in[0].Args()) == 0 {
		t.Fatal("the stream does not start with args")
	}
	byOp := map[string][][]string{}
	for _, ins := range out {
		byOp[ins.Opcode()] = append(byOp[ins.Opcode()], ins.Args())
	}
	if len(out) == 0 || out[0].Opcode() != "select" || !slices.Equal(out[0].Args(), []string{rec.Protocol}) {
		t.Errorf("the bridge began with %v, want select %s", out[:min(1, len(out))], rec.Protocol)
	}
	want := []string{strconv.Itoa(rec.Width), strconv.Itoa(rec.Height)}
	if s := byOp["size"]; len(s) == 0 || len(s[0]) < 2 || !slices.Equal(s[0][:2], want) {
		t.Errorf("handshake size = %v, want %v", s, want)
	}
	if s := byOp["image"]; len(s) != 1 || !slices.Contains(s[0], "image/webp") {
		t.Errorf("image = %v, want image/webp among them", s)
	}
	// connect answers args: the protocol version, then each parameter's
	// value in the order guacd listed them.
	names := in[0].Args()
	values := []string{guacd.ProtocolVersion}
	for _, name := range names[1:] {
		values = append(values, rec.Params[name])
	}
	if c := byOp["connect"]; len(c) != 1 || !slices.Equal(c[0], values) {
		t.Errorf("connect = %v, want %v", c, values)
	}

	// Each parameter guacd required comes back as an argv stream holding
	// the prompt's answer.
	streams := map[string]string{} // stream index to parameter name
	data := map[string][]byte{}
	answered := map[string]string{}
	for _, ins := range out {
		a := ins.Args()
		switch ins.Opcode() {
		case "argv":
			if len(a) == 3 && a[1] == "text/plain" {
				streams[a[0]] = a[2]
			}
		case "blob":
			if _, ok := streams[a[0]]; ok && len(a) == 2 {
				b, err := base64.StdEncoding.DecodeString(a[1])
				if err != nil {
					t.Errorf("argv blob: %v", err)
				}
				data[a[0]] = append(data[a[0]], b...)
			}
		case "end":
			if name, ok := streams[a[0]]; ok {
				answered[name] = string(data[a[0]])
				delete(streams, a[0])
				delete(data, a[0])
			}
		}
	}
	required := map[string]string{}
	for _, ins := range in {
		if ins.Opcode() == "required" {
			for _, name := range ins.Args() {
				required[name] = "answer"
			}
		}
	}
	if !maps.Equal(answered, required) {
		t.Errorf("argv answers = %v, want %v", answered, required)
	}

	// guacd's SSH support opens an argv stream for each terminal setting
	// a client may change; the bridge refuses each with an ack.
	var opened, refused []string
	for _, ins := range in {
		if a := ins.Args(); ins.Opcode() == "argv" && len(a) > 0 {
			opened = append(opened, a[0])
		}
	}
	for _, a := range byOp["ack"] {
		if len(a) == 3 && a[2] == strconv.Itoa(0x0100) {
			refused = append(refused, a[0])
		}
	}
	if !slices.Equal(refused, opened) {
		t.Errorf("the bridge refused streams %v, want the argv streams %v", refused, opened)
	}

	// Each frame is answered once, with its own timestamp.
	var frames, answers []string
	for _, ins := range in {
		if a := ins.Args(); ins.Opcode() == "sync" && len(a) > 0 && !slices.Contains(frames, a[0]) {
			frames = append(frames, a[0])
		}
	}
	for _, a := range byOp["sync"] {
		if len(a) > 0 {
			answers = append(answers, a[0])
		}
	}
	if !slices.Equal(answers, frames) {
		t.Errorf("the bridge answered %d frames, want the %d of the stream in order", len(answers), len(frames))
	}
}

// TestConformance replays the corpus: guacd 1.6's VNC, RDP and SSH
// support on the bench desktop, logging in, typing, scrolling, moving a
// window and resizing. Each must apply with nothing unsupported, nothing
// logged and no error, prompt and answer guacd as recorded, show the idle
// screen at its checkpoints and the pinned pixels in its regions, and end
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
				checkReplay(t, &rec, stream, r)
				if got := r.frame.Rect.Size(); got != rec.Size {
					t.Errorf("last frame is %v, want %v", got, rec.Size)
				}
				if got := frameHash(r.frame); got != rec.SHA256 {
					t.Errorf("last frame's SHA-256 = %s, want %s", got, rec.SHA256)
					keep(t, rec.Name+"-last", r.frame)
				}
			})
		})
	}
}
