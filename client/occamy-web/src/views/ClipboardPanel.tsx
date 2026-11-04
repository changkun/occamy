// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The clipboard panel: the remote clipboard's text, to copy, and text to
// put on the remote's clipboard, for the times the browser does not let
// the page reach the local clipboard on its own. The text being edited
// is the controller's draft, so that a new remote clipboard does not
// throw away what the user typed. What the last action said goes once
// the remote clipboard changes or the user edits the text, and gives way
// to word that text waits for the remote. A terminal session has its own
// panel, as it has no remote clipboard.

import { type FormEvent, type ReactElement, useId, useState } from 'react'

import { ProtocolError } from '../protocol'
import { CLIPBOARD_WAITING, type ClipboardSend, type SessionController } from '../lib/controller'
import { MAX_LABEL, clip } from '../lib/text'
import type { Abilities } from '../lib/roles'
import { copyText, pasteLocalClipboard } from './dom'

export interface ClipboardPanelProps {
  controller: SessionController
  /** The remote clipboard's text, or null before any. */
  remote: string | null
  /** The text in the panel: the remote's, or what the user made of it. */
  draft: string
  /** Whether text sent waits for the remote, which has not taken it. */
  waiting: boolean
  can: Abilities
  onClose: () => void
}

export function ClipboardPanel({ controller, remote, draft: text, waiting, can, onClose }: ClipboardPanelProps): ReactElement {
  // What the last action said, with the remote clipboard it was said
  // over: it shows while the remote clipboard stays that.
  const [status, setStatus] = useState<{ text: string; remote: string | null } | null>(null)
  const say = (s: string): void => setStatus({ text: s, remote: controller.getState().clipboard })
  const said = status !== null && status.remote === remote ? status.text : ''
  const shown = waiting ? CLIPBOARD_WAITING : said
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
      say('Your browser didn’t let the page read the clipboard. Paste into the box yourself instead.')
    }
  }
  const copy = async (): Promise<void> => {
    try {
      await copyText(text)
      say('Copied.')
    } catch {
      say('Your browser didn’t let the page copy to the clipboard. Select the text and copy it yourself.')
    }
  }

  return (
    <form className="panel" aria-labelledby={`${id}-title`} onSubmit={send} onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <h2 id={`${id}-title`}>Clipboard</h2>
      <p className="muted">
        {can.clipboard
          ? 'This is the remote computer’s clipboard. Edit the text and send it, or copy it.'
          : 'This is the remote computer’s clipboard. You can copy its text, but your role can’t change it.'}
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

export interface TerminalClipboardPanelProps {
  can: Abilities
  onClose: () => void
}

// A terminal session has no remote clipboard: its backends ignore the
// clipboard message and send none, so this says so rather than offer a
// box that nothing reads. Text goes in by the browser's paste, which the
// emulator types into the shell.
export function TerminalClipboardPanel({ can, onClose }: TerminalClipboardPanelProps): ReactElement {
  const id = useId()
  return (
    <section className="panel" aria-labelledby={`${id}-title`} onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <h2 id={`${id}-title`}>Clipboard</h2>
      <p className="muted">
        {can.input
          ? 'A terminal session has no remote clipboard. To paste text into the terminal, click it and use your browser’s paste: ⌘V, or Ctrl+Shift+V.'
          : 'A terminal session has no remote clipboard, and your role can’t type into it.'}
      </p>
      <div className="actions">
        <button type="button" autoFocus onClick={onClose}>
          Close
        </button>
      </div>
    </section>
  )
}

// sent says what became of text sent to the remote's clipboard.
function sent(r: ClipboardSend): string {
  switch (r.kind) {
    case 'sent':
      return 'Sent to the remote clipboard.'
    case 'not-open':
      return 'The remote clipboard isn’t available to you.'
    case 'failed':
      return r.error instanceof ProtocolError && r.error.kind === 'too-large'
        ? 'The text wasn’t sent because it’s too large. Send a shorter part of it.'
        : `The text wasn’t sent: ${clip(r.error.message, MAX_LABEL)}`
  }
}
