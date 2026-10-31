// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it, vi } from 'vitest'

import { VERIFIER_KEY, VERIFIER_LENGTH, base64url, challengeOf, loginURL, newVerifier, oidcErrorMessage, saveVerifier, takeVerifier } from './oidc'

// A Storage in memory, as sessionStorage is.
class MemoryStorage implements Storage {
  private items = new Map<string, string>()
  get length(): number {
    return this.items.size
  }
  clear(): void {
    this.items.clear()
  }
  getItem(key: string): string | null {
    return this.items.get(key) ?? null
  }
  key(i: number): string | null {
    return Array.from(this.items.keys())[i] ?? null
  }
  removeItem(key: string): void {
    this.items.delete(key)
  }
  setItem(key: string, value: string): void {
    this.items.set(key, value)
  }
}

describe('newVerifier', () => {
  it('is PKCE’s length and alphabet', () => {
    for (let i = 0; i < 100; i++) {
      expect(newVerifier()).toMatch(new RegExp(`^[A-Za-z0-9._~-]{${VERIFIER_LENGTH}}$`))
    }
    expect(VERIFIER_LENGTH).toBeGreaterThanOrEqual(43)
    expect(VERIFIER_LENGTH).toBeLessThanOrEqual(128)
  })

  it('draws again bytes that would bias it', () => {
    // 66 characters: bytes from 198 on would favour the first 58.
    let calls = 0
    const v = newVerifier((b) => {
      b.fill(calls++ === 0 ? 255 : 0)
    })
    expect(calls).toBe(2)
    expect(v).toBe('A'.repeat(VERIFIER_LENGTH))
  })

  it('draws from the browser’s cryptographic generator', () => {
    const random = vi.spyOn(crypto, 'getRandomValues')
    const before = Math.random
    Math.random = () => {
      throw new Error('a verifier from Math.random can be guessed')
    }
    try {
      newVerifier()
      expect(random).toHaveBeenCalled()
      const drawn = random.mock.calls.flat() as unknown[]
      expect(drawn.every((b) => b instanceof Uint8Array && b.length >= VERIFIER_LENGTH)).toBe(true)
    } finally {
      Math.random = before
      random.mockRestore()
    }
  })
})

describe('challengeOf', () => {
  it('is RFC 7636’s example', async () => {
    expect(await challengeOf('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM')
  })
})

describe('base64url', () => {
  it('has no padding and URL-safe characters', () => {
    expect(base64url(new Uint8Array([0xfb, 0xff]))).toBe('-_8')
    expect(base64url(new Uint8Array())).toBe('')
  })
})

describe('loginURL', () => {
  it('names the return URL and the challenge, as docs/api.md shows', () => {
    const u = new URL(loginURL('https://occamy.example/static/', 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM'), 'https://occamy.example')
    expect(u.pathname).toBe('/api/v1/auth/oidc/login')
    expect(Object.fromEntries(u.searchParams)).toEqual({
      return_url: 'https://occamy.example/static/',
      code_challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM',
      code_challenge_method: 'S256',
    })
  })
})

describe('the verifier’s storage', () => {
  it('keeps the verifier for one sign-in', () => {
    const s = new MemoryStorage()
    expect(saveVerifier(s, 'v')).toBe(true)
    expect(s.getItem(VERIFIER_KEY)).toBe('v')
    expect(takeVerifier(s)).toBe('v')
    expect(takeVerifier(s)).toBeNull()
  })

  it('survives storage that is missing or refuses', () => {
    expect(saveVerifier(undefined, 'v')).toBe(false)
    expect(takeVerifier(undefined)).toBeNull()
    const refusing = new MemoryStorage()
    refusing.setItem = () => {
      throw new DOMException('quota', 'QuotaExceededError')
    }
    refusing.getItem = () => {
      throw new DOMException('denied', 'SecurityError')
    }
    expect(saveVerifier(refusing, 'v')).toBe(false)
    expect(takeVerifier(refusing)).toBeNull()
  })
})

describe('oidcErrorMessage', () => {
  it('describes each error of the callback, and others', () => {
    const codes = ['access_denied', 'sign_in_failed', 'unavailable', 'something_else']
    const messages = codes.map(oidcErrorMessage)
    expect(new Set(messages).size).toBe(codes.length)
    for (const m of messages) {
      expect(m).not.toBe('')
    }
  })
})
