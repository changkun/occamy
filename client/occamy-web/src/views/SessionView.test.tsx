// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The session view in a DOM, under StrictMode, whose development effects
// run twice, with a WebSocket that never opens: an owner's action that
// waited for a sign-in is taken once.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { type AccessToken, Api } from '../lib/api'
import type { SessionAction } from '../lib/app'
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

interface Rendered {
  deletes: string[]
  left: number
  signIns: [string, SignInNext][]
}

const settle = (ms = 0): Promise<void> => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)))

// render shows the session view of target to demo, with action, and an
// Api whose DELETE answers with answer.
async function render(
  action: { seq: number; action: SessionAction } | null,
  answer: (signal: AbortSignal) => Promise<Response>,
  token = accessToken('demo'),
): Promise<Rendered> {
  const r: Rendered = { deletes: [], left: 0, signIns: [] }
  const api = new Api(
    (path, init) => {
      r.deletes.push(`${init?.method} ${path}`)
      return answer(init?.signal as AbortSignal)
    },
    Date.now,
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
let rerender: (a: { seq: number; action: SessionAction } | null) => Promise<void> = () => Promise.resolve()

const ended = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))

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
})
