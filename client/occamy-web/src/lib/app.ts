// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell's screens, and what waits for the user to sign in again, as
// a reducer that App runs. The access token lasts minutes and occamyd
// issues no refresh token, so an owner's action can find it expired:
// the sign-in form then opens over the session, which streams on, and
// the action resumes once the user has signed in. A sign-in through
// OIDC leaves the page, so what waits is kept in sessionStorage next to
// the PKCE verifier, and the page joins the session again on its
// return, within the owner's minute of grace. Whoever signs in, the
// action is taken, and occamyd decides whether the session is theirs.
// The owner's session on screen is kept in sessionStorage too, so that
// a reload, which signs in again, joins it again after.

import type { AccessToken, Identity, ShareRole } from './api'
import type { SessionTarget, Start, Target } from './targets'
import { MAX_LABEL, clip } from './text'

/** What a session view does for its owner once the user has signed in again. */
export type SessionAction =
  /** End the session for everyone. */
  | { kind: 'end' }
  /** Create a share link. */
  | { kind: 'share'; role: ShareRole; expiresIn: number }

/** What waits for the user to sign in again. */
export interface Pending {
  /** The stream to open once signed in: a connection, or the user's session again. */
  target: Target
  /** The session view the user signs in over, which carries on in place of opening target; null for none. */
  view: number | null
  /** What that session view, or the one opened, does next. */
  then: SessionAction | null
  /** Who opened the session, as the session view knew; null for none. A view opened for then takes it for the owner until its stream says. */
  owner: Identity | null
}

export type Screen =
  | { kind: 'start' }
  | { kind: 'connections' }
  /** A session view, numbered; owner is who opened the session, as far as the page knows before the stream says. */
  | { kind: 'session'; target: Target; view: number; owner: Identity | null }

export interface SignInState {
  /** Why the user signs in, such as an expired sign-in. */
  message: string
  /** What signing in leads to, which is no error, such as the session a reload left; empty for none. */
  notice: string
  /** An OIDC sign-in coming back: the exchange of its one-time code. */
  exchange: Promise<AccessToken> | null
  pending: Pending | null
}

export interface AppState {
  token: AccessToken | null
  screen: Screen
  /** The sign-in form: over the session view, if one is on screen, or else on its own; null for none. */
  signIn: SignInState | null
  /** The action the session view numbered view takes, numbered seq so that it is taken once. */
  action: { view: number; seq: number; action: SessionAction } | null
  /** The last session view's number. */
  views: number
  /** The last action's number. */
  seq: number
}

export type AppEvent =
  | { type: 'signed-in'; token: AccessToken }
  /** Open a stream, as from the connection list or Join again, at now in milliseconds since the epoch. */
  | { type: 'open'; target: Target; now: number }
  /** The session view numbered view needs a new access token for pending. */
  | { type: 'sign-in-again'; view: number; message: string; pending: Pending }
  | { type: 'cancel-sign-in' }
  /** The session view numbered view is done. */
  | { type: 'leave'; view: number }
  | { type: 'sign-out'; message: string }

export const EXPIRED = 'Your sign-in has expired. Sign in again.'

/** signInOver tells whether the sign-in form is a dialog over the session view, rather than a page of its own. */
export function signInOver(s: AppState): boolean {
  return s.signIn !== null && s.screen.kind === 'session'
}

/** initialState is the state the page starts with. */
export function initialState(start: Start): AppState {
  const base: AppState = { token: null, screen: { kind: 'start' }, signIn: null, action: null, views: 0, seq: 0 }
  switch (start.kind) {
    case 'join':
      return { ...base, screen: { kind: 'session', target: { kind: 'share', sessionId: start.sessionId, token: start.token }, view: 1, owner: null }, views: 1 }
    case 'oidc':
      return { ...base, signIn: { message: '', notice: '', exchange: start.token, pending: start.resume } }
    case 'oidc-error':
      return { ...base, signIn: { message: start.message, notice: '', exchange: null, pending: null } }
    case 'return':
      return { ...base, signIn: { message: '', notice: `Sign in again to return to ${start.target.name}.`, exchange: null, pending: { target: start.target, view: null, then: null, owner: null } } }
    default:
      return { ...base, signIn: { message: '', notice: '', exchange: null, pending: null } }
  }
}

function signedIn(s: AppState, token: AccessToken): AppState {
  const pending = s.signIn?.pending ?? null
  const base: AppState = { ...s, token, signIn: null }
  if (pending === null) {
    return s.screen.kind === 'session' ? base : { ...base, screen: { kind: 'connections' } }
  }
  let view = pending.view
  if (view === null || s.screen.kind !== 'session' || s.screen.view !== view) {
    view = s.views + 1
    base.views = view
    base.screen = { kind: 'session', target: pending.target, view, owner: pending.owner }
    base.action = null
  }
  if (pending.then !== null) {
    // Whoever signed in, occamyd decides whether the session is theirs.
    base.seq = s.seq + 1
    base.action = { view, seq: base.seq, action: pending.then }
  }
  return base
}

/** reduce returns the state after event. */
export function reduce(s: AppState, e: AppEvent): AppState {
  switch (e.type) {
    case 'signed-in':
      return signedIn(s, e.token)
    case 'open': {
      if (e.target.kind !== 'share' && (s.token === null || s.token.expires <= e.now)) {
        // Over a session view, the form opens as a dialog, and the view
        // stays until the user has signed in.
        return { ...s, signIn: { message: s.token === null ? '' : EXPIRED, notice: '', exchange: null, pending: { target: e.target, view: null, then: null, owner: null } } }
      }
      const view = s.views + 1
      return { ...s, views: view, screen: { kind: 'session', target: e.target, view, owner: null }, signIn: null, action: null }
    }
    case 'sign-in-again':
      if (s.screen.kind !== 'session' || s.screen.view !== e.view) {
        return s
      }
      return { ...s, signIn: { message: e.message, notice: '', exchange: null, pending: e.pending } }
    case 'cancel-sign-in':
      return signInOver(s) ? { ...s, signIn: null } : s
    case 'leave':
      // A view that is gone, such as one whose End came back after the
      // user moved on, leaves nothing.
      if (s.screen.kind !== 'session' || s.screen.view !== e.view) {
        return s
      }
      if (s.token === null) {
        return { ...s, screen: { kind: 'start' }, signIn: { message: '', notice: '', exchange: null, pending: null }, action: null }
      }
      return { ...s, screen: { kind: 'connections' }, signIn: null, action: null }
    case 'sign-out':
      return { ...s, token: null, screen: { kind: 'start' }, signIn: { message: e.message, notice: '', exchange: null, pending: null }, action: null }
  }
}

/** Where a pending sign-in waits while the browser visits the identity provider. */
export const RESUME_KEY = 'occamy.oidc.resume'

/**
 * saveResume keeps pending for the page's return from an OIDC sign-in:
 * its target, action and owner, since the session view will be gone. It
 * returns false where storage is refused.
 */
export function saveResume(storage: Storage | undefined, pending: Pending | null): boolean {
  try {
    if (pending === null) {
      storage?.removeItem(RESUME_KEY)
    } else {
      storage?.setItem(RESUME_KEY, JSON.stringify({ target: pending.target, then: pending.then, owner: pending.owner }))
    }
    return storage !== undefined
  } catch {
    return false
  }
}

/** Where the owner's session on screen is kept, for a reload of the page to join it again. */
export const SESSION_KEY = 'occamy.session'

/**
 * keepSession keeps target, the session of the owner's view on screen,
 * in the tab's storage, so that a reload of the page, after which the
 * user signs in again, joins it again: occamyd keeps it for its owner's
 * minute of grace. A share is not kept: its token must not outlive the
 * page.
 */
export function keepSession(storage: Storage | undefined, target: SessionTarget): void {
  try {
    storage?.setItem(SESSION_KEY, JSON.stringify({ kind: target.kind, sessionId: target.sessionId, name: target.name }))
  } catch {
    // Without storage, a reload returns to the connection list.
  }
}

/** dropSession forgets the kept session, whichever it is, as on signing out. */
export function dropSession(storage: Storage | undefined): void {
  try {
    storage?.removeItem(SESSION_KEY)
  } catch {
    // Nothing kept, nothing to forget.
  }
}

/** forgetSession forgets the kept session if it is sessionId: it has ended, occamyd refused to join it, or the user left it. */
export function forgetSession(storage: Storage | undefined, sessionId: string): void {
  try {
    if (sessionId !== '' && keptSession(storage)?.sessionId === sessionId) {
      storage?.removeItem(SESSION_KEY)
    }
  } catch {
    // Nothing kept, nothing to forget.
  }
}

/** keptSession returns the session keepSession kept, which stays kept, or null. */
export function keptSession(storage: Storage | undefined): SessionTarget | null {
  try {
    const raw = storage?.getItem(SESSION_KEY) ?? null
    const target = raw === null ? null : targetOf(JSON.parse(raw))
    return target?.kind === 'session' ? target : null
  } catch {
    return null
  }
}

/** takeResume returns what saveResume kept, or null, and forgets it. */
export function takeResume(storage: Storage | undefined): Pending | null {
  let raw: string | null
  try {
    raw = storage?.getItem(RESUME_KEY) ?? null
    storage?.removeItem(RESUME_KEY)
  } catch {
    return null
  }
  if (raw === null) {
    return null
  }
  try {
    const v: unknown = JSON.parse(raw)
    if (!isObject(v)) {
      return null
    }
    const target = targetOf(v.target)
    const then = v.then === null ? null : actionOf(v.then)
    const owner = v.owner === null ? null : identityFrom(v.owner)
    if (target === null || then === undefined || owner === undefined) {
      return null
    }
    return { target, view: null, then, owner }
  } catch {
    return null
  }
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

// SESSION_ID is what occamyd's session IDs are, unpadded base64url
// (internal/session/registry.go), bounded: a kept one joins a path under
// /api/v1/sessions/, which no other text may name.
const SESSION_ID = /^[A-Za-z0-9_-]{1,64}$/

// targetOf reads a kept target: a connection, or the user's session,
// with its name clipped as the page shows names. A share never waits for
// a sign-in.
function targetOf(v: unknown): Target | null {
  if (!isObject(v) || typeof v.name !== 'string') {
    return null
  }
  const name = clip(v.name, MAX_LABEL)
  if (v.kind === 'connection' && typeof v.id === 'string' && v.id !== '') {
    return { kind: 'connection', id: v.id, name }
  }
  if (v.kind === 'session' && typeof v.sessionId === 'string' && SESSION_ID.test(v.sessionId)) {
    return { kind: 'session', sessionId: v.sessionId, name }
  }
  return null
}

// identityFrom reads a kept identity, or returns undefined for none.
function identityFrom(v: unknown): Identity | undefined {
  if (!isObject(v) || typeof v.subject !== 'string' || v.subject === '' || typeof v.idp !== 'string' || v.idp === '') {
    return undefined
  }
  return { subject: v.subject, idp: v.idp }
}

// actionOf reads a kept action, or returns undefined for one it does not know.
function actionOf(v: unknown): SessionAction | undefined {
  if (!isObject(v)) {
    return undefined
  }
  if (v.kind === 'end') {
    return { kind: 'end' }
  }
  if (v.kind === 'share' && (v.role === 'viewer' || v.role === 'read-only') && typeof v.expiresIn === 'number' && v.expiresIn >= 60 && v.expiresIn <= 43200) {
    return { kind: 'share', role: v.role, expiresIn: v.expiresIn }
  }
  return undefined
}
