---
title: guacd bridge and the path to pure Go
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/graphics-pipeline.md
  - specs/005-upstream-guacamole/guacd-handshake-client.md
affects:
  - internal/backend/guacd/
  - internal/guacd/
effort: large
created: 2026-10-29
updated: 2026-10-30
author: Changkun Ou
dispatched_task_id: null
---

# guacd bridge and the path to pure Go

## Design Problem

During v1, some protocols still run on guacd: everything at beta.1
except SSH, and RDP until a native backend exists. Their sessions must
reach v1 clients through protocol v1. Two things need deciding:

- **How guacd's Guacamole instruction stream becomes protocol v1.** This
  determines whether clients need to know Guacamole at all.
- **The parity gate.** When does a native backend replace the bridge for
  a protocol? These gates are the concrete path to "no guacd".

## Context

- [005](../005-upstream-guacamole.md) provides `internal/guacd` (dial and
  handshake against guacd 1.6, join with `select $id`), and a v0 server
  that forwards Guacamole instructions unchanged.
- guacd 1.6 draws with `img` streams (PNG, JPEG or WebP data in `blob`
  chunks), `copy`, `rect` plus `cfill`, `cursor`, `size`, `move`, `shade`
  and `dispose` on layers and off-screen buffers. It paces itself on
  `sync` replies. Text, clipboard and input go through `clipboard`
  streams, `key` and `mouse`.
- `golang.org/x/image/webp` decodes lossy and lossless WebP, and
  `image/png` and `image/jpeg` cover the rest, all without cgo. Lossy
  WebP needs converting after it: the package hands back limited-range
  YCbCr under a full-range colour model and does not interpolate chroma,
  so `internal/guacamole` converts it as libwebp does (see the
  conformance corpus notes).
- Branch `gio-client` (PR #51, commit `1d9a31d`) holds `internal/guac`: a
  Go Guacamole client with display, layer, stream and instruction
  handlers (about 1,100 lines). It is a reuse candidate for an
  interpreter.

## Options

### Bridge model

- **I. Interpreter (recommended).** The bridge is a backend like any
  other. It implements Guacamole's display model in Go (layers, buffers,
  compositing, image streams, `copy`, `rect`/`cfill`, cursor), renders into
  the session framebuffer, and reports damage. The normal encoder takes it
  from there. It answers `sync` after applying each frame, so guacd paces
  against the bridge, and viewers are paced by session-core. Input is
  translated: `key` to `key`, `pointer` to `mouse`, `clipboard` to a
  `clipboard` stream, `resize-request` to `size`.
  - Pros: clients speak one model, so the Swift client and the web client
    get RDP (and anything else guacd has) at no client cost. Per-viewer
    pacing and sharing roles work the same for bridged sessions.
    Retiring the bridge later changes nothing for clients.
  - Cons: decode and re-encode cost on the server. Guacamole operations
    the interpreter does not implement show up as rendering bugs, which
    needs a conformance corpus.
- **P. Passthrough envelope.** Protocol v1 carries raw Guacamole
  instructions in a `legacy` stream, and clients keep a Guacamole
  renderer.
  - Pros: cheapest on the server; exact rendering.
  - Cons: every client implements two display models. The Swift client
    and the Go client core would need a Guacamole renderer, and pacing,
    sharing and recording bypass session-core. Retiring guacd later
    leaves dead client code.

### Bridged terminals

guacd's SSH renders terminals as pixels, so a bridged SSH session is
graphical. That is acceptable until the native SSH backend passes its
gate, which is planned for beta.1 anyway.

## Parity gate (bridge to native, per protocol)

A protocol's default moves from `guacd` to `native` when all of these
hold:

1. **Opt-in period:** the native backend shipped behind a per-connection
   `backend: native` for at least one beta.
2. **End-to-end parity:** the [006](../006-demo-stack-refresh.md) matrix
   passes for authentication modes, resize, clipboard in both directions,
   cursor, sharing roles, reconnect, and keyboard input including AltGr
   and dead keys.
3. **Performance:** traffic and CPU are no worse than the bridge on the
   baseline scenarios from [008](../008-native-architecture.md).
4. **Soak:** N sessions for H hours with no goroutine or memory growth.

After the switch, `backend: guacd` stays available for one minor release
as a fallback.

| Protocol | Native backend | Planned switch |
|----------|----------------|----------------|
| SSH | [native-ssh-backend](native-ssh-backend.md) | v1.0.0-beta.1 |
| VNC | [native-vnc-backend](native-vnc-backend.md) | v1.0.0-beta.2 |
| RDP | per [rdp-strategy](rdp-strategy.md) | v1.x |
| telnet, Kubernetes | none planned | bridge only, opt-in |

When RDP switches, guacd leaves the default deployment entirely. The
bridge then either stays as an opt-in backend for the remaining guacd
protocols or is deleted.

## Open Questions

1. Bridge model I or P? I is recommended; P only if server CPU proves
   prohibitive in the baseline measurements.
2. ~~Reuse `internal/guac` from `gio-client`, or write the interpreter
   fresh?~~ Answered 2026-10-30: the benchmark wrote it fresh against
   guacd 1.6 (`internal/guacamole/display.go`, all sixteen channel
   masks, layers and buffers), and the bridge reuses it.
3. Which Guacamole operations are in scope? Proposed: those guacd 1.6's
   VNC, RDP and SSH plugins emit, confirmed by recording real sessions,
   with transfer functions other than `SRC` and `OVER` rejected loudly.
   Answered by Decision 3, and the recordings of the conformance corpus
   confirmed it: the bridge applies all the drawing they contain. The
   SSH recording also has three `argv` streams, in which guacd offers
   its terminal's `color-scheme`, `font-name` and `font-size` for a
   client to change; the bridge refuses each with an `ack` (0x0100), as
   viewers cannot change them, and the corpus checks that it does.
4. Gate values: N, H, and the performance tolerance?
5. ~~After RDP goes native: keep the bridge, or delete it?~~ Answered
   2026-10-30: v1.0 ships without guacd
   ([008](../008-native-architecture.md), Path to pure Go), so the bridge
   is removed with it. It carries VNC and RDP over protocol v1 only until
   their native backends pass its parity gate.

## Decisions

Settled on 2026-10-30, following the recommendations above.

1. **Bridge model: I, the interpreter.** v1.0 ships without the Guacamole
   protocol, so no client may depend on it, and under I none does. If the
   baseline shows the server's decode and re-encode cost to be
   prohibitive, that is a reason to bring the native backends forward,
   not to put Guacamole into clients.
2. **Reuse `internal/guacamole`'s display** (open question 2, answered
   above).
3. **Scope: what guacd 1.6's VNC, RDP and SSH plugins emit.** The
   display already counts unsupported drawing operations; the bridge
   logs each opcode the first time it sees it in a session, and the
   conformance corpus fails on any. The display implements all sixteen
   channel masks and all sixteen transfer functions, so none is
   rejected; what it does not implement (paths, strokes, transforms,
   images other than PNG, JPEG and WebP) is counted instead.
4. **Gate values:** N = 20 concurrent sessions for H = 8 hours with no
   growth in goroutines or heap after the first hour, and the native
   backend's traffic and server CPU each no more than 10% above the
   bridge's on every baseline scenario. The 10x target in
   [008](../008-native-architecture.md) is measured against guacd, not
   against this gate; the gate only stops a regression.
5. **The bridge is removed at v1.0** (open question 5, answered above).

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Interpreter additions | `internal/guacamole`: the `cursor` instruction (hotspot and image from a layer region) exposed as a cursor change, and access to the composited display by rectangle for copying out | — |
| Bridge backend | `internal/backend/guacd`: a `Backend` of class graphical over `internal/guacd`. Instructions apply to the display; at each `sync` the damage is copied into the sink (`Lock` and `Unlock` per damaged rectangle) and the `sync` is answered, so guacd paces against the bridge alone. `size` of the default layer becomes `Resize`, `cursor` becomes `Cursor`, `name` becomes `Title`, clipboard streams become `Clipboard`, `required` becomes a `Prompt` whose answers go back as `argv` streams, `error` ends `Run` with the status, and `disconnect` ends it cleanly. Input translates to `key`, `mouse` (button mask, and wheel as buttons 4 and 5 pressed and released), keysyms for `Text`, a `clipboard` stream, and `size`. Writes to guacd are serialised. Tests against a scripted guacd on `net.Pipe` | Interpreter additions; session-core Backend interface |
| Conformance corpus | Instruction streams recorded from guacd 1.6's VNC, RDP and SSH plugins on the bench stack (the bench desktop over VNC and RDP, the demo SSH server; logging in, typing, scrolling, moving a window, resizing), replayed through the bridge in tests: no unsupported operation, no error, the idle screen at checkpoints, and the final frame's hash pinned | Bridge backend |
| Registration | The bridge registered in the API's backend factory for `vnc` and `rdp`, and for `telnet` and `kubernetes` behind an opt-in, with guacd parameters from the configuration under the connection's | Bridge backend; API Sessions |

The soak and performance gates run per protocol when a native backend
asks to replace the bridge, not as tasks here.

## Affects

New `internal/backend/guacd` on top of `internal/guacd`. Decides whether
[client-core](client-core.md), [009](../009-react-web-client.md) and
[010](../010-native-swift-client.md) need any Guacamole code (under I they
do not).

## Implementation notes: bridge backend

- **Status.** Interpreter additions and Bridge backend are done
  (#94). The conformance corpus followed (#104, see its notes below);
  registration has not, so the spec stays validated.
- **What the interpreter gained** (`internal/guacamole`):
  - `Display` applies `cursor`; `TakeCursor` returns the image (copied
    from the source layer when the instruction applies, as
    guacamole-common-js's `Display.setCursor` does) and the hot spot.
  - `CopyTo(dst, r)` copies an area of the composited display into an
    `*image.RGBA`, over black, so the copy is opaque.
  - `TakeDamageRects` returns the damage as at most 16 rectangles: two
    merge when their union is no larger than both together, and past 16
    the pair whose union adds the least area merges. `TakeDamage`
    returns their union, so the bench is unchanged. `Bounds` reports
    the default layer's size.
- **What the bridge is** (`internal/backend/guacd`): `New(addr,
  guacd.Config, Options{Logger, Dial})` returns a `*Backend` with
  `Class` (graphical), `Run`, `Input`, `Demand` and `Unsupported`.
  `Run` dials, calls `guacd.Handshake`, and serves guacd:
  - `sync`: the damage is copied into the sink, one `Lock` and `Unlock`
    per rectangle within the screen, and only then is the `sync`
    answered, with its timestamp alone and once per timestamp, as
    `Client.js` does. guacd rejects timestamps it did not send
    (`user-handlers.c`), so the bridge echoes them.
  - `size` of the default layer: `Resize` at the next `sync`, followed by
    the whole screen, so the session sees the new size and its content
    in one frame.
  - `cursor`: `Cursor`; a fully transparent image becomes nil, since
    guacd hides the pointer with one transparent pixel
    (`display-builtin-cursors.c`). `mouse`: `CursorMove`. `name`:
    `Title`.
  - `clipboard` streams: `Clipboard`, capped at 4 MiB, not acked:
    `StringReader.js` does not ack, and `common/clipboard.c` does not
    wait.
  - `required`: a `Prompt` (name: the protocol; one field per parameter,
    labelled "Private key" for `private-key`, secret when the name
    contains "password" or "passphrase"), answered as `argv` streams of
    `text/plain` without waiting for guacd's acks, as `ManagedClient.js`
    does. A failed prompt ends `Run` with its error.
  - `error`: `Run` returns a `*guacd.Error` with the status.
    `disconnect`: nil. A closed connection without `disconnect` is an
    error wrapping `io.ErrUnexpectedEOF`, since guacd always sends one
    (`user-handshake.c`). Cancellation closes the connection, and `Run`
    returns `ctx.Err()`.
  - Streams the bridge does not take get the ack `Client.js` sends
    without a handler: `audio` and `video` "BAD TYPE" (0x030F), `file`,
    `pipe`, `body` and echoed `argv` 0x0100. `nop`, `msg`, `set`
    (multi-touch), `log` and `ready` need nothing.
  - `Unsupported` counts what the session could not apply: drawing the
    display lacks, image types it cannot decode, refused streams other
    than `argv`, unknown opcodes, and instructions that failed
    (`"end failed"` for an image that does not decode). Each is logged
    once per session, and the session goes on.
- **Input.** `Key` is `key`; `Pointer` is `mouse` with Left 1, Middle 2,
  Right 4; `Text` is a key press and release per character; `Clipboard`
  is a `clipboard` stream in 6048-byte blobs (`ArrayBufferWriter.js`);
  `Resize` is `size`; `TermInput` returns `ErrUnsupportedInput`. Back and
  Forward are dropped: guacd's RDP support reads only bits 0x01 to 0x10
  (`input-queue.c`), and its VNC support passes the mask on as it is.
- **Decisions the spec left open.**
  - The wheel follows `Mouse.js`: a line is 18 pixels, each 53 pixels
    press and release button 4 (up, 8) or 5 (down, 16) at the last
    pointer position with the held buttons, and the remainder is
    dropped after a press, so a notch scrolls once whether a browser
    reports it as 3 lines or as 100 pixels. One press per line would
    scroll three times as far in Firefox. DX is dropped: Guacamole has
    no horizontal wheel. One `Wheel` presses at most 32 times.
  - `Text` keysyms follow `Keyboard.js`'s `keysym_from_charcode`:
    Latin-1 as it is, other characters 0x1000000 plus the code point,
    C0 controls 0xff00 plus the control. A line feed types Return
    (0xff0d) rather than Linefeed, and DEL and the C1 controls type
    nothing.
  - `Input` never writes to guacd: it encodes into a queue of 256 that
    a writer goroutine drains, as the Backend contract asks, and returns
    `ErrQueueFull` when it is full and `ErrClosed` after `Run`. Inputs
    before the handshake wait in the queue. One mutex serialises the
    writer (inputs, nops) and the reader (sync answers, acks, `argv`).
  - A `nop` goes out after 5 seconds without a write, from the writer
    goroutine, so it keeps going while the reader waits on a prompt;
    guacd drops a user silent for 15 seconds. `Client.js` sends one every
    5 seconds regardless.
  - Each stream the bridge sends goes out whole in one write, so fixed
    indices suffice: 0 for clipboard, 1 for `argv`.
  - `Demand` is ignored. guacd sends only what changed, so the bridge
    must apply every frame to keep its display whole, and must answer
    every `sync` for guacd to keep sending.
  - A malformed drawing instruction or an image that does not decode is
    counted and logged, and the session goes on, rather than ending it
    over one tile.
- **Deviations.** Decision 3 said transfer functions other than `SRC`
  and `OVER` are rejected loudly. The display implements all channel
  masks and transfer functions, and guacd builds its cursor with masks
  other than those two, so nothing is rejected; Decision 3 now says so.
- **Surprises.** On `net.Pipe`, `guacd.Handshake` fails if guacd closes
  right after `ready`, because clearing the deadline on a closed pipe
  fails; over TCP it does not.
- **Follow-ups.**
  - Send `copy` within the default layer as `CopyRect` when no visible
    layer covers it, so that scrolls through the bridge cost no pixels.
  - Horizontal wheel over VNC, where RFB buttons 6 and 7 (bits 32 and 64)
    reach the server unchanged; RDP has no bit for it.
  - Send `disconnect` before closing on cancellation, as `Client.js`
    does, so guacd logs a clean leave.
  - ~~The conformance corpus decides whether `Unsupported` stays empty on
    real VNC, RDP and SSH sessions.~~ It does on all three recordings.

## Implementation notes: conformance corpus

- **Status.** The conformance corpus is done (#104). Registration is
  the last task, so the spec stays validated.
- **What it is** (`internal/backend/guacd/testdata/conformance`): one
  recording per protocol, each a `<name>.guac.gz` (every byte guacd sent
  the bridge, cut after the last complete `sync`, gzipped) and a
  `<name>.json` manifest (guacd parameters without secrets, the size
  asked for, the steps taken, checkpoints, pinned regions, the prompts
  the bridge raised, the instruction counts, and the final frame's size
  and SHA-256), and for each region a `<name>-copy.png` of its pixels.
  - `TestConformance` (no build tag, no containers) checks each stream's
    instruction counts against its manifest, then replays it through the
    bridge from a scripted guacd on `net.Pipe` that sends the stream and
    `disconnect`, inside a `synctest` bubble, answering every prompt
    with "answer". The replay must end without error, with nothing
    unsupported and nothing logged, raise the manifest's prompts (VNC:
    one, a secret field labelled Password), show the idle screen at each
    checkpoint and the pinned pixels in each region, and end on the
    pinned frame. What the bridge sent back is checked too: `select`
    with the protocol first, `size` with the width and height asked
    for, `image` offering WebP, `connect` with the protocol version and
    each value in the order of guacd's `args`, an `argv` stream holding
    "answer" for each name guacd `required`, an `ack` with 0x0100
    refusing each `argv` stream guacd opened, and one `sync` answer for
    each frame, in order.
  - A failed checkpoint or region, or a final frame with another hash,
    writes the frame as a PNG file to the directory that
    `OCCAMY_CONFORMANCE_DUMP` names, and logs the path; without it the
    test logs how to get one.
  - `TestRecord` (build tag `record`) drives a bridge through live
    sessions and writes the corpus; its doc comment has the commands.
    In short: check that the host is idle (`uptime`) and that no bench
    stack holds port 4822, start `guacd vnc rdp ssh` from
    `docker-compose.yaml` plus `docker-compose.bench.yaml` as a compose
    project of its own (`$CONTAINER_ENGINE compose -p occamy-record`),
    run `go test -tags record -run TestRecord ./internal/backend/guacd
    -args -guacd 127.0.0.1:4822`, and take the project down with the
    same `-p`, which leaves other stacks alone. `-record vnc,rdp,ssh`
    picks recordings, and `-pin` pins the existing ones again from
    their streams, without containers, for when the bridge's rendering
    changes on purpose. A checkpoint that never shows, a stream that
    does not parse, a replay that draws nothing or a file that cannot
    be written stops a recording. Otherwise the recorder writes it and
    then reports what in its replay breaks the rules, so that
    `TestConformance` reproduces a bridge bug it found.
- **Recordings.** All three log in, type, scroll and ask to resize; the
  two desktops also drag a window. Each instruction count below is from
  its manifest; `cfill` follows each `rect`, `end` closes each `img`
  and `argv` stream, and `blob` is left out.

  | Recording | Session | Instructions | Compressed |
  |-----------|---------|--------------|------------|
  | `vnc` | bench desktop, x11vnc, 1024×768; password through `required` | `args` `ready` `required` `ack`, `size` 3, `rect`/`cfill` 16, `copy` 111, `img` 130 (PNG 108, WebP 22), `cursor` 2, `sync` 22 | 275,248 bytes (398,004 raw) |
  | `rdp` | bench desktop, xrdp without the graphics pipeline at 24 bits, 640×480 resized to 800×600 | `args` `ready`, `size` 7, `rect`/`cfill` 24, `copy` 28, `img` 28 (PNG 15, WebP 13), `cursor` 1, `dispose` 2, `sync` 19 | 89,027 bytes (125,652 raw) |
  | `ssh` | demo SSH server, 800×600 resized to 640×480; password at the terminal's prompt | `args` `ready` `argv` 3, `size` 15, `move` 13, `rect`/`cfill` 22, `copy` 13, `img` 32 (PNG 31, WebP 1), `cursor` 3, `sync` 21 | 66,713 bytes (99,181 raw) |

  The corpus is 479 KB with its manifests and pinned regions. On the
  desktops, `bench/scene` gives the
  content: letters paint the probe's swatch, F3 scrolls a page of text
  (a wheel notch is turned while it does), F4 drags a 400×300 window,
  and Escape returns to the idle gradient. The terminal session types
  `echo hello`, `seq 1 200`, turns the wheel three notches back and
  forward, resizes, runs `ls -l /` and exits.
- **Bug found and fixed: WebP colours.** A first, uncommitted VNC
  recording failed its checkpoint after the drag, under the 24-level
  tolerance the checkpoints then had: five pixels were 25 levels off in
  blue. Two errors were stacked there. guacd's lossy WebP itself had
  left an artefact that a browser shows too (libwebp decodes those
  pixels 21 off), and the bridge's decoding of that WebP added to it.
  `golang.org/x/image/webp` returns a lossy image as `image.YCbCr`,
  whose model is JPEG's full-range YCbCr, while VP8 samples are BT.601
  limited-range; it also leaves chroma at half resolution where libwebp
  interpolates it. Over the 320 WebP images of that first recording the
  display was up to 38 levels a channel off dwebp; over the 36 of the
  committed corpus (VNC 22, RDP 13, SSH 1) it is up to 34.
  `internal/guacamole/webp.go` now converts 4:2:0 images itself with
  libwebp's fixed-point arithmetic and its fancy upsampling (9:3:3:1),
  and matches dwebp 1.6 exactly on all 320 and all 36.
  `TestDisplayWebPMatchesLibwebp` pins two cwebp-encoded images, one
  with alpha, to dwebp's output, and `TestClip8` the clipping at
  256<<6, where `uint8(v>>6)` would wrap to 0. In the corpus the fix is
  guarded by the second checkpoint of each desktop: with the old
  decoder 26,328 pixels of VNC's and 43,817 of RDP's are more than 6
  levels off, against an allowance of 1,400 (see Decisions), and the
  copy regions of both fail too.
- **Decisions.**
  - The recorder tees the bridge's connection, so what is recorded is
    exactly what the bridge read. The stream is cut after the last
    complete `sync`, since the bridge reads ahead, and the hash is
    pinned from a replay of the cut stream, never from the live sink.
  - Checkpoints check meaning, not just stability. The idle screen is
    known: the gradient of `bench/scene` and the swatch's colour. A
    checkpoint frame must match it within 6 levels a channel, with none
    of the swatch's pixels and at most 1,400 of the rest further off:
    a line around the 400×300 dragged window, which is the residue
    guacd leaves (see Surprises). The recordings need 0 at their first
    checkpoints and 269 (VNC) and 865 (RDP) at their second; WebP
    decoded as libwebp does keeps the smooth gradient within 4 levels
    elsewhere. The allowance no longer scales with the screen, since
    the residue scales with the window. The recorder presses Escape,
    waits for half a second of quiet, presses the letter, and takes the
    first frame from then that shows the idle screen. The pause keeps
    the swatch lossless. Letters are chosen so each changes the
    swatch's colour, since a press that changes nothing brings no frame
    over RDP.
  - Regions pin what `copy` moved, so that a broken copy fails with a
    pixel's position rather than only a hash. The recorder lists the
    frames in which a copy of at least 128×128 lands on the screen (on
    the default layer, or on a layer `move`d onto it, as the SSH
    terminal's is), keeping the 24 with the largest copies, and replays
    the stream twice: as recorded and without its `copy` instructions.
    It pins the 128×128 square, on a 32-pixel grid within a copy's
    destination, where the two replays differ most, the latest on a
    tie, and checks it exactly. Choosing by the difference matters: a
    scroll back and forth, or a copy that later drawing covers, looks
    the same without copy, and the SSH recording's last large copy
    (scrolling the terminal forward again) did not notice copy being
    disabled. With `Display.copy` disabled, each recording now fails its
    region: VNC at (194,512), RDP at (320,192), SSH at (388,278).
  - Secrets stay out of the corpus. VNC's password goes through guacd's
    `required` and the bridge's prompt, and SSH's is typed at guacd's
    terminal prompt, so neither is in a manifest; the recorder passes
    RDP's as a parameter it does not save. The streams carry only what
    guacd sent, and it echoes no password.
  - Size: the text page and the dragged window are dense images, so
    those phases are cut short (a tenth of a second of scrolling, 80 ms
    of dragging), and RDP starts at 640×480 and resizes up, since the
    idle gradient after the resize is cheap. Through the graphics
    pipeline RDP was the largest recording (586 KB), each scrolled frame
    coming as new images. Without it guacd sends moved content as
    `copy` from its copy of the previous frame (buffer -1), as it does
    for VNC, and the recording is 89 KB.
  - RDP is recorded with `disable-gfx=true` and `color-depth=24`, so
    that the desktop's pixels reach guacd without loss (see Surprises).
    24 bits, since guacd asks for 16 (`RDP_DEFAULT_DEPTH`) when the
    graphics pipeline is off and 32 only when it is on.
- **Deviations.**
  - The spec said the demo stack. The desktops are the bench stack's
    (`docker-compose.bench.yaml`), whose `bench/scene` gives content with
    a known look, and the SSH server is the demo's. The task row now
    says so.
  - The VNC recording does not resize. x11vnc on the bench desktop has a
    fixed 1024×768 Xvfb screen and ignores the client's request, so no
    `size` of the default layer follows; the bridge's `size` is still
    sent. RDP (`resize-method=display-update`) and SSH do resize.
  - No JPEG: guacd 1.6 prefers WebP for lossy updates when the client
    accepts it (`display-worker.c`), and the bridge does.
  - There is no `required` in the RDP and SSH recordings. xrdp asks for
    no NLA, so RDP takes the password as a parameter, and guacd's SSH
    support prompts inside the terminal (`guac_terminal_prompt`).
- **Surprises.**
  - guacd's RDP shifted colours before guacd encoded anything. The
    first RDP recording, with guacd's defaults, matched the idle screen
    at none of its checkpoints within 6 levels: at the first, 307,072
    of 640×480 pixels differed, 126,254 by more than 8 and at most by
    22, on average 4.9, 5.5 and 7.3 levels darker in red, green and
    blue (the gradient's centre came as (40,79,109) for (45,84,116)),
    and guacd's PNG images already held those colours. guacd 1.6
    enables the RDP graphics pipeline unless `disable-gfx` is set
    (`settings.c`), and through it xrdp 0.10.1 encodes the screen with
    RemoteFX Progressive (its log: `starting gfx rfx pro codec
    session`), which is lossy. FreeRDP decodes it in guacd, so a
    browser shows the same colours. Recorded again on the same stack
    with `disable-gfx=true` and `color-depth=24`, xrdp sends bitmaps
    (no codec in its log) and the first checkpoint matches exactly, all
    307,200 pixels; with guacd's defaults again, the live recording
    failed that checkpoint on the swatch, (0,251,12) for (0,255,0). Deployments that want exact colours over the bridge need
    `disable-gfx`, at the cost of the pipeline's bandwidth savings.
  - guacd never repaints a lossy region without loss. It encodes a
    64×64 cell as WebP or JPEG when the cell changed at three frames a
    second or more (`GUAC_DISPLAY_JPEG_FRAMERATE`) and PNG would not
    compress it better (`display-worker.c`), and it sends only pixels
    that differ, in its own lossless copy of the screen, from what it
    sent last (`display-plan.c`). So pixels that were the wallpaper
    before and after a lossy update keep that update's error. While the
    window is dragged, a WebP update holds the window and the
    wallpaper beside it, and 4:2:0 chroma, interpolated by libwebp from
    the two nearest chroma rows, carries the window's colour a quarter
    into the wallpaper's first row past its edge. The window moves on,
    but that row was the wallpaper all along and is never sent again.
    At RDP's second checkpoint, row 400 is (59,102,117) where the
    wallpaper is (55,101,134), drawn by a 448×376 WebP image during the
    drag, which dwebp decodes to the same pixels. The residue
    is one-pixel lines along the window's edges, up to 47 levels off;
    browsers show the same. Hence the residue allowance in checkpoints.
  - guacd's RDP sends no frame while the screen stands still, so the
    recorder watches every frame from the letter's press on rather than
    waiting for the next one.
  - Load on the shared host only changes how many frames a phase
    produces; the checks do not depend on it.
- **From review.**
  - The display reads an image's size from its header
    (`DecodeConfig` for its type) before decoding it, and refuses one
    wider or taller than a layer may grow (16,384) or larger than
    8192×8192 pixels in all, with `errImageSize`, so that an `img`
    stream cannot make the bridge allocate gigabytes. Before, a
    16383×16383 lossy WebP header, which `TestImageTooLarge` sends,
    would have decoded into about 1 GiB.
  - Opaque WebP images convert to `image.RGBA` rather than
    `image.NRGBA`, which `draw.Draw` composites on its RGBA path rather
    than premultiplying each pixel. `BenchmarkWebPOver` (a 448×480 4:2:0 image converted and
    drawn with OVER), ten runs each on the shared host: median 1,425 µs
    before (load average 5.2 to 5.5) and 1,385 µs after (7.0 to 7.1),
    about 3% faster, which is within the host's noise, with the same
    860 KB and 2 allocations an operation: the converted image.
- **Follow-ups.**
  - Registration decides whether the bridge passes `disable-gfx` for
    RDP by default, trading the graphics pipeline's bandwidth for exact
    colours (see Surprises).
  - Recordings are real time, so recording again pins other frames and
    other hashes. Record again when guacd's version changes.
  - JPEG decoding is unmeasured. libjpeg-turbo interpolates chroma by
    default and `image/jpeg` does not; whether browsers keep that
    default is to be checked. A recording with WebP not announced would
    give the images to compare.
  - The corpus has no clipboard, audio, telnet or Kubernetes sessions.
