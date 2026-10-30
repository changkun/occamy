// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The handshake of protocol v1 (docs/protocol.md, Handshake): the
// client's hello travels in the upgrade request's hello parameter, and
// its token in Sec-WebSocket-Protocol, never in the URL. The decoding
// functions are the server's; the tests and fake servers use them.

import { type Hello, MessageType, decode, encode } from './messages'
import { ProtocolError, split } from './wire'

/** The WebSocket subprotocol of protocol v1. */
export const SUBPROTOCOL = 'occamy.v1'

/** The query parameter that carries hello. */
export const HELLO_PARAMETER = 'hello'

/** The largest hello, header included, in bytes. */
export const MAX_HELLO_SIZE = 2048

const TOKEN_PREFIX = `${SUBPROTOCOL}.token.`

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
const VALUES = new Int8Array(128).fill(-1)
for (let i = 0; i < ALPHABET.length; i++) {
  VALUES[ALPHABET.charCodeAt(i)] = i
}

/** base64url encodes p in base64url without padding (RFC 4648, section 5). */
export function base64url(p: Uint8Array): string {
  let s = ''
  let i = 0
  for (; i + 3 <= p.length; i += 3) {
    const v = (p[i] << 16) | (p[i + 1] << 8) | p[i + 2]
    s += ALPHABET[v >> 18] + ALPHABET[(v >> 12) & 63] + ALPHABET[(v >> 6) & 63] + ALPHABET[v & 63]
  }
  if (p.length - i === 1) {
    const v = p[i] << 16
    s += ALPHABET[v >> 18] + ALPHABET[(v >> 12) & 63]
  } else if (p.length - i === 2) {
    const v = (p[i] << 16) | (p[i + 1] << 8)
    s += ALPHABET[v >> 18] + ALPHABET[(v >> 12) & 63] + ALPHABET[(v >> 6) & 63]
  }
  return s
}

/**
 * fromBase64url decodes s strictly: only the base64url alphabet, no
 * padding, and zero trailing bits, so that each value has one encoding.
 * It throws invalid otherwise.
 */
export function fromBase64url(s: string): Uint8Array {
  if (s.length % 4 === 1) {
    throw new ProtocolError('invalid', 'not base64url')
  }
  const out = new Uint8Array(Math.floor((s.length * 6) / 8))
  let acc = 0
  let bits = 0
  let n = 0
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    const v = c < 128 ? VALUES[c] : -1
    if (v < 0) {
      throw new ProtocolError('invalid', 'not base64url')
    }
    acc = ((acc << 6) | v) & 0xffffff
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out[n++] = (acc >> bits) & 0xff
    }
  }
  if ((acc & ((1 << bits) - 1)) !== 0) {
    throw new ProtocolError('invalid', 'base64url with trailing bits')
  }
  return out
}

/** encodeHelloQuery encodes h for the hello parameter, and throws too-large past 2048 bytes. */
export function encodeHelloQuery(h: Hello): string {
  const msg = encode(h)
  if (msg.length > MAX_HELLO_SIZE) {
    throw new ProtocolError('too-large', `hello of ${msg.length} bytes, limit ${MAX_HELLO_SIZE}`)
  }
  return base64url(msg)
}

/**
 * decodeHelloQuery decodes the hello parameter as a server does: the
 * size first, then the encoding, then the message.
 */
export function decodeHelloQuery(s: string): Hello {
  const n = Math.floor((s.length * 6) / 8)
  if (n > MAX_HELLO_SIZE) {
    throw new ProtocolError('too-large', `hello of ${n} bytes, limit ${MAX_HELLO_SIZE}`)
  }
  const msg = fromBase64url(s)
  const limits = { maxMessageSize: MAX_HELLO_SIZE, maxTextureSize: 0, cacheSlots: 0 }
  const { header } = split(msg, limits)
  if (header.type !== MessageType.Hello) {
    throw new ProtocolError('invalid', `the hello query holds type 0x${header.type.toString(16)}`)
  }
  return decode(msg, 'client', limits) as Hello
}

/** tokenSubprotocol returns the subprotocol that carries token beside occamy.v1. */
export function tokenSubprotocol(token: string): string {
  return TOKEN_PREFIX + base64url(new TextEncoder().encode(token))
}

/**
 * tokenFromSubprotocols returns the token a client offered, as a server
 * reads it from the values of its Sec-WebSocket-Protocol header lines,
 * each a comma-separated list; an empty string if it offered none. It
 * throws invalid if occamy.v1 is not offered, or more than one token,
 * or one that is empty or not base64url.
 */
export function tokenFromSubprotocols(offered: string[]): string {
  let v1 = false
  let tokens = 0
  let encoded = ''
  for (const line of offered) {
    for (const part of line.split(',')) {
      const p = part.replace(/^[ \t]+|[ \t]+$/g, '')
      if (p === SUBPROTOCOL) {
        v1 = true
      } else if (p.startsWith(TOKEN_PREFIX)) {
        tokens++
        encoded = p.slice(TOKEN_PREFIX.length)
      }
    }
  }
  if (!v1) {
    throw new ProtocolError('invalid', `the client did not offer ${SUBPROTOCOL}`)
  }
  if (tokens > 1) {
    throw new ProtocolError('invalid', `the client offered ${tokens} tokens`)
  }
  if (tokens === 0) {
    return ''
  }
  if (encoded === '') {
    throw new ProtocolError('invalid', 'the client offered an empty token')
  }
  let raw: Uint8Array
  try {
    raw = fromBase64url(encoded)
  } catch {
    throw new ProtocolError('invalid', 'the offered token is not base64url')
  }
  return new TextDecoder().decode(raw)
}
