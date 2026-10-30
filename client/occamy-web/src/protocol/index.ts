// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package protocol is the browser's client of protocol v1
// (docs/protocol.md): the TypeScript counterpart of the Go module's
// client package, independent of any UI framework. It imports nothing
// outside this folder and needs nothing beyond the browser.
//
//   const session = await connect(url, token, { width, height, pixelRatio })
//   const display = new Display(session, new CanvasSurface(canvas), { onEvent })
//   bindKeyboard(canvas, session); bindPointer(canvas, session, () => session.framebuffer)
//   const end = await display.run()

export { connect, webSocketTransport } from './connect'
export type { ConnectOptions, WebSocketConstructor } from './connect'
export { Session, DEFAULT_NAME, DEFAULT_MAX_PIXELS, helloFor, checkOptions } from './session'
export type { Options, Transport, Framebuffer } from './session'
export { Display, CanvasSurface, MemorySurface, nextAnimationFrame, coalesce } from './display'
export type { Surface, DisplayOptions } from './display'
export { LocalDecoder, WorkerDecoder, serveDecoder, browserPlatform, streamInflate, canvasDecodeImage } from './decode'
export type { TileDecoder, DecodeJob, DecodeImage, Platform, Port } from './decode'
export { workerDecoder, defaultDecoder, defaultWorkers } from './worker'
export { bindKeyboard, bindPointer, bindVisibility, cursorStyle, pointerButtons, remotePoint, WheelAccumulator } from './dom'
export type { Unbind, ErrorHandler, Box } from './dom'
export { hidUsage, hidUsages, keysym, keyOf } from './keyboard'
export {
  CloseCode,
  CloseError,
  ConnectionError,
  ClosedError,
  ReadOnlyError,
  NotOwnerError,
  closeCodeName,
  closeKind,
  closeDescription,
} from './errors'
export type { CloseKind } from './errors'
export type * from './events'
export { Role, SessionClass, Codec, Buttons, WheelMode, VERSION } from './messages'
export { ProtocolError } from './wire'
export type { ErrorKind } from './wire'
