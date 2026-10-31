// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The display and the Worker decoder, with what Node has in place of a
// canvas and a Worker: a surface in memory, and a MessageChannel whose
// far end serveDecoder answers as decode.worker.ts does.

import { MessageChannel } from 'node:worker_threads'

import { describe, expect, it } from 'vitest'

import { LocalDecoder, type Port, type TileDecoder, WorkerDecoder, type WorkerPort, serveDecoder, streamInflate } from './decode'
import { Display, MemorySurface, coalesce, nextAnimationFrame } from './display'
import type { Rect, SessionEvent } from './events'
import { Codec, SessionClass } from './messages'
import { frameEnd, open, tile } from './testing/server'
import { codecs, fromHex } from './testing/vectors'
import { ProtocolError } from './wire'

const RED = [255, 0, 0, 255]
const GREEN = [0, 255, 0, 255]

describe('display', () => {
  it('presents each frame whole, after waiting for a paint', async () => {
    const { t, s } = await open()
    const surface = new MemorySurface()
    const order: string[] = []
    const seen: SessionEvent['type'][] = []
    const display = new Display(s, surface, {
      nextPaint: () => {
        order.push(`paint ${surface.presents}`)
        return Promise.resolve()
      },
      onEvent: (ev) => {
        seen.push(ev.type)
        order.push(`${ev.type} ${surface.presents}`)
        if (ev.type === 'frame' && ev.id === 2) {
          t.push({ type: 'close', code: 4005, reason: '' })
        }
      },
    })
    t.push({ type: 'resize', width: 4, height: 2 })
    t.push(tile(1, 0, 0, 4, 2, RED))
    t.push(frameEnd(1))
    t.push(tile(2, 1, 1, 1, 1, GREEN))
    t.push(frameEnd(2))
    const end = await display.run()
    expect(end).toEqual({ type: 'end', code: 4005, reason: '' })
    expect(seen).toEqual(['resize', 'frame', 'frame', 'end'])
    // A frame is presented before its event, never before a paint.
    expect(order).toEqual(['resize 0', 'paint 0', 'frame 1', 'paint 1', 'frame 2', 'end 2'])
    expect(surface.width).toBe(4)
    expect(Array.from(surface.pixels)).toEqual(Array.from(s.framebuffer.data))
    // The first frame was acknowledged when the display came back for the
    // second; the second not, since the session had ended by then.
    expect(t.take('frame-ack').map((m) => (m as { frameId: number }).frameId)).toEqual([1])
  })

  it('leaves a terminal’s frames to the caller', async () => {
    const { t, s } = await open({ welcome: { class: SessionClass.Terminal }, capabilities: { terminal: 1 } })
    const surface = new MemorySurface()
    let drawn = 0
    const display = new Display(s, surface, {
      nextPaint: () => Promise.resolve(),
      onEvent: async (ev) => {
        if (ev.type === 'frame') {
          await Promise.resolve()
          drawn++
        }
      },
    })
    t.push({ type: 'term-data', data: Uint8Array.of(0x61) })
    t.push(frameEnd(1))
    t.drop(4002, 'closed')
    await display.run()
    expect(drawn).toBe(1)
    expect(surface.presents).toBe(0)
  })

  describe('waiting for a paint', () => {
    // A page whose animation frames the test runs, and whose visibility
    // it sets.
    function page(hidden: boolean): { doc: EventTarget & { hidden: boolean }; frames: (() => void)[]; restore: () => void } {
      const g = globalThis as Record<string, unknown>
      const saved = { document: g.document, requestAnimationFrame: g.requestAnimationFrame }
      const doc = Object.assign(new EventTarget(), { hidden })
      const frames: (() => void)[] = []
      g.document = doc
      g.requestAnimationFrame = (cb: () => void) => frames.push(cb)
      return { doc, frames, restore: () => Object.assign(g, saved) }
    }
    const settled = async (p: Promise<void>): Promise<boolean> => {
      let done = false
      void p.then(() => {
        done = true
      })
      await new Promise((resolve) => setTimeout(resolve, 0))
      return done
    }

    it('waits for an animation frame in a page that can be seen', async () => {
      const { frames, restore } = page(false)
      try {
        const p = nextAnimationFrame()
        expect(await settled(p)).toBe(false)
        frames.forEach((f) => f())
        expect(await settled(p)).toBe(true)
      } finally {
        restore()
      }
    })

    it('does not wait in a hidden page, which gets no animation frames', async () => {
      const { restore } = page(true)
      try {
        expect(await settled(nextAnimationFrame())).toBe(true)
      } finally {
        restore()
      }
    })

    it('stops waiting when the page hides', async () => {
      const { doc, restore } = page(false)
      try {
        const p = nextAnimationFrame()
        expect(await settled(p)).toBe(false)
        doc.hidden = true
        doc.dispatchEvent(new Event('visibilitychange'))
        expect(await settled(p)).toBe(true)
      } finally {
        restore()
      }
    })
  })

  it('coalesces many rectangles into their bounding box', () => {
    const few: Rect[] = [{ x: 0, y: 0, width: 1, height: 1 }]
    expect(coalesce(few)).toBe(few)
    const many: Rect[] = Array.from({ length: 65 }, (_, i) => ({ x: i, y: 2 * i, width: 1, height: 3 }))
    expect(coalesce(many)).toEqual([{ x: 0, y: 0, width: 65, height: 131 }])
  })
})

describe('Worker decoder', () => {
  function workers(count: number): { decoder: WorkerDecoder; served: LocalDecoder[] } {
    const served: LocalDecoder[] = []
    const decoder = new WorkerDecoder(() => {
      const { port1, port2 } = new MessageChannel()
      const local = new LocalDecoder({ inflate: streamInflate(), decodeImage: null })
      served.push(local)
      serveDecoder(port1 as unknown as Port, local)
      return Object.assign(port2 as unknown as Port, {
        terminate: () => {
          port1.close()
          port2.close()
        },
      })
    }, count)
    return { decoder, served }
  }

  it('decodes what a LocalDecoder decodes, across Workers', async () => {
    const { decoder, served } = workers(2)
    expect(served).toHaveLength(2)
    try {
      expect(await decoder.codecs()).toEqual([Codec.Deflate, Codec.Raw])
      const vs = codecs.vectors.filter((v) => v.codec === Codec.Deflate)
      const got = await Promise.all(vs.map((v) => decoder.decode({ codec: v.codec, payload: fromHex(v.payload), width: v.width, height: v.height })))
      for (const [i, v] of vs.entries()) {
        expect(Array.from(got[i])).toEqual(Array.from(fromHex(v.pixels)))
      }
    } finally {
      decoder.close()
    }
  })

  it('passes protocol errors back as they are', async () => {
    const { decoder } = workers(1)
    try {
      const err = await decoder.decode({ codec: Codec.Deflate, payload: Uint8Array.of(1, 2, 3), width: 2, height: 2 }).catch((e: unknown) => e)
      expect(err).toBeInstanceOf(ProtocolError)
      expect((err as ProtocolError).kind).toBe('invalid')
    } finally {
      decoder.close()
    }
  })

  it('leaves the caller’s payload whole', async () => {
    const { decoder } = workers(1)
    try {
      const payload = new Uint8Array(4 * 4)
      payload.fill(7)
      await decoder.decode({ codec: Codec.Raw, payload, width: 2, height: 2 })
      expect(payload.length).toBe(16)
      expect(payload[15]).toBe(7)
    } finally {
      decoder.close()
    }
  })

  it('rejects what is pending when closed', async () => {
    const { decoder } = workers(1)
    const p = decoder.decode({ codec: Codec.Raw, payload: new Uint8Array(4), width: 1, height: 1 })
    decoder.close()
    await expect(p).rejects.toThrow(/closed/)
    await expect(decoder.decode({ codec: Codec.Raw, payload: new Uint8Array(4), width: 1, height: 1 })).rejects.toThrow(/closed/)
  })

  // A Worker that takes requests and answers only as the test says.
  function silent(): { port: WorkerPort; posted: { id: number; op: string }[]; answer: (id: number) => void } {
    const posted: { id: number; op: string }[] = []
    const port: WorkerPort = {
      postMessage: (m) => posted.push(m as { id: number; op: string }),
      onmessage: null,
      onmessageerror: null,
    }
    const answer = (id: number): void => {
      port.onmessage?.(new MessageEvent('message', { data: { id, pixels: new Uint8ClampedArray(4) } }))
    }
    return { port, posted, answer }
  }
  const job = { codec: Codec.Raw, payload: new Uint8Array(4), width: 1, height: 1 }

  it('keeps at most inFlight requests in Workers, the rest waiting', async () => {
    const w = silent()
    const decoder = new WorkerDecoder(() => w.port, { inFlight: 3, liveness: 0 })
    try {
      const all = Array.from({ length: 5 }, () => decoder.decode(job))
      expect(w.posted).toHaveLength(3)
      w.answer(w.posted[0].id)
      expect(w.posted).toHaveLength(4)
      for (const p of w.posted.slice(1)) {
        w.answer(p.id)
      }
      w.answer(w.posted[4].id)
      expect(await Promise.all(all)).toHaveLength(5)
    } finally {
      decoder.close()
    }
  })

  it('fails the requests of a Worker that stops answering', async () => {
    const w = silent()
    const decoder = new WorkerDecoder(() => w.port, { liveness: 10 })
    try {
      await expect(decoder.decode(job)).rejects.toThrow(/stopped answering/)
      // It was asked whether it is alive first.
      expect(w.posted.map((m) => m.op)).toEqual(['decode', 'ping'])
      // And gets no more.
      await expect(decoder.decode(job)).rejects.toThrow(/stopped answering/)
      expect(w.posted).toHaveLength(2)
    } finally {
      decoder.close()
    }
  })

  it('fails the requests of a Worker whose message does not deserialize', async () => {
    const w = silent()
    const decoder = new WorkerDecoder(() => w.port, { liveness: 0 })
    try {
      const p = decoder.decode(job)
      w.port.onmessageerror?.(new MessageEvent('messageerror'))
      await expect(p).rejects.toThrow(/does not deserialize/)
    } finally {
      decoder.close()
    }
  })

  it('keeps a Worker that answers whether it is alive while it decodes', async () => {
    const slow: TileDecoder = {
      codecs: () => Promise.resolve([Codec.Raw]),
      decode: (j) => new Promise((resolve) => setTimeout(() => resolve(new Uint8ClampedArray(4 * j.width * j.height)), 100)),
      close: () => undefined,
    }
    const { port1, port2 } = new MessageChannel()
    serveDecoder(port1 as unknown as Port, slow)
    const decoder = new WorkerDecoder(() => port2 as unknown as Port, { liveness: 10 })
    try {
      expect(await decoder.decode(job)).toHaveLength(4)
    } finally {
      decoder.close()
      port1.close()
      port2.close()
    }
  })

  it('ends a session whose Worker stopped answering, rather than wait', async () => {
    const w = silent()
    const decoder = new WorkerDecoder(() => w.port, { liveness: 10 })
    try {
      const { t, s } = await open({ decoder, codecs: [Codec.Deflate, Codec.Raw] })
      t.push({ type: 'resize', width: 1, height: 1 })
      t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: 1, h: 1, codec: Codec.Deflate, slot: 0, payload: Uint8Array.of(1) })
      t.push(frameEnd(1))
      let ev = await s.next()
      while (ev.type !== 'end') {
        ev = await s.next()
      }
      expect(ev.error?.message).toMatch(/stopped answering/)
    } finally {
      decoder.close()
    }
  })

  it('runs a session', async () => {
    const { decoder } = workers(2)
    try {
      const v = codecs.vectors.find((x) => x.name === 'deflate: two colours, 61×7')
      if (v === undefined) {
        throw new Error('no vector')
      }
      const { t, s } = await open({ decoder })
      t.push({ type: 'resize', width: v.width, height: v.height })
      t.push({ type: 'tile', frameId: 1, x: 0, y: 0, w: v.width, h: v.height, codec: Codec.Deflate, slot: 0, payload: fromHex(v.payload) })
      t.push(frameEnd(1))
      await s.next()
      await s.next()
      expect(Array.from(s.framebuffer.data)).toEqual(Array.from(fromHex(v.pixels)))
    } finally {
      decoder.close()
    }
  })
})
