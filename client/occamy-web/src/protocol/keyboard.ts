// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The key message carries both what a key produced, an X11 keysym, and
// which key it was, a USB HID usage from the keyboard page (0x07)
// (docs/protocol.md, key). The keysym comes from KeyboardEvent.key, the
// key's text under the user's layout; the usage from KeyboardEvent.code,
// the physical key, which backends that want scancodes translate: the
// VNC backend's QEMU extended key events and the RDP backend's keyboard
// events through their HID-to-XT table (internal/keymap/keymap.go),
// whose keys this table covers. Every backend drops Power
// (internal/backend/power.go).

/** hidUsages maps KeyboardEvent.code to the key's USB HID usage on the keyboard page. */
export const hidUsages: Readonly<Record<string, number>> = (() => {
  const t: Record<string, number> = {}
  for (let i = 0; i < 26; i++) {
    t[`Key${String.fromCharCode(65 + i)}`] = 0x04 + i
  }
  for (let i = 1; i <= 9; i++) {
    t[`Digit${i}`] = 0x1e + i - 1
    t[`Numpad${i}`] = 0x59 + i - 1
  }
  t['Digit0'] = 0x27
  t['Numpad0'] = 0x62
  for (let i = 1; i <= 12; i++) {
    t[`F${i}`] = 0x3a + i - 1
  }
  for (let i = 13; i <= 24; i++) {
    t[`F${i}`] = 0x68 + i - 13
  }
  Object.assign(t, {
    Enter: 0x28,
    Escape: 0x29,
    Backspace: 0x2a,
    Tab: 0x2b,
    Space: 0x2c,
    Minus: 0x2d,
    Equal: 0x2e,
    BracketLeft: 0x2f,
    BracketRight: 0x30,
    Backslash: 0x31,
    IntlHash: 0x32,
    Semicolon: 0x33,
    Quote: 0x34,
    Backquote: 0x35,
    Comma: 0x36,
    Period: 0x37,
    Slash: 0x38,
    CapsLock: 0x39,
    PrintScreen: 0x46,
    ScrollLock: 0x47,
    Pause: 0x48,
    Insert: 0x49,
    Home: 0x4a,
    PageUp: 0x4b,
    Delete: 0x4c,
    End: 0x4d,
    PageDown: 0x4e,
    ArrowRight: 0x4f,
    ArrowLeft: 0x50,
    ArrowDown: 0x51,
    ArrowUp: 0x52,
    NumLock: 0x53,
    NumpadDivide: 0x54,
    NumpadMultiply: 0x55,
    NumpadSubtract: 0x56,
    NumpadAdd: 0x57,
    NumpadEnter: 0x58,
    NumpadDecimal: 0x63,
    IntlBackslash: 0x64,
    ContextMenu: 0x65,
    Power: 0x66,
    NumpadEqual: 0x67,
    Open: 0x74,
    Help: 0x75,
    Select: 0x77,
    // The keyboard page's Stop, which Chromium reports as BrowserStop.
    BrowserStop: 0x78,
    Again: 0x79,
    Undo: 0x7a,
    Cut: 0x7b,
    Copy: 0x7c,
    Paste: 0x7d,
    Find: 0x7e,
    AudioVolumeMute: 0x7f,
    AudioVolumeUp: 0x80,
    AudioVolumeDown: 0x81,
    NumpadComma: 0x85,
    IntlRo: 0x87,
    KanaMode: 0x88,
    IntlYen: 0x89,
    Convert: 0x8a,
    NonConvert: 0x8b,
    Lang1: 0x90,
    Lang2: 0x91,
    Lang3: 0x92,
    Lang4: 0x93,
    Lang5: 0x94,
    NumpadParenLeft: 0xb6,
    NumpadParenRight: 0xb7,
    ControlLeft: 0xe0,
    ShiftLeft: 0xe1,
    AltLeft: 0xe2,
    MetaLeft: 0xe3,
    ControlRight: 0xe4,
    ShiftRight: 0xe5,
    AltRight: 0xe6,
    MetaRight: 0xe7,
    // Names older browsers gave the same keys.
    OSLeft: 0xe3,
    OSRight: 0xe7,
    VolumeMute: 0x7f,
    VolumeUp: 0x80,
    VolumeDown: 0x81,
  })
  return Object.freeze(t)
})()

/** hidUsage returns the HID usage of a KeyboardEvent.code, or 0 if it has none. */
export function hidUsage(code: string): number {
  return Object.hasOwn(hidUsages, code) ? hidUsages[code] : 0
}

// KeyboardEvent.location.
const LEFT = 1
const RIGHT = 2
const NUMPAD = 3

// Keysyms of named keys: [left or standard, right].
const named: Readonly<Record<string, readonly [number, number?]>> = {
  Backspace: [0xff08],
  Tab: [0xff09],
  Clear: [0xff0b],
  Enter: [0xff0d],
  Pause: [0xff13],
  ScrollLock: [0xff14],
  Escape: [0xff1b],
  Compose: [0xff20],
  KanjiMode: [0xff21],
  NonConvert: [0xff22],
  Convert: [0xff23],
  Romaji: [0xff24],
  Hiragana: [0xff25],
  Katakana: [0xff26],
  HiraganaKatakana: [0xff27],
  Zenkaku: [0xff28],
  Hankaku: [0xff29],
  ZenkakuHankaku: [0xff2a],
  KanaMode: [0xff2d],
  Eisu: [0xff2f],
  HangulMode: [0xff31],
  HanjaMode: [0xff34],
  JunjaMode: [0xff38],
  Home: [0xff50],
  ArrowLeft: [0xff51],
  ArrowUp: [0xff52],
  ArrowRight: [0xff53],
  ArrowDown: [0xff54],
  PageUp: [0xff55],
  PageDown: [0xff56],
  End: [0xff57],
  Select: [0xff60],
  PrintScreen: [0xff61],
  Execute: [0xff62],
  Insert: [0xff63],
  Undo: [0xff65],
  Redo: [0xff66],
  Again: [0xff66],
  ContextMenu: [0xff67],
  Find: [0xff68],
  Cancel: [0xff69],
  Help: [0xff6a],
  ModeChange: [0xff7e],
  NumLock: [0xff7f],
  Shift: [0xffe1, 0xffe2],
  Control: [0xffe3, 0xffe4],
  CapsLock: [0xffe5],
  Alt: [0xffe9, 0xffea],
  // The Windows and Command keys, which X calls Super.
  Meta: [0xffeb, 0xffec],
  OS: [0xffeb, 0xffec],
  Super: [0xffeb, 0xffec],
  Hyper: [0xffed, 0xffee],
  AltGraph: [0xfe03],
  Delete: [0xffff],
  AudioVolumeMute: [0x1008ff12],
  AudioVolumeDown: [0x1008ff11],
  AudioVolumeUp: [0x1008ff13],
}

// Keysyms of the numeric keypad's keys, by KeyboardEvent.key.
const keypad: Readonly<Record<string, number>> = {
  ' ': 0xff80,
  Tab: 0xff89,
  Enter: 0xff8d,
  Home: 0xff95,
  ArrowLeft: 0xff96,
  ArrowUp: 0xff97,
  ArrowRight: 0xff98,
  ArrowDown: 0xff99,
  PageUp: 0xff9a,
  PageDown: 0xff9b,
  End: 0xff9c,
  Clear: 0xff9d,
  Insert: 0xff9e,
  Delete: 0xff9f,
  '=': 0xffbd,
  '*': 0xffaa,
  '+': 0xffab,
  ',': 0xffac,
  '-': 0xffad,
  '.': 0xffae,
  '/': 0xffaf,
}

/**
 * keysym returns the X11 keysym of a key: key and location are
 * KeyboardEvent's. Text is its Unicode keysym (Latin-1 as it is, any
 * other code point 0x01000000 above it), named keys have theirs, the
 * keypad's keys their KP_ keysyms, and modifiers their left or right
 * one. It returns 0 for a key without one, such as a dead key, which
 * goes by its HID usage alone.
 */
export function keysym(key: string, location = 0): number {
  if (location === NUMPAD) {
    if (key.length === 1 && key >= '0' && key <= '9') {
      return 0xffb0 + key.charCodeAt(0) - 48
    }
    if (Object.hasOwn(keypad, key)) {
      return keypad[key]
    }
  }
  if (key.startsWith('F') && /^F([1-9]|[12][0-9]|3[0-5])$/.test(key)) {
    return 0xffbe + Number(key.slice(1)) - 1
  }
  if (Object.hasOwn(named, key)) {
    const [left, right] = named[key]
    return location === RIGHT && right !== undefined ? right : left
  }
  const cp = key.codePointAt(0)
  if (cp === undefined || key.length !== (cp > 0xffff ? 2 : 1)) {
    return 0 // not one character, such as Dead or Unidentified
  }
  if ((cp >= 0x20 && cp <= 0x7e) || (cp >= 0xa0 && cp <= 0xff)) {
    return cp
  }
  if (cp < 0x20 || (cp >= 0x7f && cp < 0xa0)) {
    return 0 // control characters are not keys' text
  }
  return 0x01000000 + cp
}

/** The parts of a KeyboardEvent that make a key message. */
export interface KeyLike {
  key: string
  code: string
  location?: number
}

/** keyOf returns the keysym and HID usage of a KeyboardEvent. */
export function keyOf(ev: KeyLike): { keysym: number; code: number } {
  return { keysym: keysym(ev.key, ev.location ?? 0), code: hidUsage(ev.code) }
}

export { LEFT as LOCATION_LEFT, RIGHT as LOCATION_RIGHT, NUMPAD as LOCATION_NUMPAD }
