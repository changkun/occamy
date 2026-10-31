// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Close codes (docs/protocol.md, Close codes) and the errors a session
// reports.

import { ProtocolError } from './wire'

/** The close codes of protocol v1, which are WebSocket close status codes. */
export const CloseCode = {
  Normal: 1000,
  ServerShutdown: 1001,
  ProtocolError: 1002,
  Unsupported: 1003,
  TooLarge: 1009,
  InternalError: 1011,
  Unauthorized: 4000,
  Forbidden: 4001,
  SessionClosed: 4002,
  OwnerLeft: 4003,
  Idle: 4004,
  RemoteEnded: 4005,
  BackendFailed: 4006,
  ViewerLimit: 4007,
  TooSlow: 4008,
} as const

/**
 * Why a session ended, for a UI to act on:
 * - done: it did its work, as when the user, the owner or the remote ended it;
 * - auth: the token is missing, invalid or expired, or grants no access;
 * - limit: it waited too long for input or for the client, or has too many viewers;
 * - server: the server or the backend failed, or shuts down;
 * - protocol: a peer broke the protocol, or the client cannot show the session;
 * - connection: the connection failed without a code.
 */
export type CloseKind = 'done' | 'auth' | 'limit' | 'server' | 'protocol' | 'connection'

interface CloseInfo {
  name: string
  kind: CloseKind
  description: string
}

const closeInfo: Record<number, CloseInfo> = {
  [CloseCode.Normal]: { name: 'normal', kind: 'done', description: 'The connection did its work.' },
  [CloseCode.ServerShutdown]: { name: 'server-shutdown', kind: 'server', description: 'The server is shutting down.' },
  [CloseCode.ProtocolError]: { name: 'protocol-error', kind: 'protocol', description: 'A peer broke the protocol.' },
  [CloseCode.Unsupported]: { name: 'unsupported', kind: 'protocol', description: 'A peer sent what the other does not support, or the client cannot show the session.' },
  [CloseCode.TooLarge]: { name: 'too-large', kind: 'protocol', description: 'A message, tile or cursor was too large.' },
  [CloseCode.InternalError]: { name: 'internal-error', kind: 'server', description: 'The server failed on its own.' },
  [CloseCode.Unauthorized]: { name: 'unauthorized', kind: 'auth', description: 'The token is missing, invalid or expired.' },
  [CloseCode.Forbidden]: { name: 'forbidden', kind: 'auth', description: 'The token does not grant access, or the origin is not allowed.' },
  [CloseCode.SessionClosed]: { name: 'session-closed', kind: 'done', description: 'The owner closed the session.' },
  [CloseCode.OwnerLeft]: { name: 'owner-left', kind: 'done', description: 'The owner left and did not return.' },
  [CloseCode.Idle]: { name: 'idle', kind: 'limit', description: 'Nobody gave input for the idle timeout.' },
  [CloseCode.RemoteEnded]: { name: 'remote-ended', kind: 'done', description: 'The remote ended the session.' },
  [CloseCode.BackendFailed]: { name: 'backend-failed', kind: 'server', description: 'The connection to the remote failed.' },
  [CloseCode.ViewerLimit]: { name: 'viewer-limit', kind: 'limit', description: 'The session has as many viewers as it allows.' },
  [CloseCode.TooSlow]: { name: 'too-slow', kind: 'limit', description: 'The client acknowledged no frame, or took no message, for the server’s viewer timeout.' },
}

/** closeCodeName returns a close code's name, such as "protocol-error", or "CloseCode(n)" for an unknown one. */
export function closeCodeName(code: number): string {
  return closeInfo[code]?.name ?? `CloseCode(${code})`
}

/**
 * closeKind says what a close code means for a UI. Code 0 is a
 * connection that failed without one; an unknown code is an error
 * described by its reason, as the protocol says, and counts as a
 * server failure.
 */
export function closeKind(code: number): CloseKind {
  if (code === 0) {
    return 'connection'
  }
  return closeInfo[code]?.kind ?? 'server'
}

/** closeDescription describes a close code for people. */
export function closeDescription(code: number): string {
  if (code === 0) {
    return 'The connection failed.'
  }
  return closeInfo[code]?.description ?? 'The server ended the connection with an unknown code.'
}

/** closeCodeFor returns the code that closes a connection over err. */
export function closeCodeFor(err: unknown): number {
  if (!(err instanceof ProtocolError)) {
    return CloseCode.InternalError
  }
  switch (err.kind) {
    case 'unsupported':
      return CloseCode.Unsupported
    case 'too-large':
      return CloseCode.TooLarge
    default:
      return CloseCode.ProtocolError
  }
}

/** CloseError reports a connection that a peer closed with a close code. */
export class CloseError extends Error {
  readonly code: number
  readonly reason: string

  constructor(code: number, reason: string) {
    super(`occamy: closed: ${closeCodeName(code)}${reason === '' ? '' : `: ${reason}`}`)
    this.name = 'CloseError'
    this.code = code
    this.reason = reason
  }

  get kind(): CloseKind {
    return closeKind(this.code)
  }
}

/** ConnectionError reports a connection that failed without a close code. */
export class ConnectionError extends Error {
  constructor(message: string) {
    super(`occamy: ${message}`)
    this.name = 'ConnectionError'
  }
}

/** ClosedError reports a call on a session that has ended, and next() after it delivered its end. */
export class ClosedError extends Error {
  constructor() {
    super('occamy: session ended')
    this.name = 'ClosedError'
  }
}

/** ReadOnlyError reports input from a read-only viewer, which the server would drop. */
export class ReadOnlyError extends Error {
  constructor() {
    super('occamy: read-only viewer')
    this.name = 'ReadOnlyError'
  }
}

/** NotOwnerError reports a prompt reply from a viewer other than the owner, which the server ignores. */
export class NotOwnerError extends Error {
  constructor() {
    super('occamy: not the owner')
    this.name = 'NotOwnerError'
  }
}

/** asError returns err if it is an Error, and an Error that describes it otherwise. */
export function asError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err))
}

/** abortError is what a call that signal cancelled rejects with: its reason, or an AbortError. */
export function abortError(signal: AbortSignal): Error {
  const reason: unknown = signal.reason
  return reason === undefined ? new DOMException('aborted', 'AbortError') : asError(reason)
}
