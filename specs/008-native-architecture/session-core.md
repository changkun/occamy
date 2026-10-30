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
updated: 2026-10-30
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
  and a failed connect sticking its claims are fixed. A client that
  joins a session as guacd ends it now starts a new session, a stress
  test runs the table under `-race`, and CI runs the pressure test. One
  sliver remains on v0: a join that guacd accepts just before the last
  user leaves gets `ready` and is then disconnected, which `occamyd`
  cannot tell from a session that ends. It exists because guacd, not
  `occamyd`, decides when a connection ends.
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
    Input(ev InputEvent) error // key, pointer, wheel, text, terminal input, clipboard, resize
    // Demand tells the backend whether any viewer can see the session,
    // so that it can pace the remote server (see Input to display).
    Demand(d Demand)
}

type Demand struct {
    Visible bool // at least one viewer can see the screen
}

type Sink interface {
    Resize(w, h int)
    // Lock returns the framebuffer's pixels in r, locked for drawing;
    // Unlock releases them and records r as damage. Graphical only.
    Lock(r image.Rectangle) *image.RGBA
    Unlock(r image.Rectangle)
    CopyRect(dst image.Rectangle, src image.Point)
    Cursor(img *image.RGBA, hot image.Point)
    CursorMove(p image.Point)
    TermData(p []byte)                 // terminal
    Clipboard(mime string, data []byte)
    Bell()
    Title(title string)
    Prompt(ctx context.Context, p Prompt) (PromptReply, error)
}
```

Graphical backends write pixels into a framebuffer that the session owns
([graphics-pipeline](graphics-pipeline.md)) and report damage. They never
encode. They draw through `Lock` and `Unlock`, so that a decoder writes
straight into the framebuffer under the row locks of decision 2, without
a copy, and the damage it reports is exactly what it drew. The RGBA
layout holds until the graphics pipeline's measurement settles the pixel
layout.

The session owner is one goroutine per session. It runs the backend,
applies events to shared state (the framebuffer, or the terminal state
plus a scrollback snapshot), serializes input from viewers that hold
input rights, and enforces roles (owner, viewer, read-only). It starts a
writer goroutine per viewer.

### Input to display

The scheduler's objective is the time from a viewer's input until that
viewer sees its effect, measured as `L` in [benchmark](benchmark.md),
and only for what a viewer can see. An input matters through its visible
effect. Delivering the input quickly, or sending control messages ahead
of frames, gains nothing while the frame that shows the effect waits;
and prioritizing a viewer who cannot see the screen is wasted work. The
baseline shows the cost of getting this wrong: in full-screen motion
x11vnc shows a key in 5.6 ms, and guacd delivers it after 84 ms at N0
and 113 ms at N1, behind frames it had already queued.

- **Frames are states, not events.** Under the pull model (decision 1),
  a frame carries the current content of its dirty tiles, so a viewer
  that skips intermediate frames loses nothing: an input's effect is in
  the newest state. Skipping stale frames and prioritizing the input are
  the same act. Terminals follow the same rule with screen diffs
  ([native-ssh-backend](native-ssh-backend.md)).
- **The frame after an input goes out at once.** When the owner applies
  an input from a viewer, that viewer's writer sends the tiles damaged
  since the input first: next in the open frame, which then ends, or in
  a new frame that starts at once, one beyond the window if the window
  is full. Those bytes go out unpaced. `frame-end`
  carries the sequence number of the last input applied before the
  frame's tiles were read ([protocol-v1](protocol-v1.md)), so the client
  measures input to display exactly, and the benchmark uses the same
  number.
- **Little is in flight.** Besides the frame window, a viewer's writes
  are paced at its delivery rate, which `frame-ack`s measure as BBR
  does: the windowed maximum delivery rate and minimum round trip. Data
  then does not queue in the network. Bytes in flight are capped at
  twice the bandwidth-delay product, and when round trips grow above
  their minimum the rate backs off to the latest measurement. On Linux
  and macOS the writer sets `TCP_NOTSENT_LOWAT`, so the kernel holds
  little unsent data. A key's frame then waits behind little more than
  the tile being written.
- **Only what is seen.** A client reports with `view` whether it is
  visible (a hidden browser tab, a minimized window) and which part of
  the screen its viewport shows. A hidden viewer gets no frames; its
  dirty region keeps growing, which costs a bitset, and it gets one frame
  of it when it becomes visible again: the current screen, never a
  backlog. Tiles outside the viewport stay dirty until they come into
  view.
- **Motion degrades smoothly.** While a viewer's writer spends most of a
  round waiting for the path, its lossy quality steps down, and it steps
  back up while the writer waits little; the frame interval follows the
  viewer's decode time ([graphics-pipeline](graphics-pipeline.md)).
  Input stays responsive while motion loses detail or rate, never the
  reverse.
- **What goes first is what the user sees directly.** The cursor is drawn
  by the client from `cursor` and `cursor-move`, so pointer motion needs
  no frame. Small messages (cursor, clipboard, bell, close, ping) are
  never queued behind tiles; acks and pings only drive pacing.
- **The remote server is paced too.** The session owns the framebuffer,
  so the remote server's rate and each viewer's rate are independent,
  and backends use each protocol's own control:
  - VNC is pull-based: the backend keeps one incremental update request
    outstanding while `Demand.Visible`, applies each update as it
    arrives and asks for the next, and stops asking while nobody can
    see.
  - RDP acknowledges graphics frames as it applies them, and sends a
    Suppress Output PDU while nobody can see.
  - SSH output always feeds the terminal state, which is cheap and keeps
    it correct, while the frames to viewers follow the rules above.

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
8. **The scheduling objective is input to display, for what is seen**
   (owner's call, 2026-10-30). A keypress matters more than a frame only
   through the frame that shows its effect, and only while the user can
   see it. This replaces an earlier design of a small-message queue
   drained ahead of tiles, which put control messages ahead of the
   frames the user was waiting for. See Input to display.

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Backend interface | `internal/backend`: `Class`, `Backend`, `Sink`, `Demand`, input events (key, pointer, wheel, text, terminal input, clipboard, resize) and prompts; `backendtest`, a scripted fake backend for both classes that draws on a clock and records input | — |
| Pacer | A per-viewer controller without I/O: the frame window, one frame beyond it after an input, bytes in flight capped near the bandwidth-delay product from `frame-ack` round trips with delay-based backoff, and AIMD steps for the frame interval and quality; tests on a simulated clock and link | — |
| Owner | `internal/session`: the goroutine that runs a backend, applies its events to the framebuffer or the terminal emulator, adds damage to each viewer's dirty tiles, applies input in arrival order by role (read-only input dropped and counted), sets `Demand` from the viewers' `view`, and ends the session per decision 5; tests under `-race` that also check for leaked goroutines | Backend interface; the graphics pipeline's framebuffer |
| Viewer writer | The goroutine per viewer: takes the dirty tiles inside the viewport when the pacer allows, encodes them through an encoder interface (raw pixels until the graphics pipeline's codecs land), sends size-capped tiles and `frame-end` with the last input applied, sends small messages ahead of tiles, sends a hidden viewer nothing and one current frame on its return, and sets `TCP_NOTSENT_LOWAT` on Linux and macOS; terminal sessions send screen diffs in the same frames | Owner, Pacer, protocol v1 Messages |
| Registry | Sessions by ID: open on a connection, join with a role, share tokens that die with the session, removal when it ends | Owner |
| Scheduling test | End to end with the fake backend in full-screen motion over a simulated N1 link: a key's `frame-end` arrives within one round trip plus a bound while frames keep flowing, a hidden viewer receives nothing and costs no encoding, and a slow viewer does not slow a fast one | Viewer writer |

The HTTP routes on top belong to
[api-and-server-stack](api-and-server-stack.md).

## Affects

New `internal/backend` (interface, event types) and `internal/session`
(owner, viewer, registry), with the HTTP layer from
[api-and-server-stack](api-and-server-stack.md) on top. 003 fixes the
remaining v0 defects in place. The core is designed so that they cannot
arise, rather than inheriting those fixes.
