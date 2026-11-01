// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build integration

package vnc

// TestEncodings runs the backend against a real server, asking for one
// encoding at a time, through a proxy that records what the server
// sends. Point it at x11vnc as TestBenchDesktop, or at TigerVNC's Xvnc
// from the demo stack's image started with scene, which serves the same
// scene. From the repository's root:
//
//	podman build -t occamy-demo-tigervnc -f docker/demo/tigervnc/Dockerfile .
//	podman run -d --rm --name occamy-vnc-tigervnc -p 127.0.0.1:15900:5900 occamy-demo-tigervnc scene
//	OCCAMY_VNC_ADDR=127.0.0.1:15900 go test -tags integration -run TestEncodings ./internal/backend/vnc
//
// OCCAMY_VNC_ENCODINGS picks the encodings, from hextile, zrle, tight and
// tight-jpeg; all of them by default. With OCCAMY_VNC_RECORD=<server>,
// each session is written to the fuzz corpus as
// recorded-<server>-<encoding>, which needs a screen within the fuzzed
// decoder's limits: start either container with -e GEOMETRY=64x48.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

const (
	keyF6 = 0xffc3 // a static photograph
	// encQualityLevel6 asks for JPEG of quality level 6 of 0 to 9
	// (rfbproto, "JPEG Quality Level Pseudo-encoding"), which the backend
	// does not ask for itself.
	encQualityLevel6 = -32 + 6
)

// encodingTests are the encodings that TestEncodings asks for, each with
// the kinds of rectangle that a session of it must hold.
var encodingTests = []struct {
	name      string
	encodings []int32
	first     kind
	last      kind
	lossy     bool
}{
	{"hextile", []int32{encHextile}, hextileRawTile, hextileColourTile, false},
	{"zrle", []int32{encZRLE}, zrleRawTile, zrlePaletteRLETile, false},
	{"tight", []int32{encTight}, tightFillRect, tightGradientRect, false},
	{"tight-jpeg", []int32{encTight, encQualityLevel6}, tightJPEGRect, tightJPEGRect, true},
}

func TestEncodings(t *testing.T) {
	addr := os.Getenv("OCCAMY_VNC_ADDR")
	if addr == "" {
		t.Skip("OCCAMY_VNC_ADDR is not set")
	}
	pw := os.Getenv("OCCAMY_VNC_PASSWORD")
	if pw == "" {
		pw = "vncpassword"
	}
	only := os.Getenv("OCCAMY_VNC_ENCODINGS")
	server := os.Getenv("OCCAMY_VNC_RECORD")
	for _, tt := range encodingTests {
		if only != "" && !strings.Contains(","+only+",", ","+tt.name+",") {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			saved := encodings
			encodings = append(append([]int32{}, tt.encodings...), encCursor, encDesktopSize)
			t.Cleanup(func() { encodings = saved })

			rec := &recorder{}
			b := newBackend(t, Config{Addr: proxy(t, addr, rec), Password: pw})
			b.Demand(backend.Demand{Visible: true})
			sink := &backendtest.Sink{}
			r := start(t, b, sink)
			match := func(what string, ref *image.RGBA) {
				t.Helper()
				waitFor(t, what, func() bool {
					if tt.lossy {
						return near(sink.Pixels(), ref, 4)
					}
					return same(sink.Pixels(), ref)
				})
			}

			press := func(k uint32) {
				t.Helper()
				for _, down := range []bool{true, false} {
					if err := b.Input(backend.Key{Down: down, Keysym: k}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The scene starts idle, whatever an earlier test left.
			waitFor(t, "the first update", func() bool { return sink.Pixels() != nil })
			press(keyEscape)
			idle := stillFrame(t, addr, pw)
			match("the idle screen", idle)
			for _, k := range "abc" {
				press(uint32(k))
				want := swatchPalette[int(k)%len(swatchPalette)]
				waitFor(t, "the swatch of "+string(k), func() bool {
					px := sink.Pixels()
					return px != nil && closeColour(px.RGBAAt(swatchCentre.X, swatchCentre.Y), want, 16)
				})
			}
			press(keyF6)
			waitFor(t, "the photograph", func() bool { return !near(sink.Pixels(), idle, 4) })
			match("the photograph", stillFrame(t, addr, pw))
			press(keyEscape)
			match("the idle screen again", idle)

			if err := r.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
			checkSession(t, rec.bytes(), pw, tt.first, tt.last, tt.lossy, server, tt.name)
		})
	}
}

// stillFrame returns the reference frame once two in a row are the same,
// which they are when the scene has drawn the whole of a still picture.
func stillFrame(t *testing.T, addr, pw string) *image.RGBA {
	t.Helper()
	prev := referenceFrame(t, addr, pw)
	deadline := time.Now().Add(timeout)
	for {
		time.Sleep(100 * time.Millisecond)
		ref := referenceFrame(t, addr, pw)
		if same(ref, prev) {
			return ref
		}
		if time.Now().After(deadline) {
			t.Fatal("the screen does not stand still")
		}
		prev = ref
	}
}

// checkSession decodes what the server sent after the handshake, checks
// that it holds rectangles of the kinds first to last, and JPEG only if
// lossy, and writes it to the fuzz corpus if server is set.
func checkSession(t *testing.T, data []byte, pw string, first, last kind, lossy bool, server, name string) {
	t.Helper()
	in := bytes.NewReader(data)
	init, err := handshake(in, io.Discard, auth{password: func() (string, error) { return pw, nil }, hasPassword: true}, defaultLimits)
	if err != nil {
		t.Fatalf("the recording's handshake: %v", err)
	}
	msgs := data[len(data)-in.Len():]
	sink := &backendtest.Sink{}
	sink.Resize(init.size.X, init.size.Y)
	in = bytes.NewReader(msgs)
	d := newDecoder(context.Background(), in, sink, init.size, defaultLimits, discardLogger)
	d.resizeEvery = 0
	// The proxy may have cut the last message short when Run stopped, and
	// only the end of the recording may end a message.
	end := 0
	for {
		if _, err := d.next(); err != nil {
			ended := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
			if !ended || in.Len() > 0 {
				t.Fatalf("the recording, after %d of its %d bytes: %v", end, len(msgs), err)
			}
			break
		}
		end = len(msgs) - in.Len()
	}
	msgs = msgs[:end]
	n := 0
	for k := first; k <= last; k++ {
		n += d.enc.counts[k]
	}
	if n == 0 {
		t.Errorf("no %v to %v in the recording", first, last)
	}
	if !lossy && d.enc.counts[tightJPEGRect] > 0 {
		t.Errorf("%d JPEG rectangles without a quality level", d.enc.counts[tightJPEGRect])
	}
	var kinds []string
	for k, c := range d.enc.counts {
		if c > 0 {
			kinds = append(kinds, fmt.Sprintf("%v: %d", kind(k), c))
		}
	}
	t.Logf("screen %v, %d bytes of messages; %s", init.size, len(msgs), strings.Join(kinds, ", "))

	if server == "" {
		return
	}
	if fuzzLimits.check(init.size.X, init.size.Y) != nil {
		t.Fatalf("a screen of %v is beyond the fuzzed decoder's limits %+v", init.size, fuzzLimits)
	}
	seed := append(putUpdate(putDesktopSize(init.size.X, init.size.Y)), msgs...)
	path := filepath.Join("testdata", "fuzz", "FuzzServerMessages", "recorded-"+server+"-"+name)
	if err := os.WriteFile(path, fmt.Appendf(nil, "go test fuzz v1\n[]byte(%q)\n", seed), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

// recorder keeps what a server sends.
type recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *recorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.buf.Bytes())
}

// proxy forwards connections to upstream, and copies what the first one
// receives from it to rec.
func proxy(t *testing.T, upstream string, rec *recorder) string {
	var once sync.Once
	return listen(t, func(nc net.Conn) {
		defer nc.Close()
		up, err := net.DialTimeout("tcp", upstream, timeout)
		if err != nil {
			t.Errorf("proxy: %v", err)
			return
		}
		defer up.Close()
		var w io.Writer = nc
		once.Do(func() { w = io.MultiWriter(nc, rec) })
		done := make(chan struct{})
		go func() {
			defer close(done)
			io.Copy(up, nc)
			up.Close()
		}()
		io.Copy(w, up)
		nc.Close()
		<-done
	})
}

// near reports whether a is b within a mean difference of tol a channel,
// as a lossy encoding draws it.
func near(a, b *image.RGBA, tol int) bool {
	if a == nil || a.Rect != b.Rect {
		return false
	}
	var sum int
	for i := range a.Pix {
		sum += max(int(a.Pix[i])-int(b.Pix[i]), int(b.Pix[i])-int(a.Pix[i]))
	}
	return sum <= tol*len(a.Pix)
}

func closeColour(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) bool { return max(int(x)-int(y), int(y)-int(x)) <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && a.A == b.A
}
