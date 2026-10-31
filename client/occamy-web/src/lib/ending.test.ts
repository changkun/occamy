// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { CloseCode, CloseError, ConnectionError, ProtocolError } from '../protocol'
import { type EndContext, TAKEOVER_REASON, describeEnd, describeFailure, endsSession } from './ending'

const owner: EndContext = { shared: false, sessionId: 'mZ3k1tYb0Q2fJw8aXr5LsA' }
const viewer: EndContext = { shared: true, sessionId: 'mZ3k1tYb0Q2fJw8aXr5LsA' }
const never: EndContext = { shared: false, sessionId: '' }

describe('describeEnd', () => {
  it('describes every close code of protocol v1 in words of its own', () => {
    const titles = new Map<number, string>()
    for (const code of Object.values(CloseCode)) {
      const e = describeEnd(code, '', owner)
      expect(e.title).not.toBe('')
      expect(e.detail).not.toBe('')
      titles.set(code, e.title)
    }
    expect(titles.get(CloseCode.TooSlow)).toMatch(/too slow/)
  })

  it('says nothing of the user’s own close', () => {
    expect(describeEnd(CloseCode.Normal, '', owner).quiet).toBe(true)
  })

  it('tells an owner whose newer stream took over, and offers to take it back', () => {
    const e = describeEnd(CloseCode.Normal, TAKEOVER_REASON, owner)
    expect(e).toMatchObject({ title: 'Opened elsewhere', quiet: false, rejoin: true, signIn: false, technical: TAKEOVER_REASON })
    expect(describeEnd(CloseCode.Normal, TAKEOVER_REASON, never).rejoin).toBe(false)
    // Another normal close with a reason is no takeover.
    expect(describeEnd(CloseCode.Normal, 'bye', owner)).toMatchObject({ title: 'Disconnected', rejoin: false, quiet: false })
  })

  it('knows the takeover by the reason occamyd sends and documents', () => {
    const api = readFileSync(new URL('../../../../docs/api.md', import.meta.url), 'utf8')
    const streams = readFileSync(new URL('../../../../server/streams.go', import.meta.url), 'utf8')
    expect(api).toContain(`\`close\` 1000 \`normal\` with "${TAKEOVER_REASON}"`)
    expect(streams).toContain(`protocol.Close{Code: protocol.CloseNormal, Reason: "${TAKEOVER_REASON}"}`)
  })

  it('names the code and clips the server’s reason', () => {
    const e = describeEnd(CloseCode.Forbidden, 'no such connection', owner)
    expect(e.technical).toBe('forbidden (4001): no such connection')
    expect(Array.from(describeEnd(CloseCode.Forbidden, 'x'.repeat(1 << 20), owner).technical).length).toBeLessThan(300)
  })

  it('sends a signed-in user to sign in again, and tells a share’s holder the link is spent', () => {
    expect(describeEnd(CloseCode.Unauthorized, 'token expired', owner)).toMatchObject({ signIn: true, title: 'Signed out' })
    expect(describeEnd(CloseCode.Unauthorized, 'invalid token', viewer)).toMatchObject({ signIn: false, title: 'Link not valid' })
  })

  it('offers to join again a session that may still run', () => {
    expect(describeEnd(0, '', owner).rejoin).toBe(true)
    expect(describeEnd(1006, '', viewer).rejoin).toBe(true)
    expect(describeEnd(CloseCode.TooSlow, '', owner).rejoin).toBe(true)
    expect(describeEnd(0, '', never).rejoin).toBe(false)
    for (const code of [CloseCode.SessionClosed, CloseCode.OwnerLeft, CloseCode.RemoteEnded, CloseCode.Idle, CloseCode.ServerShutdown, CloseCode.BackendFailed]) {
      expect(describeEnd(code, '', owner).rejoin).toBe(false)
    }
  })

  it('describes an unknown code by the server’s reason', () => {
    const e = describeEnd(4999, 'something new', owner)
    expect(e.title).toBe('Session ended')
    expect(e.technical).toBe('CloseCode(4999) (4999): something new')
  })
})

describe('endsSession', () => {
  it('takes the codes occamyd closes streams with once their session ended for everyone', () => {
    const ends: number[] = [CloseCode.ServerShutdown, CloseCode.SessionClosed, CloseCode.OwnerLeft, CloseCode.Idle, CloseCode.RemoteEnded, CloseCode.BackendFailed]
    for (const code of Object.values(CloseCode)) {
      expect(endsSession(code), String(code)).toBe(ends.includes(code))
    }
    // Nor the user's own close, or a connection that failed without a code.
    expect(endsSession(0)).toBe(false)
    expect(endsSession(1006)).toBe(false)
  })

  it('matches the session ends of occamyd’s CloseFor', () => {
    const src = readFileSync(new URL('../../../../internal/stream/serve.go', import.meta.url), 'utf8')
    const body = src.slice(src.indexOf('func CloseFor('), src.indexOf('\n}\n', src.indexOf('func CloseFor(')))
    const names = [...body.matchAll(/session\.\w+:\s+protocol\.Close(\w+),/g)].map((m) => m[1])
    expect(names).toEqual(['SessionClosed', 'OwnerLeft', 'Idle', 'RemoteEnded', 'BackendFailed', 'ServerShutdown', 'InternalError'])
    // InternalError is left out: occamyd also closes one failed stream with it.
    for (const name of names.filter((n) => n !== 'InternalError')) {
      expect(endsSession(CloseCode[name as keyof typeof CloseCode]), name).toBe(true)
    }
  })
})

describe('describeFailure', () => {
  it('describes a refusal by its close code', () => {
    expect(describeFailure(new CloseError(CloseCode.ViewerLimit, ''), viewer)?.title).toBe('Session full')
  })

  it('describes a connection that failed', () => {
    expect(describeFailure(new ConnectionError('the WebSocket closed before it opened (1006)'), never)).toMatchObject({ title: 'Connection lost', rejoin: false })
  })

  it('describes a server that broke the protocol', () => {
    expect(describeFailure(new ProtocolError('unsupported', 'the server answered subprotocol ""'), never)?.title).toBe('Protocol error')
  })

  it('says nothing of an open the user cancelled', () => {
    expect(describeFailure(new DOMException('aborted', 'AbortError'), never)).toBeNull()
  })

  it('describes anything else by its message', () => {
    expect(describeFailure(new RangeError('occamy: a size or ratio outside 0 to 65535'), never)).toMatchObject({ title: 'Could not connect' })
  })
})
