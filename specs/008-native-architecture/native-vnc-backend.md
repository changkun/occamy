---
title: Native VNC backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/graphics-pipeline.md
affects:
  - internal/backend/vnc/
effort: large
created: 2026-10-29
updated: 2026-10-30
author: Changkun Ou
dispatched_task_id: null
---

# Native VNC backend

## Design Problem

Replace guacd for VNC with a pure-Go RFB client that writes into the
session framebuffer. Three choices:

- which security types and encodings beta supports;
- whether to adopt an existing Go library or write the client;
- how resize, clipboard and keyboard map onto protocol v1.

## Context

- RFB is specified in RFC 6143, with community extensions documented in
  the rfbproto repository. libvncclient's double frees during the revival
  ([001](../001-status-quo.md)) show the cost of the C path.
- No maintained Go RFB client covers the needed encodings, as surveyed
  on 2026-10-30. `mitchellh/go-vnc` is archived and Raw-only,
  `kward/go-vnc` is minimal, and `unistack-org/go-rfb` lacks ZRLE and
  Hextile. `amitbet/vnc2video` (MIT) decodes Raw, CopyRect, RRE,
  Hextile, Zlib, ZRLE, Tight and the cursor pseudo-encodings, but has had
  no commits since 2019. None implement ExtendedDesktopSize. The client
  is therefore written in `occamyd` (decision 1), with these libraries
  and `amitbet/vnc2video`'s decoders as references only.
- Everything needed is in the standard library: `crypto/des` for VNC
  authentication, `crypto/tls` for VeNCrypt, `compress/zlib` for ZRLE and
  Tight, `image/jpeg` for Tight JPEG, and `crypto/aes` plus `math/big` for
  Apple Remote Desktop authentication.
- Test servers: x11vnc in the [006](../006-demo-stack-refresh.md)
  stack, and TigerVNC, to be added to it; QEMU's VNC server; macOS Screen
  Sharing, checked by hand.

## Proposed scope for beta

The scope is delivered in four phases (see Task Breakdown).

- **Security types:** None (1), VNC Authentication (2), VeNCrypt (19)
  with TLS and X.509 subtypes (Plain, None and VNC inner auth), and Apple
  Remote Desktop (30), so that macOS hosts work. RA2 and other proprietary
  types are out.
- **Encodings:** Raw, CopyRect, Hextile, ZRLE and Tight (including JPEG and
  the gradient filter).
- **Pseudo-encodings:** DesktopSize, ExtendedDesktopSize (with
  `SetDesktopSize` for client-driven resize), Cursor, LastRect,
  ContinuousUpdates with Fence, QEMU Extended Key Event (physical key
  codes), and Extended Clipboard (UTF-8).
- **Pixel format:** request 32-bit true colour in the framebuffer's layout
  ([graphics-pipeline](graphics-pipeline.md)), so updates copy without
  conversion.
- **Update model:** with ContinuousUpdates the server pushes, and is
  paused while nobody can see. Without it, one incremental
  `FramebufferUpdateRequest` is outstanding while `Demand.Visible`: the
  next is sent as soon as the previous update is applied, and none while
  nobody can see ([session-core](session-core.md)). Viewer pacing
  happens after the framebuffer.
- **CopyRect** is passed to `Sink.CopyRect` so it can reach viewers as
  `copy-rect`.

## Options

- **A. Own client in `internal/backend/vnc` (recommended).** Existing
  libraries serve as references only.
  - Pros: exactly the scope above; decoders written for fuzzing from the
    start; no inherited API.
  - Cons: more code up front (an estimated 2–3k lines with tests).
- **B. Adopt and extend an existing library.**
  - Pros: a faster start for Raw and CopyRect.
  - Cons: none of the known candidates covers Tight, VeNCrypt or ARD, and
    their APIs allocate per rectangle. A fork becomes the code to
    maintain anyway.

## Open Questions

Answered under Decisions, by number.

1. A or B, after a one-day survey of current Go RFB libraries?
2. Is ARD (type 30) required for beta, or are macOS hosts covered by
   VeNCrypt alone? Check which types current macOS Screen Sharing offers.
3. Keyboard: send keysyms always, and QEMU extended key events only when
   the server advertises them? How are layout differences handled for
   servers without physical codes?
4. Resize: when the server lacks ExtendedDesktopSize, scale on the client
   only?
5. Should the VNC decoders be fuzzed against recorded server traffic, and
   where does the corpus live (`internal/backend/vnc/testdata`)?

## Decisions

Settled on 2026-10-30 under the owner's standing approval to resolve open
items with the recommended options, checked against the code and RFC
6143. They are open to the owner's review.

1. **Option A: our own client in `internal/backend/vnc`.** Existing
   libraries are references only. None covers Tight, VeNCrypt, ARD or
   ExtendedDesktopSize, and their decoders allocate per rectangle and
   trust the server's lengths. The backend's decoders check every length
   against a limit before they read or allocate, and draw through
   `Sink.Lock` in bounded strips, which an adopted API would have to be
   bent to.
2. **ARD (type 30) stays in scope, in a later phase (phase 4).** Without
   the legacy "VNC viewers may control screen with password" setting,
   macOS Screen Sharing offers no type that the other phases implement:
   its login uses Apple's own types, and it does not offer VeNCrypt, as
   TigerVNC and noVNC users report. Which types current macOS offers is
   still to be checked by hand, in phase 4.
3. **Keysyms always; QEMU Extended Key Events too when the server
   advertises them (phase 3).** RFC 6143 key events carry keysyms, and
   `Text` input becomes one keysym per character. The extended event
   (rfbproto) carries an XT scancode, while `backend.Key.Code` is a USB
   HID usage, so phase 3 adds a HID-to-XT table. Servers without
   physical codes map keysyms to their own layout: there is no
   client-side layout translation, and a keysym the server's keymap
   lacks is the server's to handle (x11vnc and TigerVNC add it to the X
   keymap).
4. **Without ExtendedDesktopSize, the client scales; there is no server
   resize.** A `Resize` input is then ignored. With it (phase 3),
   `Resize` becomes `SetDesktopSize`. This matches
   [graphics-pipeline](graphics-pipeline.md) decision 5: no server-side
   scaling either.
5. **The decoders are fuzzed** with Go's native fuzzing, one target for
   server messages and one for the handshake. The seed corpus lives in
   `internal/backend/vnc/testdata/fuzz/`, where `go test` runs it as
   regression tests. Seeds are encoded by the fake server's encoders;
   traffic recorded from x11vnc, TigerVNC and QEMU joins them with the
   phase 2 encodings, whose decoders are where recorded traffic matters
   most.

## Task Breakdown

| Phase | Delivers | Needs |
|-------|----------|-------|
| 1 | `internal/backend/vnc`: the RFB 3.3, 3.7 and 3.8 handshake; security types None and VNC Authentication, with the owner asked through `Sink.Prompt` for a password the configuration lacks; Raw, CopyRect, DesktopSize and Cursor; the pixel format requested in the framebuffer's layout; one incremental update request outstanding while `Demand.Visible`; key and pointer input; the server clipboard as Latin-1 cut text. Tests against an in-process fake RFB server, fuzzed decoders | session-core Backend interface, framebuffer |
| 2 | Hextile, ZRLE, and Tight with JPEG and the gradient filter; recorded traffic in the fuzz corpus | Phase 1 |
| 3 | VeNCrypt (TLS and X.509, with Plain, None and VNC inner authentication); ExtendedDesktopSize with `SetDesktopSize`; ContinuousUpdates with Fence; QEMU Extended Key Event with the HID-to-XT table; Extended Clipboard (UTF-8); LastRect | Phase 1 |
| 4 | Apple Remote Desktop (type 30), checked by hand against macOS Screen Sharing | Phase 1 |

Registration in the API's backend factory for `vnc`, and the switch from
the guacd bridge, follow the parity gate in
[guacd-bridge](guacd-bridge.md), not these phases.

## Affects

New `internal/backend/vnc`. After its parity gate
([guacd-bridge](guacd-bridge.md)), VNC no longer needs guacd, which
leaves only RDP on the bridge.
