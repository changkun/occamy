// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The DOM bindings, with Node's EventTarget and Event standing in for
// elements and keyboard, pointer and wheel events.

import { describe, expect, it } from 'vitest'

import { type Box, MAX_CURSOR_SIDE, WheelAccumulator, bindKeyboard, bindPointer, cursorStyle, pointerButtons, remotePoint } from './dom'
import { ClosedError } from './errors'
import { Buttons, WheelMode } from './messages'

function event(type: string, props: Record<string, unknown>): Event {
  return Object.assign(new Event(type, { cancelable: true }), props)
}

function key(type: 'keydown' | 'keyup', key: string, code: string, location = 0, isComposing = false): Event {
  return event(type, { key, code, location, isComposing })
}

class Recorder {
  readonly calls: unknown[][] = []
  key(down: boolean, keysym: number, code: number): number {
    this.calls.push(['key', down, keysym, code])
    return this.calls.length
  }
  text(text: string): number {
    this.calls.push(['text', text])
    return this.calls.length
  }
  pointer(x: number, y: number, buttons: number): number {
    this.calls.push(['pointer', x, y, buttons])
    return this.calls.length
  }
  wheel(dx: number, dy: number, mode = 0): number {
    this.calls.push(['wheel', dx, dy, mode])
    return this.calls.length
  }
}

describe('keyboard', () => {
  it('repeats a key with the keysym of its first press', () => {
    const target = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: new EventTarget() })
    target.dispatchEvent(key('keydown', 'a', 'KeyA'))
    // Shift went down while a repeats.
    const repeat = event('keydown', { key: 'A', code: 'KeyA', location: 0, isComposing: false, repeat: true })
    target.dispatchEvent(repeat)
    expect(repeat.defaultPrevented).toBe(true)
    target.dispatchEvent(key('keyup', 'A', 'KeyA'))
    expect(r.calls).toEqual([
      ['key', true, 0x61, 0x04],
      ['key', true, 0x61, 0x04],
      ['key', false, 0x61, 0x04],
    ])
  })

  it('sends presses and releases with the keysym of the press', () => {
    const target = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: new EventTarget() })
    const down = key('keydown', 'a', 'KeyA')
    target.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    // Shift went down meanwhile: the release still says a.
    target.dispatchEvent(key('keyup', 'A', 'KeyA'))
    expect(r.calls).toEqual([
      ['key', true, 0x61, 0x04],
      ['key', false, 0x61, 0x04],
    ])
  })

  it('leaves keys an input method composes to compositionend', () => {
    const target = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: new EventTarget() })
    target.dispatchEvent(key('keydown', 'n', 'KeyN', 0, true))
    target.dispatchEvent(key('keydown', 'Process', 'KeyI'))
    target.dispatchEvent(key('keyup', 'n', 'KeyN'))
    target.dispatchEvent(event('compositionend', { data: '日本' }))
    target.dispatchEvent(event('compositionend', { data: '' }))
    expect(r.calls).toEqual([['text', '日本']])
  })

  it('releases the keys held when focus leaves', () => {
    const target = new EventTarget()
    const win = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: win })
    target.dispatchEvent(key('keydown', 'Control', 'ControlLeft', 1))
    target.dispatchEvent(key('keydown', 'c', 'KeyC'))
    win.dispatchEvent(new Event('blur'))
    expect(r.calls.slice(2)).toEqual([
      ['key', false, 0xffe3, 0xe0],
      ['key', false, 0x63, 0x06],
    ])
    target.dispatchEvent(key('keyup', 'c', 'KeyC'))
    expect(r.calls).toHaveLength(4)
  })

  it('releases the keys held with Command, whose releases macOS does not report', () => {
    const target = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: new EventTarget() })
    target.dispatchEvent(key('keydown', 'Meta', 'MetaLeft', 1))
    target.dispatchEvent(key('keydown', 'c', 'KeyC'))
    target.dispatchEvent(key('keyup', 'Meta', 'MetaLeft', 1))
    expect(r.calls.slice(2)).toEqual([
      ['key', false, 0xffeb, 0xe3],
      ['key', false, 0x63, 0x06],
    ])
  })

  it('sends a dead key by its usage alone, and ignores keys without either', () => {
    const target = new EventTarget()
    const r = new Recorder()
    bindKeyboard(target, r, { window: new EventTarget() })
    target.dispatchEvent(key('keydown', 'Dead', 'BracketLeft'))
    const unknown = key('keydown', 'Unidentified', '')
    target.dispatchEvent(unknown)
    expect(unknown.defaultPrevented).toBe(false)
    expect(r.calls).toEqual([['key', true, 0, 0x2f]])
  })

  it('hands input errors to onError, and stops on unbind', () => {
    const target = new EventTarget()
    const errors: unknown[] = []
    const failing = {
      key: (): number => {
        throw new ClosedError()
      },
      text: (): number => 0,
    }
    const unbind = bindKeyboard(target, failing, { window: new EventTarget(), onError: (e) => errors.push(e) })
    target.dispatchEvent(key('keydown', 'a', 'KeyA'))
    expect(errors).toHaveLength(1)
    expect(errors[0]).toBeInstanceOf(ClosedError)
    unbind()
    target.dispatchEvent(key('keydown', 'a', 'KeyA'))
    expect(errors).toHaveLength(2) // the release of the held key on unbind
  })
})

describe('pointer', () => {
  it('converts DOM buttons to the protocol’s', () => {
    expect(pointerButtons(0)).toBe(0)
    expect(pointerButtons(1)).toBe(Buttons.Left)
    expect(pointerButtons(2)).toBe(Buttons.Right)
    expect(pointerButtons(4)).toBe(Buttons.Middle)
    expect(pointerButtons(8)).toBe(Buttons.Back)
    expect(pointerButtons(16)).toBe(Buttons.Forward)
    expect(pointerButtons(7)).toBe(Buttons.Left | Buttons.Middle | Buttons.Right)
    expect(pointerButtons(32)).toBe(0)
  })

  it('maps the page to the screen, clamped', () => {
    const box: Box = { left: 10, top: 20, width: 400, height: 300 }
    const screen = { width: 800, height: 600 }
    expect(remotePoint(10, 20, box, screen)).toEqual({ x: 0, y: 0 })
    expect(remotePoint(210, 170, box, screen)).toEqual({ x: 400, y: 300 })
    expect(remotePoint(409.9, 319.9, box, screen)).toEqual({ x: 799, y: 599 })
    expect(remotePoint(500, 500, box, screen)).toEqual({ x: 799, y: 599 })
    expect(remotePoint(0, 0, box, screen)).toEqual({ x: 0, y: 0 })
    expect(remotePoint(5, 5, { ...box, width: 0 }, screen)).toEqual({ x: 0, y: 0 })
  })

  it('sends moves and buttons over an element', () => {
    const el = Object.assign(new EventTarget(), {
      getBoundingClientRect: (): Box => ({ left: 0, top: 0, width: 100, height: 50 }),
      captured: -1,
      focused: 0,
      focus() {
        this.focused++
      },
      setPointerCapture(id: number) {
        this.captured = id
      },
    })
    const r = new Recorder()
    bindPointer(el, r, () => ({ width: 200, height: 100 }))
    el.dispatchEvent(event('pointermove', { clientX: 50, clientY: 25, buttons: 0, pointerId: 1 }))
    el.dispatchEvent(event('pointerdown', { clientX: 50, clientY: 25, buttons: 2, pointerId: 1 }))
    el.dispatchEvent(event('pointerup', { clientX: 51, clientY: 25, buttons: 0, pointerId: 1 }))
    const menu = event('contextmenu', {})
    el.dispatchEvent(menu)
    expect(menu.defaultPrevented).toBe(true)
    expect(el.captured).toBe(1)
    expect(el.focused).toBe(1)
    expect(r.calls).toEqual([
      ['pointer', 100, 50, 0],
      ['pointer', 100, 50, Buttons.Right],
      ['pointer', 102, 50, 0],
    ])
  })

  it('sends nothing before the screen has a size', () => {
    const el = Object.assign(new EventTarget(), { getBoundingClientRect: (): Box => ({ left: 0, top: 0, width: 100, height: 50 }) })
    const r = new Recorder()
    bindPointer(el, r, () => ({ width: 0, height: 0 }))
    el.dispatchEvent(event('pointermove', { clientX: 1, clientY: 1, buttons: 0, pointerId: 1 }))
    expect(r.calls).toEqual([])
  })

  it('sends scrolls in the remote’s pixels', () => {
    const el = Object.assign(new EventTarget(), { getBoundingClientRect: (): Box => ({ left: 0, top: 0, width: 100, height: 50 }) })
    const r = new Recorder()
    bindPointer(el, r, () => ({ width: 200, height: 100 }))
    const w = event('wheel', { deltaX: 0, deltaY: 10, deltaMode: 0 })
    el.dispatchEvent(w)
    expect(w.defaultPrevented).toBe(true)
    expect(r.calls).toEqual([['wheel', 0, 20, WheelMode.Pixels]])
  })
})

describe('wheel', () => {
  const screen = { width: 800, height: 600 }

  it('adds up fractional deltas', () => {
    const a = new WheelAccumulator()
    expect(a.add(0, 0.4, 0, 1, screen)).toBeNull()
    expect(a.add(0, 0.4, 0, 1, screen)).toBeNull()
    expect(a.add(0, 0.4, 0, 1, screen)).toEqual({ dx: 0, dy: 1, mode: WheelMode.Pixels })
    expect(a.add(-1.5, -0.3, 0, 1, screen)).toEqual({ dx: -1, dy: 0, mode: WheelMode.Pixels })
  })

  it('scrolls lines as lines, and a page as a screen of pixels', () => {
    const a = new WheelAccumulator()
    expect(a.add(0, 3, 1, 2, screen)).toEqual({ dx: 0, dy: 3, mode: WheelMode.Lines })
    expect(a.add(0, 1, 2, 2, screen)).toEqual({ dx: 0, dy: 600, mode: WheelMode.Pixels })
    expect(a.add(0, 1, 9, 2, screen)).toBeNull()
  })

  it('starts afresh when the unit changes', () => {
    const a = new WheelAccumulator()
    expect(a.add(0, 0.5, 1, 1, screen)).toBeNull()
    expect(a.add(0, 0.5, 0, 1, screen)).toBeNull()
    expect(a.add(0, 0.5, 0, 1, screen)).toEqual({ dx: 0, dy: 1, mode: WheelMode.Pixels })
  })

  it('stays within the wheel message', () => {
    const a = new WheelAccumulator()
    expect(a.add(1e12, -1e12, 0, 1, screen)).toEqual({ dx: 0x7fffffff, dy: -0x80000000, mode: WheelMode.Pixels })
  })
})

describe('cursor', () => {
  it('shows the default arrow for an image larger than browsers show, drawing nothing', () => {
    // Node has no document: drawing would throw.
    const image = (w: number, h: number) => ({ width: w, height: h, pixels: new Uint8ClampedArray(4 * w * h) })
    expect(cursorStyle(image(MAX_CURSOR_SIDE + 1, 1), 0, 0)).toBe('default')
    expect(cursorStyle(image(1, 4096), 0, 0)).toBe('default')
    expect(cursorStyle(null, 0, 0)).toBe('none')
  })
})
