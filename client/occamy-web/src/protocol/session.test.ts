// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The session against a scripted server: the handshake, frames, events,
// the bounds on what a server can make the client hold, input, and the
// end.

import { describe, expect, it } from 'vitest'

import type { DecodeJob, TileDecoder } from './decode'
import { ClosedError, CloseError, ConnectionError, NotOwnerError, ReadOnlyError } from './errors'
import type { EndEvent, FrameEvent, SessionEvent } from './events'
import { Codec, type Message, MessageType, Role, SessionClass, encode } from './messages'
import { Session, helloFor } from './session'
import { FakeTransport, frameEnd, open, pixel, solid, testDecoder, tile, welcome } from './testing/server'
import { codecs, fromHex } from './testing/vectors'
import { ProtocolError } from './wire'

const RED = [255, 0, 0, 255]
const GREEN = [0, 255, 0, 255]
const BLUE = [0, 0, 255, 255]
const BLACK = [0, 0, 0, 255]

// settle lets microtasks and the decoder's work run.
async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
}

async function nextOf<T extends SessionEvent['type']>(s: Session, type: T): Promise<Extract<SessionEvent, { type: T }>> {
  const ev = await s.next()
  expect(ev.type).toBe(type)
  return ev as Extract<SessionEvent, { type: T }>
}

// endOf takes events until the end and returns it.
async function endOf(s: Session): Promise<EndEvent> {
  for (;;) {
    const ev = await s.next()
    if (ev.type === 'end') {
      return ev
    }
  }
}

// failsWith checks that the session ended over a protocol error of the
// server's, which the client told it with a close message of code.
async function failsWith(t: FakeTransport, s: Session, code: number, pattern?: RegExp): Promise<EndEvent> {
  const end = await endOf(s)
  expect(end.code).toBe(code)
  expect(end.error).toBeInstanceOf(ProtocolError)
  if (pattern !== undefined) {
    expect(end.error?.message).toMatch(pattern)
  }
  const close = t.take('close')
  expect(close).toEqual([{ type: 'close', code, reason: expect.any(String) as string }])
  // A browser closes a WebSocket with 1000 or 3000 to 4999 only.
  expect(t.closed?.code).toBe(1000)
  await expect(s.next()).rejects.toBeInstanceOf(ClosedError)
  return end
}

describe('hello', () => {
  it('offers the codecs once, raw last and never cached', () => {
    const h = helloFor({ width: 800, height: 600 }, [Codec.Raw, Codec.WebP, Codec.Cached, Codec.Deflate, Codec.WebP])
    expect(h.capabilities.codecs).toEqual([Codec.WebP, Codec.Deflate, Codec.Raw])
    expect(h.capabilities.cacheSlots).toBe(0)
    expect(h.capabilities.terminal).toBe(1)
    expect(h).toMatchObject({ width: 800, height: 600, visible: true, name: 'occamy-web' })
  })

  it('refuses options outside the protocol', () => {
    const t = new FakeTransport()
    expect(() => Session.start(t, { width: 70000 }, testDecoder(), [])).toThrow(RangeError)
    expect(() => Session.start(t, { maxMessageSize: 1000 }, testDecoder(), [])).toThrow(RangeError)
    expect(() => Session.start(t, { frameWindow: 256 }, testDecoder(), [])).toThrow(RangeError)
  })
})

describe('handshake', () => {
  it('starts on welcome with what both sides agree to', async () => {
    const { s } = await open({
      options: { maxTextureSize: 2048, maxMessageSize: 1 << 20, frameWindow: 4 },
      capabilities: { codecs: [Codec.WebP, Codec.Deflate, Codec.Raw], maxTextureSize: 4096, frameWindow: 3, maxMessageSize: 8 << 20 },
    })
    expect(s.viewerId).toBe(7)
    expect(s.role).toBe(Role.Owner)
    expect(s.sessionClass).toBe(SessionClass.Graphical)
    expect(s.sessionId).toBe('lab')
    // WebP was not offered; no limit goes above the client's own.
    expect(s.capabilities.codecs).toEqual([Codec.Deflate, Codec.Raw])
    expect(s.capabilities.maxTextureSize).toBe(2048)
    expect(s.capabilities.frameWindow).toBe(3)
    expect(s.capabilities.maxMessageSize).toBe(1 << 20)
  })

  it('takes no frame window above its offer, whatever the server states', async () => {
    const offered = await open({ options: { frameWindow: 2 }, capabilities: { frameWindow: 9 } })
    expect(offered.s.capabilities.frameWindow).toBe(2)
    // An unstated offer is the default, 2.
    const unstated = await open({ capabilities: { frameWindow: 9 } })
    expect(unstated.s.capabilities.frameWindow).toBe(2)
  })

  it('takes the defaults for what neither side states', async () => {
    const { s } = await open({ capabilities: { frameWindow: 0, maxMessageSize: 0 } })
    expect(s.capabilities.frameWindow).toBe(2)
    expect(s.capabilities.maxMessageSize).toBe(4 << 20)
    expect(s.capabilities.maxTextureSize).toBe(4096)
  })

  it('rejects with the code of a server that closes before welcome', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [])
    t.push({ type: 'close', code: 4000, reason: 'token expired' })
    const err = await p.catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CloseError)
    expect(err).toMatchObject({ code: 4000, reason: 'token expired', kind: 'auth' })
    expect(t.closed).toEqual({ code: 4000, reason: 'token expired' })
  })

  it('rejects with the code of a WebSocket the server closed before welcome', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [])
    t.drop(4001, 'forbidden')
    await expect(p).rejects.toMatchObject({ code: 4001, kind: 'auth' })
  })

  it('rejects with ConnectionError if the connection fails before welcome', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [])
    t.drop(1006)
    await expect(p).rejects.toBeInstanceOf(ConnectionError)
  })

  it('closes with protocol-error over a message before welcome', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [])
    t.push({ type: 'bell' })
    await expect(p).rejects.toMatchObject({ kind: 'invalid' })
    expect(t.take('close')).toEqual([{ type: 'close', code: 1002, reason: 'protocol error' }])
    expect(t.closed?.code).toBe(1000)
  })

  it('closes with unsupported over a text message', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [])
    t.pushText('hello')
    await expect(p).rejects.toMatchObject({ kind: 'unsupported' })
    expect(t.take('close')).toEqual([{ type: 'close', code: 1003, reason: 'unsupported' }])
  })

  it('closes with unsupported a graphical session without a codec in common', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [Codec.Deflate])
    t.push(welcome({ capabilities: { codecs: [Codec.WebP] } }))
    await expect(p).rejects.toMatchObject({ kind: 'unsupported' })
    expect(t.take('close')).toEqual([{ type: 'close', code: 1003, reason: 'no codec in common' }])
  })

  it('ignores an ignorable message of a later version before welcome', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, {}, testDecoder(), [Codec.Raw])
    t.pushBytes(Uint8Array.of(0xff, 0x7f, 0x01, 0x00, 1, 2, 3))
    t.push(welcome())
    await expect(p).resolves.toBeInstanceOf(Session)
  })

  it('stops waiting when its signal aborts', async () => {
    const t = new FakeTransport()
    const ac = new AbortController()
    let closed = 0
    const decoder: TileDecoder = { codecs: () => Promise.resolve([]), decode: () => Promise.reject(new Error()), close: () => closed++ }
    const p = Session.start(t, {}, decoder, [], { ownsDecoder: true, signal: ac.signal })
    ac.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
    expect(t.closed?.code).toBe(1000)
    expect(closed).toBe(1)
  })
})

describe('frames', () => {
  it('applies a frame whole and acknowledges it once shown', async () => {
    let clock = 0
    const { t, s } = await open({ options: { now: () => clock } })
    t.push({ type: 'resize', width: 4, height: 3 })
    t.push(tile(1, 0, 0, 2, 3, RED))
    t.push(tile(1, 2, 1, 2, 2, GREEN))
    clock = 10
    t.push(frameEnd(1, 0, 123456n))
    expect(await nextOf(s, 'resize')).toEqual({ type: 'resize', width: 4, height: 3 })
    const f = await nextOf(s, 'frame')
    expect(f).toEqual({ type: 'frame', id: 1, lastSeq: 0, serverTime: 123456, dirty: [{ x: 0, y: 0, width: 4, height: 3 }] })
    expect(s.size).toEqual({ width: 4, height: 3 })
    const fb = s.framebuffer
    expect([fb.width, fb.height]).toEqual([4, 3])
    expect(pixel(fb, 0, 0)).toEqual(RED)
    expect(pixel(fb, 1, 2)).toEqual(RED)
    expect(pixel(fb, 2, 0)).toEqual(BLACK)
    expect(pixel(fb, 3, 2)).toEqual(GREEN)
    expect(t.take('frame-ack')).toEqual([])
    // Asking for the next event says the frame is shown.
    clock = 15.5
    t.push({ type: 'bell' })
    await nextOf(s, 'bell')
    expect(t.take('frame-ack')).toEqual([{ type: 'frame-ack', frameId: 1, decodeTime: 5500 }])
  })

  it('lists the rectangles a frame changed, in order', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 8, height: 8 })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    t.push(tile(2, 4, 4, 2, 2, BLUE))
    t.push({ type: 'copy-rect', frameId: 2, srcX: 4, srcY: 4, dstX: 0, dstY: 0, w: 2, h: 2 })
    t.push(frameEnd(2, 9))
    const f = await nextOf(s, 'frame')
    expect(f.lastSeq).toBe(9)
    expect(f.dirty).toEqual([
      { x: 4, y: 4, width: 2, height: 2 },
      { x: 0, y: 0, width: 2, height: 2 },
    ])
    expect(pixel(s.framebuffer, 1, 1)).toEqual(BLUE)
    expect(t.take('frame-ack')).toEqual([{ type: 'frame-ack', frameId: 1, decodeTime: expect.any(Number) as number }])
  })

  it('copies overlapping rectangles as a scroll does', async () => {
    const { t, s } = await open()
    // Rows 0 to 3 of colours 10, 20, 30, 40.
    t.push({ type: 'resize', width: 3, height: 4 })
    for (let y = 0; y < 4; y++) {
      t.push(tile(1, 0, y, 3, 1, [10 * (y + 1), 0, 0, 255]))
    }
    t.push(frameEnd(1))
    // Scroll up by one row, then the right two columns left by one.
    t.push({ type: 'copy-rect', frameId: 2, srcX: 0, srcY: 1, dstX: 0, dstY: 0, w: 3, h: 3 })
    t.push({ type: 'copy-rect', frameId: 2, srcX: 1, srcY: 0, dstX: 0, dstY: 0, w: 2, h: 1 })
    t.push(frameEnd(2))
    // Scroll down by two rows.
    t.push({ type: 'copy-rect', frameId: 3, srcX: 0, srcY: 0, dstX: 0, dstY: 2, w: 3, h: 2 })
    t.push(frameEnd(3))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    await nextOf(s, 'frame')
    const rows = (): number[] => [0, 1, 2, 3].map((y) => pixel(s.framebuffer, 2, y)[0])
    expect(rows()).toEqual([20, 30, 40, 40])
    await nextOf(s, 'frame')
    expect(rows()).toEqual([20, 30, 20, 30])
  })

  it('starts the screen afresh at a resize within a frame', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push(tile(1, 0, 0, 4, 4, RED))
    t.push({ type: 'resize', width: 2, height: 2 })
    t.push(tile(1, 1, 1, 1, 1, GREEN))
    t.push(frameEnd(1))
    expect(await nextOf(s, 'resize')).toMatchObject({ width: 2, height: 2 })
    const f = await nextOf(s, 'frame')
    expect(f.dirty).toEqual([{ x: 0, y: 0, width: 2, height: 2 }])
    expect(Array.from(s.framebuffer.data)).toEqual([...BLACK, ...BLACK, ...BLACK, ...GREEN])
  })

  it('decodes deflate tiles', async () => {
    const v = codecs.vectors.find((x) => x.name === 'deflate: two rows of three colours')
    if (v === undefined) {
      throw new Error('no vector')
    }
    const { t, s } = await open()
    t.push({ type: 'resize', width: v.width, height: v.height })
    t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: v.width, h: v.height, codec: Codec.Deflate, slot: 0, payload: fromHex(v.payload) })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    expect(Array.from(s.framebuffer.data)).toEqual(Array.from(fromHex(v.pixels)))
  })

  it('delivers frames and events in the order they came', async () => {
    const { t, s } = await open()
    t.push({ type: 'title', title: 'one' })
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push({ type: 'bell' })
    t.push(frameEnd(1))
    t.push({ type: 'cursor-move', x: 1, y: 2 })
    expect((await s.next()).type).toBe('title')
    expect((await s.next()).type).toBe('bell')
    expect((await s.next()).type).toBe('resize')
    expect((await s.next()).type).toBe('frame')
    expect((await s.next()).type).toBe('cursor-move')
  })

  it('numbers frames from 1 and wraps from 2³²−1 to 0', async () => {
    const { t, s } = await open({ welcome: { class: SessionClass.Terminal }, capabilities: { terminal: 1 } })
    t.push(frameEnd(1))
    await nextOf(s, 'frame')
    // Skip to the end of the numbers, as four billion frames would.
    ;(s as unknown as { lastEnd: number }).lastEnd = 0xfffffffe
    for (const id of [0xffffffff, 0, 1]) {
      t.push(frameEnd(id))
      expect((await nextOf(s, 'frame')).id).toBe(id)
    }
  })

  it('takes the tiles of frame 0 after the wrap', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 2, height: 1 })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    ;(s as unknown as { lastEnd: number }).lastEnd = 0xffffffff
    t.push(tile(0, 1, 0, 1, 1, GREEN))
    t.push(frameEnd(0))
    expect((await nextOf(s, 'frame')).id).toBe(0)
    expect(pixel(s.framebuffer, 1, 0)).toEqual(GREEN)
  })

  it('closes with protocol-error a frame-end out of order', async () => {
    const { t, s } = await open()
    t.push(frameEnd(2))
    await failsWith(t, s, 1002, /frame-end 2, want 1/)
  })

  it('closes with protocol-error a tile of another frame', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push(tile(2, 0, 0, 1, 1, RED))
    await failsWith(t, s, 1002, /frame 2 in frame 1/)
  })

  it('closes with protocol-error a tile before the screen has a size', async () => {
    const { t, s } = await open()
    t.push(tile(1, 0, 0, 1, 1, RED))
    await failsWith(t, s, 1002, /outside the screen/)
  })

  it('closes with protocol-error a tile beyond the screen', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push(tile(1, 3, 0, 2, 1, RED))
    await failsWith(t, s, 1002, /outside the screen/)
  })

  it('closes with protocol-error a copy-rect from beyond the screen', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push({ type: 'copy-rect', frameId: 1, srcX: 3, srcY: 3, dstX: 0, dstY: 0, w: 2, h: 2 })
    await failsWith(t, s, 1002, /copy-rect source/)
  })

  it('closes with protocol-error a tile of a codec not agreed', async () => {
    const { t, s } = await open({ capabilities: { codecs: [Codec.Raw] } })
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: 1, h: 1, codec: Codec.Deflate, slot: 0, payload: Uint8Array.of(3, 0) })
    await failsWith(t, s, 1002, /not agreed/)
  })

  it('closes with protocol-error a tile that does not decode', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: 2, h: 2, codec: Codec.Deflate, slot: 0, payload: Uint8Array.of(1, 2, 3) })
    t.push(frameEnd(1))
    await failsWith(t, s, 1002, /inflate/)
  })

  it('closes with unsupported a screen larger than it shows', async () => {
    const { t, s } = await open({ options: { maxPixels: 100 } })
    t.push({ type: 'resize', width: 11, height: 10 })
    await failsWith(t, s, 1003, /more than 100 pixels/)
  })

  it('closes with protocol-error a terminal frame in a graphical session, and the reverse', async () => {
    const g = await open()
    g.t.push({ type: 'term-data', data: Uint8Array.of(0x61) })
    await failsWith(g.t, g.s, 1002, /terminal data/)
    const term = await open({ welcome: { class: SessionClass.Terminal }, capabilities: { terminal: 1 } })
    term.t.push({ type: 'resize', width: 80, height: 24 })
    term.t.push(tile(1, 0, 0, 1, 1, RED))
    await failsWith(term.t, term.s, 1002, /tile in a terminal/)
  })
})

describe('terminal frames', () => {
  it('delivers the frame’s resize and data before the frame', async () => {
    const { t, s } = await open({ welcome: { class: SessionClass.Terminal }, capabilities: { terminal: 1 } })
    t.push({ type: 'resize', width: 80, height: 24 })
    t.push({ type: 'term-snapshot', data: Uint8Array.of(1) })
    t.push({ type: 'term-data', data: Uint8Array.of(2) })
    t.push(frameEnd(1, 3))
    expect(await s.next()).toEqual({ type: 'resize', width: 80, height: 24 })
    expect(await s.next()).toEqual({ type: 'term-snapshot', data: Uint8Array.of(1) })
    expect(await s.next()).toEqual({ type: 'term-data', data: Uint8Array.of(2) })
    expect(await s.next()).toMatchObject({ type: 'frame', id: 1, lastSeq: 3, dirty: [] })
    expect(s.size).toEqual({ width: 80, height: 24 })
  })
})

describe('bounds', () => {
  it('allows the frame window and one more frame undelivered, and no more', async () => {
    const { t, s } = await open({ capabilities: { frameWindow: 2 } })
    t.push({ type: 'resize', width: 1, height: 1 })
    for (let id = 1; id <= 3; id++) {
      t.push(frameEnd(id))
    }
    expect(s.ended).toBe(false)
    t.push(frameEnd(4))
    expect(s.ended).toBe(true)
    // What came before the error is delivered first.
    await nextOf(s, 'resize')
    for (let id = 1; id <= 3; id++) {
      expect((await nextOf(s, 'frame')).id).toBe(id)
    }
    const end = await nextOf(s, 'end')
    expect(end.error?.message).toMatch(/more than 3 frames unacknowledged/)
  })

  it('counts the frame delivered and not yet acknowledged against the window', async () => {
    const { t, s } = await open({ capabilities: { frameWindow: 2 } })
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    // Frame 1 is shown, and acknowledged at the next call: with frames 2
    // and 3, the server holds three unacknowledged, the window and one.
    t.push(frameEnd(2))
    t.push(frameEnd(3))
    expect(s.ended).toBe(false)
    t.push(frameEnd(4))
    expect(s.ended).toBe(true)
    await nextOf(s, 'frame')
    await nextOf(s, 'frame')
    expect((await endOf(s)).error?.message).toMatch(/more than 3 frames unacknowledged/)
  })

  it('stops counting a frame once it acknowledged it', async () => {
    const { t, s } = await open({ capabilities: { frameWindow: 2 } })
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    t.push(frameEnd(2))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    // Acknowledges frame 1 and shows frame 2; 2, 3 and 4 are the window
    // and one.
    await nextOf(s, 'frame')
    t.push(frameEnd(3))
    t.push(frameEnd(4))
    expect(s.ended).toBe(false)
    t.push(frameEnd(5))
    expect(s.ended).toBe(true)
  })

  it('counts what each op holds, not its message only', async () => {
    // A floor of 64 KiB: 1×1 tiles of 27 bytes each hold 539 with their
    // op, so 100 fit, and 130 do not, where their messages alone would.
    const { t, s } = await open({ options: { minBacklog: 65536 } })
    t.push({ type: 'resize', width: 4, height: 4 })
    for (let i = 0; i < 100; i++) {
      t.push(tile(1, i % 4, (i >> 2) % 4, 1, 1, RED))
    }
    expect(s.ended).toBe(false)
    for (let i = 0; i < 30; i++) {
      t.push(tile(1, i % 4, (i >> 2) % 4, 1, 1, RED))
    }
    expect(s.ended).toBe(true)
    expect((await endOf(s)).error?.message).toMatch(/undelivered, more than 65536/)
  })

  it('frees what a frame holds once it is delivered', async () => {
    // A 64×64 screen holds 192 KiB; each frame holds about 33 KiB, so a
    // dozen frames would not fit if delivering one did not free it.
    const { t, s } = await open({ options: { minBacklog: 4096 } })
    t.push({ type: 'resize', width: 64, height: 64 })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    for (let id = 2; id < 30; id++) {
      t.push(tile(id, 0, 0, 64, 64, RED))
      t.push(tile(id, 0, 0, 64, 64, GREEN))
      t.push(frameEnd(id))
      expect((await nextOf(s, 'frame')).id).toBe(id)
    }
    expect(s.ended).toBe(false)
    expect((s as unknown as { backlog: number }).backlog).toBe(0)
  })

  it('drops what a frame drew before a resize from what it holds', async () => {
    // Forty full-screen tiles, each after a resize: each resize drops the
    // tile before it, so the frame writes two screens, not eighty, and
    // holds one tile.
    const { t, s } = await open({ options: { minBacklog: 4096 } })
    for (let i = 0; i < 40; i++) {
      t.push({ type: 'resize', width: 64, height: 64 })
      t.push(tile(1, 0, 0, 64, 64, i % 2 === 0 ? RED : GREEN))
    }
    expect(s.ended).toBe(false)
    expect((s as unknown as { open: { ops: unknown[] } }).open.ops.length).toBe(2)
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    expect(pixel(s.framebuffer, 0, 0)).toEqual(GREEN)
    expect((s as unknown as { backlog: number }).backlog).toBe(0)
  })

  it('counts the pixels of a tile being decoded from the start', async () => {
    // Tiles whose decoding never ends hold their 16 KiB of pixels each,
    // whatever their payload: a 64×64 screen holds 192 KiB, which eleven
    // fit in and twelve do not. Four a frame keep each frame's work
    // within its bound.
    const decoder: TileDecoder = {
      codecs: () => Promise.resolve([Codec.Deflate, Codec.Raw]),
      decode: () => new Promise(() => undefined),
      close: () => undefined,
    }
    const { t, s } = await open({ decoder, options: { minBacklog: 4096 } })
    const big = (id: number): Message => ({ type: 'tile', frameId: id, x: 0, y: 0, w: 64, h: 64, codec: Codec.Deflate, slot: 0, payload: Uint8Array.of(1) })
    t.push({ type: 'resize', width: 64, height: 64 })
    for (let i = 0; i < 11; i++) {
      const id = 1 + Math.floor(i / 4)
      t.push(big(id))
      if (i % 4 === 3) {
        t.push(frameEnd(id))
      }
    }
    expect(s.ended).toBe(false)
    t.push(big(3))
    expect(s.ended).toBe(true)
    expect(t.take('close')).toEqual([{ type: 'close', code: 1002, reason: 'protocol error' }])
  })

  it('bounds what it holds undelivered', async () => {
    // A 4×4 screen and a floor of 4 KiB: (2+2) × 3 × 4 × 16 is below it.
    const { t, s } = await open({ options: { minBacklog: 4096 } })
    // The resize holds its 8 bytes and an op's 512, each join its 1213
    // bytes: two joins fit, and a third does not.
    t.push({ type: 'resize', width: 4, height: 4 })
    const name = 'x'.repeat(1200)
    for (let i = 1; i <= 2; i++) {
      t.push({ type: 'viewer-joined', viewerId: i, role: Role.Viewer, name })
    }
    expect(s.ended).toBe(false)
    t.push({ type: 'viewer-joined', viewerId: 3, role: Role.Viewer, name })
    expect(s.ended).toBe(true)
    await failsWith(t, s, 1002, /bytes of frames and events undelivered, more than 4096/)
  })

  it('follows the screen above the floor', async () => {
    // A 64×64 screen holds (2+2) × 3 × 4 × 4096 bytes, 192 KiB.
    const { t, s } = await open({ options: { minBacklog: 4096 } })
    t.push({ type: 'resize', width: 64, height: 64 })
    t.push(tile(1, 0, 0, 64, 64, RED))
    t.push(tile(1, 0, 0, 64, 64, RED))
    t.push(frameEnd(1))
    expect(s.ended).toBe(false)
    // The frame holds 8 + 2 × 16407 bytes of messages and 3 × 512 of
    // ops, each join 10013: 16 fit in 196608 bytes, and the 17th does not.
    for (let i = 1; i <= 16; i++) {
      t.push({ type: 'viewer-joined', viewerId: i, role: Role.Viewer, name: 'x'.repeat(10000) })
    }
    expect(s.ended).toBe(false)
    t.push({ type: 'viewer-joined', viewerId: 17, role: Role.Viewer, name: 'x'.repeat(10000) })
    expect(s.ended).toBe(true)
    expect((await endOf(s)).error?.message).toMatch(/more than 196608/)
  })

  it('bounds the work of a frame', async () => {
    // 32×32 pixels, above the floor: a frame may write eight screens of
    // pixels, 8 × 4 × 1024 bytes, and the backlog holds far more.
    const { t, s } = await open({ options: { minBacklog: 4096 } })
    t.push({ type: 'resize', width: 32, height: 32 })
    t.push(frameEnd(1))
    for (let i = 0; i < 8; i++) {
      t.push({ type: 'copy-rect', frameId: 2, srcX: 0, srcY: 0, dstX: 0, dstY: 0, w: 32, h: 32 })
    }
    expect(s.ended).toBe(false)
    t.push({ type: 'copy-rect', frameId: 2, srcX: 0, srcY: 0, dstX: 0, dstY: 0, w: 32, h: 32 })
    await failsWith(t, s, 1002, /writes 36864 bytes of pixels, more than 32768/)
  })

  it('bounds the events undelivered', async () => {
    const { t, s } = await open()
    for (let i = 1; i <= 1024; i++) {
      t.push({ type: 'viewer-joined', viewerId: i, role: Role.Viewer, name: '' })
    }
    expect(s.ended).toBe(false)
    t.push({ type: 'viewer-joined', viewerId: 1025, role: Role.Viewer, name: '' })
    await failsWith(t, s, 1002, /more than 1024 events/)
  })

  it('checks a cursor’s pixels against the backlog before decoding it', async () => {
    let decodes = 0
    const decoder: TileDecoder = {
      codecs: () => Promise.resolve([Codec.Deflate, Codec.Raw]),
      decode: (job: DecodeJob) => {
        decodes++
        return Promise.resolve(new Uint8ClampedArray(4 * job.width * job.height))
      },
      close: () => undefined,
    }
    const { t, s } = await open({ decoder, options: { minBacklog: 4096 } })
    t.push({ type: 'resize', width: 4, height: 4 })
    t.push({ type: 'cursor', hotX: 0, hotY: 0, w: 32, h: 32, codec: Codec.Deflate, payload: Uint8Array.of(1) })
    expect(s.ended).toBe(true)
    expect(decodes).toBe(0)
    await failsWith(t, s, 1002, /a cursor of 4096 bytes/)
  })

  it('credits the undelivered cursor a cursor replaces', async () => {
    const decoder: TileDecoder = {
      codecs: () => Promise.resolve([Codec.Deflate, Codec.Raw]),
      decode: (job: DecodeJob) => Promise.resolve(new Uint8ClampedArray(4 * job.width * job.height)),
      close: () => undefined,
    }
    // A floor of 4 KiB: the resize holds 520 bytes and a 32×16 cursor
    // 2 KiB, so a second fits only in place of the first.
    const { t, s } = await open({ decoder, options: { minBacklog: 4096 } })
    t.push({ type: 'resize', width: 4, height: 4 })
    for (let i = 0; i < 2; i++) {
      t.push({ type: 'cursor', hotX: i, hotY: 0, w: 32, h: 16, codec: Codec.Deflate, payload: Uint8Array.of(1) })
    }
    expect(s.ended).toBe(false)
    expect(await nextOf(s, 'cursor')).toMatchObject({ hotX: 1 })
  })

  it('closes with too-large a message beyond max_message_size', async () => {
    const { t, s } = await open({ options: { maxMessageSize: 65536 } })
    t.push({ type: 'title', title: 'x'.repeat(65536) })
    await failsWith(t, s, 1009)
  })
})

describe('events', () => {
  it('keeps the latest title, clipboard, cursor and pointer position', async () => {
    const { t, s } = await open()
    t.push({ type: 'title', title: 'one' })
    t.push({ type: 'server-clipboard', mime: 'text/plain', data: Uint8Array.of(1) })
    t.push({ type: 'cursor-move', x: 1, y: 1 })
    t.push({ type: 'cursor', hotX: 0, hotY: 0, w: 1, h: 1, codec: Codec.Raw, payload: Uint8Array.of(1, 2, 3, 4) })
    t.push({ type: 'title', title: 'two' })
    t.push({ type: 'server-clipboard', mime: 'text/plain', data: Uint8Array.of(2) })
    t.push({ type: 'cursor-move', x: 2, y: 2 })
    t.push({ type: 'cursor', hotX: 1, hotY: 0, w: 2, h: 1, codec: Codec.Raw, payload: Uint8Array.of(5, 6, 7, 8, 9, 10, 11, 12) })
    t.push({ type: 'bell' })
    expect(await s.next()).toEqual({ type: 'title', title: 'two' })
    expect(await s.next()).toEqual({ type: 'clipboard', mime: 'text/plain', data: Uint8Array.of(2) })
    expect(await s.next()).toEqual({ type: 'cursor-move', x: 2, y: 2 })
    expect(await s.next()).toEqual({
      type: 'cursor',
      image: { width: 2, height: 1, pixels: Uint8ClampedArray.of(5, 6, 7, 8, 9, 10, 11, 12) },
      hotX: 1,
      hotY: 0,
    })
    expect(await s.next()).toEqual({ type: 'bell' })
  })

  it('decodes cursors, and hides the pointer for an empty one', async () => {
    const v = codecs.vectors.find((x) => x.name === 'deflate: a cursor')
    if (v === undefined) {
      throw new Error('no vector')
    }
    const { t, s } = await open()
    t.push({ type: 'cursor', hotX: 2, hotY: 3, w: v.width, h: v.height, codec: Codec.Deflate, payload: fromHex(v.payload) })
    const c = await nextOf(s, 'cursor')
    expect(c.hotX).toBe(2)
    expect(Array.from(c.image?.pixels ?? [])).toEqual(Array.from(fromHex(v.pixels)))
    t.push({ type: 'cursor', hotX: 0, hotY: 0, w: 0, h: 0, codec: 0, payload: new Uint8Array(0) })
    expect(await s.next()).toEqual({ type: 'cursor', image: null, hotX: 0, hotY: 0 })
  })

  it('ends the session over a cursor that does not decode', async () => {
    const { t, s } = await open()
    t.push({ type: 'cursor', hotX: 0, hotY: 0, w: 2, h: 2, codec: Codec.Deflate, payload: Uint8Array.of(9, 9) })
    t.push({ type: 'bell' })
    await failsWith(t, s, 1002)
  })

  it('rings three bells at most until delivered', async () => {
    const { t, s } = await open()
    for (let i = 0; i < 5; i++) {
      t.push({ type: 'bell' })
    }
    t.push({ type: 'title', title: 't' })
    for (let i = 0; i < 3; i++) {
      await nextOf(s, 'bell')
    }
    await nextOf(s, 'title')
  })

  it('forgets a viewer that left before its join was delivered', async () => {
    const { t, s } = await open()
    t.push({ type: 'viewer-joined', viewerId: 3, role: Role.ReadOnly, name: 'ann' })
    t.push({ type: 'viewer-joined', viewerId: 4, role: Role.Viewer, name: 'bob' })
    t.push({ type: 'viewer-left', viewerId: 3 })
    t.push({ type: 'viewer-left', viewerId: 5 })
    expect(await s.next()).toEqual({ type: 'viewer-joined', viewerId: 4, role: Role.Viewer, name: 'bob' })
    expect(await s.next()).toEqual({ type: 'viewer-left', viewerId: 5 })
  })

  it('answers ping with pong', async () => {
    const { t } = await open()
    t.push({ type: 'ping', nonce: 42, time: 123456789n })
    expect(t.take('pong')).toEqual([{ type: 'pong', nonce: 42, time: 123456789n }])
  })

  it('passes prompts on, and only the owner answers them', async () => {
    const { t, s } = await open()
    t.push({ type: 'prompt', id: 9, name: 'login', instruction: '', fields: [{ label: 'Password', secret: true }] })
    expect(await s.next()).toMatchObject({ type: 'prompt', id: 9, fields: [{ label: 'Password', secret: true }] })
    s.promptReply(9, ['hunter2'])
    expect(t.take('prompt-reply')).toEqual([{ type: 'prompt-reply', id: 9, answers: ['hunter2'] }])
    const viewer = await open({ welcome: { role: Role.Viewer } })
    expect(() => viewer.s.promptReply(9, [])).toThrow(NotOwnerError)
  })

  it('closes with unsupported a text message, and a message it does not know', async () => {
    const a = await open()
    a.t.pushText('x')
    await failsWith(a.t, a.s, 1003)
    const b = await open()
    b.t.pushBytes(Uint8Array.of(0xff, 0x7f, 0x00, 0x00))
    await failsWith(b.t, b.s, 1003)
  })

  it('ignores an ignorable message it does not know', async () => {
    const { t, s } = await open()
    t.pushBytes(Uint8Array.of(0xff, 0x7f, 0x01, 0x00, 1, 2))
    t.push({ type: 'bell' })
    await nextOf(s, 'bell')
  })

  it('closes with protocol-error a second welcome, and client messages', async () => {
    const a = await open()
    a.t.push(welcome())
    await failsWith(a.t, a.s, 1002)
    const b = await open()
    b.t.pushBytes(encode({ type: 'key', seq: 1, down: true, keysym: 0x61, code: 4 }))
    await failsWith(b.t, b.s, 1003)
  })
})

describe('the end', () => {
  it('delivers what came before the server closed, then the end', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    t.push({ type: 'close', code: 4005, reason: 'remote ended' })
    t.push({ type: 'bell' }) // after the end: not delivered
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    expect(await s.next()).toEqual({ type: 'end', code: 4005, reason: 'remote ended' })
    await expect(s.next()).rejects.toBeInstanceOf(ClosedError)
    expect(t.closed).toEqual({ code: 4005, reason: 'remote ended' })
    expect(t.take('close')).toEqual([])
  })

  it('reports a connection that failed', async () => {
    const { t, s } = await open()
    t.drop(1006)
    const end = await nextOf(s, 'end')
    expect(end.code).toBe(0)
    expect(end.error).toBeInstanceOf(ConnectionError)
  })

  it('reports a connection closed without a status, or over TLS, as failed', async () => {
    for (const code of [1005, 1015]) {
      const { t, s } = await open()
      t.drop(code)
      const end = await nextOf(s, 'end')
      expect(end.code).toBe(0)
      expect(end.error).toBeInstanceOf(ConnectionError)
    }
  })

  it('echoes a server’s close without a reason too long for a close frame', async () => {
    const { t, s } = await open()
    const reason = 'é'.repeat(62)
    t.push({ type: 'close', code: 4005, reason })
    expect(await s.next()).toEqual({ type: 'end', code: 4005, reason })
    expect(t.closed).toEqual({ code: 4005, reason: '' })
  })

  it('reports the code of a WebSocket the server closed without a close message', async () => {
    const { t, s } = await open()
    t.drop(4004, 'idle')
    expect(await s.next()).toEqual({ type: 'end', code: 4004, reason: 'idle' })
  })

  it('closes at once on close(), dropping what is undelivered', async () => {
    let closes = 0
    const decoder = testDecoder()
    const owned: TileDecoder = { codecs: () => decoder.codecs(), decode: (j) => decoder.decode(j), close: () => closes++ }
    const t = new FakeTransport()
    const p = Session.start(t, {}, owned, [Codec.Raw], { ownsDecoder: true })
    t.push(welcome())
    const s = await p
    t.push({ type: 'bell' })
    s.key(true, 0x61, 4)
    await s.close()
    expect(t.sent.map((m) => m.type)).toEqual(['key', 'close'])
    expect(t.closed).toEqual({ code: 1000, reason: '' })
    expect(await s.next()).toEqual({ type: 'end', code: 1000, reason: '' })
    expect(closes).toBeGreaterThanOrEqual(1)
    expect(() => s.key(true, 0x61, 4)).toThrow(ClosedError)
  })

  it('cancels a wait for an event, leaving the session as it was', async () => {
    const { t, s } = await open()
    const ac = new AbortController()
    const p = s.next(ac.signal)
    ac.abort()
    await expect(p).rejects.toMatchObject({ name: 'AbortError' })
    t.push({ type: 'bell' })
    await nextOf(s, 'bell')
  })

  it('takes one call of next() at a time', async () => {
    const { t, s } = await open()
    const p = s.next()
    await expect(s.next()).rejects.toThrow(/another call/)
    t.push({ type: 'bell' })
    await p
  })
})

describe('input', () => {
  it('numbers input from 1 and reports it in frames', async () => {
    const { t, s } = await open()
    expect(s.key(true, 0x61, 0x04)).toBe(1)
    expect(s.text('日本')).toBe(2)
    expect(s.pointer(640, 360, 1)).toBe(3)
    expect(s.wheel(0, -3, 1)).toBe(4)
    expect(s.resize(1024, 768)).toBe(5)
    expect(s.clipboard('text/plain', 'hi')).toBe(6)
    expect(s.termInput('ls\r')).toBe(7)
    s.view(true)
    expect(t.sent).toEqual<Message[]>([
      { type: 'key', seq: 1, down: true, keysym: 0x61, code: 4 },
      { type: 'text', seq: 2, text: '日本' },
      { type: 'pointer', seq: 3, x: 640, y: 360, buttons: 1 },
      { type: 'wheel', seq: 4, dx: 0, dy: -3, mode: 1 },
      { type: 'resize-request', seq: 5, width: 1024, height: 768 },
      { type: 'client-clipboard', seq: 6, mime: 'text/plain', data: new TextEncoder().encode('hi') },
      { type: 'term-input', seq: 7, data: new TextEncoder().encode('ls\r') },
      { type: 'view', visible: true, x: 0, y: 0, w: 0, h: 0 },
    ])
  })

  it('numbers input past a view and a prompt reply', async () => {
    const { s } = await open()
    expect(s.key(true, 0x61, 4)).toBe(1)
    s.view(false)
    s.promptReply(1, [])
    expect(s.key(false, 0x61, 4)).toBe(2)
  })

  it('wraps the sequence number from 2³²−1 to 1', async () => {
    const { s } = await open()
    ;(s as unknown as { seq: number }).seq = 0xfffffffe
    expect(s.key(true, 0x61, 4)).toBe(0xffffffff)
    expect(s.key(false, 0x61, 4)).toBe(1)
    expect(s.key(true, 0x62, 5)).toBe(2)
  })

  it('refuses values outside the protocol, keeping the number', async () => {
    const { t, s } = await open()
    expect(() => s.pointer(-1, 0, 0)).toThrow(RangeError)
    expect(() => s.pointer(0, 65536, 0)).toThrow(RangeError)
    expect(() => s.pointer(1.5, 0, 0)).toThrow(RangeError)
    expect(() => s.key(true, 0x61, 0x10000)).toThrow(RangeError)
    expect(() => s.wheel(0, 2 ** 31, 0)).toThrow(RangeError)
    expect(() => s.wheel(0, 1, 2)).toThrow(RangeError)
    expect(() => s.resize(70000, 1)).toThrow(RangeError)
    expect(t.sent).toEqual([])
    expect(s.key(true, 0x61, 4)).toBe(1)
  })

  it('refuses input larger than the server takes', async () => {
    const { t, s } = await open({ capabilities: { maxMessageSize: 65536 } })
    expect(() => s.clipboard('text/plain', new Uint8Array(65536))).toThrow(ProtocolError)
    expect(t.sent).toEqual([])
    expect(s.key(true, 0x61, 4)).toBe(1)
  })

  it('refuses the input of a read-only viewer, not its view', async () => {
    const { t, s } = await open({ welcome: { role: Role.ReadOnly } })
    expect(() => s.key(true, 0x61, 4)).toThrow(ReadOnlyError)
    expect(() => s.pointer(0, 0, 0)).toThrow(ReadOnlyError)
    expect(() => s.text('a')).toThrow(ReadOnlyError)
    expect(() => s.termInput('a')).toThrow(ReadOnlyError)
    expect(() => s.wheel(0, 1)).toThrow(ReadOnlyError)
    expect(() => s.resize(10, 10)).toThrow(ReadOnlyError)
    expect(() => s.clipboard('text/plain', 'a')).toThrow(ReadOnlyError)
    s.view(false)
    expect(t.sent).toEqual([{ type: 'view', visible: false, x: 0, y: 0, w: 0, h: 0 }])
  })

  it('clamps a viewport to the protocol’s coordinates', async () => {
    const { t, s } = await open()
    s.view(true, { x: -10.5, y: 20.2, width: 100.6, height: 70000 })
    expect(t.take('view')).toEqual([{ type: 'view', visible: true, x: 0, y: 20, w: 91, h: 65535 - 20 }])
    expect(() => s.view(true, { x: -50, y: 0, width: 20, height: 10 })).toThrow(RangeError)
    s.view(true, { x: 5, y: 5, width: 0, height: 10 })
    expect(t.take('view')).toEqual([{ type: 'view', visible: true, x: 0, y: 0, w: 0, h: 0 }])
    // Beyond the right edge of the coordinates, and rounded outward.
    s.view(true, { x: 65000, y: 0.5, width: 1000, height: 1 })
    expect(t.take('view')).toEqual([{ type: 'view', visible: true, x: 65000, y: 0, w: 535, h: 2 }])
  })

  it('sends hidden in hello, and the view as it changes', () => {
    expect(helloFor({ hidden: true }, []).visible).toBe(false)
  })
})

describe('frame acknowledgement', () => {
  it('acknowledges each frame delivered, when the next event is asked for', async () => {
    let clock = 0
    const { t, s } = await open({ options: { now: () => clock } })
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    clock = 2
    t.push(frameEnd(2))
    clock = 3
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    expect(t.take('frame-ack')).toEqual([])
    clock = 4
    await nextOf(s, 'frame')
    expect(t.take('frame-ack')).toEqual([{ type: 'frame-ack', frameId: 1, decodeTime: 4000 }])
    t.push({ type: 'bell' })
    clock = 4.25
    await s.next()
    expect(t.take('frame-ack')).toEqual([{ type: 'frame-ack', frameId: 2, decodeTime: 2250 }])
  })

  it('acknowledges a frame once', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    t.push({ type: 'bell' })
    t.push({ type: 'title', title: 't' })
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    await nextOf(s, 'bell')
    await nextOf(s, 'title')
    expect(t.take('frame-ack').map((m) => (m as { frameId: number }).frameId)).toEqual([1])
  })

  it('sends nothing after the end', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1))
    await nextOf(s, 'resize')
    await nextOf(s, 'frame')
    t.drop(1006)
    await nextOf(s, 'end')
    expect(t.take('frame-ack')).toEqual([])
  })
})

describe('frame events', () => {
  it('reports the server time in microseconds', async () => {
    const { t, s } = await open()
    t.push({ type: 'resize', width: 1, height: 1 })
    t.push(frameEnd(1, 0, 2n ** 40n))
    await s.next()
    const f: FrameEvent = await nextOf(s, 'frame')
    expect(f.serverTime).toBe(2 ** 40)
  })

  it('has the type numbers of docs/protocol.md', () => {
    expect(MessageType.FrameAck).toBe(0x010a)
    expect(solid(1, 1, RED)).toEqual(Uint8Array.of(255, 0, 0, 255))
  })
})

describe('decoding off the reader', () => {
  it('waits for a frame’s tiles to decode, holding back what follows', async () => {
    const release: (() => void)[] = []
    const decoder: TileDecoder = {
      codecs: () => Promise.resolve([Codec.Deflate]),
      decode: (job) =>
        new Promise((resolve) => {
          release.push(() => resolve(new Uint8ClampedArray(solid(job.width, job.height, BLUE))))
        }),
      close: () => undefined,
    }
    const { t, s } = await open({ decoder })
    t.push({ type: 'resize', width: 2, height: 2 })
    t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: 2, h: 2, codec: Codec.Deflate, slot: 0, payload: Uint8Array.of(0) })
    t.push(frameEnd(1))
    t.push({ type: 'bell' })
    let got: SessionEvent | null = null
    const p = s.next().then((ev) => (got = ev))
    await settle()
    expect(got).toBeNull()
    release[0]()
    await p
    expect(got).toMatchObject({ type: 'resize' })
    await nextOf(s, 'frame')
    expect(pixel(s.framebuffer, 1, 1)).toEqual(BLUE)
    await nextOf(s, 'bell')
  })
})
