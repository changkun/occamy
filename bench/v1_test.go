// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/bench/internal/probe"
	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/config"
	sessions "changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// The bench stack's v1 file loads, has every connection the v1 series
// open with the protocol and backend the series names, and the user the
// harness signs in as.
func TestBenchV1File(t *testing.T) {
	raw, err := os.ReadFile("../docker/bench/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
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

// v1Rig serves backend b to a v1 session of the harness in process: the
// session core and protocol v1's stream on one end of a pipe, the client
// core on the other.
func v1Rig(t *testing.T, b backend.Backend) *session {
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
	cl, err := client.New(ctx, p.end(0), opts)
	if err != nil {
		t.Fatal(err)
	}
	s.startV1(cl, nil)
	return s
}

// The probe works over v1: keys reach the backend, and the frame that
// shows a key's colour is marked.
func TestV1Probe(t *testing.T) {
	s := v1Rig(t, &swatch{input: make(chan backend.InputEvent, 64)})
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

// A v1 terminal's frames draw when they carry bytes, and T2's marker
// finds its magenta line in the emulated terminal.
func TestV1Terminal(t *testing.T) {
	s := v1Rig(t, &shell{input: make(chan backend.InputEvent, 64)})
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

// swatch is a graphical backend that paints the probe's swatch as the
// scene program does: grey on F2, and the colour of each letter.
type swatch struct{ input chan backend.InputEvent }

func (*swatch) Class() backend.Class  { return backend.Graphical }
func (*swatch) Demand(backend.Demand) {}
func (b *swatch) Input(ev backend.InputEvent) error {
	b.input <- ev
	return nil
}

func (b *swatch) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(width, height)
	fill(sink, image.Rect(0, 0, width, height), color.RGBA{A: 0xff})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-b.input:
			k, ok := ev.(backend.Key)
			switch {
			case !ok || !k.Down:
			case k.Keysym == keyF2:
				fill(sink, probe.Swatch, color.RGBA{0x80, 0x80, 0x80, 0xff})
			case k.Keysym >= 'a' && k.Keysym <= 'z':
				fill(sink, probe.Swatch, probe.Colour(rune(k.Keysym)))
			}
		}
	}
}

func fill(sink backend.Sink, r image.Rectangle, c color.RGBA) {
	img := sink.Lock(r)
	defer sink.Unlock(r)
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// shell is a terminal backend that echoes its input, and prints T2's
// magenta line on Return.
type shell struct{ input chan backend.InputEvent }

func (*shell) Class() backend.Class  { return backend.Terminal }
func (*shell) Demand(backend.Demand) {}
func (b *shell) Input(ev backend.InputEvent) error {
	b.input <- ev
	return nil
}

func (b *shell) Run(ctx context.Context, sink backend.Sink) error {
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
