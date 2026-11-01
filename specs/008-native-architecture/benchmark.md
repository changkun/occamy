---
title: Protocol benchmark
status: complete
track: architecture
depends_on:
  - specs/006-demo-stack-refresh.md
affects:
  - bench/
  - docker/bench/
  - docker/docker-compose.bench.yaml
  - internal/guacamole/
  - Makefile
  - .github/workflows/dockerimage.yml
  - docs/benchmarks.md
  - go.mod
effort: large
created: 2026-10-29
updated: 2026-11-01
author: Changkun Ou
dispatched_task_id: null
---

# Protocol benchmark

## Goal

Measure what Occamy costs today under fixed scenarios, so that protocol
v1 and its graphics pipeline are designed against numbers and proven with
the same harness. Today Occamy speaks Guacamole and runs everything through
guacd. The owner set two targets:

- **Speed:** at least 10x better than the Guacamole baseline, benchmarked.
- **Latency:** input-to-update latency under 50 ms for desktop
  interactions.

This spec defines the yardstick: the metrics, the scenarios, how they are
measured, and how the targets are judged. The harness measured v0 first.
Protocol v1 has a driver in the same harness, through
[client-core](client-core.md), whose full run waits for the rest of the
graphics pipeline and for native RDP
([the v1 series](#implementation-notes-the-v1-series)).

## Metrics

All times come from the harness's monotonic clock in one process, so no
clocks need synchronizing.

| Metric | Definition |
|--------|------------|
| Input latency `L` | From writing an input message to the WebSocket until the harness has received and decoded the drawing that reflects it (the probe, see below). Reported as p50, p95 and p99 over at least 200 inputs. |
| Processing latency `Lp` | `L` measured with no network emulation. It covers occamyd, guacd (v0), the remote server and the client decode, and it is the part Occamy can improve. |
| Bytes | TCP payload bytes through the emulated link in each direction, which include WebSocket frame headers where there is a WebSocket: totals for bounded scenarios, per second for continuous ones. |
| Server CPU | CPU time of the occamyd and guacd containers during the scenario, read from the container engine's stats before and after, and reported separately. The remote server's container is reported as well, but it is not Occamy's. |
| Client CPU | The harness's own time spent decoding and applying drawing. It stands in for a client's cost; browsers decode natively, so only ratios between series are meaningful. |
| Delivered frame rate | For scenes animated at a known rate, the scene's frames that reached the client per second, counted by the scene's frame stamps, and their share of the scene's frames. The complete frames a server sent (`sync` in v0, `frame-end` in v1) are reported apart: one may carry part of a frame of the scene, or several. |
| Time to first frame | From dialing the WebSocket until the first complete frame that shows known content of the remote: the wallpaper, or the shell's prompt. |
| Completion time | For bounded output, from the command until its end marker is visible. |

### The latency probe

- **Desktop:** the scene program (below) fills a 32×32 swatch at a fixed
  position with a colour derived from each key it receives. The probe
  completes when the swatch's centre pixel in the harness's decoded
  framebuffer has that colour. Keys come 10 a second on average, each up
  to one period of a 60 Hz screen either way from its slot, so that a
  run's percentiles cover every phase of the screen and of the scene.
- **Terminal:** the harness types into an idle shell, and the probe
  completes on the first frame containing drawing after the key. The
  harness ignores frames without drawing: guacd sends `sync` and `nop` on
  its own schedule.

### The 50 ms budget

The 50 ms requirement is end to end, so it only holds below some network
round-trip time (RTT). The budget for `L` p95 is:

| Part | Budget |
|------|--------|
| Network RTT | up to 30 ms |
| Remote server reaction (the scene program's draw) | up to 5 ms |
| Occamy: server, client decode and all queueing | up to 15 ms |
| Total | 50 ms |

The harness measures up to decoded pixels. A display adds up to one
refresh interval on top (16.7 ms at 60 Hz). Counted to photons, the
50 ms target therefore holds up to about 13 ms RTT.

## Scenarios

Every scenario runs over v0 through occamyd, the shipped path. A second
v0 series connects to guacd directly, to separate occamyd's proxying cost
from guacd's own. Four v1 series run them over protocol v1: one with
occamyd's native backends and one through the guacd bridge for comparison
within v1, as each ships, and each of the two with RDP's graphics
pipeline the other way, for RDP alone.

| ID | Protocol | Scenario | Primary metric |
|----|----------|----------|----------------|
| T1 | SSH | Type 200 characters, 10 per second, into an idle shell | `L` |
| T2 | SSH | `seq 1 100000`, then an end marker | bytes, completion time, server CPU |
| T3 | SSH | `top -d 0.5` for 30 s | bytes per second |
| G1 | VNC, RDP | Idle desktop for 30 s | bytes per second, server CPU |
| G2 | VNC, RDP | Probe keys on an otherwise still desktop | `L` |
| G3 | VNC, RDP | A page of text scrolled one line every 16 ms for 10 s | bytes, server CPU, delivered frame rate |
| G4 | VNC, RDP | A 400×300 textured window dragged across the screen at 60 Hz | bytes, server CPU, delivered frame rate |
| G5 | VNC, RDP | Full-screen procedural motion at 30 fps, with probe keys during it | delivered frame rate, bytes, `L` under load |
| G6 | VNC, RDP | One static photographic full-screen image | bytes, time until complete |
| G7 | SSH, VNC, RDP | Connect | time to first frame |

G5's `L` under load is the queueing test. Guacamole cannot put a key's
update ahead of bulk frames, and protocol v1 must.

### Network conditions

| ID | RTT | Bandwidth |
|----|-----|-----------|
| N0 | none added | unlimited |
| N1 | 20 ms | 100 Mbit/s |
| N2 | 50 ms | 20 Mbit/s |

A userspace proxy inside the harness adds half the RTT in each direction
and serializes each direction at the bandwidth, with a queue of twice the
bandwidth-delay product, so that a fast sender meets backpressure as on a
real link. It emulates latency and throughput, not loss or TCP congestion
behaviour, and it does not delay TCP handshakes, which it answers
locally.

## Targets

For each scenario, the target is 10x better than the v0-through-occamyd
baseline on its primary metric, under the same network condition, at the
p95 for latencies.

The targets gate the release (owner, 2026-10-31): v1.0 waits until the
benchmark shows native at least 10x better than guacd. It does not ship
with a guacd fallback for any protocol still short of 10x; release
candidates may still carry one ([008](../008-native-architecture.md)).

The gate is each scenario's target as defined here, not a flat 10x
(owner, 2026-10-31, keeping 008's acceptance clause "or each miss is
justified there by a bound Occamy does not control"). A scenario may
miss 10x only where a documented bound outside Occamy, such as the
network condition or the remote server's own time, makes 10x
impossible, and `docs/benchmarks.md` must justify each such miss.

The baseline run may show that a metric is bound by something Occamy does
not control: the RTT, the remote server, or the scene's own frame rate,
for example. A delivered frame rate cannot exceed the scene's. Such a
metric's target becomes that bound plus a stated margin. The evidence goes
next to it in `docs/benchmarks.md`, so that no target is quietly dropped.
The bounds are applied as follows:

- **Latency and completion:** at N0, no lower than the remote server's own
  time, where it is measured, plus 2 ms; at N1 and N2, no lower than the
  N0 target plus one RTT plus the time the link takes to carry the
  target's bytes.
- **Frame rates:** 95% of the scene's rate, or as many frames of the
  target size as the link carries, whichever is fewer.
- **Continuous scenes (G3 to G5):** bytes and CPU per delivered frame,
  from N0, so that dropping frames does not count as saving bytes and a
  frame's target does not depend on the network.
- **The photograph (G6):** one bytes target, 10x below the median of the
  runs in which guacd encoded it at least partly lossy. guacd picks PNG
  or a lossy format by how recently a region changed, so the same image
  costs 0.4 to 2.2 MiB from run to run.
- **Unmeasured bounds** leave the target in place until a measurement
  shows the server alone exceeds it.

The 50 ms budget applies separately: `L` p95 for G2 and T1 under N1, and
for G5 under N1, must be at most 50 ms with v1.

## Decisions of 2026-11-01

The owner delegated the questions this spec had left open, so these are
decided, each with its reason. They say how the v1 series runs. They do
not start it: the owner's go-ahead still does that.

1. **G5 with GFX: both are measured, and GFX on is the verdict.**
   `rdp-native` runs G5 with GFX and with `disable_gfx`. The shipped
   default, GFX on, is the verdict row. The decode cost is Occamy's own
   bound, so it cannot be a justified miss ([Targets](#targets)). If
   that row misses, decoder work comes before v1.0, and the measurement
   shows where. The decoder is not made faster first, since that would
   spend the work before the measurement says where it belongs.
2. **GFX on one side only: both sides run both ways.** `rdp-native` and
   `rdp-guacd` each run with and without GFX, so that rows compare like
   with like. guacd's GFX is reachable through `guacd.params.rdp`, and
   [rdp-strategy](rdp-strategy.md)'s table has measured it. The row of
   the shipped default is the verdict, and the like-for-like rows sit
   beside it.
3. **Method: all three changes.** The scene stamps its frames and the
   harness counts the distinct ones it was shown, in both protocols.
   G7 ends on known content in both series, SSH included. The probe's
   keys are jittered, so that no run sits at one phase of a 60 Hz tick.
   The baselines that these change are measured again with them: G2 for
   the jitter, G3 and G4 for the stamp, G5 for both, and G7 for its end.
   The rule behind that: anything that changes what the remote shows,
   or how a scenario measures it, measures that baseline again.
   `docs/benchmarks.md`, What to measure again, states the full list
   and why, along with what is not measured again.
4. **Colour depth: 32 bits per pixel on both sides.** That is native's
   default and what rdp-strategy's table measured, and mismatched
   depths may make xrdp keep a second X session, with a second scene
   program, beside the measured one. The bridge's recordings used 24
   for a reason of their own: guacd asks for 16 with GFX off, which
   loses colour, and those recordings had to be exact
   ([guacd-bridge](guacd-bridge.md)'s conformance notes). That is no
   reason to measure at 24.
5. **The 8-hour soak is a stability gate before v1.0.** It runs once
   and is not a row in the series: it answers whether `occamyd` leaks
   over hours, which the series does not ask
   ([008](../008-native-architecture.md),
   [guacd-bridge](guacd-bridge.md), Implementation notes: VNC's soak).

The harness work for decisions 1 to 4 comes with its own notes. The
other decisions of that day, which are not the benchmark's, are in
[rdp-strategy](rdp-strategy.md), [008](../008-native-architecture.md)
and [api-and-server-stack](api-and-server-stack.md).

## Components

### Harness (`bench/`)

A Go command, `go run ./bench`, with flags for the scenarios, network
conditions and series, and the endpoint. It writes a JSON result and a
Markdown table.

- **Drivers:** v0 through occamyd (log in, dial `/api/v0/connect`); v0
  direct to guacd (the `internal/guacd` handshake); v1 through the
  `client` package and API v1 (sign in, open a connection's stream). Each
  v0 driver answers `sync` the way a browser does and announces the
  image types a browser announces (PNG, JPEG, WebP); the v1 driver
  offers the codecs occamy's web client offers and acknowledges each
  frame once it has looked at it. Each keeps a decoded framebuffer.
- **Guacamole interpreter** (`internal/guacamole`, next to the parser,
  so that [guacd-bridge](guacd-bridge.md) can reuse it):
  - It applies `size`, `img`/`blob`/`end`, `copy`, `transfer`,
    `rect`/`cfill` and `dispose` to layers and buffers, and composites
    the visible layers that `move` and `shade` place. guacd's terminal
    draws on such a layer, which it moves into the default layer.
  - It counts the operations it does not support, so a skewed framebuffer
    shows up in the results.
  - It is written fresh. `internal/guac` on the `gio-client` branch
    depends on `tfriedel6/canvas` for paths, and Guacamole's fills only
    need rectangles.
  - WebP decoding uses `golang.org/x/image/webp`.
- **Network proxy:** a TCP proxy the driver dials through, configured per
  condition.
- **CPU sampling:** container stats from podman, read before and after
  each scenario. docker's stats have no cumulative CPU time.
- **Repetition:** `-runs N` measures everything N times; the table shows
  the median of each number, and the JSON keeps every run.

### Bench desktop (`docker/bench/desktop`)

An image with Xvfb and x11vnc, and xrdp with xorgxrdp, whose sessions run
the scene program instead of a desktop. One container runs per protocol,
as the `vnc` and `rdp` services, so that hosts, credentials and CPU
attribution stay as they are. x11vnc runs threaded and polls every
millisecond, without naps or move and scroll detection, so that the VNC
server stays within the 5 ms remote budget. A compose override,
`docker/docker-compose.bench.yaml`, swaps it into the stack and publishes
guacd on `127.0.0.1:4822` for the direct series. `make run` and the e2e
job do not use it. `make bench` builds occamyd's image, starts the stack
with the override and runs the harness. The demo images stay as they are.

### Scene program (`bench/scene`)

A pure-Go X11 client (`github.com/jezek/xgb`) that covers the screen with
one override-redirect window and takes the input focus, since there is no
window manager. It draws deterministic content, and function keys select
the scene, so the same control works over every protocol and series:

| Key | Scene |
|-----|-------|
| F1 | idle (G1) |
| F2 | probe: letters fill the swatch (G2) |
| F3 | scroll (G3) |
| F4 | drag (G4) |
| F5 | motion, with the probe still active (G5) |
| F6 | photo (G6) |
| Escape | back to idle |

The photo and the texture are generated procedurally from a fixed seed,
so the repository carries no image files.

Each frame of the scroll, the drag and the motion carries a stamp in the
margin that no scene animates: a column of black and white cells that
hold a 16-bit frame number and a check of it, big enough to survive
lossy encoding (`bench/internal/probe`). The harness counts the distinct
stamps it was shown, whole, so that one count holds in every protocol
and however a server cuts the scene into frames of its own.

## Testing Strategy

- Unit tests for the interpreter (each operation against expected pixels),
  the proxy (added delay and rate within tolerance), and the probe
  matcher.
- A CI smoke job runs every scenario briefly under N0 and N1 to prove
  that the harness and the bench desktop work. CI never gates on absolute numbers
  from shared runners.
- Baselines come from a documented local run, with the hardware, OS,
  engine and versions recorded.

## Tasks

| Task | Delivers |
|------|----------|
| Terminal | The harness, the v0 drivers, the proxy, CPU sampling, and T1 to T3 with G7 for SSH |
| Desktop VNC | The interpreter, the bench desktop, the scene program, and G1 to G7 over VNC |
| Desktop RDP | G1 to G7 over RDP through the same desktop image |
| Baseline | `docs/benchmarks.md` with the baseline numbers and the per-scenario targets, and the CI smoke job |

## Outcome

**Summary.** The harness, the bench desktop and the Guacamole baselines
shipped in three pull requests, implemented directly rather than
dispatched: the Terminal task in #76 and the Desktop VNC and Desktop RDP
tasks in #77, both on 2026-10-29, and the Baseline task in #81 on
2026-10-30.
[docs/benchmarks.md](../../docs/benchmarks.md) holds the terminal and
desktop baselines, as medians of three runs, and the per-scenario
targets. Drift: minimal. Of 17 items, 14 shipped as specified and 3
diverged, with the body above corrected. None is unimplemented apart
from the v1 series, which waits for [client-core](client-core.md).

### What shipped

- **Harness** (`bench/`):
  - the occamy and guacd-direct drivers, the network proxy, CPU sampling
    through podman, `-runs` with median tables, and the scenarios T1 to
    T3 and G1 to G7 over SSH, VNC and RDP;
  - `make bench`, which builds occamyd's image, starts the stack with
    `docker/docker-compose.bench.yaml` and runs it;
  - a CI smoke job that runs it quickly under N0 and N1.
- **Interpreter** (`internal/guacamole`): layers and buffers, the
  drawing and compositing operations, all sixteen channel masks, and
  counts of unsupported operations.
- **Bench desktop** (`docker/bench/desktop`): Xvfb with tuned x11vnc,
  and xrdp with xorgxrdp, each running the scene program (`bench/scene`)
  as its session. The probe colours live in `bench/internal/probe`.
- **`bench/vncprobe`**: an RFB client that runs inside the VNC container
  and measures x11vnc on its own, per scene, for the remote server's
  bounds.
- **Tests:**
  - the proxy's delay and rate, percentiles and median tables
    (`bench/bench_test.go`);
  - the probe colours (`bench/internal/probe`);
  - the scene's determinism (`bench/scene`);
  - the interpreter's operations and masks (`internal/guacamole`).
- **Documentation:** the baseline data in `docs/benchmarks/`
  (`terminal-baseline.json` and `desktop-baseline.json`).

### Design evolution

- **Bytes** are the TCP payload at the proxy, WebSocket framing
  included, instead of the WebSocket payload (#76). The difference is a
  few bytes a message, and both series are counted the same way.
- **The proxy** serializes each direction at the bandwidth behind a
  queue of twice the bandwidth-delay product, instead of a token bucket,
  so that a fast sender meets backpressure. Because it is a userspace
  proxy, TCP handshakes are not delayed, and G7 counts only the round
  trips that carry data: about two for v0 in both series.
- **The targets' rules** were settled against the baseline (#81):
  - Bytes and CPU in G3 to G5 are per delivered frame and come from N0,
    because over RDP the baseline delivered a third of the scroll's
    frames, and a per-second target would reward dropping more.
  - G6's bytes target is anchored on guacd's lossy runs instead of the
    median of all runs, because guacd's choice of PNG or a lossy format
    depends on timing. Its runs fall into three groups: 2.2 MiB,
    1.2–1.5 MiB and 0.4 MiB.
  - A bound is only applied where it was measured. Unmeasured server
    times leave the 10x target in place, rather than weakening it on an
    assumption.
- **The CI smoke job** runs under N0 and N1, not N0 alone, so that the
  proxy is exercised too.

### Not implemented

- ~~**The v1 series**, which needs the Go client core
  ([client-core](client-core.md)).~~ Its harness shipped on 2026-10-31;
  see [the v1 series](#implementation-notes-the-v1-series). Its full run
  has not been made, and waits for the items named there.

### Unspecified work

- `bench/vncprobe`, which gives the remote server's own latency and
  update rate (an improvement).
- `make bench` now builds occamyd's image, because before it measured
  whatever `occamy:latest` happened to be (an improvement).
- The interpreter's sixteen channel masks, which guacd uses on buffers
  at connect (scaffolding).
- Sessions 2 s apart, so that one does not join the previous session
  while it ends (scaffolding, 003 defect 6).

### Decisions

- The bench desktop replaces the `vnc` and `rdp` services' images
  instead of adding services, so that hosts, credentials and per-service
  CPU stay as they are.
- The probe swatch is reset to grey by every scene key. The readiness
  check alternates two letters of different colours, because an update
  that paints the colour already shown changes no pixels and draws
  nothing.
- Every wait in a desktop scenario is bounded. At N2 a VNC session could
  keep drawing long enough that waiting for quiet hung until the
  scenario's 10-minute limit.
- Runs are repeated and reported as medians, because single runs picked
  up disturbances on the host: a compile during one run more than
  doubled a p95 latency.
- The latency margin is 2 ms, and a frame rate's margin is 5% of the
  scene's rate.

### Surprises

- guacd's terminal draws on visible layer 1 at (7, 7), not on the
  default layer, so the interpreter needed layer compositing for SSH.
- The podman VM counts time that the host takes its CPUs away as CPU
  time, so a disturbed run shows more CPU as well as more time.
- x11vnc with its defaults uses about 8% of a CPU with no client
  connected, and takes 27 ms at the median and 125 ms at p95 from a key
  to its update. The bench desktop tunes it to 2.9 ms and 3.5 ms
  (`docker/bench/desktop/start.sh`), at about one CPU whether a client
  is connected or not.
- guacd 1.6 encodes a region as JPEG or WebP only if it changed recently
  enough (`src/libguac/display-worker.c`). So the encoding of a still
  image depends on timing, and G6's bytes vary 5x between runs.
- RDP stops at 24–25 frames a second with bandwidth and CPU to spare.
  Connecting over RDP takes 2.2 s, against 110 ms over VNC.
- The bench host is shared, and other work on it pushed the load average
  as high as 34 during the desktop baseline. The run logs the load every
  minute, and the medians absorbed the one disturbed run.

### Findings for v1

- occamyd costs nothing measurable over guacd.
- In full motion a key waits behind frames: 84 ms at N0 and 113 ms at
  N1 over VNC, while x11vnc shows it in 5.6 ms. This is the main fix v1
  needs, and the only 50 ms miss at N1.
- guacd coalesces T2's 575 KiB of output into 82–115 KiB of screens, so
  v1 needs terminal state on the server.

### Follow-ups

- An RDP counterpart of `bench/vncprobe`, for xrdp's reaction and its
  frame-rate ceiling.
- Each server's connect time on its own: sshd, x11vnc and xrdp.
- The shell's echo over SSH on its own.
- An image-quality score for G6, so that its bytes compare at equal
  quality. The scene's photograph can be regenerated from its seed.
- ~~The v1 series, once client-core exists.~~ The harness is done (see
  below); the full run is a follow-up of its own.

## Implementation notes: the harness for the v1 run

**Status:** the harness does what the decisions of 2026-11-01 settled
for the run, and no series has run with it: a `-quick` smoke proved that
it works and nothing else. The owner's go-ahead starts the series.
`docs/benchmarks.md`, What to measure again, lists the baselines that
are out of date until then.

- **What was done:**
  - *The frame stamp.* The scene stamps each frame of the scroll, the
    drag and the motion in the left margin, in cells big enough to
    survive lossy encoding, with a check that a half-drawn stamp or a
    plain area fails. The harness reads the stamp from the framebuffer
    of any frame that damaged it, in both protocols, and counts the
    distinct ones it was shown. In a scene, `frames` is that count, and
    `server_frames` the frames as the server cut them, in both
    protocols, so the table shows every scene's frame rate again.
  - *G7 on known content.* A session looks at its first frames for what
    its protocol shows (`shows`) until one does, and G7 ends there.
    A desktop shows the wallpaper in the middle of the screen, and a
    terminal the shell's prompt. The first frames' marker goes once it
    matched, so that it costs later frames nothing, and a marker that a
    scenario set meanwhile stays.
  - *Key jitter.* `probeLatency` puts each key up to one period of a
    60 Hz screen either way from its slot, with offsets drawn from the
    run's index, so that every series of a run presses its keys at the
    same times. G2 shares the function, so its baselines run again too.
    T1's keys are not jittered: a terminal's frames wait for no tick.
  - *GFX both ways.* `v1-native-nogfx` is `rdp-native` with
    `disable_gfx`, and `v1-guacd-gfx` is `rdp-guacd` with guacd's
    `disable-gfx` off. Each serves RDP alone. RDP's rows of a v1 series
    carry `gfx`, and those of what the side ships `verdict`, and the
    table notes which each row is.
  - *Colour depth.* guacd gets `color-depth: 32` in both bench files, as
    native RDP asks without being told.
- **Decisions:**
  - G7 ends on what the remote shows anyway, so the stack needs no
    banner. A banner for the shell would have changed what T1 to T3 see,
    and with it their baselines, by the rule of decision 3. The demo
    shell's prompt is there already, after everything it prints at
    login.
  - v0's terminal arrives as pixels, so v0 reads the prompt from guacd's
    cursor: a solid cell of guacd's grey, on guacd's grid of 10 × 19
    pixels from (7, 7), with a blank cell before it and a drawn one
    before that, the prompt's `$ `. The cursor stands on the first
    column or straight after text on the screens before the prompt, and
    a frame that clears the screen draws none. If guacd's terminal
    changes, G7 over v0's SSH fails rather than ending early.
  - `v1-guacd-gfx` runs on a bench-only service, `occamy-v1-gfx`, with a
    file of its own: guacd's parameters are global to a v1 file, and a
    benchmark is no reason for a setting per connection in the product.
    It keeps its signing key on a tmpfs, since nothing of it outlives a
    run, and `OCCAMY_V1_GFX_PORT` publishes it on another port for a
    second stack on a shared host.
  - The wallpaper's colours moved to `probe.Wallpaper`, which both the
    scene and the harness use. It truncates as the scene's code did, and
    a test pins a row of it, so that G1 and G6 keep their pixels.
- **Tests:** `bench/shows_test.go`, `bench/bench_test.go`,
  `bench/v1_test.go`, `bench/internal/probe/probe_test.go` and
  `bench/scene/content_test.go`.
  - The stamp's round trip, that it changes every frame of a scene,
    that a plain area, a half-drawn stamp and a wrong check read as
    none, and where it sits.
  - A stamp counts once, a half-drawn one not at all, and a number that
    comes back as a new frame, over v0 and v1 sessions and on the
    session alone. In a scene `frames` is the stamps' count in every
    series, and without one it stays the frames that drew.
  - G7 over a v0 session of each protocol, played from a script of
    Guacamole instructions, ends on the frame that shows the content,
    not one that sizes the screen, clears it or shows a banner. Over a
    v1 session, in process, it does not end on the blank terminal, the
    clear screen of its resize, a banner, or a black screen. `openV1`
    and `openV0` look for what their protocol shows.
  - `showsPromptPixels` against a cleared screen, a cursor at the start
    of a line, after text and on a blank line, a screen of its colour,
    and damage elsewhere; `showsPromptCells` against a banner, the
    prompt in colour, half of it, and a line that follows it.
  - The first marker goes once a frame matched, and a marker that a
    scenario set meanwhile stays.
  - Key offsets stay within the jitter, are the same for a run, differ
    between runs and centre on the slot, and the probe's keys fall on
    them, which holds however late a loaded host wakes the harness.
  - The bench files give every series its backend, GFX and colour depth,
    `occamy-v1-gfx` serves its own file on the loopback interface, and
    CI runs every v1 series. RDP's rows carry `gfx`, `verdict`, their
    note and the CPU of what served them, and no other row does.
  - What each step of the drag clears covers everywhere its window goes,
    and neither the swatch nor the stamp.
- **Surprises:**
  - The first smoke read no stamps in G4: 0 of 12 frames over VNC, and
    1 of 34 over RDP, where G3 and G5 read nearly every frame. Every
    step of the drag exposes the window behind the one it moved, and
    the scene answered each Expose event with a full redraw, which
    cleared the stamp right after the frame had drawn it. No unit test
    could see that, since it takes an X server; the frame rate read as a
    slow server rather than as a broken count. The drag now clears only
    right of the margin. It still clears there on every step, as it
    always did, so that G4 sends what it sent: on RDP that may be more
    than the moved window, and whether to stop is a question for G4's
    measurement, not one this work settled.
  - The smoke's G7 over RDP took about 10 s on every v1 path and about
    3 s on v0, with the two native paths 3 ms apart, which looked like a
    timer rather than load. It was one, and a known one: xrdp 0.10.1's
    channel server can exit as a connection closes, and sesman starts it
    only with the session
    ([native-rdp-backend](native-rdp-backend.md#implementation-notes-registration)).
    The demo images start it again from `reconnectwm.sh`, and the bench
    desktop did not have that script. xrdp logs `Connecting to chansrv`
    and, exactly 10 s later, `xrdp_mm_chansrv_connect: error in
    trans_connect chan`, and the screen stays black until then. The
    wallpaper marker is not late, since no frame arrives during the
    wait. Sesman logs `xrdp-chansrv` finishing as the first native
    session of an X session ends, 96 ms after xrdp's end of that session,
    and without the script it does not come back for that X session. All
    three of its exits on the bench stack happened so. On one fresh X
    session, two `rdp-guacd` sessions showed the wallpaper in 2627 and 3230 ms and
    left it running, one `rdp-native-nogfx` session took 2149 ms and it
    exited, so GFX is not why, and `rdp-guacd` then took 11152 ms. v0
    was only faster for running first: `occamy` and `guacd` took 3070
    and 3302 ms before any native session, and 12345 and 12997 ms after
    one, at a lower load. The first smoke's 11310 ms for `occamy` is
    not this, since v0 ran first there and xrdp logged no failure until
    native had run; that was a host load of 93 to 185. The old end of G7
    stopped at the black frame, which is why the baselines' 2.2 s never
    showed this: CI's run on the old end read 44.7 ms for `v1-native`.
    The bench desktop now has the script, and
    `TestBenchDesktopReconnectScript` keeps it there. In the same order, in `-quick` runs at a load of 21
    to 33, `rdp-guacd` took 1317 ms, `rdp-native-nogfx` 143 ms, `rdp-guacd`
    1134 ms, and v0's `occamy` and `guacd` 4353 and 4486 ms straight after
    a native session. The script restarted the channel server after each
    of the three native runs, and xrdp logged no `error in trans_connect
    chan` in seven runs. Those numbers show that the wait is gone and
    compare nothing else.
    The notes on the channel server describe a connection that closes
    just after attaching. Here a native session's end set it off 3 to 5 s
    after attaching, every time, and none through guacd did. That is not
    explained, and a server without the script still pays the wait. There
    are two leads, and neither is shown to be it. Native offers only
    `cliprdr` and `drdynvc`, and guacd also `rdpsnd`, whose setup is what
    `xrdp-chansrv`'s log stops short of. And `Client.Close` drops the
    connection with no Disconnect Provider Ultimatum, which native only
    ever receives: xrdp logs an SSL `unexpected eof` after 32 of 38 native
    connections and none of 74 through guacd.
  - The first smoke's low v1 counts in G3, such as 2 stamps of 16 frames
    over native VNC, were not misread stamps. Tallied over 6 s of the
    scroll, native VNC and guacd's VNC each read 107 stamps, so that was
    a short run on a loaded host. Of the frames that touched the stamp,
    4 to 44% read as no whole stamp, on every path, and those that were
    looked at showed why: scrolled text that jumps partway down, a
    screen torn between two scene frames, or a screen still partly
    drawn. So `frames` counts what reached the viewer whole, and the
    stamp refuses the rest as it should. Native RDP with GFX ended far
    fewer frames in that tally, 34 against 169 with GFX off, on a loaded
    host: a thing for decision 1's rows to measure, not a result.

## Implementation notes: the v1 series

**Status:** the harness is done; the measurement is not. On 2026-10-31
the owner decided that v1's numbers are not to be taken as its verdict
before the graphics pipeline's remaining tasks (Refinement, Tile cache,
and Encode cache and workers, in
[graphics-pipeline](graphics-pipeline.md)) and native RDP
([native-rdp-backend](native-rdp-backend.md)) are in occamyd. Native
RDP has been rdp's default since #132, and asks for RDP's graphics
pipeline (GFX) by default since its third phase's second part, without
ClearCodec and the cache yet; `v1-native` measures it. So
`docs/benchmarks.md` documents how to run the v1 series and publishes no
v1 results, and no target is judged met or missed yet. The full run is
also the one that gates v1.0 (owner, 2026-10-31; see
[Targets](#targets)). One local pass ran every scenario end to end on
both series, under N0 and N1. It ran on a loaded shared host, so it
checked the harness, not the numbers, and its numbers are not
published. These notes describe the two series as they were first
written; where they differ, such as in how a scene's frames count and
where G7 ends, [the notes above](#implementation-notes-the-harness-for-the-v1-run)
say what the harness does now.

- **What was done:**
  - Two series, `v1-native` (SSH, VNC and RDP through occamyd's own
    backends) and `v1-guacd` (VNC and RDP through the guacd bridge), on
    the scenarios, metrics, proxy and bench desktop of the v0 series. A
    series serves only the protocols it has a connection for and skips
    the others: SSH has no guacd backend in v1, so `v1-guacd` runs no
    terminal scenario.
  - `rdp-native` joined once #132 made native RDP rdp's default. It
    reaches the bench desktop's xrdp with `rdp-guacd`'s credentials and
    the harness's 1024 × 768, with `security: tls`, since xrdp speaks no
    NLA, and without a certificate check, since xrdp signs its
    certificate when the image is built; occamyd warns of that at
    start. When the harness was written neither path spoke RDP's
    graphics pipeline; native RDP now asks for it by default, and xrdp
    draws in RemoteFX Progressive, while the bridge gives guacd
    `disable-gfx`.
  - Native RDP's decoding rate, which holds a session to about a core
    of the benchmarks' host, caps full-screen RemoteFX at about 97
    frames a second at the harness's 1024 × 768, 37 at 1920 × 1080 and
    9 at 3840 × 2160, near the decoder's own speed on one core
    ([native-rdp-backend](native-rdp-backend.md)'s phase 3 notes). No
    scene reaches it at 1024 × 768. It is Occamy's bound, not one
    outside it, so it justifies no miss.
  - G5 over native RDP with GFX is an expected miss. G5's scene redraws
    every pixel in every frame, so `rdp-native` with GFX decodes a full
    screen of RemoteFX Progressive tiles a frame: about 10.3 ms of CPU
    at 1024 × 768 on the benchmarks' host, 10.4 to 11.9 as the review
    measured, against G5's RDP target of 5.5 ms a frame. With
    `disable_gfx`, bitmap updates cost about 1 to 2.4 ms a frame. It
    runs both ways, GFX on being the verdict, and the decoder is not
    made faster first ([decision 1](#decisions-of-2026-11-01)).
  - `bench/v1.go` drives the Go client core through API v1: it signs in
    at `POST /api/v1/auth/token`, opens
    `GET /api/v1/connections/{id}/stream`, asking for 1024 × 768 pixels
    or 99 × 39 cells, and after each scenario closes the client and ends
    the session with `DELETE /api/v1/sessions/{id}`. It opens the
    stream with `client.Dial`; when the client refuses the session the
    server welcomed it to, `client.Dial`'s `*client.RefusedError` names
    the session, which the harness ends too.
  - Every wait of a sample is bounded, so that a stack that stopped
    answering fails the sample rather than the run: 30 s for the sign-in
    and the stream's handshake and for G7's first frame, 10 s for a
    key's update and for an input to be queued, 30 s for T2's marker.
    CI's bench job stops at 30 minutes and prints the stack's logs when
    it did not succeed, and the harness exits non-zero when a chosen
    series ran no scenario. `-failfast`, which CI sets, ends a run at
    its first measurement with an error.
  - The session that the scenarios drive is no longer Guacamole's: it
    takes keys and reports complete frames that drew, with a marker
    checked on each, and a v0 or a v1 reader fills it. The scenarios did
    not change, apart from the marker, which now has a pixel form and a
    terminal-cell form.
  - The stack's `occamy-v1` service serves `docker/bench/occamy-v1.yaml`
    in the bench stack, which `docker-compose.bench.yaml` mounts over the
    demo's v1 file: the four connections, each naming its backend so
    that no change of a protocol's default moves a series, the demo
    user, and 100 sessions per user. guacd's published port and `make bench`'s project
    and ports follow `COMPOSE_PROJECT_NAME`, `OCCAMY_PORT`,
    `OCCAMY_V1_PORT` and `GUACD_PORT`, for a second stack on a shared
    host.
  - CPU sampling reads the `occamy-v1` container as well. A v1
    series' server CPU is `occamy-v1`'s, and guacd's for `v1-guacd`;
    `v1-native`'s row gives guacd's CPU in a note, apart.
  - CI's bench job runs both v1 series with `-quick` under N1 after the
    v0 smoke run, on the same stack.
- **How the metrics map to v1:**
  - *Decoding.* The client offers what the web client offers where the
    browser decodes everything: deflate, WebP, JPEG, then raw. The
    harness plugs decoders into the client core's pool: deflate from
    `internal/codec` (one `Inflater` per goroutine, from a pool), WebP
    from `gen2brain/vpx/webp` with `ToRGBA` after `DecodeConfig`, and
    JPEG from `image/jpeg`. The client core applies a frame to its
    framebuffer when `Next` delivers it, so the probe reads the same
    framebuffer a client draws from.
  - *Frames.* A graphical frame draws if its dirty rectangles are not
    empty. A frame counts from when `Next` delivers it applied, as a v0
    frame from when the harness applied it, before it answers `sync`.
    The reader then waits for the next tick of a 60 Hz screen
    (`-ack-hz`) before it calls `Next` again, which acknowledges a
    graphical frame, drawing or not, as the web client's `Display.run`
    acknowledges it after the next animation frame; it calls `Next` at
    once after a terminal's frame. `-ack-hz 0`, a diagnostic, also
    acknowledges a graphical frame at once. A v1 frame is the server's
    (see Surprises), so a scene's frame rate comes from its stamps, and
    the JSON keeps the server's frames as `server_frames`.
  - *Keys.* A desktop's key carries its physical key as a USB HID usage
    on a US layout, as the web client's carries `KeyboardEvent.code`,
    and native RDP sends it as a scancode; without it, native RDP would
    send a letter as a Unicode event, which no browser client causes.
  - *Terminals.* The SSH backend ignores keys, so the harness sends the
    bytes xterm.js sends for them as `term-input`. The client core
    passes a terminal's bytes through, and the harness writes them to
    `internal/terminal`, which stands in for xterm.js: a terminal frame
    draws if it carried bytes, and T2's end is the first frame after
    which a cell has the marker's background (SGR 48;5;201). This is the
    client-side decoding of a terminal; the client core has none.
  - *Decode time* is the wall time of each tile's decode, summed over
    the pool's goroutines, or the terminal emulator's time; the copy of
    decoded tiles into the framebuffer is not in it. v0's includes
    compositing, so the column compares v1's two series, not v1 with
    v0, and the table says so under it.
  - *G7* starts at the stream's upgrade and ends at the first frame
    that shows known content of the remote (`shows`): since the session
    core serves an empty snapshot of a blank terminal before the backend
    connected, and the SSH backend then resizes that blank terminal,
    which reaches the client as a clear screen, a frame that draws does
    not yet show the remote. The sign-in, a bcrypt check that a client
    makes once for many sessions, is recorded apart as `sign_in_ms` and
    noted on the row. v0's G7 includes its login, which v0 makes for
    every session.
- **Decisions:**
  - The terminal is 99 × 39 cells: what `stty size` reported in the
    shell of a v0 session, whose terminal guacd sizes from 1024 × 768
    pixels at 96 DPI. The same screens then cross both protocols.
  - The v1 bridge's RDP keeps occamyd's default `disable-gfx`, which the
    baseline's guacd did not have, rather than turning RDP's graphics
    pipeline back on: the bridge as shipped is what native RDP will be
    compared with within v1. Against the baseline, in which guacd 1.6
    turned the pipeline on and xrdp encoded with RemoteFX Progressive
    ([guacd-bridge](guacd-bridge.md)'s conformance notes), RDP through
    the v1 bridge therefore differs in one guacd parameter. (2026-11-01:
    the bridge as shipped stays the verdict's side, and it also runs
    with the pipeline on, [decision 2](#decisions-of-2026-11-01).)
  - The v1 series are not in `make bench`'s default series, so that the
    documented run of the v0 baseline keeps its duration; they are named
    with `-series`.
  - When a v1 frame counts, and when it is acknowledged. The review of
    #127 found that the first version counted a v1 frame from the 60 Hz
    tick that showed it, which added up to 16.7 ms to every v1 latency
    that v0, counted when its client applied the frame, did not pay, at
    one phase for a whole run. The design since: a v1 frame counts from
    when `Next` delivers it applied, like v0's from when the harness
    applied it; both clients' canvases then reach the screen at the
    display's next refresh, which guacamole-common-js, answering `sync`
    without an animation frame, and the web client pay alike, and which
    the 50 ms budget allows for. The tick paces only the
    acknowledgement of a graphical frame, as the web client's
    `Display.run` does, since that is what paces occamyd; a terminal's
    is acknowledged at once. These defaults, 60 Hz and the stamp at
    delivery, produce the gate's numbers, and the report records both
    (`ack_hz`, `frame_stamp`, and the table's header); `-ack-hz 0` is a
    diagnostic. The ticks keep one phase per process, and the probe's
    keys, 100 ms apart, are 6 periods, so where the cadence holds a
    frame back it adds one offset per run. `docs/benchmarks.md` gives
    the direction of the cadence's bias for each metric.
- **Tests:** `bench/v1_test.go` and `bench/bench_test.go`.
  - The decoders against `protocol/testdata/codecs.json` (lossless
    exactly, lossy at `min_psnr`, a rectangle other than the header's
    refused, every invalid vector refused).
  - The bench stack's v1 file loads, and has every connection the series
    name, with its protocol and its backend named, `v1-native` serving
    SSH, VNC and RDP, native RDP with `security: tls` and no certificate
    check, and the user the harness signs in as.
  - In process, through `session.Start` and `stream.Serve` with fake
    backends: the readiness check and five probe keys over a graphical
    session; a probe key whose colour follows 50 ms after a frame in
    another colour, or after a frame that changed pixels away from the
    swatch's centre while it already showed the colour, is marked only
    at its colour; a terminal session whose echo draws a frame and whose
    Return draws the marker's cells; the blank terminal served before
    the backend connected draws nothing; G7 over a terminal whose shell
    answers after 300 ms ends then, not at the blank terminal nor at the
    clear screen of resizing it after 100 ms; a screen shows the remote
    once a cell holds a character or a coloured background; at 60 Hz a
    graphical frame is acknowledged on a tick and counted when `Next`
    delivered it, not on one, and at 0 acknowledged at once; a
    terminal's frames are acknowledged at once at 60 Hz; an input to a
    network that takes nothing fails after its bound; a desktop's keys
    reach the backend with the HID usages that the web client's table
    gives the keys the scenarios press.
  - Against a fake API v1: a session whose welcome the client refuses
    is ended with DELETE, by the session that `client.Dial`'s
    `*client.RefusedError` names, and a handshake that hangs fails after
    its bound.
  - With sessions that never draw: G7, T1 and T2 fail after their
    bounds. `wait` takes only frames after its time, and a
    measurement's bytes, frames and decode time are what came after its
    start.
  - The table: a v1 scene's frame rate left out with its note, below
    the scene's rate as well, and its frames moved to `server_frames`
    by `run`; server CPU per series with guacd's apart for
    `v1-native`; the sign-in noted on a v1 G7; the decode note under a
    table with v1 rows; the refresh and when frames count, in the JSON
    and above a table with v1 rows. A chosen series that ran nothing is
    found; `-failfast` stops at the first error and the report keeps
    the plan's refresh; CI's bench job has a timeout of at most 30
    minutes, runs the bench with `-failfast`, and prints the stack's
    logs unless it succeeded.
  - `termBytes`' keys, and which tick follows a frame.
- **Surprises:**
  - A v1 frame is not a scene's frame. occamyd ends a frame whenever the
    viewer's window allows and the screen changed, and the harness
    acknowledges at once, so through the native VNC backend, which
    applies x11vnc's updates as they come, the harness counted several
    frames per frame of the scene, several times the scroll's and the
    motion's rates in the harness check. Through the guacd
    bridge, whose frames follow guacd's, it mostly stayed below the
    scene's rate, as v0 does, but in one check the scroll exceeded it
    there too. The delivered frame rate, the share of the scene,
    and bytes and CPU per frame, the measures of G3 to G5, therefore do
    not count scene frames in v1. A first version of the table noted
    only rows whose frames came faster than the scene; review found a
    native row at 59 fps of a 62.5 fps scroll that read as meeting its
    target, so the table left every v1 scene's frame rate out, until the
    scene's stamps gave a count that holds in both protocols.
  - The session core serves a blank 80 × 24 terminal before the backend
    has connected: its first frame carries an empty snapshot. The first
    version of the harness counted any snapshot as drawing, so G7 over
    SSH ended at that frame, about 1 ms after the upgrade, whatever the
    shell took, which in CI read as a false 9x over v0. Review found
    it; a snapshot now draws only if it carries bytes. The review of
    the fix found that the SSH backend then resizes the blank terminal
    to 99 × 39, which reaches the client as a seven-byte clear screen
    and ended G7 there just the same; a terminal then showed the remote
    from its first cell that was not blank, and since the decisions of
    2026-11-01 from its prompt.
- **Follow-ups:**
  - ~~The scene stamps each frame's number where the harness can read
    it, and the harness counts the distinct scene frames it was shown,
    for both protocols.~~ Done
    ([the harness for the v1 run](#implementation-notes-the-harness-for-the-v1-run)).
    The baselines of G3 to G5 are measured again with it
    ([decision 3](#decisions-of-2026-11-01)).
  - ~~End G7 on known content in both series, rather than on the first
    frame that drew.~~ Done
    ([the harness for the v1 run](#implementation-notes-the-harness-for-the-v1-run)),
    with the prompt that the shell shows anyway as its known line. G7's
    baselines are measured again.
  - ~~Owner question: whether the v1.0 gate of 10x over guacd is a flat
    10x or each target as defined.~~ Answered on 2026-10-31: each
    target as defined, a miss of 10x only where a documented bound
    outside Occamy makes it impossible ([Targets](#targets)).
  - The full v1 run, with `-runs 3` under N0 to N2 on an idle host,
    once Refinement, Tile cache, Encode cache and workers, and native
    RDP are in occamyd; then `docs/benchmarks.md` gets the v1 results
    and a table per scenario of the baseline, the v1 bridge, v1 native,
    the target and whether it is met.
  - ~~**`xrdp-chansrv` exits when a native RDP session ends,** which adds
    10 s of black screen to every later RDP connection to that X session,
    so that G7 over RDP measures the wait in rows that depend on the
    order of the series.~~ Done for the bench: the bench desktop has the
    demo's `reconnectwm.sh`
    ([the harness's notes](#implementation-notes-the-harness-for-the-v1-run)).
    Why a native session's end sets it off is still to find, since a
    server without the script waits and loses its clipboard.
  - ~~**G5 v1 p95 phase lock.** The probe's keys, every 100 ms, were
    exactly 6 ticks of 60 Hz, so one run's p95 did not cover the spread
    of phases, and could understate it by up to about 7.5 ms, in
    native's favour.~~ Done: each key is jittered by up to one tick
    either way, at 10 keys a second on average, for every series
    ([the harness for the v1 run](#implementation-notes-the-harness-for-the-v1-run)),
    and the baselines of G2 and G5, which press them, are measured
    again.
  - ~~Native RDP asks for 32 bits per pixel, and the bridge leaves
    guacd's default colour depth, so xrdp may keep an X session for
    each.~~ Done: every path asks for 32
    ([decision 4](#decisions-of-2026-11-01)).
  - ~~Native RDP speaks RDP's graphics pipeline by default, and the
    bridge keeps `disable-gfx`, so `rdp-native` and `rdp-guacd` differ
    there.~~ Done: both run both ways, and each row says which it
    measures ([decision 2](#decisions-of-2026-11-01)).
  - The client core's default decoders (a follow-up of
    [client-core](client-core.md)): once `client` has them, the harness
    uses them instead of its own.
