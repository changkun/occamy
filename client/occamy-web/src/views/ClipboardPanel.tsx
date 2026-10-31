// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The clipboard panel: the remote clipboard's text, to copy, and text to
// put on the remote's clipboard, for the times the browser does not let
// the page reach the local clipboard on its own.

import { type FormEvent, type ReactElement, useId, useState } from 'react'

import type { SessionController } from '../lib/controller'
import type { Abilities } from '../lib/roles'
import { copyText, pasteLocalClipboard } from './dom'

export interface ClipboardPanelProps {
  controller: SessionController
  /** The remote clipboard's text, or null before any. */
  remote: string | null
  can: Abilities
  onClose: () => void
}

export function ClipboardPanel({ controller, remote, can, onClose }: ClipboardPanelProps): ReactElement {
  const [text, setText] = useState(remote ?? '')
  const [status, setStatus] = useState('')
  const id = useId()

  const send = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault()
    setStatus(controller.sendClipboard(text) ? 'Sent to the remote clipboard.' : 'The remote clipboard is not open to you.')
  }
  const paste = async (): Promise<void> => {
    try {
      setText(await pasteLocalClipboard())
      setStatus('')
    } catch {
      setStatus('The browser did not let the page read the clipboard. Paste into the box instead.')
    }
  }
  const copy = async (): Promise<void> => {
    try {
      await copyText(text)
      setStatus('Copied.')
    } catch {
      setStatus('The browser did not let the page write the clipboard. Select the text and copy it instead.')
    }
  }

  return (
    <form className="panel" aria-labelledby={`${id}-title`} onSubmit={send} onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <h2 id={`${id}-title`}>Clipboard</h2>
      <p className="muted">
        {can.clipboard ? 'The remote clipboard. Edit it and send, or copy it.' : 'The remote clipboard, to copy. Your role cannot change it.'}
      </p>
      <textarea
        aria-label="Clipboard text"
        value={text}
        rows={6}
        readOnly={!can.clipboard}
        spellCheck={false}
        autoFocus
        onChange={(e) => setText(e.target.value)}
      />
      <div className="actions">
        {can.clipboard && (
          <>
            <button type="submit" className="primary">
              Send
            </button>
            <button type="button" onClick={() => void paste()}>
              Paste local
            </button>
          </>
        )}
        <button type="button" onClick={() => void copy()}>
          Copy
        </button>
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
