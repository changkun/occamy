// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The JSON routes of API v1 that the shell calls (docs/api.md), on the
// page's origin: occamyd answers no CORS preflight. Bodies are exactly
// the fields a route takes, as application/json; a token goes in
// Authorization, never in a URL; and errors come back as ApiError with
// API v1's stable code.

import { MAX_LABEL, clip } from './text'

/** A protocol of a connection, as GET /api/v1/connections names it. */
export interface Connection {
  id: string
  name: string
  protocol: string
}

/** An access token and when the shell stops using it, in milliseconds since the epoch. */
export interface AccessToken {
  token: string
  expires: number
}

/** A share of a session, from POST /api/v1/sessions/{id}/shares. */
export interface Share {
  token: string
  role: ShareRole
  expiresIn: number
}

export type ShareRole = 'viewer' | 'read-only'

/**
 * ApiError is an answer of API v1 other than success: its HTTP status
 * and the code of its error body, or "network" for no answer, "timeout"
 * for none within the call's time limit, "http" for an answer without
 * API v1's error body, such as an occamyd serving no API v1, and
 * "bad_response" for a success that did not parse.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }

  /** Whether the access token is no longer good, so the user signs in again. */
  get signedOut(): boolean {
    return this.status === 401 && (this.code === 'token_expired' || this.code === 'invalid_token' || this.code === 'unauthorized')
  }
}

// An access token is dropped this long before it expires, so that a
// request does not race its expiry.
const EXPIRY_MARGIN = 10_000

/**
 * How long a call waits for occamyd's answer, in milliseconds: occamyd
 * gives up on a request after its write timeout of 30 s (server/server.go),
 * so a call that takes longer is stuck in a proxy or the network, and
 * the page says so rather than waiting on it.
 */
export const API_TIMEOUT = 30_000

type Fetch = (input: string, init?: RequestInit) => Promise<Response>

export class Api {
  private readonly fetch: Fetch
  private readonly now: () => number
  private readonly timeout: number

  constructor(fetchFn: Fetch = (input, init) => globalThis.fetch(input, init), now: () => number = Date.now, timeout = API_TIMEOUT) {
    this.fetch = fetchFn
    this.now = now
    this.timeout = timeout
  }

  /** signIn signs a static user in with a password (POST /api/v1/auth/token). */
  async signIn(username: string, password: string): Promise<AccessToken> {
    return this.token(await this.call('POST', '/api/v1/auth/token', { body: { username, password } }))
  }

  /**
   * oidcAvailable reports whether occamyd signs users in through OIDC.
   * API v1 has no route that says so, but GET /api/v1/auth/oidc/login
   * without a challenge answers 400 with OIDC on, and 404 off, setting
   * nothing either way.
   */
  async oidcAvailable(): Promise<boolean> {
    try {
      const res = await this.fetch('/api/v1/auth/oidc/login', { method: 'GET', redirect: 'manual', cache: 'no-store', signal: AbortSignal.timeout(this.timeout) })
      return res.status === 400
    } catch {
      return false
    }
  }

  /** oidcToken exchanges an OIDC one-time code and its verifier for an access token (POST /api/v1/auth/oidc/token). */
  async oidcToken(code: string, verifier: string): Promise<AccessToken> {
    return this.token(await this.call('POST', '/api/v1/auth/oidc/token', { body: { code, code_verifier: verifier } }))
  }

  /** connections lists the connections the user may open (GET /api/v1/connections). */
  async connections(token: AccessToken): Promise<Connection[]> {
    const v = await this.call('GET', '/api/v1/connections', { token })
    const list = isObject(v) ? v.connections : undefined
    if (!Array.isArray(list) || !list.every(isConnection)) {
      throw badResponse()
    }
    return list
  }

  /** endSession closes a session for everyone, its owner's call (DELETE /api/v1/sessions/{id}). */
  async endSession(token: AccessToken, sessionId: string): Promise<void> {
    await this.call('DELETE', `/api/v1/sessions/${encodeURIComponent(sessionId)}`, { token })
  }

  /** share mints a share of a session for role, lasting expiresIn seconds (POST /api/v1/sessions/{id}/shares). */
  async share(token: AccessToken, sessionId: string, role: ShareRole, expiresIn: number): Promise<Share> {
    const v = await this.call('POST', `/api/v1/sessions/${encodeURIComponent(sessionId)}/shares`, {
      token,
      body: { role, expires_in: expiresIn },
    })
    if (!isObject(v) || typeof v.token !== 'string' || (v.role !== 'viewer' && v.role !== 'read-only') || typeof v.expires_in !== 'number') {
      throw badResponse()
    }
    return { token: v.token, role: v.role, expiresIn: v.expires_in }
  }

  private token(v: unknown): AccessToken {
    if (!isObject(v) || typeof v.access_token !== 'string' || v.access_token === '' || typeof v.expires_in !== 'number') {
      throw badResponse()
    }
    return { token: v.access_token, expires: this.now() + v.expires_in * 1000 - EXPIRY_MARGIN }
  }

  // call sends a request and returns its JSON body, or null for none.
  private async call(method: string, path: string, o: { token?: AccessToken; body?: object }): Promise<unknown> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (o.token !== undefined) {
      if (o.token.expires <= this.now()) {
        throw new ApiError(401, 'token_expired', 'Your sign-in has expired.')
      }
      headers.Authorization = `Bearer ${o.token.token}`
    }
    let body: string | undefined
    if (o.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(o.body)
    }
    // The time limit covers the answer's body too, which fetch reads
    // under the same signal.
    const signal = AbortSignal.timeout(this.timeout)
    let res: Response
    let text: string
    try {
      res = await this.fetch(path, { method, headers, body, cache: 'no-store', credentials: 'same-origin', signal })
      text = await res.text()
    } catch (err) {
      if (signal.aborted || (err instanceof Error && err.name === 'TimeoutError')) {
        throw new ApiError(0, 'timeout', 'occamyd did not answer in time.')
      }
      throw new ApiError(0, 'network', 'occamyd cannot be reached.')
    }
    let json: unknown = null
    if (text !== '') {
      try {
        json = JSON.parse(text)
      } catch {
        json = undefined
      }
    }
    if (res.ok) {
      if (json === undefined) {
        throw badResponse()
      }
      return json
    }
    const err = isObject(json) && isObject(json.error) ? json.error : null
    if (err !== null && typeof err.code === 'string') {
      throw new ApiError(res.status, err.code, typeof err.message === 'string' ? err.message : err.code)
    }
    throw new ApiError(res.status, 'http', `occamyd answered ${res.status}${res.statusText === '' ? '' : ` ${res.statusText}`}.`)
  }
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function isConnection(v: unknown): v is Connection {
  return isObject(v) && typeof v.id === 'string' && typeof v.name === 'string' && typeof v.protocol === 'string'
}

function badResponse(): ApiError {
  return new ApiError(0, 'bad_response', 'occamyd answered with something the client does not understand.')
}

/**
 * subjectOf returns the user an access token names in sub, clipped, for
 * the page to show, or empty. The token is occamyd's to verify; this
 * only reads it.
 */
export function subjectOf(token: AccessToken): string {
  const payload = token.token.split('.')[1] ?? ''
  try {
    const bytes = Uint8Array.from(atob(payload.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0))
    const claims: unknown = JSON.parse(new TextDecoder().decode(bytes))
    return isObject(claims) && typeof claims.sub === 'string' ? clip(claims.sub, MAX_LABEL) : ''
  } catch {
    return ''
  }
}

/**
 * errorMessage says what went wrong with a call, for the page. An
 * occamyd that answers sign-in without API v1's error body serves a v0
 * file, whose routes the client does not use.
 */
export function errorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) {
    return err instanceof Error ? err.message : String(err)
  }
  if (err.code === 'http' && err.status === 404) {
    return 'occamyd does not serve API v1 here. The web client needs a configuration file with version: 1.'
  }
  switch (err.code) {
    case 'unauthorized':
      return 'The user name or password is wrong.'
    case 'token_expired':
    case 'invalid_token':
      return 'Your sign-in has expired. Sign in again.'
    case 'limit_exceeded':
      return err.status === 503 ? 'occamyd runs as many sessions as it allows. Try again later.' : 'You have as many sessions or shares as occamyd allows. Close one first.'
    case 'not_found':
      return 'The session has ended, or is not yours.'
    default:
      return err.message
  }
}
