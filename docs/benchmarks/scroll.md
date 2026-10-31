# Scroll detection measurement

The [graphics pipeline](../../specs/008-native-architecture/graphics-pipeline.md)
looks for scrolls in what changed and sends a `copy-rect` for them, with
tiles only for what the copy does not complete. This page measures what
that saves and what it costs, on the frames recorded from the bench
desktop that the [codec measurement](./codecs.md) used.

## The answers

| Scene | KiB a frame: without · with | tiles a frame: without · with | encode ms a frame: without · with | of which the search |
|---|---|---|---|---|
| scroll | 58.5 · **5.0** | 180 · 15 | 3.59 · **0.54** | 0.234 |
| scroll, skipping one | 58.6 · **5.0** | 180 · 15 | 3.62 · **0.53** | 0.233 |
| drag | 9.8 · 9.8 | 5 · 5 | 2.32 · 2.30 | 0.044 |
| motion | 393.8 · 393.8 | 192 · 192 | 8.35 · 8.62 | 0.068 |
| browser, whole | 143.0 · 143.0 | 187 · 187 | 6.97 · 6.82 | 0.062 |
| photo, whole | 31.3 · 31.3 | 12 · 12 | 10.96 · 11.21 | 0.065 |

- **The scroll takes a twelfth of the bytes and about a seventh of the
  time.**
  The scrolled text moves by one 13-pixel line an update. Each update
  becomes one `copy-rect` of (64,0)–(1024,755) from 13 rows below,
  which completes 165 of the 180 changed tiles, and the 15 tiles of the
  exposed line. G3's target over VNC is 7.1 KiB a frame, against the
  baseline's 71. These 5.0 KiB leave out the `frame-end` message and the
  WebSocket's framing, a few dozen bytes. G3 itself waits for occamyd to
  serve protocol v1.
- **A viewer that skipped an update gets the same.** It is sent
  scroll-2 straight after scroll-0: one copy from 26 rows below, and the
  same 15 tiles. The copy is found against the screen that viewer's
  client shows, not against the last frame sent to anyone.
- **Other scenes send the same bytes**, byte for byte, and the search
  costs 0.04 to 0.07 ms of their frame: 0.6 to 1.9 % of its encode
  time. The differences in total encode time, with and without, are
  within what separate runs differ by (below). The dragged window moves
  sideways as well as down, which a search of vertical offsets does not
  find.

The search on its own (`BenchmarkScrollFind -cpu 1`, the median of 10
counts), on a 1024×768 screen whose shadow's row hashes are stale, as
after a frame of tiles:

| Change | Time a frame | Allocations |
|---|---|---|
| one tile | 62 ns | 0 |
| a line across a row of tiles | 136 ns | 0 |
| a new page (gives up after three columns) | 58 µs | 0 |
| a scroll of the whole screen | 223 µs | 168, 15.6 KiB |

A change that leaves no column with two dirty tiles one above the other
(a key, a caret, a button, a line within one row of tiles) is rejected
before anything is hashed. A new page is given up after three columns find no offset, so
a frame of motion costs at most three columns' hashing. A scroll votes
once and tries the offset found on the other 15 columns.

## How it was measured

**The corpus** is the codec measurement's: frames of the bench desktop at
1024×768, recorded over VNC by `make corpus` and described in
[bench/testdata/corpus](../../bench/testdata/corpus/README.md). A scene
is a viewer that has its first frame and is sent each of the others in
turn: the three consecutive updates of the scrolling text; the same with
the middle one skipped; the three of the dragged window; the two of the
plasma; and the idle desktop replaced whole by the browser page and by
the photograph.

**The run.** `go test ./internal/stream -run TestScrollTimes -v
-scroll.runs 15` drives one viewer's writer from `internal/stream`, the
one occamyd runs, over a session and its framebuffer of 64-pixel tiles.
The viewer agrees to WebP and deflate, and the pacer is held at quality
70, as in the codec measurement. Each update is painted as a backend
reports damage, tile by tile where the image differs. Frames are never
acknowledged, so the pacer neither cuts nor paces them, and the test
fails if a frame leaves any change unsent. A frame's encode time is the
writer's: reading the tiles, the search and encoding. A scene's figure
is the median of 15 passes for each update, summed over its updates and
divided by their number. The bytes are those of the frame's `tile` and
`copy-rect` messages. Everything runs on one core (`GOMAXPROCS=1`).

It ran three times on 2026-10-31 from 02:03 to 02:06 UTC, on the Apple
M5 Pro (18 cores) of the [baselines](../benchmarks.md), with Go 1.27.1
on arm64. The host's load average, logged every 5 seconds in
[scroll-load.log](./scroll-load.log), stayed between 2.9 and 4.5 over a
minute, with other sessions' work on the host. The table is the median
of the three runs for each cell. The runs differed by up to 14 % on the
scroll's encode times without detection (3.53 to 4.07 ms), by up to
35 % with it (0.52 to 0.70 ms), and by 2 to 9 % on the other scenes.
The bytes and the tile counts are the same in every run.

`go test ./internal/stream -run TestScrollCorpus` checks the bytes on
every run of the tests. It requires a scroll to cost at most an eighth
without detection, the other scenes to cost the same, and the client's
screen to equal the server's after every lossless frame.
