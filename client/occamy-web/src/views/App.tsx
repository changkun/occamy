// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The shell's screens: sign-in, the connection list, and a session. The
// access token lives in memory only, for its lifetime of minutes; a new
// tab or a reload signs in again, as occamyd issues no refresh tokens.

import { type ReactElement, useState } from 'react'

import type { AccessToken, Api, Connection } from '../lib/api'
import type { Start, Target } from '../lib/targets'
import { Connections } from './Connections'
import { SessionView } from './SessionView'
import { SignIn } from './SignIn'

type Screen =
  | { kind: 'sign-in'; message: string; pending: Promise<AccessToken> | null }
  | { kind: 'connections' }
  | { kind: 'session'; target: Target; attempt: number }

function firstScreen(start: Start): Screen {
  switch (start.kind) {
    case 'join':
      return { kind: 'session', target: { kind: 'share', sessionId: start.sessionId, token: start.token }, attempt: 0 }
    case 'oidc':
      return { kind: 'sign-in', message: '', pending: start.token }
    case 'oidc-error':
      return { kind: 'sign-in', message: start.message, pending: null }
    default:
      return { kind: 'sign-in', message: '', pending: null }
  }
}

export interface AppProps {
  api: Api
  start: Start
  storage: Storage | undefined
}

export function App({ api, start, storage }: AppProps): ReactElement {
  const [token, setToken] = useState<AccessToken | null>(null)
  const [screen, setScreen] = useState<Screen>(() => firstScreen(start))

  const signIn = (message = ''): void => {
    setToken(null)
    setScreen({ kind: 'sign-in', message, pending: null })
  }
  const signedIn = (t: AccessToken): void => {
    setToken(t)
    setScreen({ kind: 'connections' })
  }
  const open = (target: Target, attempt = 0): void => {
    if (target.kind !== 'share' && (token === null || token.expires <= Date.now())) {
      signIn('Your sign-in has expired. Sign in again.')
      return
    }
    setScreen({ kind: 'session', target, attempt })
  }

  switch (screen.kind) {
    case 'connections':
      if (token !== null) {
        return (
          <Connections
            api={api}
            token={token}
            onOpen={(c: Connection) => open({ kind: 'connection', id: c.id, name: c.name })}
            onSignOut={signIn}
          />
        )
      }
      break
    case 'session':
      return (
        <SessionView
          key={screen.attempt}
          api={api}
          token={token}
          target={screen.target}
          onLeave={() => (token !== null ? setScreen({ kind: 'connections' }) : signIn())}
          onSignIn={signIn}
          onRejoin={(target) => open(target, screen.attempt + 1)}
        />
      )
    default:
      break
  }
  return (
    <SignIn
      api={api}
      storage={storage}
      message={screen.kind === 'sign-in' ? screen.message : ''}
      pending={screen.kind === 'sign-in' ? screen.pending : null}
      onSignedIn={signedIn}
    />
  )
}
