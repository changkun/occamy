---
title: Protocol benchmark
status: validated
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
updated: 2026-10-29
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
measured, and how the targets are judged. The harness measures v0 first.
Protocol v1 gets a driver in the same harness once
[client-core](client-core.md) exists.

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
| Delivered frame rate | Complete frames received per second (`sync` in v0, `frame-end` in v1), and for scenes animated at a known rate, the share of scene frames that reached the client. |
| Time to first frame | From dialing the WebSocket until the first complete frame. |
| Completion time | For bounded output, from the command until its end marker is visible. |

### The latency probe

- **Desktop:** the scene program (below) fills a 32×32 swatch at a fixed
  position with a colour derived from each key it receives. The probe
  completes when the swatch's centre pixel in the harness's decoded
  framebuffer has that colour.
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
from guacd's own. A v1 series is added later.

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
and enforces the bandwidth with a token bucket. It emulates latency and
throughput, not loss or TCP congestion behaviour.

## Targets

For each scenario, the target is 10x better than the v0-through-occamyd
baseline on its primary metric, under the same network condition, at the
p95 for latencies.

The baseline run may show that a metric is bound by something Occamy does
not control: the RTT, the remote server, or the scene's own frame rate,
for example. A delivered frame rate cannot exceed the scene's. Such a
metric's target becomes that bound plus a stated margin. The evidence goes
next to it in `docs/benchmarks.md`, so that no target is quietly dropped.

The 50 ms budget applies separately: `L` p95 for G2 and T1 under N1, and
for G5 under N1, must be at most 50 ms with v1.

## Components

### Harness (`bench/`)

A Go command, `go run ./bench`, with flags for the scenarios, network
conditions and series, and the endpoint. It writes a JSON result and a
Markdown table.

- **Drivers:** v0 through occamyd (log in, dial `/api/v1/connect`); v0
  direct to guacd (the `internal/guacd` handshake); v1 later through the
  `client` package. Each driver answers `sync` the way a browser does,
  announces the image types a browser announces (PNG, JPEG, WebP), and
  keeps a decoded framebuffer.
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
job do not use it. `make bench` starts the stack with the override and
runs the harness. The demo images stay as they are.

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

## Testing Strategy

- Unit tests for the interpreter (each operation against expected pixels),
  the proxy (added delay and rate within tolerance), and the probe
  matcher.
- A CI smoke job runs every scenario briefly under N0 to prove that the
  harness and the bench desktop work. CI never gates on absolute numbers
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

## Implementation notes

- **Status:** the Terminal task shipped in PR #76 on 2026-10-29, and the
  Desktop VNC and Desktop RDP tasks in the PR after it. The desktop
  baseline numbers and the targets of the Baseline task remain.
- **Desktop decisions:**
  - The bench desktop replaces the `vnc` and `rdp` services' images
    rather than adding a service, so the harness's hosts, credentials
    and per-service CPU stay as they are.
  - The probe swatch is reset to grey by every scene key, and the
    harness's readiness check alternates two letters of different
    colours, because an update that paints the colour already shown
    changes no pixels and draws nothing.
  - Every wait in a desktop scenario is bounded. At N2 a VNC session
    could keep drawing long enough that waiting for quiet hung until the
    scenario's 10-minute limit.
  - The interpreter applies all sixteen channel masks; guacd uses
    destination-out and destination-atop on buffers at connect.
- **Done ahead of plan:** the interpreter, planned for Desktop VNC,
  because SSH needed it; and from the Baseline task, the CI smoke job
  and `docs/benchmarks.md` with the terminal numbers.
- **Decisions:**
  - Repetition with medians, because single runs picked up disturbances
    on the host: a compile during one run more than doubled a p95 latency.
  - Bytes are counted at the proxy, so they include WebSocket framing.
    The spec said WebSocket payload; the difference is a few bytes per
    message, and the proxy sees both series the same way.
  - Sessions are 2 s apart, so that a session with the same claims does
    not join the one before it while it ends (003, defect 6).
- **Surprises:**
  - guacd's terminal draws on visible layer 1 at (7, 7), not on the
    default layer, so the interpreter needed layer compositing for SSH.
  - The podman VM counts time the host takes its CPUs away as CPU time,
    so a disturbed run shows more CPU as well as more time.
  - x11vnc with its defaults uses about 8% of a CPU with no client
    connected, and takes 27 ms at the median and 125 ms at p95 from a
    key to its update. The bench desktop tunes it to 2.8 ms and 3.4 ms
    (`docker/bench/desktop/start.sh`), at about one CPU whether a client
    is connected or not.
- **Findings for the targets:** occamyd costs nothing measurable over
  guacd. T1 is near its floor (3 ms p95 at N0), so its target is the
  bound plus a margin. guacd coalesces T2's 575 KiB into 82–115 KiB of
  screens, so v1 needs terminal state on the server. G7's bound needs
  the SSH handshake measured alone.
