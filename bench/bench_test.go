// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"changkun.de/x/occamy/internal/terminal"
)

// echoServer echoes every connection until it closes.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func dialProxy(t *testing.T, cond netCondition) (*proxy, net.Conn) {
	t.Helper()
	p, err := startProxy(echoServer(t), cond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	c, err := net.Dial("tcp", p.addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return p, c
}

func TestProxyDelay(t *testing.T) {
	p, c := dialProxy(t, netCondition{RTT: 40 * time.Millisecond})
	buf := make([]byte, 4)
	for range 3 {
		start := time.Now()
		c.Write([]byte("ping"))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatal(err)
		}
		// The message crosses the link once each way.
		if rtt := time.Since(start); rtt < 40*time.Millisecond || rtt > 60*time.Millisecond {
			t.Errorf("round trip through the proxy took %v, want about 40ms", rtt)
		}
	}
	if up, down := p.bytes(); up != 12 || down != 12 {
		t.Errorf("bytes = %d up, %d down, want 12 and 12", up, down)
	}
}

func TestProxyRate(t *testing.T) {
	_, c := dialProxy(t, netCondition{Mbps: 20})
	const n = 500 << 10
	go c.Write(make([]byte, n))
	start := time.Now()
	if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	// 500 KiB at 20 Mbit/s takes about 205ms in each direction, and the
	// two directions overlap.
	want := time.Duration(n * 8 * float64(time.Second) / 20e6)
	if got := time.Since(start); got < want || got > 2*want+50*time.Millisecond {
		t.Errorf("500 KiB through 20 Mbit/s took %v, want about %v", got, want)
	}
}

// A link in the kernel is the proxy's: half the RTT each way, the
// bandwidth in kbit/s and the proxy's queue in packets, with the loss.
func TestShapeArgs(t *testing.T) {
	for _, c := range []struct {
		n    netCondition
		want string
	}{
		{netCondition{Name: "N0", Loss: 2}, "5637 0 0 2 44"},
		{netCondition{Name: "N1", RTT: 20 * time.Millisecond, Mbps: 100}, "5637 10000 100000 0 334"},
		{netCondition{Name: "N2", RTT: 50 * time.Millisecond, Mbps: 20, Loss: 0.5}, "5637 25000 20000 0.5 167"},
	} {
		if got := strings.Join(c.n.shapeArgs(5637), " "); got != c.want {
			t.Errorf("%s with %g%% loss: shape %s, want %s", c.n.Name, c.n.Loss, got, c.want)
		}
	}
}

// fakeEngine returns a container engine that records each command line
// it is given, and the file it records them in; it fails when fail is
// set, and otherwise succeeds without a word, as the link image does.
func fakeEngine(t *testing.T, fail bool) (engine, log string) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake engine is a shell script")
	}
	dir := t.TempDir()
	engine, log = filepath.Join(dir, "engine"), filepath.Join(dir, "log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\n", log)
	if fail {
		script += "echo no such image >&2\nexit 125\n"
	}
	if err := os.WriteFile(engine, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return engine, log
}

// A loss study's block shapes its series' server in the kernel, in the
// server's network namespace, and clears it afterwards; a collection
// first clears every server that its loss blocks shape.
func TestShape(t *testing.T) {
	engine, log := fakeEngine(t, false)
	f := planFile{Runs: 1, Studies: []studyFile{
		{Study: "loss", Series: []string{"v1-native", "v1-native-w2", "guacd"}, Nets: []string{"N2"}, Loss: []float64{2}, Scenarios: "T1"},
		{Study: "matrix", Series: []string{"v1-native-cubic"}, Nets: []string{"N0"}, Scenarios: "T1"},
	}}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	p := plan{blocks: blocks, engine: engine, project: "occcmp", linkImage: "netem"}
	if err := p.clearLinks(); err != nil {
		t.Fatal(err)
	}
	if err := p.shape(blocks[0].series, &blocks[0].net); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	const run = "run --rm --network container:occcmp-%s-1 --cap-add NET_ADMIN netem %s\n"
	want := fmt.Sprintf(run, "occamy-v1", "clear") + fmt.Sprintf(run, "guacd", "clear") +
		fmt.Sprintf(run, "occamy-v1", "5637 25000 20000 2 167")
	if string(got) != want {
		t.Errorf("engine ran\n%s\nwant\n%s", got, want)
	}

	p.engine, _ = fakeEngine(t, true)
	if err := p.shape(blocks[0].series, nil); err == nil || !strings.Contains(err.Error(), "no such image") {
		t.Errorf("a failing engine: %v, want its message", err)
	}
}

// A block reads the congestion control that its server's occamyd
// reports at its debug listener, and warns where it is not the one its
// series stands for; each result records it, and nothing where the
// harness does not read the server's runtime.
func TestCongestion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"goroutines": 9, "tcp_congestion": "cubic"}`)
	}))
	defer srv.Close()
	p := plan{debug: map[string]string{"occamy-v1-cubic": srv.Listener.Addr().String(), "occamy-v1": srv.Listener.Addr().String()}}
	var logged strings.Builder
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	for _, tt := range []struct {
		series, server, want string
		warns                bool
	}{
		{"v1-native-cubic", "occamy-v1-cubic", "cubic", false},
		{"v1-native", "occamy-v1", "cubic", true},
		{"guacd", "guacd", "", false},
	} {
		logged.Reset()
		if got := p.congestion(t.Context(), series{Name: tt.series, server: tt.server}); got != tt.want {
			t.Errorf("%s: congestion %q, want %q", tt.series, got, tt.want)
		}
		if warns := strings.Contains(logged.String(), "load tcp_bbr"); warns != tt.warns {
			t.Errorf("%s: logged %q, want a warning: %v", tt.series, logged.String(), tt.warns)
		}
	}

	r := &runner{series: series{Name: "v1-native-cubic"}, net: netConditions[2], study: "loss", factor: "loss=2%", congestion: "cubic"}
	raw, err := json.Marshal(r.newResult("G5", "vnc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"congestion":"cubic"`) {
		t.Errorf("JSON %s, want the congestion control", raw)
	}
	r = &runner{series: series{Name: "v1-native"}, net: netConditions[0]}
	if raw, _ := json.Marshal(r.newResult("G5", "vnc")); strings.Contains(string(raw), "congestion") {
		t.Errorf("JSON %s names a congestion control nothing read", raw)
	}
}

// Each block samples the services of every bench stack and its own
// server, and no other.
func TestCPUSamplerServices(t *testing.T) {
	if c := newCPUSampler("podman", "occcmp", "occamy-v1-cubic"); !slices.Equal(c.services, append(slices.Clone(services), "occamy-v1-cubic")) {
		t.Errorf("services %v, want the stack's and occamy-v1-cubic", c.services)
	}
	if c := newCPUSampler("podman", "occcmp", "occamy-v1"); !slices.Equal(c.services, services) {
		t.Errorf("services %v, want the stack's", c.services)
	}
	if newCPUSampler("none", "occcmp", "occamy-v1") != nil {
		t.Error("a sampler without an engine")
	}
}

func TestSummarize(t *testing.T) {
	var d []time.Duration
	for i := 100; i >= 1; i-- {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	got := summarize(d)
	want := percentiles{N: 100, P50: 50, P95: 95, P99: 99, Max: 100}
	if *got != want {
		t.Errorf("summarize = %+v, want %+v", *got, want)
	}
	if summarize(nil) != nil {
		t.Error("summarize(nil) is not nil")
	}
}

func TestTableMedians(t *testing.T) {
	t1 := func(run int, p95 float64) result {
		return result{Scenario: "T1", Protocol: "ssh", Series: "guacd", Net: "N0", Run: run,
			Latency: &percentiles{P50: 2, P95: p95, P99: p95}}
	}
	t3 := func(run int, kib int64) result {
		return result{Scenario: "T3", Protocol: "ssh", Series: "guacd", Net: "N0", Run: run,
			Seconds: 2, BytesDown: kib << 10, CPUMS: map[string]float64{"guacd": 3, "ssh": 5}}
	}
	broken := t3(2, 0)
	broken.Seconds, broken.CPUMS, broken.Error = 0, nil, "session ended"
	rows := table([]result{t1(0, 3), t3(0, 10), t1(1, 50), t3(1, 30), t1(2, 4), broken})

	if len(rows) != 2 || rows[0].first.Scenario != "T1" || rows[1].first.Scenario != "T3" {
		t.Fatalf("rows are not T1, then T3: %+v", rows)
	}
	// One disturbed run does not move the median.
	if got := rows[0].cells[colP95]; got != 4 {
		t.Errorf("T1 p95 = %v, want 4", got)
	}
	// A run that failed has no numbers, and the others still count.
	if got := rows[1].cells[colRate]; got != 10 {
		t.Errorf("T3 KiB/s = %v, want 10, the median of 5 and 15", got)
	}
	if got := rows[1].cells[colServerCPU]; got != 3 {
		t.Errorf("T3 server CPU = %v, want 3", got)
	}
	if !math.IsNaN(rows[1].cells[colP50]) {
		t.Errorf("T3 has a latency: %v", rows[1].cells[colP50])
	}
	if want := []string{"run 3 error: session ended"}; !slices.Equal(rows[1].notes, want) {
		t.Errorf("T3 notes = %q, want %q", rows[1].notes, want)
	}
}

// In a table with v1 rows: a scene's frame rate is what its stamps
// counted, in either protocol, not the server's own frames; the server
// CPU of a series is that of what serves it, with guacd's apart for
// v1-native; and a v1 G7 gives its sign-in, which v0's first frame
// includes.
func TestTableV1(t *testing.T) {
	cpu := map[string]float64{"occamy-v1": 20, "guacd": 300, "vnc": 4000}
	g3 := func(series string, scene int64) result {
		r := result{Scenario: "G3", Protocol: "vnc", Series: series, Net: "N1",
			Seconds: 10, Frames: 3800, sceneFrames: scene, SceneFPS: scrollFPS, CPUMS: cpu}
		r.sceneCount()
		return r
	}
	g7 := func(series string, signIn *percentiles) result {
		return result{Scenario: "G7", Protocol: "vnc", Series: series, Net: "N1",
			FirstFrame: &percentiles{P50: 120}, SignIn: signIn}
	}
	rows := table([]result{
		g3("guacd", 414), g3("v1-native", 594),
		g7("guacd", nil), g7("v1-native", &percentiles{P50: 61.5}),
	})
	if len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	v0, native := rows[0], rows[1]
	for i, want := range []float64{41.4, 59.4} {
		if got := rows[i].cells[colFPS]; got != want {
			t.Errorf("%s frames a second = %v, want %v", rows[i].first.Series, got, want)
		}
	}
	if got := v0.cells[colServerCPU]; got != 300 {
		t.Errorf("v0 server CPU = %v, want guacd's, 300", got)
	}
	if got := native.cells[colServerCPU]; got != 20 {
		t.Errorf("v1-native server CPU = %v, want occamy-v1's, 20", got)
	}
	if !slices.Contains(native.notes, "guacd 300 ms CPU, not in server CPU") {
		t.Errorf("v1-native notes %q, want guacd's CPU", native.notes)
	}
	if slices.ContainsFunc(v0.notes, func(n string) bool { return strings.HasPrefix(n, "guacd") }) {
		t.Errorf("v0 notes guacd's CPU apart: %q", v0.notes)
	}
	if len(rows[2].notes) != 0 {
		t.Errorf("v0 G7 notes %q", rows[2].notes)
	}
	if !slices.ContainsFunc(rows[3].notes, func(n string) bool { return strings.HasPrefix(n, "sign-in p50 61.5 ms") }) {
		t.Errorf("v1 G7 notes %q, want its sign-in", rows[3].notes)
	}
}

// An RDP row of a v1 series says whether GFX was on and whether that is
// what its side ships, in the table and in the JSON, and its server CPU
// is that of what served it.
func TestGFXRows(t *testing.T) {
	cpu := map[string]float64{"occamy-v1": 20, "occamy-v1-cubic": 30, "guacd": 300, "rdp": 4000}
	for name, want := range map[string]struct {
		note    string
		json    string
		cpu     float64
		apart   bool // guacd's CPU is noted apart
		verdict bool
	}{
		"v1-native":       {"GFX on, as shipped: the verdict", `"gfx":true,"verdict":true`, 20, true, true},
		"v1-native-w2":    {"GFX on, like for like beside the verdict", `"gfx":true`, 20, true, false},
		"v1-native-cubic": {"GFX on, like for like beside the verdict", `"gfx":true`, 30, true, false},
		"v1-native-nogfx": {"GFX off, like for like beside the verdict", `"gfx":false`, 20, true, false},
	} {
		r := &runner{series: series{Name: name}, net: netConditions[0]}
		res := r.newResult("G5", "rdp")
		res.CPUMS = cpu
		rows := table([]result{*res})
		if !slices.Contains(rows[0].notes, want.note) {
			t.Errorf("%s notes %q, want %q", name, rows[0].notes, want.note)
		}
		if got := rows[0].cells[colServerCPU]; got != want.cpu {
			t.Errorf("%s server CPU = %v, want %v", name, got, want.cpu)
		}
		if got := !math.IsNaN(rows[0].cells[colIdleGuacdCPU]); got != want.apart {
			t.Errorf("%s notes guacd's CPU apart: %v, want %v", name, got, want.apart)
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want.json) {
			t.Errorf("%s JSON %s, want %s", name, raw, want.json)
		}
		if got := strings.Contains(string(raw), `"verdict"`); got != want.verdict {
			t.Errorf("%s JSON %s: verdict %v, want %v", name, raw, got, want.verdict)
		}
	}
}

// GFX is RDP's and v1's: no other row says anything of it.
func TestNoGFXRows(t *testing.T) {
	for _, c := range []struct{ series, proto string }{
		{"v1-native", "vnc"}, {"v1-native", "ssh"}, {"guacd", "rdp"},
	} {
		r := &runner{series: series{Name: c.series}, net: netConditions[0]}
		res := r.newResult("G5", c.proto)
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"gfx"`) || strings.Contains(string(raw), `"verdict"`) {
			t.Errorf("%s over %s: JSON %s", c.series, c.proto, raw)
		}
		if notes := table([]result{*res})[0].notes; len(notes) != 0 {
			t.Errorf("%s over %s: notes %q", c.series, c.proto, notes)
		}
	}
}

// A table with v1 rows says what their decode time is.
func TestMarkdownDecodeNote(t *testing.T) {
	for series, want := range map[string]bool{"guacd": false, "v1-native": true} {
		var b strings.Builder
		rep := report{Results: []result{{Scenario: "T3", Protocol: "ssh", Series: series, Net: "N0", Seconds: 1, DecodeMS: 2}}}
		rep.markdown(&b)
		if got := strings.Contains(b.String(), decodeNote); got != want {
			t.Errorf("%s: decode note %v, want %v:\n%s", series, got, want, b.String())
		}
	}
}

// The report states when frames count and how v1 acknowledges them: in
// the JSON, and above a table with v1 rows.
func TestReportDisplay(t *testing.T) {
	for _, c := range []struct {
		series string
		hz     float64
		want   string
	}{
		{"v1-native", 60, "acknowledges a graphical frame at the next refresh of a 60 Hz screen (-ack-hz), and a terminal's at once"},
		{"v1-native-w2", 0, "acknowledges a graphical frame at once (-ack-hz 0, a diagnostic"},
		{"guacd", 60, ""},
	} {
		rep := report{AckHz: c.hz, FrameStamp: frameStamp, Results: []result{{Scenario: "T3", Protocol: "vnc", Series: c.series, Net: "N0", Seconds: 1}}}
		raw, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			AckHz      *float64 `json:"ack_hz"`
			FrameStamp string   `json:"frame_stamp"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.AckHz == nil || *got.AckHz != c.hz || got.FrameStamp != frameStamp {
			t.Errorf("%s: the JSON has ack_hz %v and frame_stamp %q", c.series, got.AckHz, got.FrameStamp)
		}
		var b strings.Builder
		rep.markdown(&b)
		header, _, _ := strings.Cut(b.String(), "| Scenario")
		if c.want == "" {
			if strings.Contains(header, "acknowledges") {
				t.Errorf("%s: a v0 table states v1's acknowledgements:\n%s", c.series, header)
			}
		} else if !strings.Contains(header, c.want) || !strings.Contains(header, "v1 frames count from when Next delivers them applied") {
			t.Errorf("%s at %g Hz: the header does not state %q:\n%s", c.series, c.hz, c.want, header)
		}
	}
}

// silent returns a session whose server never draws.
func silent() *session {
	s := newSession(time.Now())
	s.conn = &nopConn{s: s}
	return s
}

type nopConn struct {
	s    *session
	once sync.Once
}

func (*nopConn) key(int) error { return nil }
func (c *nopConn) close() {
	c.once.Do(func() {
		close(c.s.frames)
		close(c.s.done)
	})
}

// testRunner runs sr under N0 through a proxy to an echo server.
func testRunner(t *testing.T, sr series) *runner {
	t.Helper()
	p, err := startProxy(echoServer(t), netConditions[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return &runner{series: sr, net: netConditions[0], proxy: p, quick: true}
}

// within returns what f returns, failing the test if f takes longer than
// d.
func within[T any](t *testing.T, d time.Duration, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(d):
		t.Fatalf("did not return within %v", d)
		panic("unreachable")
	}
}

// A sample that the stack never answers fails within its bound: G7's
// first frame, T1's echo of a key, and T2's end marker.
func TestSampleTimeouts(t *testing.T) {
	defer func(o, k, m time.Duration) { openTimeout, keyWait, markerWait = o, k, m }(openTimeout, keyWait, markerWait)
	openTimeout, keyWait, markerWait = 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond
	r := testRunner(t, series{Name: "v1-native", open: func(context.Context, string, string) (*session, error) {
		return silent(), nil
	}})
	t.Run("G7", func(t *testing.T) {
		res := within(t, 5*time.Second, func() result { return r.connect(context.Background(), "vnc") })
		if !strings.Contains(res.Error, context.DeadlineExceeded.Error()) {
			t.Errorf("G7 error %q, want a deadline", res.Error)
		}
	})
	for name, run := range map[string]func(context.Context, *runner, *session, *result) error{
		"T1": runTypeEcho, "T2": runBulkOutput,
	} {
		t.Run(name, func(t *testing.T) {
			s := silent()
			defer s.close()
			err := within(t, 5*time.Second, func() error { return run(context.Background(), r, s, &result{}) })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: %v, want a deadline", name, err)
			}
		})
	}
}

// wait takes only frames after its time.
func TestWaitAfter(t *testing.T) {
	s := newSession(time.Now())
	t0 := time.Now()
	for _, d := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
		s.frames <- frame{at: t0.Add(d), marked: true}
	}
	f, err := s.wait(context.Background(), t0, func(f frame) bool { return f.marked })
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(time.Millisecond); !f.at.Equal(want) {
		t.Errorf("wait took the frame %v after t, want the one after 1ms", f.at.Sub(t0))
	}
}

// A measurement counts what happened between its start and its end, not
// before.
func TestMeasureDeltas(t *testing.T) {
	p, c := dialProxy(t, netCondition{})
	r := &runner{proxy: p}
	s := newSession(time.Now())
	echo := func(n int) {
		t.Helper()
		if _, err := c.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	echo(1000)
	s.drawn.Store(5)
	s.sceneFrames.Store(11)
	s.sceneShown = []time.Time{s.start, s.start.Add(time.Millisecond)}
	s.decode.Store(int64(7 * time.Millisecond))
	m := r.measure(s)
	echo(40)
	s.drawn.Add(3)
	s.sceneFrames.Add(2)
	later := []time.Time{s.start.Add(40 * time.Millisecond), s.start.Add(70 * time.Millisecond)}
	s.sceneShown = append(s.sceneShown, later...)
	s.decode.Add(int64(2 * time.Millisecond))
	var res result
	m.finish(&res, m.at.Add(2*time.Second))
	want := result{Seconds: 2, BytesUp: 40, BytesDown: 40, Frames: 3, sceneFrames: 2, DecodeMS: 2, sceneShown: later}
	if res.Seconds != want.Seconds || res.BytesUp != want.BytesUp || res.BytesDown != want.BytesDown ||
		res.Frames != want.Frames || res.sceneFrames != want.sceneFrames || res.DecodeMS != want.DecodeMS ||
		!slices.Equal(res.sceneShown, want.sceneShown) {
		t.Errorf("measured %+v, want %+v", res, want)
	}
}

// A scene's cadence is the time between the first showings of its
// consecutive frames; a scenario without a scene, or a scene of one
// frame, has none.
func TestFrameInterval(t *testing.T) {
	t0 := time.Now()
	at := func(ms ...int) []time.Time {
		var ts []time.Time
		for _, m := range ms {
			ts = append(ts, t0.Add(time.Duration(m)*time.Millisecond))
		}
		return ts
	}
	// 21 frames: 19 that came after 33 ms, and one after 100 ms, where
	// two frames of the scene never showed.
	shown := []int{0}
	for i := range 19 {
		shown = append(shown, 33*(i+1))
	}
	shown = append(shown, shown[len(shown)-1]+100)
	r := result{SceneFPS: motionFPS, Frames: 40, sceneFrames: int64(len(shown)), sceneShown: at(shown...)}
	r.sceneCount()
	want := percentiles{N: 20, P50: 33, P95: 33, P99: 100, Max: 100}
	if r.FrameInterval == nil || *r.FrameInterval != want {
		t.Errorf("frame interval %+v, want %+v", r.FrameInterval, want)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"frame_interval_ms":{"n":20,"p50":33,"p95":33,"p99":100,"max":100}`) {
		t.Errorf("JSON %s, want frame_interval_ms", raw)
	}
	rows := table([]result{{Scenario: "G5", Protocol: "vnc", Series: "guacd", Net: "N0", Seconds: 1, Frames: 21, SceneFPS: motionFPS, FrameInterval: &want}})
	if note := fmt.Sprintf(intervalNote, 33.0); !slices.Contains(rows[0].notes, note) {
		t.Errorf("notes %q, want %q", rows[0].notes, note)
	}
	for name, r := range map[string]result{
		"no scene":     {Frames: 3, sceneFrames: 3, sceneShown: at(0, 33, 66)},
		"one frame":    {SceneFPS: motionFPS, sceneFrames: 1, sceneShown: at(0)},
		"none at all":  {SceneFPS: motionFPS},
		"unknown time": {SceneFPS: motionFPS, sceneFrames: 2},
	} {
		r.sceneCount()
		if r.FrameInterval != nil {
			t.Errorf("%s: frame interval %+v, want none", name, r.FrameInterval)
		}
	}
}

// In a scene, in either protocol, frames are what the stamps counted, and
// the server's own go to the JSON as server_frames. A scenario without a
// scene keeps the frames that drew.
func TestRunSceneFrames(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	run := func(fps float64) func(context.Context, *runner, *session, *result) error {
		return func(_ context.Context, _ *runner, _ *session, res *result) error {
			res.Seconds, res.Frames, res.sceneFrames, res.SceneFPS = 10, 3800, 594, fps
			return nil
		}
	}
	open := func(context.Context, string, string) (*session, error) { return silent(), nil }
	for _, name := range []string{"guacd", "v1-native", "v1-native-nogfx"} {
		r := testRunner(t, series{Name: name, open: open})
		res := r.run(context.Background(), scenario{ID: "G3", Protocol: "vnc", run: run(scrollFPS)})
		if res.Frames != 594 || res.ServerFrames != 3800 {
			t.Errorf("%s, a scene: frames %d, server frames %d, want 594 and 3800", name, res.Frames, res.ServerFrames)
		}
		res = r.run(context.Background(), scenario{ID: "G1", Protocol: "vnc", run: run(0)})
		if res.Frames != 3800 || res.ServerFrames != 0 {
			t.Errorf("%s, no scene: frames %d, server frames %d, want 3800 and 0", name, res.Frames, res.ServerFrames)
		}
	}
}

// The first marker is looked at until a frame matches it, and no longer.
// A scenario's own marker outlives it, even when set while the frame
// that matched was on its way.
func TestFirstMarker(t *testing.T) {
	first := marker{cells: func(*terminal.Screen) bool { return true }}
	s := newSession(time.Now())
	s.expect(first)
	m := s.marker.Load()
	s.emit(frame{}, m)
	if s.marker.Load() != m {
		t.Error("a frame that did not match retired the first marker")
	}
	s.emit(frame{marked: true}, m)
	if s.marker.Load() != nil {
		t.Error("the first marker stays after a frame matched it")
	}

	s = newSession(time.Now())
	s.expect(first)
	m = s.marker.Load()
	s.setMarker(magenta)
	own := s.marker.Load()
	s.emit(frame{marked: true}, m)
	if s.marker.Load() != own {
		t.Error("a frame that matched the first marker retired the scenario's")
	}
	s.emit(frame{marked: true}, own)
	if s.marker.Load() != own {
		t.Error("a frame that matched the scenario's marker retired it")
	}
}

// A run's key offsets are its own, the same for every series, within the
// jitter either way, and spread around their slots.
func TestKeyOffsets(t *testing.T) {
	const n = 2000
	a := keyOffsets(0, n)
	if len(a) != n {
		t.Fatalf("%d offsets, want %d", len(a), n)
	}
	if !slices.Equal(a, keyOffsets(0, n)) {
		t.Error("two series of a run press their keys at different times")
	}
	if slices.Equal(a, keyOffsets(1, n)) {
		t.Error("two runs press their keys at the same times")
	}
	var sum, spread time.Duration
	for _, d := range a {
		if d <= -keyJitter || d >= keyJitter {
			t.Fatalf("offset %v, want within %v either way", d, keyJitter)
		}
		sum += d
		spread = max(spread, d-a[0], a[0]-d)
	}
	if mean := sum / n; mean < -keyJitter/8 || mean > keyJitter/8 {
		t.Errorf("mean offset %v, want about 0", mean)
	}
	if spread < keyJitter {
		t.Errorf("offsets spread over %v, want at least %v", spread, keyJitter)
	}
}

// answerConn is a session whose server answers each key at once, with a
// frame that any marker takes.
type answerConn struct {
	s    *session
	mu   sync.Mutex
	keys []time.Time
}

func (c *answerConn) key(int) error {
	now := time.Now()
	c.mu.Lock()
	c.keys = append(c.keys, now)
	c.mu.Unlock()
	c.s.frames <- frame{at: now.Add(time.Millisecond), marked: true}
	return nil
}

func (c *answerConn) close() { close(c.s.done); close(c.s.frames) }

// The probe's keys fall on the run's offsets from their slots, which a
// fixed period would not, however late the host wakes the harness.
func TestProbeLatencyJitter(t *testing.T) {
	const run, n = 3, 12
	s := newSession(time.Now())
	c := &answerConn{s: s}
	s.conn = c
	if _, err := probeLatency(context.Background(), s, run, n); err != nil {
		t.Fatal(err)
	}
	offsets := keyOffsets(run, n)
	var jittered, fixed time.Duration
	for i := 1; i < n; i++ {
		got := c.keys[i].Sub(c.keys[0])
		slot := time.Duration(i) * keyPeriod
		jittered += (got - slot - (offsets[i] - offsets[0])).Abs()
		fixed += (got - slot).Abs()
	}
	if jittered >= fixed {
		t.Errorf("the keys stray %v from the jittered times and %v from a fixed period", jittered/(n-1), fixed/(n-1))
	}
}

// A chosen series that serves none of the chosen scenarios is refused,
// rather than measure nothing.
func TestMatrixBlocksIdle(t *testing.T) {
	ssh := []scenario{{ID: "T1", Protocol: "ssh"}}
	nogfx := series{Name: "v1-native-nogfx", protocols: []string{"rdp"}}
	if _, err := matrixBlocks("matrix", []series{{Name: "v1-native"}, nogfx}, netConditions[:1], ssh); err == nil {
		t.Error("a series without a scenario to run: no error")
	}
	blocks, err := matrixBlocks("matrix", []series{{Name: "v1-native"}}, netConditions[:2], ssh)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("%d blocks, %v; want 2", len(blocks), err)
	}
}

// CI's bench job has a bound well below GitHub's default of 6 hours,
// each of its runs stops at the first error, and the stack's logs follow
// a failure or the job's timeout, which cancels it.
func TestCIBench(t *testing.T) {
	raw, err := os.ReadFile("../.github/workflows/dockerimage.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			TimeoutMinutes int `yaml:"timeout-minutes"`
			Env            map[string]string
			Steps          []struct {
				Name, If, Run string
			}
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	job := wf.Jobs["bench"]
	if m := job.TimeoutMinutes; m <= 0 || m > 30 {
		t.Errorf("the bench job's timeout-minutes is %d, want 1 to 30", m)
	}
	// The kernel has BBR loaded before the stack starts, so that occamyd
	// uses it, as it ships.
	loaded := false
	runs, logs := 0, false
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "modprobe tcp_bbr") {
			loaded = true
		}
		if strings.Contains(s.Run, "make bench") || strings.Contains(s.Run, "go run ./bench") {
			if !loaded {
				t.Errorf("step %q runs the bench before BBR is loaded", s.Name)
			}
			runs++
			if !strings.Contains(s.Run, " -failfast ") {
				t.Errorf("step %q runs the bench without -failfast", s.Name)
			}
		}
		if strings.Contains(s.Run, "go run ./bench") {
			for name := range v1Connections {
				if !slices.Contains(strings.Split(flagValue(s.Run, "-series"), ","), name) {
					t.Errorf("step %q does not run the series %s", s.Name, name)
				}
			}
		}
		if s.Name == "Stack logs" {
			logs = true
			if s.If != "${{ !success() }}" {
				t.Errorf("the stack's logs run if %q, want after a failure or a cancel", s.If)
			}
		}
	}
	if runs != 2 || !logs {
		t.Errorf("%d bench runs and the stack's logs %v, want 2 and true", runs, logs)
	}
}

// flagValue returns what follows flag in a command line.
func flagValue(cmd, flag string) string {
	f := strings.Fields(cmd)
	if i := slices.Index(f, flag); i >= 0 && i+1 < len(f) {
		return f[i+1]
	}
	return ""
}

// measure stops at the first result with an error with failfast, and
// measures everything without; its report states the plan's refresh.
func TestMeasure(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	refused := series{Name: "v1-native", target: echoServer(t), open: func(context.Context, string, string) (*session, error) {
		return nil, errors.New("refused")
	}}
	nop := func(context.Context, *runner, *session, *result) error { return nil }
	scs := []scenario{{ID: "T1", Protocol: "ssh", run: nop}, {ID: "T2", Protocol: "ssh", run: nop}}
	for failfast, want := range map[bool]int{true: 1, false: 3} { // T1, T2 and G7
		blocks, err := matrixBlocks("matrix", []series{refused}, netConditions[:1], scs)
		if err != nil {
			t.Fatal(err)
		}
		p := plan{blocks: blocks, quick: true, ackHz: 30, failfast: failfast, engine: "none"}
		rep := within(t, 10*time.Second, func() report {
			rep, err := p.measure(context.Background(), nil)
			if err != nil {
				t.Error(err)
			}
			return rep
		})
		if len(rep.Results) != want {
			t.Errorf("failfast %v: %d results, want %d", failfast, len(rep.Results), want)
		}
		if rep.Runs != 1 || rep.AckHz != 30 || rep.FrameStamp != frameStamp {
			t.Errorf("failfast %v: %d runs, ack_hz %v, frame_stamp %q", failfast, rep.Runs, rep.AckHz, rep.FrameStamp)
		}
	}
}

// -scenarios gives what it names, in the order they run, and refuses what
// it does not know rather than leave it out.
func TestChooseScenarios(t *testing.T) {
	all, err := chooseScenarios("all")
	if err != nil || len(all) != len(scenarios) {
		t.Errorf("all: %d scenarios, %v", len(all), err)
	}
	got, err := chooseScenarios("G2,T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "T1" || got[1].ID != "G2" || got[2].ID != "G2" {
		t.Errorf("G2,T1: %+v, want T1, then G2 of VNC and RDP", got)
	}
	for _, spec := range []string{"G7", "G2,G7", "T4", "G2,", ""} {
		if got, err := chooseScenarios(spec); err == nil {
			t.Errorf("%q: %d scenarios, want an error", spec, len(got))
		}
	}
}

// A scene with no stamp to count is an error, which -failfast and the
// exit code see, not a frame rate of 0.
func TestSceneWithoutStamps(t *testing.T) {
	for _, c := range []struct {
		name string
		r    result
		bad  bool
	}{
		{"stamps", result{SceneFPS: scrollFPS, Frames: 12, sceneFrames: 3}, false},
		{"none read", result{SceneFPS: scrollFPS, Frames: 12}, true},
		{"nothing drawn", result{SceneFPS: scrollFPS}, true},
		{"no scene", result{Frames: 12}, false},
		{"an error of its own", result{SceneFPS: scrollFPS, Frames: 12, Error: "lost"}, false},
	} {
		want := c.r.Error
		c.r.sceneCount()
		if c.bad && c.r.Error == "" {
			t.Errorf("%s: no error", c.name)
		}
		if !c.bad && c.r.Error != want {
			t.Errorf("%s: error %q, want %q", c.name, c.r.Error, want)
		}
	}
}
