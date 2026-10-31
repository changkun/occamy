// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// A terminal session in xterm.js: the server's screen diffs are escape
// sequences, its grid sets the emulator's, and what the user types goes
// back as terminal input. The page loads it, and xterm.js, only for a
// terminal session.

import '@xterm/xterm/css/xterm.css'

import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { type ReactElement, useEffect, useRef } from 'react'

import type { SessionController } from '../lib/controller'
import type { Size } from '../lib/layout'
import type { Abilities } from '../lib/roles'
import { TERMINAL_FONT, TERMINAL_FONT_SIZE } from './dom'

// How long the window stays one size before the owner asks for its grid.
const RESIZE_DELAY = 300

export interface TerminalViewProps {
  controller: SessionController
  can: Abilities
  box: Size
  label: string
}

// bytes turns xterm.js's binary input, one character per byte, into bytes.
function bytes(s: string): Uint8Array {
  return Uint8Array.from(s, (c) => c.charCodeAt(0) & 0xff)
}

export default function TerminalView({ controller, can, box, label }: TerminalViewProps): ReactElement {
  const ref = useRef<HTMLDivElement>(null)
  const fitRef = useRef<FitAddon | null>(null)

  useEffect(() => {
    const el = ref.current
    if (el === null) {
      return
    }
    const term = new Terminal({
      fontFamily: TERMINAL_FONT,
      fontSize: TERMINAL_FONT_SIZE,
      disableStdin: !can.input,
      cursorBlink: can.input,
      scrollback: 1000,
      allowProposedApi: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    term.textarea?.setAttribute('aria-label', label)
    fitRef.current = fit
    const subs = can.input
      ? [term.onData((d) => controller.sendTerminalInput(d)), term.onBinary((d) => controller.sendTerminalInput(bytes(d)))]
      : []
    const detach = controller.showTerminal({
      reset: () => term.reset(),
      write: (data) => new Promise<void>((resolve) => term.write(data, resolve)),
      resize: (cols, rows) => term.resize(cols, rows),
    })
    term.focus()
    return () => {
      detach()
      for (const s of subs) {
        s.dispose()
      }
      fitRef.current = null
      term.dispose()
    }
  }, [controller, can.input, label])

  useEffect(() => {
    if (!can.resize) {
      return
    }
    const timer = setTimeout(() => {
      const d = fitRef.current?.proposeDimensions()
      if (d !== undefined && Number.isFinite(d.cols) && Number.isFinite(d.rows)) {
        controller.requestResize(d.cols, d.rows)
      }
    }, RESIZE_DELAY)
    return () => clearTimeout(timer)
  }, [controller, can.resize, box])

  return <div ref={ref} className="terminal" />
}
