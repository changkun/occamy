// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// What a session view streams (docs/api.md, Streams), and how the page
// starts: signed out, back from an OIDC sign-in, or from a share link.

import type { AccessToken, Api } from './api'
import { type Pending, takeResume } from './app'
import type { Fragment } from './fragment'
import { newVerifier, oidcErrorMessage, takeVerifier } from './oidc'

/** A stream a session view opens. */
export type Target =
  /** Open a session on a connection and stream it as its owner. */
  | { kind: 'connection'; id: string; name: string }
  /** Join a running session as its owner again, with the access token. */
  | { kind: 'session'; sessionId: string; name: string }
  /** Join a running session with a share. */
  | { kind: 'share'; sessionId: string; token: string }

/** streamURL is the WebSocket URL of target's stream route on the page's origin. */
export function streamURL(origin: string, target: Target): string {
  const base = origin.replace(/^http/, 'ws')
  if (target.kind === 'connection') {
    return `${base}/api/v1/connections/${encodeURIComponent(target.id)}/stream`
  }
  return `${base}/api/v1/sessions/${encodeURIComponent(target.sessionId)}/stream`
}

/** streamToken is the token target's stream takes: the share, or else the access token. */
export function streamToken(target: Target, access: AccessToken | null): string {
  return target.kind === 'share' ? target.token : (access?.token ?? '')
}

/** What the page starts with. */
export type Start =
  | { kind: 'signed-out' }
  | { kind: 'join'; sessionId: string; token: string }
  /** Back from OIDC: the exchange of the one-time code, under way, and what waited for it. */
  | { kind: 'oidc'; token: Promise<AccessToken>; resume: Pending | null }
  | { kind: 'oidc-error'; message: string }

/**
 * start decides what the page starts with from its fragment, once, before
 * it renders, so that the one-time code is exchanged and the verifier
 * spent exactly once. A code without a verifier in this tab was not this
 * tab's sign-in: someone may have sent the browser there with a
 * challenge of their own, so the code is spent with a throwaway
 * verifier and nobody is signed in. The verifier, and what waited for
 * the sign-in, are forgotten on every start: a sign-in that failed or
 * was abandoned leaves nothing for a later one to find.
 */
export function start(fragment: Fragment, api: Api, storage: Storage | undefined): Start {
  const verifier = takeVerifier(storage)
  const resume = takeResume(storage)
  switch (fragment.kind) {
    case 'join':
      return { kind: 'join', sessionId: fragment.sessionId, token: fragment.token }
    case 'oidc-error':
      return { kind: 'oidc-error', message: oidcErrorMessage(fragment.error) }
    case 'oidc-code':
      if (verifier === null) {
        api.oidcToken(fragment.code, newVerifier()).catch(() => undefined)
        return { kind: 'oidc-error', message: 'That sign-in was not started in this tab, so it was not used. Sign in here.' }
      }
      return { kind: 'oidc', token: api.oidcToken(fragment.code, verifier), resume }
    default:
      return { kind: 'signed-out' }
  }
}
