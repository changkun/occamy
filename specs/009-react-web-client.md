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
  TypeScript with no runtime dependency, about 4,900 lines and 3,300 of
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
    Frame IDs are checked, wrapping from 2³²−1 to 0; the frames waiting
    undelivered and the one delivered and not yet acknowledged stay
    within the window and one more, as the protocol allows; the backlog,
    a frame's work, the screen's pixels and a cursor's pixels (before
    they are allocated) are bounded as in Go, each queued message counted
    at its size and 512 bytes more for what holding it costs; title,
    clipboard, cursor and pointer position keep their latest value,
    bells stop at three, and a viewer that leaves cancels its queued
    join. Input is numbered from 1, wrapping from 2³²−1 to 1, and checked
    against the protocol's ranges, the agreed message size and the role
    before anything is sent. A terminal snapshot may come in any frame,
    as occamyd sends one when a hidden viewer shows again. The server's
    text (title, clipboard, prompt strings, viewer names, close reason)
    is bounded only by the agreed message size, 4 MiB by default; the
    library passes it on whole, and the shell clips what it shows.
  - Decoding goes through `TileDecoder`: `LocalDecoder` decodes on the
    calling thread, and `WorkerDecoder` hands the same work to Workers
    running `serveDecoder` (`decode.worker.ts`), so the session cannot
    tell them apart. `WorkerDecoder` keeps at most
    `navigator.hardwareConcurrency` requests in Workers, the rest waiting
    with their payloads not yet copied. A Worker with requests that has
    said nothing for 5 s is pinged, and one that does not answer within
    5 s more fails its requests, and so the session, as does a Worker
    that reports an error or a message that does not deserialize.
    Deflate inflates through
    `DecompressionStream('deflate-raw')`, fed the payload 1 KiB at a
    time as it asks for more, into a buffer that grows to the
    container's cap, stopping at the chunk that passes the cap. WebP and
    JPEG decode through `createImageBitmap` and a 2D canvas, after the
    size their header claims (VP8, VP8L, VP8X, SOF; PNG's IHDR too) is
    checked against the rectangle and without their EXIF (below). The
    client offers deflate where `DecompressionStream` takes
    `deflate-raw`, WebP where a 1×1 probe decodes, JPEG where
    `createImageBitmap` exists, raw always, and never `cached`, since it
    keeps no tile cache.
  - `Display` waits for an animation frame and presents each frame's
    dirty rectangles to a `Surface` in one task, so the browser never
    paints half a frame; `CanvasSurface` puts them on a canvas or an
    OffscreenCanvas, resizing it in the same task. A hidden page gets
    no animation frames, so there it presents at once, and its frames are
    acknowledged as they arrive: occamyd closes a viewer that leaves a
    frame unacknowledged for its viewer timeout, 30 s, with `too-slow`.
  - `keyboard.ts` maps `KeyboardEvent.code` to USB HID usages, the keys
    of the VNC backend's HID-to-XT table, which a test reads from
    `qemukeys.go`, and `KeyboardEvent.key` to X11 keysyms: Latin-1 as
    it is, other text 0x01000000 above its code point, named keys,
    keypad keys by location, left and right modifiers.
  - `dom.ts` binds the DOM: keys (a release and an auto-repeat carry
    the first press's keysym, keys held are released on blur and, for
    macOS, when Command goes up, composed text goes as `text`), the
    pointer (PointerEvent's buttons reordered to the protocol's, the page
    mapped to the screen, a press focuses the element), the wheel
    (fractions add up; lines stay lines, a page is a screen), the page's
    visibility as `view`, and a cursor image as a CSS `cursor`, or the
    default arrow for one over 128 pixels a side, which browsers ignore.
  - `errors.ts` names the close codes, `too-slow` (4008) among them, and
    sorts them for a UI: done, auth, limit, server, protocol,
    connection. A test holds them to docs/protocol.md's table.
  - `client/internal/webinterop`, a Go command that serves
    `internal/stream` and `internal/session` over WebSocket on loopback
    with a scripted desktop and an echoing terminal, and reports the
    desktop's screen, cursor and input, and a log of frames sent and
    demands, as JSON.
  - On the server, `internal/stream` logs a client's `close` whose code
    is not `normal` at info, since a browser's close frame carries 1000
    for most codes; the guacd backend drops a key of keysym 0, as the
    VNC backend does, since guacd takes keysyms only.
  - `src/protocol/demo`, a development page served by its own Vite
    config, in no build of the app: a live session on a canvas, and with
    `?check` the browser checks below.
- **Tests:** vitest in Node, in the `web` job with lint, `tsc` on the
  library (browser types only) and on the tests, and the build; 459
  tests, in about 3 s. Every vector of `protocol/testdata`: each
  message of `messages.json` decoded to its fields and encoded back,
  each invalid one refused with its error, the hello queries, tokens and
  subprotocols, `framing.json` whole, and `codecs.json`'s lossless
  vectors exact through Node's `DecompressionStream`, also inflated a
  byte at a time, its invalid payloads refused, and the cap shown by a
  harness that counts what the decoder pulls: exactly the cap for the
  vector at the cap, the cap and one byte for the vector over it and for
  the bomb, and no more than one platform chunk when the platform
  chunks it. A harness that inflates as eagerly as Chrome, in 64 KiB
  chunks, shows the feeding: a container that fits is fed whole, a bomb
  no more than one 1 KiB slice, and the feeding stops when the consumer
  does. WebP and JPEG vectors are checked in Node as far as their
  headers, with every other size refused before a decoder is called,
  and EXIF is dropped from JPEG, PNG and WebP. Scripted servers cover
  the handshake and the agreement, frames, an overlapping copy-rect
  both ways, a resize within a frame, the framebuffer, the window (the
  shown frame counted until acknowledged), backlog (each message's cost
  counted and freed on delivery), work, event and cursor bounds,
  coalescing, bells, viewers, pings, prompts, each way a server breaks
  the protocol with the code the client sends, frame IDs and input
  numbers that wrap, read-only and owner rules, viewports, a terminal
  snapshot in a later frame, the end, and `close()`. The interop test
  runs `connect()` over Node's WebSocket against `webinterop`, which CI
  builds with the race detector and stops on a race: the framebuffer
  equals the desktop byte for byte (Node offers deflate and raw, so
  nothing is lossy), the cursor equals it with straight alpha, a key
  paints the swatch in a frame whose `lastSeq` reports it, pointer,
  wheel and text arrive as sent, the clipboard comes back, bell and
  title arrive, a resize request resizes, a hidden client gets no
  frames (by the server's log of frames sent, not by a wait) and the
  current screen when shown, the remote's end is `remote-ended`, a
  wrong token is `unauthorized`, and a terminal echoes. `webinterop`
  has a Go test of its own with the Go client. Lint lets the library
  import only its own modules, which a test checks with `pako`, `fs`
  and `../main.js`.
  - Tested in Node through stand-ins only: Workers (a `MessageChannel`
    whose far end runs `serveDecoder`, and ports that fail or fall
    silent), the canvas (`MemorySurface`), DOM events and a hidden page
    (Node's `EventTarget` and `Event`), and `connect`'s failure paths (a
    scripted WebSocket).
  - Checked by hand in Chrome 156 (headless shell 156.0.8078.4, driven
    over the DevTools protocol): the demo page's 55 checks pass, through
    Vite's dev server and through a production build of the demo, whose
    Worker is bundled. Lossy vectors decode at their `min_psnr` or above
    (WebP 35.8 and 39.8 dB, JPEG 36.8 and 38.8 dB); lossless ones are
    exact, but for the translucent pixels of the lossless WebP cursor
    (below); the invalid ones are refused, trailing bytes included; a
    JPEG with an EXIF orientation of 6 decodes as encoded; Workers decode
    exactly as the page does; `CanvasSurface` presents exactly;
    `cursorStyle` draws a PNG. A deflate bomb of 512 MiB (521,826
    bytes) for a 64×64 tile is refused in 0.5 ms with the renderer at
    85 MiB, from 76; fed whole, as before, it took 79 ms (median of
    three) and 608 MiB. A session with `webinterop` through
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
    its folder, and lint allows it nothing else, so it moves to a React
    shell or a package of its own unchanged.
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
    JPEG's full range. No option stops a browser applying EXIF
    orientation, so the client drops EXIF from the payload first.
  - No pure-JS inflater: `DecompressionStream` refuses trailing bytes and
    a stream without its final block, and, fed a slice at a time as it
    asks, inflates at most a slice, about 1 MiB, past what the client
    takes.
  - The cost of holding a queued message, 512 bytes in both clients,
    covers what was measured beyond the message's own bytes: in Node
    339 bytes for a raw tile, 118 for a copy-rect and 405 for a deflate
    tile; in Go 110 for raw and copy-rect, 334 for a codec of the
    caller's.
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
  - `DecompressionStream` yields 64 KiB chunks in Chrome and 16 KiB in
    Node, so the client may inflate the rest of the chunk that crosses
    the cap, and it reports trailing bytes only at the end, so the client
    drains it. Chrome inflates whatever it was written at once, however
    little is read: a 512 MiB bomb written whole took the renderer to
    608 MiB before the client refused it, hence the slices. Node buffers
    16 KiB of input and inflates as it is read.
  - Chrome 156 applies an EXIF orientation under `imageOrientation:
    'none'`, which the HTML standard has made another name for
    `'from-image'`: a 24×16 JPEG or PNG with orientation 6 decodes as
    16×24. It ignores a WebP's EXIF. The client drops all three.
  - A 2D canvas keeps premultiplied pixels: the lossless WebP cursor
    vector's translucent pixels read back within 1, its opaque ones
    exactly. occamyd sends cursors in deflate to a client that lists it,
    which this one does wherever `DecompressionStream` exists.
  - occamyd's encoder chooses lossy or lossless per tile: a tile with
    both a gradient and sharp two-colour stripes went WebP at quality 90
    with errors up to 126 (16 dB in that tile). `webinterop`'s desktop
    keeps its stripes to whole tiles.
  - Both clients counted the frames queued against the window and one
    more, not a frame delivered and not yet acknowledged, so they allowed
    one frame more than the protocol does; both count it now.
  - Chrome for Testing 156 with `--headless=new` on macOS never finished
    loading the demo page under the DevTools protocol; the headless
    shell of the same build loads and runs it.
- **Follow-ups:**
  - Decide React or Vue, then build the shell on this core and retire
    `occamy.js` with v0. The shell clips the server's text before it
    shows it.
  - A browser job (Playwright with Chromium, Firefox and WebKit) that
    runs the demo's checks against `webinterop`.
  - The tile cache, in both clients.
  - Present from a Worker through an OffscreenCanvas if main-thread
    `putImageData` shows in profiles.
  - copy-rect is tested against scripted servers only, since
    `internal/stream` does not send it yet; add it to the interop test
    once it does.
