// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The clipboard panel: the remote clipboard's text, to copy, and text to
// put on the remote's clipboard, for the times the browser does not let
// the page reach the local clipboard on its own. The text being edited
// is the controller's draft, so that a new remote clipboard does not
// throw away what the user typed. What the last action said goes once
// the remote clipboard changes or the user edits the text.

import { type FormEvent, type ReactElement, useId, useState } from 'react'

import { ProtocolError } from '../protocol'
import type { ClipboardSend, SessionController } from '../lib/controller'
import { MAX_LABEL, clip } from '../lib/text'
import type { Abilities } from '../lib/roles'
import { copyText, pasteLocalClipboard } from './dom'

export interface ClipboardPanelProps {
  controller: SessionController
  /** The remote clipboard's text, or null before any. */
  remote: string | null
  /** The text in the panel: the remote's, or what the user made of it. */
  draft: string
  can: Abilities
  onClose: () => void
}

export function ClipboardPanel({ controller, remote, draft: text, can, onClose }: ClipboardPanelProps): ReactElement {
  // What the last action said, with the remote clipboard it was said
  // over: it shows while the remote clipboard stays that.
  const [status, setStatus] = useState<{ text: string; remote: string | null } | null>(null)
  const say = (s: string): void => setStatus({ text: s, remote: controller.getState().clipboard })
  const shown = status !== null && status.remote === remote ? status.text : ''
  // edit puts t in the box, which makes what was said out of date.
  const edit = (t: string): void => {
    controller.setDraft(t)
    setStatus(null)
  }
  const id = useId()

  const send = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault()
    say(sent(controller.sendClipboard(text)))
  }
  const paste = async (): Promise<void> => {
    try {
      edit(await pasteLocalClipboard())
    } catch {
      say('The browser did not let the page read the clipboard. Paste into the box instead.')
    }
  }
  const copy = async (): Promise<void> => {
    try {
      await copyText(text)
      say('Copied.')
    } catch {
      say('The browser did not let the page write the clipboard. Select the text and copy it instead.')
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
        onChange={(e) => edit(e.target.value)}
      />
      {remote !== null && remote !== text && (
        <p className="muted">
          Edited: the remote clipboard holds other text.{' '}
          <button type="button" onClick={() => edit(remote)}>
            Show it
          </button>
        </p>
      )}
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
        {shown}
      </p>
    </form>
  )
}

// sent says what became of text sent to the remote's clipboard.
function sent(r: ClipboardSend): string {
  switch (r.kind) {
    case 'sent':
      return 'Sent to the remote clipboard.'
    case 'not-open':
      return 'The remote clipboard is not open to you.'
    case 'failed':
      return r.error instanceof ProtocolError && r.error.kind === 'too-large'
        ? 'Not sent: the text is too large for one message to occamyd. Send less of it.'
        : `Not sent: ${clip(r.error.message, MAX_LABEL)}`
  }
}
