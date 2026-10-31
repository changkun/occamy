// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The messages of Occamy protocol v1 (docs/protocol.md, Message types),
// in both directions: a client encodes what it sends and decodes what
// the server sends, and the tests and fake servers do the reverse, so
// that every golden vector is checked both ways.

import {
  type Header,
  type Limits,
  MAX_CACHED_PIXELS,
  MIN_MESSAGE_SIZE,
  ProtocolError,
  Reader,
  Writer,
  isIgnorable,
  maxTextureSize,
  split,
} from './wire'

/** The message types. 0x0001 to 0x00FF go either way, 0x01xx from the client, 0x02xx from the server. */
export const MessageType = {
  Ping: 0x0001,
  Pong: 0x0002,
  Close: 0x0003,

  Hello: 0x0101,
  Key: 0x0102,
  Text: 0x0103,
  TermInput: 0x0104,
  Pointer: 0x0105,
  Wheel: 0x0106,
  ResizeRequest: 0x0107,
  ClientClipboard: 0x0108,
  View: 0x0109,
  FrameAck: 0x010a,
  PromptReply: 0x010b,

  Welcome: 0x0201,
  Resize: 0x0202,
  Tile: 0x0203,
  CopyRect: 0x0204,
  FrameEnd: 0x0205,
  Cursor: 0x0206,
  CursorMove: 0x0207,
  TermData: 0x0208,
  TermSnapshot: 0x0209,
  ServerClipboard: 0x020a,
  Bell: 0x020b,
  Title: 0x020c,
  Prompt: 0x020d,
  ViewerJoined: 0x020e,
  ViewerLeft: 0x020f,
} as const

/** The protocol's major version, the first field of hello. */
export const VERSION = 1

/** A viewer's role. */
export const Role = { Owner: 1, Viewer: 2, ReadOnly: 3 } as const
export type Role = (typeof Role)[keyof typeof Role]

/** A session's class. */
export const SessionClass = { Terminal: 1, Graphical: 2 } as const
export type SessionClass = (typeof SessionClass)[keyof typeof SessionClass]

/** The codecs. 7 to 0x7F are unassigned and 0x80 to 0xFF reserved for video. */
export const Codec = { Raw: 1, Deflate: 2, PNG: 3, JPEG: 4, WebP: 5, Cached: 6 } as const
export type Codec = (typeof Codec)[keyof typeof Codec]

export function isCodec(c: number): c is Codec {
  return c >= Codec.Raw && c <= Codec.Cached
}

/** A wheel's unit. */
export const WheelMode = { Pixels: 0, Lines: 1 } as const

/** Pointer buttons, as the pointer message's bits. */
export const Buttons = { Left: 1, Middle: 2, Right: 4, Back: 8, Forward: 16 } as const

/** The capability IDs. */
export const CapabilityID = {
  Codecs: 1,
  MaxTextureSize: 2,
  Terminal: 3,
  PixelRatio: 4,
  CacheSlots: 5,
  FrameWindow: 6,
  MaxMessageSize: 7,
} as const

/** The frame window unless the handshake agrees on another. */
export const DEFAULT_FRAME_WINDOW = 2

/**
 * Capabilities are what a client offers in hello and what the server
 * agrees to in welcome. A zero value (or an empty list) is not sent and
 * takes the protocol's default.
 */
export interface Capabilities {
  /** The codecs the client decodes, in order of preference. */
  codecs: number[]
  maxTextureSize: number
  terminal: number
  pixelRatio: number
  cacheSlots: number
  frameWindow: number
  maxMessageSize: number
  /** Entries of IDs this implementation does not know, kept so that they encode again as they came. */
  unknown: { id: number; value: Uint8Array }[]
}

export function emptyCapabilities(): Capabilities {
  return {
    codecs: [],
    maxTextureSize: 0,
    terminal: 0,
    pixelRatio: 0,
    cacheSlots: 0,
    frameWindow: 0,
    maxMessageSize: 0,
    unknown: [],
  }
}

/** capabilityLimits returns the limits that c sets. */
export function capabilityLimits(c: Capabilities): Limits {
  return { maxMessageSize: c.maxMessageSize, maxTextureSize: c.maxTextureSize, cacheSlots: c.cacheSlots }
}

export interface PromptField {
  label: string
  secret: boolean
}

// The messages. Integer fields are numbers, except the u64 fields of
// ping, pong and frame-end, which are bigints so that no value is lost.
// Byte fields are views into the message they were decoded from.

export interface Ping { type: 'ping'; nonce: number; time: bigint }
export interface Pong { type: 'pong'; nonce: number; time: bigint }
export interface Close { type: 'close'; code: number; reason: string }
export interface Hello {
  type: 'hello'
  width: number
  height: number
  cols: number
  rows: number
  visible: boolean
  name: string
  capabilities: Capabilities
}
export interface Key { type: 'key'; seq: number; down: boolean; keysym: number; code: number }
export interface Text { type: 'text'; seq: number; text: string }
export interface TermInput { type: 'term-input'; seq: number; data: Uint8Array }
export interface Pointer { type: 'pointer'; seq: number; x: number; y: number; buttons: number }
export interface Wheel { type: 'wheel'; seq: number; dx: number; dy: number; mode: number }
export interface ResizeRequest { type: 'resize-request'; seq: number; width: number; height: number }
/** The clipboard message from the client, type 0x0108. */
export interface ClientClipboard { type: 'client-clipboard'; seq: number; mime: string; data: Uint8Array }
export interface View { type: 'view'; visible: boolean; x: number; y: number; w: number; h: number }
export interface FrameAck { type: 'frame-ack'; frameId: number; decodeTime: number }
export interface PromptReply { type: 'prompt-reply'; id: number; answers: string[] }
export interface Welcome {
  type: 'welcome'
  viewerId: number
  role: Role
  class: SessionClass
  sessionId: string
  capabilities: Capabilities
}
export interface Resize { type: 'resize'; width: number; height: number }
export interface Tile {
  type: 'tile'
  frameId: number
  x: number
  y: number
  w: number
  h: number
  codec: Codec
  slot: number
  payload: Uint8Array
}
export interface CopyRect {
  type: 'copy-rect'
  frameId: number
  srcX: number
  srcY: number
  dstX: number
  dstY: number
  w: number
  h: number
}
export interface FrameEnd { type: 'frame-end'; frameId: number; serverTime: bigint; lastSeq: number }
export interface Cursor {
  type: 'cursor'
  hotX: number
  hotY: number
  w: number
  h: number
  codec: number
  payload: Uint8Array
}
export interface CursorMove { type: 'cursor-move'; x: number; y: number }
export interface TermData { type: 'term-data'; data: Uint8Array }
export interface TermSnapshot { type: 'term-snapshot'; data: Uint8Array }
/** The clipboard message from the server, type 0x020a. */
export interface ServerClipboard { type: 'server-clipboard'; mime: string; data: Uint8Array }
export interface Bell { type: 'bell' }
export interface Title { type: 'title'; title: string }
export interface Prompt { type: 'prompt'; id: number; name: string; instruction: string; fields: PromptField[] }
export interface ViewerJoined { type: 'viewer-joined'; viewerId: number; role: Role; name: string }
export interface ViewerLeft { type: 'viewer-left'; viewerId: number }

export type ClientMessage =
  | Hello | Key | Text | TermInput | Pointer | Wheel | ResizeRequest
  | ClientClipboard | View | FrameAck | PromptReply
export type ServerMessage =
  | Welcome | Resize | Tile | CopyRect | FrameEnd | Cursor | CursorMove
  | TermData | TermSnapshot | ServerClipboard | Bell | Title | Prompt
  | ViewerJoined | ViewerLeft
export type Message = Ping | Pong | Close | ClientMessage | ServerMessage

/** The peer that sends a message. */
export type Peer = 'client' | 'server'

const typeOf: Record<Message['type'], number> = {
  'ping': MessageType.Ping,
  'pong': MessageType.Pong,
  'close': MessageType.Close,
  'hello': MessageType.Hello,
  'key': MessageType.Key,
  'text': MessageType.Text,
  'term-input': MessageType.TermInput,
  'pointer': MessageType.Pointer,
  'wheel': MessageType.Wheel,
  'resize-request': MessageType.ResizeRequest,
  'client-clipboard': MessageType.ClientClipboard,
  'view': MessageType.View,
  'frame-ack': MessageType.FrameAck,
  'prompt-reply': MessageType.PromptReply,
  'welcome': MessageType.Welcome,
  'resize': MessageType.Resize,
  'tile': MessageType.Tile,
  'copy-rect': MessageType.CopyRect,
  'frame-end': MessageType.FrameEnd,
  'cursor': MessageType.Cursor,
  'cursor-move': MessageType.CursorMove,
  'term-data': MessageType.TermData,
  'term-snapshot': MessageType.TermSnapshot,
  'server-clipboard': MessageType.ServerClipboard,
  'bell': MessageType.Bell,
  'title': MessageType.Title,
  'prompt': MessageType.Prompt,
  'viewer-joined': MessageType.ViewerJoined,
  'viewer-left': MessageType.ViewerLeft,
}

/** messageType returns the type number of a message. */
export function messageType(m: Message): number {
  return typeOf[m.type]
}

/**
 * accepts reports whether a receiver of what peer `from` sends knows
 * type t: the types of that peer and those of either peer.
 */
export function knownType(t: number, from: Peer): boolean {
  if (t >= MessageType.Ping && t <= MessageType.Close) {
    return true
  }
  if (from === 'client') {
    return t >= MessageType.Hello && t <= MessageType.PromptReply
  }
  return t >= MessageType.Welcome && t <= MessageType.ViewerLeft
}

/**
 * accept reports whether a receiver that knows the types `known` decodes
 * a message with header h: true if it knows the type, false if it does
 * not but the message is ignorable and is skipped. Otherwise it throws
 * unsupported.
 */
export function accept(h: Header, known: (t: number) => boolean): boolean {
  if (known(h.type)) {
    return true
  }
  if (isIgnorable(h)) {
    return false
  }
  throw new ProtocolError('unsupported', `type 0x${h.type.toString(16).padStart(4, '0')}`)
}

/**
 * decode decodes msg, a whole message that peer `from` sent, under the
 * limits l. It returns null for a message of a type the receiver does
 * not know that is marked ignorable, which the receiver skips, and
 * throws a ProtocolError for any message the protocol refuses.
 */
export function decode(msg: Uint8Array, from: Peer, l: Limits): Message | null {
  const { header, body } = split(msg, l)
  if (!accept(header, (t) => knownType(t, from))) {
    return null
  }
  return decodeBody(header.type, body, l)
}

function decodeBody(t: number, body: Uint8Array, l: Limits): Message {
  const r = new Reader(body)
  let m: Message
  switch (t) {
    case MessageType.Ping:
      m = { type: 'ping', nonce: r.u32(), time: r.u64() }
      break
    case MessageType.Pong:
      m = { type: 'pong', nonce: r.u32(), time: r.u64() }
      break
    case MessageType.Close:
      m = { type: 'close', code: r.u16(), reason: r.string() }
      break
    case MessageType.Hello:
      return decodeHello(r)
    case MessageType.Key:
      m = { type: 'key', seq: r.u32(), down: r.bool(), keysym: r.u32(), code: r.u16() }
      break
    case MessageType.Text:
      m = { type: 'text', seq: r.u32(), text: r.string() }
      break
    case MessageType.TermInput:
      m = { type: 'term-input', seq: r.u32(), data: r.bytes() }
      break
    case MessageType.Pointer:
      m = { type: 'pointer', seq: r.u32(), x: r.u16(), y: r.u16(), buttons: r.u8() }
      break
    case MessageType.Wheel: {
      const seq = r.u32()
      const dx = r.i32()
      const dy = r.i32()
      const mode = r.u8()
      if (mode > WheelMode.Lines) {
        throw new ProtocolError('invalid', `wheel mode ${mode}`)
      }
      m = { type: 'wheel', seq, dx, dy, mode }
      break
    }
    case MessageType.ResizeRequest:
      m = { type: 'resize-request', seq: r.u32(), width: r.u16(), height: r.u16() }
      break
    case MessageType.ClientClipboard:
      m = { type: 'client-clipboard', seq: r.u32(), mime: r.string(), data: r.bytes() }
      break
    case MessageType.View:
      m = { type: 'view', visible: r.bool(), x: r.u16(), y: r.u16(), w: r.u16(), h: r.u16() }
      break
    case MessageType.FrameAck:
      m = { type: 'frame-ack', frameId: r.u32(), decodeTime: r.u32() }
      break
    case MessageType.PromptReply: {
      const id = r.u32()
      const n = r.count(4)
      const answers: string[] = []
      for (let i = 0; i < n; i++) {
        answers.push(r.string())
      }
      m = { type: 'prompt-reply', id, answers }
      break
    }
    case MessageType.Welcome: {
      const viewerId = r.u32()
      const role = readRole(r)
      const cls = r.u8()
      if (cls !== SessionClass.Terminal && cls !== SessionClass.Graphical) {
        throw new ProtocolError('invalid', `class ${cls}`)
      }
      m = { type: 'welcome', viewerId, role, class: cls, sessionId: r.string(), capabilities: readCapabilities(r) }
      break
    }
    case MessageType.Resize:
      m = { type: 'resize', width: r.u16(), height: r.u16() }
      break
    case MessageType.Tile:
      return decodeTile(r, l)
    case MessageType.CopyRect:
      m = {
        type: 'copy-rect',
        frameId: r.u32(),
        srcX: r.u16(),
        srcY: r.u16(),
        dstX: r.u16(),
        dstY: r.u16(),
        w: r.u16(),
        h: r.u16(),
      }
      break
    case MessageType.FrameEnd:
      m = { type: 'frame-end', frameId: r.u32(), serverTime: r.u64(), lastSeq: r.u32() }
      break
    case MessageType.Cursor:
      return decodeCursor(r, l)
    case MessageType.CursorMove:
      m = { type: 'cursor-move', x: r.u16(), y: r.u16() }
      break
    case MessageType.TermData:
      m = { type: 'term-data', data: r.bytes() }
      break
    case MessageType.TermSnapshot:
      m = { type: 'term-snapshot', data: r.bytes() }
      break
    case MessageType.ServerClipboard:
      m = { type: 'server-clipboard', mime: r.string(), data: r.bytes() }
      break
    case MessageType.Bell:
      m = { type: 'bell' }
      break
    case MessageType.Title:
      m = { type: 'title', title: r.string() }
      break
    case MessageType.Prompt: {
      const id = r.u32()
      const name = r.string()
      const instruction = r.string()
      const n = r.count(5) // a label's length and a bool
      const fields: PromptField[] = []
      for (let i = 0; i < n; i++) {
        fields.push({ label: r.string(), secret: r.bool() })
      }
      m = { type: 'prompt', id, name, instruction, fields }
      break
    }
    case MessageType.ViewerJoined:
      m = { type: 'viewer-joined', viewerId: r.u32(), role: readRole(r), name: r.string() }
      break
    case MessageType.ViewerLeft:
      m = { type: 'viewer-left', viewerId: r.u32() }
      break
    default:
      throw new ProtocolError('unsupported', `type 0x${t.toString(16).padStart(4, '0')} is not a v1 message`)
  }
  r.finish()
  return m
}

function readRole(r: Reader): Role {
  const role = r.u8()
  if (role < Role.Owner || role > Role.ReadOnly) {
    throw new ProtocolError('invalid', `role ${role}`)
  }
  return role as Role
}

function decodeHello(r: Reader): Hello {
  const version = r.u16()
  if (version !== VERSION) {
    throw new ProtocolError('unsupported', `protocol version ${version}, want ${VERSION}`)
  }
  const m: Hello = {
    type: 'hello',
    width: r.u16(),
    height: r.u16(),
    cols: r.u16(),
    rows: r.u16(),
    visible: r.bool(),
    name: r.string(),
    capabilities: readCapabilities(r),
  }
  r.finish()
  return m
}

// readSide reads a tile's width or height: at least 1, at most the
// maximum texture size.
function readSide(r: Reader, what: string, l: Limits): number {
  const v = r.u16()
  if (v === 0) {
    throw new ProtocolError('invalid', `${what} 0`)
  }
  if (v > maxTextureSize(l)) {
    throw new ProtocolError('too-large', `${what} ${v}, limit ${maxTextureSize(l)}`)
  }
  return v
}

function decodeTile(r: Reader, l: Limits): Tile {
  const frameId = r.u32()
  const x = r.u16()
  const y = r.u16()
  const w = readSide(r, 'tile width', l)
  const h = readSide(r, 'tile height', l)
  const codec = r.u8()
  if (!isCodec(codec)) {
    throw new ProtocolError('invalid', `tile codec ${codec}`)
  }
  const slot = r.u16()
  const slots = Math.max(l.cacheSlots, 0)
  if (slot > slots) {
    throw new ProtocolError('invalid', `cache slot ${slot} of ${slots}`)
  }
  const payload = r.bytes()
  r.finish()
  if (slot !== 0 && w * h > MAX_CACHED_PIXELS) {
    throw new ProtocolError('invalid', `${w}x${h} tile in cache slot ${slot}, more than ${MAX_CACHED_PIXELS} pixels`)
  }
  if (codec === Codec.Cached && (slot === 0 || payload.length !== 0)) {
    throw new ProtocolError('invalid', `cached tile with slot ${slot} and a ${payload.length}-byte payload`)
  }
  if (codec === Codec.Raw && payload.length !== 4 * w * h) {
    throw new ProtocolError('invalid', `raw ${w}x${h} tile with ${payload.length} bytes`)
  }
  return { type: 'tile', frameId, x, y, w, h, codec, slot, payload }
}

function decodeCursor(r: Reader, l: Limits): Cursor {
  const side = (what: string): number => {
    const v = r.u16()
    if (v > maxTextureSize(l)) {
      throw new ProtocolError('too-large', `cursor ${what} ${v}, limit ${maxTextureSize(l)}`)
    }
    return v
  }
  const hotX = r.u16()
  const hotY = r.u16()
  const w = side('width')
  const h = side('height')
  const codec = r.u8()
  const payload = r.bytes()
  r.finish()
  if (w === 0 && h === 0) {
    if (hotX !== 0 || hotY !== 0 || codec !== 0 || payload.length !== 0) {
      throw new ProtocolError('invalid', 'hidden cursor with a hot spot, codec or payload')
    }
  } else if (w === 0 || h === 0) {
    throw new ProtocolError('invalid', `cursor of ${w}x${h}`)
  } else if (!isCodec(codec) || codec === Codec.Cached) {
    throw new ProtocolError('invalid', `cursor codec ${codec}`)
  } else if (hotX >= w || hotY >= h) {
    throw new ProtocolError('invalid', `hot spot ${hotX},${hotY} outside a ${w}x${h} cursor`)
  } else if (codec === Codec.Raw && payload.length !== 4 * w * h) {
    throw new ProtocolError('invalid', `raw ${w}x${h} cursor with ${payload.length} bytes`)
  }
  return { type: 'cursor', hotX, hotY, w, h, codec, payload }
}

// capabilityMinSize is the smallest entry: an ID and an empty value.
const capabilityMinSize = 2 + 4

function readCapabilities(r: Reader): Capabilities {
  const c = emptyCapabilities()
  const n = r.count(capabilityMinSize)
  let last = 0
  for (let i = 0; i < n; i++) {
    const id = r.u16()
    const v = r.bytes()
    if (id <= last) {
      throw new ProtocolError('invalid', `capability ${id} after ${last}`)
    }
    last = id
    switch (id) {
      case CapabilityID.Codecs:
        c.codecs = readCodecList(v)
        break
      case CapabilityID.MaxTextureSize:
        c.maxTextureSize = capValue(id, v, 2)
        break
      case CapabilityID.Terminal:
        c.terminal = capValue(id, v, 1)
        break
      case CapabilityID.PixelRatio:
        c.pixelRatio = capValue(id, v, 2)
        break
      case CapabilityID.CacheSlots:
        c.cacheSlots = capValue(id, v, 2)
        break
      case CapabilityID.FrameWindow:
        c.frameWindow = capValue(id, v, 1)
        break
      case CapabilityID.MaxMessageSize:
        c.maxMessageSize = capValue(id, v, 4)
        if (c.maxMessageSize < MIN_MESSAGE_SIZE) {
          throw new ProtocolError('invalid', `max_message_size ${c.maxMessageSize} is below ${MIN_MESSAGE_SIZE}`)
        }
        break
      default:
        c.unknown.push({ id, value: v })
    }
  }
  return c
}

// capValue reads the value of a capability that is a little-endian
// integer of size bytes, and refuses one of another size or zero.
function capValue(id: number, v: Uint8Array, size: number): number {
  if (v.length !== size) {
    throw new ProtocolError('invalid', `capability ${id} has ${v.length} bytes, want ${size}`)
  }
  let x = 0
  for (let i = size - 1; i >= 0; i--) {
    x = x * 256 + v[i]
  }
  if (x === 0) {
    throw new ProtocolError('invalid', `capability ${id} is zero`)
  }
  return x
}

// readCodecList reads the codecs capability: at least one codec, none 0
// and none twice. Unknown codecs are allowed.
function readCodecList(v: Uint8Array): number[] {
  if (v.length === 0) {
    throw new ProtocolError('invalid', 'empty codec list')
  }
  const seen = new Set<number>()
  for (const c of v) {
    if (c === 0 || seen.has(c)) {
      throw new ProtocolError('invalid', `codec ${c} listed twice or zero`)
    }
    seen.add(c)
  }
  return Array.from(v)
}

/** encode encodes m, header included, with flags 0. */
export function encode(m: Message): Uint8Array {
  const w = new Writer().header(messageType(m))
  switch (m.type) {
    case 'ping':
    case 'pong':
      w.u32(m.nonce).u64(m.time)
      break
    case 'close':
      w.u16(m.code).string(m.reason)
      break
    case 'hello':
      w.u16(VERSION).u16(m.width).u16(m.height).u16(m.cols).u16(m.rows).bool(m.visible).string(m.name)
      writeCapabilities(w, m.capabilities)
      break
    case 'key':
      w.u32(m.seq).bool(m.down).u32(m.keysym).u16(m.code)
      break
    case 'text':
      w.u32(m.seq).string(m.text)
      break
    case 'term-input':
      w.u32(m.seq).bytes(m.data)
      break
    case 'pointer':
      w.u32(m.seq).u16(m.x).u16(m.y).u8(m.buttons)
      break
    case 'wheel':
      w.u32(m.seq).i32(m.dx).i32(m.dy).u8(m.mode)
      break
    case 'resize-request':
      w.u32(m.seq).u16(m.width).u16(m.height)
      break
    case 'client-clipboard':
      w.u32(m.seq).string(m.mime).bytes(m.data)
      break
    case 'view':
      w.bool(m.visible).u16(m.x).u16(m.y).u16(m.w).u16(m.h)
      break
    case 'frame-ack':
      w.u32(m.frameId).u32(m.decodeTime)
      break
    case 'prompt-reply':
      w.u32(m.id).count(m.answers.length)
      for (const a of m.answers) {
        w.string(a)
      }
      break
    case 'welcome':
      w.u32(m.viewerId).u8(m.role).u8(m.class).string(m.sessionId)
      writeCapabilities(w, m.capabilities)
      break
    case 'resize':
      w.u16(m.width).u16(m.height)
      break
    case 'tile':
      w.u32(m.frameId).u16(m.x).u16(m.y).u16(m.w).u16(m.h).u8(m.codec).u16(m.slot).bytes(m.payload)
      break
    case 'copy-rect':
      w.u32(m.frameId).u16(m.srcX).u16(m.srcY).u16(m.dstX).u16(m.dstY).u16(m.w).u16(m.h)
      break
    case 'frame-end':
      w.u32(m.frameId).u64(m.serverTime).u32(m.lastSeq)
      break
    case 'cursor':
      w.u16(m.hotX).u16(m.hotY).u16(m.w).u16(m.h).u8(m.codec).bytes(m.payload)
      break
    case 'cursor-move':
      w.u16(m.x).u16(m.y)
      break
    case 'term-data':
    case 'term-snapshot':
      w.bytes(m.data)
      break
    case 'server-clipboard':
      w.string(m.mime).bytes(m.data)
      break
    case 'bell':
      break
    case 'title':
      w.string(m.title)
      break
    case 'prompt':
      w.u32(m.id).string(m.name).string(m.instruction).count(m.fields.length)
      for (const f of m.fields) {
        w.string(f.label).bool(f.secret)
      }
      break
    case 'viewer-joined':
      w.u32(m.viewerId).u8(m.role).string(m.name)
      break
    case 'viewer-left':
      w.u32(m.viewerId)
      break
  }
  return w.finish()
}

// writeCapabilities writes c as a list: the known capabilities that are
// set, in order of ID, then the unknown ones.
function writeCapabilities(w: Writer, c: Capabilities): void {
  const ints: [number, number, number][] = [
    [CapabilityID.MaxTextureSize, c.maxTextureSize, 2],
    [CapabilityID.Terminal, c.terminal, 1],
    [CapabilityID.PixelRatio, c.pixelRatio, 2],
    [CapabilityID.CacheSlots, c.cacheSlots, 2],
    [CapabilityID.FrameWindow, c.frameWindow, 1],
    [CapabilityID.MaxMessageSize, c.maxMessageSize, 4],
  ]
  const set = ints.filter(([, v]) => v !== 0)
  w.count((c.codecs.length > 0 ? 1 : 0) + set.length + c.unknown.length)
  if (c.codecs.length > 0) {
    w.u16(CapabilityID.Codecs).bytes(Uint8Array.from(c.codecs))
  }
  for (const [id, v, size] of set) {
    w.u16(id).u32(size)
    if (size === 1) {
      w.u8(v)
    } else if (size === 2) {
      w.u16(v)
    } else {
      w.u32(v)
    }
  }
  for (const u of c.unknown) {
    w.u16(u.id).bytes(u.value)
  }
}
