# Refinement measurement

The [graphics pipeline](../../specs/008-native-architecture/graphics-pipeline.md)
sends a tile in motion at a low quality, and lossy if it has 17 to 256
colours. Once the tile has been still for a while, it sends it again at
its final quality: exact if it has up to 256 colours, and at the quality
SSIM picks if it has more. A still photographic tile goes at that
quality at once. This page measures what that saves, what it costs, and
how it treats input. It uses the frames recorded from the bench desktop
that the [codec measurement](./codecs.md) used.

## The answers

| Scene | KiB a frame: before · after | encode ms a frame: before · after | refinement once still: KiB · messages · encode ms | longest message of it, ms |
|---|---|---|---|---|
| motion (G5) | 392.4 · **13.8** | 9.53 · 11.65 | 391.0 · 192 · 9.07 | 0.10 |
| drag (G4) | 30.6 · **5.2** | 4.25 · 2.13 | 17.9 · 30 · 34.00 | 2.56 |
| scroll (G3) | 5.0 · 5.0 | 0.62 · 0.66 | none | |
| photo, once (G6) | 150.8 · **121.8** | 21.99 · 66.68 | none | |
| browser, once | 147.3 · 140.1 | 7.93 · 15.99 | none | |
| terminal-aa, once | 103.5 · 103.5 | 4.62 · 5.12 | none | |

- **The plasma takes a twenty-eighth of the bytes while it moves.** Its
  tiles have 17 to 256 colours, so they went lossless before, at 392.4
  KiB a frame. In motion they now go as WebP at quality 30, at 13.8 KiB.
  G5's target is about 5 KiB a frame. Once the plasma stops, refinement
  sends every tile again in deflate, 391.0 KiB in 192 messages, and the
  client then shows the server's screen exactly.
- **The dragged window takes a sixth of the bytes.** It is photographic,
  so it went lossy before too, but at the pacer's quality of 90. In
  motion it goes at 30. Once still, 30 of its tiles go again at the
  quality SSIM picks: 17.9 KiB. Five more reach the score at 30, as they
  are, and go no more. Searching them takes 34 ms of one core in all, as
  measured before that change, and at most
  2.6 ms for one message, which is the longest that refinement holds up
  an input on the writer.
- **The photograph shown once goes under G6's 139 KiB**: 121.8 KiB
  against 150.8, since the search finds that quality 87, not the
  pacer's 90, reaches the score on most rows. It needs no refinement.
  The search costs three times the encode time: 66.7 ms of one core
  against 22.0.
- **The browser page's pictures** take 140.1 KiB against 147.3, for the
  same reason, and twice the encode time.
- **The scroll and the antialiased terminal send what they sent before**,
  byte for byte, and need no refinement: their tiles are lossless.
- **No tile ends worse than SSIM 0.91**, or than before where before was
  worse, as `TestRefineCorpus` checks on every scene. Before, the
  photograph's worst tile scored 0.939 and the dragged window's 0.956,
  both at quality 90; after, both score 0.910.

`BenchmarkStill -cpu 1` times one row of the photograph, 16 tiles: 1.75
ms as WebP at quality 90, 5.39 ms searched (medians of five counts). The
search decodes twice, and vpx allocates each decode, so it allocates 713
KB a row in 23 allocations, against 0.7 KB for one encode.

## Input while refining

`TestRefineYieldsToInput` presses 40 keys, 23 to 63 ms apart, while a
screen of photograph that has just stopped moving is refined. It runs
over the benchmark's N1 (100 Mbit/s, 10 ms each way) and N2 (20 Mbit/s,
25 ms each way), with refinement and without, in fake time, where
encoding takes none. A key's time is from the key to the frame that
shows its effect:

| Link | with refinement: median · worst | without: median · worst | tiles refined meanwhile |
|---|---|---|---|
| N1 | 20.0 · 20.0 ms | 20.0 · 20.0 ms | 56 |
| N2 | 50.0 · 50.9 ms | 50.0 · 50.0 ms | 158 |

- On N1 no key waits. On N2 the worst waits 0.84 ms for a message of
  refinement ahead of it on the link. The test allows 5 ms, the pacer's
  target delay, which bounds the bytes of a frame of refinement.
- On a real writer, a key can also wait for the encode of the one
  message of refinement under way: at most 2.6 ms of one core on the
  corpus, the dragged window's photographic tiles.

## The SSIM score

`encoder.StillSSIM` is 0.91, which each tile of a merged run must
reach. The photograph's twelve rows of sixteen tiles take this much,
searched in steps of 3 from the pacer's lowest quality to its highest
(30 to 90):

| Score on each tile | KiB |
|---|---|
| 0.88 | 93.5 |
| 0.89 | 102.4 |
| 0.90 | 110.9 |
| **0.91** | **121.5**, rows at quality 87 to 90, median 87 |
| 0.915 | 133.1 |
| 0.92 | 141.9 |

- G6's target is 139 KiB for the photograph. 0.92 misses it, and 0.91
  meets it with 17.5 KiB to spare.
- Scored on a run's mean instead, 0.92 took 121.4 KiB, about what 0.91
  takes on each tile, but it left one tile at 0.909. The mean hides the
  worst tile, and the worst tile is what a viewer notices.
- At quality 90, the pacer's highest, 78 of the photograph's 192 tiles
  score under 0.95, so a score of 0.95 is out of reach on two fifths of
  it.
- The guacd baseline's JPEG at quality 70 scores 0.871 on the photograph
  as a whole.
- Steps of 10 took 150.6 KiB at 0.92 on the mean, against 121.4 with
  steps of 3: the coarser ladder overshoots by up to 9 points.

## Memory

- **A writer** keeps 13 bytes a tile for refinement: its heat (4), when
  it last changed (8) and its goal (1), and 1 more for its quality when
  there is no scroll shadow to hold it. That is 2.4 KiB at 1024×768, 6.5
  KiB at 1080p and 26 KiB at 4K, beside the shadow's 4 bytes a pixel.
- **A searching encoder** holds, once warm on the photograph's rows, 62
  KiB more than one that only encodes for WebP: the last candidate and
  the best encoding, and the SSIM's column sums. For JPEG it holds 339
  KiB more, mostly the run's decoded pixels (256 KiB), since image/jpeg
  decodes into a YCbCr image that is converted into a buffer kept for
  the next search.
- **The search's garbage** is the 713 KB a photographic row's decodes
  allocate with vpx, about 8.4 MB for a full screen of photograph,
  collected after each search.

## How it was measured

**The corpus** is the codec measurement's: frames of the bench desktop
at 1024×768, recorded over VNC by `make corpus` and described in
[bench/testdata/corpus](../../bench/testdata/corpus/README.md). A scene
is a viewer that has its first frame and is sent each of the others in
turn:

- motion: the plasma, `motion-1` and `motion-0` in turn, eight frames,
  the first two unmeasured while the tiles warm up;
- drag: the dragged window, `drag-1`, `drag-2`, `drag-1` and `drag-0`,
  twice, the first two unmeasured;
- scroll: the scrolled text, two frames;
- photo, browser and terminal-aa, once: the idle desktop replaced whole
  by the photograph, the browser page and the antialiased terminal.

Frames of the plasma, the drag and the scroll are taken at least 100 ms
apart, as frames at 30 a second are, so that each counts as a snapshot
of motion. The frames of the other scenes are not.

**The run.** `go test ./internal/stream -run TestRefineCorpus -v` gives
the bytes and checks them. `go test ./internal/stream -run
TestRefineTimes -v -refine.runs 15` gives the times, the median of 15
passes, on one core (`GOMAXPROCS=1`). Each drives one viewer's writer
from `internal/stream`, the one occamyd runs. The writer serves a session
with a framebuffer of 64-pixel tiles, has scroll detection on, and
serves a viewer of WebP and deflate. The pacer has its defaults: quality
30 to 90, starting at 90. Frames are never acknowledged, so the pacer
neither cuts nor paces them.

*Before* is the encoder as it was before refinement: tiles of 17 to 256
colours lossless whatever their motion, lossy tiles at the pacer's
quality with no search, and no refinement. *After* sends the scene, then
refines what the writer would once the scene had been still for the
refine delay: every candidate tile, one message each. The bytes and
messages of that are the refinement columns. Their longest encode of one
message is what an input waits for the writer.

It ran three times on 2026-10-31 from 09:02 to 09:04 UTC, on the Apple
M5 Pro (18 cores) of the [baselines](../benchmarks.md), with Go 1.27.1
on arm64. The host's load average, logged every 5 seconds in
[refinement-load.log](./refinement-load.log), stayed between 8.0 and
12.7 over a minute, from other sessions' tests and a podman VM. Each
cell is the median of the three runs. The runs differed by 2 to 10 %
on the encode times of the plasma, the drag, the photograph and the
browser, by 20 % on the longest message of refinement (2.37 to 2.85
ms), and by up to 73 % on the shortest frames, the scroll's and the
terminal's (0.59 to 1.02 ms on the scroll before). The bytes and
message counts are the same in every run.
