// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell in a DOM, under StrictMode: an End that waited for the
// owner's single sign-on, which came back signed in as someone else, is
// ended as occamyd decides, once the owner signs in over the session;
// and a reload over the owner's session joins it again once the user has
// signed in, or lists the connections if the user would rather; and
// signing out forgets the session kept for a reload.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { type AccessToken, Api, type Identity } from '../lib/api'
import { SESSION_KEY } from '../lib/app'
import { type Start, type Target, start } from '../lib/targets'
import { App } from './App'

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

// NeverOpens is a WebSocket that stays connecting, as a stream of a
// session that is not the user's does not open.
class NeverOpens extends EventTarget {
  /** The URLs of the streams opened. */
  static urls: string[] = []
  binaryType = 'blob'
  protocol = ''
  readyState = 0
  constructor(url: string | URL) {
    super()
    NeverOpens.urls.push(String(url))
  }
  close(): void {
    this.readyState = 3
  }
  send(): void {}
}

let root: Root
let container: HTMLElement

beforeEach(() => {
  NeverOpens.urls = []
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

// tab is the tab's sessionStorage, holding items.
function tab(items: Map<string, string>): Storage {
  return {
    get length() {
      return items.size
    },
    clear: () => items.clear(),
    getItem: (k) => items.get(k) ?? null,
    key: () => null,
    removeItem: (k) => void items.delete(k),
    setItem: (k, v) => void items.set(k, v),
  }
}

const lab = JSON.stringify({ kind: 'session', sessionId: 'mZ3k', name: 'Lab' })

// show renders the shell started with begin.
async function show(api: Api, begin: Start, storage: Storage): Promise<void> {
  await act(async () => {
    root.render(
      <StrictMode>
        <App api={api} start={begin} storage={storage} />
      </StrictMode>,
    )
    await Promise.resolve()
  })
  await settle()
}

// signIn signs in with the form on the page as user.
async function signIn(user: string): Promise<void> {
  const form = container.querySelector<HTMLFormElement>('form.sign-in')
  form?.querySelector<HTMLInputElement>('input[name=username]')?.setAttribute('value', user)
  form?.querySelector<HTMLInputElement>('input[name=password]')?.setAttribute('value', 'occamy')
  act(() => form?.requestSubmit())
  await settle(10)
}

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
    // The view opened for alice's End, signed in as bob: occamyd refuses
    // him, and with no stream open the session may have ended.
    expect(log.filter((l) => l.startsWith('DELETE'))).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    expect(text()).toContain('You are signed in as bob, and occamyd did not end this session: it has ended already, or alice opened it. Sign in as alice to end it if it runs on.')
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

  it('joins the owner’s session again after a reload, once the user has signed in', async () => {
    const items = new Map([[SESSION_KEY, lab]])
    const storage = tab(items)
    const log: string[] = []
    const api = occamyd('demo', log)
    // The page loads again, with no fragment, as on a reload.
    await show(api, start({ kind: 'none' }, api, storage), storage)
    // The page says so politely, as no error.
    expect(container.querySelector('[role=status]')?.textContent).toBe('Sign in again to return to Lab.')
    expect(container.querySelector('[role=alert]')).toBeNull()
    expect(NeverOpens.urls).toEqual([])
    await signIn('demo')
    // Signed in, the page joins the session rather than list connections.
    expect(log).toContain('POST /api/v1/auth/token')
    expect(log).not.toContain('GET /api/v1/connections')
    expect(NeverOpens.urls.map((u) => u.split('?')[0])).toEqual([`ws://${location.host}/api/v1/sessions/mZ3k/stream`])
    expect(text()).toContain('Connecting to Lab…')
    // The user leaves it: a reload no longer joins it.
    act(() => button('Cancel')?.click())
    await settle()
    expect(log).toContain('GET /api/v1/connections')
    expect(items.has(SESSION_KEY)).toBe(false)
  })

  it('lists the connections after a reload over the owner’s session, if the user would rather, and forgets the session', async () => {
    const items = new Map([[SESSION_KEY, lab]])
    const storage = tab(items)
    const log: string[] = []
    const api = occamyd('demo', log)
    await show(api, start({ kind: 'none' }, api, storage), storage)
    act(() => button('Go to the connections instead')?.click())
    await settle()
    expect(container.querySelector('[role=status]')).toBeNull()
    expect(button('Go to the connections instead')).toBeUndefined()
    expect(items.has(SESSION_KEY)).toBe(false)
    await signIn('demo')
    expect(log).toContain('GET /api/v1/connections')
    expect(NeverOpens.urls).toEqual([])
    // A reload now starts signed out.
    expect(start({ kind: 'none' }, api, storage)).toEqual({ kind: 'signed-out' })
  })

  it('forgets the session kept for a reload on signing out', async () => {
    const items = new Map([[SESSION_KEY, lab]])
    const storage = tab(items)
    const log: string[] = []
    const api = occamyd('demo', log)
    await show(api, { kind: 'signed-out' }, storage)
    // A sign-in that does not return to the session offers nothing else.
    expect(button('Go to the connections instead')).toBeUndefined()
    await signIn('demo')
    expect(log).toContain('GET /api/v1/connections')
    expect(items.has(SESSION_KEY)).toBe(true)
    act(() => button('Sign out')?.click())
    await settle()
    expect(container.querySelector('form.sign-in')).not.toBeNull()
    expect(items.has(SESSION_KEY)).toBe(false)
  })
})
