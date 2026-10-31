// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// A scripted server for the session tests: a Transport whose messages
// the test sends and reads as protocol messages.

import { LocalDecoder, type TileDecoder, streamInflate } from '../decode'
import {
  type Capabilities,
  Codec,
  type Message,
  Role,
  SessionClass,
  type Welcome,
  decode,
  emptyCapabilities,
  encode,
} from '../messages'
import { type Options, Session, type Transport } from '../session'

const everything = { maxMessageSize: 1 << 30, maxTextureSize: 0xffff, cacheSlots: 0 }

/** FakeTransport is the client's end of a connection to a scripted server. */
export class FakeTransport implements Transport {
  onmessage: ((data: ArrayBuffer | string) => void) | null = null
  onclose: ((code: number, reason: string) => void) | null = null
  /** What the client sent, decoded. */
  readonly sent: Message[] = []
  /** The code and reason the client closed the connection with. */
  closed: { code: number; reason: string } | null = null

  send(msg: Uint8Array): void {
    if (this.closed !== null) {
      throw new Error('send on a closed connection')
    }
    const m = decode(msg, 'client', everything)
    if (m === null) {
      throw new Error('the client sent an ignorable message')
    }
    this.sent.push(m)
  }

  /** close closes the connection as a browser's WebSocket does, refusing what a browser refuses. */
  close(code: number, reason: string): void {
    if (code !== 1000 && !(code >= 3000 && code <= 4999)) {
      throw new DOMException(`close code ${code}`, 'InvalidAccessError')
    }
    if (new TextEncoder().encode(reason).length > 123) {
      throw new DOMException('a close reason longer than 123 bytes', 'SyntaxError')
    }
    if (this.closed !== null) {
      return
    }
    this.closed = { code, reason }
    // The close event fires once the closing handshake is done.
    queueMicrotask(() => this.onclose?.(code, reason))
  }

  /** push sends m to the client. */
  push(m: Message): void {
    this.pushBytes(encode(m))
  }

  /** pushBytes sends a binary message to the client. */
  pushBytes(b: Uint8Array): void {
    this.onmessage?.(b.slice().buffer)
  }

  /** pushText sends a text message to the client. */
  pushText(s: string): void {
    this.onmessage?.(s)
  }

  /** drop closes the connection from the server's end, with code, or 1006 for a failed connection. */
  drop(code = 1006, reason = ''): void {
    this.closed ??= { code, reason }
    this.onclose?.(code, reason)
  }

  /** take returns and forgets what the client sent of the given types. */
  take(...types: Message['type'][]): Message[] {
    const out: Message[] = []
    for (let i = 0; i < this.sent.length;) {
      if (types.includes(this.sent[i].type)) {
        out.push(...this.sent.splice(i, 1))
      } else {
        i++
      }
    }
    return out
  }
}

/** A deflate and raw decoder on this thread, with the platform's DecompressionStream. */
export function testDecoder(): TileDecoder {
  return new LocalDecoder({ inflate: streamInflate(), decodeImage: null })
}

export interface OpenOptions {
  options?: Options
  welcome?: Partial<Omit<Welcome, 'capabilities'>>
  capabilities?: Partial<Capabilities>
  decoder?: TileDecoder
  codecs?: number[]
}

/** open starts a session against a scripted server that welcomes it. */
export async function open(o: OpenOptions = {}): Promise<{ t: FakeTransport; s: Session }> {
  const t = new FakeTransport()
  const p = Session.start(t, o.options ?? {}, o.decoder ?? testDecoder(), o.codecs ?? [Codec.Deflate, Codec.Raw])
  t.push(welcome(o))
  const s = await p
  return { t, s }
}

/** welcome is the welcome of a scripted server. */
export function welcome(o: OpenOptions = {}): Welcome {
  return {
    type: 'welcome',
    viewerId: 7,
    role: Role.Owner,
    class: SessionClass.Graphical,
    sessionId: 'lab',
    capabilities: { ...emptyCapabilities(), codecs: [Codec.Deflate, Codec.Raw], frameWindow: 2, maxMessageSize: 4 << 20, ...o.capabilities },
    ...o.welcome,
  }
}

/** solid returns w×h raw pixels of one colour. */
export function solid(w: number, h: number, rgba: readonly number[]): Uint8Array {
  const p = new Uint8Array(4 * w * h)
  for (let i = 0; i < p.length; i += 4) {
    p.set(rgba, i)
  }
  return p
}

/** tile is a raw tile of frame id. */
export function tile(frameId: number, x: number, y: number, w: number, h: number, rgba: readonly number[]): Message {
  return { type: 'tile', frameId, x, y, w, h, codec: Codec.Raw, slot: 0, payload: solid(w, h, rgba) }
}

/** frameEnd ends frame id. */
export function frameEnd(frameId: number, lastSeq = 0, serverTime = BigInt(frameId) * 1000n): Message {
  return { type: 'frame-end', frameId, serverTime, lastSeq }
}

/** pixel returns the RGBA of x, y of a framebuffer. */
export function pixel(fb: { width: number; data: Uint8ClampedArray }, x: number, y: number): number[] {
  const at = 4 * (y * fb.width + x)
  return Array.from(fb.data.subarray(at, at + 4))
}
