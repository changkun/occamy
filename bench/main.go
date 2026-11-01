// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command bench measures Occamy sessions under the fixed scenarios of
// specs/008-native-architecture/benchmark.md, against the stack that
// make bench starts:
//
//	make bench
//	go run ./bench -scenarios T1,T2 -net N0,N1 -quick
//
// It writes the results as JSON and prints them as a Markdown table.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// series is a way of reaching a remote: protocol v0 through occamyd or
// straight to guacd, or protocol v1 through occamyd with one backend
// (v1.go).
type series struct {
	Name   string
	target string
	// open opens a session on proto through the proxy at addr. The
	// session's start is when the client began to connect.
	open func(ctx context.Context, addr, proto string) (*session, error)
	// protocols are the protocols the series serves; nil serves every
	// one.
	protocols []string
}

func (s series) serves(proto string) bool {
	return s.protocols == nil || slices.Contains(s.protocols, proto)
}

// openV0 opens v0 sessions with dial.
func openV0(dial func(ctx context.Context, addr string, cred config.JWT) (rawConn, error)) func(context.Context, string, string) (*session, error) {
	return func(ctx context.Context, addr, proto string) (*session, error) {
		start := time.Now()
		c, err := dial(ctx, addr, credentials[proto])
		if err != nil {
			return nil, err
		}
		return newV0Session(c, start), nil
	}
}

func main() {
	var (
		addr      = flag.String("addr", "localhost:5636", "occamyd address")
		guacdAddr = flag.String("guacd", "localhost:4822", "guacd address for the direct series")
		v1Addr    = flag.String("v1", "localhost:5637", "occamy-v1 address for the v1 series")
		scen      = flag.String("scenarios", "all", "comma-separated scenario IDs, or all")
		nets      = flag.String("net", "N0,N1,N2", "comma-separated network conditions")
		seriesF   = flag.String("series", "occamy,guacd", "comma-separated series: occamy, guacd, v1-native, v1-guacd")
		quick     = flag.Bool("quick", false, "fewer samples and shorter scenarios, to check the setup")
		ackHz     = flag.Float64("ack-hz", 60, "v1: the screen refresh at which a graphical frame is acknowledged, or 0 for at once, a diagnostic; the gate's numbers use 60")
		runs      = flag.Int("runs", 1, "how many times to run everything; the table shows medians")
		failfast  = flag.Bool("failfast", false, "stop at the first measurement with an error")
		engine    = flag.String("engine", "podman", "container engine for CPU statistics, or none")
		project   = flag.String("project", "occamy", "compose project of the stack")
		out       = flag.String("out", "bench-results.json", "JSON output file")
	)
	flag.Parse()
	log.SetFlags(log.Ltime)

	all := []series{
		{Name: "occamy", target: *addr, open: openV0(dialOccamy)},
		{Name: "guacd", target: *guacdAddr, open: openV0(dialGuacd)},
	}
	for _, name := range []string{"v1-native", "v1-guacd"} {
		conns := v1Connections[name]
		all = append(all, series{
			Name: name, target: *v1Addr, open: openV1(conns, newDisplay(*ackHz)),
			protocols: slices.Sorted(maps.Keys(conns)),
		})
	}
	var chosenSeries []series
	for _, name := range strings.Split(*seriesF, ",") {
		i := slices.IndexFunc(all, func(s series) bool { return s.Name == name })
		if i < 0 {
			log.Fatalf("unknown series %q", name)
		}
		chosenSeries = append(chosenSeries, all[i])
	}
	var chosenNets []netCondition
	for _, name := range strings.Split(*nets, ",") {
		i := slices.IndexFunc(netConditions, func(n netCondition) bool { return n.Name == name })
		if i < 0 {
			log.Fatalf("unknown network condition %q", name)
		}
		chosenNets = append(chosenNets, netConditions[i])
	}
	var chosen []scenario
	for _, sc := range scenarios {
		if *scen == "all" || slices.Contains(strings.Split(*scen, ","), sc.ID) {
			chosen = append(chosen, sc)
		}
	}
	if len(chosen) == 0 {
		log.Fatalf("no scenarios match %q", *scen)
	}

	p := plan{
		series: chosenSeries, nets: chosenNets, scenarios: chosen,
		quick: *quick, runs: *runs, ackHz: *ackHz, failfast: *failfast,
		engine: *engine, project: *project,
	}
	report := p.measure(context.Background())

	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
		log.Fatal(err)
	}
	report.markdown(os.Stdout)
	failed := slices.ContainsFunc(report.Results, func(r result) bool { return r.Error != "" })
	if failed && p.failfast {
		log.Print("stopped at the first error (-failfast)")
	} else if names := idle(chosenSeries, report.Results); len(names) > 0 {
		log.Printf("no scenario chosen runs on %s", strings.Join(names, ", "))
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}

// plan is what a bench run measures, as its flags chose.
type plan struct {
	series    []series
	nets      []netCondition
	scenarios []scenario
	quick     bool
	runs      int
	ackHz     float64
	// failfast ends the run at the first result with an error, rather
	// than measure the rest of a stack that stopped working.
	failfast        bool
	engine, project string
}

// measure runs p. Each run measures everything once, so that a
// disturbance on the host affects one run of a measurement rather than
// all of them.
func (p plan) measure(ctx context.Context) report {
	rep := report{
		Started:    time.Now().UTC(),
		Quick:      p.quick,
		Runs:       max(p.runs, 1),
		AckHz:      p.ackHz,
		FrameStamp: frameStamp,
		Host:       hostInfo(p.engine, p.project),
	}
	for run := range rep.Runs {
		log.Printf("run %d of %d", run+1, rep.Runs)
		for _, sr := range p.series {
			for _, nc := range p.nets {
				if p.measureOn(ctx, &rep, run, sr, nc) {
					return rep
				}
			}
		}
	}
	return rep
}

// measureOn adds to rep each chosen scenario and first frame that sr
// serves, measured on nc, and reports whether failfast stopped there.
func (p plan) measureOn(ctx context.Context, rep *report, run int, sr series, nc netCondition) (stopped bool) {
	px, err := startProxy(sr.target, nc)
	if err != nil {
		log.Fatal(err)
	}
	defer px.close()
	r := &runner{
		series: sr, net: nc, proxy: px, quick: p.quick, runIndex: run,
		cpu: newCPUSampler(p.engine, p.project),
	}
	stop := func(res result) bool {
		rep.Results = append(rep.Results, res)
		return p.failfast && res.Error != ""
	}
	for _, sc := range p.scenarios {
		if sr.serves(sc.Protocol) && stop(r.run(ctx, sc)) {
			return true
		}
	}
	for _, proto := range protocolsOf(p.scenarios) {
		if sr.serves(proto) && stop(r.connect(ctx, proto)) {
			return true
		}
	}
	return false
}

// idle returns the series that ran nothing, such as one that serves
// none of the chosen scenarios' protocols.
func idle(chosen []series, results []result) []string {
	var names []string
	for _, sr := range chosen {
		if !slices.ContainsFunc(results, func(r result) bool { return r.Series == sr.Name }) {
			names = append(names, sr.Name)
		}
	}
	return names
}

func protocolsOf(scs []scenario) []string {
	var protos []string
	for _, sc := range scs {
		if !slices.Contains(protos, sc.Protocol) {
			protos = append(protos, sc.Protocol)
		}
	}
	return protos
}

// runner runs scenarios for one series under one network condition.
type runner struct {
	series   series
	net      netCondition
	proxy    *proxy
	quick    bool
	runIndex int // which of the runs this is
	cpu      *cpuSampler
}

// gap separates sessions, so that a session with the same claims does
// not join the previous one while it ends
// (specs/003-session-lifecycle-hardening.md, defect 6).
var gap = 2 * time.Second

// Bounds on what a sample waits for, so that a stack that stopped
// answering fails the sample instead of holding the run: openTimeout
// for opening a session, which for v1 is the sign-in and the stream's
// handshake, and in G7 for the first frame too; inputTimeout for a v1
// input to be queued, which waits only while the network takes nothing.
var (
	openTimeout  = 30 * time.Second
	inputTimeout = 10 * time.Second
)

func (r *runner) samples(n int) int {
	if r.quick {
		return max(n/10, 3)
	}
	return n
}

func (r *runner) duration(d time.Duration) time.Duration {
	if r.quick {
		return d / 6
	}
	return d
}

func (r *runner) newResult(id, proto string) *result {
	return &result{Scenario: id, Protocol: proto, Series: r.series.Name, Net: r.net.Name, Run: r.runIndex}
}

// run runs one scenario on a fresh session.
func (r *runner) run(ctx context.Context, sc scenario) result {
	res := r.newResult(sc.ID, sc.Protocol)
	log.Printf("%s %s %s", res.Series, res.Net, res.Scenario)
	defer time.Sleep(gap)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	s, err := r.series.open(ctx, r.proxy.addr(), sc.Protocol)
	if err != nil {
		res.Error = err.Error()
		log.Printf("  %v", err)
		return *res
	}
	err = sc.run(ctx, r, s, res)
	if cerr := s.close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		res.Error = err.Error()
		log.Printf("  %v", err)
	}
	res.Unsupported = s.unsupported
	res.serverFrames()
	return *res
}

// connect (G7) measures the time to the first frame of new sessions
// that shows the remote. For v1 it starts at the stream's upgrade,
// after the sign-in, which it records apart (v1.go).
func (r *runner) connect(ctx context.Context, proto string) result {
	res := r.newResult("G7", proto)
	log.Printf("%s %s %s %s", res.Series, res.Net, res.Scenario, proto)
	var times, signIns []time.Duration
	for range r.samples(20) {
		sctx, cancel := context.WithTimeout(ctx, openTimeout)
		s, err := r.series.open(sctx, r.proxy.addr(), proto)
		if err != nil {
			cancel()
			res.Error = err.Error()
			break
		}
		_, err = s.wait(sctx, s.start, func(f frame) bool { return f.content })
		cancel()
		s.close()
		if err != nil {
			res.Error = err.Error()
			break
		}
		times = append(times, time.Duration(s.firstFrame.Load()))
		if s.signIn > 0 {
			signIns = append(signIns, s.signIn)
		}
		time.Sleep(gap)
	}
	if res.Error != "" {
		log.Printf("  %s", res.Error)
	}
	res.FirstFrame = summarize(times)
	res.SignIn = summarize(signIns)
	return *res
}

// measurement is a snapshot taken when a scenario's measured part starts.
type measurement struct {
	r        *runner
	s        *session
	at       time.Time
	up, down int64
	drawn    int64
	decode   int64
	cpu      map[string]time.Duration
}

func (r *runner) measure(s *session) *measurement {
	m := &measurement{r: r, s: s, cpu: r.cpu.sample()}
	m.up, m.down = r.proxy.bytes()
	m.drawn, m.decode = s.drawn.Load(), s.decode.Load()
	m.at = time.Now()
	return m
}

// finish records the measured part, which ended at end, into res.
func (m *measurement) finish(res *result, end time.Time) {
	up, down := m.r.proxy.bytes()
	res.Seconds = end.Sub(m.at).Seconds()
	res.BytesUp, res.BytesDown = up-m.up, down-m.down
	res.Frames = m.s.drawn.Load() - m.drawn
	res.DecodeMS = ms(time.Duration(m.s.decode.Load() - m.decode))
	if after := m.r.cpu.sample(); after != nil && m.cpu != nil {
		res.CPUMS = map[string]float64{}
		for name, t := range after {
			res.CPUMS[name] = ms(t - m.cpu[name])
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// percentiles summarizes a latency distribution in milliseconds.
type percentiles struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// summarize returns nearest-rank percentiles, or nil without samples.
func summarize(d []time.Duration) *percentiles {
	if len(d) == 0 {
		return nil
	}
	d = slices.Clone(d)
	slices.Sort(d)
	rank := func(p float64) float64 {
		i := int(p*float64(len(d))+0.999999) - 1
		return ms(d[min(max(i, 0), len(d)-1)])
	}
	return &percentiles{N: len(d), P50: rank(0.50), P95: rank(0.95), P99: rank(0.99), Max: ms(d[len(d)-1])}
}

// result holds one scenario's measurements for one series and network
// condition.
type result struct {
	Scenario string `json:"scenario"`
	Protocol string `json:"protocol"`
	Series   string `json:"series"`
	Net      string `json:"net"`
	Run      int    `json:"run"`

	Latency    *percentiles `json:"latency_ms,omitempty"`
	FirstFrame *percentiles `json:"first_frame_ms,omitempty"`
	// SignIn is G7's sign-in to API v1, which a v1 client does once per
	// access token rather than once per session, so FirstFrame leaves it
	// out.
	SignIn     *percentiles `json:"sign_in_ms,omitempty"`
	Completion float64      `json:"completion_ms,omitempty"`

	Seconds   float64 `json:"seconds,omitempty"`
	BytesUp   int64   `json:"bytes_up,omitempty"`
	BytesDown int64   `json:"bytes_down,omitempty"`
	Frames    int64   `json:"frames,omitempty"`
	// ServerFrames are a v1 scene's frames, which are the server's: one
	// may carry part of a frame of the scene, or several, so they are
	// not Frames until the scene stamps its frames
	// (docs/benchmarks.md, The v1 series).
	ServerFrames int64   `json:"server_frames,omitempty"`
	DecodeMS     float64 `json:"client_decode_ms,omitempty"`
	// SceneFPS is the rate at which the scene animates, which bounds
	// Frames/Seconds.
	SceneFPS float64            `json:"scene_fps,omitempty"`
	CPUMS    map[string]float64 `json:"cpu_ms,omitempty"`

	Unsupported map[string]int `json:"unsupported,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// v1 reports a result of a v1 series.
func (r *result) v1() bool { return strings.HasPrefix(r.Series, "v1-") }

// serverFrames moves a v1 scene's frames to ServerFrames.
func (r *result) serverFrames() {
	if r.v1() && r.SceneFPS > 0 {
		r.ServerFrames, r.Frames = r.Frames, 0
	}
}

type report struct {
	Started time.Time `json:"started"`
	Quick   bool      `json:"quick"`
	Runs    int       `json:"runs"`
	// AckHz is the refresh at which the v1 series acknowledge a
	// graphical frame (-ack-hz), or 0 for at once.
	AckHz float64 `json:"ack_hz"`
	// FrameStamp is when a frame counts as drawn, per protocol.
	FrameStamp string            `json:"frame_stamp"`
	Host       map[string]string `json:"host"`
	Results    []result          `json:"results"`
}

// frameStamp is when the series count a frame as drawn.
const frameStamp = "v1: when Next delivers the frame applied, before its acknowledgement; v0: when the client applied it, before it answers sync"

// ackText says when the v1 series acknowledge a graphical frame.
func (rep *report) ackText() string {
	if rep.AckHz <= 0 {
		return "at once (-ack-hz 0, a diagnostic; the gate's numbers are measured at 60 Hz)"
	}
	return fmt.Sprintf("at the next refresh of a %g Hz screen (-ack-hz)", rep.AckHz)
}

func (rep *report) markdown(w io.Writer) {
	fmt.Fprintf(w, "Started %s", rep.Started.Format(time.RFC3339))
	if rep.Quick {
		fmt.Fprint(w, " (quick)")
	}
	if rep.Runs > 1 {
		fmt.Fprintf(w, ". Each number is the median of %d runs.", rep.Runs)
	}
	fmt.Fprintln(w)
	if slices.ContainsFunc(rep.Results, func(r result) bool { return r.v1() }) {
		fmt.Fprintf(w, displayHeader, rep.ackText())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Scenario | Series | Net | Latency p50/p95/p99 ms | First frame p50 ms | Completion ms | Down KiB | Down KiB/s | Frames/s | Server CPU ms | Remote CPU ms | Decode ms | Notes |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	rows := table(rep.Results)
	defer func() {
		if slices.ContainsFunc(rows, func(r row) bool { return r.first.v1() }) {
			fmt.Fprintln(w)
			fmt.Fprintln(w, decodeNote)
		}
	}()
	for _, row := range rows {
		r, c := row.first, row.cells
		f := func(i int, format string) string {
			if math.IsNaN(c[i]) {
				return ""
			}
			return fmt.Sprintf(format, c[i])
		}
		lat := ""
		if !math.IsNaN(c[colP50]) {
			lat = fmt.Sprintf("%.1f / %.1f / %.1f", c[colP50], c[colP95], c[colP99])
		}
		scen := r.Scenario
		if strings.HasPrefix(r.Scenario, "G") {
			scen += " " + r.Protocol
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			scen, r.Series, r.Net, lat, f(colFirst, "%.1f"), f(colCompletion, "%.0f"),
			f(colDown, "%.1f"), f(colRate, "%.1f"), f(colFPS, "%.1f"),
			f(colServerCPU, "%.0f"), f(colRemoteCPU, "%.0f"), f(colDecode, "%.0f"),
			strings.ReplaceAll(strings.Join(row.notes, "; "), "|", "/"))
	}
}

// The numbers in a row of the table.
const (
	colP50 = iota
	colP95
	colP99
	colFirst
	colCompletion
	colDown
	colRate
	colFPS
	colServerCPU
	colRemoteCPU
	colDecode
	// Numbers that go into notes.
	colSignIn
	colIdleGuacdCPU
	numCols
)

// cells returns the numbers of one result, with NaN where the scenario
// does not measure something.
func cells(r *result) [numCols]float64 {
	var c [numCols]float64
	for i := range c {
		c[i] = math.NaN()
	}
	if r.Latency != nil {
		c[colP50], c[colP95], c[colP99] = r.Latency.P50, r.Latency.P95, r.Latency.P99
	}
	if r.FirstFrame != nil {
		c[colFirst] = r.FirstFrame.P50
	}
	if r.SignIn != nil {
		c[colSignIn] = r.SignIn.P50
	}
	if r.Completion > 0 {
		c[colCompletion] = r.Completion
	}
	if r.Seconds > 0 {
		c[colDown] = float64(r.BytesDown) / 1024
		c[colRate] = float64(r.BytesDown) / 1024 / r.Seconds
		if !r.v1() || r.SceneFPS == 0 {
			c[colFPS] = float64(r.Frames) / r.Seconds
		}
	}
	if r.CPUMS != nil {
		switch r.Series {
		case "v1-native": // guacd serves nothing of it
			c[colServerCPU] = r.CPUMS["occamy-v1"]
		case "v1-guacd":
			c[colServerCPU] = r.CPUMS["occamy-v1"] + r.CPUMS["guacd"]
		default:
			c[colServerCPU] = r.CPUMS["occamy"] + r.CPUMS["guacd"]
		}
		c[colRemoteCPU] = r.CPUMS[r.Protocol]
		if r.Series == "v1-native" {
			if v, ok := r.CPUMS["guacd"]; ok {
				c[colIdleGuacdCPU] = v
			}
		}
	}
	if r.DecodeMS > 0 {
		c[colDecode] = r.DecodeMS
	}
	return c
}

// Notes on what a row's numbers mean.
const (
	// serverFramesNote replaces the frame rate of a v1 scene, whose
	// frames are the server's (result.ServerFrames).
	serverFramesNote = "server frames, not comparable until the scene frame stamp lands"
	// signInNote gives a v1 G7's sign-in, which its first frame leaves
	// out and v0's includes as its login.
	signInNote = "sign-in p50 %.1f ms, not in the first frame; v0's first frame includes its login"
	// idleGuacdNote gives guacd's CPU during a v1-native scenario, which
	// guacd serves nothing of, so its server CPU leaves it out.
	idleGuacdNote = "guacd %.0f ms CPU, not in server CPU"
	// displayHeader states, above a table with v1 rows, when its frames
	// count and how they are acknowledged.
	displayHeader = "v1 frames count from when Next delivers them applied, as v0's from when the client applied them. v1 acknowledges a graphical frame %s, and a terminal's at once.\n"
	// decodeNote follows a table with v1 rows.
	decodeNote = "Decode ms: v0's includes compositing each frame. v1's is the wall time of each tile's decode, summed over the client core's decoding goroutines, or the terminal emulator's time, without compositing, so it compares v1's series with each other, not with v0."
)

// row is a line of the table: one measurement over all runs.
type row struct {
	first *result
	cells [numCols]float64 // medians over the runs
	notes []string
}

// table groups the results of every run by measurement, in the order
// they were first measured, and takes the median of each number.
func table(results []result) []row {
	type key struct{ scenario, protocol, series, net string }
	var keys []key
	runs := map[key][]*result{}
	for i := range results {
		r := &results[i]
		k := key{r.Scenario, r.Protocol, r.Series, r.Net}
		if _, ok := runs[k]; !ok {
			keys = append(keys, k)
		}
		runs[k] = append(runs[k], r)
	}
	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		rs := runs[k]
		row := row{first: rs[0]}
		for col := range numCols {
			var vs []float64
			for _, r := range rs {
				if v := cells(r)[col]; !math.IsNaN(v) {
					vs = append(vs, v)
				}
			}
			row.cells[col] = median(vs)
		}
		if fps := rs[0].SceneFPS; fps > 0 {
			row.notes = append(row.notes, fmt.Sprintf("scene %g fps", fps))
			if rs[0].v1() {
				row.notes = append(row.notes, serverFramesNote)
			}
		}
		if v := row.cells[colSignIn]; !math.IsNaN(v) {
			row.notes = append(row.notes, fmt.Sprintf(signInNote, v))
		}
		if v := row.cells[colIdleGuacdCPU]; !math.IsNaN(v) {
			row.notes = append(row.notes, fmt.Sprintf(idleGuacdNote, v))
		}
		unsupported := map[string]int{}
		for _, r := range rs {
			if r.Error != "" {
				row.notes = append(row.notes, fmt.Sprintf("run %d error: %s", r.Run+1, r.Error))
			}
			for op, n := range r.Unsupported {
				unsupported[op] += n
			}
		}
		for _, op := range slices.Sorted(maps.Keys(unsupported)) {
			row.notes = append(row.notes, fmt.Sprintf("%s ×%d", op, unsupported[op]))
		}
		rows = append(rows, row)
	}
	return rows
}

// median returns the median of vs, or NaN if there are none.
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return math.NaN()
	}
	vs = slices.Clone(vs)
	slices.Sort(vs)
	n := len(vs)
	if n%2 == 1 {
		return vs[n/2]
	}
	return (vs[n/2-1] + vs[n/2]) / 2
}

// cpuSampler reads the CPU time of the stack's containers.
type cpuSampler struct {
	engine  string
	project string
}

func newCPUSampler(engine, project string) *cpuSampler {
	if engine == "" || engine == "none" {
		return nil
	}
	return &cpuSampler{engine: engine, project: project}
}

var services = []string{"occamy", "occamy-v1", "guacd", "ssh", "vnc", "rdp"}

// sample returns the cumulative CPU time per compose service, or nil if
// the engine does not report it. Only podman reports cumulative CPU time
// in its statistics.
func (c *cpuSampler) sample() map[string]time.Duration {
	if c == nil {
		return nil
	}
	args := []string{"stats", "--no-stream", "--format", "json"}
	for _, svc := range services {
		args = append(args, c.project+"-"+svc+"-1")
	}
	out, err := exec.Command(c.engine, args...).Output() // #nosec G204 -- engine and project come from flags
	if err != nil {
		log.Printf("cpu statistics: %v", err)
		return nil
	}
	var stats []struct {
		Name    string `json:"name"`
		CPUTime string `json:"cpu_time"`
	}
	if err := json.Unmarshal(out, &stats); err != nil {
		return nil
	}
	m := map[string]time.Duration{}
	for _, st := range stats {
		d, err := time.ParseDuration(st.CPUTime)
		if err != nil {
			return nil
		}
		svc := strings.TrimSuffix(strings.TrimPrefix(st.Name, c.project+"-"), "-1")
		m[svc] = d
	}
	return m
}

// hostInfo records what the numbers were measured on, as far as it can
// find out.
func hostInfo(engine, project string) map[string]string {
	info := map[string]string{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": fmt.Sprint(runtime.NumCPU())}
	run := func(key, name string, args ...string) {
		out, err := exec.Command(name, args...).Output() // #nosec G204 -- fixed commands
		if err == nil {
			info[key] = strings.TrimSpace(string(out))
		}
	}
	switch runtime.GOOS {
	case "darwin":
		run("cpu", "sysctl", "-n", "machdep.cpu.brand_string")
		run("os", "sw_vers", "-productVersion")
	case "linux":
		run("cpu", "sh", "-c", "grep -m1 'model name' /proc/cpuinfo | cut -d: -f2")
		run("os", "uname", "-r")
	}
	if engine != "" && engine != "none" {
		run("engine", engine, "--version")
		run("occamy_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-occamy-1")
		run("occamy_v1_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-occamy-v1-1")
		run("guacd_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-guacd-1")
	}
	return info
}
