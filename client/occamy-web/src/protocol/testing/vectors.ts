// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The golden vectors of protocol/testdata, which the Go protocol and
// encoder packages generate, and helpers for the tests that run them.

import codecsJSON from '../../../../../protocol/testdata/codecs.json'
import framingJSON from '../../../../../protocol/testdata/framing.json'
import messagesJSON from '../../../../../protocol/testdata/messages.json'

export interface Limits {
  max_message_size: number
  max_texture_size: number
  cache_slots: number
}

export interface MessageVector {
  name: string
  message: string
  type: number
  from: 'client' | 'server' | 'either'
  fields: Record<string, unknown>
  hex: string
}

export interface InvalidMessageVector {
  name: string
  from: 'client' | 'server' | 'either'
  hex: string
  error: string
  limits?: Limits
}

export interface MessagesFile {
  limits: Limits
  messages: MessageVector[]
  invalid: InvalidMessageVector[]
  hello_query: { name: string; query: string; hex?: string; error?: string }[]
  tokens: { token: string; subprotocol: string }[]
  subprotocols: { name: string; offered: string[]; token: string; error?: string }[]
}

export interface FramingFile {
  header: { name: string; type: number; flags: number; ignorable: boolean; hex: string }[]
  integers: { name: string; kind: string; value: string; hex: string }[]
  bytes: { name: string; value: string; hex: string }[]
  strings: { name: string; value: string; hex: string }[]
  invalid: { name: string; kind: string; hex: string; error: string; max_message_size?: number }[]
  dispatch: { name: string; registered: number[]; hex: string; action: string }[]
}

export interface CodecVector {
  name: string
  codec: number
  width: number
  height: number
  pixels: string
  payload: string
  container?: string
  exact: boolean
  min_psnr?: number
  handmade?: boolean
}

export interface CodecsFile {
  vectors: CodecVector[]
  invalid: { name: string; codec: number; width: number; height: number; payload: string }[]
}

export const messages = messagesJSON as unknown as MessagesFile
export const framing = framingJSON as unknown as FramingFile
export const codecs = codecsJSON as unknown as CodecsFile

/** fromHex decodes lowercase hex. */
export function fromHex(s: string): Uint8Array {
  if (s.length % 2 !== 0 || !/^[0-9a-f]*$/.test(s)) {
    throw new Error(`bad hex: ${s.slice(0, 32)}`)
  }
  const p = new Uint8Array(s.length / 2)
  for (let i = 0; i < p.length; i++) {
    p[i] = parseInt(s.slice(2 * i, 2 * i + 2), 16)
  }
  return p
}

/** toHex encodes p as lowercase hex. */
export function toHex(p: Uint8Array): string {
  let s = ''
  for (const b of p) {
    s += b.toString(16).padStart(2, '0')
  }
  return s
}

/**
 * psnr is the vectors' peak signal-to-noise ratio of b against a, in
 * decibels over the red, green and blue of every pixel.
 */
export function psnr(a: Uint8Array | Uint8ClampedArray, b: Uint8Array | Uint8ClampedArray): number {
  if (a.length !== b.length) {
    throw new Error(`psnr of ${a.length} and ${b.length} bytes`)
  }
  let sum = 0
  let n = 0
  for (let i = 0; i < a.length; i += 4) {
    for (let c = 0; c < 3; c++) {
      const d = a[i + c] - b[i + c]
      sum += d * d
      n++
    }
  }
  if (sum === 0) {
    return Infinity
  }
  return 10 * Math.log10((255 * 255) / (sum / n))
}
