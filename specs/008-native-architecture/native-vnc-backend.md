---
title: Native VNC backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/graphics-pipeline.md
affects:
  - internal/backend/vnc/
  - internal/keymap/
effort: large
created: 2026-10-29
updated: 2026-10-31
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
  stack and on the bench desktop (`docker/bench/desktop`), which the
  integration tests use; TigerVNC's Xvnc, in an image on top of the bench
  desktop (`internal/backend/vnc/testdata/servers`) that phase 2's
  integration test uses, and with VeNCrypt only
  (`docker/test/tigervnc`), which phase 3's integration test uses, both
  still to be added to the stack; QEMU's VNC server; macOS Screen
  Sharing, checked by hand.

## Proposed scope for beta

The scope is delivered in four phases (see Task Breakdown).

- **Security types:** None (1), VNC Authentication (2), VeNCrypt (19)
  with its X.509 subtypes (Plain, None and VNC inner auth), and Apple
  Remote Desktop (30), so that macOS hosts work, but only where the
  connection's configuration allows it, since its key exchange
  authenticates no server (phase 4 notes). VeNCrypt's anonymous TLS
  subtypes, which no certificate authenticates, are out (phase 3
  notes), as are RA2, Apple's other types (33, 35, 36) and other
  proprietary types.
- **Encodings:** Raw, CopyRect, Hextile, ZRLE and Tight (including JPEG and
  the gradient filter). Sequential JPEG is decoded when a server sends
  it, but the backend asks for no quality level, so servers keep Tight
  lossless (see phase 2's notes).
- **Pseudo-encodings:** DesktopSize, ExtendedDesktopSize (with
  `SetDesktopSize` for client-driven resize), Cursor, LastRect,
  ContinuousUpdates with Fence, QEMU Extended Key Event (physical key
  codes), and Extended Clipboard (UTF-8).
- **Pixel format:** request 32-bit true colour in the framebuffer's layout
  ([graphics-pipeline](graphics-pipeline.md)): little-endian, red at bit
  0, green at 8 and blue at 16, so that a pixel's bytes are `image.RGBA`'s.
  Updates copy without conversion; only the padding byte is set to opaque
  alpha. ZRLE's and Tight's 3-byte compact pixels are then red, green and
  blue, and gain the alpha byte.
- **Update model:** with ContinuousUpdates, which servers offer only
  to clients that take Fences, the server pushes, and is paused while
  nobody can see. Without it, the backend asks for an
  update while `Demand.Visible` and asks for the next as soon as that
  update is applied, and asks for none while nobody can see. That is
  [session-core](session-core.md)'s one outstanding request as far as RFB
  can enforce it: at most one request per update applied (see the
  implementation notes). Viewer pacing happens after the framebuffer.
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
   TigerVNC and noVNC users report. Published captures show 30, 33, 36
   and 35 from macOS 10.13 and 10.15, 30, 33 and 36 from macOS 26.6, and
   2 besides with the legacy setting (phase 4 notes); a Mac of the
   owner's is still to be checked by hand. Type 30 is off unless the
   configuration allows it.
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
   resize.** A `Resize` input is then ignored; one that comes before the
   server has shown whether it supports the extension waits for its
   first update. With it (phase 3), `Resize` becomes `SetDesktopSize`.
   This matches
   [graphics-pipeline](graphics-pipeline.md) decision 5: no server-side
   scaling either.
5. **The decoders are fuzzed** with Go's native fuzzing, one target for
   server messages and one for the handshake. The seed corpus lives in
   `internal/backend/vnc/testdata/fuzz/`, where `go test` runs it as
   regression tests. Seeds are encoded by the fake server's encoders.
   Traffic recorded from x11vnc and TigerVNC, a short session of each
   phase 2 encoding, joined them in phase 2, since those decoders are
   where recorded traffic matters most; QEMU's is still to be recorded.

## Task Breakdown

| Phase | Delivers | Needs |
|-------|----------|-------|
| 1 | `internal/backend/vnc`: the RFB 3.3, 3.7 and 3.8 handshake; security types None and VNC Authentication, with the owner asked through `Sink.Prompt` for a password the configuration lacks; Raw, CopyRect, DesktopSize and Cursor; the pixel format requested in the framebuffer's layout; one incremental update request outstanding while `Demand.Visible`; key and pointer input; the server clipboard as Latin-1 cut text. Tests against an in-process fake RFB server, fuzzed decoders | session-core Backend interface, framebuffer |
| 2 | Hextile, ZRLE, and Tight with JPEG and the gradient filter; recorded traffic in the fuzz corpus | Phase 1 |
| 3 | VeNCrypt (X.509, with Plain, None and VNC inner authentication); ExtendedDesktopSize with `SetDesktopSize`; ContinuousUpdates with Fence; QEMU Extended Key Event with the HID-to-XT table; Extended Clipboard (UTF-8); LastRect | Phase 1 |
| 4 | Apple Remote Desktop (type 30), only where the configuration allows it, checked by hand against macOS Screen Sharing (the procedure is in phase 4's notes; no Mac has run it yet) | Phase 1 |

Registration in the API's backend factory for `vnc` is done, as `vnc`'s
default, and the deletion of `vnc`'s guacd path follows the parity
gate in [guacd-bridge](guacd-bridge.md), not these phases. (2026-10-30: the
Sessions task of [api-and-server-stack](api-and-server-stack.md)
registered the native backend as `vnc`'s default ahead of the gate,
since no configuration key picked a backend then. 2026-10-31: the gate's
opt-in period started, with the guacd bridge as `vnc`'s default and
`backend: native` registering this backend for a connection. Later on
2026-10-31, the owner decided that native is the default wherever a
native backend exists: this backend is `vnc`'s default again, and
`backend: guacd` is an explicit fallback and the benchmark's baseline,
kept until the benchmark shows native at least 10x better. See
Implementation notes: registration.)

## Affects

New `internal/backend/vnc`. Once the parity gate deletes VNC's guacd
path ([guacd-bridge](guacd-bridge.md)), VNC no longer needs guacd, which
leaves RDP's fallback, until its own deletion gate, and telnet on the
bridge. The HID-to-XT table derives from `internal/keymap`, the
scan code table it shares with the RDP client
since [native-rdp-backend](native-rdp-backend.md)'s phase 1.

## Implementation notes: phase 1

- **Status.** Phase 1 is done: `internal/backend/vnc` implements
  `backend.Backend` with `Config`, `New` and `Backend`, in about 1,400
  lines of code and 2,500 of tests. Nothing wires it into `occamyd`: the
  sessions task registers backends (done: `vnc` connections and ad-hoc
  targets use it by default, and `backend: guacd` picks the bridge
  instead).
- **What was built.**
  - Run dials with the context, runs the handshake, sends
    `SetPixelFormat` and `SetEncodings` (CopyRect, Raw, Cursor,
    DesktopSize; phase 2 adds Tight, ZRLE and Hextile) in one write,
    reports the size with `Sink.Resize` and the desktop name with
    `Sink.Title`, and then applies the server's messages until the
    connection ends. A writer goroutine sends queued
    input and update requests, batching what is ready into one write.
  - Raw rectangles are read in strips of whole rows (up to 256 KiB) and
    each strip is copied into `Sink.Lock`'s pixels after it has arrived,
    so the framebuffer is never locked while the backend waits for the
    network, and memory does not grow with the rectangle.
  - CopyRect goes to `Sink.CopyRect`, the Cursor pseudo-encoding to
    `Sink.Cursor` (the mask's clear bits transparent), `Bell` to
    `Sink.Bell`, and `ServerCutText` to `Sink.Clipboard` as `text/plain`
    in UTF-8. `SetColourMapEntries` is skipped.
  - Tests run against a fake RFB server on the loopback interface that
    encodes pixels in the pixel format the client asks for, so a wrong
    request shows as wrong pixels. They cover each version's handshake,
    both security types, wrong and prompted passwords, refusals, each
    encoding, a resize mid-stream, the cursor, the clipboard both ways,
    input, demand gating, the server closing within each kind of message,
    cancellation mid-update, protocol errors and allocations. Every test
    that runs the backend checks for leaked goroutines; the unit tests of
    the handshake, the decoder and the conversions run no backend.
    Coverage is 98% of statements. An integration test
    behind the `integration` build tag runs against the bench desktop's
    x11vnc: the idle screen decoded by the backend matches, pixel for
    pixel, the one an independent client reads in x11vnc's own pixel
    format, and keys repaint the scene's swatch in their exact colours.
  - Two fuzz targets, `FuzzServerMessages` and `FuzzHandshake`, with
    seeds in `testdata/fuzz/`. `TestFuzzCorpus` keeps the committed seeds
    equal to those the tests encode; `-args -update-corpus` rewrites
    them. A first run, 60 s of `FuzzServerMessages` and 45 s of
    `FuzzHandshake`, found nothing, nor did a second after the review
    fixes (45 s and 20 s).
- **Decisions made during implementation.**
  - Limits, checked before anything is read or allocated: a screen of at
    most 16384 pixels a side and 2^25 in all (8K UHD fits), since the
    session allocates the whole screen; a cursor of at most 256×256, as
    TigerVNC's viewer allows; a desktop name of 4 KiB; 4 MiB of cut text,
    beyond which the rest is read and dropped with a warning; the first
    4 KiB of a failure's reason. An empty screen, from ServerInit or
    DesktopSize, is an error, since the session cannot hold one.
  - Resizes: an update resizes the screen at most once, and a second
    DesktopSize in it is a protocol error. Servers send it as the last
    rectangle (RFC 6143, section 7.8.2), and libvncserver as an update of
    its own, while each resize reallocates the session's screen under its
    exclusive lock. Rectangles after the one DesktopSize are still drawn,
    on the new screen. Resizes also wait at least 100 ms for each other,
    so that updates of 16 bytes cannot resize the screen faster than ten
    times a second; an interactive resize still follows at that rate, and
    TigerVNC's viewer waits 500 ms before it asks for one of its own. The
    wait is a delay, not an error, and ends with Run's context.
  - Cancellation: the decoder checks Run's context between messages and
    between rectangles, so that Run returns at once when cancelled
    instead of applying what is left in its 64 KiB read buffer.
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
    Demand is kept on the `Backend`, not the Run, since the session
    reports only changes and may report one before Run.
  - Requests: the bound enforced is at most one request per update
    applied, and none without one, not exactly one outstanding. RFB has
    no request IDs, so every update, including one nobody asked for,
    counts as the answer to the pending request. A server that sends
    updates unasked, which RFC 6143 forbids, may then hold two requests.
    That is harmless where it was checked: libvncserver sends size
    changes and cursor shapes before it looks at the requested region,
    and keeps that region as the union of the requests (`sraRgnOr`), so
    a second request merges with the first. Not clearing the pending
    request on such updates would stall the loop against a server that
    answers a request with a cursor-only update. session-core's "one
    incremental update request outstanding" is the intent; this is the
    form RFB allows.
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
    ignored. `Resize` is ignored (decision 4; phase 3 sends it to servers
    with ExtendedDesktopSize) and `TermInput` fails with
    `ErrUnsupportedInput`. The queue holds 256 inputs and 8 MiB plus
    64 KiB of their messages, two clipboards of the largest size, then
    `ErrInputFull`. A `Text` costs 16 bytes a character, so one of more
    than about 500,000 characters always fails.
  - Text from the server, both the desktop name and cut text, is read as
    UTF-8 if it is valid UTF-8 and as Latin-1 otherwise. RFC 6143 says
    cut text is Latin-1, but many servers send UTF-8, and Latin-1 text
    beyond ASCII is rarely valid UTF-8. Cut text cut short by the 4 MiB
    limit within a UTF-8 character loses that character's first bytes
    before the check. Text sent to the server stays Latin-1, as RFC 6143
    requires; Extended Clipboard (phase 3) removes the asymmetry with
    servers that support it.
- **Deviations from the spec.** None in scope. The pixel format bullet
  above now says that the padding byte is made opaque, which "without
  conversion" left out, and the update model bullet says which bound on
  requests is enforced.
- **Review fixes.** A review of the pull request found:
  - DesktopSize amplification: an update could carry 65535 DesktopSize
    rectangles of 12 bytes, each reallocating the screen. 200 of them
    alternating between 16384×2048 and 16383×2048, a 2404-byte update,
    allocated 25.6 GiB in half a second, and after 65535 of them Run took
    14.7 s to return from a cancel, since the decoder applied what its
    buffer held. Fixed by the resize and cancellation rules above.
  - More than one request could be outstanding after an update nobody
    asked for. Kept, and documented, as the requests decision above
    explains.
  - The input queue was bounded by count only, so 256 clipboards or long
    `Text`s could queue gigabytes against a server that stopped reading.
    Fixed by the byte budget.
  - Cut text was always read as Latin-1, while the desktop name was
    sniffed for UTF-8. Both now follow the text rule above.
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
    QEMU in the fuzz corpus. Done in phase 2, but for QEMU's traffic.
  - The PointerPos pseudo-encoding (-232) would report the remote
    pointer's moves to `Sink.CursorMove`; it is not in the scope above.
  - A server that answers incremental requests with empty updates would
    make the request loop spin at the round-trip rate; none seen so far.
  - Resizes are rate-limited by a fixed interval. A budget that scales
    with the screen's size would bound the work better: at the largest
    screen, ten resizes a second still reallocate 1.25 GiB a second.
  - Registration for `vnc` in the API's backend factory (done, as the
    default, which it is again since 2026-10-31), and the parity gate
    against the guacd bridge, which now decides when `vnc`'s guacd
    fallback is deleted.

## Implementation notes: phase 2

- **Status.** Phase 2 is done: Hextile, ZRLE and Tight, with Tight's
  fill, JPEG, and its copy, palette and gradient filters, in
  `hextile.go`, `zrle.go` and `tight.go`, the zlib streams in `zlib.go`
  and the state they share in `codec.go`: about 1,150 lines of code and
  2,300 of tests. The backend asks for them in `SetEncodings`.
- **What was built.**
  - `SetEncodings` lists CopyRect, Tight, ZRLE, Hextile, Raw, Cursor and
    DesktopSize (decisions below).
  - Each row of tiles, 16 pixels high for Hextile and 64 for ZRLE, is
    decoded into scratch space at the rectangle's width and drawn under
    one `Sink.Lock`. Tight's basic rectangles go through the strips of
    whole rows that Raw uses, and a JPEG image is drawn in strips once
    it is decoded. The pixel format makes ZRLE's and Tight's compact
    pixels 3 bytes, red, green and blue, which gain an opaque alpha byte.
  - The zlib streams, one for ZRLE and four for Tight, last as long as
    Run's connection: a rectangle's data continues the stream of the ones
    before it, a Tight rectangle's control byte resets the streams it
    names, and they are dropped when Run returns. The inflater reads the
    connection itself, through a reader that hands it the rectangle's
    compressed length and no more, and the decoder takes from it only
    the bytes that the rectangle's pixels need.
  - Tests: hand-built byte streams for every Hextile subencoding, every
    ZRLE subencoding, and every Tight compression and filter, with edge
    tiles, zlib state across rectangles and Tight's resets; round trips
    through encoders that the fake server gained (`encode_test.go`); all
    three over `Run`; errors; and allocations under hostile lengths.
    Coverage of the package is 96% of statements.
  - `TestEncodings`, behind the `integration` tag, asks a real server for
    one encoding at a time through a recording proxy: Hextile, ZRLE,
    Tight, and Tight with JPEG quality level 6. It matches the screen
    against an independent client's reference, pixel for pixel when
    lossless and within a mean difference of 4 a channel with JPEG,
    through the bench desktop's idle screen, swatch keys and photograph.
    It then decodes the recording again and checks that it holds the
    encoding's rectangles, and no JPEG unless asked for. It passed
    against x11vnc 0.9.17 and TigerVNC 1.15's Xvnc (Debian trixie), at
    1024×768 and at 64×48. `testdata/servers` builds TigerVNC on the
    bench desktop's image.
  - The fuzz corpus gained a seed for each subencoding and filter, with
    zlib data in stored blocks so that the seeds do not change with Go's
    compressor, and eight recorded sessions, `recorded-<server>-<encoding>`:
    the four encodings from x11vnc and from TigerVNC, at 64×48 to fit the
    fuzzed decoder's limits, after a DesktopSize to that size.
    `TestRecordedSessions` decodes each to its end and checks it holds its
    encoding.
- **Decisions made during implementation.**
  - Order: Tight first, as in TigerVNC's viewer, then ZRLE, Hextile and
    Raw. Lossless, Tight and ZRLE are within 4% of each other, and
    Hextile twice their size: the session above at 1024×768 (the idle
    screen, three swatches, the photograph and the idle screen again)
    took 1.50, 1.45 and 3.25 MB from x11vnc, and 1.49, 1.46 and 3.25 MB
    from TigerVNC. Tight also sends solid areas as fills, and is the only
    one of the three that can carry JPEG.
  - No JPEG quality level is asked for, so servers keep Tight lossless and
    the framebuffer holds their exact pixels. The graphics pipeline counts
    each tile's colours to pick lossless or lossy, and refines tiles of up
    to 256 colours to exact pixels; JPEG's artefacts would make text
    photographic and its refinement inexact. JPEG costs much less
    upstream: quality level 6 took the session to 0.12 MB from x11vnc and
    0.47 MB from TigerVNC. A server that sends JPEG anyway is decoded.
  - Lengths are checked before anything is read or allocated. A ZRLE
    rectangle's compressed length may not exceed what deflate takes for
    its largest encoding (1 + 3 × 127 bytes a tile and 4 a pixel, plus an
    eighth and 1 KiB); a Tight rectangle's, the same bound on its data.
    A JPEG rectangle's buffer grows as the data arrives rather than to
    the length claimed.
  - JPEG: a rectangle may be at most 2048 pixels wide, as TigerVNC's
    viewer allows, and 65536 pixels in all, the most that TigerVNC,
    libvncserver and QEMU send in one; both recorded sessions' JPEG
    rectangles, 84 from TigerVNC and 12 from x11vnc at 1024×768, fit.
    Before `image/jpeg` sees the image, its markers are walked from the
    start of the image to its end: only a baseline or extended
    sequential frame (SOF0, SOF1) is accepted, with at most a scan for
    each of its components, and the frame's size must be the
    rectangle's. `image/jpeg` decodes every scan an image has, without a
    limit, and each scan is a pass over the image's blocks, so decoding
    then costs no more than a pass for each component. An error from
    `image/jpeg` that reads as `io.ErrUnexpectedEOF` becomes a plain one,
    since the image arrived whole.
  - Updates: an update may draw at most four times the screen's pixels,
    counting each rectangle that draws by its area on the screen, and a
    DesktopSize starts the count again at the new size. A rectangle of a
    few bytes can draw the whole screen, as a Tight fill, a CopyRect or a
    ZRLE rectangle of one colour do, which took 18 to 25 ms at 8192×4096,
    so an update of 65535 of them would have taken half an hour. Servers
    draw each pixel about once an update: the recorded sessions drew at
    most one screen in one. The decoder checks Run's context between
    rectangles, as in phase 1.
  - Decompression bombs: the inflater runs only as far as the pixels
    read from it, so a rectangle inflates no more than its pixels and a
    few tens of KiB beyond them, the inflater's 32 KiB window and a 32 KiB
    buffer. Inflated data that the pixels do not take is an error. Up to
    16 compressed bytes may be left after the pixels, the end of a
    server's flush, and are kept for the stream's next rectangle; more is
    an error. An honest tail is about 7 bytes: when the window fills just
    as the pixels end, the end of the last block and the empty stored
    block of the flush; about 12 when a server flushes twice.
  - What goes unseen: the inflater has not looked at the bytes left, so
    data among them is not seen as extra. It happens when the window
    fills exactly where the pixels end, and when a flush is followed by
    more data and a second flush. The data then starts the stream's next
    rectangle, which it garbles, or makes fail one rectangle late; the
    connection's framing does not depend on it, as each rectangle's
    length is read before its data. The 16 bytes bound what it can
    inflate to: after the 5 bytes of the flush, 11 are left for the rest
    of a block whose Huffman codes were read before, and at 2 bits for a
    match of 258 bytes they make about 11 KB. 10,750 bytes slipped
    through with `BestCompression` at a window boundary, and 1,250 with
    a second flush.
  - Strictness: ZRLE's unused subencodings (17 to 127 and 129), Tight's
    compression types above JPEG (PNG belongs to TightPNG), unknown
    filters, palette indices beyond the palette, runs past the tile and
    Hextile subrectangles past the tile, including edge tiles, are errors
    rather than guesses. A Tight palette of one colour is read with a bit
    a pixel, as TigerVNC reads it; an index of 1 is an error.
  - Hextile's background and foreground carry over from tile to tile,
    raw tiles included, and a coloured subrectangle sets the foreground,
    as TigerVNC and libvncclient decode them.
  - The decoder counts tiles and rectangles by kind (`codecs.counts`),
    for the tests and the recorded sessions.
- **Deviations from the spec.** None in scope. The encodings bullet now
  says that JPEG is decoded but not asked for, the pixel format bullet
  says how compact pixels are expanded, decision 5 says which servers'
  traffic was recorded, and the test servers bullet names the TigerVNC
  image.
- **Review fixes.** A review of the pull request found:
  - JPEG scan bombs: `image/jpeg` decodes every scan of a progressive
    image, each a pass over all its blocks, and a cancel cannot stop
    it. 16,000 scans in 304 KB took 27 s at 2048×2048 in the review, and
    13 s here; the 4 MiB that a Tight rectangle may hold would have
    taken minutes. A baseline image with more scans than components is
    slow too: 1,000 scans in 266 KB took 138 ms at 256×256, about
    0.5 µs a byte. Fixed by the JPEG rule above: sequential frames
    only, a scan per component, and 65536 pixels, down from 2048×2048.
    `TestTightJPEGScanBombs` holds the review's images, and each now
    fails in microseconds. (Phase 4 replaced the test's 100 ms bound,
    which timed the sink's 16 MB screen too and failed once on CI under
    the race detector, with a count of the images that reach
    `image/jpeg`: none of these.)
  - `io.ErrUnexpectedEOF`, which Run reserves for a connection that
    closes within a message, also came from data that had arrived
    whole: `io.ReadFull` turned the inflater's `io.EOF`, at the end of a
    stream that a server ended, into it, and `image/jpeg` returned it
    for an image cut short within its length. The inflater's end is now
    "the server ended its zlib stream", and `image/jpeg`'s error a plain
    one. `checkSession` accepted the error anywhere in a recording, and
    now only at its end.
  - The unseen data in the zlib tail could inflate to about 32 KB with
    a tail of 64 bytes. The tail is now 16 bytes, and what is left
    unseen is described above. The review's way to close the gap, a
    copy of the output in a 32 KiB ring and a restart of the inflater
    with it as the dictionary after reading what is left, was not
    taken: it restarts only when the output is a multiple of 32 KiB, so
    it misses data after a second flush, and its cost, a copy of every
    inflated byte, could not be measured to the 5% asked for on the
    shared host, where the review's runs varied by 20 to 30% from one to
    the next. It is a follow-up.
  - Two guards had no test. Without the cap on `zinput.Read`, a stored
    block that claims more bytes than the rectangle has read the next
    messages as its own, and the tail's copy panicked;
    `TestZlibStoredBlockPastRect` covers it. The JPEG area bound was
    tested on a screen too small for the rectangle, whose check failed
    first; `TestTightJPEGErrors` uses one of 2100×300.
  - A Tight fill, 16 bytes with its header, took 16 ms at 8192×4096 in
    the review, and 18 to 25 ms here, with nothing to bound how many an
    update holds. Fixed by the draw budget above.
  - The reference client summed its rectangles' areas, so rectangles
    that overlapped could reach the screen's area with pixels missing.
    It now counts each pixel once.
  - The notes said that `flate` hands out data at the end of any stored
    block; it is at an empty one. They now say so, and what unseen data
    does to the next rectangle.
- **Surprises / gotchas.**
  - Go's `compress/flate` hands out inflated data only at an empty
    stored block, which is the marker that `Z_SYNC_FLUSH` writes, when
    its 32 KiB window fills, and at the end of the stream, not at the
    end of other blocks, stored ones included. A server's flush ends
    each rectangle with an empty stored block, so a rectangle's pixels
    are all available once its data is read. A server that flushed with
    `Z_PARTIAL_FLUSH` would fail with "the compressed data ends before the
    rectangle's pixels"; TigerVNC, libvncserver and QEMU use
    `Z_SYNC_FLUSH`.
  - `flate` keeps the first error it sees, so the reader that feeds it
    must not report the end of a rectangle's data as an error unless the
    rectangle is malformed, or the stream is lost for the connection.
    It wraps a reader without `ReadByte` in a buffer of its own, which
    would read past the rectangle; the reader implements `io.ByteReader`.
  - TigerVNC's Xvnc sometimes answered the reference client's first
    request with an update without pixels, which left phase 1's reference
    client waiting; it now asks again until it has every pixel. It marks
    each pixel it is sent in a bitmap of the screen and counts it once,
    so rectangles that overlap, within an update or across the updates
    it asks for, cannot make it stop with pixels missing.
  - Neither server sent a gradient rectangle in these sessions, only the
    copy and palette filters, so the gradient filter is tested with
    hand-built data and the fake server's encoder alone.
  - `bufio.Reader` reads a request at least as large as its buffer
    straight from the underlying reader. With a 16 KiB buffer, inflated
    data beyond a large Tight rectangle's pixels stayed in the inflater,
    unseen by the check for it, and started the next rectangle. The
    buffer is now as large as the inflater's window, the most it hands
    out at once, so every read takes all it has handed out.
  - With the race detector on, `slices.Grow` allocated twice the growth,
    which broke the JPEG allocation bound; the JPEG buffer grows by hand.
  - Go's fuzzer minimizes each new input for up to a minute and does not
    count those runs, and with recorded sessions of a few KiB in the
    corpus it reported 0 executions a second for most of a run. Use
    `-fuzzminimizetime 2s`: 2 minutes of `FuzzServerMessages` then ran
    3.6 million inputs, found 323 new ones and no failure.
- **Measured.** Decode throughput on one core of an Apple M5 Pro, in
  bytes of pixels drawn (4 a pixel), the median of six runs, with the
  shared host's load average between 16 and 29 on 18 cores. The sessions
  are those of `TestEncodings` from each server at 1024×768, which draw
  about three full screens; the synthetic frame is a 1024×768 desktop of
  wallpaper, text and a photograph that the fake server's encoders send
  (`BenchmarkDecode`).

  | Encoding | x11vnc session | TigerVNC session | Synthetic frame |
  |----------|---------------:|-----------------:|----------------:|
  | Raw | | | 10.1 GB/s |
  | Hextile | 4.7 GB/s | 4.6 GB/s | 4.1 GB/s |
  | ZRLE | 615 MB/s | 552 MB/s | 825 MB/s |
  | Tight | 567 MB/s | 552 MB/s | 752 MB/s |
  | Tight with JPEG | 993 MB/s | 541 MB/s | 668 MB/s |

  A full screen of 1024×768 in ZRLE or Tight takes 4 to 6 ms, most of it
  in `compress/flate`'s Huffman decoding, so one core decodes full-screen
  changes at 3840×2160 at 16 to 25 frames a second. Both servers send the
  photograph's Hextile tiles raw, which is why Hextile decodes at nearly
  the speed of a copy.
- **Mutation checks.** Each of these was reverted in turn, and a test
  failed: Tight's stream resets, the bound on ZRLE runs, the check for
  inflated data left over, the buffer as large as the window, keeping
  the zlib tail, its 16-byte bound, the cap on a stored block at the
  rectangle's end, the inflater's end as its own error, the gradient's
  clamp and its first pixel, a coloured Hextile subrectangle setting the
  foreground, the Hextile subrectangle bound at edge tiles, Tight's
  palette index check, the ZRLE and Tight length bounds, the JPEG
  marker walk, its sequential frames, its scan count, its area of 65536
  pixels, the image size and width checks, the draw budget, and its
  start again at a DesktopSize. One survived: turning `image/jpeg`'s
  `io.ErrUnexpectedEOF` into a plain error, since no image that passes
  the marker walk makes `image/jpeg` return it. It is kept in case one
  does.
- **Follow-ups.**
  - QEMU's VNC server, for recorded traffic and the integration test.
  - A per-connection JPEG quality for slow upstream links, which the
    graphics pipeline would have to treat as lossy pixels.
  - TigerVNC in the [006](../006-demo-stack-refresh.md) stack, so that
    the end-to-end matrix covers a server other than x11vnc.
  - Phase 3's LastRect ends a Tight server's updates of unknown length
    (`0xFFFF` rectangles); the decoders need nothing for it. Done in
    phase 3.
  - Closing the zlib tail's gap: keep the last 32 KiB of output in a
    ring, and whenever compressed bytes are left after the pixels, read
    them through the inflater, failing at the first byte it hands out,
    and restart it with the ring as its dictionary. That covers a second
    flush as well, and needs a measurement on a quiet host of what the
    copy into the ring costs, against 5%.
  - Inflating is the decoding cost of ZRLE and Tight, and a single zlib
    stream cannot be split across cores. If full-screen motion at 4K
    matters before beta, a faster pure-Go inflater is the lever.

## Implementation notes: phase 3

- **Status.** Phase 3 is done: VeNCrypt, ExtendedDesktopSize,
  ContinuousUpdates with Fence, QEMU Extended Key Events, Extended
  Clipboard and LastRect, each in a file of its own (`vencrypt.go`,
  `desktopsize.go`, `continuous.go`, `qemukeys.go`, `clipboard.go`,
  `extensions.go`), in about 1,500 lines of code and 2,000 of tests.
  Statement coverage stays at 98%. Nothing wires the backend into
  `occamyd` yet, and `internal/config` has no keys for the new options
  (follow-ups; both done since, see Implementation notes:
  registration).
- **What was built.**
  - `SetEncodings` lists the pseudo-encodings ExtendedDesktopSize,
    LastRect, ContinuousUpdates, Fence, QEMU Extended Key Event and
    Extended Clipboard after phase 2's list (CopyRect, Tight, ZRLE,
    Hextile, Raw, Cursor, DesktopSize). Code shared with phase 2 changes
    little: the decoder's rectangle switch and message switch fall
    through to `extensions.go`, the encodings list gains an `extensions`
    list after it, and the handshake's security switch calls
    `veNCrypt`. The decoder tells a `peer`, the Run's state, what the
    server's extension messages ask for; the writer sends what is due.
  - Phase 3 merged after phase 2. An ExtendedDesktopSize that changes
    the size resizes through DesktopSize's code, so it starts phase 2's
    per-update draw budget again, and one of the size the screen has
    does not, so a server cannot reset the budget with it.
    `TestUpdateDrawBudget` covers both; keeping the count across the
    resize, or resetting it for every ExtendedDesktopSize, makes it
    fail. Pseudo-encodings cost nothing against the budget, and LastRect
    ends the update as the rectangle count would. Phase 2's test helper
    `desktop` is now `desktopImage`, since phase 3 has a type of that
    name.
  - **VeNCrypt 0.2.** The backend chooses VeNCrypt whenever a server
    offers it. Of the subtypes it takes X509Plain (262), X509Vnc (261)
    and X509None (260), in an order set by the configuration: Plain
    first with a username, VNC Authentication first with only a
    password, None first with neither, so that nobody is asked for
    credentials a server does not need. TigerVNC also lists None and VNC
    Authentication among the subtypes; they run without TLS, and are
    chosen as outside VeNCrypt unless TLS is required. Before RFB 3.8,
    VeNCrypt with the None subtype has no SecurityResult, as TigerVNC
    sends none for it (`SConnection::approveConnection` tests the
    subtype); every other subtype has one. TLS runs over the
    same connection with `crypto/tls`, at least TLS 1.2; a server that
    sent anything after its ack and before the client's hello is a
    protocol error, since those bytes would be read as plain text.
  - **Plain** sends `Config.Username` and `Config.Password`, the same
    password VNC Authentication uses. The owner is asked, through
    `Sink.Prompt`, for whichever the configuration lacks, in one prompt:
    `VNC username and password for host:port` with the fields `Username`
    and `Password` (secret), or one of them.
  - **The TLS policy** (`Config.TLS`) mirrors the SSH backend's host
    keys. By default the certificate must chain to the system's roots and
    carry the host of `Config.Addr`, an IP address included, or
    `TLS.ServerName`. `TLS.RootCAs` replaces the roots, for a private CA.
    `TLS.Pins` accepts certificates as they are, ignoring chain, names
    and validity, as SSH host keys are; a pin is a certificate in PEM or
    its SHA-256 fingerprint, in hex as `openssl x509 -fingerprint
    -sha256` prints it (colons and case optional) or in base64 after
    `SHA256:`. `TLS.InsecureSkipVerify` accepts any certificate that is
    not pinned and logs a warning with its subject and fingerprint. A
    certificate the policy refuses fails Run with a `CertificateError`
    that carries the subject, the fingerprint (what `Pins` takes) and the
    reason (`x509.HostnameError`, `x509.UnknownAuthorityError`, or not
    pinned).
  - **ExtendedDesktopSize.** A `Resize` becomes `SetDesktopSize` once the
    server has sent an ExtendedDesktopSize rectangle, which servers do in
    answer to the first request for the whole screen. The layout sent is
    one screen covering the framebuffer, with the id and flags of the
    server's first screen, as noVNC sends it. One `SetDesktopSize` awaits
    its answer at a time, and the newest `Resize` asked for meanwhile
    follows it; a size beyond the limits, or the size the screen already
    has, is not sent. A rectangle that changes the size is a resize like
    DesktopSize, under the same limits and the same once-an-update and
    100 ms rules. A refusal changes nothing. The size refused is not
    asked for again until the server resizes the screen on request (the
    latest 16 refused sizes are kept), so each is logged once, as a
    warning with the size and the reason; after status 1, resizing
    administratively prohibited, no size is asked for on that
    connection. A forwarded request is logged at debug level and refuses
    nothing.
  - **ContinuousUpdates with Fence.** Once the server has sent both a
    Fence and EndOfContinuousUpdates, the backend enables continuous
    updates for the whole screen while a viewer can see it, again after
    a resize, and disables them while nobody can. While they are on, it
    sends no incremental requests; the first request, and the one after a
    resize, still ask for the whole screen. Every Fence that asks for an
    answer gets one, with its payload and BlockBefore, through a queue of
    its own that the writer drains in order.
  - **QEMU Extended Key Events.** Once the server has sent the
    pseudo-encoding, a `Key` whose `Code`, a USB HID usage, has an XT
    keycode goes as an extended event, with its keysym, even a zero one.
    A key goes up as it went down: one pressed as a KeyEvent before the
    server's support arrived is released as one. A key without a code,
    or with one the table lacks, goes as a KeyEvent, or not at all
    without a keysym. The table covers the keyboard page's usages 0x04
    to 0xe7, generated from keycodemapdb's `keymaps.csv` (USB to AT set
    1, with an E0 prefix as the high bit, as QEMU numbers keys). Lang1
    and Lang2, the Korean keys, are the single set 1 bytes they send,
    0xf2 and 0xf1, as keycodemapdb has them; the first generation left
    them out, and review caught it.
  - **Extended Clipboard.** The backend answers the server's caps with
    its own: text, the actions request, peek, notify and provide, and
    4 MiB of text taken unasked. Before the server's caps, and with a
    server whose caps lack text, the owner's text goes as Latin-1 cut
    text as in phase 1. Otherwise the owner's copy notifies the server,
    queued with the other input so that it arrives before the keys that
    paste it, and the text is kept for the server to ask for; a server
    that takes no notifications gets the text unasked. A notification of
    text from the server is answered with a request, and a peek with a
    notification. The backend sends no action that the server's caps
    lack. Text both ways is UTF-8 with CR LF line ends and a terminating
    NUL; text from the server is cut at its NUL, its line ends become LF,
    invalid UTF-8 becomes U+FFFD, and text beyond 4 MiB is cut there,
    with a character cut in two dropped, and a warning.
  - **LastRect** ends an update that counted its rectangles as 65535.
  - Tests: the fake server runs VeNCrypt with real TLS (`tls.Server`),
    records the subtypes chosen and Plain's credentials, parses the new
    client messages, and encodes the new server messages. A test file
    per extension runs the backend against it. The TLS tests make a CA, a
    server certificate for localhost and 127.0.0.1 that the server
    presents with the CA after it, a self-signed one and another CA, and
    cover each policy: a private CA by address, by name and by
    `ServerName`; a wrong host; an untrusted root; the system's roots;
    each form of pin; a pin that ignores the name; another certificate
    pinned; the CA pinned, which does not pin the leaf; insecure mode
    with its warning; and New's errors. Servers that speak only TLS 1.0
    or 1.1 are refused. VeNCrypt's None subtype is run in RFB 3.7 and
    3.8, by the backend and by a scripted server.
  - Fuzzing: seeds for every new server message and six VeNCrypt
    handshakes. `FuzzServerMessages` now runs a Run's state as the
    decoder's peer and the writer's choice of what is due after each
    message, so the extensions' state is fuzzed too. 60 s of it and 45 s
    of `FuzzHandshake` found nothing.
  - Integration: `TestTigerVNC`, behind the `integration` tag, runs
    against TigerVNC's Xvnc (`docker/test/tigervnc`, Debian trixie's
    TigerVNC 1.15) offering only X509Vnc and X509Plain. It builds the
    image and starts the container itself, with podman or
    `OCCAMY_CONTAINER_ENGINE`, as the RDP spike's test does: both are
    named `occamy-vnc-it-until<time>-<pid>` after the time the run ends
    at the latest, the port is the engine's choice, and the run removes
    them, or the next run once that time has passed. A later run removes
    only names that are a run's whole, after any registry and path and
    before any tag: review found that matching the prefix anywhere in a
    name, as the RDP spike's test did (it now matches as narrowly),
    would remove other people's containers and images on a shared host.
    Phase 4 narrows this to a bare name or one after `localhost/`.
    `TestExpired`, outside the tag, pins the matcher. It skips without an engine;
    `OCCAMY_VNC_TLS_ADDR` points it at a running server instead.
    It connects with a private CA by address and by name, a pinned
    fingerprint, insecure mode with its warning, Plain with a configured
    and with a prompted password; it is refused with a wrong host, the
    system's roots, another pin, and wrong Plain and VNC passwords; and
    with the CA it checks that TigerVNC enables continuous updates,
    ExtendedDesktopSize, QEMU key events and Extended Clipboard (caps
    `0x1f000001`), that a `Resize` to 800×600 and back resizes the
    screen, that continuous updates stop and resume with demand, and that
    `owner: naïve € ✓` copied by the owner comes back as `echo: naïve € ✓`
    through a loop on the X display that answers the clipboard with
    `xclip`. It passed every run, under the race detector, and takes
    about 30 s with the image's layers cached.
- **Decisions made during implementation.**
  - The anonymous TLS subtypes (TLSNone, TLSVnc and TLSPlain, 257 to
    259) are never chosen: anonymous Diffie-Hellman authenticates
    nobody, so anyone between the backend and the server reads
    everything, and `crypto/tls` does not implement it. Plain without
    TLS (256) is not chosen either, since it sends the password in the
    clear. A server whose VeNCrypt subtypes are only these fails with
    `ErrNoSecurityType`, even if it also offers None or VNC
    Authentication outside VeNCrypt: types [19, 2] with the subtype
    [258], which phase 1 reached with VNC Authentication, now fail. The
    subtypes arrive only after VeNCrypt is chosen, and RFB has no way
    back to the other types short of a new connection, which the backend
    does not make. Choosing VeNCrypt only when TLS is required would
    give up TLS with every server that offers it, so the backend keeps
    the preference, `TLS.Require` or not. TigerVNC is unaffected, since
    it lists its plain types among the subtypes too.
  - VeNCrypt is preferred over None and VNC Authentication whenever it
    is offered, and fails closed: a TigerVNC with a self-signed
    certificate, which phase 1 reached with VNC Authentication, now
    fails with a `CertificateError` until its fingerprint is pinned, its
    CA is configured, or insecure mode is on. The error shows the
    fingerprint to pin.
  - The certificate is checked in `tls.Config.VerifyConnection`, with
    `InsecureSkipVerify` set so that `crypto/tls` does not check it
    first. Pins and insecure mode then share one path, and a refusal
    carries the fingerprint. `gosec` G402 is suppressed there with that
    reason.
  - Pins replace the roots, so `New` refuses `RootCAs` with `Pins`. A
    pinned certificate in insecure mode is accepted without a warning;
    any other is accepted with one.
  - `TLS.Require`, not in the phase's scope, refuses servers that do not
    offer VeNCrypt with an X.509 subtype: a 3.3 server that chooses None
    or VNC Authentication, a server that does not offer VeNCrypt, and
    VeNCrypt's plain subtypes. Without it, anyone between the backend
    and the server could strip VeNCrypt from the types offered. It is
    implied by `RootCAs` and `Pins`, since whoever configures trust for a
    server expects TLS.
  - ExtendedDesktopSize that reports the size the screen already has
    changes nothing. TigerVNC sends one in answer to every request for
    the whole screen, and the backend asks for the whole screen after
    every resize, so treating it as a resize would loop.
  - A `Resize` before the server's first update is kept, not dropped,
    since the server shows its support only then; without support it is
    never sent (decision 4).
  - Re-enabling continuous updates when a viewer comes back sends no
    request: TigerVNC keeps the damage outside the requested region and
    sends it once the region is requested again.
  - Fences: BlockAfter and SyncNext are cleared from answers, since the
    reader goes on before the answer is written; the backend sends no
    Fence of its own, since it never changes the pixel format.
  - The clipboard caps take 4 MiB of text unasked, the size of cut text
    phase 1 takes, rather than rfbproto's recommended 0: with text as
    the only format, unasked text is not ambiguous, and it saves a round
    trip. TigerVNC then sends small text unasked instead of a
    notification.
  - The server's text grows a buffer as it decompresses, up to the 4 MiB
    kept, never to the size the stream claims; the rest of the message
    is skipped without decompressing it. A stream that does not
    decompress is skipped with a warning, since the message's length
    still frames it.
- **Deviations from the spec.**
  - The scope said VeNCrypt "with TLS and X.509 subtypes"; the anonymous
    TLS subtypes are left out, as decided above, and the scope bullet
    and the task breakdown now say so.
  - `TLS.Require` is an addition.
  - Decision 4 now says that a `Resize` before the server shows its
    support waits for it.
  - The integration image makes its own CA and server certificate when
    it is built, and the test reads `ca.pem` and `server.pem` out of
    the container, rather than the certificates coming from the build
    context, so that no key material is in the repository and every
    build has fresh keys.
- **Mutation checks.** Each change below to the security-relevant code
  made the named tests fail, and was reverted:
  - `verify` accepting every certificate: the wrong host, untrusted
    root, system roots, self-signed and other-pin cases of
    `TestTLSPolicy`, its insecure cases (no warning), and
    `TestCertificateErrorMessage`.
  - Verification without the server's name: the wrong-host case.
  - Every pin matching: the other-pin case.
  - Pins compared against the chain's last certificate instead of the
    leaf: undetected at first, since the fake server presented the leaf
    alone. The fake server now presents its CA after the leaf, as real
    servers do, and the pinned-certificate cases fail. Keep the CA in
    that chain.
  - Insecure mode without its warning: both insecure cases.
  - `TLS.Require` ignored in `choose`, in `chooseSubtype`, or for 3.3:
    the required-TLS cases of `TestVeNCrypt` and `TestChoose`, one for
    each.
  - Plain sending the password as the username: every Plain case of
    `TestVeNCrypt` and the script test of X509Plain.
  - The prompt skipped: the prompted cases of `TestVeNCrypt`,
    `TestPlainCredentialErrors` and `TestPasswordPrompt`.
  - The prompt's answers swapped: the case that asks for both.
  - Pins matching any certificate in the chain: undetected until
    review. `TestTLSPolicy` now pins the CA, and that case fails.
  - `MinVersion` at TLS 1.0: the TLS 1.0 and 1.1 cases of
    `TestTLSVersion`.
  - SecurityResult skipped by the top-level type, not VeNCrypt's
    subtype: the 3.7 None-within cases of `TestVeNCrypt` and
    `TestVeNCryptScripts`.

  Outside the security code, each of these made
  `TestExtendedDesktopSize` fail: a refused size sent again, a
  prohibition ignored, and the refused sizes never forgotten. Passing
  plain cut text through a string and back again made the Latin-1 case
  of `TestCutTextAllocations` fail; the UTF-8 case, bounded at five
  times for the race detector, catches only phase 1's whole path.
- **Surprises / gotchas.**
  - TigerVNC lists the plain security types among VeNCrypt's subtypes,
    so a VeNCrypt-only offer may still mean no TLS.
  - TigerVNC sends ExtendedDesktopSize for every request for the whole
    screen, and enables continuous updates only for clients that take
    Fences.
  - TigerVNC logs `TLS shutdown failed` and a broken pipe whenever the
    backend disconnects, since Run closes the TCP connection without
    TLS's close_notify. It is harmless, since RFB frames its own
    messages, and the TLS tests that fail on purpose also log `TLS
    Handshake failed: A TLS fatal alert has been received`.
  - TigerVNC blacklists an address after five failed logins, and PAM
    delays a wrong Plain password by about 2 s. The test image raises
    the blacklist threshold, since the integration test fails to log in
    on purpose.
  - Reading the server's text with `io.CopyN` into a `bytes.Buffer`
    allocated 16 MiB for 4 MiB kept: `ReadFrom` grows a full buffer for
    512 more bytes, doubling it to 8 MiB. For Extended Clipboard,
    `bytes.ToValidUTF8` and two `bytes.ReplaceAll` then copied the text
    three more times. Phase 1's plain cut text, read the same way, then
    went through a string and back, two more copies: 28 MiB for 4 MiB
    kept, the sink's copy included, as review found. Both now read
    through `readText`, whose buffer grows to
    the kept size at most. Extended text is converted in place; plain cut
    text is passed on as read if it is UTF-8, and Latin-1 is converted
    once, into a buffer of its exact UTF-8 size. 4 MiB of either kind of
    text allocates 11.7 MiB, the sink's copy included (14.4 MiB for
    Extended Clipboard and 15 MiB for cut text under the race detector);
    4 MiB of Latin-1, 8 MiB as UTF-8, allocates 24 MiB (27 MiB).
    `TestExtendedClipboardAllocations` holds Extended Clipboard to four
    times the bytes kept, and `TestCutTextAllocations` cut text to five,
    since the race detector adds more there, and Latin-1 to eight; the
    copies removed would take cut text to seven.
  - keycodemapdb maps Backslash and Non-US # to one XT keycode, 0x2b:
    they are one key, on US and ISO keyboards.
- **Follow-ups.**
  - Configuration v1 keys for the Sessions task, which registers
    backends; `internal/config` is unchanged here. A VNC connection
    needs `username`, and a `tls` block with `require`, `server_name`,
    `ca_file` (read into `RootCAs`), `pins` (a list) and
    `insecure_skip_verify`; and, since phase 4, `ard`. All but `ard`
    were done in the Sessions task's review (#111), with
    `vnc.CheckPin` for the configuration's checks; see
    api-and-server-stack.md. `ard` was added with the opt-in period;
    see Implementation notes: registration.
  - Protocol v1's web client must fill `Key.Code` from
    `KeyboardEvent.code` for QEMU key events to be used; the current
    Guacamole-protocol client sends keysyms only.
  - session-core: any viewer whose input is accepted can send `Resize`,
    which now resizes the remote screen for everyone. Whether only the
    owner may resize is session-core's to decide.
  - Send TLS's close_notify on a clean disconnect, without letting a
    server that does not read delay Run's return.
  - TigerVNC in the demo stack, and QEMU's VNC server, which would
    exercise QEMU key events end to end; the integration test checks
    only that TigerVNC enables them.
  - Clipboard amplification: an Extended Clipboard provide of about
    8 KB of zlib decompresses to the 4 MiB kept, which goes to every
    viewer, and nothing limits how often a server sends one. A broken or
    hostile server can so make each viewer receive megabytes for every
    few kilobytes it sends. A byte budget or rate per connection, here
    or in session-core, would bound it.
  - Fence answers queue 16 deep. Once 16 wait, the reader waits for the
    writer to take one, which keeps them in order and bounds memory. A
    server that sends Fences but does not read what the backend writes
    fills the TCP send buffer: the writer blocks in its write, the
    reader blocks behind it, and the session hangs with a frozen screen
    until Run's context is cancelled, which closes the connection, or
    the server closes it. A write deadline would turn that into an
    error; none is set, as in phase 1, where a server that stops reading
    blocks the writer the same way.

## Implementation notes: phase 4

- **Status.** Phase 4 is done but for its check against a Mac: Apple
  Remote Desktop authentication, security type 30, in `ard.go`, about
  200 lines of code and 800 of tests, behind `Config.ARD`, which is off
  by default. No Mac with Screen Sharing on was at hand, so the check by
  hand below is still to be run. Statement coverage of the package is
  97%. Nothing wires the backend into `occamyd` yet, and
  `internal/config` has no key for `ARD` (follow-ups; both done since,
  see Implementation notes: registration).
- **What was built.**
  - **The exchange**, as libvncclient (`HandleARDAuth`), noVNC
    (`_negotiateARDAuth`) and TigerVNC's viewer (`CSecurityDH`)
    implement it: the server sends a 2-byte generator, a 2-byte key
    length n, its prime and its public key in n bytes each. The client
    makes a private key x, takes the MD5 of the shared secret, y^x mod p
    in n bytes with its leading zeros, as all three keep them, as an
    AES-128 key, and sends 128 bytes of credentials encrypted with it in
    ECB mode, each block on its own: the username in the first 64 and the
    password in the second, each ending in a NUL, the rest of each field
    random. Its own public key follows, in n bytes, in the same write.
    SecurityResult follows, with the reason in 3.8.
  - **Allowed or not.** `Config.ARD` allows type 30. Without it, a
    server that offers 30 and nothing else the backend takes fails with
    `ErrNoSecurityType` and an error that names the setting: `the server
    offers [30 33 36 35]; Apple Remote Desktop authentication (type 30) is
    not allowed, since its key exchange authenticates no server and anyone
    in between could read the password: set Config.ARD to allow it`. Only
    the type numbers are shown, never a credential. A 3.3 server that
    chooses 30 is handled the same way. `New` refuses `ARD` together
    with a TLS requirement (`TLS.Require`, `RootCAs` or `Pins`), under
    which type 30 would never be chosen. Every login with type 30 logs a
    warning, as insecure TLS does.
  - **Bounds**, checked before anything is read or allocated for them,
    and before the owner is asked for anything, in TigerVNC's order: the
    key length must be 128 to 1024 bytes, so that at most 2 KiB is
    allocated for the key; the prime more than 1; the generator and the
    server's public key within [2, p-2], the range OpenSSL's
    `DH_check_pub_key` accepts, which leaves out 0, 1 and p-1, whose
    secrets anyone can tell.
  - **Credentials** come from `Config.Username` and `Config.Password`;
    the owner is asked through `Sink.Prompt` for whichever is missing,
    in one prompt, as for VeNCrypt's Plain (`VNC username and password
    for host:port`, the fields `Username` and `Password`, the latter
    secret). Each may be at most 63 bytes, which with its NUL fill its
    64-byte field, and may not hold a NUL, where the server would cut it.
    Either is refused with an error that names the limit and not the
    credential: never cut short, as libvncclient and noVNC cut them, since
    a login with less than the owner gave fails, or succeeds as somebody
    else.
  - **Clearing.** The plaintext of the credentials, the shared secret's
    bytes, the MD5 key, and the words of the private key and of the
    secret are cleared once they have been used, the private key's on
    every path out, by a `defer` from where it is made. What Go does not
    let the backend clear is not: the strings that hold the configured
    or prompted credentials; the key schedule inside `crypto/aes`'s
    cipher; the copy of the MD5 key that `ardKey` returns by value,
    which leaves a copy in its frame; the buffer of random bytes inside
    `crypto/rand.Int`, from which the private key is made; and the
    temporaries of `math/big`'s exponentiation, among them the words
    it takes from its pool and returns without clearing.
  - **Tests**, in `ard_test.go`: a known-answer test of two vectors, each
    computed apart from the package (the exchange and the key with
    Python's `pow` and `hashlib`, the ciphertext with `openssl enc
    -aes-128-ecb -nopad`), which checks the server's and the client's
    public keys, the AES key, the plaintext, the ciphertext and the
    response, and that the plaintext and the key are cleared. The first
    is noVNC's own test of its handler, with a prime of bytes 0 to 127,
    whose expected ciphertext OpenSSL reproduces; its secret and public
    key start with two zero bytes, so it catches a key or a public key
    without its leading zeros. The second uses RFC 2409's group 2, as
    macOS 10.13 sent it. `TestARDScripts` runs scripted servers through the
    handshake: success in 3.3, 3.7, 3.8 and Apple's 3.889, with RFC
    2409's group, with a small prime, and with the generator 5 and the
    512-byte key reported from macOS 26.6; failures, macOS's
    little-endian one among them; each bound (key lengths 0, 127,
    1025 and 65535, a key cut short, primes 0, 1 and 3, generators 0, 1,
    p-1, p and beyond, public keys 0, 1, p-1, p and beyond); credentials
    of 63 bytes, 64 or more, and with a NUL; type 30 refused when not
    allowed and when TLS is required. Each case checks what the client
    sent and whether it asked for credentials, and that the error does
    not show them. `TestARD` runs the backend against the fake server,
    which now speaks type 30 in RFC 2409's group 2 and decrypts the
    credentials with code of its own: a login, macOS's types, the prompt
    for both credentials and for each, a long answer, a wrong password
    with the server's reason, refusals without `ARD`, the choice among
    VeNCrypt, VNC Authentication, None and type 30, the warning, and no
    password in the log. Others cover the private key's size and range,
    errors from the prompt and the random source, `New`, allocations,
    write errors, and `choose`.
  - **The review's tests.** An independent review of the pull request
    computed four more exchanges from the wire format alone, with
    Python and OpenSSL: RFC 2409's group 2 with a private key searched
    for so that both the secret and the client's public key start with
    a zero byte, an odd key length of 129 bytes with the generator 5,
    p = 7 in 128 bytes, and credentials of 63 bytes. `TestARDVectors`
    (`ard_vectors_test.go`) runs each through the handshake in 3.3,
    3.7, 3.8 and 3.889, with the server's bytes written out by hand and
    the random stream fed through `crypto/rand.Int`, and compares every
    byte the client writes. `TestARDFreshRandomness` runs the handshake
    with the default random source, as `Run` does, and checks that
    three connections send three responses and that the public key is
    no small power of the generator; every other test feeds a source
    of its own, so a constant default had passed them all.
  - **Fuzzing.** `FuzzHandshake` allows type 30, with keys made from a
    fixed seed so that an input runs the same way twice, and gained five
    seeds: macOS's offer and a login, a failure, macOS's little-endian
    failure, a 3.3 server, and a small prime. 60 s of it ran 5.2 million
    inputs and found 258 new ones and no failure.
  - **By hand:** `TestMacOS` (`ard_integration_test.go`, behind the
    `integration` tag) reads a Mac's version and offer, and the group
    that type 30 sends, disconnecting before any credential is sent;
    checks that the backend refuses type 30 without `ARD`; and, given a
    Mac account, logs in and waits for the screen. It skips without
    `OCCAMY_VNC_ARD_ADDR`. It ran against the fake server, not a Mac.
- **What was not done.**
  - **The check against a Mac.** Nothing listened on 127.0.0.1:5900 on
    the development Mac (macOS 26.6): `netstat` showed no listener on
    port 5900, and `com.apple.screensharing` was not loaded, so no
    connection was made, and no system setting was changed to make one.
  - **Apple's other types.** Published captures show what macOS
    offers: 30, 33, 36 and 35 from High Sierra (10.13, Tenable's in 2017)
    and from 10.15.6 (Apple's developer forums); 30, 33 and 36 from macOS
    26.6.2 (okou issue 35044); and 30, 33, 36, 2 and 35 from a Mac mini
    with "VNC viewers may control screen with password" on (Termix
    issue 1015). libvncserver's discussion of Apple's high performance
    mode (issue 696) names 33 RSA with SRP, 35 Kerberos and 36 SRP, which
    rest on Apple's own key exchanges and, for 35, a Kerberos realm. Only
    30 is implemented; the others are follow-ups.
  - **Type 30 within TLS.** No server offers it: TigerVNC's server
    implements VeNCrypt's X.509 subtypes with None, VNC Authentication
    and Plain inside (`SecurityServer.cxx`), and type 30 only in its
    viewer (`CSecurityDH`); libvncserver has no server side of type 30;
    macOS offers no VeNCrypt; and VeNCrypt has no subtype for it. So the
    backend does not choose 30 among VeNCrypt's subtypes, and the only
    path to type 30 is `ARD`.
  - The `internal/config` key (follow-ups; done since, see
    Implementation notes: registration).
- **Decisions made during implementation.**
  - **Off by default, and a plain switch.** `Config.ARD` is a bool, as
    phase 3's options are flat, rather than a list of allowed types:
    type 30 is the only type the backend takes that needs consent. With
    it, an attacker between the backend and the Mac reads the account's
    password; without it, the Mac is out of reach. It also raises what
    a downgrade gets from any server: without a TLS requirement, an
    attacker in between can strip VeNCrypt from the offer and offer
    type 30 alone, and is sent the whole password, where without `ARD`
    the most it gets is VNC Authentication's DES response from the
    password's first 8 bytes. `Config.ARD`'s documentation says so,
    and to set it only for Macs, on trusted networks. The owner, not
    the backend, has to choose that.
  - **The choice.** VeNCrypt still comes first. Then, if `ARD` allows it:
    type 30 when a username is configured, even over VNC Authentication
    and None, since of the types here only 30 takes one, and the
    username says the password is a Mac account's; and otherwise only
    when the server offers neither None nor VNC Authentication. There is
    one `Password`: with a username it is the account's, and VNC
    Authentication would send it, cut to 8 bytes, as a DES response that
    fails and that an eavesdropper can attack offline. Without a
    username it is a VNC password, which type 30 would send as an
    account's. Type 30 is never chosen while TLS is required, as it has
    no TLS.
  - **The key length's bounds are TigerVNC's,** 128 to 1024 bytes,
    the strictest of the three references: libvncclient takes any
    length, 0 too, and allocates five times it, and noVNC any length.
    Every group reported from macOS fits: 128 bytes with the generator 2
    in Tenable's capture of 10.13, and 512 bytes with the generator 5
    from 26.6.2 in okou's report. Odd lengths are taken, as by all
    three. The bound is on the length the server sends, as TigerVNC's;
    a smaller prime within it, with leading zero bytes, is taken, and
    gets a private key of as many bits as it has.
  - **The group is not checked to be prime,** which would cost several
    exponentiations of the same size: a server that sends a bad group
    already holds the password it is sent, and an attacker in between
    reads it whatever the group.
  - **The private key has 256 bits,** uniform in [2, 2^256+1] from
    `crypto/rand.Int`, which draws uniformly below a bound by rejection,
    or in [2, p-2] for a prime of 256 bits or fewer. The brief asked for
    at least 256 bits, or as many as the prime has. The references use
    as many bits as the key length (TigerVNC and noVNC draw n random
    bytes, libvncclient with OpenSSL as many bits as the prime), or the
    key length in bits with libgcrypt, which is 128 for macOS's 1024-bit
    group. 256 is enough because the secret becomes an AES-128 key, and
    it keeps an 8192-bit group at 6.7 ms, where a full-size key costs
    about 200 ms (below).
  - The SecurityResult is a failure whenever it is not 0, which already
    held: macOS sends 1 in little-endian order (below).
- **Deviations from the brief.**
  - The brief refused "a username or password of 64 bytes or more,
    counting the NUL". Read literally, that refuses 63 bytes, which with
    their NUL fit the 64-byte field. The limit is 63 bytes without the
    NUL, the most that all three references send whole: TigerVNC refuses
    64 or more, noVNC cuts to 63, and libvncclient copies at most 64
    bytes, NUL included, so that a longer credential loses its NUL.
  - The key length is refused below 128 bytes, not only at 0 (above).
  - `New` refusing `ARD` with a TLS requirement is an addition.
  - The scope bullet now says that type 30 needs the configuration's
    consent, and that Apple's other types are out.
- **Measured.** `BenchmarkARD`, on one core of an Apple M5 Pro with the
  shared host's load average between 4 and 10 on 18 cores, the median
  of three runs: the client's side of the exchange (the private key, two
  exponentiations, the encryption) takes 0.16 ms with a 128-byte key,
  1.7 ms with 512 bytes and 6.7 ms with 1024, the largest taken. One
  exponentiation with a 1024-byte modulus took 3.3 ms with a 256-bit
  exponent and 100 ms with a full-size one, so a full-size private key
  would cost about 200 ms. A server can make the backend spend that once
  a connection, and not again without a new one.
- **Surprises / gotchas.**
  - macOS sends a failed SecurityResult as `01 00 00 00`, 1 in
    little-endian order, which a client that tests for 1 reads as
    success (Greenbone's forum, macOS Ventura 13.6.6). The length of its
    reason is big-endian. `TestARDScripts` holds the bytes.
  - Tenable's capture of High Sierra shows the generator 2, a key length
    of 128 and a prime starting `ffffffffffffffffc90fdaa22168c234`:
    RFC 2409's group 2, a safe prime, which the fake server uses.
  - noVNC's test vector uses a prime with a leading zero byte, which is
    what makes a secret without its leading zeros detectable; a test
    with RFC 2409's group alone would miss it 255 times in 256.
  - The first mutation of the private key's size survived: the test
    compared the key's bits with `ardExponent`, the constant that the
    mutation changed. It now says 256.
- **Mutation checks.** Each change below to the security-relevant code
  made the named tests fail, and was reverted:
  - Allowing type 30 without `ARD`, in `choose` and for a 3.3 server:
    the not-allowed cases of `TestARD`, `TestARDScripts` and
    `TestChoose`.
  - Type 30 chosen while TLS is required: `TestARDScripts`' required-TLS
    case and `TestChoose`.
  - Type 30 never preferred with a username, or always preferred:
    `TestARD`'s choice cases and `TestChoose`.
  - The error without the setting's name: `TestARD`, `TestARDScripts`
    and `TestHandshakeScripts`.
  - The key length's lower bound dropped, or lowered to 1: the cases of
    0 and 127. Its upper bound dropped: the cases of 1025 and 65535,
    and `TestARDAllocations`.
  - The prime's check dropped: the cases of primes 0 and 1, by their
    message only, since the generator's check refuses those primes too.
  - Either bound on the generator, or on the server's public key,
    dropped: their cases.
  - Credentials of 64 bytes allowed, or of any length: the long
    username and password cases. A NUL allowed: the NUL cases.
  - Credentials asked for before the key is read: every bound case,
    which checks that nobody is asked.
  - The MD5 of the secret without its leading zeros, or the public key
    without them: `TestARDKnownAnswer`.
  - A private key of 128 bits, after the test's fix above, or one up to
    p for a small prime: `TestARDPrivateKey`.
  - The plaintext or the AES key not cleared: `TestARDKnownAnswer`.
  - The username and the password swapped: `TestARDKnownAnswer` and
    `TestARD`.
  - A SecurityResult tested for 1, not for 0: macOS's little-endian
    failure.
  - The prompt skipped: `TestARD`'s prompted cases. The warning dropped:
    `TestARD`'s logins. `New` taking `ARD` with a TLS requirement:
    `TestARDNew`.
  - After the review: the default random source made constant, as a
    seeded ChaCha8 or as zeros: `TestARDFreshRandomness`, alone. The
    not-allowed error suggesting `ARD` while TLS is required:
    `TestARDScripts`' required-TLS case without `ARD`.
  - Not caught, since no test can see it from outside the functions:
    dropping the clearing of the private key, of the secret's words or
    of its bytes. The review found the same.
- **Phase 2's and 3's integration tests, run once.** Phase 2's
  `TestEncodings` had been compiled but not run since phase 3 merged.
  With images built from master's `docker/bench/desktop` and
  `testdata/servers` under names of this run's own, on ports the engine
  chose, and removed afterwards, on the shared host at a load average
  of 4 to 9:
  - `TestEncodings` passed against TigerVNC 1.15's Xvnc and against
    x11vnc 0.9.17 at 1024×768, and `TestBenchDesktop` against x11vnc.
    The lossless sessions are within 1% of phase 2's sizes (TigerVNC:
    Hextile 3.25 MB, ZRLE 1.46 MB, Tight 1.49 MB). The Tight JPEG session
    from TigerVNC took 0.97 MB, against 0.47 MB in phase 2, and x11vnc's
    0.12 MB as before. The test replaces the encodings it asks for but
    not phase 3's pseudo-encodings, so TigerVNC now runs continuous
    updates, and the session held 24 lossless zlib rectangles beside 52
    JPEG ones; that it refreshes the lossy areas losslessly unasked is
    the likely cause, unconfirmed.
  - `TestTigerVNC` passed under the race detector in 28 s. It found no
    earlier run's containers or images to remove, and removed its own.
- **The clean-up matcher.** `expired` took a run's name after any
  registry and path, so that a run would remove an image pulled as
  `docker.io/someone/occamy-vnc-it-until1-2`. It now takes a bare name,
  as docker lists images and both engines list containers, or one after
  `localhost/`, as podman lists an image built here, with a tag after
  it or none. `TestExpired` covers other registries and paths,
  `localhost:5000/`, and tags that are not tags.
- **Checking against a Mac by hand.** On a Mac you own, on a network
  you trust:
  1. Turn on System Settings › General › Sharing › Screen Sharing.
     Leave "VNC viewers may control screen with password" off first, so
     that the Mac offers Apple's types alone.
  2. From another machine, record what it offers, without logging in:
     `OCCAMY_VNC_ARD_ADDR=<mac>:5900 go test -tags integration -run
     TestMacOS -v ./internal/backend/vnc`. It logs the version, the
     types, and type 30's generator, key length and prime size, and
     checks that the backend refuses type 30 without `ARD`. To read the
     group it chooses type 30 and disconnects once the prime has
     arrived, so the Mac sees an exchange abandoned, not a login failed.
  3. Log in: add `OCCAMY_VNC_ARD_USER` and `OCCAMY_VNC_ARD_PASSWORD`,
     an account of that Mac. The test logs in with type 30 and waits for
     the screen.
  4. Turn the legacy VNC password on, and run step 2 again to see the
     offer with type 2.
  5. Record the macOS version and what the test logged here, and turn
     Screen Sharing off again.
- **Follow-ups.**
  - The check by hand above, on current macOS.
  - Configuration v1's `ard` key for VNC connections, with its risk
    documented and named by the error for a server that offers only
    type 30: done; see Implementation notes: registration.
  - Apple's types 33, 35 and 36, with SRP and Kerberos, would log in to
    a Mac without type 30's exposure; each needs a Mac to be built
    against.
  - The prompt says "VNC username and password" for a Mac account too;
    naming the Mac's account might help the owner.
  - `TestEncodings` replaces the encodings it asks for but not phase 3's
    pseudo-encodings, so its Tight JPEG session from TigerVNC doubled
    under continuous updates. Confirm why, or have it clear
    `extensions` as it replaces `encodings`.

## Implementation notes: registration

- **Status.** The backend is `vnc`'s default: it serves every vnc
  connection and ad-hoc target that does not set `backend: guacd` (or
  `adhoc_backends: {vnc: guacd}`), which goes through the guacd bridge
  as an explicit fallback and the benchmark's baseline
  ([guacd-bridge](guacd-bridge.md), Parity gate and Implementation
  notes: native by default). That is the owner's decision of
  2026-10-31: native is the default wherever a native backend exists.
  Before it, during the parity gate's opt-in period, the bridge served
  vnc by default and `backend: native` opted in; and before that, this
  backend served every vnc session, with no key.
- **What was done.**
  - `ard: true` on a vnc connection sets `Config.ARD`. Configuration
    v1 refuses it on other protocols, with `backend: guacd`, and
    with a `tls` that requires TLS (`require`, `ca_file` or `pins`),
    since type 30 runs without TLS. `docs/configuration.md` describes
    the risk. Ad-hoc targets cannot have it.
  - `Config.ARDSetting` names the key in the error for a server that
    offers only type 30: `ard: true on connection "studio-mac"`, or,
    for an ad-hoc target, a connection for it, since ad-hoc targets have
    no `ard`.
  - `Config.Width` and `Height` carry the owner's `hello` size, which
    occamyd's connection stream passed to no one before: `Input`
    returned `ErrNotRunning` for a `Resize` that came before `Run`. A
    `Resize` is now kept for the next `Run` in any case, so a session
    stream's owner, whose resize may reach the backend before the
    session's goroutine has started `Run`, is not lost either. Each
    `Run` asks for the newest size once the server shows
    ExtendedDesktopSize. A size beyond `MaxSide` or `MaxPixels`, from
    a `Resize` or from `Config`, is not asked for, so a hello of
    8192×8192, within `backend.MaxResizeSide` but over 2^25 pixels,
    leaves the server's size (`TestConfigSizeBeyondLimits`).
  - `internal/backend/vnc/vnctest` is a fake RFB 3.8 server for tests
    outside the package: None, VNC Authentication and VeNCrypt's X.509
    subtypes, Raw in the client's pixel format, Cursor, DesktopSize,
    ExtendedDesktopSize with SetDesktopSize, and cut text, answering
    update requests itself. It counts failed authentications and,
    right or wrong, the credentials clients send (`Credentials`), so
    a test can tell that a refused certificate got none. `Conn.Close`
    shuts the server's side down and reads until the client closes
    its own: closing with bytes unread reset the connection on Linux,
    which `TestServerEnds` saw once in CI. In a Linux/arm64 container
    under the race detector with `-cpu=1,2,4`, the old close failed 9
    of 900 runs and the new one none; a review on Linux/arm64 did not
    reproduce the reset with the old close at all.
    `server/vnc_e2e_test.go` runs the backend through occamyd's routes
    against it with the Go client, for connections and for ad-hoc
    targets by address and by name.
- **Follow-ups.**
  - `vnctest` duplicates part of the package's own fake server
    (`server_test.go`), which scripts every byte and reaches into the
    backend; the package's tests could move to `vnctest` where they
    need no scripting.
  - `vnctest` sends Raw only; the encodings stay tested inside the
    package and against TigerVNC.
