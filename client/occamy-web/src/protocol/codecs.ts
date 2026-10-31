// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The payloads of the codecs (docs/protocol.md, Codecs): the deflate
// container, and the sizes that WebP, JPEG and PNG headers claim, which
// a receiver checks against the message's rectangle before it decodes
// anything. Everything here is pure computation over bytes; inflating
// and decoding images is the platform's (decode.ts).

import { Codec } from './messages'
import { ProtocolError } from './wire'

/** Pixels are RGBA, four bytes each, row by row without padding, with straight alpha. */
export type Pixels = Uint8ClampedArray

/**
 * Inflate inflates a raw DEFLATE stream (RFC 1951), yielding its output
 * in chunks. It throws if the payload is not one DEFLATE stream with
 * nothing after it; a consumer that stops iterating cancels it.
 */
export type Inflate = (payload: Uint8Array) => AsyncIterable<Uint8Array>

/**
 * containerCap is the longest deflate container of a w×h rectangle:
 * raw pixels with alpha, or 256 colours with alpha and a byte an index,
 * which is the longer for a rectangle of up to 341 pixels.
 */
export function containerCap(w: number, h: number): number {
  return Math.max(1 + 4 * w * h, 1026 + w * h)
}

/**
 * inflateContainer inflates a deflate payload, refusing it as soon as
 * it inflates past the rectangle's longest container: it counts the
 * bytes the stream yields and stops at the chunk that takes them past
 * the cap, so it keeps at most the cap, whatever the payload inflates
 * to. Its buffer grows with what the stream yields, up to the cap, so a
 * short container of a large rectangle takes little memory.
 *
 * The stream may have inflated beyond what it yielded: the rest of the
 * chunk past the cap, DecompressionStream's chunks being 16 KiB in Node
 * and 64 KiB in Chrome, and what it inflated ahead, which slicedInflate
 * bounds by a slice of the payload, about 1 MiB.
 */
export async function inflateContainer(payload: Uint8Array, w: number, h: number, inflate: Inflate): Promise<Uint8Array> {
  const cap = containerCap(w, h)
  let buf = new Uint8Array(Math.min(cap, 4096))
  let n = 0
  try {
    for await (const chunk of inflate(payload)) {
      if (n + chunk.length > cap) {
        // Leaving the loop cancels the stream.
        throw new ProtocolError('invalid', `a deflate container longer than ${cap} bytes for ${w}x${h} pixels`)
      }
      if (n + chunk.length > buf.length) {
        const grown = new Uint8Array(Math.min(cap, Math.max(2 * buf.length, n + chunk.length)))
        grown.set(buf.subarray(0, n))
        buf = grown
      }
      buf.set(chunk, n)
      n += chunk.length
    }
  } catch (err) {
    if (err instanceof ProtocolError) {
      throw err
    }
    throw new ProtocolError('invalid', `a deflate payload that does not inflate: ${String(err)}`)
  }
  return buf.subarray(0, n)
}

/**
 * decodeContainer reads a deflate container of w×h pixels into dst, or
 * a new array, and refuses a form other than 0 to 3, a container longer
 * or shorter than its form and the rectangle call for, and an index
 * beyond the palette.
 */
export function decodeContainer(c: Uint8Array, w: number, h: number, dst?: Pixels): Pixels {
  const out = dst ?? new Uint8ClampedArray(4 * w * h)
  if (c.length === 0) {
    throw new ProtocolError('invalid', 'an empty deflate container')
  }
  const form = c[0]
  if (form > 3) {
    throw new ProtocolError('invalid', `deflate container form ${form}`)
  }
  const alpha = (form & 2) !== 0
  const bpp = alpha ? 4 : 3
  const pixels = w * h
  if ((form & 1) !== 0) {
    // Raw pixels.
    if (c.length !== 1 + bpp * pixels) {
      throw new ProtocolError('invalid', `a raw container of ${c.length} bytes for ${w}x${h} pixels`)
    }
    for (let i = 0, s = 1, d = 0; i < pixels; i++, s += bpp, d += 4) {
      out[d] = c[s]
      out[d + 1] = c[s + 1]
      out[d + 2] = c[s + 2]
      out[d + 3] = alpha ? c[s + 3] : 255
    }
    return out
  }
  if (c.length < 2) {
    throw new ProtocolError('invalid', 'a palette container without its palette')
  }
  const colours = c[1] + 1
  const indices = 2 + colours * bpp
  if (c.length < indices) {
    throw new ProtocolError('invalid', `a palette of ${colours} colours cut short`)
  }
  // The palette as RGBA words in memory order, so that a pixel is one
  // store.
  const palette = new Uint32Array(colours)
  const bytes = new Uint8Array(palette.buffer)
  for (let i = 0, s = 2; i < colours; i++, s += bpp) {
    bytes[4 * i] = c[s]
    bytes[4 * i + 1] = c[s + 1]
    bytes[4 * i + 2] = c[s + 2]
    bytes[4 * i + 3] = alpha ? c[s + 3] : 255
  }
  const words = new Uint32Array(out.buffer, out.byteOffset, pixels)
  if (colours === 1) {
    if (c.length !== indices) {
      throw new ProtocolError('invalid', 'indices after a palette of one colour')
    }
    words.fill(palette[0])
    return out
  }
  const bits = colours <= 2 ? 1 : colours <= 4 ? 2 : colours <= 16 ? 4 : 8
  const rowBytes = Math.ceil((w * bits) / 8)
  if (c.length !== indices + rowBytes * h) {
    throw new ProtocolError('invalid', `${c.length - indices} bytes of indices for ${w}x${h} pixels at ${bits} bits`)
  }
  const mask = (1 << bits) - 1
  const perByte = 8 / bits
  for (let y = 0, row = indices, d = 0; y < h; y++, row += rowBytes) {
    for (let x = 0; x < w; x++, d++) {
      const shift = 8 - bits * ((x % perByte) + 1)
      const index = (c[row + Math.floor(x / perByte)] >> shift) & mask
      if (index >= colours) {
        throw new ProtocolError('invalid', `index ${index} beyond a palette of ${colours}`)
      }
      words[d] = palette[index]
    }
  }
  return out
}

/** decodeDeflate inflates and reads a deflate payload of w×h pixels. */
export async function decodeDeflate(payload: Uint8Array, w: number, h: number, inflate: Inflate, dst?: Pixels): Promise<Pixels> {
  return decodeContainer(await inflateContainer(payload, w, h, inflate), w, h, dst)
}

/** decodeRaw copies a raw payload of w×h pixels into dst, or a new array. */
export function decodeRaw(payload: Uint8Array, w: number, h: number, dst?: Pixels): Pixels {
  if (payload.length !== 4 * w * h) {
    throw new ProtocolError('invalid', `raw ${w}x${h} pixels with ${payload.length} bytes`)
  }
  const out = dst ?? new Uint8ClampedArray(4 * w * h)
  out.set(payload)
  return out
}

/** The MIME types of the image codecs. */
export const imageTypes: Partial<Record<Codec, string>> = {
  [Codec.PNG]: 'image/png',
  [Codec.JPEG]: 'image/jpeg',
  [Codec.WebP]: 'image/webp',
}

export function isImageCodec(c: number): boolean {
  return c === Codec.PNG || c === Codec.JPEG || c === Codec.WebP
}

/** A size an image's header claims. */
export interface Size {
  width: number
  height: number
}

/**
 * checkImageSize refuses an image payload whose header claims a size
 * other than w×h, before anything decodes it: a few bytes of header can
 * claim a frame that takes hundreds of megabytes to decode into.
 */
export function checkImageSize(codec: number, payload: Uint8Array, w: number, h: number): void {
  const s = imageSize(codec, payload)
  if (s.width !== w || s.height !== h) {
    throw new ProtocolError('invalid', `an image whose header claims ${s.width}x${s.height} for a ${w}x${h} rectangle`)
  }
}

/** imageSize returns the size an image payload's header claims, and throws invalid if it has none. */
export function imageSize(codec: number, payload: Uint8Array): Size {
  switch (codec) {
    case Codec.WebP:
      return webpSize(payload)
    case Codec.JPEG:
      return jpegSize(payload)
    case Codec.PNG:
      return pngSize(payload)
  }
  throw new ProtocolError('invalid', `codec ${codec} is not an image`)
}

const u16le = (p: Uint8Array, i: number): number => p[i] | (p[i + 1] << 8)
const u24le = (p: Uint8Array, i: number): number => p[i] | (p[i + 1] << 8) | (p[i + 2] << 16)
const u32le = (p: Uint8Array, i: number): number => (p[i] | (p[i + 1] << 8) | (p[i + 2] << 16) | (p[i + 3] << 24)) >>> 0
const u16be = (p: Uint8Array, i: number): number => (p[i] << 8) | p[i + 1]
const u32be = (p: Uint8Array, i: number): number => ((p[i] << 24) | (p[i + 1] << 16) | (p[i + 2] << 8) | p[i + 3]) >>> 0
const fourcc = (p: Uint8Array, i: number): string => String.fromCharCode(p[i], p[i + 1], p[i + 2], p[i + 3])

function badHeader(what: string): ProtocolError {
  return new ProtocolError('invalid', `an image without a valid ${what} header`)
}

/**
 * webpSize reads a WebP file's size: a lossy (VP8) key frame's, a
 * lossless (VP8L) image's, or an extended (VP8X) file's canvas, which
 * must be a still image whose VP8 or VP8L chunk is the same size, as
 * libwebp requires of a still image.
 */
export function webpSize(p: Uint8Array): Size {
  if (p.length < 20 || fourcc(p, 0) !== 'RIFF' || fourcc(p, 8) !== 'WEBP') {
    throw badHeader('WebP')
  }
  const first = webpChunk(p, 12)
  if (first.id !== 'VP8X') {
    return webpImageSize(p, first)
  }
  const d = first.data
  if (first.size < 10) {
    throw badHeader('WebP VP8X')
  }
  if ((p[d] & 0x02) !== 0) {
    throw new ProtocolError('invalid', 'an animated WebP')
  }
  const canvas = { width: u24le(p, d + 4) + 1, height: u24le(p, d + 7) + 1 }
  for (let at = first.next; at + 8 <= p.length;) {
    const c = webpChunk(p, at)
    if (c.id === 'VP8 ' || c.id === 'VP8L') {
      const s = webpImageSize(p, c)
      if (s.width !== canvas.width || s.height !== canvas.height) {
        throw new ProtocolError('invalid', `a WebP canvas of ${canvas.width}x${canvas.height} around an image of ${s.width}x${s.height}`)
      }
      return canvas
    }
    at = c.next
  }
  throw badHeader('WebP image')
}

interface Chunk {
  id: string
  size: number
  data: number
  next: number
}

function webpChunk(p: Uint8Array, at: number): Chunk {
  if (at + 8 > p.length) {
    throw badHeader('WebP chunk')
  }
  const size = u32le(p, at + 4)
  const data = at + 8
  if (data + size > p.length) {
    throw badHeader('WebP chunk')
  }
  return { id: fourcc(p, at), size, data, next: data + size + (size & 1) }
}

function webpImageSize(p: Uint8Array, c: Chunk): Size {
  const d = c.data
  if (c.id === 'VP8 ') {
    // A frame tag of 3 bytes whose bit 0 is 0 for a key frame, the
    // start code 9d 01 2a, then the width and height in 14 bits each
    // beside 2 bits of scale.
    if (c.size < 10 || (p[d] & 1) !== 0 || p[d + 3] !== 0x9d || p[d + 4] !== 0x01 || p[d + 5] !== 0x2a) {
      throw badHeader('WebP VP8')
    }
    return { width: u16le(p, d + 6) & 0x3fff, height: u16le(p, d + 8) & 0x3fff }
  }
  if (c.id === 'VP8L') {
    // The signature 0x2f, then the width and height less one in 14 bits
    // each, the alpha hint, and a version of 3 bits that is 0.
    if (c.size < 5 || p[d] !== 0x2f) {
      throw badHeader('WebP VP8L')
    }
    const bits = u32le(p, d + 1)
    if (bits >>> 29 !== 0) {
      throw badHeader('WebP VP8L')
    }
    return { width: (bits & 0x3fff) + 1, height: ((bits >>> 14) & 0x3fff) + 1 }
  }
  throw badHeader('WebP image')
}

/**
 * jpegSize reads a JPEG file's size from its frame header (SOF), which
 * precedes the first scan. A frame of height 0, whose height a later
 * DNL marker gives, is refused like any other size that differs.
 */
export function jpegSize(p: Uint8Array): Size {
  if (p.length < 4 || p[0] !== 0xff || p[1] !== 0xd8) {
    throw badHeader('JPEG')
  }
  let i = 2
  while (i < p.length) {
    if (p[i] !== 0xff) {
      throw badHeader('JPEG')
    }
    while (i < p.length && p[i] === 0xff) {
      i++ // fill bytes
    }
    if (i >= p.length) {
      break
    }
    const marker = p[i++]
    if ((marker >= 0xd0 && marker <= 0xd7) || marker === 0x01) {
      continue // markers without a length
    }
    if (marker === 0x00 || marker === 0xd8 || marker === 0xd9 || marker === 0xda) {
      break // a scan, or the end, before a frame header
    }
    if (i + 2 > p.length) {
      break
    }
    const len = u16be(p, i)
    if (len < 2 || i + len > p.length) {
      break
    }
    const sof = marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc
    if (sof) {
      if (len < 8) {
        break
      }
      return { width: u16be(p, i + 5), height: u16be(p, i + 3) }
    }
    i += len
  }
  throw badHeader('JPEG frame')
}

const pngSignature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]

/** pngSize reads a PNG file's size from its IHDR chunk, which comes first. */
export function pngSize(p: Uint8Array): Size {
  if (p.length < 24 || pngSignature.some((b, i) => p[i] !== b) || u32be(p, 8) !== 13 || fourcc(p, 12) !== 'IHDR') {
    throw badHeader('PNG')
  }
  return { width: u32be(p, 16), height: u32be(p, 20) }
}

/**
 * withoutExif returns an image payload without the EXIF metadata that a
 * browser reads an orientation from: a JPEG's APP1 Exif segments before
 * its first scan, a PNG's eXIf chunks, and an extended WebP's EXIF
 * chunk. A browser applies that orientation whatever createImageBitmap
 * is told: Chrome 156 turns a 24×16 JPEG or PNG with orientation 6 into
 * 16×24 under imageOrientation 'none', which the HTML standard has since
 * made another name for 'from-image'. Orientations that only flip keep
 * the size, which the size check cannot catch, and the tile would be
 * drawn mirrored. The server writes no EXIF.
 *
 * A payload without any comes back as it is, not copied. The walk stops
 * where the structure stops making sense and keeps the rest unchanged,
 * for the decoder to refuse.
 */
export function withoutExif(codec: number, p: Uint8Array): Uint8Array {
  switch (codec) {
    case Codec.JPEG:
      return cut(p, jpegExif(p))
    case Codec.PNG:
      return cut(p, pngExif(p))
    case Codec.WebP: {
      const drop = webpExif(p)
      if (drop.length === 0) {
        return p
      }
      const out = cut(p, drop)
      const size = out.length - 8
      out.set([size & 0xff, (size >>> 8) & 0xff, (size >>> 16) & 0xff, size >>> 24], 4)
      out[20] &= ~0x08 // VP8X's EXIF flag
      return out
    }
  }
  return p
}

type Span = [start: number, end: number]

function jpegExif(p: Uint8Array): Span[] {
  const drop: Span[] = []
  if (p.length < 4 || p[0] !== 0xff || p[1] !== 0xd8) {
    return drop
  }
  let i = 2
  while (i < p.length && p[i] === 0xff) {
    const start = i
    while (i < p.length && p[i] === 0xff) {
      i++ // fill bytes
    }
    if (i >= p.length) {
      break
    }
    const marker = p[i++]
    if ((marker >= 0xd0 && marker <= 0xd7) || marker === 0x01) {
      continue // markers without a length
    }
    if (marker === 0x00 || marker === 0xd8 || marker === 0xd9 || marker === 0xda || i + 2 > p.length) {
      break // a scan, or the end
    }
    const len = u16be(p, i)
    if (len < 2 || i + len > p.length) {
      break
    }
    if (marker === 0xe1 && len >= 7 && fourcc(p, i + 2) === 'Exif' && p[i + 6] === 0) {
      drop.push([start, i + len])
    }
    i += len
  }
  return drop
}

function pngExif(p: Uint8Array): Span[] {
  const drop: Span[] = []
  if (p.length < 8 || pngSignature.some((b, i) => p[i] !== b)) {
    return drop
  }
  for (let at = 8; at + 12 <= p.length;) {
    // The length, the type, the data, and a CRC of 4 bytes.
    const next = at + 12 + u32be(p, at)
    if (next > p.length) {
      break
    }
    const id = fourcc(p, at + 4)
    if (id === 'eXIf') {
      drop.push([at, next])
    } else if (id === 'IEND') {
      break
    }
    at = next
  }
  return drop
}

function webpExif(p: Uint8Array): Span[] {
  const drop: Span[] = []
  // EXIF belongs to the extended format, whose VP8X chunk comes first.
  if (p.length < 30 || fourcc(p, 0) !== 'RIFF' || fourcc(p, 8) !== 'WEBP' || fourcc(p, 12) !== 'VP8X') {
    return drop
  }
  for (let at = 12; at + 8 <= p.length;) {
    const size = u32le(p, at + 4)
    // A chunk's data is padded to an even length.
    const next = at + 8 + size + (size & 1)
    if (next > p.length) {
      break
    }
    if (fourcc(p, at) === 'EXIF') {
      drop.push([at, next])
    }
    at = next
  }
  return drop
}

/** cut returns p without the spans, which are in order and apart, or p itself if there are none. */
function cut(p: Uint8Array, drop: Span[]): Uint8Array {
  if (drop.length === 0) {
    return p
  }
  const out = new Uint8Array(p.length - drop.reduce((n, [s, e]) => n + e - s, 0))
  let from = 0
  let to = 0
  for (const [s, e] of drop) {
    out.set(p.subarray(from, s), to)
    to += s - from
    from = e
  }
  out.set(p.subarray(from), to)
  return out
}
