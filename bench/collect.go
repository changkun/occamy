// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// This file collects a plan in blocks, in an order of each run's own,
// resuming a results file and waiting for a quiet host, as
// specs/008-native-architecture/comparison-benchmark.md's Collection
// describes.

// planFile is a plan as bench/plans/*.yaml write it.
type planFile struct {
	Runs int `yaml:"runs"`
	// Seed draws each run's order of blocks; 0 keeps the file's order.
	Seed uint64 `yaml:"seed"`
	// MaxLoad is the load gate: before each block, the host's 1-minute
	// load must be below it, for up to LoadWait. 0 measures at any load.
	MaxLoad  float64       `yaml:"max_load"`
	LoadWait time.Duration `yaml:"load_wait"`
	Studies  []studyFile   `yaml:"studies"`
}

// studyFile is a study of a plan file: its series under its network
// conditions, each measuring its scenarios.
type studyFile struct {
	Study     string   `yaml:"study"`
	Series    []string `yaml:"series"`
	Nets      []string `yaml:"nets"`
	Scenarios string   `yaml:"scenarios"`
}

// readPlanFile reads a plan file, refusing a field it does not know.
func readPlanFile(name string) (planFile, error) {
	var f planFile
	raw, err := os.ReadFile(name) // #nosec G304 -- the plan the operator names
	if err != nil {
		return f, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", name, err)
	}
	if f.Runs < 1 {
		return f, fmt.Errorf("%s: runs is %d, want at least 1", name, f.Runs)
	}
	if len(f.Studies) == 0 {
		return f, fmt.Errorf("%s: no studies", name)
	}
	return f, nil
}

// block is what a run measures in one go: one series under one network
// condition, in one study, on fresh sessions.
type block struct {
	study string
	// factor is the study's setting, such as viewers=4; the matrix has
	// none.
	factor    string
	series    series
	net       netCondition
	scenarios []scenario // those the series serves
}

// blockKey identifies a block of a run, as its results record it.
type blockKey struct {
	run                        int
	study, series, net, factor string
}

func (b block) key(run int) blockKey {
	return blockKey{run, b.study, b.series.Name, b.net.Name, b.factor}
}

func (r *result) blockKey() blockKey {
	return blockKey{r.Run, r.Study, r.Series, r.Net, r.Factor}
}

// String describes b with its scenarios, so that a results file names
// what its plan measures.
func (b block) String() string {
	s := strings.Join([]string{b.study, b.series.Name, b.net.Name}, " ")
	if b.factor != "" {
		s += " " + b.factor
	}
	var ids []string
	for _, sc := range b.scenarios {
		ids = append(ids, sc.ID+" "+sc.Protocol)
	}
	return s + ": " + strings.Join(ids, ", ")
}

// results is how many results b adds to a run: one per scenario, and G7
// once per protocol.
func (b block) results() int { return len(b.scenarios) + len(protocolsOf(b.scenarios)) }

// matrixBlocks returns a block for each of chosen under each of nets,
// with the scenarios of scs that it serves. A series that serves none of
// them is an error, so that a plan does not quietly measure nothing.
func matrixBlocks(study string, chosen []series, nets []netCondition, scs []scenario) ([]block, error) {
	var blocks []block
	for _, sr := range chosen {
		var served []scenario
		for _, sc := range scs {
			if sr.serves(sc.Protocol) {
				served = append(served, sc)
			}
		}
		if len(served) == 0 {
			return nil, fmt.Errorf("%s: no scenario chosen runs on %s", study, sr.Name)
		}
		for _, nc := range nets {
			blocks = append(blocks, block{study: study, series: sr, net: nc, scenarios: served})
		}
	}
	return blocks, nil
}

// studyBlocks returns the blocks of a plan file's studies, with all the
// series that the harness knows.
func studyBlocks(f planFile, all []series) ([]block, error) {
	var blocks []block
	seen := map[blockKey]bool{}
	for _, st := range f.Studies {
		if st.Study != "matrix" {
			return nil, fmt.Errorf("unknown study %q", st.Study)
		}
		chosen, err := chooseSeries(all, st.Series)
		if err != nil {
			return nil, err
		}
		nets, err := chooseNets(st.Nets)
		if err != nil {
			return nil, err
		}
		scs, err := chooseScenarios(st.Scenarios)
		if err != nil {
			return nil, err
		}
		bs, err := matrixBlocks(st.Study, chosen, nets, scs)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if seen[b.key(0)] {
				return nil, fmt.Errorf("%s comes twice", b)
			}
			seen[b.key(0)] = true
		}
		blocks = append(blocks, bs...)
	}
	return blocks, nil
}

// chooseSeries returns the series of all that names names, in that order.
func chooseSeries(all []series, names []string) ([]series, error) {
	var chosen []series
	for _, name := range names {
		i := slices.IndexFunc(all, func(s series) bool { return s.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown series %q", name)
		}
		chosen = append(chosen, all[i])
	}
	if len(chosen) == 0 {
		return nil, errors.New("no series")
	}
	return chosen, nil
}

// chooseNets returns the network conditions that names names.
func chooseNets(names []string) ([]netCondition, error) {
	var chosen []netCondition
	for _, name := range names {
		i := slices.IndexFunc(netConditions, func(n netCondition) bool { return n.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown network condition %q", name)
		}
		chosen = append(chosen, netConditions[i])
	}
	if len(chosen) == 0 {
		return nil, errors.New("no network condition")
	}
	return chosen, nil
}

// order returns the blocks in the order run measures them: drawn from
// the plan's seed and the run, so that drift on the host during a run,
// and the block before, favour no series, and a resumed run keeps the
// order it began with.
func (p plan) order(run int) []block {
	blocks := slices.Clone(p.blocks)
	if p.seed != 0 {
		rng := rand.New(rand.NewPCG(p.seed, uint64(run))) // #nosec G115,G404 -- a run's index, for an order of its own
		rng.Shuffle(len(blocks), func(i, j int) { blocks[i], blocks[j] = blocks[j], blocks[i] })
	}
	return blocks
}

// planRecord is what a results file measures, so that -resume continues
// the same plan and no other.
type planRecord struct {
	Seed    uint64   `json:"seed,omitempty"`
	MaxLoad float64  `json:"max_load,omitempty"`
	Blocks  []string `json:"blocks"`
}

func (p plan) record() *planRecord {
	rec := &planRecord{Seed: p.seed, MaxLoad: p.maxLoad}
	for _, b := range p.blocks {
		rec.Blocks = append(rec.Blocks, b.String())
	}
	return rec
}

// continues reports why prev is not of the same plan as p, or nil.
func (p plan) continues(prev *report) error {
	rec := p.record()
	switch {
	case prev.Plan == nil:
		return errors.New("it records no plan")
	case prev.Quick != p.quick:
		return fmt.Errorf("it is quick %v, not %v", prev.Quick, p.quick)
	case prev.AckHz != p.ackHz:
		return fmt.Errorf("it acknowledges at %g Hz, not %g", prev.AckHz, p.ackHz)
	case prev.Plan.Seed != rec.Seed:
		return fmt.Errorf("its seed is %d, not %d", prev.Plan.Seed, rec.Seed)
	case !slices.Equal(prev.Plan.Blocks, rec.Blocks):
		return errors.New("it measures other blocks")
	}
	return nil
}

// kept returns the results of prev that p keeps on resuming, and the
// blocks they complete: those of blocks of p with all their results and
// no error. Others are measured again.
func (p plan) kept(prev []result) ([]result, map[blockKey]bool) {
	byBlock := map[blockKey][]result{}
	for _, r := range prev {
		byBlock[r.blockKey()] = append(byBlock[r.blockKey()], r)
	}
	var results []result
	done := map[blockKey]bool{}
	for run := range p.runs {
		for _, b := range p.order(run) {
			rs := byBlock[b.key(run)]
			if len(rs) != b.results() || slices.ContainsFunc(rs, func(r result) bool { return r.Error != "" }) {
				continue
			}
			results = append(results, rs...)
			done[b.key(run)] = true
		}
	}
	return results, done
}

// readReport reads a results file.
func readReport(name string) (*report, error) {
	raw, err := os.ReadFile(name) // #nosec G304 -- the results file the operator names
	if err != nil {
		return nil, err
	}
	var rep report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &rep, nil
}

// resumable reads name for -resume: nil if it does not exist yet.
func resumable(name string) (*report, error) {
	rep, err := readReport(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return rep, err
}

// writeReport writes rep to name, through a file beside it, so that an
// interrupted write leaves the last complete results.
func writeReport(name string, rep *report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), name)
}

// loadAverage returns the host's load average over the last minute; a
// variable, so that tests can set the load.
var loadAverage = readLoadAverage

// loadPoll is how often the load gate looks at the load, and loadReport
// how often it says that it still waits.
var (
	loadPoll   = 30 * time.Second
	loadReport = 10 * time.Minute
)

// errBusy ends a collection whose host stayed busy for longer than the
// load gate waits.
var errBusy = errors.New("the host stayed busy")

// waitLoad waits until the host's load is below p.maxLoad, for up to
// p.loadWait. Without a load average, as on a system that it does not
// read, it measures without the gate and says so.
func (p plan) waitLoad(ctx context.Context) error {
	if p.maxLoad <= 0 {
		return nil
	}
	start := time.Now()
	var said time.Time
	for {
		l, err := loadAverage()
		if err != nil {
			log.Printf("load average: %v; measuring without the load gate", err)
			return nil
		}
		if l < p.maxLoad {
			if !said.IsZero() {
				log.Printf("load %.1f after %v", l, time.Since(start).Round(time.Second))
			}
			return nil
		}
		if time.Since(start) >= p.loadWait {
			return fmt.Errorf("%w: load %.1f, at or above %g for %v", errBusy, l, p.maxLoad, p.loadWait)
		}
		if said.IsZero() || time.Since(said) >= loadReport {
			log.Printf("load %.1f, waiting until it is below %g", l, p.maxLoad)
			said = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(loadPoll):
		}
	}
}
