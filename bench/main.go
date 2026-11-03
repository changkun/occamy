// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Command bench measures Occamy sessions under the fixed scenarios of
// specs/008-native-architecture/benchmark.md, against the stack that
// make bench starts:
//
//	make bench
//	go run ./bench -scenarios T1,T2 -net N0,N1 -quick
//	go run ./bench -plan bench/plans/compare.yaml -resume -out bench-compare.json
//
// It writes the results as JSON and prints them as a Markdown table. A
// plan file (-plan) measures the studies of
// specs/008-native-architecture/comparison-benchmark.md in one go.
package main

import (
	"bytes"
	"context"
	"errors"
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
	// server is the compose service that the series' client connects to,
	// and port where it listens inside the stack: where a loss study
	// emulates the link (plan.shape).
	server string
	port   int
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

// endpoints are where the series reach the stack: occamyd for v0, guacd
// for the direct series, occamy-v1 for the v1 series, occamy-v1-gfx for
// v1-guacd-gfx, and occamy-v1-bbr for v1-native-bbr.
type endpoints struct{ occamy, guacd, v1, v1GFX, v1BBR string }

// allSeries returns every series that the harness knows, in the order
// -series lists them, reaching the stack at at. The v1 series show their
// frames on d.
func allSeries(at endpoints, d display) []series {
	all := []series{
		{Name: "occamy", target: at.occamy, server: "occamy", port: 5636, open: openV0(dialOccamy)},
		{Name: "guacd", target: at.guacd, server: "guacd", port: 4822, open: openV0(dialGuacd)},
	}
	for _, v := range []struct{ name, target, server string }{
		{"v1-native", at.v1, "occamy-v1"},
		{"v1-native-w2", at.v1, "occamy-v1"},
		{"v1-native-nogfx", at.v1, "occamy-v1"},
		{"v1-guacd", at.v1, "occamy-v1"},
		{"v1-guacd-gfx", at.v1GFX, "occamy-v1-gfx"},
		{"v1-native-bbr", at.v1BBR, "occamy-v1-bbr"},
	} {
		conns := v1Connections[v.name]
		all = append(all, series{
			Name: v.name, target: v.target, server: v.server, port: 5637,
			open: openV1(conns, d, v1Windows[v.name]), protocols: slices.Sorted(maps.Keys(conns)),
		})
	}
	return all
}

// openV0 opens v0 sessions with dial.
func openV0(dial func(ctx context.Context, addr string, cred config.JWT) (rawConn, error)) func(context.Context, string, string) (*session, error) {
	return func(ctx context.Context, addr, proto string) (*session, error) {
		start := time.Now()
		c, err := dial(ctx, addr, credentials[proto])
		if err != nil {
			return nil, err
		}
		return newV0Session(c, start, shows(proto)), nil
	}
}

func main() {
	var (
		addr      = flag.String("addr", "localhost:5636", "occamyd address")
		guacdAddr = flag.String("guacd", "localhost:4822", "guacd address for the direct series")
		v1Addr    = flag.String("v1", "localhost:5637", "occamy-v1 address for the v1 series")
		v1GFXAddr = flag.String("v1-gfx", "localhost:5640", "occamy-v1-gfx address for the v1-guacd-gfx series")
		v1BBRAddr = flag.String("v1-bbr", "localhost:5642", "occamy-v1-bbr address for the v1-native-bbr series")
		scen      = flag.String("scenarios", "all", "comma-separated scenario IDs, or all")
		nets      = flag.String("net", "N0,N1,N2", "comma-separated network conditions")
		seriesF   = flag.String("series", "occamy,guacd", "comma-separated series: occamy, guacd, v1-native, v1-native-w2, v1-native-nogfx, v1-native-bbr, v1-guacd, v1-guacd-gfx")
		quick     = flag.Bool("quick", false, "fewer samples and shorter scenarios, to check the setup")
		ackHz     = flag.Float64("ack-hz", 60, "v1: the screen refresh at which a graphical frame is acknowledged, or 0 for at once, a diagnostic; the gate's numbers use 60")
		runs      = flag.Int("runs", 1, "how many times to run everything; the table shows medians")
		failfast  = flag.Bool("failfast", false, "stop at the first measurement with an error")
		engine    = flag.String("engine", "podman", "container engine for CPU statistics, or none")
		project   = flag.String("project", "occamy", "compose project of the stack")
		out       = flag.String("out", "bench-results.json", "JSON output file, saved after each series and network condition")
		planName  = flag.String("plan", "", "a plan file, such as bench/plans/compare.yaml, instead of -series, -net, -scenarios and -runs")
		resume    = flag.Bool("resume", false, "continue -out if it exists, a file of the same plan, keeping its complete blocks")
		maxLoad   = flag.Float64("max-load", 0, "before each result, wait until the host's 1-minute load is below this; 0 does not wait; set, it overrides the plan file's")
		loadWait  = flag.Duration("load-wait", 2*time.Hour, "how long -max-load waits before the harness saves and exits; set, it overrides the plan file's")
		debugF    = flag.String("runtime", "", "occamyd debug listeners to read the runtime from, as service=address pairs, such as occamy-v1=localhost:5638")
		linkImage = flag.String("link-image", "occamy-bench-netem", "the image that emulates a loss study's link in the kernel (docker/bench/netem)")
	)
	flag.Parse()
	log.SetFlags(log.Ltime)

	all := allSeries(endpoints{occamy: *addr, guacd: *guacdAddr, v1: *v1Addr, v1GFX: *v1GFXAddr, v1BBR: *v1BBRAddr}, newDisplay(*ackHz))
	debug, err := parseDebug(*debugF)
	if err != nil {
		log.Fatal(err)
	}
	p := plan{
		quick: *quick, runs: *runs, ackHz: *ackHz, failfast: *failfast,
		engine: *engine, project: *project, debug: debug, linkImage: *linkImage,
		maxLoad: *maxLoad, loadWait: *loadWait,
		save: func(rep *report) error { return writeReport(*out, rep) },
	}
	if *planName != "" {
		set := map[string]bool{}
		flag.Visit(func(f *flag.Flag) {
			set[f.Name] = true
			switch f.Name {
			case "series", "net", "scenarios", "runs":
				log.Fatalf("-%s comes from the plan file, not a flag", f.Name)
			}
		})
		f, err := readPlanFile(*planName)
		if err != nil {
			log.Fatal(err)
		}
		if p.blocks, err = studyBlocks(f, all); err != nil {
			log.Fatalf("%s: %v", *planName, err)
		}
		p.runs, p.seed, p.keys = f.Runs, f.Seed, f.Keys
		if f.Connects != nil {
			p.connects = *f.Connects
		}
		if !set["max-load"] {
			p.maxLoad = f.MaxLoad
		}
		if !set["load-wait"] {
			p.loadWait = f.LoadWait
		}
		if p.engine == "" || p.engine == "none" {
			if slices.ContainsFunc(p.blocks, func(b block) bool { return b.kernel }) {
				log.Fatalf("%s: a loss study emulates the link with the container engine, and -engine is none", *planName)
			}
		}
	} else {
		chosenSeries, err := chooseSeries(all, strings.Split(*seriesF, ","))
		if err != nil {
			log.Fatal(err)
		}
		chosenNets, err := chooseNets(strings.Split(*nets, ","))
		if err != nil {
			log.Fatal(err)
		}
		chosen, err := chooseScenarios(*scen)
		if err != nil {
			log.Fatal(err)
		}
		if p.blocks, err = matrixBlocks("matrix", chosenSeries, chosenNets, chosen); err != nil {
			log.Fatal(err)
		}
	}

	var prev *report
	if *resume {
		if prev, err = resumable(*out); err != nil {
			log.Fatal(err)
		}
		if prev != nil {
			if err := p.continues(prev); err != nil {
				log.Fatalf("%s is not of this plan: %v; move it away or choose another -out", *out, err)
			}
		}
	}
	report, err := p.measure(context.Background(), prev)
	if werr := writeReport(*out, &report); werr != nil {
		log.Fatal(werr)
	}
	report.markdown(os.Stdout)
	if err != nil {
		log.Printf("%v; what was measured is in %s, and the same command with -resume continues it", err, *out)
		os.Exit(2)
	}
	if slices.ContainsFunc(report.Results, func(r result) bool { return r.Error != "" }) {
		if p.failfast {
			log.Print("stopped at the first error (-failfast)")
		}
		os.Exit(1)
	}
}

// plan is what a bench run measures, as its flags or plan file chose.
type plan struct {
	blocks []block
	runs   int
	// seed draws each run's order of blocks; 0 keeps the order of blocks.
	seed  uint64
	quick bool
	ackHz float64
	// failfast ends the run at the first result with an error, rather
	// than measure the rest of a stack that stopped working.
	failfast        bool
	engine, project string
	debug           map[string]string // occamyd service: its debug listener
	// linkImage is the image whose shape emulates a link in the kernel
	// (docker/bench/netem), for the blocks of a loss study.
	linkImage string
	// maxLoad and loadWait are the load gate (waitLoad).
	maxLoad  float64
	loadWait time.Duration
	// keys and connects are a plan file's: how many probe keys a latency
	// scenario presses, and how many sessions G7 opens; 0 is the default.
	keys, connects int
	// save, if set, keeps what was measured after each block.
	save func(*report) error
}

// measure runs p, continuing prev, the results of the same plan, if it
// is not nil. Each run measures every block once, so that a disturbance
// on the host affects one run of a measurement rather than all of them,
// in an order of its own (order). Before each result it waits for a
// quiet host (waitLoad), and after each block, it saves. It returns an
// error, with what it measured, when the host stayed busy or a save
// failed; a block that the busy host stopped part-way is measured again
// whole when the collection resumes (kept).
func (p plan) measure(ctx context.Context, prev *report) (report, error) {
	rep := report{
		Started:    time.Now().UTC(),
		Quick:      p.quick,
		Runs:       max(p.runs, 1),
		AckHz:      p.ackHz,
		FrameStamp: frameStamp,
		Host:       hostInfo(p.engine, p.project),
		Plan:       p.record(),
	}
	p.runs = rep.Runs
	// A collection that was stopped may have left a link shaped.
	if err := p.clearLinks(); err != nil {
		return rep, err
	}
	done := map[blockKey]bool{}
	if prev != nil {
		rep.Started, rep.Host = prev.Started, prev.Host
		rep.Resumed = append(slices.Clone(prev.Resumed), time.Now().UTC())
		rep.Results, done = p.kept(prev.Results)
		log.Printf("resuming: %d of %d blocks measured", len(done), rep.Runs*len(p.blocks))
	}
	for run := range rep.Runs {
		for i, b := range p.order(run) {
			if done[b.key(run)] {
				continue
			}
			log.Printf("run %d of %d, block %d of %d: %s %s %s", run+1, rep.Runs, i+1, len(p.blocks), b.study, b.series.Name, b.net.Name)
			stopped, err := p.measureBlock(ctx, &rep, run, b)
			if p.save != nil {
				if err := p.save(&rep); err != nil {
					return rep, err
				}
			}
			if err != nil || stopped {
				return rep, err
			}
		}
	}
	return rep, nil
}

// measureBlock adds to rep each scenario of b and the first frame of each
// of their protocols, each once the host is quiet (waitLoad), and reports
// whether failfast stopped there. It returns the load gate's error if the
// host stayed busy before one of them, with those before it in rep.
//
// The gate holds each result rather than each block: a block takes
// minutes, and a scene such as G5 keeps several cores busy, so the load
// that a block's own results raise would otherwise carry into the
// results after them unchecked.
func (p plan) measureBlock(ctx context.Context, rep *report, run int, b block) (stopped bool, err error) {
	carried, congestion := b.net, ""
	if b.kernel {
		if congestion, err = p.shape(b.series, &b.net); err != nil {
			return false, err
		}
		defer func() {
			if _, err := p.shape(b.series, nil); err != nil {
				log.Print(err)
			}
		}()
		// The link is the kernel's; the proxy only counts the bytes.
		carried = netCondition{Name: b.net.Name}
	}
	px, err := startProxy(b.series.target, carried)
	if err != nil {
		log.Fatal(err)
	}
	defer px.close()
	r := &runner{
		series: b.series, net: b.net, proxy: px, quick: p.quick, runIndex: run,
		study: b.study, factor: b.factor, congestion: congestion, keys: p.keys, connects: p.connects,
		cpu: newCPUSampler(p.engine, p.project, b.series.server), debug: p.debug,
	}
	// add adds the result that m measures once the host is quiet, and
	// reports whether failfast stops the block there.
	add := func(m func() result) (bool, error) {
		if err := p.waitLoad(ctx); err != nil {
			return false, err
		}
		res := m()
		rep.Results = append(rep.Results, res)
		return p.failfast && res.Error != "", nil
	}
	for _, sc := range b.scenarios {
		if stop, err := add(func() result { return r.run(ctx, sc) }); stop || err != nil {
			return stop, err
		}
	}
	if !b.connect {
		return false, nil
	}
	for _, proto := range protocolsOf(b.scenarios) {
		if stop, err := add(func() result { return r.connect(ctx, proto) }); stop || err != nil {
			return stop, err
		}
	}
	return false, nil
}

// shape emulates n in the kernel on the client connections of s's server,
// with the link image's shape in the server's network namespace, and
// returns the TCP congestion control there, or clears what it emulated
// when n is nil.
func (p plan) shape(s series, n *netCondition) (congestion string, err error) {
	args := []string{
		"run", "--rm", "--network", "container:" + p.project + "-" + s.server + "-1",
		"--cap-add", "NET_ADMIN", p.linkImage,
	}
	if n == nil {
		args = append(args, "clear")
	} else {
		args = append(args, n.shapeArgs(s.port)...)
	}
	out, err := exec.Command(p.engine, args...).CombinedOutput() // #nosec G204 -- engine, project and image come from flags
	if err != nil {
		return "", fmt.Errorf("emulating the link on %s: %w: %s", s.server, err, bytes.TrimSpace(out))
	}
	if n == nil {
		return "", nil
	}
	return congestionOf(out), nil
}

// congestionOf returns the congestion control that the link image's
// output names on its line "congestion NAME", or "" if none does.
func congestionOf(out []byte) string {
	for line := range strings.Lines(string(out)) {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "congestion "); ok {
			return name
		}
	}
	return ""
}

// clearLinks clears the link in the kernel of every server whose series
// a block of p emulates there.
func (p plan) clearLinks() error {
	cleared := map[string]bool{}
	for _, b := range p.blocks {
		if !b.kernel || cleared[b.series.server] {
			continue
		}
		cleared[b.series.server] = true
		if _, err := p.shape(b.series, nil); err != nil {
			return err
		}
	}
	return nil
}

// chooseScenarios returns the scenarios that spec names: comma-separated
// IDs, or all. An ID it does not know is an error, so that a run does
// not silently leave out what it was asked for.
func chooseScenarios(spec string) ([]scenario, error) {
	if spec == "all" {
		return scenarios, nil
	}
	ids := strings.Split(spec, ",")
	for _, id := range ids {
		switch {
		case id == "G7":
			return nil, errors.New("G7 is no scenario: it follows a protocol's chosen scenarios, so any T scenario brings SSH's and any G scenario a desktop's")
		case !slices.ContainsFunc(scenarios, func(sc scenario) bool { return sc.ID == id }):
			return nil, fmt.Errorf("unknown scenario %q", id)
		}
	}
	var chosen []scenario
	for _, sc := range scenarios {
		if slices.Contains(ids, sc.ID) {
			chosen = append(chosen, sc)
		}
	}
	return chosen, nil
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
	// study and factor are those of the block the runner measures, and
	// congestion the TCP congestion control of its server, where a loss
	// study read it.
	study, factor, congestion string
	// keys and connects are the plan's (plan); 0 is the default.
	keys, connects int
	cpu            *cpuSampler
	debug          map[string]string // occamyd service: its debug listener
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

// probeKeys is how many probe keys a latency scenario presses: 200, or
// the plan's keys.
func (r *runner) probeKeys() int {
	if r.keys > 0 {
		return r.samples(r.keys)
	}
	return r.samples(200)
}

// connectSamples is how many sessions G7 opens: 20, or the plan's
// connects.
func (r *runner) connectSamples() int {
	if r.connects > 0 {
		return r.samples(r.connects)
	}
	return r.samples(20)
}

func (r *runner) duration(d time.Duration) time.Duration {
	if r.quick {
		return d / 6
	}
	return d
}

func (r *runner) newResult(id, proto string) *result {
	res := &result{
		Scenario: id, Protocol: proto, Series: r.series.Name, Net: r.net.Name, Run: r.runIndex,
		Study: r.study, Factor: r.factor, Congestion: r.congestion, At: time.Now().UTC(),
	}
	if l, err := loadAverage(); err == nil {
		res.Load = l
	}
	if path, ok := rdpPaths[res.Series]; ok && proto == "rdp" {
		res.GFX, res.Verdict = &path.gfx, path.verdict
	}
	return res
}

// run runs one scenario on a fresh session, observing what the stack and
// the harness spend on it. A scenario may take longer on a slow link
// (slowness), up to three times as long.
func (r *runner) run(ctx context.Context, sc scenario) result {
	res := r.newResult(sc.ID, sc.Protocol)
	log.Printf("%s %s %s", res.Series, res.Net, res.Scenario)
	defer time.Sleep(gap)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute*time.Duration(min(r.net.slowness(), 3)))
	defer cancel()
	o := r.observe(ctx)
	r.runOn(ctx, sc, res)
	o.finish(ctx, res)
	return *res
}

func (r *runner) runOn(ctx context.Context, sc scenario, res *result) {
	s, err := r.series.open(ctx, r.proxy.addr(), sc.Protocol)
	if err != nil {
		res.Error = err.Error()
		log.Printf("  %v", err)
		return
	}
	s.slowness = r.net.slowness()
	err = sc.run(ctx, r, s, res)
	if cerr := s.close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		res.Error = err.Error()
		log.Printf("  %v", err)
	}
	res.Unsupported = s.unsupported
	res.sceneCount()
}

// connect (G7) measures the time to the first frame of new sessions
// that shows known content of the remote (shows). For v1 it starts at
// the stream's upgrade, after the sign-in, which it records apart
// (v1.go).
func (r *runner) connect(ctx context.Context, proto string) result {
	res := r.newResult("G7", proto)
	log.Printf("%s %s %s %s", res.Series, res.Net, res.Scenario, proto)
	o := r.observe(ctx)
	var times, signIns []time.Duration
	for range r.connectSamples() {
		sctx, cancel := context.WithTimeout(ctx, openTimeout*time.Duration(r.net.slowness()))
		s, err := r.series.open(sctx, r.proxy.addr(), proto)
		if err != nil {
			cancel()
			res.Error = err.Error()
			break
		}
		f, err := s.wait(sctx, s.start, func(f frame) bool { return f.marked })
		cancel()
		s.close()
		if err != nil {
			res.Error = err.Error()
			break
		}
		times = append(times, f.at.Sub(s.start))
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
	o.finish(ctx, res)
	return *res
}

// measurement is a snapshot taken when a scenario's measured part starts.
type measurement struct {
	r        *runner
	s        *session
	at       time.Time
	up, down int64
	drawn    int64
	scene    int64
	shown    int // the scene's frames shown, whose times are not the measured part's
	decode   int64
	cpu      map[string]time.Duration
}

func (r *runner) measure(s *session) *measurement {
	m := &measurement{r: r, s: s, cpu: r.cpu.sample()}
	m.up, m.down = r.proxy.bytes()
	m.drawn, m.scene, m.decode = s.drawn.Load(), s.sceneFrames.Load(), s.decode.Load()
	m.shown = s.shown()
	m.at = time.Now()
	return m
}

// finish records the measured part, which ended at end, into res.
func (m *measurement) finish(res *result, end time.Time) {
	up, down := m.r.proxy.bytes()
	res.Seconds = end.Sub(m.at).Seconds()
	res.BytesUp, res.BytesDown = up-m.up, down-m.down
	res.Frames = m.s.drawn.Load() - m.drawn
	res.sceneFrames = m.s.sceneFrames.Load() - m.scene
	res.sceneShown = m.s.shownSince(m.shown)
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

// intervals returns the time between each of ts and the one before it.
func intervals(ts []time.Time) []time.Duration {
	var d []time.Duration
	for i := 1; i < len(ts); i++ {
		d = append(d, ts[i].Sub(ts[i-1]))
	}
	return d
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
	// Study and Factor are the block's study and its setting, such as
	// viewers=4, which the matrix leaves empty.
	Study  string `json:"study,omitempty"`
	Factor string `json:"factor,omitempty"`
	// Congestion is the TCP congestion control of the series' server,
	// which a loss study reads where it emulates the link, such as cubic.
	Congestion string `json:"congestion,omitempty"`
	// At is when the result started, and Load the host's 1-minute load
	// average then.
	At   time.Time `json:"at,omitzero"`
	Load float64   `json:"load_1m,omitempty"`
	// GFX says whether a v1 series reached RDP through its graphics
	// pipeline, and Verdict that this is what the series' side ships, so
	// that the row counts for the targets. Neither is set for another
	// protocol, nor for v0.
	GFX     *bool `json:"gfx,omitempty"`
	Verdict bool  `json:"verdict,omitempty"`

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
	// Frames are complete frames that drew, except in a scene: there, the
	// scene's frames that the client showed, each counted once by its
	// stamp, in any protocol.
	Frames int64 `json:"frames,omitempty"`
	// ServerFrames are a scene's frames as the server cut them: one may
	// carry part of a frame of the scene, or several.
	ServerFrames int64   `json:"server_frames,omitempty"`
	DecodeMS     float64 `json:"client_decode_ms,omitempty"`
	// SceneFPS is the rate at which the scene animates, which bounds
	// Frames/Seconds.
	SceneFPS float64 `json:"scene_fps,omitempty"`
	// FrameInterval is a scene's cadence: the time between the first
	// showings of consecutive frames of the scene, both shown in the
	// measured part, in either protocol. Frames that never showed widen
	// it; a cadence of 1/SceneFPS shows every frame evenly.
	FrameInterval *percentiles       `json:"frame_interval_ms,omitempty"`
	CPUMS         map[string]float64 `json:"cpu_ms,omitempty"`
	// Memory, PIDs, Runtime and ClientCPUMS cover the whole scenario,
	// with its session's start and end: each container's memory and
	// processes, occamyd's runtime from its debug listener (-runtime),
	// and the harness's own CPU time, network emulation included.
	Memory      map[string]memoryStat  `json:"mem_mib,omitempty"`
	PIDs        map[string]int         `json:"pids,omitempty"`
	Runtime     map[string]runtimeStat `json:"runtime,omitempty"`
	ClientCPUMS float64                `json:"client_cpu_ms,omitempty"`

	Unsupported map[string]int `json:"unsupported,omitempty"`
	Error       string         `json:"error,omitempty"`

	sceneFrames int64       // the stamps counted, until sceneCount moves them
	sceneShown  []time.Time // when each was first shown, for sceneCount
}

// seriesLabel names r's series in a table, with its study and factor
// outside the matrix.
func (r *result) seriesLabel() string {
	label := r.Series
	if r.Study != "" && r.Study != "matrix" {
		label += " " + r.Study
	}
	if r.Factor != "" {
		label += " " + r.Factor
	}
	return label
}

// v1 reports a result of a v1 series.
func (r *result) v1() bool { return strings.HasPrefix(r.Series, "v1-") }

// sceneCount makes a scene's frames the count of its stamps, in both
// protocols, keeps the server's own count in ServerFrames, and gives the
// scene's cadence (FrameInterval). A scene with no stamp to count
// measured nothing, which is an error, not a frame rate of 0.
func (r *result) sceneCount() {
	if r.SceneFPS == 0 {
		return
	}
	r.ServerFrames, r.Frames = r.Frames, r.sceneFrames
	r.FrameInterval = summarize(intervals(r.sceneShown))
	if r.Frames == 0 && r.Error == "" {
		r.Error = fmt.Sprintf("none of the scene's %d frames showed a stamp", r.ServerFrames)
		log.Printf("  %s", r.Error)
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
	// Plan is what the results measure, which -resume checks, and
	// Resumed when they were continued.
	Plan    *planRecord `json:"plan,omitempty"`
	Resumed []time.Time `json:"resumed,omitempty"`
	Results []result    `json:"results"`
}

// frameStamp is when the series count a frame as drawn.
const frameStamp = "v1: when Next delivers the frame applied, less any wait for the reader at a refresh; v0: when the client applied it, before it answers sync"

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
			scen, r.seriesLabel(), r.Net, lat, f(colFirst, "%.1f"), f(colCompletion, "%.0f"),
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
	colInterval
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
	if r.FrameInterval != nil {
		c[colInterval] = r.FrameInterval.P95
	}
	if r.Completion > 0 {
		c[colCompletion] = r.Completion
	}
	if r.Seconds > 0 {
		c[colDown] = float64(r.BytesDown) / 1024
		c[colRate] = float64(r.BytesDown) / 1024 / r.Seconds
		c[colFPS] = float64(r.Frames) / r.Seconds
	}
	if r.CPUMS != nil {
		// guacd serves nothing of the native series.
		native := strings.HasPrefix(r.Series, "v1-native")
		switch {
		case native:
			c[colServerCPU] = r.CPUMS[v1Service(r.Series)]
		case r.Series == "v1-guacd":
			c[colServerCPU] = r.CPUMS["occamy-v1"] + r.CPUMS["guacd"]
		case r.Series == "v1-guacd-gfx":
			c[colServerCPU] = r.CPUMS["occamy-v1-gfx"] + r.CPUMS["guacd"]
		default:
			c[colServerCPU] = r.CPUMS["occamy"] + r.CPUMS["guacd"]
		}
		c[colRemoteCPU] = r.CPUMS[r.Protocol]
		if native {
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
	// verdictNote and besideNote say what a v1 RDP row is, by whether it
	// is GFX on or off: the side's shipped default, or the other way.
	verdictNote = "GFX %s, as shipped: the verdict"
	besideNote  = "GFX %s, like for like beside the verdict"
	// signInNote gives a v1 G7's sign-in, which its first frame leaves
	// out and v0's includes as its login.
	signInNote = "sign-in p50 %.1f ms, not in the first frame; v0's first frame includes its login"
	// idleGuacdNote gives guacd's CPU during a v1-native scenario, which
	// guacd serves nothing of, so its server CPU leaves it out.
	idleGuacdNote = "guacd %.0f ms CPU, not in server CPU"
	// intervalNote gives a scene's cadence (result.FrameInterval).
	intervalNote = "scene frames %.1f ms apart at p95"
	// displayHeader states, above a table with v1 rows, when its frames
	// count and how they are acknowledged.
	displayHeader = "v1 frames count from when Next delivers them applied, less any wait for the reader at a refresh, as v0's from when the client applied them. v1 acknowledges a graphical frame %s, and a terminal's at once.\n"
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
	type key struct{ study, factor, scenario, protocol, series, net string }
	var keys []key
	runs := map[key][]*result{}
	for i := range results {
		r := &results[i]
		k := key{r.Study, r.Factor, r.Scenario, r.Protocol, r.Series, r.Net}
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
		if gfx := rs[0].GFX; gfx != nil {
			state, note := "off", besideNote
			if *gfx {
				state = "on"
			}
			if rs[0].Verdict {
				note = verdictNote
			}
			row.notes = append(row.notes, fmt.Sprintf(note, state))
		}
		if fps := rs[0].SceneFPS; fps > 0 {
			row.notes = append(row.notes, fmt.Sprintf("scene %g fps", fps))
		}
		if v := row.cells[colInterval]; !math.IsNaN(v) {
			row.notes = append(row.notes, fmt.Sprintf(intervalNote, v))
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
	engine   string
	project  string
	services []string
}

// newCPUSampler samples the services of every bench stack and server,
// which may be one of a profile's that only some stacks start.
func newCPUSampler(engine, project, server string) *cpuSampler {
	if engine == "" || engine == "none" {
		return nil
	}
	svcs := services
	if server != "" && !slices.Contains(svcs, server) {
		svcs = append(slices.Clone(svcs), server)
	}
	return &cpuSampler{engine: engine, project: project, services: svcs}
}

// services are those of every bench stack.
var services = []string{"occamy", "occamy-v1", "occamy-v1-gfx", "guacd", "ssh", "vnc", "rdp"}

// sample returns the cumulative CPU time per compose service, or nil if
// the engine does not report it. Only podman reports cumulative CPU time
// in its statistics.
func (c *cpuSampler) sample() map[string]time.Duration {
	if cs := c.stats(); cs != nil {
		return cs.cpu
	}
	return nil
}

// stats returns what the engine reports of each compose service, or nil
// if it reports nothing.
func (c *cpuSampler) stats() *containerStats {
	if c == nil {
		return nil
	}
	args := []string{"stats", "--no-stream", "--format", "json"}
	for _, svc := range c.services {
		args = append(args, c.project+"-"+svc+"-1")
	}
	out, err := exec.Command(c.engine, args...).Output() // #nosec G204 -- engine and project come from flags
	if err != nil {
		log.Printf("container statistics: %v", err)
		return nil
	}
	cs, err := parseStats(out, c.project)
	if err != nil {
		return nil
	}
	return &cs
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
		run("occamy_v1_gfx_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-occamy-v1-gfx-1")
		run("occamy_v1_bbr_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-occamy-v1-bbr-1")
		run("guacd_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-guacd-1")
	}
	return info
}
