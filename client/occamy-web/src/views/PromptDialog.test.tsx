// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The prompt's markup, rendered without a DOM: what the browser's
// password manager is told about the remote's fields.

import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { PromptDialog } from './PromptDialog'

function inputs(html: string): string[] {
  return html.match(/<input[^>]*>/g) ?? []
}

describe('PromptDialog', () => {
  it('keeps the password manager away from the remote’s fields', () => {
    const html = renderToStaticMarkup(
      <PromptDialog
        prompt={{ type: 'prompt', id: 1, name: 'SSH', instruction: '', fields: [{ label: 'User', secret: false }, { label: 'Password', secret: true }] }}
        onAnswer={() => undefined}
        onEnd={() => undefined}
      />,
    )
    const [user, password] = inputs(html)
    expect(user).toContain('type="text"')
    expect(user).toContain('autoComplete="off"')
    // A password manager fills and saves current-password fields with
    // the user's Occamy password; the remote's answer is not that, and
    // one-time-code keeps the manager from filling or saving it.
    expect(password).toContain('type="password"')
    expect(password).toContain('autoComplete="one-time-code"')
    expect(html).not.toMatch(/current-password|new-password|"username"/)
  })
})
