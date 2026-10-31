// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// connect over a scripted WebSocket. The interop test runs it over
// Node's WebSocket against occamyd's stream code.

import { describe, expect, it } from 'vitest'

import { type WebSocketConstructor, connect } from './connect'
import { CloseError, ConnectionError } from './errors'
import { decodeHelloQuery, fromBase64url } from './handshake'
import { Codec, decode, encode } from './messages'
import { testDecoder, welcome } from './testing/server'
import { ProtocolError } from './wire'

class FakeWebSocket extends EventTarget {
  static last: FakeWebSocket | null = null
  readonly url: URL
  readonly protocols: string[]
  protocol = ''
  binaryType = 'blob'
  readonly sent: Uint8Array[] = []
  closed: { code?: number; reason?: string } | null = null

  constructor(url: string | URL, protocols?: string | string[]) {
    super()
    this.url = new URL(url)
    this.protocols = typeof protocols === 'string' ? [protocols] : (protocols ?? [])
    FakeWebSocket.last = this
  }

  send(data: Uint8Array): void {
    this.sent.push(data)
  }

  close(code?: number, reason?: string): void {
    if (this.closed !== null) {
      return
    }
    this.closed = { code, reason }
    queueMicrotask(() => this.fire('close', { code: code ?? 1005, reason: reason ?? '', wasClean: true }))
  }

  fire(type: string, props: Record<string, unknown> = {}): void {
    this.dispatchEvent(Object.assign(new Event(type), props))
  }

  open(protocol = 'occamy.v1'): void {
    this.protocol = protocol
    this.fire('open')
  }

  message(data: Uint8Array | string): void {
    this.fire('message', { data: typeof data === 'string' ? data : data.slice().buffer })
  }
}

const WS = FakeWebSocket as unknown as WebSocketConstructor

// socket returns the WebSocket connect made, once it made it.
async function socket(): Promise<FakeWebSocket> {
  for (let i = 0; i < 100 && FakeWebSocket.last === null; i++) {
    await Promise.resolve()
  }
  const ws = FakeWebSocket.last
  if (ws === null) {
    throw new Error('connect made no WebSocket')
  }
  FakeWebSocket.last = null
  return ws
}

describe('connect', () => {
  it('sends hello in the URL and the token beside occamy.v1', async () => {
    const p = connect('wss://occamy.example/api/v1/connections/lab/stream?x=1', 'secret token', {
      decoder: testDecoder(),
      WebSocket: WS,
      width: 1280,
      height: 720,
      pixelRatio: 200,
    })
    const ws = await socket()
    expect(ws.url.origin + ws.url.pathname).toBe('wss://occamy.example/api/v1/connections/lab/stream')
    expect(ws.url.searchParams.get('x')).toBe('1')
    const hello = decodeHelloQuery(ws.url.searchParams.get('hello') ?? '')
    expect(hello).toMatchObject({ width: 1280, height: 720, visible: true, name: 'occamy-web' })
    expect(hello.capabilities.codecs).toEqual([Codec.Deflate, Codec.Raw])
    expect(hello.capabilities.pixelRatio).toBe(200)
    expect(ws.protocols).toHaveLength(2)
    expect(ws.protocols[0]).toBe('occamy.v1')
    expect(new TextDecoder().decode(fromBase64url(ws.protocols[1].slice('occamy.v1.token.'.length)))).toBe('secret token')
    expect(ws.url.toString()).not.toContain('secret')
    // Messages before the session listens wait for it, in order.
    ws.open()
    ws.message(encode(welcome()))
    ws.message(encode({ type: 'title', title: 'lab' }))
    const s = await p
    expect(ws.binaryType).toBe('arraybuffer')
    expect(await s.next()).toEqual({ type: 'title', title: 'lab' })
    s.key(true, 0x61, 4)
    expect(decode(ws.sent[0], 'client', { maxMessageSize: 1 << 20, maxTextureSize: 4096, cacheSlots: 0 })).toMatchObject({ type: 'key', seq: 1 })
  })

  it('offers occamy.v1 alone without a token', async () => {
    const p = connect('ws://localhost/stream', '', { decoder: testDecoder(), WebSocket: WS })
    const ws = await socket()
    expect(ws.protocols).toEqual(['occamy.v1'])
    ws.open()
    ws.message(encode(welcome()))
    await p
  })

  it('refuses a server that picks another subprotocol', async () => {
    const p = connect('ws://localhost/stream', 't', { decoder: testDecoder(), WebSocket: WS })
    const ws = await socket()
    ws.open('occamy.v2')
    const err = await p.catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ProtocolError)
    expect((err as ProtocolError).kind).toBe('unsupported')
    expect(ws.closed?.code).toBe(1000)
  })

  it('rejects with ConnectionError if the WebSocket closes before it opens', async () => {
    const p = connect('ws://localhost/stream', 't', { decoder: testDecoder(), WebSocket: WS })
    const ws = await socket()
    ws.fire('close', { code: 1006, reason: '', wasClean: false })
    await expect(p).rejects.toBeInstanceOf(ConnectionError)
  })

  it('rejects with the server’s code if it closes after the upgrade', async () => {
    const p = connect('ws://localhost/stream', 'expired', { decoder: testDecoder(), WebSocket: WS })
    const ws = await socket()
    ws.open()
    ws.message(encode({ type: 'close', code: 4000, reason: 'unauthorized' }))
    const err = await p.catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CloseError)
    expect(err).toMatchObject({ code: 4000, kind: 'auth' })
    expect(ws.closed?.code).toBe(4000)
  })

  it('reports a WebSocket that closed uncleanly as a failed connection', async () => {
    const p = connect('ws://localhost/stream', '', { decoder: testDecoder(), WebSocket: WS })
    const ws = await socket()
    ws.open()
    ws.message(encode(welcome()))
    const s = await p
    ws.fire('close', { code: 4005, reason: '', wasClean: false })
    const end = await s.next()
    expect(end).toMatchObject({ type: 'end', code: 0 })
    expect((end as { error?: unknown }).error).toBeInstanceOf(ConnectionError)
  })

  it('stops when its signal aborts during the upgrade', async () => {
    const ac = new AbortController()
    const p = connect('ws://localhost/stream', '', { decoder: testDecoder(), WebSocket: WS, signal: ac.signal })
    const ws = await socket()
    ac.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
    expect(ws.closed).not.toBeNull()
  })

  it('refuses options outside the protocol before connecting', async () => {
    await expect(connect('ws://localhost/stream', '', { decoder: testDecoder(), WebSocket: WS, width: -1 })).rejects.toBeInstanceOf(RangeError)
    expect(FakeWebSocket.last).toBeNull()
  })
})
