// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell's controller against occamyd's stream code, as the core's
// interop test runs the core: over Node's WebSocket to
// client/internal/webinterop, with stand-ins for the canvas (the core's
// MemorySurface), the page's document and window, and the terminal
// emulator. DOM events are Node's Event with the fields the core's
// bindings read.
//
// It runs when OCCAMY_INTEROP_SERVER names the built server, as CI's web
// job sets it:
//
//	go build -o /tmp/webinterop ./client/internal/webinterop
//	OCCAMY_INTEROP_SERVER=/tmp/webinterop npm test

import { type ChildProcess, spawn } from 'node:child_process'
import { createInterface } from 'node:readline'

import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { MemorySurface, ProtocolError, Role, SessionClass } from '../protocol'
import { type ControllerOptions, SessionController, type SessionState, type TerminalSink } from './controller'

const server = process.env.OCCAMY_INTEROP_SERVER ?? ''

interface ImageJSON {
  width: number
  height: number
  pixels: string
}

interface InputJSON {
  type: string
  [k: string]: unknown
}

// A page's document, as far as visibility goes.
class FakeDocument extends EventTarget {
  visibilityState: DocumentVisibilityState = 'visible'

  set(state: DocumentVisibilityState): void {
    this.visibilityState = state
    this.dispatchEvent(new Event('visibilitychange'))
  }
}

// A canvas, as far as the pointer and key bindings go: a box on the page
// at the screen's size.
class FakeElement extends EventTarget {
  box = { left: 0, top: 0, width: 320, height: 200 }
  focused = 0
  getBoundingClientRect(): { left: number; top: number; width: number; height: number } {
    return this.box
  }
  setPointerCapture(): void {}
  focus(): void {
    this.focused++
  }
}

function event(type: string, fields: Record<string, unknown>): Event {
  return Object.assign(new Event(type, { cancelable: true }), fields)
}

// A terminal emulator that keeps what it was written.
class FakeTerminal implements TerminalSink {
  text = ''
  resets = 0
  sizes: [number, number][] = []
  hold = false
  private readonly decoder = new TextDecoder()

  reset(): void {
    this.resets++
    this.text = ''
  }
  write(data: Uint8Array): Promise<void> {
    this.text += this.decoder.decode(data, { stream: true })
    return this.hold ? new Promise(() => undefined) : Promise.resolve()
  }
  resize(cols: number, rows: number): void {
    this.sizes.push([cols, rows])
  }
}

// CI builds the server; a CI run without it would skip the test unseen.
it.runIf(server === '' && process.env.CI === 'true')('has the interop server in CI', () => {
  throw new Error('OCCAMY_INTEROP_SERVER is not set')
})

describe.skipIf(server === '')('the shell’s controller with occamyd’s stream', { timeout: 30_000 }, () => {
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
  const inputs = (): Promise<InputJSON[]> => get<InputJSON[]>('/inputs')

  function open(route: string, o: Partial<ControllerOptions> = {}): SessionController {
    return new SessionController({ url: ws(route), token: 'interop', shared: false, ...o })
  }

  // until waits for the controller's state to hold f.
  function until(c: SessionController, f: (s: SessionState) => boolean, ms = 5000): Promise<SessionState> {
    return new Promise((resolve, reject) => {
      const check = (): boolean => {
        const s = c.getState()
        if (f(s)) {
          clearTimeout(timer)
          unsubscribe()
          resolve(s)
          return true
        }
        return false
      }
      const timer = setTimeout(() => {
        unsubscribe()
        reject(new Error(`the state did not come within ${ms} ms: ${JSON.stringify(c.getState())}`))
      }, ms)
      const unsubscribe = c.subscribe(check)
      check()
    })
  }

  // waitFor polls f until it holds.
  async function waitFor(f: () => Promise<boolean> | boolean, ms = 5000): Promise<void> {
    const deadline = Date.now() + ms
    while (!(await f())) {
      if (Date.now() > deadline) {
        throw new Error('timed out')
      }
      await new Promise((resolve) => setTimeout(resolve, 20))
    }
  }

  it('runs a graphical session: display, keys, pointer, clipboard, title, bell, resize, visibility and the end', async () => {
    const doc = new FakeDocument()
    const win = new EventTarget()
    const written: string[] = []
    const c = open('/graphical', {
      document: doc as unknown as Document,
      window: win,
      writeClipboard: (text) => {
        written.push(text)
        return Promise.resolve()
      },
    })
    try {
      await c.open({ width: 320, height: 200, cols: 0, rows: 0, pixelRatio: 100 })
      expect(c.getState()).toMatchObject({ phase: 'open', role: Role.Owner, sessionClass: SessionClass.Graphical, sessionId: 'interop' })
      expect(c.abilities).toMatchObject({ input: true, resize: true, prompts: true, manage: true })

      // The canvas shows the desktop, pixel for pixel, with its title
      // and pointer.
      const surface = new MemorySurface()
      const canvas = new FakeElement()
      c.showGraphical(surface, canvas)
      await until(c, (s) => s.shown && s.title === 'interop' && s.cursor?.image != null)
      const shows = async (): Promise<boolean> => {
        const screen = await get<ImageJSON>('/screen')
        const want = Buffer.from(screen.pixels, 'base64')
        return surface.width === screen.width && surface.height === screen.height && Buffer.from(surface.pixels).equals(want)
      }
      await waitFor(shows)

      // Keys and the pointer over the canvas reach the desktop; a press
      // focuses the canvas, which takes the keys.
      canvas.dispatchEvent(event('keydown', { key: 'x', code: 'KeyX', location: 0, isComposing: false }))
      canvas.dispatchEvent(event('keyup', { key: 'x', code: 'KeyX', location: 0, isComposing: false }))
      canvas.dispatchEvent(event('pointerdown', { clientX: 10, clientY: 20, buttons: 1, pointerId: 1 }))
      canvas.dispatchEvent(event('wheel', { clientX: 10, clientY: 20, deltaX: 0, deltaY: 3, deltaMode: 1 }))
      await waitFor(async () => (await inputs()).length >= 4)
      expect(await inputs()).toEqual([
        { type: 'key', down: true, keysym: 0x78, code: 0x1b },
        { type: 'key', keysym: 0x78, code: 0x1b },
        { type: 'pointer', x: 10, y: 20, buttons: 1 },
        { type: 'wheel', dy: 3, mode: 1 },
      ])
      expect(canvas.focused).toBe(1)

      // The clipboard goes to the remote, whose clipboard the page takes
      // it for at once; the remote echoes it back, and the page puts it
      // on the local clipboard and does not send the same text back.
      expect(c.sendClipboard('from the shell')).toEqual({ kind: 'sent' })
      expect(c.getState()).toMatchObject({ clipboard: 'from the shell', draft: 'from the shell' })
      await waitFor(() => written.length > 0)
      expect(written).toEqual(['from the shell'])
      expect(c.getState().clipboard).toBe('from the shell')
      expect(c.syncClipboard('from the shell')).toBe(false)
      // Text over the largest message, 4 MiB by default, is refused, and
      // the remote clipboard stays what it was.
      const big = c.sendClipboard('x'.repeat(4 << 20))
      expect(big.kind === 'failed' && big.error instanceof ProtocolError && big.error.kind).toBe('too-large')
      expect(c.getState().clipboard).toBe('from the shell')

      // Text an input method composed goes as text; the desktop takes
      // some as commands.
      canvas.dispatchEvent(event('compositionend', { data: 'bell' }))
      await until(c, (s) => s.bells === 1)
      canvas.dispatchEvent(event('compositionend', { data: 'title another' }))
      await until(c, (s) => s.title === 'another')

      // The owner resizes the remote; asking again for the same size
      // sends nothing.
      c.requestResize(160, 96)
      await until(c, (s) => s.size.width === 160 && s.size.height === 96)
      await waitFor(shows)
      const resizes = async (): Promise<number> => (await inputs()).filter((i) => i.type === 'resize').length
      c.requestResize(160, 96)
      expect(await resizes()).toBe(1)

      // A hidden page says so, and the desktop is asked for nothing
      // until it shows again.
      const log = (): Promise<string[]> => get<string[]>('/log')
      doc.set('hidden')
      await waitFor(async () => (await log()).includes('hidden'))
      doc.set('visible')
      await waitFor(async () => (await log()).lastIndexOf('shown') > (await log()).lastIndexOf('hidden'))

      // The remote ends the session: the page says why, and takes no
      // more input.
      canvas.dispatchEvent(event('compositionend', { data: 'end' }))
      const ended = await until(c, (s) => s.phase === 'ended')
      expect(ended.ending).toMatchObject({ title: 'Session ended', technical: expect.stringMatching(/^remote-ended \(4005\)/) as string })
      const seen = (await inputs()).length
      canvas.dispatchEvent(event('keydown', { key: 'y', code: 'KeyY', location: 0, isComposing: false }))
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect((await inputs()).length).toBe(seen)
    } finally {
      c.dispose()
    }
  })

  it('runs a terminal session: snapshot, echo, resize and close', async () => {
    const c = open('/terminal', { document: new FakeDocument() as unknown as Document })
    const term = new FakeTerminal()
    try {
      await c.open({ width: 640, height: 480, cols: 80, rows: 24, pixelRatio: 100 })
      expect(c.getState()).toMatchObject({ phase: 'open', sessionClass: SessionClass.Terminal })
      c.showTerminal(term)
      await until(c, (s) => s.shown)
      expect(term.resets).toBe(1)
      expect(term.sizes).toEqual([[80, 24]])

      c.sendTerminalInput('echo hi\r')
      await waitFor(() => term.text.includes('echo hi'))

      c.requestResize(100, 30)
      await until(c, (s) => s.size.width === 100 && s.size.height === 30)
      expect(term.sizes.at(-1)).toEqual([100, 30])

      await c.close()
      expect(c.getState()).toMatchObject({ phase: 'ended', ending: { quiet: true } })
    } finally {
      c.dispose()
    }
  })

  it('acknowledges a terminal’s frames once the page hides, though the emulator has not drawn them', async () => {
    const doc = new FakeDocument()
    const c = open('/terminal', { document: doc as unknown as Document })
    const term = new FakeTerminal()
    term.hold = true
    try {
      await c.open({ width: 0, height: 0, cols: 80, rows: 24, pixelRatio: 0 })
      c.showTerminal(term)
      await waitFor(() => term.resets === 1)
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(c.getState().shown).toBe(false)
      doc.set('hidden')
      await until(c, (s) => s.shown)
    } finally {
      c.dispose()
    }
  })

  it('says a wrong token is refused', async () => {
    const c = new SessionController({ url: ws('/graphical'), token: 'wrong', shared: true })
    await c.open({ width: 320, height: 200, cols: 0, rows: 0, pixelRatio: 0 })
    expect(c.getState()).toMatchObject({ phase: 'ended', ending: { title: 'Link not valid', technical: 'unauthorized (4000): token refused' } })
  })
})
