// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { Role } from '../protocol'
import { type AccessToken, Api, type Identity } from './api'
import { type EndOptions, endSession, sessionOwner } from './owner'

// token returns an access token of occamyd's for sub, signed in at idp,
// lasting until expires.
function token(sub: string, idp: string, expires: number): AccessToken {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return { token: `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp }))}.c2ln`, expires }
}

const now = 1_000_000
const fresh = token('demo', 'local', now + 600_000)
const demo: Identity = { subject: 'demo', idp: 'local' }

type Answer = (signal: AbortSignal, init?: RequestInit) => Promise<Response>

// options returns endSession's options for the session demo opened and
// pressed End on, with an Api whose DELETE answers with answer, recording
// the order of the stream's close and the call.
function options(answer: Answer, o: Partial<EndOptions> = {}, timeout?: number): { log: string[]; options: EndOptions } {
  const log: string[] = []
  const api = new Api(
    (path, init) => {
      log.push(`${init?.method} ${path}`)
      return answer(init?.signal as AbortSignal, init)
    },
    () => now,
    timeout,
  )
  return {
    log,
    options: { api, token: fresh, owner: demo, resumed: false, sessionId: 'mZ3k', now, close: () => Promise.resolve(void log.push('close')), endedForEveryone: () => false, ...o },
  }
}

const ok = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))
const error = (status: number, code: string) => (): Promise<Response> => Promise.resolve(new Response(JSON.stringify({ error: { code, message: code } }), { status }))
const hung = (signal: AbortSignal): Promise<Response> => new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason as Error)))

// occamyd answers DELETE as occamyd does for a session opened by owner:
// it ends the session for a token whose sub is owner's, signed in with a
// password or, linked to that user, through single sign-on, and answers
// 404 for anyone else.
function occamyd(owner: string): Answer {
  return (_, init) => {
    const auth = (init?.headers as Record<string, string> | undefined)?.Authorization ?? ''
    const payload = auth.replace(/^Bearer /, '').split('.')[1] ?? ''
    const claims = JSON.parse(Buffer.from(payload, 'base64url').toString()) as { sub?: string }
    return claims.sub === owner ? ok() : error(404, 'not_found')()
  }
}

describe('endSession for the user who opened the session', () => {
  it('closes the owner’s stream, then ends the session for everyone', async () => {
    const { log, options: o } = options(ok)
    expect(await endSession(o)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('takes the same name signed in through single sign-on for the same user, as occamyd does', async () => {
    const { log, options: o } = options(error(404, 'not_found'), { token: token('demo', 'oidc', now + 600_000) })
    expect(await endSession(o)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('sends nothing and closes nothing with an expired token, so that the owner signs in with the session on screen', async () => {
    for (const resumed of [false, true]) {
      for (const t of [token('demo', 'local', now), null]) {
        const { log, options: o } = options(ok, { token: t, resumed })
        expect(await endSession(o)).toMatchObject({ kind: 'sign-in', closed: false })
        expect(log).toEqual([])
      }
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
    for (const resumed of [false, true]) {
      const { log, options: o } = options(ok, { sessionId: '', resumed })
      expect(await endSession(o)).toEqual({ kind: 'ended' })
      expect(log).toEqual(['close'])
    }
  })
})

describe('endSession that occamyd decides on', () => {
  const mallory = token('mallory', 'local', now + 600_000)
  // What End is taken for, as the token's user and whether it resumed,
  // and what the user is told when occamyd does not find the session.
  const cases: [string, Partial<EndOptions>, RegExp][] = [
    ['another user', { token: mallory }, /^You are signed in as mallory, but demo opened this session, so it was not ended for everyone\. Sign in as demo to end it\.$/],
    ['a token it cannot read', { token: { token: 'eyJ.opaque', expires: now + 600_000 } }, /^occamyd has no session of yours to end: it has ended, or someone else opened it\.$/],
    ['an End that resumed', { resumed: true }, /^occamyd has no session of yours to end, signed in as demo: it has ended, or someone else opened it\.$/],
    ['a session view that did not know its owner', { owner: null }, /^occamyd has no session of yours to end, signed in as demo:/],
  ]

  it('keeps the stream open, and says whose the session is, when occamyd does not find it', async () => {
    for (const [what, o, message] of cases) {
      const { log, options: opts } = options(error(404, 'not_found'), o)
      const got = await endSession(opts)
      expect(got.kind, what).toBe('not-owner')
      expect(got.kind === 'not-owner' && got.message, what).toMatch(message)
      expect(log, what).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    }
  })

  it('ends a session that occamyd takes for theirs, closing the stream after', async () => {
    for (const [what, o] of cases) {
      const { log, options: opts } = options(ok, o)
      expect(await endSession(opts), what).toEqual({ kind: 'ended' })
      expect(log, what).toEqual(['DELETE /api/v1/sessions/mZ3k', 'close'])
    }
  })

  it('leaves the stream open when occamyd no longer takes the token, or fails, and says the session runs on', async () => {
    const { log, options: o } = options(error(401, 'invalid_token'), { token: mallory })
    expect(await endSession(o)).toMatchObject({ kind: 'sign-in', closed: false })
    expect(log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    for (const [answer, timeout] of [
      [error(500, 'internal'), undefined],
      [hung, 20],
    ] as const) {
      const failed = options(answer, { resumed: true }, timeout)
      const got = await endSession(failed.options)
      expect(got.kind).toBe('failed')
      expect(got.kind === 'failed' && got.message).toMatch(/^The session was not ended for everyone: .* It runs on, with its viewers\.$/)
      expect(failed.log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    }
  })

  it('takes the stream’s 4002 for the session ended, ahead of a 404', async () => {
    // Ended already, as by an End whose answer did not come in time: End
    // only closes the stream.
    const before = options(error(404, 'not_found'), { resumed: true, endedForEveryone: () => true })
    expect(await endSession(before.options)).toEqual({ kind: 'ended' })
    expect(before.log).toEqual(['close'])
    // Ended meanwhile, as when another End came first.
    for (const answer of [error(404, 'not_found'), error(500, 'internal'), hung]) {
      let closed = false
      const meanwhile = options(
        (signal) => {
          closed = true
          return answer(signal)
        },
        { resumed: true, endedForEveryone: () => closed },
        20,
      )
      expect(await endSession(meanwhile.options)).toEqual({ kind: 'ended' })
      expect(meanwhile.log).toEqual(['DELETE /api/v1/sessions/mZ3k', 'close'])
    }
  })
})

describe('an End that resumed after a sign-in', () => {
  const alice: Identity = { subject: 'alice', idp: 'local' }

  it('ends alice’s session once alice signs in, after bob was told it is not his', async () => {
    const asBob = options(occamyd('alice'), { owner: alice, token: token('bob', 'oidc', now + 600_000), resumed: true })
    const told = await endSession(asBob.options)
    expect(told).toEqual({ kind: 'not-owner', message: 'You are signed in as bob, but alice opened this session, so it was not ended for everyone. Sign in as alice to end it.' })
    expect(asBob.log).toEqual(['DELETE /api/v1/sessions/mZ3k'])
    const asAlice = options(occamyd('alice'), { owner: alice, token: token('alice', 'local', now + 600_000), resumed: true })
    expect(await endSession(asAlice.options)).toEqual({ kind: 'ended' })
    expect(asAlice.log).toEqual(['DELETE /api/v1/sessions/mZ3k', 'close'])
  })

  it('ends the session of a user linked to their single sign-on', async () => {
    const { log, options: o } = options(occamyd('alice'), { owner: alice, token: token('alice', 'oidc', now + 600_000), resumed: true })
    expect(await endSession(o)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['DELETE /api/v1/sessions/mZ3k', 'close'])
  })
})

describe('sessionOwner', () => {
  const seed: Identity = { subject: 'alice', idp: 'local' }
  const bob = token('bob', 'oidc', now + 600_000)

  it('takes the user of the stream’s token once the stream is the owner’s', () => {
    expect(sessionOwner(Role.Owner, bob, seed)).toEqual({ subject: 'bob', idp: 'oidc' })
    expect(sessionOwner(Role.Owner, bob, null)).toEqual({ subject: 'bob', idp: 'oidc' })
  })

  it('takes the seed until then, and for another role or a token it cannot read', () => {
    for (const role of [null, Role.Viewer, Role.ReadOnly]) {
      expect(sessionOwner(role, bob, seed)).toBe(seed)
      expect(sessionOwner(role, bob, null)).toBeNull()
    }
    expect(sessionOwner(Role.Owner, null, seed)).toBe(seed)
    expect(sessionOwner(Role.Owner, { token: 'eyJ.opaque', expires: now + 600_000 }, seed)).toBe(seed)
  })
})
