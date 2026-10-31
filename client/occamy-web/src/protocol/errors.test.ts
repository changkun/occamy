// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The close codes against docs/protocol.md's table, which they follow.

import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { CloseCode, CloseError, closeCodeFor, closeCodeName, closeDescription, closeKind } from './errors'
import { ProtocolError } from './wire'

const doc = readFileSync(new URL('../../../../docs/protocol.md', import.meta.url), 'utf8')
const section = doc.slice(doc.indexOf('\n## Close codes'), doc.indexOf('\n## ', doc.indexOf('\n## Close codes') + 1))
const table = [...section.matchAll(/^\| (\d+) \| `([a-z-]+)` \|/gm)].map((m) => ({ code: Number(m[1]), name: m[2] }))

describe('close codes', () => {
  it('are those of docs/protocol.md, by number and name', () => {
    expect(table.length).toBeGreaterThan(10)
    expect(table.map((r) => r.code)).toEqual(Object.values(CloseCode).sort((a, b) => a - b))
    for (const r of table) {
      expect(closeCodeName(r.code)).toBe(r.name)
    }
  })

  it('include too-slow, the viewer the server waited on for its viewer timeout', () => {
    expect(CloseCode.TooSlow).toBe(4008)
    expect(closeCodeName(4008)).toBe('too-slow')
    expect(closeKind(4008)).toBe('limit')
    expect(closeDescription(4008)).toMatch(/viewer timeout/)
    expect(new CloseError(4008, 'no frame-ack for 30s').message).toBe('occamy: closed: too-slow: no frame-ack for 30s')
  })

  it('say what each means for a UI', () => {
    const kinds = Object.fromEntries(table.map((r) => [r.name, closeKind(r.code)]))
    expect(kinds).toEqual({
      normal: 'done',
      'server-shutdown': 'server',
      'protocol-error': 'protocol',
      unsupported: 'protocol',
      'too-large': 'protocol',
      'internal-error': 'server',
      unauthorized: 'auth',
      forbidden: 'auth',
      'session-closed': 'done',
      'owner-left': 'done',
      idle: 'limit',
      'remote-ended': 'done',
      'backend-failed': 'server',
      'viewer-limit': 'limit',
      'too-slow': 'limit',
    })
    for (const r of table) {
      expect(closeDescription(r.code)).not.toMatch(/unknown/)
    }
  })

  it('treat an unknown code as an error the reason describes, and 0 as a failed connection', () => {
    expect(closeCodeName(4999)).toBe('CloseCode(4999)')
    expect(closeKind(4999)).toBe('server')
    expect(closeDescription(4999)).toMatch(/unknown code/)
    expect(closeKind(0)).toBe('connection')
    expect(closeDescription(0)).toBe('The connection failed.')
  })

  it('close over an error with the code it calls for', () => {
    expect(closeCodeFor(new ProtocolError('unsupported', ''))).toBe(CloseCode.Unsupported)
    expect(closeCodeFor(new ProtocolError('too-large', ''))).toBe(CloseCode.TooLarge)
    expect(closeCodeFor(new ProtocolError('invalid', ''))).toBe(CloseCode.ProtocolError)
    expect(closeCodeFor(new Error('boom'))).toBe(CloseCode.InternalError)
  })
})
