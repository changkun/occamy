// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Sizes: what the shell asks the remote for, in device pixels or
// character cells, and how it scales the remote's screen to the page.

export interface Size {
  width: number
  height: number
}

/** The largest screen side occamyd asks a remote for, in pixels (docs/protocol.md, resize-request). */
export const MAX_SIDE = 8192

/** The largest terminal occamyd asks a remote for, in cells. */
export const MAX_COLS = 1000
export const MAX_ROWS = 500

const clamp = (v: number, lo: number, hi: number): number => Math.min(Math.max(v, lo), hi)

/**
 * deviceSize returns the screen to ask for to fill a box of css pixels at
 * a device pixel ratio: whole device pixels, from 1 to MAX_SIDE a side,
 * or zero for an empty box, which asks for nothing.
 */
export function deviceSize(css: Size, ratio: number): Size {
  const r = Number.isFinite(ratio) && ratio > 0 ? ratio : 1
  if (!(css.width > 0) || !(css.height > 0)) {
    return { width: 0, height: 0 }
  }
  return {
    width: clamp(Math.round(css.width * r), 1, MAX_SIDE),
    height: clamp(Math.round(css.height * r), 1, MAX_SIDE),
  }
}

/** pixelRatio is hello's pixel_ratio: device pixels per CSS pixel in hundredths, or 0 for none. */
export function pixelRatio(ratio: number): number {
  if (!Number.isFinite(ratio) || ratio <= 0) {
    return 0
  }
  return clamp(Math.round(ratio * 100), 1, 0xffff)
}

/**
 * fitSize returns the CSS size that shows a screen whole within a box,
 * keeping its aspect ratio: scaled down to fit, or up to fill, as the
 * Vue client scaled. It is zero until both have a size.
 */
export function fitSize(box: Size, screen: Size): Size {
  if (!(box.width > 0 && box.height > 0 && screen.width > 0 && screen.height > 0)) {
    return { width: 0, height: 0 }
  }
  const scale = Math.min(box.width / screen.width, box.height / screen.height)
  return { width: screen.width * scale, height: screen.height * scale }
}

/**
 * cellGrid returns how many character cells of a size fit in a box: at
 * least one each way, and at most what occamyd asks a remote for.
 */
export function cellGrid(box: Size, cell: Size): { cols: number; rows: number } {
  if (!(cell.width > 0 && cell.height > 0)) {
    return { cols: 0, rows: 0 }
  }
  return {
    cols: clamp(Math.floor(box.width / cell.width), 1, MAX_COLS),
    rows: clamp(Math.floor(box.height / cell.height), 1, MAX_ROWS),
  }
}
