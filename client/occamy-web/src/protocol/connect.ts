// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// connect opens a session over a WebSocket, performing the handshake of
// docs/protocol.md: hello in the upgrade request's hello parameter, the
// token in Sec-WebSocket-Protocol beside occamy.v1, never in the URL.

import type { TileDecoder } from './decode'
import { ConnectionError, abortError } from './errors'
import { HELLO_PARAMETER, SUBPROTOCOL, encodeHelloQuery, tokenSubprotocol } from './handshake'
import { type Options, Session, type Transport, checkOptions, helloFor } from './session'
import { defaultDecoder } from './worker'
import { ProtocolError } from './wire'

/** A WebSocket constructor, the platform's by default. */
export type WebSocketConstructor = new (url: string | URL, protocols?: string | string[]) => WebSocket

export interface ConnectOptions extends Options {
  /**
   * Decodes tiles and cursors: Workers where the platform has them, the
   * calling thread otherwise. A decoder passed here is the caller's to
   * close; the default one closes with the session.
   */
  decoder?: TileDecoder
  /** The WebSocket constructor; globalThis.WebSocket by default. */
  WebSocket?: WebSocketConstructor
  /** Cancels the handshake. Once connect() resolves, the session lasts until close() or the server ends it. */
  signal?: AbortSignal
}

/**
 * connect opens a session on url, a stream route of occamyd such as
 * wss://host/api/v1/connections/lab/stream, and resolves once the
 * server welcomed the client. token is an access or share token; an
 * empty one offers occamy.v1 alone.
 *
 * It offers the codecs the decoder decodes: deflate where
 * DecompressionStream inflates raw DEFLATE, WebP and JPEG where
 * createImageBitmap decodes them, and raw. If the server refuses the
 * client after the upgrade, such as for its token, connect rejects with
 * a CloseError with the server's code; if the connection fails, with a
 * ConnectionError.
 */
export async function connect(url: string | URL, token: string, options: ConnectOptions = {}): Promise<Session> {
  checkOptions(options)
  const ownsDecoder = options.decoder === undefined
  const decoder = options.decoder ?? defaultDecoder()
  try {
    const codecs = options.codecs ?? (await decoder.codecs())
    const u = new URL(url)
    u.searchParams.set(HELLO_PARAMETER, encodeHelloQuery(helloFor(options, codecs)))
    const protocols = [SUBPROTOCOL]
    if (token !== '') {
      protocols.push(tokenSubprotocol(token))
    }
    const WS = options.WebSocket ?? globalThis.WebSocket
    const ws = new WS(u, protocols)
    const transport = webSocketTransport(ws)
    await opened(ws, options.signal)
    if (ws.protocol !== SUBPROTOCOL) {
      ws.close(1000)
      throw new ProtocolError('unsupported', `the server answered subprotocol "${ws.protocol}", want ${SUBPROTOCOL}`)
    }
    return await Session.start(transport, options, decoder, codecs, { ownsDecoder, signal: options.signal })
  } catch (err) {
    if (ownsDecoder) {
      decoder.close()
    }
    throw err
  }
}

function opened(ws: WebSocket, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const cleanup = (): void => {
      ws.removeEventListener('open', onOpen)
      ws.removeEventListener('close', onClose)
      signal?.removeEventListener('abort', onAbort)
    }
    const onOpen = (): void => {
      cleanup()
      resolve()
    }
    const onClose = (ev: CloseEvent): void => {
      cleanup()
      reject(new ConnectionError(`the WebSocket closed before it opened (${ev.code})`))
    }
    const onAbort = (): void => {
      cleanup()
      ws.close()
      reject(abortError(signal as AbortSignal))
    }
    if (signal?.aborted === true) {
      onAbort()
      return
    }
    ws.addEventListener('open', onOpen)
    ws.addEventListener('close', onClose)
    signal?.addEventListener('abort', onAbort)
  })
}

/**
 * webSocketTransport adapts a WebSocket, one protocol message per binary
 * message. Messages that arrive before the session listens wait for it.
 */
export function webSocketTransport(ws: WebSocket): Transport {
  ws.binaryType = 'arraybuffer'
  const early: (ArrayBuffer | string)[] = []
  let onmessage: Transport['onmessage'] = null
  const t: Transport = {
    send: (msg) => ws.send(msg as Uint8Array<ArrayBuffer>),
    close: (code, reason) => ws.close(code, reason),
    get onmessage() {
      return onmessage
    },
    set onmessage(f) {
      onmessage = f
      while (onmessage !== null && early.length > 0) {
        onmessage(early.shift() as ArrayBuffer | string)
      }
    },
    onclose: null,
  }
  ws.addEventListener('message', (ev: MessageEvent<ArrayBuffer | string>) => {
    if (onmessage === null) {
      early.push(ev.data)
    } else {
      onmessage(ev.data)
    }
  })
  ws.addEventListener('close', (ev: CloseEvent) => {
    t.onclose?.(ev.wasClean ? ev.code : 1006, ev.reason)
  })
  return t
}
