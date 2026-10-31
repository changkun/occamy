// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// SessionController runs one stream for a view: it connects through the
// client core, runs the core's Display, binds input by role, and keeps
// what the page shows (title, viewers, prompts, clipboard, the end) as
// an immutable state that React reads through useSyncExternalStore. It
// imports no framework, so the tests drive it in Node against occamyd's
// stream code.

import {
  CloseCode,
  CloseError,
  ClosedError,
  type ConnectOptions,
  type CursorEvent,
  Display,
  type PromptEvent,
  ReadOnlyError,
  Role,
  type Session,
  SessionClass,
  type SessionEvent,
  type Surface,
  type TileDecoder,
  type Unbind,
  type WebSocketConstructor,
  bindKeyboard,
  bindPointer,
  bindVisibility,
  connect,
} from '../protocol'
import { type Ending, type EndContext, describeEnd, describeFailure } from './ending'
import { MAX_COLS, MAX_ROWS, MAX_SIDE, type Size } from './layout'
import { type Abilities, abilities } from './roles'
import { MAX_LABEL, TEXT_MIME, clip, clipboardText } from './text'

/** Another viewer of the session. */
export interface Viewer {
  id: number
  role: Role
  /** The viewer's user name, clipped, or empty for one joined by a share. */
  name: string
}

export interface SessionState {
  /** connecting until welcome, open until the end, then ended. */
  phase: 'connecting' | 'open' | 'ended'
  /** This client's role, once open. */
  role: Role | null
  sessionClass: SessionClass | null
  /** The session's ID, from welcome; empty before. */
  sessionId: string
  /** The remote's title, clipped; empty until it sends one. */
  title: string
  /** The other viewers, in the order they joined. */
  viewers: readonly Viewer[]
  /** The owner's open credential prompts, oldest first. */
  prompts: readonly PromptEvent[]
  /** The remote clipboard's latest text, as the remote said or this client sent, or null before any. */
  clipboard: string | null
  /**
   * The clipboard panel's text: the remote clipboard's, until the user
   * edits it, after which a new remote clipboard leaves it alone. Text
   * sent is the remote clipboard's.
   */
  draft: string
  /** How many times the bell rang. */
  bells: number
  /** The remote pointer's image, for cursorStyle. */
  cursor: CursorEvent | null
  /** The screen's size: pixels for a graphical session, cells for a terminal; zero before the first frame. */
  size: Size
  /** Whether the first frame has been shown. */
  shown: boolean
  /** Why it ended, once ended; null for an open the user cancelled. */
  ending: Ending | null
  /**
   * The close code the stream ended with, as its end event gives it,
   * such as 4002 once occamyd ended the session for everyone, or that
   * refused it, such as 4001; 0 until then, for a stream the page
   * closed, and for one that failed to open without a code, as when
   * occamyd could not be reached.
   */
  code: number
}

/** What a stream asks the remote for in its hello. */
export interface HelloSize {
  /** Device pixels, for a graphical session. */
  width: number
  height: number
  /** Cells, for a terminal; 0 for the server's default. */
  cols: number
  rows: number
  /** Device pixels per CSS pixel in hundredths; 0 for none. */
  pixelRatio: number
}

/** TerminalSink is the terminal emulator a terminal session writes to, such as xterm.js. */
export interface TerminalSink {
  /** reset clears the emulator, before a snapshot. */
  reset(): void
  /** write feeds it escape sequences, resolving once they are drawn. */
  write(data: Uint8Array): Promise<void>
  /** resize sets its grid to the server's, in cells. */
  resize(cols: number, rows: number): void
}

/** The element a graphical session's pointer and keys bind to, such as its canvas. */
export type InputElement = Parameters<typeof bindPointer>[0]

export interface ControllerOptions {
  /** The stream route's WebSocket URL. */
  url: string
  /** The access token, session token or share the route takes. */
  token: string
  /** Joined with a share link rather than as a signed-in user. */
  shared: boolean
  /** The page's document, for its visibility; without one, the client stays visible. */
  document?: Document
  /** The page's window, for releasing keys on blur and closing on pagehide. */
  window?: EventTarget
  /**
   * Puts the remote's clipboard on the local one, for a role that gives
   * input; it may refuse, as browsers do without focus. A read-only
   * viewer's local clipboard is left alone: what someone else copied
   * there would replace what the viewer copied, unasked.
   */
  writeClipboard?: (text: string) => Promise<void>
  /** connect, or a stand-in. */
  connect?: (url: string, token: string, options: ConnectOptions) => Promise<Session>
  WebSocket?: WebSocketConstructor
  decoder?: TileDecoder
}

const initial: SessionState = {
  phase: 'connecting',
  role: null,
  sessionClass: null,
  sessionId: '',
  title: '',
  viewers: [],
  prompts: [],
  clipboard: null,
  draft: '',
  bells: 0,
  cursor: null,
  size: { width: 0, height: 0 },
  shown: false,
  ending: null,
  code: 0,
}

/**
 * overlayOf says what covers the session's screen: the connecting
 * notice until the first frame shows, why the session ended, or nothing,
 * as for a session the user left, of which there is nothing to tell.
 */
export function overlayOf(state: SessionState): 'connecting' | 'ended' | null {
  if (state.phase === 'connecting' || (state.phase === 'open' && !state.shown)) {
    return 'connecting'
  }
  if (state.phase === 'ended' && state.ending !== null && !state.ending.quiet) {
    return 'ended'
  }
  return null
}

const nothing: Abilities = abilities(Role.ReadOnly)

// report passes on what input fails with, but for a read-only viewer's
// input and input after the end, which go nowhere.
function report(err: unknown): void {
  if (err instanceof ClosedError || err instanceof ReadOnlyError) {
    return
  }
  if (typeof globalThis.reportError === 'function') {
    globalThis.reportError(err)
  }
}

function safely(f: () => unknown): void {
  try {
    f()
  } catch (err) {
    report(err)
  }
}

export class SessionController {
  private readonly options: ControllerOptions
  private state: SessionState = initial
  private readonly listeners = new Set<() => void>()
  private abort = new AbortController()
  private session: Session | null = null
  private can: Abilities = nothing
  private unbinds: Unbind[] = []
  private terminal: TerminalSink | null = null
  private running = false
  // The clipboard text the remote and this client last agreed on, so
  // that a local clipboard that only echoes the remote's is not sent back.
  private lastClipboard: string | null = null
  private lastResize: Size = { width: 0, height: 0 }

  constructor(options: ControllerOptions) {
    this.options = options
  }

  /** subscribe calls listener whenever the state changes, until the returned function is called. */
  readonly subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** getState returns the current state, the same object until it changes. */
  readonly getState = (): SessionState => this.state

  /** What this client may do, by its role; nothing until the stream is open. */
  get abilities(): Abilities {
    return this.can
  }

  private update(patch: Partial<SessionState>): void {
    this.state = { ...this.state, ...patch }
    for (const l of this.listeners) {
      l()
    }
  }

  private context(): EndContext {
    return { shared: this.options.shared, sessionId: this.state.sessionId }
  }

  private hidden(): boolean {
    return this.options.document?.visibilityState === 'hidden'
  }

  /**
   * open connects, asking for size, and resolves once the stream is open
   * or has failed, as the state says. dispose cancels it; open may be
   * called again after dispose, as React does with effects in
   * development.
   */
  async open(size: HelloSize): Promise<void> {
    this.abort = new AbortController()
    const signal = this.abort.signal
    this.update({ ...initial })
    // React runs an effect twice in development, cleaning up the first
    // at once: waiting a microtask lets that cleanup cancel this open
    // before a WebSocket starts.
    await Promise.resolve()
    if (signal.aborted) {
      return
    }
    const hidden = this.hidden()
    let s: Session
    try {
      s = await (this.options.connect ?? connect)(this.options.url, this.options.token, {
        width: Math.min(size.width, MAX_SIDE),
        height: Math.min(size.height, MAX_SIDE),
        cols: Math.min(size.cols, MAX_COLS),
        rows: Math.min(size.rows, MAX_ROWS),
        pixelRatio: size.pixelRatio,
        hidden,
        signal,
        WebSocket: this.options.WebSocket,
        decoder: this.options.decoder,
      })
    } catch (err) {
      if (!signal.aborted) {
        this.finish(describeFailure(err, this.context()), err instanceof CloseError ? err.code : 0)
      }
      return
    }
    if (signal.aborted) {
      void s.close()
      return
    }
    this.session = s
    this.can = abilities(s.role)
    this.lastResize = { width: 0, height: 0 }
    const doc = this.options.document
    if (doc !== undefined) {
      this.unbinds.push(bindVisibility(doc, s))
      if (this.hidden() !== hidden) {
        safely(() => s.view(!this.hidden()))
      }
    }
    const win = this.options.window
    if (win !== undefined) {
      // A page that goes away says so, rather than leaving occamyd to
      // find out.
      const onPageHide = (): void => void s.close()
      win.addEventListener('pagehide', onPageHide)
      this.unbinds.push(() => win.removeEventListener('pagehide', onPageHide))
    }
    this.update({ phase: 'open', role: s.role, sessionClass: s.sessionClass, sessionId: s.sessionId })
  }

  /**
   * showGraphical presents a graphical session to surface, and binds the
   * pointer and keys over element if the role gives input, until the
   * returned function unbinds them. The session runs on the first
   * surface it is shown on; a later call, as React's development effects
   * make with the same canvas, binds the input again.
   */
  showGraphical(surface: Surface, element: InputElement & EventTarget): () => void {
    const s = this.session
    if (s === null || this.state.phase !== 'open') {
      return () => undefined
    }
    const bound: Unbind[] = []
    if (this.can.input) {
      bound.push(bindKeyboard(element, s, { window: this.options.window, onError: report }))
      bound.push(bindPointer(element, s, () => s.framebuffer, { onError: report }))
    }
    this.unbinds.push(...bound)
    if (!this.running) {
      this.run(s, surface)
    }
    return () => {
      for (const u of bound) {
        u()
      }
      this.unbinds = this.unbinds.filter((u) => !bound.includes(u))
    }
  }

  /**
   * showTerminal writes a terminal session to terminal, until the
   * returned function detaches it. A later call writes the rest to
   * another emulator, as React's development effects make before
   * anything is written.
   */
  showTerminal(terminal: TerminalSink): () => void {
    const s = this.session
    if (s === null || this.state.phase !== 'open') {
      return () => undefined
    }
    this.terminal = terminal
    if (!this.running) {
      this.run(s, null)
    }
    return () => {
      if (this.terminal === terminal) {
        this.terminal = null
      }
    }
  }

  // run shows the session until it ends. If showing it fails here, as
  // when the canvas or the terminal emulator throws, the stream is
  // closed with unsupported, a session the client cannot show, so that
  // occamyd does not keep streaming to a page that shows nothing.
  private run(s: Session, surface: Surface | null): void {
    this.running = true
    const signal = this.abort.signal
    new Display(s, surface, { onEvent: (ev) => this.onEvent(s, ev) }).run(signal).catch((err: unknown) => {
      if (!signal.aborted) {
        this.finish(describeFailure(err, this.context()))
        void s.close(undefined, CloseCode.Unsupported, 'the client failed to show the session')
      }
    })
  }

  private onEvent(s: Session, ev: SessionEvent): void | Promise<void> {
    switch (ev.type) {
      case 'frame':
        if (!this.state.shown) {
          this.update({ shown: true })
        }
        return
      case 'resize':
        this.terminal?.resize(ev.width, ev.height)
        this.update({ size: { width: ev.width, height: ev.height } })
        return
      case 'term-snapshot':
        this.terminal?.reset()
        return this.write(ev.data)
      case 'term-data':
        return this.write(ev.data)
      case 'cursor':
        this.update({ cursor: ev })
        return
      case 'cursor-move':
        // The local pointer is where the user points; the remote's move
        // shows once the user moves it.
        return
      case 'clipboard': {
        const text = clipboardText(ev.mime, ev.data)
        if (text !== null) {
          this.agree(text)
          if (this.can.input) {
            this.options.writeClipboard?.(text).catch(() => undefined)
          }
        }
        return
      }
      case 'bell':
        this.update({ bells: this.state.bells + 1 })
        return
      case 'title':
        this.update({ title: clip(ev.title, MAX_LABEL) })
        return
      case 'prompt':
        if (this.can.prompts) {
          this.update({ prompts: [...this.state.prompts, ev] })
        }
        return
      case 'viewer-joined':
        if (ev.viewerId !== s.viewerId) {
          const viewers = this.state.viewers.filter((v) => v.id !== ev.viewerId)
          this.update({ viewers: [...viewers, { id: ev.viewerId, role: ev.role, name: clip(ev.name, MAX_LABEL) }] })
        }
        return
      case 'viewer-left':
        this.update({ viewers: this.state.viewers.filter((v) => v.id !== ev.viewerId) })
        return
      case 'end':
        this.finish(describeEnd(ev.code, ev.reason, this.context()), ev.code)
        return
    }
  }

  // write feeds the terminal, and resolves once it has drawn, so that the
  // frame is acknowledged after it shows. A hidden page draws nothing and
  // waits for nothing, and a page that hides stops waiting: its frames
  // are acknowledged as they arrive, as the core's Display does for a
  // canvas, since occamyd closes a viewer that leaves a frame
  // unacknowledged for its viewer timeout, and a hidden page's timers,
  // which the emulator draws by, may be throttled to a minute.
  private write(data: Uint8Array): Promise<void> | undefined {
    const t = this.terminal
    if (t === null) {
      return undefined
    }
    const drawn = t.write(data)
    const doc = this.options.document
    if (doc === undefined) {
      return drawn
    }
    if (this.hidden()) {
      return undefined
    }
    return new Promise<void>((resolve) => {
      const done = (): void => {
        doc.removeEventListener('visibilitychange', onChange)
        resolve()
      }
      const onChange = (): void => {
        if (doc.visibilityState === 'hidden') {
          done()
        }
      }
      doc.addEventListener('visibilitychange', onChange)
      drawn.then(done, done)
    })
  }

  private finish(ending: Ending | null, code = 0): void {
    if (this.state.phase === 'ended') {
      return
    }
    for (const u of this.unbinds.splice(0)) {
      u()
    }
    this.update({ phase: 'ended', ending, prompts: [], code })
  }

  /** sendTerminalInput sends what the terminal emulator produced, if the role gives input. */
  sendTerminalInput(data: string | Uint8Array): void {
    const s = this.session
    if (s !== null && this.can.input && this.state.phase === 'open') {
      safely(() => s.termInput(data))
    }
  }

  /** sendClipboard puts text on the remote's clipboard; false if the role may not. */
  sendClipboard(text: string): boolean {
    const s = this.session
    if (s === null || !this.can.clipboard || this.state.phase !== 'open') {
      return false
    }
    safely(() => s.clipboard(TEXT_MIME, text))
    this.agree(text)
    return true
  }

  // agree takes text for the remote clipboard's, as the remote said or as
  // this client sent it: the panel's text follows, unless the user has
  // edited it since the two last agreed.
  private agree(text: string): void {
    this.lastClipboard = text
    const { draft, clipboard } = this.state
    this.update({ clipboard: text, draft: draft === (clipboard ?? '') ? text : draft })
  }

  /**
   * syncClipboard sends the local clipboard's text unless the remote has
   * it already, as when it came from the remote; false if it sent none.
   */
  syncClipboard(text: string): boolean {
    if (text === this.lastClipboard) {
      return false
    }
    return this.sendClipboard(text)
  }

  /**
   * requestResize asks the remote for a screen of width × height, pixels
   * or cells, if this client is the owner, unless that was the last size
   * asked for, or the screen is that size and no other size is on its
   * way: a window dragged back to the screen's size while the remote
   * still applies another asks for it again.
   */
  requestResize(width: number, height: number): void {
    const s = this.session
    if (s === null || !this.can.resize || this.state.phase !== 'open' || width <= 0 || height <= 0) {
      return
    }
    const graphical = this.state.sessionClass === SessionClass.Graphical
    const w = Math.min(Math.round(width), graphical ? MAX_SIDE : MAX_COLS)
    const h = Math.min(Math.round(height), graphical ? MAX_SIDE : MAX_ROWS)
    const { size } = this.state
    const asked = this.lastResize
    const settled = (asked.width === 0 && asked.height === 0) || (asked.width === size.width && asked.height === size.height)
    if ((settled && w === size.width && h === size.height) || (w === asked.width && h === asked.height)) {
      return
    }
    this.lastResize = { width: w, height: h }
    safely(() => s.resize(w, h))
  }

  /** setDraft keeps the clipboard panel's text as the user edits it. */
  setDraft(text: string): void {
    if (text !== this.state.draft) {
      this.update({ draft: text })
    }
  }

  /** answerPrompt answers the owner's prompt id, one answer per field. */
  answerPrompt(id: number, answers: string[]): void {
    const s = this.session
    if (s === null || !this.can.prompts) {
      return
    }
    safely(() => s.promptReply(id, answers))
    this.update({ prompts: this.state.prompts.filter((p) => p.id !== id) })
  }

  /**
   * close leaves the session, and cancels an open still under way; for
   * the owner, the session runs on for the owner's grace, a minute.
   */
  async close(): Promise<void> {
    const s = this.session
    this.abort.abort()
    this.finish(describeEnd(CloseCode.Normal, '', this.context()))
    await s?.close()
  }

  /** dispose cancels an open, closes the stream and removes the bindings. */
  dispose(): void {
    this.abort.abort()
    const s = this.session
    this.session = null
    this.running = false
    this.terminal = null
    this.can = nothing
    for (const u of this.unbinds.splice(0)) {
      u()
    }
    if (s !== null) {
      void s.close()
    }
  }
}
