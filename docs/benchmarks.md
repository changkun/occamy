# Benchmarks

Occamy is measured against Guacamole on the same remote desktops and
terminals, over the same emulated networks, on one host. A harness plays
the user: it types, presses keys while the screen moves, and opens a
photograph, and it records what each server sends and when the effect
shows. Each comparison repeats every measurement, in random order, and
`bench/report.py` analyses the runs with 95% intervals and corrected
tests.

The numbers on this page were measured on 2026-11-03 on an Apple M5 Pro
with 18 CPUs and podman 6.0.2, against the `guacamole/guacd:1.6.0`
image. [How it is measured](#what-is-measured) follows the results.

## Occamy against Guacamole

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="benchmarks/essential/factors-guacamole-to-occamy-dark.svg">
  <img alt="Occamy's improvement over Guacamole on each scenario's primary metric, with 95% intervals, with no added delay and at 50 ms and 20 Mbit/s" src="benchmarks/essential/factors-guacamole-to-occamy.svg">
</picture>

Each dot shows Occamy's *improvement* over Guacamole on one scenario's
primary metric: Guacamole's latency, time, or data divided by Occamy's.
An improvement of 3× means that Occamy needs a third of what Guacamole
needs. The line through each dot is the 95% interval, and anything to
the right of 1× is better than Guacamole. Fourteen of the 16
differences are significant after Holm's correction. The two that are
not are typing over SSH, where both are equally fast.

**A key's latency, 95th percentile, in ms**

| Scenario | Network | Guacamole | Occamy | Improvement | p (Holm) |
|---|---|---:|---:|---:|---:|
| Typing, SSH | No delay | 2.48 [2.20, 5.39] | 1.73 [1.41, 2.63] | 1.49× [1.18, 2.21] | 0.0519 |
| Typing, SSH | 50 ms, 20 Mbit/s | 55.4 [54.3, 55.8] | 54.5 [53.4, 55.1] | 1.02× [0.999, 1.03] | 0.0649 |
| Key press, VNC | No delay | 16.4 [15.7, 17.0] | 4.64 [4.28, 5.31] | 3.49× [3.21, 3.73] | 0.0346 |
| Key press, VNC | 50 ms, 20 Mbit/s | 69.4 [67.5, 73.3] | 57.5 [56.4, 59.0] | 1.21× [1.18, 1.24] | 0.0346 |
| Key press, RDP | No delay | 20.6 [19.3, 22.4] | 8.10 [6.92, 12.3] | 2.55× [1.82, 2.98] | 0.0346 |
| Key press, RDP | 50 ms, 20 Mbit/s | 71.4 [70.2, 72.6] | 60.0 [59.3, 60.7] | 1.19× [1.18, 1.21] | 0.0346 |
| Key during motion, VNC | No delay | 155 [132, 165] | 49.0 [39.6, 54.3] | 3.13× [2.71, 3.87] | 0.0346 |
| Key during motion, VNC | 50 ms, 20 Mbit/s | 214 [182, 229] | 93.2 [91.1, 113] | 2.19× [1.97, 2.47] | 0.0346 |
| Key during motion, RDP | No delay | 150 [135, 191] | 66.8 [57.9, 92.3] | 2.27× [1.83, 2.76] | 0.0346 |
| Key during motion, RDP | 50 ms, 20 Mbit/s | 226 [201, 240] | 117 [111, 126] | 1.91× [1.70, 2.05] | 0.0346 |

**Long output over SSH, time until it has all shown, in ms**

| Network | Guacamole | Occamy | Improvement | p (Holm) |
|---|---:|---:|---:|---:|
| No delay | 930 [864, 978] | 19.7 [16.6, 48.6] | 46.8× [20.1, 55.7] | 0.0346 |
| 50 ms, 20 Mbit/s | 951 [917, 1,316] | 107 [105, 109] | 8.89× [8.54, 12.1] | 0.0346 |

**A full-screen photo, data to the client, in KiB**

| Protocol | Network | Guacamole | Occamy | Improvement | p (Holm) |
|---|---|---:|---:|---:|---:|
| VNC | No delay | 2,235 [1,497, 2,235] | 122 [122, 122] | 18.3× [12.3, 18.3] | 0.0346 |
| VNC | 50 ms, 20 Mbit/s | 2,235 [1,441, 2,235] | 122 [122, 122] | 18.3× [11.8, 18.3] | 0.0346 |
| RDP | No delay | 2,175 [1,379, 2,175] | 108 [108, 115] | 20.1× [12.7, 20.1] | 0.0346 |
| RDP | 50 ms, 20 Mbit/s | 2,175 [1,169, 2,175] | 108 [108, 111] | 20.1× [10.8, 20.1] | 0.0346 |

Each value is the median of 6 runs with its 95% interval. With 6 runs a
side, the smallest p-value the exact test can give is 0.0022, and Holm's
correction over 16 tests raises it to 0.0346.

What the tables show:

- **A key shows sooner.** On a still desktop, a key's effect shows in
  4.6 ms over VNC and 8.1 ms over RDP at the 95th percentile, against
  16.4 and 20.6 with Guacamole. During full-screen motion it shows in 49
  and 67 ms, against 155 and 150. With a 50 ms round trip, the network
  accounts for most of the time, yet Occamy still shows a key in 58 and
  60 ms on a still desktop, compared with 69 and 71, and in 93 and
  117 ms during motion, compared with 214 and 226.
- **Long output finishes at once.** `seq 1 100000` over SSH finishes in
  20 ms instead of 930. Occamy sends the terminal's last screen, not
  every line that scrolled past.
- **A photograph costs a twentieth of the data.** 108 to 122 KiB
  instead of 2.1 to 2.2 MiB: Occamy sends photographs as WebP or JPEG,
  where Guacamole sent most of them as PNG.
- **Motion costs less.** During full-screen motion Occamy sends 7.7 to
  8.0 KiB a frame, against Guacamole's 46 to 66, and its server spends
  12 to 16 ms of CPU on a frame, against 47 to 59. Over VNC the viewer
  sees 29 of the scene's 30 frames a second, where Guacamole shows 25.
  Over RDP both show about 24, the RDP server's own rate.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="benchmarks/essential/latency_p95-dark.svg">
  <img alt="The latency's 95th percentile for Guacamole and Occamy in each scenario, with no delay and at 50 ms and 20 Mbit/s, medians with 95% intervals, on a log axis" src="benchmarks/essential/latency_p95.svg">
</picture>

[The full report](benchmarks/essential/report.md) has every metric with
its interval and test, occamyd's memory and CPU, and a figure for each.
[The results](benchmarks/essential/bench-essential.json) hold every run.

## On a lossy link

When packets are lost, TCP waits for each lost packet before it delivers
what came after it, and slows down as if the link were congested.
Occamy's server asks Linux for BBR, a congestion control that sets its
rate from the path rather than from losses
([configuration](configuration.md)). A key during full-screen motion,
latency at the 95th percentile in ms, medians with 95% intervals:

| Network and loss | Guacamole, VNC | Occamy, VNC | Guacamole, RDP | Occamy, RDP |
|---|---:|---:|---:|---:|
| 20 ms, 100 Mbit/s, no loss | 132 [123, 172] | 61.3 [58.0, 74.8] | 121 [117, 130] | 81.7 [79.8, 86.8] |
| 20 ms, 100 Mbit/s, 0.5% lost | 164 [152, 184] | 68.6 [65.8, 76.8] | 165 [153, 1,734] | 89.3 [81.7, 104] |
| 20 ms, 100 Mbit/s, 2% lost | 255 [209, 2,857] | 79.8 [72.4, 87.2] | 2,188 [264, 4,186] | 107 [91.3, 131] |
| 50 ms, 20 Mbit/s, no loss | 184 [169, 219] | 95.2 [92.7, 106] | 179 [172, 226] | 114 [111, 114] |
| 50 ms, 20 Mbit/s, 0.5% lost | 339 [276, 3,942] | 122 [94.8, 132] | 5,575 [362, 7,941] | 135 [127, 158] |
| 50 ms, 20 Mbit/s, 2% lost | 7,053, in 4 of 6 runs | 149 [136, 167] | not within 10 s in any run | 163 [150, 173] |

With 2% of packets lost at 50 ms, Occamy keeps showing 28 of the
scene's 30 frames a second over VNC, and a key during motion costs it
about one more round trip. Guacamole needs 1.2 to 1.6 MiB a second for
the scene, more than TCP carries at that loss, so its frames queue
behind the link and a key waits behind them for seconds.

Guacamole's column comes from [step 2](#step-2-what-loss-costs) and
Occamy's from [step 3a](#step-3a-the-congestion-control), two runs on the
same host and image over the same emulated link.

## Design experiments

Each change to how Occamy paces and carries frames is measured against
the one before it, under the same conditions, before it ships.

### Step 1: the frame window from the path

Occamy's server lets as many frames go unacknowledged as cover the
round trip, where it used to stop at two. Measured on 2026-11-02, a key
during full-screen motion over VNC at 50 ms, medians with 95%
intervals:

| | Window of two | Window from the path |
|---|---:|---:|
| Scene frames shown a second, of 30 | 18.8 [15.9, 28.3] | 29.6 [29.2, 29.9] |
| Scene frames never shown | 37% [6, 47] | 1.5% [0.4, 2.8] |
| Gap between frames, 95th percentile | 76.7 ms [64.0, 98.9] | 49.1 ms [47.8, 52.8] |
| Latency, 95th percentile | 119 ms [116, 133] | 95.7 ms [92.9, 99.4] |

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="benchmarks/realtime/1/factors-frame-window-dark.svg">
  <img alt="The improvement of Occamy with the window from the path over a window of two, on each scenario's primary metric, with 95% intervals" src="benchmarks/realtime/1/factors-frame-window.svg">
</picture>

The window was the limit only where the round trip is long and the
screen changes often. With no delay and at 20 ms, two frames already
covered the round trip, and nothing changed. Typing, long output and a
key on a still desktop did not change either. Over RDP the server's own
rate of about 24 frames a second holds, but the frames come more evenly:
48 ms apart at the 95th percentile instead of 63.
[The full report](benchmarks/realtime/1/report.md).

### Step 2: what loss costs

This step measured both servers over a link that loses 0.5% or 2% of
the packets to the client, before Occamy asked for BBR.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="benchmarks/realtime/2/loss-latency_p95-dark.svg">
  <img alt="A key's latency at the 95th percentile against the share of packets lost, for Guacamole and Occamy, in each scenario and network" src="benchmarks/realtime/2/loss-latency_p95.svg">
</picture>

- **There are two costs.** A lost packet holds back every frame behind
  it until it is sent again, up to three round trips. And TCP reads a
  random loss as congestion. With the congestion control Linux uses by
  default, its rate falls to about 1.22 · MSS / (RTT · √p). At 50 ms
  with 2% lost, that is 245 KiB a second, less than the motion scene's
  350 to 420. Occamy then showed only 16 of its 30 frames a second, and
  a key waited 5 to 8 round trips.
- **Guacamole needs more than the link carries** in every lossy cell,
  so its frames queue and a key waits behind them: 2.2 s over RDP at
  20 ms with 2% lost, and 5.6 s at 50 ms with 0.5%.
- **Typing and a key on a still desktop** send a packet or two a key,
  so the 95th percentile hardly moves. The few keys whose packet is
  lost wait three round trips and 200 ms. That is the wait for Linux's
  tail loss probe, the same with any congestion control.

[The full report](benchmarks/realtime/2/report.md). With 6 runs a side,
no difference here can be significant after the correction over 39
tests, so the conclusions rest on the shifts and their intervals.

### Step 3a: the congestion control

The same lossy link, with Occamy's server on CUBIC, Linux's default,
beside BBR.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="benchmarks/realtime/3a/factors-congestion-control-dark.svg">
  <img alt="The improvement of Occamy with BBR over Occamy with CUBIC on a key's latency, in each scenario, network and loss, with 95% intervals" src="benchmarks/realtime/3a/factors-congestion-control.svg">
</picture>

- **With BBR, loss costs a key about one round trip.** At 50 ms with 2%
  lost, a key during motion shows in 149 ms over VNC and 163 ms over
  RDP, compared with 360 and 471 ms with CUBIC: improvements of 2.45×
  and 2.97×.
- **The frame rate holds.** At 50 ms with 2% lost, the viewer sees 27.6
  of the scene's 30 frames a second over VNC instead of 16.4, and 24.4
  over RDP, the server's rate without loss.
- **Without loss nothing changes**, and neither does typing or a key on
  a still desktop.
- **A frame costs the server less CPU,** 19.6 ms over VNC at 50 ms with
  2% lost, against 24.9 with CUBIC.

Five of the 30 differences are significant after Holm's correction, all
on a key during motion with loss. Occamy has asked for BBR since.
[The full report](benchmarks/realtime/3a/report.md).

## What is measured

### The setup

`make bench` starts the demo stack with a bench desktop in place of the
demo's VNC and RDP servers, and guacd beside `occamyd`. The harness then
connects to each:

- **Guacamole.** The harness speaks the Guacamole protocol to guacd, as
  Guacamole's web application does, with guacd's defaults and
  `ignore-cert` for RDP. Like guacamole-common-js, it applies each frame,
  answers its `sync` at once, and sends a `nop` every 5 seconds.
- **Occamy.** The harness drives the Go client of protocol v1 against
  `occamyd`. It offers the codecs the web client offers: deflate, WebP,
  JPEG, scaled and raw. Like the web client, it acknowledges a desktop's
  frame at the next refresh of a 60 Hz screen, and a terminal's at once.

Both decode every image into a framebuffer, and each scenario runs on a
fresh session.

**The bench desktop** (`docker/bench/desktop`) runs a scene program
(`bench/scene`) with no window manager. It covers the screen with one
window and draws content from fixed seeds. Function keys switch scenes:
idle, the probe, a scroll, a drag, full-screen motion and a photograph.
The VNC server is x11vnc on Xvfb, tuned to answer a key in 2.9 ms at the
median and 3.5 ms at the 95th percentile. The RDP server is xrdp with
xorgxrdp, as Debian configures it. The terminal is 99 × 39 cells, the
size of guacd's at 1024 × 768 pixels.

### Scenarios

| Code | Name | Protocols | What happens | Primary metric |
|---|---|---|---|---|
| T1 | Typing | SSH | 200 characters, 10 a second, into an idle shell | Latency |
| T2 | Long output | SSH | `seq 1 100000`, then an end marker | Time until the marker shows |
| T3 | top | SSH | `top -d 0.5` for 30 s | Data per second |
| G1 | Still desktop | VNC, RDP | Nothing changes for 30 s | Data per second |
| G2 | Key press | VNC, RDP | Keys, 10 a second on average, on a still desktop | Latency |
| G3 | Scrolling text | VNC, RDP | A page scrolls a line every 16 ms for 10 s | Data per frame |
| G4 | Dragging a window | VNC, RDP | A 400 × 300 window dragged at 60 Hz for 10 s | Data per frame |
| G5 | Key during motion | VNC, RDP | Full-screen motion at 30 frames a second, with keys | Latency |
| G6 | Full-screen photo | VNC, RDP | One photograph fills the screen | Data to the client |
| G7 | Connecting | all | Connect, until the remote's known content shows | Time to the first frame |

### Networks

A proxy inside the harness carries every connection. It adds half the
round trip in each direction, sends at the bandwidth, and queues up to
twice the bandwidth-delay product.

| Code | Round trip | Bandwidth |
|---|---|---|
| N0 | none added | unlimited |
| N1 | 20 ms | 100 Mbit/s |
| N2 | 50 ms | 20 Mbit/s |
| N3 | 100 ms | 5 Mbit/s |
| N4 | 250 ms | 1 Mbit/s |

A proxy carries a byte stream and has no packets to lose. So a loss
study emulates the whole link in the kernel instead, with `tc netem` in
the network namespace of the series' server, and loses packets on the
way to the client only.

### The metrics

- **Latency** is from writing a key to the first frame that shows its
  effect. Each letter paints a 32 × 32 swatch in a colour of its own,
  and the latency ends when the client's framebuffer shows that colour at
  the swatch's centre. Keys come 100 ms apart on average, each moved by
  up to one 60 Hz period, so that they meet the screen's refresh at every
  phase. Typing's keys stay exactly 100 ms apart.
- **Frames** are the scene's frames that reached the client whole. Each
  frame of the scroll, the drag and the motion carries its number in the
  margin, and the harness counts the numbers it was shown. A server may
  split a frame of the scene or merge several, so this counts what the
  viewer saw, not what the server sent.
- **The gap between frames** is the time between consecutive frames
  shown. A scene shown whole and evenly at 30 frames a second has every
  gap at 33 ms.
- **Data** is the TCP payload to the client. Occamy's includes the
  WebSocket's frame headers.
- **Server CPU** is the CPU time of the container that serves the
  series: guacd's, including the process it forks for each connection,
  or `occamyd`'s.
- **Client CPU** is the harness's own, network emulation included, which
  every series pays alike.

A sample the stack does not answer fails rather than holding the run:
after 30 s to sign in and open the stream, 10 s for a key's effect, and
30 s for the long output's end marker.

### Analysis

`make bench-compare` and the plans below measure in **blocks**: one
series under one network in one study. Every run measures every block
once, in an order drawn from the plan's seed and the run's number, so
that drift on the host favours no series. Before each result, the
harness waits until the host's 1-minute load is below 8, so that one
result's load does not spill into the next.

`bench/report.py` then computes, for each scenario, network and metric:

- the **median** of the runs, with an exact distribution-free 95%
  interval from order statistics;
- the **improvement** of one series over the other: the other's value
  divided by its own, or its own divided by the other's for a metric
  where higher is better, such as frames shown a second. It is the
  Hodges–Lehmann estimate of that ratio, with its exact 95% interval
  from the Mann–Whitney distribution;
- the **p-value** of the exact two-sided permutation test of the rank
  sum, over every split of the runs.

Each comparison has one primary metric per scenario, fixed before any
data. Their p-values are corrected with Holm's method at 0.05. The other
metrics are exploratory, and their p-values are not corrected. The
report also says whether any conclusion changes without the results
that started above the load gate.

## Targets

For Occamy's stable release, each scenario's primary metric should be a
tenth of Guacamole's on the same network, an improvement of 10×. For a
latency, this applies to the 95th percentile. A key's effect should also
show within 50 ms. Where something outside Occamy makes that impossible,
such as the network's round trip or the remote server's own response
time, the target is that limit plus a small margin.
[The benchmark's spec](../specs/008-native-architecture/benchmark.md#targets)
sets each target, and [the full report](benchmarks/essential/report.md)
marks the ones Occamy meets.

## Running the benchmark

```sh
make bench
```

builds `occamyd`'s image from the working tree, starts the demo stack
with `docker/docker-compose.bench.yaml` on top, which adds guacd on
`127.0.0.1:4822`, and runs every scenario against Guacamole (`guacd`)
and Occamy (`v1-native`) under N0, N1 and N2. The results go to
`bench-results.json`, and a table to standard output.

Keep the host idle: a compile during a run more than doubled some
latencies. Use `-runs 3`, so that the table shows the median of three
runs and one disturbed run does not move it.

Server CPU needs podman, whose container statistics report CPU time;
with docker, pass `-engine none`. On a host shared with another stack,
`COMPOSE_PROJECT_NAME`, `OCCAMY_V1_PORT`, `OCCAMY_V1_CUBIC_PORT`,
`GUACD_PORT`, `OCCAMY_V1_DEBUG_PORT` and `OCCAMY_V1_CUBIC_DEBUG_PORT`
start it under its own name and ports, and `OCCAMY_IMAGE` names the
image it builds.

### The comparison

Each plan in `bench/plans` is one comparison:

| Command | Plan | What it measures | Time |
|---|---|---|---|
| `make bench-essential` | `essential.yaml` | Guacamole against Occamy: T1, T2, and G2, G5 and G6 over VNC and RDP, under N0 and N2, 6 runs | about 1 hour |
| `make bench-compare` | `compare.yaml` | Every scenario, network and series of the full comparison | several hours |
| `make bench-realtime-1` | `realtime-1.yaml` | Step 1: the frame window | about 2 hours |
| `make bench-realtime-2` | `realtime-2.yaml` | Step 2: Guacamole and Occamy with loss | about 3.5 hours |
| `make bench-realtime-3a` | `realtime-3a.yaml` | Step 3a: CUBIC against BBR with loss, 8 runs | about 2 hours |

Each writes its results as it goes. If the host's load stays above the
gate for 2 hours, it saves and exits, and the same command resumes.
`BENCH_PLAN` runs `make bench-compare` with another plan file. A plan's
`keys` sets how many keys a latency scenario presses, 200 by default,
and `connects` how many sessions G7 opens, 20 by default, or none.

A loss study needs the image that emulates the link,
`occamy-bench-netem`, which its target builds. Step 3a needs BBR in the
host's kernel; on podman's virtual machine:

```sh
podman machine ssh sudo modprobe tcp_bbr
```

The series:

| Series | What it runs |
|---|---|
| `guacd` | Guacamole: guacd, through the Guacamole protocol |
| `v1-native` | Occamy, as it ships |
| `v1-native-w2` | Occamy, with a client that offers a window of two frames |
| `v1-native-cubic` | Occamy on a second service that asks for CUBIC (`docker/bench/occamy-v1-cubic.yaml`) |
| `v1-native-nogfx` | Occamy over RDP without the graphics pipeline |

Results from before v1.0.0-beta.2 also have `occamy`, `v1-guacd` and
`v1-guacd-gfx`, which went through guacd, and step 3a's run has
`v1-native-bbr`, when `v1-native` ran CUBIC.

### Reports

```sh
make bench-essential-report    # bench-essential.json into docs/benchmarks/essential/
make bench-report              # bench-compare.json into docs/benchmarks/comparison/
make bench-realtime-1-report   # bench-realtime-1.json into docs/benchmarks/realtime/1/
make bench-realtime-2-report   # bench-realtime-2.json into docs/benchmarks/realtime/2/
make bench-realtime-3a-report  # bench-realtime-3a.json into docs/benchmarks/realtime/3a/
```

[uv](https://docs.astral.sh/uv/) runs `bench/report.py` with its own
numpy, scipy and matplotlib. It writes `results.csv`, one row per run;
`summary.csv`, the medians with their intervals; `comparisons.csv`, the
factors and p-values; `loss.csv` in a loss study; `report.md`; and each
figure as SVG in a light and a dark version.
`uv run bench/report_test.py` checks the analysis on synthetic results
with known answers. To analyse a committed run again:

```sh
uv run bench/report.py docs/benchmarks/essential/bench-essential.json -o docs/benchmarks/essential
```

### Flags

`BENCH_FLAGS` passes flags to the harness:

```sh
make bench BENCH_FLAGS="-scenarios T1 -net N1 -series v1-native"
```

| Flag | Default | Meaning |
|---|---|---|
| `-scenarios` | `all` | Scenario codes, comma-separated |
| `-net` | `N0,N1,N2` | Networks |
| `-series` | `guacd,v1-native` | Series, from the table above |
| `-runs` | `1` | How many times to run everything; the table shows medians |
| `-plan` | none | A plan file, in place of `-series`, `-net`, `-scenarios` and `-runs` |
| `-out` | `bench-results.json` | Where the results go, saved as they come |
| `-resume` | off | Continue `-out` if it measures the same plan, keeping its complete blocks |
| `-max-load`, `-load-wait` | `0`, `2h` | Wait before each result until the 1-minute load is below `-max-load`, for up to `-load-wait`, then save and exit with status 2 |
| `-ack-hz` | `60` | The screen refresh on whose ticks Occamy's frames are acknowledged; `0` acknowledges at once, a diagnostic |
| `-quick` | off | A tenth of the samples and a sixth of the time, to check the setup |
| `-failfast` | off | Stop at the first error and exit non-zero |
| `-engine` | `podman` | The container engine to read CPU time from, or `none` |
| `-project` | `occamy` | The stack's compose project |
| `-v1`, `-v1-cubic`, `-guacd` | `localhost:5637`, `localhost:5642`, `localhost:4822` | Where the series' servers listen |
| `-runtime` | none | `occamyd`'s debug listeners, as `service=address` pairs, to read its runtime from; `make bench BENCH_RUNTIME=1` sets them |
| `-link-image` | `occamy-bench-netem` | The image that emulates a loss study's link (`docker/bench/netem`) |

CI runs a quick smoke of every series on every push, with `-failfast`,
and compares no numbers, since shared runners are too noisy.

## Design

The benchmark's design, the targets and their derivation, and each
real-time step are in the specs:
[the benchmark](../specs/008-native-architecture/benchmark.md),
[the comparison](../specs/008-native-architecture/comparison-benchmark.md)
and [real-time pacing and transport](../specs/008-native-architecture/realtime-transport.md).
Results from before v1.0.0-beta.2, measured through guacd, are in this
page's history.
