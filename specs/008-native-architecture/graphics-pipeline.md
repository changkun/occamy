---
title: Graphics pipeline
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/benchmark.md
affects:
  - internal/framebuffer/
  - internal/encoder/
effort: large
created: 2026-10-29
updated: 2026-10-30
author: Changkun Ou
dispatched_task_id: null
---

# Graphics pipeline

## Design Problem

Turn backend damage into the smallest set of tiles each viewer can decode
quickly, in pure Go. That means choosing:

- the framebuffer format and tile size;
- how damage is tracked;
- how tiles are classified;
- which codecs to use, given that the server must stay `CGO_ENABLED=0`
  and clients are browsers and a Go core under Swift;
- how quality adapts per viewer.

## Context

- Pure-Go encoders without cgo, as surveyed on 2026-10-30:
  - the standard library's `image/png`, `image/jpeg` (rewritten in Go
    1.26) and `compress/flate` (faster in Go 1.27, which speeds up PNG
    too);
  - `github.com/klauspost/compress` for flate and zstd;
  - `github.com/deepteams/webp` and `github.com/gen2brain/vpx/webp`,
    lossless and lossy WebP encoders with SIMD;
  - `github.com/gen2brain/jpegn`, JPEG with SIMD;
  - `github.com/zeebo/xxh3` for hashing: xxh3-64 for scroll detection's
    row hashes, xxh3-128 for the tile cache.

  Their speed figures are the projects' own, several measured against
  the older standard library, so the measurement compares them on Go
  1.27. The WebP encoders and jpegn are months old. libwebp and
  libjpeg-turbo would bring cgo back.
- Browsers decode PNG, JPEG and WebP natively (`createImageBitmap`, in a
  Worker), and inflate with `DecompressionStream('deflate-raw')` (Chrome
  103, Firefox 113, Safari 16.4). No engine ships a native zstd decoder,
  and Chrome has no brotli one, so zstd on the wire needs a decoder in
  the client.
- libguac's approach, for reference: a 64-pixel heat map on each surface
  picks lossy encoding for frequently updated regions and PNG otherwise.
- Backends: VNC delivers rectangles, already decoded, plus CopyRect. The
  guacd bridge delivers decoded image streams plus `copy` operations.
  Both write into the framebuffer that [session-core](session-core.md)
  owns.

## Proposed shape

- **Framebuffer:** 32-bit pixels, one buffer per session, tiles of 64×64
  with a version each. A change gives the tiles it touches a new version
  from one counter for the screen, so a viewer's dirty tiles are those
  above the versions it last sent, and the tiles changed since an input
  are those above the counter's value when the input was applied. A
  CopyRect from the backend is forwarded as
  `copy-rect` when every viewer's last frame contains the source region;
  otherwise it degrades to damage.
- **Classification per tile: content picks the codec, motion picks the
  quality.** Count distinct colours with an early exit. At or below 256
  colours (text, UI) the tile is lossless. Above, it is photographic and
  lossy, at a quality that the tile's motion (a heat counter decayed per
  second) and the viewer's link set: low while it moves, high once it
  stops. guacd 1.6 chooses PNG or JPEG by timing alone
  (`src/libguac/display-worker.c`), so the same photograph cost 0.4 to
  2.2 MiB between runs of the baseline's G6.
- **Refinement (build to lossless):** when a region stops changing for a
  short delay, it is resent at its final quality: exact for low-colour
  tiles, so text and UI always end up exact, and a high lossy quality
  for photographic tiles, set by a perceptual score (SSIM against the
  framebuffer) rather than bit-exactness, which would cost PNG's size.
- **Scroll and move detection:** the framebuffer keeps a hash per row of
  each tile column. When a large region changes, the encoder looks for a
  vertical or horizontal offset at which most row hashes match the
  previous frame, and sends `copy-rect` for the matched part and tiles
  for the exposed strip only. The baseline's G3 sends 71 KiB a frame
  over VNC and 516 KiB over RDP for a one-line scroll; the target is
  7.1 and 52 KiB. Backend CopyRect is used directly when it exists.
- **Client tile cache:** the server mirrors a bounded LRU of tiles that
  each client holds, keyed by a content hash (xxh3). A tile whose
  content the client already has is sent as a `tile` with the `cached`
  codec and the slot number, a few bytes instead of the pixels.
  Repeated content (toolbars, desktop backgrounds, a window moved back)
  then costs nothing to resend.
- **Adaptive quality per viewer:** the lossy quality and the frame
  interval follow each viewer's frame-ack round trips and decode times
  (additive increase, multiplicative decrease), as
  [session-core](session-core.md) describes under Input to display. Only
  the visible viewport is encoded.
- **Encode cache:** keyed by (tile, version, codec, quality), shared by all
  viewers at the same quality (see session-core pacing).
- **Parallelism:** encode the tiles of one frame across a worker pool
  sized to `GOMAXPROCS`, bounded per session so one busy session cannot
  starve others.

## Options

### Lossless codec

- **A. Palette or raw pixels plus `deflate-raw` (recommended for beta).**
  The tile becomes a palette with indexed pixels (≤256 colours), or raw
  pixels otherwise, deflated at a fast level.
  - Pros: `compress/flate` on the server; native `DecompressionStream` in
    browsers; trivial in the Go client core; text and UI compress very
    well.
  - Cons: worse than PNG filters on gradients; needs a small custom
    container format.
- **B. PNG.**
  - Pros: universal decoding.
  - Cons: `image/png` encoding is slow compared with A at equal
    compression; each tile pays header and CRC overhead.
- **C. QOI.**
  - Pros: very fast to encode and decode.
  - Cons: no native browser decoder (needs about 100 lines of JS or WASM);
    compresses UI content less well than palette plus deflate.
- **D. Lossless WebP (VP8L).**
  - Pros: browsers decode it natively; smaller than PNG on screen
    content, by the encoders' own figures.
  - Cons: the pure-Go encoders are young; encoding costs more than
    palette plus deflate.

### Lossy codec

- **A. JPEG via `image/jpeg` (recommended).** Native decoding everywhere.
  Quality is adjustable per viewer.
- **B. WebP lossy.** Better quality per byte than JPEG, and decoded
  natively by browsers. Pure-Go encoders appeared in 2026
  (`deepteams/webp`, `gen2brain/vpx/webp`); the measurement compares them
  with JPEG (standard library and jpegn) for speed and size at equal
  SSIM.

### Pixel layout

- **A. RGBA (Go `image.RGBA`, browser `ImageData`).** No swizzle in the
  web client or in Go.
- **B. BGRX little-endian.** Matches the common RFB true-colour format and
  CoreGraphics' native order, so VNC and Swift avoid a swizzle.

### Video tier (after 1.0)

A video codec for high-motion regions with WebCodecs decoding.
`github.com/gen2brain/vpx/vp8` is a pure-Go VP8 encoder with inter
frames whose output libvpx decodes, and WebCodecs decodes VP8 in Chrome
and Firefox; Safari's support needs a runtime check. It has no rate
control and no published realtime speed, so it needs a benchmark before
it is a candidate. H.264 without cgo would mean OpenH264 loaded through
purego, downloaded by the user under Cisco's binary licence, or the
operating system's hardware encoders. Kept out of beta; protocol v1 reserves a
codec ID range for it.

## Decisions

Settled on 2026-10-30. Items 1 and 2 were the owner's calls.

1. **Codecs stay pure Go.** Palette or raw pixels with deflate, PNG
   where it wins, and JPEG, all encoded in Go. The video tier stays after
   1.0, so the G5 bytes target (about 5 KiB a frame) is expected to be
   missed, and it will be revised to what adaptive JPEG reaches, with the
   measurement as evidence. Latency and frame-rate targets stay.
2. **Quality adapts per viewer in beta,** as above, rather than a fixed
   quality per session: motion loses detail or rate before input loses
   responsiveness.
3. **Content picks the codec and motion picks the quality**, with
   refinement to exact pixels for low-colour tiles and to a perceptual
   quality for photographic ones.
4. **Scroll and move detection and the client tile cache are in beta,**
   since G3 and G4's targets need them.
5. **No server-side downscaling in beta.** Encoding only the visible
   viewport covers small screens; scaling can come later as a capability.
6. **The lossless codec, the tile size and the pixel layout** (questions
   1 to 3 below) are decided by the first task's measurement on recorded
   frames from the bench desktop's scenes, not in advance. Lossless and
   lossy WebP join that measurement (added 2026-10-30), since pure-Go
   encoders for them now exist; any of them keeps decision 1.

## Open Questions

1. Lossless codec A, B or C? Benchmark A against B on a corpus of
   recorded VNC frames (desktop, terminal, browser, photo) before
   deciding.
2. Tile size 64 or 128? Smaller tiles mean finer damage but more
   per-tile overhead.
3. Pixel layout A or B? The swizzle happens once per decoded tile in one
   place either way.

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Framebuffer | `internal/framebuffer`: RGBA pixels in square tiles, versions from one counter, a lock per row of tiles, drawing and CopyRect for backends, tile reads and a change signal for writers; the tile size is a parameter | — |
| Measurement | A corpus of frames recorded from the bench desktop's scenes (desktop, terminal, browser, photograph, scroll), and benchmarks on Go 1.27 that settle questions 1 to 3 and the lossy codec: palette or raw pixels with deflate, PNG, lossless WebP, JPEG (standard library and gen2brain/jpegn) and lossy WebP, scored for size, encode time and SSIM | — |
| Codecs | `internal/encoder`: classification by colour count, the lossless codec the measurement picked, JPEG at a quality, and their codec IDs in protocol v1 | Measurement |
| Refinement | Motion heat per tile, and refinement after a still delay: exact for low-colour tiles, a quality chosen by SSIM for photographic ones | Codecs |
| Scroll detection | Row hashes per tile column (xxh3-64), the offset search, and `copy-rect` with tiles for the exposed strip | Framebuffer |
| Tile cache | Content hashes (xxh3-128 from zeebo/xxh3, so collisions are out of reach), the server's mirror of each client's cache, and the `cached` codec | Codecs, protocol v1 Messages |
| Encode cache and workers | Encoded tiles cached by tile, version, codec and quality and shared by viewers; a worker pool bounded per session | Codecs |

## Affects

New `internal/framebuffer` and `internal/encoder`. Fixes the codec IDs in
[protocol-v1](protocol-v1.md) and the decoders in
[client-core](client-core.md) and [009](../009-react-web-client.md).
