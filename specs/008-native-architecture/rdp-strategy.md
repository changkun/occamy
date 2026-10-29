---
title: RDP strategy
status: drafted
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
affects:
  - internal/backend/rdp/
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# RDP strategy

## Design Problem

RDP is the last protocol tying Occamy to guacd. Decide how Occamy reaches
RDP hosts after v1.0: keep the bridge, run a helper process around an
existing RDP stack, or implement RDP in Go. Only the last option makes
Occamy pure Go in the strict sense. This spec plans the evaluation that
decides.

## Context

- Through v1.0, RDP runs on guacd 1.6 (FreeRDP 2 or 3 inside guacd) via
  the bridge ([guacd-bridge](guacd-bridge.md)).
- A modern RDP client needs:
  - TLS and NLA (CredSSP with NTLMv2; Kerberos for domains);
  - the X.224, MCS and GCC connection sequence and licensing exchange;
  - fast-path input and output;
  - a bitmap codec path;
  - virtual channels for clipboard (CLIPRDR) and dynamic resize (DISP).
- Codec reality: the GFX pipeline prefers H.264 (AVC420/444). A client
  that does not advertise AVC still receives RemoteFX, planar or
  ClearCodec through GFX, or legacy bitmap updates (interleaved RLE,
  planar), at a bandwidth cost on high-motion content. Decoding H.264 in
  pure Go is not realistic, so a pure-Go client must not advertise it.
- Go RDP implementations exist (for example `tomatome/grdp`). Their
  maintenance and NLA coverage must be checked at evaluation time.

## Options

- **A. Keep the guacd bridge.** No work, but guacd stays in deployments
  that need RDP, so "pure Go" is never reached for RDP.
- **B. Helper process around FreeRDP 3.** A small C program per connection
  that speaks the backend interface over a Unix socket, with pixels in
  shared memory (see [session-core](session-core.md) question 7). Mature
  protocol coverage, including AVC. C, its build and FreeRDP's security
  record come back, though isolated per connection.
- **C. Helper process around IronRDP (Rust).** Same isolation as B on a
  memory-safe, actively developed stack. Its NLA, GFX and codec coverage
  must be verified, and it brings a Rust toolchain into the build.
- **D. Pure-Go RDP client in `internal/backend/rdp`.** Scope: TLS, NLA with
  NTLMv2, the connection sequence, fast-path, bitmap updates with
  interleaved RLE and planar decoding, GFX without AVC (RemoteFX, planar,
  ClearCodec), CLIPRDR and DISP. Kerberos, audio, drive redirection and
  RemoteApp come later.

## Evaluation plan

A time-boxed spike for D (target: two to four weeks of work), run any time
after session-core settles:

1. TLS and NLA, then a desktop bitmap stream from xrdp in the
   [006](../006-demo-stack-refresh.md) stack.
2. The same against Windows Server 2022 or 2025, and Windows 11, with NLA.
3. Measure bytes per second against guacd's RDP (AVC on) for the baseline
   scenarios.

Outcome:

- If the spike passes, D becomes the v1.x plan, and RDP follows the
  bridge's parity gate.
- If it fails, choose between B and C on NLA support, GFX coverage,
  licence compatibility with MIT, maintenance activity and build
  complexity. guacd then leaves the default deployment while `occamyd`
  stays pure Go, with RDP in an isolated helper.

## Open Questions

1. Is a pure-Go RDP worth its maintenance cost, or is "pure-Go `occamyd`
   plus an isolated helper for RDP" an acceptable end state?
2. Spike budget and owner: when does it run relative to beta.2?
3. Where does the Windows check run: a manual host, a self-hosted
   runner, or a cloud VM in CI?
4. Is the bandwidth penalty of no H.264 acceptable for the target use
   (administration rather than video), given the tile pipeline's lossy
   tier?
5. Is Kerberos (domain-joined targets) a requirement for the first native
   RDP release?

## Affects

Possibly a new `internal/backend/rdp`, or a helper binary and an IPC
definition in session-core. Decides when guacd leaves Occamy entirely.
