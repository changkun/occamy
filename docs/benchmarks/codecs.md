# Codec measurement

The [graphics pipeline](../../specs/008-native-architecture/graphics-pipeline.md)
left its codecs, its tile size and its pixel layout to a measurement on
recorded frames. This page is that measurement: what was compared, on
what, and what it decided. Every table here is an excerpt of
[codecs-tables.md](./codecs-tables.md), which the measurement writes, and
[codecs.json](./codecs.json) holds the sums behind it.

## The answers

| Question | Answer | The numbers that decide it |
|----------|--------|----------------------------|
| 1. Lossless codec | **A: palette or raw pixels with deflate**, `compress/flate` at level 1 | 2.1 to 2.5 times smaller than PNG on the desktop, the bitmap terminal and the dragged window, 1.3 times on scrolled text and 5 to 10 % on antialiased text, in two fifths to all of PNG's encode time; 12 bytes for a solid tile against PNG's 88. Lossless WebP is 27 to 35 % smaller on antialiased text but 10 to 21 times slower; QOI is 1.4 to 3.9 times larger |
| 2. Tile size | **64** | A key costs 0.1 to 1.7 KiB at 64 and 0.2 to 4.3 KiB at 128, a scrolled line 4.6 against 8.7 KiB (G3's target is 7.1), and at 128 the browser page sends 42 % of its area lossy instead of 6 % |
| 3. Pixel layout | **A: RGBA** | x11vnc sends RGBX on request, pixel for pixel the same as its own BGRX; every Go encoder takes `image.RGBA`; a swizzle would cost 3.2 µs a tile, nearly half of what encoding a tile of 3 to 16 colours takes |
| Lossy codec | **WebP (`gen2brain/vpx/webp`, method 0)**, with JPEG (`gen2brain/jpegn`) for viewers without WebP | Half JPEG's bytes or less at equal SSIM on photographic tiles (photo 110 against 233 KiB, drag 13 against 30), 53 bytes of fixed cost a tile against JPEG's 656, at 1.6 to 1.8 times `image/jpeg`'s encode time |

The measurement also found two things the spec had not planned for:

- **Colour count alone sends the motion scene lossless.** The plasma has
  at most 256 colours in every tile, so the spec's classification sends
  G5's frames as palette and deflate: 388 KiB a frame, eight times
  guacd's 49. Lossy WebP sends the same tiles in 26 KiB at an SSIM of
  0.989. On antialiased text, also 17 to 256 colours, lossy saves only a
  fifth to two fifths and blurs it. So a tile of 17 to 256 colours should go lossy
  while it moves and be refined to exact pixels once it stops; tiles of
  up to 16 colours stay lossless, where they are 5 to 10 times smaller
  than lossy.
- **Lossy tiles cost their headers.** A JPEG carries about 650 bytes of
  tables, so 192 tiles of the motion scene at JPEG quality 30 take
  137 KiB, against 33 KiB as 12 rows of 16 tiles and 26 KiB as one image.
  WebP's fixed cost is 53 bytes, and its tiles take 26 KiB against 14 as
  rows. Encoding a frame's adjacent lossy tiles as one rectangle keeps
  64-pixel tiles for damage and classification without paying for them
  in lossy headers.

## How it was measured

**The corpus** is 13 frames of the bench desktop at 1024×768, recorded
over VNC by `make corpus` and described in
[bench/testdata/corpus](../../bench/testdata/corpus/README.md): the idle
desktop, xterm with its bitmap font, xterm with an antialiased font, a
documentation page in dillo, the photograph, three consecutive updates of
the dragged window and of the scrolling text, and two of the plasma. It
takes 2.5 MiB.

**The run.** `bench/codecs` splits each frame into tiles through
`internal/framebuffer` at 64 and 128 pixels, classifies each tile by its
colours, and encodes and decodes it with every candidate, five times. A
tile's times are the median of its five, and a table sums those medians.
Lossless results are checked against the original pixels; none differed.
SSIM is luma SSIM (BT.601 weights, 8×8 windows, the usual constants),
averaged over tiles. It ran on 2026-10-30 from 11:50 to 11:55 UTC, on
the Apple M5 Pro (18 cores) of the [baselines](../benchmarks.md), with Go
1.27.1 on arm64 and `GOMAXPROCS=1`, so that every time is one core's,
including the libraries that would otherwise spread one image over
several. The host's one-minute load average, logged every 30 seconds
([codecs-load.log](./codecs-load.log)), stayed between 4.2 and 6.7, with
other sessions' work on the host.

```sh
make corpus   # re-records bench/testdata/corpus
go test -tags measure -run TestMeasure -timeout 2h ./bench/codecs -args -out DIR -runs 5
```

**The candidates:**

| Name | What |
|------|------|
| `pal+flate1`, `pal+flate6` | Lossless A: the palette container below, `compress/flate` at levels 1 and 6 |
| `pal+kflate1` | The same with `klauspost/compress/flate` at level 1 |
| `pal+zstd1`, `pal+zstd3` | The same with `klauspost/compress/zstd`, fastest and default; no browser decodes zstd natively |
| `png1`, `png6` | Lossless B: `image/png` at best speed and default, with a palette for up to 256 colours |
| `qoi` | Lossless C: QOI, written for the measurement |
| `webpll-dt0`, `webpll-dt4` | Lossless WebP from `deepteams/webp` v1.2.8, methods 0 and 4 |
| `webpll-vpx0`, `webpll-vpx4` | Lossless WebP from `gen2brain/vpx/webp` v0.2.1, methods 0 and 4 |
| `jpeg qN` | `image/jpeg`, 4:2:0, qualities 30 to 90 |
| `jpegn qN` | `gen2brain/jpegn` v0.6.2, baseline, 4:2:0, standard tables |
| `webp-dtM qN` | Lossy WebP from `deepteams/webp`, methods 0 and 4 |
| `webp-vpxM qN` | Lossy WebP from `gen2brain/vpx/webp`, methods 0 and 4, on one goroutine |

All are pure Go; the WebP and JPEG libraries use assembly on arm64. The
palette container is the form byte 0, the number of colours minus one,
three bytes of red, green and blue a colour, then each pixel's index at
1, 2, 4 or 8 bits for up to 2, 4, 16 or 256 colours, rows padded to a
byte; or the form byte 1 and three bytes a pixel
(`bench/codecs/palette.go`). The palette comes from the classification,
which counts colours with an early exit at 257 and takes 9 to 14 µs a
64-pixel tile, 1.1 to 2.6 ms a frame; that time is reported apart and is
not in the encode times of `pal+*` and `png*`.

**The classes** are those of the spec's classification, split finer for
the report. Tiles at 64 pixels:

| Scene | Frames | 1 colour | 2 | 3–16 | 17–256 | over 256 |
|---|---|---|---|---|---|---|
| desktop | 1 | 0 | 0 | 192 | 0 | 0 |
| terminal | 1 | 32 | 43 | 117 | 0 | 0 |
| terminal-aa | 1 | 27 | 0 | 76 | 89 | 0 |
| browser | 1 | 8 | 1 | 50 | 121 | 12 |
| photo | 1 | 0 | 0 | 0 | 0 | 192 |
| drag | 3 | 0 | 0 | 471 | 0 | 105 |
| scroll | 3 | 0 | 540 | 36 | 0 | 0 |
| motion | 2 | 0 | 0 | 0 | 384 | 0 |

The wallpaper's gradient changes every few rows, so its tiles have 3 to
16 colours. Antialiased text has 17 to 256, as does every tile of the
plasma, which draws from a palette of 256 colours.

## 1. The lossless codec

On each scene's tiles of up to 256 colours, the ones the pipeline sends
lossless, at 64 pixels: KiB a frame · encode ms a frame.

| Codec | desktop | terminal | terminal-aa | browser | drag | scroll | motion |
|---|---|---|---|---|---|---|---|
| pal+flate1 | 15.1 · 1.3 | 17.4 · 1.4 | 103.2 · 3.7 | 125.5 · 4.4 | 12.3 · 1.1 | 55.4 · 3.4 | 388.1 · 7.7 |
| pal+flate6 | 14.4 · 1.7 | 16.4 · 1.7 | 99.5 · 4.8 | 121.6 · 5.7 | 11.8 · 1.4 | 53.6 · 3.6 | 319.4 · 10.0 |
| pal+kflate1 | 15.1 · 1.3 | 17.4 · 1.4 | 103.2 · 2.9 | 125.5 · 3.3 | 12.3 · 1.1 | 55.4 · 2.9 | 388.1 · 6.1 |
| pal+zstd1 | 18.5 · 1.4 | 20.3 · 1.5 | 104.4 · 2.4 | 126.5 · 2.8 | 15.1 · 1.2 | 57.2 · 2.7 | 403.8 · 5.0 |
| png1 | 37.0 · 3.3 | 36.0 · 3.6 | 115.0 · 4.4 | 132.2 · 4.4 | 30.3 · 2.7 | 71.9 · 5.4 | 375.7 · 6.5 |
| png6 | 34.8 · 3.7 | 33.8 · 3.8 | 110.8 · 5.3 | 128.1 · 5.4 | 28.4 · 3.1 | 69.4 · 5.7 | 318.6 · 8.7 |
| qoi | 21.1 · 0.8 | 64.0 · 0.9 | 156.1 · 1.2 | 180.8 · 1.2 | 17.3 · 0.6 | 215.9 · 1.4 | 895.9 · 3.5 |
| webpll-dt4 | 18.8 · 31.2 | 21.5 · 27.2 | 75.0 · 38.3 | 89.7 · 39.9 | 15.4 · 25.6 | 59.2 · 25.2 | 288.0 · 69.7 |
| webpll-vpx4 | 18.6 · 53.3 | 21.1 · 45.3 | 68.2 · 79.7 | 82.0 · 94.4 | 15.2 · 43.7 | 57.4 · 39.5 | 252.9 · 119.1 |

- **A beats PNG** everywhere the pipeline is lossless, except the
  plasma's gradients, where PNG's filters win by 3 % at best speed: 2.4
  times smaller on the desktop, 2.1 on the bitmap terminal, 2.5 on the
  drag's wallpaper, 1.3 on the scroll's bitmap text, 1.1 and 1.05 on
  antialiased text. Its fixed cost is 12 bytes a solid tile against 88,
  and it encodes in two fifths of PNG's time on simple tiles and about the
  same on text. PNG wins only on tiles above 256 colours (13.6 bits a
  pixel against 19.3), which the pipeline does not send lossless. Go
  decodes A in a quarter to three quarters of PNG's time.
- **Level 1 is enough.** Level 6 saves 3 to 6 % on screen content at 1.3
  times the encode time; only the plasma gains 18 %, and under motion it
  goes lossy anyway (below). `klauspost/compress/flate` writes the same
  bytes at level 1 and encodes text a quarter faster; it is a later
  optimization, not a reason for a dependency now. zstd is larger at level 1 and at most 3 % smaller at level 3,
  and browsers would need a decoder for it.
- **Lossless WebP** is the smallest on antialiased text, 27 to 35 % below
  A, but at 25 to 95 ms a frame against 4: too slow for interactive
  updates on one core. It remains a candidate for refining still text,
  where time matters less; that needs a measurement of its own. Method 0
  of `gen2brain/vpx` is unusable on gradients: 168 KiB for the desktop
  against 19 at method 4.
- **QOI** is the fastest and 1.4 to 3.9 times larger, 3.9 times on the
  scrolling text, and browsers need a decoder in JavaScript.

## 2. The tile size

At 128 pixels, full frames are smaller, because every tile's fixed cost
is paid a quarter as often and deflate sees more context:

| Scene | 64: KiB · ms a frame | 128: KiB · ms a frame |
|---|---|---|
| desktop | 15.1 · 1.3 | 6.9 · 0.6 |
| terminal | 17.4 · 1.4 | 11.6 · 0.8 |
| terminal-aa | 103.2 · 3.7 | 63.2 · 1.7 |
| browser | 140.1 · 6.0 | 93.1 · 8.2 |
| photo | 44.1 · 12.4 | 34.9 · 11.0 |
| drag | 24.5 · 3.6 | 15.9 · 3.2 |
| scroll | 55.4 · 3.4 | 52.2 · 2.0 |

(`pal+flate1` for tiles of up to 256 colours and `webp-vpx0 q70` above.)

But a session is mostly small changes, and those cost more at 128. The
tiles a change touches, sent whole, in the same mix, with `jpeg q70` for
lossy tiles:

| Change | 64: tiles · KiB | 128: tiles · KiB |
|---|---|---|
| A key in the browser's text | 1 · 1.7 | 1 · 4.3 |
| A key in either terminal, or the probe swatch | 1 · 0.1–0.2 | 1 · 0.2–0.3 |
| A scrolled line, after a copy-rect of 13 rows | 15 · 4.6 | 8 · 8.7 |
| The dragged window, one update | 35 · 35 | 12 · 22 |

G3's target is 7.1 KiB a frame: met at 64, missed at 128. The drag is
cheaper at 128 because its tiles are lossy and pay JPEG's headers per
tile; it is measured without move detection, which G4 needs anyway.

Larger tiles also mix more content, so more of the screen crosses 256
colours and goes lossy: 42 % of the browser page's area at 128 against
6 % at 64, and 25 % of the drag's against 18 %. At 128, text next to a
link or a code block would be blurred until refinement.

So tiles stay at 64 pixels, and the cost that favours 128, lossy
headers, is removed by encoding adjacent lossy tiles together (below).

## 3. The pixel layout

- **The source.** x11vnc sends red, green, blue and padding when asked
  with SetPixelFormat, and the capture checked that the photograph came
  identically in that order and in x11vnc's own blue-first order. A VNC
  backend therefore never swizzles. The guacd bridge decodes PNG, JPEG
  and WebP in Go, which produce RGBA.
- **The encoders.** `image/png`, `image/jpeg`, `jpegn` and both WebP
  libraries take `image.RGBA` (or `NRGBA`). With BGRX, every lossy and
  PNG encode would first swizzle: 3.2 µs a 64-pixel tile and 12.8 at
  128, about 0.6 ms a frame, half of what `pal+flate1` takes to encode
  the whole desktop (1.3 ms).
- **The bytes.** Raw pixels deflate the same in either channel order: RGB
  and BGR within 0.8 % in every class. Dropping the padding byte matters
  more: four bytes a pixel deflate 1 to 14 % larger at 64 pixels, up to 31 %
  at 128, and up to twice as
  slowly above 256 colours, which is why the container stores three.
- **The clients.** A browser's `ImageData` and `createImageBitmap` are
  RGBA. Core Graphics draws either order natively, so the Swift client
  gains nothing from BGRX.

RGBA is what `internal/framebuffer`, raw codec 1 in
[the protocol](../protocol.md#codecs), and the `Encoder` interface of
`internal/stream` (pixels "in RGBA order") already assume; the
measurement confirms them.

## The lossy codec

On tiles above 256 colours at 64 pixels, over all scenes: bits a pixel,
SSIM, encode and Go decode µs a tile.

| Codec | q30 | q70 | q90 |
|---|---|---|---|
| jpeg | 1.51 · 0.850 · 41 · 25 | 1.95 · 0.890 · 39 · 28 | 3.03 · 0.934 · 53 · 38 |
| jpegn | 1.58 · 0.850 · 11 · 13 | 2.01 · 0.890 · 12 · 13 | 3.10 · 0.934 · 16 · 19 |
| webp-vpx0 | 0.37 · 0.845 · 64 · 31 | 0.62 · 0.867 · 70 · 35 | 1.91 · 0.959 · 123 · 64 |
| webp-vpx4 | 0.35 · 0.849 · 176 · 30 | 0.59 · 0.872 · 212 · 36 | 1.99 · 0.969 · 355 · 70 |
| webp-dt0 | 0.39 · 0.830 · 141 · 29 | 0.70 · 0.872 · 137 · 33 | 2.06 · 0.964 · 258 · 59 |

At equal SSIM, interpolated between the measured qualities: KiB a frame
of each scene's lossy tiles, and the quality that reaches it.

| Tiles | Codec | SSIM 0.85 | SSIM 0.90 | SSIM 0.95 |
|---|---|---|---|---|
| photo, 64 | jpeg | 149 (q50) | 233 (q84) | not reached |
| photo, 64 | webp-vpx0 | 52 (q72) | 110 (q83) | 170 (q90) |
| photo, 64 | webp-vpx4 | 47 (q71) | 105 (q82) | 171 (q88) |
| drag, 64 | jpeg | 27 or less (q30) | 30 (q50) | 53 (q90) |
| drag, 64 | webp-vpx0 | 7 or less (q30) | 13 (q71) | 28 (q86) |
| photo, 128 | jpeg | 66 (q50) | 150 (q84) | not reached |
| photo, 128 | webp-vpx0 | 43 (q72) | 98 (q83) | 157 (q90) |

On whole frames and on rows of 16 tiles, where headers matter less: KiB,
SSIM and encode ms a frame.

| Scene | Unit | jpeg q30 | jpeg q70 | jpegn q70 | webp-vpx0 q30 | webp-vpx0 q70 |
|---|---|---|---|---|---|---|
| motion | 64-pixel tiles | 137 · 0.979 · 7.4 | 149 · 0.992 · 5.9 | 155 · 0.992 · 1.9 | 26 · 0.989 · 11.1 | 33 · 0.991 · 11.0 |
| motion | rows of tiles | 33 · 0.979 · 6.8 | 44 · 0.992 · 6.9 | 44 · 0.992 · 1.5 | 14 · 0.989 · 8.1 | 18 · 0.990 · 8.6 |
| motion | frame | 26 · 0.979 · 7.1 | 38 · 0.992 · 7.3 | 38 · 0.992 · 1.6 | 11 · 0.989 · 8.1 | 15 · 0.990 · 8.4 |
| photo | rows of tiles | 32 · 0.830 · 7.2 | 70 · 0.871 · 7.9 | 71 · 0.871 · 1.8 | 14 · 0.824 · 8.3 | 31 · 0.843 · 9.9 |
| photo | frame | 25 · 0.830 · 7.7 | 64 · 0.871 · 8.3 | 64 · 0.871 · 1.9 | 13 · 0.824 · 8.2 | 30 · 0.843 · 10.0 |

- **WebP is smaller at equal quality**: 2.0 to 2.3 times on 64-pixel
  tiles, 1.5 times on the photograph's 128-pixel tiles, 2.4 to 2.9 times on the plasma's rows and frames. On the
  grainy photograph as whole rows, the two meet at an SSIM of 0.87
  (JPEG q70 at 70 KiB, WebP about q75 at about 62) and WebP pulls ahead
  above it (about 116 against 175 KiB at 0.92).
- **WebP costs more CPU**: `vpx` at method 0 encodes at 1.8 times
  `image/jpeg`'s time a tile and about the same on rows, and 5.5 to 6
  times `jpegn`'s. That is 8 to 12 ms of one core for a full-screen frame,
  2 to 3 ms over a pool of four. Method 4 saves another 5 to 20 % at
  three times the time; `deepteams/webp` is as small at method 0 and
  twice as slow, and its method 4 is worse at equal SSIM.
- **`jpegn` writes JPEG of the same quality** as `image/jpeg` at the
  same setting, within 5 % of its bytes (34 more header bytes a tile),
  in a third of the time to encode and half to decode.

So lossy tiles are WebP from `gen2brain/vpx/webp` at method 0, and a
viewer that does not announce WebP gets JPEG from `jpegn`. What would
flip it: CPU becoming the limit before bandwidth on the server (JPEG
from `jpegn` is 5.5 times cheaper), or browsers' WebP decoding proving
slower than JPEG on low-end clients, which this measurement does not
cover.

## Motion, and G5's bytes

The plasma of G5 has 17 to 256 colours in every tile, so classification
by colour count sends it lossless. Lossy, by class, at 64 pixels: bits a
pixel and SSIM.

| Tiles | pal+flate1 | webp-vpx0 q30 | webp-vpx0 q70 | jpegn q70 |
|---|---|---|---|---|
| motion, 17–256 colours | 4.04 · 1 | 0.27 · 0.989 | 0.35 · 0.991 | 1.62 · 0.992 |
| browser, 17–256 colours | 2.03 · 1 | 1.26 · 0.985 | 1.61 · 0.994 | 2.83 · 0.972 |
| terminal-aa, 17–256 colours | 2.16 · 1 | 1.25 · 0.984 | 1.61 · 0.993 | 2.84 · 0.948 |
| scroll, 2 colours | 0.60 · 1 | 2.76 · 0.992 | 3.32 · 0.997 | 5.17 · 0.976 |
| terminal, 2 colours | 0.23 · 1 | 1.98 · 0.994 | 2.31 · 0.997 | 4.02 · 0.973 |

Lossy is 15 times smaller than lossless on the plasma, a fifth to two
fifths smaller on antialiased text at a visible loss, and 5 to 10 times larger on
two-colour text. Hence the rule the spec now has: while a tile moves, 17
to 256 colours go lossy like photographs, and refinement makes them
exact once still; up to 16 colours are always lossless.

**G5's target.** [docs/benchmarks.md](../benchmarks.md#targets) sets
4.9 KiB a frame, 10 times below guacd's 49. Pure-Go codecs do not reach
it on this scene: the best measured is WebP at quality 30, 11 KiB as
one image and 14 KiB as rows of tiles, at an SSIM of 0.989. JPEG from
`jpegn` at quality 30 takes 26 and 33 KiB. The spec's notes propose 14
KiB a frame as G5's target, with the evidence; the targets table is not
changed here.

**G6's target** of 139 KiB stands: the photograph takes 44 KiB as
64-pixel WebP tiles at quality 70 and 31 KiB as rows, while JPEG tiles at
quality 70 take 175 KiB, which only rows (70 KiB) bring under it.

## Limits

- SSIM is on luma, so it does not see chroma subsampling. Text in
  colour, links for example, loses more under 4:2:0 than the scores
  show.
- Decode times are Go's, which the Go client core uses. Browsers decode
  PNG, JPEG and WebP natively and inflate with `DecompressionStream`,
  then expand the palette in JavaScript; none of that is measured here.
- The scenes are the bench desktop's: two terminals and one page are
  real applications, while the photograph, the plasma and the scrolled
  text are generated. A real video or photograph has more detail than
  the plasma and less grain than the photograph.
- Encode times are one core's with `GOMAXPROCS=1`, sums of each tile's
  median, under a load average of 4 to 7 on 18 cores.
