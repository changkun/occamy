// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build rdp_integration

package rdp

// The integration test runs the backend against xrdp in the bench
// desktop image (docker/bench/desktop), whose sessions show the scene
// program (bench/scene), as internal/rdp's integration test runs the
// client:
//
//	go test -tags rdp_integration -run Integration -timeout 30m ./internal/backend/rdp
//
// It builds a container image and starts a container, with podman or
// the engine OCCAMY_CONTAINER_ENGINE names. OCCAMY_RDP_IMAGE names an
// image to use instead of building one, and OCCAMY_RDP_ADDR a running
// server to use instead of starting a container; OCCAMY_RDP_CONTAINER
// may then name its container for the pointer check.
//
// The image and the container a run makes are its own, named with the
// time the run ends at the latest (runs_test.go): concurrent runs do
// not share them. A run removes them when it ends; one that panics
// leaves them behind, and the next run removes those whose time has
// passed. It matches the whole name, so that it removes nobody else's
// image or container.

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp"
)

var (
	rdpAddr      string
	rdpContainer = os.Getenv("OCCAMY_RDP_CONTAINER")
	engine       = envOr("OCCAMY_CONTAINER_ENGINE", "podman")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(func() int {
		addr, stop, err := startServer()
		if err != nil {
			fmt.Fprintln(os.Stderr, "rdp backend integration:", err)
			return 1
		}
		defer stop()
		rdpAddr = addr
		return m.Run()
	}())
}

// runName returns this run's name for its image and container, which
// runPattern matches.
func runName() string {
	timeout := 24 * time.Hour
	if f := flag.Lookup("test.timeout"); f != nil {
		if d, err := time.ParseDuration(f.Value.String()); err == nil && d > 0 {
			timeout = d
		}
	}
	return fmt.Sprintf("%s%d-%d", runPrefix, time.Now().Add(timeout+time.Minute).Unix(), os.Getpid())
}

// cleanUp removes the containers and images of earlier runs whose time
// has passed. podman lists images with a registry before the name.
func cleanUp() {
	out, _ := exec.Command(engine, "ps", "-a", "--filter", "name="+runPrefix, "--format", "{{.Names}}").Output()
	for _, name := range strings.Fields(string(out)) {
		if expired(name, time.Now()) {
			exec.Command(engine, "rm", "-f", name).Run()
		}
	}
	out, _ = exec.Command(engine, "images", "--format", "{{.Repository}}").Output()
	for _, repo := range strings.Fields(string(out)) {
		if expired(repo, time.Now()) {
			exec.Command(engine, "rmi", "-f", repo).Run()
		}
	}
}

// startServer returns the address of an xrdp server showing the scene.
func startServer() (string, func(), error) {
	if addr := os.Getenv("OCCAMY_RDP_ADDR"); addr != "" {
		return addr, func() {}, nil
	}
	cleanUp()
	name := runName()
	image := os.Getenv("OCCAMY_RDP_IMAGE")
	removeImage := func() {}
	if image == "" {
		image = name
		cmd := exec.Command(engine, "build", "-q", "-t", image, "-f", "docker/bench/desktop/Dockerfile", ".")
		cmd.Dir = "../../.."
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", nil, fmt.Errorf("building the desktop image: %w", err)
		}
		removeImage = func() { exec.Command(engine, "rmi", "-f", image).Run() }
	}
	out, err := exec.Command(engine, "run", "-d", "--rm", "--name", name,
		"-p", "127.0.0.1::3389", image, "rdp").CombinedOutput()
	if err != nil {
		removeImage()
		return "", nil, fmt.Errorf("starting %s: %w: %s", image, err, out)
	}
	rdpContainer = name
	stop := func() {
		exec.Command(engine, "rm", "-f", name).Run()
		removeImage()
	}
	out, err = exec.Command(engine, "port", name, "3389/tcp").Output()
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("port of %s: %w", name, err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	for deadline := time.Now().Add(60 * time.Second); ; {
		if probeX224(addr) == nil {
			break
		}
		if time.Now().After(deadline) {
			stop()
			return "", nil, fmt.Errorf("xrdp at %s does not answer", addr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return addr, stop, nil
}

// probeX224 checks that a server answers an X.224 Connection Request
// for TLS with a Connection Confirm.
func probeX224(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	// TPKT, an X.224 Connection Request, and an RDP_NEG_REQ for TLS.
	req := []byte{3, 0, 0, 19, 14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0, 1, 0, 0, 0}
	if _, err := c.Write(req); err != nil {
		return err
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	if h[0] != 3 || n < 7 {
		return fmt.Errorf("not a TPKT: % x", h)
	}
	p := make([]byte, n-4)
	if _, err := io.ReadFull(c, p); err != nil {
		return err
	}
	if p[1]&0xf0 != 0xd0 {
		return fmt.Errorf("not a Connection Confirm: % x", p)
	}
	return nil
}

// gfxWallpaper reports whether img shows the scene's wallpaper as the
// graphics pipeline, which the backend asks for by default, draws it:
// within 4 levels a channel of RemoteFX Progressive, outside the
// swatch's tile, whose wavelets the swatch's edges ring through
// (internal/rdp's TestIntegrationGFX).
func gfxWallpaper(img *image.RGBA) bool {
	if img == nil {
		return false
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	near := func(a, b uint8) bool { d := int(a) - int(b); return -4 <= d && d <= 4 }
	for y := range h {
		want := wallpaper(y, h)
		for x := range w {
			got := img.RGBAAt(x, y)
			if (x >= 64 || y >= 64) && (!near(got.R, want.R) || !near(got.G, want.G) || !near(got.B, want.B)) {
				return false
			}
		}
	}
	return true
}

// xrdpConfig is the backend's configuration for the server: xrdp signs
// its certificate itself and has no NLA. The backend's log goes to the
// test's.
func xrdpConfig(t *testing.T) Config {
	return Config{
		Addr:     rdpAddr,
		Username: "occamy", Password: "occamy",
		Security: SecurityTLS,
		TLS:      rdp.TLS{InsecureSkipVerify: true},
		Logger:   slog.New(slog.NewTextHandler(testLog{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// testLog writes to a test's log.
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// probeColour is the colour bench/scene paints the swatch for letter k
// (bench/internal/probe).
func probeColour(k rune) color.RGBA {
	palette := []color.RGBA{
		{0xff, 0x00, 0x00, 0xff}, {0x00, 0xff, 0x00, 0xff}, {0x00, 0x00, 0xff, 0xff},
		{0xff, 0xff, 0x00, 0xff}, {0x00, 0xff, 0xff, 0xff}, {0xff, 0x00, 0xff, 0xff},
		{0xff, 0x80, 0x00, 0xff}, {0x80, 0x00, 0xff, 0xff},
	}
	return palette[int(k)%len(palette)]
}

// swatchShows waits up to d for the swatch to show the colour of k.
func swatchShows(sink *backendtest.Sink, k rune, d time.Duration) bool {
	want := probeColour(k)
	near := func(a, b uint8) bool { x := int(a) - int(b); return -40 <= x && x <= 40 }
	for deadline := time.Now().Add(d); ; time.Sleep(50 * time.Millisecond) {
		got := sinkAt(sink, image.Pt(32, 32))
		if near(got.R, want.R) && near(got.G, want.G) && near(got.B, want.B) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// hidLetter is the USB HID usage of a lowercase letter's key.
func hidLetter(k rune) uint16 { return uint16(0x04 + k - 'a') }

// The backend draws xrdp's desktop, through the graphics pipeline, types
// keys by their physical code and text, moves the pointer on the
// session's X display, scrolls, has the server suppress its output
// while nobody can see, and ends at once on cancel with nothing left
// running.
func TestIntegration(t *testing.T) {
	checkGoroutines(t)
	b := newBackend(t, xrdpConfig(t))
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)

	// xrdp shows its own screens while sesman starts the session.
	waitLong(t, 90*time.Second, "the wallpaper", func() bool { return gfxWallpaper(sink.Pixels()) })
	input := func(evs ...backend.InputEvent) {
		t.Helper()
		for _, ev := range evs {
			if err := b.Input(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	press := func(code uint16) {
		input(backend.Key{Down: true, Code: code}, backend.Key{Code: code})
	}

	// Keys by physical code: F2 resets the swatch and a letter paints
	// it. Both may arrive in one update, so the attempts alternate
	// between letters of different colours.
	painted := false
	for i := range 10 {
		k := 'y' + rune(i%2)
		press(0x3b) // F2
		press(hidLetter(k))
		if swatchShows(sink, k, 3*time.Second) {
			painted = true
			break
		}
	}
	if !painted {
		t.Fatalf("the swatch does not take the colour of the letter keys: %v", sinkAt(sink, image.Pt(32, 32)))
	}
	for _, k := range "abc" {
		press(hidLetter(k))
		if !swatchShows(sink, k, 5*time.Second) {
			t.Fatalf("key %q: the swatch shows %v, want %v", k, sinkAt(sink, image.Pt(32, 32)), probeColour(k))
		}
	}
	// Text, as Unicode keyboard events where xrdp takes them.
	for _, k := range "de" {
		input(backend.Text{Text: string(k)})
		if !swatchShows(sink, k, 5*time.Second) {
			t.Fatalf("text %q: the swatch shows %v, want %v", k, sinkAt(sink, image.Pt(32, 32)), probeColour(k))
		}
	}

	// While nobody can see, xrdp is asked to stop sending updates; when a
	// viewer can see again, it redraws what changed.
	b.Demand(backend.Demand{Visible: false})
	time.Sleep(time.Second)
	press(hidLetter('f'))
	if swatchShows(sink, 'f', 2*time.Second) {
		t.Log("xrdp drew the swatch while told to suppress its output")
	}
	b.Demand(backend.Demand{Visible: true})
	if !swatchShows(sink, 'f', 10*time.Second) {
		t.Fatalf("after the output resumed, the swatch shows %v, want %v", sinkAt(sink, image.Pt(32, 32)), probeColour('f'))
	}

	// The pointer: the scene ignores it, so the test asks the session's
	// X server where it is.
	if rdpContainer == "" {
		t.Log("pointer not checked: no container")
	} else {
		p := image.Pt(517, 333)
		input(backend.Pointer{X: p.X, Y: p.Y})
		want := fmt.Sprintf("%d %d", p.X, p.Y)
		var got string
		waitLong(t, 5*time.Second, "the X pointer at "+want, func() bool {
			got = queryPointer(t)
			return strings.Contains(got, want)
		})
	}
	// Clicks and the wheel must not end the session.
	input(
		backend.Pointer{X: 600, Y: 400, Buttons: backend.ButtonRight},
		backend.Pointer{X: 600, Y: 400},
		backend.Wheel{DY: 3, Mode: backend.WheelLines},
		backend.Wheel{DX: -106},
	)
	select {
	case <-r.done:
		t.Fatalf("Run returned %v after pointer input", r.err)
	case <-time.After(time.Second):
	}

	begin := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("Run took %v to return after cancel", d)
	}
}

// xrdp offers TLS alone: the backend connects when TLS is among the
// protocols it offers. Offered only NLA, xrdp selects standard RDP
// security rather than failing the negotiation, which the client
// refuses at once with the hint that names the setting allowing TLS.
// With the graphics pipeline disabled, xrdp draws with bitmap updates,
// which arrive exactly.
func TestIntegrationSecurity(t *testing.T) {
	checkGoroutines(t)
	cfg := xrdpConfig(t)
	cfg.Security = SecurityAny
	cfg.DisableGFX = true
	sink := &backendtest.Sink{}
	r := start(t, newBackend(t, cfg), sink)
	waitLong(t, 90*time.Second, "the wallpaper with security any", func() bool {
		bad, _ := matchesWallpaper(sink.Pixels(), 0)
		return bad == 0
	})
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("security any: Run returned %v", err)
	}

	cfg.Security = SecurityNLA
	r = start(t, newBackend(t, cfg), &backendtest.Sink{})
	begin := time.Now()
	err := r.wait(t)
	if err == nil || !strings.Contains(err.Error(), "selected security protocol 0x0") || !strings.Contains(err.Error(), "allow TLS with Config.Security") || time.Since(begin) > 5*time.Second {
		t.Errorf("security nla: Run returned %v after %v, want standard RDP security refused with the hint", err, time.Since(begin))
	}
}

// The clipboard crosses xrdp's clipboard channel both ways: text a
// viewer pastes is the CLIPBOARD selection of the session's X display,
// which xrdp-chansrv serves, and text an X client puts there reaches the
// viewers. The test reads and sets the selection with internal/rdp's
// testdata/xclipboard in the container.
func TestIntegrationClipboard(t *testing.T) {
	if rdpContainer == "" {
		t.Skip("clipboard not checked: no container")
	}
	checkGoroutines(t)
	b := newBackend(t, xrdpConfig(t))
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitLong(t, 90*time.Second, "the wallpaper", func() bool { return gfxWallpaper(sink.Pixels()) })

	pasted := fmt.Sprintf("pasted at %d: héllo €\nsecond line", time.Now().UnixNano())
	if err := b.Input(backend.Clipboard{MIME: "text/plain", Data: []byte(pasted)}); err != nil {
		t.Fatal(err)
	}
	var got string
	waitLong(t, 20*time.Second, "the pasted text in the X clipboard", func() bool {
		got = xClipboard(t, "get")
		return strings.Contains(got, strconv.Quote(pasted)) || strings.Contains(got, strconv.Quote(strings.ReplaceAll(pasted, "\n", "\r\n")))
	})
	t.Logf("X clipboard: %s", got)

	copied := fmt.Sprintf("copied at %d: wörld ✓\nsecond line", time.Now().UnixNano())
	set := exec.Command(engine, "exec", rdpContainer, xClipboardPath(t), "set", copied)
	if err := set.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		set.Process.Kill()
		set.Wait()
	}()
	waitLong(t, 20*time.Second, "the copied text from xrdp", func() bool {
		mime, data := sink.ClipboardContent()
		return mime == "text/plain" && string(data) == copied
	})

	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// A viewer's Resize resizes xrdp's session through Display Control: the
// sink takes the new size, and the session goes on drawing at it, the
// swatch taking the colour of a key. The scene keeps its window's size,
// so the test does not look for its wallpaper at the new size. It
// resizes the session back, as the other tests expect it.
func TestIntegrationResize(t *testing.T) {
	checkGoroutines(t)
	b := newBackend(t, xrdpConfig(t))
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitLong(t, 90*time.Second, "the wallpaper", func() bool { return gfxWallpaper(sink.Pixels()) })
	first := sink.Size()
	for i, size := range []image.Point{image.Pt(1280, 720), image.Pt(800, 600), first} {
		begin := time.Now()
		if err := b.Input(backend.Resize{Width: size.X, Height: size.Y}); err != nil {
			t.Fatal(err)
		}
		waitLong(t, 30*time.Second, fmt.Sprintf("the sink at %v", size), func() bool {
			img := sink.Pixels()
			return img != nil && img.Rect.Size() == size && sink.Size() == size
		})
		t.Logf("%v after %v", size, time.Since(begin).Round(time.Millisecond))
		k := 'a' + rune(i)
		for _, ev := range []backend.InputEvent{backend.Key{Down: true, Code: hidLetter(k)}, backend.Key{Code: hidLetter(k)}} {
			if err := b.Input(ev); err != nil {
				t.Fatal(err)
			}
		}
		if !swatchShows(sink, k, 10*time.Second) {
			t.Fatalf("at %v, key %q: the swatch shows %v, want %v", size, k, sinkAt(sink, image.Pt(32, 32)), probeColour(k))
		}
	}
	select {
	case <-r.done:
		t.Fatalf("Run returned %v after the resizes", r.err)
	default:
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v, want context.Canceled", err)
	}
}

// waitLong polls cond until it holds or d passes.
func waitLong(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// containerTools are the programs of internal/rdp's testdata built for
// the container and copied into it, by name.
var containerTools struct {
	mu    sync.Mutex
	paths map[string]string
}

// containerTool builds internal/rdp's testdata/name for the container,
// copies it in once, and returns its path there.
func containerTool(t *testing.T, name string) string {
	t.Helper()
	containerTools.mu.Lock()
	defer containerTools.mu.Unlock()
	if p, ok := containerTools.paths[name]; ok {
		return p
	}
	arch, err := exec.Command(engine, "exec", rdpContainer, "uname", "-m").Output()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	goarch := map[string]string{"aarch64": "arm64", "x86_64": "amd64"}[strings.TrimSpace(string(arch))]
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "../../rdp/testdata/"+name)
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", name, err, out)
	}
	path := "/tmp/" + name
	if out, err := exec.Command(engine, "cp", bin, rdpContainer+":"+path).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", name, err, out)
	}
	if containerTools.paths == nil {
		containerTools.paths = map[string]string{}
	}
	containerTools.paths[name] = path
	return path
}

// queryPointer runs internal/rdp's testdata/querypointer in the
// container.
func queryPointer(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(engine, "exec", rdpContainer, containerTool(t, "querypointer")).Output()
	if err != nil {
		t.Fatalf("querypointer: %v", err)
	}
	return string(out)
}

// xClipboardPath returns the path of internal/rdp's testdata/xclipboard
// in the container.
func xClipboardPath(t *testing.T) string {
	t.Helper()
	return containerTool(t, "xclipboard")
}

// xClipboard runs internal/rdp's testdata/xclipboard in the container
// with args, and returns what it prints.
func xClipboard(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(engine, append([]string{"exec", rdpContainer, xClipboardPath(t)}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("xclipboard: %v: %s", err, out)
	}
	return string(out)
}
