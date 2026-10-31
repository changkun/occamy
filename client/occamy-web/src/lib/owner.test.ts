// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { type AccessToken, Api, type Identity } from './api'
import { type EndOptions, endSession } from './owner'

// token returns an access token of occamyd's for sub, signed in at idp,
// lasting until expires.
function token(sub: string, idp: string, expires: number): AccessToken {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return { token: `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp }))}.c2ln`, expires }
}

const now = 1_000_000
const fresh = token('demo', 'local', now + 600_000)
const demo: Identity = { subject: 'demo', idp: 'local' }

// options returns endSession's options for the session demo opened, with
// an Api whose DELETE answers with answer, recording the order of the
// stream's close and the call.
function options(answer: (signal: AbortSignal) => Promise<Response>, o: Partial<EndOptions> = {}, timeout?: number): { log: string[]; options: EndOptions } {
  const log: string[] = []
  const api = new Api(
    (path, init) => {
      log.push(`${init?.method} ${path}`)
      return answer(init?.signal as AbortSignal)
    },
    () => now,
    timeout,
  )
  return { log, options: { api, token: fresh, owner: demo, sessionId: 'mZ3k', now, close: () => Promise.resolve(void log.push('close')), ...o } }
}

const ok = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))
const error = (status: number, code: string) => (): Promise<Response> => Promise.resolve(new Response(JSON.stringify({ error: { code, message: code } }), { status }))

describe('endSession', () => {
  it('closes the owner’s stream, then ends the session for everyone', async () => {
    const { log, options: o } = options(ok)
    expect(await endSession(o)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('sends nothing and closes nothing with an expired token, so that the owner signs in with the session on screen', async () => {
    for (const t of [token('demo', 'local', now), null]) {
      const { log, options: o } = options(ok, { token: t })
      expect(await endSession(o)).toMatchObject({ kind: 'sign-in', closed: false })
      expect(log).toEqual([])
    }
  })

  it('asks the owner to sign in when occamyd no longer takes the token', async () => {
    const { log, options: o } = options(error(401, 'invalid_token'))
    expect(await endSession(o)).toMatchObject({ kind: 'sign-in', closed: true })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('tells the owner when the session was not ended for everyone', async () => {
    for (const answer of [error(500, 'internal'), () => Promise.reject(new TypeError('offline'))]) {
      const { options: o } = options(answer)
      const got = await endSession(o)
      expect(got.kind).toBe('failed')
      expect(got.kind === 'failed' && got.message).toMatch(/not ended for everyone.*minute of grace/)
    }
  })

  it('tells the owner when occamyd does not answer in time, rather than waiting on it', async () => {
    const hung = (signal: AbortSignal): Promise<Response> =>
      new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason as Error)))
    const { log, options: o } = options(hung, {}, 20)
    const got = await endSession(o)
    expect(got).toEqual({ kind: 'failed', message: 'The session was not ended for everyone: occamyd did not answer in time. Its viewers stay until your minute of grace runs out.' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('takes a session that ended already as ended', async () => {
    const { options: o } = options(error(404, 'not_found'))
    expect(await endSession(o)).toEqual({ kind: 'ended' })
  })

  it('only closes a stream that never opened', async () => {
    const { log, options: o } = options(ok, { sessionId: '' })
    expect(await endSession(o)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close'])
  })
})

describe('endSession for a user who did not open the session', () => {
  const others: [string, AccessToken, RegExp][] = [
    ['another user', token('mallory', 'local', now + 600_000), /^You are signed in as mallory \(password\), but demo \(password\) opened this session, so it was not ended for everyone\. Sign in as demo \(password\) to end it\.$/],
    ['the same name signed in elsewhere', token('demo', 'oidc', now + 600_000), /^You are signed in as demo \(single sign-on\), but demo \(password\) opened/],
    ['a token it cannot read', { token: 'eyJ.opaque', expires: now + 600_000 }, /^You are signed in as someone else, but demo \(password\) opened/],
  ]

  it('keeps the stream open, and says whose the session is, when occamyd does not find it', async () => {
    for (const [what, t, message] of others) {
      const { log, options: o } = options(error(404, 'not_found'), { token: t })
      const got = await endSession(o)
      expect(got.kind, what).toBe('not-owner')
      expect(got.kind === 'not-owner' && got.message, what).toMatch(message)
      expect(log, what).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    }
  })

  it('ends a session that occamyd takes for theirs, closing the stream after', async () => {
    for (const [what, t] of others) {
      const { log, options: o } = options(ok, { token: t })
      expect(await endSession(o), what).toEqual({ kind: 'ended' })
      expect(log, what).toEqual(['DELETE /api/v1/sessions/mZ3k', 'close'])
    }
  })

  it('leaves the stream open when occamyd no longer takes the token, or fails', async () => {
    const { log, options: o } = options(error(401, 'invalid_token'), { token: others[0][1] })
    expect(await endSession(o)).toMatchObject({ kind: 'sign-in', closed: false })
    expect(log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    const failed = options(error(500, 'internal'), { token: others[0][1] })
    expect(await endSession(failed.options)).toMatchObject({ kind: 'failed' })
    expect(failed.log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
  })

  it('takes nobody for the owner of a session view without one', async () => {
    const { log, options: o } = options(error(404, 'not_found'), { owner: null })
    const got = await endSession(o)
    expect(got.kind === 'not-owner' && got.message).toMatch(/^You are signed in as demo \(password\), whom occamyd does not take for this session's owner/)
    expect(log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
  })
})
