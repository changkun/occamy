// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The owner's End session: the owner's stream closes, and the session
// ends for everyone through API v1, so that its viewers are told at
// once rather than after the owner's minute of grace.

import { Role } from '../protocol'
import { type AccessToken, type Api, ApiError, type Identity, errorMessage, identityOf, sameIdentity } from './api'
import { MAX_LABEL, clip } from './text'

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
  /** Who opened the session, as sessionOwner says; null for none known. */
  owner: Identity | null
  /** An End that waited for a sign-in, which occamyd decides on with the stream open. */
  resumed: boolean
  /** The session's ID; empty for a stream that never opened, which has nothing to end. */
  sessionId: string
  /** Now, in milliseconds since the epoch. */
  now: number
  /** Closes the owner's own stream. */
  close: () => Promise<void>
  /** Whether occamyd closed the stream with 4002, as it does once the session is ended for everyone. */
  endedForEveryone: () => boolean
}

/**
 * sessionOwner is who opened the session a view shows: the user of the
 * token its stream was opened with, once occamyd's welcome says the
 * stream is the owner's. Until then, and for another role, it is seed,
 * the owner a view opened for an action that waited for a sign-in is
 * for, or null.
 */
export function sessionOwner(role: Role | null, token: AccessToken | null, seed: Identity | null): Identity | null {
  return role === Role.Owner && token !== null ? (identityOf(token) ?? seed) : seed
}

/**
 * endSession ends the owner's session for everyone. With an access token
 * that has expired it sends nothing and leaves the stream open, so that
 * the owner signs in again with the session still on screen.
 *
 * The user who opened the session, pressing End, has their stream closed
 * first, so that they are not told the session was closed, and then the
 * session ended; occamyd's 404 then means it had ended already.
 *
 * Otherwise, as for an End that waited for a sign-in, or another user
 * signed in since, occamyd decides: the session is ended with the stream
 * open, and the stream closed after. A 404 then means the session is not
 * this user's, unless occamyd closed the stream with 4002 meanwhile, as
 * after an earlier End whose answer was lost.
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
  const opener = !o.resumed && o.owner !== null && user !== null && sameIdentity(o.owner, user)
  if (opener) {
    await o.close()
  } else if (o.endedForEveryone()) {
    await o.close()
    return { kind: 'ended' }
  }
  try {
    await o.api.endSession(o.token, o.sessionId)
  } catch (err) {
    if (!opener && o.endedForEveryone()) {
      await o.close()
      return { kind: 'ended' }
    }
    if (err instanceof ApiError && err.code === 'not_found') {
      return opener ? { kind: 'ended' } : { kind: 'not-owner', message: notOwner(o.owner, user) }
    }
    if (err instanceof ApiError && err.signedOut) {
      // A stream closed already: the owner is told so, in case the
      // sign-in is cancelled.
      return { kind: 'sign-in', message: 'occamyd no longer takes your sign-in. Sign in again to end the session for everyone.', closed: opener }
    }
    return {
      kind: 'failed',
      message: opener
        ? `The session was not ended for everyone: ${errorMessage(err)} Its viewers stay until your minute of grace runs out.`
        : `The session was not ended for everyone: ${errorMessage(err)} It runs on, with its viewers.`,
    }
  }
  if (!opener) {
    await o.close()
  }
  return { kind: 'ended' }
}

// notOwner says why occamyd found no session to end for user, now signed
// in: owner, who opened it, is someone else, or the session has ended.
// Either is null for one not known.
function notOwner(owner: Identity | null, user: Identity | null): string {
  if (owner === null || user === null || sameIdentity(owner, user)) {
    const who = user === null ? '' : `, signed in as ${clip(user.subject, MAX_LABEL)}`
    return `occamyd has no session of yours to end${who}: it has ended, or someone else opened it.`
  }
  const name = clip(owner.subject, MAX_LABEL)
  return `You are signed in as ${clip(user.subject, MAX_LABEL)}, but ${name} opened this session, so it was not ended for everyone. Sign in as ${name} to end it.`
}
