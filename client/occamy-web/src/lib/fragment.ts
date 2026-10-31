// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// What arrives in the page's fragment, which browsers never send to a
// server: an OIDC sign-in's one-time code or error, from occamyd's
// callback (docs/configuration.md, Signing in through OIDC), and a share
// link's session and share token. The shell reads it once and removes it
// from the address bar and the history entry.

/** What a fragment asks the shell to do. */
export type Fragment =
  | { kind: 'none' }
  | { kind: 'oidc-code'; code: string }
  | { kind: 'oidc-error'; error: string }
  | { kind: 'join'; sessionId: string; token: string }

/** readFragment parses location.hash, with or without its #. */
export function readFragment(hash: string): Fragment {
  const p = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash)
  const code = p.get('code')
  if (code !== null && code !== '') {
    return { kind: 'oidc-code', code }
  }
  const error = p.get('error')
  if (error !== null && error !== '') {
    return { kind: 'oidc-error', error }
  }
  const sessionId = p.get('join')
  const token = p.get('token')
  if (sessionId !== null && sessionId !== '' && token !== null && token !== '') {
    return { kind: 'join', sessionId, token }
  }
  return { kind: 'none' }
}

/**
 * takeFragment reads the page's fragment, once, as the page loads, and
 * removes one that asks for something from the address bar and from the
 * history entry: it may carry an OIDC one-time code or a share, neither
 * of which should outlive the load, be bookmarked, or come back with
 * the Back button.
 */
export function takeFragment(location: Pick<Location, 'hash' | 'pathname' | 'search'>, history: Pick<History, 'replaceState'>): Fragment {
  const fragment = readFragment(location.hash)
  if (fragment.kind !== 'none') {
    history.replaceState(null, '', location.pathname + location.search)
  }
  return fragment
}

/**
 * shareLink returns a link to page that joins session sessionId with a
 * share. The share rides in the fragment, which no server or proxy sees;
 * whoever holds the link holds the share until it expires.
 */
export function shareLink(page: string, sessionId: string, token: string): string {
  const base = page.split('#')[0]
  return `${base}#${new URLSearchParams({ join: sessionId, token }).toString()}`
}

/** pageURL is the address of the page itself: without its query and fragment, as OIDC's return_url. */
export function pageURL(location: Pick<Location, 'origin' | 'pathname'>): string {
  return location.origin + location.pathname
}
