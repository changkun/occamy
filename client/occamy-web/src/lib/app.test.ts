// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { type AppEvent, type AppState, EXPIRED, type Pending, RESUME_KEY, initialState, reduce, saveResume, signInOver, takeResume } from './app'
import type { Target } from './targets'

const now = 1_000_000
const fresh = { token: 'eyJ.fresh', expires: now + 600_000 }
const expired = { token: 'eyJ.old', expires: now - 1 }
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
    expect(s.signIn).toMatchObject({ message: EXPIRED, pending: { target: lab, view: null, then: null } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s).toMatchObject({ token: fresh, signIn: null, screen: { kind: 'session', target: lab } })
  })
})

describe('an owner’s action after the token expired', () => {
  it('keeps the session view on screen while the owner signs in to share it, then shares', () => {
    const before = onSession()
    const pending: Pending = { target: mine, view: 1, then: { kind: 'share', role: 'viewer', expiresIn: 3600 } }
    let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending })
    // The session view is not replaced: the stream runs on under the form.
    expect(s.screen).toBe(before.screen)
    expect(signInOver(s)).toBe(true)
    s = reduce(s, { type: 'signed-in', token: { ...fresh, token: 'eyJ.new' } })
    expect(s.screen).toBe(before.screen)
    expect(s).toMatchObject({ token: { token: 'eyJ.new' }, signIn: null, action: { view: 1, action: { kind: 'share', role: 'viewer', expiresIn: 3600 } } })
  })

  it('ends the session once the owner has signed in', () => {
    const before = onSession()
    let s = reduce(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' } } })
    s = reduce(s, { type: 'signed-in', token: fresh })
    expect(s.screen).toBe(before.screen)
    expect(s.action).toMatchObject({ view: 1, action: { kind: 'end' } })
    // A second End after another sign-in is another action.
    const seq = s.action?.seq
    s = run(s, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' } } }, { type: 'signed-in', token: fresh })
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
    const s = run(before, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: { kind: 'end' } } }, { type: 'cancel-sign-in' })
    expect(s).toEqual(before)
  })

  it('opens the session again with its action when the owner came back from single sign-on', async () => {
    const token = Promise.resolve(fresh)
    let s = initialState({ kind: 'oidc', token, resume: { target: mine, view: null, then: { kind: 'share', role: 'read-only', expiresIn: 600 } } })
    expect(s.signIn).toMatchObject({ exchange: token })
    s = reduce(s, { type: 'signed-in', token: await token })
    expect(s.screen).toEqual({ kind: 'session', target: mine, view: 1 })
    expect(s.action).toMatchObject({ view: 1, action: { kind: 'share', role: 'read-only', expiresIn: 600 } })
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
    expect(reduce(s, { type: 'sign-in-again', view: 1, message: EXPIRED, pending: { target: mine, view: 1, then: null } })).toBe(s)
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

  it('keeps the target and the action across the page’s visit to the identity provider, once', () => {
    const s = storage()
    expect(saveResume(s, { target: mine, view: 3, then: { kind: 'share', role: 'viewer', expiresIn: 3600 } })).toBe(true)
    expect(takeResume(s)).toEqual({ target: mine, view: null, then: { kind: 'share', role: 'viewer', expiresIn: 3600 } })
    expect(takeResume(s)).toBeNull()
    expect(saveResume(undefined, null)).toBe(false)
  })

  it('takes nothing it does not know', () => {
    const s = storage()
    for (const raw of [
      'not json',
      '[]',
      '{"target":{"kind":"share","sessionId":"s","token":"t"},"then":null}',
      '{"target":{"kind":"session","sessionId":"","name":"x"},"then":null}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"delete"}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"share","role":"owner","expiresIn":600}}',
      '{"target":{"kind":"session","sessionId":"s","name":"x"},"then":{"kind":"share","role":"viewer","expiresIn":1}}',
    ]) {
      s.setItem(RESUME_KEY, raw)
      expect(takeResume(s)).toBeNull()
      expect(s.getItem(RESUME_KEY)).toBeNull()
    }
  })
})
