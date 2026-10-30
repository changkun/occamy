// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build record

// TestRecord records the conformance corpus in testdata/conformance from
// the bench stack of docs/benchmarks.md: guacd 1.6, the bench desktop
// (bench/scene) over VNC and RDP, and the demo SSH server. It drives a
// bridge through each session, keeps every byte guacd sent up to the last
// complete frame, and pins that frame by replaying the stream as
// TestConformance does.
//
//	cd docker
//	podman compose -f docker-compose.yaml -f docker-compose.bench.yaml up -d --build --wait guacd vnc rdp ssh
//	cd ..
//	go test -tags record -run TestRecord -v ./internal/backend/guacd -args -guacd 127.0.0.1:4822
//	go test -run TestConformance ./internal/backend/guacd
//	cd docker && podman compose -f docker-compose.yaml -f docker-compose.bench.yaml down
//
// -record vnc,rdp,ssh picks the recordings to make. The scenes animate in
// real time, so a new recording differs from the last and pins another
// frame.
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
	"image/png"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
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
			rec := s.rec
			l := startLive(t, &rec, s.secret)
			s.play(l)
			stream := l.stop()
			save(t, &rec, stream)
		})
	}
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
		if dir := os.Getenv("OCCAMY_RECORD_DUMP"); dir != "" {
			dump(l.t, filepath.Join(dir, l.rec.Name+"-"+string(k)+".png"), fb)
		}
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

// save replays stream, pins its last frame in rec, and writes both.
func save(t *testing.T, rec *recording, stream []byte) {
	t.Helper()
	counts, err := instructions(stream)
	if err != nil {
		t.Fatal(err)
	}
	rec.Instructions = counts
	r := replay(stream, rec)
	checkReplay(t, rec, r)
	if r.frame != nil {
		rec.Size = r.frame.Rect.Size()
		rec.SHA256 = frameHash(r.frame)
	}
	t.Logf("%d bytes, instructions %v", len(stream), counts)
	t.Logf("unsupported %v, prompts %+v", r.unsupported, r.prompts)

	if err := os.MkdirAll(corpus, 0o750); err != nil {
		t.Fatal(err)
	}
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
}

// dump writes img as a PNG, for a look at a frame that failed.
func dump(t *testing.T, path string, img image.Image) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Error(err)
	}
}
