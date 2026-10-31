// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import type { AccessToken, Identity } from './api'
import { type AppEvent, type AppState, EXPIRED, type Pending, RESUME_KEY, initialState, reduce, saveResume, signInOver, takeResume } from './app'
import type { Target } from './targets'

// token returns an access token of occamyd's for sub, signed in at idp,
// lasting until expires.
function token(sub: string, idp: string | undefined, expires: number): AccessToken {
  const b64 = (s: string): string => Buffer.from(s).toString('base64url')
  return { token: `${b64('{"alg":"EdDSA"}')}.${b64(JSON.stringify({ kind: 'access', sub, idp }))}.c2ln`, expires }
}

const now = 1_000_000
const fresh = token('demo', 'local', now + 600_000)
const expired = token('demo', 'local', now - 1)
const demo: Identity = { subject: 'demo', idp: 'local' }
const lab: Target = { kind: 'connection', id: 'lab', name: 'Lab' }
const mine: Target = { kind: 'session', sessionId: 'mZ3k', name: 'Lab' }

function run(s: AppState, ...events: AppEvent[]): AppState {
  return events.reduce(reduce, s)
}

// onSession returns the state of a user signed in with token, whose
// session view of lab is on screen.
function onSession(token = fresh): AppState {
  return run(initialState({ kind: 'signed-out' }), { type: 'signed-in', token }, { type: 'open', target: lab, now: token.expires - 1 })
}

describe('the shell’s screens', () => {
  it('signs in to the connection list, and opens a connection', () => {
    let s = initialState({ kind: 'signed-out' })
    expect(s.screen).toEqual({ kind: 'start' })
    expect(s.signIn).toEqual({ message: '', exchange: null, pending: null })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, screen: { kind: 'connections' }, signIn: null })
    s = reduce(s, { type: 'open', target: lab, now })
    expect(s.screen).toEqual({ kind: 'session', target: lab, view: 1 })
  })

  it('opens a share link without signing in', () => {
    const s = initialState({ kind: 'join', sessionId: 'mZ3k', token: 'q8Gf' })
    expect(s).toMatchObject({ token: null, signIn: null, screen: { kind: 'session', target: { kind: 'share', sessionId: 'mZ3k', token: 'q8Gf' } } })
  })

  it('sends a connection opened after the token expired to sign in, and opens it after', () => {
    let s = run(initialState({ kind: 'signed-out' }), { type: 'signed-in', token: expired })
    s = reduce(s, { type: 'open', target: lab, now })
    expect(s.screen).toEqual({ kind: 'connections' })
    expect(signInOver(s)).toBe(false)
    expect(s.signIn).toMatchObject({ message: EXPIRED, pending: { target: lab, view: null, then: null, owner: null } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, signIn: null, screen: { kind: 'session', target: lab } })
  })
})

describe('an owner’s action after the token expired', () => {
  it('keeps the session view on screen while the owner signs in to share it, then shares', () => {
    const before = onSession()
    const pending: Pending = { target: mine, view: 1, then: { kind: 'share', role: 'viewer', expiresIn: 3600 }, owner: demo }
    let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending })
    // The session view is not replaced: the stream runs on under the form.
    expect(s.screen).toBe(before.screen)
    expect(signInOver(s)).toBe(true)
    const again = token('demo', 'local', now + 900_000)
    s = reduce(s, { type: 'signed-in', token: again })
    expect(s.screen).toBe(before.screen)
    expect(s).toMatchObject({ token: again, signIn: null, action: { view: 1, action: { kind: 'share', role: 'viewer', expiresIn: 3600 } } })
  })

  it('ends the session once the owner has signed in', () => {
    const before = onSession()
    let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' }, owner: demo } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s.screen).toBe(before.screen)
    expect(s.action).toMatchObject({ view: 1, action: { kind: 'end' } })
    // A second End after another sign-in is another action.
    const seq = s.action?.seq
    s = run(s, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' }, owner: demo } }, { type: 'signed-in', token: fresh })
    expect(s.action?.seq).not.toBe(seq)
  })

  it('joins the session again, not the connection list, once the owner has signed in', () => {
    const before = onSession(expired)
    expect(before.screen).toMatchObject({ kind: 'session', view: 1 })
    let s = reduce(before, { type: 'open', target: mine, now })
    // The ended view stays under the form.
    expect(s.screen).toBe(before.screen)
    expect(signInOver(s)).toBe(true)
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ signIn: null, action: null, screen: { kind: 'session', target: mine, view: 2 } })
  })

  it('leaves the session view as it was when the owner cancels', () => {
    const before = onSession()
    const s = run(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' }, owner: demo } }, { type: 'cancel-sign-in' })
    expect(s).toEqual(before)
  })

  it('opens the session again with its action when the owner came back from single sign-on', async () => {
    const exchange = Promise.resolve(fresh)
    let s = initialState({ kind: 'oidc', token: exchange, resume: { target: mine, view: null, then: { kind: 'share', role: 'read-only', expiresIn: 600 }, owner: demo } })
    expect(s.signIn).toMatchObject({ exchange })
    s = reduce(s, { type: 'signed-in', token: await exchange })
    expect(s.screen).toEqual({ kind: 'session', target: mine, view: 1 })
    expect(s.action).toMatchObject({ view: 1, action: { kind: 'share', role: 'read-only', expiresIn: 600 } })
  })
})

describe('an owner’s action when someone else signs in', () => {
  const end = { kind: 'end' } as const
  const share = { kind: 'share', role: 'viewer', expiresIn: 3600 } as const

  it('drops the action, and says who signed in and whose the session is', () => {
    for (const [then, done] of [
      [end, /so it was not ended for everyone\. Sign in as demo \(password\) to end it\./],
      [share, /so no share link was made\. Sign in as demo \(password\) to share it\./],
    ] as const) {
      const before = onSession()
      let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then, owner: demo } })
      s = reduce(s, { type: 'signed-in', token: token('mallory', 'local', now + 600_000) })
      // The session view stays, with its stream, and is told why.
      expect(s.screen).toBe(before.screen)
      expect(s.action).toMatchObject({ view: 1, action: { kind: 'refused', then } })
      const message = s.action?.action.kind === 'refused' ? s.action.action.message : ''
      expect(message).toMatch(/^You are signed in as mallory \(password\), but demo \(password\) opened this session/)
      expect(message).toMatch(done)
    }
  })

  it('takes the same name signed in elsewhere for someone else, as occamyd may', () => {
    let s = reduce(onSession(), { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: end, owner: demo } })
    s = reduce(s, { type: 'signed-in', token: token('demo', 'oidc', now + 600_000) })
    expect(s.action?.action).toMatchObject({ kind: 'refused', then: end })
    expect(s.action?.action.kind === 'refused' && s.action.action.message).toMatch(/^You are signed in as demo \(single sign-on\), but demo \(password\)/)
  })

  it('takes a token without idp for a password’s, as occamyd does', () => {
    let s = reduce(onSession(), { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: end, owner: demo } })
    s = reduce(s, { type: 'signed-in', token: token('demo', undefined, now + 600_000) })
    expect(s.action?.action).toEqual(end)
  })

  it('drops the action of a session view gone through single sign-on', async () => {
    const exchange = Promise.resolve(token('mallory', 'oidc', now + 600_000))
    let s = initialState({ kind: 'oidc', token: exchange, resume: { target: mine, view: null, then: end, owner: demo } })
    s = reduce(s, { type: 'signed-in', token: await exchange })
    expect(s.screen).toEqual({ kind: 'session', target: mine, view: 1 })
    expect(s.action?.action).toMatchObject({ kind: 'refused', then: end })
  })

  it('drops an action without an owner', () => {
    let s = reduce(onSession(), { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: end, owner: null } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s.action?.action).toMatchObject({ kind: 'refused', then: end })
  })
})

describe('leaving a session view', () => {
  it('goes back to the connection list, or to sign-in without a token', () => {
    expect(reduce(onSession(), { type: 'leave', view: 1 }).screen).toEqual({ kind: 'connections' })
    const shared = initialState({ kind: 'join', sessionId: 'mZ3k', token: 'q8Gf' })
    expect(reduce(shared, { type: 'leave', view: 1 })).toMatchObject({ screen: { kind: 'start' }, signIn: { message: '' } })
  })

  it('leaves nothing once that view is gone, such as when its End comes back late', () => {
    let s = run(onSession(), { type: 'leave', view: 1 }, { type: 'open', target: mine, now })
    expect(s.screen).toMatchObject({ kind: 'session', view: 2 })
    s = reduce(s, { type: 'leave', view: 1 })
    expect(s.screen).toMatchObject({ kind: 'session', view: 2 })
    // Nor does a view that is gone ask for a sign-in.
    expect(reduce(s, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: null, owner: demo } })).toBe(s)
  })
})

describe('saveResume and takeResume', () => {
  function storage(): Storage {
    const items = new Map<string, string>()
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

  it('keeps the target, the action and its owner across the page’s visit to the identity provider, once', () => {
    const s = storage()
    expect(saveResume(s, { target: mine, view: 3, then: { kind: 'share', role: 'viewer', expiresIn: 3600 }, owner: demo })).toBe(true)
    expect(takeResume(s)).toEqual({ target: mine, view: null, then: { kind: 'share', role: 'viewer', expiresIn: 3600 }, owner: demo })
    expect(takeResume(s)).toBeNull()
    expect(saveResume(s, { target: lab, view: null, then: null, owner: null })).toBe(true)
    expect(takeResume(s)).toEqual({ target: lab, view: null, then: null, owner: null })
    expect(saveResume(undefined, null)).toBe(false)
  })

  it('takes nothing it does not know', () => {
    const s = storage()
    for (const raw of [
      'not json',
      '[]',
      '{"target":{"kind":"share","sessionId":"s","token":"t"},"then":null,"owner":null}',
      '{"target":{"kind":"session","sessionId":"","name":"x"},"then":null,"owner":null}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"delete"},"owner":{"subject":"a","idp":"local"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"share","role":"owner","expiresIn":600},"owner":{"subject":"a","idp":"local"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"share","role":"viewer","expiresIn":1},"owner":{"subject":"a","idp":"local"}}',
      // An action without its owner, as saved before the owner was kept.
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"},"owner":null}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"},"owner":{"subject":"","idp":"local"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"},"owner":{"subject":"a"}}',
    ]) {
      s.setItem(RESUME_KEY, raw)
      expect(takeResume(s)).toBeNull()
      expect(s.getItem(RESUME_KEY)).toBeNull()
    }
  })
})
