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
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# guacd bridge and the path to pure Go

## Design Problem

During v1, some protocols still run on guacd: RDP until its native
backend is registered, telnet until guacd is removed, and VNC where a
connection asks for guacd as a fallback (as of 2026-10-31; the first
plan had everything at beta.1 except SSH on guacd). Their sessions must
reach v1 clients through protocol v1. Two things need deciding:

- **How guacd's Guacamole instruction stream becomes protocol v1.** This
  determines whether clients need to know Guacamole at all.
- **The parity gate.** When does a native backend replace the bridge for
  a protocol? These gates are the concrete path to "no guacd".
  (Answered by the owner on 2026-10-31: native is the default as soon
  as it exists, and the gate decides when a protocol's guacd path is
  deleted; see Parity gate.)

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

guacd's SSH renders terminals as pixels, so a bridged SSH session would
be graphical. In v1 there is none: ssh is native from the start, the
configuration refuses `backend: guacd` for it, and the bridge never
serves it.

## Parity gate (bridge to native, per protocol)

**The owner's decisions, 2026-10-31.** They override this spec where
they differ:

- The goal is to remove Guacamole entirely: no guacd and no Guacamole
  protocol in v1.0.
- guacd is kept, as an opt-in fallback and as the benchmark baseline,
  until there is clear benchmark evidence that native outperforms it
  10x or more. Then it is deleted, protocol by protocol.
- So native must be the default wherever a native backend exists. guacd
  is never the default for a protocol that has one.
- Telnet gets no native backend: v1.0 doesn't need telnet. It stays
  guacd-only for now, and is dropped when guacd is removed in v1.0.

The gate therefore no longer decides when a protocol's default moves to
`native`; it decides when the protocol's guacd path is deleted:

1. **Default:** native is the default as soon as a native backend
   exists, with no opt-in period before it.
2. **Fallback:** guacd stays only as an explicit fallback,
   `backend: guacd` on a connection or in `adhoc_backends`, and as the
   benchmark's baseline.
3. **Deletion:** deleting a protocol's guacd path requires the
   benchmark to show native at least 10x better than the guacd
   baseline on each scenario's primary metric, per
   [benchmark](benchmark.md) and
   [docs/benchmarks.md](../../docs/benchmarks.md), plus the parity
   matrix: the [006](../006-demo-stack-refresh.md) matrix passes for
   authentication modes, resize, clipboard in both directions, cursor,
   sharing roles, reconnect, and keyboard input including AltGr and
   dead keys.

Superseded by these decisions: the gate as first written, which moved a
protocol's default to `native` only after an opt-in period of at least
one beta behind `backend: native`, end-to-end parity, traffic and CPU
no worse than the bridge's and a soak, and then kept `backend: guacd`
for one minor release. Instead of that minor release, `backend: guacd`
now stays until the benchmark shows native at least 10x better, and
the protocol's guacd path is then deleted. The soak (Decision 4) is not
one of the owner's conditions.

| Protocol | Native backend | Default | guacd path |
|----------|----------------|---------|------------|
| SSH | [native-ssh-backend](native-ssh-backend.md) | `native`, from the start of v1 | none: `backend: guacd` is refused |
| VNC | [native-vnc-backend](native-vnc-backend.md) | `native`, since 2026-10-31 | `backend: guacd`, an explicit fallback and the benchmark's baseline, until the deletion gate passes |
| RDP | [native-rdp-backend](native-rdp-backend.md), per [rdp-strategy](rdp-strategy.md) | `guacd` until the native RDP backend is registered, once NLA has run end to end; `native` from then on | the only path until then; afterwards an explicit fallback until the deletion gate passes |
| telnet | none: v1.0 doesn't need telnet (owner, 2026-10-31) | `guacd`, for now | the only path; telnet is dropped when guacd is removed in v1.0 |
| Kubernetes | none planned | not registered (Implementation notes: registration) | none |

Once native RDP is registered, guacd is the default for telnet alone,
and it runs otherwise only for connections that opt into the fallback
and for the benchmark. The bridge is deleted with the last guacd path,
and v1.0 ships without it (Decision 5).

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
4. Gate values: N, H, and the performance tolerance? Answered by
   Decision 4, whose tolerance the owner's 10x gate superseded on
   2026-10-31 (see Parity gate).
5. ~~After RDP goes native: keep the bridge, or delete it?~~ Answered
   2026-10-30: v1.0 ships without guacd
   ([008](../008-native-architecture.md), Path to pure Go), so the bridge
   is removed with it. It carries VNC and RDP over protocol v1 only until
   their native backends pass its parity gate. Since 2026-10-31 it does
   so as an explicit fallback once a protocol has a native backend, not
   as the default (see Parity gate).

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
   **Superseded on 2026-10-31** by the owner's decisions (see Parity
   gate): no performance condition comes before a native default, and
   a protocol's guacd path is deleted only when native is at least 10x
   better than the guacd baseline on each scenario's primary metric,
   so the 10x target is now the gate and the 10% tolerance decides
   nothing. The soak values are not one of the owner's conditions; the
   soak runs as a check for leaks in `occamyd` (Implementation notes:
   VNC's soak).
5. **The bridge is removed at v1.0** (open question 5, answered above).

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Interpreter additions | `internal/guacamole`: the `cursor` instruction (hotspot and image from a layer region) exposed as a cursor change, and access to the composited display by rectangle for copying out | — |
| Bridge backend | `internal/backend/guacd`: a `Backend` of class graphical over `internal/guacd`. Instructions apply to the display; at each `sync` the damage is copied into the sink (`Lock` and `Unlock` per damaged rectangle) and the `sync` is answered, so guacd paces against the bridge alone. `size` of the default layer becomes `Resize`, `cursor` becomes `Cursor`, `name` becomes `Title`, clipboard streams become `Clipboard`, `required` becomes a `Prompt` whose answers go back as `argv` streams, `error` ends `Run` with the status, and `disconnect` ends it cleanly. Input translates to `key`, `mouse` (button mask, and wheel as buttons 4 and 5 pressed and released), keysyms for `Text`, a `clipboard` stream, and `size`. Writes to guacd are serialised. Tests against a scripted guacd on `net.Pipe` | Interpreter additions; session-core Backend interface |
| Conformance corpus | Instruction streams recorded from guacd 1.6's VNC, RDP and SSH plugins on the bench stack (the bench desktop over VNC and RDP, the demo SSH server; logging in, typing, scrolling, moving a window, resizing), replayed through the bridge in tests: no unsupported operation, no error, the idle screen at checkpoints, and the final frame's hash pinned | Bridge backend |
| Registration | The bridge registered in the API's backend factory for `vnc` and `rdp`, and for `telnet` and `kubernetes` behind an opt-in, with guacd parameters from the configuration under the connection's (built for `rdp` and `telnet`; see Implementation notes: registration) | Bridge backend; API Sessions |

The parity gate runs per protocol, when a protocol's guacd path is to
be deleted, not as a task here.

## Affects

New `internal/backend/guacd` on top of `internal/guacd`. Decides whether
[client-core](client-core.md), [009](../009-react-web-client.md) and
[010](../010-native-swift-client.md) need any Guacamole code (under I they
do not).

## Implementation notes: bridge backend

- **Status.** Interpreter additions and Bridge backend are done
  (#94). The conformance corpus followed (#104, see its notes below);
  registration has not, so the spec stays validated. (2026-10-30: it
  has since; see Implementation notes: registration.)
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
  - The conformance corpus decides whether `Unsupported` stays empty on
    real VNC, RDP and SSH sessions: it does on all three recordings.

## Implementation notes: conformance corpus

- **Status.** The conformance corpus is done (#104). Registration is
  the last task, so the spec stays validated. (2026-10-30: see
  Implementation notes: registration.)
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
    colours (see Surprises). Decided: it does, unless
    `guacd.params.rdp` sets it.
  - Recordings are real time, so recording again pins other frames and
    other hashes. Record again when guacd's version changes.
  - JPEG decoding is unmeasured. libjpeg-turbo interpolates chroma by
    default and `image/jpeg` does not; whether browsers keep that
    default is to be checked. A recording with WebP not announced would
    give the images to compare.
  - The corpus has no clipboard, audio, telnet or Kubernetes sessions.

## Implementation notes: registration

- **Status.** Done by the Sessions task of
  [api-and-server-stack](api-and-server-stack.md), except the bridge for
  VNC and for Kubernetes, below. Every task of the breakdown is built;
  the parity gate runs per protocol, not as a task.
- **What was done.** The API's backend factory makes the bridge for
  `rdp` and `telnet` connections and ad-hoc targets, at guacd's
  `guacd.address`, with `guacd.params.<protocol>` under the target's
  `hostname` and `port`, the connection's user name and its stored
  password. RDP gets `disable-gfx: "true"` unless the configuration sets
  it (see the follow-up above). The owner's `hello` sizes the screen,
  each side capped at 8192.
- **Deviations.**
  - `vnc` went to the native backend by default, ahead of the parity
    gate's opt-in period, and no configuration key (`backend:`) picked
    the bridge for it, so a v1 file could not serve VNC through guacd.
    Resolved: see Implementation notes: the opt-in period. Since
    2026-10-31, by the owner's decisions (Parity gate), native is
    `vnc`'s default again, now with `backend: guacd` as the fallback
    (Implementation notes: native by default).
  - telnet needs no opt-in beyond a configured connection, or an ad-hoc
    target where those are allowed. It stays on the bridge alone and is
    dropped when guacd is removed in v1.0.
  - Kubernetes is not registered: connections have `host:port` targets
    only, and Kubernetes needs a cluster, a namespace and a pod.
- **Follow-ups.** ~~The `backend:` key of the parity gate, so that VNC
  can go through the bridge and back~~ (done, below); Kubernetes targets.

## Implementation notes: the opt-in period

**Superseded on 2026-10-31 by the owner's decisions** (Parity gate):
native is the default as soon as a native backend exists, so VNC's
opt-in period ended without the steps it waited for, and the bridge
serves `vnc` only as an explicit fallback (Implementation notes: native
by default). What the opt-in period built stays; where these notes said
that guacd was `vnc`'s default, they are corrected below.

- **Status.** VNC's opt-in period (the first gate's step 1) ran from the
  `backend:` key's introduction until 2026-10-31: the bridge served
  `vnc` by default then, and `backend: native` opted a connection in.
  SSH had no opt-in period: it was native from the start, and the bridge
  does not serve it.
- **What was done.**
  - Configuration v1 has `backend` on each connection, `guacd` or
    `native`, which `ParseV1` fills with the protocol's default: `native`
    for ssh, and, since 2026-10-31, for vnc; `guacd` for rdp and telnet.
    `adhoc_backends` does the same for ad-hoc targets, by protocol. ssh
    refuses `guacd`; rdp refuses `native` until the native RDP backend
    has run NLA end to end ([rdp-strategy](rdp-strategy.md)); telnet has
    no native backend.
  - The backend factory in `server/backends.go` is keyed by protocol and
    backend: ssh/native, vnc/native, and vnc, rdp and telnet on guacd.
    The session's state names the backend as before (`ssh`, `vnc`,
    `guacd`).
  - A vnc connection's `tls` needs the native backend, since the bridge
    would not check the certificate as `tls` says; `ard` too
    ([native-vnc-backend](native-vnc-backend.md), phase 4). Both are
    refused with `backend: guacd`.
  - The [006](../006-demo-stack-refresh.md) stack runs `occamy-v1` with
    the demo VNC server as `vnc-native` and `vnc-guacd`, and `make e2e`
    streams both with the Go client (`tests/v1_test.go`); occamyd's own
    tests drive native VNC through the routes against
    `internal/backend/vnc/vnctest` (`server/vnc_e2e_test.go`).
- **What the matrix covers so far.** Against x11vnc in the demo stack:
  VNC Authentication with a stored password, the desktop arriving, and
  pointer and key input acknowledged, on both backends. Against the
  fake server: stored and prompted credentials, a refused password,
  the screen, key, pointer and text input, resize by the hello and by
  the client, clipboard both ways, the cursor, visibility, the end
  reasons, the `tls` policy and an ad-hoc target. Not yet: sharing
  roles, reconnect, AltGr and dead keys, which the parity matrix has
  since covered on x11vnc and TigerVNC (Implementation notes: VNC's
  parity matrix).
- **Follow-ups.** Moved to Implementation notes: native by default. The
  first gate's steps 3 and 4 are superseded, and so is the minor release
  that would have kept `backend: guacd` after a switch: `backend: guacd`
  stays until the benchmark shows native at least 10x better, and VNC's
  guacd path is then deleted.

## Implementation notes: native by default

- **Status.** Done on 2026-10-31, by the owner's decisions (Parity
  gate): `vnc` defaults to the native backend, and `backend: guacd` is
  an opt-in fallback and the benchmark's baseline.
- **What was done.**
  - `config.DefaultBackend` returns `native` for ssh and vnc and
    `guacd` for rdp and telnet, so a vnc connection without `backend`,
    and an ad-hoc vnc target without `adhoc_backends`, get the native
    backend. `backend: guacd` stays valid for vnc, and
    `adhoc_backends: {vnc: guacd}` for ad-hoc targets.
  - `tls` and `ard` still need the native backend; their refusals now
    name the backend that was set (`backend: guacd would not: leave
    backend out, or set native`), since a connection without `backend`
    no longer reaches them.
  - The demo stack's `vnc-native` leaves `backend` out, so `make e2e`
    streams vnc's default, and `vnc-guacd` sets `backend: guacd` for the
    baseline. `TestDemoV1File` checks both.
  - Tests: `TestParseV1Backends` (the default for connections and
    ad-hoc targets, explicit `guacd`, `tls` and `ard` without
    `backend`), `TestParseV1Problems` (`tls` and `ard` with
    `backend: guacd` refused) and `TestBackendDefaults`. Flipping vnc's
    default back to `guacd` fails all three, `TestParseV1Example` and
    `TestDemoV1File`.
- **Follow-ups.** The deletion gate for VNC: ~~the rest of the parity
  matrix on real servers (TigerVNC's ExtendedDesktopSize and clipboard,
  sharing roles, reconnect, keyboard layouts)~~ (done: Implementation
  notes: VNC's parity matrix), and the benchmark comparing native VNC
  with the guacd baseline on each scenario's primary metric. RDP
  defaults to `native` when the native RDP backend is registered.

## Implementation notes: VNC's parity matrix

- **Status.** The parity matrix, one of the two conditions for
  deleting VNC's guacd path (Parity gate, 3), passes: on x11vnc and
  TigerVNC, the native backend passes every cell that guacd passes, and
  three that guacd fails. No native failure was found, so nothing was
  fixed. The other condition, the benchmark showing native at least 10x
  better than the guacd baseline, remains.
- **How it runs.** `tests/parity_test.go` (`TestVNCParity`) drives
  `occamy-v1` in the [006](../006-demo-stack-refresh.md) stack with the
  Go client core, over API v1 and protocol v1, through a connection for
  each server, security type and keyboard layout, once on the native
  backend and once with `backend: guacd` (006, Addendum:
  TigerVNC and the VNC parity matrix). `make e2e` runs it, so CI's
  `e2e` job does. The servers' desktop reports what reached the server
  over the session itself: a line typed into a probe at the top left
  comes back on the clipboard as its UTF-8 bytes in hex, and clipboard
  text `owner: …` comes back as `echo: …`, so each check sees the
  server's side, not just the backend's. Before it types, a check
  waits 10 s for the echo of a fresh nonce, once, so that nothing sent
  before is still in flight. A freshly started x11vnc drops the first
  clipboard text on its port (Findings), so before the matrix the test
  warms each port of both servers: through the native backend, it
  repeats the nonce until the probe answers, and logs how many sessions
  and nonces each port took.
- **How a cell is judged.** A cell expected to fail is listed in
  `parityKnown` with the reason and a part of the error that only that
  failure gives: `"? ? ??"` for x11vnc's Latin-1, `want "€ ✓ 日本"` for
  guacd's, and `backend-failed` for guacd's X509. Any other failure
  fails the test, and so does a listed cell that fails without that
  part, or passes, so the table below is what the suite finds;
  `TestParityMatrixDoc` checks that it is the table that
  `parityFeatures` and `parityKnown` give. A cell tries up to three
  times, and before it tries again, waits 15 s and warms the server's
  ports through the native backend. guacd is the baseline, so a guacd
  cell tries again after any failure but its listed one. A native cell
  that cannot reach the desktop, whose screen does not arrive or whose
  probe does not answer, runs the same check through guacd on the same
  server at once, and tries again only if guacd cannot reach the
  desktop either, which is how a hung server looks. If guacd reaches
  the desktop, the native failure stands, and so does a native failure
  that found a difference. A cell that
  passes, or fails as listed, at a later try shows that try, as in
  `pass (try 2)`, so a retried cell does not read as a plain pass. One
  cell is lenient to native: guacd cannot give X509 credentials, so the
  X509Plain cell's guacd twin never reaches the desktop, and the native
  cell tries again whenever it cannot. `TestParityCell` drives this
  logic with fake checks.
- **The matrix.** x11vnc 0.9.17 (libvncserver 0.9.15) and TigerVNC
  1.15's Xvnc, Debian trixie; guacd 1.6. On the shared host, at a load
  average of 7 to 18, the matrix as first written, with a barrier that
  tried four times and no warm-up, took 192 s on fresh servers and
  124 to 128 s on warm ones. Every run gave the table below. The runs
  that failed cells did so on server faults (next items), or, once, on
  the first version of the x11vnc watchdog, which killed a live x11vnc.
  A whole run with the warm-up, the single-shot barrier and the guacd
  twin of a native cell (How a cell is judged) is pending a local run;
  its time, and whether it gives this table at every cell's first try,
  are not yet recorded. CI runs it in `make e2e`.

  | Feature | x11vnc/native | x11vnc/guacd | tigervnc/native | tigervnc/guacd |
  |---|---|---|---|---|
  | auth: None | pass | pass | pass | pass |
  | auth: VNC Authentication, stored password | pass | pass | pass | pass |
  | auth: VNC Authentication, asked of the owner | — | — | pass | pass |
  | auth: VNC Authentication, wrong answer refused | — | — | pass | pass |
  | auth: VeNCrypt X509Plain | — | — | pass | fail: guacd: no X509 credentials |
  | screen and input acknowledged | pass | pass | pass | pass |
  | resize: by the owner's hello | — | — | pass | pass |
  | resize: by a request | — | — | pass | pass |
  | resize: ignored without ExtendedDesktopSize | pass | pass | — | — |
  | clipboard to the server, Latin-1 | pass | pass | pass | pass |
  | clipboard to the server, beyond Latin-1 | fail: x11vnc: Latin-1 only | fail: x11vnc: Latin-1 only | pass | fail: guacd: Latin-1 only |
  | clipboard both ways, Latin-1 | pass | pass | pass | pass |
  | clipboard both ways, beyond Latin-1 | fail: x11vnc: Latin-1 only | fail: x11vnc: Latin-1 only | pass | fail: guacd: Latin-1 only |
  | cursor | pass | pass | pass | pass |
  | keyboard: us | pass | pass | pass | pass |
  | keyboard: de, keysyms and codes | pass | pass | pass | pass |
  | keyboard: de, keysyms only | pass | pass | pass | pass |
  | keyboard: fr, keysyms and codes | pass | pass | pass | pass |
  | keyboard: fr, keysyms only | pass | pass | pass | pass |
  | keyboard: de client on a us server | pass | pass | pass | pass |
  | text input | pass | pass | pass | pass |
  | sharing: a viewer's input | pass | pass | pass | pass |
  | sharing: a read-only viewer's input dropped | pass | pass | pass | pass |
  | reconnect: the owner rejoins occamyd's session | pass | pass | pass | pass |

  A dash is a cell not run: x11vnc offers no VeNCrypt and no
  ExtendedDesktopSize, and the password asked of the owner, which is
  occamyd's and the backends' rather than the server's, runs against
  TigerVNC only. TigerVNC has ExtendedDesktopSize, so it has no row for
  its absence.
- **What the rows check.**
  - auth: the session reaches the screen with the session's state
    naming the backend, and a typed line arrives. The password asked of
    the owner must be asked: a screen without the prompt fails. A wrong
    answer to the prompt ends the session as `backend-failed`.
  - screen: 1024×768, not of one colour, and a frame acknowledging a
    key.
  - resize: TigerVNC takes 900×700 from the owner's `hello` and
    800×600 and back from a request; x11vnc keeps 1024×768.
  - clipboard: "naïve café, ½ °C" and "€ ✓ 日本" reach the server's
    clipboard byte for byte, and "naïve café\nzweite Zeile" and
    "€ ✓ 日本" come back to the client.
  - cursor: the root window's cursor and the xterm's differ, and each
    change arrives with its hot spot inside the image. The xterm's
    cursor is the last to have arrived once a nonce, sent after the
    pointer moved onto the xterm, has come back: the server sends the
    cursor as it takes the move, before the probe even reads the
    clipboard.
  - keyboard: keys as a browser on that layout reports them (the keysym
    produced, the key's code, AltGr and Shift held where the layout
    needs them, and a dead key as a code with no keysym followed by the
    composed character) type `Hello, World! @~123`, `zy@€{üöäß"ôé^` and
    `aqzwéèçà@€#êë` on the screen with that layout. "keysyms only"
    drops the codes, as a client that knows no physical keys sends them.
  - text input: "日本 ✓ naïve" sent as `Text`.
  - sharing: a viewer's typed line arrives and the session lists both
    roles; a read-only viewer is refused by the client, and its pointer,
    key and clipboard messages, written past the client, reach nothing
    while the owner's line comes through alone.
  - reconnect: the owner closes the stream and rejoins occamyd's
    session by its id within the grace period, at the same size, and
    types. The check cannot tell whether the backend kept its
    connection to the server through the grace period or made another;
    it shows that occamyd's session, not the VNC connection, carries
    on.
- **Findings.**
  - Clipboard beyond Latin-1: RFB's cut text is Latin-1. x11vnc has no
    Extended Clipboard, so "€ ✓ 日本" arrives as "? ? ??" through either
    backend, and comes back so. guacd 1.6 does not ask TigerVNC for
    the Extended Clipboard either, so only the native backend carries
    UTF-8 there.
  - VeNCrypt X509Plain through guacd: libvncclient asks for X509
    credentials, and guacd 1.6 gives only a username and password; it
    logs "Unsupported credential type requested" and ends the session
    (status 519). guacd would also check no certificate, which is why a
    vnc connection's `tls` needs `backend: native`.
  - Keyboard: x11vnc offers no QEMU key events, so both backends send it
    the same KeyEvents, keysyms only; a dead key, which has no keysym,
    is dropped by both, and the composed character that follows arrives
    as its keysym. x11vnc adds a keysym missing from the layout to a
    spare key, and after `setxkbmap` drops that key still presses it,
    typing nothing; the screens therefore keep their layouts, one per
    port. The native backend sends TigerVNC the codes as QEMU key
    events; without `RawKeyboard`, TigerVNC maps the keysyms to its own
    layout, so a German client types German text on its us screen too.
  - A freshly started x11vnc drops the first cut text sent to it,
    whichever backend sends it; the next comes back in about 5 s, and
    every session's first after that within a second or two, through
    either backend. On fresh servers, with two sessions of two nonces
    each per port and backend, the native backend's first nonce on
    each of the four x11vnc ports was lost and guacd's sessions after
    it lost none; after a restart of the x11vnc container, with guacd
    first, guacd's first nonce on each port was lost and the native
    backend's lost none. TigerVNC lost none. It is x11vnc's start, not
    a backend: the barrier no longer tries four times, which hid that,
    and the test warms each port before the matrix and before a retry,
    and logs what each port took.
  - Once in six whole runs, a guacd session to TigerVNC ended as it
    connected: libvncclient logged "Error handling message from VNC
    server", and guacd ended with status 515. The next check on the
    same port passed; hence the second try for guacd cells.
  - In CI, x11vnc twice closed guacd's connection to it as guacd
    connected for a viewer's check, logging "unknown message type 53",
    then 48: it read bytes that guacd sent as the start of a message,
    and guacd ended with status 515. The cell failed both its tries, so
    a guacd cell now has three, as a cell that cannot reach the desktop
    has. The cause, guacd's or libvncserver's, is not known; the native
    backend's cells on the same server passed.
  - x11vnc hangs for good now and then as a client connects, through
    either backend: it logs "caught X11 error:" with no detail, drops
    its polling images on its way to exit and stops in a futex wait,
    leaving connections unaccepted, so every later cell on its port
    fails. With MIT-SHM, that happened once in six whole runs, then four
    times in about two runs of the x11vnc columns, on a Mac. Without it
    (`-noshm`), fifteen runs of the x11vnc columns on the Mac had no X
    error, but the first run in CI had three, all in guacd cells as
    guacd connected. The demo x11vnc now logs the failed request
    (`X11VNC_PRINT_XERROR`), and runs under a watchdog that kills one
    that reports an X error or leaves a connection unaccepted for 26 s,
    and starts it again, backing off up to 16 s while it keeps exiting
    ([006](../006-demo-stack-refresh.md), Addendum: TigerVNC and the
    VNC parity matrix). x11vnc serves its clients in one thread, and
    waits up to its `-readtimeout` (LibVNCServer's `rfbMaxClientWait`,
    which `-rfbwait` also sets), 20 s by default, for a client that does
    not take what it sends, accepting nobody meanwhile; a first
    watchdog that waited 10 s could have killed a live x11vnc so. In
    that CI run, a cell's second try met the hung x11vnc that its first
    try had found, before the watchdog had killed it; hence up to three
    tries. It is a fault of the server, not of either backend.
  - Read-only input is dropped by the session, before any backend, so
    the row cannot differ between backends; it is in the matrix because
    the gate names sharing roles.
- **Not covered.** Pointer buttons and the wheel (pointer moves are
  covered, through the cursor and the typing probe's focus); X509Vnc on
  its own and the `tls` policy against a real server, which
  `TestTigerVNC` covers; Apple Remote Desktop, checked by hand against
  macOS; QEMU's VNC server.

## Implementation notes: VNC's soak

- **Status.** A short soak of `occamyd` serving VNC passed: 8 sessions
  at a time for 30 minutes, with no growth in goroutines, live heap or
  open files. The owner's decisions of 2026-10-31 took the soak out of
  the conditions for deleting a guacd path (Parity gate), so it decides
  nothing there; it looks for leaks in `occamyd`. Decision 4's values
  make the full run, 20 sessions (N) for 8 hours (H), which has not
  been done.
- **How it runs.** `tests/soak_test.go` (`TestSoak`), through
  `make soak` against the [006](../006-demo-stack-refresh.md) stack
  started with `make run OCCAMY_DEBUG=1`: sessions to `tigervnc-native`
  by default, with the Go client core over API v1 and protocol v1.
  x11vnc hangs now and then (Implementation notes: VNC's parity
  matrix), and each restart of it by its watchdog would end the
  sessions on it and fail a long soak, so `vnc-native` is not in the
  default; `OCCAMY_SOAK_CONNECTIONS` takes it, or any of `occamy-v1`'s
  connections, in turn. Each session moves the
  pointer, below the parity matrix's typing probe so that the letters do
  not reach it, and types a letter every 2 s, presses Return every 20 s
  and, once a minute, asks its desktop, over the clipboard, for a
  terminal that scrolls for 2 s (`desktop.sh`'s `motion:`, which takes
  1 to 60 s and keeps at most four such terminals at once). Every other
  session has a read-only viewer, by a share. A session closes after 10
  minutes and another opens; the first ones close at staggered times, so
  that sessions turn over all through the run. Every 30 s the test asks
  the debug listener for a heap profile with `gc=1`, which collects,
  then reads `/debug/vars`: `goroutines` and `fds`, which `occamyd`
  publishes for this, and `memstats.HeapAlloc`, the live heap after that
  collection. A sample that fails is tried again twice, 5 s apart,
  before the soak gives up. It logs each sample with the host's load
  average, and writes them as CSV to `OCCAMY_SOAK_OUT`. The debug
  listener has no authentication: `occamy-v1` opens it only when
  `OCCAMY_V1_DEBUG_ADDRESS` is set (`debug.address_env`), which
  `make run OCCAMY_DEBUG=1` does, and the stack publishes it on the
  host's loopback interface only, though its other services can reach
  it while it is on. `OCCAMY_DEBUG=1` also mounts the v0 service's
  file in debug mode (`occamy-debug.yaml`) for the pressure test; the
  v0 service is now published on the loopback interface only, and in
  release mode without it.
- **What fails it.** After a warm-up of a tenth of the run, a metric
  grew if the median of the last quarter of its samples exceeds the
  first quarter's by more than a bound, and no quarter's median falls
  below the first's: 50 goroutines, 32 MiB of heap and 25 file
  descriptors. Medians, so that a sample taken while sessions open or
  close does not decide. A dip between the first quarter and the last
  does not hide growth, as a rule that wanted every median to rise on
  the one before did (medians of 100, 200, 199 and 400 grew); a
  quarter below the first says that the first was high. Fewer than
  eight samples after the warm-up fail the run, and a soak too short
  for eight is refused before it starts. Once every session has closed,
  the goroutines, the heap and the files must come back to within the
  bound of where they started, within a minute. Any session that fails
  fails the run. `TestSoakGrowth` checks the judgement on made-up
  series, at the bound, with plateaus, low outliers, eight samples,
  early growth and a dip, and `TestSoakAfter`, `TestSoakRetry` and
  `TestSoakConfig` the rest. The bounds
  suit the full run, about a thousand sessions, in which a leak of one
  goroutine or one descriptor per session exceeds them many times over;
  the short run, about thirty sessions, catches a leaked descriptor per
  session but only two or more goroutines.
- **The run.** On 2026-10-31, `make soak` with 8 sessions for 30
  minutes, against the stack under its own project name on the shared
  host, at a one-minute load average of 8 to 46 (median 17): 31 sessions
  opened, 123,897 frames and 21,275 inputs, and no session failed. After
  the warm-up, `occamyd` held 83 to 85 goroutines, 69.8 to 70.9 MiB of
  live heap and 30 or 31 open files, with 8 sessions and 4 viewers
  streaming; the quarters' medians were 84 goroutines and 30 files
  throughout, and 70.1, 70.2, 70.4 and 70.5 MiB of heap, a rise far
  inside the bound. Before the first session it had 8 goroutines, 1.6
  MiB and 10 files, and once the last had closed 9, 2.1 MiB and 11.
  That run took `vnc-native` and `tigervnc-native` in turn, and judged
  growth by the earlier rule, with the heap left out of the check once
  the sessions had closed; its samples pass the present rule and check
  too.
- **Not covered.** guacd's sessions, whose leaks would be guacd's, and
  native RDP and SSH sessions; `OCCAMY_SOAK_CONNECTIONS` takes any of
  `occamy-v1`'s connections, and the stack has none for RDP or SSH
  through API v1 yet. Clipboard text larger than a line, and the
  reconnects that the parity matrix covers. Each session ends by its
  owner deleting it, so the path on which the owner leaves and the
  owner grace runs out, which ends a session from a timer, is not
  exercised.
