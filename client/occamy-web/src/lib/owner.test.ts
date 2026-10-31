// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { Api } from './api'
import { endSession } from './owner'

const now = 1_000_000
const fresh = { token: 'eyJ.fresh', expires: now + 600_000 }

// owner returns endSession's options with an Api whose DELETE answers
// with answer, recording the order of the stream's close and the call.
function owner(answer: () => Promise<Response>, token: { token: string; expires: number } | null = fresh, sessionId = 'mZ3k') {
  const log: string[] = []
  const api = new Api(
    (path, init) => {
      log.push(`${init?.method} ${path}`)
      return answer()
    },
    () => now,
  )
  return { log, options: { api, token, sessionId, now, close: () => Promise.resolve(void log.push('close')) } }
}

const ok = (): Promise<Response> => Promise.resolve(new Response(null, { status: 204 }))
const error = (status: number, code: string) => (): Promise<Response> => Promise.resolve(new Response(JSON.stringify({ error: { code, message: code } }), { status }))

describe('endSession', () => {
  it('closes the owner’s stream, then ends the session for everyone', async () => {
    const { log, options } = owner(ok)
    expect(await endSession(options)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('sends nothing and closes nothing with an expired token, so that the owner signs in with the session on screen', async () => {
    for (const token of [{ token: 'eyJ.old', expires: now }, null]) {
      const { log, options } = owner(ok, token)
      expect(await endSession(options)).toMatchObject({ kind: 'sign-in', closed: false })
      expect(log).toEqual([])
    }
  })

  it('asks the owner to sign in when occamyd no longer takes the token', async () => {
    const { log, options } = owner(error(401, 'invalid_token'))
    expect(await endSession(options)).toMatchObject({ kind: 'sign-in', closed: true })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('tells the owner when the session was not ended for everyone', async () => {
    for (const answer of [error(500, 'internal'), () => Promise.reject(new TypeError('offline'))]) {
      const { options } = owner(answer)
      const got = await endSession(options)
      expect(got.kind).toBe('failed')
      expect(got.kind === 'failed' && got.message).toMatch(/not ended for everyone.*minute of grace/)
    }
  })

  it('tells the owner when occamyd does not answer in time, rather than waiting on it', async () => {
    const log: string[] = []
    const api = new Api(
      (path, init) => {
        log.push(`${init?.method} ${path}`)
        const signal = init?.signal as AbortSignal
        return new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason as Error)))
      },
      () => now,
      20,
    )
    const got = await endSession({ api, token: fresh, sessionId: 'mZ3k', now, close: () => Promise.resolve(void log.push('close')) })
    expect(got).toEqual({ kind: 'failed', message: 'The session was not ended for everyone: occamyd did not answer in time. Its viewers stay until your minute of grace runs out.' })
    expect(log).toEqual(['close', 'DELETE /api/v1/sessions/mZ3k'])
  })

  it('takes a session that ended already as ended', async () => {
    const { options } = owner(error(404, 'not_found'))
    expect(await endSession(options)).toEqual({ kind: 'ended' })
  })

  it('only closes a stream that never opened', async () => {
    const { log, options } = owner(ok, fresh, '')
    expect(await endSession(options)).toEqual({ kind: 'ended' })
    expect(log).toEqual(['close'])
  })
})
