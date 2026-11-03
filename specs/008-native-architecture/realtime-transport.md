---
title: Real-time pacing and transport
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/comparison-benchmark.md
affects:
  - internal/pacer/
  - internal/stream/
  - protocol/
  - server/streams.go
  - client/client.go
  - client/occamy-web/src/protocol/
  - bench/
  - docker/bench/netem/
  - docs/protocol.md
  - docs/benchmarks.md
  - docs/benchmarks/
effort: xlarge
created: 2026-11-02
updated: 2026-11-03
author: Changkun Ou
dispatched_task_id: null
---

# Real-time pacing and transport

## Design Problem

The targets in [benchmark](benchmark.md) exist to make the protocol
between the browser and occamy minimise the time from a viewer's input to
its effect on screen, and the time a change takes to reach every viewer,
under every network condition. The binary protocol of
[protocol-v1](protocol-v1.md) removed the text framing and base64 of the
Guacamole protocol. This spec takes the next layer: how frames are paced,
acknowledged and carried.

The owner's question (2026-11-02): can the protocol behave more like a UDP
stream, where acknowledgements are feedback rather than a barrier, frames
go out without waiting for them, possibly arriving out of order, and the
client reorders and renders them adaptively, so that real-time control,
up to games, works?

Three mechanisms stand between the protocol and that goal today:

1. **The frame window is a barrier.** The server keeps at most two frames
   unacknowledged ([protocol-v1](protocol-v1.md) decision 3). On a long
   link the window, not the link, sets how many frames go out.
2. **TCP delivers in order.** One lost packet holds every later byte of
   the connection, the frame after an input included, for a round trip or
   a retransmission timeout.
3. **Full-screen motion is encoded tile by tile on the CPU**, at about
   22 ms of server CPU per frame in the essential comparison, more than a
   60 Hz frame allows.

The spec takes them one step at a time, and measures each step before the
next.

## Evidence

The essential comparison of 2026-11-02
([docs/benchmarks/essential/](../../docs/benchmarks/essential/)),
v1-native, G5 (a key during full-screen motion that animates at 30 fps),
six runs per cell:

| | VNC N0 | VNC N2 | RDP N0 | RDP N2 |
|---|---|---|---|---|
| Frames the server cut, per second | 58.6–59.3 | 27.9–29.7 | 25.9–26.5 | 24.9–25.2 |
| Scene frames shown, per second | 28.6–29.4 | 15.3–23.8 | 23.9–24.6 | 23.2–24.5 |
| Key latency p95, ms | 40.7–50.0 | 119.8–149.3 | 62.1–69.6 | 115.6–139.4 |

- Over VNC the backend applies each scene frame as about two updates, so
  the server cuts about 59 frames a second without delay. Under N2 (50 ms,
  20 Mbit/s) it cuts 27.9 to 29.7. A window of two frames, each
  acknowledged at the client's next 60 Hz refresh, allows two frames per
  round trip plus decode plus the wait for that refresh, about 70 ms:
  about 29 a second.
- The link carried 260 to 334 KiB/s, 11% to 14% of N2's 20 Mbit/s.
- xrdp produces about 25 updates a second, under that bound, so RDP's
  frame rate does not fall with the delay.
- Host load does not explain the VNC drop: without delay every run shows
  28.6–29.4 fps at loads from 5.7 to 9.6, and under N2 the runs at loads
  of 5.5 to 5.9 show 18.1, 20.8 and 23.8.
- Shown frames vary from 15 to 24 a second while the server's frames
  stay between 28 and 30. A likely cause, not yet measured: the
  acknowledgements of two frames that arrive within one refresh free both window slots at
  once, two frames leave back to back, and the second rarely carries a
  new scene frame.
- The pacer reads time spent waiting on the window as time spent waiting
  on the path (`StartFrame` sets `waitingSince` for a full window as for
  the byte cap, and `endRound` adds it to `busy` and to `limited`), so on
  a long, fast link it lowers the quality and may leave startup although
  bandwidth is not short. G5 hides this because its tiles already go at
  the lowest quality.

## Principles

1. **Send without waiting for a frame's acknowledgement, never faster
   than the path.** A sender that outpaces its link builds a queue: twice
   the link's rate adds a second of queue every second, and the frame
   after an input waits behind all of it. Over TCP the queue forms in the
   socket buffer, where a stale frame can no longer be dropped; over UDP
   it forms in a router, which drops packets at random, the input's frame
   among them. Every UDP streaming system keeps a rate controller for
   this reason. The bound on what is in flight therefore stays, in bytes
   from the path model; the count of frames stops being the limit.
2. **Acknowledgements are measurements.** A `frame-ack` frees what it
   covers from the bytes in flight and reports the round trip and the
   time the client took to show the frame. It no longer gates the next
   frame by count.
3. **Retransmit state, not bytes.** A tile that was lost or overtaken is
   sent again at its current version, never as the old bytes. This is
   the model of Mosh's state synchronisation protocol: each update names
   the state it builds on, the receiver acknowledges the state it has,
   and old updates are useless rather than owed.
4. **Order only what depends on order.** Inputs, terminal output, copies
   and scrolls, cache references and control messages need the previous
   state; a tile's content at a version does not.
5. **Latency for whoever controls, cadence for whoever watches.** The
   frame after an input is shown at once; a viewer that is only watching
   may trade a frame of delay for an even cadence.
6. **Each step is measured against the step before, under the same
   conditions, with Guacamole's number beside it.** A step is kept when
   its cells improve and the guard cells do not get worse.

## Steps

The order differs from the first sketch (2026-11-02), which put versioned
tiles before the loss measurement: versioned tiles change nothing visible
over TCP, so the loss measurement decides first whether they and a UDP
transport are worth building.

### Step 1: The window from the path; acknowledgements as feedback

**Protocol.** `frame_window` stays the hard bound that clients enforce
(docs/protocol.md, The frame window; the web client closes a server that
exceeds it). What changes is the offers:

- `protocol.MaxFrameWindow = 16` is offered by the server
  (`server/streams.go`, `streamCapabilities`), by the Go client core when
  `Options.FrameWindow` is zero, and by the web client when
  `frameWindow` is unset. A client that offers 2, or nothing, as every
  client released before this step does, agrees on 2 and is served as
  today.
- docs/protocol.md says that the server uses as many of the agreed frames
  as the path needs, and that a client that cannot show frames as fast as
  they come still holds the server back: by the agreed window, by the
  bytes in flight, which acknowledgements free, and by the decode time
  each acknowledgement reports, which spaces the server's frames.
- The web client's backlog bound (`maxBacklog`) scales with the window;
  it is checked against a window of 16 so that a well-behaved server is
  never taken for a broken one.

**Pacer** (`internal/pacer`):

- A graphical viewer's frames in flight are limited to
  `n = clamp(ceil((srtt + decode) / spacing) + 1, 2, Window)`, where
  `srtt + decode` is the time from a frame's end to its acknowledgement as
  measured, and `spacing = max(Interval(), 1/60 s)`: no screen shows more
  frames than its refresh. Without delay `n` is 2, as today; under N2
  about 5. The cap on bytes in flight, twice the bandwidth-delay product,
  is unchanged and remains what keeps the network's queue short.
- A terminal viewer keeps a window of 2 in this step, whatever was
  agreed: bulk output coalesces under it (protocol-v1 decision 4), and T2
  is a guard of this step.
- Waiting on the frame limit is not waiting on the path. `StartFrame`
  records the two separately; only waits on the byte cap and on `Pace`
  count toward a round's `busy`, which steps the quality, and toward
  `limited`, which ends startup.
- The input's exception stays: the frame that carries an input's effect
  may go one beyond the limit.

**Harness** (`bench/`):

- A v1 series can set the window its client offers. `v1-native` offers
  the default; `v1-native-w2` offers 2, which reproduces the window
  before this step on the same image and in the same run.
- Scenes record when each scene frame was shown, so that the report has
  the share of scene frames never shown and the 95th percentile of the
  interval between shown scene frames, beside the frame rate.
- The load gate is checked before each result rather than before each
  block ([comparison-benchmark](comparison-benchmark.md), Outcome of the
  essential plan).
- The report compares `v1-native-w2` to `v1-native` as a family of its
  own, with the same tests and corrections.

*Built.* `v1-native-w2` opens `v1-native`'s connections with
`client.Options.FrameWindow` 2 (`v1Windows` in `bench/v1.go`). Each
scene result gives `frame_interval_ms`, the percentiles of the time
between the first showings of consecutive stamps, measured by the
reader that both protocols share, so `guacd`'s and `occamy`'s scenes
have it too. `bench/report.py` takes `scene_missed`, 1 − frames ÷
(`scene_fps` × seconds) in percent, and `frame_interval_p95` from them,
as secondary metrics of G3, G4 and G5, and compares the family
**Frame window**, `v1-native-w2` to `v1-native`, with Holm's correction
and without the 10x and 50 ms targets, which set Occamy against
Guacamole. A block that the load gate stops part-way is measured again
whole on resume. `make bench-realtime-1` collects the plan below and
`make bench-realtime-1-report` writes its report into
`docs/benchmarks/realtime/1/`.

**Measurement.** Plan `bench/plans/realtime-1.yaml`, six runs:

| Cells | Networks | Series |
|---|---|---|
| G5 over VNC and RDP | N0, N1, N2 | v1-native, v1-native-w2 |
| T1, T2, and G2 over VNC and RDP (guards) | N0, N2 | v1-native, v1-native-w2 |

168 results, about two hours. Guacamole's numbers come from the
essential run of the same day, which had no N1 cells; the write-up says
so.

**Kept if** G5's scene frames shown per second, or its key latency, is
better under N1 or N2 with no significant loss elsewhere, and no guard
cell is significantly worse.

### Step 2: Loss in the benchmark

The network emulation (`bench/netem.go`) is a userspace proxy that
delays and serialises a byte stream; it cannot drop a packet, because
there is none to drop. Loss needs packet-level emulation: `tc netem`.

**Where the link is.** Delay, bandwidth and loss must act on the same
TCP connection, the one that loses a packet and then waits for it
again. On the bench host they cannot be split between the proxy and the
kernel: podman's port forwarding on macOS (gvproxy) ends the client's
connection on the Mac and opens another inside the VM, so a loss inside
the VM on a link the proxy delays would cost a retransmission on a path
of no delay. A **loss study** therefore emulates the whole link in the
kernel, in the network namespace of the series' server, and the proxy
only counts the bytes:

- Packets from the series' port to outside the stack's network pass a
  `prio` qdisc's band with `netem`: half the RTT, the bandwidth, the
  loss, and a queue limit of the proxy's twice the bandwidth-delay
  product in packets of 1500 bytes. Packets from clients to the port
  are redirected to an `ifb` device with the same `netem` without loss.
  Only packets to or from the series' port and outside the stack's
  network are shaped, so a server's backends stay untouched, as do
  occamyd's connections to guacd's own port and the debug listener.
- TSO and GSO are off on the server's interface while it shapes, so a
  loss drops one packet rather than a segment of up to 44.
- The harness runs the image `occamy-bench-netem` (`docker/bench/netem`)
  with `--network container:<server>` and `NET_ADMIN` before each block
  and clears it after, and clears every server it shapes when a
  collection starts, in case a stopped one left a link shaped. A plan
  with a loss study and `-engine none` is refused.

**Loss to the client only.** The question is what TCP's in-order
delivery costs: a frame behind a lost packet waits for its
retransmission, which a transport without ordering (step 3) would not.
A key or acknowledgement lost on the way to the server costs a
retransmission that such a transport would still pay, as its own
loss recovery, so it would only add noise to the answer. Loss is
random, at 0%, 0.5% and 2%.

**Measurement.** T1, G2 and G5 over v1-native and guacd at N1 and N2,
with 0%, 0.5% and 2% loss (`bench/plans/realtime-2.yaml`, 6 runs, 360
results). The control is the same link in the kernel at 0%, not the
matrix's proxy at N1 and N2: the kernel's link also delays a
connection's handshake, which the proxy does not. The figure of
interest is what loss adds to key latency at the 95th percentile, the
Hodges–Lehmann shift with its exact interval, beside the round trip.

**Decides** whether step 3 is built: if loss of 2% or less adds less than
about one round trip to key latency at the 95th percentile, step 3 waits
and step 4 comes next.

### Step 3: Order-independent tiles over a loss-tolerant transport

Outlined here; specified in full once step 2's numbers exist.

- **Versioned tiles.** Each tile update carries the tile's version; the
  client applies it only if it is newer than what it has, so updates may
  arrive in any order or twice. A lost or overtaken tile is marked
  damaged again and goes out at its current version (principle 3).
  Copies, scrolls and cache references are sent only against versions the
  client has acknowledged, or on the ordered path.
- **WebTransport (QUIC).** A reliable stream for inputs, control,
  terminal output and everything ordered; each graphical frame's tiles
  on a unidirectional stream of their own, reset when a later frame
  overtakes all of them. Streams rather than datagrams, because a tile
  is larger than a datagram. The pacer stays above QUIC's congestion
  control, for latency rather than throughput. WebSocket remains the
  fallback, for proxies that do not pass QUIC and for browsers without
  WebTransport; Safari's support is checked before relying on it. On the
  server, quic-go and webtransport-go keep occamyd in pure Go.
- [protocol-v1](protocol-v1.md) decision 6 puts WebTransport after 1.0.
  If step 2 shows a large cost, the owner decides whether this step
  moves before it.

**Measurement.** Step 2's cells over both transports.

### Step 4: Cadence for watchers

- A viewer that sent an input in the last 2 s is latency-first, as
  today. A viewer that is idle, or read-only, is cadence-first: the
  client shows frames one refresh after their server time, so that they
  appear evenly, and the server may give it a larger window and a higher
  quality.
- Lossy quality falls into a few tiers, so that viewers on similar links
  share the encode cache, which is keyed by quality.

**Measurement.** The viewers study of
[comparison-benchmark](comparison-benchmark.md), with the interval
between shown frames and the age of a frame when shown, per role.

### Step 5: A video path for full-screen motion

Tiles encoded on the CPU do not reach 60 fps of full-screen motion at
the essential run's cost per frame. A game needs a video codec, decoded
in the browser by WebCodecs. Options, decided when the step starts:

- RDP's AVC420 and AVC444 passed through without decoding, which keeps
  occamyd in pure Go ([native-rdp-backend](native-rdp-backend.md) has GFX
  without AVC today).
- A hardware encoder in a helper process, outside occamyd, for VNC and
  for motion that the backend does not encode.

**Measurement.** G4 and G5 at N0 to N2, and a 60 fps scene.

## Below the round trip

No transport shows an input's effect sooner than the round trip. Under
N2 the 50 ms budget of [benchmark](benchmark.md) is spent by the network
alone; only the client can do better, by predicting: the cursor is
already drawn locally, and a terminal can echo printable keys before the
server confirms them, as Mosh does, confirmed or undone by the input
sequence number in `frame-end` (protocol-v1, Proposed shape). That is a
separate spec when its turn comes.

## Write-up

Each step's results go in `docs/benchmarks/realtime/<step>/`, the raw
JSON with its report, and `docs/benchmarks.md` gains a section that
follows the steps: for each metric, the value at each step with its
interval, and Guacamole's beside it.

## Implementation notes: step 1

- **Status.** Kept. The pacer, the offers and the docs are built
  (#180), and the harness (#181), and measured on 2026-11-02
  (docs/benchmarks.md, Step 1: the frame window from the path).
- **Pacer.** `Pacer.Window` is `clamp(ceil((srtt + decode) / spacing) +
  1, 2, Config.Window)` with `spacing = max(Interval(), 1/60 s)`. Before
  the first ack `srtt` and `decode` are zero, so a viewer starts at 2
  frames, as before, and grows once a round trip is measured. The
  reported decode time runs from a frame's arrival to its ack, so it
  includes the wait for the screen's refresh; the round trip does not.
  `StartFrame` tests the cap on bytes before the frame limit, so a
  refusal at both counts as waiting for the path; a refusal at the frame
  limit alone ends any wait at the cap that was being counted.
- **`Config.FixedWindow`** keeps `Window` frames from the start. Tests
  that drive a writer without acks set it with `Window: 1 << 20`, which
  meant an unlimited window before this step.
- **Terminals.** `internal/stream.viewerPacer` keeps a terminal viewer's
  window at most 2 whatever was agreed; the protocol allows it, since the
  agreed window is a bound, not a promise.
- **Offers.** `protocol.MaxFrameWindow` (16) is offered by
  `server/streams.go`, by the Go client when `Options.FrameWindow` is
  zero, and by the web client when `frameWindow` is unset or zero
  (`MAX_FRAME_WINDOW`). The web client's backlog bound, three screens of
  pixels per frame of the window plus two, becomes 216 bytes a pixel at
  16, capped at 1 GiB; that still holds 17 frames of a 4K screen's raw
  pixels (about 540 MiB), so a server within the window is never taken
  for a broken one.
- **Tests.** `TestWindowFollowsThePath` (the formula),
  `TestWindowWaitIsNotPathWait` (fails before this step: quality fell to
  30 from waiting on the window alone), `TestWindowDoesNotSetTheFrameRate`
  (simulated N2, a scene that changes 60 times a second: 46.5 frames a
  second with window 2, 69.7 with the path's, the input's p95 27.4 and
  30.9 ms), client `TestWindowFollowsLongPath` (a 50 ms pipe: 40 frames a
  second with window 2, 95 with the path's), `TestOfferedWindowHoldsServer`
  (a caller that stops reading still holds the server back) and
  `TestViewerPacerWindow`.
- **Cost seen in simulation.** More frames in flight means the frame
  after an input can queue behind more bytes at the bottleneck: 3.5 ms
  more at the 95th percentile in the N2 simulation. On the real stack
  the frame after a key gained 23 ms instead (below).
- **Measured** (`make bench-realtime-1`, 168 results, one image built
  from `447525c`, `v1-native` against `v1-native-w2`). G5 over VNC
  under N2, the one cell where the window was the limit: the server
  cuts 56 frames a second instead of 30; the viewer sees 29.6 scene
  frames a second instead of 18.8, 1.57x [1.06, 1.84], and misses 1.5%
  of them instead of 37%; shown frames are 49 ms apart at the 95th
  percentile instead of 77; a key shows in 95.7 ms instead of 119 at
  the 95th percentile, 1.25x [1.21, 1.34], significant after Holm's
  correction (p 0.002, adjusted 0.03). The cost is CPU: 601 ms a second
  instead of 493, for nearly twice the frames. Over RDP under N2 shown
  frames come 48 ms apart instead of 63, 1.33x [1.18, 1.45], at xrdp's
  unchanged 24 a second. No other primary metric, and no guard (T1, T2,
  G2 under N0 and N2), changed significantly.
- **Why the key gained, a hypothesis not yet measured.** With a window
  of two, the VNC update that carries a key's effect often arrives after
  the input's own frame has gone (that frame carries what had changed
  by then), and it then waits for the window like any frame: up to a
  round trip and a refresh. With the window from the path it goes at
  once. Per-frame send and ack times in the harness would show it.
- **Where it leaves G5 over VNC under N2.** 95.7 ms [92.9, 99.4]
  against the target of 63.4 (docs/benchmarks.md, Targets): 50 ms of
  it is the round trip, and the rest is mostly occamyd's encoding of
  full-screen motion, 14 KiB and about 20 ms of CPU a shown frame, which
  is Occamy's own cost (benchmark.md, decision 1) and step 5's subject.
- **Load.** Two results started below the load gate and ran while the
  host's load rose far above it (G5 over VNC: window 2 under N0, 16.8
  frames a second and 213 ms; the path's window under N1, 8.9 and
  466 ms). They widen those cells' intervals; neither cell differs
  otherwise. The gate checks the load before a result, not during one.

## Implementation notes: step 2

- **Status.** The loss study is built; the run waits for the owner's
  go-ahead.
- **Built.** `docker/bench/netem` (`shape.sh` and its image); a `loss`
  study in plan files with a `loss` list, its blocks carrying the loss
  as their factor (`loss=2%`) and shaped in the kernel
  (`block.kernel`, `plan.shape`, `plan.clearLinks`); each series names
  its server and port; `netCondition.Loss` and `shapeArgs`, which share
  the proxy's queue (`queueBytes`); `make bench-realtime-2` and
  `bench-realtime-2-report`. `bench/report.py` adds `loss.csv` and a
  section with the shift each loss adds over the same cell without
  loss, Holm-corrected over the primary metrics, with figures against
  the loss; the 10x and 50 ms targets judge no loss cell.
- **Checked on the bench host.** A request on an open connection to
  occamy-v1 with N2 shaped took 51 ms, against 1 ms on the unshaped
  debug port; a new connection took 102 ms, since the handshake inside
  the VM is shaped too. netem and ifb work in a rootful podman
  container's namespace with `NET_ADMIN`. A pilot of one block each,
  not a measurement, v1-native under N2 with 30 keys: at 0% occamy-v1
  retransmitted no segment and T1's median was 51.5 ms; at 2% it
  retransmitted 71, and G5 over VNC's key p95 was 464 ms against 118.
- **Surprises.** gvproxy ends the client's TCP connection on the Mac:
  the servers see 192.168.127.1, not the harness. A `prio` qdisc's
  default priomap sends priority 2 to band 3, so the shaping band would
  have caught unfiltered packets; the priomap sends every priority to
  band 1.

## Affects

- Step 1: `internal/pacer`, `internal/stream`, `protocol/handshake.go`,
  `server/streams.go`, `client/client.go`,
  `client/occamy-web/src/protocol/session.ts`, `bench/`,
  `docs/protocol.md`, `docs/benchmarks.md`.
- Step 2: `bench/`, `docker/docker-compose.bench.yaml`.
- Steps 3 to 5 are scoped when they start.
