// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The owner's End session: the owner's stream closes, and the session
// ends for everyone through API v1, so that its viewers are told at
// once rather than after the owner's minute of grace.

import { type AccessToken, type Api, ApiError, type Identity, describeIdentity, errorMessage, identityOf, sameIdentity } from './api'
import type { SessionAction } from './app'

/** How an End went. */
export type EndOutcome =
  /** The session ended for everyone, or had ended already. */
  | { kind: 'ended' }
  /** The access token is no longer good: the user signs in again, and End resumes. */
  | { kind: 'sign-in'; message: string; closed: boolean }
  /** occamyd did not end it: the owner is told, and may try again. */
  | { kind: 'failed'; message: string }
  /** occamyd does not take the user now signed in for the session's owner: they may sign in as the owner. */
  | { kind: 'not-owner'; message: string }

export interface EndOptions {
  api: Api
  token: AccessToken | null
  /** Who opened the session view, the session's owner; null for none known. */
  owner: Identity | null
  /** The session's ID; empty for a stream that never opened, which has nothing to end. */
  sessionId: string
  /** Now, in milliseconds since the epoch. */
  now: number
  /** Closes the owner's own stream. */
  close: () => Promise<void>
}

/**
 * endSession ends the owner's session for everyone. With an access token
 * that has expired it sends nothing and leaves the stream open, so that
 * the owner signs in again with the session still on screen. With the
 * token of the user who opened the session, it closes the owner's stream
 * first, so that the owner is not told the session was closed, and then
 * ends the session; occamyd's 404 then means it had ended already. With
 * another user's token, signed in since, the stream stays open until
 * occamyd has ended the session, since its 404 may mean the session is
 * not this user's: occamyd takes some users for one owner, which the
 * page cannot tell. If the session does not end, the owner is told,
 * since the viewers stay until the owner's grace ends.
 */
export async function endSession(o: EndOptions): Promise<EndOutcome> {
  if (o.sessionId === '') {
    await o.close()
    return { kind: 'ended' }
  }
  if (o.token === null || o.token.expires <= o.now) {
    return { kind: 'sign-in', message: 'Your sign-in has expired. Sign in again to end the session for everyone.', closed: false }
  }
  const user = identityOf(o.token)
  const mine = o.owner !== null && user !== null && sameIdentity(o.owner, user)
  if (mine) {
    await o.close()
  }
  try {
    await o.api.endSession(o.token, o.sessionId)
  } catch (err) {
    if (err instanceof ApiError && err.code === 'not_found') {
      return mine ? { kind: 'ended' } : { kind: 'not-owner', message: notOwner({ kind: 'end' }, o.owner, user) }
    }
    if (err instanceof ApiError && err.signedOut) {
      // A stream closed already: the owner is told so, in case the
      // sign-in is cancelled.
      return { kind: 'sign-in', message: 'occamyd no longer takes your sign-in. Sign in again to end the session for everyone.', closed: mine }
    }
    return {
      kind: 'failed',
      message: `The session was not ended for everyone: ${errorMessage(err)} Its viewers stay until your minute of grace runs out.`,
    }
  }
  if (!mine) {
    await o.close()
  }
  return { kind: 'ended' }
}

/**
 * notOwner says that what the owner asked for was not done, since the
 * user now signed in, user, is not owner, who opened the session; null
 * for one not known.
 */
export function notOwner(then: SessionAction, owner: Identity | null, user: Identity | null): string {
  const [done, todo] = then.kind === 'end' ? ['it was not ended for everyone', 'end it'] : ['no share link was made', 'share it']
  const who = user === null ? 'someone else' : describeIdentity(user)
  if (owner === null) {
    return `You are signed in as ${who}, whom occamyd does not take for this session's owner, so ${done}.`
  }
  const opener = describeIdentity(owner)
  return `You are signed in as ${who}, but ${opener} opened this session, so ${done}. Sign in as ${opener} to ${todo}.`
}
