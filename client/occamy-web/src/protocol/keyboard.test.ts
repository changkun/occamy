// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { hidUsage, hidUsages, keyOf, keysym } from './keyboard'

// The HID-to-XT table that the VNC and RDP backends send keys by,
// internal/keymap's set1, read from its source: each entry is
// `0xUU: 0xXX, // Name` or `0xUU: 0xXXXX, // Name`, a set 1 make code
// of a byte or two, where Name is KeyboardEvent.code's name of the key,
// or a description of it.
const keymap = readFileSync(new URL('../../../../internal/keymap/keymap.go', import.meta.url), 'utf8')
const set1 = keymap.match(/^var set1 = \[0xe8\]uint16\{\n([^}]*)^\}/m)?.[1] ?? ''
const hidToXT = new Map<number, { xt: number; name: string }>()
for (const m of set1.matchAll(/^\t0x([0-9a-f]{2}): 0x([0-9a-f]{2,4}), +\/\/ (\w+)/gm)) {
  hidToXT.set(parseInt(m[1], 16), { xt: parseInt(m[2], 16), name: m[3] })
}

// The keys of the table that every backend drops, read from the source
// of internal/backend's PowerKey, which they share: `const HIDPower = 0xUU`.
const power = readFileSync(new URL('../../../../internal/backend/power.go', import.meta.url), 'utf8')
const drops = new Set([...power.matchAll(/^const HIDPower = 0x([0-9a-f]{2})$/gm)].map((m) => parseInt(m[1], 16)))

// Go's names that are not KeyboardEvent.code's.
const goNames: Record<string, string | null> = {
  Katakana: 'Lang3',
  Hiragana: 'Lang4',
  Menu: null, // the keyboard page's Menu, which no browser reports
  International6: null, // no code; browsers report the key as NumpadComma or not at all
}

describe('HID usages', () => {
  it('reads the Go table', () => {
    expect(hidToXT.size).toBeGreaterThanOrEqual(148)
  })

  it('reads what the backends drop', () => {
    expect(drops.size).toBe(1)
  })

  it('maps every key of the backends’ table', () => {
    for (const [usage, { name }] of hidToXT) {
      const code = name in goNames ? goNames[name] : name
      if (code === null) {
        continue
      }
      expect([code, hidUsage(code)]).toEqual([code, usage])
    }
  })

  // The VNC and RDP backends send the table's keys, but Power, which
  // every backend drops on purpose: the remote would give it the whole
  // machine (specs/008-native-architecture/native-rdp-backend.md,
  // decision 4). The client still maps Power, as it maps every key the
  // table has, but no backend sends it.
  it('maps only keys the backends can send, and Power', () => {
    const dropped: string[] = []
    for (const [code, usage] of Object.entries(hidUsages)) {
      expect([code, hidToXT.get(usage)?.xt ?? 0]).not.toEqual([code, 0])
      if (drops.has(usage)) {
        dropped.push(code)
      }
    }
    expect(dropped).toEqual(['Power'])
  })

  it('maps the older names of keys', () => {
    expect(hidUsage('OSLeft')).toBe(hidUsage('MetaLeft'))
    expect(hidUsage('OSRight')).toBe(hidUsage('MetaRight'))
    expect(hidUsage('VolumeMute')).toBe(hidUsage('AudioVolumeMute'))
  })

  it('has no usage for keys it does not know', () => {
    expect(hidUsage('')).toBe(0)
    expect(hidUsage('Unidentified')).toBe(0)
    expect(hidUsage('toString')).toBe(0)
    expect(hidUsage('__proto__')).toBe(0)
  })
})

describe('keysyms', () => {
  const cases: [string, number, number][] = [
    ['a', 0, 0x61],
    ['A', 0, 0x41],
    [' ', 0, 0x20],
    ['~', 0, 0x7e],
    ['é', 0, 0xe9],
    ['ÿ', 0, 0xff],
    ['€', 0, 0x010020ac],
    ['ß', 0, 0xdf],
    ['я', 0, 0x0100044f],
    ['😀', 0, 0x0101f600],
    ['Enter', 0, 0xff0d],
    ['Backspace', 0, 0xff08],
    ['Tab', 0, 0xff09],
    ['Escape', 0, 0xff1b],
    ['Delete', 0, 0xffff],
    ['ArrowLeft', 0, 0xff51],
    ['PageDown', 0, 0xff56],
    ['F1', 0, 0xffbe],
    ['F12', 0, 0xffc9],
    ['F24', 0, 0xffd5],
    ['Shift', 1, 0xffe1],
    ['Shift', 2, 0xffe2],
    ['Control', 1, 0xffe3],
    ['Control', 2, 0xffe4],
    ['Alt', 1, 0xffe9],
    ['Alt', 2, 0xffea],
    ['Meta', 1, 0xffeb],
    ['Meta', 2, 0xffec],
    ['AltGraph', 2, 0xfe03],
    ['CapsLock', 0, 0xffe5],
    ['1', 3, 0xffb1],
    ['0', 3, 0xffb0],
    ['Enter', 3, 0xff8d],
    ['+', 3, 0xffab],
    ['.', 3, 0xffae],
    ['Delete', 3, 0xff9f],
    ['ArrowUp', 3, 0xff97],
    ['1', 0, 0x31],
    ['Dead', 0, 0],
    ['Unidentified', 0, 0],
    ['Process', 0, 0],
    ['', 0, 0],
    ['ab', 0, 0],
    ['\u0000', 0, 0],
    ['\u0085', 0, 0],
  ]
  for (const [key, location, want] of cases) {
    it(`${JSON.stringify(key)} at ${location}`, () => {
      expect(keysym(key, location)).toBe(want)
    })
  }

  it('makes a key message from a KeyboardEvent', () => {
    expect(keyOf({ key: 'a', code: 'KeyA', location: 0 })).toEqual({ keysym: 0x61, code: 0x04 })
    // An AZERTY a is the key QWERTY calls KeyQ.
    expect(keyOf({ key: 'a', code: 'KeyQ' })).toEqual({ keysym: 0x61, code: 0x14 })
    expect(keyOf({ key: 'Dead', code: 'BracketLeft' })).toEqual({ keysym: 0, code: 0x2f })
  })
})
