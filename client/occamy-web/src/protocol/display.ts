// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Display shows a graphical session. The session applies each frame to
// its framebuffer in memory when next() delivers it; the display then
// waits for the next animation frame and presents what changed to a
// canvas, in one task, so the browser never paints half a frame, and
// comes back to next(), which acknowledges the frame as shown.

import type { EndEvent, Rect, SessionEvent } from './events'
import { SessionClass } from './messages'
import type { Framebuffer, Session } from './session'

/** Surface is where a display presents frames. */
export interface Surface {
  /**
   * present shows fb, whose dirty rectangles changed since the last call:
   * at its size, which a resize changes, with every changed pixel.
   */
  present(fb: Readonly<Framebuffer>, dirty: Rect[]): void
}

// Beyond this many rectangles, a present puts their bounding box once.
const MAX_RECTS = 64

/** coalesce returns rectangles that cover dirty: dirty itself, or its bounding box. */
export function coalesce(dirty: Rect[]): Rect[] {
  if (dirty.length <= MAX_RECTS) {
    return dirty
  }
  let [x0, y0, x1, y1] = [Infinity, Infinity, 0, 0]
  for (const r of dirty) {
    x0 = Math.min(x0, r.x)
    y0 = Math.min(y0, r.y)
    x1 = Math.max(x1, r.x + r.width)
    y1 = Math.max(y1, r.y + r.height)
  }
  return [{ x: x0, y: y0, width: x1 - x0, height: y1 - y0 }]
}

type Context2D = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D

/**
 * CanvasSurface presents frames to a canvas, or to an OffscreenCanvas,
 * such as one a Worker drew into after transferControlToOffscreen. The
 * canvas takes the screen's size in device pixels; scaling it for the
 * page is the caller's, through CSS.
 */
export class CanvasSurface implements Surface {
  readonly canvas: HTMLCanvasElement | OffscreenCanvas
  private readonly ctx: Context2D
  private image: ImageData | null = null

  constructor(canvas: HTMLCanvasElement | OffscreenCanvas) {
    this.canvas = canvas
    // Called on each type alone: on the union, TypeScript resolves
    // getContext('2d') to any rendering context.
    const ctx: Context2D | null =
      'transferControlToOffscreen' in canvas
        ? canvas.getContext('2d', { alpha: false })
        : canvas.getContext('2d', { alpha: false })
    if (ctx === null) {
      throw new Error('occamy: the canvas has no 2D context')
    }
    this.ctx = ctx
  }

  present(fb: Readonly<Framebuffer>, dirty: Rect[]): void {
    if (fb.width === 0 || fb.height === 0) {
      return
    }
    let rects = dirty
    if (this.canvas.width !== fb.width || this.canvas.height !== fb.height) {
      // Resizing clears the canvas, so it happens here, in the same task
      // as the frame that redraws it.
      this.canvas.width = fb.width
      this.canvas.height = fb.height
      rects = [{ x: 0, y: 0, width: fb.width, height: fb.height }]
    }
    if (this.image?.data !== fb.data) {
      // ImageData wraps the framebuffer's pixels without copying them;
      // putImageData copies the dirty rectangles out.
      this.image = new ImageData(fb.data as Uint8ClampedArray<ArrayBuffer>, fb.width, fb.height)
    }
    for (const r of coalesce(rects)) {
      this.ctx.putImageData(this.image, 0, 0, r.x, r.y, r.width, r.height)
    }
  }
}

/**
 * MemorySurface keeps what was presented in memory, rectangle by
 * rectangle, as a canvas would: for tests, and for callers that read
 * pixels rather than show them.
 */
export class MemorySurface implements Surface {
  width = 0
  height = 0
  pixels = new Uint8ClampedArray(0)
  presents = 0

  present(fb: Readonly<Framebuffer>, dirty: Rect[]): void {
    let rects = dirty
    if (fb.width !== this.width || fb.height !== this.height) {
      this.width = fb.width
      this.height = fb.height
      this.pixels = new Uint8ClampedArray(4 * fb.width * fb.height)
      rects = [{ x: 0, y: 0, width: fb.width, height: fb.height }]
    }
    for (const r of coalesce(rects)) {
      for (let y = r.y; y < r.y + r.height; y++) {
        const at = 4 * (y * fb.width + r.x)
        this.pixels.set(fb.data.subarray(at, at + 4 * r.width), at)
      }
    }
    this.presents++
  }
}

/**
 * nextAnimationFrame resolves at the next animation frame, or at once
 * where there are none: in a hidden page, which gets none until it
 * shows again, and where there is no requestAnimationFrame. A page that
 * hides while it waits stops waiting. Presenting at once in a hidden
 * page keeps what follows a frame, such as a prompt, the clipboard or
 * the end, from waiting until the page shows.
 */
export function nextAnimationFrame(): Promise<void> {
  if (typeof requestAnimationFrame !== 'function') {
    return Promise.resolve()
  }
  const doc = typeof document !== 'undefined' ? document : null
  if (doc?.hidden === true) {
    return Promise.resolve()
  }
  return new Promise((resolve) => {
    let done = false
    const finish = (): void => {
      if (!done) {
        done = true
        doc?.removeEventListener('visibilitychange', onChange)
        resolve()
      }
    }
    const onChange = (): void => {
      if (doc?.hidden === true) {
        finish()
      }
    }
    doc?.addEventListener('visibilitychange', onChange)
    // The continuation runs in the callback's microtask checkpoint,
    // before the browser paints.
    requestAnimationFrame(finish)
  })
}

export interface DisplayOptions {
  /** Waits for the next chance to present; nextAnimationFrame by default. */
  nextPaint?: () => Promise<void>
  /**
   * Receives every event, a frame after it is presented. The display
   * waits for a promise it returns before it asks for the next event,
   * which acknowledges the frame: a terminal's caller can resolve it once
   * its emulator has drawn the frame's data.
   */
  onEvent?: (ev: SessionEvent) => void | Promise<void>
}

/** Display runs a session's events, presenting a graphical session's frames to a surface. */
export class Display {
  private readonly session: Session
  private readonly surface: Surface | null
  private readonly options: DisplayOptions

  /** surface is null for a terminal session, whose frames are the caller's emulator's. */
  constructor(session: Session, surface: Surface | null, options: DisplayOptions = {}) {
    this.session = session
    this.surface = surface
    this.options = options
  }

  /** run takes events until the session ends, and resolves with its end. */
  async run(signal?: AbortSignal): Promise<EndEvent> {
    const paint = this.options.nextPaint ?? nextAnimationFrame
    const graphical = this.session.sessionClass === SessionClass.Graphical
    for (;;) {
      const ev = await this.session.next(signal)
      if (ev.type === 'frame' && graphical && this.surface !== null) {
        await paint()
        this.surface.present(this.session.framebuffer, ev.dirty)
      }
      await this.options.onEvent?.(ev)
      if (ev.type === 'end') {
        return ev
      }
    }
  }
}
