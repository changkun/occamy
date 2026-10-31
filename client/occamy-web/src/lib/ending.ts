// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Why a session ended, for people: the close code's meaning
// (docs/protocol.md, Close codes) in the shell's words, the server's
// reason clipped, and what the user can do next.

import { CloseCode, CloseError, ConnectionError, ProtocolError, closeCodeName, closeDescription, closeKind } from '../protocol'
import { MAX_LABEL, clip } from './text'

/** How the stream was joined, which decides what the user can do once it ends. */
export interface EndContext {
  /** Joined with a share link, not as a signed-in user. */
  shared: boolean
  /** The session's ID, once welcome named it; empty if the stream never opened. */
  sessionId: string
}

export interface Ending {
  /** A headline. */
  title: string
  /** What happened and what to do, in a sentence or two. */
  detail: string
  /** The close code's name and the server's reason, clipped, for those who report it; empty if none. */
  technical: string
  /** Sign in again: the access token is no longer good. */
  signIn: boolean
  /** Join the session again: it may still run, as after a lost connection. */
  rejoin: boolean
  /** The user ended it, so there is nothing to tell. */
  quiet: boolean
}

/**
 * TAKEOVER_REASON is the reason of the normal close that occamyd sends an
 * owner's stream when a newer stream of the owner takes over
 * (docs/api.md, GET /api/v1/sessions/{id}/stream): the session runs on
 * elsewhere.
 */
export const TAKEOVER_REASON = 'the owner connected again'

/** describeEnd describes a session that ended with code and reason. */
export function describeEnd(code: number, reason: string, ctx: EndContext): Ending {
  const technical = code === 0 ? clip(reason, MAX_LABEL) : `${closeCodeName(code)} (${code})${reason === '' ? '' : `: ${clip(reason, MAX_LABEL)}`}`
  const e: Ending = { title: '', detail: closeDescription(code), technical, signIn: false, rejoin: false, quiet: false }
  switch (code) {
    case CloseCode.Normal:
      if (reason === TAKEOVER_REASON && !ctx.shared) {
        return {
          ...e,
          title: 'Opened elsewhere',
          detail: 'You opened this session in another window or tab, which took it over. It runs on there; join it here to take it back.',
          technical: reason,
          rejoin: ctx.sessionId !== '',
        }
      }
      return { ...e, title: 'Disconnected', detail: 'The session was closed.', technical: reason === '' ? '' : clip(reason, MAX_LABEL), quiet: reason === '' }
    case CloseCode.SessionClosed:
      return { ...e, title: 'Session ended', detail: 'The owner ended the session.' }
    case CloseCode.OwnerLeft:
      return { ...e, title: 'Session ended', detail: 'The owner left and did not come back in time.' }
    case CloseCode.RemoteEnded:
      return { ...e, title: 'Session ended', detail: 'The remote computer ended the session, as when its user logs out.' }
    case CloseCode.Unauthorized:
      return ctx.shared
        ? { ...e, title: 'Link not valid', detail: 'This share link has expired, or its session has ended. Ask the owner for a new one.' }
        : { ...e, title: 'Signed out', detail: 'Your sign-in has expired or is not valid. Sign in again.', signIn: true }
    case CloseCode.Forbidden:
      return { ...e, title: 'Access refused', detail: 'occamyd refused this stream: the connection or session is not open to you, the session has ended, or you have as many sessions as it allows.' }
    case CloseCode.Idle:
      return { ...e, title: 'Ended for inactivity', detail: 'Nobody gave input for the session’s idle timeout, so occamyd ended it.' }
    case CloseCode.ViewerLimit:
      return { ...e, title: 'Session full', detail: 'The session has as many viewers as occamyd allows. Try again later.' }
    case CloseCode.TooSlow:
      return {
        ...e,
        title: 'Disconnected: too slow',
        detail: 'occamyd heard nothing from this page for its viewer timeout, 30 seconds by default: the network stalled, or the page could not keep up.',
        rejoin: ctx.sessionId !== '',
      }
    case CloseCode.BackendFailed:
      return { ...e, title: 'Remote unreachable', detail: 'occamyd could not connect to the remote computer, or lost the connection to it.' }
    case CloseCode.ServerShutdown:
      return { ...e, title: 'Server shut down', detail: 'occamyd is shutting down, which ends its sessions. Try again once it is back.' }
    default:
      break
  }
  // The core reports a connection that failed without a code as 0; a
  // browser's own codes for one say the same.
  switch (code === 1005 || code === 1006 || code === 1015 ? 'connection' : closeKind(code)) {
    case 'connection':
      return {
        ...e,
        title: 'Connection lost',
        detail: ctx.sessionId === '' ? 'The connection to occamyd failed.' : 'The connection to occamyd failed. The session may still run: join it again.',
        rejoin: ctx.sessionId !== '',
      }
    case 'protocol':
      return { ...e, title: 'Protocol error', detail: `${closeDescription(code)} This is a bug in occamyd or in this client.` }
    default:
      return { ...e, title: 'Session ended' }
  }
}

/**
 * describeFailure describes a stream that failed to open: refused with a
 * close code, failed without one, or broken by the server. It returns
 * null for a cancelled open, which the user asked for.
 */
export function describeFailure(err: unknown, ctx: EndContext): Ending | null {
  if (err instanceof DOMException && err.name === 'AbortError') {
    return null
  }
  if (err instanceof CloseError) {
    return describeEnd(err.code, err.reason, ctx)
  }
  if (err instanceof ConnectionError) {
    return { ...describeEnd(0, '', ctx), detail: 'occamyd cannot be reached.', rejoin: false }
  }
  if (err instanceof ProtocolError) {
    return describeEnd(err.kind === 'unsupported' ? CloseCode.Unsupported : CloseCode.ProtocolError, err.message, ctx)
  }
  const message = err instanceof Error ? err.message : String(err)
  return { title: 'Could not connect', detail: clip(message, MAX_LABEL), technical: '', signIn: false, rejoin: false, quiet: false }
}
