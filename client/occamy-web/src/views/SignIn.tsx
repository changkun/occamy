// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Sign-in: a static user's password (POST /api/v1/auth/token) and, where
// occamyd has auth.oidc, single sign-on through its identity provider.

import { type FormEvent, type ReactElement, useEffect, useEffectEvent, useState } from 'react'

import { type AccessToken, type Api, ApiError, errorMessage } from '../lib/api'
import { pageURL } from '../lib/fragment'
import { challengeOf, loginURL, newVerifier, saveVerifier } from '../lib/oidc'
import { formText } from './dom'

export interface SignInProps {
  api: Api
  storage: Storage | undefined
  /** Why the user is here again, such as an expired sign-in. */
  message: string
  /** An OIDC sign-in coming back: the exchange of its one-time code. */
  pending: Promise<AccessToken> | null
  onSignedIn: (token: AccessToken) => void
}

export function SignIn({ api, storage, message, pending, onSignedIn }: SignInProps): ReactElement {
  const [error, setError] = useState(message)
  const [busy, setBusy] = useState(pending !== null)
  const [oidc, setOidc] = useState(false)
  const signedIn = useEffectEvent(onSignedIn)

  useEffect(() => {
    let live = true
    void api.oidcAvailable().then((available) => {
      if (live) {
        setOidc(available)
      }
    })
    return () => {
      live = false
    }
  }, [api])

  useEffect(() => {
    if (pending === null) {
      return
    }
    let live = true
    pending.then(
      (token) => {
        if (live) {
          signedIn(token)
        }
      },
      (err: unknown) => {
        if (live) {
          setBusy(false)
          setError(err instanceof ApiError && err.code === 'unauthorized' ? 'The sign-in took too long or was used already. Sign in again.' : errorMessage(err))
        }
      },
    )
    return () => {
      live = false
    }
  }, [pending])

  const submit = async (e: FormEvent<HTMLFormElement>): Promise<void> => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    setBusy(true)
    setError('')
    try {
      onSignedIn(await api.signIn(formText(form, 'username'), formText(form, 'password')))
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  const singleSignOn = async (): Promise<void> => {
    setError('')
    try {
      const verifier = newVerifier()
      if (!saveVerifier(storage, verifier)) {
        throw new Error('This browser keeps no session storage for the page, which single sign-on needs.')
      }
      location.assign(loginURL(pageURL(location), await challengeOf(verifier)))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <main className="center">
      <form className="card sign-in" onSubmit={(e) => void submit(e)} aria-busy={busy} aria-describedby={error === '' ? undefined : 'sign-in-error'}>
        <h1>Occamy</h1>
        <p className="lead">Sign in to open a remote desktop or terminal.</p>
        {error !== '' && (
          <p id="sign-in-error" className="error" role="alert">
            {error}
          </p>
        )}
        <label>
          User name
          <input name="username" autoComplete="username" autoCapitalize="none" spellCheck={false} required disabled={busy} autoFocus />
        </label>
        <label>
          Password
          <input name="password" type="password" autoComplete="current-password" required disabled={busy} />
        </label>
        <button type="submit" className="primary" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
        {oidc && (
          <>
            <p className="divider">or</p>
            <button type="button" onClick={() => void singleSignOn()} disabled={busy}>
              Sign in with single sign-on
            </button>
          </>
        )}
      </form>
    </main>
  )
}
