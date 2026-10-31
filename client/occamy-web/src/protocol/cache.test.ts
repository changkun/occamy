// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The tile cache: a tile that names a slot is stored in it as it is
// read, and a cached tile draws what its slot holds, as occamyd's mirror
// of the slots expects.

import { createHash } from 'node:crypto'

import { describe, expect, it } from 'vitest'

import type { EndEvent, SessionEvent } from './events'
import { Codec, type Message } from './messages'
import { Session } from './session'
import { FakeTransport, frameEnd, open, pixel, solid, testDecoder } from './testing/server'
import { cache, codecs, fromHex } from './testing/vectors'
import { ProtocolError } from './wire'

const RED = [255, 0, 0, 255]
const GREEN = [0, 255, 0, 255]
const BLUE = [0, 0, 255, 255]

// nextFrame returns the events up to the next frame, or the end.
async function nextFrame(s: Session): Promise<SessionEvent[]> {
  const evs: SessionEvent[] = []
  for (;;) {
    const ev = await s.next()
    evs.push(ev)
    if (ev.type === 'frame' || ev.type === 'end') {
      return evs
    }
  }
}

function raw(frameId: number, x: number, y: number, w: number, h: number, rgba: readonly number[], slot: number): Message {
  return { type: 'tile', frameId, x, y, w, h, codec: Codec.Raw, slot, payload: solid(w, h, rgba) }
}

function cached(frameId: number, x: number, y: number, w: number, h: number, slot: number): Message {
  return { type: 'tile', frameId, x, y, w, h, codec: Codec.Cached, slot, payload: new Uint8Array(0) }
}

// The deflate vector of two rows of three colours: decoded, not copied.
const deflate = (() => {
  const v = codecs.vectors.find((c) => c.codec === Codec.Deflate && c.name.includes('two rows of three colours'))
  if (v === undefined) {
    throw new Error('no deflate vector')
  }
  return { w: v.width, h: v.height, payload: fromHex(v.payload), pixels: fromHex(v.pixels) }
})()

function deflated(frameId: number, x: number, y: number, slot: number): Message {
  return { type: 'tile', frameId, x, y, w: deflate.w, h: deflate.h, codec: Codec.Deflate, slot, payload: deflate.payload.slice() }
}

// region returns the pixels of w×h at x, y of the framebuffer.
function region(s: Session, x: number, y: number, w: number, h: number): number[] {
  const out: number[] = []
  for (let j = 0; j < h; j++) {
    for (let i = 0; i < w; i++) {
      out.push(...pixel(s.framebuffer, x + i, y + j))
    }
  }
  return out
}

const withCache = {
  options: { cacheSlots: 4 },
  capabilities: { codecs: [Codec.Deflate, Codec.Raw, Codec.Cached], cacheSlots: 4 },
}

describe('cache.json', () => {
  it('shows the server’s screen after each frame', async () => {
    const t = new FakeTransport()
    const p = Session.start(t, { cacheSlots: 4 }, testDecoder(), [Codec.Deflate, Codec.Raw])
    t.pushBytes(fromHex(cache.welcome))
    const s = await p
    expect(s.capabilities.cacheSlots).toBe(4)
    expect(s.capabilities.codecs).toContain(Codec.Cached)
    for (const f of cache.frames) {
      for (const m of f.messages) {
        t.pushBytes(fromHex(m))
      }
      const evs = await nextFrame(s)
      const end = evs.find((e): e is EndEvent => e.type === 'end')
      expect(end, f.name).toBeUndefined()
      const fb = s.framebuffer
      const sum = createHash('sha256').update(fb.data).digest('hex')
      expect([fb.width, fb.height, sum], f.name).toEqual([f.width, f.height, f.screen])
    }
    await s.close()
  })
})

describe('tile cache', () => {
  it('agrees to the fewer slots', async () => {
    const { s } = await open({ options: { cacheSlots: 16 }, capabilities: { cacheSlots: 64 } })
    expect(s.capabilities.cacheSlots).toBe(16)
    const { s: none } = await open({ options: { cacheSlots: 0 }, capabilities: { codecs: [Codec.Raw, Codec.Cached], cacheSlots: 64 } })
    expect(none.capabilities.cacheSlots).toBe(0)
    expect(none.capabilities.codecs).toEqual([Codec.Raw])
  })

  it('draws a tile again from its slot, in its frame and later', async () => {
    const { t, s } = await open(withCache)
    // Stored, then drawn again in the same frame: a raw tile, and one
    // still being decoded.
    t.push({ type: 'resize', width: 8, height: 8 })
    t.push(raw(1, 0, 0, 4, 4, RED, 1))
    t.push(deflated(1, 4, 0, 2))
    t.push(cached(1, 0, 4, 4, 4, 1))
    t.push(cached(1, 4, 4, deflate.w, deflate.h, 2))
    t.push(frameEnd(1))
    await nextFrame(s)
    const want = Array.from(deflate.pixels)
    expect(region(s, 0, 4, 4, 4)).toEqual(region(s, 0, 0, 4, 4))
    expect(pixel(s.framebuffer, 3, 7)).toEqual(RED)
    expect(region(s, 4, 0, deflate.w, deflate.h)).toEqual(want)
    expect(region(s, 4, 4, deflate.w, deflate.h)).toEqual(want)

    // A frame that a resize drops still stores its tiles, and the slots
    // outlive the resize.
    t.push(raw(2, 0, 0, 4, 4, BLUE, 3))
    t.push({ type: 'resize', width: 8, height: 8 })
    t.push(cached(2, 0, 0, 4, 4, 3))
    t.push(cached(2, 4, 0, 4, 4, 1))
    t.push(cached(2, 0, 4, deflate.w, deflate.h, 2))
    t.push(frameEnd(2))
    await nextFrame(s)
    expect(pixel(s.framebuffer, 0, 0)).toEqual(BLUE)
    expect(pixel(s.framebuffer, 7, 3)).toEqual(RED)
    expect(region(s, 0, 4, deflate.w, deflate.h)).toEqual(want)

    // A slot stored again holds the newer tile.
    t.push(raw(3, 0, 0, 2, 2, GREEN, 1))
    t.push(cached(3, 6, 6, 2, 2, 1))
    t.push(frameEnd(3))
    await nextFrame(s)
    expect(pixel(s.framebuffer, 7, 7)).toEqual(GREEN)
    expect(pixel(s.framebuffer, 6, 6)).toEqual(GREEN)
    await s.close()
  })

  for (const [name, m] of [
    ['an empty slot', cached(1, 0, 0, 4, 4, 2)],
    ['a slot of another size', cached(1, 0, 0, 4, 2, 1)],
    ['a slot of the transposed size', cached(1, 0, 0, 2, 8, 3)],
  ] as const) {
    it(`ends the session at a cached tile of ${name}`, async () => {
      const { t, s } = await open(withCache)
      t.push({ type: 'resize', width: 8, height: 8 })
      t.push(raw(1, 0, 0, 4, 4, RED, 1))
      t.push(raw(1, 0, 0, 8, 2, RED, 3))
      t.push(m)
      const evs = await nextFrame(s)
      const end = evs.at(-1)
      expect(end?.type).toBe('end')
      expect((end as EndEvent).error).toBeInstanceOf(ProtocolError)
      expect(((end as EndEvent).error as ProtocolError).kind).toBe('invalid')
    })
  }
})
