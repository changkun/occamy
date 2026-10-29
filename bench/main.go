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
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// series is a way of reaching guacd.
type series struct {
	Name   string
	target string
	dial   func(ctx context.Context, addr string, cred config.JWT) (rawConn, error)
}

func main() {
	var (
		addr      = flag.String("addr", "localhost:5636", "occamyd address")
		guacdAddr = flag.String("guacd", "localhost:4822", "guacd address for the direct series")
		scen      = flag.String("scenarios", "all", "comma-separated scenario IDs, or all")
		nets      = flag.String("net", "N0,N1,N2", "comma-separated network conditions")
		seriesF   = flag.String("series", "occamy,guacd", "comma-separated series: occamy, guacd")
		quick     = flag.Bool("quick", false, "fewer samples and shorter scenarios, to check the setup")
		engine    = flag.String("engine", "podman", "container engine for CPU statistics, or none")
		project   = flag.String("project", "docker", "compose project of the stack")
		out       = flag.String("out", "bench-results.json", "JSON output file")
	)
	flag.Parse()
	log.SetFlags(log.Ltime)

	all := []series{
		{Name: "occamy", target: *addr, dial: dialOccamy},
		{Name: "guacd", target: *guacdAddr, dial: dialGuacd},
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

	report := report{
		Started: time.Now().UTC(),
		Quick:   *quick,
		Host:    hostInfo(*engine, *project),
	}
	ctx := context.Background()
	for _, sr := range chosenSeries {
		for _, nc := range chosenNets {
			p, err := startProxy(sr.target, nc)
			if err != nil {
				log.Fatal(err)
			}
			r := &runner{
				series: sr, net: nc, proxy: p, quick: *quick,
				cpu: newCPUSampler(*engine, *project),
			}
			for _, sc := range chosen {
				report.Results = append(report.Results, r.run(ctx, sc))
			}
			for _, proto := range protocolsOf(chosen) {
				report.Results = append(report.Results, r.connect(ctx, proto))
			}
			p.close()
		}
	}

	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
		log.Fatal(err)
	}
	report.markdown(os.Stdout)
	for _, r := range report.Results {
		if r.Error != "" {
			os.Exit(1)
		}
	}
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
	series series
	net    netCondition
	proxy  *proxy
	quick  bool
	cpu    *cpuSampler
}

// gap separates sessions, so that a session with the same claims does
// not join the previous one while it ends
// (specs/003-session-lifecycle-hardening.md, defect 6).
const gap = 2 * time.Second

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
	return &result{Scenario: id, Protocol: proto, Series: r.series.Name, Net: r.net.Name}
}

// run runs one scenario on a fresh session.
func (r *runner) run(ctx context.Context, sc scenario) result {
	res := r.newResult(sc.ID, sc.Protocol)
	log.Printf("%s %s %s", res.Series, res.Net, res.Scenario)
	defer time.Sleep(gap)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	start := time.Now()
	conn, err := r.series.dial(ctx, r.proxy.addr(), credentials[sc.Protocol])
	if err != nil {
		res.Error = err.Error()
		return *res
	}
	s := newSession(conn, start)
	err = sc.run(ctx, r, s, res)
	if cerr := s.close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		res.Error = err.Error()
		log.Printf("  %v", err)
	}
	if len(s.display.Unsupported) > 0 {
		res.Unsupported = s.display.Unsupported
	}
	return *res
}

// connect (G7) measures the time to the first frame of new sessions.
func (r *runner) connect(ctx context.Context, proto string) result {
	res := r.newResult("G7", proto)
	log.Printf("%s %s %s %s", res.Series, res.Net, res.Scenario, proto)
	var times []time.Duration
	for range r.samples(20) {
		start := time.Now()
		conn, err := r.series.dial(ctx, r.proxy.addr(), credentials[proto])
		if err != nil {
			res.Error = err.Error()
			break
		}
		s := newSession(conn, start)
		_, err = s.wait(ctx, start, func(frame) bool { return true })
		s.close()
		if err != nil {
			res.Error = err.Error()
			break
		}
		times = append(times, time.Duration(s.firstFrame.Load()))
		time.Sleep(gap)
	}
	res.FirstFrame = summarize(times)
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

	Latency    *percentiles `json:"latency_ms,omitempty"`
	FirstFrame *percentiles `json:"first_frame_ms,omitempty"`
	Completion float64      `json:"completion_ms,omitempty"`

	Seconds   float64            `json:"seconds,omitempty"`
	BytesUp   int64              `json:"bytes_up,omitempty"`
	BytesDown int64              `json:"bytes_down,omitempty"`
	Frames    int64              `json:"frames,omitempty"`
	DecodeMS  float64            `json:"client_decode_ms,omitempty"`
	CPUMS     map[string]float64 `json:"cpu_ms,omitempty"`

	Unsupported map[string]int `json:"unsupported,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type report struct {
	Started time.Time         `json:"started"`
	Quick   bool              `json:"quick"`
	Host    map[string]string `json:"host"`
	Results []result          `json:"results"`
}

func (rep *report) markdown(w io.Writer) {
	fmt.Fprintf(w, "Started %s", rep.Started.Format(time.RFC3339))
	if rep.Quick {
		fmt.Fprint(w, " (quick)")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Scenario | Series | Net | Latency p50/p95/p99 ms | First frame p50 ms | Completion ms | Down KiB | Down KiB/s | Frames/s | occamy+guacd CPU ms | Remote CPU ms | Decode ms | Notes |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range rep.Results {
		lat, first, done := "", "", ""
		if r.Latency != nil {
			lat = fmt.Sprintf("%.1f / %.1f / %.1f", r.Latency.P50, r.Latency.P95, r.Latency.P99)
		}
		if r.FirstFrame != nil {
			first = fmt.Sprintf("%.1f", r.FirstFrame.P50)
		}
		if r.Completion > 0 {
			done = fmt.Sprintf("%.0f", r.Completion)
		}
		down, rate, fps := "", "", ""
		if r.Seconds > 0 {
			down = fmt.Sprintf("%.1f", float64(r.BytesDown)/1024)
			rate = fmt.Sprintf("%.1f", float64(r.BytesDown)/1024/r.Seconds)
			fps = fmt.Sprintf("%.1f", float64(r.Frames)/r.Seconds)
		}
		server, remote := "", ""
		if r.CPUMS != nil {
			server = fmt.Sprintf("%.0f", r.CPUMS["occamy"]+r.CPUMS["guacd"])
			remote = fmt.Sprintf("%.0f", r.CPUMS[r.Protocol])
		}
		decode := ""
		if r.DecodeMS > 0 {
			decode = fmt.Sprintf("%.0f", r.DecodeMS)
		}
		var notes []string
		if r.Error != "" {
			notes = append(notes, "error: "+r.Error)
		}
		for op, n := range r.Unsupported {
			notes = append(notes, fmt.Sprintf("%s ×%d", op, n))
		}
		scen := r.Scenario
		if r.Scenario == "G7" {
			scen += " " + r.Protocol
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			scen, r.Series, r.Net, lat, first, done, down, rate, fps, server, remote, decode,
			strings.ReplaceAll(strings.Join(notes, "; "), "|", "/"))
	}
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

var services = []string{"occamy", "guacd", "ssh", "vnc", "rdp"}

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
		run("guacd_image", engine, "inspect", "--format", "{{.ImageName}} {{.Image}}", project+"-guacd-1")
	}
	return info
}
