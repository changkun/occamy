// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The session view in a DOM, under StrictMode, whose development effects
// run twice: an owner's action that waited for a sign-in is taken once,
// End shows that it waits for occamyd and that it failed, and takes no
// second press meanwhile, an End that resumed is ended or refused as
// occamyd decides, for the user who opened the session as the stream's
// welcome says, and the stream closed for the session's end counts for
// the session ended.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CloseCode, Role, type Session, SessionClass, type SessionEvent } from '../protocol'
import { type AccessToken, Api, type Identity } from '../lib/api'
import type { SessionAction } from '../lib/app'
import { describeEnd } from '../lib/ending'
import type * as controller from '../lib/controller'
import type { Target } from '../lib/targets'
import { SessionView, type SignInNext } from './SessionView'

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

// The view's controller connects with stream.connect, once a test sets it.
const stream = vi.hoisted(() => ({ connect: undefined as controller.ControllerOptions['connect'] }))
vi.mock('../lib/controller', async (importOriginal) => {
  const m = await importOriginal<typeof controller>()
  class SessionController extends m.SessionController {
    constructor(o: controller.ControllerOptions) {
      super({ ...o, connect: stream.connect ?? o.connect })
    }
  }
  return { ...m, SessionController }
})

// NeverOpens is a WebSocket that stays connecting, so that the stream's
// open waits until the view closes it.
class NeverOpens extends EventTarget {
  binaryType = 'blob'
  protocol = ''
  readyState = 0
  close(): void {
    this.readyState = 3
  }
  send(): void {}
}

// Welcomed stands in for the core's Session where the controller and
// its Display touch it, as controller.test.ts's FakeSession does: a
// graphical stream occamyd welcomed as the owner's.
class Welcomed {
  static last: Welcomed | null = null
  readonly sessionId = 'mZ3k'
  readonly viewerId = 1
  readonly role = Role.Owner
  readonly sessionClass = SessionClass.Graphical
  readonly framebuffer = { width: 0, height: 0, data: new Uint8ClampedArray(0) }
  /** Whether the client closed the stream. */
  closed = false
  private readonly events: SessionEvent[] = []
  private wake: (() => void) | null = null

  constructor() {
    Welcomed.last = this
  }

  /** push delivers ev from occamyd. */
  push(ev: SessionEvent): void {
    this.events.push(ev)
    this.wake?.()
  }

  /** end closes the stream from occamyd's end, as with 4002 once the session has ended for everyone. */
  end(code: number): void {
    this.push({ type: 'end', code, reason: '' })
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

  close(): Promise<void> {
    this.closed = true
    return Promise.resolve()
  }

  resize(): number {
    return 1
  }

  view(): void {}

  clipboard(): number {
    return 1
  }
}

let root: Root
let container: HTMLElement

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  vi.stubGlobal('WebSocket', NeverOpens)
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  stream.connect = undefined
})

// welcomed has the view's stream welcomed as the owner's, on a canvas
// with a 2D context that draws nothing.
function welcomed(): void {
  stream.connect = () => Promise.resolve(new Welcomed() as unknown as Session)
  const ctx = { font: '', measureText: () => ({ width: 9 }) }
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(ctx as unknown as CanvasRenderingContext2D)
}

function accessToken(sub: string, idp = 'local', expires = Date.now() + 600_000): AccessToken {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return { token: `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp }))}.c2ln`, expires }
}

const target: Target = { kind: 'session', sessionId: 'mZ3k', name: 'Lab' }
const demo: Identity = { subject: 'demo', idp: 'local' }
const alice: Identity = { subject: 'alice', idp: 'local' }

type Answer = (signal: AbortSignal, init?: RequestInit) => Promise<Response>

// occamyd answers DELETE as occamyd does for a session opened by owner:
// it ends it for a token whose sub is owner's, signed in with a password
// or, linked to that user, through single sign-on, and answers 404 for
// anyone else.
function occamyd(owner: string): Answer {
  return (_, init) => {
    const auth = (init?.headers as Record<string, string> | undefined)?.Authorization ?? ''
    const claims = JSON.parse(Buffer.from(auth.split('.')[1] ?? '', 'base64url').toString()) as { sub?: string }
    return claims.sub === owner ? ended() : notFound()
  }
}

interface Props {
  token: AccessToken
  /** Who opened the session, as the page knew before the view. */
  owner: Identity | null
  action: { seq: number; action: SessionAction } | null
}

interface Rendered {
  deletes: string[]
  left: number
  signIns: [string, SignInNext][]
  rerender: (p: Partial<Props>) => Promise<void>
}

const settle = (ms = 0): Promise<void> => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)))

// render shows the session view of target, signed in as demo, for an End
// that waited for demo unless props say otherwise, with an Api whose
// DELETE answers with answer.
async function render(props: Partial<Props>, answer: Answer, timeout?: number): Promise<Rendered> {
  let p: Props = { token: accessToken('demo'), owner: demo, action: { seq: 1, action: { kind: 'end' } }, ...props }
  const api = new Api(
    (path, init) => {
      r.deletes.push(`${init?.method} ${path}`)
      return answer(init?.signal as AbortSignal, init)
    },
    Date.now,
    timeout,
  )
  const view = (): React.JSX.Element => (
    <StrictMode>
      <SessionView
        api={api}
        token={p.token}
        target={target}
        owner={p.owner}
        action={p.action}
        onLeave={() => void r.left++}
        onSignIn={(message, next) => void r.signIns.push([message, next])}
        onRejoin={() => undefined}
      />
    </StrictMode>
  )
  const show = async (): Promise<void> => {
    await act(async () => {
      root.render(view())
      await Promise.resolve()
    })
    await settle()
  }
  const r: Rendered = {
    deletes: [],
    left: 0,
    signIns: [],
    rerender: (next) => {
      p = { ...p, ...next }
      return show()
    },
  }
  await show()
  return r
}

const ended = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))
const notFound = (): Promise<Response> => Promise.resolve(new Response(JSON.stringify({ error: { code: 'not_found', message: 'no such session' } }), { status: 404 }))
const failed = (): Promise<Response> => Promise.resolve(new Response(JSON.stringify({ error: { code: 'internal', message: 'internal' } }), { status: 500 }))
const hung = (signal: AbortSignal): Promise<Response> => new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason as Error)))
const text = (): string => container.textContent ?? ''
const button = (label: string): HTMLButtonElement | undefined => [...container.querySelectorAll('button')].find((b) => b.textContent === label)
const end = (seq: number): Props['action'] => ({ seq, action: { kind: 'end' } })

describe('SessionView', () => {
  it('takes an End that waited for the owner once, though its effects run twice', async () => {
    const r = await render({}, ended)
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(r.left).toBe(1)
    // Rendered again with the same action, it ends nothing more.
    await r.rerender({ action: end(1) })
    await settle()
    expect(r.deletes).toHaveLength(1)
  })

  it('says it is ending the session while occamyd answers, and lets the owner leave', async () => {
    let answer: (r: Response) => void = () => undefined
    const r = await render({}, () => new Promise((resolve) => (answer = resolve)))
    expect(text()).toContain('Ending the session for everyone…')
    act(() => button('Back to connections')?.click())
    expect(r.left).toBe(1)
    answer(new Response(null, { status: 204 }))
  })

  it('asks an owner whose sign-in expired to sign in over the session, rather than wait', async () => {
    const r = await render({ token: accessToken('demo', 'local', Date.now() - 1) }, ended)
    expect(r.deletes).toEqual([])
    expect(r.signIns).toEqual([['Your sign-in has expired. Sign in again to end the session for everyone.', { target, then: { kind: 'end' }, here: true, owner: demo }]])
    expect(text()).not.toContain('Ending the session for everyone…')
  })

  it('tells the owner when occamyd did not answer in time, and tries again', async () => {
    const r = await render({}, hung, 20)
    await settle(60)
    expect(text()).toContain('Not ended for everyone')
    expect(text()).toContain('occamyd did not answer in time. It runs on, with its viewers.')
    expect(text()).not.toContain('Ending the session for everyone…')
    act(() => button('Try again')?.click())
    await settle()
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k', 'DELETE /api/v1/sessions/mZ3k'])
    expect(text()).toContain('Ending the session for everyone…')
  })

  it('ends alice’s session once alice signs in, after bob, signed in through single sign-on, was told it is not his', async () => {
    // The view opened for alice's End, after bob signed in. Its stream is
    // not open, so the session may have ended.
    const r = await render({ token: accessToken('bob', 'oidc'), owner: alice }, occamyd('alice'))
    const message = 'You are signed in as bob, and occamyd did not end this session: it has ended already, or alice opened it. Sign in as alice to end it if it runs on.'
    expect(text()).toContain(message)
    expect(r.left).toBe(0)
    expect(button('Try again')).toBeUndefined()
    act(() => button('Sign in')?.click())
    expect(r.signIns).toEqual([[message, { target, then: { kind: 'end' }, here: true, owner: alice }]])
    // alice signs in with her password, over the view.
    await r.rerender({ token: accessToken('alice'), action: end(2) })
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k', 'DELETE /api/v1/sessions/mZ3k'])
    expect(r.left).toBe(1)
  })

  it('ends the session of a user linked to their single sign-on', async () => {
    const r = await render({ token: accessToken('alice', 'oidc'), owner: alice }, occamyd('alice'))
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(r.left).toBe(1)
  })
})

describe('SessionView of a stream welcomed as the owner’s', () => {
  beforeEach(welcomed)

  it('takes the user who opened it for the owner, not whoever signs in after', async () => {
    // alice opens the session, and presses End once her sign-in expired.
    const r = await render({ token: accessToken('alice', 'local', Date.now() - 1), owner: null, action: null }, occamyd('alice'))
    expect(Welcomed.last?.closed).toBe(false)
    act(() => button('End session')?.click())
    await settle()
    expect(r.signIns).toHaveLength(1)
    expect(r.signIns[0][1]).toMatchObject({ then: { kind: 'end' }, here: true, owner: alice })
    // bob signs in over the view: occamyd refuses him, and the stream runs on.
    await r.rerender({ token: accessToken('bob'), action: end(1) })
    const message = 'You are signed in as bob, but alice opened this session, so it was not ended for everyone. Sign in as alice to end it.'
    expect(text()).toContain(message)
    expect(Welcomed.last?.closed).toBe(false)
    act(() => button('Sign in')?.click())
    expect(r.signIns[1]).toEqual([message, { target, then: { kind: 'end' }, here: true, owner: alice }])
    // alice signs in again: occamyd ends her session, and the stream closes.
    await r.rerender({ token: accessToken('alice'), action: end(2) })
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k', 'DELETE /api/v1/sessions/mZ3k'])
    expect(Welcomed.last?.closed).toBe(true)
    expect(r.left).toBe(1)
  })

  it('tries an End that resumed again as occamyd decides on, as it was', async () => {
    let answer: Answer = failed
    const r = await render({ token: accessToken('alice'), owner: null }, (signal, init) => answer(signal, init))
    expect(text()).toContain('It runs on, with its viewers.')
    // A 404 now says the session is not alice's, rather than ended.
    answer = notFound
    act(() => button('Try again')?.click())
    await settle()
    expect(text()).toContain('occamyd has no session of yours to end, signed in as alice')
    expect(r.left).toBe(0)
    expect(Welcomed.last?.closed).toBe(false)
  })

  it('takes the stream’s 4002 for the session ended, rather than what End said before', async () => {
    const r = await render({ token: accessToken('alice'), owner: null }, hung, 20)
    await settle(60)
    expect(text()).toContain('Not ended for everyone')
    // The End that did not answer in time ended the session after all.
    act(() => Welcomed.last?.end(CloseCode.SessionClosed))
    await settle()
    expect(text()).not.toContain('Not ended for everyone')
    expect(text()).toContain('Session ended')
    expect(r.deletes).toHaveLength(1)
  })

  // The codes occamyd closes a session's streams with once the session
  // ended for everyone (CloseFor, internal/stream/serve.go), but 1011.
  const sessionEnds = [CloseCode.ServerShutdown, CloseCode.SessionClosed, CloseCode.OwnerLeft, CloseCode.Idle, CloseCode.RemoteEnded, CloseCode.BackendFailed]

  it.each(sessionEnds)('takes close %i before a resumed End for the session ended, and sends nothing', async (code) => {
    // alice's sign-in expired, and the stream ends while she signs in.
    const r = await render({ token: accessToken('alice', 'local', Date.now() - 1), owner: null, action: null }, notFound)
    act(() => button('End session')?.click())
    await settle()
    expect(r.signIns).toHaveLength(1)
    act(() => Welcomed.last?.end(code))
    await settle()
    expect(container.querySelector('#end-title')?.textContent).toBe(describeEnd(code, '', { shared: false, sessionId: 'mZ3k' }).title)
    await r.rerender({ token: accessToken('alice'), action: end(1) })
    expect(r.deletes).toEqual([])
    expect(r.left).toBe(1)
    expect(text()).not.toContain('Not ended for everyone')
  })

  it.each(sessionEnds)('takes close %i during a resumed End for the session ended, ahead of a 404', async (code) => {
    const r = await render({ token: accessToken('alice'), owner: null, action: null }, () => {
      Welcomed.last?.end(code)
      return new Promise((resolve) => setTimeout(() => resolve(notFound()), 10))
    })
    // A user who did not open the session ends it as occamyd decides.
    await r.rerender({ token: accessToken('bob'), action: end(1) })
    await settle(20)
    expect(r.deletes).toHaveLength(1)
    expect(r.left).toBe(1)
    expect(text()).not.toContain('Not ended for everyone')
  })

  it.each(sessionEnds)('replaces what End said with close %i, once the session ended', async (code) => {
    const r = await render({ token: accessToken('alice'), owner: null, action: null }, notFound)
    await r.rerender({ token: accessToken('bob'), action: end(1) })
    expect(text()).toContain('You are signed in as bob, but alice opened this session, so it was not ended for everyone.')
    act(() => Welcomed.last?.end(code))
    await settle()
    expect(text()).not.toContain('Not ended for everyone')
    expect(container.querySelector('#end-title')?.textContent).toBe(describeEnd(code, '', { shared: false, sessionId: 'mZ3k' }).title)
  })

  it.each([CloseCode.InternalError, CloseCode.Forbidden])('takes close %i for no end of the session, so a 404 may still be the session of another', async (code) => {
    const r = await render({ token: accessToken('alice'), owner: null, action: null }, () => {
      Welcomed.last?.end(code)
      return new Promise((resolve) => setTimeout(() => resolve(notFound()), 10))
    })
    await r.rerender({ token: accessToken('bob'), action: end(1) })
    await settle(20)
    expect(r.left).toBe(0)
    // With no stream open, the session may have ended too.
    expect(text()).toContain('You are signed in as bob, and occamyd did not end this session: it has ended already, or alice opened it.')
  })

  it('takes no second End while one waits for occamyd', async () => {
    const r = await render({ token: accessToken('alice'), owner: null }, hung)
    expect(text()).toContain('Ending the session for everyone…')
    expect(button('End session')?.disabled).toBe(true)
    // The remote asks for credentials meanwhile: the prompt's End waits too.
    act(() => Welcomed.last?.push({ type: 'prompt', id: 1, name: 'Lab', instruction: '', fields: [{ label: 'Password', secret: true }] }))
    await settle()
    const inPrompt = [...container.querySelectorAll<HTMLButtonElement>('dialog button')].find((b) => b.textContent === 'End session')
    expect(inPrompt).toBeDefined()
    act(() => inPrompt?.click())
    await settle()
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(Welcomed.last?.closed).toBe(false)
    expect(text()).toContain('Ending the session for everyone…')
  })
})
