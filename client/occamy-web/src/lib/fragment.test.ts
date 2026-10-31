// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { pageURL, readFragment, shareLink, takeFragment } from './fragment'

describe('readFragment', () => {
  it('reads an OIDC one-time code and error, as occamyd’s callback sends them', () => {
    expect(readFragment('#code=JX2E7OZK6V3QWBTLDM4PNRYH5A')).toEqual({ kind: 'oidc-code', code: 'JX2E7OZK6V3QWBTLDM4PNRYH5A' })
    expect(readFragment('#error=access_denied')).toEqual({ kind: 'oidc-error', error: 'access_denied' })
  })

  it('reads a share link', () => {
    expect(readFragment('#join=mZ3k1tYb0Q2fJw8aXr5LsA&token=q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty')).toEqual({
      kind: 'join',
      sessionId: 'mZ3k1tYb0Q2fJw8aXr5LsA',
      token: 'q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty',
    })
  })

  it('reads nothing from an empty, unknown or partial fragment', () => {
    for (const hash of ['', '#', '#/desktop', '#join=abc', '#token=abc', '#code=', '#error=']) {
      expect(readFragment(hash)).toEqual({ kind: 'none' })
    }
  })

  it('takes a fragment without its #', () => {
    expect(readFragment('error=unavailable')).toEqual({ kind: 'oidc-error', error: 'unavailable' })
  })
})

describe('shareLink', () => {
  it('puts the session and the share in the fragment, which round-trips', () => {
    const link = shareLink('https://occamy.example/static/', 'mZ3k1tYb0Q2fJw8aXr5LsA', 'q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty')
    expect(link).toBe('https://occamy.example/static/#join=mZ3k1tYb0Q2fJw8aXr5LsA&token=q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty')
    const url = new URL(link)
    expect(url.search).toBe('')
    expect(readFragment(url.hash)).toEqual({ kind: 'join', sessionId: 'mZ3k1tYb0Q2fJw8aXr5LsA', token: 'q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty' })
  })

  it('replaces the page’s own fragment, and escapes what needs it', () => {
    const link = shareLink('https://occamy.example/static/#code=x', 'a&b', 'c=d')
    expect(readFragment(new URL(link).hash)).toEqual({ kind: 'join', sessionId: 'a&b', token: 'c=d' })
  })
})

describe('takeFragment', () => {
  function page(hash: string): { location: { hash: string; pathname: string; search: string }; replaced: unknown[][]; history: Pick<History, 'replaceState'> } {
    const replaced: unknown[][] = []
    return { location: { hash, pathname: '/static/', search: '?lang=en' }, replaced, history: { replaceState: (...args: unknown[]) => void replaced.push(args) } }
  }

  it('removes a one-time code, an error and a share from the address bar and the history entry', () => {
    for (const hash of ['#code=JX2E7', '#error=access_denied', '#join=s&token=t']) {
      const p = page(hash)
      expect(takeFragment(p.location, p.history)).toEqual(readFragment(hash))
      expect(p.replaced).toEqual([[null, '', '/static/?lang=en']])
    }
  })

  it('is how the page reads its fragment', () => {
    const main = readFileSync(new URL('../main.tsx', import.meta.url), 'utf8')
    expect(main).toContain('takeFragment(location, history)')
    expect(main).not.toMatch(/readFragment|location\.hash/)
  })

  it('leaves a fragment that asks for nothing', () => {
    for (const hash of ['', '#', '#section']) {
      const p = page(hash)
      expect(takeFragment(p.location, p.history)).toEqual({ kind: 'none' })
      expect(p.replaced).toEqual([])
    }
  })
})

describe('pageURL', () => {
  it('is the page without its query and fragment', () => {
    expect(pageURL({ origin: 'https://occamy.example', pathname: '/static/' })).toBe('https://occamy.example/static/')
  })
})
