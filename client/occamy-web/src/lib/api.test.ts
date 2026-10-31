// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The API v1 calls against a scripted fetch: each request's method,
// path, headers and exact body as docs/api.md gives them, and each
// answer, error and failure.

import { describe, expect, it } from 'vitest'

import { API_TIMEOUT, Api, ApiError, errorMessage, identityOf, sameIdentity, subjectOf } from './api'

interface Sent {
  path: string
  method: string
  headers: Record<string, string>
  body: string | undefined
  redirect: RequestRedirect | undefined
}

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json; charset=utf-8' } })

// scripted returns an Api whose fetch answers with answer, and what it sent.
function scripted(answer: (s: Sent) => Response | Promise<Response>, now = 1_000_000): { api: Api; sent: Sent[] } {
  const sent: Sent[] = []
  const api = new Api(
    async (path, init) => {
      const s: Sent = {
        path,
        method: init?.method ?? 'GET',
        headers: { ...(init?.headers as Record<string, string> | undefined) },
        body: init?.body as string | undefined,
        redirect: init?.redirect,
      }
      sent.push(s)
      return answer(s)
    },
    () => now,
  )
  return { api, sent }
}

const token = { token: 'eyJ.access.token', expires: 2_000_000 }

describe('signIn', () => {
  it('posts exactly the user name and password as JSON, and keeps the token with its expiry', async () => {
    const { api, sent } = scripted(() => json(200, { access_token: 'eyJ.a.b', token_type: 'Bearer', expires_in: 900 }))
    const got = await api.signIn('alice', 'correct horse')
    expect(sent).toEqual([
      {
        path: '/api/v1/auth/token',
        method: 'POST',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: '{"username":"alice","password":"correct horse"}',
        redirect: undefined,
      },
    ])
    // Ten seconds early, so that no request races the expiry.
    expect(got).toEqual({ token: 'eyJ.a.b', expires: 1_000_000 + 900_000 - 10_000 })
  })

  it('reports API v1’s error code', async () => {
    const { api } = scripted(() => json(401, { error: { code: 'unauthorized', message: 'wrong user name or password' } }))
    const err = await api.signIn('alice', 'nope').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 401, code: 'unauthorized', message: 'wrong user name or password' })
    expect(errorMessage(err)).toBe('The user name or password is wrong.')
  })

  it('tells an occamyd without API v1 by its plain 404', async () => {
    const { api } = scripted(() => new Response('404 page not found\n', { status: 404, statusText: 'Not Found' }))
    const err = await api.signIn('alice', 'x').catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 404, code: 'http' })
    expect(errorMessage(err)).toMatch(/version: 1/)
  })

  it('refuses a success it cannot read', async () => {
    for (const body of ['not json', '{"token_type":"Bearer"}', '{"access_token":"","expires_in":900}', '[]']) {
      const { api } = scripted(() => new Response(body, { status: 200 }))
      await expect(api.signIn('a', 'b')).rejects.toMatchObject({ code: 'bad_response' })
    }
  })

  it('reports a failed connection', async () => {
    const { api } = scripted(() => Promise.reject(new TypeError('Failed to fetch')))
    await expect(api.signIn('a', 'b')).rejects.toMatchObject({ status: 0, code: 'network' })
  })
})

describe('OIDC', () => {
  it('finds OIDC by the login route’s 400 without a challenge, and none by its 404', async () => {
    for (const [status, want] of [
      [400, true],
      [404, false],
      [500, false],
    ] as const) {
      const { api, sent } = scripted(() => json(status, { error: { code: 'x', message: 'x' } }))
      expect(await api.oidcAvailable()).toBe(want)
      expect(sent[0]).toMatchObject({ path: '/api/v1/auth/oidc/login', method: 'GET', redirect: 'manual' })
    }
    const { api } = scripted(() => Promise.reject(new TypeError('offline')))
    expect(await api.oidcAvailable()).toBe(false)
  })

  it('exchanges the code with its verifier', async () => {
    const { api, sent } = scripted(() => json(200, { access_token: 'eyJ.o.c', token_type: 'Bearer', expires_in: 60 }))
    expect(await api.oidcToken('JX2E7', 'dBjftJeZ4CVP')).toEqual({ token: 'eyJ.o.c', expires: 1_000_000 + 60_000 - 10_000 })
    expect(sent[0]).toMatchObject({ path: '/api/v1/auth/oidc/token', method: 'POST', body: '{"code":"JX2E7","code_verifier":"dBjftJeZ4CVP"}' })
  })
})

describe('the routes that take an access token', () => {
  it('lists connections with the token in Authorization', async () => {
    const list = { connections: [{ id: 'build-box', name: 'Build box', protocol: 'ssh' }], adhoc_targets: false }
    const { api, sent } = scripted(() => json(200, list))
    expect(await api.connections(token)).toEqual(list.connections)
    expect(sent[0]).toEqual({
      path: '/api/v1/connections',
      method: 'GET',
      headers: { Accept: 'application/json', Authorization: 'Bearer eyJ.access.token' },
      body: undefined,
      redirect: undefined,
    })
  })

  it('refuses a connection list it cannot read', async () => {
    for (const body of [{}, { connections: [{ id: 1, name: 'x', protocol: 'ssh' }] }, { connections: null }]) {
      const { api } = scripted(() => json(200, body))
      await expect(api.connections(token)).rejects.toMatchObject({ code: 'bad_response' })
    }
  })

  it('ends a session, escaping its ID, and takes 204 with no body', async () => {
    const { api, sent } = scripted(() => new Response(null, { status: 204 }))
    await api.endSession(token, 'a/b')
    expect(sent[0]).toMatchObject({ path: '/api/v1/sessions/a%2Fb', method: 'DELETE', body: undefined })
  })

  it('mints a share', async () => {
    const { api, sent } = scripted(() => json(201, { token: 'q8Gf2Lw0', role: 'read-only', expires_in: 600 }))
    expect(await api.share(token, 'mZ3k', 'read-only', 600)).toEqual({ token: 'q8Gf2Lw0', role: 'read-only', expiresIn: 600 })
    expect(sent[0]).toMatchObject({
      path: '/api/v1/sessions/mZ3k/shares',
      method: 'POST',
      headers: { Authorization: 'Bearer eyJ.access.token', 'Content-Type': 'application/json' },
      body: '{"role":"read-only","expires_in":600}',
    })
  })

  it('sends nothing with an expired token, and says to sign in again', async () => {
    const { api, sent } = scripted(() => json(200, {}), 3_000_000)
    const err = await api.connections(token).catch((e: unknown) => e)
    expect(sent).toEqual([])
    expect(err).toMatchObject({ status: 401, code: 'token_expired', signedOut: true })
  })

  it('says to sign in again when occamyd refuses the token', async () => {
    for (const code of ['token_expired', 'invalid_token']) {
      const { api } = scripted(() => json(401, { error: { code, message: 'x' } }))
      const err = (await api.connections(token).catch((e: unknown) => e)) as ApiError
      expect(err.signedOut).toBe(true)
      expect(errorMessage(err)).toMatch(/Sign in again/)
    }
    const { api } = scripted(() => json(429, { error: { code: 'limit_exceeded', message: 'x' } }))
    const err = (await api.share(token, 's', 'viewer', 3600).catch((e: unknown) => e)) as ApiError
    expect(err.signedOut).toBe(false)
  })
})

describe('the time limit', () => {
  // hung answers once the request's signal aborts, with its reason.
  const hung = (_: string, init?: RequestInit): Promise<Response> =>
    new Promise((_, reject) => init?.signal?.addEventListener('abort', () => reject(init.signal?.reason as Error)))

  it('gives up on occamyd when it does not answer in time', async () => {
    const api = new Api(hung, () => 1_000_000, 20)
    const err = await api.connections(token).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 0, code: 'timeout', message: 'occamyd did not answer in time.', signedOut: false })
    expect(errorMessage(err)).toBe('occamyd did not answer in time.')
    await expect(api.endSession(token, 's')).rejects.toMatchObject({ code: 'timeout' })
    await expect(api.signIn('a', 'b')).rejects.toMatchObject({ code: 'timeout' })
    expect(await api.oidcAvailable()).toBe(false)
  })

  it('takes the answer’s body under the same limit', async () => {
    const api = new Api(
      (_, init) => {
        const body = new ReadableStream<Uint8Array>({
          start(c) {
            c.enqueue(new TextEncoder().encode('{"connections":'))
            init?.signal?.addEventListener('abort', () => c.error(init.signal?.reason))
          },
        })
        return Promise.resolve(new Response(body, { status: 200 }))
      },
      () => 1_000_000,
      20,
    )
    await expect(api.connections(token)).rejects.toMatchObject({ status: 0, code: 'timeout' })
  })

  it('lasts as long as occamyd takes to give up on a request', async () => {
    let signal: AbortSignal | undefined
    const api = new Api((_, init) => {
      signal = init?.signal ?? undefined
      return Promise.resolve(json(200, { connections: [] }))
    }, () => 1_000_000)
    await api.connections(token)
    expect(signal?.aborted).toBe(false)
    // occamyd's writeTimeout, in server/server.go.
    expect(API_TIMEOUT).toBe(30_000)
  })
})

const jwt = (claims: unknown): string => {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify(claims))}.c2ln`
}

describe('identityOf', () => {
  it('reads the user and where they signed in', () => {
    expect(identityOf({ token: jwt({ sub: 'alice', idp: 'oidc', kind: 'access' }), expires: 0 })).toEqual({ subject: 'alice', idp: 'oidc' })
    expect(identityOf({ token: jwt({ sub: 'alice', idp: 'local' }), expires: 0 })).toEqual({ subject: 'alice', idp: 'local' })
  })

  it('takes a token without idp for a static user’s, as occamyd does', () => {
    expect(identityOf({ token: jwt({ sub: 'alice' }), expires: 0 })).toEqual({ subject: 'alice', idp: 'local' })
  })

  it('reads no identity from a token it cannot read', () => {
    for (const token of ['', 'opaque', 'a.!!!.c', jwt({ sub: 7 }), jwt({ sub: '' }), jwt({ sub: 'alice', idp: '' }), jwt({ sub: 'alice', idp: 1 }), jwt(['alice'])]) {
      expect(identityOf({ token, expires: 0 })).toBeNull()
    }
  })

  it('tells owners apart by name alone, as occamyd does', () => {
    const alice = { subject: 'alice', idp: 'local' }
    expect(sameIdentity(alice, { subject: 'alice', idp: 'local' })).toBe(true)
    // An OIDC user named as a static user is that user, by link_static_users.
    expect(sameIdentity(alice, { subject: 'alice', idp: 'oidc' })).toBe(true)
    expect(sameIdentity(alice, { subject: 'Alice', idp: 'local' })).toBe(false)
    expect(sameIdentity(alice, { subject: 'bob', idp: 'local' })).toBe(false)
  })
})

describe('subjectOf', () => {

  it('reads the user the token names', () => {
    expect(subjectOf({ token: jwt({ sub: 'alice', kind: 'access' }), expires: 0 })).toBe('alice')
    expect(subjectOf({ token: jwt({ sub: 'Ünïcödé 名前' }), expires: 0 })).toBe('Ünïcödé 名前')
  })

  it('clips a long name', () => {
    expect(Array.from(subjectOf({ token: jwt({ sub: 'x'.repeat(5000) }), expires: 0 }))).toHaveLength(200)
  })

  it('is empty for a token it cannot read', () => {
    for (const token of ['', 'opaque', 'a.!!!.c', jwt({ sub: 7 }), jwt(['alice'])]) {
      expect(subjectOf({ token, expires: 0 })).toBe('')
    }
  })
})
