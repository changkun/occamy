// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The controller without a server: how it opens, fails and is
// cancelled. controller.interop.test.ts runs it against occamyd's stream
// code.

import { describe, expect, it } from 'vitest'

import { CloseCode, CloseError, ConnectionError, type ConnectOptions, Role, type Session, SessionClass, type SessionEvent } from '../protocol'
import { type ControllerOptions, SessionController, type SessionState, type TerminalSink, overlayOf } from './controller'
import { describeEnd } from './ending'

const size = { width: 1280, height: 720, cols: 100, rows: 30, pixelRatio: 200 }

function controller(connect: (url: string, token: string, o: ConnectOptions) => Promise<Session>, o: Partial<ControllerOptions> = {}): SessionController {
  return new SessionController({ url: 'ws://occamy.test/api/v1/connections/lab/stream', token: 't', shared: false, connect, ...o })
}

// FakeSession stands in for the core's Session where the controller and
// the core's Display touch it: the events it delivers, and what the
// controller sends.
class FakeSession {
  readonly sessionId = 'sess'
  readonly viewerId = 1
  readonly framebuffer = { width: 0, height: 0, data: new Uint8ClampedArray(0) }
  readonly resizes: [number, number][] = []
  readonly closes: { code: number; reason: string }[] = []
  private readonly events: SessionEvent[] = []
  private wake: (() => void) | null = null

  constructor(
    readonly role: Role,
    readonly sessionClass: SessionClass = SessionClass.Terminal,
  ) {}

  push(ev: SessionEvent): void {
    this.events.push(ev)
    this.wake?.()
  }

  async next(signal?: AbortSignal): Promise<SessionEvent> {
    for (;;) {
      const ev = this.events.shift()
      if (ev !== undefined) {
        return ev
      }
      await new Promise<void>((resolve, reject) => {
        this.wake = resolve
        signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
      })
    }
  }

  resize(width: number, height: number): number {
    this.resizes.push([width, height])
    return 1
  }

  close(_timeout?: number, code: number = CloseCode.Normal, reason = ''): Promise<void> {
    this.closes.push({ code, reason })
    return Promise.resolve()
  }

  view(): void {}

  clipboard(): number {
    return 1
  }
}

// settle lets the controller's Display take what the fake delivered.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
}

const sink: TerminalSink = { reset: () => undefined, write: () => Promise.resolve(), resize: () => undefined }

// opened returns a controller whose stream is s, open and shown on a terminal.
async function opened(s: FakeSession, o: Partial<ControllerOptions> = {}, terminal: TerminalSink = sink): Promise<SessionController> {
  const c = controller(() => Promise.resolve(s as unknown as Session), o)
  await c.open(size)
  c.showTerminal(terminal)
  return c
}

function clipboardEvent(text: string): SessionEvent {
  return { type: 'clipboard', mime: 'text/plain;charset=utf-8', data: new TextEncoder().encode(text) }
}

describe('SessionController', () => {
  it('opens with the hello’s sizes, the token and visibility', async () => {
    const calls: { url: string; token: string; o: ConnectOptions }[] = []
    const c = controller((url, token, o) => {
      calls.push({ url, token, o })
      return Promise.reject(new CloseError(CloseCode.Forbidden, 'no such connection'))
    })
    await c.open(size)
    expect(calls).toHaveLength(1)
    expect(calls[0].url).toBe('ws://occamy.test/api/v1/connections/lab/stream')
    expect(calls[0].token).toBe('t')
    expect(calls[0].o).toMatchObject({ width: 1280, height: 720, cols: 100, rows: 30, pixelRatio: 200, hidden: false })
    expect(calls[0].o.signal).toBeInstanceOf(AbortSignal)
  })

  it('opens hidden in a hidden page', async () => {
    let hidden: boolean | undefined
    const doc = Object.assign(new EventTarget(), { visibilityState: 'hidden' }) as unknown as Document
    const c = controller(
      (_u, _t, o) => {
        hidden = o.hidden
        return Promise.reject(new ConnectionError('refused'))
      },
      { document: doc },
    )
    await c.open(size)
    expect(hidden).toBe(true)
  })

  it('keeps the hello within what occamyd asks a remote for', async () => {
    let o: ConnectOptions = {}
    const c = controller((_u, _t, got) => {
      o = got
      return Promise.reject(new ConnectionError('refused'))
    })
    await c.open({ width: 20000, height: 9000, cols: 5000, rows: 900, pixelRatio: 100 })
    expect(o).toMatchObject({ width: 8192, height: 8192, cols: 1000, rows: 500 })
  })

  it('describes a refusal, by how the stream was joined', async () => {
    const refuse = (): Promise<Session> => Promise.reject(new CloseError(CloseCode.Unauthorized, 'token expired'))
    const signedIn = controller(refuse)
    await signedIn.open(size)
    expect(signedIn.getState()).toMatchObject({ phase: 'ended', ending: { title: 'Signed out', signIn: true } })

    const shared = controller(refuse, { shared: true })
    await shared.open(size)
    expect(shared.getState()).toMatchObject({ phase: 'ended', ending: { title: 'Link not valid', signIn: false } })
  })

  it('tells its subscribers, and stops once they unsubscribe', async () => {
    const c = controller(() => Promise.reject(new ConnectionError('refused')))
    let calls = 0
    const unsubscribe = c.subscribe(() => calls++)
    const before = c.getState()
    await c.open(size)
    expect(calls).toBeGreaterThan(0)
    expect(c.getState()).not.toBe(before)
    unsubscribe()
    const seen = calls
    await c.open(size)
    expect(calls).toBe(seen)
  })

  it('never connects when disposed at once, as React’s development effects do', async () => {
    let connects = 0
    const c = controller(() => {
      connects++
      return new Promise<Session>(() => undefined)
    })
    const first = c.open(size)
    c.dispose()
    await first
    expect(connects).toBe(0)
    expect(c.getState().phase).toBe('connecting')
  })

  it('cancels an open in flight, and says nothing of it', async () => {
    let signal: AbortSignal | undefined
    const c = controller((_u, _t, o) => {
      signal = o.signal
      return new Promise<Session>((_resolve, reject) => o.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))))
    })
    const open = c.open(size)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(signal?.aborted).toBe(false)
    c.dispose()
    await open
    expect(signal?.aborted).toBe(true)
    expect(c.getState()).toMatchObject({ phase: 'connecting', ending: null })
  })

  it('opens again after dispose', async () => {
    let connects = 0
    const c = controller(() => {
      connects++
      return Promise.reject(new ConnectionError('refused'))
    })
    void c.open(size)
    c.dispose()
    await c.open(size)
    expect(connects).toBe(1)
    expect(c.getState().phase).toBe('ended')
  })

  it('takes no input before it is open', () => {
    const c = controller(() => Promise.reject(new ConnectionError('refused')))
    expect(c.sendClipboard('x')).toBe(false)
    expect(c.syncClipboard('x')).toBe(false)
    c.sendTerminalInput('x')
    c.requestResize(100, 100)
    c.answerPrompt(1, ['secret'])
    expect(c.abilities).toMatchObject({ input: false, resize: false, prompts: false })
  })

  it('asks for the screen’s size again when the window returns to it while another is on its way', async () => {
    const s = new FakeSession(Role.Owner)
    const c = await opened(s)
    s.push({ type: 'resize', width: 80, height: 24 })
    await settle()
    expect(c.getState().size).toEqual({ width: 80, height: 24 })

    c.requestResize(80, 24) // the screen's size, nothing asked: nothing to do
    c.requestResize(100, 30)
    c.requestResize(100, 30) // asked already
    c.requestResize(80, 24) // dragged back before the remote applied 100 × 30
    expect(s.resizes).toEqual([
      [100, 30],
      [80, 24],
    ])

    s.push({ type: 'resize', width: 100, height: 30 })
    s.push({ type: 'resize', width: 80, height: 24 })
    await settle()
    c.requestResize(80, 24) // settled at the size asked for
    expect(s.resizes).toHaveLength(2)
  })

  it('puts the remote clipboard on the local one only for a role that gives input', async () => {
    for (const [role, text, writes] of [
      [Role.Owner, 'owner', ['owner']],
      [Role.Viewer, 'viewer', ['viewer']],
      [Role.ReadOnly, 'read-only', []],
    ] as const) {
      const written: string[] = []
      const s = new FakeSession(role)
      const c = await opened(s, { writeClipboard: (t) => Promise.resolve(void written.push(t)) })
      s.push(clipboardEvent(text))
      await settle()
      // Every role sees the remote clipboard in the panel.
      expect(c.getState().clipboard).toBe(text)
      expect(written).toEqual(writes)
    }
  })

  it('keeps the user’s clipboard draft when the remote clipboard changes', async () => {
    const s = new FakeSession(Role.Owner)
    const c = await opened(s)
    s.push(clipboardEvent('first'))
    await settle()
    expect(c.getState()).toMatchObject({ clipboard: 'first', draft: 'first' })

    c.setDraft('mine')
    s.push(clipboardEvent('second'))
    await settle()
    expect(c.getState()).toMatchObject({ clipboard: 'second', draft: 'mine' })

    // A draft that is the remote's again follows it again.
    c.setDraft('second')
    s.push(clipboardEvent('third'))
    await settle()
    expect(c.getState()).toMatchObject({ clipboard: 'third', draft: 'third' })
  })

  it('closes the stream as unsupported when it fails to show it', async () => {
    const s = new FakeSession(Role.Owner)
    const broken: TerminalSink = { ...sink, write: () => Promise.reject(new Error('the emulator broke')) }
    const c = await opened(s, {}, broken)
    s.push({ type: 'term-data', data: new Uint8Array([0x61]) })
    await settle()
    expect(c.getState()).toMatchObject({ phase: 'ended', ending: { title: 'Could not connect', detail: 'the emulator broke' } })
    expect(s.closes).toEqual([{ code: CloseCode.Unsupported, reason: 'the client failed to show the session' }])
  })

  it('cancels an open under way on close', async () => {
    const s = new FakeSession(Role.Owner)
    let connected: (s: Session) => void = () => undefined
    const c = controller(
      () =>
        new Promise<Session>((resolve) => {
          connected = resolve
        }),
    )
    const open = c.open(size)
    await settle()
    await c.close()
    // The stream opens as the user leaves: it is closed, not shown.
    connected(s as unknown as Session)
    await open
    expect(c.getState()).toMatchObject({ phase: 'ended', ending: { quiet: true } })
    expect(s.closes).toHaveLength(1)
  })
})

describe('overlayOf', () => {
  const base: SessionState = { ...new SessionController({ url: '', token: '', shared: false }).getState() }
  const owner = { shared: false, sessionId: 'sess' }

  it('covers the screen until the first frame, and says why it ended', () => {
    expect(overlayOf(base)).toBe('connecting')
    expect(overlayOf({ ...base, phase: 'open' })).toBe('connecting')
    expect(overlayOf({ ...base, phase: 'open', shown: true })).toBeNull()
    expect(overlayOf({ ...base, phase: 'ended', ending: describeEnd(CloseCode.SessionClosed, '', owner) })).toBe('ended')
  })

  it('says nothing of a session the user left', () => {
    expect(overlayOf({ ...base, phase: 'ended', ending: describeEnd(CloseCode.Normal, '', owner) })).toBeNull()
    expect(overlayOf({ ...base, phase: 'ended', ending: null })).toBeNull()
  })
})
