---
title: Session core and backend interface
status: drafted
track: architecture
depends_on: []
affects:
  - internal/backend/
  - internal/session/
  - server/
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Session core and backend interface

## Design Problem

Define how a backend connection becomes a shared session:

- the Go interface every backend implements (native SSH, native VNC, the
  guacd bridge, and possibly out-of-process RDP);
- the goroutine that owns it;
- how any number of viewers attach with different roles and speeds;
- how a late joiner gets a consistent picture;
- how sessions end.

The core replaces v0's mutex-guarded session map, whose failure modes
[003](../003-session-lifecycle-hardening.md) catalogues.

## Context

- v0: `server/routers.go` `routeConn` keys sessions by credential hash.
  The guacd switch ([guacd-sessions](../005-upstream-guacamole/guacd-sessions.md))
  keeps that key but stops holding the lock across I/O, and removes an
  entry when its last user leaves.
- 003 lists the v0 defects: lock held across joins (fixed), racing
  teardown, joining a dying session, unbounded user counts, no pressure
  test in CI.
- Protocol v1 ([protocol-v1](protocol-v1.md)) gives each viewer a frame
  window and acks.

## Proposed shape

```go
package backend

type Class int // Terminal or Graphical

type Backend interface {
    Class() Class
    // Run connects and blocks until ctx ends or the remote closes. Events go
    // to sink; Run must not block on a slow sink.
    Run(ctx context.Context, sink Sink) error
    Input(ev InputEvent) error // key, pointer, wheel, text, clipboard, resize
}

type Sink interface {
    Resize(w, h int)
    Damage(r image.Rectangle)          // graphical: the framebuffer changed
    CopyRect(dst image.Rectangle, src image.Point)
    Cursor(img *image.RGBA, hot image.Point)
    TermData(p []byte)                 // terminal
    Clipboard(mime string, data []byte)
    Bell()
    Prompt(p Prompt) (PromptReply, error)
}
```

Graphical backends write pixels into a framebuffer that the session owns
([graphics-pipeline](graphics-pipeline.md)) and report damage. They never
encode.

The session owner is one goroutine per session. It runs the backend,
applies events to shared state (the framebuffer, or the terminal state
plus a scrollback snapshot), serializes input from viewers that hold
input rights, and enforces roles (owner, viewer, read-only). It starts a
writer goroutine per viewer.

## Options

### Viewer pacing

- **A. Pull model with per-viewer dirty regions (recommended).** Each
  viewer keeps a dirty region (a set of tiles) that the owner ORs damage
  into. A viewer's writer, whenever its frame window has room, swaps out
  the region, encodes those tiles from the current framebuffer, and sends
  one frame. A slow viewer naturally gets fewer, coalesced frames and
  never queues stale pixels. This is the VNC update model.
  - Pros: bounded memory per viewer; no frame drops needed; fairness by
    construction.
  - Cons: encoding is per viewer unless cached (below); reading the
    framebuffer needs a lock or tile versioning.
- **B. Push model with bounded per-viewer queues.** The owner encodes once
  per frame and pushes it to every viewer, dropping or merging frames
  when a queue is full.
  - Pros: one encode per frame.
  - Cons: merging encoded frames is hard; dropping frames loses damage
    unless a full refresh follows; memory scales with queue depth.

Encoding cost under A: cache the encoded tile by (tile index, tile
version, codec, quality). Viewers at the same quality share encodes, so A
costs about the same as B in the common case.

### Framebuffer concurrency

- **A. Per-tile version counters plus an RWMutex per tile row.** Writers
  (the backend) take write locks on rows; readers (viewer encoders) take
  read locks and record versions.
- **B. Double buffering with a snapshot per frame.** Simple, but copies
  the framebuffer for every frame.

### Out-of-process backends

The [rdp-strategy](rdp-strategy.md) helper options need the backend
interface over a socket. Either define a small IPC encoding of
`Sink` and `InputEvent` now, so the interface is serializable from the
start, or wait until an out-of-process backend exists. Pixel data would
travel through shared memory (`memfd` on Linux) to avoid copies.

## Open Questions

1. Is pacing model A agreed, including the encoded-tile cache?
2. Framebuffer concurrency A or B? The answer depends on the graphics
   pipeline's tile size.
3. Late join for terminals: keep a headless terminal emulator per session
   (an exact snapshot) or replay a bounded byte log since the last
   full-screen clear (approximate)? See
   [native-ssh-backend](native-ssh-backend.md).
4. Input arbitration: may several viewers send input at once (as v0
   does), or does input require an explicit grant from the owner?
5. Lifecycle: does the session end when the owner leaves, or when the
   last viewer leaves? Idle timeout default? Reconnect grace period for a
   dropped owner?
6. Recording: a sink tap in the core (asciicast v2 for terminals, a frame
   log for graphics), or out of scope for beta?
7. Should IPC for out-of-process backends be defined now (serializable
   interface) or later?

## Affects

New `internal/backend` (interface, event types) and `internal/session`
(owner, viewer, registry), with the HTTP layer from
[api-and-server-stack](api-and-server-stack.md) on top. 003's remaining
defects are resolved by this design rather than patched on v0.
