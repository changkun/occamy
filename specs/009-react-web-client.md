---
title: React web client
status: testing
track: client
depends_on: []
affects:
  - client/occamy-web/
  - client/internal/webinterop/
  - server/
  - internal/config/
  - docker/
  - .github/workflows/dockerimage.yml
  - .github/workflows/release.yml
  - conf.yaml
  - README.md
  - SECURITY.md
  - CHANGELOG.md
  - docs/
effort: medium
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# React web client

## Overview

The demo web client was moved to Vue 3 during the revival. On 2026-10-31
the owner decided to rebuild it with React (see Decisions), as the web
client of protocol v1 and API v1: with it, the Vue app, `occamy.js` and
the client's last use of the Guacamole protocol go. This spec captures
the shape of that client. It is built: the protocol core first, then
the React shell on it (see the implementation notes below).

## Current State

- `client/occamy-web/` is React 19 and TypeScript, built by Vite 8: the
  protocol v1 client core in `src/protocol`, the shell's logic in
  `src/lib` (no React), and its views in `src/views`. It speaks API v1
  and protocol v1 alone. A v1 file serves its build under `/static/`
  with `web_client`.
- Before it, the client was Vue 3 with vue-router and element-plus: a
  `Login.vue` form (protocol, host, username and password) posting to
  `/api/v0/login`, and a `Desktop.vue` that wired the 9k-line fork of
  guacamole-common-js, `occamy.js`, to `/api/v0/connect`. All of it is
  gone.

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

## Decisions

Settled on 2026-10-31.

- **React, by the owner's decision.** The web client is rebuilt in
  React, and Vue 3 goes. The goal is to remove Guacamole entirely: no
  guacd, no Guacamole protocol and no v0 routes, so the shell speaks
  protocol v1 through the TypeScript core and API v1 alone, never
  `occamy.js` or `/api/v0`. This resolves the open question "React or
  stay on Vue 3?": React has the larger ecosystem, terminal widgets
  included, and the Vue code was small, so the switch was cheap.
- **Plain components, no UI library.** The client has a sign-in form, a
  list and a session view; element-plus and vue-router go with Vue.
- **Order:** the two steps of Architecture collapse into one. The
  protocol core came first, so the React shell is built on it directly,
  with no interim shell around `occamy.js`.

## Testing Strategy

- Component tests for the login form and token handling.
- Browser end-to-end test (Playwright or similar) against the
  [006](006-demo-stack-refresh.md) stack: log in, see a non-blank canvas,
  send a key.

## Acceptance Criteria

- [x] Decision recorded (React or Vue): React, by the owner, on
      2026-10-31.
- [x] If React: shell at parity with the Vue client, with lint and build in
      CI.

## Implementation notes: protocol client

- **Status:** step 2's protocol core is built, ahead of step 1. The
  shell decision was open when it was built, and the core does not
  depend on it: it imports no framework, so either shell wraps it. The
  owner chose React on 2026-10-31 (see Decisions).
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
    `createImageBitmap` exists, raw always, and then `cached`, keeping a
    tile cache of 1024 slots by default (`cacheSlots`; added by the
    graphics pipeline's Tile cache task on 2026-10-31).
  - `Display` waits for an animation frame and presents each frame's
    dirty rectangles to a `Surface` in one task, so the browser never
    paints half a frame; `CanvasSurface` puts them on a canvas or an
    OffscreenCanvas, resizing it in the same task. A hidden page gets
    no animation frames, so there it presents at once, and its frames are
    acknowledged as they arrive: occamyd closes a viewer that leaves a
    frame unacknowledged for its viewer timeout, 30 s, with `too-slow`.
  - `keyboard.ts` maps `KeyboardEvent.code` to USB HID usages, the keys
    of the HID-to-XT table that the VNC and RDP backends send by, which
    a test reads from `internal/keymap/keymap.go` (the RDP backend drops
    Power, which the test reads from its source too), and
    `KeyboardEvent.key` to X11 keysyms: Latin-1 as
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
- **What was not done:** the shell and its wiring, and deleting
  `occamy.js`, came next (Implementation notes: React shell). Not done:
  removing the v0 routes; the tile cache; applying frames and
  presenting them off the main thread with an OffscreenCanvas; a browser
  job in CI.
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
  - Done: the React shell on this core, with `occamy.js` retired; it
    clips the server's text before it shows it. The v0 routes go at
    v1.0.
  - A browser job (Playwright with Chromium, Firefox and WebKit) that
    runs the demo's checks against `webinterop`.
  - The tile cache, in both clients.
  - Present from a Worker through an OffscreenCanvas if main-thread
    `putImageData` shows in profiles.
  - copy-rect is tested against scripted servers only, since
    `internal/stream` does not send it yet; add it to the interop test
    once it does.

## Implementation notes: React shell

- **Status:** built on the protocol core, at parity with the Vue client
  and past it; the Vue app, `occamy.js`, element-plus, vue-router and
  axios are deleted. The server's v0 routes stay until v1.0.
- **What was built:** about 2,400 lines of TypeScript and 400 of CSS,
  and 1,200 lines of tests.
  - `src/lib`, the shell's logic, free of React so that vitest runs it
    in Node: `api.ts` (API v1: sign-in, OIDC's token exchange, the
    connections, ending a session, shares; strict JSON with API v1's
    error codes, and nothing sent with an expired token, which counts as
    expired 10 s early), `oidc.ts` (PKCE: a 64-character verifier drawn
    without bias, its S256 challenge, the login URL, the verifier kept in
    `sessionStorage` for the round trip), `fragment.ts` and `targets.ts`
    (what the page starts with, read once from its fragment and removed
    from the address bar: a share link, `#join=…&token=…`, or OIDC's
    `#code=` or `#error=`; a code without this tab's verifier is spent
    with a throwaway one; the verifier is forgotten on every start),
    `app.ts` (the screens as a reducer, and what waits for the user to
    sign in again: a stream to open, or an owner's End or share, kept in
    `sessionStorage` across single sign-on), `owner.ts` (End: the
    owner's stream, then `DELETE`), `controller.ts` (one stream:
    connect with the page's size and visibility, run the core's
    `Display`, bind input by role, keep what the page shows as an
    immutable state for `useSyncExternalStore`), `ending.ts` (a close
    code and reason, or a failure to connect, as a title, a sentence,
    the technical detail and what to offer: join again, or sign in),
    `roles.ts`, `layout.ts` and `text.ts` (clipping the server's
    strings, with control and bidi characters dropped).
  - `src/views`: sign-in (a password, and single sign-on where
    `GET /api/v1/auth/oidc/login` without a challenge answers 400),
    the connection list, and the session view: a toolbar (title, role,
    the other viewers, Clipboard, Share for the owner, Full screen, End
    session or Leave), a canvas scaled to fit with the remote's cursor,
    or xterm.js for a terminal session, loaded only then, an overlay
    while connecting and one for the end, the owner's credential prompt
    in a modal `<dialog>` with secret fields as password inputs, a
    clipboard panel, and a share panel that mints a viewer or read-only
    link lasting 10 minutes to 12 hours.
  - The owner's window size goes to the remote 300 ms after it settles,
    in device pixels or cells; viewers and read-only clients see the
    owner's size, scaled. Read-only clients bind no input. The remote's
    clipboard goes to the local one when the page has focus, for a role
    that gives input, and the local one to the remote on focus where the
    browser already lets the page read it (Chrome's `clipboard-read`
    permission); otherwise the clipboard panel copies and pastes by
    hand, and keeps what the user typed there when the remote's
    clipboard changes. Control, Alt and Shift together leave the canvas
    or the terminal for the toolbar, since the remote takes every other
    key, Tab included.
  - `vite build` writes the licenses of the packages it bundles to
    `dist/THIRD_PARTY_LICENSES.md` (React, React DOM, scheduler,
    xterm.js and its fit add-on, all MIT); the image ships it under
    `licenses/web/`, and the release archive in `dist/`.
  - occamyd: `web_client` in a v1 file names the directory served under
    `/static/`, through an `os.Root` opened per request: no directory
    listings, no symbolic links out of it. The page is `no-cache`, the
    hashed `assets/` are cached for a year as `immutable`, and every
    response has `nosniff`, `no-referrer` and a Content-Security-Policy
    of `'self'` for scripts, the Worker and connections (with the
    request's `ws://` and `wss://` origin named), `data:` images for the
    cursor, inline styles for React and xterm.js, and
    `frame-ancestors 'none'`. The v0 `client: true` route serves through
    the same handler. Validation refuses a `web_client` that is a file
    or unreadable; occamyd warns at start when it holds no `index.html`
    or does not exist.
    The demo stack's `occamy-v1` serves the image's build and opens the
    demo SSH server through the native backend.
- **Parity with the Vue client:**
  - Sign-in: the Vue client posted a protocol, host, user name and
    password to `/api/v0/login`; the React client signs the user in
    through API v1 and lists the registry's connections, so targets and
    their credentials stay in occamyd's configuration.
  - Display scaling: both fit the remote's screen to the window, up and
    down; the React client also sizes the remote to the window for the
    owner, and tells the hello the device pixel ratio.
  - Mouse: buttons, motion and the wheel, through the core's
    `bindPointer`.
  - Keyboard: keysyms as before, now with each key's USB HID usage, and
    AltGr, dead keys and composed text as the core handles them; keys
    held are released when the canvas loses focus.
  - New: terminal sessions, the clipboard, credential prompts, sharing,
    viewers, visibility, the remote's cursor and title, the bell, and
    why a session ended.
- **Tests:** 131 in `src/lib` and `src/views`, in the `web` job. Unit
  tests for the API calls against a scripted fetch (each request's
  method, path, headers and exact body), PKCE against RFC 7636's vector
  and drawn from `crypto.getRandomValues`, the fragment and its removal
  from the history entry, the start of the page and the verifier it
  forgets, the screens' reducer (an owner's End, share and Join again
  after the token expired, a late leave of a view that is gone), End
  (nothing sent or closed with an expired token, the owner told when it
  fails), layout, roles, clipping, the ending of every close code and
  of a takeover, and the prompt's markup, rendered without a DOM. The
  controller runs against a stand-in `connect` and session (the
  hello's size and visibility, clamping, refusals, React's development
  effects cancelling an open, close cancelling one, the owner's resize
  while another is on its way, the clipboard by role and the panel's
  draft, a failure to show the session closing the stream) and, in the
  interop test, against `webinterop` built with the race detector: a
  graphical session's frames, keys, pointer, wheel, clipboard both
  ways, bell, title, resize and the end blocking input, the visibility
  log, and a terminal session's snapshot, echo, resize, close, and
  acknowledgements while hidden; a wrong share is "Link not valid". A
  lint test holds the shell to the core's index.
  - Checked by hand in Chrome 156 (headless shell 156.0.8078.4, driven
    over the DevTools protocol) against occamyd serving the build with
    `web_client` under `/static/`, with vnctest's fake RFB server through
    the native VNC backend and a fake SSH server through the native SSH
    backend: a wrong password and a right one; the connection list; a
    VNC session on a canvas at the window's size in device pixels with
    the remote's cursor; a click and keys reaching the server; the
    window's resizes reaching it as SetDesktopSize; the remote clipboard
    shown, and text sent back; a read-only share joined from a second
    page, both toolbars counting the other viewer; the owner ending the
    session and the viewer told so; an SSH session in xterm.js with
    typed input echoed and resizes sent as window changes; a connection
    with `prompt: true` asking for the password in the dialog; and a
    share link that is not valid. With the page's clock set ahead to
    expire its token, and occamyd configured for OIDC: Share, End (with
    a viewer watching, cancelled once) and Join again each asking to
    sign in over the session, which kept streaming, then going on; the
    owner's stream taken over by a newer one, told "Opened elsewhere";
    `#error=` and `#code=` forgetting the verifier and what waited; and
    single sign-on leaving for occamyd, whose issuer was unreachable,
    and coming back with its error. No page broke the
    Content-Security-Policy. The console showed only the browser's
    failed-resource lines for requests meant to fail: the OIDC probe's
    404, or 400 where single sign-on is configured, the wrong password's
    401 and the bogus code's 401.
    Firefox and Safari were not run.
- **Decisions:**
  - No router and no UI library: three screens in one component's
    state. The access token lives in memory only; a reload signs in
    again, as occamyd issues no refresh token.
  - No `@vitejs/plugin-react`: Vite 8 compiles JSX with its own
    transformer (`oxc.jsx`), so the only new build dependency is
    `eslint-plugin-react-hooks` 7, with the React Compiler's checks.
  - The controller is re-entrant, so React's development effects, which
    mount, unmount and mount again, open one stream: an open waits a
    microtask, which the first cleanup cancels.
  - The owner ends a session by closing its own stream, then ending the
    session through API v1, so viewers are told at once rather than
    after the owner's minute of grace.
  - 2026-10-31: an owner's action that finds the access token expired
    (End, Share, Join again) opens the sign-in form as a modal dialog
    over the session view, which stays mounted and keeps streaming, and
    resumes once the owner has signed in; the owner may cancel and
    carry on. End with an expired token sends nothing and closes
    nothing. When `DELETE` fails, the owner is told that the session
    was not ended for everyone, with Try again; a `404` counts as ended.
    Single sign-on leaves the page, so what waits is kept in
    `sessionStorage` with the verifier and the page joins the session
    again on its return, within the owner's grace, then ends or shares
    it.
  - 2026-10-31: the remote clipboard reaches the local clipboard on its
    own only for a role that gives input (owner, viewer). A read-only
    viewer sees it in the clipboard panel and copies it from there, so
    that what someone else copies does not replace what the viewer
    copied, unasked.
  - 2026-10-31: an owner whose newer stream took over is told by the
    reason of occamyd's normal close, "the owner connected again", which
    `docs/api.md` documents and a test holds both to, rather than a new
    close code: the page says the session was opened elsewhere and
    offers to join it again.
  - 2026-10-31: the remote's secret prompt fields are
    `autocomplete="one-time-code"`, which keeps password managers from
    filling them with, or saving them as, the Occamy password. A page
    that fails to show a session (the canvas or the terminal emulator
    throws) closes the stream with `close` 1003 `unsupported`, for
    which the core's `Session.close` takes a code; browsers close the
    WebSocket itself with 1000.
  - The shell imports the core through `src/protocol/index.ts` alone,
    which lint enforces, so the tile cache and other changes inside the
    core do not reach it.
  - Dependencies are pinned as the repository pins them: caret ranges
    in `package.json`, exact versions in `package-lock.json`.
- **Deviations:** Architecture's two steps collapsed into one (see
  Decisions). Testing names component tests and a Playwright test
  against the demo stack: the shell's logic is tested in Node without a
  DOM, and the browser run above was by hand. The licenses come from
  Vite's `build.license`, not `scripts/third-party-licenses.sh`, which
  reads Go modules only.
- **Surprises:**
  - `gosec` flags `os.Stat` on `web_client`'s path as traversal by
    taint (G703); it is the operator's configuration, as elsewhere.
- **Follow-ups:**
  - Remove the v0 routes, and with them `client: true`, at v1.0.
  - Make `conf.yaml`, which the image and the archives ship, a v1 file,
    and v1 the main path of the README. It needs a writable place for
    the signing key in the distroless image and a first user, which are
    the owner's to choose.
  - A browser job in CI (Playwright with Chromium, Firefox and WebKit)
    against `webinterop` or the demo stack.
  - An end-to-end test of the demo's `ssh` connection through
    `occamy-v1`; `tests/` streams only its VNC connections.
