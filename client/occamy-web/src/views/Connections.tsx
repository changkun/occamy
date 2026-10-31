// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The connections the user may open (GET /api/v1/connections).

import { type ReactElement, useEffect, useEffectEvent, useState } from 'react'

import { type AccessToken, type Api, ApiError, type Connection, errorMessage, subjectOf } from '../lib/api'

export interface ConnectionsProps {
  api: Api
  token: AccessToken
  onOpen: (c: Connection) => void
  /** Signs out, with why if occamyd refused the token. */
  onSignOut: (message?: string) => void
}

const protocols: Record<string, string> = { ssh: 'SSH', vnc: 'VNC', rdp: 'RDP', telnet: 'Telnet' }

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
        <span className="brand">Occamy</span>
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
