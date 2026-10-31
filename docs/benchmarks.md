# Benchmarks

Protocol v1 is designed against measurements of what Occamy costs today,
and it is proven with the same harness. Its two targets are a 10x
improvement over the Guacamole baseline on each scenario's primary metric,
and input-to-update latency under 50 ms for desktop interactions.
[specs/008-native-architecture/benchmark.md](../specs/008-native-architecture/benchmark.md)
defines the metrics, the scenarios and how the targets are judged. This
page covers how to run the harness and what it measured.

## Running

```sh
make bench
```

This builds occamyd's image from the working tree, then builds and starts
the demo stack with `docker/docker-compose.bench.yaml` on top, which
publishes guacd on `127.0.0.1:4822`. Then it runs every scenario in both
v0 series under every network condition, which takes about 50 minutes: 12
for the terminal and 38 for the desktop. Results go to
`bench-results.json`, and a Markdown table goes to standard output. The
v1 series run on the same stack when `-series` names them; see
[The v1 series](#the-v1-series).

On a host shared with another stack, `COMPOSE_PROJECT_NAME`,
`OCCAMY_PORT`, `OCCAMY_V1_PORT` and `GUACD_PORT` start this one under
its own project name and ports, and `make bench` points the harness at
them.

The harness and the stack share the host, so anything else that uses the
CPU shows up in the numbers. A compile during a run more than doubled
some latencies. Keep the host idle, and use `-runs 3`: the table then
shows the median of each number over three runs, so one disturbed run
does not move it. The JSON keeps every run.

To pass flags to the harness, use `BENCH_FLAGS`:

```sh
make bench BENCH_FLAGS="-scenarios T1 -net N1 -series occamy"
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-scenarios` | `all` | Comma-separated scenario IDs |
| `-net` | `N0,N1,N2` | Network conditions |
| `-series` | `occamy,guacd` | `occamy` goes through occamyd; `guacd` connects to guacd directly; `v1-native` and `v1-guacd` speak protocol v1 to occamy-v1, with native backends or through guacd |
| `-runs` | `1` | How many times to run everything; the table shows medians |
| `-quick` | off | A tenth of the samples and a sixth of the durations, to check the setup |
| `-ack-hz` | `60` | v1: the screen refresh on whose ticks the harness shows and acknowledges frames, as a browser does; `0` acknowledges each at once ([The v1 series](#the-v1-series)) |
| `-engine` | `podman` | Container engine for CPU time, or `none` |
| `-project` | `docker` | The stack's compose project, whose containers CPU time is read from |
| `-addr`, `-v1`, `-guacd` | `localhost:5636`, `localhost:5637`, `localhost:4822` | occamyd for v0, occamy-v1 for v1, and guacd for the direct series |
| `-out` | `bench-results.json` | JSON output |

Server CPU needs podman: its container statistics report cumulative CPU
time, and docker's do not. With docker, pass `-engine none`. CI runs a
quick smoke variant on every push and does not compare numbers, because
shared runners are too noisy.

## What is measured

The harness is a client. It decodes every image into a framebuffer,
answers each `sync` once the frame is applied, and sends a `nop` every
5 seconds, as the web client does. Each scenario runs on a fresh session.

- **Series.** `occamy` logs in to occamyd and opens its WebSocket, which
  is the shipped path. `guacd` performs the Guacamole handshake with guacd
  directly, with the parameters occamyd would send, so the difference
  between the two is occamyd's cost.
- **Networks.** A proxy inside the harness carries every connection. It
  adds half the RTT in each direction and serializes at the bandwidth,
  with a queue of twice the bandwidth-delay product. It does not emulate
  loss.

  | ID | RTT | Bandwidth |
  |----|-----|-----------|
  | N0 | none added | unlimited |
  | N1 | 20 ms | 100 Mbit/s |
  | N2 | 50 ms | 20 Mbit/s |

- **Bytes** are the TCP payload through the proxy. In the `occamy` series
  and the v1 series they include the WebSocket frame headers, which the
  `guacd` series does not have.
- **Server CPU** is the CPU time of the occamyd and guacd containers
  during the measured part of a scenario. guacd forks a process per
  connection, and the container's CPU time includes it.
- **Decode** is the harness's time spent applying drawing and compositing
  frames. A browser decodes natively, so only ratios between series mean
  anything.

The scenarios:

| ID | Protocol | Scenario | Primary metric |
|----|----------|----------|----------------|
| T1 | SSH | Type 200 characters, 10 per second, into an idle shell; latency is from each key to the first frame that draws | latency |
| T2 | SSH | `seq 1 100000`, then a magenta end marker; completion is from Return until the marker is visible | bytes, completion, server CPU |
| T3 | SSH | `top -d 0.5` for 30 s | bytes per second |
| G1 | VNC, RDP | A still desktop for 30 s | bytes per second, server CPU |
| G2 | VNC, RDP | Probe keys, 10 per second, on a still desktop | latency |
| G3 | VNC, RDP | A page of text scrolling one line every 16 ms, for 10 s | bytes, server CPU, frame rate |
| G4 | VNC, RDP | A 400×300 textured window dragged at 60 Hz, for 10 s | bytes, server CPU, frame rate |
| G5 | VNC, RDP | Full-screen motion at 30 fps, with probe keys during it | frame rate, bytes, latency under load |
| G6 | VNC, RDP | One photographic full-screen image; completion is from the key until the last update | bytes, completion |
| G7 | all | Connect | time to the first frame |

### The bench desktop

`make bench` replaces the demo's VNC and RDP servers with the bench
desktop (`docker/bench/desktop`). The service names, hosts and
credentials stay the same. In both, the session is the scene program
(`bench/scene`), with no window manager. It covers the screen with one
window, draws content generated from fixed seeds, and switches scenes on
function keys: F1 is idle, F2 the probe, F3 the scroll, F4 the drag, F5
the motion, F6 the photo, and Escape goes back to idle.

Every letter paints a 32×32 swatch near the top left corner in a colour
of its own (`bench/internal/probe`). Latency is from writing the key
until the harness's framebuffer shows that colour at the swatch's
centre, within a tolerance for lossy encoding and 16-bit colour. Every
scene starts with a grey swatch, so the first probe key always changes
it.

The VNC server is x11vnc on Xvfb. With its defaults, x11vnc alone takes
27 ms at the median and 125 ms at p95 from a key to the update that
shows it, which would hide everything after it. In the bench desktop it
runs threaded, polls every millisecond, and skips its naps and its
detection of window moves and scrolls, which brings it to 2.9 ms and
3.5 ms. The price is about one of the VM's CPUs, with or without a
client, since it polls the screen every millisecond. `bench/vncprobe`
measures this: a VNC client that runs inside the container, so the
numbers include no network and no guacd. It presses keys in any scene
and counts the updates a second, and its doc comment shows how to run
it. The RDP server is xrdp with xorgxrdp, as configured by Debian.

## The v1 series

Two series measure protocol v1 with the same scenarios, metrics, network
conditions and bench desktop as the baseline:

```sh
make bench BENCH_FLAGS="-series v1-native,v1-guacd -runs 3"
```

**There are no v1 results yet.** v1's full run waits for the rest of
the [graphics pipeline](../specs/008-native-architecture/graphics-pipeline.md)
(refinement, the tile cache, and the encode cache and its workers) and
for [native RDP](../specs/008-native-architecture/native-rdp-backend.md),
which occamyd does not register yet. Numbers taken before then would not
be v1's verdict, so this page publishes none; the harness and the CI
smoke run only keep the v1 path working until then.

| Series | Backend | Protocols | Connections in `docker/bench/occamy-v1.yaml` |
|---|---|---|---|
| `v1-native` | occamyd's own | SSH, VNC | `ssh`, `vnc-native` |
| `v1-guacd` | the guacd bridge | VNC, RDP | `vnc-guacd`, `rdp-guacd` |

Neither series runs every scenario: native RDP is not registered, and
SSH has no guacd backend in v1. A v1 session runs as follows:

- **Client.** The harness drives the Go client core (`client/`) through
  the stack's `occamy-v1` service, whose file
  `docker-compose.bench.yaml` replaces with `docker/bench/occamy-v1.yaml`.
  It signs in at `POST /api/v1/auth/token`, opens
  `GET /api/v1/connections/{id}/stream`, and afterwards ends the session
  with `DELETE /api/v1/sessions/{id}`, since an owner who leaves keeps
  the session for the 60 s owner grace. A session whose welcome the
  client refuses is ended the same way.
- **Codecs.** It offers what occamy's web client offers when the browser
  decodes them all: deflate, WebP, JPEG and raw, in that order. The
  client core decodes tiles in parallel as they arrive, with deflate
  from `internal/codec`, WebP from `github.com/gen2brain/vpx/webp` and
  JPEG from `image/jpeg` plugged in, and applies a frame to its
  framebuffer when `Next` delivers it.
- **Frames.** A frame draws if it changed a rectangle. **A v1 frame is
  the server's, not the scene's:** occamyd ends a frame whenever its
  window allows and the screen has changed, and through the native VNC
  backend it ends several for each frame of the scene, as x11vnc's
  updates come in: in a check of the harness, several times the scene's
  rate for the scroll and for the motion, and in another, fewer. Frames
  a second, the share of the scene, and bytes and CPU per frame
  therefore do not count the scene's frames in v1, and do not compare
  with v0, until the scene stamps each frame's number for the harness to
  count. Until then, the table leaves a v1 scene's frame rate out, with
  the note "server frames, not comparable until the scene frame stamp
  lands", and the JSON keeps those frames as `server_frames`.
- **Acknowledgement.** occamy's web client shows a frame at the
  browser's next animation frame and acknowledges it then, by asking
  for the next event (`client/occamy-web/src/protocol/display.ts`), and
  occamyd paces its frames by those acknowledgements, with two frames
  in flight. The harness does the same on the ticks of a 60 Hz screen,
  `-ack-hz`, for every frame, drawing or not: a frame counts from the
  tick that shows it, and the reader asks for the next event after it.
  `-ack-hz 0` acknowledges each frame as soon as the client core
  applied it. How the cadence moves each metric:
  - *Latency (T1, G2, G5), completion (T2, G6) and G7* end on a tick,
    so 60 Hz adds 0 to 16.7 ms, 8.3 on average, to what `-ack-hz 0`
    measures, which is earlier than any browser could show the frame.
    The v0 series answer each `sync` at once, as the baseline did, so
    against the baseline the default counts that tick against v1;
    `-ack-hz 0` compares like with like.
  - *Frame rates* reach at most 60 a second at 60 Hz, as in a browser
    on a 60 Hz screen; at once, they count every frame the server can
    end.
  - *Bytes, server CPU and decode time* in moving scenes (G3 to G5)
    should be lower at 60 Hz than at once: when acknowledgements come
    slower, occamyd coalesces more of the scene into each frame. Acking
    at once models a client faster than a browser.
- **The probe** is the baseline's: a key's latency ends when the client
  core's framebuffer, after the frame is applied, shows the key's colour
  at the swatch's centre.
- **Terminals.** The SSH backend takes the bytes a terminal emulator
  sends, not keys, so a key goes as the bytes xterm.js sends for it. The
  client core passes a terminal's bytes through untouched, and the
  harness writes them to occamyd's own terminal emulator
  (`internal/terminal`), as a browser writes them to xterm.js. A
  terminal frame draws if it carried bytes, and T2's end marker is a
  cell with the magenta background. The terminal is 99 × 39 cells, the
  size of guacd's at 1024 × 768 pixels and 96 DPI, so that the shell
  prints the same screens in both protocols.
- **Decode** is the wall time of each tile's decode, summed over the
  client core's decoding goroutines, which run in parallel, or the
  terminal emulator's time; copying decoded tiles into the framebuffer
  is not in it. The v0 series' includes compositing, so the column
  compares v1's two series, not v1 with v0, and a table with v1 rows
  says so under it.
- **Server CPU** is the `occamy-v1` container's, and guacd's for
  `v1-guacd`. guacd serves nothing of `v1-native`, so its CPU there is
  left out and given in a note.
- **G7** starts at the stream's upgrade and ends at the first frame that
  shows the remote. For a terminal, that is the first frame with bytes:
  the session core serves a blank terminal before the backend has
  connected, whose empty snapshot shows nothing. For a desktop, it is
  the first frame that drew, as in v0, which may be the backend sizing
  the screen before its first image; see the benchmark spec's
  follow-ups. The sign-in before the upgrade checks a bcrypt hash and is
  done once for many sessions, so G7 leaves it out: the JSON records it
  as `sign_in_ms`, and the table notes it on the row. The v0 series' G7
  includes its login, which v0 needs for every session.
- **Bounds.** A sample that the stack does not answer fails rather than
  holding the run: 30 s to sign in and open the stream and, in G7, for
  the first frame; 10 s for a key's update and for an input to be
  queued; 30 s for T2's end marker. CI's bench job stops at 30 minutes,
  and the harness exits non-zero when a chosen series ran no scenario.
- **RDP through guacd** differs from the baseline in one parameter:
  occamyd gives guacd `disable-gfx`, because xrdp's colours shifted
  through RDP's graphics pipeline. The baseline did not set it, and
  guacd 1.6 then turns the pipeline on, through which xrdp encodes with
  RemoteFX Progressive
  ([guacd-bridge](../specs/008-native-architecture/guacd-bridge.md)'s
  conformance notes).

CI runs both v1 series quickly under N1, after the v0 smoke run, on the
same stack.

## Terminal baseline

Measured on 2026-10-29 with `make bench BENCH_FLAGS="-runs 3"`, on an
Apple M5 Pro (18 cores) with macOS 26.6 and podman 6.0.2, whose VM has 8
CPUs and 3.7 GiB. guacd is the `guacamole/guacd:1.6.0` image and occamyd
is built from master. Every number is the median of three runs, and
[terminal-baseline.json](./benchmarks/terminal-baseline.json) holds each
run.

**T1, typing echo:** latency in ms, p50 / p95 / p99.

| Network | occamy | guacd direct |
|---|---|---|
| N0 | 2.0 / 3.1 / 4.2 | 1.8 / 3.2 / 6.3 |
| N1 | 25.5 / 27.8 / 33.9 | 25.7 / 27.2 / 27.7 |
| N2 | 55.0 / 57.2 / 65.2 | 55.2 / 59.1 / 64.9 |

**T2, `seq 1 100000`:** the command prints 575 KiB.

| Network | Series | Completion ms | Down KiB | Frames/s | occamy + guacd CPU ms | ssh CPU ms |
|---|---|---|---|---|---|---|
| N0 | occamy | 897 | 86 | 23 | 881 | 34 |
| N0 | guacd direct | 946 | 104 | 28 | 922 | 28 |
| N1 | occamy | 1261 | 115 | 21 | 1188 | 78 |
| N1 | guacd direct | 966 | 104 | 20 | 904 | 32 |
| N2 | occamy | 943 | 82 | 20 | 865 | 32 |
| N2 | guacd direct | 958 | 84 | 20 | 873 | 31 |

**T3, `top -d 0.5`:**

| Network | Series | Down KiB/s | Up KiB/s | Frames/s | occamy + guacd CPU ms/s |
|---|---|---|---|---|---|
| N0 | occamy | 10.9 | 0.06 | 2.0 | 8.9 |
| N0 | guacd direct | 10.7 | 0.05 | 2.0 | 7.5 |
| N1 | occamy | 14.3 | 0.06 | 2.0 | 16.6 |
| N1 | guacd direct | 12.4 | 0.05 | 2.0 | 12.2 |
| N2 | occamy | 11.0 | 0.06 | 2.0 | 8.2 |
| N2 | guacd direct | 12.7 | 0.05 | 2.0 | 10.0 |

**G7, connecting over SSH:** time to the first frame in ms, p50 / p95.

| Network | occamy | guacd direct |
|---|---|---|
| N0 | 115 / 133 | 114 / 128 |
| N1 | 193 / 291 | 162 / 209 |
| N2 | 227 / 285 | 224 / 265 |

### Findings

- **occamyd costs nothing measurable.** The two series agree within the
  spread between runs in every scenario. What a terminal session costs
  is guacd's and the protocol's.
- **Typing is already near its floor.** At N0 an echo takes 2 ms at the
  median and 3 ms at p95, including the remote shell. With a network,
  latency is the RTT plus about 5 ms at the median and 7–9 ms at p95.
  10x faster at N0 is 0.3 ms, which the remote shell's own echo may not
  allow; see [Targets](#targets).
- **guacd coalesces bulk output into screen frames.** 575 KiB of text
  reaches the client as 82–115 KiB in about 20 frames per second: guacd
  keeps the terminal's state and draws only the screens it has time
  for. A protocol that streamed the shell's raw output would send 5–7x
  more. Protocol v1 has to keep terminal state on the server and send
  the screen, as mosh does, to beat this.
- **Bulk output is bound by guacd's CPU.** T2 takes about a second under
  every network condition, and occamyd and guacd use as much CPU time as
  the command takes to complete: one core is busy throughout. The remote
  shell needs only about 30 ms. The time goes into guacd's terminal
  emulator and into rendering the text as images.
- **`top` costs about 5.5 KiB per refresh**, at two refreshes a second,
  because guacd sends the changed parts of the screen as images. Sending
  changed characters instead is where v1 can save.
- **Connecting takes about 115 ms before any network delay**, and 2–4
  RTTs more with one. The SSH handshake and guacd's per-connection
  process are inside this, so its bound needs a measurement of the SSH
  handshake alone.

The CPU numbers come from the podman VM, which counts time that the
host takes its CPUs away as CPU time. A disturbed run therefore shows
more CPU as well as more time, which is one more reason for medians.

## Desktop baseline

Measured on 2026-10-30 with `bench -runs 3 -scenarios G1,G2,G3,G4,G5,G6`,
which runs G7 for VNC and RDP as well, on the same host as the terminal
baseline. occamyd is v0.6.0 and guacd the `guacamole/guacd:1.6.0` image.
The run took 1 h 52 min. The host's load average over one minute, logged
every minute, had a median of 5 and passed 10 in 20 of 113 minutes, at
most 34: some of the peaks came from G7's RDP connects, others from other
work on the host. Every number is the median of three runs, which
absorbs the one visibly disturbed run: G5 over RDP at N1 in the second
run, with a p95 of 354 ms against 110 ms in the other two.
[desktop-baseline.json](./benchmarks/desktop-baseline.json) holds each
run.

**G1, a still desktop:** over 30 s a VNC session receives 150–170 bytes
and sends 60–100, and an RDP session 18–32 and 36–72, under every
network condition. occamyd and guacd use 1–4 ms of CPU a second,
which is the spread between runs. x11vnc uses about one CPU throughout,
as described above, and xrdp nothing measurable.

**G2, key latency on a still desktop:** p50 / p95 / p99 in ms.

| Protocol | Network | occamy | guacd direct |
|---|---|---|---|
| VNC | N0 | 14.6 / 15.6 / 16.7 | 14.5 / 15.5 / 16.2 |
| VNC | N1 | 37.7 / 39.4 / 39.5 | 37.3 / 39.1 / 39.5 |
| VNC | N2 | 67.7 / 69.4 / 69.8 | 67.6 / 69.3 / 69.6 |
| RDP | N0 | 17.1 / 17.6 / 18.9 | 16.9 / 17.8 / 19.0 |
| RDP | N1 | 40.6 / 41.5 / 43.2 | 40.0 / 41.2 / 43.0 |
| RDP | N2 | 70.6 / 71.7 / 73.8 | 70.2 / 71.2 / 71.5 |

**G3, scrolling text:** the scene scrolls at 62.5 frames a second.

| Protocol | Network | Series | Down MiB/s | Frames/s | Share of scene | occamy + guacd CPU ms/s | x11vnc or xrdp CPU ms/s | Decode ms/s |
|---|---|---|---|---|---|---|---|---|
| VNC | N0 | occamy | 2.88 | 41.4 | 66% | 1590 | 1745 | 294 |
| VNC | N0 | guacd direct | 3.00 | 42.3 | 68% | 1548 | 1750 | 301 |
| VNC | N1 | occamy | 2.98 | 41.9 | 67% | 1582 | 1724 | 292 |
| VNC | N1 | guacd direct | 2.84 | 41.1 | 66% | 1552 | 1753 | 296 |
| VNC | N2 | occamy | 1.73 | 25.5 | 41% | 1139 | 1759 | 178 |
| VNC | N2 | guacd direct | 1.70 | 25.0 | 40% | 1083 | 1757 | 179 |
| RDP | N0 | occamy | 9.89 | 19.6 | 31% | 2065 | 301 | 823 |
| RDP | N0 | guacd direct | 9.95 | 19.6 | 31% | 2020 | 312 | 815 |
| RDP | N1 | occamy | 8.68 | 17.8 | 28% | 1900 | 301 | 726 |
| RDP | N1 | guacd direct | 8.69 | 17.9 | 29% | 1820 | 300 | 714 |
| RDP | N2 | occamy | 2.38 | 1.5 | 2% | 937 | 285 | 46 |
| RDP | N2 | guacd direct | 2.38 | 1.4 | 2% | 844 | 294 | 46 |

**G4, a dragged window:** the scene moves the window 60 times a second.

| Protocol | Network | Series | Down MiB/s | Frames/s | Share of scene | occamy + guacd CPU ms/s | x11vnc or xrdp CPU ms/s | Decode ms/s |
|---|---|---|---|---|---|---|---|---|
| VNC | N0 | occamy | 2.29 | 59.7 | 99% | 660 | 1630 | 225 |
| VNC | N0 | guacd direct | 2.28 | 59.5 | 99% | 638 | 1625 | 223 |
| VNC | N1 | occamy | 2.29 | 59.7 | 99% | 670 | 1628 | 225 |
| VNC | N1 | guacd direct | 2.25 | 58.7 | 98% | 641 | 1620 | 224 |
| VNC | N2 | occamy | 1.86 | 48.6 | 81% | 577 | 1641 | 184 |
| VNC | N2 | guacd direct | 1.95 | 50.8 | 85% | 578 | 1640 | 194 |
| RDP | N0 | occamy | 0.94 | 24.2 | 40% | 342 | 76 | 92 |
| RDP | N0 | guacd direct | 0.95 | 24.6 | 41% | 329 | 76 | 94 |
| RDP | N1 | occamy | 0.95 | 24.5 | 41% | 354 | 83 | 94 |
| RDP | N1 | guacd direct | 0.95 | 24.6 | 41% | 330 | 76 | 93 |
| RDP | N2 | occamy | 0.94 | 24.1 | 40% | 340 | 76 | 93 |
| RDP | N2 | guacd direct | 0.95 | 24.4 | 41% | 332 | 77 | 93 |

**G5, full-screen motion:** the scene draws 30 frames a second, and the
latency is that of probe keys during it.

| Protocol | Network | Series | Down MiB/s | Frames/s | Share of scene | occamy + guacd CPU ms/s | x11vnc or xrdp CPU ms/s | Decode ms/s | Latency p50 / p95 / p99 ms |
|---|---|---|---|---|---|---|---|---|---|
| VNC | N0 | occamy | 1.43 | 29.8 | 99% | 1212 | 1769 | 307 | 66.3 / 84.4 / 95.8 |
| VNC | N0 | guacd direct | 1.42 | 29.8 | 99% | 1203 | 1769 | 309 | 65.6 / 89.5 / 116.5 |
| VNC | N1 | occamy | 1.42 | 29.8 | 99% | 1221 | 1776 | 309 | 66.7 / 112.7 / 118.5 |
| VNC | N1 | guacd direct | 1.42 | 29.8 | 99% | 1242 | 1793 | 327 | 64.3 / 124.7 / 145.8 |
| VNC | N2 | occamy | 1.34 | 29.6 | 99% | 1167 | 1762 | 304 | 128.7 / 154.1 / 176.5 |
| VNC | N2 | guacd direct | 1.34 | 29.4 | 98% | 1184 | 1777 | 321 | 129.8 / 161.2 / 177.7 |
| RDP | N0 | occamy | 1.60 | 24.8 | 83% | 1357 | 232 | 332 | 68.4 / 88.1 / 97.3 |
| RDP | N0 | guacd direct | 1.60 | 24.6 | 82% | 1386 | 243 | 343 | 70.5 / 90.2 / 103.2 |
| RDP | N1 | occamy | 1.61 | 24.8 | 83% | 1368 | 235 | 333 | 84.2 / 109.8 / 124.2 |
| RDP | N1 | guacd direct | 1.60 | 24.8 | 83% | 1352 | 238 | 336 | 84.1 / 110.6 / 120.7 |
| RDP | N2 | occamy | 1.36 | 24.8 | 83% | 1276 | 234 | 325 | 134.7 / 167.2 / 174.3 |
| RDP | N2 | guacd direct | 1.39 | 24.7 | 82% | 1265 | 236 | 326 | 134.5 / 168.0 / 176.8 |

**G6, a photograph:** completion is from the key until the last update.

| Protocol | Network | Series | Completion ms | Down KiB | occamy + guacd CPU ms | x11vnc or xrdp CPU ms |
|---|---|---|---|---|---|---|
| VNC | N0 | occamy | 173 | 2236 | 264 | 2325 |
| VNC | N0 | guacd direct | 147 | 1441 | 177 | 2299 |
| VNC | N1 | occamy | 302 | 2236 | 281 | 2459 |
| VNC | N1 | guacd direct | 298 | 2235 | 255 | 2441 |
| VNC | N2 | occamy | 707 | 1498 | 189 | 2869 |
| VNC | N2 | guacd direct | 720 | 1515 | 191 | 2890 |
| RDP | N0 | occamy | 131 | 1400 | 308 | 19 |
| RDP | N0 | guacd direct | 131 | 1399 | 196 | 20 |
| RDP | N1 | occamy | 173 | 1380 | 198 | 29 |
| RDP | N1 | guacd direct | 147 | 447 | 285 | 22 |
| RDP | N2 | occamy | 271 | 423 | 1167 | 25 |
| RDP | N2 | guacd direct | 281 | 431 | 175 | 22 |

**G7, connecting:** time to the first frame in ms, p50 / p95.

| Protocol | Network | occamy | guacd direct |
|---|---|---|---|
| VNC | N0 | 110 / 113 | 110 / 114 |
| VNC | N1 | 156 / 162 | 154 / 158 |
| VNC | N2 | 216 / 220 | 215 / 217 |
| RDP | N0 | 2190 / 2198 | 2188 / 2196 |
| RDP | N1 | 2237 / 2242 | 2235 / 2238 |
| RDP | N2 | 2297 / 2305 | 2296 / 2303 |

### The remote server

`bench/vncprobe` measured x11vnc on its own in each scene after the
run, 200 keys each, with the host idle:

| Scene | p50 ms | p95 ms | p99 ms | Updates/s |
|---|---|---|---|---|
| F2, still | 2.9 | 3.5 | 4.1 | 10.0 |
| F3, scroll at 62.5 fps | 12.2 | 42.3 | 51.9 | 61.2 |
| F4, drag at 60 fps | 8.6 | 13.5 | 14.9 | 63.2 |
| F5, motion at 30 fps | 4.3 | 5.6 | 22.6 | 40.3 |

The probe asks for raw pixels over loopback. guacd negotiates compressed
encodings, which cost x11vnc more, so these are lower bounds on its
share. The update rates include the probe keys' own updates where the
scene does not redraw the swatch anyway: F2's are only those, and F5's
are 30 a second from the scene and 10 from the keys. There is no RDP
counterpart yet.

### Findings

- **occamyd costs nothing measurable** on a desktop either. The two
  series agree within the spread between runs in every scenario.
- **On a still desktop a key takes the RTT plus 16–19 ms over VNC** at
  p95, of which x11vnc's own reaction is 3.5 ms: guacd and the protocol
  take 12 ms at N0 and 16 ms with a network. Over RDP it is the RTT plus
  18–22 ms, with xrdp's share unmeasured. All of G2 is within the 50 ms
  budget at N1.
- **Under load a key waits behind the frames.** In the motion scene
  x11vnc shows a key within 5.6 ms, but the harness sees it after 84 ms
  at N0 and 113 ms at N1 over VNC, and 88 and 110 ms over RDP: twice the
  50 ms budget at N1. A Guacamole connection is one ordered stream, so a
  key's update waits for the frames queued before it. This is the main
  thing protocol v1 has to fix.
- **The scroll loses a third of its frames on the way.** x11vnc sends
  the probe 61 of the scene's 62.5 updates a second, and the harness
  receives 41 through guacd, which uses 1.6 CPUs meanwhile. The probe
  asks for raw pixels, which cost x11vnc less than guacd's compressed
  encodings, so this does not yet show which of the two drops them. The
  drag and the motion reach 99% of the scene over VNC at N0 and N1.
- **RDP stops at 24–25 frames a second.** G4 delivers that under every
  network condition with a third of a CPU and under 1 MiB/s, G5 reaches
  the same 24.8, and G3 delivers 20. Whether xrdp or guacd's RDP client sets this ceiling
  needs an RDP counterpart of `bench/vncprobe` to tell.
- **RDP's scroll is the most expensive scene:** about 516 KiB a frame
  against 71 KiB over VNC, 10 MiB/s at N0. At N2 it fills the 20 Mbit/s
  link and delivers 1.5 frames a second.
- **guacd chooses the photograph's encoding by timing, not content.**
  guacd 1.6 encodes a region as JPEG or WebP only if it changed recently
  enough (`src/libguac/display-worker.c`), so the same photograph
  arrived as 2.2 MiB of PNG in 10 of 36 runs, and as 0.4–1.5 MiB with
  lossy parts in the others. As PNG it also took longer: 1069 ms against
  about 707 ms over VNC at N2. The harness does not score image quality,
  so G6's bytes compare at whatever quality guacd chose.
- **Connecting over RDP takes 2.2 s,** against 110 ms over VNC. A network
  adds about two RTTs to either: the occamy series logs in and then opens
  the WebSocket, and guacd's handshake has two exchanges. The harness's
  proxy does not delay TCP handshakes, which a real network adds on top.

## Targets

[The spec](../specs/008-native-architecture/benchmark.md#targets) sets
each scenario's target at 10x better than the occamy series' baseline
under the same network condition, at p95 for latencies. Where the
baseline shows a bound that Occamy cannot beat, the target is that bound
plus a margin instead. The tables below apply this to the medians above:
the number before each arrow is the baseline, and the bold one the
target.

The targets gate the release (owner, 2026-10-31): v1.0 waits until the
benchmark shows native at least 10x better than guacd. It does not ship
with a guacd fallback for any protocol still short of 10x; release
candidates may still carry one. Whether that means a flat 10x or each
target as defined here, such as T1 at N1 capped by a network bound, is
an open question for the owner.

- **Latency and completion.** At N0 a target is no lower than the remote
  server's own time, where `bench/vncprobe` measured it, plus 2 ms. A
  network cannot make the endpoints faster, so at N1 and N2 a target is
  no lower than the N0 target plus one RTT, for the input out and its
  update back, plus the time the link takes to carry the target's bytes.
- **Frame rates** are 95% of the scene's rate, or as many frames of the
  target size as the link carries, whichever is fewer. Where the
  baseline already does better, as G4 and G5 over VNC do, the target is
  a floor not to fall below.
- **Continuous scenes.** For G3 to G5, bytes and CPU are per delivered
  frame, so that dropping frames does not count as saving them, and the
  target comes from N0, since a frame's content does not depend on the
  network. Where the baseline dropped frames, this allows more bytes a
  second at the target frame rate than 10x fewer would: over RDP,
  3.0 MiB/s against 1.0 in G3 and 226 KiB/s against 96 in G4, and over
  VNC 420 KiB/s against 295 in G3.
- **Bounds not yet measured leave the target in place.** The shell's
  echo for T1, xrdp's reaction for G2 and G5 over RDP, xrdp's frame rate
  for G3 to G5, and each server's connect time for G7 have not been
  measured on their own. If one alone exceeds its target, the target
  becomes that bound plus 2 ms, or 95% of it for a frame rate.

**Latency,** p95 in ms:

| Scenario | N0 | N1 | N2 | Bound |
|---|---|---|---|---|
| T1 typing | 3.1 → **0.3** | 27.8 → **20.3** | 57.2 → **50.3** | the shell's echo, unmeasured |
| G2 key, VNC | 15.6 → **5.5** | 39.4 → **25.5** | 69.4 → **55.5** | x11vnc: 3.5 ms |
| G2 key, RDP | 17.6 → **1.8** | 41.5 → **21.8** | 71.7 → **51.8** | xrdp, unmeasured |
| G5 key under load, VNC | 84.4 → **8.4** | 113 → **28.4** | 154 → **58.4** | x11vnc in the motion scene: 5.6 ms |
| G5 key under load, RDP | 88.1 → **8.8** | 110 → **28.8** | 167 → **58.8** | xrdp, unmeasured |

**Completion,** in ms:

| Scenario | N0 | N1 | N2 | Bound |
|---|---|---|---|---|
| G6 photo, VNC | 173 → **17.3** | 302 → **48.6** | 707 → **124** | RTT and the target bytes |
| G6 photo, RDP | 131 → **13.1** | 173 → **44.5** | 271 → **120** | RTT and the target bytes |
| T2 `seq` | 897 → **89.7** | 1261 → **126** | 943 → **143** | RTT and the target bytes |

**Connecting,** time to the first frame at p95 in ms:

| Scenario | N0 | N1 | N2 | Bound |
|---|---|---|---|---|
| G7 SSH | 133 → **13.3** | 291 → **33.3** | 285 → **63.3** | sshd, unmeasured |
| G7 VNC | 113 → **11.3** | 162 → **31.3** | 220 → **61.3** | x11vnc, unmeasured |
| G7 RDP | 2198 → **220** | 2242 → **240** | 2305 → **270** | xrdp, unmeasured |

**Bytes and CPU:**

| Scenario | Metric | N0 | N1 | N2 |
|---|---|---|---|---|
| T2 `seq` | KiB down | 86 → **8.6** | 115 → **12** | 82 → **8.2** |
| T2 `seq` | occamy + guacd CPU ms | 881 → **88** | 1188 → **119** | 865 → **86** |
| T3 `top` | KiB/s down | 10.9 → **1.1** | 14.3 → **1.4** | 11.0 → **1.1** |
| G3 scroll, VNC | KiB per frame | 71 → **7.1** | 72 → **7.1** | 70 → **7.1** |
| G3 scroll, VNC | occamy + guacd CPU ms per frame | 38.4 → **3.8** | 38.4 → **3.8** | 45.6 → **3.8** |
| G3 scroll, RDP | KiB per frame | 516 → **52** | 499 → **52** | 1628 → **52** |
| G3 scroll, RDP | occamy + guacd CPU ms per frame | 104.2 → **10** | 106.3 → **10** | 624.8 → **10** |
| G4 drag, VNC | KiB per frame | 39 → **3.9** | 39 → **3.9** | 39 → **3.9** |
| G4 drag, VNC | occamy + guacd CPU ms per frame | 11.1 → **1.1** | 11.2 → **1.1** | 11.8 → **1.1** |
| G4 drag, RDP | KiB per frame | 40 → **4.0** | 40 → **4.0** | 40 → **4.0** |
| G4 drag, RDP | occamy + guacd CPU ms per frame | 14.2 → **1.4** | 14.5 → **1.4** | 14.0 → **1.4** |
| G5 motion, VNC | KiB per frame | 49 → **4.9** | 49 → **4.9** | 46 → **4.9** |
| G5 motion, VNC | occamy + guacd CPU ms per frame | 40.4 → **4.0** | 41.0 → **4.0** | 39.7 → **4.0** |
| G5 motion, RDP | KiB per frame | 66 → **6.6** | 66 → **6.6** | 56 → **6.6** |
| G5 motion, RDP | occamy + guacd CPU ms per frame | 54.9 → **5.5** | 55.1 → **5.5** | 51.5 → **5.5** |

**Frame rate,** in frames a second:

| Scenario | Scene | N0 | N1 | N2 |
|---|---|---|---|---|
| G3 scroll, VNC | 62.5 | 41.4 → **59** | 41.9 → **59** | 25.5 → **59** |
| G3 scroll, RDP | 62.5 | 19.6 → **59** | 17.8 → **59** | 1.5 → **47** |
| G4 drag, VNC | 60 | 59.7 → **57** | 59.7 → **57** | 48.6 → **57** |
| G4 drag, RDP | 60 | 24.2 → **57** | 24.5 → **57** | 24.1 → **57** |
| G5 motion, VNC | 30 | 29.8 → **28** | 29.8 → **28** | 29.6 → **28** |
| G5 motion, RDP | 30 | 24.8 → **28** | 24.8 → **28** | 24.8 → **28** |

**G6's bytes** have one target for the photograph, 139 KiB: 10x below
the median of the 26 runs in which guacd encoded it at least partly
lossy, 1.4 MiB. The 10 runs that sent it all as PNG are left out,
because v1's encoder competes with guacd's lossy output rather than its
fallback. guacd's smallest was 382 KiB.

**G1 is at its floor.** An idle session costs a keepalive every few
seconds, 1–6 bytes a second each way, and occamyd's and guacd's CPU is
within the spread between runs. The target is to stay there: keepalives
only, and no CPU the harness can tell from that spread.

**The 50 ms budget.** At N1 the baseline already meets it for T1
(27.8 ms) and G2 (39.4 ms over VNC, 41.5 ms over RDP), and misses it by
more than twice for G5 (113 ms over VNC, 110 ms over RDP). Every target
above meets it at N1.

The graphics pipeline's codecs, tile size and pixel layout were chosen
by a separate measurement on frames recorded from the bench desktop:
[benchmarks/codecs.md](./benchmarks/codecs.md). It also bears on G5's
bytes, which no pure-Go codec measured there brings near 4.9 KiB a
frame. Scroll detection, measured on the same frames in
[benchmarks/scroll.md](./benchmarks/scroll.md), sends the recorded
scrolling text in 5.0 KiB a frame against 58.5, under G3's target of
7.1 over VNC.

Four measurements would replace assumptions here: an RDP counterpart of
`bench/vncprobe`, for xrdp's reaction and its frame-rate ceiling; each
server's connect time on its own; the shell's echo over SSH on its own;
and an image-quality score for G6, so that its bytes compare at equal
quality.
