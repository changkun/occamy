// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testSeries are the harness's series, serving what they serve, for plans
// that do not measure.
func testSeries() []series {
	return allSeries(endpoints{}, display{})
}

// The harness knows every series, each serving its protocols from a
// server of the stack.
func TestAllSeries(t *testing.T) {
	want := map[string][]string{
		"guacd":     nil,
		"v1-native": {"rdp", "ssh", "vnc"}, "v1-native-w2": {"rdp", "ssh", "vnc"},
		"v1-native-cubic": {"rdp", "ssh", "vnc"}, "v1-native-nogfx": {"rdp"},
	}
	all := testSeries()
	if len(all) != len(want) {
		t.Errorf("%d series, want %d", len(all), len(want))
	}
	servers := map[string]string{
		"guacd": "guacd:4822", "v1-native-cubic": "occamy-v1-cubic:5637",
	}
	for _, s := range all {
		if protos, ok := want[s.Name]; !ok || !slices.Equal(s.protocols, protos) {
			t.Errorf("series %s serves %v, want %v", s.Name, s.protocols, protos)
		}
		server := cmp.Or(servers[s.Name], "occamy-v1:5637")
		if got := fmt.Sprintf("%s:%d", s.server, s.port); got != server {
			t.Errorf("series %s is served by %s, want %s", s.Name, got, server)
		}
	}
}

// The comparison's plan measures what its spec says: all three series
// under N0 to N2, and the two main ones under N3 and N4, every scenario
// each serves, 7 times, behind the load gate.
func TestComparePlan(t *testing.T) {
	f, err := readPlanFile("plans/compare.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Runs != 7 || f.Seed == 0 || f.MaxLoad != 8 || f.LoadWait != 2*time.Hour {
		t.Errorf("runs %d, seed %d, max_load %g, load_wait %v; want 7, a seed, 8 and 2h", f.Runs, f.Seed, f.MaxLoad, f.LoadWait)
	}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3*3+2*2 {
		t.Errorf("%d blocks, want %d", len(blocks), 3*3+2*2)
	}
	for _, b := range blocks {
		switch {
		case b.series.Name == "v1-native-nogfx" && slices.ContainsFunc(b.scenarios, func(sc scenario) bool { return sc.Protocol != "rdp" }):
			t.Errorf("%s: v1-native-nogfx serves only RDP", b)
		case b.series.Name == "v1-native" && len(b.scenarios) != len(scenarios):
			t.Errorf("%s: %d scenarios, want all %d", b, len(b.scenarios), len(scenarios))
		}
	}
}

// The essential plan measures guacd and v1-native under N0 and N2, on T1,
// T2, and G2, G5 and G6 over VNC and RDP, 6 times with 100 keys, and
// leaves G7 out.
func TestEssentialPlan(t *testing.T) {
	f, err := readPlanFile("plans/essential.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Runs != 6 || f.Keys != 100 || f.Connects == nil || *f.Connects != 0 || f.MaxLoad != 8 {
		t.Errorf("runs %d, keys %d, connects %v, max_load %g; want 6, 100, 0 and 8", f.Runs, f.Keys, f.Connects, f.MaxLoad)
	}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 4 {
		t.Fatalf("%d blocks, want 4", len(blocks))
	}
	for _, b := range blocks {
		if b.connect || b.results() != 8 || strings.Contains(b.String(), "G7") {
			t.Errorf("%s: connect %v, %d results; want no G7 and 8", b, b.connect, b.results())
		}
	}
	p := plan{blocks: blocks, keys: f.Keys, connects: *f.Connects}
	other := p
	other.keys = 200
	if err := p.continues(&report{Plan: other.record()}); err == nil {
		t.Error("results with 200 keys continue a plan of 100")
	}
}

// The plan of the real-time transport's step 1 measures v1-native and
// v1-native-w2 on G5 over VNC and RDP under N0, N1 and N2, and on T1, T2
// and G2 under N0 and N2: 168 results in 6 runs of 100 keys, without G7.
func TestRealtime1Plan(t *testing.T) {
	f, err := readPlanFile("plans/realtime-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Runs != 6 || f.Keys != 100 || f.Connects == nil || *f.Connects != 0 || f.MaxLoad != 8 || f.LoadWait != 2*time.Hour {
		t.Errorf("runs %d, keys %d, connects %v, max_load %g, load_wait %v; want 6, 100, 0, 8 and 2h",
			f.Runs, f.Keys, f.Connects, f.MaxLoad, f.LoadWait)
	}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]bool{}
	results := 0
	for _, b := range blocks {
		if b.connect {
			t.Errorf("%s measures G7", b)
		}
		results += b.results()
		for _, sc := range b.scenarios {
			cells[fmt.Sprintf("%s %s %s %s", b.series.Name, sc.ID, sc.Protocol, b.net.Name)] = true
		}
	}
	if results*f.Runs != 168 || len(cells) != 28 {
		t.Errorf("%d results a run in %d cells, want 28 and 168 in all", results, len(cells))
	}
	for _, s := range []string{"v1-native", "v1-native-w2"} {
		for _, net := range []string{"N0", "N1", "N2"} {
			for _, proto := range []string{"vnc", "rdp"} {
				if !cells[fmt.Sprintf("%s G5 %s %s", s, proto, net)] {
					t.Errorf("no G5 over %s for %s under %s", proto, s, net)
				}
			}
		}
		for _, net := range []string{"N0", "N2"} {
			for _, cell := range []string{"T1 ssh", "T2 ssh", "G2 vnc", "G2 rdp"} {
				if !cells[fmt.Sprintf("%s %s %s", s, cell, net)] {
					t.Errorf("no %s for %s under %s", cell, s, net)
				}
			}
		}
	}
}

// Step 2's plan measures v1-native and guacd on the keys of T1, G2 and
// G5 under N1 and N2, each with none, 0.5% and 2% loss, every block on a
// link in the kernel.
func TestRealtime2Plan(t *testing.T) {
	f, err := readPlanFile("plans/realtime-2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Runs != 6 || f.Keys != 100 || f.Connects == nil || *f.Connects != 0 || f.MaxLoad != 8 || f.LoadWait != 2*time.Hour {
		t.Errorf("runs %d, keys %d, connects %v, max_load %g, load_wait %v; want 6, 100, 0, 8 and 2h",
			f.Runs, f.Keys, f.Connects, f.MaxLoad, f.LoadWait)
	}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]bool{}
	results := 0
	for _, b := range blocks {
		if !b.kernel || b.connect || b.study != "loss" {
			t.Errorf("%s: kernel %v, connect %v; want a loss study's block in the kernel without G7", b, b.kernel, b.connect)
		}
		if want := fmt.Sprintf("loss=%g%%", b.net.Loss); b.factor != want {
			t.Errorf("%s: factor %q, want %q", b, b.factor, want)
		}
		results += b.results()
		for _, sc := range b.scenarios {
			cells[fmt.Sprintf("%s %s %s %s %s", b.series.Name, sc.ID, sc.Protocol, b.net.Name, b.factor)] = true
		}
	}
	if results*f.Runs != 360 || len(cells) != 60 {
		t.Errorf("%d results a run in %d cells, want 60 and 360 in all", results, len(cells))
	}
	for _, s := range []string{"v1-native", "guacd"} {
		for _, net := range []string{"N1", "N2"} {
			for _, loss := range []string{"0", "0.5", "2"} {
				for _, cell := range []string{"T1 ssh", "G2 vnc", "G2 rdp", "G5 vnc", "G5 rdp"} {
					if !cells[fmt.Sprintf("%s %s %s loss=%s%%", s, cell, net, loss)] {
						t.Errorf("no %s for %s under %s with %s%% loss", cell, s, net, loss)
					}
				}
			}
		}
	}
}

// Step 3a's plan measures step 2's loss study with v1-native-cubic in
// guacd's place, 8 times, so that Holm's correction over its 30 cells
// can find a difference.
func TestRealtime3aPlan(t *testing.T) {
	f, err := readPlanFile("plans/realtime-3a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Runs != 8 || f.Keys != 100 || f.Connects == nil || *f.Connects != 0 || f.MaxLoad != 8 || f.LoadWait != 2*time.Hour {
		t.Errorf("runs %d, keys %d, connects %v, max_load %g, load_wait %v; want 8, 100, 0, 8 and 2h",
			f.Runs, f.Keys, f.Connects, f.MaxLoad, f.LoadWait)
	}
	blocks, err := studyBlocks(f, testSeries())
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]bool{}
	results := 0
	for _, b := range blocks {
		if !b.kernel || b.connect || b.study != "loss" {
			t.Errorf("%s: kernel %v, connect %v; want a loss study's block in the kernel without G7", b, b.kernel, b.connect)
		}
		results += b.results()
		for _, sc := range b.scenarios {
			cells[fmt.Sprintf("%s %s %s %s %s", b.series.Name, sc.ID, sc.Protocol, b.net.Name, b.factor)] = true
		}
	}
	if results*f.Runs != 480 || len(cells) != 60 {
		t.Errorf("%d results a run in %d cells, want 60 and 480 in all", results, len(cells))
	}
	for _, s := range []string{"v1-native-cubic", "v1-native"} {
		for _, net := range []string{"N1", "N2"} {
			for _, loss := range []string{"0", "0.5", "2"} {
				for _, cell := range []string{"T1 ssh", "G2 vnc", "G2 rdp", "G5 vnc", "G5 rdp"} {
					if !cells[fmt.Sprintf("%s %s %s loss=%s%%", s, cell, net, loss)] {
						t.Errorf("no %s for %s under %s with %s%% loss", cell, s, net, loss)
					}
				}
			}
		}
	}
}

// A plan's keys and connects set how many samples a scenario takes, a
// tenth of them in a quick run; unset, they are 200 and 20.
func TestPlanSamples(t *testing.T) {
	for _, c := range []struct {
		r              runner
		keys, connects int
	}{
		{runner{}, 200, 20},
		{runner{keys: 100, connects: 5}, 100, 5},
		{runner{keys: 100, quick: true}, 10, 3},
	} {
		if k, n := c.r.probeKeys(), c.r.connectSamples(); k != c.keys || n != c.connects {
			t.Errorf("keys %d, connects %d: %d keys and %d connects, want %d and %d", c.r.keys, c.r.connects, k, n, c.keys, c.connects)
		}
	}
}

// A plan file with a field the harness does not know, an unknown study,
// series or network, a block twice, or a loss outside a loss study or out
// of range, is refused.
func TestPlanFileRefused(t *testing.T) {
	for name, text := range map[string]string{
		"field":       "runs: 1\nrun: 2\nstudies: [{study: matrix, series: [guacd], nets: [N0], scenarios: all}]\n",
		"study":       "runs: 1\nstudies: [{study: soak, series: [guacd], nets: [N0], scenarios: all}]\n",
		"series":      "runs: 1\nstudies: [{study: matrix, series: [guacamole], nets: [N0], scenarios: all}]\n",
		"net":         "runs: 1\nstudies: [{study: matrix, series: [guacd], nets: [N9], scenarios: all}]\n",
		"twice":       "runs: 1\nstudies: [{study: matrix, series: [guacd], nets: [N0, N0], scenarios: all}]\n",
		"no runs":     "studies: [{study: matrix, series: [guacd], nets: [N0], scenarios: all}]\n",
		"matrix loss": "runs: 1\nstudies: [{study: matrix, series: [guacd], nets: [N0], loss: [1], scenarios: all}]\n",
		"no loss":     "runs: 1\nstudies: [{study: loss, series: [guacd], nets: [N0], scenarios: all}]\n",
		"all lost":    "runs: 1\nstudies: [{study: loss, series: [guacd], nets: [N0], loss: [100], scenarios: all}]\n",
		"loss twice":  "runs: 1\nstudies: [{study: loss, series: [guacd], nets: [N0], loss: [1, 1], scenarios: all}]\n",
	} {
		file := filepath.Join(t.TempDir(), "plan.yaml")
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := readPlanFile(file)
		if err == nil {
			_, err = studyBlocks(f, testSeries())
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Without a seed every run keeps the order of the blocks. With one, each
// run has an order of its own, the same whenever it is drawn, so that a
// resumed run keeps the order it began with.
func TestOrder(t *testing.T) {
	blocks, err := matrixBlocks("matrix", testSeries()[:3], netConditions, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	names := func(bs []block) []string {
		var s []string
		for _, b := range bs {
			s = append(s, b.String())
		}
		return s
	}
	p := plan{blocks: blocks}
	if !slices.Equal(names(p.order(3)), names(blocks)) {
		t.Error("without a seed, the order changed")
	}
	p.seed = 42
	run0, run1 := names(p.order(0)), names(p.order(1))
	if !slices.Equal(run0, names(p.order(0))) {
		t.Error("run 0's order is not the same when drawn again")
	}
	if slices.Equal(run0, run1) || slices.Equal(run0, names(blocks)) {
		t.Error("runs 0 and 1 have the same order, or the blocks' own")
	}
	slices.Sort(run0)
	all := names(blocks)
	slices.Sort(all)
	if !slices.Equal(run0, all) {
		t.Error("run 0 does not measure every block once")
	}
}

// refusing is a series whose sessions fail at once, counting how often it
// was asked for one.
func refusing(t *testing.T, name string, opens *atomic.Int64) series {
	return series{Name: name, target: echoServer(t), open: func(context.Context, string, string) (*session, error) {
		opens.Add(1)
		return nil, errors.New("refused")
	}}
}

// Resuming keeps the blocks with all their results and no error, measures
// the others again, and refuses the results of another plan.
func TestResume(t *testing.T) {
	defer func(d time.Duration) { gap = d }(gap)
	gap = 0
	var opens atomic.Int64
	a, b := refusing(t, "v1-native", &opens), refusing(t, "guacd", &opens)
	scs := []scenario{{ID: "T1", Protocol: "ssh"}}
	blocks, err := matrixBlocks("matrix", []series{a, b}, netConditions[:1], scs)
	if err != nil {
		t.Fatal(err)
	}
	p := plan{blocks: blocks, runs: 2, seed: 7, quick: true, engine: "none"}
	ok := func(series string, run int) []result {
		return []result{
			{Scenario: "T1", Protocol: "ssh", Series: series, Net: "N0", Run: run, Study: "matrix"},
			{Scenario: "G7", Protocol: "ssh", Series: series, Net: "N0", Run: run, Study: "matrix"},
		}
	}
	prev := &report{Quick: true, Plan: p.record(), Runs: 2, Results: slices.Concat(
		ok("v1-native", 0), // complete
		ok("guacd", 0)[:1], // incomplete
		ok("v1-native", 1), // complete
		[]result{{Scenario: "T1", Protocol: "ssh", Series: "guacd", Net: "N0", Run: 1, Study: "matrix", Error: "lost"},
			{Scenario: "G7", Protocol: "ssh", Series: "guacd", Net: "N0", Run: 1, Study: "matrix"}}, // an error
	)}
	if err := p.continues(prev); err != nil {
		t.Fatal(err)
	}
	var saves int
	p.save = func(*report) error { saves++; return nil }
	rep := within(t, 10*time.Second, func() report {
		rep, err := p.measure(context.Background(), prev)
		if err != nil {
			t.Error(err)
		}
		return rep
	})
	if n := opens.Load(); n != 4 { // guacd's T1 and G7, in two runs
		t.Errorf("%d sessions opened, want 4: guacd's two blocks again", n)
	}
	if saves != 2 || len(rep.Results) != 8 || len(rep.Resumed) != 1 {
		t.Errorf("%d saves, %d results, resumed %v; want 2, 8 and once", saves, len(rep.Results), rep.Resumed)
	}

	for name, change := range map[string]func(*report){
		"no plan": func(r *report) { r.Plan = nil },
		"quick":   func(r *report) { r.Quick = false },
		"seed":    func(r *report) { r.Plan = &planRecord{Seed: 8, Blocks: r.Plan.Blocks} },
		"blocks":  func(r *report) { r.Plan = &planRecord{Seed: 7, Blocks: r.Plan.Blocks[:1]} },
	} {
		other := *prev
		change(&other)
		if err := p.continues(&other); err == nil {
			t.Errorf("%s: continued", name)
		}
	}
}

// The load gate waits until the load is below its bound, gives up after
// its wait with errBusy, and does not wait without a bound.
func TestWaitLoad(t *testing.T) {
	defer func(f func() (float64, error), poll time.Duration) { loadAverage, loadPoll = f, poll }(loadAverage, loadPoll)
	loadPoll = time.Millisecond
	loads := []float64{12, 9, 7.5}
	loadAverage = func() (float64, error) {
		l := loads[0]
		if len(loads) > 1 {
			loads = loads[1:]
		}
		return l, nil
	}
	p := plan{maxLoad: 8, loadWait: time.Minute}
	if err := p.waitLoad(context.Background()); err != nil || len(loads) != 1 {
		t.Errorf("waitLoad = %v with %v left; want nil once the load is 7.5", err, loads)
	}
	loads = []float64{30}
	p.loadWait = 20 * time.Millisecond
	if err := p.waitLoad(context.Background()); !errors.Is(err, errBusy) {
		t.Errorf("waitLoad = %v, want errBusy", err)
	}
	p.maxLoad = 0
	if err := p.waitLoad(context.Background()); err != nil {
		t.Errorf("without a bound: %v", err)
	}
}

// The load gate holds each result, not only each block: a result that
// leaves the host busy holds the next one of its block, and the
// collection stops there, with what it measured saved.
func TestLoadGateEachResult(t *testing.T) {
	defer func(f func() (float64, error), poll, d time.Duration) {
		loadAverage, loadPoll, gap = f, poll, d
	}(loadAverage, loadPoll, gap)
	loadPoll, gap = time.Millisecond, 0
	var busy atomic.Bool
	loadAverage = func() (float64, error) {
		if busy.Load() {
			return 30, nil
		}
		return 1, nil
	}
	busying := series{Name: "guacd", target: echoServer(t), open: func(context.Context, string, string) (*session, error) {
		busy.Store(true)
		return nil, errors.New("refused")
	}}
	blocks, err := matrixBlocks("matrix", []series{busying}, netConditions[:1], []scenario{{ID: "T1", Protocol: "ssh"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := blocks[0].results(); n != 2 { // T1, and SSH's G7
		t.Fatalf("a block of %d results, want 2", n)
	}
	saved := -1
	p := plan{blocks: blocks, runs: 1, quick: true, engine: "none", maxLoad: 8, loadWait: 20 * time.Millisecond,
		save: func(rep *report) error { saved = len(rep.Results); return nil }}
	type measured struct {
		rep report
		err error
	}
	got := within(t, 10*time.Second, func() measured {
		rep, err := p.measure(context.Background(), nil)
		return measured{rep, err}
	})
	if !errors.Is(got.err, errBusy) {
		t.Errorf("measure = %v, want errBusy before the block's second result", got.err)
	}
	if len(got.rep.Results) != 1 || got.rep.Results[0].Scenario != "T1" || saved != 1 {
		t.Errorf("%d results, %d saved; want T1's alone, saved", len(got.rep.Results), saved)
	}
}

// A results file is written whole, and reads back.
func TestWriteReport(t *testing.T) {
	file := filepath.Join(t.TempDir(), "results.json")
	rep := report{Runs: 7, Plan: &planRecord{Seed: 1, Blocks: []string{"matrix guacd N0: T1 ssh"}},
		Results: []result{{Scenario: "T1", Series: "guacd", Load: 3.5, At: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}}}
	if err := writeReport(file, &rep); err != nil {
		t.Fatal(err)
	}
	got, err := readReport(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs != 7 || got.Plan.Seed != 1 || len(got.Results) != 1 || got.Results[0].Load != 3.5 || !got.Results[0].At.Equal(rep.Results[0].At) {
		t.Errorf("read back %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Errorf("%d files beside the results, want none", len(entries)-1)
	}
	if prev, err := resumable(filepath.Join(t.TempDir(), "none.json")); prev != nil || err != nil {
		t.Errorf("a missing file: %v, %v; want nothing to resume", prev, err)
	}
}

// Slow links stretch the waits in proportion to how slow they are.
func TestSlowness(t *testing.T) {
	want := map[string]int64{"N0": 1, "N1": 1, "N2": 1, "N3": 4, "N4": 20}
	for _, n := range netConditions {
		if got := n.slowness(); got != want[n.Name] {
			t.Errorf("%s: slowness %d, want %d", n.Name, got, want[n.Name])
		}
	}
}

func TestParseLoadAverage(t *testing.T) {
	for text, want := range map[string]float64{" 35.40 25.02 16.16 ": 35.4, "0.52 0.58 0.59 1/389 12345\n": 0.52} {
		if got, err := parseLoadAverage(text); err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", text, got, err, want)
		}
	}
	if _, err := parseLoadAverage(""); err == nil {
		t.Error("no load: no error")
	}
}

// podman's statistics give each service's CPU time, memory in use and
// processes.
func TestParseStats(t *testing.T) {
	out := []byte(`[{"name":"occamy-guacd-1","cpu_time":"4.768068s","mem_usage":"11.46MB / 8.287GB","pids":"3"},
		{"name":"occamy-occamy-v1-1","cpu_time":"1m2s","mem_usage":"1.5GiB / 8GB","pids":"12"}]`)
	cs, err := parseStats(out, "occamy")
	if err != nil {
		t.Fatal(err)
	}
	if cs.cpu["guacd"] != 4768068*time.Microsecond || cs.cpu["occamy-v1"] != 62*time.Second {
		t.Errorf("cpu %v", cs.cpu)
	}
	if d := cs.mem["guacd"] - 11.46e6/(1<<20); d > 1e-9 || d < -1e-9 || cs.mem["occamy-v1"] != 1536 {
		t.Errorf("memory %v", cs.mem)
	}
	if cs.pids["guacd"] != 3 || cs.pids["occamy-v1"] != 12 {
		t.Errorf("pids %v", cs.pids)
	}
	for _, s := range []string{"12XB", "MB", ""} {
		if _, err := parseSize(s); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
}

// The runtime comes from occamyd's /debug/vars: its state at the end, and
// what it spent between the two reads.
func TestReadRuntime(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/vars" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) == 1 {
			w.Write([]byte(`{"goroutines":10,"fds":20,"memstats":{"HeapInuse":1048576,"TotalAlloc":1048576,"NumGC":3,"PauseTotalNs":1000000}}`))
			return
		}
		w.Write([]byte(`{"goroutines":14,"fds":22,"memstats":{"HeapInuse":4194304,"TotalAlloc":11534336,"NumGC":8,"PauseTotalNs":3500000}}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	r := &runner{debug: map[string]string{"occamy-v1": addr, "occamy-v1-cubic": "127.0.0.1:1"}}
	o := r.observe(context.Background())
	var res result
	o.finish(context.Background(), &res)
	got, ok := res.Runtime["occamy-v1"]
	want := runtimeStat{Goroutines: 14, FDs: 22, HeapMiB: 4, GCs: 5, GCPauseMS: 2.5, AllocMiB: 10}
	if !ok || got != want {
		t.Errorf("runtime %+v, want %+v", got, want)
	}
	if _, ok := res.Runtime["occamy-v1-cubic"]; ok {
		t.Error("a listener that did not answer has a runtime")
	}
	if m, err := parseDebug("occamy-v1=localhost:5638,occamy-v1-cubic=localhost:5643"); err != nil || len(m) != 2 {
		t.Errorf("parseDebug: %v, %v", m, err)
	}
	if _, err := parseDebug("occamy-v1"); err == nil {
		t.Error("parseDebug without an address: no error")
	}
}
