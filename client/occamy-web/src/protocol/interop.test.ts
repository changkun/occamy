// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The client against occamyd's stream code: connect() over Node's
// WebSocket to client/internal/webinterop, which serves internal/stream
// and internal/session with a scripted desktop and an echoing terminal.
// Node decodes deflate and raw, so every tile is lossless and the
// framebuffer must equal the desktop's screen byte for byte.
//
// It runs when OCCAMY_INTEROP_SERVER names the built server, as CI's web
// job sets it:
//
//	go build -o /tmp/webinterop ./client/internal/webinterop
//	OCCAMY_INTEROP_SERVER=/tmp/webinterop npm test

import { type ChildProcess, spawn } from 'node:child_process'
import { createInterface } from 'node:readline'

import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { connect } from './connect'
import { CloseError } from './errors'
import type { SessionEvent } from './events'
import { Codec, Role, SessionClass } from './messages'
import { DEFAULT_CACHE_SLOTS, type Session } from './session'

const server = process.env.OCCAMY_INTEROP_SERVER ?? ''

interface ImageJSON {
  width: number
  height: number
  hotX?: number
  hotY?: number
  pixels: string
}

interface InputJSON {
  type: string
  [k: string]: unknown
}

// The colour the desktop paints its swatch for a key press of keysym,
// backendtest.SwatchColor.
function swatch(keysym: number): number[] {
  return [keysym & 0xff, (keysym >> 8) & 0xff, 0x40 | ((keysym >> 16) & 0xff), 0xff]
}

// CI builds the server; a CI run without it would skip the test unseen.
it.runIf(server === '' && process.env.CI === 'true')('has the interop server in CI', () => {
  throw new Error('OCCAMY_INTEROP_SERVER is not set')
})

describe.skipIf(server === '')('interop with occamyd’s stream', { timeout: 30_000 }, () => {
  let proc: ChildProcess
  let base = ''

  beforeAll(async () => {
    proc = spawn(server, [], { stdio: ['pipe', 'pipe', 'inherit'] })
    const lines = createInterface({ input: proc.stdout as NodeJS.ReadableStream })
    base = await new Promise<string>((resolve, reject) => {
      lines.once('line', resolve)
      proc.once('error', reject)
      proc.once('exit', (code) => reject(new Error(`the interop server exited (${code})`)))
    })
    lines.close()
  })

  afterAll(() => {
    proc.stdin?.end()
    proc.kill()
  })

  const ws = (route: string): string => base.replace(/^http/, 'ws') + route
  const get = async <T>(route: string): Promise<T> => {
    const res = await fetch(base + route)
    expect(res.ok).toBe(true)
    return (await res.json()) as T
  }
  const bytes = (b64: string): number[] => Array.from(Buffer.from(b64, 'base64'))

  // until takes s's events until f returns something for one, failing on
  // the end, and on a timeout.
  async function until<T>(s: Session, f: (ev: SessionEvent) => T | undefined, ms = 5000): Promise<T> {
    const ac = new AbortController()
    const timer = setTimeout(() => ac.abort(new Error(`no such event within ${ms} ms`)), ms)
    try {
      for (;;) {
        const ev = await s.next(ac.signal)
        const v = f(ev)
        if (v !== undefined) {
          return v
        }
        if (ev.type === 'end') {
          throw new Error(`the session ended: ${JSON.stringify(ev)}`)
        }
      }
    } finally {
      clearTimeout(timer)
    }
  }

  // shows waits until s's framebuffer is the desktop's screen.
  async function shows(s: Session): Promise<void> {
    const screen = await get<ImageJSON>('/screen')
    const want = bytes(screen.pixels)
    const same = (): boolean => s.framebuffer.width === screen.width && s.framebuffer.height === screen.height && Array.from(s.framebuffer.data).every((v, i) => v === want[i])
    if (same()) {
      return
    }
    await until(s, (ev) => (ev.type === 'frame' && same() ? true : undefined))
  }

  // waitFor polls f until it holds.
  async function waitFor(f: () => Promise<boolean>, ms = 5000): Promise<void> {
    const deadline = Date.now() + ms
    while (!(await f())) {
      if (Date.now() > deadline) {
        throw new Error('timed out')
      }
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
  }

  it('runs a graphical session', async () => {
    const s = await connect(ws('/graphical'), 'interop', { width: 320, height: 200, name: 'interop-test' })
    try {
      expect(s.sessionClass).toBe(SessionClass.Graphical)
      expect(s.role).toBe(Role.Owner)
      expect(s.sessionId).toBe('interop')
      // Node decodes deflate and raw; occamyd's encoder sends more. The
      // tile cache is agreed too.
      expect(s.capabilities.codecs).toEqual([Codec.Deflate, Codec.Raw, Codec.Cached])
      expect(s.capabilities.cacheSlots).toBe(DEFAULT_CACHE_SLOTS)

      // The first frames show the screen, the pointer and the title. A
      // viewer is first sent the session's state as it is, which may be
      // from before the desktop set them: no pointer and no title.
      const seen: SessionEvent[] = []
      const pointer = (): SessionEvent | undefined => seen.findLast((e) => e.type === 'cursor' && e.image !== null)
      await until(s, (ev) => {
        seen.push(ev)
        const titled = seen.some((e) => e.type === 'title' && e.title === 'interop')
        return ev.type === 'frame' && titled && pointer() !== undefined ? true : undefined
      })
      await shows(s)
      const cursor = pointer()
      const want = await get<ImageJSON>('/cursor')
      expect(cursor).toMatchObject({ hotX: want.hotX, hotY: want.hotY, image: { width: want.width, height: want.height } })
      expect(Array.from(cursor?.type === 'cursor' ? (cursor.image?.pixels ?? []) : [])).toEqual(bytes(want.pixels))
      expect(seen.filter((e) => e.type === 'title').at(-1)).toEqual({ type: 'title', title: 'interop' })

      // A key paints the swatch, in a frame that reports the key.
      const seq = s.key(true, 0x78, 0x1b)
      s.key(false, 0x78, 0x1b)
      await until(s, (ev) => (ev.type === 'frame' && ev.lastSeq >= seq ? true : undefined))
      await shows(s)
      expect(Array.from(s.framebuffer.data.subarray(0, 4))).toEqual(swatch(0x78))

      // Pointer, wheel and text reach the desktop as sent.
      s.pointer(10, 20, 1)
      s.wheel(0, -3, 1)
      s.text('日本')
      await waitFor(async () => (await get<InputJSON[]>('/inputs')).length >= 5)
      expect(await get<InputJSON[]>('/inputs')).toEqual([
        { type: 'key', down: true, keysym: 0x78, code: 0x1b },
        { type: 'key', keysym: 0x78, code: 0x1b },
        { type: 'pointer', x: 10, y: 20, buttons: 1 },
        { type: 'wheel', dy: -3, mode: 1 },
        { type: 'text', text: '日本' },
      ])

      // Another key, then the first again: the swatch's tile comes back
      // from the tile cache, and the screen is still the desktop's.
      for (const keysym of [0x79, 0x78]) {
        const k = s.key(true, keysym, 0x1b)
        s.key(false, keysym, 0x1b)
        await until(s, (ev) => (ev.type === 'frame' && ev.lastSeq >= k ? true : undefined))
        await shows(s)
      }
      expect(await get<number>('/cached')).toBeGreaterThan(0)

      // The clipboard comes back, the bell rings, the title changes.
      s.clipboard('text/plain', 'from the browser')
      expect(await until(s, (ev) => (ev.type === 'clipboard' ? ev : undefined))).toEqual({
        type: 'clipboard',
        mime: 'text/plain',
        data: new TextEncoder().encode('from the browser'),
      })
      s.text('bell')
      await until(s, (ev) => (ev.type === 'bell' ? true : undefined))
      s.text('title another')
      expect(await until(s, (ev) => (ev.type === 'title' ? ev.title : undefined))).toBe('another')

      // A resize request resizes the screen.
      s.resize(160, 96)
      expect(await until(s, (ev) => (ev.type === 'resize' ? ev : undefined))).toEqual({ type: 'resize', width: 160, height: 96 })
      await until(s, (ev) => (ev.type === 'frame' ? true : undefined))
      expect(s.size).toEqual({ width: 160, height: 96 })
      await shows(s)

      // A hidden client gets no frames; shown again, it gets one frame
      // of the screen. The server's log orders what it sent and the
      // demands it took, so this holds without a clock: no frame went
      // out between hiding and showing, and the frame after showing is
      // the one after the last before hiding and carries the key the
      // desktop took while hidden.
      const log = (): Promise<string[]> => get<string[]>('/log')
      const frameIds = (entries: string[]): number[] => entries.filter((e) => e.startsWith('frame ')).map((e) => Number(e.slice(6)))
      const logged = (await log()).length
      s.view(false)
      await waitFor(async () => (await log()).slice(logged).includes('hidden'))
      const hidden = (await log()).lastIndexOf('hidden')
      const lastShown = frameIds((await log()).slice(0, hidden)).at(-1) ?? 0
      const hiddenSeq = s.key(true, 0x79, 0x1c)
      await waitFor(async () => (await get<InputJSON[]>('/inputs')).some((i) => i.keysym === 0x79))
      s.view(true)
      const shown = await until(s, (ev) => (ev.type === 'frame' && ev.id > lastShown ? ev : undefined))
      expect(shown.id).toBe(lastShown + 1)
      expect(shown.lastSeq).toBeGreaterThanOrEqual(hiddenSeq)
      const after = await log()
      const showing = after.indexOf('shown', hidden)
      expect(showing).toBeGreaterThan(hidden)
      expect(frameIds(after.slice(hidden, showing))).toEqual([])
      await shows(s)
      expect(Array.from(s.framebuffer.data.subarray(0, 4))).toEqual(swatch(0x79))

      // The remote ends the session.
      s.text('end')
      const end = await until(s, (ev) => (ev.type === 'end' ? ev : undefined))
      expect(end).toEqual({ type: 'end', code: 4005, reason: expect.any(String) as string })
    } finally {
      await s.close()
    }
  })

  it('refuses a wrong token with unauthorized', async () => {
    const err = await connect(ws('/graphical'), 'wrong', {}).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CloseError)
    expect(err).toMatchObject({ code: 4000, kind: 'auth' })
  })

  it('runs a terminal session, and closes it', async () => {
    const s = await connect(ws('/terminal'), 'interop', { cols: 80, rows: 24 })
    expect(s.sessionClass).toBe(SessionClass.Terminal)
    const seen: SessionEvent['type'][] = []
    await until(s, (ev) => {
      seen.push(ev.type)
      return ev.type === 'frame' ? true : undefined
    })
    expect(seen).toContain('term-snapshot')
    expect(s.size).toEqual({ width: 80, height: 24 })
    s.termInput('echo hi\r')
    let out = ''
    const dec = new TextDecoder()
    await until(s, (ev) => {
      if (ev.type === 'term-data') {
        out += dec.decode(ev.data)
      }
      return out.includes('echo hi') ? true : undefined
    })
    await s.close()
    expect(await s.next()).toEqual({ type: 'end', code: 1000, reason: '' })
  })
})
