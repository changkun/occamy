---
title: Go client core
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/graphics-pipeline.md
affects:
  - client/
  - tests/
effort: medium
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# Go client core

## Design Problem

Design one pure-Go protocol v1 client library that serves three users
with very different needs:

- the end-to-end and pressure tests, which want a plain Go API;
- command-line tools, such as screenshots and scripted input;
- the Swift app ([010](../010-native-swift-client.md)), which links the
  library through a `c-archive` C ABI.

The Swift constraint shapes the API, so it has to be settled before the
library is written.

## Context

- Protocol encoding lives in the public `protocol` package
  ([protocol-v1](protocol-v1.md)). Tile codecs come from
  [graphics-pipeline](graphics-pipeline.md).
- cgo export rules: C may not keep Go pointers after a call returns
  unless they are pinned (`runtime.Pinner`). Long-lived handles go through
  `runtime/cgo.Handle`. Exported functions are called on arbitrary OS
  threads, and Go callbacks into C run on Go-created threads.
- The v0 harness used in the revival (a WebSocket client that counts
  opcodes, appended to [006](../006-demo-stack-refresh.md)) is the
  baseline the end-to-end tests replace.

## Proposed shape

- Package `changkun.de/x/occamy/client`, pure Go, no cgo, no UI.
- `Dial(ctx, url, token, Options) (*Session, error)` performs `hello` and
  `welcome` and reports the backend class, role and size.
- Graphical sessions keep a client-side framebuffer, decode tiles into it
  (in parallel), apply `copy-rect`, ack frames after decoding, and report
  dirty rectangles.
- Terminal sessions pass `term-data` and `term-snapshot` bytes through
  untouched, because terminal emulation belongs to the UI (xterm.js,
  SwiftTerm).
- Input methods: `Key`, `Text`, `TermInput`, `Pointer`, `Wheel`,
  `Resize`, `Clipboard`, `PromptReply`.
- Reconnect with a session resume token, if the API design provides one.
- The C export layer is not in this package. It is a `package main` in
  [010](../010-native-swift-client.md)'s tree that wraps this API.

## Options

### Event delivery

- **A. Pull: `(*Session).Next(ctx) (Event, error)` (recommended).** The
  caller loops. In C this becomes
  `occamy_session_next(handle, timeout_ms, *event)`, called from a thread
  that Swift owns.
  - Pros: no C function pointers and no calls from Go threads into Swift;
    natural backpressure; easy to test.
  - Cons: one blocked thread per session on the Swift side; event structs
    must be C-friendly.
- **B. Callbacks: an `EventHandler` interface.** In C, registered function
  pointers.
  - Pros: lowest latency; familiar.
  - Cons: Swift code runs on Go threads, so every callback must hop to its
    own executor; reentrancy rules get subtle.
- **C. Channels.** Idiomatic for Go users, but they do not cross the C ABI,
  so C would still need A or B on top.

### Framebuffer ownership

- **A. C-allocated memory (`C.malloc`) owned by the core.** Swift wraps it
  as a `CGImage` data provider or uploads dirty rectangles to a Metal
  texture, guarded by a lock exposed through the ABI.
- **B. Go memory pinned with `runtime.Pinner`.** No C allocation, but the
  pin must outlive every Swift reader, which the ABI has to make explicit.

### Web client reuse

The core could also be compiled to WebAssembly for the browser. That is
not recommended: native browser decoders (`createImageBitmap`,
`DecompressionStream`) beat Go-in-WASM, and the bundle would grow by
several megabytes. The web client implements the protocol in TypeScript
against the golden vectors from the `protocol` package.

## Decisions

Settled on 2026-10-29, following the recommendations above.

1. **Event delivery: A,** pull through `(*Session).Next(ctx)`.
2. **Framebuffer: A, in the export layer.**
   - The `client` package stays pure Go. It decodes into a Go framebuffer
     and reports dirty rectangles.
   - [010](../010-native-swift-client.md)'s cgo export layer keeps a
     C-allocated mirror. It copies dirty rectangles into the mirror under
     the lock that `occamy_framebuffer_lock` exposes.
   - No Go pointer crosses the ABI, and no pinning rules leak into Swift.
3. **Public `client/` from the first beta,** with no API stability promise
   until the release candidate, matching the protocol.
4. **Tests move gradually.** End-to-end and pressure tests for protocol v1
   use this package. The Guacamole tests in `tests/` stay as long as the
   v0 routes do, which is until v1.0.

## Affects

New `client/` Go package, used by `tests/`, the Swift client's C export
layer, and any tools.

## Implementation notes

- **What was done:** package `changkun.de/x/occamy/client`, pure Go with
  no new dependency, about 2,000 lines and 2,900 of tests.
  - `Dial(ctx, url, token, Options)` performs the upgrade of
    [docs/protocol.md](../../docs/protocol.md) with coder/websocket:
    `hello` in the `hello` query parameter beside the route's own, the
    token in `occamy.v1.token.` beside `occamy.v1` and never in the URL.
    An empty token offers `occamy.v1` alone, for a caller that sends an
    `Authorization` header through `Options.HTTPHeader`. A server that
    picks another subprotocol is refused with `unsupported`, and a
    `close` before `welcome` is Dial's `*CloseError` with its code.
  - `New(ctx, Conn, Options)` runs over any connection with one message
    per call, such as `stream.Serve`'s end of an in-process pipe;
    `Options.Hello` is the `hello` its caller hands the server.
  - The session reports `ViewerID`, `Role`, `Class`, `SessionID`, the
    agreed `Capabilities`, and `Size` as of the last `Resize` delivered.
  - `Next(ctx)` delivers `Frame` (ID, `LastSeq`, server time, dirty
    rectangles), `Resize`, `TermSnapshot`, `TermData`, `Cursor`,
    `CursorMove`, `Clipboard`, `Bell`, `Title`, `Prompt`,
    `ViewerJoined`, `ViewerLeft`, and last `End` with the close code,
    reason and error. Then it returns `ErrClosed`.
  - Graphical sessions keep an `*image.RGBA` framebuffer, read under a
    lock with `Framebuffer(func)` or copied with `Screenshot`. Raw tiles
    are copied in. Other codecs go through the `Decoder`s of
    `Options.Decoders`, offered in their order with raw last, and a pool
    of `DecodeWorkers` decodes them as they arrive. `copy-rect` copies
    within the framebuffer, overlaps included. The cursor is an
    `*image.NRGBA` with straight alpha, and nil when hidden.
  - Terminal sessions pass `term-snapshot` and `term-data` bytes through
    untouched, with the frame's `Resize` and the title.
  - `Key`, `Text`, `TermInput`, `Pointer`, `Wheel`, `Resize` and
    `Clipboard` number inputs from 1, wrapping from 2³²−1 to 1, and return
    each input's `seq`; `View` and `PromptReply` send the rest. A
    read-only viewer's input is refused with `ErrReadOnly`, and a reply
    from a viewer other than the owner with `ErrNotOwner`, before anything
    is sent. `View` clamps the viewport to the protocol's coordinates and
    refuses one that shows nothing, which the wire would read as the
    whole screen.
  - `Close` sends the input queued so far and `close` (normal), closes
    the WebSocket with the same code, and waits for every goroutine of
    the session. It waits up to a second for the network to take the
    input, then for coder/websocket's close handshake, which bounds each
    of its steps at 5 s: about 6 s against a server that stopped
    answering.
- **Decisions:**
  - A frame is applied to the framebuffer when `Next` delivers it, not
    when it arrives. The framebuffer therefore always holds a whole
    frame, so the export layer never copies half of one, and a caller
    that falls behind holds the server at its frame window rather than
    queueing stale frames. Tiles of plugged codecs still decode as they
    arrive, in parallel, so delivery waits only for the copy.
  - A frame is acknowledged when the caller calls `Next` again, not when
    `Next` returns it. The protocol acknowledges a frame once it is
    shown, and the caller shows it after `Next` returns; only its next
    call says it is done. Acknowledging at delivery would leave the
    drawing out of `decode_time` and let the server send faster than the
    caller draws. A caller that waits in `Next` acknowledges as it
    enters, so a steady caller adds no delay. Only the newest waiting
    acknowledgement is sent, since it covers the earlier ones.
  - `decode_time` runs from the frame's `frame-end` arriving to its
    acknowledgement. The server takes it out of the round trip and
    spaces its frames by it, so a caller that takes frames at 30 a second
    gets about 30, each fresh. The cost is a long pause: the pacer averages decode
    times over about eight frames, so each frame held for a pause of
    length H adds about H/8 to the frame interval, which then shrinks by
    an eighth a frame. Motion stays slow for about H per held frame, two
    or three of them after a full window, so a 30 s stall costs about a
    minute of slowed motion afterwards. Input is not slowed: the frame
    after an input ignores the interval. A caller whose window is hidden
    says so with `View` instead, and the pacer should damp the sample
    (Follow-ups).
  - The slow-consumer policy is one ordered queue of small events and
    complete frames, each frame at its `frame-end`. Frames are bounded by
    the protocol: more than `frame_window` + 1 undelivered is a
    `protocol-error`. Title, clipboard, cursor and pointer position hold
    their latest value and replace an undelivered event of their kind in
    place; at most 3 bells wait; a viewer that leaves cancels its
    undelivered join. More than 1024 undelivered events, or more bytes
    than max(64 MiB, min((window + 2) × 3 screens, 1 GiB)), is taken for
    a broken server and closed with `protocol-error`. The screen is the
    largest that a frame held or being read was drawn for, so a queued
    frame of a large screen does not count against the small one the
    next frame resizes to; a tile of a plugged codec counts its decoded
    pixels, whatever its payload's size; a terminal holds 64 MiB. A
    screen larger than `Options.MaxPixels` (8192 × 8192 by default) ends
    the session with `unsupported` before anything is allocated.
  - A frame's work is bounded apart from its bytes: the pixels its
    tiles, copies and resizes write or allocate may total eight screens
    (64 MiB at least), or the session ends with `protocol-error` before
    `Next` does any of it. A `copy-rect` or `resize` costs a few bytes to
    send and a screen to apply, which the backlog does not see. A
    `resize` drops what its frame drew before it, which no one would
    see, so resizes in a row cost one screen.
  - `Next` returns its context's error while it waits for an event or
    for a frame's tiles to decode, and leaves the frame queued for the
    next call. Once it applies a frame it finishes it, so that a live
    session's framebuffer never holds half a frame; the work bound keeps
    that short. At the default `MaxPixels` a hostile frame may still
    write eight screens of 256 MiB, a few hundred milliseconds of
    copying that the context does not cut; the bound is the knob if that
    is too long. `Close` stops it between two messages, since the
    session is over.
  - Nothing is queued once the session ended: `Next` delivers what came
    before, then `End`, then `ErrClosed`. The reader stops at the end,
    and a frame or event that races it is dropped.
  - Input never waits for `Next`. Input methods queue their message for
    a writer goroutine and return; they wait, until their context is
    done, only while more than `max_message_size` bytes are queued and
    unsent. The writer sends the latest ack first, then pongs (16 at
    most), then input and views in order. Sequence numbers are taken in
    queue order, so the wire carries them in order. The caller's context
    never reaches the connection, because coder/websocket closes the
    connection when a write's context ends.
  - The client checks what the server sends against the session:
    messages of its class only, tiles and copies within the screen as
    resized so far, frame IDs that grow by one from 1 (wrapping from
    2³²−1 to 0), agreed codecs, one `welcome`, binary WebSocket messages
    only. A failure is closed with the protocol's code, sent as `close`
    and then as the WebSocket's close code. A message over the agreed
    `max_message_size`, which coder/websocket's read limit catches and
    closes with 1009, ends the session with `too-large`.
  - A `Decoder` is Go: one in C would need cgo, which the package does
    without. It is called from several goroutines, and it checks the
    size the payload declares before it allocates, as
    `image.DecodeConfig` allows, so a few bytes cannot make it allocate
    more than its tile.
  - The client uses no capability beyond what it offered, whatever
    `welcome` states, and its frame window is the smaller of its offer
    and the server's.
  - No tile cache: the cached codec is not offered and `cache_slots` is
    0, until the Codecs task adds one.
- **Deviations from the proposed shape:**
  - Input methods take a context and return the sequence number and an
    error. `View` is added: the export layer needs visibility and
    viewports.
  - No reconnect. The protocol has no resume token; a client that lost
    its connection dials again and joins the session as a new viewer.
  - `internal/stream`'s test client is not replaced by this package.
    Those tests stop acknowledging on demand, watch the wire's order,
    record messages that do not decode, and hook each frame under a
    lock; the public client deliberately offers none of these.
- **Surprises:**
  - A `resize-request` from the client panicked `stream`'s terminal
    writer (index out of range): the input's rows in motion were
    recorded for the old screen size. Fixed here, with
    `TestTerminalResizeRequest`
    ([session-core](session-core.md), terminal frames).
  - The pacer treats a zero decode average as no sample and adopts the
    next one whole. In a synctest bubble decoding takes no time, so the
    first held frame sets the frame interval to the whole hold, which
    makes the hold test's recovery slower than the H/8 a frame above;
    the test waits ten seconds for full rate.
  - Review found these, each now fixed with a test:
    - A frame could show a key's effect with a `last_seq` below the key
      (`TestInputLastSeq`, 15 runs of 200 under `-race`): the server
      recorded the input after the backend had it. Fixed in `session`
      and `stream` ([session-core](session-core.md), later fix).
    - A full frame of a large screen still queued, then a `resize` to a
      small one, closed a correct server with `protocol-error`: the
      backlog limit followed the new screen only.
    - A `title` the server sent before it read the client's close was
      delivered after `End`.
    - 1.4 KB of `resize` and `copy-rect` made `Next` allocate and move
      several GiB.
    - `Next` could wait forever for a tile submitted after the decoders
      exited, past its context.
    - A message over the read limit ended the session with code 0.
  - `FuzzSession`, once its backlog floor was lowered, found a cursor of
    a plugged codec that declared 4096 × 304 pixels in an 18-byte message:
    the client allocated them and only then found the backlog full. A
    cursor's pixels are now checked against the backlog before they are
    allocated (`TestCursorBacklog`; the input is kept in
    `testdata/fuzz`).
- **Protocol text clarified** in [docs/protocol.md](../../docs/protocol.md):
  - A frame is acknowledged once shown, which for a client that hands
    frames to a renderer is when the renderer asks for the next one, and
    an acknowledgement covers the earlier frames, so a client may skip
    one that a later one covers.
  - Frame IDs wrap from 2³²−1 to 0; input sequence numbers from 2³²−1 to
    1, so a `last_seq` of 0 always means none.
  - `last_seq` is the last input applied before the server finished
    reading the frame's content, and a frame never shows a later one's
    effect.
  - `frame-ack`'s `decode_time` runs from `frame-end` arriving to the
    acknowledgement, any wait to be shown included. The server's round
    trip and delivery rate assume this; "decode and draw" alone left it
    open.
  - A viewer never has more than `frame_window` + 1 frames
    unacknowledged, and a client may close a server that sends more with
    `protocol-error`. This followed from the window and its input
    exception but was not stated.
- **Follow-ups:**
  - Codecs task: once the Codecs PR (#109) and this one are on master,
    a follow-up PR wires its decoders into `client` as the default
    `Options.Decoders`: deflate and raw through `internal/codec`
    (`codec.Inflater`, one per decoding goroutine, into a sub-image of
    the screen, and `codec.DecodeRaw`), WebP through
    `github.com/gen2brain/vpx/webp` with `ToRGBA` (`x/image/webp`
    misreads VP8's colour range), and JPEG through `image/jpeg`, each
    after the size its header claims (`DecodeConfig`, or vpx's
    `FrameSizeLimit`) is checked against the rectangle, as
    docs/protocol.md requires since #109. A tile
    message may then cover up to 16 tiles of a row, and cursor pixels
    are straight alpha, as `Cursor` already is. Their tests are
    `protocol/testdata/codecs.json`: lossless vectors exact, lossy ones
    at `min_psnr`, invalid ones refused. The tile cache (`cached` and
    slots) comes with it. #109 also turns `stream.Encoder` into
    per-viewer encoders made by `Config.NewEncoder`, which this
    package's tests that build a `stream.Config` follow: their PNG codec
    is a `stream.Encoder` with `Tile`, `Lossy` and `Cursor`. Until then the
    plugged-codec path, its worker pool and agreement are tested with a
    PNG codec in the tests.
  - [010](../010-native-swift-client.md)'s export layer: wrap `Next` as
    `occamy_session_next(handle, timeout_ms, *event)`, copy each
    `Frame.Dirty` rectangle from `Framebuffer` into the C mirror inside
    the callback, and map `End.Code` to the close code. Input calls
    return the sequence number for latency measurement. `Close` can take
    about 6 s against a server that stopped answering, so the app calls
    it off the main thread. Decoders plugged into the client are Go;
    VideoToolbox or another C decoder would need cgo in the core.
  - Streams task: `stream.Serve` sends `close` but cannot close the
    WebSocket with the same code, since `stream.Conn` has no `Close`;
    occamyd's handler must. `dial_test.go`'s `wsServer` is the adapter
    such a handler needs. The owner's `hello` size still does not size
    the remote (known; see session-core).
  - Pacer: the decode average follows the raw samples, and a held frame
    is one sample of the whole hold. It feeds three things: the frame
    interval, so motion stays slow for about as long as the hold
    (Decisions); the bandwidth-delay product, so the cap on bytes in
    flight grows with it and a stalled client is sent more; and the
    unpaced burst of the frame after an input, which grows the same way.
    The pacer should cap or damp a sample far above the average, and not
    take a zero average for no sample. The round trip and delivery rate
    need the whole time, so the fix belongs in the average, not in what
    clients report.
  - `stream`'s terminal writer keeps the larger of two sequence numbers
    for the pacer's input timing (`max(in.seq, seq)`), which picks the
    old one once numbers wrap. It only times one input, once in 2³².
- **Browser counterpart:** the TypeScript core in
  `client/occamy-web/src/protocol` follows this package's design, its
  bounds and its delivery and acknowledgement rules, for the browser;
  see [009](../009-react-web-client.md#implementation-notes-protocol-client).
- **Tests:** end-to-end against `stream.Serve` with real sessions and
  the backendtest backends, in synctest bubbles: a graphical screen drawn
  and copied at random converges byte for byte, through raw tiles and
  through a PNG codec decoded in parallel; a terminal's bytes fed to
  `internal/terminal` give the server's screen; key and pointer inputs
  are numbered from 1 and the frame that shows a key's swatch reports
  it in `last_seq`; a terminal echo carries its input; a hidden client
  gets no frames and a viewport limits tiles; a caller that stops
  calling `Next` holds the server at the window and frames flow again
  after; 400 inputs never wait behind a slow caller; and the session's
  close codes. Scripted servers cover overlapping `copy-rect`s,
  coalescing and order of the small state, pings, prompts, the
  agreement, input waiting on a stalled network, and thirteen ways a
  server breaks the protocol, each with the code the client sends and
  closes with. The golden vectors check the client's bytes for every
  client message, its reading of every server message, and the close
  code for each of the 29 invalid server vectors. `FuzzSession` plays a
  hostile server with screens of four sizes and the backlog's 64 MiB
  floor lowered, so that the limits that follow from the screen apply,
  and checks for panics, the backlog and framebuffer bounds, and that
  after `End` nothing is queued and `Next` returns `ErrClosed`. The
  dial tests run a real WebSocket through httptest with a token check
  and `stream.Serve`, check for leaked goroutines, and cover a context
  cancelled after `Dial`, a text message and a message over the read
  limit. Each finding of the review has a test: a shrink with a large
  frame queued and within a frame, nothing after `End`, the work bound
  both ways and resizes in a row, `Next` waiting on a decode until its
  context or `Close`, a tile submitted to a closed pool, `Close`
  flushing input, viewports at negative coordinates, and numbers that
  wrap. Mutations checked: acknowledging on receipt, skipping
  `copy-rect`, dropping the frame ID checks, the window bound and
  `MaxPixels`, numbering inputs wrong, dropping coalescing, input
  waiting for `Next`, decoded pixels left out of the backlog, the pool
  decoding after `Close`, `Close` not flushing input, text messages
  taken, the read limit's error unmapped, the backlog measured by the
  last screen only, no work bound, resizes not merged, the decode wait
  ignoring `Close`, a tile queued to a closed pool, `Next` delivering
  after `End`, a viewport that shows nothing sent, and a cursor
  allocated before the backlog check: each fails a test.
