// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The session view in a DOM, under StrictMode, whose development effects
// run twice, with a WebSocket that never opens: an owner's action that
// waited for a sign-in is taken once, End shows that it waits for
// occamyd and that it failed, and an action dropped for another user
// says why.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { type AccessToken, Api } from '../lib/api'
import type { ViewAction } from '../lib/app'
import { notOwner } from '../lib/owner'
import type { Target } from '../lib/targets'
import { SessionView, type SignInNext } from './SessionView'

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

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
})

function accessToken(sub: string, expires = Date.now() + 600_000): AccessToken {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return { token: `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp: 'local' }))}.c2ln`, expires }
}

const target: Target = { kind: 'session', sessionId: 'mZ3k', name: 'Lab' }
const demo = { subject: 'demo', idp: 'local' }

interface Rendered {
  deletes: string[]
  left: number
  signIns: [string, SignInNext][]
}

const settle = (ms = 0): Promise<void> => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)))

// render shows the session view of target to demo, with action, and an
// Api whose DELETE answers with answer.
async function render(
  action: { seq: number; action: ViewAction } | null,
  answer: (signal: AbortSignal) => Promise<Response>,
  timeout?: number,
  token = accessToken('demo'),
): Promise<Rendered> {
  const r: Rendered = { deletes: [], left: 0, signIns: [] }
  const api = new Api(
    (path, init) => {
      r.deletes.push(`${init?.method} ${path}`)
      return answer(init?.signal as AbortSignal)
    },
    Date.now,
    timeout,
  )
  const view = (a: typeof action): React.JSX.Element => (
    <StrictMode>
      <SessionView
        api={api}
        token={token}
        target={target}
        action={a}
        onLeave={() => void r.left++}
        onSignIn={(message, next) => void r.signIns.push([message, next])}
        onRejoin={() => undefined}
      />
    </StrictMode>
  )
  await act(async () => {
    root.render(view(action))
    await Promise.resolve()
  })
  await settle()
  rerender = (a) =>
    act(async () => {
      root.render(view(a))
      await Promise.resolve()
    })
  return r
}
let rerender: (a: { seq: number; action: ViewAction } | null) => Promise<void> = () => Promise.resolve()

const ended = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))
const text = (): string => container.textContent ?? ''
const button = (label: string): HTMLButtonElement | undefined => [...container.querySelectorAll('button')].find((b) => b.textContent === label)

describe('SessionView', () => {
  it('takes an End that waited for the owner once, though its effects run twice', async () => {
    const end = { seq: 1, action: { kind: 'end' } } as const
    const r = await render(end, ended)
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(r.left).toBe(1)
    // Rendered again with the same action, it ends nothing more.
    await rerender(end)
    await rerender({ ...end })
    await settle()
    expect(r.deletes).toHaveLength(1)
  })

  it('says it is ending the session while occamyd answers, and lets the owner leave', async () => {
    let answer: (r: Response) => void = () => undefined
    const r = await render({ seq: 1, action: { kind: 'end' } }, () => new Promise((resolve) => (answer = resolve)))
    expect(text()).toContain('Ending the session for everyone…')
    act(() => button('Back to connections')?.click())
    expect(r.left).toBe(1)
    answer(new Response(null, { status: 204 }))
  })

  it('asks an owner whose sign-in expired to sign in over the session, rather than wait', async () => {
    const r = await render({ seq: 1, action: { kind: 'end' } }, ended, undefined, accessToken('demo', Date.now() - 1))
    expect(r.deletes).toEqual([])
    expect(r.signIns).toEqual([['Your sign-in has expired. Sign in again to end the session for everyone.', { target, then: { kind: 'end' }, here: true, owner: demo }]])
    expect(text()).not.toContain('Ending the session for everyone…')
  })

  it('tells the owner when occamyd did not answer in time, and tries again', async () => {
    const hung = (signal: AbortSignal): Promise<Response> => new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason as Error)))
    const r = await render({ seq: 1, action: { kind: 'end' } }, hung, 20)
    await settle(60)
    expect(text()).toContain('Not ended for everyone')
    expect(text()).toContain('occamyd did not answer in time.')
    expect(text()).not.toContain('Ending the session for everyone…')
    act(() => button('Try again')?.click())
    await settle()
    expect(r.deletes).toEqual(['DELETE /api/v1/sessions/mZ3k', 'DELETE /api/v1/sessions/mZ3k'])
    expect(text()).toContain('Ending the session for everyone…')
  })

  it('says why an action was dropped for another user, and offers to sign in as the owner', async () => {
    const message = notOwner({ kind: 'end' }, demo, { subject: 'mallory', idp: 'local' })
    const r = await render({ seq: 1, action: { kind: 'refused', then: { kind: 'end' }, message } }, ended)
    expect(r.deletes).toEqual([])
    expect(text()).toContain('Not ended for everyone')
    expect(text()).toContain(message)
    expect(button('Try again')).toBeUndefined()
    act(() => button('Sign in')?.click())
    expect(r.signIns).toEqual([[message, { target, then: { kind: 'end' }, here: true, owner: demo }]])
  })
})
