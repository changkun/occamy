// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { Api } from './api'
import { VERIFIER_KEY } from './oidc'
import { start, streamToken, streamURL } from './targets'

describe('streamURL', () => {
  it('opens a connection’s stream, or joins a session’s, on the page’s origin', () => {
    expect(streamURL('https://occamy.example', { kind: 'connection', id: 'build-box', name: 'Build box' })).toBe(
      'wss://occamy.example/api/v1/connections/build-box/stream',
    )
    expect(streamURL('http://localhost:5637', { kind: 'session', sessionId: 'mZ3k', name: 'x' })).toBe('ws://localhost:5637/api/v1/sessions/mZ3k/stream')
    expect(streamURL('http://localhost:5637', { kind: 'share', sessionId: 'a/b', token: 't' })).toBe('ws://localhost:5637/api/v1/sessions/a%2Fb/stream')
  })
})

describe('streamToken', () => {
  const access = { token: 'eyJ.access', expires: 0 }
  it('is the share for a share, and the access token otherwise', () => {
    expect(streamToken({ kind: 'share', sessionId: 's', token: 'q8Gf' }, access)).toBe('q8Gf')
    expect(streamToken({ kind: 'connection', id: 'c', name: 'c' }, access)).toBe('eyJ.access')
    expect(streamToken({ kind: 'session', sessionId: 's', name: 'c' }, null)).toBe('')
  })
})

describe('start', () => {
  // An Api whose token exchange records its calls and succeeds.
  function api(): { api: Api; exchanges: string[] } {
    const exchanges: string[] = []
    return {
      exchanges,
      api: new Api(
        (_path, init) => {
          exchanges.push(init?.body as string)
          return Promise.resolve(new Response('{"access_token":"eyJ.o","token_type":"Bearer","expires_in":900}', { status: 200 }))
        },
        () => 0,
      ),
    }
  }

  function storage(verifier?: string): Storage {
    const items = new Map<string, string>(verifier === undefined ? [] : [[VERIFIER_KEY, verifier]])
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

  it('starts signed out without a fragment', () => {
    expect(start({ kind: 'none' }, api().api, storage())).toEqual({ kind: 'signed-out' })
  })

  it('joins from a share link', () => {
    expect(start({ kind: 'join', sessionId: 's', token: 't' }, api().api, storage())).toEqual({ kind: 'join', sessionId: 's', token: 't' })
  })

  it('exchanges the one-time code with this tab’s verifier, and spends the verifier', async () => {
    const { api: a, exchanges } = api()
    const s = storage('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')
    const got = start({ kind: 'oidc-code', code: 'JX2E7' }, a, s)
    expect(got.kind).toBe('oidc')
    if (got.kind === 'oidc') {
      expect(await got.token).toEqual({ token: 'eyJ.o', expires: 900_000 - 10_000 })
    }
    expect(exchanges).toEqual(['{"code":"JX2E7","code_verifier":"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}'])
    expect(s.getItem(VERIFIER_KEY)).toBeNull()
  })

  it('spends a code this tab did not ask for, and signs nobody in', async () => {
    const { api: a, exchanges } = api()
    const got = start({ kind: 'oidc-code', code: 'planted' }, a, storage())
    expect(got.kind).toBe('oidc-error')
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(exchanges).toHaveLength(1)
    const body = JSON.parse(exchanges[0]) as { code: string; code_verifier: string }
    expect(body.code).toBe('planted')
    expect(body.code_verifier).toMatch(/^[A-Za-z0-9._~-]{43,128}$/)
  })

  it('says why an OIDC sign-in failed', () => {
    expect(start({ kind: 'oidc-error', error: 'access_denied' }, api().api, storage())).toEqual({
      kind: 'oidc-error',
      message: 'The identity provider or occamyd refused this user.',
    })
  })
})
