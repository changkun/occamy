// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The owner's credential prompt: the remote asks for answers, such as a
// password the connection's configuration does not hold. The server's
// strings are clipped, and secret fields are password inputs.

import { type FormEvent, type ReactElement, useEffect, useId, useRef } from 'react'

import type { PromptEvent } from '../protocol'
import { MAX_LABEL, MAX_PARAGRAPH, clip } from '../lib/text'
import { formText } from './dom'

export interface PromptDialogProps {
  prompt: PromptEvent
  onAnswer: (answers: string[]) => void
  /** Ends the session, as the remote waits for the answer. */
  onEnd: () => void
}

export function PromptDialog({ prompt, onAnswer, onEnd }: PromptDialogProps): ReactElement {
  const ref = useRef<HTMLDialogElement>(null)
  const id = useId()

  useEffect(() => {
    const dialog = ref.current
    if (dialog !== null && !dialog.open) {
      dialog.showModal()
    }
    return () => dialog?.close()
  }, [])

  const submit = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault()
    const form = new FormData(e.currentTarget)
    onAnswer(prompt.fields.map((_, i) => formText(form, `answer-${i}`)))
  }

  const title = clip(prompt.name, MAX_LABEL)
  const instruction = clip(prompt.instruction, MAX_PARAGRAPH)
  return (
    <dialog
      ref={ref}
      className="prompt"
      aria-labelledby={`${id}-title`}
      aria-describedby={instruction !== '' ? `${id}-instruction` : undefined}
      // The remote waits for an answer; Escape would leave it waiting.
      onCancel={(e) => e.preventDefault()}
    >
      <form onSubmit={submit}>
        <h2 id={`${id}-title`}>{title !== '' ? title : 'The remote asks'}</h2>
        {instruction !== '' && (
          <p id={`${id}-instruction`} className="instruction">
            {instruction}
          </p>
        )}
        {prompt.fields.map((f, i) => (
          <label key={i}>
            {clip(f.label, MAX_LABEL) || `Answer ${i + 1}`}
            <input
              name={`answer-${i}`}
              type={f.secret ? 'password' : 'text'}
              autoComplete={f.secret ? 'one-time-code' : 'off'}
              autoCapitalize="none"
              spellCheck={false}
              autoFocus={i === 0}
            />
          </label>
        ))}
        <div className="actions">
          <button type="submit" className="primary">
            Continue
          </button>
          <button type="button" onClick={onEnd}>
            End session
          </button>
        </div>
      </form>
    </dialog>
  )
}
