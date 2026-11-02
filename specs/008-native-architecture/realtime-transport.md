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
  - docs/protocol.md
  - docs/benchmarks.md
  - docs/benchmarks/
effort: xlarge
created: 2026-11-02
updated: 2026-11-02
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
there is none to drop. Loss needs packet-level emulation: `tc netem` on
the bench network inside the podman VM, which needs `NET_ADMIN` for the
container that applies it, and a harness that applies and clears the
qdisc around each block.

**Measurement.** T1, G2 and G5 over v1-native and guacd at N1 and N2,
with 0%, 0.5% and 2% loss. The figure of interest is how much key
latency at the 95th percentile grows with loss: the cost of TCP's
in-order delivery, which is what a UDP transport would remove.

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

## Affects

- Step 1: `internal/pacer`, `internal/stream`, `protocol/handshake.go`,
  `server/streams.go`, `client/client.go`,
  `client/occamy-web/src/protocol/session.ts`, `bench/`,
  `docs/protocol.md`, `docs/benchmarks.md`.
- Step 2: `bench/`, `docker/docker-compose.bench.yaml`.
- Steps 3 to 5 are scoped when they start.
