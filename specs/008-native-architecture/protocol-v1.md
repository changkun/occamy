---
title: Occamy protocol v1
status: drafted
track: architecture
depends_on: []
affects:
  - protocol/
  - docs/protocol.md
  - docs/protocol-cn.md
effort: large
created: 2026-10-29
updated: 2026-10-29
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
- **Handshake:** client `hello` carries the protocol version, token,
  client name and capabilities (codecs, max texture size, terminal
  support, pixel ratio, initial size). Server `welcome` carries the
  session ID, role, backend class (terminal or graphical), the agreed
  capabilities, and an initial `snapshot` that follows immediately.
- **Server to client:** `resize`, `tile` (frame ID, rectangle, codec,
  payload), `copy-rect`, `frame-end` (frame ID and timestamp), `cursor`
  (image and hotspot) and `cursor-move`, `term-data` (bytes),
  `term-snapshot`, `clipboard` (MIME type and data), `bell`, `title`,
  `prompt` (keyboard-interactive or credential request), `viewer-joined`
  and `viewer-left`, `ping`, and `close` (code and reason).
- **Client to server:** `key` (down or up, keysym, and physical code as the
  W3C `KeyboardEvent.code` string or a USB HID usage), `text` (UTF-8, for
  IME), `term-input` (bytes produced by the client's terminal emulator,
  such as xterm.js `onData`), `pointer` (x, y, button mask), `wheel` (dx,
  dy, with pixel or line mode), `resize-request`, `clipboard`, `prompt-reply`,
  `frame-ack` (frame ID and decode time), `pong`, and `close`.
- **Flow control:** the server keeps at most `window` unacknowledged
  frames per viewer (agreed in `welcome`, default 2). While the window is
  full, damage accumulates (see [session-core](session-core.md)) and is
  sent as one coalesced frame once an ack arrives. Terminal data is not
  windowed but is bounded by the WebSocket write buffer, with a
  disconnect-on-overflow limit.
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

## Open Questions

1. Encoding A, B or C? A is recommended given the payload-heavy traffic
   and a small message catalogue.
2. Physical key code: `KeyboardEvent.code` strings or HID usages? HID is
   recommended on the wire, with the translation table in the client
   core and the web client.
3. Is the default frame window of 2 right? It should be tuned against the
   slow-viewer test and a 100 ms RTT link.
4. Should terminals get their own flow control (byte credits), or is the
   WebSocket buffer limit enough for beta?
5. Multiplexing: one session per WebSocket for beta, or streams from the
   start, so that file transfer can be added later without a new
   connection?
6. Is WebTransport (datagrams for pointer moves) deferred to after 1.0?
7. Must `docs/protocol.md` be normative, with the Go package as the
   reference implementation, or the reverse?

## Affects

Creates `protocol/` (public package) and `docs/protocol.md`.
`docs/protocol-cn.md` is relabelled as the v0 (Guacamole) description. The
web client ([009](../009-react-web-client.md)) and the client core
([client-core](client-core.md)) implement it.
