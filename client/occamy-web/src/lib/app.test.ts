// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import type { AccessToken, Identity } from './api'
import { type AppEvent, type AppState, EXPIRED, type Pending, RESUME_KEY, SESSION_KEY, dropSession, forgetSession, initialState, keepSession, keptSession, reduce, saveResume, signInOver, takeResume } from './app'
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
    expect(s.signIn).toEqual({ message: '', notice: '', exchange: null, pending: null })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, screen: { kind: 'connections' }, signIn: null })
    s = reduce(s, { type: 'open', target: lab, now })
    expect(s.screen).toEqual({ kind: 'session', target: lab, view: 1, owner: null })
  })

  it('signs in again after a reload over the owner’s session, and joins it again', () => {
    let s = initialState({ kind: 'return', target: { kind: 'session', sessionId: 'mZ3k', name: 'Lab' } })
    expect(s.screen).toEqual({ kind: 'start' })
    expect(signInOver(s)).toBe(false)
    // It says where signing in leads, which is no error.
    expect(s.signIn).toEqual({ message: '', notice: 'Sign in again to return to Lab.', exchange: null, pending: { target: mine, view: null, then: null, owner: null } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, signIn: null, action: null, screen: { kind: 'session', target: mine, view: 1, owner: null } })
  })

  it('signs in for the connection list instead, once the user turns from the session a reload left', () => {
    let s = initialState({ kind: 'return', target: { kind: 'session', sessionId: 'mZ3k', name: 'Lab' } })
    s = reduce(s, { type: 'skip-return' })
    expect(s.signIn).toEqual({ message: '', notice: '', exchange: null, pending: null })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, signIn: null, screen: { kind: 'connections' } })
    // There is no return to turn from once signed in, nor over a session
    // view whose owner signs in again.
    expect(reduce(s, { type: 'skip-return' })).toBe(s)
    const over = reduce(onSession(expired), { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: lab, view: 1, then: { kind: 'end' }, owner: demo } })
    expect(signInOver(over)).toBe(true)
    expect(reduce(over, { type: 'skip-return' })).toBe(over)
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
    expect(s.screen).toEqual({ kind: 'session', target: mine, view: 1, owner: demo })
    expect(s.action).toMatchObject({ view: 1, action: { kind: 'share', role: 'read-only', expiresIn: 600 } })
  })
})

describe('an owner’s action when someone else signs in', () => {
  const end = { kind: 'end' } as const
  const share = { kind: 'share', role: 'viewer', expiresIn: 3600 } as const

  it('takes the action all the same, for occamyd to decide whose the session is', () => {
    for (const then of [end, share]) {
      const before = onSession()
      let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then, owner: demo } })
      s = reduce(s, { type: 'signed-in', token: token('mallory', 'local', now + 600_000) })
      // The session view stays, with its stream, and takes the action.
      expect(s.screen).toBe(before.screen)
      expect(s.action).toMatchObject({ view: 1, action: then })
    }
  })

  it('opens a session view gone through single sign-on for the owner it was for', async () => {
    const exchange = Promise.resolve(token('mallory', 'oidc', now + 600_000))
    let s = initialState({ kind: 'oidc', token: exchange, resume: { target: mine, view: null, then: end, owner: demo } })
    s = reduce(s, { type: 'signed-in', token: await exchange })
    // The view takes demo for the owner until its stream says.
    expect(s.screen).toEqual({ kind: 'session', target: mine, view: 1, owner: demo })
    expect(s.action?.action).toEqual(end)
  })

  it('takes an action whose owner the session view did not know', () => {
    let s = reduce(onSession(), { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: end, owner: null } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s.action?.action).toEqual(end)
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
    // An action whose owner the session view did not know is kept too.
    expect(saveResume(s, { target: mine, view: 1, then: { kind: 'end' }, owner: null })).toBe(true)
    expect(takeResume(s)).toEqual({ target: mine, view: null, then: { kind: 'end' }, owner: null })
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
      // Kept before the owner was.
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"},"owner":{"subject":"","idp":"local"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"end"},"owner":{"subject":"a"}}',
    ]) {
      s.setItem(RESUME_KEY, raw)
      expect(takeResume(s)).toBeNull()
      expect(s.getItem(RESUME_KEY)).toBeNull()
    }
  })
})

describe('keepSession, keptSession and forgetSession', () => {
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

  it('keeps the owner’s session for every load, until it is forgotten', () => {
    const s = storage()
    expect(keptSession(s)).toBeNull()
    keepSession(s, { kind: 'session', sessionId: 'mZ3k', name: 'Lab' })
    expect(keptSession(s)).toEqual(mine)
    expect(keptSession(s)).toEqual(mine)
    // Another session's view forgets nothing of this one.
    forgetSession(s, 'other')
    expect(keptSession(s)).toEqual(mine)
    forgetSession(s, 'mZ3k')
    expect(keptSession(s)).toBeNull()
    expect(s.getItem(SESSION_KEY)).toBeNull()
    // Without storage, or with storage that throws, nothing is kept.
    keepSession(undefined, { kind: 'session', sessionId: 'mZ3k', name: 'Lab' })
    expect(keptSession(undefined)).toBeNull()
    const refusing = { ...s, getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
    keepSession(refusing, { kind: 'session', sessionId: 'mZ3k', name: 'Lab' })
    expect(keptSession(refusing)).toBeNull()
    forgetSession(refusing, 'mZ3k')
  })

  it('forgets whichever session is kept, as on signing out', () => {
    const s = storage()
    keepSession(s, { kind: 'session', sessionId: 'mZ3k', name: 'Lab' })
    dropSession(s)
    expect(keptSession(s)).toBeNull()
    expect(s.getItem(SESSION_KEY)).toBeNull()
    dropSession(undefined)
    dropSession({ ...s, removeItem: () => { throw new Error('denied') } })
  })

  it('takes a session ID only as occamyd makes them, and clips the name as the page shows names', () => {
    const s = storage()
    const longest = 'A'.repeat(64)
    s.setItem(SESSION_KEY, JSON.stringify({ kind: 'session', sessionId: `${longest.slice(2)}_-`, name: 'Lab' }))
    expect(keptSession(s)).toEqual({ kind: 'session', sessionId: `${longest.slice(2)}_-`, name: 'Lab' })
    s.setItem(SESSION_KEY, JSON.stringify({ kind: 'session', sessionId: 'mZ3k', name: `${'n'.repeat(300)}\u202e` }))
    expect(keptSession(s)).toEqual({ kind: 'session', sessionId: 'mZ3k', name: `${'n'.repeat(199)}…` })
    s.setItem(SESSION_KEY, JSON.stringify({ kind: 'session', sessionId: 'mZ3k', name: 'Lab\u202e' }))
    expect(keptSession(s)).toEqual({ kind: 'session', sessionId: 'mZ3k', name: 'Lab ' })
  })

  it('takes nothing it does not know', () => {
    const s = storage()
    for (const raw of [
      'not json',
      '[]',
      'null',
      '{"kind":"share","sessionId":"s","token":"t"}',
      '{"kind":"connection","id":"lab","name":"Lab"}',
      '{"kind":"session","sessionId":"","name":"x"}',
      '{"kind":"session","sessionId":"s"}',
      // Not a session ID of occamyd's, which would join another path.
      '{"kind":"session","sessionId":"../connections","name":"x"}',
      '{"kind":"session","sessionId":"mZ3k?x=1","name":"x"}',
      '{"kind":"session","sessionId":"mZ3k=","name":"x"}',
      `{"kind":"session","sessionId":"${'A'.repeat(65)}","name":"x"}`,
    ]) {
      s.setItem(SESSION_KEY, raw)
      expect(keptSession(s)).toBeNull()
    }
  })
})
