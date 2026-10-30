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
  stack and on the bench desktop (`docker/bench/desktop`), which the
  integration tests use; TigerVNC's Xvnc, in an image on top of the bench
  desktop (`internal/backend/vnc/testdata/servers`) that phase 2's
  integration test uses, and still to be added to the stack; QEMU's VNC
  server; macOS Screen Sharing, checked by hand.

## Proposed scope for beta

The scope is delivered in four phases (see Task Breakdown).

- **Security types:** None (1), VNC Authentication (2), VeNCrypt (19)
  with TLS and X.509 subtypes (Plain, None and VNC inner auth), and Apple
  Remote Desktop (30), so that macOS hosts work. RA2 and other proprietary
  types are out.
- **Encodings:** Raw, CopyRect, Hextile, ZRLE and Tight (including JPEG and
  the gradient filter). JPEG is decoded when a server sends it, but the
  backend asks for no quality level, so servers keep Tight lossless (see
  phase 2's notes).
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
- **Update model:** with ContinuousUpdates the server pushes, and is
  paused while nobody can see. Without it, the backend asks for an
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
   regression tests. Seeds are encoded by the fake server's encoders.
   Traffic recorded from x11vnc and TigerVNC, a short session of each
   phase 2 encoding, joined them in phase 2, since those decoders are
   where recorded traffic matters most; QEMU's is still to be recorded.

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
  `backend.Backend` with `Config`, `New` and `Backend`, in about 1,400
  lines of code and 2,500 of tests. Nothing wires it into `occamyd`: the
  sessions task registers backends.
- **What was built.**
  - Run dials with the context, runs the handshake, sends
    `SetPixelFormat` and `SetEncodings` (CopyRect, Raw, Cursor,
    DesktopSize; phase 2 adds Tight, ZRLE and Hextile) in one write, reports the size with `Sink.Resize` and
    the desktop name with `Sink.Title`, and then applies the server's
    messages until the connection ends. A writer goroutine sends queued
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
    ignored. `Resize` is ignored (decision 4) and `TermInput` fails with
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
    requires; Extended Clipboard (phase 3) removes the asymmetry.
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
  - Registration for `vnc` in the API's backend factory, and the parity
    gate against the guacd bridge.

## Implementation notes: phase 2

- **Status.** Phase 2 is done: Hextile, ZRLE and Tight, with Tight's
  fill, JPEG, and its copy, palette and gradient filters, in
  `hextile.go`, `zrle.go` and `tight.go`, the zlib streams in `zlib.go`
  and the state they share in `codec.go`: about 1,050 lines of code and
  2,000 of tests. The backend asks for them in `SetEncodings`.
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
    A JPEG rectangle may be at most 2048 pixels wide, as TigerVNC's viewer
    allows, and 2048×2048 in all, since `image/jpeg` allocates the whole
    image; its buffer grows as the data arrives rather than to the length
    claimed, and the image's size must be the rectangle's before it is
    decoded.
  - Decompression bombs: the inflater runs only as far as the pixels
    read from it, so a rectangle inflates no more than its pixels and a
    few tens of KiB beyond them, the inflater's 32 KiB window and a 32 KiB
    buffer. Inflated data that the pixels do not take is an error. Up to
    64 compressed bytes may be left after the pixels, the end of a
    server's flush, and are kept for the stream's next rectangle; more is
    an error. One case goes unseen: when the inflater's window fills
    exactly where the pixels end, it holds back what follows until the
    flush, so extra data whose compressed form fits in those 64 bytes
    starts the stream's next rectangle. That garbles only the server's
    own pixels.
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
- **Surprises / gotchas.**
  - Go's `compress/flate` hands out inflated data only at the end of a
    stored block, when its 32 KiB window fills, and at the end of the
    stream, not at the end of each block. A server's `Z_SYNC_FLUSH` ends
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
    client waiting; it now asks again until it has every pixel.
  - Neither server sent a gradient rectangle in these sessions, only the
    copy and palette filters, so the gradient filter is tested with
    hand-built data and the fake server's encoder alone.
  - `bufio.Reader` reads a request at least as large as its buffer
    straight from the underlying reader, so inflated data beyond a large
    Tight rectangle's pixels stayed in the inflater, unseen by the check
    for it, and started the next rectangle. Reads now go through the
    buffer in pieces smaller than it, and the buffer is as large as the
    window, the most the inflater hands out at once.
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
  inflated data left over, keeping the zlib tail, its 64-byte bound, the
  gradient's clamp and its first pixel, a coloured Hextile subrectangle
  setting the foreground, the Hextile subrectangle bound at edge tiles,
  Tight's palette index check, the ZRLE and Tight length bounds, and the
  JPEG image size and width checks.
- **Follow-ups.**
  - QEMU's VNC server, for recorded traffic and the integration test.
  - A per-connection JPEG quality for slow upstream links, which the
    graphics pipeline would have to treat as lossy pixels.
  - TigerVNC in the [006](../006-demo-stack-refresh.md) stack, so that
    the end-to-end matrix covers a server other than x11vnc.
  - Phase 3's LastRect ends a Tight server's updates of unknown length
    (`0xFFFF` rectangles); the decoders need nothing for it.
  - Inflating is the decoding cost of ZRLE and Tight, and a single zlib
    stream cannot be split across cores. If full-screen motion at 4K
    matters before beta, a faster pure-Go inflater is the lever.
