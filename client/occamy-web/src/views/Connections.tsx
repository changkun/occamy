// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The connections the user may open (GET /api/v1/connections).

import { type ReactElement, useEffect, useEffectEvent, useState } from 'react'

import logo from '../assets/occamy.png'
import { type AccessToken, type Api, ApiError, type Connection, errorMessage, subjectOf } from '../lib/api'

export interface ConnectionsProps {
  api: Api
  token: AccessToken
  onOpen: (c: Connection) => void
  /** Signs out, with why if occamyd refused the token. */
  onSignOut: (message?: string) => void
}

const protocols: Record<string, string> = { ssh: 'SSH', vnc: 'VNC', rdp: 'RDP', telnet: 'Telnet', kubernetes: 'Kubernetes' }

// desktops are the protocols that show a screen; the others a terminal.
const desktops = new Set(['vnc', 'rdp'])

// Glyph draws a screen for a desktop's connection and a prompt for a
// terminal's, in the text's colour.
function Glyph({ protocol }: { protocol: string }): ReactElement {
  return (
    <span className="glyph" aria-hidden="true">
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        {desktops.has(protocol) ? (
          <>
            <rect x="3" y="4" width="18" height="12" rx="2" />
            <path d="M8 20h8M12 16v4" />
          </>
        ) : (
          <>
            <rect x="3" y="4" width="18" height="16" rx="2" />
            <path d="m7 9 3 3-3 3M13 15h4" />
          </>
        )}
      </svg>
    </span>
  )
}

export function Connections({ api, token, onOpen, onSignOut }: ConnectionsProps): ReactElement {
  const [list, setList] = useState<Connection[] | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const signOut = useEffectEvent(onSignOut)
  const user = subjectOf(token)

  useEffect(() => {
    let live = true
    api.connections(token).then(
      (connections) => {
        if (live) {
          setList(connections)
          setError('')
        }
      },
      (err: unknown) => {
        if (!live) {
          return
        }
        if (err instanceof ApiError && err.signedOut) {
          signOut(errorMessage(err))
        } else {
          setError(errorMessage(err))
        }
      },
    )
    return () => {
      live = false
    }
  }, [api, token, attempt])

  return (
    <div className="page">
      <header className="bar">
        <span className="brand mark">
          <img src={logo} alt="" width={26} height={24} />
          Occamy
        </span>
        <span className="spacer" />
        {user !== '' && <span className="muted">Signed in as {user}</span>}
        <button type="button" onClick={() => onSignOut()}>
          Sign out
        </button>
      </header>
      <main className="list" aria-busy={list === null && error === ''}>
        <div className="list-head">
          <h1>Connections</h1>
          <button type="button" onClick={() => setAttempt((n) => n + 1)}>
            Refresh
          </button>
        </div>
        {error !== '' && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {list === null && error === '' && <p className="muted">Loading…</p>}
        {list !== null && list.length === 0 && (
          <p className="muted">No connection is open to you. occamyd lists connections under connections in its configuration, each with the users and groups it allows.</p>
        )}
        {list !== null && list.length > 0 && (
          <ul className="connections">
            {list.map((c) => (
              <li key={c.id}>
                <button type="button" onClick={() => onOpen(c)}>
                  <Glyph protocol={c.protocol} />
                  <span className="name">{c.name}</span>
                  <span className="tag">{protocols[c.protocol] ?? c.protocol}</span>
                  {c.name !== c.id && <span className="muted id">{c.id}</span>}
                </button>
              </li>
            ))}
          </ul>
        )}
      </main>
    </div>
  )
}
