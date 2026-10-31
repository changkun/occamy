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
  WebSocket's framing, a few dozen bytes. G3 itself, measured end to
  end through occamyd, has not been run on protocol v1 yet.
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
counts), on screens whose shadow's row hashes are stale, as after a
frame of tiles. Each cell gives the time a frame and the runs (tile
columns' stretches of dirty tiles) searched:

| Change | 1024×768 | 1920×1080 | 3840×2160 |
|---|---|---|---|
| one tile | 39 ns · 0 | 45 ns · 0 | 101 ns · 0 |
| a line across a row of tiles | 137 ns · 0 | 323 ns · 0 | 1.1 µs · 0 |
| a new page | 58 µs · 3 | 86 µs · 3 | 178 µs · 3 |
| flat motion over the screen | 57 µs · 3 | 83 µs · 3 | 170 µs · 3 |
| a scroll of the whole screen | 223 µs · 16 | 636 µs · 30 | 4.00 ms · 60 |
| a sidebar scrolls beside a new page | 128 µs · 5 | 187 µs · 5 | 385 µs · 5 |
| a sidebar scrolls beside flat motion | 126 µs · 5 | 186 µs · 5 | 386 µs · 5 |

- **Changes within one row of tiles** leave no column with two dirty
  tiles one above the other, as a key, a caret, a button or a line do.
  They are rejected before anything is hashed, and allocate nothing.
- **A frame stops** once it has searched three runs more without a copy
  than it found copies in: at most 2h+3 runs for h copies. Motion with
  no scroll costs three runs' hashing. A sidebar that scrolls beside new
  content or motion costs five: the sidebar's run, found first or among
  the first, raises the limit by one.
- **A scroll of the whole screen** votes once and tries the offset found
  on every other column. It allocates the copies' tile lists: 168
  allocations and 15.6 KiB at 1024×768, 854 and 234 KiB at 4K.

**Before the review**, the miss limit counted only until the first copy
was found. A sidebar scroll beside other changes then searched every
run: 16, 30 and 60. Measured the same way, the earlier code took:

| Change | 1024×768 | 1920×1080 | 3840×2160 |
|---|---|---|---|
| a sidebar scrolls beside a new page | 415 µs | 1.14 ms | 6.40 ms |
| a sidebar scrolls beside flat motion | 426 µs | 1.18 ms | 6.33 ms |

Driven through a writer at 1024×768, frame by frame for 15 frames, the
search was 31 % of the median frame's encode time beside flat motion
and 8 % beside a new page. It is now 8.7 % and 2.2 %.

## Lossy quality through copies

A copy moves the pixels a client has, which for a lossy tile are its
decoder's approximation at the quality the tile was sent at. The
shadow records that quality, and a copy completes a tile only if its
pixels end up lossless or at least at the pacer's quality now. A tile
the copy draws at a lower quality is sent again, as it would be
without the copy.

`TestScrollLossyQuality` sends the corpus's photograph, 12 tiles, with
the pacer held at one quality. It then scrolls the photograph by 16
rows with the pacer held at another:

| Quality: sent, then scrolled | Scroll detection | dB after the scroll | copies | tiles | KiB |
|---|---|---|---|---|---|
| 30, then 90 | off | 39.8 | 0 | 12 | 147.9 |
| 30, then 90 | on, before the review | 34.9 | 1 | 1 | 8.9 |
| 30, then 90 | on | 39.8 | 1 | 12 | 147.9 |
| 90, then 30 | off | 34.6 | 0 | 12 | 13.6 |
| 90, then 30 | on | 39.1 | 1 | 1 | 0.7 |

Before the review, a copy kept a viewer at quality 30 after the pacer
had raised it to 90. Now the tiles go again at 90, and the copy costs 20
bytes for nothing. Scrolled at a lower quality than they were sent at,
the copy keeps the better pixels for a twentieth of the bytes.

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
The bytes and the tile counts are the same in every run. A run after
the review's revision gave the same bytes and tiles.

**The search on its own.** `go test ./internal/stream -run XXX -bench
BenchmarkScrollFind -benchmem -cpu 1 -count 10` ran on 2026-10-31 from
03:56 to 04:01 UTC, on the same host, after the review's revision. Each
scene draws a page, sends it as one frame, changes the framebuffer, and
times the search of the dirty tiles with every row hash marked stale.
The table gives the median of the 10 counts. The load average, logged
in [scroll-find-load.log](./scroll-find-load.log), was between 3.2 and
9.1 over a minute. The slowest count of each cell was at most 13 %
above its median, except flat motion at 1024×768 (22 %) and one tile,
whose few dozen nanoseconds varied by half.
The figures from before the revision are the median of 5 counts of the
same scenes on the earlier code, under a load average of 5 to 7. The
writer figures come from the review's own test, which sent 15 frames of
each scene through the harness that `TestScrollTimes` uses, with the
pacer held at quality 70. That test is not kept in the tree;
`BenchmarkScrollFind`'s sidebar rows are the reproducible form.

`go test ./internal/stream -run TestScrollCorpus` checks the bytes on
every run of the tests. It requires a scroll to cost at most an eighth
without detection, the other scenes to cost the same, and the client's
screen to equal the server's after every lossless frame.
