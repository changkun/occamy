// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { MAX_COLS, MAX_ROWS, MAX_SIDE, cellGrid, deviceSize, fitSize, pixelRatio } from './layout'

describe('deviceSize', () => {
  it('asks for the box in device pixels', () => {
    expect(deviceSize({ width: 1280, height: 720 }, 1)).toEqual({ width: 1280, height: 720 })
    expect(deviceSize({ width: 1280, height: 720 }, 2)).toEqual({ width: 2560, height: 1440 })
    expect(deviceSize({ width: 1000.4, height: 600.6 }, 1.5)).toEqual({ width: 1501, height: 901 })
  })

  it('stays within what occamyd asks a remote for', () => {
    expect(deviceSize({ width: 5000, height: 100 }, 2)).toEqual({ width: MAX_SIDE, height: 200 })
    expect(deviceSize({ width: 0.2, height: 0.2 }, 1)).toEqual({ width: 1, height: 1 })
  })

  it('asks for nothing for an empty box', () => {
    expect(deviceSize({ width: 0, height: 720 }, 1)).toEqual({ width: 0, height: 0 })
    expect(deviceSize({ width: NaN, height: 720 }, 1)).toEqual({ width: 0, height: 0 })
  })

  it('takes a ratio that is not a number for 1', () => {
    expect(deviceSize({ width: 800, height: 600 }, NaN)).toEqual({ width: 800, height: 600 })
    expect(deviceSize({ width: 800, height: 600 }, 0)).toEqual({ width: 800, height: 600 })
  })
})

describe('pixelRatio', () => {
  it('is in hundredths', () => {
    expect(pixelRatio(1)).toBe(100)
    expect(pixelRatio(2)).toBe(200)
    expect(pixelRatio(1.25)).toBe(125)
    expect(pixelRatio(2.6666)).toBe(267)
  })

  it('is 0 for none, and within u16', () => {
    expect(pixelRatio(0)).toBe(0)
    expect(pixelRatio(NaN)).toBe(0)
    expect(pixelRatio(1e9)).toBe(0xffff)
  })
})

describe('fitSize', () => {
  it('scales down to fit, keeping the aspect ratio', () => {
    expect(fitSize({ width: 800, height: 800 }, { width: 1600, height: 900 })).toEqual({ width: 800, height: 450 })
    const narrow = fitSize({ width: 800, height: 300 }, { width: 1600, height: 900 })
    expect(narrow.width).toBeCloseTo(1600 / 3)
    expect(narrow.height).toBe(300)
  })

  it('scales up to fill', () => {
    expect(fitSize({ width: 1600, height: 1200 }, { width: 800, height: 600 })).toEqual({ width: 1600, height: 1200 })
  })

  it('is zero until both have a size', () => {
    expect(fitSize({ width: 0, height: 0 }, { width: 800, height: 600 })).toEqual({ width: 0, height: 0 })
    expect(fitSize({ width: 800, height: 600 }, { width: 0, height: 0 })).toEqual({ width: 0, height: 0 })
  })
})

describe('cellGrid', () => {
  it('counts whole cells', () => {
    expect(cellGrid({ width: 800, height: 600 }, { width: 9, height: 17 })).toEqual({ cols: 88, rows: 35 })
  })

  it('has at least one cell, and at most what occamyd asks for', () => {
    expect(cellGrid({ width: 1, height: 1 }, { width: 9, height: 17 })).toEqual({ cols: 1, rows: 1 })
    expect(cellGrid({ width: 1e6, height: 1e6 }, { width: 9, height: 17 })).toEqual({ cols: MAX_COLS, rows: MAX_ROWS })
  })

  it('is zero for a cell without a size', () => {
    expect(cellGrid({ width: 800, height: 600 }, { width: 0, height: 17 })).toEqual({ cols: 0, rows: 0 })
  })
})
