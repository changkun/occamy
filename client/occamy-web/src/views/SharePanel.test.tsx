// @vitest-environment happy-dom

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The share panel in a DOM, under StrictMode, whose development effects
// run twice: a share that waited for a sign-in is created once, and one
// that occamyd refuses says so.

import { StrictMode, act } from 'react'
import { type Root, createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { Api } from '../lib/api'
import { SharePanel, type ShareRequest } from './SharePanel'

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined
}

let root: Root
let container: HTMLElement

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

// shares returns an Api whose POST of a share answers at once, and the
// bodies it was sent.
function shares(): { api: Api; sent: string[] } {
  const sent: string[] = []
  const api = new Api((_, init) => {
    sent.push(init?.body as string)
    return Promise.resolve(new Response(JSON.stringify({ token: `share-${sent.length}`, role: 'viewer', expires_in: 3600 }), { status: 200 }))
  })
  return { api, sent }
}

const token = { token: 'eyJ.access.token', expires: Number.MAX_SAFE_INTEGER }

async function render(api: Api, request: ShareRequest | null, onSignIn: () => void = () => undefined): Promise<void> {
  await act(async () => {
    root.render(
      <StrictMode>
        <SharePanel api={api} token={token} sessionId="mZ3k" request={request} onClose={() => undefined} onSignIn={onSignIn} />
      </StrictMode>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe('SharePanel', () => {
  it('creates a share that waited for the owner once, though its effects run twice', async () => {
    const { api, sent } = shares()
    const request = { seq: 1, role: 'viewer', expiresIn: 3600 } as const
    await render(api, request)
    expect(sent).toEqual(['{"role":"viewer","expires_in":3600}'])
    expect(container.querySelector<HTMLInputElement>('input[readonly]')?.value).toMatch(/#join=mZ3k&token=share-1$/)
    // Rendered again, with the same request or a copy of it, it creates none.
    await render(api, request)
    await render(api, { ...request })
    expect(sent).toHaveLength(1)
    // Another request, numbered anew, is another share.
    await render(api, { seq: 2, role: 'read-only', expiresIn: 600 })
    expect(sent).toEqual(['{"role":"viewer","expires_in":3600}', '{"role":"read-only","expires_in":600}'])
  })

  it('creates nothing without a request', async () => {
    const { api, sent } = shares()
    await render(api, null)
    expect(sent).toEqual([])
  })

  it('says so when occamyd does not take the session for the user’s, as for another user signed in', async () => {
    const sent: string[] = []
    const api = new Api((path, init) => {
      sent.push(`${init?.method} ${path}`)
      return Promise.resolve(new Response(JSON.stringify({ error: { code: 'not_found', message: 'no such session' } }), { status: 404 }))
    })
    let signIns = 0
    await render(api, { seq: 1, role: 'viewer', expiresIn: 3600 }, () => void signIns++)
    expect(sent).toEqual(['POST /api/v1/sessions/mZ3k/shares'])
    expect(container.textContent).toContain('The session has ended, or is not yours.')
    expect(container.querySelector('input[readonly]')).toBeNull()
    expect(signIns).toBe(0)
  })
})
