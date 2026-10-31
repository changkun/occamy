// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The framing of Occamy protocol v1 (docs/protocol.md, Framing): a
// 4-byte header of two little-endian u16, the type and the flags, then
// fixed-size fields and length-prefixed variable ones. Reader checks
// each field as it reads it and throws at the first failure, so the
// error is the one the Go reference implementation reports.

/** The ways a message fails to decode, each named as in the golden vectors. */
export type ErrorKind =
  | 'truncated'
  | 'too-large'
  | 'invalid-utf8'
  | 'trailing-data'
  | 'invalid'
  | 'unsupported'

/**
 * ProtocolError reports a message that breaks the protocol. The peer
 * that receives it closes the connection with the code that
 * closeCodeFor gives.
 */
export class ProtocolError extends Error {
  readonly kind: ErrorKind

  constructor(kind: ErrorKind, message?: string) {
    super(message === undefined ? `protocol: ${kind}` : `protocol: ${kind}: ${message}`)
    this.name = 'ProtocolError'
    this.kind = kind
  }
}

/** The size of a message header in bytes. */
export const HEADER_SIZE = 4

/** Bit 0 of a header's flags: a receiver that does not know the type skips the message. */
export const FLAG_IGNORABLE = 1

/** The largest message, header included, unless the handshake agrees on another. */
export const DEFAULT_MAX_MESSAGE_SIZE = 4 << 20

/** The smallest max_message_size a peer may state. */
export const MIN_MESSAGE_SIZE = 64 << 10

/** The largest tile or cursor side, in pixels, unless the client states another. */
export const DEFAULT_MAX_TEXTURE_SIZE = 4096

/**
 * The most pixels of a tile stored in a cache slot, so that a client
 * bounds its cache at 16 KiB a slot.
 */
export const MAX_CACHED_PIXELS = 4096

/** The largest number of elements in a list. */
export const MAX_COUNT = 0xffff

/** The sizes a receiver accepts. Zero means the protocol's default. */
export interface Limits {
  maxMessageSize: number
  maxTextureSize: number
  /** The client's tile cache slots, numbered from 1; zero means none. */
  cacheSlots: number
}

/** The limits of a peer that agreed on nothing. */
export const DEFAULT_LIMITS: Readonly<Limits> = Object.freeze({
  maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
  maxTextureSize: DEFAULT_MAX_TEXTURE_SIZE,
  cacheSlots: 0,
})

export function maxMessageSize(l: Limits): number {
  return l.maxMessageSize > 0 ? l.maxMessageSize : DEFAULT_MAX_MESSAGE_SIZE
}

export function maxTextureSize(l: Limits): number {
  return l.maxTextureSize > 0 ? l.maxTextureSize : DEFAULT_MAX_TEXTURE_SIZE
}

/** checkSize throws too-large if a message of n bytes exceeds l. */
export function checkSize(n: number, l: Limits): void {
  const limit = maxMessageSize(l)
  if (n > limit) {
    throw new ProtocolError('too-large', `${n} bytes, limit ${limit}`)
  }
}

/** A message header. */
export interface Header {
  type: number
  flags: number
}

/** isIgnorable reports whether a receiver that does not know h.type may skip the message. */
export function isIgnorable(h: Header): boolean {
  return (h.flags & FLAG_IGNORABLE) !== 0
}

/**
 * split splits msg into its header and its body, which is a view of msg.
 * It checks the message's size against l before anything else.
 */
export function split(msg: Uint8Array, l: Limits): { header: Header; body: Uint8Array } {
  checkSize(msg.length, l)
  if (msg.length < HEADER_SIZE) {
    throw new ProtocolError('truncated', 'shorter than a header')
  }
  const header = { type: msg[0] | (msg[1] << 8), flags: msg[2] | (msg[3] << 8) }
  return { header, body: msg.subarray(HEADER_SIZE) }
}

// A decoder that refuses what the protocol refuses: invalid UTF-8,
// including overlong forms, surrogates and code points above U+10FFFF,
// and that keeps a leading U+FEFF, so that a string decodes to what was
// sent.
const utf8Decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true })
const utf8Encoder = new TextEncoder()

/** Reader reads the fields of a message body in order. */
export class Reader {
  private readonly buf: Uint8Array
  private readonly view: DataView
  private off = 0

  constructor(body: Uint8Array) {
    this.buf = body
    this.view = new DataView(body.buffer, body.byteOffset, body.byteLength)
  }

  /** The number of unread bytes. */
  get remaining(): number {
    return this.buf.length - this.off
  }

  private need(n: number): number {
    if (this.remaining < n) {
      throw new ProtocolError('truncated')
    }
    const at = this.off
    this.off += n
    return at
  }

  u8(): number {
    return this.buf[this.need(1)]
  }

  u16(): number {
    return this.view.getUint16(this.need(2), true)
  }

  u32(): number {
    return this.view.getUint32(this.need(4), true)
  }

  u64(): bigint {
    return this.view.getBigUint64(this.need(8), true)
  }

  i8(): number {
    return this.view.getInt8(this.need(1))
  }

  i16(): number {
    return this.view.getInt16(this.need(2), true)
  }

  i32(): number {
    return this.view.getInt32(this.need(4), true)
  }

  i64(): bigint {
    return this.view.getBigInt64(this.need(8), true)
  }

  /** bool reads a u8 that is 0 or 1. */
  bool(): boolean {
    const v = this.u8()
    if (v > 1) {
      throw new ProtocolError('invalid', `bool ${v}`)
    }
    return v === 1
  }

  /**
   * count reads the u16 count of a list whose elements each take at least
   * minSize bytes, and refuses one that cannot fit in the rest of the
   * body, so that a forged count allocates nothing.
   */
  count(minSize: number): number {
    const n = this.u16()
    if (n > Math.floor(this.remaining / minSize)) {
      throw new ProtocolError('truncated', `a list of ${n} past the end`)
    }
    return n
  }

  /** bytes reads a byte string as a view into the body, without copying it. */
  bytes(): Uint8Array {
    const n = this.u32()
    const at = this.need(n)
    return this.buf.subarray(at, at + n)
  }

  /** string reads a UTF-8 string, and refuses invalid UTF-8. */
  string(): string {
    const p = this.bytes()
    try {
      return utf8Decoder.decode(p)
    } catch {
      throw new ProtocolError('invalid-utf8')
    }
  }

  /** finish refuses bytes left after the last field. */
  finish(): void {
    if (this.remaining > 0) {
      throw new ProtocolError('trailing-data', `${this.remaining} bytes after the last field`)
    }
  }
}

/** Writer appends the fields of a message. */
export class Writer {
  private buf: Uint8Array
  private view: DataView
  private len = 0

  constructor(size = 64) {
    this.buf = new Uint8Array(size)
    this.view = new DataView(this.buf.buffer)
  }

  private grow(n: number): number {
    const at = this.len
    if (at + n > this.buf.length) {
      const next = new Uint8Array(Math.max(2 * this.buf.length, at + n))
      next.set(this.buf.subarray(0, at))
      this.buf = next
      this.view = new DataView(next.buffer)
    }
    this.len += n
    return at
  }

  header(type: number, flags = 0): this {
    return this.u16(type).u16(flags)
  }

  u8(v: number): this {
    const at = this.grow(1)
    this.view.setUint8(at, v)
    return this
  }

  u16(v: number): this {
    const at = this.grow(2)
    this.view.setUint16(at, v, true)
    return this
  }

  u32(v: number): this {
    const at = this.grow(4)
    this.view.setUint32(at, v, true)
    return this
  }

  u64(v: bigint): this {
    const at = this.grow(8)
    this.view.setBigUint64(at, v, true)
    return this
  }

  i8(v: number): this {
    const at = this.grow(1)
    this.view.setInt8(at, v)
    return this
  }

  i16(v: number): this {
    const at = this.grow(2)
    this.view.setInt16(at, v, true)
    return this
  }

  i32(v: number): this {
    const at = this.grow(4)
    this.view.setInt32(at, v, true)
    return this
  }

  i64(v: bigint): this {
    const at = this.grow(8)
    this.view.setBigInt64(at, v, true)
    return this
  }

  bool(v: boolean): this {
    return this.u8(v ? 1 : 0)
  }

  /** count writes the u16 count of a list, whose elements the caller writes next. */
  count(n: number): this {
    if (!Number.isInteger(n) || n < 0 || n > MAX_COUNT) {
      throw new RangeError(`protocol: a list of ${n}, longer than a u16 count`)
    }
    return this.u16(n)
  }

  bytes(p: Uint8Array): this {
    this.u32(p.length)
    const at = this.grow(p.length)
    this.buf.set(p, at)
    return this
  }

  /**
   * string writes s as UTF-8. A lone surrogate, which UTF-8 cannot carry,
   * goes as U+FFFD, as the protocol asks of a sender.
   */
  string(s: string): this {
    return this.bytes(utf8Encoder.encode(s))
  }

  /** raw writes p as it is, without a length. */
  raw(p: Uint8Array): this {
    const at = this.grow(p.length)
    this.buf.set(p, at)
    return this
  }

  /** finish returns the message written so far. */
  finish(): Uint8Array {
    return this.buf.slice(0, this.len)
  }
}
