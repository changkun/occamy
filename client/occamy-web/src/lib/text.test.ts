// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { clip, clipboardText } from './text'

describe('clip', () => {
  it('keeps short text', () => {
    expect(clip('Ubuntu desktop', 20)).toBe('Ubuntu desktop')
    expect(clip('', 5)).toBe('')
  })

  it('cuts long text with an ellipsis, within the limit', () => {
    expect(clip('abcdefghij', 5)).toBe('abcd…')
    expect(Array.from(clip('x'.repeat(4 << 20), 200))).toHaveLength(200)
  })

  it('counts code points and splits no surrogate pair', () => {
    expect(clip('😀😀😀', 3)).toBe('😀😀😀')
    expect(clip('😀😀😀😀', 3)).toBe('😀😀…')
  })

  it('turns control and bidirectional override characters into spaces', () => {
    expect(clip('a\u0000b\u001bc\u007fd\u0085e', 20)).toBe('a b c d e')
    expect(clip('evil‮gnp.exe', 20)).toBe('evil gnp.exe')
    expect(clip('line\nbreak\ttab', 20)).toBe('line\nbreak\ttab')
  })
})

describe('clipboardText', () => {
  const utf8 = (s: string): Uint8Array => new TextEncoder().encode(s)

  it('decodes text as UTF-8', () => {
    expect(clipboardText('text/plain', utf8('héllo'))).toBe('héllo')
    expect(clipboardText('text/plain;charset=utf-8', utf8('日本'))).toBe('日本')
    expect(clipboardText('TEXT/PLAIN', utf8('x'))).toBe('x')
  })

  it('replaces bytes that are not UTF-8', () => {
    expect(clipboardText('text/plain', new Uint8Array([0x61, 0xff, 0x62]))).toBe('a�b')
  })

  it('refuses what is not text', () => {
    expect(clipboardText('image/png', new Uint8Array([0x89]))).toBeNull()
    expect(clipboardText('', new Uint8Array())).toBeNull()
  })
})
