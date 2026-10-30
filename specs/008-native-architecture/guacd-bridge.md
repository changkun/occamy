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
  `image/png` and `image/jpeg` cover the rest, all without cgo.
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
   conformance corpus fails on any. Transfer functions other than `SRC`
   and `OVER` are rejected loudly, as proposed.
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
| Conformance corpus | Instruction streams recorded from guacd 1.6's VNC, RDP and SSH plugins on the demo stack (logging in, typing, scrolling, moving a window, resizing), replayed through the bridge in tests: no unsupported operation, no error, and the final frame's hash pinned | Bridge backend |
| Registration | The bridge registered in the API's backend factory for `vnc` and `rdp`, and for `telnet` and `kubernetes` behind an opt-in, with guacd parameters from the configuration under the connection's | Bridge backend; API Sessions |

The soak and performance gates run per protocol when a native backend
asks to replace the bridge, not as tasks here.

## Affects

New `internal/backend/guacd` on top of `internal/guacd`. Decides whether
[client-core](client-core.md), [009](../009-react-web-client.md) and
[010](../010-native-swift-client.md) need any Guacamole code (under I they
do not).
