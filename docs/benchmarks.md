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

This builds and starts the demo stack with
`docker/docker-compose.bench.yaml` on top, which publishes guacd on
`127.0.0.1:4822`. Then it runs every scenario in every series under every
network condition, which takes about 12 minutes. Results go to
`bench-results.json`, and a Markdown table goes to standard output.

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
| `-series` | `occamy,guacd` | `occamy` goes through occamyd; `guacd` connects to guacd directly |
| `-runs` | `1` | How many times to run everything; the table shows medians |
| `-quick` | off | A tenth of the samples and a sixth of the durations, to check the setup |
| `-engine` | `podman` | Container engine for CPU time, or `none` |
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
  they include the WebSocket frame headers, which the `guacd` series does
  not have.
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
detection of window moves and scrolls, which brings it to 2.8 ms and
3.4 ms. `bench/vncprobe` measures this: a VNC client that runs inside
the container, so the numbers include no network and no guacd. Its doc
comment shows how to run it. The RDP server is xrdp with
xorgxrdp, as configured by Debian.

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
  latency is the RTT plus about 5 ms at the median and 7–9 ms at p95. A
  10x improvement on 3 ms is below what the host's scheduling can
  resolve, so T1's target will be the bound plus a margin, not 10x.
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
