# Tile cache measurement

The [graphics pipeline](../../specs/008-native-architecture/graphics-pipeline.md)
gives each client a cache of tiles in numbered slots, and the server a
mirror of each client's slots. A tile sent alone and exact names the
slot used least recently, and the client keeps its pixels there. When a
tile's content is in a slot again, the server sends a `cached` tile of
that slot: 23 bytes instead of the pixels. This page measures what that
saves and what it costs. It uses the frames recorded from the bench
desktop that the [codec measurement](./codecs.md) and the
[refinement measurement](./refinement.md) used.

## The answers

| Scene | KiB a frame: without · with | cached tiles a frame | encode ms a frame: without · with | refinement once still, KiB: without · with |
|---|---|---|---|---|
| switch, browser | 79.7 · **9.4** | 186 | 6.20 · **1.61** | none |
| switch, terminal | 12.1 · **2.4** | 108 | 1.57 · **0.20** | none |
| switch, photo | 70.6 · **63.1** | 96 | 35.40 · 33.67 | none |
| motion (G5) | 13.8 · 4.3 | 192 | 11.44 · 0.32 | 391.0 · 0.0 |
| drag (G4) | 5.2 · 5.2 | 0 | 2.16 · 2.31 | 17.9 · 17.9 |
| scroll (G3) | 5.0 · 5.0 | 0 | 0.76 · 0.88 | none |
| photo, once (G6) | 121.8 · 121.8 | 0 | 78.71 · 71.89 | none |
| browser, once | 140.1 · 139.2 | 26 | 16.49 · 15.28 | none |
| terminal-aa, once | 103.5 · 102.5 | 37 | 4.57 · 4.28 | none |

- **A window switched back and forth over the desktop takes an eighth
  of the bytes** once the cache holds both: 9.4 KiB a frame against
  79.7 for the browser page, and 2.4 against 12.1 for the terminal.
  Nearly every tile comes from a slot, and the server encodes only what
  differs, so it spends a quarter of the encode time, or an eighth.
- **A photograph switched in the same way saves a tenth**: its tiles go
  lossy, which the cache does not store, so only the desktop comes from
  it.
- **The motion scene's figures are the corpus's, not the plasma's.**
  The corpus records two frames of the plasma, and the scene shows them
  in turn, so after the first two frames every tile has been seen. A
  real plasma never repeats, and its moving tiles go lossy, so the cache
  gives it nothing and costs it the hashes, 0.1 ms a frame.
- **The drag, the scroll and the photograph shown once send what they
  sent before**, byte for byte. The drag's changed tiles each hold part
  of the window at a new offset, the scroll goes as `copy-rect`, and
  the photograph is lossy.
- **The browser page and the terminal shown once** take a little less:
  26 and 37 of their tiles repeat one the client already has, sent
  earlier in the frame or with the desktop, such as a blank margin.
- **No scene takes more bytes**, and every scene ends with the client
  showing what it showed without the cache, exactly where it did then,
  as `TestCacheCorpus` checks.
- **The encode times of the scenes that send the same bytes** differ by
  less than the runs differ from each other: the hashes cost 0.53 µs a
  tile.

## Latency

A frame reaches the client after its encode, its bytes at the link's
rate, and the link's one-way delay. On the benchmark's N1 (100 Mbit/s,
10 ms each way) and N2 (20 Mbit/s, 25 ms each way), from the medians
above:

| Scene | N1, ms: without · with | N2, ms: without · with |
|---|---|---|
| switch, browser | 22.7 · **12.4** | 63.8 · **30.5** |
| switch, terminal | 12.6 · **10.4** | 31.5 · **26.2** |
| switch, photo | 51.2 · 48.8 | 89.3 · 84.5 |

The switched browser page reaches a viewer on N2 in half the time. The
scenes whose bytes do not change keep their latency.

## Memory

- **The server's mirror** of a client's slots holds 70.4 KiB a viewer
  once all 1024 slots are full (`TestTileCacheMemory`): each slot's
  hash, size and place in the order of use, and a map from content to
  slot. It holds no pixels.
- **The client** holds at most 16 KiB of pixels a slot, since a slot
  takes only tiles of up to 4096 pixels (`protocol.MaxCachedPixels`):
  16 MiB at the default 1024 slots. The Go client and the web client's
  protocol core keep a raw tile's pixels, or the decoded pixels of any
  other tile; neither keeps a payload once it is decoded. The Go client
  copies a raw tile's pixels out of its message, whose buffer has room
  to spare; the web core keeps the message, 23 bytes over the pixels.
  A client offers fewer
  slots, or none, with `client.Options.CacheSlots` and `NoCache`, or the
  web core's `cacheSlots`.
- **The hash** is xxh3-128 over the tile's pixels, 0.53 µs for a 64×64
  tile, 31 GB/s (`BenchmarkTileKey`, one core): 0.1 ms for the 192
  tiles of a 1024×768 screen. The server hashes every tile it is about
  to encode, so that it can look it up. Each mirror hashes under a
  secret seed of its own, which costs nothing measurable: 0.56 µs a
  tile seeded and unseeded, the medians of ten runs of each in turn at a
  load of 15 (2026-10-31, logged in `tile-cache-load.log`).

## How it was measured

**The corpus** is the codec measurement's: frames of the bench desktop
at 1024×768, recorded over VNC by `make corpus` and described in
[bench/testdata/corpus](../../bench/testdata/corpus/README.md). The
scenes are the refinement measurement's, and three more:

- switch, browser; switch, terminal; switch, photo: the idle desktop,
  then the browser page, the antialiased terminal or the photograph and
  the desktop in turn, six frames, the first two unmeasured, as a window
  brought forward and sent back with alt-tab is.

**The run.** `go test ./internal/stream -run TestCacheCorpus -v` gives
the bytes and checks them. `go test ./internal/stream -run
TestCacheTimes -v -cache.runs 15` gives the times, the median of 15
passes, on one core (`GOMAXPROCS=1`). Each drives one viewer's writer
from `internal/stream`, the one occamyd runs, as the refinement
measurement does: 64-pixel tiles, scroll detection on, WebP and
deflate, the pacer's defaults, and frames never acknowledged. *With* is
a mirror of 1024 slots, the default; *without* is none, as before. The
refinement columns are what the writer sends once the scene has been
still for the refine delay.

It ran three times on 2026-10-31 from 10:05 to 10:07 UTC, on the Apple
M5 Pro (18 cores) of the [baselines](../benchmarks.md), with Go 1.27.1
on arm64. The host's load average, logged every 5 seconds in
[tile-cache-load.log](./tile-cache-load.log), stayed between 10.3 and
14.8 over a minute, from other sessions' tests and a podman VM. Each
cell is the median of the three runs. The runs differed by 1 to 19 %
on the encode times, and by up to 60 % on the shortest frames, the
scroll's and the switched terminal's with the cache. The bytes and
counts are the same in every run.
