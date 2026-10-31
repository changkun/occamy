// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The browser's side of an OIDC sign-in through occamyd
// (docs/configuration.md, Signing in through OIDC): a PKCE verifier kept
// in sessionStorage, its S256 challenge sent to
// /api/v1/auth/oidc/login, and the one-time code from the fragment
// exchanged with the verifier for an access token.

/** Where the verifier waits between the login redirect and the return. */
export const VERIFIER_KEY = 'occamy.oidc.verifier'

// PKCE's unreserved characters (RFC 7636, section 4.1).
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~'

/** The verifier's length, within PKCE's 43 to 128. */
export const VERIFIER_LENGTH = 64

/**
 * newVerifier returns a random verifier of VERIFIER_LENGTH characters of
 * PKCE's alphabet, each equally likely: bytes at or above the largest
 * multiple of the alphabet's size are drawn again.
 */
export function newVerifier(random: (b: Uint8Array<ArrayBuffer>) => void = (b) => crypto.getRandomValues(b)): string {
  const limit = 256 - (256 % ALPHABET.length)
  let out = ''
  const buf = new Uint8Array(new ArrayBuffer(VERIFIER_LENGTH * 2))
  while (out.length < VERIFIER_LENGTH) {
    random(buf)
    for (const b of buf) {
      if (b < limit && out.length < VERIFIER_LENGTH) {
        out += ALPHABET[b % ALPHABET.length]
      }
    }
  }
  return out
}

/** base64url encodes bytes without padding. */
export function base64url(bytes: Uint8Array): string {
  let s = ''
  for (const b of bytes) {
    s += String.fromCharCode(b)
  }
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** challengeOf returns the S256 challenge of a verifier: its SHA-256 in base64url. */
export async function challengeOf(verifier: string): Promise<string> {
  if (globalThis.crypto?.subtle === undefined) {
    throw new Error('this page is not a secure context, so it cannot sign in through OIDC: serve occamyd over https')
  }
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return base64url(new Uint8Array(digest))
}

/** loginURL is where the browser starts a sign-in that returns to returnURL. */
export function loginURL(returnURL: string, challenge: string): string {
  const q = new URLSearchParams({ return_url: returnURL, code_challenge: challenge, code_challenge_method: 'S256' })
  return `/api/v1/auth/oidc/login?${q.toString()}`
}

/** saveVerifier keeps the verifier for the return; false where storage is refused. */
export function saveVerifier(storage: Storage | undefined, verifier: string): boolean {
  try {
    storage?.setItem(VERIFIER_KEY, verifier)
    return storage !== undefined
  } catch {
    return false
  }
}

/** takeVerifier returns the kept verifier, or null, and forgets it: a verifier serves one sign-in. */
export function takeVerifier(storage: Storage | undefined): string | null {
  try {
    const v = storage?.getItem(VERIFIER_KEY) ?? null
    storage?.removeItem(VERIFIER_KEY)
    return v
  } catch {
    return null
  }
}

/** oidcErrorMessage describes an error that occamyd's callback put in the fragment. */
export function oidcErrorMessage(code: string): string {
  switch (code) {
    case 'access_denied':
      return 'The identity provider or occamyd refused this user.'
    case 'sign_in_failed':
      return 'The identity provider failed, or its answer did not verify.'
    case 'unavailable':
      return 'The identity provider cannot be reached right now. Try again later.'
    default:
      return 'The sign-in failed.'
  }
}
