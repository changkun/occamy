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
  stack and on the bench desktop (`docker/bench/desktop`), which phase 1's
  integration test uses; TigerVNC, to be added to the stack; QEMU's VNC
  server; macOS Screen Sharing, checked by hand.

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
  ([graphics-pipeline](graphics-pipeline.md)): little-endian, red at bit
  0, green at 8 and blue at 16, so that a pixel's bytes are `image.RGBA`'s.
  Updates copy without conversion; only the padding byte is set to opaque
  alpha.
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

## Implementation notes: phase 1

- **Status.** Phase 1 is done: `internal/backend/vnc` implements
  `backend.Backend` with `Config`, `New` and `Backend`, in about 1,300
  lines of code and 2,200 of tests. Nothing wires it into `occamyd`: the
  sessions task registers backends.
- **What was built.**
  - Run dials with the context, runs the handshake, sends
    `SetPixelFormat` and `SetEncodings` (CopyRect, Raw, Cursor,
    DesktopSize) in one write, reports the size with `Sink.Resize` and
    the desktop name with `Sink.Title`, and then applies the server's
    messages until the connection ends. A writer goroutine sends queued
    input and update requests, batching what is ready into one write.
  - Raw rectangles are read in strips of whole rows (up to 256 KiB) and
    each strip is copied into `Sink.Lock`'s pixels after it has arrived,
    so the framebuffer is never locked while the backend waits for the
    network, and memory does not grow with the rectangle.
  - CopyRect goes to `Sink.CopyRect`, the Cursor pseudo-encoding to
    `Sink.Cursor` (the mask's clear bits transparent), `Bell` to
    `Sink.Bell`, and `ServerCutText` from Latin-1 to UTF-8 as
    `text/plain` to `Sink.Clipboard`. `SetColourMapEntries` is skipped.
  - Tests run against a fake RFB server on the loopback interface that
    encodes pixels in the pixel format the client asks for, so a wrong
    request shows as wrong pixels. They cover each version's handshake,
    both security types, wrong and prompted passwords, refusals, each
    encoding, a resize mid-stream, the cursor, the clipboard both ways,
    input, demand gating, the server closing within each kind of message,
    protocol errors and allocations; every test checks for leaked
    goroutines. Coverage is 98% of statements. An integration test
    behind the `integration` build tag runs against the bench desktop's
    x11vnc: the idle screen decoded by the backend matches, pixel for
    pixel, the one an independent client reads in x11vnc's own pixel
    format, and keys repaint the scene's swatch in their exact colours.
  - Two fuzz targets, `FuzzServerMessages` and `FuzzHandshake`, with
    seeds in `testdata/fuzz/`. `TestFuzzCorpus` keeps the committed seeds
    equal to those the tests encode; `-args -update-corpus` rewrites
    them. A first run, 60 s of `FuzzServerMessages` and 45 s of
    `FuzzHandshake`, found nothing.
- **Decisions made during implementation.**
  - Limits, checked before anything is read or allocated: a screen of at
    most 16384 pixels a side and 2^25 in all (8K UHD fits), since the
    session allocates the whole screen; a cursor of at most 256×256, as
    TigerVNC's viewer allows; a desktop name of 4 KiB; 4 MiB of cut text,
    beyond which the rest is read and dropped with a warning; the first
    4 KiB of a failure's reason. An empty screen, from ServerInit or
    DesktopSize, is an error, since the session cannot hold one.
  - Versions: 3.7 is 3.7, 3.8 and anything later (Apple's 3.889, 4.x) is
    answered with 3.8, as TigerVNC does, and any other 3.x is 3.3. RFC
    6143 says to treat every version but 3.7 and 3.8 as 3.3; answering
    3.889 with 3.3 would lose the 3.8 handshake that Apple's server
    speaks.
  - Security: VNC Authentication over None when a password is
    configured, None over it when not, so that nobody is asked for a
    password a server does not need. A failed SecurityResult wraps
    `ErrAuthFailed`, a refusal before authentication `ErrRefused`, and
    no common type `ErrNoSecurityType`; each carries the server's reason
    when the version gives one. The configured or prompted password is
    used for one attempt; a wrong one ends Run, as servers close the
    connection after a failure.
  - The prompt has the instruction `VNC password for host:port` and one
    secret field, `Password`. Input that arrives while the owner answers
    is queued and written after the handshake.
  - The client always asks for a shared session (`ClientInit` shared
    flag set), so that Occamy never disconnects other VNC clients.
  - Updates: the first request after the handshake, and the first after
    a DesktopSize, ask for the whole screen; the others for changes.
    Updates nobody asked for are applied, and do not count against the
    outstanding request beyond clearing it. Demand is kept on the
    `Backend`, not the Run, since the session reports only changes and
    may report one before Run.
  - A server that closes the connection between messages ends the
    session normally (Run returns nil), since RFB has no other way to end
    one; closing it within a message wraps `io.ErrUnexpectedEOF`.
  - Input: keysyms as they come, keysym 0 dropped; `Text` as a press and
    release per character, with the guacd bridge's mapping; buttons 1 to
    3, and Back as button 8, as in X11, while Forward (button 9) has no
    bit and is dropped; the wheel as presses of buttons 4 and 5 (6 and 7
    horizontally) at the pointer with its buttons held, scaled as the
    guacd bridge does. The pointer is clamped to the screen. Clipboard
    input of a `text/*` type, or none, becomes Latin-1 cut text with LF
    line ends and `?` for characters outside Latin-1; other types are
    ignored. `Resize` is ignored (decision 4) and `TermInput` fails with
    `ErrUnsupportedInput`. The queue holds 256 inputs, then
    `ErrInputFull`.
- **Deviations from the spec.** None in scope. The pixel format bullet
  above now says that the padding byte is made opaque, which "without
  conversion" left out.
- **Surprises / gotchas.**
  - x11vnc draws the pointer into the pixels for a client that does not
    take the Cursor pseudo-encoding, so frames compared across clients
    differ by the pointer unless both take it.
  - Servers leave the padding byte as they like (x11vnc sends 0), and the
    framebuffer treats it as alpha, so it must be set; the fake server
    fills it with a pattern to catch a client that keeps it.
  - RFC 6143's description of VNC Authentication leaves out that every
    implementation reverses the bits of each key byte. The test pins the
    response against a vector computed with OpenSSL, not with this
    package.
- **Follow-ups.**
  - Phase 2's encodings, and recorded traffic from x11vnc, TigerVNC and
    QEMU in the fuzz corpus.
  - The PointerPos pseudo-encoding (-232) would report the remote
    pointer's moves to `Sink.CursorMove`; it is not in the scope above.
  - A server that answers incremental requests with empty updates would
    make the request loop spin at the round-trip rate; none seen so far.
  - Registration for `vnc` in the API's backend factory, and the parity
    gate against the guacd bridge.
