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
  - internal/codec/
  - internal/encoder/
  - internal/stream/
  - protocol/
effort: large
created: 2026-10-29
updated: 2026-10-31
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
  together as one rectangle, a run of tiles along a row, so that the
  lossy format's headers are paid once per rectangle. Tiles stay 64
  pixels for damage and classification. (The Codecs task merges runs
  along rows only; blocks are not built, see its notes.)
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
  (Built 2026-10-31 for vertical offsets, with the hashes kept per
  viewer against what its client shows rather than by the framebuffer,
  and backend CopyRect not yet forwarded: see Implementation notes:
  Scroll detection.)
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
   (Amended 2026-10-30 by the measurement: PNG wins only on still tiles
   of 17 to 256 colours with smooth gradients, by 3 % (2 % over that
   class, 3.28 bits a pixel against 3.35), and loses on all other
   content sent lossless, so occamyd does not send it in beta; the lossy
   codec is WebP with JPEG as the fallback, decision 10. The G5 proposal
   is in the implementation notes.)
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
   antialiased text but 9 to 21 times slower, so it is left for
   refinement, after its own measurement; QOI is 1.4 to 3.9 times
   larger.
8. **Tiles are 64 pixels, and lossy tiles are merged into rectangles.**
   At 64 a key costs 0.1 to 1.7 KiB against 0.2 to 2.9 at 128, a
   scrolled line after a copy 4.6 KiB against 8.7 (G3's target is 7.1),
   and the browser page sends 6 % of its area lossy against 42 %. Full
   frames cost more at 64: 1.5 to 2.2 times the bytes on the desktop,
   the terminals, the browser and the drag (the desktop 15.1 KiB against
   6.9), and 1.06 to 1.3 times on the scroll, the photograph and the
   plasma. That cost is lossless, since every tile carries its own
   palette and starts deflate afresh, and merging lossy tiles does not
   recover it; 64 is chosen for damage, which a session mostly sends.
   Merging is still worth it for lossy tiles, whose headers are paid per
   tile, about 650 bytes a JPEG: the motion scene at JPEG quality 30
   takes 137 KiB as tiles and 33 KiB as rows of tiles. The `tile`
   message already carries any size up to the maximum texture size.
9. **Pixels are RGBA** (option A), in the framebuffer, in raw codec 1
   and in what the `stream.Encoder` interface takes. x11vnc sends RGBX
   when asked, the Go encoders take `image.RGBA`, browsers' `ImageData`
   is RGBA, and a swizzle would cost 3.3 µs a 64-pixel tile. The native
   VNC backend asks for this order and makes the padding opaque alpha. A
   native RDP backend would receive blue-first bitmaps and swizzle as it
   draws, once per changed pixel rather than per encode; that is for its
   design to weigh, not a reason to change the layout.
10. **Lossy tiles are WebP from `gen2brain/vpx/webp` at method 0** on one
    goroutine, as codec 5, and JPEG from `gen2brain/jpegn` (baseline,
    4:2:0) as codec 4 for a viewer that lists JPEG but not WebP. At
    equal SSIM WebP takes 0.35 to 0.54 of JPEG's bytes on 64-pixel tiles
    (110 against 233 KiB for the photograph at 0.90) and 0.65 on
    128-pixel ones, with 53 bytes of fixed cost a tile against 656. At
    that SSIM it takes about twice `image/jpeg`'s encode time (1.9 to 2.1
    times) and 6.5 to 7 times jpegn's: 18 ms of one core for the
    photograph's full screen at 0.90, against 9.2 and 2.7. `jpegn` writes
    the same quality as `image/jpeg` within 5 % of its bytes in a third
    of the time. What would flip this: CPU becoming the server's limit
    before bandwidth, or browsers' WebP decoding proving slower than
    JPEG on low-end clients, which the measurement did not cover.

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
| Codecs (done 2026-10-30) | `internal/encoder`, an implementation of `stream.Encoder`: classification by colour count (up to 16, 17 to 256, above 256); the palette or raw container with `compress/flate` at level 1 as `deflate` (2); raw RGBA (1); lossy WebP from gen2brain/vpx/webp at method 0 (5), and JPEG from gen2brain/jpegn (4) for viewers that list JPEG but not WebP; adjacent lossy tiles of a frame encoded as one rectangle. Fixes the deflate container, including alpha for cursors, in [docs/protocol.md](../../docs/protocol.md), which marked it provisional. Adds vpx and jpegn to occamyd's `go.mod`, with the Go 1.26.4 they need, and their licence and PATENTS notices to the image's `licenses/` | Measurement |
| Refinement | Motion heat per tile, and refinement after a still delay: exact for tiles of up to 256 colours, which go lossy while they move if they have more than 16, and a quality chosen by SSIM for photographic ones; gives `stream.Encoder` the tile's motion | Codecs |
| Scroll detection (done 2026-10-31) | Row hashes per tile column (xxh3-64), the offset search, and `copy-rect` with tiles for the exposed strip. Built per viewer against a shadow of what its client shows, vertical offsets only, verified pixel for pixel; the corpus's scroll takes 5.0 KiB a frame against 58.5 ([notes](#implementation-notes-scroll-detection)) | Framebuffer |
| Tile cache | Content hashes (xxh3-128 from zeebo/xxh3, so collisions are out of reach), the server's mirror of each client's cache, and the `cached` codec | Codecs, protocol v1 Messages |
| Encode cache and workers | Encoded tiles cached by tile, version, codec and quality and shared by viewers; a worker pool bounded per session | Codecs |

## Affects

New `internal/framebuffer`, `internal/codec` and `internal/encoder`.
Fixes the provisional codec layouts of [protocol-v1](protocol-v1.md)
(raw's channel order, the deflate container, whether WebP is sent) and
the decoders in [client-core](client-core.md) and
[009](../009-react-web-client.md). The measurement fixed raw's order as
RGBA and chose WebP for lossy tiles; the Codecs task fixed the deflate
container in [docs/protocol.md](../../docs/protocol.md#the-deflate-container),
with golden payloads of every codec in
`protocol/testdata/codecs.json`.

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
- `bench/codecs`, a Go module of its own: tile classification, the
  palette container, QOI, SSIM and the candidates, with unit tests, and
  the measurement behind the `measure` build tag, which `make codecs`
  runs and which writes `codecs.json`, `codecs-tables.md` and
  `codecs-load.log`, the host's load average every 30 seconds. CI's go
  job vets it with the tag and runs its tests.

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
- `bench/codecs` has its own `go.mod` (module
  `changkun.de/x/occamy/bench/codecs`, with a `replace` of the root to
  `../..`, so it still imports `internal/framebuffer`). Its dependencies
  (`deepteams/webp`, `gen2brain/jpegn`, `gen2brain/vpx`,
  `klauspost/compress`) are pure Go, and gen2brain's need Go 1.26.4, so
  occamyd's `go.mod` keeps its Go version and gains nothing until the
  Codecs task links vpx and jpegn. The first version of this work bumped
  the root module instead; review moved it out, since every builder of
  occamyd would have needed the newer Go and its image build fetched
  about 54 MB it never linked.

**Surprises.**

- Colour count alone sends the motion scene lossless at 388 KiB a frame,
  eight times guacd's baseline, because its plasma draws from 256
  colours.
- A JPEG's tables cost about 650 bytes a tile, so lossy tiles are
  merged into rectangles. Full frames still cost 1.5 to 2.2 times more
  at 64 than at 128 on most screen content, from the per-tile palette
  and deflate context; the first version of these notes put that on the
  lossy headers, which review corrected.
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
  for alpha. (Done: forms 2 and 3.)
- The Codecs task ships the licence and PATENTS notices of
  `gen2brain/vpx` and `gen2brain/jpegn` in the image's `licenses/`, as
  docker/Dockerfile does for Guacamole's. (Done.)
- Decision 9 assumes VNC and the guacd bridge as sources. A native RDP
  backend receives blue-first bitmaps; its design should measure the
  swizzle on drawing before it asks for another layout.
- Encoding a full frame's adjacent lossless tiles together, where their
  union keeps to 256 colours, could recover part of what 64-pixel tiles
  cost on full frames; not measured.
- Lossless WebP for refining still antialiased text, and the vp8 video
  tier, each need a measurement of their own.
- Browsers' decode times (WebP, JPEG, and inflating plus expanding the
  palette in JavaScript) are not measured; the Go client's are.
- Classification takes 6 to 14 µs a 64-pixel tile, 1.2 to 2.7 ms a full
  frame on one core, as much as encoding simple content; it should run
  once per tile version and be shared by viewers, like encoded tiles.
- SSIM was measured on luma only, so chroma subsampling's cost to
  coloured text is not in the scores.
- G4 needs move detection: without it, a drag update costs 12 KiB at 64
  pixels with WebP for its lossy tiles (36 with JPEG), against a target
  of 3.9.

**G5's bytes, a proposal.** [docs/benchmarks.md](../../docs/benchmarks.md#targets)
sets 4.9 KiB a frame for G5 over VNC, 10 times below guacd's 49, and
decision 1 expected it to be missed. Measured on the motion scene, the
best pure-Go result is lossy WebP at quality 30: 11 KiB a frame as one
image and 14 KiB as rows of tiles, at an SSIM of 0.989; JPEG from
`jpegn` at the same SSIM (quality 50) takes 32 and 38 KiB. The proposal
is to revise G5's bytes target
to 14 KiB a frame over both VNC and RDP, what rows of WebP tiles reach,
since occamyd's encoding of the same scene does not depend on the
backend. The targets table stays unchanged until the owner decides.
G6's 139 KiB stands at equal SSIM: on the photograph's 64-pixel tiles
JPEG at quality 70 scores 0.871 in 175 KiB, over the target, and WebP
reaches 0.871 at about quality 78 in about 75 KiB.

**Review fixes, 2026-10-30.** An independent review reproduced every
byte and SSIM and found the method sound; it led to these changes,
measured again on 2026-10-30 at 19:31 UTC (same bytes and SSIM; times
within 5 % for 95 % of the sums per codec and class, within 10 % for
98 %):

- `bench/codecs` became its own module, with `make codecs` and a CI
  step; occamyd's `go.mod` is master's again.
- PNG decoding expands the palette through a table instead of
  image/draw, which had cost more than the decoding; Go decodes A in a
  quarter to 95 % of PNG's time, depending on the colours.
- The damage figures use WebP, the chosen codec, and JPEG from jpegn
  beside it; the browser's key tile at 128 has 257 colours and goes
  lossy.
- Times are quoted at equal SSIM where bytes are: WebP takes about
  twice image/jpeg's encode time there, not 1.6 to 1.8 times, which
  holds only at equal settings.
- The raw layouts take turns going first, and the measurement logs the
  load average itself.
- Decisions 1, 7, 8, 9 and 10 and the G6 claim were corrected as above;
  docs/protocol.md says occamyd sends only `raw` until the Codecs task
  lands. (It has: docs/protocol.md now says what occamyd sends in each
  codec.)

## Implementation notes: Codecs

Done on 2026-10-30.

**What was built.**

- `internal/codec`, with the standard library only, so that a client
  decodes without linking the lossy encoders: raw pixels both ways; the
  palette, counted with an early exit at the 257th colour; and the
  deflate container, written at `compress/flate` level 1 with a
  compressor reused between tiles and read by `Inflater.Decode`, which
  inflates straight into the destination image, row by row, and refuses
  anything the protocol does not allow: a form other than 0 to 3, a
  container longer or shorter than the rectangle, an index beyond the
  palette, bytes after the DEFLATE stream. It inflates at most one byte
  past the rectangle's longest container, `max(1 + 4wh, 1026 + wh)`
  bytes (raw pixels with alpha, or 256 colours with alpha and a byte an
  index), and allocates nothing once warm. Three fuzzers (any payload,
  counting the bytes inflated; any payload against a plain decoder
  written from docs/protocol.md; round trips of any pixels) found
  nothing in 30 to 60 seconds each.
- `internal/encoder`, `stream.Encoder`'s implementation. `New` takes the
  codecs a viewer agreed to and picks deflate or else raw for lossless
  tiles, and WebP or else JPEG for lossy ones. `Tile` counts a tile's
  colours (`Classify`: up to 16, 17 to 256, more), encodes it lossless
  or reports it lossy; `Lossy` encodes a lossy tile, or a run of them,
  with `gen2brain/vpx/webp` at method 0 on one thread or `gen2brain/jpegn`
  at 4:2:0, opaque whatever its alpha; `Cursor` takes the cursor as an
  `image.RGBA` holds it, premultiplied, and encodes it exactly with
  straight alpha, converted as `color.NRGBAModel` converts.
- `internal/stream`: `Config.NewEncoder` gives each viewer its own
  encoder for the codecs it agreed to, internal/encoder's by default,
  and the server offers `webp`, `jpeg`, `deflate` and `raw` unless told
  otherwise. A graphical viewer that agrees to none of them is closed
  with `unsupported`. The writer merges a frame's lossy tiles into runs:
  consecutive dirty tiles in one row of tiles, at most
  `encoder.MaxRun` (16) of them, at most the client's texture size wide,
  and halved until the message fits the agreed message size. The
  lossless tile that ends a run is encoded once, kept, and sent as the
  next message, unless an input's effect takes the frame's turn first or
  a newer version of the tile was seen since. In an input's frame a run
  ends where a drawing operation does, so that the frame can end before
  one that would take it past its 16 KiB budget. The cursor goes in the
  viewer's codecs too. The debug log gives each frame's encode time.
- docs/protocol.md fixes the deflate container (forms 0 to 3: palette
  or raw, opaque or with alpha) and says what occamyd sends in each
  codec; its two container examples are checked by `go test ./protocol`.
  `protocol/testdata/codecs.json` holds 20 payloads, of each codec
  occamyd sends and each container form, with their pixels, three of
  them containers occamyd does not send but a receiver takes (one at the
  cap, padding bits set, a colour listed twice), and 21 deflate payloads
  a receiver refuses; `go test ./internal/encoder -run TestGoldenCodecs
  -update` writes it. Lossless vectors decode exactly.
  Lossy ones carry a floor on PSNR 3 dB under what the reference
  decoders reach (vpx's for WebP, which decodes as libwebp does, and
  `image/jpeg`), since decoders differ in rounding and in upsampling
  chroma.
- `go.mod` needs Go 1.26.4 and gains `gen2brain/vpx` v0.2.1 and
  `gen2brain/jpegn` v0.6.2, both pure Go: every package builds with
  `CGO_ENABLED=0` for the release's Linux, macOS and Windows on amd64
  and arm64, for Linux on 386, arm, ppc64le, riscv64 and s390x, for
  FreeBSD, and with `-tags noasm`. The image's `licenses/` carries
  vpx's COPYING, COPYING.webpkit and PATENTS, jpegn's LICENSE, and
  jpegli's BSD-3 notice from `third_party/jpegli`, since jpegn's adaptive
  quantisation and progressive scans are ported from jpegli;
  `THIRD_PARTY_LICENSES` now takes PATENTS files, Go's and
  golang.org/x's among them, and jpegli's notice after jpegn's once
  occamyd links jpegn.

**Decisions.**

- **Rows, not blocks.** The writer sends tiles in index order, so a run
  along a row needs one tile of lookahead, while a block needs the rows
  below classified before the first is sent. Rows already reach the
  measurement's numbers (below); blocks would save a few headers more
  on large photographic areas.
- **At most 16 tiles a run,** 1024 pixels: rows of 16 were what the
  measurement measured, and the cap bounds the pixels a lossy encode
  takes at once (256 KiB) and the time a frame's first message waits.
- **Quality is the pacer's** (30 to 90, starting at 90) for both WebP
  and JPEG, though the two scales differ (WebP scores lower at 30 and
  70 and higher at 90). Choosing a quality by SSIM is the Refinement
  task's.
- **A viewer's order of preference is ignored**: the measurement ranks
  the codecs of each kind. A viewer with no lossy codec gets every tile
  lossless, photographic ones as deflate's raw form; one with no
  lossless codec gets every tile lossy. The cursor is deflate, else
  raw, else lossless WebP; a viewer that agrees only JPEG gets no
  cursor and draws its own pointer.
- **Tiles of 17 to 256 colours are lossless** until the Refinement task
  gives the encoder a tile's motion, so the motion scene costs 388 KiB
  a frame for now, as the measurement found.
- **Classification is per viewer**: each writer counts its tiles'
  colours. Sharing it, and the encoded tiles, is the Encode cache's.
- **`internal/codec` is a package of its own** rather than part of
  internal/encoder, so that `client/` imports the decoders without vpx
  and jpegn.

**Deviations from the task.**

- `stream.Encoder` changed shape. It was one `Encode(dst, pix, w, h,
  quality)` shared by every viewer, with raw as the default; it is now
  `Tile`, `Lossy` and `Cursor`, one encoder per viewer from
  `Config.NewEncoder`, since the codecs depend on what the viewer
  agreed and the encoder keeps buffers and compressors.
- Lossy tiles are merged along rows only, as above, not into blocks.
- CI's licence check (`go-licenses check`) covers `./...` instead of
  occamyd's main package: occamyd does not link `internal/stream` until
  it serves protocol v1, so vpx and jpegn are not yet in its binary or
  in the release archives' `THIRD_PARTY_LICENSES`, and a check of `.`
  would not see them. The image ships their notices already, copied from
  the module cache in the build stage.

**Measured encode times.**

One core (`GOMAXPROCS=1`), at quality 70, classification included:
the median of five passes over each frame, summed over a scene's frames
and divided by their number (`go test ./internal/encoder -run
TestCorpusTimes -corpus.runs 5`), on 2026-10-30 at 22:29 under a load
average of 9.8 over a minute (18 over five) on 18 cores. The
measurement ran under 5 to 9, and left classification out of its encode
times.

| Scene | KiB a frame: WebP rows · JPEG rows | ms a frame: WebP rows · WebP tiles · JPEG rows | Measurement, WebP tiles: KiB · ms |
|---|---|---|---|
| desktop | 15.1 · 15.1 | 3.0 · 2.8 · 3.0 | 15.1 · 1.4 |
| terminal | 17.4 · 17.4 | 2.7 · 2.8 · 2.6 | 17.4 · 1.5 |
| terminal-aa | 103.2 · 103.2 | 5.1 · 4.8 · 4.8 | 103.2 · 3.9 |
| browser | 138.8 · 144.2 | 7.1 · 7.1 · 5.8 | 140.1 · 6.1 |
| photo | 31.1 · 70.7 | 12.4 · 13.2 · 2.9 | 44.1 · 12.4 (rows: 31 · 9.8, jpegn 71 · 1.8) |
| drag | 22.0 · 30.0 | 4.5 · 5.0 · 3.1 | 24.5 · 3.7 |
| scroll | 55.4 · 55.4 | 4.4 · 4.5 · 4.6 | 55.4 · 3.6 |
| motion | 388.1 · 388.1 | 9.4 · 9.0 · 8.7 | 388.1 · 8.0 |

- The bytes are the measurement's: to the 0.1 KiB on lossless content,
  31.1 KiB against 31 for the photograph as rows of WebP, and 70.7
  against 71 as rows of JPEG. Rows take 29 % less than single tiles on
  the photograph and 10 % less on the drag; on the browser, whose 12
  lossy tiles are a small part of the frame, 1 % less.
- The times are the measurement's plus 0.8 to 2.6 ms a frame, about
  what classification cost it (1.2 to 2.7 ms a frame); the photograph
  as rows of WebP, 2.6 ms over, is more than its classification, which
  stops early, explains, and is not accounted for. Scenes without
  lossy tiles take the same time in every mode within 8 %, as they
  should.
- `BenchmarkFrame -cpu 1`, which times whole frames with their pixel
  copies, gives medians over five counts from 4 % below these to 24 %
  above (antialiased text). Seven runs earlier, under load averages of
  15 to 35, gave medians up to 30 % higher (the photograph as WebP
  rows: 16.0 ms), so these times are indicative; the bytes are exact.
- A viewer encodes on one core at most: a full-screen photograph
  changing 30 times a second would take 37 % of a core as WebP and 9 %
  as JPEG, and the plasma, lossless for now, 27 %.
- A viewer's warm encoder holds about 870 KiB (WebP and deflate, after
  a text tile, a photographic tile and a run of 16), and its writer
  about 0.5 MiB more of run buffers: 16 tiles' pixels and the run's
  assembled pixels.

**Surprises.**

- vpx unpremultiplies every pixel of an `image.RGBA`, with a division;
  tiles are opaque, so the encoder hands it the same pixels as an
  `image.NRGBA`, which it copies as they are.
- The photograph at the pacer's starting quality of 90 takes 151 KiB a
  frame as rows of WebP (175 as JPEG), over G6's 139; at 70 it takes 31.
  G6 depends on the quality the pacer settles at, or the one refinement
  picks.
- The first golden vectors' floors on PSNR were 10 dB too low: the
  tests decoded WebP with `golang.org/x/image/webp`, which reads VP8's
  limited-range colours as full-range, as the guacd bridge's
  conformance work found at the same time. They decode with vpx now,
  as the measurement did, so its scores stand.
- The measured container needed only its form byte widened for alpha:
  opaque tiles cost the same bytes as in the measurement.
- Two of the run's conditions, a gap in the dirty tiles and the end of
  a row, survived a mutation check against the first tests; the tests
  now cover both.
- An independent review found four bugs the tests missed, each with a
  regression test now that fails without its fix. The tile read ahead at
  the end of a lossy run went out as an input's effect, though the input
  had repainted it since. An input's frame carried a whole drawing
  operation past its budget when a run crossed into it (320 KiB raw
  against 16). Cursors arrive premultiplied and went out labelled
  straight, so guacd's white at half alpha drew grey. The documented cap
  on inflation, `1 + 4wh`, was wrong: a palette of 256 colours exceeds
  it, and so did occamyd's own container of a 1×64 edge tile of 64
  colours (258 bytes against 257). Framebuffer.Resize, which the review
  took for transparent black, was already opaque (`draw.Draw` with
  `image.Black`); a test pins it.
- A mutation check after the fixes killed 16 of 21 mutants. The five
  that survive change no output: the end-of-frame lookahead reset, the
  reset where an input's effect starts, and the version check each
  cover for the others (removing the version check with either reset
  fails a test); a run's check of the screen's bounds, beside its check
  of the tile's rectangle, matters only for a tile that a resize leaves
  in place, which the client draws again after the resize; and a
  container longer than the rectangle is refused by the next case when
  its own is removed.

**Follow-ups.**

- Refinement: `stream.Encoder` gains the tile's motion, `Tile` sends 17
  to 256 colours lossy while they move, and a still tile is resent
  exact, or at a quality chosen by SSIM. Lossless WebP for still
  antialiased text needs its measurement first. G6: the photograph at
  quality 90, where the pacer starts, takes 151 KiB a frame as rows of
  WebP against G6's 139; picking the quality by SSIM is how Refinement
  meets it.
- jpegn's repository does not carry the notice of jpegli, from which its
  adaptive quantisation (`adaptive.go`) and progressive scans are ported;
  occamy ships jpegli's notice itself, and asking jpegn to add it is the
  owner's.
- Tile cache: `cached` (6) is not in `encoder.Codecs()`. A cached tile
  is looked up before classification, and a run of lossy tiles is
  broken at a cached one.
- Encode cache and workers: classification and payloads keyed by tile,
  version, codec and quality, shared across viewers; a run's payload
  depends on which neighbours are dirty, so the cache holds single
  tiles, or runs keyed by their tiles' versions. Today a viewer encodes
  on its writer's goroutine, one core at most, with no bound per
  session.
- client-core (#107, merged meanwhile): wiring these decoders in as
  `client`'s default `Options.Decoders` is a PR of its own, not this
  one, since it changes what a client with default options offers and
  links vpx into the public client package; client-core's follow-ups
  describe it. `codec.Inflater.Decode(dst *image.RGBA, payload)` and
  `codec.DecodeRaw` decode `deflate` and `raw` straight into a
  `SubImage` of the client's screen, one Inflater per decoding
  goroutine. `gen2brain/vpx/webp` decodes WebP as libwebp does (with
  `ToRGBA` for lossy images; lossless ones, cursors, come back as
  `image.NRGBA`), and `image/jpeg` decodes JPEG; `golang.org/x/image/webp`
  drawn as it is gets VP8's colour range wrong and fails the golden
  vectors. Before it decodes WebP or JPEG, the client checks the size
  the header claims against the rectangle (`DecodeConfig`, or vpx's
  `FrameSizeLimit`), as docs/protocol.md now requires and the tests'
  `decodeLossy` helpers do. A tile message may cover up to 16 tiles of
  a row. A cursor's pixels are straight alpha, so the client views them
  as `image.NRGBA`. Its tests run `protocol/testdata/codecs.json`.
- 009: inflate with `DecompressionStream('deflate-raw')`, read the
  container as docs/protocol.md describes, in a Worker, and decode WebP
  and JPEG with `createImageBitmap`; its tests run `codecs.json` too,
  exact for lossless vectors, above `min_psnr` for lossy ones, refusing
  every invalid one.
- Browsers' decode times are still unmeasured.

## Implementation notes: Scroll detection

**Status.** Built and measured on 2026-10-31. A viewer's writer gets it
as soon as occamyd serves protocol v1, which does not yet link
`internal/stream`. `stream.Config.NoScroll` turns it off.

**What was built.**

- **The previous screen is each viewer's own.** A `copy-rect` copies the
  pixels a client already has, so each writer keeps a *shadow* of what
  its client shows (`internal/stream/scroll.go`). It holds:
  - the pixels of every tile the writer sent, taken when the tile's
    message is written;
  - every copy the writer sent, applied in the order the client applies
    them;
  - which tiles the client has drawn since the last resize, before which
    its screen is undefined;
  - each tile's row hashes, xxh3-64 from `zeebo/xxh3`, computed when a
    search first needs them and dropped when the tile is sent again.

  The shadow takes the buffers the tiles were read into and gives back
  its old ones, so no pixels are copied. Versions cannot stand in for
  it: the framebuffer holds only the current pixels, and in a scroll the
  source rows have changed as well. With a shadow, three cases need
  nothing of their own:
  - a viewer that skipped frames, on a slow link or by stalling;
  - a viewer that was hidden;
  - a viewer whose tiles outside its viewport are stale.
- **The search, once a frame** (`scroller.find`):
  - **Quick reject.** The frame's dirty tiles are split into runs down
    each tile column. Only runs of at least two tiles are searched, so a
    key, a caret or a button never reaches the hashing. The longest
    runs go first.
  - **Vote.** Every eighth row of a run, starting four rows in, is hashed and
    looked up in an open-addressing table of the shadow's row hashes for
    that column. Each row found votes for its distance. Three kinds of
    row do not vote: one that has not changed, one found at its own
    place, and one whose hash occurs twice in the column, such as a
    blank line. The offset with the most votes wins if it has at least
    two. A plurality is enough, because two panes in one column may
    scroll by different amounts and every offset is verified anyway.
  - **Verify.** `try` finds the longest span of the run's rows that equal
    the shadow's rows at that offset, pixel for pixel (`bytes.Equal`).
    It also finds the tiles the copy completes: those whose rows outside
    the span already equal what the client has, in a tile the client
    has drawn. A hash collision can cost a search but never a wrong
    pixel.
  - **Hint.** The offset of the last copy found is tried first in the
    next run, so a scroll of the whole screen votes once.
  - **Miss limit.** The search stops after three runs that found no
    offset while nothing has been found.
  - **Order.** Within a column, a copy must not read rows that an
    earlier copy wrote, in the order they are sent.
  - **Merge.** Copies of adjacent columns with the same rows and offset
    are merged into one `copy-rect`.
- **Emission** (`writer.sendFrame`):
  - The search runs before the first tile of the frame's order. It runs
    again when an input's effect takes the frame over.
  - Its copies go out before the frame's tiles. They are unpaced, as
    `frame-end` is, since each is 20 bytes.
  - A tile a copy completes is marked sent at the version read during
    the search. In an input's frame it is also marked as the input's
    effect. The frame skips such tiles, and a run of lossy tiles ends
    before one.
  - In an input's frame, a copy that completed tiles carries the input's
    effect: the frame reports the input, and the copy's bytes count
    toward the frame's 16 KiB budget. A wheel whose effect is a copy
    alone is therefore resolved by it.
  - The debug log gives each frame's copies and search time.
- **Tests:**
  - **Detection** (`scroll_test.go`):
    - offsets of 1, 13, 63, 64, 65, 100 and 300 rows up, and of 1, 13,
      64 and 200 down;
    - partial widths;
    - repeated rows, as stripes, which must not vote;
    - a hash forced to collide (the first pixel only);
    - edge tiles smaller than 64;
    - resizes, and sources and destinations the client has not drawn;
    - copies that conflict within a column;
    - the bounds, counted as rows hashed, runs searched and offsets
      tried;
    - a property test: 40 seeds of a scrolling sidebar and pane, after
      which the client must equal the server and the shadow the client.
  - **Through the writer** (`scroll_writer_test.go`), each compared
    exactly with the server's screen:
    - remote copies and repaints;
    - a client that stalled, was hidden, or has a viewport;
    - two viewers on links of different speeds;
    - a wheel, including one that arrives in the middle of a frame of
      motion;
    - a resize that keeps the grid;
    - a lossy run beside a copy;
    - a screen that scrolled again after its tiles were collected;
    - 8 seeds of random panes, copies, paints, resizes, hidden spells,
      viewports and stalls.
  - **On the corpus** (`scroll_corpus_test.go`): the bytes, below, and
    the timing.
- **Mutation check.** 24 mutants of the verification, the per-viewer
  base, the emission, the bounds and the hashes are all killed.
- **Licences.** `go.mod` gains `zeebo/xxh3` v1.1.0 (BSD-2-Clause), which
  links `klauspost/cpuid/v2` (MIT) into amd64 builds. The image's
  `licenses/` carries both, copied from the module cache as vpx's and
  jpegn's are. `scripts/third-party-licenses.sh` now lists every
  release platform's architectures as well as its systems, so cpuid's
  licence reaches `THIRD_PARTY_LICENSES` once occamyd links
  `internal/stream`, whichever host runs it.

**Measurements.** Results, method and load are in
[docs/benchmarks/scroll.md](../../docs/benchmarks/scroll.md). The
figures are medians, on one core of the bench host, under a load average
of 2.9 to 4.5:

| Scene | KiB a frame: without · with | encode ms a frame: without · with | search ms |
|---|---|---|---|
| scroll | 58.5 · **5.0** | 3.59 · **0.54** | 0.234 |
| scroll, skipping one | 58.6 · **5.0** | 3.62 · **0.53** | 0.233 |
| drag | 9.8 · 9.8 | 2.32 · 2.30 | 0.044 |
| motion | 393.8 · 393.8 | 8.35 · 8.62 | 0.068 |
| browser, whole | 143.0 · 143.0 | 6.97 · 6.82 | 0.062 |
| photo, whole | 31.3 · 31.3 | 10.96 · 11.21 | 0.065 |

- **Scroll.** Each update of the corpus's scrolled text is one copy and
  15 tiles. That is under G3's target of 7.1 KiB a frame, measured
  without the WebSocket's framing; G3 itself waits for occamyd to serve
  protocol v1.
- **Other scenes** send the same bytes, and the search costs 0.6 to 1.9 %
  of their encode time.
- **The search alone** (`BenchmarkScrollFind`, 1024×768) costs:
  - 62 ns for a change of one tile;
  - 136 ns for a line across a row of tiles;
  - 58 µs for a new page, given up after three columns;
  - 223 µs for a scroll of the whole screen.

**Decisions.**

- **The shadow costs memory: 4·W·H bytes a viewer,** 3 MiB at 1024×768,
  plus 8 bytes a row of each tile column for the hashes (96 KiB). The
  alternatives fail as follows:
  - Versions cannot locate moved content.
  - The frame last sent to everyone is wrong for a viewer that skipped
    it.
  - Recomputing what a client shows from its history would cost more
    than keeping it.
- **Vertical offsets only.** Row hashes find vertical moves cheaply.
  Horizontal ones would need column hashes, or a 2-D search per region,
  and the corpus has no horizontal scroll. The dragged window moves
  diagonally, so G4 is not helped.
- **Whole tile columns.** A copy spans whole tile columns. A column that
  straddles a pane's edge has pixels that do not move, so its tiles go
  as tiles, unless the part that does not move is uniform; a uniform
  part is the same at any offset and is rightly copied too.
- **Lossy pixels are copied as the client has them.** The shadow holds
  the pixels that were encoded, while a client holds the decoder's
  approximation. A copy moves that approximation, which is what sending
  the same pixels lossy again would show.
- **The search reads whole runs.** It reads the run's tiles, not only
  the rows it samples, because `try` compares every row and the
  framebuffer hands out tiles. The reads are most of a miss's cost.

**Deviations from the task.**

- **Hashes are kept per viewer, not by the framebuffer** as Proposed
  shape describes. They must be hashes of what each client has, and
  hashing a run only when it is searched costs less than hashing every
  drawing operation.
- **Backend copies are not forwarded.** The framebuffer's `CopyRect`
  still records damage, so a VNC CopyRect or a guacd `copy` reaches the
  writer as changed tiles, and the search finds it again (the writer
  tests do both). Forwarding a backend's copy needs every viewer's
  shadow to hold its source, which the shadow now makes checkable.
- **Horizontal scrolls are not detected**, as above.

**Surprises.**

- **The miss limit can hide a scroll.** A scroll beside a larger area of
  new content goes as tiles: the new content's columns are searched
  first, either as longer runs or further left, and the frame gives up
  before reaching the scroll. A writer test found this when two paints
  split across frames by chance. The limit stays, since motion would
  otherwise hash every column of every frame.
- **The first conflict check was too strict.** It compared each copy's
  whole reads and writes, and refused two copies of one column that the
  client applies safely one after the other. It now checks in the order
  the copies are sent.
- **The corpus harness's pacer cut frames under the race detector.** It
  measured a rate from acknowledgements that came at once, and could cut
  the first full frame, which left tiles undefined and no copy to find.
  The harness now never acknowledges, and fails if a frame leaves
  anything unsent.
- **Some mutants survived only because a flaky test killed them.** Before
  that test was fixed, it was killing them by chance. Those three
  mutants needed tests of their own:
  - a span that starts inside a tile the client has never drawn;
  - two coincidental rows that vote for two offsets;
  - a screen that moved between collection and search.

**Follow-ups.**

- **Horizontal and 2-D moves**, for G4's dragged window.
- **Forward backend copies**, a VNC CopyRect or a guacd `copy`, as
  `copy-rect` when every viewer's shadow holds the source, which saves
  the search.
- **Refinement must follow lossy pixels through copies.** A tile a copy
  completes may hold lossy pixels moved from elsewhere, and still needs
  refining.
- **Share the shadow, or its hashes,** between viewers that are caught
  up, and with the Encode cache. Today each viewer keeps its own.
- **Search more cheaply:**
  - read only the sampled rows until an offset wins, to make a miss
    cheaper;
  - keep a scroll frame's 168 allocations (the copies' tile lists) in
    the scroller.
- **One copy per column when columns differ.** Copies of adjacent
  columns merge only when their rows and offset are equal. A scroll
  whose span ends at a different row in each column, because content
  differs, sends a copy per column.
- **A frame the pacer cuts after a copy** shows the copied rows before
  the exposed strip's tiles. Tiles that a copy only partly covers show
  shifted rows above stale ones until the next frame. A frame without
  copies that is cut shows tearing of the same kind.
