// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The controller without a server: how it opens, fails and is
// cancelled. controller.interop.test.ts runs it against occamyd's stream
// code.

import { describe, expect, it } from 'vitest'

import { CloseCode, CloseError, ConnectionError, type ConnectOptions, type Session } from '../protocol'
import { type ControllerOptions, SessionController } from './controller'

const size = { width: 1280, height: 720, cols: 100, rows: 30, pixelRatio: 200 }

function controller(connect: (url: string, token: string, o: ConnectOptions) => Promise<Session>, o: Partial<ControllerOptions> = {}): SessionController {
  return new SessionController({ url: 'ws://occamy.test/api/v1/connections/lab/stream', token: 't', shared: false, connect, ...o })
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
})
