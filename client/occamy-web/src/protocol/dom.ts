// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Bindings from DOM events to a session's input: the keyboard, the
// pointer and wheel, and the page's visibility. Each returns a function
// that removes it. The arithmetic lives in plain functions, which the
// tests call without a DOM.

import type { CursorImage } from './events'
import { ClosedError, ReadOnlyError } from './errors'
import { keyOf } from './keyboard'
import { Buttons, WheelMode } from './messages'
import type { Session } from './session'

/** Unbind removes a binding's listeners. */
export type Unbind = () => void

/** The errors of input a binding sends; by default reported, except ClosedError and ReadOnlyError. */
export type ErrorHandler = (err: unknown) => void

function defaultOnError(err: unknown): void {
  if (err instanceof ClosedError || err instanceof ReadOnlyError) {
    return // a viewer's or an ended session's input goes nowhere
  }
  if (typeof globalThis.reportError === 'function') {
    globalThis.reportError(err)
  } else {
    queueMicrotask(() => {
      throw err
    })
  }
}

/** The parts of a KeyboardEvent a key binding reads. */
interface KeyEventLike extends Event {
  key: string
  code: string
  location: number
  isComposing: boolean
}

/**
 * bindKeyboard sends the keys pressed while target has focus, and text
 * an input method composed there. A release sends the keysym its press
 * did, whatever the modifiers are by then, and keys still held when the
 * window loses focus are released, so the remote never keeps a key down
 * that the user let go of elsewhere. Keys the remote takes do not reach
 * the page: their default action is prevented.
 */
export function bindKeyboard(
  target: EventTarget,
  session: Pick<Session, 'key' | 'text'>,
  options: { window?: EventTarget; onError?: ErrorHandler } = {},
): Unbind {
  const onError = options.onError ?? defaultOnError
  // The keys down, by KeyboardEvent.code, with the keysym each pressed.
  const held = new Map<string, { keysym: number; code: number }>()
  const send = (down: boolean, k: { keysym: number; code: number }): void => {
    try {
      session.key(down, k.keysym, k.code)
    } catch (err) {
      onError(err)
    }
  }
  const releaseAll = (except?: string): void => {
    for (const [id, k] of held) {
      if (id !== except) {
        held.delete(id)
        send(false, k)
      }
    }
  }
  const onKeyDown = (e: Event): void => {
    const ev = e as KeyEventLike
    // An input method's keys compose text, which compositionend sends.
    if (ev.isComposing || ev.key === 'Process') {
      return
    }
    const k = keyOf(ev)
    if (k.keysym === 0 && k.code === 0) {
      return
    }
    ev.preventDefault()
    const id = ev.code !== '' ? ev.code : ev.key
    held.set(id, k)
    send(true, k)
  }
  const onKeyUp = (e: Event): void => {
    const ev = e as KeyEventLike
    const id = ev.code !== '' ? ev.code : ev.key
    const k = held.get(id)
    if (k === undefined) {
      return
    }
    ev.preventDefault()
    held.delete(id)
    send(false, k)
    if (ev.key === 'Meta') {
      // macOS sends no keyup for keys released while Command is down.
      releaseAll()
    }
  }
  const onCompositionEnd = (e: Event): void => {
    const data = (e as CompositionEvent).data
    if (data !== '') {
      try {
        session.text(data)
      } catch (err) {
        onError(err)
      }
    }
  }
  const onBlur = (): void => releaseAll()
  const win = options.window ?? (typeof window !== 'undefined' ? window : undefined)
  target.addEventListener('keydown', onKeyDown)
  target.addEventListener('keyup', onKeyUp)
  target.addEventListener('compositionend', onCompositionEnd)
  target.addEventListener('blur', onBlur)
  win?.addEventListener('blur', onBlur)
  return () => {
    target.removeEventListener('keydown', onKeyDown)
    target.removeEventListener('keyup', onKeyUp)
    target.removeEventListener('compositionend', onCompositionEnd)
    target.removeEventListener('blur', onBlur)
    win?.removeEventListener('blur', onBlur)
    releaseAll()
  }
}

/**
 * pointerButtons converts PointerEvent.buttons to the pointer message's:
 * the DOM numbers the secondary (right) button 2 and the auxiliary
 * (middle) 4, the protocol the other way round.
 */
export function pointerButtons(buttons: number): number {
  let b = buttons & (Buttons.Left | Buttons.Back | Buttons.Forward)
  if ((buttons & 2) !== 0) {
    b |= Buttons.Right
  }
  if ((buttons & 4) !== 0) {
    b |= Buttons.Middle
  }
  return b
}

/** A box on the page, such as getBoundingClientRect's. */
export interface Box {
  left: number
  top: number
  width: number
  height: number
}

/** A screen's size in pixels. */
export interface Size {
  width: number
  height: number
}

/**
 * remotePoint maps a point on the page to the remote screen's pixel
 * under it, for a box that shows the whole screen stretched across it,
 * clamped to the screen.
 */
export function remotePoint(clientX: number, clientY: number, box: Box, screen: Size): { x: number; y: number } {
  const clamp = (v: number, n: number): number => Math.min(Math.max(Math.floor(v), 0), Math.max(n - 1, 0))
  const x = box.width > 0 ? ((clientX - box.left) * screen.width) / box.width : 0
  const y = box.height > 0 ? ((clientY - box.top) * screen.height) / box.height : 0
  return { x: clamp(x, screen.width), y: clamp(y, screen.height) }
}

// WheelEvent.deltaMode.
const DOM_DELTA_PIXEL = 0
const DOM_DELTA_LINE = 1
const DOM_DELTA_PAGE = 2

/**
 * WheelAccumulator turns WheelEvents' fractional deltas into the wheel
 * message's whole ones, keeping what is left over for the next event, so
 * a touchpad's small deltas add up rather than vanish. Pixel deltas are
 * in CSS pixels and scale by scale, the remote's pixels for one; a page
 * is a screen's height of pixels.
 */
export class WheelAccumulator {
  private mode: number = WheelMode.Pixels
  private dx = 0
  private dy = 0

  /** add returns the whole deltas to send and their mode, or null for none yet. */
  add(deltaX: number, deltaY: number, deltaMode: number, scale: number, screen: Size): { dx: number; dy: number; mode: number } | null {
    let mode: number = WheelMode.Pixels
    let fx = deltaX * scale
    let fy = deltaY * scale
    if (deltaMode === DOM_DELTA_LINE) {
      mode = WheelMode.Lines
      fx = deltaX
      fy = deltaY
    } else if (deltaMode === DOM_DELTA_PAGE) {
      fx = deltaX * screen.width
      fy = deltaY * screen.height
    } else if (deltaMode !== DOM_DELTA_PIXEL) {
      return null
    }
    if (mode !== this.mode) {
      this.mode = mode
      this.dx = 0
      this.dy = 0
    }
    this.dx += fx
    this.dy += fy
    // || 0 turns −0 into 0.
    const dx = Math.trunc(this.dx) || 0
    const dy = Math.trunc(this.dy) || 0
    this.dx -= dx
    this.dy -= dy
    if (dx === 0 && dy === 0) {
      return null
    }
    const limit = (v: number): number => Math.min(Math.max(v, -0x80000000), 0x7fffffff)
    return { dx: limit(dx), dy: limit(dy), mode }
  }
}

/** The parts of a PointerEvent or WheelEvent a pointer binding reads. */
interface PointerEventLike extends Event {
  clientX: number
  clientY: number
  buttons: number
  pointerId: number
  deltaX: number
  deltaY: number
  deltaMode: number
}

interface PointerTarget extends EventTarget {
  getBoundingClientRect(): Box
  setPointerCapture?(pointerId: number): void
  focus?(options?: FocusOptions): void
}

/**
 * bindPointer sends the pointer's moves, buttons and scrolls over
 * element, which shows the screen of size() stretched across its box,
 * such as a canvas whose CSS size scales the screen. A press focuses
 * the element, for a keyboard binding on it; the element keeps the
 * pointer while a button is down; and the remote, not the page, takes
 * right clicks and scrolls.
 */
export function bindPointer(
  element: PointerTarget,
  session: Pick<Session, 'pointer' | 'wheel'>,
  size: () => Size,
  options: { onError?: ErrorHandler } = {},
): Unbind {
  const onError = options.onError ?? defaultOnError
  const wheel = new WheelAccumulator()
  let last = { x: 0, y: 0 }
  const onPointer = (e: Event): void => {
    const ev = e as PointerEventLike
    const screen = size()
    if (screen.width === 0 || screen.height === 0) {
      return
    }
    if (ev.type === 'pointerdown') {
      element.setPointerCapture?.(ev.pointerId)
      // Preventing the default keeps a drag from selecting the page, and
      // may keep the click from focusing the element, which takes the
      // keyboard: it focuses it itself.
      ev.preventDefault()
      element.focus?.({ preventScroll: true })
    }
    last = remotePoint(ev.clientX, ev.clientY, element.getBoundingClientRect(), screen)
    try {
      session.pointer(last.x, last.y, pointerButtons(ev.buttons))
    } catch (err) {
      onError(err)
    }
  }
  const onWheel = (e: Event): void => {
    const ev = e as PointerEventLike
    ev.preventDefault()
    const screen = size()
    const box = element.getBoundingClientRect()
    const scale = box.width > 0 ? screen.width / box.width : 1
    const d = wheel.add(ev.deltaX, ev.deltaY, ev.deltaMode, scale, screen)
    if (d === null) {
      return
    }
    try {
      session.wheel(d.dx, d.dy, d.mode)
    } catch (err) {
      onError(err)
    }
  }
  const onContextMenu = (e: Event): void => e.preventDefault()
  const types = ['pointerdown', 'pointermove', 'pointerup', 'pointercancel'] as const
  for (const t of types) {
    element.addEventListener(t, onPointer)
  }
  element.addEventListener('wheel', onWheel, { passive: false })
  element.addEventListener('contextmenu', onContextMenu)
  return () => {
    for (const t of types) {
      element.removeEventListener(t, onPointer)
    }
    element.removeEventListener('wheel', onWheel)
    element.removeEventListener('contextmenu', onContextMenu)
  }
}

/**
 * bindVisibility tells the server whether the page can be seen, so a
 * hidden tab gets no frames and one frame of the current screen when it
 * shows again.
 */
export function bindVisibility(doc: Document, session: Pick<Session, 'view'>, options: { onError?: ErrorHandler } = {}): Unbind {
  const onError = options.onError ?? defaultOnError
  const onChange = (): void => {
    try {
      session.view(doc.visibilityState !== 'hidden')
    } catch (err) {
      onError(err)
    }
  }
  doc.addEventListener('visibilitychange', onChange)
  return () => doc.removeEventListener('visibilitychange', onChange)
}

/**
 * cursorStyle returns a CSS cursor value that shows a cursor event's
 * image with its hot spot, and none for a hidden pointer. Its pixels are
 * straight alpha, as ImageData's are. Browsers refuse cursor images
 * larger than about 128 pixels, and fall back to the default arrow.
 */
export function cursorStyle(image: CursorImage | null, hotX: number, hotY: number): string {
  if (image === null || image.width === 0 || image.height === 0) {
    return 'none'
  }
  const canvas = document.createElement('canvas')
  canvas.width = image.width
  canvas.height = image.height
  const ctx = canvas.getContext('2d')
  if (ctx === null) {
    return 'default'
  }
  ctx.putImageData(new ImageData(image.pixels as Uint8ClampedArray<ArrayBuffer>, image.width, image.height), 0, 0)
  return `url(${canvas.toDataURL('image/png')}) ${hotX} ${hotY}, default`
}
