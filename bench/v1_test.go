// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/bench/internal/probe"
	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/config"
	sessions "changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// benchFile loads a v1 file of the bench stack, as docker-compose.yaml and
// docker-compose.bench.yaml give it to occamyd, and returns it with its
// connections by ID. Every connection names its backend, so that a change
// of a protocol's default backend does not move a series.
func benchFile(t *testing.T, name string) (*config.File, map[string]config.Connection) {
	t.Helper()
	raw, err := os.ReadFile("../docker/bench/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var named struct {
		Connections []struct {
			ID      string `yaml:"id"`
			Backend string `yaml:"backend"`
		} `yaml:"connections"`
	}
	if err := yaml.Unmarshal(raw, &named); err != nil {
		t.Fatal(err)
	}
	for _, c := range named.Connections {
		if c.Backend == "" {
			t.Errorf("%s: connection %q takes its protocol's default backend", name, c.ID)
		}
	}
	dir := t.TempDir() + "/"
	raw = []byte(strings.NewReplacer("/run/occamy/", dir, "/var/lib/occamy/", dir).Replace(string(raw)))
	for _, env := range []string{"OCCAMY_DEMO_VNC_PASSWORD", "OCCAMY_BENCH_SSH_PASSWORD", "OCCAMY_BENCH_RDP_PASSWORD"} {
		t.Setenv(env, "secret")
	}
	f, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	conns := map[string]config.Connection{}
	for _, c := range f.Connections {
		conns[c.ID] = c
	}
	return f, conns
}

// The bench stack's v1 files load, have every connection the v1 series
// open with the protocol and backend the series names, and the user the
// harness signs in as. A series that serves RDP runs it with GFX as it
// says, and every path to RDP asks for one colour depth, 32 bits, so that
// xrdp keeps one X session, as native asks without being told.
func TestBenchV1File(t *testing.T) {
	f, conns := benchFile(t, "occamy-v1.yaml")
	gfxFile, gfxConns := benchFile(t, "occamy-v1-gfx.yaml")
	for name, byProto := range v1Connections {
		file, in := f, conns
		if name == "v1-guacd-gfx" {
			file, in = gfxFile, gfxConns
		}
		want, _, _ := strings.Cut(strings.TrimPrefix(name, "v1-"), "-")
		for proto, id := range byProto {
			c, ok := in[id]
			if !ok || c.Protocol != proto || c.Backend != want {
				t.Errorf("series %s, %s: connection %q is %+v, want protocol %s with backend %s", name, proto, id, c, proto, want)
			}
		}
		id, ok := byProto["rdp"]
		if !ok {
			continue
		}
		path, ok := rdpPaths[name]
		if !ok {
			t.Errorf("series %s serves rdp, and rdpPaths does not say how", name)
		}
		gfx := !in[id].DisableGFX
		if want == config.BackendGuacd {
			gfx = file.Guacd.Params["rdp"]["disable-gfx"] == "false"
		}
		if gfx != path.gfx {
			t.Errorf("series %s runs with GFX %v, and rdpPaths says %v", name, gfx, path.gfx)
		}
		if d := file.Guacd.Params["rdp"]["color-depth"]; d != "32" {
			t.Errorf("series %s: guacd's color-depth %q, want 32", name, d)
		}
	}
	if len(rdpPaths) != 5 {
		t.Errorf("rdpPaths has %d series, want RDP's five", len(rdpPaths))
	}
	for _, proto := range []string{"ssh", "vnc", "rdp"} {
		if _, ok := v1Connections["v1-native"][proto]; !ok {
			t.Errorf("v1-native serves no %s", proto)
		}
	}
	// v1-native-w2 differs from v1-native in its client's window alone.
	if !maps.Equal(v1Connections["v1-native-w2"], v1Connections["v1-native"]) || rdpPaths["v1-native-w2"].gfx != rdpPaths["v1-native"].gfx {
		t.Errorf("v1-native-w2 opens %v with GFX %v, want v1-native's %v with GFX %v",
			v1Connections["v1-native-w2"], rdpPaths["v1-native-w2"].gfx, v1Connections["v1-native"], rdpPaths["v1-native"].gfx)
	}
	if rdpPaths["v1-native-w2"].verdict {
		t.Error("v1-native-w2 counts for the targets, but its window is not what ships")
	}
	for name, w := range v1Windows {
		if _, ok := v1Connections[name]; !ok || w == 0 {
			t.Errorf("v1Windows gives %s a window of %d, want a v1 series and a window", name, w)
		}
	}
	// The bench desktop's xrdp speaks TLS without NLA, with a certificate
	// it made itself.
	for _, series := range []string{"v1-native", "v1-native-nogfx"} {
		if c := conns[v1Connections[series]["rdp"]]; c.Security != "tls" || !c.TLS.InsecureSkipVerify {
			t.Errorf("%s has security %q and tls %+v, want tls without a certificate check", series, c.Security, c.TLS)
		}
	}
	for _, file := range []*config.File{f, gfxFile} {
		if len(file.Auth.Users) != 1 || file.Auth.Users[0].Name != v1Account.name ||
			bcrypt.CompareHashAndPassword([]byte(file.Auth.Users[0].PasswordHash), []byte(v1Account.secret)) != nil {
			t.Errorf("users %+v, want %s with the harness's password", file.Auth.Users, v1Account.name)
		}
	}
}

// occamy-v1-gfx serves its own file, on the port the harness and make
// bench take by default, and only on the loopback interface, since the
// demo user's password is public; so is its debug listener, which has no
// authentication, on the port make bench-compare reads it from.
func TestBenchGFXService(t *testing.T) {
	raw, err := os.ReadFile("../docker/docker-compose.bench.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Ports, Volumes []string
		}
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	svc := compose.Services["occamy-v1-gfx"]
	if want := []string{"127.0.0.1:${OCCAMY_V1_GFX_PORT:-5640}:5637", "127.0.0.1:${OCCAMY_V1_GFX_DEBUG_PORT:-5641}:5638"}; !slices.Equal(svc.Ports, want) {
		t.Errorf("ports %q, want %q", svc.Ports, want)
	}
	if want := []string{"./bench/occamy-v1-gfx.yaml:/occamy/v1.yaml:ro"}; !slices.Equal(svc.Volumes, want) {
		t.Errorf("volumes %q, want %q", svc.Volumes, want)
	}
	if !slices.Contains(services, "occamy-v1-gfx") {
		t.Error("the harness does not sample occamy-v1-gfx's CPU")
	}
}

// The bench desktop has the demo's reconnection script. Every RDP path of
// a series shares one X session, and xrdp 0.10.1's channel server can
// exit as a connection closes, so without the script every later
// connection stays black for the 10 s that xrdp spends trying to reach
// it, and G7 measures that wait instead of the path, for a row that
// depends on which path ran before it.
func TestBenchDesktopReconnectScript(t *testing.T) {
	raw, err := os.ReadFile("../docker/bench/desktop/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	const line = "COPY docker/demo/rdp/reconnectwm.sh /etc/xrdp/reconnectwm.sh"
	if !slices.Contains(strings.Split(string(raw), "\n"), line) {
		t.Errorf("docker/bench/desktop/Dockerfile has no line %q", line)
	}
}

// The decoders read the codec vectors as the protocol says: lossless
// ones exactly, lossy ones at their min_psnr, and invalid ones refused.
func TestDecoders(t *testing.T) {
	b, err := os.ReadFile("../protocol/testdata/codecs.json")
	if err != nil {
		t.Fatal(err)
	}
	type vector struct {
		Name          string
		Codec         protocol.Codec
		Width, Height int
		Pixels        string
		Payload       string
		Exact         bool
		MinPSNR       float64 `json:"min_psnr"`
	}
	var file struct{ Vectors, Invalid []vector }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	var spent atomic.Int64
	byCodec := map[protocol.Codec]client.Decoder{}
	for _, d := range decoders(&spent) {
		byCodec[d.Codec()] = d
	}
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	checked := 0
	for _, v := range file.Vectors {
		d, ok := byCodec[v.Codec]
		if !ok {
			continue // raw, which the client core decodes itself
		}
		want := unhex(v.Pixels)
		if v.Codec == protocol.CodecWebP && !opaque(want) {
			// Lossless WebP is a cursor, which occamyd sends only to a
			// client that lists neither deflate nor raw, unlike this one.
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			got := make([]byte, 4*v.Width*v.Height)
			if err := d.Decode(got, v.Width, v.Height, unhex(v.Payload)); err != nil {
				t.Fatal(err)
			}
			if v.Exact {
				if !bytes.Equal(got, want) {
					t.Errorf("pixels\n%x\nwant\n%x", got, want)
				}
				return
			}
			if p := psnr(got, want); p < v.MinPSNR {
				t.Errorf("PSNR %.1f dB, want at least %g", p, v.MinPSNR)
			}
			// A rectangle other than the payload's is refused before
			// anything is decoded.
			if err := d.Decode(make([]byte, 4*(v.Width+1)*v.Height), v.Width+1, v.Height, unhex(v.Payload)); err == nil {
				t.Errorf("a %d×%d payload decoded into %d×%d", v.Width, v.Height, v.Width+1, v.Height)
			}
		})
		checked++
	}
	if checked < 10 {
		t.Errorf("checked %d vectors", checked)
	}
	for _, v := range file.Invalid {
		t.Run("invalid/"+v.Name, func(t *testing.T) {
			if err := byCodec[v.Codec].Decode(make([]byte, 4*v.Width*v.Height), v.Width, v.Height, unhex(v.Payload)); err == nil {
				t.Error("decoded")
			}
		})
	}
	if spent.Load() <= 0 {
		t.Error("no decoding time counted")
	}
}

func opaque(pix []byte) bool {
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return false
		}
	}
	return true
}

// psnr compares the colour channels, as internal/encoder's golden test
// does.
func psnr(a, b []byte) float64 {
	var sum float64
	n := 0
	for i := 0; i < len(a); i += 4 {
		for k := range 3 {
			d := float64(a[i+k]) - float64(b[i+k])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}

// A desktop key carries the HID usage of its key on a US layout, as the
// web client's KeyboardEvent.code gives it.
func TestHIDUsage(t *testing.T) {
	for keysym, want := range map[int]uint16{
		'a': 0x04, 'z': 0x1d, '1': 0x1e, '9': 0x26, '0': 0x27, ' ': 0x2c,
		keyReturn: 0x28, keyEscape: 0x29, keyF1: 0x3a, keyF2: 0x3b, keyF6: 0x3f, keyF1 + 11: 0x45,
		'A': 0, keyF1 + 12: 0, 0x20ac: 0,
	} {
		if got := hidUsage(keysym); got != want {
			t.Errorf("hidUsage(%#x) = %#x, want %#x", keysym, got, want)
		}
	}
}

// Over v1, a desktop's keys reach the backend with their physical key.
func TestV1KeyCodes(t *testing.T) {
	b := &keyLog{painter: newSwatch()}
	s := v1Rig(t, b)
	for _, k := range []int{'a', keyF2} {
		if err := s.key(k); err != nil {
			t.Fatal(err)
		}
	}
	want := []backend.Key{{Down: true, Keysym: 'a', Code: 0x04}, {Keysym: 'a', Code: 0x04}, {Down: true, Keysym: keyF2, Code: 0x3b}, {Keysym: keyF2, Code: 0x3b}}
	deadline := time.Now().Add(5 * time.Second)
	for len(b.got()) < len(want) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.got(); !slices.Equal(got, want) {
		t.Errorf("the backend got %+v, want %+v", got, want)
	}
}

// keyLog is a painter that records the keys it gets.
type keyLog struct {
	*painter
	mu   sync.Mutex
	keys []backend.Key
}

func (b *keyLog) Input(ev backend.InputEvent) error {
	if k, ok := ev.(backend.Key); ok {
		b.mu.Lock()
		b.keys = append(b.keys, k)
		b.mu.Unlock()
	}
	return b.painter.Input(ev)
}

func (b *keyLog) got() []backend.Key {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.keys)
}

func TestTermBytes(t *testing.T) {
	for keysym, want := range map[int]string{'a': "a", ' ': " ", keyReturn: "\r", keyEscape: "\x1b"} {
		if got, ok := termBytes(keysym); !ok || string(got) != want {
			t.Errorf("termBytes(%#x) = %q, %v, want %q", keysym, got, ok, want)
		}
	}
	if _, ok := termBytes(keyF1); ok {
		t.Error("F1 has terminal bytes")
	}
}

// rig is how v1RigWith serves a session.
type rig struct {
	// display shows the client's frames; at once if zero.
	display display
	// wrap, if set, wraps the client's end of the pipe.
	wrap func(pipeEnd) client.Conn
	// first is what the session's first frames should show (shows).
	first marker
}

// v1Rig serves backend b to a v1 session of the harness in process: the
// session core and protocol v1's stream on one end of a pipe, the client
// core on the other.
func v1Rig(t *testing.T, b backend.Backend) *session {
	t.Helper()
	return v1RigWith(t, b, rig{})
}

func v1RigWith(t *testing.T, b backend.Backend, o rig) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, owner, err := sessions.Start(ctx, sessions.Config{Backend: b, TileSize: 64})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	p := newPipe()
	s := newSession(time.Now())
	s.expect(o.first)
	opts := client.Options{Width: width, Height: height, Cols: 80, Rows: 24, Decoders: decoders(&s.decode)}
	served := make(chan error, 1)
	go func() { served <- stream.Serve(ctx, p.end(1), owner, opts.Hello(), stream.Config{SessionID: "s1"}) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		p.close()
		<-served
	})
	var c client.Conn = p.end(0)
	if o.wrap != nil {
		c = o.wrap(p.end(0))
	}
	cl, err := client.New(ctx, c, opts)
	if err != nil {
		t.Fatal(err)
	}
	s.startV1(cl, o.display, nil)
	return s
}

// The probe works over v1: keys reach the backend, and the frame that
// shows a key's colour is marked.
func TestV1Probe(t *testing.T) {
	s := v1Rig(t, newSwatch())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ready(ctx, s); err != nil {
		t.Fatal(err)
	}
	lat, err := probeLatency(ctx, s, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lat) != 5 {
		t.Errorf("%d latencies", len(lat))
	}
	if s.drawn.Load() < 6 {
		t.Errorf("%d frames drawn", s.drawn.Load())
	}
	if err := s.close(); err != nil {
		t.Error(err)
	}
}

// A frame that was ready before the reader came back for it counts from
// then, plus what applying it took; one that the reader waited for counts
// from when Next delivered it.
func TestCounted(t *testing.T) {
	at := func(ms int) time.Time { return time.Unix(1000, 0).Add(time.Duration(ms) * time.Millisecond) }
	for _, c := range []struct {
		name               string
		ready, called, now int
		want               int
	}{
		{"came during the tick", 10, 50, 51, 11},
		{"came as the reader returned", 50, 50, 51, 51},
		{"the reader waited for it", 60, 50, 61, 61},
	} {
		if got := counted(at(c.called), at(c.now), at(c.ready)); !got.Equal(at(c.want)) {
			t.Errorf("%s: %v after the start, want %d ms", c.name, got.Sub(at(0)), c.want)
		}
	}
}

// A probe key's latency ends at the frame that shows its colour where
// the probe looks, not at an earlier frame: one in another colour, or
// one that changed only pixels elsewhere while the swatch's centre
// still showed the colour.
func TestV1ProbeMarksOnlyItsFrame(t *testing.T) {
	const lag = 50 * time.Millisecond
	near := func(c color.RGBA) color.RGBA { // another colour that matches c
		for _, v := range []*uint8{&c.R, &c.G, &c.B} {
			if *v >= 0x80 {
				*v -= 16
			} else {
				*v += 16
			}
		}
		return c
	}
	// Every step changes the swatch's pixels, so that a server which
	// sends only what changed still sends it.
	b := newSwatch()
	b.script['a'] = func(sink backend.Sink) {
		fill(sink, probe.Swatch, probe.Colour('b'))
		time.Sleep(lag)
		fill(sink, probe.Swatch, probe.Colour('a'))
	}
	b.script['p'] = func(sink backend.Sink) { fill(sink, probe.Swatch, near(probe.Colour('a'))) }
	b.script['q'] = func(sink backend.Sink) {
		fill(sink, image.Rect(256, 256, 320, 320), color.RGBA{0xff, 0, 0, 0xff})
		time.Sleep(lag)
		fill(sink, probe.Swatch, probe.Colour('a'))
	}
	s := v1Rig(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.settle(ctx, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	latency := func(key int) time.Duration {
		t.Helper()
		s.setMarker(probeMarker('a'))
		t0 := time.Now()
		if err := s.key(key); err != nil {
			t.Fatal(err)
		}
		f, err := s.wait(ctx, t0, func(f frame) bool { return f.marked })
		if err != nil {
			t.Fatal(err)
		}
		return f.at.Sub(t0)
	}
	if d := latency('a'); d < lag {
		t.Errorf("a key that paints another colour first marked after %v, before its colour after %v", d, lag)
	}
	latency('p') // the centre shows a colour that matches a's
	if d := latency('q'); d < lag {
		t.Errorf("a key that paints elsewhere first marked after %v, before the swatch after %v", d, lag)
	}
}

// A v1 terminal's frames draw when they carry bytes, and T2's marker
// finds its magenta line in the emulated terminal.
func TestV1Terminal(t *testing.T) {
	s := v1Rig(t, newShell(0))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.settle(ctx, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	s.setMarker(magenta)
	t0 := time.Now()
	if err := s.key('a'); err != nil {
		t.Fatal(err)
	}
	f, err := s.wait(ctx, t0, func(frame) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if f.marked {
		t.Error("the echo of a key is marked")
	}
	t0 = time.Now()
	if err := s.key(keyReturn); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wait(ctx, t0, func(f frame) bool { return f.marked }); err != nil {
		t.Fatal(err)
	}
	if s.decode.Load() <= 0 {
		t.Error("no emulation time counted")
	}
	if err := s.close(); err != nil {
		t.Error(err)
	}
}

// A terminal frame draws only if it carried bytes: the blank terminal
// that the session core serves before the backend connected, whose
// snapshot is empty, draws nothing.
func TestV1TerminalBlank(t *testing.T) {
	var fl *frameLog
	s := v1RigWith(t, newShell(time.Hour), rig{wrap: func(e pipeEnd) client.Conn {
		fl = newFrameLog(e)
		return fl
	}})
	// The reader acknowledges a frame once it handled it.
	deadline := time.Now().Add(5 * time.Second)
	for len(fl.acked()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the blank terminal's frame was not acknowledged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.drawn.Load(); n != 0 {
		t.Errorf("%d frames drew before the shell printed", n)
	}
}

// A terminal's G7 ends at the first frame that shows the shell's prompt:
// not the blank terminal the session core serves before the backend
// connected, nor the clear screen that resizing a blank terminal sends,
// as the SSH backend resizes its terminal before the shell prints, nor
// what the shell prints before its prompt.
func TestV1TerminalFirstFrame(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	const delay = 300 * time.Millisecond
	for name, set := range map[string]func(*shell){
		"blank":   func(*shell) {},
		"resized": func(b *shell) { b.resize = 100 * time.Millisecond },
		"banner":  func(b *shell) { b.banner = 100 * time.Millisecond },
	} {
		t.Run(name, func(t *testing.T) {
			r := testRunner(t, series{Name: "v1-native", open: func(context.Context, string, string) (*session, error) {
				b := newShell(delay)
				set(b)
				return v1RigWith(t, b, rig{first: shows("ssh")}), nil
			}})
			res := r.connect(context.Background(), "ssh")
			if res.Error != "" {
				t.Fatal(res.Error)
			}
			if first := time.Duration(res.FirstFrame.P50 * float64(time.Millisecond)); first < delay {
				t.Errorf("the first frame drew after %v, before the prompt after %v", first, delay)
			}
		})
	}
}

// A desktop's G7 ends at the first frame that shows the wallpaper, not at
// the frame that sizes the screen.
func TestV1DesktopFirstFrame(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	const delay = 300 * time.Millisecond
	r := testRunner(t, series{Name: "v1-native", open: func(context.Context, string, string) (*session, error) {
		return v1RigWith(t, &slowDesktop{newSwatch(), delay}, rig{first: shows("vnc")}), nil
	}})
	res := r.connect(context.Background(), "vnc")
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if first := time.Duration(res.FirstFrame.P50 * float64(time.Millisecond)); first < delay {
		t.Errorf("the first frame drew after %v, before the wallpaper after %v", first, delay)
	}
}

// A v1 session counts a scene's frames by their stamps: once each, not
// for frames that draw elsewhere.
func TestV1Stamps(t *testing.T) {
	stamp := func(n uint16) func(backend.Sink) {
		return func(sink backend.Sink) {
			img := sink.Lock(probe.StampArea)
			defer sink.Unlock(probe.StampArea)
			paintStamp(img, n)
		}
	}
	b := newSwatch()
	b.script['p'] = stamp(7)
	b.script['q'] = stamp(8)
	s := v1Rig(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.settle(ctx, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		key  int
		want int64
	}{{'p', 1}, {'a', 1}, {'q', 2}, {'b', 2}} {
		if err := s.key(c.key); err != nil {
			t.Fatal(err)
		}
		if err := s.settle(ctx, 200*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if got := s.sceneFrames.Load(); got != c.want {
			t.Errorf("after key %c, %d frames of the scene, want %d", c.key, got, c.want)
		}
	}
	// Each was shown once, when the frame that drew it counts from.
	if shown := s.shownSince(0); len(shown) != 2 || !shown[1].After(shown[0]) || shown[0].Before(s.start) {
		t.Errorf("the scene's frames shown at %v, want two in order after the start", shown)
	}
	if err := s.close(); err != nil {
		t.Error(err)
	}
}

// At a refresh rate, a graphical frame counts from when Next delivered
// it, and is acknowledged at the screen's next tick, as the web client
// acknowledges it after the next animation frame; at 0, at once.
func TestV1Display(t *testing.T) {
	// run shows a backend that repaints at an interval that is not a
	// multiple of the refresh's, so that frames arrive at every phase of
	// it, while the client waits for them. It takes 30 frames, about
	// 0.7 s on an idle host, and waits longer on a loaded one.
	run := func(t *testing.T, d display) ([]frame, *frameLog) {
		t.Helper()
		var fl *frameLog
		s := v1RigWith(t, &flicker{every: 23 * time.Millisecond}, rig{display: d, wrap: func(e pipeEnd) client.Conn {
			fl = newFrameLog(e)
			return fl
		}})
		var fs []frame
		timeout := time.After(20 * time.Second)
		for len(fs) < 30 {
			select {
			case f := <-s.frames:
				fs = append(fs, f)
			case <-timeout:
				t.Fatalf("%d frames in 20 s", len(fs))
			}
		}
		return fs, fl
	}
	t.Run("60 Hz", func(t *testing.T) {
		d := newDisplay(60)
		fs, fl := run(t, d)
		phase := func(at time.Time) time.Duration { return at.Sub(displayOrigin) % d.period }
		var acks, stamps []time.Duration
		for _, at := range fl.acked() {
			acks = append(acks, phase(at))
		}
		for _, f := range fs {
			stamps = append(stamps, phase(f.at))
		}
		if p := medianDuration(acks); p > d.period/4 {
			t.Errorf("frames acknowledged a median %v after a tick, want on ticks", p)
		}
		off := 0
		for _, p := range stamps {
			if p > d.period/4 {
				off++
			}
		}
		t.Logf("%d acknowledgements a median %v after a tick; %d of %d frames counted more than %v after one",
			len(acks), medianDuration(acks), off, len(stamps), d.period/4)
		if !slices.ContainsFunc(stamps, func(p time.Duration) bool { return p != 0 }) || off < len(stamps)/4 {
			t.Errorf("%d of %d frames counted more than %v after a tick, want them counted when Next delivered them", off, len(stamps), d.period/4)
		}
	})
	t.Run("at once", func(t *testing.T) {
		d := newDisplay(0)
		if d != (display{}) {
			t.Fatalf("newDisplay(0) = %+v", d)
		}
		_, fl := run(t, d)
		if m := medianDuration(fl.delays()); m > 4*time.Millisecond {
			t.Errorf("frames acknowledged a median %v after their end, want at once", m)
		}
	})
}

// A terminal's frames are acknowledged at once, whatever the refresh,
// as the web client's terminal acknowledges them once its emulator
// took their bytes.
func TestV1TerminalAck(t *testing.T) {
	var fl *frameLog
	s := v1RigWith(t, newShell(0), rig{display: newDisplay(60), wrap: func(e pipeEnd) client.Conn {
		fl = newFrameLog(e)
		return fl
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.settle(ctx, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		t0 := time.Now()
		if err := s.key('a' + i%26); err != nil {
			t.Fatal(err)
		}
		if _, err := s.wait(ctx, t0, func(frame) bool { return true }); err != nil {
			t.Fatal(err)
		}
		// Keys at every phase of the refresh.
		time.Sleep(time.Duration(3+i*7%13) * time.Millisecond)
	}
	d := fl.delays()
	if len(d) < 20 {
		t.Fatalf("%d frames acknowledged", len(d))
	}
	t.Logf("%d frames acknowledged a median %v after their end", len(d), medianDuration(d))
	if m := medianDuration(d); m > 4*time.Millisecond {
		t.Errorf("a terminal's frames acknowledged a median %v after their end, want at once", m)
	}
}

// frameLog records when the client read each frame's end, and when it
// sent each acknowledgement, by frame ID.
type frameLog struct {
	pipeEnd
	mu         sync.Mutex
	ends, acks map[uint32]time.Time
}

func newFrameLog(e pipeEnd) *frameLog {
	return &frameLog{pipeEnd: e, ends: map[uint32]time.Time{}, acks: map[uint32]time.Time{}}
}

func (l *frameLog) ReadMessage(ctx context.Context) ([]byte, error) {
	b, err := l.pipeEnd.ReadMessage(ctx)
	if err == nil {
		if m, _ := protocol.Decode(b, protocol.ServerMessages(), protocol.Limits{}); m != nil {
			if e, ok := m.(*protocol.FrameEnd); ok {
				l.note(l.ends, e.FrameID)
			}
		}
	}
	return b, err
}

func (l *frameLog) WriteMessage(ctx context.Context, b []byte) error {
	if m, _ := protocol.Decode(b, protocol.ClientMessages(), protocol.Limits{}); m != nil {
		if a, ok := m.(*protocol.FrameAck); ok {
			l.note(l.acks, a.FrameID)
		}
	}
	return l.pipeEnd.WriteMessage(ctx, b)
}

func (l *frameLog) note(m map[uint32]time.Time, id uint32) {
	now := time.Now()
	l.mu.Lock()
	m[id] = now
	l.mu.Unlock()
}

// acked returns when each acknowledgement was sent.
func (l *frameLog) acked() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Collect(maps.Values(l.acks))
}

// delays returns how long after its end each acknowledged frame was
// acknowledged.
func (l *frameLog) delays() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var d []time.Duration
	for id, at := range l.acks {
		if end, ok := l.ends[id]; ok {
			d = append(d, at.Sub(end))
		}
	}
	return d
}

func medianDuration(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(d))
	return s[len(s)/2]
}

func TestDisplayNext(t *testing.T) {
	d := display{period: 10 * time.Millisecond}
	for after, want := range map[time.Duration]time.Duration{
		0:                     10 * time.Millisecond,
		time.Millisecond:      10 * time.Millisecond,
		25 * time.Millisecond: 30 * time.Millisecond,
		30 * time.Millisecond: 40 * time.Millisecond,
	} {
		if got := d.next(displayOrigin.Add(after)).Sub(displayOrigin); got != want {
			t.Errorf("a frame at %v is shown at %v, want %v", after, got, want)
		}
	}
	now := time.Now()
	if got := (display{}).next(now); !got.Equal(now) {
		t.Errorf("at once shows a frame at %v, want %v", got, now)
	}
}

// An input fails after inputTimeout when the network takes nothing.
func TestV1KeyTimeout(t *testing.T) {
	defer func(d time.Duration) { inputTimeout = d }(inputTimeout)
	inputTimeout = 100 * time.Millisecond
	var stall *stallConn
	s := v1RigWith(t, newSwatch(), rig{wrap: func(e pipeEnd) client.Conn {
		stall = &stallConn{pipeEnd: e}
		return stall
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.wait(ctx, s.start, func(frame) bool { return true }); err != nil {
		t.Fatal(err)
	}
	stall.stalled.Store(true)
	done := make(chan error, 1)
	go func() {
		for range 1 << 20 {
			if err := s.key('a'); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("keys to a stalled network: %v, want a deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a key to a stalled network does not return")
	}
}

// stallConn stops taking messages once stalled.
type stallConn struct {
	pipeEnd
	stalled atomic.Bool
}

func (c *stallConn) WriteMessage(ctx context.Context, b []byte) error {
	if c.stalled.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.p.done:
			return io.ErrClosedPipe
		}
	}
	return c.pipeEnd.WriteMessage(ctx, b)
}

// openV1 bounds the handshake, and ends a session whose welcome the
// client refused rather than leave it for the owner grace.
func TestOpenV1(t *testing.T) {
	ended := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"t"}`)
	})
	mux.HandleFunc("GET /api/v1/connections/refused/stream", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		// No codec: a graphical session the client cannot show.
		welcome := protocol.Welcome{ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical, SessionID: "s1"}
		if err := ws.Write(r.Context(), websocket.MessageBinary, welcome.Append(nil)); err != nil {
			return
		}
		for {
			if _, _, err := ws.Read(r.Context()); err != nil {
				return
			}
		}
	})
	release := make(chan struct{}) // lets a hung handshake go once the test ends
	mux.HandleFunc("GET /api/v1/connections/hung/stream", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	const promptAfter = 300 * time.Millisecond
	mux.HandleFunc("GET /api/v1/connections/shell/stream", func(w http.ResponseWriter, r *http.Request) {
		hello, err := protocol.DecodeHelloQuery(r.URL.Query().Get(protocol.HelloParameter))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		b := newShell(promptAfter)
		b.banner = 50 * time.Millisecond
		srv, owner, err := sessions.Start(r.Context(), sessions.Config{Backend: b, TileSize: 64})
		if err != nil {
			return
		}
		defer srv.Close()
		_ = stream.Serve(r.Context(), wsEnd{ws}, owner, hello, stream.Config{SessionID: "s2"})
	})
	windows := make(chan uint8, 1)
	mux.HandleFunc("GET /api/v1/connections/vnc-native/stream", func(w http.ResponseWriter, r *http.Request) {
		hello, err := protocol.DecodeHelloQuery(r.URL.Query().Get(protocol.HelloParameter))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		windows <- hello.Capabilities.FrameWindow
		http.Error(w, "no remote in this test", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer t" {
			ended <- r.PathValue("id")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)
	addr := strings.TrimPrefix(srv.URL, "http://")

	t.Run("refused", func(t *testing.T) {
		if _, err := openV1(map[string]string{"vnc": "refused"}, display{}, 0)(context.Background(), addr, "vnc"); err == nil {
			t.Fatal("a session with no codec opened")
		}
		select {
		case id := <-ended:
			if id != "s1" {
				t.Errorf("ended session %q, want s1", id)
			}
		case <-time.After(5 * time.Second):
			t.Error("the refused session was not ended")
		}
	})
	t.Run("hung", func(t *testing.T) {
		defer func(d time.Duration) { openTimeout = d }(openTimeout)
		openTimeout = 200 * time.Millisecond
		done := make(chan error, 1)
		go func() {
			_, err := openV1(map[string]string{"vnc": "hung"}, display{}, 0)(context.Background(), addr, "vnc")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Error("a hung handshake opened")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("opening does not return when the handshake hangs")
		}
	})
	// A session looks at its first frames for what its protocol shows.
	t.Run("shows", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s, err := openV1(map[string]string{"ssh": "shell"}, display{}, 0)(ctx, addr, "ssh")
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		f, err := s.wait(ctx, s.start, func(f frame) bool { return f.marked })
		if err != nil {
			t.Fatal(err)
		}
		if d := f.at.Sub(s.start); d < promptAfter {
			t.Errorf("marked a frame after %v, before the prompt after %v", d, promptAfter)
		}
	})
	// v1-native's client offers the client core's default window,
	// protocol.MaxFrameWindow, within which the server sizes its own, and
	// v1-native-w2's offers 2, on the same connection.
	t.Run("window", func(t *testing.T) {
		all := allSeries(endpoints{v1: addr}, display{})
		for name, want := range map[string]uint8{"v1-native": protocol.MaxFrameWindow, "v1-native-w2": 2} {
			i := slices.IndexFunc(all, func(s series) bool { return s.Name == name })
			if i < 0 {
				t.Fatalf("no series %s", name)
			}
			if all[i].target != addr {
				t.Errorf("%s reaches %q, want occamy-v1's %q", name, all[i].target, addr)
			}
			if _, err := all[i].open(context.Background(), addr, "vnc"); err == nil {
				t.Errorf("%s opened a session that the test refuses", name)
			}
			select {
			case got := <-windows:
				if got != want {
					t.Errorf("%s offers a frame window of %d, want %d", name, got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not ask for a stream", name)
			}
		}
	})
}

// wsEnd is the server's end of a stream on a WebSocket.
type wsEnd struct{ ws *websocket.Conn }

func (e wsEnd) ReadMessage(ctx context.Context) ([]byte, error) {
	_, b, err := e.ws.Read(ctx)
	return b, err
}

func (e wsEnd) WriteMessage(ctx context.Context, b []byte) error {
	return e.ws.Write(ctx, websocket.MessageBinary, b)
}

func (e wsEnd) Close(code protocol.CloseCode, reason string) error {
	return e.ws.Close(websocket.StatusCode(code), reason)
}

// painter is a graphical backend that runs a script when a key is
// pressed.
type painter struct {
	input  chan backend.InputEvent
	script map[uint32]func(sink backend.Sink)
}

// newSwatch paints the probe's swatch as the scene program does: grey
// on F2, and the colour of each letter.
func newSwatch() *painter {
	p := &painter{input: make(chan backend.InputEvent, 64), script: map[uint32]func(backend.Sink){}}
	p.script[keyF2] = func(sink backend.Sink) { fill(sink, probe.Swatch, color.RGBA{0x80, 0x80, 0x80, 0xff}) }
	for k := 'a'; k <= 'z'; k++ {
		c := probe.Colour(k)
		p.script[uint32(k)] = func(sink backend.Sink) { fill(sink, probe.Swatch, c) }
	}
	return p
}

func (*painter) Class() backend.Class  { return backend.Graphical }
func (*painter) Demand(backend.Demand) {}
func (b *painter) Input(ev backend.InputEvent) error {
	b.input <- ev
	return nil
}

func (b *painter) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(width, height)
	fill(sink, image.Rect(0, 0, width, height), color.RGBA{A: 0xff})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-b.input:
			if k, ok := ev.(backend.Key); ok && k.Down && b.script[k.Keysym] != nil {
				b.script[k.Keysym](sink)
			}
		}
	}
}

// slowDesktop is a graphical backend that sizes the screen at once, in
// black, and shows the wallpaper after delay.
type slowDesktop struct {
	*painter
	delay time.Duration
}

func (b *slowDesktop) Run(ctx context.Context, sink backend.Sink) error {
	all := image.Rect(0, 0, width, height)
	sink.Resize(width, height)
	fill(sink, all, color.RGBA{A: 0xff})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.delay):
	}
	img := sink.Lock(all)
	paintWallpaper(img)
	sink.Unlock(all)
	<-ctx.Done()
	return ctx.Err()
}

// flicker is a graphical backend that repaints the swatch every so
// often, in turns of two colours.
type flicker struct{ every time.Duration }

func (*flicker) Class() backend.Class           { return backend.Graphical }
func (*flicker) Demand(backend.Demand)          {}
func (*flicker) Input(backend.InputEvent) error { return nil }

func (b *flicker) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(width, height)
	tick := time.NewTicker(b.every)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			fill(sink, probe.Swatch, probe.Colour('a'+rune(i%2)))
		}
	}
}

func fill(sink backend.Sink, r image.Rectangle, c color.RGBA) {
	img := sink.Lock(r)
	defer sink.Unlock(r)
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// testPrompt is the prompt of a shell, as the demo shell's looks.
const testPrompt = promptStart + "bench:~$ "

// shell is a terminal backend that echoes its input, and prints T2's
// magenta line on Return. It prints its prompt after delay, as a remote
// shell answers some time after the session started. With resize, it
// resizes its blank terminal after resize, as the SSH backend does once
// the shell's channel opens, and with banner it prints a line after
// banner, as sshd prints the message of the day before the prompt.
type shell struct {
	input                 chan backend.InputEvent
	delay, resize, banner time.Duration
}

func newShell(delay time.Duration) *shell {
	return &shell{input: make(chan backend.InputEvent, 64), delay: delay}
}

func (*shell) Class() backend.Class  { return backend.Terminal }
func (*shell) Demand(backend.Demand) {}
func (b *shell) Input(ev backend.InputEvent) error {
	b.input <- ev
	return nil
}

func (b *shell) Run(ctx context.Context, sink backend.Sink) error {
	start := time.Now()
	after := func(d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(start.Add(d))):
			return nil
		}
	}
	if b.resize > 0 {
		if err := after(b.resize); err != nil {
			return err
		}
		sink.Resize(100, 30)
	}
	if b.banner > 0 {
		if err := after(b.banner); err != nil {
			return err
		}
		sink.Resize(80, 24)
		sink.TermData([]byte("Linux bench 6.12\r\n"))
	}
	if err := after(b.delay); err != nil {
		return err
	}
	sink.Resize(80, 24)
	sink.TermData([]byte(testPrompt))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-b.input:
			in, ok := ev.(backend.TermInput)
			switch {
			case !ok:
			case bytes.Equal(in.Data, []byte{'\r'}):
				sink.TermData([]byte("\r\n\x1b[48;5;201m        \x1b[0m\r\n" + testPrompt))
			default:
				sink.TermData(in.Data)
			}
		}
	}
}

// pipe joins two ends in process, each message a write and a read.
type pipe struct {
	q    [2]chan []byte // to end 0 and to end 1
	done chan struct{}
	once sync.Once
}

func newPipe() *pipe {
	return &pipe{q: [2]chan []byte{make(chan []byte, 4096), make(chan []byte, 4096)}, done: make(chan struct{})}
}

func (p *pipe) close() { p.once.Do(func() { close(p.done) }) }

func (p *pipe) end(i int) pipeEnd { return pipeEnd{p, i} }

type pipeEnd struct {
	p *pipe
	i int
}

func (e pipeEnd) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case b := <-e.p.q[e.i]:
		return b, nil
	case <-e.p.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e pipeEnd) WriteMessage(ctx context.Context, b []byte) error {
	select {
	case e.p.q[1-e.i] <- slices.Clone(b):
		return nil
	case <-e.p.done:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e pipeEnd) Close(protocol.CloseCode, string) error {
	e.p.close()
	return nil
}

// A scene ends once the screen shows the wallpaper, not once its key is
// on its way: a client that closes at once can lose the key.
func TestEndScene(t *testing.T) {
	defer func(d time.Duration) { keyWait = d }(keyWait)
	keyWait = 2 * time.Second
	t.Run("shown", func(t *testing.T) {
		p := newSwatch()
		p.script[keyEscape] = func(sink backend.Sink) {
			all := image.Rect(0, 0, width, height)
			img := sink.Lock(all)
			paintWallpaper(img)
			sink.Unlock(all)
		}
		if err := endScene(context.Background(), v1Rig(t, p)); err != nil {
			t.Error(err)
		}
	})
	t.Run("never shown", func(t *testing.T) {
		keyWait = 100 * time.Millisecond
		if err := endScene(context.Background(), v1Rig(t, newSwatch())); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%v, want the deadline", err)
		}
	})
}
