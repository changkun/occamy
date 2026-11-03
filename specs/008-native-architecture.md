---
title: Occamy v1 architecture
status: drafted
track: architecture
depends_on: []
affects:
  - server/
  - internal/
  - protocol/
  - client/
  - docs/
effort: xlarge
created: 2026-10-29
updated: 2026-11-03
author: Changkun Ou
dispatched_task_id: null
---

# Occamy v1 architecture

## Overview

Everything Occamy has shipped so far (tags `0.1.0` to `v0.5.0`) is a proof
of concept: a Guacamole-protocol proxy, around libguac up to `0.4.0` and
around guacd since `v0.5.0` ([005](005-upstream-guacamole.md)). This spec
designs **Occamy v1**: one pure-Go server with its own protocol and native
backends. The first release target is **v1 beta**. The spec defines the
beta scope, the path from guacd to pure Go, and the sub-designs, which are
broken out as children.

The recommendation not to reimplement libguac stands. libguac is shaped
around Guacamole's drawing-instruction protocol, and a port would carry
over that protocol's costs (below) without its ecosystem. v1 is a smaller
core built around two content classes, terminals and pixels. Guacamole
was kept as a bridge, an explicit fallback and the benchmark's baseline
through beta.1. From beta.2 occamyd has none of it, and guacd remains
only as the benchmark's baseline (the owner, 2026-11-03; v1.0.0-beta.2
scope).

## Versioning

- **v0** is the PoC line: the Guacamole protocol on `/api/v0/login` and
  `/api/v0/connect` (`/api/v1/…` through 0.6.0, though the design was never
  v1). It makes no compatibility promise. Its tags up to `0.4.0` lack the `v` prefix, so
  the Go toolchain ignores them; from `v0.5.0` on, tags carry it.
- **v1** starts with `v1.0.0-beta.1`. Betas may still break the API and
  the protocol. From the release candidate on, both are frozen, and later
  v1 changes are additive and negotiated through capabilities. The module
  path stays `changkun.de/x/occamy` (no `/v2` suffix needed for v1).
- What happens to the v0 routes is decided in
  [api-and-server-stack](008-native-architecture/api-and-server-stack.md):
  kept behind `legacy_v0` through beta.1, and removed in beta.2 with the
  Guacamole protocol they serve (the owner, 2026-11-03).

## v1 beta scope

In scope:

1. `occamyd` as a single binary built with `CGO_ENABLED=0`.
2. API v1: authentication, connection registry, sessions, sharing.
3. Occamy protocol v1 over binary WebSocket frames.
4. Session core: one owner per session, per-viewer pacing, explicit
   sharing roles.
5. Graphics pipeline: framebuffer, damage tracking, lossless and lossy
   tiles.
6. Native SSH backend, with terminals rendered by the client, and, for
   v1.0, a native Kubernetes backend (pod exec) on the same terminal
   path ([native-kubernetes-backend](008-native-architecture/native-kubernetes-backend.md)).
7. Native VNC backend, vnc's default (the owner's decision of
   2026-10-31), with the guacd bridge as an explicit fallback.
8. Native RDP backend, rdp's default since 2026-10-31, ahead of rc.1,
   with the guacd bridge as an explicit fallback; checked against xrdp
   and on a Windows Server 2025 CI runner, not yet on a real Windows
   host. Its graphics pipeline is on by default since phase 3's second
   part, with RemoteFX Progressive verified against xrdp and RemoteFX
   against the specification's vectors, and ClearCodec and the cache to
   come, which block v1.0: without them that Windows server's screen
   stays nearly black under GFX.
   telnet, which gets no native backend, goes through guacd until guacd
   is removed. Where a native backend exists it is the default,
   and guacd serves that protocol only with an explicit `backend: guacd`.
   (2026-11-03: beta.2 removes the fallback and telnet; see
   v1.0.0-beta.2 scope.)
9. Clients: the web client on protocol v1 ([009](009-react-web-client.md))
   and the Go client core, which the end-to-end tests and the Swift client
   ([010](010-native-swift-client.md)) build on. The Swift client is a
   preview by rc.1 and does not gate beta.1. (2026-11-03: it is part of
   beta.2; see v1.0.0-beta.2 scope.)

Out of scope for beta.1: video encoding (H.264 and similar), audio, file
transfer and SFTP, a recording player, WebTransport, native RDP beyond
its registration (GFX and Windows), and multi-node deployment.
WebTransport and a video path for motion are steps of the
[real-time transport](008-native-architecture/realtime-transport.md),
which gate no release and land in the next once measured and kept.
(2026-11-03: native RDP's GFX shipped in beta.1; session recording with
a player, WebTransport and the video path are planned for beta.3; see
v1.0.0-beta.2 scope.)

Release gate for `v1.0.0-beta.1`, once the beta.1 items below are in:

- The end-to-end suite passes for every beta.1 backend, covering
  authentication, resize, clipboard, sharing (owner, viewer, read-only)
  and reconnect.
- Protocol decoders are fuzzed in CI (`.github/workflows/fuzz.yml`).
- A throttled viewer does not lower another viewer's frame rate on the
  same session (`TestSlowViewer` in `internal/stream`, on a fake clock:
  nothing waits for the viewer behind; what encoding for it costs in CPU
  is for the benchmark).
- Traffic and CPU are measured against the v0 baselines (see Performance).
  **Waived for beta.1** by the owner on 2026-11-02: beta.1 ships before
  the benchmark's first full run. v1.0 still waits for it (Path to pure
  Go).
- The [004](004-security-hardening.md) properties hold by design: no
  credentials in tokens or logs, an allowlisted connection registry, and
  no debug endpoints on the public listener.
- Decoding is bounded for the process, not only per session: a native
  RDP session may take about a core, so one user's
  `limits.sessions_per_user` sessions to hostile servers would take that
  many cores ([native-rdp-backend](008-native-architecture/native-rdp-backend.md),
  phase 3's follow-ups). The owner confirmed this gate on 2026-11-01.
  **Done** on 2026-11-01: `limits.decode_cores`, half of `GOMAXPROCS` by
  default, shared evenly by the sessions that want more than it has, and
  every GFX PDU charged, including those applied that do little, and
  every frame the client reads, whatever it does with it.
- `docs/protocol.md` and the API reference are complete.

## v1.0.0-beta.2 scope

The owner's plan of 2026-11-03, with the owner's answers the same day:

1. **No guacd in the product.** occamyd drops the guacd bridge
   (`backend: guacd` and `internal/backend/guacd`), the v0 routes with
   the Guacamole protocol they serve, and telnet, which has no native
   backend and which nobody uses. The demo stack loses its guacd
   service. guacd stays only in the benchmark, for the `guacd` series:
   Guacamole's protocol straight from guacd, against Occamy's native
   series. Telnet is not benchmarked. This supersedes the rule of
   2026-10-31 that deleted a protocol's guacd path only once the
   benchmark showed native 10x better; the 10x rule remains the target
   of the stable release (Path to pure Go).
2. **The real-time work measured and kept so far ships, the rest waits
   for beta.3.** In beta.2: the frame window from the path and BBR
   ([realtime-transport](008-native-architecture/realtime-transport.md),
   steps 1 and 3a), the cheaper classification of moving tiles and the
   scaled codec for tiles in motion
   ([graphics-pipeline](008-native-architecture/graphics-pipeline.md)).
   In beta.3: steps 3b (versioned tiles over WebTransport), 3c (the wait
   for a lone lost packet), 4 (cadence for watchers) and 5 (a video
   path), prediction below the round trip, and a scale factor that
   adapts. The essential comparison
   ([comparison-benchmark](008-native-architecture/comparison-benchmark.md))
   runs again on the beta.2 image, `guacd` against `v1-native`, and its
   numbers are the release's.
3. **A macOS app** ([010](010-native-swift-client.md)): as simple as
   VNC Viewer, in the style of macOS 26. It is signed and notarized with
   the owner's Apple developer account once it is ready.

What removing the fallback costs, judged here rather than by the owner:
native RDP has not met a real Windows host (#32). On a Windows Server
2025 CI runner its graphics pipeline left the screen nearly black,
since every bitmap came as ClearCodec, which it does not decode yet, so
Windows hosts need `disable_gfx: true`, with which the whole desktop
drew correctly; the clipboard failed both ways there
([native-rdp-backend](008-native-architecture/native-rdp-backend.md)).
In beta.1 a Windows host could fall back to guacd; in beta.2 the
release notes and the configuration reference say to set
`disable_gfx`. ClearCodec and the bitmap cache remain a v1.0 blocker,
planned for beta.3.

Two controls existed only through guacd, so they go with it
(docs/configuration.md lists both as reasons to keep `backend: guacd`):

- **Restricting the clipboard** (guacd's `disable-copy` and
  `disable-paste`). The native backends send the server's clipboard to
  every viewer and the owner's and interactive viewers' to the server.
  A connection key that restricts each direction, enforced by the
  session core, is small and is planned for beta.2, so that removing
  guacd removes no security control.
- **Recording sessions** (guacd's `recording-path`). The native
  backends record nothing yet. Recording needs a format and a player,
  which is a larger piece, planned for beta.3; until then occamyd
  refuses a connection that still sets guacd's recording parameters,
  rather than serving it unrecorded.

## Path to pure Go

"Pure Go" means `occamyd` builds with `CGO_ENABLED=0` **and** no protocol
needs an external process. 005 delivered the first half in v0.5.0: C left
`occamyd`'s process, and guacd became a sidecar. The second half replaces
guacd one protocol at a time. Each switch is gated by the parity criteria
in [guacd-bridge](008-native-architecture/guacd-bridge.md). The goal, the
owner's call on 2026-10-30, is a v1.0 with no Guacamole anywhere: neither
guacd nor the Guacamole protocol. RDP is native if its spike passes; if
not, an isolated helper process takes its place, the one exception to
"no external process", and still free of Guacamole.

The owner's calls on 2026-10-31 sharpen this. Native is the default
wherever a native backend exists, so VNC is native by default now, and
the parity criteria no longer gate a switch of default. guacd stays only
as an explicit `backend: guacd` fallback and as the benchmark's
baseline, and is deleted per protocol once the benchmark shows the
native backend at least 10x better than the guacd baseline on each
scenario's primary metric and the parity matrix passes
([guacd-bridge](008-native-architecture/guacd-bridge.md), Parity
gate). RDP is native in v1.0, with no helper
([rdp-strategy](008-native-architecture/rdp-strategy.md) decision 5),
and Windows is checked later, once a host is available. v1.0 also needs
a native Kubernetes backend (pod exec), specified in
[native-kubernetes-backend](008-native-architecture/native-kubernetes-backend.md)
and never served through guacd, and the React web client on protocol
v1. Telnet gets no native backend: it
stays on guacd and is dropped when guacd is removed. A further call
that day (owner, 2026-10-31): v1.0 waits until the benchmark shows
native at least 10x better than guacd. It does not ship with a guacd
fallback for any protocol still short of 10x; release candidates may
still carry one ([benchmark](008-native-architecture/benchmark.md),
Targets).

The owner's plan of 2026-11-03 removes guacd from occamyd in beta.2,
for every protocol and whatever the benchmark shows, and telnet with
it; guacd remains the benchmark's baseline. The 10x rule stays as the
target of the stable release: v1.0 waits until the benchmark shows
native at least 10x better than the guacd baseline on each scenario's
primary metric, or a miss is justified by a bound Occamy does not
control ([benchmark](008-native-architecture/benchmark.md), Targets).

| Milestone | Delivers | guacd needed for |
|-----------|----------|------------------|
| v0.5.0 ([005](005-upstream-guacamole.md), released 2026-10-29) | `occamyd` without cgo; everything through guacd | VNC, SSH, RDP |
| v1.0.0-beta.1 (released 2026-11-02) | API v1, protocol v1, session core, graphics pipeline, guacd bridge, native SSH, native VNC as vnc's default, native RDP as rdp's default (not yet checked against Windows), web client on v1 | VNC and RDP only with `backend: guacd`; telnet |
| v1.0.0-beta.2 | No guacd, no Guacamole protocol, no v0 routes and no telnet in occamyd; real-time steps 1 and 3a and the scaled codec; the essential comparison on its image; the macOS app ([010](010-native-swift-client.md)) | nothing: guacd is the benchmark's baseline only |
| v1.0.0-beta.3 | Real-time steps 3b, 3c, 4 and 5; native RDP's ClearCodec and bitmap cache | nothing |
| v1.0.0-rc.1 | API and protocol frozen; the Windows check runs once a host is available (#32), as [rdp-strategy](008-native-architecture/rdp-strategy.md) decision 5 says | nothing |
| v1.0.0 | The benchmark shows native at least 10x better than the guacd baseline on each scenario's primary metric, or a justified miss | nothing |
| v1.x | Kerberos, audio and the rest of RDP's later scope per [rdp-strategy](008-native-architecture/rdp-strategy.md) | nothing |

The client side is a separate matter. The Swift app links the Go client
core through cgo's `c-archive` export ([010](010-native-swift-client.md)).
That is an intentional boundary inside one client, not a dependency of the
server.

## What the v0 design costs

- **C in-process** (up to `0.4.0`). The libguac fork ran inside
  `occamyd` through cgo, so a memory bug crashed every session: the two
  double frees fixed during the revival, plus #15, #17, #20 and #22. 005
  removed it in v0.5.0.
- **Protocol.** Text instructions carry image data as base64 PNG, JPEG or
  WebP in `blob` instructions. That adds a third to the encoded size and
  forces text WebSocket frames. Clients must implement Guacamole's layers
  and drawing model (the 9k-line fork of guacamole-common-js that the Vue
  client carried as `occamy.js`, until [009](009-react-web-client.md)).
- **Terminals as pixels.** SSH output is emulated server-side and drawn
  with Pango and Cairo into PNG tiles. Traffic scales with pixels, not
  characters, and clients cannot select text or choose fonts.
- **Implicit sharing.** Identical credentials silently join one session
  (`config.JWT.GenerateID`).
- **External process.** Since v0.5.0, every connection needs guacd.

## Architecture

```mermaid
flowchart LR
  W[Web client] -- protocol v1 --> GW
  SW[Swift client] --> CC[Go client core] -- protocol v1 --> GW
  GW[API v1: auth, registry, sessions] --> SE[Session owner]
  SE --> TB[Terminal backends: SSH, Kubernetes]
  SE --> GB[Graphical backends: VNC, RDP]
  SE --> BR[guacd bridge]
  GB --> FB[Framebuffer and damage]
  BR --> FB
  FB --> EN[Tile encoder, per-viewer pacing]
```

Principles:

1. **One pure-Go binary.** Anything that needs C runs in a separate
   process behind the backend interface, so a crash ends one connection,
   not the server.
2. **Terminals stay text.** Terminal backends stream bytes and clients
   render them; only graphical backends produce pixels.
3. **One owner per session.** One goroutine owns the backend connection
   and its state. Viewers have independent pacing: a slow viewer gets
   coalesced updates and never blocks the backend or other viewers.
4. **Binary, versioned protocol** with capability negotiation.
5. **Compatibility is an adapter**, not the core: the guacd bridge is a
   backend like any other.

## Components

Each component is a sub-design (see Design Breakdown):

- **API and server stack:** HTTP API v1, authentication, connection
  registry, sharing links, configuration, fate of the v0 routes, and the
  choice between gin and the standard library.
- **Protocol v1:** framing, hello and capabilities, message catalogue,
  flow control, errors and versioning rules; `docs/protocol.md`; a public
  Go package shared by server and clients.
- **Session core:** the backend interface, the session owner, viewer
  fan-out and pacing, late-join snapshots, roles and lifecycle.
- **Graphics pipeline:** framebuffer format, damage tracking, tile
  classification, codecs, and per-viewer quality.
- **guacd bridge:** runs guacd connections under protocol v1 and defines
  the parity criteria for replacing it per protocol.
- **Native SSH, native VNC:** the first two native backends.
- **Native Kubernetes backend:** a terminal in a pod through the API
  server's exec stream, the second terminal backend, with no guacd
  path.
- **RDP strategy:** guacd, helper process, or pure Go, decided by
  evaluation (decided: pure Go).
- **Native RDP backend:** the backend over the strategy's pure-Go
  client, then virtual channels, GFX without AVC, and the Windows check.
- **Go client core:** a pure-Go protocol v1 client library for tests,
  tools and the Swift app.

## Performance

v1 has two performance targets, defined and measured by
[benchmark](008-native-architecture/benchmark.md):

- at least 10x better than the v0 Guacamole baseline on each scenario's
  primary metric, under the same network conditions;
- input-to-update latency p95 at most 50 ms for desktop interactions at
  20 ms RTT, including while the screen is in full motion.

The baseline and the per-scenario targets are in
[docs/benchmarks.md](../docs/benchmarks.md), and the designs below are
judged against them. Where they come from:

- Terminals send bytes, not PNG tiles of rendered text.
- Binary frames remove base64's 4/3 expansion.
- Encoding is limited to damaged tiles and spread across cores.
- Small messages never wait behind tiles, and per-viewer windows keep
  queues short, so a keystroke's update is not stuck behind bulk frames.
- Native backends remove guacd's encode and the bridge's decode and
  re-encode.

Each beta reruns the benchmark, which also covers #18.

## Testing Strategy

- Backend conformance against real servers in the
  [006](006-demo-stack-refresh.md) stack (x11vnc, TigerVNC, OpenSSH,
  xrdp). The guacd bridge's parity matrix runs there for VNC
  ([guacd-bridge](008-native-architecture/guacd-bridge.md)).
- Protocol: golden encode/decode tests, and `go test -fuzz` on every
  decoder.
- Encoder: lossless tiles must round-trip exactly; lossy tiles meet a
  quality threshold.
- Load: a pressure test on the v1 session stream, including a deliberately
  slow viewer that must not stall the others.

## Acceptance Criteria

- [ ] v1 beta scope and milestones agreed.
- [ ] Every sub-design validated and broken into tasks.
- [x] Guacamole baseline recorded with the
  [benchmark](008-native-architecture/benchmark.md): terminals on master
  after v0.5.0, desktops on v0.6.0
  ([docs/benchmarks.md](../docs/benchmarks.md)).
- [ ] v1 meets the benchmark's 10x and 50 ms targets, or each miss is
  justified there by a bound Occamy does not control.
- [ ] The 8-hour soak passes before v1.0: a stability gate, run once
  with 20 sessions, and not a row in the benchmark's series (the owner's
  decision of 2026-11-01;
  [guacd-bridge](008-native-architecture/guacd-bridge.md),
  Implementation notes: VNC's soak).
- [x] RDP strategy decided after evaluation: pure Go, after the spike's
  steps 1 and 3 against xrdp
  ([rdp-strategy](008-native-architecture/rdp-strategy.md) decisions 1
  and 5).

## Design Breakdown

| # | Sub-design | Design problem | Depends on | Effort | Status |
|---|-----------|---------------|-----------|--------|--------|
| 1 | [API and server stack](008-native-architecture/api-and-server-stack.md) | HTTP API v1, auth, registry, v0 routes, gin or stdlib | — | large | validated |
| 2 | [Protocol v1](008-native-architecture/protocol-v1.md) | Wire format, capabilities, flow control, versioning | — | large | complete |
| 3 | [Session core](008-native-architecture/session-core.md) | Backend interface, session owner, viewer pacing | — | large | validated |
| 4 | [Graphics pipeline](008-native-architecture/graphics-pipeline.md) | Framebuffer, damage, codecs, quality | protocol-v1, session-core | large | validated |
| 5 | [Native SSH backend](008-native-architecture/native-ssh-backend.md) | SSH, terminal state, host keys | protocol-v1, session-core | medium | complete |
| 6 | [guacd bridge](008-native-architecture/guacd-bridge.md) | guacd under protocol v1; parity gates for removal | session-core, graphics-pipeline, 005 handshake client | large | validated |
| 7 | [Native VNC backend](008-native-architecture/native-vnc-backend.md) | RFB client: security types, encodings | session-core, graphics-pipeline | large | validated |
| 8 | [Go client core](008-native-architecture/client-core.md) | Client library shaped for tests and a C ABI | protocol-v1, graphics-pipeline | medium | validated |
| 9 | [RDP strategy](008-native-architecture/rdp-strategy.md) | guacd, helper process, or pure-Go RDP | session-core | large | validated |
| 10 | [Protocol benchmark](008-native-architecture/benchmark.md) | Yardstick: scenarios, metrics, the Guacamole baseline, the 10x and 50 ms targets | — | large | complete |
| 11 | [Native RDP backend](008-native-architecture/native-rdp-backend.md) | The backend over the RDP spike's client; channels, GFX without AVC, the Windows check | session-core, graphics-pipeline, rdp-strategy | large | validated |
| 12 | [Native Kubernetes backend](008-native-architecture/native-kubernetes-backend.md) | A pod's terminal over the API server's exec channel protocols; credentials, certificate, names | session-core, native-ssh-backend, api-and-server-stack | medium | validated |
| 13 | [Comparison benchmark](008-native-architecture/comparison-benchmark.md) | Guacamole against Occamy across workloads, networks, viewers and server load, collected in one go and analysed with tests and intervals | benchmark | xlarge | validated |
| 14 | [Real-time pacing and transport](008-native-architecture/realtime-transport.md) | Frames paced by the path, not by acknowledgements; loss in the benchmark; order-independent tiles over WebTransport; cadence for watchers; a video path for motion; each step measured | protocol-v1, session-core, comparison-benchmark | xlarge | validated |

```mermaid
graph LR
  P[2 protocol v1] --> G[4 graphics pipeline]
  S[3 session core] --> G
  P --> SSH[5 native SSH]
  S --> SSH
  S --> B[6 guacd bridge]
  G --> B
  S --> VNC[7 native VNC]
  G --> VNC
  P --> CC[8 Go client core]
  G --> CC
  S --> RDP[9 RDP strategy]
  RDP --> NRDP[11 native RDP]
  G --> NRDP
  BM[10 benchmark] --> G
  A[1 API and server stack] --> K8S[12 native Kubernetes]
  S --> K8S
  SSH --> K8S
  BM --> CMP[13 comparison benchmark]
  P --> RT[14 real-time pacing and transport]
  S --> RT
  CMP --> RT
```

**Recommended iteration order:** #10 first, so that the Guacamole
baseline exists before any encoding or pacing is built. Alongside it, #1,
#2 and #3, the foundations; the protocol is the longest-lived contract.
Then #4 and #5. Then #6, #7 and #8. The #9 evaluation spike can run at any
time alongside; #11 follows it. #12 follows #5, whose terminal path it
shares. #14 follows #13's essential run, one step at a time,
each measured before the next starts.
