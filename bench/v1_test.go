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
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// The bench stack's v1 file loads, has every connection the v1 series
// open with the protocol and backend the series names, and the user the
// harness signs in as. Every connection names its backend, so that a
// change of a protocol's default backend does not move a series.
func TestBenchV1File(t *testing.T) {
	raw, err := os.ReadFile("../docker/bench/occamy-v1.yaml")
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
			t.Errorf("connection %q takes its protocol's default backend", c.ID)
		}
	}
	// What docker-compose.yaml and docker-compose.bench.yaml give it.
	raw = []byte(strings.ReplaceAll(string(raw), "/run/occamy/", t.TempDir()+"/"))
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
	for name, byProto := range v1Connections {
		want := strings.TrimPrefix(name, "v1-")
		for proto, id := range byProto {
			c, ok := conns[id]
			if !ok || c.Protocol != proto || c.Backend != want {
				t.Errorf("series %s, %s: connection %q is %+v, want protocol %s with backend %s", name, proto, id, c, proto, want)
			}
		}
	}
	if len(f.Auth.Users) != 1 || f.Auth.Users[0].Name != v1Account.name ||
		bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte(v1Account.secret)) != nil {
		t.Errorf("users %+v, want %s with the harness's password", f.Auth.Users, v1Account.name)
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
	lat, err := probeLatency(ctx, s, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lat) != 5 {
		t.Errorf("%d latencies", len(lat))
	}
	if s.drawn.Load() < 6 || s.firstFrame.Load() <= 0 {
		t.Errorf("%d frames drawn, the first after %v", s.drawn.Load(), time.Duration(s.firstFrame.Load()))
	}
	if err := s.close(); err != nil {
		t.Error(err)
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

// A terminal's first frame is the first that shows the remote: not the
// blank terminal the session core serves before the backend connected,
// nor the clear screen that resizing a blank terminal sends, as the SSH
// backend resizes its terminal before the shell prints.
func TestV1TerminalFirstFrame(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	const delay = 300 * time.Millisecond
	for name, resize := range map[string]time.Duration{"blank": 0, "resized": 100 * time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			r := testRunner(t, series{Name: "v1-native", open: func(context.Context, string, string) (*session, error) {
				b := newShell(delay)
				b.resize = resize
				return v1Rig(t, b), nil
			}})
			res := r.connect(context.Background(), "ssh")
			if res.Error != "" {
				t.Fatal(res.Error)
			}
			if first := time.Duration(res.FirstFrame.P50 * float64(time.Millisecond)); first < delay {
				t.Errorf("the first frame drew after %v, before the shell's first output after %v", first, delay)
			}
		})
	}
}

// A terminal shows something once a cell holds a character or a
// coloured background; a cleared screen, or a moved cursor, shows
// nothing.
func TestShowsCells(t *testing.T) {
	for in, want := range map[string]bool{
		"":                         false,
		"\x1b[H\x1b[2J\x1b[5;5H":   false,
		"$ ":                       true,
		"\x1b[48;5;201m \x1b[0m":   true,
		"\x1b[3;3H\xe4\xb8\x80":    true, // a wide character
		"\x1b[7m\x1b[0m\x1b[1;1H ": false,
	} {
		term := terminal.New(80, 24, terminal.Options{})
		term.Write([]byte(in))
		if got := showsCells(term.Snapshot()); got != want {
			t.Errorf("%q shows %v, want %v", in, got, want)
		}
	}
}

// At a refresh rate, a graphical frame counts from when Next delivered
// it, and is acknowledged at the screen's next tick, as the web client
// acknowledges it after the next animation frame; at 0, at once.
func TestV1Display(t *testing.T) {
	// run shows a backend that repaints at an interval that is not a
	// multiple of the refresh's, so that frames arrive at every phase of
	// it, while the client waits for them.
	run := func(t *testing.T, d display) ([]frame, *frameLog) {
		t.Helper()
		var fl *frameLog
		s := v1RigWith(t, &flicker{every: 23 * time.Millisecond}, rig{display: d, wrap: func(e pipeEnd) client.Conn {
			fl = newFrameLog(e)
			return fl
		}})
		var fs []frame
		end := time.After(700 * time.Millisecond)
		for {
			select {
			case f := <-s.frames:
				fs = append(fs, f)
			case <-end:
				if len(fs) < 15 {
					t.Fatalf("%d frames", len(fs))
				}
				return fs, fl
			}
		}
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
		if _, err := openV1(map[string]string{"vnc": "refused"}, display{})(context.Background(), addr, "vnc"); err == nil {
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
			_, err := openV1(map[string]string{"vnc": "hung"}, display{})(context.Background(), addr, "vnc")
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

// shell is a terminal backend that echoes its input, and prints T2's
// magenta line on Return. It starts after delay, as a remote shell
// answers some time after the session started. With resize, it resizes
// its blank terminal after resize, as the SSH backend does once the
// shell's channel opens.
type shell struct {
	input         chan backend.InputEvent
	delay, resize time.Duration
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
	if b.resize > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(b.resize):
		}
		sink.Resize(100, 30)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(start.Add(b.delay))):
	}
	sink.Resize(80, 24)
	sink.TermData([]byte("$ "))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-b.input:
			in, ok := ev.(backend.TermInput)
			switch {
			case !ok:
			case bytes.Equal(in.Data, []byte{'\r'}):
				sink.TermData([]byte("\r\n\x1b[48;5;201m        \x1b[0m\r\n$ "))
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
