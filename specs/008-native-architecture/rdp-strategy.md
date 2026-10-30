---
title: RDP strategy
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
affects:
  - internal/backend/rdp/
effort: large
created: 2026-10-29
updated: 2026-10-30
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
- Since v0.5.0, RDP through guacd is tested end to end against xrdp only.
  That needs `security` left unset, so that TLS is negotiated, and
  `ignore-cert`. Nobody has checked a Windows host with NLA yet; that
  check is open on #32. Step 2 of the evaluation plan below needs a
  Windows host too, so the two checks can share one.
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

## Decisions

Settled on 2026-10-30. Items 1 and 2 were the owner's calls; 3 and 4
follow the recommendations.

1. **D, pure-Go RDP, decided by the spike first.** The end state is an
   `occamyd` with no Guacamole and no cgo for every protocol. If the
   spike fails, the fallback is a helper process (B or C), which is still
   free of Guacamole; guacd does not stay for RDP.
2. **No H.264.** Codecs stay pure Go ([graphics-pipeline](graphics-pipeline.md)
   decision 1); RDP uses the bitmap and GFX codecs without AVC, and the
   lossy tier covers motion.
3. **The spike runs during beta.1,** as soon as the backend interface in
   [session-core](session-core.md) exists, in parallel with the other
   beta.1 work. It needs nothing else from them.
4. **No Kerberos in the first native RDP release.** NTLMv2 through
   CredSSP covers local and most domain accounts; Kerberos follows when a
   deployment needs it.

## Open Questions

1. Where does the Windows check run: a manual host, a self-hosted
   runner, or a cloud VM in CI? The spike's second step needs Windows
   Server or Windows 11 with NLA, and so does
   [#32](https://github.com/changkun/occamy/issues/32).

## Affects

Possibly a new `internal/backend/rdp`, or a helper binary and an IPC
definition in session-core. Decides when guacd leaves Occamy entirely.
