// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The codec vectors of protocol/testdata/codecs.json. Deflate runs with
// the platform's DecompressionStream, which Node has as browsers do;
// WebP and JPEG run as far as their headers, since Node has no image
// decoder: the browser's decoding of them is checked by hand
// (specs/009-react-web-client.md, Implementation notes).

import { constants as zlib, inflateRawSync } from 'node:zlib'

import { describe, expect, it } from 'vitest'

import {
  type Inflate,
  checkImageSize,
  containerCap,
  decodeContainer,
  decodeDeflate,
  decodeRaw,
  imageSize,
  inflateContainer,
  jpegSize,
  pngSize,
  webpSize,
  withoutExif,
} from './codecs'
import { INFLATE_SLICE, LocalDecoder, slicedInflate, streamInflate } from './decode'
import { Codec } from './messages'
import { codecs, fromHex, toHex } from './testing/vectors'
import { ProtocolError } from './wire'

const inflate = streamInflate() as Inflate

// counting wraps an inflate, re-chunking its output into chunks of size
// bytes, and counts what it yielded and whether its consumer stopped it.
function counting(inner: Inflate, size: number): { inflate: Inflate; yielded: () => number; stopped: () => boolean } {
  let yielded = 0
  let stopped = false
  return {
    inflate: async function* (payload) {
      let finished = false
      try {
        for await (const chunk of inner(payload)) {
          for (let i = 0; i < chunk.length; i += size) {
            const c = chunk.subarray(i, i + size)
            yielded += c.length
            yield c
          }
        }
        finished = true
      } finally {
        stopped = !finished
      }
    },
    yielded: () => yielded,
    stopped: () => stopped,
  }
}

// eager is an inflater that, as Chrome's DecompressionStream does,
// inflates each chunk written to it at once, in 64 KiB chunks, whether
// or not its output is read; Node's waits for its output to be read, so
// it cannot show what an eager one is made to do. It counts the payload
// bytes it was fed and the bytes it inflated.
function eager(): { inflate: Inflate; fed: () => number; inflated: () => number; cancelled: () => boolean } {
  let fed = 0
  let inflated = 0
  let cancelled = false
  const make = (): TransformStream<BufferSource, Uint8Array> => {
    let input = new Uint8Array(0)
    let out = 0
    const run = (c: TransformStreamDefaultController<Uint8Array>, finishFlush: number): void => {
      const all = inflateRawSync(input, { finishFlush })
      for (let i = out; i < all.length; i += 64 << 10) {
        c.enqueue(new Uint8Array(all.subarray(i, Math.min(i + (64 << 10), all.length))))
      }
      inflated += all.length - out
      out = all.length
    }
    // Transformer's cancel, which the reader's cancel calls, is newer
    // than TypeScript's DOM types.
    const transformer: Transformer<BufferSource, Uint8Array> & { cancel: () => void } = {
      transform(chunk, c) {
        const b = ArrayBuffer.isView(chunk) ? new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength) : new Uint8Array(chunk)
        fed += b.length
        const grown = new Uint8Array(input.length + b.length)
        grown.set(input)
        grown.set(b, input.length)
        input = grown
        run(c, zlib.Z_SYNC_FLUSH)
      },
      flush(c) {
        run(c, zlib.Z_FINISH)
      },
      cancel() {
        cancelled = true
      },
    }
    return new TransformStream<BufferSource, Uint8Array>(transformer)
  }
  return { inflate: slicedInflate(make), fed: () => fed, inflated: () => inflated, cancelled: () => cancelled }
}

// deflated is data compressed as raw DEFLATE.
async function deflated(data: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([data as Uint8Array<ArrayBuffer>]).stream().pipeThrough(new CompressionStream('deflate-raw'))
  return new Uint8Array(await new Response(stream).arrayBuffer())
}

const settle = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 20))

async function rejectsInvalid(p: Promise<unknown>): Promise<ProtocolError> {
  const err = await p.then(
    () => null,
    (e: unknown) => e,
  )
  expect(err).toBeInstanceOf(ProtocolError)
  expect((err as ProtocolError).kind).toBe('invalid')
  return err as ProtocolError
}

describe('platform', () => {
  it('has DecompressionStream for raw DEFLATE', () => {
    expect(streamInflate()).not.toBeNull()
  })
})

describe('codec vectors', () => {
  for (const v of codecs.vectors) {
    const payload = fromHex(v.payload)
    const want = fromHex(v.pixels)
    if (v.codec === Codec.Raw) {
      it(v.name, () => {
        expect(toHex(new Uint8Array(decodeRaw(payload, v.width, v.height)))).toBe(v.pixels)
      })
    } else if (v.codec === Codec.Deflate) {
      it(v.name, async () => {
        expect(v.exact).toBe(true)
        const pixels = await decodeDeflate(payload, v.width, v.height, inflate)
        expect(toHex(new Uint8Array(pixels))).toBe(v.pixels)
        if (v.container !== undefined) {
          const c = await inflateContainer(payload, v.width, v.height, inflate)
          expect(toHex(c)).toBe(v.container)
          expect(toHex(new Uint8Array(decodeContainer(fromHex(v.container), v.width, v.height)))).toBe(v.pixels)
        }
        // Inflated a byte at a time, as a stream may yield it.
        const one = counting(inflate, 1)
        expect(toHex(new Uint8Array(await decodeDeflate(payload, v.width, v.height, one.inflate)))).toBe(v.pixels)
        expect(one.stopped()).toBe(false)
        // Through the decoder a session uses.
        const d = new LocalDecoder({ inflate, decodeImage: null })
        const got = await d.decode({ codec: v.codec, payload, width: v.width, height: v.height })
        expect(toHex(new Uint8Array(got))).toBe(v.pixels)
      })
    } else {
      it(`${v.name}: header`, () => {
        expect(want.length).toBe(4 * v.width * v.height)
        expect(imageSize(v.codec, payload)).toEqual({ width: v.width, height: v.height })
        checkImageSize(v.codec, payload, v.width, v.height)
        const others = [
          [v.width + 1, v.height],
          [v.width, v.height - 1],
        ]
        if (v.width !== v.height) {
          others.push([v.height, v.width])
        }
        for (const [w, h] of others) {
          expect(() => checkImageSize(v.codec, payload, w, h)).toThrow(ProtocolError)
        }
        // A header cut short has no size.
        expect(() => imageSize(v.codec, payload.subarray(0, 12))).toThrow(ProtocolError)
      })
      it(`${v.name}: refused before decoding at another size`, async () => {
        let decoded = 0
        const d = new LocalDecoder({
          inflate,
          decodeImage: (_codec, _payload, width, height) => {
            decoded++
            return Promise.resolve(new Uint8ClampedArray(4 * width * height))
          },
        })
        await rejectsInvalid(d.decode({ codec: v.codec, payload, width: v.width + 1, height: v.height }))
        expect(decoded).toBe(0)
        await d.decode({ codec: v.codec, payload, width: v.width, height: v.height })
        expect(decoded).toBe(1)
      })
    }
  }
})

describe('invalid codec payloads', () => {
  for (const v of codecs.invalid) {
    it(v.name, async () => {
      const d = new LocalDecoder({ inflate, decodeImage: null })
      await rejectsInvalid(d.decode({ codec: v.codec, payload: fromHex(v.payload), width: v.width, height: v.height }))
    })
  }
})

describe('the inflation cap', () => {
  const vector = (name: string): { payload: Uint8Array; width: number; height: number } => {
    const v = [...codecs.vectors, ...codecs.invalid].find((x) => x.name === name)
    if (v === undefined) {
      throw new Error(`no vector ${name}`)
    }
    return { payload: fromHex(v.payload), width: v.width, height: v.height }
  }

  it('is the longest container of the rectangle', () => {
    expect(containerCap(3, 2)).toBe(1026 + 6)
    expect(containerCap(1, 1)).toBe(1027)
    // The palette form is the longer up to 341 pixels, raw with alpha beyond.
    expect(containerCap(341, 1)).toBe(1026 + 341)
    expect(containerCap(342, 1)).toBe(1 + 4 * 342)
    expect(containerCap(64, 64)).toBe(1 + 4 * 64 * 64)
  })

  it('takes a container at the cap', async () => {
    const v = vector('deflate: at the cap, 256 colours with alpha for 3×2 pixels')
    const one = counting(inflate, 1)
    const c = await inflateContainer(v.payload, v.width, v.height, one.inflate)
    expect(c.length).toBe(containerCap(v.width, v.height))
    expect(one.yielded()).toBe(containerCap(v.width, v.height))
  })

  for (const name of ['a byte over the cap', 'a bomb: 1 MiB of zeros']) {
    it(`stops at the cap and one byte: ${name}`, async () => {
      const v = vector(name)
      const cap = containerCap(v.width, v.height)
      // A byte at a time, the decoder takes exactly the cap and one byte.
      const one = counting(inflate, 1)
      await rejectsInvalid(inflateContainer(v.payload, v.width, v.height, one.inflate))
      expect(one.yielded()).toBe(cap + 1)
      expect(one.stopped()).toBe(true)
      // As the platform chunks it, it stops at the first chunk past the cap.
      const platform = counting(inflate, Infinity)
      await rejectsInvalid(inflateContainer(v.payload, v.width, v.height, platform.inflate))
      expect(platform.stopped()).toBe(true)
      expect(platform.yielded()).toBeGreaterThan(cap)
      expect(platform.yielded()).toBeLessThanOrEqual(64 << 10)
    })
  }
})

describe('feeding the inflater', () => {
  // A slice inflates to at most 1032 bytes a byte.
  const sliceOut = 1032 * 1024

  it('feeds a container that fits, a slice at a time', async () => {
    // Bytes that do not repeat stay about their length deflated.
    const raw = new Uint8Array(1 + 4 * 64 * 64)
    raw[0] = 3
    let x = 1
    for (let i = 1; i < raw.length; i++) {
      x ^= x << 13
      x ^= x >>> 17
      x ^= x << 5
      raw[i] = x
    }
    const payload = await deflated(raw)
    expect(payload.length).toBeGreaterThan(8 * INFLATE_SLICE)
    const f = eager()
    expect(await inflateContainer(payload, 64, 64, f.inflate)).toEqual(raw)
    expect(f.fed()).toBe(payload.length)
  })

  it('inflates a slice of a bomb past the cap, not the bomb', async () => {
    // 16 MiB of zeros deflate to about 16 KiB, of which a slice inflates
    // to about 1 MiB, past the cap of a 64×64 tile.
    const payload = await deflated(new Uint8Array(16 << 20))
    expect(payload.length).toBeGreaterThan(8 * INFLATE_SLICE)
    const f = eager()
    await rejectsInvalid(inflateContainer(payload, 64, 64, f.inflate))
    await settle()
    expect(f.fed()).toBeGreaterThan(0)
    expect(f.fed()).toBeLessThanOrEqual(1024)
    expect(f.inflated()).toBeLessThanOrEqual(sliceOut)
  })

  it('stops feeding when the consumer stops', async () => {
    const payload = await deflated(new Uint8Array(16 << 20))
    const f = eager()
    for await (const chunk of f.inflate(payload)) {
      expect(chunk.length).toBeGreaterThan(0)
      break
    }
    await settle()
    expect(f.fed()).toBeLessThanOrEqual(1024)
    expect(f.inflated()).toBeLessThanOrEqual(sliceOut)
    expect(f.cancelled()).toBe(true)
  })

  it('reads a payload slice by slice through the platform', async () => {
    const payload = await deflated(new Uint8Array(1 + 4 * 64 * 64).fill(7, 1))
    const c = await inflateContainer(payload, 64, 64, inflate)
    expect(c.length).toBe(1 + 4 * 64 * 64)
  })
})

describe('containers', () => {
  it('ignores the padding bits of a row', () => {
    // Form 0, two colours (a count less one of 1), 3×1 pixels at 1 bit
    // an index, MSB first: 1, 0, 1, then padding bits set.
    const c = Uint8Array.of(0, 1, 1, 2, 3, 4, 5, 6, 0b10111111)
    expect(Array.from(decodeContainer(c, 3, 1))).toEqual([4, 5, 6, 255, 1, 2, 3, 255, 4, 5, 6, 255])
  })

  it('refuses a palette cut short', () => {
    expect(() => decodeContainer(Uint8Array.of(0, 0), 1, 1)).toThrow(ProtocolError)
  })
})

// Headers built by hand, for the forms the encoder does not send.
describe('image headers', () => {
  const riff = (chunks: Uint8Array[]): Uint8Array => {
    const body = chunks.reduce((n, c) => n + c.length, 4)
    const p = new Uint8Array(8 + body)
    p.set([0x52, 0x49, 0x46, 0x46, body & 0xff, (body >> 8) & 0xff, 0, 0, 0x57, 0x45, 0x42, 0x50])
    let at = 12
    for (const c of chunks) {
      p.set(c, at)
      at += c.length
    }
    return p
  }
  const chunk = (id: string, data: number[]): Uint8Array => {
    const p = new Uint8Array(8 + data.length + (data.length & 1))
    p.set([...id].map((c) => c.charCodeAt(0)))
    p.set([data.length & 0xff, (data.length >> 8) & 0xff, 0, 0], 4)
    p.set(data, 8)
    return p
  }
  const vp8 = (w: number, h: number): Uint8Array => chunk('VP8 ', [0x10, 0, 0, 0x9d, 0x01, 0x2a, w & 0xff, w >> 8, h & 0xff, h >> 8])
  const vp8x = (flags: number, w: number, h: number): Uint8Array =>
    chunk('VP8X', [flags, 0, 0, 0, (w - 1) & 0xff, (w - 1) >> 8, 0, (h - 1) & 0xff, (h - 1) >> 8, 0])

  it('reads a VP8 key frame', () => {
    expect(webpSize(riff([vp8(300, 200)]))).toEqual({ width: 300, height: 200 })
  })

  it('reads a VP8L header, width first', () => {
    // The width and height less one in 14 bits each, LSB first.
    const bits = (300 - 1) | ((200 - 1) << 14)
    const vp8l = chunk('VP8L', [0x2f, bits & 0xff, (bits >> 8) & 0xff, (bits >> 16) & 0xff, (bits >>> 24) & 0xff])
    expect(webpSize(riff([vp8l]))).toEqual({ width: 300, height: 200 })
  })

  it('refuses a VP8 inter frame', () => {
    const c = vp8(4, 4)
    c[8] |= 1
    expect(() => webpSize(riff([c]))).toThrow(ProtocolError)
  })

  it('reads an extended WebP whose image fills its canvas', () => {
    expect(webpSize(riff([vp8x(0x10, 64, 32), chunk('ALPH', [0, 0]), vp8(64, 32)]))).toEqual({ width: 64, height: 32 })
  })

  it('refuses an extended WebP whose canvas differs from its image', () => {
    expect(() => webpSize(riff([vp8x(0, 64, 32), vp8(8, 8)]))).toThrow(ProtocolError)
  })

  it('refuses an animated WebP', () => {
    expect(() => webpSize(riff([vp8x(0x02, 8, 8), vp8(8, 8)]))).toThrow(/animated/)
  })

  it('refuses a chunk longer than the payload', () => {
    const p = riff([vp8(8, 8)])
    p[16] = 0xff
    expect(() => webpSize(p)).toThrow(ProtocolError)
  })

  it('reads a PNG', () => {
    const p = Uint8Array.of(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52, 0, 0, 1, 0, 0, 0, 0, 3)
    expect(pngSize(p)).toEqual({ width: 256, height: 3 })
    expect(() => pngSize(p.subarray(0, 23))).toThrow(ProtocolError)
  })

  it('refuses a JPEG whose scan comes before a frame header', () => {
    expect(() => jpegSize(Uint8Array.of(0xff, 0xd8, 0xff, 0xda, 0, 2))).toThrow(ProtocolError)
  })

  it('reads a JPEG frame header after other segments and fill bytes', () => {
    const p = Uint8Array.of(0xff, 0xd8, 0xff, 0xe0, 0, 4, 1, 2, 0xff, 0xff, 0xc2, 0, 11, 8, 0, 9, 0, 17, 1, 1, 0x11, 0)
    expect(jpegSize(p)).toEqual({ width: 17, height: 9 })
  })

  it('refuses codecs that are not images', () => {
    expect(() => imageSize(Codec.Deflate, new Uint8Array(32))).toThrow(ProtocolError)
  })

  describe('EXIF', () => {
    // A TIFF header, big-endian, and one IFD entry: orientation 6.
    const tiff = [0x4d, 0x4d, 0, 0x2a, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0]
    const ascii = (s: string): number[] => [...s].map((c) => c.charCodeAt(0))
    const concat = (...parts: (Uint8Array | number[])[]): Uint8Array => {
      const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
      let at = 0
      for (const p of parts) {
        out.set(p, at)
        at += p.length
      }
      return out
    }
    const segment = (marker: number, data: number[]): number[] => [0xff, marker, (data.length + 2) >> 8, (data.length + 2) & 0xff, ...data]
    const png = (type: string, data: number[]): number[] => {
      // The CRC is not checked here, so zeros stand in for it.
      const n = data.length
      return [n >>> 24, (n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff, ...ascii(type), ...data, 0, 0, 0, 0]
    }
    const jpeg = fromHex(codecs.vectors.find((v) => v.name === 'jpeg: 24×16')?.payload ?? '')
    const exif = segment(0xe1, [...ascii('Exif'), 0, 0, ...tiff])

    it('drops a JPEG APP1 Exif segment and its fill bytes, before and after other segments', () => {
      const xmp = segment(0xe1, [...ascii('http://ns.adobe.com/xap/1.0/'), 0, 1, 2])
      const p = concat(jpeg.subarray(0, 2), [0xff], exif, xmp, exif, jpeg.subarray(2))
      expect(withoutExif(Codec.JPEG, p)).toEqual(concat(jpeg.subarray(0, 2), xmp, jpeg.subarray(2)))
      expect(jpegSize(withoutExif(Codec.JPEG, p))).toEqual({ width: 24, height: 16 })
    })

    it('returns a payload without EXIF as it is', () => {
      expect(withoutExif(Codec.JPEG, jpeg)).toBe(jpeg)
      const webp = riff([vp8(8, 8)])
      expect(withoutExif(Codec.WebP, webp)).toBe(webp)
      const raw = new Uint8Array(16)
      expect(withoutExif(Codec.Raw, raw)).toBe(raw)
    })

    it('leaves a JPEG whose structure breaks off where it breaks', () => {
      // A segment longer than the payload, before the Exif one.
      const p = concat(jpeg.subarray(0, 2), [0xff, 0xe0, 0xff, 0xff], exif)
      expect(withoutExif(Codec.JPEG, p)).toBe(p)
      // Exif in a segment after the scan starts is the scan's business.
      const q = concat(jpeg.subarray(0, 2), segment(0xda, [0]), exif)
      expect(withoutExif(Codec.JPEG, q)).toBe(q)
    })

    it('drops a PNG eXIf chunk', () => {
      const sig = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
      const ihdr = png('IHDR', [0, 0, 0, 24, 0, 0, 0, 16, 8, 2, 0, 0, 0])
      const idat = png('IDAT', [1, 2, 3])
      const end = png('IEND', [])
      const p = concat(sig, ihdr, png('eXIf', tiff), idat, end)
      expect(withoutExif(Codec.PNG, p)).toEqual(concat(sig, ihdr, idat, end))
      // Nothing after IEND is read.
      const after = concat(sig, ihdr, end, png('eXIf', tiff))
      expect(withoutExif(Codec.PNG, after)).toBe(after)
    })

    it("drops a WebP EXIF chunk, its padding, and VP8X's EXIF flag, and fixes the RIFF size", () => {
      const odd = [...tiff, 0] // 27 bytes, padded to 28
      const p = riff([vp8x(0x08 | 0x10, 24, 16), chunk('ALPH', [0, 0]), vp8(24, 16), chunk('EXIF', odd)])
      const want = riff([vp8x(0x10, 24, 16), chunk('ALPH', [0, 0]), vp8(24, 16)])
      expect(withoutExif(Codec.WebP, p)).toEqual(want)
      expect(webpSize(want)).toEqual({ width: 24, height: 16 })
    })
  })
})
