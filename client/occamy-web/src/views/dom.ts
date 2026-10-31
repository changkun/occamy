// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// What the views need of the browser beyond React: an element's size,
// and the local clipboard, which browsers guard.

import { type RefObject, useEffect, useState } from 'react'

import type { Size } from '../lib/layout'

/** The terminal's font, which the hello's estimate of a cell and the emulator share. */
export const TERMINAL_FONT = 'ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace'
export const TERMINAL_FONT_SIZE = 14

/** formText is a form field's text, or empty. */
export function formText(form: FormData, name: string): string {
  const v = form.get(name)
  return typeof v === 'string' ? v : ''
}

/** useBoxSize is the element's content size in CSS pixels, as it changes. */
export function useBoxSize(ref: RefObject<HTMLElement | null>): Size {
  const [size, setSize] = useState<Size>({ width: 0, height: 0 })
  useEffect(() => {
    const el = ref.current
    if (el === null) {
      return
    }
    const set = (width: number, height: number): void =>
      setSize((s) => (s.width === width && s.height === height ? s : { width, height }))
    const r = el.getBoundingClientRect()
    set(r.width, r.height)
    const observer = new ResizeObserver((entries) => {
      const box = entries[entries.length - 1].contentRect
      set(box.width, box.height)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [ref])
  return size
}

/**
 * writeLocalClipboard puts the remote's clipboard text on the local one.
 * Browsers allow it only to a focused page, so it rejects otherwise, and
 * the clipboard panel shows the text to copy by hand.
 */
export async function writeLocalClipboard(text: string): Promise<void> {
  if (!document.hasFocus()) {
    throw new Error('the page has no focus')
  }
  await navigator.clipboard.writeText(text)
}

/**
 * readLocalClipboard reads the local clipboard's text where the user has
 * let the page read it already, and is null otherwise: it never asks,
 * since a prompt on every focus would be worse than pasting by hand.
 */
export async function readLocalClipboard(): Promise<string | null> {
  try {
    const p = await navigator.permissions.query({ name: 'clipboard-read' as PermissionName })
    if (p.state !== 'granted') {
      return null
    }
    return await navigator.clipboard.readText()
  } catch {
    // Firefox and Safari know no clipboard-read permission.
    return null
  }
}

/** pasteLocalClipboard reads the local clipboard on the user's click, which may ask. */
export async function pasteLocalClipboard(): Promise<string> {
  return navigator.clipboard.readText()
}

/** copyText puts text on the local clipboard on the user's click. */
export async function copyText(text: string): Promise<void> {
  await navigator.clipboard.writeText(text)
}
