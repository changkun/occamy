// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The owner's End session: the owner's stream closes, and the session
// ends for everyone through API v1, so that its viewers are told at
// once rather than after the owner's minute of grace.

import { type AccessToken, type Api, ApiError, errorMessage } from './api'

/** How an End went. */
export type EndOutcome =
  /** The session ended for everyone, or had ended already. */
  | { kind: 'ended' }
  /** The access token is no longer good: the user signs in again, and End resumes. */
  | { kind: 'sign-in'; message: string; closed: boolean }
  /** occamyd did not end it: the owner is told, and may try again. */
  | { kind: 'failed'; message: string }

export interface EndOptions {
  api: Api
  token: AccessToken | null
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
 * the owner signs in again with the session still on screen; otherwise
 * it closes the owner's stream first, so that the owner is not told the
 * session was closed, and then ends the session. If that fails, the
 * owner is told, since the viewers stay until the owner's grace ends.
 */
export async function endSession(o: EndOptions): Promise<EndOutcome> {
  if (o.sessionId === '') {
    await o.close()
    return { kind: 'ended' }
  }
  if (o.token === null || o.token.expires <= o.now) {
    return { kind: 'sign-in', message: 'Your sign-in has expired. Sign in again to end the session for everyone.', closed: false }
  }
  await o.close()
  try {
    await o.api.endSession(o.token, o.sessionId)
  } catch (err) {
    if (err instanceof ApiError && err.code === 'not_found') {
      return { kind: 'ended' }
    }
    if (err instanceof ApiError && err.signedOut) {
      // The stream is closed already: the owner is told so, in case
      // the sign-in is cancelled.
      return { kind: 'sign-in', message: 'occamyd no longer takes your sign-in. Sign in again to end the session for everyone.', closed: true }
    }
    return {
      kind: 'failed',
      message: `The session was not ended for everyone: ${errorMessage(err)} Its viewers stay until your minute of grace runs out.`,
    }
  }
  return { kind: 'ended' }
}
