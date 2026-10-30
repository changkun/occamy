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
- The measurement of 2026-10-30 compared these encoders on frames
  recorded from the bench desktop:
  [docs/benchmarks/codecs.md](../../docs/benchmarks/codecs.md).

## Proposed shape

- **Framebuffer:** RGBA pixels, one buffer per session, tiles of 64×64
  with a version each. A change gives the tiles it touches a new version
  from one counter for the screen, so a viewer's dirty tiles are those
  above the versions it last sent, and the tiles changed since an input
  are those above the counter's value when the input was applied. A
  CopyRect from the backend is forwarded as
  `copy-rect` when every viewer's last frame contains the source region;
  otherwise it degrades to damage.
- **Classification per tile: content picks the codec, motion picks the
  quality.** Count distinct colours with an early exit. Up to 16 colours
  (text, UI, flat gradients) the tile is always lossless. From 17 to 256
  (antialiased text, smooth gradients) it is lossless while still and
  lossy while it moves. Above 256 it is photographic and lossy. A lossy
  tile's quality is set by its motion (a heat counter decayed per
  second) and the viewer's link: low while it moves, high once it
  stops. guacd 1.6 chooses PNG or JPEG by timing alone
  (`src/libguac/display-worker.c`), so the same photograph cost 0.4 to
  2.2 MiB between runs of the baseline's G6.
- **Lossy rectangles:** a frame's adjacent lossy tiles are encoded
  together as one rectangle, a run of tiles along a row or a block of
  them, so that the lossy format's headers are paid once per rectangle.
  Tiles stay 64 pixels for damage and classification.
- **Refinement (build to lossless):** when a region stops changing for a
  short delay, it is resent at its final quality: exact for tiles of up
  to 256 colours, so text and UI always end up exact, and a high lossy quality
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

- **A. Palette or raw pixels plus `deflate-raw` (chosen; decision 7).**
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

- **A. JPEG via `image/jpeg` (the fallback, from `gen2brain/jpegn`;
  decision 10).** Native decoding everywhere. Quality is adjustable per
  viewer.
- **B. WebP lossy (chosen; decision 10).** Better quality per byte than
  JPEG, and decoded natively by browsers. Pure-Go encoders appeared in
  2026 (`deepteams/webp`, `gen2brain/vpx/webp`); the measurement compared
  them with JPEG (standard library and jpegn) for speed and size at
  equal SSIM.

### Pixel layout

- **A. RGBA (Go `image.RGBA`, browser `ImageData`; chosen, decision
  9).** No swizzle in the web client or in Go.
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
operating system's hardware encoders. Kept out of beta; protocol v1 reserves
codec IDs 0x80 to 0xFF for it.

## Decisions

Settled on 2026-10-30. Items 1 and 2 were the owner's calls.

1. **Codecs stay pure Go.** Palette or raw pixels with deflate, PNG
   where it wins, and JPEG, all encoded in Go. The video tier stays after
   1.0, so the G5 bytes target (about 5 KiB a frame) is expected to be
   missed, and it will be revised to what adaptive JPEG reaches, with the
   measurement as evidence. Latency and frame-rate targets stay.
   (Amended 2026-10-30 by the measurement: PNG wins nowhere the pipeline
   sends lossless, so occamyd does not send it, and the lossy codec is
   WebP with JPEG as the fallback, decision 10. The G5 proposal is in
   the implementation notes.)
2. **Quality adapts per viewer in beta,** as above, rather than a fixed
   quality per session: motion loses detail or rate before input loses
   responsiveness.
3. **Content picks the codec and motion picks the quality**, with
   refinement to exact pixels for low-colour tiles and to a perceptual
   quality for photographic ones. (Amended 2026-10-30: content and
   motion together pick the codec for tiles of 17 to 256 colours, which
   are lossless while still and lossy while they move. The motion scene
   has at most 256 colours in every tile, and sent lossless it costs 388
   KiB a frame against 26 KiB as lossy WebP at an SSIM of 0.989. Tiles
   of up to 16 colours are always lossless, where lossy is 5 to 10 times
   larger.)
4. **Scroll and move detection and the client tile cache are in beta,**
   since G3 and G4's targets need them.
5. **No server-side downscaling in beta.** Encoding only the visible
   viewport covers small screens; scaling can come later as a capability.
6. **The lossless codec, the tile size and the pixel layout** (questions
   1 to 3 below) are decided by the first task's measurement on recorded
   frames from the bench desktop's scenes, not in advance. Lossless and
   lossy WebP join that measurement (added 2026-10-30), since pure-Go
   encoders for them now exist; any of them keeps decision 1. (Done
   2026-10-30: the answers are decisions 7 to 10, from
   [docs/benchmarks/codecs.md](../../docs/benchmarks/codecs.md).)
7. **Lossless is palette or raw pixels with deflate** (option A), with
   `compress/flate` at level 1, as codec 2 (`deflate`). On the tiles the
   pipeline sends lossless it is 2.1 to 2.5 times smaller than PNG on
   the desktop, the bitmap terminal and the dragged window, and 5 to 10
   % smaller on antialiased text, at two fifths to all of PNG's encode
   time, with 12 bytes for a solid tile against 88. Level 6 saves 3 to 6
   % at 1.3 times the time. Lossless WebP is 27 to 35 % smaller on
   antialiased text but 10 to 21 times slower, so it is left for
   refinement, after its own measurement; QOI is 1.4 to 3.9 times
   larger.
8. **Tiles are 64 pixels, and lossy tiles are merged into rectangles.**
   At 64 a key costs 0.1 to 1.7 KiB against 0.2 to 4.3 at 128, a
   scrolled line after a copy 4.6 KiB against 8.7 (G3's target is 7.1),
   and the browser page sends 6 % of its area lossy against 42 %. 128's
   advantage on full frames comes mostly from lossy headers paid per
   tile, about 650 bytes a JPEG, which merging adjacent lossy tiles
   removes: the motion scene at JPEG quality 30 takes 137 KiB as tiles
   and 33 KiB as rows of tiles. The `tile` message already carries any
   size up to the maximum texture size.
9. **Pixels are RGBA** (option A), in the framebuffer, in raw codec 1
   and in what the `stream.Encoder` interface takes. x11vnc sends RGBX
   when asked, the Go encoders take `image.RGBA`, browsers' `ImageData`
   is RGBA, and a swizzle would cost 3.2 µs a 64-pixel tile.
10. **Lossy tiles are WebP from `gen2brain/vpx/webp` at method 0** on one
    goroutine, as codec 5, and JPEG from `gen2brain/jpegn` (baseline,
    4:2:0) as codec 4 for a viewer that does not list WebP. At equal
    SSIM WebP takes half of JPEG's bytes or less on 64-pixel tiles (110
    against 233 KiB for the photograph at 0.90) and 53 bytes of fixed
    cost a tile against 656, at 1.6 to 1.8 times `image/jpeg`'s encode
    time and 5.5 to 6 times jpegn's. `jpegn` writes the same quality as
    `image/jpeg` within 5 % of its bytes in a third of the time. What
    would flip this: CPU becoming the server's limit before bandwidth,
    or browsers' WebP decoding proving slower than JPEG on low-end
    clients, which the measurement did not cover.

## Open Questions

1. ~~Lossless codec A, B or C? Benchmark A against B on a corpus of
   recorded VNC frames (desktop, terminal, browser, photo) before
   deciding.~~ Answered 2026-10-30: A, palette or raw pixels with
   deflate at level 1 (decision 7).
2. ~~Tile size 64 or 128? Smaller tiles mean finer damage but more
   per-tile overhead.~~ Answered 2026-10-30: 64, with adjacent lossy
   tiles encoded as one rectangle (decision 8).
3. ~~Pixel layout A or B? The swizzle happens once per decoded tile in
   one place either way.~~ Answered 2026-10-30: A, RGBA (decision 9).

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Framebuffer | `internal/framebuffer`: RGBA pixels in square tiles, versions from one counter, a lock per row of tiles, drawing and CopyRect for backends, tile reads and a change signal for writers; the tile size is a parameter | — |
| Measurement (done 2026-10-30) | A corpus of frames recorded from the bench desktop's scenes (desktop, two terminals, browser, photograph, drag, scroll, motion), and benchmarks on Go 1.27 that settle questions 1 to 3 and the lossy codec: palette or raw pixels with deflate, PNG, QOI, lossless WebP, JPEG (standard library and gen2brain/jpegn) and lossy WebP, scored for size, encode and decode time and SSIM; results in [docs/benchmarks/codecs.md](../../docs/benchmarks/codecs.md) | — |
| Codecs | `internal/encoder`, an implementation of `stream.Encoder`: classification by colour count (up to 16, 17 to 256, above 256); the palette or raw container with `compress/flate` at level 1 as `deflate` (2); raw RGBA (1); lossy WebP from gen2brain/vpx/webp at method 0 (5), and JPEG from gen2brain/jpegn (4) for viewers that do not list WebP; adjacent lossy tiles of a frame encoded as one rectangle. Fixes the deflate container, including alpha for cursors, in [docs/protocol.md](../../docs/protocol.md), which marks it provisional | Measurement |
| Refinement | Motion heat per tile, and refinement after a still delay: exact for tiles of up to 256 colours, which go lossy while they move if they have more than 16, and a quality chosen by SSIM for photographic ones; gives `stream.Encoder` the tile's motion | Codecs |
| Scroll detection | Row hashes per tile column (xxh3-64), the offset search, and `copy-rect` with tiles for the exposed strip | Framebuffer |
| Tile cache | Content hashes (xxh3-128 from zeebo/xxh3, so collisions are out of reach), the server's mirror of each client's cache, and the `cached` codec | Codecs, protocol v1 Messages |
| Encode cache and workers | Encoded tiles cached by tile, version, codec and quality and shared by viewers; a worker pool bounded per session | Codecs |

## Affects

New `internal/framebuffer` and `internal/encoder`. Fixes the provisional
codec layouts of [protocol-v1](protocol-v1.md) (raw's channel order, the
deflate container, whether WebP is sent) and the decoders in
[client-core](client-core.md) and [009](../009-react-web-client.md). The
measurement fixed raw's order as RGBA and chose WebP for lossy tiles;
the deflate container's exact layout is left to the Codecs task.

## Implementation notes: measurement

Done on 2026-10-30. The results, the method and the full tables are in
[docs/benchmarks/codecs.md](../../docs/benchmarks/codecs.md).

**What was built.**

- `bench/internal/rfb`, the RFB 3.8 client that `bench/vncprobe` used,
  shared, with a choice of RGBX or BGRX and the cursor pseudo-encoding,
  so that x11vnc does not paint the cursor into frames.
- `bench/vnccapture` and `make corpus`, which start the bench desktop
  image with dillo and xterm added, drive each scene over VNC, and save
  13 frames at 1024×768 as PNG to `bench/testdata/corpus` (2.5 MiB):
  desktop, terminal (bitmap font), terminal-aa (antialiased font),
  browser (a documentation page in dillo), photo, three consecutive
  updates each of drag and scroll, and two of motion. A sequence's frames
  are held in memory until it ends, so they are consecutive updates.
- `bench/codecs`: tile classification, the palette container, QOI, SSIM
  and the candidates, with unit tests that run in `go test ./...`, and
  the measurement behind the `measure` build tag, which writes
  `codecs.json` and `codecs-tables.md`.

**Decisions** are 7 to 10 above, with decision 3 amended for moving
tiles of 17 to 256 colours.

**Deviations from the task.**

- The corpus has three scenes more than planned: antialiased text,
  because xterm's bitmap font and the browser are not the same content
  class; the drag; and the motion, which turned out to decide decision
  3's amendment.
- QOI and `klauspost/compress` (flate and zstd) were measured alongside,
  and the lossy codecs were also measured on whole frames and rows of
  tiles, which is what decided the tile size.
- `go.mod` moves from Go 1.26.0 to 1.26.4, which gen2brain's modules
  require. The new dependencies (`deepteams/webp`, `gen2brain/jpegn`,
  `gen2brain/vpx`, `klauspost/compress`) are pure Go and used only by
  `bench/codecs`; occamyd links none of them yet.

**Surprises.**

- Colour count alone sends the motion scene lossless at 388 KiB a frame,
  eight times guacd's baseline, because its plasma draws from 256
  colours.
- A JPEG's tables cost about 650 bytes a tile, which made 128-pixel
  tiles look better than they are; merged rectangles remove it.
- Lossless WebP from `gen2brain/vpx` at method 0 is unusable on
  gradients (168 KiB for the desktop against 19 at method 4), and
  `deepteams/webp`'s method 4 is worse than its method 0 at equal SSIM.
- x11vnc sends red-first pixels on request, identical to its own
  blue-first ones, so BGRX saves VNC nothing.

**Follow-ups.**

- `stream.Encoder` takes pixels, size and quality only. Decision 3's
  amendment needs the tile's motion as well, or a quality that already
  encodes it; the Refinement task adds it.
- The measured container stores palette entries and raw pixels as RGB
  without alpha, while cursors use the same codecs with straight alpha.
  The Codecs task fixes the container in docs/protocol.md with a form
  for alpha.
- Lossless WebP for refining still antialiased text, and the vp8 video
  tier, each need a measurement of their own.
- Browsers' decode times (WebP, JPEG, and inflating plus expanding the
  palette in JavaScript) are not measured; the Go client's are.
- Classification takes 9 to 14 µs a 64-pixel tile, 1.1 to 2.6 ms a full
  frame on one core, as much as encoding simple content; it should run
  once per tile version and be shared by viewers, like encoded tiles.
- SSIM was measured on luma only, so chroma subsampling's cost to
  coloured text is not in the scores.
- G4 needs move detection: without it, a drag update costs 35 KiB at 64
  pixels with JPEG for its lossy tiles, against a target of 3.9.

**G5's bytes, a proposal.** [docs/benchmarks.md](../../docs/benchmarks.md#targets)
sets 4.9 KiB a frame for G5 over VNC, 10 times below guacd's 49, and
decision 1 expected it to be missed. Measured on the motion scene, the
best pure-Go result is lossy WebP at quality 30: 11 KiB a frame as one
image and 14 KiB as rows of tiles, at an SSIM of 0.989; JPEG from
`jpegn` takes 26 and 33 KiB. The proposal is to revise G5's bytes target
to 14 KiB a frame over both VNC and RDP, what rows of WebP tiles reach,
since occamyd's encoding of the same scene does not depend on the
backend. The targets table stays unchanged until the owner decides.
G6's 139 KiB stands: the photograph takes 44 KiB as WebP tiles at
quality 70.
