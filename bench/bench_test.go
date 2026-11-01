// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
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
		return result{Scenario: "T1", Protocol: "ssh", Series: "occamy", Net: "N0", Run: run,
			Latency: &percentiles{P50: 2, P95: p95, P99: p95}}
	}
	t3 := func(run int, kib int64) result {
		return result{Scenario: "T3", Protocol: "ssh", Series: "occamy", Net: "N0", Run: run,
			Seconds: 2, BytesDown: kib << 10, CPUMS: map[string]float64{"occamy": 1, "guacd": 2, "ssh": 5}}
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
	cpu := map[string]float64{"occamy": 1, "occamy-v1": 20, "guacd": 300, "vnc": 4000}
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
		g3("occamy", 414), g3("v1-native", 594), g3("v1-guacd", 380),
		g7("occamy", nil), g7("v1-native", &percentiles{P50: 61.5}),
	})
	if len(rows) != 5 {
		t.Fatalf("%d rows", len(rows))
	}
	v0, native, bridge := rows[0], rows[1], rows[2]
	for i, want := range []float64{41.4, 59.4, 38} {
		if got := rows[i].cells[colFPS]; got != want {
			t.Errorf("%s frames a second = %v, want %v", rows[i].first.Series, got, want)
		}
	}
	if got := v0.cells[colServerCPU]; got != 301 {
		t.Errorf("v0 server CPU = %v, want occamy's and guacd's, 301", got)
	}
	if got := native.cells[colServerCPU]; got != 20 {
		t.Errorf("v1-native server CPU = %v, want occamy-v1's, 20", got)
	}
	if !slices.Contains(native.notes, "guacd 300 ms CPU, not in server CPU") {
		t.Errorf("v1-native notes %q, want guacd's CPU", native.notes)
	}
	if got := bridge.cells[colServerCPU]; got != 320 {
		t.Errorf("v1-guacd server CPU = %v, want occamy-v1's and guacd's, 320", got)
	}
	if slices.ContainsFunc(bridge.notes, func(n string) bool { return strings.HasPrefix(n, "guacd") }) {
		t.Errorf("v1-guacd notes guacd's CPU apart: %q", bridge.notes)
	}
	if len(rows[3].notes) != 0 {
		t.Errorf("v0 G7 notes %q", rows[3].notes)
	}
	if !slices.ContainsFunc(rows[4].notes, func(n string) bool { return strings.HasPrefix(n, "sign-in p50 61.5 ms") }) {
		t.Errorf("v1 G7 notes %q, want its sign-in", rows[4].notes)
	}
}

// An RDP row of a v1 series says whether GFX was on and whether that is
// what its side ships, in the table and in the JSON, and its server CPU
// is that of what served it.
func TestGFXRows(t *testing.T) {
	cpu := map[string]float64{"occamy-v1": 20, "occamy-v1-gfx": 50, "guacd": 300, "rdp": 4000}
	for name, want := range map[string]struct {
		note    string
		json    string
		cpu     float64
		apart   bool // guacd's CPU is noted apart
		verdict bool
	}{
		"v1-native":       {"GFX on, as shipped: the verdict", `"gfx":true,"verdict":true`, 20, true, true},
		"v1-native-nogfx": {"GFX off, like for like beside the verdict", `"gfx":false`, 20, true, false},
		"v1-guacd":        {"GFX off, as shipped: the verdict", `"gfx":false,"verdict":true`, 320, false, true},
		"v1-guacd-gfx":    {"GFX on, like for like beside the verdict", `"gfx":true`, 350, false, false},
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
		{"v1-native", "vnc"}, {"v1-native", "ssh"}, {"v1-guacd", "vnc"}, {"occamy", "rdp"}, {"guacd", "rdp"},
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
	for series, want := range map[string]bool{"occamy": false, "v1-native": true} {
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
		{"v1-guacd", 0, "acknowledges a graphical frame at once (-ack-hz 0, a diagnostic"},
		{"occamy", 60, ""},
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
	s.decode.Store(int64(7 * time.Millisecond))
	m := r.measure(s)
	echo(40)
	s.drawn.Add(3)
	s.sceneFrames.Add(2)
	s.decode.Add(int64(2 * time.Millisecond))
	var res result
	m.finish(&res, m.at.Add(2*time.Second))
	want := result{Seconds: 2, BytesUp: 40, BytesDown: 40, Frames: 3, sceneFrames: 2, DecodeMS: 2}
	if res.Seconds != want.Seconds || res.BytesUp != want.BytesUp || res.BytesDown != want.BytesDown ||
		res.Frames != want.Frames || res.sceneFrames != want.sceneFrames || res.DecodeMS != want.DecodeMS {
		t.Errorf("measured %+v, want %+v", res, want)
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
	for _, name := range []string{"occamy", "guacd", "v1-native", "v1-guacd"} {
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

// A chosen series that ran nothing is reported.
func TestIdle(t *testing.T) {
	chosen := []series{{Name: "v1-native"}, {Name: "v1-guacd"}}
	got := idle(chosen, []result{{Series: "v1-native"}})
	if !slices.Equal(got, []string{"v1-guacd"}) {
		t.Errorf("idle = %q, want v1-guacd", got)
	}
	if got := idle(chosen, []result{{Series: "v1-native"}, {Series: "v1-guacd"}}); len(got) != 0 {
		t.Errorf("idle = %q, want none", got)
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
	runs, logs := 0, false
	for _, s := range job.Steps {
		if strings.Contains(s.Run, "make bench") || strings.Contains(s.Run, "go run ./bench") {
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
		p := plan{series: []series{refused}, nets: netConditions[:1], scenarios: scs, quick: true, ackHz: 30, failfast: failfast, engine: "none"}
		rep := within(t, 10*time.Second, func() report { return p.measure(context.Background()) })
		if len(rep.Results) != want {
			t.Errorf("failfast %v: %d results, want %d", failfast, len(rep.Results), want)
		}
		if rep.Runs != 1 || rep.AckHz != 30 || rep.FrameStamp != frameStamp {
			t.Errorf("failfast %v: %d runs, ack_hz %v, frame_stamp %q", failfast, rep.Runs, rep.AckHz, rep.FrameStamp)
		}
	}
}
