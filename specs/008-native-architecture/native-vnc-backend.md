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
  integration test uses; TigerVNC's Xvnc with VeNCrypt
  (`docker/test/tigervnc`), which phase 3's integration test uses, and
  which is still to be added to the stack; QEMU's VNC server; macOS
  Screen Sharing, checked by hand.

## Proposed scope for beta

The scope is delivered in four phases (see Task Breakdown).

- **Security types:** None (1), VNC Authentication (2), VeNCrypt (19)
  with its X.509 subtypes (Plain, None and VNC inner auth), and Apple
  Remote Desktop (30), so that macOS hosts work. VeNCrypt's anonymous
  TLS subtypes, which no certificate authenticates, are out (phase 3
  notes), as are RA2 and other proprietary types.
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
   resize.** A `Resize` input is then ignored; one that comes before the
   server has shown whether it supports the extension waits for its
   first update. With it (phase 3), `Resize` becomes `SetDesktopSize`.
   This matches
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
| 3 | VeNCrypt (X.509, with Plain, None and VNC inner authentication); ExtendedDesktopSize with `SetDesktopSize`; ContinuousUpdates with Fence; QEMU Extended Key Event with the HID-to-XT table; Extended Clipboard (UTF-8); LastRect | Phase 1 |
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
    QEMU in the fuzz corpus.
  - The PointerPos pseudo-encoding (-232) would report the remote
    pointer's moves to `Sink.CursorMove`; it is not in the scope above.
  - A server that answers incremental requests with empty updates would
    make the request loop spin at the round-trip rate; none seen so far.
  - Resizes are rate-limited by a fixed interval. A budget that scales
    with the screen's size would bound the work better: at the largest
    screen, ten resizes a second still reallocate 1.25 GiB a second.
  - Registration for `vnc` in the API's backend factory, and the parity
    gate against the guacd bridge.

## Implementation notes: phase 3

- **Status.** Phase 3 is done: VeNCrypt, ExtendedDesktopSize,
  ContinuousUpdates with Fence, QEMU Extended Key Events, Extended
  Clipboard and LastRect, each in a file of its own (`vencrypt.go`,
  `desktopsize.go`, `continuous.go`, `qemukeys.go`, `clipboard.go`,
  `extensions.go`), in about 1,500 lines of code and 2,000 of tests.
  Statement coverage stays at 98%. Nothing wires the backend into
  `occamyd` yet, and `internal/config` has no keys for the new options
  (follow-ups).
- **What was built.**
  - `SetEncodings` lists the pseudo-encodings ExtendedDesktopSize,
    LastRect, ContinuousUpdates, Fence, QEMU Extended Key Event and
    Extended Clipboard after phase 1's. Code shared with phase 2 changes
    little: the decoder's rectangle switch and message switch fall
    through to `extensions.go`, the encodings list gains an `extensions`
    list after it, and the handshake's security switch calls
    `veNCrypt`. The decoder tells a `peer`, the Run's state, what the
    server's extension messages ask for; the writer sends what is due.
  - **VeNCrypt 0.2.** The backend chooses VeNCrypt whenever a server
    offers it. Of the subtypes it takes X509Plain (262), X509Vnc (261)
    and X509None (260), in an order set by the configuration: Plain
    first with a username, VNC Authentication first with only a
    password, None first with neither, so that nobody is asked for
    credentials a server does not need. TigerVNC also lists None and VNC
    Authentication among the subtypes; they run without TLS, and are
    chosen as outside VeNCrypt unless TLS is required. TLS runs over the
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
    100 ms rules. A refusal is logged as a warning, or at debug level if
    the server forwarded the request, and changes nothing.
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
    pinned; insecure mode with its warning; and New's errors.
  - Fuzzing: seeds for every new server message and six VeNCrypt
    handshakes. `FuzzServerMessages` now runs a Run's state as the
    decoder's peer and the writer's choice of what is due after each
    message, so the extensions' state is fuzzed too. 60 s of it and 45 s
    of `FuzzHandshake` found nothing.
  - Integration: `TestTigerVNC`, behind the `integration` tag, runs
    against TigerVNC's Xvnc (`docker/test/tigervnc`, Debian trixie's
    TigerVNC 1.15) offering only X509Vnc and X509Plain. It connects with
    a private CA by address and by name, a pinned fingerprint, insecure
    mode with its warning, Plain with a configured and with a prompted
    password; it is refused with a wrong host, the system's roots,
    another pin, and wrong Plain and VNC passwords; and with the CA it
    checks that TigerVNC enables continuous updates, ExtendedDesktopSize,
    QEMU key events and Extended Clipboard (caps `0x1f000001`), that a
    `Resize` to 800×600 and back resizes the screen, that continuous
    updates stop and resume with demand, and that `owner: naïve € ✓`
    copied by the owner comes back as `echo: naïve € ✓` through a loop
    on the X display that answers the clipboard with `xclip`. It passed
    every run, under the race detector.
- **Decisions made during implementation.**
  - The anonymous TLS subtypes (TLSNone, TLSVnc and TLSPlain, 257 to
    259) are never chosen: anonymous Diffie-Hellman authenticates
    nobody, so anyone between the backend and the server reads
    everything, and `crypto/tls` does not implement it. Plain without
    TLS (256) is not chosen either, since it sends the password in the
    clear. A server that offers only these fails with
    `ErrNoSecurityType`; there is no reconnect to try other types.
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
    it is built, and the test copies `ca.pem` and `server.pem` out of
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
    512 more bytes, doubling it to 8 MiB. `bytes.ToValidUTF8` and two
    `bytes.ReplaceAll` then copied the text three more times. The buffer
    now grows to the kept size at most and the text is converted in
    place: 4 MiB of text allocates 11.7 MiB, the sink's copy included
    (14.4 MiB under the race detector), and
    `TestExtendedClipboardAllocations` holds it to four times the text.
  - keycodemapdb maps Backslash and Non-US # to one XT keycode, 0x2b:
    they are one key, on US and ISO keyboards.
- **Follow-ups.**
  - Configuration v1 keys for the Sessions task, which registers
    backends; `internal/config` is unchanged here. A VNC connection
    needs `username`, and a `tls` block with `require`, `server_name`,
    `ca_file` (read into `RootCAs`), `pins` (a list) and
    `insecure_skip_verify`.
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
