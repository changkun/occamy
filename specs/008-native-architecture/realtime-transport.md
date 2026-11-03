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
  - server/congestion.go
  - internal/config/
  - client/client.go
  - client/occamy-web/src/protocol/
  - bench/
  - docker/bench/netem/
  - docs/protocol.md
  - docs/benchmarks.md
  - docs/benchmarks/
  - docs/configuration.md
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
   the connection, the frame after an input included, for a round trip,
   or for two round trips and 200 ms when it was the only packet in
   flight (step 3a).
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
7. **Spend the server's CPU before the client's or the link's.** The
   owner's framing (2026-11-03): the targets are there to push the
   design of the protocol, and of a well-engineered service that
   optimises latency and bandwidth while preserving real-time control;
   and the server has more machines to scale out to, while each user's
   client has a fixed compute capability. What this spec takes from it,
   decided here rather than by the owner: the bytes on the link, the
   client's decoding and drawing, and latency rank above the server's
   CPU. The server CPU targets of [benchmark](benchmark.md) stay, so no
   target is dropped, but a design that costs the server CPU to save
   bytes or client work is taken, and one that saves only server CPU
   waits. The first design made under it is the scaled codec for tiles
   in motion ([graphics-pipeline](graphics-pipeline.md), Implementation
   notes: Scaled motion), which takes G5's bytes from 14.2 to 8.0 KiB
   a frame over VNC and from 14.1 to 7.7 over RDP, and occamyd's CPU
   down with them, for half a millisecond more of the Go client's
   decoding a frame over VNC, the upscale included.

## Prior art and coverage

Searched on 2026-11-03, for step 3, with the owner's two questions:
which way the field is heading, and whom each option reaches. Each
claim carries its source; what was looked for and not found is listed
at the end, so that a later search knows where to start.

### Transports of remote desktops and browser streaming

| Product | Native client | Browser client |
|---|---|---|
| Amazon DCV | QUIC over UDP by default since 2024.0, WebSocket over TCP where UDP does not pass; sign-in stays on WebSocket ([client](https://docs.aws.amazon.com/dcv/latest/userguide/using-connecting-win.html), [server](https://docs.aws.amazon.com/en_us/dcv/latest/adminguide/disable-quic.html)) | WebSocket only: "the web browser client doesn't support the QUIC (UDP) transport protocol" ([web client](https://docs.aws.amazon.com/dcv/latest/userguide/using-connecting-browser-connect.html)) |
| Amazon WorkSpaces (DCV) | UDP first on port 443, TCP when UDP is blocked ([2024-11](https://aws.amazon.com/about-aws/whats-new/2024/11/amazon-workspaces-wsp-desktop-traffic-tcp-udp-port-443)) | not looked at |
| Citrix HDX | EDT, Citrix's own transport over UDP, TCP when it is unavailable ([adaptive transport](https://docs.citrix.com/en-us/citrix-daas/hdx-transport/adaptive-transport), [EDT](https://docs.citrix.com/en-us/citrix-daas/hdx-transport/adaptive-transport/enlightened-data-transport)) | WebSocket, without EDT, "built upon TCP" ([Citrix, 2020](https://www.citrix.com/blogs/2020/08/06/your-guide-to-implementing-citrix-workspace-app-for-html5/)) |
| Azure Virtual Desktop | RDP Shortpath, UDP after a TCP start through the gateway ([Shortpath](https://learn.microsoft.com/azure/virtual-desktop/rdp-shortpath)) | not found |
| Chrome Remote Desktop | | WebRTC; its older protocol is obsolete ([Chromium](https://chromium.googlesource.com/chromium/src/+/f303bc03a281b55dbd066185f79daa61dd8ba8d1/remoting/GEMINI.md)) |
| GeForce NOW | | WebRTC in Chrome ([XDA](https://www.xda-developers.com/play-nvidia-geforce-now-games-chrome-windows-macos/)) |
| Neko | | WebRTC, with WebSocket as a fallback for control ([Neko](https://github.com/m1k1o/neko)) |
| Selkies 2.0 | | WebSocket with WebCodecs by default, one TCP port, no STUN or TURN; WebRTC on request ([2.0.0rc0](https://github.com/selkies-project/selkies/releases/tag/v2.0.0rc0)) |
| Apache Guacamole | | WebSocket, or an HTTP tunnel: this project's baseline |

- Native clients moved to UDP, each with TCP as the fallback. Their
  browser clients stayed on TCP.
- Browser streaming that left TCP went to WebRTC. Selkies went back to
  WebSocket by default, for reverse proxies and NAT.
- No remote desktop on WebTransport was found. Media over QUIC, which
  browsers reach through WebTransport, is for live media: Cloudflare
  opened a relay network in August 2025
  ([Cloudflare](https://developers.cloudflare.com/moq)), and an analysis
  of Chrome's usage counters argues that the talk of a move away from
  WebRTC is ahead of the numbers
  ([webrtcHacks](https://webrtchacks.com/is-everyone-switching-to-moq/)).

### Whom each option reaches

**WebTransport** needs every browser, network and proxy on the path:

- Browsers: Chrome and Edge 97, Firefox 114, and Safari 26.4 on macOS
  and iOS since 2026-03-24, so every engine
  ([web-features](https://web-platform-dx.github.io/web-features-explorer/features/webtransport/),
  [WebKit](https://webkit.org/blog/17862/webkit-features-for-safari-26-4/)).
  WebKit says WebTransport can run over HTTP/2 and TCP where QUIC
  cannot, with the same API; whether webtransport-go serves that is for
  3b to check.
- Networks: some corporate networks block UDP on port 443; no
  measurement of how many was found.
- Reverse proxies: none of nginx, Caddy, Traefik or Envoy documents
  passing WebTransport; a proxy has to understand it
  ([Caddy](https://github.com/caddyserver/caddy/issues/5421)). A
  deployment behind one gets the fallback.
- Server: quic-go and webtransport-go keep occamyd in pure Go. quic-go
  has CUBIC alone, and its maintainers declined both a plug-in interface
  for congestion control and a port of BBR
  ([#776](https://github.com/quic-go/quic-go/issues/776),
  [#1888](https://github.com/quic-go/quic-go/pull/1888),
  [#4565](https://github.com/quic-go/quic-go/issues/4565)).

**BBR** needs only the server's kernel; no client, browser, network or
proxy changes:

- Linux has had it since 4.9, as the module `tcp_bbr`. Mainline
  carried BBR v1, with v3 out of tree, at the last public word on it
  ([bbr-dev, March 2025](https://groups.google.com/g/bbr-dev/c/4vwZGw0nxdQ)).
- An unprivileged process may set `TCP_CONGESTION` only to what
  `net.ipv4.tcp_allowed_congestion_control` lists, and loading
  `tcp_bbr` adds `bbr` to it, since BBR is not restricted
  ([man-pages](https://git.tldp.org/LDP/man-pages/commit/bf561a0fbcfed101aea2d523fe5cd50e90273786),
  [bbr-dev](https://groups.google.com/g/bbr-dev/c/zxuW4vaBBSg)). A
  container cannot load a module; the host loads it once.
- Checked on the bench host's podman VM (Fedora, kernel 7.1.3): the
  module ships unloaded; after `modprobe tcp_bbr` the kernel's default
  stays CUBIC and `bbr` is allowed; a container started with
  `--sysctl net.ipv4.tcp_congestion_control=bbr` reads `bbr` in its
  namespace, and the server's connections show `bbr` in `ss -tin`.
- Adoption: Google used BBRv2 for more than 98% of its internal TCP
  traffic in 2021, and made v2 to lose fewer packets than v1
  ([IETF 110](https://datatracker.ietf.org/meeting/110/materials/slides-110-iccrg-bbr-updates-00.pdf)).
  A 2024 measurement study reports BBR's share of the internet falling
  since 2019 ([NUS](https://events.comp.nus.edu.sg/view/22693)); no
  firm figure for 2025 was found.

**What follows.** The two remove different costs of loss (Step 3):
BBR on TCP removes the throughput bound and keeps the wait behind a lost
packet; WebTransport through quic-go removes the wait and keeps the
bound. Whatever the transport, one fallback is kept, and with
WebTransport it will be used, not held in reserve.

**Looked for and not found.** How many networks block UDP on port 443;
whether Azure Virtual Desktop's web client uses Shortpath; a current
Citrix document on the HTML5 client (the statement above is from
2020); BBR's share of internet traffic after 2024; a measure of
WebTransport's use (one secondary source gave about 0.001% of Chrome's
page loads, not checked).

## Steps

The order differs from the first sketch (2026-11-02), which put versioned
tiles before the loss measurement: versioned tiles change nothing visible
over TCP, so the loss measurement decides first whether they and a UDP
transport are worth building.

No step gates a release (owner, 2026-11-03, after 3a's measurement).
Each changes the server alone or adds to protocol v1 in the ways its
versioning allows, through a capability, an ignorable message, a codec
or a second transport beside WebSocket, so a client that agrees on none
of them sees no change. Each lands in whichever release is next once it
is measured and kept, in a beta, in the release candidate, or after the
candidate freezes the protocol, whose later changes are additive
([008](../008-native-architecture.md), versioning).

The owner's plan for beta.2 (2026-11-03) ships what is measured and
kept by then, steps 1 and 3a, and moves the unfinished steps, 3b, 3c, 4
and 5, and prediction below the round trip, to beta.3
([008](../008-native-architecture.md), v1.0.0-beta.2 scope).

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

### Step 3: The two costs of loss

Step 2 measured two costs of loss (Implementation notes: step 2): the
wait behind a lost packet, up to 3 round trips, where the scene's rate
fits under TCP's throughput bound for the loss; and, under N2 with 2%,
the bound itself, about 1.22 · MSS / (RTT · √p), below the scene's
rate, to which a congestion control that reads every loss as congestion
holds any transport. Each half of the step removes one. 3a needs no
change to the protocol or the clients, so it goes first, and its
answer is an input to 3b's transport. 3a's measurement found a third
cost, the wait for a lone lost packet, which 3c outlines.

The owner on 2026-11-03 had no preference between the two, asked what
the field does, and said to go ahead, with one fallback kept whatever
the transport. 3a went first. After its measurement the owner decided
that 3b, which brings WebTransport, gates no release, as no step does
([Steps](#steps); [protocol-v1](protocol-v1.md) decision 6).

3a and 3b are complements, not alternatives: BBR on TCP removes the
bound and keeps the wait, and WebTransport through quic-go, whose
congestion control is CUBIC alone, removes the wait and keeps the bound.
[Prior art and coverage](#prior-art-and-coverage) has what other
remote desktops and browsers do, whom each option reaches, and the
sources.

#### Step 3a: The congestion control

- **Mechanism.** BBR models the path's bandwidth and round trip and
  sends at that rate, instead of halving its window at each loss. It is
  a property of the server's TCP connections.
- **Harness.** `v1-native` runs occamyd as it ships, which asks for BBR,
  and `v1-native-cubic` opens the same connections on `occamy-v1-cubic`,
  whose v1 file asks for CUBIC (`congestion_control: cubic`). Each v1
  result read with `-runtime` records the congestion control its
  server's connections use as `congestion`, from occamyd's
  `tcp_congestion`. The report compares the family **Congestion
  control**, `v1-native-cubic` to `v1-native`, with Holm's correction and
  without the targets. The measurement ran before occamyd asked for BBR,
  with `v1-native` on the kernel's default, CUBIC, beside
  `v1-native-bbr`, a service whose network namespace took BBR
  (Implementation notes: step 3a).
- **Measurement.** Plan `bench/plans/realtime-3a.yaml`: step 2's loss
  study, T1, G2 and G5 under N1 and N2 with none, 0.5% and 2% of the
  packets lost, with the CUBIC series in `guacd`'s place, 8 runs of 100
  probe keys: 480 results, about two hours. `make bench-realtime-3a`
  collects it, and `make bench-realtime-3a-report` writes its report
  into `docs/benchmarks/realtime/3a/`.
- **Kept if** G5's key latency or frame rate is significantly better in
  a cell with loss, and no cell without loss and no guard (T1, G2) is
  significantly worse. Then occamyd asks for BBR on its connections
  (`TCP_CONGESTION`, which an unprivileged process may set to BBR once
  the module is loaded), keeps the kernel's default where it cannot,
  and the deployment docs say how to load the module.

#### Step 3b: Order-independent tiles over a loss-tolerant transport

Outlined here; specified in full when it starts. It adds to protocol v1
and changes nothing in it: a capability agrees on versioned tiles, and
WebTransport is a second transport beside WebSocket, which delimits
each message today, so the new transport delimits them itself, since
the header carries no length.

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
  control, for latency rather than throughput. WebSocket over TCP, with
  3a's congestion control, remains the fallback, for networks that block
  UDP and proxies that do not pass WebTransport. On the server, quic-go
  and webtransport-go keep occamyd in pure Go.
- **Congestion control.** 3a showed what the bound costs: under N2 with
  2%, CUBIC let 16 to 18 of the scene's frames a second through, and BBR
  24 to 28, about the rate without loss. So QUIC needs a controller that does
  not read a random loss as congestion, which quic-go does not offer;
  how to get one, and how the pacer keeps the cadence even at a lower
  rate, are part of this step's design. QUIC's probe timeout is the
  smoothed round trip, four of its deviations and the peer's largest
  acknowledgement delay (RFC 9002, section 6.2); Linux TCP's probe for a
  lone lost packet waits twice the round trip and 200 ms (3a, 3c).

**Measurement.** Step 2's cells over both transports, with 8 runs, so
that a difference can be significant after Holm's correction.

#### Step 3c: The wait for a lone lost packet

Outlined here; specified in full when it starts. 3a's measurement found
this cost; 3a does not remove it, and 3b removes it only for clients on
WebTransport. Like 3a it needs no change to the protocol or the
clients, so it can go before 3b.

- **Mechanism.** When a lost packet is the only one in flight, no later
  packet shows its loss, and Linux sends a tail loss probe after twice
  the smoothed round trip plus the socket's minimum retransmission
  timeout, 200 ms by default, which allows for a receiver that delays
  its acknowledgement of a lone segment (`tcp_schedule_loss_probe`).
  Typing and a key on a still desktop send just such packets: with 2%
  loss their slowest keys wait three round trips and 200 ms, 264 to
  274 ms under N1 and 354 to 385 under N2, with BBR as with CUBIC.
  Since Linux 6.15 an unprivileged process can lower the minimum for a
  socket, from 2 jiffies to 200 ms (`TCP_RTO_MIN_US`); it also lowers
  the floor of the retransmission timeout.
- **Risk.** A receiver that delays its acknowledgement for longer than
  the new minimum gets a spurious probe, one extra segment, which it
  acknowledges at once; a spurious timeout costs more. The bench's
  receiver is gvproxy's user-space stack rather than a browser's
  operating system, so the measurement counts the probes and spurious
  timeouts in each namespace without loss as well as with it.
- **Harness.** A service in `occamy-v1-cubic`'s pattern that keeps
  occamyd's BBR and lowers `net.ipv4.tcp_rto_min_us`, which is per
  namespace on the bench host's kernel, beside `v1-native`.
- **Kept if** the 99th percentile of T1 and G2 with loss is
  significantly better, and no cell without loss is significantly
  worse. Then occamyd sets the minimum on its client connections where
  the kernel allows it.

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
- VP8 inter frames from occamyd, at a reduced resolution. Measured on
  2026-11-03 on the plasma of the codec corpus (two consecutive motion
  frames, 1024×768, quality 30, `github.com/gen2brain/vpx` v0.2.1's
  VP8 encoder at its fastest method, on the M5 Pro bench host):

  | Encoding of a frame | KiB | Encode, ms | SSIM |
  |---|---|---|---|
  | WebP tiles at full size | 13.6 | 8.6 | 0.988 |
  | WebP tiles at half size (the scaled codec) | 6.9 | 2.6 | 0.986 |
  | VP8 key frame of the whole screen | 11.0 | 5.9 | |
  | VP8 inter frame, full size | 5.0 | 24.3 | 0.989 |
  | VP8 inter frame, half size | 1.4 | 6.4 | 0.990 |

  An inter frame is 13.6 to 5.0 KiB against the tiles, and 11.0 to 5.0
  against a key frame, which is what a WebP tile is. Its cost is the
  motion search, which is fixed per macroblock: an inter frame of an
  unchanged screen still takes 20.8 ms (0.4 KiB), and four threads take
  24.1 ms, since the encoder does not split an inter frame. Decoding one
  in Go took 0.93 ms. At half size it is the smallest by far, but an
  inter frame depends on the one before it, so it needs the stream
  ordered and every frame delivered, which the order-independent tiles
  of step 3b give up; a lost or skipped frame needs a key frame, and a
  viewer joining needs one too. That is why it waits for this step
  rather than replacing the scaled codec.

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

- **Status.** Done. Built in #183 (`853c4a2`) and measured on
  2026-11-03 from `e56b585`:
  [docs/benchmarks.md](../../docs/benchmarks.md#step-2-what-loss-costs)
  and `docs/benchmarks/realtime/2`.
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
- **Measured.** 352 of 360 results; the 8 that failed are guacd's G5
  under N2 with 2%, where a key's effect did not show within 10 s.
  What loss added to `v1-native`'s key latency at the 95th percentile
  during full-screen motion (G5), over VNC and over RDP: 10.0 and
  15.4 ms with 0.5% under N1, 59.8 [44.2, 88.3] and 64.8 [39.0, 83.3]
  with 2%; 60.4 and 83.7 [55.1, 159] with 0.5% under N2, 271 and
  407 [304, 471] with 2%. To typing and to a key on a still desktop it
  added at most 5.0 and 12.9 ms at the 95th percentile, but with 2% the
  99th was 263 to 273 ms under N1 in 13 of 18 runs and 354 to 363 under
  N2 in 8 of 18, against 22 to 59. (These notes first gave 236 to 271
  at either round trip: medians over runs on either side of the slow
  keys.)
- **Decision.** Loss of 2% adds 3 to 8 round trips to a key during
  motion, more than about one, so step 3 is built: 3a first, and
  whether 3b comes before 1.0 is decided on 3a's measurement (Step 3;
  [protocol-v1](protocol-v1.md) decision 6). After 3a, no step gates a
  release ([Steps](#steps)).
- **What it tells step 3.** Loss costs two things. Where the scene's
  rate fits under TCP's throughput bound for the loss, about
  1.22 · MSS / (RTT · √p) (N1 with 2%, N2 with 0.5%), the frame rate
  holds and the cost is the wait for a retransmission, up to 3 round
  trips, which delivery in any order removes. Under N2 with 2% the
  bound, about 245 KiB a second, is below the scene's 420: the link
  carried 244 over VNC and 220 over RDP, the window let 16.8 and 15.6
  frames a second out, and 44% and 48% of the scene's frames were
  never shown. That is congestion control reading a random loss as
  congestion, and any transport with a loss-based controller has the
  same bound; quic-go's is CUBIC, in an internal package. The slow keys
  wait three round trips and 200 ms, Linux's tail loss probe for a lone
  packet, which step 3a measured; these notes first took it for the
  retransmission timeout.
- **Surprises.** gvproxy ends the client's TCP connection on the Mac:
  the servers see 192.168.127.1, not the harness. A `prio` qdisc's
  default priomap sends priority 2 to band 3, so the shaping band would
  have caught unfiltered packets; the priomap sends every priority to
  band 1. At 6 runs a side the exact test's smallest p is 0.0022, so
  Holm's correction over 39 tests could find no difference significant
  whatever the data; the decision rule never asked it to, and the
  report now says when the correction is out of reach. A plan with as
  many tests takes 8 runs or more. guacd queued frames wherever the
  loss bound was below its 1,200 to 1,600 KiB a second, so each key
  waited for the one before and its G5 cells took up to 9 minutes;
  the VNC server's CPU in guacd's G5 under N2 with 2% was 1,079 s,
  against 32 s without loss, not looked into.

## Implementation notes: step 3a

- **Status.** Measured, kept and shipped. Built in #186 (`401193e`) and
  measured on 2026-11-03 on step 2's image, built from `e56b585`, since
  the commits between change only the harness, with the results in #187
  (`6d3586d`):
  [docs/benchmarks.md](../../docs/benchmarks.md#step-3a-the-congestion-control)
  and `docs/benchmarks/realtime/3a`. occamyd asks for BBR from #188.
- **Built for the run** (#186; replaced in #188, Shipped below). The
  `occamy-v1-bbr` service, behind the compose profile
  `bbr`, with BBR as its namespace's default congestion control and its
  signing key on a tmpfs; the series `v1-native-bbr` and the harness's
  `-v1-bbr` (`OCCAMY_V1_BBR_PORT`, `OCCAMY_V1_BBR_DEBUG_PORT`); the link
  image's `congestion` line and each loss-study result's `congestion`;
  the report's family **Congestion control**; the plan, `make
  bench-realtime-3a` and `bench-realtime-3a-report`. CI's bench job
  loads `tcp_bbr`, starts the profile and runs `v1-native-bbr` in its
  smoke run.
- **Decisions.** A service of its own rather than a setting of
  occamyd's: the experiment changes the kernel's choice and nothing
  else, on the same image, and a profile keeps a kernel without BBR
  from failing everyone's bench stack. The CPU sampler adds a block's
  own server to the services every stack has, since podman's statistics
  fail a whole request that names a container that does not exist.
  `v1-native-bbr` is violet, as `v1-native-w2` is: red, the palette's
  last slot, fails the validator beside `v1-native`'s orange (normal
  vision ΔE 7.1), and the two never share a figure.
- **Checked on the bench host.** `tcp_bbr` was loaded in the podman VM
  on 2026-11-03 (`podman machine ssh sudo modprobe tcp_bbr`); the
  kernel's default stays CUBIC, and the module stays until the VM
  restarts. In `occamy-v1-bbr`'s namespace `ss -tin` showed `bbr` on a
  client connection during a block, with 28 retransmissions under N2
  with 2%. A pilot, one run of 30 keys and not a measurement, G5 under
  N2 with 2%: with CUBIC a key showed in 408 ms over VNC and 458 over
  RDP at the 95th percentile, with 13.1 and 19.0 scene frames a second;
  with BBR in 163 and 154 ms, with 28.5 and 25.2.
- **Measured.** All 480 results, each with its series' congestion
  control, in 2 h 22 min, 29 of them waiting at the load gate. Key latency
  at the 95th percentile, BBR against CUBIC, over Holm's correction for
  30 tests: 5 differences significant, all better, all G5 with loss:
  2.45x [2.29, 2.65] over VNC and 2.97x [2.78, 3.39] over RDP under N2
  with 2% (149 ms against 360, 163 against 471), 1.55x and 1.41x under
  N2 with 0.5%, 1.56x over VNC under N1 with 2%. What 2% adds over no
  loss, with BBR: 17.3 [10.3, 21.9] and 24.2 [17.7, 47.3] ms under N1,
  52.2 [43.3, 59.4] and 50.0 [38.7, 53.1] under N2, about a round trip,
  against 64.5, 55.7, 266 and 359 with CUBIC. Under N2 with 2% the scene
  keeps 27.6 frames a second over VNC and 24.4 over RDP, against 16.4
  and 17.6, and the link carries 398 and 346 KiB a second, against 240
  and 250 (secondary, uncorrected). No cell without loss and no guard
  is significantly worse; without loss the factors are 0.97 to 1.00.
  CUBIC's G5 under N2 with 2% repeats step 2's: 360 against 379 over
  VNC, 471 against 519 over RDP.
- **Decision.** Kept by the rule set before the run (Step 3a): occamyd
  asks for BBR on its client connections (`TCP_CONGESTION`) and keeps
  the kernel's default where it cannot, and the deployment docs say how
  to load `tcp_bbr`.
- **Shipped (#188).** occamyd sets `TCP_CONGESTION` on its public
  listener to the v1 file's `congestion_control`, `bbr` by default, and
  the connections it accepts inherit it; `system` leaves the kernel's
  choice. Where the kernel refuses, because the module is not loaded or
  an unprivileged process may not use the name
  (`net.ipv4.tcp_allowed_congestion_control`), occamyd keeps the
  kernel's choice and logs a warning that says which and how to fix it.
  It logs the congestion control its connections use, and `/debug/vars`
  gives it as `tcp_congestion`. The docs are
  [configuration.md](../../docs/configuration.md#tcp-congestion-control).
  The bench now runs occamyd as it ships: `v1-native` is BBR, and
  `v1-native-cubic`, on `occamy-v1-cubic`, whose file has `v1-native`'s
  connections and asks for CUBIC, is the baseline. The compose profile
  `bbr`, `occamy-v1-bbr`, `-v1-bbr` and the link image's `congestion`
  line are gone; each v1 result read with `-runtime` records `congestion`
  from occamyd's `tcp_congestion`, and the harness warns when a server's
  connections use another than its series stands for. The report keeps
  the family of 3a's run, `v1-native` to `v1-native-bbr`, beside the new
  one, `v1-native-cubic` to `v1-native`, so that it still analyses the
  run on disk. CI loads `tcp_bbr` and runs `v1-native-cubic` beside
  `v1-native` in its smoke run. On the bench host the rebuilt stack
  logged `TCP congestion control: bbr` and `cubic`, and a quick run, not
  a measurement, recorded each series' own.
- **Decisions in shipping.** A setting, `congestion_control`, rather
  than BBR alone: an operator whose kernel lacks the module, or who
  wants the kernel's own, can say so, and the bench's baseline is
  occamyd's own image with one line of its file changed rather than a
  namespace's sysctl. Any name is accepted that has the form of one
  (lower case, digits and underscores, up to the kernel's 15 bytes),
  since only the kernel knows which it has, and only at listen. The
  listener rather than each connection: Linux copies a congestion
  control set on a listener to the connections it accepts
  (`tcp_ca_openreq_child`), as root and unprivileged, checked on 7.1.3
  for reno, cubic and bbr. A refusal is a warning, not an error, so a
  server on a kernel without BBR still serves, as the rule set before
  the run said. The v0 proxy's listener is left alone, since v0 is the
  Guacamole path being retired. Elsewhere than Linux occamyd listens as
  before and reports no congestion control.
- **Checked after the run.** Typing under N1 with 2% leaned against BBR,
  30.2 ms [21.6, 85.3] against 21.7 [21.5, 22.0], p 0.038 before the
  correction and 0.87 after; four of BBR's 8 runs were above 35 ms and
  none of CUBIC's, at loads from 2.3 to 6.5, so not the host. A
  diagnostic, three runs of that cell on each series, not a
  measurement, read each namespace's counters around it: 21.6 ms with
  BBR against 22.0, about 6.4 segments sent a key on both (1,925 and
  1,907 for 300 keys), 11 and 13 retransmissions, 3 and 6 loss probes,
  and no timeout. It did not repeat, and it is not explained.
- **What it tells 3b.** With BBR, what loss still costs a key during
  motion is about one round trip at the 95th percentile, the wait
  behind a lost packet, which 3b's delivery in any order removes; under
  N2 with 2% that is 149 and 163 ms against 95 and 114 without loss, and
  the 99th percentile is 170 and 175 against 112 and 125. Step 2's rule
  built step 3 because loss cost more than about one round trip; it
  now costs about one. The slowest keys of typing and of a still
  desktop wait for the tail loss probe, which no congestion control
  changes (Step 3c).
- **Surprises.** `comparisons.csv` had two columns named `factor`, the
  study's setting, such as `loss=2%`, and the estimate, so a CSV reader
  kept one of them; the estimate is now `times_better`, and the three
  reports on disk have the new header. The 99th percentile of typing
  and of a still desktop with 2% loss is not a retransmission timeout,
  as step 2's notes had it: each namespace counted 124 and 164 tail
  loss probes over the run and 6 and 9 timeouts, and the slow keys take
  264 to 274 ms under N1 and 354 to 385 under N2, three round trips and
  200 ms, the probe's wait for a lone packet in Linux
  (`tcp_schedule_loss_probe`: twice the smoothed round trip, plus the
  minimum retransmission timeout when one packet is in flight). Step
  2's "236 to 271 ms at either round trip" was a median over runs on
  either side of those keys.

## Affects

- Step 1: `internal/pacer`, `internal/stream`, `protocol/handshake.go`,
  `server/streams.go`, `client/client.go`,
  `client/occamy-web/src/protocol/session.ts`, `bench/`,
  `docs/protocol.md`, `docs/benchmarks.md`.
- Step 2: `bench/`, `docker/docker-compose.bench.yaml`.
- Step 3a: `bench/`, `docker/docker-compose.bench.yaml`,
  `docker/bench/`, `.github/workflows/dockerimage.yml`,
  `docs/benchmarks/realtime/3a`; `internal/config`, the public listener
  in `server/server.go` and `server/congestion*.go`,
  `docs/configuration.md` and `docs/architecture.md`.
- Steps 3b, 3c, 4 and 5 are scoped when they start.
