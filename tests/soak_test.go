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
// its debug listener. It fails if any of them grows monotonically past a
// bound after the warm-up, or does not come back near where it started
// once every session has closed, and if sessions fail. It is skipped
// unless OCCAMY_SOAK_SESSIONS is set; make soak sets the rest:
//
//	make run
//	make soak OCCAMY_SOAK_SESSIONS=8 OCCAMY_SOAK_DURATION=30m
//
// Decision 4's full soak is OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h.
//
// The variables:
//
//	OCCAMY_SOAK_SESSIONS     sessions at once
//	OCCAMY_SOAK_DURATION     how long, 1h by default
//	OCCAMY_SOAK_CONNECTIONS  the connections that the sessions take in
//	                         turn, vnc-native,tigervnc-native by default
//	OCCAMY_SOAK_LIFETIME     how long a session lasts before it closes and
//	                         another opens, 10m by default; 0 keeps each
//	                         for the whole run
//	OCCAMY_SOAK_INTERVAL     how often to sample, 30s by default
//	OCCAMY_SOAK_WARMUP       how long before samples count, a tenth of the
//	                         duration by default
//	OCCAMY_SOAK_BOUNDS       the growth allowed, goroutines=50,heap=32,fds=25
//	                         by default, the heap in MiB
//	OCCAMY_SOAK_DEBUG        occamy-v1's debug listener, from
//	                         OCCAMY_V1_DEBUG_PORT in make soak
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

func soakConfigFromEnv() (soakConfig, error) {
	cfg := soakConfig{
		duration:    time.Hour,
		connections: []string{"vnc-native", "tigervnc-native"},
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
		return cfg, errors.New("OCCAMY_SOAK_DEBUG is not set: occamy-v1's debug listener, such as localhost:5638")
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
		s, err := soakSampleDebug(cfg.debug)
		if err != nil {
			t.Fatalf("sampling occamyd: %v", err)
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
		if grew, detail := soakGrowth(series, cfg.bounds[m]); grew {
			t.Errorf("%s grew past %v: %s", m, cfg.bounds[m], detail)
		} else {
			t.Logf("%s: %s", m, detail)
		}
		if m != "heap" && after.values[m] > before.values[m]+cfg.bounds[m] {
			t.Errorf("%s: %v once the sessions closed, %v before they opened", m, after.values[m], before.values[m])
		}
	}
}

// soakGrowth judges a series of samples taken after the warm-up: it
// grew if the medians of its four quarters never fall and the last
// exceeds the first by more than bound. Medians, so that a sample taken
// while sessions open or close does not decide.
func soakGrowth(series []float64, bound float64) (bool, string) {
	if len(series) < 8 {
		return false, fmt.Sprintf("%d samples, too few to judge", len(series))
	}
	var medians []float64
	for q := range 4 {
		part := slices.Clone(series[q*len(series)/4 : (q+1)*len(series)/4])
		slices.Sort(part)
		medians = append(medians, part[len(part)/2])
	}
	rising := true
	for i := 1; i < len(medians); i++ {
		rising = rising && medians[i] >= medians[i-1]
	}
	detail := fmt.Sprintf("medians of the quarters %.1f, range %.1f to %.1f", medians, slices.Min(series), slices.Max(series))
	return rising && medians[3]-medians[0] > bound, detail
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
		x, y := uint16(rng.IntN(1024)), uint16(rng.IntN(768))
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
	for _, tt := range []struct {
		name   string
		series []float64
		bound  float64
		grew   bool
	}{
		{"too few samples", ramp(7, 100, 100), 10, false},
		{"flat", ramp(40, 100, 0), 10, false},
		{"rising past the bound", ramp(40, 100, 1), 10, true},
		{"rising within the bound", ramp(40, 100, 0.2), 10, false},
		{"falling", ramp(40, 200, -1), 10, false},
		{"a spike in the last quarter", append(ramp(39, 100, 0), 500), 10, false},
		{"up and down", append(append(ramp(10, 100, 0), ramp(10, 150, 0)...), append(ramp(10, 100, 0), ramp(10, 150, 0)...)...), 10, false},
		{"steps up", append(append(ramp(10, 100, 0), ramp(10, 105, 0)...), append(ramp(10, 110, 0), ramp(10, 120, 0)...)...), 10, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if grew, detail := soakGrowth(tt.series, tt.bound); grew != tt.grew {
				t.Errorf("soakGrowth = %v (%s), want %v", grew, detail, tt.grew)
			}
		})
	}
}
