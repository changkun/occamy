// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// What Session.next delivers, as the Go client's Next does
// (client/events.go).

import type { Pixels } from './codecs'
import type { PromptField, Role } from './messages'

/** A rectangle of the screen, in pixels. */
export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

/**
 * FrameEvent closes a frame. For a graphical session, the framebuffer
 * holds the frame once next() returns it, and dirty lists the rectangles
 * it changed: tiles and copy-rect destinations in the order they came,
 * or the whole screen after a resize. For a terminal session, the
 * frame's resize, term-snapshot and term-data came just before it. The
 * frame is acknowledged when next() is called again, which says the
 * caller has shown it.
 */
export interface FrameEvent {
  type: 'frame'
  /** The frame's ID, from 1 on each connection, wrapping from 2³²−1 to 0. */
  id: number
  /**
   * The sequence number of the last input of this client that the server
   * applied before it read the frame's content, or 0. The frame shows the
   * effect of no later input.
   */
  lastSeq: number
  /** When the server read the frame's content, in microseconds since the session started. */
  serverTime: number
  dirty: Rect[]
}

/**
 * ResizeEvent reports the screen's new size, in pixels for a graphical
 * session and in character cells for a terminal. It starts a frame,
 * which redraws the whole screen, and whose FrameEvent comes next.
 */
export interface ResizeEvent {
  type: 'resize'
  width: number
  height: number
}

/** TermSnapshotEvent replaces the terminal: reset the emulator and write data to it. */
export interface TermSnapshotEvent {
  type: 'term-snapshot'
  data: Uint8Array
}

/** TermDataEvent is escape sequences that update the terminal, to write to the emulator. */
export interface TermDataEvent {
  type: 'term-data'
  data: Uint8Array
}

/** A cursor's image: width × height RGBA pixels with straight alpha. */
export interface CursorImage {
  width: number
  height: number
  pixels: Pixels
}

/**
 * CursorEvent sets the pointer's image, which the caller draws at the
 * local pointer with its hot spot there. A null image hides the pointer.
 */
export interface CursorEvent {
  type: 'cursor'
  image: CursorImage | null
  hotX: number
  hotY: number
}

/** CursorMoveEvent reports that the remote moved the pointer, in pixels. */
export interface CursorMoveEvent {
  type: 'cursor-move'
  x: number
  y: number
}

/** ClipboardEvent is the remote clipboard's new content. */
export interface ClipboardEvent {
  type: 'clipboard'
  mime: string
  data: Uint8Array
}

/** BellEvent rings the bell. */
export interface BellEvent {
  type: 'bell'
}

/** TitleEvent is the remote's desktop or window title. */
export interface TitleEvent {
  type: 'title'
  title: string
}

/** PromptEvent asks the owner for answers; promptReply answers it once, one answer per field. */
export interface PromptEvent {
  type: 'prompt'
  id: number
  name: string
  instruction: string
  fields: PromptField[]
}

/** ViewerJoinedEvent reports a viewer in the session: one that joined, and, after welcome, each one already there. */
export interface ViewerJoinedEvent {
  type: 'viewer-joined'
  viewerId: number
  role: Role
  name: string
}

/** ViewerLeftEvent reports that a viewer left. */
export interface ViewerLeftEvent {
  type: 'viewer-left'
  viewerId: number
}

/** EndEvent is the last event: the session ended. */
export interface EndEvent {
  type: 'end'
  /**
   * Why: the server's close code, 1000 after close(), or the code the
   * client closed with when the server broke the protocol. It is 0 if
   * the connection failed without one. closeKind and closeDescription
   * (errors.ts) say what it means.
   */
  code: number
  /** Describes the code, for people and logs. */
  reason: string
  /** The error that ended the session on the client's side: the server's protocol error, or the connection's failure. */
  error?: Error
}

export type SessionEvent =
  | FrameEvent
  | ResizeEvent
  | TermSnapshotEvent
  | TermDataEvent
  | CursorEvent
  | CursorMoveEvent
  | ClipboardEvent
  | BellEvent
  | TitleEvent
  | PromptEvent
  | ViewerJoinedEvent
  | ViewerLeftEvent
  | EndEvent
