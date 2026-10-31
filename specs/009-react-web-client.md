---
title: React web client
status: vague
track: client
depends_on: []
affects:
  - client/occamy-web/
  - client/internal/webinterop/
  - docker/Dockerfile
  - .github/workflows/dockerimage.yml
  - .github/workflows/release.yml
effort: medium
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# React web client

## Overview

The demo web client was moved to Vue 3 during the revival. Rebuilding it with
React is under consideration. This spec captures the recommended shape so that
the decision does not block on [008](008-native-architecture.md).

## Current State

- `client/occamy-web/`: Vue 3, Vite, vue-router, element-plus. Two views:
  `Login.vue` (protocol, host, username and password form posting to
  `/api/v0/login`) and `Desktop.vue` (wires `occamy.Client`, `Display`,
  `Mouse` and `Keyboard` to a WebSocket at `/api/v0/connect`).
- `src/components/occamy.js` is a 9k-line fork of guacamole-common-js and
  holds all protocol and rendering logic; the Vue code around it is under
  200 lines.

## Architecture

Recommended in two steps:

1. **React shell.** Vite with React and TypeScript. A login view and a
   desktop view that wrap the existing `occamy.js` objects, at parity with
   the Vue client: protocol selection, display scaling, mouse and keyboard.
   Small, and independent of protocol work.
2. **Protocol client swap.** For `v1.0.0-beta.1`
   ([008](008-native-architecture.md)), replace `occamy.js` with a
   TypeScript client for
   [protocol v1](008-native-architecture/protocol-v1.md): tile decoding in a
   Worker (`createImageBitmap`, `DecompressionStream`, OffscreenCanvas) and
   a terminal view (xterm.js) for terminal backends. It is tested against
   the golden vectors from the Go `protocol` package.

Open questions:

- React or stay on Vue 3? React has the larger ecosystem, including terminal
  widgets; the current Vue code is small, so either choice is cheap now.
- UI component library, or plain components? The client has one form.

## Testing Strategy

- Component tests for the login form and token handling.
- Browser end-to-end test (Playwright or similar) against the
  [006](006-demo-stack-refresh.md) stack: log in, see a non-blank canvas,
  send a key.

## Acceptance Criteria

- [ ] Decision recorded (React or Vue).
- [ ] If React: shell at parity with the Vue client, with lint and build in
      CI.

## Implementation notes: protocol client

- **Status:** step 2's protocol core is built, ahead of step 1. The
  shell decision, React or Vue, is still open, and the core does not
  depend on it: it imports no framework, so either shell wraps it. The
  Vue app does not use it yet, and `occamy.js` and the v0 routes are
  untouched. The spec stays `vague` until the shell is decided.
- **What was built:** `client/occamy-web/src/protocol`, strict
  TypeScript with no runtime dependency, about 4,500 lines and 2,300 of
  tests. It is the browser's counterpart of the Go client
  ([client-core](008-native-architecture/client-core.md)) and keeps its
  design: the same delivery and acknowledgement rules and the same
  bounds on what a server can make it hold.
  - `wire.ts`, `messages.ts`, `handshake.ts`: framing, every message both
    ways with every length and value check of docs/protocol.md, the
    capabilities, `hello` in the upgrade query (strict base64url, at most
    2048 bytes) and the token as a subprotocol.
  - `connect(url, token, options)` opens the WebSocket with `occamy.v1`
    and the token beside it, never in the URL, buffers messages that
    arrive before the session listens, refuses another subprotocol with
    `unsupported`, and resolves on `welcome`; a `close` before it
    rejects with a `CloseError` of its code, a failed connection with a
    `ConnectionError`. `Session.start` does the same over any
    `Transport`.
  - `Session`: `next()` delivers frames, applied to an RGBA framebuffer
    in memory as they are delivered (a resize first), with their dirty
    rectangles, terminal bytes untouched, and the small state, then the
    end with its close code; a frame is acknowledged when `next()` is
    called again, with `decode_time` from its `frame-end` to then.
    Frame IDs are checked, wrapping from 2³²−1 to 0; at most the window
    and one more frame wait undelivered; the backlog, a frame's work, the
    screen's pixels and a cursor's pixels (before they are allocated) are
    bounded as in Go; title, clipboard, cursor and pointer position keep
    their latest value, bells stop at three, and a viewer that leaves
    cancels its queued join. Input is numbered from 1, wrapping from
    2³²−1 to 1, and checked against the protocol's ranges, the agreed
    message size and the role before anything is sent.
  - Decoding goes through `TileDecoder`: `LocalDecoder` decodes on the
    calling thread, and `WorkerDecoder` hands the same work to Workers
    running `serveDecoder` (`decode.worker.ts`), so the session cannot
    tell them apart. Deflate inflates through
    `DecompressionStream('deflate-raw')`, counting what it yields and
    stopping at the chunk that passes the container's cap. WebP and JPEG
    decode through `createImageBitmap` and a 2D canvas, after the size
    their header claims (VP8, VP8L, VP8X, SOF; PNG's IHDR too) is
    checked against the rectangle. The client offers deflate where
    `DecompressionStream` takes `deflate-raw`, WebP where a 1×1 probe
    decodes, JPEG where `createImageBitmap` exists, raw always, and
    never `cached`, since it keeps no tile cache.
  - `Display` waits for an animation frame and presents each frame's
    dirty rectangles to a `Surface` in one task, so the browser never
    paints half a frame; `CanvasSurface` puts them on a canvas or an
    OffscreenCanvas, resizing it in the same task.
  - `keyboard.ts` maps `KeyboardEvent.code` to USB HID usages, the keys
    of the VNC backend's HID-to-XT table, which a test reads from
    `qemukeys.go`, and `KeyboardEvent.key` to X11 keysyms: Latin-1 as
    it is, other text 0x01000000 above its code point, named keys,
    keypad keys by location, left and right modifiers.
  - `dom.ts` binds the DOM: keys (a release carries its press's keysym,
    keys held are released on blur and, for macOS, when Command goes up,
    composed text goes as `text`), the pointer (PointerEvent's buttons
    reordered to the protocol's, the page mapped to the screen, a press
    focuses the element), the wheel (fractions add up; lines stay lines,
    a page is a screen), the page's visibility as `view`, and a cursor
    image as a CSS `cursor`.
  - `errors.ts` names the close codes and sorts them for a UI: done,
    auth, limit, server, protocol, connection.
  - `client/internal/webinterop`, a Go command that serves
    `internal/stream` and `internal/session` over WebSocket with a
    scripted desktop and an echoing terminal, and reports the desktop's
    screen, cursor, input and demands as JSON.
  - `src/protocol/demo`, a development page served by its own Vite
    config, in no build of the app: a live session on a canvas, and with
    `?check` the browser checks below.
- **Tests:** vitest in Node, in the `web` job with lint, `tsc` on the
  library (browser types only) and on the tests, and the build; 401
  tests, under a second. Every vector of `protocol/testdata`: each
  message of `messages.json` decoded to its fields and encoded back,
  each invalid one refused with its error, the hello queries, tokens and
  subprotocols, `framing.json` whole, and `codecs.json`'s lossless
  vectors exact through Node's `DecompressionStream`, also inflated a
  byte at a time, its invalid payloads refused, and the cap shown by a
  harness that counts what the decoder pulls: exactly the cap for the
  vector at the cap, the cap and one byte for the vector over it and for
  the bomb, and no more than one platform chunk when the platform
  chunks it. WebP and JPEG vectors are checked in Node as far as their
  headers, with every other size refused before a decoder is called.
  Scripted servers cover the handshake and the agreement, frames, an
  overlapping copy-rect both ways, a resize within a frame, the
  framebuffer, the window, backlog, work, event and cursor bounds,
  coalescing, bells, viewers, pings, prompts, each way a server breaks
  the protocol with the code the client sends, frame IDs and input
  numbers that wrap, read-only and owner rules, viewports, the end, and
  `close()`. The interop test runs `connect()` over Node's WebSocket
  against `webinterop`: the framebuffer equals the desktop byte for
  byte (Node offers deflate and raw, so nothing is lossy), the cursor
  equals it with straight alpha, a key paints the swatch in a frame
  whose `lastSeq` reports it, pointer, wheel and text arrive as sent,
  the clipboard comes back, bell and title arrive, a resize request
  resizes, a hidden client gets no frames and the current screen when
  shown, the remote's end is `remote-ended`, a wrong token is
  `unauthorized`, and a terminal echoes. `webinterop` has a Go test of
  its own with the Go client.
  - Tested in Node through stand-ins only: Workers (a `MessageChannel`
    whose far end runs `serveDecoder`), the canvas (`MemorySurface`),
    DOM events (Node's `EventTarget` and `Event`), and `connect`'s
    failure paths (a scripted WebSocket).
  - Checked by hand in Chrome 156 (headless, driven over the DevTools
    protocol): the demo page's 54 checks pass, through Vite's dev
    server and through a production build of the demo, whose Worker is
    bundled. Lossy vectors decode at their `min_psnr` or above (WebP
    35.8 and 39.8 dB, JPEG 36.8 and 38.8 dB); lossless ones are exact,
    but for the translucent pixels of the lossless WebP cursor (below);
    the invalid ones are refused, trailing bytes included; Workers decode
    exactly as the page does; `CanvasSurface` presents exactly;
    `cursorStyle` draws a PNG. A session with `webinterop` through
    Workers agrees on deflate, WebP, JPEG and raw, shows the desktop at
    47 dB, the canvas holds the framebuffer exactly, and the cursor is
    exact. A real click focuses the canvas, and real key presses reach
    the remote with keysym and usage (q: 0x71 and 0x14; right Shift:
    0xffe2 and 0xe5). To run them:

    ```sh
    go build -o /tmp/webinterop ./client/internal/webinterop
    /tmp/webinterop   # prints http://127.0.0.1:PORT
    cd client/occamy-web && npx vite --config src/protocol/demo/vite.config.ts
    # open http://localhost:5173/index.html?check&server=http://127.0.0.1:PORT
    ```

    The results are on the page and in `window.occamyChecks`. Firefox and
    Safari were not run.
- **What was not done:** the shell and its wiring (login, the desktop
  and terminal views, xterm.js for terminal sessions, prompts, the
  clipboard through `navigator.clipboard`, resize requests from a
  `ResizeObserver`); deleting `occamy.js` and the v0 routes; the tile
  cache; applying frames and presenting them off the main thread with
  an OffscreenCanvas; a browser job in CI.
- **Decisions:**
  - The core lives in `src/protocol` of the existing app: one lockfile,
    one CI job, and lint and `tsc` on it now. It imports nothing outside
    its folder, and lint forbids Node modules and UI frameworks there,
    so it moves to a React shell or a package of its own unchanged.
    `vite build` does not compile it until the app imports it, so `tsc`
    is its gate. TypeScript 6.0, the newest that typescript-eslint
    supports; vitest 5; no runtime dependency.
  - The framebuffer is RGBA in memory, not a canvas. A frame applies in
    full, copy-rect reads the previous frame exactly, and Node tests
    every pixel; presenting is a `putImageData` of the dirty rectangles
    (their bounding box beyond 64) after an animation frame.
  - `createImageBitmap` with `premultiplyAlpha: 'none'` and
    `colorSpaceConversion: 'none'`, read back through a 2D canvas,
    rather than `ImageDecoder`, which fewer browsers have. The browser's
    decoders convert YCbCr to RGB as each format means, so the client
    never meets the Go trap of reading lossy WebP's limited range as
    JPEG's full range.
  - No pure-JS inflater: `DecompressionStream` refuses trailing bytes and
    a stream without its final block, and stops when the consumer does.
  - A protocol error ends the session with a `close` carrying the
    protocol's code, then closes the WebSocket with 1000 when browsers
    refuse that code (below).
  - The interop server is Go under `client/internal`, built by the `web`
    job with `setup-go`; the interop test runs when
    `OCCAMY_INTEROP_SERVER` names it, and fails in CI without it.
- **Deviations:** the Architecture above puts the React shell first;
  the core came first, independent of the shell. It names
  OffscreenCanvas in the Worker: decoding is in Workers, but frames
  apply and present on the main thread. Testing names Playwright against
  the demo stack: the browser checks ran by hand instead.
- **Surprises:**
  - Browsers close a WebSocket only with 1000 or 3000 to 4999, so the
    client cannot close with `protocol-error`, `unsupported` or
    `too-large`; docs/protocol.md now says so.
  - A browser's WebSocket has no read limit; the client checks a
    message's size when it arrives.
  - `DecompressionStream` yields 16 KiB chunks in Chrome and Node, so the
    client may inflate the rest of the chunk that crosses the cap, and it
    reports trailing bytes only at the end, so the client drains it.
  - A 2D canvas keeps premultiplied pixels: the lossless WebP cursor
    vector's translucent pixels read back within 1, its opaque ones
    exactly. occamyd sends cursors in deflate to a client that lists it,
    which this one does wherever `DecompressionStream` exists.
  - occamyd's encoder chooses lossy or lossless per tile: a tile with
    both a gradient and sharp two-colour stripes went WebP at quality 90
    with errors up to 126 (16 dB in that tile). `webinterop`'s desktop
    keeps its stripes to whole tiles.
  - Both clients count the frames queued against the window and one
    more, not a frame delivered and not yet acknowledged, so they allow
    one frame more than the protocol does.
- **Follow-ups:**
  - Decide React or Vue, then build the shell on this core and retire
    `occamy.js` with v0.
  - A browser job (Playwright with Chromium, Firefox and WebKit) that
    runs the demo's checks against `webinterop`.
  - Count the delivered, unacknowledged frame in the window bound, in
    both clients.
  - The tile cache, in both clients.
  - Present from a Worker through an OffscreenCanvas if main-thread
    `putImageData` shows in profiles.
  - copy-rect is tested against scripted servers only, since
    `internal/stream` does not send it yet; add it to the interop test
    once it does.
  - `client/events.go` says `Next` acknowledges a frame as it delivers
    it; it acknowledges on the next call, as `Next`'s own comment says.
