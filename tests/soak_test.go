// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

// TestSoak is the soak of Decision 4 in
// specs/008-native-architecture/guacd-bridge.md, which looks for leaks in
// occamyd rather than deciding a default or a deletion: sessions against
// occamy-v1 for a duration, each moving the pointer and typing every few
// seconds and asking its desktop for screen motion now and then, while
// the test samples occamyd's goroutines, live heap and open files from
// its debug listener. It fails if any of them grows past a bound after
// the warm-up (soakGrowth), or does not come back near where it started
// once every session has closed, and if sessions fail. It is skipped
// unless OCCAMY_SOAK_SESSIONS is set; make soak sets the rest, against a
// stack whose debug listeners make run OCCAMY_DEBUG=1 turned on:
//
//	make run OCCAMY_DEBUG=1
//	make soak OCCAMY_SOAK_SESSIONS=8 OCCAMY_SOAK_DURATION=30m
//
// Decision 4's full soak is OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h.
//
// The variables:
//
//	OCCAMY_SOAK_SESSIONS     sessions at once
//	OCCAMY_SOAK_DURATION     how long, 1h by default
//	OCCAMY_SOAK_CONNECTIONS  the connections that the sessions take in
//	                         turn, tigervnc-native by default: x11vnc
//	                         hangs now and then, and its watchdog's
//	                         restarts would end sessions
//	                         (docker/demo/vnc/start.sh)
//	OCCAMY_SOAK_LIFETIME     how long a session lasts before it closes and
//	                         another opens, 10m by default; 0 keeps each
//	                         for the whole run
//	OCCAMY_SOAK_INTERVAL     how often to sample, 30s by default
//	OCCAMY_SOAK_WARMUP       how long before samples count, a tenth of the
//	                         duration by default; the samples after it
//	                         must number soakMinSamples at least
//	OCCAMY_SOAK_BOUNDS       the growth allowed, goroutines=50,heap=32,fds=25
//	                         by default, the heap in MiB
//	OCCAMY_SOAK_DEBUG        occamy-v1's debug listener, from
//	                         OCCAMY_V1_DEBUG_PORT in make soak, which
//	                         make run OCCAMY_DEBUG=1 publishes
//	OCCAMY_SOAK_OUT          a CSV file for the samples
//
// Every other session has a read-only viewer, by a share, for its life.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
)

// soakConfig is TestSoak's configuration, from its variables.
type soakConfig struct {
	sessions    int
	duration    time.Duration
	connections []string
	lifetime    time.Duration
	interval    time.Duration
	warmup      time.Duration
	bounds      map[string]float64
	debug       string
	out         string
}

// soakMetrics are what a sample holds and the bounds name.
var soakMetrics = []string{"goroutines", "heap", "fds"}

// soakMinSamples is the fewest samples after the warm-up that soakGrowth
// judges: three in each quarter, so that no one sample is a quarter's
// median.
const soakMinSamples = 12

// soakSampleTries is how often the soak tries to sample occamyd before it
// gives up, soakSamplePause apart.
const (
	soakSampleTries = 3
	soakSamplePause = 5 * time.Second
)

func soakConfigFromEnv() (soakConfig, error) {
	cfg := soakConfig{
		duration:    time.Hour,
		connections: []string{"tigervnc-native"},
		lifetime:    10 * time.Minute,
		interval:    30 * time.Second,
		bounds:      map[string]float64{"goroutines": 50, "heap": 32, "fds": 25},
		debug:       os.Getenv("OCCAMY_SOAK_DEBUG"),
		out:         os.Getenv("OCCAMY_SOAK_OUT"),
	}
	var err error
	if cfg.sessions, err = strconv.Atoi(os.Getenv("OCCAMY_SOAK_SESSIONS")); err != nil || cfg.sessions < 1 {
		return cfg, fmt.Errorf("OCCAMY_SOAK_SESSIONS=%q: want a number of sessions", os.Getenv("OCCAMY_SOAK_SESSIONS"))
	}
	for name, d := range map[string]*time.Duration{
		"OCCAMY_SOAK_DURATION": &cfg.duration,
		"OCCAMY_SOAK_LIFETIME": &cfg.lifetime,
		"OCCAMY_SOAK_INTERVAL": &cfg.interval,
		"OCCAMY_SOAK_WARMUP":   &cfg.warmup,
	} {
		if v := os.Getenv(name); v != "" {
			if *d, err = time.ParseDuration(v); err != nil {
				return cfg, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if cfg.warmup == 0 {
		cfg.warmup = cfg.duration / 10
	}
	if cfg.interval <= 0 || cfg.duration <= 0 || cfg.warmup >= cfg.duration {
		return cfg, errors.New("OCCAMY_SOAK_INTERVAL and OCCAMY_SOAK_DURATION must be positive, and OCCAMY_SOAK_WARMUP shorter than the duration")
	}
	if n := (cfg.duration - cfg.warmup) / cfg.interval; n < soakMinSamples {
		return cfg, fmt.Errorf("%v after a warm-up of %v, sampled every %v, gives %d samples, fewer than the %d that judging growth takes: soak longer or sample more often",
			cfg.duration, cfg.warmup, cfg.interval, n, soakMinSamples)
	}
	if v := os.Getenv("OCCAMY_SOAK_CONNECTIONS"); v != "" {
		cfg.connections = strings.Split(v, ",")
	}
	if v := os.Getenv("OCCAMY_SOAK_BOUNDS"); v != "" {
		for _, kv := range strings.Split(v, ",") {
			name, value, _ := strings.Cut(kv, "=")
			f, err := strconv.ParseFloat(value, 64)
			if _, known := cfg.bounds[name]; !known || err != nil || f < 0 {
				return cfg, fmt.Errorf("OCCAMY_SOAK_BOUNDS: %q: want goroutines, heap or fds = a number", kv)
			}
			cfg.bounds[name] = f
		}
	}
	if cfg.debug == "" {
		return cfg, errors.New("OCCAMY_SOAK_DEBUG is not set: occamy-v1's debug listener, such as localhost:5638, which make run OCCAMY_DEBUG=1 publishes")
	}
	return cfg, nil
}

// A soakSample is occamyd's state at a time: the goroutines, the heap
// that a collection left, in MiB, and the open files; and the host's
// load average and the sessions open.
type soakSample struct {
	at       time.Duration
	values   map[string]float64
	sessions int64
	load     string
}

// soakStats counts what the workers did.
type soakStats struct {
	open, opened, frames, inputs atomic.Int64

	mu     sync.Mutex
	errors []string
}

func (s *soakStats) fail(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, fmt.Sprintf("%s: ", time.Now().Format(time.TimeOnly))+fmt.Sprintf(format, args...))
}

func TestSoak(t *testing.T) {
	if os.Getenv("OCCAMY_SOAK_SESSIONS") == "" {
		t.Skip("OCCAMY_SOAK_SESSIONS not set, see TestSoak")
	}
	cfg, err := soakConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	addr := endpointV1(t)
	var out *csv.Writer
	if cfg.out != "" {
		f, err := os.Create(cfg.out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = csv.NewWriter(f)
		defer out.Flush()
		_ = out.Write(append(append([]string{"seconds"}, soakMetrics...), "sessions", "load"))
	}
	t.Logf("%d sessions over %v for %v, sessions of %v, sampled every %v after %v of warm-up, bounds %v",
		cfg.sessions, cfg.connections, cfg.duration, cfg.lifetime, cfg.interval, cfg.warmup, cfg.bounds)

	start := time.Now()
	record := func(s soakSample) {
		t.Logf("%6.0fs: %4.0f goroutines, %6.1f MiB heap, %3.0f fds, %2d sessions, load %s",
			s.at.Seconds(), s.values["goroutines"], s.values["heap"], s.values["fds"], s.sessions, s.load)
		if out != nil {
			row := []string{strconv.FormatFloat(s.at.Seconds(), 'f', 0, 64)}
			for _, m := range soakMetrics {
				row = append(row, strconv.FormatFloat(s.values[m], 'f', -1, 64))
			}
			_ = out.Write(append(row, strconv.FormatInt(s.sessions, 10), s.load))
			out.Flush()
		}
	}
	stats := &soakStats{}
	sampleNow := func() soakSample {
		s, err := soakRetry(soakSampleTries, soakSamplePause, t.Logf, func() (soakSample, error) { return soakSampleDebug(cfg.debug) })
		if err != nil {
			t.Fatalf("sampling occamyd at %s, which make run OCCAMY_DEBUG=1 publishes: %v", cfg.debug, err)
		}
		s.at, s.sessions = time.Since(start), stats.open.Load()
		record(s)
		return s
	}
	before := sampleNow()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()
	var wg sync.WaitGroup
	for i := range cfg.sessions {
		wg.Go(func() { soakWorker(ctx, addr, i, cfg, stats) })
	}
	var samples []soakSample
	tick := time.NewTicker(cfg.interval)
	for done := false; !done; {
		select {
		case <-tick.C:
			if s := sampleNow(); s.at >= cfg.warmup {
				samples = append(samples, s)
			}
		case <-ctx.Done():
			done = true
		}
	}
	tick.Stop()
	wg.Wait()

	// Every session has closed: occamyd should come back near where it
	// started, once the sessions' goroutines have ended.
	var after soakSample
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(5 * time.Second) {
		after = sampleNow()
		if after.values["goroutines"] <= before.values["goroutines"]+cfg.bounds["goroutines"] || time.Now().After(deadline) {
			break
		}
	}
	t.Logf("%d sessions opened, %d frames, %d inputs, %d errors", stats.opened.Load(), stats.frames.Load(), stats.inputs.Load(), len(stats.errors))
	for _, e := range stats.errors {
		t.Errorf("session error: %s", e)
	}
	for _, m := range soakMetrics {
		var series []float64
		for _, s := range samples {
			series = append(series, s.values[m])
		}
		switch grew, detail, err := soakGrowth(series, cfg.bounds[m]); {
		case err != nil:
			t.Errorf("%s: %v", m, err)
		case grew:
			t.Errorf("%s grew past %v: %s", m, cfg.bounds[m], detail)
		default:
			t.Logf("%s: %s", m, detail)
		}
	}
	for _, e := range soakAfter(before, after, cfg.bounds) {
		t.Error(e)
	}
}

// soakAfter compares each metric once the sessions have closed with its
// value before they opened, and returns what stayed above it by more
// than its bound.
func soakAfter(before, after soakSample, bounds map[string]float64) []string {
	var stayed []string
	for _, m := range soakMetrics {
		if after.values[m] > before.values[m]+bounds[m] {
			stayed = append(stayed, fmt.Sprintf("%s: %v once the sessions closed, %v before they opened", m, after.values[m], before.values[m]))
		}
	}
	return stayed
}

// soakRetry calls get up to tries times, pause apart, until it succeeds,
// logging each failure, and returns the last error if none does.
func soakRetry(tries int, pause time.Duration, logf func(string, ...any), get func() (soakSample, error)) (soakSample, error) {
	for try := 1; ; try++ {
		s, err := get()
		if err == nil || try == tries {
			return s, err
		}
		logf("sampling, try %d of %d: %v", try, tries, err)
		time.Sleep(pause)
	}
}

// soakGrowth judges a series of samples taken after the warm-up, by the
// medians of its four quarters: it grew if the last median exceeds the
// first by more than bound, whatever the quarters between do. Medians,
// so that a sample taken while sessions open or close does not decide:
// a quarter has three samples at least (soakMinSamples), and one of
// even length takes the mean of its middle two. A dip between the
// first quarter and the last, even below the first, does not excuse
// growth: the last quarter still ends more than bound above the first,
// and further above the dip. Fewer than soakMinSamples samples are an
// error.
func soakGrowth(series []float64, bound float64) (bool, string, error) {
	if len(series) < soakMinSamples {
		return false, "", fmt.Errorf("%d samples after the warm-up, fewer than the %d that judging growth takes", len(series), soakMinSamples)
	}
	var medians []float64
	for q := range 4 {
		part := slices.Clone(series[q*len(series)/4 : (q+1)*len(series)/4])
		slices.Sort(part)
		medians = append(medians, (part[(len(part)-1)/2]+part[len(part)/2])/2)
	}
	detail := fmt.Sprintf("medians of the quarters %.1f, range %.1f to %.1f", medians, slices.Min(series), slices.Max(series))
	return medians[3]-medians[0] > bound, detail, nil
}

// soakSampleDebug reads occamyd's state from its debug listener: a heap
// profile with gc=1 first, which collects, so that memstats then shows
// the live heap.
func soakSampleDebug(debug string) (soakSample, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get("http://" + debug + "/debug/pprof/heap?gc=1")
	if err != nil {
		return soakSample{}, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	resp, err = c.Get("http://" + debug + "/debug/vars")
	if err != nil {
		return soakSample{}, err
	}
	defer resp.Body.Close()
	var vars struct {
		Goroutines *float64 `json:"goroutines"`
		FDs        *float64 `json:"fds"`
		MemStats   struct {
			HeapAlloc float64
		} `json:"memstats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&vars); err != nil {
		return soakSample{}, err
	}
	if vars.Goroutines == nil || vars.FDs == nil {
		return soakSample{}, errors.New("/debug/vars has no goroutines or fds: occamyd predates them")
	}
	return soakSample{
		values: map[string]float64{"goroutines": *vars.Goroutines, "heap": vars.MemStats.HeapAlloc / (1 << 20), "fds": *vars.FDs},
		load:   hostLoad(),
	}, nil
}

// hostLoad returns the host's load averages, as the shared host's
// measurements log them.
func hostLoad() string {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return "?"
		}
		return strings.Join(strings.Fields(string(b))[:3], " ")
	}
	b, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return "?"
	}
	return strings.Trim(strings.TrimSpace(string(b)), "{ }")
}

// soakWorker runs sessions one after another until ctx is done.
func soakWorker(ctx context.Context, addr string, i int, cfg soakConfig, stats *soakStats) {
	for n := 0; ctx.Err() == nil; n++ {
		conn := cfg.connections[(i+n)%len(cfg.connections)]
		if err := soakSession(ctx, addr, i, conn, cfg, stats); err != nil && ctx.Err() == nil {
			stats.fail("worker %d, %s: %v", i, conn, err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// soakSession runs one session for cfg.lifetime, or until ctx is done.
func soakSession(ctx context.Context, addr string, i int, conn string, cfg soakConfig, stats *soakStats) error {
	token, err := signIn(addr)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cl, err := client.Dial(dctx, "ws://"+addr+"/api/v1/connections/"+url.PathEscape(conn)+"/stream", token,
		client.Options{Name: "occamy-soak/1", Width: 1024, Height: 768})
	cancel()
	if err != nil {
		return err
	}
	stats.opened.Add(1)
	stats.open.Add(1)
	id := cl.SessionID()
	defer func() {
		// The access token may have expired meanwhile.
		if token, err := signIn(addr); err == nil {
			_ = apiV1(addr, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape(id), token, nil, nil)
		}
		cl.Close()
		stats.open.Add(-1)
	}()
	ended := make(chan error, 2)
	drain := func(cl *client.Session, who string) {
		for {
			ev, err := cl.Next(context.Background())
			if err != nil {
				ended <- fmt.Errorf("%s: %w", who, err)
				return
			}
			switch ev := ev.(type) {
			case client.Frame:
				stats.frames.Add(1)
			case client.End:
				ended <- fmt.Errorf("%s: %w", who, endError(ev))
				return
			}
		}
	}
	go drain(cl, "owner")
	if i%2 == 1 {
		var share struct {
			Token string `json:"token"`
		}
		body := []byte(`{"role":"read-only"}`)
		if err := apiV1(addr, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(id)+"/shares", token, body, &share); err != nil {
			return err
		}
		vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		viewer, err := client.Dial(vctx, "ws://"+addr+"/api/v1/sessions/"+url.PathEscape(id)+"/stream", share.Token,
			client.Options{Name: "occamy-soak-viewer/1", Width: 1024, Height: 768})
		cancel()
		if err != nil {
			return err
		}
		defer viewer.Close()
		go drain(viewer, "viewer")
	}

	life := cfg.lifetime
	if life <= 0 {
		life = cfg.duration
	}
	// Start the workers' sessions at different times, so that they do
	// not all close at once.
	if cfg.lifetime > 0 && stats.opened.Load() <= int64(cfg.sessions) {
		life = time.Duration(i+1) * cfg.lifetime / time.Duration(cfg.sessions)
	}
	end := time.After(life)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	rng := rand.New(rand.NewPCG(uint64(i), uint64(time.Now().UnixNano())))
	letters := "asdfghjkl"
	for step := 0; ; step++ {
		select {
		case <-ctx.Done():
			return nil
		case <-end:
			return nil
		case err := <-ended:
			return err
		case <-tick.C:
		}
		send := func(_ uint32, err error) error {
			stats.inputs.Add(1)
			return err
		}
		// Below the typing probe at the top left (desktop.sh), so that
		// the letters typed do not reach it and the parity matrix's
		// first typed line after a soak is its own.
		x, y := uint16(rng.IntN(1024)), uint16(100+rng.IntN(768-100))
		if err := send(cl.Pointer(ctx, x, y, 0)); err != nil {
			return err
		}
		c := uint32(letters[rng.IntN(len(letters))])
		var errs []error
		switch {
		case step%30 == 15:
			// Screen motion: a terminal that scrolls for two seconds
			// (docker/demo/vnc/desktop.sh).
			errs = append(errs, send(cl.Clipboard(ctx, "text/plain", []byte("motion: 2"))))
		case step%10 == 9:
			errs = append(errs, send(cl.Key(ctx, true, 0xff0d, 0x28)), send(cl.Key(ctx, false, 0xff0d, 0x28)))
		default:
			errs = append(errs, send(cl.Key(ctx, true, c, 0)), send(cl.Key(ctx, false, c, 0)))
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
}

// TestSoakGrowth checks how the soak judges a series of samples, without
// a stack.
func TestSoakGrowth(t *testing.T) {
	ramp := func(n int, from, step float64) []float64 {
		var s []float64
		for i := range n {
			s = append(s, from+float64(i)*step)
		}
		return s
	}
	// quarters returns a series of four quarters of n samples each, at
	// the values given.
	quarters := func(n int, values ...float64) []float64 {
		var s []float64
		for _, v := range values {
			s = append(s, ramp(n, v, 0)...)
		}
		return s
	}
	// Quarters of 100, 110 with one sample of 50, 120 and 130.
	lowOutlier := quarters(10, 100, 110, 120, 130)
	lowOutlier[13] = 50
	// Quarters of four at 100 but the last, half of it at 160: its
	// median is 130.
	halfHigh := append(quarters(4, 100, 100, 100), 100, 100, 160, 160)
	for _, tt := range []struct {
		name   string
		series []float64
		bound  float64
		grew   bool
	}{
		{"flat", ramp(40, 100, 0), 10, false},
		{"rising past the bound", ramp(40, 100, 1), 10, true},
		{"rising within the bound", ramp(40, 100, 0.2), 10, false},
		{"rising by exactly the bound", quarters(10, 100, 103, 107, 110), 10, false},
		{"rising by just past the bound", quarters(10, 100, 103, 107, 110.5), 10, true},
		{"falling", ramp(40, 200, -1), 10, false},
		{"a spike in the last quarter", append(ramp(39, 100, 0), 500), 10, false},
		{"up and down, ending near the start", quarters(10, 100, 150, 100, 105), 10, false},
		{"up and down, ending high", quarters(10, 100, 150, 100, 150), 10, true},
		{"steps up", quarters(10, 100, 105, 110, 120), 10, true},
		{"a plateau", quarters(10, 100, 100, 150, 200), 10, true},
		{"a plateau at the end", quarters(10, 100, 150, 200, 200), 10, true},
		{"a low outlier in a quarter", lowOutlier, 10, true},
		{"exactly the fewest samples", quarters(3, 100, 120, 140, 160), 10, true},
		{"a spike at the fewest samples", append(ramp(soakMinSamples-1, 100, 0), 500), 10, false},
		{"a low first sample at the fewest samples", append([]float64{50}, ramp(soakMinSamples-1, 100, 0)...), 10, false},
		{"half a quarter high, the median within the bound", halfHigh, 40, false},
		{"half a quarter high, the median past the bound", halfHigh, 20, true},
		{"growth early, then level", quarters(10, 100, 200, 200, 205), 10, true},
		{"a dip after growth", quarters(10, 100, 200, 199, 400), 10, true},
		{"a dip below the first, then growth", quarters(10, 100, 90, 150, 200), 10, true},
		{"a dip within the bound, then growth", quarters(10, 100, 99, 300, 400), 50, true},
		// A leak of 300 goroutines in a run whose second quarter dips.
		{"a leak with a dip", quarters(10, 121.5, 106.5, 271.5, 346.5), 50, true},
		{"a dip past the bound, then growth", quarters(10, 200, 100, 300, 400), 50, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			grew, detail, err := soakGrowth(tt.series, tt.bound)
			if err != nil {
				t.Fatal(err)
			}
			if grew != tt.grew {
				t.Errorf("soakGrowth = %v (%s), want %v", grew, detail, tt.grew)
			}
		})
	}
	// Fewer samples are refused, among them eight, where a quarter of
	// two would take one sample, such as a spike, for its median.
	for _, n := range []int{soakMinSamples - 1, 8} {
		if grew, detail, err := soakGrowth(append(ramp(n-1, 100, 0), 500), 10); err == nil {
			t.Errorf("soakGrowth judged %d samples: grew %v (%s)", n, grew, detail)
		}
	}
}

// TestSoakAfter checks that every metric, the heap too, must come back
// near where it started once the sessions have closed.
func TestSoakAfter(t *testing.T) {
	bounds := map[string]float64{"goroutines": 50, "heap": 32, "fds": 25}
	sample := func(g, h, f float64) soakSample {
		return soakSample{values: map[string]float64{"goroutines": g, "heap": h, "fds": f}}
	}
	before := sample(10, 2, 10)
	for _, tt := range []struct {
		after  soakSample
		stayed []string
	}{
		{sample(60, 34, 35), nil},
		{sample(61, 2, 10), []string{"goroutines"}},
		{sample(10, 34.5, 10), []string{"heap"}},
		{sample(10, 2, 36), []string{"fds"}},
	} {
		var stayed []string
		for _, e := range soakAfter(before, tt.after, bounds) {
			m, _, _ := strings.Cut(e, ":")
			stayed = append(stayed, m)
		}
		if !slices.Equal(stayed, tt.stayed) {
			t.Errorf("after %v: %v stayed up, want %v", tt.after.values, stayed, tt.stayed)
		}
	}
}

// TestSoakRetry checks that a failed sample is tried again before the
// soak gives up.
func TestSoakRetry(t *testing.T) {
	for _, tt := range []struct {
		fails, calls int
		ok           bool
	}{
		{0, 1, true},
		{soakSampleTries - 1, soakSampleTries, true},
		{soakSampleTries, soakSampleTries, false},
	} {
		calls := 0
		_, err := soakRetry(soakSampleTries, 0, t.Logf, func() (soakSample, error) {
			calls++
			if calls <= tt.fails {
				return soakSample{}, errors.New("refused")
			}
			return soakSample{}, nil
		})
		if calls != tt.calls || (err == nil) != tt.ok {
			t.Errorf("failing %d times: %d calls and %v, want %d calls, success %v", tt.fails, calls, err, tt.calls, tt.ok)
		}
	}
}

// TestSoakConfig checks the soak's defaults, and that a soak too short
// for soakMinSamples samples is refused before it starts.
func TestSoakConfig(t *testing.T) {
	t.Setenv("OCCAMY_SOAK_SESSIONS", "2")
	t.Setenv("OCCAMY_SOAK_DEBUG", "localhost:5638")
	cfg, err := soakConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.connections, []string{"tigervnc-native"}) {
		t.Errorf("the default connections are %v, want tigervnc-native alone", cfg.connections)
	}
	for _, tt := range []struct {
		duration, warmup, interval string
		ok                         bool
	}{
		{"1h", "", "30s", true},
		{"7m", "1m", "30s", true},
		{"7m", "1m", "31s", false},
		{"6m", "", "30s", false},
	} {
		t.Setenv("OCCAMY_SOAK_DURATION", tt.duration)
		t.Setenv("OCCAMY_SOAK_WARMUP", tt.warmup)
		t.Setenv("OCCAMY_SOAK_INTERVAL", tt.interval)
		if _, err := soakConfigFromEnv(); (err == nil) != tt.ok {
			t.Errorf("%s with a warm-up of %q every %s: %v, want success %v", tt.duration, tt.warmup, tt.interval, err, tt.ok)
		}
	}
}
