// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// A session: the toolbar, the remote's screen (a canvas, or a terminal
// emulator for a terminal session), the clipboard and share panels, the
// owner's credential prompts, and why the session ended.

import { type ReactElement, Suspense, lazy, useEffect, useEffectEvent, useMemo, useRef, useState, useSyncExternalStore } from 'react'

import { CanvasSurface, Role, SessionClass, cursorStyle } from '../protocol'
import type { AccessToken, Api } from '../lib/api'
import type { SessionAction } from '../lib/app'
import { type SessionController as Controller, SessionController, type SessionState, overlayOf } from '../lib/controller'
import type { Ending } from '../lib/ending'
import { type Size, cellGrid, deviceSize, fitSize, pixelRatio } from '../lib/layout'
import { endSession } from '../lib/owner'
import { type Abilities, abilities, roleName } from '../lib/roles'
import { type Target, streamToken, streamURL } from '../lib/targets'
import { ClipboardPanel } from './ClipboardPanel'
import { PromptDialog } from './PromptDialog'
import { SharePanel, type ShareRequest } from './SharePanel'
import { TERMINAL_FONT, TERMINAL_FONT_SIZE, readLocalClipboard, useBoxSize, writeLocalClipboard } from './dom'

const TerminalView = lazy(() => import('./TerminalView'))

// How long the window stays one size before the owner asks the remote
// for it, so that a drag asks once.
const RESIZE_DELAY = 300

/** What waits for the user to sign in again, as a session view asks. */
export interface SignInNext {
  /** The stream to open after, unless here. */
  target: Target
  /** What the view does after. */
  then: SessionAction | null
  /** The view stays, rather than target opening in its place. */
  here: boolean
}

export interface SessionViewProps {
  api: Api
  token: AccessToken | null
  target: Target
  /** An owner's action that waited for the owner to sign in again, numbered so that it is taken once. */
  action: { seq: number; action: SessionAction } | null
  /** Back to where the user came from. */
  onLeave: () => void
  /** Asks the user to sign in again, over this view. */
  onSignIn: (message: string, next: SignInNext) => void
  /** Join the same session again, as after a lost connection. */
  onRejoin: (target: Target) => void
}

// cellSize estimates a terminal cell, for the hello's columns and rows;
// the emulator measures it exactly once it runs.
function cellSize(): Size {
  const ctx = document.createElement('canvas').getContext('2d')
  if (ctx === null) {
    return { width: 9, height: 17 }
  }
  ctx.font = `${TERMINAL_FONT_SIZE}px ${TERMINAL_FONT}`
  return { width: ctx.measureText('W').width, height: Math.ceil(TERMINAL_FONT_SIZE * 1.2) }
}

export function SessionView({ api, token, target, action, onLeave, onSignIn, onRejoin }: SessionViewProps): ReactElement {
  const [controller] = useState(
    () =>
      new SessionController({
        url: streamURL(location.origin, target),
        token: streamToken(target, token),
        shared: target.kind === 'share',
        document,
        window,
        writeClipboard: writeLocalClipboard,
      }),
  )
  const state = useSyncExternalStore(controller.subscribe, controller.getState)
  const can = abilities(state.role ?? Role.ReadOnly)
  const stageRef = useRef<HTMLDivElement>(null)
  const toolbarRef = useRef<HTMLElement>(null)
  const box = useBoxSize(stageRef)
  const [panel, setPanel] = useState<'clipboard' | 'share' | null>(null)
  const [shareRequest, setShareRequest] = useState<ShareRequest | null>(null)
  // Why End did not end the session for everyone; empty for none.
  const [problem, setProblem] = useState('')
  const handled = useRef(0)
  const name = target.kind === 'share' ? 'Shared session' : target.name
  const signedIn = target.kind !== 'share'
  // The session's ID: from welcome, or the one this view joins again.
  const sessionId = state.sessionId !== '' ? state.sessionId : target.kind === 'session' ? target.sessionId : ''

  useEffect(() => {
    const r = stageRef.current?.getBoundingClientRect() ?? { width: 0, height: 0 }
    const ratio = window.devicePixelRatio
    void controller.open({ ...deviceSize(r, ratio), ...cellGrid(r, cellSize()), pixelRatio: pixelRatio(ratio) })
    return () => controller.dispose()
  }, [controller])

  useEffect(() => {
    document.title = `${state.title !== '' ? state.title : name} · Occamy`
    return () => {
      document.title = 'Occamy'
    }
  }, [state.title, name])

  // The local clipboard goes to the remote when the page gets focus, where
  // the browser lets the page read it without asking.
  useEffect(() => {
    if (state.phase !== 'open' || !can.clipboard) {
      return
    }
    const onFocus = (): void => {
      void readLocalClipboard().then((text) => {
        if (text !== null) {
          controller.syncClipboard(text)
        }
      })
    }
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [controller, state.phase, can.clipboard])

  // Ctrl+Alt+Shift leaves the remote's screen for the toolbar: the screen
  // takes every other key, Tab included.
  useEffect(() => {
    const stage = stageRef.current
    if (stage === null) {
      return
    }
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.ctrlKey && e.altKey && e.shiftKey && (e.key === 'Control' || e.key === 'Alt' || e.key === 'Shift')) {
        e.preventDefault()
        e.stopPropagation()
        toolbarRef.current?.querySelector('button')?.focus()
      }
    }
    stage.addEventListener('keydown', onKeyDown, { capture: true })
    return () => stage.removeEventListener('keydown', onKeyDown, { capture: true })
  }, [])

  // again is the stream that joins this session again: the share, the
  // owner's session, or the connection of a stream that never opened.
  const again = (): Target => {
    if (target.kind === 'share') {
      return { ...target, sessionId: sessionId !== '' ? sessionId : target.sessionId }
    }
    return sessionId !== '' ? { kind: 'session', sessionId, name } : target
  }

  // end ends the session for everyone, as its owner. With an expired
  // sign-in, the owner signs in over the session, and End resumes.
  const end = async (): Promise<void> => {
    setProblem('')
    const outcome = await endSession({ api, token, sessionId, now: Date.now(), close: () => controller.close() })
    switch (outcome.kind) {
      case 'ended':
        onLeave()
        return
      case 'sign-in':
        if (outcome.closed) {
          // Should the owner cancel the sign-in, the closed stream's
          // view still says the session runs on.
          setProblem('The session was not ended for everyone: your sign-in has expired. Its viewers stay until your minute of grace runs out.')
        }
        onSignIn(outcome.message, { target: again(), then: { kind: 'end' }, here: true })
        return
      case 'failed':
        setProblem(outcome.message)
        return
    }
  }

  const leave = async (): Promise<void> => {
    if (can.manage && signedIn) {
      await end()
      return
    }
    await controller.close()
    onLeave()
  }

  // An action that waited for the owner to sign in again is taken once,
  // though React's development effects run twice: End at once, which
  // needs no stream, and a share once the stream is open.
  const take = useEffectEvent((a: { seq: number; action: SessionAction }) => {
    if (a.action.kind === 'end') {
      void end()
      return
    }
    setPanel('share')
    setShareRequest({ seq: a.seq, role: a.action.role, expiresIn: a.action.expiresIn })
  })
  useEffect(() => {
    if (action === null || action.seq === handled.current || (action.action.kind === 'share' && state.phase !== 'open')) {
      return
    }
    handled.current = action.seq
    take(action)
  }, [action, state.phase])

  const togglePanel = (p: 'clipboard' | 'share'): void => {
    setShareRequest(null)
    setPanel(panel === p ? null : p)
  }
  const closePanel = (): void => {
    setShareRequest(null)
    setPanel(null)
  }

  const fullScreen = (): void => {
    if (document.fullscreenElement !== null) {
      void document.exitFullscreen()
    } else {
      void document.documentElement.requestFullscreen().catch(() => undefined)
    }
  }

  const terminal = state.sessionClass === SessionClass.Terminal
  return (
    <div className="session">
      <header className="bar" ref={toolbarRef} aria-label="Session">
        <span className="brand" title={state.title !== '' ? state.title : name}>
          {state.title !== '' ? state.title : name}
        </span>
        {state.role !== null && <span className="tag">{roleName(state.role)}</span>}
        <Viewers state={state} />
        <span className="spacer" />
        {state.phase === 'open' && (
          <>
            <button type="button" aria-pressed={panel === 'clipboard'} onClick={() => togglePanel('clipboard')}>
              Clipboard
            </button>
            {can.manage && token !== null && (
              <button type="button" aria-pressed={panel === 'share'} onClick={() => togglePanel('share')}>
                Share
              </button>
            )}
          </>
        )}
        <button type="button" onClick={fullScreen}>
          Full screen
        </button>
        {state.phase !== 'ended' && (
          <button type="button" className="danger" onClick={() => void leave()}>
            {can.manage ? 'End session' : 'Leave'}
          </button>
        )}
      </header>
      <div className="stage" ref={stageRef}>
        {state.phase !== 'connecting' &&
          (terminal ? (
            <Suspense fallback={null}>
              <TerminalView controller={controller} can={can} box={box} label={`Terminal: ${name}`} />
            </Suspense>
          ) : (
            <Desktop controller={controller} state={state} can={can} box={box} label={name} />
          ))}
        {state.bells > 0 && <div key={state.bells} className="bell" aria-hidden="true" />}
        <p className="sr-only" aria-live="polite">
          {state.bells > 0 ? `Bell ${state.bells}` : ''}
        </p>
        {panel === 'clipboard' && state.phase === 'open' && (
          <ClipboardPanel controller={controller} remote={state.clipboard} draft={state.draft} can={can} onClose={closePanel} />
        )}
        {panel === 'share' && state.phase === 'open' && token !== null && (
          <SharePanel
            api={api}
            token={token}
            sessionId={state.sessionId}
            request={shareRequest}
            onClose={closePanel}
            onSignIn={(message, then) => onSignIn(message, { target: again(), then, here: true })}
          />
        )}
        {problem === '' && overlayOf(state) === 'connecting' && (
          <div className="overlay" role="status">
            <div className="card">
              <p>Connecting to {name}…</p>
              <button type="button" onClick={() => void controller.close().then(onLeave)}>
                Cancel
              </button>
            </div>
          </div>
        )}
        {problem === '' && overlayOf(state) === 'ended' && state.ending !== null && (
          <EndOverlay
            ending={state.ending}
            leaveLabel={signedIn ? 'Back to connections' : 'Close'}
            onLeave={onLeave}
            onSignIn={() => onSignIn(state.ending?.detail ?? '', { target: again(), then: null, here: false })}
            onRejoin={() => onRejoin(again())}
          />
        )}
        {problem !== '' && (
          <EndOverlay
            ending={{ title: 'Not ended for everyone', detail: problem, technical: '', signIn: false, rejoin: false, quiet: false }}
            leaveLabel="Back to connections"
            onLeave={onLeave}
            onSignIn={() => undefined}
            onRejoin={() => undefined}
            onRetry={() => void end()}
          />
        )}
      </div>
      {state.prompts.length > 0 && state.phase === 'open' && (
        <PromptDialog key={state.prompts[0].id} prompt={state.prompts[0]} onAnswer={(answers) => controller.answerPrompt(state.prompts[0].id, answers)} onEnd={() => void leave()} />
      )}
    </div>
  )
}

function Viewers({ state }: { state: SessionState }): ReactElement | null {
  if (state.viewers.length === 0) {
    return null
  }
  const n = state.viewers.length
  return (
    <details className="viewers">
      <summary>
        {n} other {n === 1 ? 'viewer' : 'viewers'}
      </summary>
      <ul>
        {state.viewers.map((v) => (
          <li key={v.id}>
            {v.name !== '' ? v.name : 'Guest'} <span className="muted">({roleName(v.role)})</span>
          </li>
        ))}
      </ul>
    </details>
  )
}

interface DesktopProps {
  controller: Controller
  state: SessionState
  can: Abilities
  box: Size
  label: string
}

// Desktop shows a graphical session on a canvas at the remote's size in
// device pixels, scaled by CSS to fit the stage.
function Desktop({ controller, state, can, box, label }: DesktopProps): ReactElement {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const open = state.phase === 'open'

  useEffect(() => {
    const canvas = canvasRef.current
    if (canvas === null || !open) {
      return
    }
    const detach = controller.showGraphical(new CanvasSurface(canvas), canvas)
    canvas.focus({ preventScroll: true })
    return detach
  }, [controller, open])

  useEffect(() => {
    if (!open || !can.resize) {
      return
    }
    const timer = setTimeout(() => {
      const d = deviceSize(box, window.devicePixelRatio)
      controller.requestResize(d.width, d.height)
    }, RESIZE_DELAY)
    return () => clearTimeout(timer)
  }, [controller, open, can.resize, box])

  const fit = fitSize(box, state.size)
  const cursor = useMemo(() => (state.cursor === null ? 'default' : cursorStyle(state.cursor.image, state.cursor.hotX, state.cursor.hotY)), [state.cursor])
  return (
    <>
      <canvas
        ref={canvasRef}
        className="screen"
        tabIndex={0}
        role="application"
        aria-roledescription="remote desktop"
        aria-label={label}
        aria-describedby="screen-keys"
        style={{ width: `${fit.width}px`, height: `${fit.height}px`, cursor }}
      />
      <p id="screen-keys" className="sr-only">
        {can.input ? 'Keys, pointer and wheel go to the remote while it has focus. Press Control, Alt and Shift together to return to the toolbar.' : 'Read-only: you watch, and your input goes nowhere.'}
      </p>
    </>
  )
}

interface EndOverlayProps {
  ending: Ending
  leaveLabel: string
  onLeave: () => void
  onSignIn: () => void
  onRejoin: () => void
  /** Tries again what failed, such as End; without it, nothing is offered. */
  onRetry?: () => void
}

function EndOverlay({ ending, leaveLabel, onLeave, onSignIn, onRejoin, onRetry }: EndOverlayProps): ReactElement {
  const headingRef = useRef<HTMLHeadingElement>(null)
  useEffect(() => headingRef.current?.focus(), [])
  return (
    <div className="overlay">
      <section className="card" role="alert" aria-labelledby="end-title">
        <h2 id="end-title" ref={headingRef} tabIndex={-1}>
          {ending.title}
        </h2>
        <p>{ending.detail}</p>
        {ending.technical !== '' && (
          <details>
            <summary>Details</summary>
            <p>
              <code>{ending.technical}</code>
            </p>
          </details>
        )}
        <div className="actions">
          {onRetry !== undefined && (
            <button type="button" className="primary" onClick={onRetry}>
              Try again
            </button>
          )}
          {ending.rejoin && (
            <button type="button" className="primary" onClick={onRejoin}>
              Join again
            </button>
          )}
          {ending.signIn && (
            <button type="button" className="primary" onClick={onSignIn}>
              Sign in
            </button>
          )}
          <button type="button" onClick={onLeave}>
            {leaveLabel}
          </button>
        </div>
      </section>
    </div>
  )
}
