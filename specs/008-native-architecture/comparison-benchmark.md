---
title: Comparison benchmark
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/benchmark.md
affects:
  - bench/
  - docker/bench/
  - docker/docker-compose.bench.yaml
  - Makefile
  - docs/benchmarks.md
  - docs/benchmarks/
effort: xlarge
created: 2026-11-02
updated: 2026-11-03
author: Changkun Ou
dispatched_task_id: null
---

# Comparison benchmark

## Goal

Compare the Guacamole protocol with Occamy's across what people do on a
remote desktop or terminal, how fast their link is, how many of them share
a session, and how much load the server carries, with results that hold up
statistically. [benchmark](benchmark.md) defined the yardstick, its
scenarios and the 10x and 50 ms targets; this spec widens it and fixes how
the comparison is collected and analysed before any of it is measured.

The owner set the scope on 2026-11-02:

- **Paths:** all six series of [benchmark](benchmark.md): `guacd` and
  `occamy` (Guacamole's protocol, straight from guacd and through occamyd
  v0), `v1-native` (Occamy as shipped), and `v1-guacd`, `v1-native-nogfx`
  and `v1-guacd-gfx`, which separate what the protocol gains from what the
  backends gain and give like-for-like RDP rows. (2026-11-03: beta.2
  removes the bridge and the v0 routes from occamyd, so `occamy`,
  `v1-guacd` and `v1-guacd-gfx` go with them; `guacd` stays as the
  baseline, beside the `v1-native` series. See
  [008](../008-native-architecture.md), v1.0.0-beta.2 scope. Built:
  `bench/plans/compare.yaml`'s matrix runs `guacd`, `v1-native` and
  `v1-native-nogfx` under N0 to N2, and `guacd` and `v1-native` under
  N3 and N4, 13 pairs of series and network instead of 24; the
  essential plan, `guacd` against `v1-native`, is unchanged.)
- **Factors:** workloads (typing, clicking, drawing, scrolling, video and
  more), a ladder of networks from local to very slow, the number of
  viewers and operators on a session, and the number of sessions on a
  server.
- **Measurements:** client latency, the server's send latency, the
  server's load and how it scales, and runtime metrics.
- **Rigour:** 7 runs, randomised order, a load gate, tables, tests,
  confidence intervals and bar plots with them.
- **One shot:** one command collects everything, resuming where it
  stopped; another analyses it.

## Workloads

Existing scenarios keep their IDs and definitions ([benchmark](benchmark.md)).
New ones follow the same rules: generated content from fixed seeds, the
scene's frame stamps where it animates, and probes jittered within a 60 Hz
period around a 10 per second schedule.

| ID | Protocols | Workload | Primary metric | Secondary |
|----|-----------|----------|----------------|-----------|
| T1 | SSH | Type 200 characters | `L` p95 | `L` p50 |
| T2 | SSH | `seq 1 100000`, then an end marker | completion | bytes, server CPU |
| T3 | SSH | `top -d 0.5` for 30 s | bytes/s | server CPU |
| T4 | SSH | **new:** page through a 200000-line file in `less`, 200 pages at 10 a second | `L` p95 (key to first drawing) | `L` p50, bytes/s |
| T5 | SSH | **new:** a coloured log of varied lines at about 100 lines a second for 30 s | bytes/s | server CPU |
| G1 | VNC, RDP | Idle desktop for 30 s | bytes/s | server CPU |
| G2 | VNC, RDP | Probe keys on a still desktop | `L` p95 | `L` p50 |
| G3 | VNC, RDP | Text scrolled a line every 16 ms for 10 s | bytes per frame | CPU per frame, frames/s |
| G4 | VNC, RDP | A 400×300 window dragged at 60 Hz | bytes per frame | CPU per frame, frames/s |
| G5 | VNC, RDP | Full-screen procedural motion at 30 fps, with probe keys | `L` p95 under load | frames/s, bytes per frame |
| G6 | VNC, RDP | One photographic full-screen image | bytes | completion |
| G7 | SSH, VNC, RDP | Connect, 20 times | first frame p95 | first frame p50 |
| G8 | VNC, RDP | **new:** 200 clicks at seeded spots; the scene fills a 24×24 swatch around each in a colour of its position | `L` p95 (click to pixel) | `L` p50 |
| G9 | VNC, RDP | **new:** drawing: 600 pointer moves at 60 Hz with the button held, along rows that never cross; the scene paints the stroke | `L` p95 (move to pixel) | bytes, `L` p50 |
| G10 | VNC, RDP | **new:** video in a 640×360 window at 30 fps, with probe keys | frames/s | `L` p95 under load, bytes per frame, CPU per frame |
| G11 | VNC, RDP | **new:** the same video full screen | frames/s | `L` p95 under load, bytes per frame, CPU per frame |
| G12 | SSH, VNC, RDP | **new:** resize to 800×600 (a terminal to 100×30) and back, 10 times | time to the first frame at the new size, p95 | p50 |

Details of the new workloads:

- **Video (G10, G11).** Photographic content, generated as G6's photo is,
  zoomed and panned with bilinear sampling by a different factor every
  frame, with a cross-fade to the next image every 3 s and a light
  per-frame grain from the frame's seed. Nothing in it is an exact copy
  of the frame before, so scroll and copy detection cannot make it cheaper
  than real video would be. It draws the stamp, and keeps the swatch in
  the margin still.
- **Clicks (G8).** The scene takes button presses and fills the swatch
  around the press, in `probe.ClickColour(x, y)`. The harness clicks a
  seeded sequence of spots that never repeats within a run, outside the
  margin and the stamp, and waits for the pixel at the spot.
- **Drawing (G9).** With the button held, the scene draws a 3-pixel line
  from the last point to each new one, in one ink on a white canvas that
  the scene's start clears. The harness moves 8 pixels a step along rows
  12 pixels apart, so no point is painted twice, and keeps every pending
  point: a point's latency ends at the first frame where its pixel is ink.
- **Pager and log (T4, T5).** The file and the log come from `seq` and
  `printf` in the demo shell, so the SSH image needs nothing new.
- **Resize (G12).** v1 sends `Resize`, v0 the `size` instruction. A
  remote that keeps its size, as a VNC server on a fixed framebuffer may,
  records the scenario as unsupported rather than as a failure.

## Networks

| ID | Name | RTT | Bandwidth |
|----|------|-----|-----------|
| N0 | local | none added | unlimited |
| N1 | fast | 20 ms | 100 Mbit/s |
| N2 | medium | 50 ms | 20 Mbit/s |
| N3 | slow | 100 ms | 5 Mbit/s |
| N4 | very slow | 250 ms | 1 Mbit/s |

N3 and N4 are new. The link emulates delay and throughput as
[benchmark](benchmark.md) describes, not loss: the harness's proxy carries
TCP, and loss on TCP shows only as delay. A loss study
([realtime-transport](realtime-transport.md), step 2) emulates the whole
link in the kernel instead, where a packet can be dropped.

## Studies

Everything is measured in **blocks**: one series, one network condition,
one study and one setting of its factor, measured on fresh sessions. Each
of the 7 runs measures every block once, in an order drawn for that run
from a seed the results record, so that drift on the host during a run,
and the block before, favour no series.

### Matrix

Every workload, single user.

- `guacd`, `occamy`, `v1-native`, `v1-native-nogfx`, `v1-guacd`,
  `v1-guacd-gfx` under N0, N1 and N2.
- `guacd`, `occamy` and `v1-native` under N3 and N4: the slow links on the
  three main paths, which keeps the run within the time the owner allowed.

A series runs the workloads of the protocols it serves.

### Sharing

Several people on one session, over VNC (G2 and G10) and SSH (T1), on
`guacd` (joining by guacd's connection ID), `occamy` (v0 joins a session
with the same claims) and `v1-native` (shares).

- **Viewers:** 1, 2, 4, 8 and 16 read-only viewers besides the owner, all
  on N1. The owner types the probe keys, or plays the video with them;
  every viewer measures, from the owner's key, its own `L`, and its own
  frames per second.
- **Operators:** 2 and 4 people with input, on N1, taking turns at 10 keys
  a second between them: each key goes once the one before has reached
  its typist. Each measures `L` for its own keys, and every other one the
  key's arrival at theirs.
- **A slow viewer:** 4 viewers on N1 and one more on N4, during G10. The
  fast viewers' `L` and frames per second, set against the 4-viewer block
  without the slow one, measure how much one slow link costs the others.
  Occamy paces each viewer to its own link; guacd sends every user of a
  connection the same stream.

### Scale

Many independent sessions on one server, on `guacd`, `occamy` and
`v1-native`, under N0, 30 s per level.

- **Terminals:** 1, 2, 4, 8, 16, 32 and 64 SSH sessions, each typing 5
  keys a second (with `L` measured) in one variant, and each running
  `top -d 0.5` in another.
- **Desktops:** 1, 2, 4, 8 and 16 sessions to the same VNC display
  playing G10's video. Each is its own backend connection, so the server's
  cost per session shows; x11vnc's own cost grows too and is reported apart.

Per level: the server's CPU and memory, `L` p95 for the typing variant,
delivered frames per second, and the runtime metrics below.

### Breakdown

Where a probe's time goes. For this study only, the server reaches the
remote through a relay in the harness, on the same clock as everything else:

- `t0`: the harness writes the input;
- `t1`: the input's first byte reaches the remote, through the relay;
- `t2`: the remote's first byte after `t1` leaves for the server;
- `t3`: the probe's frame has arrived at the client, before decoding;
- `t4`: the frame is decoded and shows the probe.

`t1 − t0` is the input's way to the remote, `t2 − t1` the remote's
reaction, `t3 − t2` the server's send latency including its link, and
`t4 − t3` the client's decoding. T1, G2 and G8 on every series that
serves them, under N0 and N1, on still screens, where the first bytes
after an input are its answer. The relay adds a hop through the
container engine's host gateway to each series alike; this study's
absolute `L` is therefore not compared with the matrix's.

The remote servers publish their ports on the loopback interface for the
relay, on ports a second stack can move, and the v1 files gain relay
connections.

## Measurements

Every result keeps what [benchmark](benchmark.md) records, and adds:

| Field | What |
|-------|------|
| `study`, `factor` | The block's study and setting, such as `viewers=4` |
| `at`, `load_1m` | When the result started, and the host's 1-minute load average then |
| `mem_mib` | Each container's memory at the end, and its peak over samples every 2 s |
| `runtime` | occamyd's runtime from `/debug/vars` on its debug listener, before and after: goroutines, heap in use, GC cycles and pause time, bytes allocated, open files |
| `client_cpu_ms` | The harness's own user and system CPU time; it includes the network emulation, which every series pays alike |
| `frame_interval_ms` | In a scene, the percentiles of the time between the first showings of consecutive stamps in the measured part, added by [realtime-transport](realtime-transport.md), step 1 |
| `viewers` | Per viewer in the sharing study: role, network, `L` percentiles, frames per second |
| `breakdown` | Per probe in the breakdown study: `t1 − t0`, `t2 − t1`, `t3 − t2`, `t4 − t3` |

guacd has no runtime endpoint; its memory and CPU come from the container
engine, as occamyd's also do.

## Collection

```sh
make bench-compare            # collects into bench-compare.json, resuming it
make bench-report             # analyses it into docs/benchmarks/comparison/
```

- **Plan.** The design is a file, `bench/plans/compare.yaml`: runs, seed,
  load gate, and per study its series, networks, workloads and factor
  levels. `go run ./bench -plan bench/plans/compare.yaml` runs it; the
  flags of a single run stay as they are.
- **Load gate.** Before each result the harness waits until the host's
  1-minute load is below 8, for up to 2 hours. If it stays above, the
  harness saves what it has and exits; running the command again resumes.
  Each result records the load, so the analysis can flag results that
  started above the gate. (The gate was first checked before each block;
  the essential plan's outcome below moved it.)
- **Resume.** The harness saves after every block. On start, a results
  file of the same plan is continued: blocks complete without errors are
  kept, others measured again, a block the gate stopped part-way among
  them. A file of another plan is refused.
- **Stack.** `make bench-compare` starts the bench stack as `make bench`
  does. `COMPOSE_PROJECT_NAME`, `OCCAMY_IMAGE` and the port variables put a
  second stack beside another on a shared host.

## Analysis

The plan is fixed here, before any data, so that no choice follows the
numbers.

- **Unit.** A run is one replicate. Within a run, a cell's statistic is
  computed from its samples (a p95 from 200 keys, a total, a rate), so the
  samples of one run are not treated as independent.
- **Values.** For each series and cell: the median of the runs, and an
  exact distribution-free 95% interval for it from order statistics (at
  7 runs, the lowest and highest run, covering 98.4%).
- **Comparisons.** For a pair of series and a cell, the factor by which
  the second is better: the Hodges–Lehmann estimate of the ratio, the
  median of all pairwise ratios between their runs, with its exact 95%
  interval from the Mann–Whitney distribution. Its p-value is the exact
  two-sided permutation test of the rank sum, over every split of the
  runs, which handles ties.
- **Families.**
  1. **Gate**, confirmatory: `occamy` to `v1-native` on
     [benchmark](benchmark.md)'s scenarios, T1 to G7, at N0 to N2, on each
     primary metric: 54 tests, Holm-corrected. At 7 runs a complete
     separation gives p = 0.00058, below Holm's first threshold of
     0.05/54.
  2. **Guacamole to Occamy**, exploratory: `guacd` to `v1-native`, and
     `occamy` to `v1-native` beyond the gate (new workloads, N3, N4), each
     with Benjamini–Hochberg at 5%.
  3. **Where it comes from**, exploratory: the protocol alone (`guacd` to
     `v1-guacd` on VNC, to `v1-guacd-gfx` on RDP) and the backends alone
     (`v1-guacd` to `v1-native` on VNC, to `v1-native-nogfx` on RDP with
     GFX off, `v1-guacd-gfx` to `v1-native` with it on),
     Benjamini–Hochberg.

  Secondary metrics get intervals and unadjusted p-values and are labelled
  as such.
- **Targets.** A 10x target is met where the interval's lower bound is at
  least 10, not where the estimate is; the 50 ms target where the upper
  bound of `v1-native`'s `L` p95 at N1 is at most 50 ms (T1, G2, G5 as in
  [benchmark](benchmark.md), and G8, G9 and G10). Misses are justified, or
  not, by [benchmark](benchmark.md)'s rule.
- **Sharing and scale.** Per series, the slope of each metric over viewers
  or sessions, by the Theil–Sen estimator over the per-run values, with a
  95% interval from resampling the runs; between series, the ratio of the
  slopes with its interval. Capacity: the most sessions at which the upper
  bound of `L` p95 stays at most 50 ms.
- **Sensitivity.** The same analysis without the results that started
  with the host's load above the gate; a conclusion that changes is
  reported as fragile.

### Figures

SVG, generated with the tables, in light and dark:

- per workload group and network, bars of each series' median with the
  95% interval, on a log axis where values span more than tenfold;
- the factors of each family, with intervals, on a log axis with lines at
  1x and 10x;
- the breakdown, stacked by part, per series;
- sharing and scale, the metric over viewers or sessions, per series, with
  interval bands.

## Implementation

In this order, one pull request each, each leaving `make bench` working:

1. **Infrastructure:** this spec; plans, blocks, the shuffled order,
   resume, the load gate; `study`, `factor`, `at`, `load_1m`, `mem_mib`,
   `runtime`, `client_cpu_ms`; N3 and N4; `make bench-compare`.
2. **Workloads:** the scene's clicks, drawing and video; pointer input on
   v0 and v1; T4, T5, G8 to G12.
3. **Sharing and scale.**
4. **Breakdown:** the relay, the published ports and the relay connections.
5. **Analysis:** `bench/report.py`, a Python script that uv runs with
   numpy, scipy and matplotlib, and `make bench-report`: tables, tests and
   figures, tested on synthetic results with known answers
   (`bench/report_test.py`).
6. **The run and its results:** `docs/benchmarks.md`,
   [benchmark](benchmark.md)'s verdicts, the CHANGELOG, and a sentence in
   the README once there are numbers.

The collection takes about 32 hours of a quiet host: about 21 for the
matrix, 5 for sharing, 5 for scale and 1 for the breakdown. (Estimated
with six series; since beta.2 the matrix has 13 of its 24 pairs of
series and network.)

## Essential plan

The full collection takes about 32 hours, so the owner chose on
2026-11-02 to measure an essential subset first, about an hour, and the
full plan after it: `bench/plans/essential.yaml`, `make bench-essential`.

- `guacd` against `v1-native`: Guacamole's protocol straight from guacd
  1.6, against Occamy as shipped.
- N0, where each side's own processing shows, and N2, a constrained link,
  where bandwidth and queueing behind frames show.
- T1 and T2; G2, G5 and G6 over VNC and RDP: 8 cells, 16 primary
  comparisons with the two networks.
- 6 runs, the fewest at which an exact two-sided rank test passes Holm's
  correction over 16 comparisons (2/924 < 0.05/16), with 100 probe keys a
  scenario per run, 600 a cell in all. G7 is left out.

Its single family, `guacd` to `v1-native` on each cell's primary metric,
is confirmatory and Holm-corrected; secondary metrics get intervals and
unadjusted p-values. N1 is not in the plan, so the 50 ms target is
judged at N0, where it is `v1-native`'s own processing; the full plan
judges it at N1. The analysis is a Python script over the results
(`bench/report.py`), with numpy, scipy and matplotlib;
`make bench-essential-report` writes it into `docs/benchmarks/essential/`.

### Outcome of the essential plan

Measured on 2026-11-02, in 2 h 18 min, with all 192 results; the
numbers, the targets and the verdict are in
[docs/benchmarks.md](../../docs/benchmarks.md#the-essential-comparison),
and the report in `docs/benchmarks/essential/`.

- 14 of the 16 primary differences are significant after Holm's
  correction; T1 is not, at N0 or N2. Against
  [benchmark](benchmark.md)'s targets, 11 of 36 are met: all of T2's,
  G6's bytes, and G5's frame rate over VNC at N0. No miss is justified
  yet, so the v1.0 gate is not met. G5's latency, bytes and CPU per
  frame are Occamy's own and come first.
- The run measured G2's and G5's baselines again at N0 and N2, with the
  jitter and the stamp, and their targets now come from it.
- G5 now reports CPU per frame as a secondary metric, since its target
  is one.
- 52 results started above the load gate, none at the start of a block:
  the gate is checked between blocks, and the load rose within them,
  most often after G5. Without them 2–4 runs remain on a side, too few
  to reach 5% after correction, so 11 conclusions are fragile by this
  spec's definition, although their factors move by at most 6%. The
  full plan should check the gate before each result rather than each
  block. Done with [realtime-transport](realtime-transport.md), step 1:
  the harness checks the gate before each result, with the same bound,
  wait and resume.

Measured again on 2026-11-03 on `a4663c16`, the measured path of
v1.0.0-beta.2, in 53 min with all 192 results, as [008](../008-native-architecture.md)'s beta.2
scope asks:

- 14 of the 16 primary differences are significant after correction,
  T1 again the two that are not, and 12 of 36 targets are met: G2's
  latency over VNC at N0 and G5's frame rate over VNC at N2 are new, and
  T2's bytes at N0 now miss, 8.95 KiB [5.64, 9.67] against 8.6, in 31
  frames a run where the first run sent 20.
- The harness held a result at the load gate 10 times, for 16 min, and
  no result started above it, so no conclusion depends on the load.
- The targets of G2 and G5 stay where the first run set them; this run's
  `guacd` medians do not move them.

## Not measured

- Packet loss, which a userspace proxy over TCP cannot emulate. The loss
  study of [realtime-transport](realtime-transport.md), step 2, measures
  it on a link in the kernel.
- A browser's own costs. The harness decodes in Go; a browser decodes
  natively and composites on the GPU. Client CPU compares the series with
  each other, not with a browser.
- Windows RDP servers, until a Windows host is available.
