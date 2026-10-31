// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell's screens: sign-in, the connection list, and a session, as
// src/lib/app.ts decides them. The access token lives in memory only,
// for its lifetime of minutes; a new tab or a reload signs in again, as
// occamyd issues no refresh tokens, and a reload over the owner's session
// joins it again after. An owner whose token expired signs in over the
// session, which streams on underneath.

import { type ReactElement, useReducer } from 'react'

import type { Api, Connection } from '../lib/api'
import { dropSession, initialState, reduce, signInOver } from '../lib/app'
import type { Start } from '../lib/targets'
import { Connections } from './Connections'
import { SessionView } from './SessionView'
import { SignIn } from './SignIn'

export interface AppProps {
  api: Api
  start: Start
  storage: Storage | undefined
}

export function App({ api, start, storage }: AppProps): ReactElement {
  const [state, dispatch] = useReducer(reduce, start, initialState)
  const { token, screen, signIn } = state

  const form =
    signIn === null ? null : (
      <SignIn
        api={api}
        storage={storage}
        message={signIn.message}
        notice={signIn.notice}
        pending={signIn.exchange}
        resume={signIn.pending}
        onSignedIn={(t) => dispatch({ type: 'signed-in', token: t })}
        onCancel={signInOver(state) ? () => dispatch({ type: 'cancel-sign-in' }) : undefined}
        onSkip={() => {
          dropSession(storage)
          dispatch({ type: 'skip-return' })
        }}
      />
    )

  if (screen.kind === 'session') {
    const view = screen.view
    return (
      <>
        <SessionView
          key={view}
          api={api}
          token={token}
          target={screen.target}
          owner={screen.owner}
          action={state.action?.view === view ? state.action : null}
          storage={storage}
          onLeave={() => dispatch({ type: 'leave', view })}
          onSignIn={(message, next) => dispatch({ type: 'sign-in-again', view, message, pending: { target: next.target, view: next.here ? view : null, then: next.then, owner: next.owner } })}
          onRejoin={(target) => dispatch({ type: 'open', target, now: Date.now() })}
        />
        {form}
      </>
    )
  }
  if (form !== null || token === null || screen.kind === 'start') {
    return (
      form ?? (
        <SignIn
          api={api}
          storage={storage}
          message=""
          notice=""
          pending={null}
          resume={null}
          onSignedIn={(t) => dispatch({ type: 'signed-in', token: t })}
        />
      )
    )
  }
  return (
    <Connections
      api={api}
      token={token}
      onOpen={(c: Connection) => dispatch({ type: 'open', target: { kind: 'connection', id: c.id, name: c.name }, now: Date.now() })}
      onSignOut={(message = '') => {
        dropSession(storage)
        dispatch({ type: 'sign-out', message })
      }}
    />
  )
}
