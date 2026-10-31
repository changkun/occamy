---
title: Occamy protocol v1
status: complete
track: architecture
depends_on: []
affects:
  - protocol/
  - docs/protocol.md
  - docs/protocol-cn.md
effort: large
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# Occamy protocol v1

## Design Problem

Define the wire protocol between clients and `occamyd`. It is the
longest-lived contract in v1: the web client, the Go client core (and
through it the Swift app), and third parties all depend on it, and it
freezes at the release candidate. It must carry two content classes,
terminal bytes and pixel tiles, with per-viewer flow control. It must
evolve without breaking old clients.

## Context

- v0 speaks Guacamole: text instructions (`4.size,4.1024,3.768;`) with
  image data base64-encoded in `blob` instructions. It has no flow control
  beyond `sync` echoes, and no capability negotiation beyond the handshake
  `args` (see [005](../005-upstream-guacamole.md)).
  `docs/protocol-cn.md` documents it.
- The session core ([session-core](session-core.md)) produces events
  (terminal data, damage, cursor, clipboard, bell, resize, close) and
  consumes input (key, pointer, text, clipboard, resize).
- Clients: browsers decode JPEG and PNG natively (`createImageBitmap`) and
  inflate deflate streams with `DecompressionStream('deflate-raw')`. The Go
  client core decodes in Go and hands pixels across a C ABI.

## Proposed shape

- **Transport:** WebSocket, binary frames only, subprotocol `occamy.v1`.
  One session per connection.
- **Framing:** one protocol message per WebSocket message, so WebSocket
  already delimits messages. Header: `type u16 | flags u16`, then a
  type-specific body in little-endian fixed fields followed by
  length-prefixed variable fields. Flag bit 0 marks a message that
  receivers may ignore if they do not know its type. Unknown non-ignorable
  types close the connection with `unsupported`.
- **Handshake:** client `hello` carries the protocol version first, the
  initial size in pixels and in character cells (the client does not
  know the class yet), visibility, the client name, and capabilities
  (codecs, max texture size, terminal level, pixel ratio, cache slots,
  frame window, max message size). Server `welcome` carries the viewer's
  ID, role, backend class (terminal or graphical), the session ID and the
  agreed capabilities, and the first frame follows immediately.
  - **One round trip to the first frame.** `hello` travels in the
    WebSocket upgrade request, base64url-encoded in the `hello` query
    parameter, and the server sends `welcome` and the snapshot right
    after `101 Switching Protocols`, without waiting for a message.
    v0 needs two round trips after sign-in, and a separate `hello` would
    make v1 no better.
  - **The token never goes in the URL.** The client offers two
    subprotocols, `occamy.v1` and `occamy.v1.token.<token>` with the
    token base64url-encoded, as Kubernetes does for its WebSocket API.
    Browsers can set this header and proxies do not log it as they log
    URLs. The server authenticates the token and answers with
    `occamy.v1` alone. On failure it still completes the upgrade and
    sends `close`, since a browser cannot read the status of a refused
    upgrade. Native clients may
    send `Authorization: Bearer` instead. `hello` itself carries no
    secret.
- **Server to client:** `resize`, `tile` (frame ID, rectangle, codec,
  payload, and an optional client cache slot to store it in; the `cached`
  codec draws a stored slot instead of a payload, see
  [graphics-pipeline](graphics-pipeline.md)), `copy-rect`, `frame-end`
  (frame ID, timestamp and the last input it reflects), `cursor` (image
  and hotspot) and `cursor-move`, `term-data` (bytes),
  `term-snapshot`, `clipboard` (MIME type and data), `bell`, `title`,
  `prompt` (keyboard-interactive or credential request), `viewer-joined`
  (viewer ID, role and name) and `viewer-left`, and `ping`, `pong` and
  `close` (code and reason), which go either way.
- **Client to server:** `key` (down or up, keysym, and the physical key as
  a USB HID usage from the keyboard page), `text` (UTF-8, for
  IME), `term-input` (bytes produced by the client's terminal emulator,
  such as xterm.js `onData`), `pointer` (x, y, button mask), `wheel` (dx,
  dy, with pixel or line mode), `resize-request`, `clipboard`, `prompt-reply`,
  `frame-ack` (frame ID and decode time), `view` (whether the client is
  visible, and the rectangle of the screen its viewport shows), and
  `ping`, `pong` and `close`.
- **Terminals are framed screen diffs.** The server keeps the terminal's
  screen ([native-ssh-backend](native-ssh-backend.md)) and, once per
  frame, sends what changed as `term-data`: escape sequences that turn
  the client's screen into the server's, as mosh does. A `frame-end`
  closes each terminal frame like a graphical one. Intermediate screens
  are skipped under bulk output, and the client can confirm or undo its
  predicted echo from the input sequence number in `frame-end`.
- **Flow control:** the server keeps at most `window` unacknowledged
  frames per viewer (agreed in `welcome`, default 2). While the window is
  full, damage accumulates (see [session-core](session-core.md)) and is
  sent as one coalesced frame once an ack arrives. Terminal frames are
  windowed the same way, so bulk output coalesces instead of queueing.
- **Latency:** the design must meet the targets in
  [docs/benchmarks.md](../../docs/benchmarks.md#targets) and the 50 ms
  budget in [benchmark](benchmark.md). The objective is the time from an
  input until its effect is visible to the viewer who made it, and only
  for what a viewer can see ([session-core](session-core.md), Input to
  display).
  - Every input message carries a u32 sequence number, and `frame-end`
    carries the last sequence number the frame reflects. A client can
    then measure input-to-update latency exactly.
  - Frames carry states, so skipping a stale frame never loses an
    input's effect. The frame after an input is sent at once, with the
    tiles damaged since the input first.
  - A viewer that reports itself hidden with `view` gets no frames, and
    tiles outside its viewport wait until they are in view.
  - Messages the user sees directly or that end the session (`cursor`,
    `cursor-move`, `clipboard`, `bell`, `close`) are never queued behind
    tiles.
  - A tile message has a size cap, so anything waits behind at most one
    tile.
  - Besides the frame window, the bytes in flight per viewer are capped
    near the link's bandwidth-delay product, estimated from `frame-ack`
    round trips with a delay-based controller. Without that cap, one
    large frame fills the TCP buffers.
  - The client draws the cursor locally from `cursor`, so pointer motion
    does not wait for a round trip.
- **Versioning:** the major version lives in the subprotocol name. Minor
  evolution goes through capabilities and ignorable messages. Message type
  numbers are never reused.
- **Limits:** a maximum message size (negotiated, default 4 MiB) and
  maximum tile dimensions are enforced before allocation.
- **Package:** a public Go package `changkun.de/x/occamy/protocol` (no
  `internal/`), so the client core and third parties share one
  implementation. A TypeScript codec for the web client is written by hand
  against golden test vectors produced by the Go package.

## Options

### Message encoding

- **A. Hand-written binary structs (recommended).** Fixed little-endian
  fields and length-prefixed byte strings, encoded and decoded by
  hand-written Go and TypeScript.
  - Pros: zero-copy access to tile payloads; no dependencies; trivially
    fuzzable; the smallest browser code.
  - Cons: every message is hand-written twice; schema evolution needs the
    discipline above.
- **B. Protocol Buffers for control messages, raw bytes for payloads.**
  - Pros: schema and code generation for Go, TypeScript and Swift; field
    evolution is built in.
  - Cons: adds `google.golang.org/protobuf` and a JS runtime; payloads
    still need a separate path to avoid copies; code generation enters the
    build.
- **C. CBOR (or MessagePack) maps.**
  - Pros: self-describing; easy to extend.
  - Cons: larger and slower per message than A; dynamic decoding in hot
    paths; weaker typing.

### Physical key codes

- **A. W3C `KeyboardEvent.code` strings.** Browsers produce them directly;
  the Swift client maps `NSEvent.keyCode` to them.
- **B. USB HID usage IDs (u16).** Compact and layout-neutral; both RDP
  scancodes and QEMU's extended key events map from them through a table.

## Decisions

Settled on 2026-10-29.

1. **Encoding: A.** Messages are hand-written binary structs, because the
   traffic is mostly payload and the message catalogue is small.
2. **Physical keys: HID usages.** The wire carries a USB HID usage from
   the keyboard page (0x07) as a u16. The Go client core and the web
   client each hold the table that translates from `KeyboardEvent.code`
   or `NSEvent.keyCode`.
3. **Frame window: 2 by default.** `welcome` can agree a different value.
   Tune it with the graphics pipeline's slow-viewer test and the
   [benchmark](benchmark.md) under N1 and N2 before the release
   candidate.
4. **Terminals are framed screen diffs under the frame window**
   (revised 2026-10-30, owner's call). The earlier decision, raw bytes
   bounded by the write buffer with disconnect on overflow, would send
   575 KiB for T2 where guacd sends 86 KiB, against a target of 8.6 KiB.
5. **One session per WebSocket for beta.** The ignorable flag and
   never-reused type numbers leave room to add streams later through a
   capability, for file transfer for example.
6. **WebTransport comes after 1.0.**
7. **`docs/protocol.md` is normative.** The Go `protocol` package is the
   reference implementation. Golden vectors generated by the package are
   checked against the examples in the document, and a disagreement
   between the two is a bug, fixed before the release candidate.
8. **The first frame takes one round trip** (2026-10-30). `hello` rides
   in the upgrade request and the token in `Sec-WebSocket-Protocol`, as
   described under Handshake, because the baseline's G7 targets assume
   one round trip at N1 and N2.
9. **The scheduling objective is input to display, for what is seen**
   (owner's call, 2026-10-30): `view` reports visibility and the
   viewport, and the latency rules above replace an earlier rule that
   sent small control messages ahead of tiles.

## Task Breakdown

| Task | Delivers |
|------|----------|
| Framing | `protocol/`: header, primitives (fixed little-endian integers, length-prefixed bytes and strings), the type registry, the ignorable flag, size limits checked before allocation, and fuzz tests |
| Handshake | `hello`, `welcome` and capabilities, with version and limit checks |
| Messages | Every server-to-client and client-to-server message above, with round-trip and golden tests |
| Document | `docs/protocol.md` written to match, golden vectors under `protocol/testdata/`, and `docs/protocol-cn.md` relabelled as the v0 (Guacamole) description |

## Affects

Creates `protocol/` (public package) and `docs/protocol.md`.
`docs/protocol-cn.md` is relabelled as the v0 (Guacamole) description. The
web client ([009](../009-react-web-client.md)) and the client core
([client-core](client-core.md)) implement it.

## Implementation notes

- **Status.** All four tasks are done: Framing in #84, and Handshake,
  Messages and Document in #96. The spec stays validated until the
  wrap-up. (Wrapped up on 2026-10-31: see Outcome.)

### Framing

- **What Framing shipped.** The public package `protocol/`:
  - `Header` (`Type`, `Flags`), `FlagIgnorable`, `AppendHeader`, and
    `Split`, which checks the message size before it reads the header.
  - `Append*` functions for u8 to u64, i8 to i64, byte strings and UTF-8
    strings, and a `Decoder` whose `Read*` methods mirror them. Its
    errors are sticky, and `Finish` reports trailing bytes.
  - `Registry` with `Register`, `Lookup` and `Accept`. For an unknown
    type, `Accept` says skip if the message is ignorable and returns
    `ErrUnsupported` otherwise.
  - `Limits.MaxMessageSize` (zero means 4 MiB) with `CheckSize`, and
    `Subprotocol` (`occamy.v1`).
  - Golden vectors in `protocol/testdata/framing.json`, three fuzz
    targets, and benchmarks.
- **Decisions the spec left open.**
  - Byte strings and UTF-8 strings have a u32 little-endian length
    prefix. A u16 length could not hold a 4 MiB message, and varints
    have more than one encoding per value and need more code in
    TypeScript.
  - Flag bits 1 to 15 are reserved: senders leave them zero, receivers
    ignore them, and `Split` keeps them. A flag that changes how a body
    reads must therefore be agreed as a capability first.
  - Type 0 is reserved and cannot be registered, so a zeroed buffer
    never decodes as a message. There is no private or experimental
    type range. The spec's extension path is capabilities and
    never-reused numbers. Messages or Document can still add a range
    before the release candidate.
  - A length past the end of the message is `truncated`, like any read
    past the end. Byte strings are not copied. The slice's capacity ends
    at its length, so appending to it cannot overwrite the next field.
  - Decoding rejects invalid UTF-8, as `TextDecoder` with `fatal: true`
    does (no overlongs, surrogates or code points above U+10FFFF). The
    encoder replaces invalid UTF-8 with U+FFFD instead of failing or
    panicking, because strings such as window titles come from remote
    hosts and a receiver would close the connection over them.
  - `Decoder.Finish` rejects trailing bytes. `Len` lets Messages accept
    them for a message where that is wanted.
  - The golden vectors are one JSON file. Integer values are decimal
    strings, so 64-bit values survive JavaScript numbers. The file also
    has malformed inputs with stable error names (`truncated`,
    `too-large`, `invalid-utf8`), and dispatch cases (`decode`, `skip`,
    `unsupported`) for the web client's codec to check.
- **Left to later tasks.**
  - Codec IDs and the range reserved for the video tier belong to
    Messages and [graphics-pipeline](graphics-pipeline.md), because the
    width of the codec field is part of the `tile` layout.
  - Close codes, including `unsupported`, come with `close` in
    Messages. `ErrUnsupported` maps to that code.
  - Messages will likely need a list primitive for lists such as
    codecs: a count that is checked against the bytes left before
    anything is allocated.
  - Handshake picks an encoding for the pixel ratio and other
    fractional values. Fixed point fits the integers here.
- **Verification.** Each fuzz target ran for 35 s locally with no
  failures: `FuzzSplit` about 19.5 M inputs, `FuzzDecoder` 23 M,
  `FuzzRoundTrip` 8.1 M. Benchmarks ran on an Apple M5 Pro under load
  from other work. Encoding the header takes 1.7 ns and `Split` 2.1 ns.
  A 1 MiB payload appends in about 12.6 µs and reads in 3.8 ns because
  it is not copied. None of them allocates.
- **Deviations.** None from the spec.

### Handshake, Messages and Document

- **What shipped.**
  - Every message as a type with `Append(b) []byte` and
    `Decode(body, Limits) error`, behind the `Message` interface; `New`;
    `Decode(msg, registry, limits)`, which splits, dispatches and
    decodes; `ClientMessages` and `ServerMessages`, the registries a
    server and a client accept with; `Type.String`.
  - The handshake: `Hello`, `Welcome`, `Capabilities` with `Agree` and
    `Limits`, `EncodeHelloQuery` and `DecodeHelloQuery` with
    `MaxHelloSize`, `TokenSubprotocol` and `TokenFromSubprotocols`.
  - `CloseCode` with `CloseCodeFor`, which maps decoding errors to close
    codes (`ErrUnsupported` to `unsupported`).
  - Primitives: `AppendBool` and `ReadBool`, `AppendCount`, `ReadCount`
    and `MaxCount`, `ErrInvalid`, and `Limits.MaxTextureSize` and
    `Limits.CacheSlots`.
  - [docs/protocol.md](../../docs/protocol.md), golden vectors in
    `protocol/testdata/messages.json` (42 messages, 52 invalid messages,
    hello queries, tokens and subprotocol headers), a test that checks
    every example in the document against them, three more fuzz
    targets, examples and benchmarks. `docs/protocol-cn.md` is marked
    as the v0 description.
- **Type numbers.** Either way: `ping` 0x0001, `pong` 0x0002, `close`
  0x0003. From the client: `hello` 0x0101, `key` 0x0102, `text` 0x0103,
  `term-input` 0x0104, `pointer` 0x0105, `wheel` 0x0106,
  `resize-request` 0x0107, `clipboard` 0x0108, `view` 0x0109,
  `frame-ack` 0x010a, `prompt-reply` 0x010b. From the server: `welcome`
  0x0201, `resize` 0x0202, `tile` 0x0203, `copy-rect` 0x0204,
  `frame-end` 0x0205, `cursor` 0x0206, `cursor-move` 0x0207,
  `term-data` 0x0208, `term-snapshot` 0x0209, `clipboard` 0x020a,
  `bell` 0x020b, `title` 0x020c, `prompt` 0x020d, `viewer-joined`
  0x020e, `viewer-left` 0x020f. A message sent the wrong way is an
  unknown type to its receiver.
- **Decisions the spec left open.**
  - A list is a u16 count, and the count times the element's smallest
    size must fit in the bytes left, or the read is `truncated` before
    anything is allocated. A boolean is a u8 that is 0 or 1.
  - One new error, `ErrInvalid` (vector name `invalid`), for values the
    protocol does not allow. A tile or cursor over the maximum texture
    size wraps `ErrTooLarge`, and a hello of another version wraps
    `ErrUnsupported`, so each maps to the close code of the same name.
  - A decoder checks each field as it reads it, then trailing bytes, then
    the rules between fields, so implementations agree on which error
    a message fails with. It checks only what needs no state beyond the
    agreed limits: booleans, roles, classes, wheel modes, codecs,
    capabilities, tile and cursor sizes, cache slots, a cached tile's
    slot and empty payload, a raw image's size, a hidden cursor's form,
    and the hot spot. Checks against the session (a rectangle within the
    screen, a frame ID that matches, a stored slot) belong to the
    receiver, and the document lists them per message.
  - Capabilities have one encoding: entries in ascending order of ID,
    each once, no zero values, known IDs with values of their exact
    size. Unknown entries are kept raw in `Capabilities.Unknown`, so a
    message re-encodes exactly. Known IDs run from 1 to 7 without gaps,
    so unknown ones always follow them.
  - Agreement (`Agree`): the client's codecs that the server has, in
    the client's order; the client's texture size, lowered to the
    server's; the lower terminal level and the fewer cache slots; the
    client's pixel ratio; the smaller frame window and message size,
    each counting an unstated value as its default. `welcome` therefore
    always states the frame window and the message size. A graphical
    session without an agreed codec, or a terminal without an agreed
    terminal level, closes with `unsupported`.
  - The pixel ratio is a u16 in hundredths (200 for twice the density),
    the fixed point the Framing notes suggested.
  - Codec IDs: 1 raw, 2 deflate, 3 PNG, 4 JPEG, 5 WebP, 6 cached; 0 is
    not a codec (it marks a hidden cursor), 7 to 0x7F are unassigned and
    0x80 to 0xFF are reserved for the video tier. Raw's channel order and
    deflate's container are provisional until the graphics pipeline's
    measurement, and WebP is one of its candidates. (2026-10-30: the
    measurement fixed raw as RGBA and picked WebP for lossy tiles; the
    graphics pipeline's Codecs task fixed the deflate container, with
    forms for alpha, in docs/protocol.md, and the payloads of every
    codec occamyd sends in `protocol/testdata/codecs.json`.)
  - Close codes are WebSocket close statuses, so the `close` message and
    the close frame carry one number: `normal` 1000, `server-shutdown`
    1001, `protocol-error` 1002, `unsupported` 1003, `too-large` 1009,
    `internal-error` 1011, then `unauthorized` 4000, `forbidden` 4001,
    `session-closed` 4002, `owner-left` 4003, `idle` 4004,
    `remote-ended` 4005, `backend-failed` 4006, `viewer-limit` 4007,
    and, since the delta review of the server, `too-slow` 4008. A
    WebSocket library that closes on its own read limit uses 1009, which
    is already ours. The session core's end reasons map one to one onto
    codes: closed 4002, owner left 4003, idle 4004, remote ended 4005,
    failed 4006, shutdown 1001 and internal 1011
    (`internal/stream/serve.go`, `closeFor`). The other codes are not
    end reasons: the server sends `unauthorized`, `forbidden` and
    `viewer-limit` when it refuses a stream, and `too-slow` when it
    drops a viewer. (Corrected 2026-10-31: this said the end reasons
    map onto the codes from 4002 and onto 1001.)
  - Since that review, too: a viewer that leaves a frame
    unacknowledged for the server's viewer timeout gets `too-slow`,
    hidden or not, so a hidden client acknowledges the frames it was
    sent as they arrive; and a `term-snapshot` may come in any frame,
    as occamyd sends one in a client's first frame after it was
    hidden (docs/protocol.md, The frame window and term-snapshot).
  - The hello query carries the whole message, header included, so the
    server decodes it as any message. It is at most 2048 bytes, 2731
    characters, well within the 8 KiB request lines that common proxies
    allow, and its length is checked before decoding. Decoding is
    strict, so each hello has one encoding.
  - Tokens may arrive in several `Sec-WebSocket-Protocol` lines, each a
    comma-separated list. More than one token, an empty one or one that
    is not base64url is refused; no token means `Authorization`. Errors
    never contain the token, which a test and a fuzz target check.
  - A refused token, `Origin` or hello still gets the upgrade and a
    `close`, because browsers cannot read the status of a refused
    upgrade; a request without `occamy.v1` gets an HTTP error. A hello
    on the WebSocket, or a second welcome, is a protocol error. A client
    sends nothing before welcome.
  - A frame is the `resize`, `tile`, `copy-rect`, `term-data` and
    `term-snapshot` messages up to its `frame-end`; the client shows it
    at `frame-end`. Frame IDs start at 1 per connection, and an ack
    covers every earlier frame.
  - Reserved pointer button bits are ignored rather than refused, as
    reserved header flags are. No private type range was added.
- **Deviations from the layout proposed for these tasks.**
  - `hello` and `welcome` put fixed fields before strings and
    capabilities, as the spec's Framing bullet requires: `hello` is
    version, width, height, cols, rows, visible, name, capabilities;
    `welcome` is viewer ID, role, class, session ID, capabilities.
  - `hello` gains `cols` and `rows`. The client does not know the class
    when it sends `hello`, and with one round trip the server must start
    the PTY at the right size from `hello` alone.
  - `welcome` gains `viewer_id`, without which a client cannot tell
    itself apart in `viewer-joined` and `viewer-left`.
  - `welcome` has no fields for the frame window and the maximum message
    size besides the capabilities: two places for one value would need a
    rule for when they disagree.
  - `viewer-joined` gains `name`, so a client can show who is watching.
  - Close codes are WebSocket statuses rather than small integers, and
    `internal-error` (1011) is added for the server's own failures.
  - The two clipboards are two types (0x0108 and 0x020a), since the
    client's is input and carries a sequence number.
  - `Decode` takes `Limits` on every message, so one interface serves
    all of them; tile and cursor use it.
  - The Go `Hello` has no version field: `Append` writes 1 and `Decode`
    checks it.
- **Surprises.**
  - Go's base64 decoder skips line breaks even in strict mode, so a hello
    query or token would have more than one encoding; decoding checks
    the alphabet first.
  - While this was built, master's graphics pipeline turned WebP from
    rejected into a measured candidate (pure-Go encoders appeared), so
    the codec is marked as a candidate rather than as unused.
  - `internal/session` (merged meanwhile) keeps `ViewerInfo` without a
    name, so the viewer writer sends an empty name until it has one.
- **Verification.** Each new fuzz target ran for 30 s locally with no
  failures: `FuzzMessage` (decode as client and server, re-encode to the
  same bytes, byte fields alias the message) about 6.4 M inputs,
  `FuzzHelloQuery` 6.7 M, `FuzzTokenSubprotocol` 6.0 M. Every proper
  prefix of every vector is `truncated` and a byte more is
  `trailing-data`. Decoding a tile allocates nothing and does not copy
  the payload (`testing.AllocsPerRun`). Medians of seven benchmark runs
  on an Apple M5 Pro under heavy load from other work (load average
  about 20): appending a tile with a 6 KiB payload 92 ns, decoding it
  16 ns, both without allocating; decoding a key through `Decode` 31 ns
  with one allocation, the message; decoding a hello query 168 ns.
- **Follow-ups.** The viewer writer and the Streams task use this
  package. The web client's TypeScript codec is tested against
  `messages.json`. (All three done: `internal/stream`, `server/` and
  `client/occamy-web/src/protocol` use it.) The graphics pipeline's measurement fixes raw's
  channel order and deflate's container (raw's order is fixed as RGBA,
  and its Codecs task fixed the container). `ViewerInfo` gains a name.

## Outcome

**Summary.** The four tasks shipped in two pull requests, implemented
directly rather than dispatched: Framing in #84 and Handshake, Messages
and Document in #96, both on 2026-10-30. Later pull requests fixed what
this spec left to the graphics pipeline and the server: raw's channel
order and WebP in #103, a floor on the message size in #100, the
deflate container and per-codec vectors in #109, and `too-slow` with
the hidden client's acknowledgements in #111. Drift: minimal. Of 22
items (13 in the proposed shape and 9 decisions), 18 shipped as
specified, 2 diverged (the handshake's layout and the frame window's
exception for input), and 2 are partly not implemented (the viewer's
name and decision 3's tuning): 82% satisfied.

### What shipped

- **`protocol/`**, the public package:
  - the header, the primitives and the type registry (Framing);
  - 29 message types, 3 either way, 11 from the client and 15 from the
    server, each with `Append` and `Decode`;
  - the handshake: `Hello`, `Welcome`, `Capabilities` with `Agree`, the
    hello query and the token subprotocol;
  - 15 close codes, WebSocket statuses, and `CloseCodeFor`;
  - golden vectors in `protocol/testdata/`: `framing.json`,
    `messages.json` and, from #109, `codecs.json`;
  - unit, golden and round-trip tests, a test that checks every example
    in docs/protocol.md against the vectors, 6 fuzz targets, 10
    benchmarks and 3 examples.
- **[docs/protocol.md](../../docs/protocol.md)**, the normative
  description (decision 7), and `docs/protocol-cn.md` marked as the v0
  (Guacamole) description.
- **Users of the package**, built by other specs: the server's viewer
  writer and streams (`internal/stream`, `server/`), the Go client core
  (`client/`, #107), and the web client's TypeScript codec
  (`client/occamy-web/src/protocol`, #114), tested against
  `messages.json`.

### Design evolution

- **The handshake's layout** (#96). `hello` and `welcome` put fixed
  fields before strings, `hello` gained `cols` and `rows`, `welcome`
  gained `viewer_id`, and the frame window and the message size live
  only in the capabilities. The proposed shape above now says so;
  Implementation notes, Deviations, lists each change.
- **Close codes are WebSocket statuses** (#96), with `internal-error`
  (1011) for the server's own failures. `too-slow` (4008) was added in
  #111 (0fe9ff2).
- **The frame window has an exception for input** (#96,
  docs/protocol.md, The frame window). The proposed shape says at most
  `window` frames are unacknowledged. The frame that carries an input's
  effect goes out at once, one beyond the window if it is full, so a
  viewer can have `frame_window` + 1 frames unacknowledged.
- **A hidden viewer's acknowledgements** (#111, 93482c4). A viewer that
  leaves a frame unacknowledged for the viewer timeout gets `too-slow`,
  hidden or not, so a hidden client acknowledges frames as they arrive.
  A `term-snapshot` may also come in any frame.

### Not implemented

- **The viewer's name.** `viewer-joined` carries a `name`, and
  docs/protocol.md calls it the viewer's user name, but occamyd always
  sends it empty: `session.ViewerInfo` has only an ID and a role, and
  `internal/stream/writer.go` sets no name. The follow-up is in the
  Implementation notes.
- **Decision 3's tuning.** `DefaultFrameWindow` is 2, as decided, but it
  has not been tuned with the slow-viewer test and the benchmark under
  N1 and N2. That waits for the benchmark's v1 series and must happen
  before the release candidate.
- **Predicted echo.** The wire carries what it needs, the input
  sequence number in `frame-end`, but no client uses it yet. It belongs
  to the clients ([native-ssh-backend](native-ssh-backend.md),
  Predicted echo).

### Unspecified work

- `max_message_size` below 64 KiB is refused (#100, 6375f97), since a
  peer could agree a size too small for any terminal data and make the
  writer panic or send empty messages (an improvement).
- `too-slow` and the hidden client's acknowledgements (#111), so a viewer
  that stops acknowledging or reading cannot hold the server's memory
  (an improvement).
- `protocol/testdata/codecs.json`, the payload of every codec occamyd
  sends, for clients' decoders (scaffolding, #109).

### Decisions

- Lengths are u32, so a 4 MiB message fits and each value has one
  encoding. Decoding refuses invalid UTF-8, and encoding replaces it
  with U+FFFD, since titles and similar strings come from remote hosts.
- A decoder checks fields, then trailing bytes, then the rules between
  fields, so implementations agree on which error a message fails with.
  Checks that need the session's state belong to the receiver.
- No private or experimental type range. Extensions go through
  capabilities and type numbers that are never reused.
- One session per WebSocket (decision 5) and WebTransport after 1.0
  (decision 6) are deferrals by decision, not gaps.

### Surprises

- Go's base64 decoder skips line breaks even in strict mode, so a hello
  query or token would have had more than one encoding.
- The graphics pipeline turned WebP from rejected into the lossy codec
  while this was built, once pure-Go encoders appeared.
- The Implementation notes said the end reasons map onto the codes from
  4002 and onto 1001. `internal` maps to 1011 as well, and 4007 and 4008
  are not end reasons; the note is corrected above.

### Follow-ups

- `ViewerInfo` gains a name, so `viewer-joined` carries one.
- Tune the frame window with the benchmark's v1 series before the
  release candidate.
- Predicted echo in the web client ([009](../009-react-web-client.md))
  and the client core ([client-core](client-core.md)).
- The protocol freezes at the release candidate. After that, changes go
  through capabilities and ignorable messages only.
- Downstream specs checked: [client-core](client-core.md),
  [graphics-pipeline](graphics-pipeline.md) and
  [native-ssh-backend](native-ssh-backend.md) depend on this one and
  need no correction.
