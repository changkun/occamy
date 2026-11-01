# Encode cache measurement

The [graphics pipeline](../../specs/008-native-architecture/graphics-pipeline.md)
gives each session an encode cache and workers. The viewers of a
session share the cache: each encoding of a tile, or of a run of lossy
tiles at a quality, is made once, by whichever viewer or worker asks
first, and the others find it there. Once a viewer's writer has a tile
to encode itself, it hands the rows after its own to one of the
session's workers, which asks for another each time it begins an
encoding, up to two; the writer then finds what they made. And
`encoder.Encoder.Still`, the SSIM search that picks a still photograph's
quality, now gallops from where the session's last search ended. This
page measures what that saves and what it costs, on the frames recorded
from the bench desktop that the [tile cache measurement](./tile-cache.md)
used.

## The answers

Milliseconds a frame, wall · CPU, the median of nine rounds. *Before*
is this task's base, each viewer alone; *now* is the defaults: the
cache and two workers.

| Scene | 1 viewer: before | 1 viewer: now | 4 viewers: before | 4 viewers: now |
|---|---|---|---|---|
| motion (G5) | 0.78 · 0.78 | 0.74 · 0.74 | 1.16 · 4.10 | 1.15 · 4.22 |
| drag (G4) | 4.02 · 4.14 | **2.19** · 4.22 | 4.33 · 16.34 | **2.37 · 9.75** |
| scroll (G3) | 1.51 · 1.35 | 1.25 · 1.23 | 1.63 · 6.16 | 1.55 · 5.23 |
| photo, once (G6) | 96.46 · 95.10 | **43.65** · 105.93 | 95.72 · 374.30 | **44.35 · 162.54** |
| browser, once | 21.45 · 22.44 | **9.43 · 21.09** | 22.50 · 88.91 | **11.76 · 45.33** |
| terminal-aa, once | 7.33 · 7.30 | **4.52** · 8.83 | 7.77 · 29.65 | **4.47 · 20.20** |
| switch, browser | 2.71 · 2.72 | **1.63** · 2.97 | 3.14 · 11.72 | **2.05 · 7.84** |
| switch, terminal | 0.42 · 0.42 | 0.43 · 0.40 | 0.64 · 2.19 | 0.64 · 2.24 |
| switch, photo | 45.39 · 44.87 | **21.20** · 48.87 | 47.65 · 185.75 | **21.05 · 77.82** |

- **Every scene that encodes reaches its viewers sooner**, in 38 to
  56 % less time: the photograph shown once in 44 ms against 96, the
  browser page in 9.4 against 21.5, the antialiased terminal in 4.5
  against 7.3, and the dragged window in 2.2 against 4.0. Two viewers
  take the same times within a millisecond.
- **Several viewers take less CPU.** Each encoding is made once, so
  four viewers take 32 to 58 % less on the scenes that encode: 163 ms
  against 374 for the photograph. Two take 15 to 36 % less.
- **The plasma, the scroll and the switched terminal are unchanged**,
  within the rounds' spread. Their frames come from the tile cache or
  go as `copy-rect`, and wake no worker. The first version of the
  workers, which woke one for every row of every frame, cost the plasma
  42 % more CPU; that is gone.
- **Every scene sends the same bytes**, the same in every round.
- **The gallop alone** takes the browser page shown once from 22.4 ms
  of CPU to 16.6 for a viewer alone, and the refinement of the dragged
  window from 40.5 to 30.2: their runs settle far from 80, where a
  search starts, which the walk crossed a step at a time.

## Where the workers lose

At one viewer the workers trade CPU for time: the two cores together
do the writer's work sooner, and some more besides. One viewer pays 9
to 21 % more CPU on the scenes that encode, the repeated window
switches among them; the drag pays 2 %, and the browser page shown
once is the exception. occamyd always runs the cache and two workers, with no
switch to turn them off yet, so this is the trade it makes. A
saturated host was not measured: the load was 8 to 11 on 18 cores, and
where every core is busy the workers' CPU comes from other sessions.

| Scene, 1 viewer | before | cache alone | now | now against before | now against the cache |
|---|---|---|---|---|---|
| photo, once | 95.10 | 93.76 | 105.93 | +11 % | +13 % |
| browser, once | 22.44 | 16.56 | 21.09 | −6 % | +27 % |
| terminal-aa, once | 7.30 | 7.59 | 8.83 | +21 % | +16 % |
| switch, browser | 2.72 | 2.69 | 2.97 | +9 % | +10 % |
| switch, photo | 44.87 | 45.94 | 48.87 | +9 % | +6 % |
| drag | 4.14 | 4.04 | 4.22 | +2 % | +4 % |

CPU ms a frame. *Cache alone* is the cache with no workers
(`EncodeWorkers: -1`), in the same build as *now*.

- **The antialiased terminal shown once** is the cell to weigh: 21 %
  more CPU than before for 38 % less time. Its rounds ranged from 7.4
  to 9.3 ms with the workers and from 6.5 to 9.1 before, so the cost is
  real but loosely measured. Two viewers of it take 24 % less than
  before, and four 32 % less.
- **The browser page shown once** takes less than before, since the
  gallop saves more than the workers cost; the workers alone cost it
  27 %, a session's first searches (below).
- **Three costs** make up the difference:
  - **Each worker's first search** in a session starts at 80, as the
    writer's does, since none has ended yet to start from; a browser
    page then pays three searches from 80 down to 30 where a viewer
    alone pays one. Later searches start where the session's last
    ended.
  - **A worker reads and hashes each tile it takes**, to look it up in
    the client's tile cache as the writer will, and the writer reads
    and hashes it again.
  - **This host's efficiency cores** take longer for the same work
    than its performance cores, and the workers' goroutines can land on
    them.
- **Without the workers** (`EncodeWorkers: -1`) the cache costs
  nothing at one viewer: within the rounds' spread of before or below
  it on every scene.

## How many workers

The default is two, or half the cores below four. The first default
was half the cores, nine here. Milliseconds a frame, wall · CPU:

| Scene | 2 workers | 4 workers | 9 workers |
|---|---|---|---|
| photo, once | 43.65 · 105.93 | 29.92 · 117.78 | 28.38 · 125.92 |
| photo, once, 4 viewers | 44.35 · 162.54 | 29.15 · 171.64 | 28.11 · 180.75 |
| browser, once | 9.43 · 21.09 | 10.51 · 28.72 | 10.12 · 30.91 |
| browser, once, 4 viewers | 11.76 · 45.33 | 11.10 · 50.90 | 11.02 · 54.95 |
| terminal-aa, once | 4.52 · 8.83 | 3.86 · 8.49 | 4.14 · 9.23 |
| switch, browser | 1.63 · 2.97 | 1.68 · 3.12 | 1.69 · 3.13 |
| switch, photo | 21.20 · 48.87 | 12.82 · 48.63 | 13.59 · 46.66 |
| drag | 2.19 · 4.22 | 2.30 · 4.45 | 2.15 · 4.41 |

- **More workers send a photograph sooner**: four take a third off the
  photograph shown once, 30 ms against 44, for 11 % more CPU, and nine
  no more than four, for 19 %.
- **They send a browser page no sooner**, for 36 to 47 % more CPU:
  most of its tiles are exact and quick, and its runs wait on the
  workers' first searches. The terminal, the switched windows and the
  drag come within the rounds' spread.
- **Each worker costs more CPU than the last** on this host, an Apple
  M5 Pro with 6 performance cores and 12 efficiency cores: the workers
  past the first few run on the slower cores. A host of one kind of
  core may do better with more; `stream.Config.EncodeWorkers` sets
  them.

Two give most of the time saved for the least CPU, and leave a busy
host's other sessions their cores.

## Memory

- **The cache** may hold up to 32 MiB a session
  (`stream.DefaultEncodeCacheBytes`), times the sessions: no bound
  covers them all. A run's encodings are kept one a quality, so as a
  viewer's pacer moves, the runs at qualities it left stay until the
  budget takes them. On the corpus it held 438 KiB at most, the
  plasma's two frames, and 173 KiB for the photograph; those are the
  peaks of short passes, a few frames of a scene in a new session, not
  what a long session holds. The budget leaves room for viewers at
  other qualities, whose runs are kept apart, and for a 4K screen, ten
  times the corpus's pixels. Each encoding counts its payload and 256
  bytes, and the oldest go first.
- **A worker** keeps an encoder for each profile it has encoded for,
  and the tiles of a run of up to 16 and their pixels side by side,
  512 KiB.

## How it was measured

**The corpus and scenes** are the [tile cache measurement](./tile-cache.md)'s:
frames of the bench desktop at 1024×768, recorded over VNC by `make
corpus` and described in
[bench/testdata/corpus](../../bench/testdata/corpus/README.md), and
the refinement measurement's scenes with the three switched windows.

**The run.** `go test ./internal/stream -run TestShareTimes -v
-share.runs N` sends each scene to 1, 2 and 4 viewers of one session,
in three modes: each viewer encoding alone, as with `NoShare`; sharing
the encode cache with no workers (*cache alone*); and sharing it and
the session's two workers (*now*). Each viewer has the writer that
occamyd runs, as the refinement and tile cache measurements drive it:
64-pixel tiles, scroll detection, WebP and deflate, the pacer's
defaults, a tile cache of 1024 slots, and frames never acknowledged.
The viewers send each frame at once, each on a goroutine of its own, as
their writers run. A frame's wall time is until every viewer has sent
it; its CPU time is the process's (`getrusage`) meanwhile, so it
counts the workers. Each pass makes a new session, so a scene shown
once is a session's first search.

*Before* is the same test built with this task's base, the encoder's
walk of one step a try, each viewer alone. The *4 workers* and *9
workers* columns are the same test with four and nine workers. The
three builds ran in turn, one pass of each a round, for nine rounds,
and each cell is the median of its rounds, so that the host's load fell
on each alike.

It ran on 2026-10-31 from 16:03 to 16:05 UTC, on the Apple M5 Pro (18
cores) of the [baselines](../benchmarks.md), with Go 1.27.1 on arm64
and GOMAXPROCS 18. The host's load average, logged every 5 seconds in
[encode-cache-load.log](./encode-cache-load.log), stayed between 8.2
and 10.7 over a minute, from other sessions' builds and browser tests;
it had stayed above 7 for the half hour before. Within a cell, the
rounds of frames of 5 ms or more differed by a median of 16 % in wall
time and 11 % in CPU time, with single rounds up to twice the median,
such as one of the browser page shown once at 18.2 ms; the rounds of
the shortest frames, the plasma's and the switched terminal's, differed
by a median of a third. The medians set those rounds aside.
