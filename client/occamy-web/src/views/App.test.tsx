// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell in a DOM, under StrictMode: an End that waited for the
// owner's single sign-on, which came back signed in as someone else, is
// ended as occamyd decides, once the owner signs in over the session.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { type AccessToken, Api, type Identity } from '../lib/api'
import type { Target } from '../lib/targets'
import { App } from './App'

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

// NeverOpens is a WebSocket that stays connecting, as a stream of a
// session that is not the user's does not open.
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

function encoded(sub: string, idp: string): string {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp }))}.c2ln`
}

const json = (v: unknown, status = 200): Promise<Response> => Promise.resolve(new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } }))

// occamyd answers the shell as occamyd does with OIDC off: a password
// signs anyone in, and only owner, whose session mZ3k is, ends it.
function occamyd(owner: string, log: string[]): Api {
  return new Api(
    (path, init) => {
      log.push(`${init?.method} ${path}`)
      if (path === '/api/v1/auth/token' && init?.method === 'POST') {
        const { username } = JSON.parse(init.body as string) as { username: string }
        return json({ access_token: encoded(username, 'local'), token_type: 'Bearer', expires_in: 600 })
      }
      if (path === '/api/v1/sessions/mZ3k' && init?.method === 'DELETE') {
        const auth = (init.headers as Record<string, string>).Authorization
        const { sub } = JSON.parse(Buffer.from(auth.split('.')[1], 'base64url').toString()) as { sub: string }
        return sub === owner ? Promise.resolve(new Response(null, { status: 204 })) : json({ error: { code: 'not_found', message: 'no such session' } }, 404)
      }
      if (path === '/api/v1/connections') {
        return json({ connections: [] })
      }
      return json({ error: { code: 'not_found', message: 'not found' } }, 404)
    },
    Date.now,
  )
}

const settle = (ms = 0): Promise<void> => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)))
const text = (): string => container.textContent ?? ''
const button = (label: string): HTMLButtonElement | undefined => [...container.querySelectorAll('button')].find((b) => b.textContent === label)

describe('App', () => {
  it('ends alice’s session once alice signs in, after her single sign-on came back as bob', async () => {
    const mine: Target = { kind: 'session', sessionId: 'mZ3k', name: 'Lab' }
    const alice: Identity = { subject: 'alice', idp: 'local' }
    const bob: AccessToken = { token: encoded('bob', 'oidc'), expires: Date.now() + 600_000 }
    const log: string[] = []
    const api = occamyd('alice', log)
    await act(async () => {
      root.render(
        <StrictMode>
          <App api={api} start={{ kind: 'oidc', token: Promise.resolve(bob), resume: { target: mine, view: null, then: { kind: 'end' }, owner: alice } }} storage={undefined} />
        </StrictMode>,
      )
      await Promise.resolve()
    })
    await settle(10)
    // The view opened for alice's End, signed in as bob: occamyd refuses him.
    expect(log.filter((l) => l.startsWith('DELETE'))).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(text()).toContain('You are signed in as bob, but alice opened this session, so it was not ended for everyone. Sign in as alice to end it.')
    act(() => button('Sign in')?.click())
    await settle()
    const form = container.querySelector<HTMLFormElement>('dialog form.sign-in')
    expect(form).not.toBeNull()
    form?.querySelector<HTMLInputElement>('input[name=username]')?.setAttribute('value', 'alice')
    form?.querySelector<HTMLInputElement>('input[name=password]')?.setAttribute('value', 'secret')
    act(() => form?.requestSubmit())
    await settle(10)
    // alice signed in over the session: occamyd ends it, and the shell
    // goes back to the connection list.
    expect(log.filter((l) => l.startsWith('DELETE'))).toEqual(['DELETE /api/v1/sessions/mZ3k', 'DELETE /api/v1/sessions/mZ3k'])
    expect(log).toContain('GET /api/v1/connections')
    expect(container.querySelector('dialog')).toBeNull()
  })
})
