---
title: Session core and backend interface
status: validated
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
  Since the guacd switch in v0.5.0
  ([guacd-sessions](../005-upstream-guacamole/guacd-sessions.md)), it no
  longer holds the lock across I/O, and it removes an entry when its last
  user leaves.
- 003 lists the v0 defects. The lock held across joins, racing teardown
  and a failed connect sticking its claims are fixed. Still open on v0:
  a client can join a session as its last user leaves (guacd answers
  516), the table has no concurrent stress test, and the pressure test
  does not run in CI.
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

Each viewer's writer has two queues: small messages and tiles. It always
drains the small queue first, so a cursor move, terminal data or a
keystroke's update never waits behind bulk tiles. On Linux and macOS it
sets `TCP_NOTSENT_LOWAT` on the connection, so that the kernel holds
little unsent data and the writer's ordering still takes effect.
[benchmark](benchmark.md)'s G5 measures this.

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

## Decisions

Settled on 2026-10-29. Items 4 and 5 were the owner's call; the rest
follow the recommendations above.

1. **Pacing: A,** with the encoded-tile cache keyed by tile index, tile
   version, codec and quality.
2. **Framebuffer concurrency: A,** per-tile version counters and an
   RWMutex per tile row. The row height follows the tile size that the
   [graphics-pipeline](graphics-pipeline.md) benchmark picks.
3. **Late join for terminals:** decided in
   [native-ssh-backend](native-ssh-backend.md). The core only requires a
   snapshot that the backend can produce on demand.
4. **Input: the share role decides.** The owner and every `viewer` may
   send input at the same time, as v0 allows. A `read-only` viewer may
   not, and its input messages are dropped and counted. The owner
   goroutine applies input in arrival order.
5. **Lifecycle: the session ends with its owner.**
   - When the owner's connection drops, the session waits for a grace
     period for the owner to reconnect (`session.owner_grace`, default
     60 s). Viewers stay connected and see a `viewer-left` for the owner.
   - If the owner does not come back in time, or closes the session, it
     ends and every viewer gets `close`.
   - A session with no input from anyone for `session.idle_timeout`
     (default 30 min) ends the same way.
   - Share tokens die with the session.
6. **Recording is out of scope for beta.** The `Sink` interface leaves
   room for a tap later.
7. **IPC for out-of-process backends waits** until
   [rdp-strategy](rdp-strategy.md) picks a helper.

## Affects

New `internal/backend` (interface, event types) and `internal/session`
(owner, viewer, registry), with the HTTP layer from
[api-and-server-stack](api-and-server-stack.md) on top. 003 fixes the
remaining v0 defects in place. The core is designed so that they cannot
arise, rather than inheriting those fixes.
