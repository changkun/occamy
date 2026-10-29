---
title: Graphics pipeline
status: drafted
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
updated: 2026-10-29
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

- Pure-Go encoders available without cgo: `image/png`, `image/jpeg` and
  `compress/flate` in the standard library. `golang.org/x/image/webp`
  decodes only, and there is no WebP encoder in `x/image`. libwebp and
  libjpeg-turbo would bring cgo back.
- Browsers decode PNG and JPEG natively (`createImageBitmap`, in a
  Worker), and inflate with `DecompressionStream('deflate-raw')` (Chrome
  103, Firefox 113, Safari 16.4).
- libguac's approach, for reference: a 64-pixel heat map on each surface
  picks lossy encoding for frequently updated regions and PNG otherwise.
- Backends: VNC delivers rectangles, already decoded, plus CopyRect. The
  guacd bridge delivers decoded image streams plus `copy` operations.
  Both write into the framebuffer that [session-core](session-core.md)
  owns.

## Proposed shape

- **Framebuffer:** 32-bit pixels, one buffer per session, tiles of 64×64
  with a version counter each. A backend damage report bumps the version
  of the tiles it touches. A CopyRect from the backend is forwarded as
  `copy-rect` when every viewer's last frame contains the source region;
  otherwise it degrades to damage.
- **Classification per tile:** count distinct colours with an early exit.
  At or below 256 colours the tile is lossless; above, it is lossy if the
  tile's update frequency (a heat counter decayed per second) is high, and
  lossless otherwise.
- **Refinement:** when a lossy region stops changing for a short delay,
  resend it losslessly so that static content ends up exact.
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

### Lossy codec

- **A. JPEG via `image/jpeg` (recommended).** Native decoding everywhere.
  Quality is adjustable per viewer.
- **B. WebP lossy.** Better compression, but no pure-Go encoder of usable
  speed; it would need cgo or a helper. Rejected for the server.

### Pixel layout

- **A. RGBA (Go `image.RGBA`, browser `ImageData`).** No swizzle in the
  web client or in Go.
- **B. BGRX little-endian.** Matches the common RFB true-colour format and
  CoreGraphics' native order, so VNC and Swift avoid a swizzle.

### Video tier (after 1.0)

H.264 or AV1 for high-motion regions with WebCodecs decoding. Pure-Go
encoders are not fast enough, so it needs a helper process or a hardware
encoder, plus a licensing check. Kept out of beta; protocol v1 reserves a
codec ID range for it.

## Open Questions

1. Lossless codec A, B or C? Benchmark A against B on a corpus of
   recorded VNC frames (desktop, terminal, browser, photo) before
   deciding.
2. Tile size 64 or 128? Smaller tiles mean finer damage but more
   per-tile overhead.
3. Pixel layout A or B? The swizzle happens once per decoded tile in one
   place either way.
4. Per-viewer quality: drive JPEG quality from frame-ack round-trip time
   and decode time (AIMD), or offer only a fixed per-session quality in
   beta?
5. Should the server downscale for viewers whose viewport is smaller than
   the remote screen, or leave scaling to the client in beta?

## Affects

New `internal/framebuffer` and `internal/encoder`. Fixes the codec IDs in
[protocol-v1](protocol-v1.md) and the decoders in
[client-core](client-core.md) and [009](../009-react-web-client.md).
