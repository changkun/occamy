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
updated: 2026-10-30
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
  no new dependency, about 1,800 lines and 2,100 of tests.
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
    `Clipboard` number inputs from 1 and return each input's `seq`;
    `View` and `PromptReply` send the rest. A read-only viewer's input
    is refused with `ErrReadOnly`, and a reply from a viewer other than
    the owner with `ErrNotOwner`, before anything is sent.
  - `Close` sends the input queued so far and `close` (normal), closes
    the WebSocket with the same code, and waits for every goroutine of
    the session.
- **Decisions:**
  - A frame is applied to the framebuffer, and acknowledged, when `Next`
    delivers it, not when it arrives. The framebuffer therefore always
    holds a whole frame, so the export layer never copies half of one,
    and a caller that falls behind holds the server at its frame window
    rather than queueing stale frames. Tiles of plugged codecs still
    decode as they arrive, in parallel, so delivery waits only for the
    copy.
  - `decode_time` runs from the frame's `frame-end` arriving to its
    delivery. The server takes it out of the round trip and spaces its
    frames by it, so a caller that takes frames at 30 a second gets about
    30, each fresh. After a pause the server's frame interval recovers
    over a few frames; a caller whose window is hidden says so with
    `View` instead.
  - The slow-consumer policy is one ordered queue of small events and
    complete frames, each frame at its `frame-end`. Frames are bounded by
    the protocol: more than `frame_window` + 1 undelivered is a
    `protocol-error`. Title, clipboard, cursor and pointer position hold
    their latest value and replace an undelivered event of their kind in
    place; at most 3 bells wait; a viewer that leaves cancels its
    undelivered join. More than 1024 undelivered events, or more bytes
    than max(64 MiB, min((window + 2) × 3 screens, 1 GiB)), is taken for
    a broken server and closed with `protocol-error`. A screen larger
    than `Options.MaxPixels` (8192 × 8192 by default) ends the session
    with `unsupported` before anything is allocated.
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
    resized so far, frame IDs that grow by one from 1, agreed codecs, one
    `welcome`. A failure is closed with the protocol's code, sent as
    `close` and then as the WebSocket's close code.
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
    next one whole. In a synctest bubble decoding takes no time, so one
    held frame sets the frame interval to the whole hold, and it decays
    by an eighth a frame. Real decode times are never exactly zero.
- **Protocol text clarified** in [docs/protocol.md](../../docs/protocol.md):
  - `frame-ack`'s `decode_time` runs from `frame-end` arriving to the
    acknowledgement, any wait to be shown included. The server's round
    trip and delivery rate assume this; "decode and draw" alone left it
    open.
  - A viewer never has more than `frame_window` + 1 frames
    unacknowledged, and a client may close a server that sends more with
    `protocol-error`. This followed from the window and its input
    exception but was not stated.
- **Follow-ups:**
  - Codecs task: `Decoder`s for the lossless and lossy codecs, the tile
    cache (`cached` and slots), and an `Encoder` for each in `stream`.
    The plugged-codec path, its worker pool and agreement are tested with
    a PNG codec in the tests.
  - [010](../010-native-swift-client.md)'s export layer: wrap `Next` as
    `occamy_session_next(handle, timeout_ms, *event)`, copy each
    `Frame.Dirty` rectangle from `Framebuffer` into the C mirror inside
    the callback, and map `End.Code` to the close code. Input calls
    return the sequence number for latency measurement.
  - Streams task: `stream.Serve` sends `close` but cannot close the
    WebSocket with the same code, since `stream.Conn` has no `Close`;
    occamyd's handler must. `dial_test.go`'s `wsServer` is the adapter
    such a handler needs. The owner's `hello` size still does not size
    the remote (known; see session-core).
  - The pacer could ignore a zero decode average and damp outliers, so
    one long hold does not slow motion for several frames.
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
  hostile server and checks for panics, the backlog bound and the
  framebuffer bound. The dial tests run a real WebSocket through
  httptest with a token check and `stream.Serve`, and check for leaked
  goroutines. Mutations checked: acknowledging on receipt, skipping
  `copy-rect`, dropping the frame ID checks, the window bound and
  `MaxPixels`, numbering inputs wrong, dropping coalescing, and input
  waiting for `Next` each fail a test.
