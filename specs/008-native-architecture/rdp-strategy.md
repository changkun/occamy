---
title: RDP strategy
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
affects:
  - internal/rdp/
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
- Go RDP code, as surveyed on 2026-10-30:
  - `tomatome/grdp` and most Go RDP forks are GPL-3.0, so no code from
    them can enter MIT `occamyd`, and the provenance of code in other
    projects matters.
  - `casvisor/gordp` (Apache-2.0, created 2026-10) and
    `bouncyball-git/gopher-rdp` (MIT, created 2026-02) do NLA and decode
    the GFX codecs (planar, ClearCodec, RemoteFX Progressive, ZGFX), by
    their own account. Both are young and self-verified. The spike
    borrowed nothing from them: every layer it needed was written from
    the specifications.
  - `github.com/oiweiwei/go-msrpc/ssp` (MIT, active) implements NTLM with
    signing and sealing, Kerberos and SPNEGO, which CredSSP needs.
    `Azure/go-ntlmssp` cannot: it has no key exchange or sealing. The
    spike uses `ssp/ntlm` for NTLMv2 and its sealing. That package
    imports `ssp/credential`, which brings two Kerberos stacks into the
    build (`jcmturner/gokrb5/v8` and `oiweiwei/gokrb5.fork/v9`, see
    Spike results).
  - IronRDP (Rust, MIT or Apache-2.0) is the reference for the protocol
    and its tests.

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
- **D. Pure-Go RDP client in `internal/rdp`, with a backend in
  `internal/backend/rdp`.** Scope: TLS, NLA with NTLMv2 on
  `go-msrpc/ssp`, the connection sequence, fast-path, bitmap updates with
  interleaved RLE and planar decoding, GFX without AVC (RemoteFX, planar,
  ClearCodec), CLIPRDR and DISP. Kerberos, audio, drive redirection and
  RemoteApp come later.

## Evaluation plan

A time-boxed spike for D (target: two to four weeks of work), run any time
after session-core settles:

1. TLS, then a desktop bitmap stream from xrdp in the bench desktop
   image (`docker/bench/desktop`, the scene program of
   [benchmark](benchmark.md)). xrdp 0.10.1 has no CredSSP, so NLA is
   checked against the [MS-NLMP] test vectors and an in-process CredSSP
   acceptor instead, and live only in step 2.
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

`internal/rdp`, the protocol client the spike built, and a backend in
`internal/backend/rdp` on it; or, if the spike fails, a helper binary
and an IPC definition in session-core. Decides when guacd leaves Occamy
entirely.

## Spike results

Steps 1 and 3 ran on 2026-10-30 against xrdp 0.10.1 (Debian trixie,
xorgxrdp) in the bench desktop image. Step 2 has not run: there is no
Windows host, and [#32](https://github.com/changkun/occamy/issues/32)
stays open.

| Criterion | Result |
|-----------|--------|
| TLS security negotiation with xrdp | Pass, live. |
| NLA (CredSSP with NTLMv2) | Unverified live; passes the [MS-NLMP] 4.2.4 vectors and a round trip against an in-process acceptor. |
| Connection sequence through capabilities and licensing | Pass, live, at 32, 24, 16 and 15 bits per pixel. |
| Bitmap updates decoded to the scene's pixels | Pass, live: every pixel of the 1024×768 wallpaper, exact at 32 and 24 bits per pixel, within 8 per channel at 16 and 15. |
| Keyboard and mouse input reach the session | Pass, live: keys paint the probe swatch; the pointer lands where it was sent, on the session's X display. |

- **What was built.** `internal/rdp`, a pure-Go RDP client of about
  3,500 lines plus 2,000 lines of tests. It contains:
  - X.224 negotiation, TLS, and CredSSP version 6 with NTLMv2 (TSRequest
    in `encoding/asn1`, NTLM and its sealing from `go-msrpc/ssp/ntlm`,
    the public key bound by the SHA-256 hash of [MS-CSSP] 3.1.5,
    PROTOCOL_HYBRID_EX's early user authorization result);
  - MCS and GCC in BER and PER, with channel joins;
  - the Client Info PDU with automatic logon;
  - licensing, but only the server's STATUS_VALID_CLIENT shortcut;
  - Demand Active and Confirm Active, connection finalization, and
    reactivation after a Deactivate All (written, not yet exercised);
  - fast-path and slow-path updates with fragment reassembly;
  - bitmap updates, uncompressed, interleaved RLE (8, 15, 16 and 24 bits
    per pixel) and planar (32 bits per pixel);
  - pointer shapes (colour, new and large, cached, system) and position;
  - fast-path and slow-path keyboard (scancodes and Unicode) and mouse
    input, wheel included, with keysym and USB HID usage tables for a US
    layout;
  - Suppress Output and Refresh Rect PDUs, for the backend's `Demand`.
- **Placement.** The protocol is its own package, as `internal/guacd`
  is beside `internal/backend/guacd`. `Client.Run` draws into an
  `Output`, a subset of `backend.Sink`, so a session's sink plugs in as
  it is. The backend in `internal/backend/rdp` is not built yet: it maps
  `backend.InputEvent` to the client's input methods and `Demand` to
  Suppress Output.
- **Decisions.**
  - The client asks for bitmap updates only. It advertises no drawing
    orders, no bitmap, glyph or brush caches, no bulk compression, no
    bitmap codecs and no virtual channels. A server then has nothing to
    send but bitmap updates, so two codecs cover the whole screen and the
    client keeps no cache state.
  - Only TLS-based security is offered (SSL, HYBRID, HYBRID_EX). A server
    that selects standard RDP security is refused, since RC4 with the
    server's proprietary certificate is not worth implementing.
  - NTLM comes from `go-msrpc/ssp/ntlm`, used directly: its
    Authentifier gives the three messages, the checksum, the RC4 seal
    and the signature, which is all GSS_WrapEx needs. Without SPNEGO,
    the NTLM tokens go straight into the TSRequest, as mstsc and FreeRDP
    send them when Kerberos is off.
  - Keys go out as scancodes, which the server maps through the keyboard
    layout the client announced. A keysym with no key on the US layout
    goes out as a Unicode keyboard event.
- **What xrdp sends.** With the capabilities above, xrdp 0.10.1 sends
  only fast-path and slow-path bitmap updates. They are planar (RLE
  planes, no alpha, no colour loss) at 32 bits per pixel, and
  interleaved RLE at 24, 16 and 15. It sends no orders and no surface
  commands. The first screen at 32 bits per pixel is 192 bitmaps in
  79 KB of PDUs; at 24 bits it is 154 bitmaps in 12 KB. xrdp's build
  has RemoteFX (`--enable-rfxcodec`) but no x264, so it would send
  RemoteFX through surface commands to a client that advertises the
  codec, and RemoteFX Progressive through GFX. It never sends H.264.
  NSCodec is not in xrdp at all. The GFX steps of option D can therefore
  be developed against xrdp for RemoteFX only. Planar and ClearCodec
  under GFX need Windows.
- **Surprises.**
  - xrdp has no CredSSP. Offered SSL, HYBRID and HYBRID_EX, it selects
    SSL. Offered HYBRID alone, it selects standard RDP security (protocol
    0) rather than failing the negotiation.
  - `go-msrpc/ssp/ntlm` imports `ssp/credential`, which imports two
    Kerberos libraries for its keytab and ccache types. The build gains
    `jcmturner/gokrb5/v8`, `oiweiwei/gokrb5.fork/v9`,
    `jcmturner/aescts/v2`, `jcmturner/gofork`, `hashicorp/go-uuid`,
    `oiweiwei/go-oem` and `indece-official/go-ebcdic`. Their licences
    are Apache-2.0, BSD-3-Clause, MPL-2.0 (go-uuid) and MIT; go-licenses
    accepts them. Kerberos is still not used (decision 4), but its code
    is linked. If that weight matters, NTLMv2 with sealing is about 400
    lines and the vectors in `credssp_test.go` would carry over.
  - go-msrpc's NTLMv2 adds target name and channel binding AV pairs to
    the client's blob. The worked example of [MS-NLMP] 4.2.4.2.2 has
    neither, so its NTProofStr cannot be reproduced byte for byte. The
    test checks the response against the formulas of 3.3.2 instead.
- **Verification.**
  - The integration test, under the `rdp_integration` build tag, builds
    the bench desktop image, starts xrdp, and connects at each colour
    depth. It compares every pixel with the scene's wallpaper, paints the
    probe swatch with keys, and asks the session's X server
    (`testdata/querypointer`) where the pointer is.
  - It records xrdp's frames to `testdata/xrdp-*.trace.gz` (16 KB in
    all). `TestReplay` plays them back from a fake server over TLS
    without a container.
  - `TestReplayNLA` puts the same server behind CredSSP, with
    go-msrpc's NTLM acceptor, for HYBRID and HYBRID_EX. It checks the
    delegated credentials and a wrong password's STATUS_LOGON_FAILURE.
  - Unit tests cover every RLE order, planar through a test encoder, the
    MCS PDUs against [MS-RDPBCGR] 4.1, and input encoding.
  - Five fuzzers (fast-path, slow-path, RLE, planar, pointer), seeded
    from the recordings, ran 20 seconds each without a finding.
- **What the NLA result rests on.** The [MS-NLMP] 4.2.4 vectors check
  NTOWFv2, the LMv2 response, the encrypted session key, the sealing
  and signing keys, and the sealed message and its signature. The
  TSRequest framing, the public key binding and the error path are
  checked only against an acceptor built from the same library and this
  package's own TSRequest code. They are self-consistent, not
  interoperable, until a Windows host answers.
- **What remains for a beta-quality backend.**
  - The backend in `internal/backend/rdp`: Run, input mapping, `Demand`
    to Suppress Output, credentials from the registry.
  - Step 2 against Windows with NLA, and with it the parts xrdp cannot
    exercise. These are the full licensing exchange (Windows Server with
    an RDS licensing server), server redirection, and the auto-detect
    and multitransport requests, which the client now skips.
  - Answers to auto-detect requests (a server may lower quality
    without them), and the Heartbeat PDU.
  - CLIPRDR and DISP over static virtual channels and DRDYNVC; dynamic
    resize needs DISP.
  - GFX without AVC (RemoteFX, RemoteFX Progressive, planar, ClearCodec,
    ZGFX). Step 3 shows this is required, not optional.
  - Pacing of bitmap updates. Bitmap updates have no frame
    acknowledgement, so the backend has to pace xrdp itself: Suppress
    Output while the session is behind, or the Frame Acknowledge
    capability with surface commands.
  - Scrolls as screen-to-screen copies. Advertising the ScrBlt order and
    applying it with `Sink.CopyRect` should make G3 cheap; whether
    xorgxrdp sends it is unchecked.
  - Keyboard layouts other than US English, and keysyms that need
    AltGr.
  - Auto-reconnect cookies and the Save Session Info PDU.
- **Step 3: bytes on the RDP link.** `TestBytesAgainstGuacd` (under the
  `rdp_integration` tag) drives the bench scenes through this client and
  through guacd 1.6's FreeRDP against the same xrdp. It reads the bytes
  the xrdp container sent, TCP and TLS included. Each series is the
  median of three runs; host load was 4 to 8. guacd's FreeRDP
  negotiates the GFX pipeline with RemoteFX Progressive, which is lossy;
  xrdp's log says so. The step was meant to compare against guacd with
  AVC, but this xrdp build has no H.264 encoder, so RemoteFX Progressive
  is the baseline. The client's bitmap updates are lossless and unpaced,
  so the comparison is not like for like. guacd's lower rates also
  include the frames that GFX flow control kept xrdp from sending.

  | KB/s from xrdp | G1 idle | G3 scroll | G4 drag | G5 motion | G6 photo (5 s) |
  |----------------|--------:|----------:|--------:|----------:|---------------:|
  | guacd, GFX RemoteFX Progressive, 32 bpp | 1.3 | 11,502 | 1,008 | 1,316 | 33 |
  | native, planar, 32 bpp | 7.4 | 19,080 | 13,048 | 49,769 | 512 |
  | guacd, GFX RemoteFX Progressive, 16 bpp | 1.3 | 11,459 | 1,015 | 1,313 | 30 |
  | native, interleaved RLE, 16 bpp | 0.3 | 10,253 | 7,056 | 30,217 | 315 |

  Native idle at 32 bpp is one full-screen redraw (79 KB) arriving
  inside the window, not a steady rate. Full-screen motion at 30 fps
  costs 50 MB/s (400 Mbit/s) over bitmap updates, 38 times guacd's
  link. The drag costs 13 times guacd's link, and the photograph 15
  times. Only scrolling is close, because RemoteFX Progressive does
  poorly on scrolled text too. Bitmap updates are fine for a LAN
  between `occamyd` and a lightly used desktop. For motion they are not
  a beta-quality path.
- **Verdict so far.** Step 1 passed in one working session, decoding
  exactly and fuzzing clean. Nothing in it argues for a helper process.
  Step 3 confirms what option D already scopes: GFX with RemoteFX
  Progressive and flow control is required work for beta, not a later
  optimization. Every remaining protocol risk is on the Windows path:
  live NLA, licensing and the message channel. More xrdp work cannot
  retire those, so the decision between D and a helper waits on
  step 2.
