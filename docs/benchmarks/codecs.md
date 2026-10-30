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
| 1. Lossless codec | **A: palette or raw pixels with deflate**, `compress/flate` at level 1 | 2.1 to 2.5 times smaller than PNG on the desktop, the bitmap terminal and the dragged window, 1.3 times on scrolled text and 5 to 10 % on antialiased text, in two fifths to all of PNG's encode time; 12 bytes for a solid tile against PNG's 88. PNG is 3 % smaller only on the plasma's gradients. Lossless WebP is 27 to 35 % smaller on antialiased text but 9 to 21 times slower; QOI is 1.4 to 3.9 times larger |
| 2. Tile size | **64** | A key costs 0.1 to 1.7 KiB at 64 and 0.2 to 2.9 KiB at 128, a scrolled line 4.6 against 8.7 KiB (G3's target is 7.1), and at 128 the browser page sends 42 % of its area lossy instead of 6 %. Full frames cost more at 64: 1.5 to 2.2 times on the desktop, the terminals, the browser and the drag |
| 3. Pixel layout | **A: RGBA** | x11vnc sends RGBX on request, pixel for pixel the same as its own BGRX; every Go encoder takes `image.RGBA`; a swizzle would cost 3.3 µs a tile, two fifths of what encoding a tile of 3 to 16 colours takes |
| Lossy codec | **WebP (`gen2brain/vpx/webp`, method 0)**, with JPEG (`gen2brain/jpegn`) for viewers that list only JPEG | At equal SSIM, 0.35 to 0.54 of JPEG's bytes on 64-pixel tiles (the photograph at 0.90: 110 against 233 KiB) and 0.65 on 128-pixel ones, with 53 bytes of fixed cost a tile against JPEG's 656; at that SSIM it takes about twice `image/jpeg`'s encode time and 6.5 to 7 times `jpegn`'s |

The measurement also found two things the spec had not planned for:

- **Colour count alone sends the motion scene lossless.** The plasma has
  at most 256 colours in every tile, so the spec's classification sends
  G5's frames as palette and deflate: 388 KiB a frame, eight times
  guacd's 49. Lossy WebP sends the same tiles in 26 KiB at an SSIM of
  0.989. On antialiased text, also 17 to 256 colours, lossy saves only a
  fifth to two fifths and blurs it. So a tile of 17 to 256 colours
  should go lossy while it moves and be refined to exact pixels once it
  stops; tiles of up to 16 colours stay lossless, where they are 5 to 10
  times smaller than lossy.
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
averaged over tiles. It ran on 2026-10-30 from 19:31 to 19:35 UTC, on
the Apple M5 Pro (18 cores) of the [baselines](../benchmarks.md), with Go
1.27.1 on arm64 and `GOMAXPROCS=1`, so that every time is one core's,
including the libraries that would otherwise spread one image over
several. The measurement logs the host's load average every 30 seconds
([codecs-load.log](./codecs-load.log)); the one-minute average stayed
between 4.7 and 8.8, with other sessions' work on the host. An earlier
run at 11:50 gave the same bytes and SSIM for every tile; its times per
codec and class were within 5 % for 95 % of the sums and within 10 %
for 98 %, the rest being solid tiles' sub-microsecond times.

`bench/codecs` is a Go module of its own, so that occamyd's `go.mod`
carries none of the candidates; gen2brain's modules need Go 1.26.4.

```sh
make corpus   # re-records bench/testdata/corpus
make codecs   # measures, and writes the three files here to docs/benchmarks
```

`make codecs` runs, in `bench/codecs`,
`go test -tags measure -run TestMeasure -timeout 2h . -args -out ../../docs/benchmarks`.

**The candidates:**

| Name | What |
|------|------|
| `pal+flate1`, `pal+flate6` | Lossless A: the palette container below, `compress/flate` at levels 1 and 6 |
| `pal+kflate1` | The same with `klauspost/compress/flate` at level 1 |
| `pal+zstd1`, `pal+zstd3` | The same with `klauspost/compress/zstd`, fastest and default; no browser decodes zstd natively |
| `png1`, `png6` | Lossless B: `image/png` at best speed and default, with a palette for up to 256 colours, which decoding expands through a table |
| `qoi` | Lossless C: QOI, written for the measurement |
| `webpll-dt0`, `webpll-dt4` | Lossless WebP from `deepteams/webp` v1.2.8: method 0 at effort 0, and method 4 at effort 75 |
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
which counts colours with an early exit at 257 and takes 6 to 14 µs a
64-pixel tile, 1.2 to 2.7 ms a frame; that time is reported apart and is
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
| pal+flate1 | 15.1 · 1.4 | 17.4 · 1.5 | 103.2 · 3.9 | 125.5 · 4.5 | 12.3 · 1.2 | 55.4 · 3.6 | 388.1 · 8.0 |
| pal+flate6 | 14.4 · 1.9 | 16.4 · 1.8 | 99.5 · 5.0 | 121.6 · 5.9 | 11.8 · 1.6 | 53.6 · 3.7 | 319.4 · 10.8 |
| pal+kflate1 | 15.1 · 1.5 | 17.4 · 1.5 | 103.2 · 3.1 | 125.5 · 3.5 | 12.3 · 1.2 | 55.4 · 3.1 | 388.1 · 6.4 |
| pal+zstd1 | 18.5 · 1.7 | 20.3 · 1.8 | 104.4 · 2.9 | 126.5 · 3.1 | 15.1 · 1.4 | 57.2 · 3.1 | 403.8 · 5.6 |
| png1 | 37.0 · 3.5 | 36.0 · 3.7 | 115.0 · 4.8 | 132.2 · 4.6 | 30.3 · 2.9 | 71.9 · 5.7 | 375.7 · 7.0 |
| png6 | 34.8 · 3.9 | 33.8 · 3.9 | 110.8 · 5.6 | 128.1 · 5.5 | 28.4 · 3.2 | 69.4 · 5.7 | 318.6 · 9.5 |
| qoi | 21.1 · 0.7 | 64.0 · 0.8 | 156.1 · 1.2 | 180.8 · 1.2 | 17.3 · 0.6 | 215.9 · 1.4 | 895.9 · 3.5 |
| webpll-dt4 | 18.8 · 31.5 | 21.5 · 27.2 | 75.0 · 38.4 | 89.7 · 40.2 | 15.4 · 25.7 | 59.2 · 25.3 | 288.0 · 70.1 |
| webpll-vpx4 | 18.6 · 53.4 | 21.1 · 45.2 | 68.2 · 79.8 | 82.0 · 94.7 | 15.2 · 43.7 | 57.4 · 39.7 | 252.9 · 119.4 |

- **A beats PNG** on screen content: 2.5 times smaller on the desktop,
  2.1 on the bitmap terminal, 2.5 on the drag's wallpaper, 1.3 on the
  scroll's bitmap text, 1.1 and 1.05 on antialiased text. Its fixed cost
  is 12 bytes a solid tile against 88, and it encodes in two fifths of
  PNG's time on simple tiles and about the same on text. Go decodes A in
  a quarter to two thirds of PNG's time on tiles of up to 16 colours and
  70 to 95 % on 17 to 256, with PNG's palette expanded through a
  table.
- **Where PNG wins.** On the plasma's gradients PNG's filters are 3 %
  smaller at best speed, which makes the class of 17 to 256 colours as a
  whole 2 % smaller as PNG (3.28 bits a pixel against 3.35). Those tiles
  go lossy while they move and are sent lossless only once still, as
  refinement; a second lossless codec for 3 % there is not worth it in
  beta. Above 256 colours PNG is 30 % smaller (13.6 bits a pixel
  against 19.3), but the pipeline does not send those tiles lossless.
- **Level 1 is enough.** Level 6 saves 3 to 6 % on screen content at 1.3
  times the encode time; only the plasma gains 18 %, and under motion it
  goes lossy anyway (below). `klauspost/compress/flate` writes the same
  bytes at level 1 and encodes text a fifth faster; it is a later
  optimization, not a reason for a dependency now. zstd is larger at
  level 1 and at most 3 % smaller at level 3, and browsers would need a
  decoder for it.
- **Lossless WebP** is the smallest on antialiased text, 27 to 35 % below
  A, but at 38 to 95 ms a frame there against 4, 9 to 21 times slower:
  too slow for interactive updates on one core. It remains a candidate
  for refining still text, where time matters less; that needs a
  measurement of its own. Method 0 of `gen2brain/vpx` is unusable on
  gradients: 168 KiB for the desktop against 19 at method 4.
- **QOI** is the fastest and 1.4 to 3.9 times larger, 3.9 times on the
  scrolling text, and browsers need a decoder in JavaScript.

## 2. The tile size

Full frames cost more at 64 pixels than at 128, with `pal+flate1` for
tiles of up to 256 colours and `webp-vpx0 q70` above:

| Scene | 64: KiB · ms a frame | 128: KiB · ms a frame | 64 against 128 |
|---|---|---|---|
| desktop | 15.1 · 1.4 | 6.9 · 0.6 | 2.2 times |
| terminal | 17.4 · 1.5 | 11.6 · 0.7 | 1.5 |
| terminal-aa | 103.2 · 3.9 | 63.2 · 1.6 | 1.6 |
| browser | 140.1 · 6.1 | 93.1 · 8.2 | 1.5 |
| photo | 44.1 · 12.4 | 34.9 · 10.8 | 1.3 |
| drag | 24.5 · 3.7 | 15.9 · 3.2 | 1.5 |
| scroll | 55.4 · 3.6 | 52.2 · 1.9 | 1.06 |
| motion, lossless | 388.1 · 8.0 | 343.9 · 4.5 | 1.13 |

The cost is lossless, not lossy: the desktop and both terminals have no
lossy tile and gain the most at 128. Every tile carries its own palette
and starts deflate afresh, so four 64-pixel tiles pay for four palettes
and lose the context a 128-pixel tile shares. Merging lossy tiles into
rectangles does not recover it. So on a full frame, such as the first
one a viewer receives, 64-pixel tiles send 1.5 to 2.2 times the bytes
of 128-pixel ones on the desktop, the terminals, the browser and the
drag, and 1.06 to 1.3 times on the scrolled text, the photograph and
the plasma.

But a session is mostly small changes, and those cost more at 128. The
tiles a change touches, sent whole, in the same mix:

| Change | 64: tiles · KiB | 128: tiles · KiB |
|---|---|---|
| A key in the browser's text | 1 · 1.7 | 1 · 2.9, lossy |
| A key in either terminal, or the probe swatch | 1 · 0.1–0.2 | 1 · 0.2–0.3 |
| A scrolled line, after a copy-rect of 13 rows | 15 · 4.6 | 8 · 8.7 |
| The dragged window, one update | 35 · 12.0–12.3 | 12 · 10.5–10.7 |

G3's target is 7.1 KiB a frame: met at 64, missed at 128. The browser's
key tile has 257 colours at 128, so it goes lossy, as text, until
refinement; at 64 it is lossless. With JPEG from `jpegn` for the lossy
tiles, the key costs 4.4 KiB at 128 and the drag 36 KiB at 64 against
23, since JPEG pays its headers per tile. The drag is measured without
move detection, which G4 (3.9 KiB a frame) needs anyway.

Larger tiles also mix more content, so more of the screen crosses 256
colours and goes lossy: 42 % of the browser page's area at 128 against
6 % at 64, and 25 % of the drag's against 18 %. At 128, text next to a
link or a code block would be blurred until refinement.

So tiles stay at 64 pixels for damage, and full frames pay for it.
Encoding a full frame's adjacent lossless tiles together, where their
union keeps to 256 colours, could recover part of it; that is not
measured here.

## 3. The pixel layout

- **The source.** x11vnc sends red, green, blue and padding when asked
  with SetPixelFormat, and the capture checked that the photograph came
  identically in that order and in x11vnc's own blue-first order. The
  native VNC backend asks for that order and makes the padding opaque
  alpha, so it never swizzles. The guacd bridge decodes PNG, JPEG and
  WebP in Go, which produce RGBA. A native RDP backend would receive
  blue-first bitmaps and swizzle once as it draws.
- **The encoders.** `image/png`, `image/jpeg`, `jpegn` and both WebP
  libraries take `image.RGBA` (or `NRGBA`). With BGRX, every lossy and
  PNG encode would first swizzle: 3.3 µs a 64-pixel tile and 12.7 at
  128, about 0.6 ms a frame, 45 % of what `pal+flate1` takes to encode
  the whole desktop (1.4 ms).
- **The bytes.** Raw pixels deflate the same in either channel order: RGB
  and BGR within 0.8 % in every class. Dropping the padding byte matters
  more: four bytes a pixel deflate 1 to 14 % larger at 64 pixels, up to
  31 % at 128, and up to 2.2 times as slowly above 256 colours, which is
  why the container stores three. The layouts take turns going first in
  the measurement, so that none always finds the tile in the cache.
- **The clients.** A browser's `ImageData` and `createImageBitmap` are
  RGBA. Core Graphics draws either order natively, so the Swift client
  gains nothing from BGRX.

RGBA is what `internal/framebuffer`, raw codec 1 in
[the protocol](../protocol.md#codecs), and the `Encoder` interface of
`internal/stream` (pixels "in RGBA order") already assume; the
measurement confirms them.

## The lossy codec

On tiles above 256 colours at 64 pixels, over all scenes: bits a pixel,
SSIM, encode and Go decode µs a tile, at equal quality settings.

| Codec | q30 | q70 | q90 |
|---|---|---|---|
| jpeg | 1.51 · 0.850 · 42 · 26 | 1.95 · 0.890 · 40 · 28 | 3.03 · 0.934 · 54 · 38 |
| jpegn | 1.58 · 0.850 · 12 · 14 | 2.01 · 0.890 · 12 · 13 | 3.10 · 0.934 · 16 · 19 |
| webp-vpx0 | 0.37 · 0.845 · 66 · 32 | 0.62 · 0.867 · 70 · 35 | 1.91 · 0.959 · 123 · 65 |
| webp-vpx4 | 0.35 · 0.849 · 176 · 30 | 0.59 · 0.872 · 212 · 36 | 1.99 · 0.969 · 355 · 70 |
| webp-dt0 | 0.39 · 0.830 · 143 · 29 | 0.70 · 0.872 · 137 · 33 | 2.06 · 0.964 · 258 · 59 |

Equal settings are not equal quality: WebP scores lower than JPEG at
q30 and q70 and higher at q90. At equal SSIM, interpolated between the
measured qualities: KiB a frame of each scene's lossy tiles, the quality
that reaches it, and encode ms a frame there.

| Tiles | Codec | SSIM 0.85 | SSIM 0.90 | SSIM 0.95 |
|---|---|---|---|---|
| photo, 64 | jpeg | 149 (q50) · 7.0 | 233 (q84) · 9.2 | not reached |
| photo, 64 | jpegn | 155 (q50) · 2.0 | 239 (q84) · 2.7 | not reached |
| photo, 64 | webp-vpx0 | 52 (q72) · 13.1 | 110 (q83) · 18.2 | 170 (q90) · 22.9 |
| photo, 64 | webp-vpx4 | 47 (q71) · 39.2 | 105 (q82) · 52.6 | 171 (q88) · 65.1 |
| drag, 64 | jpeg | 27 or less (q30) · 1.5 | 30 (q50) · 1.3 | 53 (q90) · 1.9 |
| drag, 64 | jpegn | 29 or less (q30) · 0.4 | 31 (q50) · 0.4 | 54 (q90) · 0.6 |
| drag, 64 | webp-vpx0 | 7 or less (q30) · 2.4 | 13 (q71) · 2.7 | 28 (q86) · 3.9 |
| photo, 128 | jpeg | 66 (q50) · 7.6 | 150 (q84) · 9.6 | not reached |
| photo, 128 | webp-vpx0 | 43 (q72) · 11.5 | 98 (q83) · 16.4 | 157 (q90) · 21.1 |

On whole frames and on rows of 16 tiles, where headers matter less: KiB,
SSIM and encode ms a frame.

| Scene | Unit | jpeg q30 | jpeg q70 | jpegn q70 | webp-vpx0 q30 | webp-vpx0 q70 |
|---|---|---|---|---|---|---|
| motion | 64-pixel tiles | 137 · 0.979 · 7.6 | 149 · 0.992 · 6.1 | 155 · 0.992 · 1.9 | 26 · 0.989 · 11.3 | 33 · 0.991 · 11.0 |
| motion | rows of tiles | 33 · 0.979 · 6.9 | 44 · 0.992 · 7.0 | 44 · 0.992 · 1.5 | 14 · 0.989 · 8.3 | 18 · 0.990 · 8.5 |
| motion | frame | 26 · 0.979 · 7.4 | 38 · 0.992 · 7.6 | 38 · 0.992 · 1.6 | 11 · 0.989 · 8.7 | 15 · 0.990 · 8.6 |
| photo | rows of tiles | 32 · 0.830 · 7.2 | 70 · 0.871 · 7.9 | 71 · 0.871 · 1.8 | 14 · 0.824 · 8.2 | 31 · 0.843 · 9.8 |
| photo | frame | 25 · 0.830 · 7.6 | 64 · 0.871 · 8.5 | 64 · 0.871 · 1.8 | 13 · 0.824 · 8.2 | 30 · 0.843 · 9.8 |

- **WebP is smaller at equal SSIM**: 1.9 to 2.9 times on the photograph's
  and the drag's 64-pixel tiles across the three targets, so 0.35 to
  0.54 of JPEG's bytes; 1.5 times on the photograph's 128-pixel tiles;
  2.8 to 2.9 times on the plasma's rows and frames at 0.989. The grainy
  photograph as whole rows narrows the gap: at JPEG q70's SSIM of 0.871,
  WebP takes about 62 KiB against 70 (61 against 64 as one image), and
  at 0.92 about 117 against 175.
- **WebP costs more CPU**: at the qualities that reach the same SSIM,
  `vpx` at method 0 takes about twice `image/jpeg`'s encode time (1.9 to
  2.1 times) and 6.5 to 7 times `jpegn`'s; for the photograph's full
  screen at 0.90, 18 ms of one core against 9.2 and 2.7, or 4 to 5 ms
  over a pool of four. The per-tile table's 1.6 to 1.8 times compares
  equal settings at q30 to q70, where WebP's quality is lower. Method 4
  saves another 4 to 20 % at three times the time; `deepteams/webp` is
  as small at method 0 and twice as slow, and its method 4 is worse at
  equal SSIM.
- **`jpegn` writes JPEG of the same quality** as `image/jpeg` at the
  same setting, within 5 % of its bytes (34 more header bytes a tile),
  in a third of the time to encode and half to decode.

So lossy tiles are WebP from `gen2brain/vpx/webp` at method 0, and a
viewer that lists JPEG but not WebP gets JPEG from `jpegn`. What would
flip it: CPU becoming the limit before bandwidth on the server (JPEG
from `jpegn` is 6.5 to 7 times cheaper at equal SSIM), or browsers' WebP
decoding proving slower than JPEG on low-end clients, which this
measurement does not cover.

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
fifths smaller on antialiased text at a visible loss, and 5 to 10 times
larger on two-colour text. Hence the rule the spec now has: while a tile
moves, 17 to 256 colours go lossy like photographs, and refinement makes
them exact once still; up to 16 colours are always lossless.

**G5's target.** [docs/benchmarks.md](../benchmarks.md#targets) sets
4.9 KiB a frame, 10 times below guacd's 49. Pure-Go codecs do not reach
it on this scene: the best measured is WebP at quality 30, 11 KiB as
one image and 14 KiB as rows of tiles, at an SSIM of 0.989. JPEG from
`jpegn` at quality 50, the same SSIM, takes 32 and 38 KiB. The spec's
notes propose 14 KiB a frame as G5's target, with the evidence; the
targets table is not changed here.

**G6's target** of 139 KiB stands at equal SSIM. On the photograph's
64-pixel tiles, JPEG at quality 70 scores 0.871 and takes 175 KiB, over
the target; WebP reaches the same SSIM at about quality 78 in about
75 KiB. As rows of tiles, JPEG takes 70 KiB and WebP about 62.

## Limits

- SSIM is on luma, so it does not see chroma subsampling. Text in
  colour, links for example, loses more under 4:2:0 than the scores
  show. A review of this measurement scored chroma as well and found
  that it does not favour WebP.
- Decode times are Go's, which the Go client core uses. Browsers decode
  PNG, JPEG and WebP natively and inflate with `DecompressionStream`,
  then expand the palette in JavaScript; none of that is measured here.
- The scenes are the bench desktop's: two terminals and one page are
  real applications, while the photograph, the plasma and the scrolled
  text are generated. A real video or photograph has more detail than
  the plasma and less grain than the photograph.
- Encode times are one core's with `GOMAXPROCS=1`, sums of each tile's
  median, under a load average of 5 to 9 on 18 cores.
