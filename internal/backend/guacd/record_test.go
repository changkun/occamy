// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build record

// TestRecord records the conformance corpus in testdata/conformance from
// the bench stack of docs/benchmarks.md: guacd 1.6, the bench desktop
// (bench/scene) over VNC and RDP, and the demo SSH server. It drives a
// bridge through each session, keeps every byte guacd sent up to the last
// complete frame, and pins what TestConformance checks by replaying the
// stream as it does.
//
// The stack runs as a compose project of its own, with only the services
// the recordings need, so that taking it down leaves other stacks alone.
// Its guacd is published on 127.0.0.1:4822, as make bench's is, so run it
// while no bench stack is up. CONTAINER_ENGINE is podman or docker.
//
//	export CONTAINER_ENGINE=podman
//	compose="$CONTAINER_ENGINE compose -p occamy-record -f docker/docker-compose.yaml -f docker/docker-compose.bench.yaml"
//	$compose up -d --build --wait guacd vnc rdp ssh
//	go test -tags record -run TestRecord -v ./internal/backend/guacd -args -guacd 127.0.0.1:4822
//	go test -run TestConformance ./internal/backend/guacd
//	$compose down
//
// -record vnc,rdp,ssh picks the recordings to make. The scenes animate in
// real time, so a new recording differs from the last and pins other
// frames. -pin pins the existing recordings again from their streams,
// without a stack, for when the bridge's rendering changes on purpose.
// When a checkpoint fails, the frame goes as a PNG file to the directory
// that OCCAMY_CONFORMANCE_DUMP names.
package guacd_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	bridge "changkun.de/x/occamy/internal/backend/guacd"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

var (
	recordGuacd = flag.String("guacd", "127.0.0.1:4822", "address of the bench stack's guacd")
	recordOnly  = flag.String("record", "vnc,rdp,ssh", "comma-separated recordings to make")
	recordPin   = flag.Bool("pin", false, "pin the existing recordings again instead of recording")
)

// Keysyms the recorder presses.
const (
	keyEscape = 0xff1b
	keyF2     = 0xffbf
	keyF3     = 0xffc0
	keyF4     = 0xffc1
)

func TestRecord(t *testing.T) {
	scenarios := map[string]struct {
		rec    recording
		secret map[string]string // parameters the manifest leaves out
		play   func(*live)
	}{
		"vnc": {recording{
			Name: "vnc", Protocol: "vnc", Width: 1024, Height: 768,
			// No password, so that guacd asks for it with required.
			Params: map[string]string{"hostname": "vnc", "port": "5900"},
		}, nil, func(l *live) { desktop(l, image.Pt(800, 600)) }},
		"rdp": {recording{
			Name: "rdp", Protocol: "rdp", Width: 640, Height: 480,
			Params: map[string]string{
				"hostname": "rdp", "port": "3389", "username": "occamy",
				"ignore-cert": "true", "resize-method": "display-update",
				// Through the graphics pipeline, which guacd enables by
				// default, xrdp encodes the screen with RemoteFX
				// Progressive, a lossy codec: the idle screen reached
				// guacd 5 to 7 levels darker on average and up to 22
				// off. Without it xrdp sends bitmaps as they are, at
				// 24 bits, since guacd asks for 16 without it.
				"disable-gfx": "true", "color-depth": "24",
			},
		}, map[string]string{"password": "occamy"}, func(l *live) { desktop(l, image.Pt(800, 600)) }},
		"ssh": {recording{
			Name: "ssh", Protocol: "ssh", Width: 800, Height: 600,
			// No password, so that guacd's terminal asks for it.
			Params: map[string]string{"hostname": "ssh", "port": "22", "username": "occamy"},
		}, nil, terminal},
	}
	for name := range strings.SplitSeq(*recordOnly, ",") {
		s, ok := scenarios[name]
		if !ok {
			t.Fatalf("no recording %q", name)
		}
		t.Run(name, func(t *testing.T) {
			if *recordPin {
				pin(t, name)
				return
			}
			rec := s.rec
			l := startLive(t, &rec, s.secret)
			s.play(l)
			stream := l.stop()
			save(t, &rec, stream)
		})
	}
}

// pin reads a recording and saves it again, which pins what its replay
// shows now.
func pin(t *testing.T, name string) {
	t.Helper()
	base := filepath.Join(corpus, name)
	data, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var rec recording
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	stream, err := readStream(base + ".guac.gz")
	if err != nil {
		t.Fatal(err)
	}
	save(t, &rec, stream)
}

// desktop plays the bench desktop: it waits for the scene, then types,
// scrolls, drags a window and resizes to size.
func desktop(l *live, size image.Point) {
	l.ready()
	l.step("Escape and a: the idle scene with the swatch painted")
	l.checkpoint('a')

	l.step("typing hello, each letter painting the swatch, and moving the pointer")
	for i, k := range "hello" {
		l.input(backend.Pointer{X: 100 + 50*i, Y: 200 + 20*i})
		l.key(uint32(k))
		l.waitSwatch(k)
	}

	l.step("scrolling: F3 scrolls a page of text a line every 16 ms, for a tenth of a second with the wheel turned")
	l.key(keyF3)
	l.input(backend.Wheel{DY: 3, Mode: backend.WheelLines})
	time.Sleep(100 * time.Millisecond)
	l.key(keyEscape)

	l.step("moving a window: F4 drags a 400×300 window for 80 ms")
	l.key(keyF4)
	time.Sleep(80 * time.Millisecond)
	l.key(keyEscape)

	l.step("b: the idle scene again")
	l.checkpoint('b')

	l.step(fmt.Sprintf("resizing to %dx%d", size.X, size.Y))
	l.input(backend.Resize{Width: size.X, Height: size.Y})
	time.Sleep(time.Second)
	l.quiet(time.Second, 20*time.Second)

	l.step("c, the last frame")
	l.key('c')
	l.waitSwatch('c')
	l.quiet(500*time.Millisecond, 10*time.Second)
}

// terminal plays an SSH session: it logs in at guacd's prompt, types,
// scrolls, resizes and exits.
func terminal(l *live) {
	l.step("logging in: the password typed at guacd's prompt")
	l.settle(time.Second, 30*time.Second)
	l.text("occamy\n")
	l.settle(time.Second, 30*time.Second)

	l.step("typing: echo hello")
	l.text("echo hello\n")
	l.settle(500*time.Millisecond, 10*time.Second)

	l.step("scrolling: seq 1 200, then the wheel back three notches and forward three")
	l.text("seq 1 200\n")
	l.settle(500*time.Millisecond, 10*time.Second)
	for _, dy := range []int{-3, -3, -3, 3, 3, 3} {
		l.input(backend.Wheel{DY: dy, Mode: backend.WheelLines})
		l.settle(300*time.Millisecond, 10*time.Second)
	}

	l.step("resizing to 640x480")
	l.input(backend.Resize{Width: 640, Height: 480})
	l.settle(time.Second, 10*time.Second)

	l.step("typing: ls -l /")
	l.text("ls -l /\n")
	l.settle(500*time.Millisecond, 10*time.Second)

	l.step("exit")
	l.text("exit\n")
	select {
	case err := <-l.done:
		l.done <- err
	case <-time.After(10 * time.Second):
	}
}

// tee keeps every byte read from guacd, and calls onSync with the
// timestamp of each sync the bridge answers, from the bridge's goroutine
// once it has drawn that frame.
type tee struct {
	net.Conn // set when the bridge dials
	onSync   func(ts string)

	mu   sync.Mutex
	read []byte
}

func (c *tee) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	c.read = append(c.read, p[:n]...)
	c.mu.Unlock()
	return n, err
}

func (c *tee) Write(p []byte) (int, error) {
	if ts, ok := strings.CutPrefix(string(p), "4.sync,"); ok {
		if _, ts, ok := strings.Cut(strings.TrimSuffix(ts, ";"), "."); ok {
			c.onSync(ts)
		}
	}
	return c.Conn.Write(p)
}

// watch is a sink that tracks the swatch's centre and when the bridge
// last drew.
type watch struct {
	*backendtest.Sink

	mu     sync.Mutex
	locked *image.RGBA
	centre color.RGBA
	drawn  time.Time
}

var swatchCentre = image.Pt(32, 32)

func (w *watch) Lock(r image.Rectangle) *image.RGBA {
	img := w.Sink.Lock(r)
	w.mu.Lock()
	w.locked = img
	w.mu.Unlock()
	return img
}

func (w *watch) Unlock(r image.Rectangle) {
	w.mu.Lock()
	if swatchCentre.In(r) {
		w.centre = w.locked.RGBAAt(swatchCentre.X, swatchCentre.Y)
	}
	w.drawn = time.Now()
	w.mu.Unlock()
	w.Sink.Unlock(r)
}

func (w *watch) Resize(width, height int) {
	w.Sink.Resize(width, height)
	w.mu.Lock()
	w.drawn = time.Now()
	w.mu.Unlock()
}

// live is a recorded session.
type live struct {
	t      *testing.T
	rec    *recording
	b      *bridge.Backend
	conn   *tee
	sink   *watch
	cancel context.CancelFunc
	done   chan error
	bytes  int // read from guacd when the last step began

	mu    sync.Mutex
	idle  rune        // the letter of the idle screen awaited, or 0
	match chan string // the sync of the first frame that shows it
}

func startLive(t *testing.T, rec *recording, secret map[string]string) *live {
	l := &live{t: t, rec: rec, done: make(chan error, 1), match: make(chan string, 1)}
	l.conn = &tee{onSync: l.synced}
	l.sink = &watch{Sink: &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		// The bench stack's VNC password, for guacd's required.
		answers := make([]string, len(p.Fields))
		for i, f := range p.Fields {
			if f.Label == "Password" {
				answers[i] = "vncpassword"
			}
		}
		return backend.PromptReply{Answers: answers}, nil
	}}}
	params := maps.Clone(rec.Params)
	maps.Copy(params, secret)
	l.b = bridge.New(*recordGuacd, guacd.Config{
		Protocol: rec.Protocol, Params: params, Width: rec.Width, Height: rec.Height,
	}, bridge.Options{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			l.conn.Conn = c
			return l.conn, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	go func() { l.done <- l.b.Run(ctx, l.sink) }()
	return l
}

func (l *live) step(s string) {
	l.conn.mu.Lock()
	n := len(l.conn.read)
	l.conn.mu.Unlock()
	l.t.Logf("%s (the last step read %d bytes)", s, n-l.bytes)
	l.bytes = n
	l.rec.Steps = append(l.rec.Steps, s)
}

func (l *live) input(ev backend.InputEvent) {
	l.t.Helper()
	if err := l.b.Input(ev); err != nil {
		l.t.Fatalf("Input(%#v) = %v", ev, err)
	}
}

func (l *live) key(keysym uint32) {
	l.t.Helper()
	l.input(backend.Key{Down: true, Keysym: keysym})
	l.input(backend.Key{Keysym: keysym})
}

func (l *live) text(s string) {
	l.t.Helper()
	l.input(backend.Text{Text: s})
}

// waitSwatch waits until the swatch shows the colour of letter k.
func (l *live) waitSwatch(k rune) {
	l.t.Helper()
	if !l.swatchWithin(k, 5*time.Second) {
		l.t.Fatalf("the swatch does not show %q", k)
	}
}

func (l *live) swatchWithin(k rune, d time.Duration) bool {
	want := swatchColour(k)
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		l.sink.mu.Lock()
		c := l.sink.centre
		l.sink.mu.Unlock()
		if !far(c.R, want.R) && !far(c.G, want.G) && !far(c.B, want.B) {
			return true
		}
	}
	return false
}

// quiet waits until the bridge has not drawn for d, and fails after max.
func (l *live) quiet(d, max time.Duration) {
	l.t.Helper()
	l.quietSince(time.Time{}, d, max)
}

// settle waits until the bridge draws and then has not drawn for d, and
// fails after max: the input before it changes the screen.
func (l *live) settle(d, max time.Duration) {
	l.t.Helper()
	l.quietSince(time.Now(), d, max)
}

func (l *live) quietSince(since time.Time, d, max time.Duration) {
	l.t.Helper()
	for end := time.Now().Add(max); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		l.sink.mu.Lock()
		drawn := l.sink.drawn
		l.sink.mu.Unlock()
		if drawn.After(since) && time.Since(drawn) >= d {
			return
		}
	}
	l.t.Fatalf("the screen did not settle within %v", max)
}

// ready waits until the scene program answers, as the bench's ready
// does: xrdp starts the session on the first connection, which takes a
// while. F2 shows the probe scene and a letter paints its swatch,
// alternating letters so that each attempt changes the swatch. w and x
// paint colours that a, b and c do not, so that those change it too.
func (l *live) ready() {
	l.t.Helper()
	l.step("logging in, and waiting for the scene to answer F2 and a letter")
	for i := range 60 {
		k := 'w' + rune(i%2)
		l.key(keyF2)
		l.key(uint32(k))
		if l.swatchWithin(k, time.Second) {
			return
		}
	}
	l.t.Fatal("the scene program does not answer")
}

// checkpoint presses Escape, waits for the screen to settle, presses
// letter k and records the first frame after it that shows the idle
// scene with k's swatch (showsIdle). It looks at every frame from the
// press on, since guacd's RDP sends none while the screen stands still.
// The pause keeps the swatch lossless: guacd encodes a region lossy when
// it changed less than a third of a second before.
func (l *live) checkpoint(k rune) {
	l.t.Helper()
	l.key(keyEscape)
	l.quiet(500*time.Millisecond, 10*time.Second)
	l.mu.Lock()
	l.idle = k
	l.mu.Unlock()
	l.key(uint32(k))
	select {
	case ts := <-l.match:
		l.rec.Checkpoints = append(l.rec.Checkpoints, checkpoint{Sync: ts, Letter: string(k)})
	case <-time.After(15 * time.Second):
		fb := l.sink.Pixels()
		keep(l.t, l.rec.Name+"-"+string(k), fb)
		l.t.Fatalf("no frame shows the idle scene with %q: %v", k, showsIdle(fb, k))
	}
}

// synced checks, as the bridge answers a sync, whether the frame shows
// the idle scene a checkpoint awaits.
func (l *live) synced(ts string) {
	l.mu.Lock()
	k := l.idle
	l.mu.Unlock()
	if k == 0 {
		return
	}
	fb := l.sink.Pixels()
	if fb == nil || showsIdle(fb, k) != nil {
		return
	}
	l.mu.Lock()
	l.idle = 0
	l.mu.Unlock()
	l.match <- ts
}

// stop ends the session and returns what guacd sent up to the end of the
// last frame.
func (l *live) stop() []byte {
	l.t.Helper()
	l.cancel()
	if err := <-l.done; err != nil && !errors.Is(err, context.Canceled) {
		l.t.Errorf("Run = %v", err)
	}
	l.conn.mu.Lock()
	stream := slices.Clone(l.conn.read)
	l.conn.mu.Unlock()
	end, err := lastSyncEnd(stream)
	if err != nil {
		l.t.Fatal(err)
	}
	return stream[:end]
}

// lastSyncEnd returns where the last complete sync instruction in stream
// ends. Bytes after it belong to a frame that did not end, or were cut
// when the session stopped.
func lastSyncEnd(stream []byte) (int, error) {
	r := bufio.NewReader(bytes.NewReader(stream))
	end, pos := 0, 0
	for {
		raw, err := guacamole.ReadRaw(r)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		pos += len(raw)
		if bytes.HasPrefix(raw, []byte("4.sync,")) {
			end = pos
		}
	}
	if end == 0 {
		return 0, errors.New("no frame recorded")
	}
	return end, nil
}

// save pins in rec what TestConformance checks of stream, from a replay
// of it: the opcode counts, the prompts, the pixels that a copy onto the
// screen moved, and the last frame. It writes the stream, the manifest
// and the pinned pixels. What the replay breaks is reported after the
// recording is written, so that TestConformance reproduces it; only a
// replay that draws nothing stops it.
func save(t *testing.T, rec *recording, stream []byte) {
	t.Helper()
	counts, err := instructions(stream)
	if err != nil {
		t.Fatal(err)
	}
	rec.Instructions = counts
	rec.Regions = nil
	frames, err := copyFrames(stream)
	if err != nil {
		t.Fatal(err)
	}
	var syncs []string
	for _, g := range frames {
		syncs = append(syncs, g.Sync)
	}
	r := replay(stream, rec, syncs...)
	if r.frame == nil {
		t.Fatalf("the replay drew nothing: %v", r.err)
	}
	rec.Prompts = prompts(r.prompts)
	rec.Size = r.frame.Rect.Size()
	rec.SHA256 = frameHash(r.frame)
	if err := os.MkdirAll(corpus, 0o750); err != nil {
		t.Fatal(err)
	}

	// Pin the square that a bridge ignoring copy would draw most
	// differently, of the frames that copy onto the screen: there a
	// broken copy shows. The latest wins a tie.
	bare := replay(withoutCopies(stream), rec, syncs...)
	var (
		pinned region
		most   int
	)
	for _, g := range frames {
		a, b := r.snaps[g.Sync], bare.snaps[g.Sync]
		if a == nil || b == nil || a.Rect != b.Rect {
			continue
		}
		sq, n := bestTile(g.Rect.Intersect(a.Rect), func(sq image.Rectangle) int {
			var n int
			for y := sq.Min.Y; y < sq.Max.Y; y++ {
				for x := sq.Min.X; x < sq.Max.X; x++ {
					if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
						n++
					}
				}
			}
			return n
		})
		if n > 0 && n >= most {
			pinned, most = g, n
			pinned.Rect = sq
		}
	}
	t.Logf("%d frames copy onto the screen; without copy, %d pixels of the pinned square differ", len(frames), most)
	if most > 0 {
		fb := r.snaps[pinned.Sync]
		pinned.PNG = rec.Name + "-copy.png"
		crop := image.NewRGBA(image.Rectangle{Max: pinned.Rect.Size()})
		draw.Draw(crop, crop.Rect, fb, pinned.Rect.Min, draw.Src)
		var b bytes.Buffer
		if err := png.Encode(&b, crop); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(corpus, pinned.PNG), b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		rec.Regions = append(rec.Regions, pinned)
	}
	t.Logf("%d bytes, instructions %v", len(stream), counts)
	t.Logf("unsupported %v, prompts %+v, regions %+v", r.unsupported, r.prompts, rec.Regions)

	var z bytes.Buffer
	w, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
	w.Write(stream)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(corpus, rec.Name)
	if err := os.WriteFile(base+".guac.gz", z.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := json.MarshalIndent(rec, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".json", append(m, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s.guac.gz (%d bytes compressed), opcodes %v", base, z.Len(), slices.Sorted(maps.Keys(counts)))
	checkReplay(t, rec, stream, r)
}

// The side of a pinned square, and the grid it sits on.
const tileSide, tileStep = 128, 32

// bestTile returns the tileSide square of r, on a tileStep grid, or r
// where it is smaller, that score rates highest, and that score.
func bestTile(r image.Rectangle, score func(image.Rectangle) int) (image.Rectangle, int) {
	best, most := image.Rectangle{}, 0
	if r.Empty() {
		return best, most
	}
	for y := r.Min.Y; ; y += tileStep {
		y = max(min(y, r.Max.Y-tileSide), r.Min.Y)
		for x := r.Min.X; ; x += tileStep {
			x = max(min(x, r.Max.X-tileSide), r.Min.X)
			sq := image.Rect(x, y, x+tileSide, y+tileSide).Intersect(r)
			if n := score(sq); n > most {
				best, most = sq, n
			}
			if x+tileSide >= r.Max.X {
				break
			}
		}
		if y+tileSide >= r.Max.Y {
			break
		}
	}
	return best, most
}

// maxCopyFrames bounds the frames copyFrames returns, each of which a
// replay keeps.
const maxCopyFrames = 24

// copyFrames returns the frames in which a copy of at least a tileSide
// square lands on the screen, on the default layer or a layer moved onto
// it: the sync that ends each and the largest such copy in it. It keeps
// the maxCopyFrames with the largest copies, the latest of those as
// large, in stream order.
func copyFrames(stream []byte) ([]region, error) {
	all, err := parse(stream)
	if err != nil {
		return nil, err
	}
	// Where each layer on the screen sits.
	at := map[int]image.Point{0: {}}
	var (
		frames []region
		g      region
		area   int
	)
	for _, ins := range all {
		a := ins.Args()
		n := make([]int, len(a))
		for i, s := range a {
			n[i], _ = strconv.Atoi(s)
		}
		switch ins.Opcode() {
		case "move":
			if len(n) == 5 && n[1] == 0 {
				at[n[0]] = image.Pt(n[2], n[3])
			} else if len(n) == 5 {
				delete(at, n[0])
			}
		case "copy":
			if len(n) != 9 || n[3] < tileSide || n[4] < tileSide || n[3]*n[4] <= area {
				continue
			}
			if o, ok := at[n[6]]; ok {
				g.Rect = image.Rect(n[7], n[8], n[7]+n[3], n[8]+n[4]).Add(o)
				g.Why = fmt.Sprintf("a %d×%d copy from layer %d to layer %d at %v",
					n[3], n[4], n[0], n[6], image.Pt(n[7], n[8]))
				area = n[3] * n[4]
			}
		case "sync":
			if area > 0 {
				g.Sync = a[0]
				frames = append(frames, g)
			}
			area = 0
		}
	}
	if len(frames) <= maxCopyFrames {
		return frames, nil
	}
	order := make([]int, len(frames))
	for i := range order {
		order[i] = i
	}
	size := func(i int) int { return frames[i].Rect.Dx() * frames[i].Rect.Dy() }
	slices.SortStableFunc(order, func(i, j int) int {
		if d := size(j) - size(i); d != 0 {
			return d
		}
		return j - i
	})
	order = order[:maxCopyFrames]
	slices.Sort(order)
	kept := make([]region, 0, len(order))
	for _, i := range order {
		kept = append(kept, frames[i])
	}
	return kept, nil
}

// withoutCopies returns stream without its copy instructions: what a
// bridge that ignored them would draw.
func withoutCopies(stream []byte) []byte {
	var out []byte
	r := bufio.NewReader(bytes.NewReader(stream))
	for {
		raw, err := guacamole.ReadRaw(r)
		if err != nil {
			return out
		}
		if !bytes.HasPrefix(raw, []byte("4.copy,")) {
			out = append(out, raw...)
		}
	}
}
