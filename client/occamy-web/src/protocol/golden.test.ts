// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Every vector of protocol/testdata/framing.json and messages.json,
// both ways: each valid message decodes to its fields and encodes back
// to its bytes, built from its fields too, and each invalid one fails
// with the named error, as docs/protocol.md (Versioning) requires.

import { describe, expect, test } from 'vitest'

import {
  decodeHelloQuery,
  encodeHelloQuery,
  tokenFromSubprotocols,
  tokenSubprotocol,
} from './handshake'
import { type Message, type Peer, accept, decode, encode } from './messages'
import { framing, fromHex, messages, toHex } from './testing/vectors'
import { type Limits, ProtocolError, Reader, Writer, isIgnorable, split } from './wire'

const typeNames: Record<number, Message['type']> = {
  0x0001: 'ping',
  0x0002: 'pong',
  0x0003: 'close',
  0x0101: 'hello',
  0x0102: 'key',
  0x0103: 'text',
  0x0104: 'term-input',
  0x0105: 'pointer',
  0x0106: 'wheel',
  0x0107: 'resize-request',
  0x0108: 'client-clipboard',
  0x0109: 'view',
  0x010a: 'frame-ack',
  0x010b: 'prompt-reply',
  0x0201: 'welcome',
  0x0202: 'resize',
  0x0203: 'tile',
  0x0204: 'copy-rect',
  0x0205: 'frame-end',
  0x0206: 'cursor',
  0x0207: 'cursor-move',
  0x0208: 'term-data',
  0x0209: 'term-snapshot',
  0x020a: 'server-clipboard',
  0x020b: 'bell',
  0x020c: 'title',
  0x020d: 'prompt',
  0x020e: 'viewer-joined',
  0x020f: 'viewer-left',
}

// The vectors' field names are docs/protocol.md's; this package's are
// the same in camel case.
const snake = (k: string): string => k.replace(/[A-Z]/g, (c) => '_' + c.toLowerCase())
const camel = (k: string): string => k.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())

// toFields renders a message as the vectors write fields: integers as
// decimal strings, byte strings as hex.
function toFields(v: unknown): unknown {
  if (typeof v === 'bigint' || typeof v === 'number') {
    return String(v)
  }
  if (v instanceof Uint8Array) {
    return toHex(v)
  }
  if (Array.isArray(v)) {
    return v.map(toFields)
  }
  if (v !== null && typeof v === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, x] of Object.entries(v)) {
      if (k !== 'type') {
        out[snake(k)] = toFields(x)
      }
    }
    return out
  }
  return v
}

const stringFields = new Set(['reason', 'name', 'text', 'mime', 'title', 'session_id', 'instruction', 'label', 'answers'])
const bigintFields = new Set(['time', 'server_time'])
const byteFields = new Set(['data', 'payload', 'value'])

// fromFields builds a message's fields from a vector's.
function fromFields(fields: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(fields)) {
    out[camel(k)] = fromField(k, v)
  }
  return out
}

function fromField(k: string, v: unknown): unknown {
  if (Array.isArray(v)) {
    return v.map((x) => (x !== null && typeof x === 'object' ? fromFields(x as Record<string, unknown>) : fromField(k, x)))
  }
  if (v !== null && typeof v === 'object') {
    return fromFields(v as Record<string, unknown>)
  }
  if (typeof v !== 'string' || stringFields.has(k)) {
    return v
  }
  if (bigintFields.has(k)) {
    return BigInt(v)
  }
  if (byteFields.has(k)) {
    return fromHex(v)
  }
  return Number(v)
}

function limitsOf(l: { max_message_size: number; max_texture_size: number; cache_slots: number }): Limits {
  return { maxMessageSize: l.max_message_size, maxTextureSize: l.max_texture_size, cacheSlots: l.cache_slots }
}

const senders = (from: 'client' | 'server' | 'either'): Peer[] => (from === 'either' ? ['client', 'server'] : [from])

function kindOf(f: () => unknown): string {
  try {
    f()
  } catch (err) {
    if (err instanceof ProtocolError) {
      return err.kind
    }
    throw err
  }
  return 'no error'
}

describe('messages.json', () => {
  const limits = limitsOf(messages.limits)

  test('has every message type', () => {
    const types = new Set(messages.messages.map((v) => v.type))
    expect([...types].sort((a, b) => a - b)).toEqual(Object.keys(typeNames).map(Number).sort((a, b) => a - b))
  })

  for (const v of messages.messages) {
    for (const from of senders(v.from)) {
      test(`${v.name} from the ${from}`, () => {
        const msg = fromHex(v.hex)
        const m = decode(msg, from, limits)
        expect(m).not.toBeNull()
        expect(m?.type).toBe(typeNames[v.type])
        expect(toFields(m)).toEqual(v.fields)
        expect(toHex(encode(m as Message))).toBe(v.hex)
        const built = { type: typeNames[v.type], ...fromFields(v.fields) } as Message
        expect(toHex(encode(built))).toBe(v.hex)
      })
    }
  }

  for (const v of messages.invalid) {
    for (const from of senders(v.from)) {
      test(`invalid: ${v.name} from the ${from}`, () => {
        const l = v.limits === undefined ? limits : limitsOf(v.limits)
        expect(kindOf(() => decode(fromHex(v.hex), from, l))).toBe(v.error)
      })
    }
  }

  for (const v of messages.hello_query) {
    test(`hello query: ${v.name}`, () => {
      if (v.error !== undefined) {
        expect(kindOf(() => decodeHelloQuery(v.query))).toBe(v.error)
        return
      }
      const h = decodeHelloQuery(v.query)
      expect(toHex(encode(h))).toBe(v.hex)
      expect(encodeHelloQuery(h)).toBe(v.query)
    })
  }

  for (const v of messages.tokens) {
    test(`token ${JSON.stringify(v.token)}`, () => {
      expect(tokenSubprotocol(v.token)).toBe(v.subprotocol)
      expect(tokenFromSubprotocols(['occamy.v1', v.subprotocol])).toBe(v.token)
    })
  }

  for (const v of messages.subprotocols) {
    test(`subprotocols: ${v.name}`, () => {
      if (v.error !== undefined) {
        expect(kindOf(() => tokenFromSubprotocols(v.offered))).toBe(v.error)
        return
      }
      expect(tokenFromSubprotocols(v.offered)).toBe(v.token)
    })
  }
})

type Kind = 'uint8' | 'uint16' | 'uint32' | 'uint64' | 'int8' | 'int16' | 'int32' | 'int64'

function readInt(r: Reader, kind: Kind): number | bigint {
  switch (kind) {
    case 'uint8': return r.u8()
    case 'uint16': return r.u16()
    case 'uint32': return r.u32()
    case 'uint64': return r.u64()
    case 'int8': return r.i8()
    case 'int16': return r.i16()
    case 'int32': return r.i32()
    case 'int64': return r.i64()
  }
}

function writeInt(w: Writer, kind: Kind, value: string): Writer {
  switch (kind) {
    case 'uint8': return w.u8(Number(value))
    case 'uint16': return w.u16(Number(value))
    case 'uint32': return w.u32(Number(value))
    case 'uint64': return w.u64(BigInt(value))
    case 'int8': return w.i8(Number(value))
    case 'int16': return w.i16(Number(value))
    case 'int32': return w.i32(Number(value))
    case 'int64': return w.i64(BigInt(value))
  }
}

describe('framing.json', () => {
  for (const v of framing.header) {
    test(`header: ${v.name}`, () => {
      const { header } = split(fromHex(v.hex), { maxMessageSize: 0, maxTextureSize: 0, cacheSlots: 0 })
      expect(header).toEqual({ type: v.type, flags: v.flags })
      expect(isIgnorable(header)).toBe(v.ignorable)
      expect(toHex(new Writer().header(v.type, v.flags).finish())).toBe(v.hex)
    })
  }

  for (const v of framing.integers) {
    test(`integer: ${v.name}`, () => {
      const r = new Reader(fromHex(v.hex))
      expect(String(readInt(r, v.kind as Kind))).toBe(v.value)
      r.finish()
      expect(toHex(writeInt(new Writer(), v.kind as Kind, v.value).finish())).toBe(v.hex)
    })
  }

  for (const v of framing.bytes) {
    test(`bytes: ${v.name}`, () => {
      const r = new Reader(fromHex(v.hex))
      expect(toHex(r.bytes())).toBe(v.value)
      r.finish()
      expect(toHex(new Writer().bytes(fromHex(v.value)).finish())).toBe(v.hex)
    })
  }

  for (const v of framing.strings) {
    test(`string: ${v.name}`, () => {
      const r = new Reader(fromHex(v.hex))
      expect(r.string()).toBe(v.value)
      r.finish()
      expect(toHex(new Writer().string(v.value).finish())).toBe(v.hex)
    })
  }

  for (const v of framing.invalid) {
    test(`invalid: ${v.name}`, () => {
      const p = fromHex(v.hex)
      if (v.kind === 'message') {
        const l = { maxMessageSize: v.max_message_size ?? 0, maxTextureSize: 0, cacheSlots: 0 }
        expect(kindOf(() => split(p, l))).toBe(v.error)
        return
      }
      const r = new Reader(p)
      const read = v.kind === 'bytes' ? () => r.bytes() : v.kind === 'string' ? () => r.string() : () => readInt(r, v.kind as Kind)
      expect(kindOf(read)).toBe(v.error)
    })
  }

  for (const v of framing.dispatch) {
    test(`dispatch: ${v.name}`, () => {
      const { header } = split(fromHex(v.hex), { maxMessageSize: 0, maxTextureSize: 0, cacheSlots: 0 })
      let action: string
      try {
        action = accept(header, (t) => v.registered.includes(t)) ? 'decode' : 'skip'
      } catch (err) {
        action = err instanceof ProtocolError ? err.kind : String(err)
      }
      expect(action).toBe(v.action)
    })
  }
})

describe('strings', () => {
  test('a lone surrogate goes as U+FFFD', () => {
    const p = new Writer().string('a\ud800b').finish()
    expect(toHex(p)).toBe('0500000061efbfbd62')
  })

  test('a leading U+FEFF survives', () => {
    const p = new Writer().string('﻿x').finish()
    expect(new Reader(p).string()).toBe('﻿x')
  })
})
