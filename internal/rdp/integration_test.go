// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build rdp_integration

package rdp

// The integration test runs the client against xrdp in the bench desktop
// image (docker/bench/desktop), whose sessions show the scene program
// (bench/scene):
//
//	go test -tags rdp_integration -run Integration -timeout 30m ./internal/rdp
//
// The rdp_integration build tag builds a container image and starts a
// container, with podman or the engine OCCAMY_CONTAINER_ENGINE names,
// such as docker. The build downloads Go modules and Debian packages,
// which can take longer than go test's default 10-minute timeout, hence
// -timeout. OCCAMY_RDP_IMAGE names an image to use instead of building
// one, and OCCAMY_RDP_ADDR a running server to use instead of starting a
// container; OCCAMY_RDP_CONTAINER may then name its container for the
// mouse check. OCCAMY_RDP_DUMP names a directory for screenshots. With
// -rdp.record the test rewrites the recordings that TestReplay plays
// back.
//
// The image and the container a run makes are its own, named with the
// time the run ends at the latest, its -timeout: concurrent runs do not
// share them. A run removes them when it ends; one that panics, as on
// -timeout, leaves them behind, and the next run removes those whose
// time has passed.

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
)

var (
	rdpAddr      string
	rdpContainer = os.Getenv("OCCAMY_RDP_CONTAINER")
	engine       = envOr("OCCAMY_CONTAINER_ENGINE", "podman")
	record       = flag.Bool("rdp.record", false, "rewrite testdata/*.trace.gz from the live server")
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
			fmt.Fprintln(os.Stderr, "rdp integration:", err)
			return 1
		}
		defer stop()
		rdpAddr = addr
		return m.Run()
	}())
}

// runPrefix starts the names of the images and containers that runs
// make, which go on with the Unix time a run ends at the latest.
const runPrefix = "occamy-rdp-it-until"

// runName returns this run's name for its image and container.
func runName() string {
	timeout := 24 * time.Hour
	if f := flag.Lookup("test.timeout"); f != nil {
		if d, err := time.ParseDuration(f.Value.String()); err == nil && d > 0 {
			timeout = d
		}
	}
	return fmt.Sprintf("%s%d-%d", runPrefix, time.Now().Add(timeout+time.Minute).Unix(), os.Getpid())
}

// expired reports whether name is a run's whose time has passed.
func expired(name string) bool {
	_, rest, ok := strings.Cut(name, runPrefix)
	if !ok {
		return false
	}
	until, _, _ := strings.Cut(rest, "-")
	t, err := strconv.ParseInt(until, 10, 64)
	return err == nil && time.Now().Unix() > t
}

// cleanUp removes the containers and images of earlier runs whose time
// has passed. podman lists images with a registry before the name.
func cleanUp() {
	out, _ := exec.Command(engine, "ps", "-a", "--filter", "name="+runPrefix, "--format", "{{.Names}}").Output()
	for _, name := range strings.Fields(string(out)) {
		if expired(name) {
			exec.Command(engine, "rm", "-f", name).Run()
		}
	}
	out, _ = exec.Command(engine, "images", "--format", "{{.Repository}}").Output()
	for _, repo := range strings.Fields(string(out)) {
		if expired(repo) {
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
		cmd.Dir = "../.."
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
	// Wait until xrdp answers the X.224 connection request.
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

func probeX224(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(connectionRequest("", ProtocolSSL)); err != nil {
		return err
	}
	f, _, err := newTransport(c, nil).readFrame(nil)
	if err != nil {
		return err
	}
	_, err = parseConnectionConfirm(f.data)
	return err
}

// session is a client connected to the test server.
type session struct {
	c    *Client
	scr  *screen
	done chan error

	mu    sync.Mutex
	trace []byte // the server's frames, recorded
}

func dial(t *testing.T, cfg Config) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := net.Dial("tcp", rdpAddr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Username, cfg.Password = "occamy", "occamy"
	// xrdp signs its certificate itself and has no NLA: the test skips
	// verification and offers TLS alone, explicitly.
	cfg.TLS.InsecureSkipVerify, cfg.Logger = true, quiet
	if cfg.Protocols == 0 {
		cfg.Protocols = ProtocolSSL
	}
	s := &session{scr: &screen{}, done: make(chan error, 1)}
	cfg.Trace = func(d Direction, p []byte) {
		if d != FromServer {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.trace = appendRecord(s.trace, p)
	}
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	defer ccancel()
	c, err := Connect(cctx, conn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.c = c
	go func() { s.done <- c.Run(ctx, s.scr) }()
	t.Cleanup(func() {
		cancel()
		<-s.done
	})
	return s
}

func dump(t *testing.T, s *session, name string) {
	dir := os.Getenv("OCCAMY_RDP_DUMP")
	if dir == "" {
		return
	}
	img := s.scr.snapshot()
	if img == nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Log(err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
}

// waitFor polls cond until it holds or d passes.
func waitFor(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
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

// tap presses and releases the key with scancode sc.
func tap(t *testing.T, c *Client, sc uint16) {
	t.Helper()
	if err := c.Key(sc, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Key(sc, false); err != nil {
		t.Fatal(err)
	}
}

// swatchShows waits for the swatch to show the colour of letter k.
func swatchShows(s *session, k rune, d time.Duration) bool {
	want := probeColour(k)
	return waitFor(d, func() bool {
		got := s.scr.at(32, 32)
		return near(got.R, want.R, 40) && near(got.G, want.G, 40) && near(got.B, want.B, 40)
	})
}

func letter(t *testing.T, k rune) uint16 {
	sc, ok := ScancodeForKeysym(uint32(k))
	if !ok {
		t.Fatalf("no scancode for %q", k)
	}
	return sc
}

// The scene's pixels arrive whole at every colour depth: planar at 32 bits
// per pixel, interleaved RLE below. Keys reach the scene, which paints the
// swatch, and the pointer moves on the session's X display.
func TestIntegration(t *testing.T) {
	for _, tc := range []struct {
		bpp   int
		tol   int // per channel: 15- and 16-bit colour truncate
		codec string
	}{
		{32, 0, "planar"},
		{24, 0, "rle24"},
		{16, 8, "rle16"},
		{15, 8, "rle15"},
	} {
		t.Run(fmt.Sprintf("%dbpp", tc.bpp), func(t *testing.T) {
			s := dial(t, Config{ColorDepth: tc.bpp})
			t.Logf("security protocol %#x, desktop %v", s.c.Protocol(), image.Pt(s.c.Size()))
			if s.c.Protocol() != ProtocolSSL {
				t.Errorf("security protocol %#x, want TLS", s.c.Protocol())
			}
			// xrdp shows its own screens while sesman starts the session,
			// which takes a while for the first connection at a depth.
			ok := waitFor(90*time.Second, func() bool {
				bad, _ := matchesWallpaper(s.scr.snapshot(), tc.tol)
				return bad == 0
			})
			dump(t, s, fmt.Sprintf("%dbpp", tc.bpp))
			if !ok {
				bad, first := matchesWallpaper(s.scr.snapshot(), tc.tol)
				t.Fatalf("%d pixels differ from the wallpaper, the first at %v: %v, want %v; stats %+v",
					bad, first, s.scr.at(first.X, first.Y), wallpaper(first.Y, 768), s.c.Stats())
			}
			if !s.c.Finalized() {
				t.Error("no Font Map PDU from the server")
			}
			st := s.c.Stats()
			t.Logf("stats after the first screen: %+v", st)
			if st.Bitmaps[tc.codec] == 0 {
				t.Errorf("no %s bitmaps: %v", tc.codec, st.Bitmaps)
			}
			if *record {
				s.mu.Lock()
				saveTrace(t, fmt.Sprintf("testdata/xrdp-%dbpp.trace.gz", tc.bpp), s.trace)
				s.mu.Unlock()
			}

			// Keyboard: F2 resets the swatch and a letter paints it, as
			// bench's ready() does. Both may arrive in one update, so the
			// attempts alternate between letters of different colours.
			painted := false
			for i := range 10 {
				k := 'y' + rune(i%2)
				tap(t, s.c, 0x3c) // F2
				tap(t, s.c, letter(t, k))
				if swatchShows(s, k, 3*time.Second) {
					painted = true
					break
				}
			}
			if !painted {
				t.Fatalf("the swatch does not take the colour of the letter keys: %v", s.scr.at(32, 32))
			}
			for _, k := range "abc" {
				tap(t, s.c, letter(t, k))
				if !swatchShows(s, k, 5*time.Second) {
					t.Fatalf("key %q: the swatch shows %v, want %v", k, s.scr.at(32, 32), probeColour(k))
				}
			}

			// Mouse: the scene ignores the pointer, so the test asks the
			// session's X server where it is.
			if rdpContainer == "" {
				t.Log("mouse not checked: no container")
				return
			}
			p := image.Pt(500+tc.bpp, 300+tc.bpp)
			if err := s.c.MouseMove(p); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("%d %d", p.X, p.Y)
			var got string
			if !waitFor(5*time.Second, func() bool {
				got = queryPointer(t)
				return strings.Contains(got, want)
			}) {
				t.Errorf("the X pointer is at %q, want %s on one display", got, want)
			}
			// Clicks and a wheel notch must not end the session.
			for _, b := range []Button{ButtonLeft, ButtonRight} {
				if err := s.c.MouseButton(b, true, p); err != nil {
					t.Fatal(err)
				}
				if err := s.c.MouseButton(b, false, p); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.c.MouseWheel(-120, false, p); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-s.done:
				t.Fatalf("Run returned %v after mouse input", err)
			case <-time.After(500 * time.Millisecond):
			}
		})
	}
}

var queryPointerBin struct {
	once sync.Once
	path string
	err  error
}

// queryPointer runs testdata/querypointer in the container.
func queryPointer(t *testing.T) string {
	t.Helper()
	q := &queryPointerBin
	q.once.Do(func() {
		arch, err := exec.Command(engine, "exec", rdpContainer, "uname", "-m").Output()
		if err != nil {
			q.err = err
			return
		}
		goarch := map[string]string{"aarch64": "arm64", "x86_64": "amd64"}[strings.TrimSpace(string(arch))]
		if goarch == "" {
			goarch = runtime.GOARCH
		}
		dir, err := os.MkdirTemp("", "querypointer")
		if err != nil {
			q.err = err
			return
		}
		bin := filepath.Join(dir, "querypointer")
		build := exec.Command("go", "build", "-o", bin, "./testdata/querypointer")
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			q.err = fmt.Errorf("%w: %s", err, out)
			return
		}
		if out, err := exec.Command(engine, "cp", bin, rdpContainer+":/tmp/querypointer").CombinedOutput(); err != nil {
			q.err = fmt.Errorf("%w: %s", err, out)
			return
		}
		q.path = "/tmp/querypointer"
	})
	if q.err != nil {
		t.Fatalf("querypointer: %v", q.err)
	}
	out, err := exec.Command(engine, "exec", rdpContainer, q.path).Output()
	if err != nil {
		t.Fatalf("querypointer: %v", err)
	}
	return string(out)
}

// appendRecord appends a frame to a recording: its length in four bytes,
// big-endian, then the frame.
func appendRecord(b, p []byte) []byte {
	n := len(p)
	b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return append(b, p...)
}

func saveTrace(t *testing.T, name string, trace []byte) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(trace)
	zw.Close()
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d bytes of server frames in %s (%d compressed)", len(trace), name, buf.Len())
}
