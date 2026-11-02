// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file reads what the host, the stack's containers and occamyd's
// runtime spend: specs/008-native-architecture/comparison-benchmark.md,
// Measurements.

// readLoadAverage returns the host's load average over the last minute.
func readLoadAverage() (float64, error) {
	var text string
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			return 0, err
		}
		text = strings.Trim(strings.TrimSpace(string(out)), "{}") // { 1.23 4.56 7.89 }
	case "linux":
		out, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return 0, err
		}
		text = string(out)
	default:
		return 0, fmt.Errorf("no load average on %s", runtime.GOOS)
	}
	return parseLoadAverage(text)
}

// parseLoadAverage returns the first of the load averages in text.
func parseLoadAverage(text string) (float64, error) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return 0, fmt.Errorf("no load average in %q", text)
	}
	return strconv.ParseFloat(f[0], 64)
}

// containerStats is what the container engine reports of the stack's
// containers, by compose service.
type containerStats struct {
	cpu  map[string]time.Duration // cumulative
	mem  map[string]float64       // MiB in use
	pids map[string]int
}

// parseSize returns the bytes of a size as podman prints it: decimal
// units (kB, MB, GB), or binary ones (KiB, MiB, GiB).
func parseSize(s string) (float64, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		i = len(s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	units := map[string]float64{
		"": 1, "B": 1,
		"kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	}
	u, ok := units[strings.TrimSpace(s[i:])]
	if !ok {
		return 0, fmt.Errorf("size %q: unknown unit", s)
	}
	return n * u, nil
}

// parseStats reads podman's statistics in JSON, for the services of
// project.
func parseStats(out []byte, project string) (containerStats, error) {
	var stats []struct {
		Name     string `json:"name"`
		CPUTime  string `json:"cpu_time"`
		MemUsage string `json:"mem_usage"` // in use / limit
		PIDs     string `json:"pids"`
	}
	if err := json.Unmarshal(out, &stats); err != nil {
		return containerStats{}, err
	}
	cs := containerStats{cpu: map[string]time.Duration{}, mem: map[string]float64{}, pids: map[string]int{}}
	for _, st := range stats {
		svc := strings.TrimSuffix(strings.TrimPrefix(st.Name, project+"-"), "-1")
		d, err := time.ParseDuration(st.CPUTime)
		if err != nil {
			return containerStats{}, err
		}
		cs.cpu[svc] = d
		if used, _, ok := strings.Cut(st.MemUsage, "/"); ok {
			b, err := parseSize(used)
			if err != nil {
				return containerStats{}, err
			}
			cs.mem[svc] = b / (1 << 20)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(st.PIDs)); err == nil {
			cs.pids[svc] = n
		}
	}
	return cs, nil
}

// runtimeVars is what occamyd's debug listener publishes at /debug/vars
// that the harness records.
type runtimeVars struct {
	Goroutines int `json:"goroutines"`
	FDs        int `json:"fds"`
	Memstats   struct {
		HeapInuse    uint64 `json:"HeapInuse"`
		TotalAlloc   uint64 `json:"TotalAlloc"`
		NumGC        uint32 `json:"NumGC"`
		PauseTotalNs uint64 `json:"PauseTotalNs"`
	} `json:"memstats"`
}

// runtimeStat is a service's runtime over a result: its state at the end,
// and what it spent during it.
type runtimeStat struct {
	Goroutines int     `json:"goroutines"`
	FDs        int     `json:"fds"`
	HeapMiB    float64 `json:"heap_inuse_mib"`
	GCs        uint32  `json:"gc_cycles"`
	GCPauseMS  float64 `json:"gc_pause_ms"`
	AllocMiB   float64 `json:"alloc_mib"`
}

func runtimeDelta(before, after runtimeVars) runtimeStat {
	return runtimeStat{
		Goroutines: after.Goroutines,
		FDs:        after.FDs,
		HeapMiB:    float64(after.Memstats.HeapInuse) / (1 << 20),
		GCs:        after.Memstats.NumGC - before.Memstats.NumGC,
		GCPauseMS:  float64(after.Memstats.PauseTotalNs-before.Memstats.PauseTotalNs) / 1e6,
		AllocMiB:   float64(after.Memstats.TotalAlloc-before.Memstats.TotalAlloc) / (1 << 20),
	}
}

// readRuntime reads the runtime of the occamyd whose debug listener is at
// addr.
func readRuntime(ctx context.Context, addr string) (runtimeVars, error) {
	var v runtimeVars
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/debug/vars", nil)
	if err != nil {
		return v, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, fmt.Errorf("%s: %s", req.URL, resp.Status)
	}
	return v, json.NewDecoder(resp.Body).Decode(&v)
}

// processCPU returns the harness's own user and system CPU time.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// memoryStat is a container's memory over a result, in MiB.
type memoryStat struct {
	End  float64 `json:"end"`
	Peak float64 `json:"peak"`
}

// memPoll is how often an observer samples the containers' memory.
var memPoll = 2 * time.Second

// observer records, while a result is measured, what the stack and the
// harness spend: the containers' memory at its peak and at the end,
// occamyd's runtime, and the harness's CPU time.
type observer struct {
	cpu     *cpuSampler
	debug   map[string]string // occamyd service: its debug listener
	runtime map[string]runtimeVars
	client  time.Duration

	mu   sync.Mutex
	peak map[string]float64
	stop chan struct{}
	done chan struct{}
}

// observe starts observing.
func (r *runner) observe(ctx context.Context) *observer {
	o := &observer{cpu: r.cpu, debug: r.debug, runtime: map[string]runtimeVars{}, peak: map[string]float64{}}
	for svc, addr := range o.debug {
		if v, err := readRuntime(ctx, addr); err == nil {
			o.runtime[svc] = v
		}
	}
	o.client = processCPU()
	if o.cpu != nil {
		o.stop, o.done = make(chan struct{}), make(chan struct{})
		go o.sampleMemory()
	}
	return o
}

func (o *observer) sampleMemory() {
	defer close(o.done)
	t := time.NewTicker(memPoll)
	defer t.Stop()
	for {
		o.notePeak(o.cpu.stats())
		select {
		case <-o.stop:
			return
		case <-t.C:
		}
	}
}

func (o *observer) notePeak(cs *containerStats) {
	if cs == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for svc, mib := range cs.mem {
		o.peak[svc] = max(o.peak[svc], mib)
	}
}

// finish records what was spent since observe into res.
func (o *observer) finish(ctx context.Context, res *result) {
	res.ClientCPUMS = ms(processCPU() - o.client)
	for svc, before := range o.runtime {
		after, err := readRuntime(ctx, o.debug[svc])
		if err != nil {
			continue
		}
		if res.Runtime == nil {
			res.Runtime = map[string]runtimeStat{}
		}
		res.Runtime[svc] = runtimeDelta(before, after)
	}
	if o.cpu == nil {
		return
	}
	close(o.stop)
	<-o.done
	end := o.cpu.stats()
	if end == nil {
		return
	}
	o.notePeak(end)
	res.Memory = map[string]memoryStat{}
	for svc, mib := range end.mem {
		res.Memory[svc] = memoryStat{End: mib, Peak: o.peak[svc]}
	}
	res.PIDs = end.pids
}

// parseDebug reads -runtime: comma-separated service=address pairs.
func parseDebug(spec string) (map[string]string, error) {
	m := map[string]string{}
	if spec == "" {
		return m, nil
	}
	for _, pair := range strings.Split(spec, ",") {
		svc, addr, ok := strings.Cut(pair, "=")
		if !ok || svc == "" || addr == "" {
			return nil, errors.New("-runtime wants service=address pairs, such as occamy-v1=localhost:5638")
		}
		m[svc] = addr
	}
	return m, nil
}
