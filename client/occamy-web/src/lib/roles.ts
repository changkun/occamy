// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// What each role may do in the shell (docs/api.md, Authentication): the
// owner opened the session and alone answers prompts, resizes it, shares
// and ends it; a viewer gives input too; a read-only viewer only watches,
// and the server drops its input.

import { Role } from '../protocol'

export interface Abilities {
  /** Keys, pointer, wheel, text and terminal input. */
  input: boolean
  /** Sending the local clipboard to the remote, which is input. */
  clipboard: boolean
  /** Resizing the remote to the window: a viewer's would resize everyone's screen. */
  resize: boolean
  /** Answering the remote's credential prompts, which only the owner gets. */
  prompts: boolean
  /** Minting share links and ending the session for everyone, through API v1. */
  manage: boolean
}

export function abilities(role: Role): Abilities {
  switch (role) {
    case Role.Owner:
      return { input: true, clipboard: true, resize: true, prompts: true, manage: true }
    case Role.Viewer:
      return { input: true, clipboard: true, resize: false, prompts: false, manage: false }
    default:
      return { input: false, clipboard: false, resize: false, prompts: false, manage: false }
  }
}

/** roleName names a role as API v1 does. */
export function roleName(role: Role): string {
  switch (role) {
    case Role.Owner:
      return 'owner'
    case Role.Viewer:
      return 'viewer'
    case Role.ReadOnly:
      return 'read-only'
    default:
      return 'unknown'
  }
}
