// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The share panel: the owner mints a link that joins the session as a
// viewer or read-only (POST /api/v1/sessions/{id}/shares).

import { type FormEvent, type ReactElement, useId, useState } from 'react'

import { type AccessToken, type Api, ApiError, type ShareRole, errorMessage } from '../lib/api'
import { pageURL, shareLink } from '../lib/fragment'
import { copyText } from './dom'

export interface SharePanelProps {
  api: Api
  token: AccessToken
  sessionId: string
  onClose: () => void
  onSignIn: (message: string) => void
}

// The lifetimes offered, within API v1's 60 seconds to 12 hours.
const lifetimes: readonly (readonly [number, string])[] = [
  [600, '10 minutes'],
  [3600, '1 hour'],
  [4 * 3600, '4 hours'],
  [12 * 3600, '12 hours'],
]

export function SharePanel({ api, token, sessionId, onClose, onSignIn }: SharePanelProps): ReactElement {
  const [role, setRole] = useState<ShareRole>('read-only')
  const [lifetime, setLifetime] = useState(3600)
  const [link, setLink] = useState('')
  const [status, setStatus] = useState('')
  const [busy, setBusy] = useState(false)
  const id = useId()

  const create = async (e: FormEvent<HTMLFormElement>): Promise<void> => {
    e.preventDefault()
    setBusy(true)
    setStatus('')
    try {
      const share = await api.share(token, sessionId, role, lifetime)
      setLink(shareLink(pageURL(location), sessionId, share.token))
      setStatus(`Anyone with this link can join as ${share.role === 'viewer' ? 'a viewer' : 'read-only'} until it expires or the session ends.`)
    } catch (err) {
      if (err instanceof ApiError && err.signedOut) {
        onSignIn(errorMessage(err))
        return
      }
      setStatus(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }
  const copy = async (): Promise<void> => {
    try {
      await copyText(link)
      setStatus('Copied.')
    } catch {
      setStatus('The browser did not let the page write the clipboard. Select the link and copy it instead.')
    }
  }

  return (
    <form className="panel" aria-labelledby={`${id}-title`} aria-busy={busy} onSubmit={(e) => void create(e)} onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <h2 id={`${id}-title`}>Share this session</h2>
      <fieldset>
        <legend>Others join</legend>
        <label className="choice">
          <input type="radio" name="role" checked={role === 'read-only'} onChange={() => setRole('read-only')} autoFocus />
          Read-only: they watch
        </label>
        <label className="choice">
          <input type="radio" name="role" checked={role === 'viewer'} onChange={() => setRole('viewer')} />
          Viewer: they also type, point and use the clipboard
        </label>
      </fieldset>
      <label>
        Link expires after
        <select value={lifetime} onChange={(e) => setLifetime(Number(e.target.value))}>
          {lifetimes.map(([s, name]) => (
            <option key={s} value={s}>
              {name}
            </option>
          ))}
        </select>
      </label>
      {link !== '' && (
        <label>
          Link
          <input value={link} readOnly spellCheck={false} onFocus={(e) => e.currentTarget.select()} />
        </label>
      )}
      <div className="actions">
        <button type="submit" className="primary" disabled={busy}>
          {link === '' ? 'Create link' : 'Create another'}
        </button>
        {link !== '' && (
          <button type="button" onClick={() => void copy()}>
            Copy link
          </button>
        )}
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
      <p className="muted" role="status">
        {status}
      </p>
    </form>
  )
}
