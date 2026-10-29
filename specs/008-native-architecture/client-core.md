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
updated: 2026-10-29
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
