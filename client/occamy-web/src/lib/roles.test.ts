// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { describe, expect, it } from 'vitest'

import { Role } from '../protocol'
import { abilities, roleName } from './roles'

describe('abilities', () => {
  it('lets the owner do everything', () => {
    expect(abilities(Role.Owner)).toEqual({ input: true, clipboard: true, resize: true, prompts: true, manage: true })
  })

  it('lets a viewer give input, but not resize, answer prompts, share or end', () => {
    expect(abilities(Role.Viewer)).toEqual({ input: true, clipboard: true, resize: false, prompts: false, manage: false })
  })

  it('lets a read-only viewer only watch', () => {
    expect(abilities(Role.ReadOnly)).toEqual({ input: false, clipboard: false, resize: false, prompts: false, manage: false })
  })

  it('gives an unknown role nothing', () => {
    expect(abilities(9 as Role)).toEqual(abilities(Role.ReadOnly))
  })
})

describe('roleName', () => {
  it('names roles as API v1 does', () => {
    expect([Role.Owner, Role.Viewer, Role.ReadOnly].map(roleName)).toEqual(['owner', 'viewer', 'read-only'])
  })
})
