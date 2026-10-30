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
a copy, and the damage it reports is exactly what it drew. The layout
is RGBA, which the graphics pipeline's measurement confirmed on
2026-10-30.

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
  an input from a viewer, that viewer's writer sends the input's effect
  first: the tiles that changed after the input and were still before
  it, smallest drawing operation first. A tile already in motion changes
  after the input anyway, and taking that change for the effect would
  spend the input's priority on the motion. The effect goes next in the
  open frame, which then ends, or in a new frame that starts at once,
  one beyond the window if the window is full, and its bytes go out
  unpaced. The frame ends before a large operation, so that its
  `frame-end` does not wait behind motion drawn in the same moment. `frame-end`
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
  the screen its viewport shows. A hidden viewer gets no frames, which
  costs nothing to track: its dirty tiles are those whose versions passed
  the ones it last received ([graphics-pipeline](graphics-pipeline.md)),
  and it gets one frame of them when it becomes visible again: the
  current screen, never a backlog. Tiles outside the viewport stay dirty until they come into
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
  never queued behind tiles; acks and pings only drive pacing. Each
  viewer keeps them as state, the latest cursor, title or clipboard, so a
  slow viewer never queues them either.
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
   [graphics-pipeline](graphics-pipeline.md) benchmark picks (64, on
   2026-10-30).
3. **Late join for terminals:** decided in
   [native-ssh-backend](native-ssh-backend.md). The session keeps the
   terminal's screen in `internal/terminal` from the backend's output, so
   a late joiner gets a snapshot without asking the backend.
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

## Implementation notes: owner and registry

- **What was done:** `internal/session` with `Start`, `Session`
  (`Join`, `Close`, `Framebuffer`, `Changed`, `Size`, `Done`, `End`),
  `Viewer` (`Take`, `Wake`, `Input`, `LastInput`, `View`, `Answer`,
  `Leave`) and `Registry` (`Open`, `Get`, `Share`, `JoinShared`).
- **Decisions:**
  - One owner goroutine runs commands from a channel; viewers' calls
    become commands, so input reaches the backend in arrival order. Sink
    methods run on the backend's goroutines and never wait for the owner
    goroutine, so ending a session cannot deadlock on a backend blocked
    in the sink.
  - Each viewer's small state is a `Pending` of latest values (resize,
    cursor, clipboard, title, bells, joins, leaves, prompts, input, end),
    taken whole by its writer, so a slow writer skips stale values
    instead of queueing them.
  - An input records the framebuffer's clock read before the backend
    sees the input, so every tile it changes is above that clock even
    when a backend draws before `Input` returns. An input the backend
    rejects is not recorded and raises no `Pending.Input`.
  - The session serialises its calls to the terminal emulator and to
    `Resize`, since backends call the sink from several goroutines.
  - Defaults: owner grace 60 s, idle timeout 30 min, tile size 64.
  - Session IDs are 128 random bits; share tokens are 256 random bits,
    held only as SHA-256 hashes, with an expiry, and die with the
    session. Expired shares are pruned when the next is minted.
- **Surprises:** an independent review found five bugs the fake backends
  never reached (the clock read after `Input`, rejected inputs recorded,
  unserialised terminal calls, a prompt delivered twice to a returning
  owner, setters storing and broadcasting in separate critical sections).
  All have regression tests now.
- **Follow-ups:** `Viewer.Session` exposes `Close` and `Join` without a
  role check, so the HTTP layer checks roles.


## Implementation notes: viewer writer

- **What was done:** `internal/stream`, with `Serve(ctx, conn, viewer,
  hello, Config)`: it sends `welcome`, runs a reader goroutine that
  applies the client's messages, and runs the writer loop until the
  session ends or the client leaves. `Conn` is a message interface, so
  the tests run over a simulated link (a bottleneck rate, a one-way delay
  and a send buffer of one message) under `testing/synctest`.
  `SetLowWater` sets `TCP_NOTSENT_LOWAT` on Linux and macOS and does
  nothing elsewhere.
- **Decisions:**
  - An input's effect is the change after its clock on tiles that were
    still before it. The writer keeps two snapshots of the tile versions
    at least 100 ms apart. A tile that changed between them, or since the
    last one and before the input, was in motion, unless that change went
    out as an earlier input's effect, so that the next key on the same
    caret keeps its priority. Without this rule, full-screen motion drawn
    after a key took the key's priority, and an effect that appeared
    40 ms later got none.
  - Inputs that come while an effect is awaited join it: the first
    input's clock precedes their effects too, and each input restarts
    the timeout. So a key-up, which shows nothing, does not hold back the
    next key. Once an effect is sent, later inputs are awaited from the
    clock at which it was found.
  - The effect's tiles go smallest operation first: tiles that share a
    version were changed by one drawing operation. The input's frame ends
    before an operation that would take it past 16 KiB. The first
    operation goes whole, since a scroll may be the effect.
  - The writer looks for the effect before each tile, not after it.
    While the pacer holds a frame back, the writer wakes on framebuffer
    changes as long as an effect is awaited. An input whose effect does
    not appear within 1 s loses its priority.
  - `frame-end`'s `last_seq` is the last input recorded when the writer
    finished reading the frame's content (see the later fix below). An
    input applied before a frame's content was read may still have its
    effect on the way from the remote end, so `last_seq` says which
    inputs a frame could show, not that it shows their effect; the frame
    that goes beyond the window does show it.
  - Small state goes out before every tile, during pacing sleeps and
    while waiting, so it waits for at most the tile being written.
  - Resizes follow the framebuffer's bounds, not the session's small
    state. `resize` precedes every tile of the new size.
  - A client that breaks the protocol gets `close` with `CloseCodeFor`,
    including a `hello` after the handshake. The session's end gets
    `close` with its reason's code. Either waits at most 1 s for a client
    that stopped reading. A client that hangs up (`io.EOF` from `Conn`)
    or a failed connection gets nothing.
  - A client that decodes no raw tiles is refused with `unsupported`. A
    small message larger than the client's message size, such as a huge
    remote clipboard, is dropped. Up to 16 pings wait for their pongs,
    and acks beyond 64 drop the oldest.
  - Tiles go through an `Encoder` interface, raw RGBA until the graphics
    pipeline's codecs land. Cursors go raw, and not at all if they are
    larger than the client's texture limit; a hidden cursor carries no hot
    spot, codec or payload.
- **What was not done:**
  - `TCP_NOTSENT_LOWAT` is not set on a connection yet. coder/websocket
    hides the socket, so the HTTP layer reaches it first: the HTTP server
    task's `ConnContext` keeps each connection in the request context,
    and `connFrom` returns the TCP socket below TLS. The Streams task
    calls `SetLowWater` on it before the upgrade.
  - The Scheduling test task.
- **Surprises:**
  - The tile is the unit of priority. With 64-pixel tiles, the test
    screen's 32-pixel swatch shares a tile with the motion below it, so
    the key's effect counts as motion; the input test uses 32-pixel
    tiles. A real effect in a tile with motion gets no priority.
  - At most three tiles are ahead of an effect's tile: one on the wire,
    one in the send buffer and the one being written. Three raw 64-pixel
    tiles are 48 KiB, which take 10 ms at 5 MB/s; coded tiles are a few
    KiB.
  - `testing/synctest` stops time once a test's function returns, so a
    goroutine left in `time.Sleep` at cleanup deadlocks the bubble. The
    simulated link's waits select on close.
  - An independent review found seven bugs the first tests missed, each
    with a regression test now that fails without its fix: small state
    sent during a pacing sleep overwrote the tile waiting in the same
    buffer, which then counted as sent; a hidden cursor carried a hot spot
    and codec that clients must reject; the close after a protocol error
    could wait forever on a client that stopped reading; key-ups and the
    previous key's effect took the next key's priority while typing;
    `last_seq` lagged when an effect joined an open frame; a hang-up was
    taken for a protocol error; and a late `hello` got `unsupported`.
    The test client now records undecodable messages, and every test
    fails on one.
- **Follow-ups:** the Scheduling test, and `SetLowWater` on each
  stream's socket (the HTTP server's `connFrom` reaches it; the Streams
  task calls it).

## Implementation notes: terminal frames

- **What was done:** `Serve` serves terminal sessions. A frame is the
  title if it changed, `resize` if the size changed, the
  `terminal.Diff` from the screen the viewer last got to the current
  one, and `frame-end`. The first frame's diff goes as `term-snapshot`.
  The session makes the `terminal.Terminal` itself when `Config.Terminal`
  is nil, exposes snapshots through `Session.Screen`, and gives the
  backend the emulator's answers to the application's queries as
  terminal input.
- **Decisions:**
  - The emulator's answers are queued, and the owner goroutine gives them
    to the backend. They come from `TermData` on a backend goroutine,
    which must not wait for the backend's own `Input`.
  - A terminal's rows and cursor are its tiles. Each snapshot carries the
    version at which each row came to look as it does and at which the
    cursor last moved (`Screen.Versions`): a row redrawn the same, or one
    that scrolled into a row showing the same, keeps its version. An
    input's effect is a change after its clock on a row or the cursor
    that was not in motion before it, judged as for tiles, so a status
    line or a scrolling log does not take the frame beyond the window
    while the echo is still on its way from the remote end. The viewer
    keeps the screen it took the input's clock from, so the rows are read
    at that clock. While every row is in motion, as under a fast
    scrolling log, an echo cannot be told from the output and gets no
    priority.
  - `last_seq` is read after the snapshot, as in a graphical frame after
    the tiles: it names every input whose effect the snapshot can hold,
    and perhaps one more whose echo comes in a later frame.
  - A diff goes whole: a client's screen is right only once it has all of
    it, so a terminal frame is never cut. At the cap on bytes in flight it
    waits for acknowledgements of earlier frames (`Pacer.Hold`); a frame
    alone in flight goes on at the pacing rate (`Pacer.Pace`).
    `frame-end` goes unpaced, as in a graphical frame. A diff larger than
    the client's message size goes as several messages, split at
    character boundaries, and the first of a snapshot is the
    `term-snapshot`.
  - A screen redrawn the same, which bumps the terminal's version but
    changes nothing a viewer sees (`Screen.Equal`), costs no frame.
  - A terminal viewer gets no `cursor-move`, which is the graphical
    pointer, and the title from the screen, not the session's empty one
    at join.
  - A client that renders no terminal (`terminal` capability 0) is
    refused a terminal session with `unsupported`. The server offers
    level 1 unless configured otherwise.
- **Not done:** the owner's `hello` columns and rows do not size the
  terminal yet; the Streams task asks the backend for them.
- **Follow-ups:** the emulator's answers to queries are dropped, at Debug
  level, when the backend's input queue is full (the SSH backend's holds
  64, shared with viewer input), and a dropped answer to a device
  attributes query leaves a shell such as fish waiting. The owner's
  `select` also picks at random between a viewer's input and pending
  answers when both are ready. Neither is fixed here.
- **Tests:** a burst of 5000 lines converges in a few frames with one
  snapshot, resizes follow the backend, an input's echo goes out beyond a
  stalled client's window with its `last_seq`, an echo a round trip late
  under a status line redrawn every 5 ms gets the frame beyond the window
  and no frame reports the input without it, a hidden client gets no
  frames and one when shown, a diff over a 64 KiB message size arrives
  in pieces that each end at a character boundary, a diff past the cap
  waits for acknowledgements, identical redraws cost no frame, and a
  terminal client gets no pointer and one title. Each test fails when
  the behaviour it names is removed. The test client feeds what it gets
  into its own `terminal.Terminal`, rejects a piece split inside a
  character, and compares screens with the server's.
- **Review fixes:** an independent review found three bugs and three
  smaller faults, each with a test that fails without its fix:
  - a `max_message_size` below the term-data header made the writer
    panic, and one a few bytes above it looped on empty messages. The
    protocol now refuses a size below 64 KiB (`protocol.MinMessageSize`),
    and a frame that cannot make progress ends the stream with an error;
  - output between an input and its echo took the input's priority, so
    the frame beyond the window carried the output and `last_seq` without
    the echo, which then waited for a normal slot. Fixed by the row
    versions above;
  - `Delay`'s cap result was ignored, so a terminal frame never waited at
    the cap, and its own `frame-end` was the only message paced;
  - terminal viewers got `cursor-move` and the title three times;
  - a redraw that changed nothing cost a frame and, within 1 s of an
    input, the input's priority;
  - the chunk test could not catch a split inside a character, since the
    client's emulator reassembles one.
  The race fixes touched the graphical path too: the writer's last two
  snapshots of motion were taken as it looped, so after it waited a long
  time on a full window, a change between them hundreds of milliseconds
  before an input counted as motion. While the pacer holds frames back
  the writer now takes a snapshot at least every `activeWindow`, and
  snapshots older than twice that are not used. Either this or reading
  rows at the input's clock fixes the status-line test; without both it
  fails in about one run in five.
- **Cost:** a snapshot allocates a version per row, about 400 ns more
  for a 200x50 screen.
- **Later fix:** the Go client core's end-to-end tests found that a
  `resize-request` panicked the terminal writer: the rows in motion at
  the input were recorded for the old size and indexed with the new
  rows. occamyd hit it too, when a `resize-request` added rows while an
  input was pending. Both places that read those rows, the search for
  the input's effect (`termEffect`) and the record of what the effect
  changed (`keepEffects`), now take rows of a screen of another size as
  still, as tiles already did (`TestTerminalResizeRequest`,
  `TestTerminalResizeWhileBusy` with every row in motion,
  `TestTermEffectResized`, `TestKeepEffectsResized`;
  [client-core](client-core.md)).
- **Later fix:** the client core's `TestInputLastSeq` failed 15 runs
  of 200 under `-race`: a frame showed a key's effect with a
  `last_seq` below the key. The viewer recorded the input's `seq` only
  after `backend.Input` returned, and the remote could draw the effect
  in between; the graphical writer also read `last_seq` when the frame
  started. Now the viewer records `seq` before it hands the input to the
  backend, and puts the previous one back if the backend refuses it, and
  both writers read `last_seq` after the frame's content (the tiles, or
  the terminal snapshot). An effect on screen therefore always has its
  input recorded, and `last_seq` never names less than a frame shows; it
  may name one input more, whose effect comes later, which the protocol
  allows (`TestInputRecordedBeforeBackend`; the client test passes 200
  runs of 200 under `-race`).
