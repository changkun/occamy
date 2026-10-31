// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Session is a protocol v1 client's connection to one occamyd session.
// It mirrors the Go client (client/client.go, read.go, input.go): the
// caller pulls events with next(); a graphical frame is applied to the
// framebuffer when next() delivers it, never half of one, and
// acknowledged when next() is called again, which says the caller has
// shown it. Tiles decode as they arrive, through a TileDecoder that may
// run in Workers. The limits are the Go client's: the undelivered
// backlog is bounded by the screen's size, a frame's work by eight
// screens, sizes are checked before anything is allocated for them,
// and nothing is delivered after the end.

import type { Pixels } from './codecs'
import type { DecodeJob, TileDecoder } from './decode'
import {
  CloseError,
  CloseCode,
  ClosedError,
  ConnectionError,
  NotOwnerError,
  ReadOnlyError,
  abortError,
  asError,
  closeCodeFor,
} from './errors'
import type { CursorEvent, EndEvent, FrameEvent, Rect, SessionEvent } from './events'
import {
  type Capabilities,
  Codec,
  DEFAULT_FRAME_WINDOW,
  type Hello,
  type Message,
  Role,
  SessionClass,
  type Welcome,
  WheelMode,
  capabilityLimits,
  decode,
  emptyCapabilities,
  encode,
} from './messages'
import {
  DEFAULT_MAX_MESSAGE_SIZE,
  DEFAULT_MAX_TEXTURE_SIZE,
  type Limits,
  MIN_MESSAGE_SIZE,
  ProtocolError,
  checkSize,
} from './wire'

/**
 * Transport carries one protocol message per call, as a WebSocket does
 * with one binary message each. webSocketTransport adapts a WebSocket;
 * tests join a session to a scripted server with their own.
 */
export interface Transport {
  /** send sends a message. */
  send(msg: Uint8Array): void
  /** close closes the connection with a close code. */
  close(code: number, reason: string): void
  /**
   * The session sets these. A text message arrives as a string, and a
   * binary one as an ArrayBuffer that the session keeps: the transport
   * must not reuse it.
   */
  onmessage: ((data: ArrayBuffer | string) => void) | null
  /**
   * onclose reports that the connection closed: with the peer's close
   * code, or with 0 if it failed without one.
   */
  onclose: ((code: number, reason: string) => void) | null
}

/** The hello's name if Options.name is empty. */
export const DEFAULT_NAME = 'occamy-web'

/** The largest graphical screen a session takes, in pixels: 8192×8192, 256 MiB. */
export const DEFAULT_MAX_PIXELS = 8192 * 8192

/** The tile cache slots a session offers unless Options.cacheSlots says otherwise: 16 MiB of tiles at most. */
export const DEFAULT_CACHE_SLOTS = 1024

// The least the session holds of undelivered frames and events before
// it takes the server for broken, and the least work it allows a frame.
const MIN_BACKLOG = 64 << 20

// The undelivered small events; those that hold the latest value replace
// each other, so a server that is not broken stays far below.
const MAX_EVENTS = 1024

// The undelivered bells; more ring as one burst anyway.
const MAX_BELLS = 3

// The most the backlog follows the screen up to.
const MAX_BACKLOG = 1 << 30

// What an op of an undelivered frame holds beyond its message, counted
// in the backlog with it: its objects, a view of its message, and a
// decoding tile's job and pixels' array. Measured in Node with 200,000
// ops of 1×1 pixels: 339 bytes beyond the message for a raw tile, 118
// for a copy-rect and 405 for a deflate tile, decoded. Without it, a
// frame of tiny ops would hold about 13 times what it counts.
const OP_COST = 512

/** Options configure a session. */
export interface Options {
  /** Names the client and its version in hello; DEFAULT_NAME if empty. */
  name?: string
  /** The screen size the client asks for, in device pixels, if the session is graphical. */
  width?: number
  height?: number
  /** The terminal size the client asks for, in cells, if the session is a terminal; 0 for the server's default. */
  cols?: number
  rows?: number
  /** Starts the client hidden: the server sends no frames until view() shows it. */
  hidden?: boolean
  /**
   * The codecs to offer, in order of preference; the decoder's codecs()
   * by default. Raw is always offered after them, and then cached, unless
   * the client keeps no tile cache.
   */
  codecs?: number[]
  /**
   * The tile cache slots the client offers; DEFAULT_CACHE_SLOTS by
   * default, and 0 for no cache. A slot holds the pixels of one tile of
   * at most 4096 pixels, 16 KiB, which the server can then draw again by
   * naming the slot.
   */
  cacheSlots?: number
  /** The largest tile or cursor side the client decodes; 4096 by default. */
  maxTextureSize?: number
  /** Device pixels per CSS pixel in hundredths, such as 200; 0 states none (100). */
  pixelRatio?: number
  /** Unacknowledged frames the client allows; the server's default if 0. */
  frameWindow?: number
  /** The largest message the client takes, in bytes; 4 MiB by default, and at least 64 KiB. */
  maxMessageSize?: number
  /** The largest graphical screen the client shows, in pixels; DEFAULT_MAX_PIXELS by default. */
  maxPixels?: number
  /** The clock of frame-ack's decode time, in milliseconds; performance.now by default. */
  now?: () => number
  /**
   * The floor of the backlog and of a frame's work, in bytes: 64 MiB.
   * Tests lower it to reach the limits with small screens.
   */
  minBacklog?: number
}

/** The framebuffer of a graphical session: width × height RGBA pixels, row by row. */
export interface Framebuffer {
  width: number
  height: number
  data: Pixels
}

/** helloFor returns the hello a client of options sends, offering codecs. */
export function helloFor(options: Options, codecs: number[]): Hello {
  const offered: number[] = []
  for (const c of codecs) {
    if (c > 0 && c <= 0xff && c !== Codec.Raw && c !== Codec.Cached && !offered.includes(c)) {
      offered.push(c)
    }
  }
  offered.push(Codec.Raw)
  const slots = options.cacheSlots ?? DEFAULT_CACHE_SLOTS
  if (slots > 0) {
    offered.push(Codec.Cached)
  }
  const caps = emptyCapabilities()
  caps.codecs = offered
  caps.cacheSlots = slots
  caps.maxTextureSize = options.maxTextureSize ?? 0
  caps.terminal = 1
  caps.pixelRatio = options.pixelRatio ?? 0
  caps.frameWindow = options.frameWindow ?? 0
  caps.maxMessageSize = options.maxMessageSize ?? 0
  return {
    type: 'hello',
    width: options.width ?? 0,
    height: options.height ?? 0,
    cols: options.cols ?? 0,
    rows: options.rows ?? 0,
    visible: !(options.hidden ?? false),
    name: options.name !== undefined && options.name !== '' ? options.name : DEFAULT_NAME,
    capabilities: caps,
  }
}

/** checkOptions refuses options the protocol does not allow. */
export function checkOptions(o: Options): void {
  const u16 = (v: number | undefined): boolean => v === undefined || (Number.isInteger(v) && v >= 0 && v <= 0xffff)
  if (![o.width, o.height, o.cols, o.rows, o.maxTextureSize, o.pixelRatio].every(u16)) {
    throw new RangeError('occamy: a size or ratio outside 0 to 65535')
  }
  if (!u16(o.cacheSlots)) {
    throw new RangeError('occamy: cache slots outside 0 to 65535')
  }
  if (o.frameWindow !== undefined && !(Number.isInteger(o.frameWindow) && o.frameWindow >= 0 && o.frameWindow <= 0xff)) {
    throw new RangeError('occamy: a frame window outside 0 to 255')
  }
  const m = o.maxMessageSize
  if (m !== undefined && m !== 0 && !(Number.isInteger(m) && m >= MIN_MESSAGE_SIZE && m <= 0xffffffff)) {
    throw new RangeError(`occamy: a max_message_size below ${MIN_MESSAGE_SIZE}`)
  }
  if (o.maxPixels !== undefined && !(o.maxPixels >= 0)) {
    throw new RangeError('occamy: a negative maxPixels')
  }
}

// The limits the client accepts before welcome: its own.
function ownLimits(o: Options): Limits {
  return {
    maxMessageSize: o.maxMessageSize !== undefined && o.maxMessageSize > 0 ? o.maxMessageSize : DEFAULT_MAX_MESSAGE_SIZE,
    maxTextureSize: o.maxTextureSize !== undefined && o.maxTextureSize > 0 ? o.maxTextureSize : DEFAULT_MAX_TEXTURE_SIZE,
    cacheSlots: 0,
  }
}

const orDefault = (v: number, def: number): number => (v === 0 ? def : v)

/**
 * agree returns the capabilities the client uses from those the server
 * agreed to: none beyond what it offered, and no limit above its own,
 * whatever the server states.
 */
function agree(w: Capabilities, offered: Capabilities): Capabilities {
  const a = emptyCapabilities()
  a.codecs = w.codecs.filter((c) => offered.codecs.includes(c))
  a.maxTextureSize = Math.min(orDefault(w.maxTextureSize, DEFAULT_MAX_TEXTURE_SIZE), orDefault(offered.maxTextureSize, DEFAULT_MAX_TEXTURE_SIZE))
  a.terminal = Math.min(w.terminal, offered.terminal)
  a.pixelRatio = offered.pixelRatio
  a.frameWindow = Math.min(orDefault(w.frameWindow, DEFAULT_FRAME_WINDOW), orDefault(offered.frameWindow, DEFAULT_FRAME_WINDOW))
  a.maxMessageSize = Math.min(orDefault(w.maxMessageSize, DEFAULT_MAX_MESSAGE_SIZE), orDefault(offered.maxMessageSize, DEFAULT_MAX_MESSAGE_SIZE))
  a.cacheSlots = Math.min(w.cacheSlots, offered.cacheSlots)
  return a
}

// browserCloseCode is the code a browser may close a WebSocket with:
// 1000 or 3000 to 4999. The close message carries the protocol's code
// when it is another.
function browserCloseCode(code: number): number {
  return code === CloseCode.Normal || (code >= 3000 && code <= 4999) ? code : CloseCode.Normal
}

// The longest reason a WebSocket close frame holds.
const MAX_CLOSE_REASON = 123

function closeTransport(t: Transport, code: number, reason: string): void {
  try {
    t.close(browserCloseCode(code), new TextEncoder().encode(reason).length > MAX_CLOSE_REASON ? '' : reason)
  } catch {
    // Closed already.
  }
}

function reasonFor(code: number): string {
  switch (code) {
    case CloseCode.Unsupported:
      return 'unsupported'
    case CloseCode.TooLarge:
      return 'too large'
  }
  return 'protocol error'
}

// failedCode reports whether a WebSocket close code says the connection
// closed without a code of its peer's: 1005 no status, 1006 abnormal,
// 1015 TLS.
function failedCode(code: number): boolean {
  return code === 0 || code === 1005 || code === 1006 || code === 1015
}

// A tile of a codec that decodes, settled once pixels or error is set.
interface Job {
  done: Promise<void>
  pixels: Pixels | null
  error: Error | null
}

// A tile cache slot: the tile stored in it, width × height, whose pixels
// are raw, or job's once it is decoded.
interface Slot {
  width: number
  height: number
  raw: Uint8Array | null
  job: Job | null
}

type Op =
  | { kind: 'resize'; width: number; height: number; bytes: number; work: number }
  | { kind: 'tile'; rect: Rect; raw: Uint8Array | null; job: Job | null; bytes: number; work: number }
  | { kind: 'copy'; rect: Rect; srcX: number; srcY: number; bytes: number; work: number }
  | { kind: 'term-data' | 'term-snapshot'; data: Uint8Array; bytes: number; work: number }

// A frame being read, or read and not yet delivered.
interface Frame {
  ops: Op[]
  id: number
  lastSeq: number
  serverTime: number
  arrived: number
  bytes: number // what its ops hold, counted in the backlog
  work: number // bytes of pixels its ops write or allocate
  area: number // pixels of the largest screen it was read for
}

// An undelivered item: a small event, a cursor that decodes, or a
// complete frame.
type Item =
  | { kind: 'event'; ev: SessionEvent; bytes: number }
  | { kind: 'cursor'; ev: Promise<CursorEvent>; bytes: number }
  | { kind: 'frame'; f: Frame }

/** Session is a client's connection to one occamyd session. */
export class Session {
  /** This viewer's ID, as viewer-joined and viewer-left name viewers. */
  readonly viewerId: number
  readonly role: Role
  readonly sessionClass: SessionClass
  /** The session's ID, for joining and sharing it. */
  readonly sessionId: string
  /**
   * The capabilities the client uses: those the server agreed to within
   * what the client offered, with the frame window and the maximum
   * message and texture sizes always stated.
   */
  readonly capabilities: Capabilities

  private readonly transport: Transport
  private readonly decoder: TileDecoder
  private readonly ownsDecoder: boolean
  private readonly limits: Limits
  private readonly window: number
  private readonly maxPixels: number
  private readonly minBacklog: number
  private readonly now: () => number

  // The reader's state.
  private wireW = 0
  private wireH = 0
  private sized = false
  private lastEnd = 0
  private open: Frame | null = null

  // What next() delivers.
  private queue: Item[] = []
  private pending: SessionEvent[] = []
  private pendingArrived = 0 // when the frame-end of the frame in pending arrived
  private frames = 0
  private backlog = 0
  private end: EndEvent | null = null
  private delivered = false
  private discard = false
  private shown: { id: number; arrived: number } | null = null
  private waiter: (() => void) | null = null
  private nexting = false
  private screenW = 0
  private screenH = 0
  private fb: Framebuffer = { width: 0, height: 0, data: new Uint8ClampedArray(0) }
  private readonly slots = new Map<number, Slot>() // the tile cache

  // The writer's state.
  private seq = 0
  private stopped = false // nothing more is sent
  private readonly closed: Promise<void>
  private resolveClosed: () => void = () => undefined

  /**
   * start runs a session over transport, whose server was sent the hello
   * of options with codecs (helloFor): it waits for welcome and rejects
   * with a CloseError if the server closed instead, such as for a
   * refused token. connect() is start over a WebSocket.
   */
  static start(
    transport: Transport,
    options: Options,
    decoder: TileDecoder,
    codecs: number[],
    { ownsDecoder = false, signal }: { ownsDecoder?: boolean; signal?: AbortSignal } = {},
  ): Promise<Session> {
    checkOptions(options)
    const offered = helloFor(options, codecs).capabilities
    const limits = ownLimits(options)
    return new Promise<Session>((resolve, reject) => {
      const done = (err: unknown): void => {
        transport.onmessage = null
        transport.onclose = null
        signal?.removeEventListener('abort', onAbort)
        if (ownsDecoder) {
          decoder.close()
        }
        reject(asError(err))
      }
      const onAbort = (): void => {
        closeTransport(transport, CloseCode.Normal, '')
        done(abortError(signal as AbortSignal))
      }
      if (signal?.aborted === true) {
        onAbort()
        return
      }
      signal?.addEventListener('abort', onAbort)
      transport.onclose = (code, reason) => {
        done(failedCode(code) ? new ConnectionError(`connection closed before welcome (${code})`) : new CloseError(code, reason))
      }
      transport.onmessage = (data) => {
        let m: Message | null
        try {
          if (typeof data === 'string') {
            throw new ProtocolError('unsupported', 'a text message')
          }
          m = decode(new Uint8Array(data), 'server', limits)
          if (m === null) {
            return // an ignorable message of a later version
          }
          if (m.type === 'close') {
            closeTransport(transport, m.code, m.reason)
            done(new CloseError(m.code, m.reason))
            return
          }
          if (m.type !== 'welcome') {
            throw new ProtocolError('invalid', `${m.type} before welcome`)
          }
        } catch (err) {
          const code = closeCodeFor(err)
          try {
            transport.send(encode({ type: 'close', code, reason: reasonFor(code) }))
          } catch {
            // Closed already.
          }
          closeTransport(transport, code, reasonFor(code))
          done(err)
          return
        }
        const s = new Session(transport, options, decoder, ownsDecoder, m, offered)
        const why = s.unsupported()
        if (why !== '') {
          try {
            transport.send(encode({ type: 'close', code: CloseCode.Unsupported, reason: why }))
          } catch {
            // Closed already.
          }
          closeTransport(transport, CloseCode.Unsupported, why)
          done(new ProtocolError('unsupported', why))
          return
        }
        signal?.removeEventListener('abort', onAbort)
        s.attach()
        resolve(s)
      }
    })
  }

  private constructor(
    transport: Transport,
    options: Options,
    decoder: TileDecoder,
    ownsDecoder: boolean,
    w: Welcome,
    offered: Capabilities,
  ) {
    this.transport = transport
    this.decoder = decoder
    this.ownsDecoder = ownsDecoder
    this.viewerId = w.viewerId
    this.role = w.role
    this.sessionClass = w.class
    this.sessionId = w.sessionId
    this.capabilities = agree(w.capabilities, offered)
    this.limits = capabilityLimits(this.capabilities)
    this.window = this.capabilities.frameWindow
    this.maxPixels = options.maxPixels !== undefined && options.maxPixels > 0 ? options.maxPixels : DEFAULT_MAX_PIXELS
    this.minBacklog = options.minBacklog ?? MIN_BACKLOG
    this.now = options.now ?? (() => performance.now())
    this.closed = new Promise((resolve) => {
      this.resolveClosed = resolve
    })
  }

  // unsupported says why the client cannot show the session, or "". A
  // cached tile names a slot that an earlier tile filled, so a graphical
  // session needs a codec in common besides cached.
  private unsupported(): string {
    if (this.sessionClass === SessionClass.Graphical && !this.capabilities.codecs.some((c) => c !== Codec.Cached)) {
      return 'no codec in common'
    }
    if (this.sessionClass === SessionClass.Terminal && this.capabilities.terminal === 0) {
      return 'no terminal level in common'
    }
    return ''
  }

  private attach(): void {
    this.transport.onmessage = (data) => this.read(data)
    this.transport.onclose = (code, reason) => {
      this.stopped = true
      if (this.end === null) {
        this.fail(failedCode(code) ? new ConnectionError(`connection closed (${code})`) : new CloseError(code, reason))
      }
      this.resolveClosed()
    }
  }

  private graphical(): boolean {
    return this.sessionClass === SessionClass.Graphical
  }

  /**
   * The screen's size as of the last resize next() delivered: pixels for
   * a graphical session, cells for a terminal; zero until the first frame.
   */
  get size(): { width: number; height: number } {
    return { width: this.screenW, height: this.screenH }
  }

  /**
   * The screen of a graphical session as of the last frame next()
   * delivered. Its pixels change when next() delivers the next frame;
   * read them before calling next() again. Empty before the first frame.
   */
  get framebuffer(): Readonly<Framebuffer> {
    return this.fb
  }

  /** Whether the session has ended. */
  get ended(): boolean {
    return this.end !== null
  }

  // read handles one message from the server. Messages within a frame
  // join the open frame, which next() applies once it is complete; the
  // rest become events at once.
  private read(data: ArrayBuffer | string): void {
    if (this.end !== null) {
      return // what arrives after the end is not delivered
    }
    try {
      if (typeof data === 'string') {
        throw new ProtocolError('unsupported', 'a text message')
      }
      this.handle(new Uint8Array(data))
    } catch (err) {
      this.fail(err)
    }
  }

  private handle(b: Uint8Array): void {
    const m = decode(b, 'server', this.limits)
    if (m === null) {
      return // an ignorable message of a later version
    }
    const n = b.length
    switch (m.type) {
      case 'resize':
        return this.onResize(m.width, m.height, n)
      case 'tile':
        return this.tile(m.frameId, { x: m.x, y: m.y, width: m.w, height: m.h }, m.codec, m.slot, m.payload, n)
      case 'copy-rect':
        return this.copyRect(m.frameId, m.srcX, m.srcY, { x: m.dstX, y: m.dstY, width: m.w, height: m.h }, n)
      case 'frame-end':
        return this.frameEnd(m.frameId, m.serverTime, m.lastSeq)
      case 'term-snapshot':
      case 'term-data':
        return this.term(m.type, m.data, n)
      case 'cursor':
        return this.cursor(m.hotX, m.hotY, m.w, m.h, m.codec, m.payload)
      case 'cursor-move':
        return this.event({ type: 'cursor-move', x: m.x, y: m.y }, n)
      case 'server-clipboard':
        return this.event({ type: 'clipboard', mime: m.mime, data: m.data }, n)
      case 'bell':
        return this.event({ type: 'bell' }, n)
      case 'title':
        return this.event({ type: 'title', title: m.title }, n)
      case 'prompt':
        return this.event({ type: 'prompt', id: m.id, name: m.name, instruction: m.instruction, fields: m.fields }, n)
      case 'viewer-joined':
        return this.event({ type: 'viewer-joined', viewerId: m.viewerId, role: m.role, name: m.name }, n)
      case 'viewer-left':
        return this.event({ type: 'viewer-left', viewerId: m.viewerId }, n)
      case 'ping':
        this.write({ type: 'pong', nonce: m.nonce, time: m.time })
        return
      case 'pong':
        return
      case 'close':
        throw new CloseError(m.code, m.reason)
      case 'welcome':
        throw new ProtocolError('invalid', 'a second welcome')
    }
    throw new ProtocolError('unsupported', `${m.type} from the server`)
  }

  private onResize(w: number, h: number, n: number): void {
    if (!this.graphical()) {
      ;[this.wireW, this.wireH, this.sized] = [w, h, true]
      return this.add({ kind: 'resize', width: w, height: h, bytes: n, work: 0 })
    }
    if (w * h > this.maxPixels) {
      throw new ProtocolError('unsupported', `a ${w}x${h} screen, more than ${this.maxPixels} pixels`)
    }
    ;[this.wireW, this.wireH, this.sized] = [w, h, true]
    const f = this.open
    if (f !== null) {
      // A resize starts the screen afresh, so what the frame drew before
      // it is never applied, and resizes in a row cost one. Tiles that
      // are being decoded stay counted until the frame is delivered.
      let bytes = 0
      let work = 0
      for (const o of f.ops) {
        if (o.kind !== 'tile' || o.job === null) {
          bytes += o.bytes
          work += o.work
        }
      }
      f.ops = []
      f.bytes -= bytes
      f.work -= work
      this.backlog -= bytes
    }
    this.add({ kind: 'resize', width: w, height: h, bytes: n, work: 4 * w * h })
  }

  // screen checks that r lies within the screen as the messages read so
  // far left it. An empty rectangle lies within any screen.
  private screen(what: string, r: Rect): void {
    const empty = r.width <= 0 || r.height <= 0
    if (!this.sized || (!empty && (r.x + r.width > this.wireW || r.y + r.height > this.wireH))) {
      throw new ProtocolError('invalid', `${what} ${r.width}x${r.height}+${r.x}+${r.y} outside the screen ${this.wireW}x${this.wireH}`)
    }
  }

  // inFrame checks that a message of frame id belongs to the open frame.
  private inFrame(id: number): void {
    const want = (this.lastEnd + 1) >>> 0
    if (id !== want) {
      throw new ProtocolError('invalid', `a message of frame ${id} in frame ${want}`)
    }
  }

  // tile adds a tile to the open frame. One that names a slot is stored
  // in it from now on, whatever becomes of its frame, so that a cached
  // tile later in the frame draws it.
  private tile(frameId: number, r: Rect, codec: Codec, slot: number, payload: Uint8Array, n: number): void {
    if (!this.graphical()) {
      throw new ProtocolError('invalid', 'a tile in a terminal session')
    }
    this.inFrame(frameId)
    this.screen('tile', r)
    if (!this.capabilities.codecs.includes(codec)) {
      throw new ProtocolError('invalid', `a tile of codec ${codec}, which was not agreed`)
    }
    const pixels = 4 * r.width * r.height
    if (codec === Codec.Cached) {
      const c = this.slots.get(slot)
      if (c === undefined || c.width !== r.width || c.height !== r.height) {
        const holds = c === undefined ? 'nothing' : `${c.width}x${c.height}`
        throw new ProtocolError('invalid', `a ${r.width}x${r.height} tile from cache slot ${slot}, which holds ${holds}`)
      }
      return this.add({ kind: 'tile', rect: r, raw: c.raw, job: c.job, bytes: n, work: pixels })
    }
    if (codec === Codec.Raw) {
      // The payload views the message's buffer, which the session keeps.
      this.add({ kind: 'tile', rect: r, raw: payload, job: null, bytes: n, work: pixels })
      if (slot !== 0) {
        this.slots.set(slot, { width: r.width, height: r.height, raw: payload, job: null })
      }
      return
    }
    // The decoded pixels count from the start, whatever the payload's size.
    const o: Op = { kind: 'tile', rect: r, raw: null, job: null, bytes: n + pixels, work: pixels }
    this.add(o)
    o.job = this.submit({ codec, payload, width: r.width, height: r.height })
    if (slot !== 0) {
      this.slots.set(slot, { width: r.width, height: r.height, raw: null, job: o.job })
    }
  }

  // submit starts decoding job. A payload that does not decode ends the
  // session at once, with protocol-error.
  private submit(job: DecodeJob): Job {
    const j: Job = { done: Promise.resolve(), pixels: null, error: null }
    j.done = this.decoder.decode(job).then(
      (pixels) => {
        j.pixels = pixels
      },
      (err: unknown) => {
        j.error = asError(err)
        if (this.end === null) {
          this.fail(err)
        }
      },
    )
    return j
  }

  private copyRect(frameId: number, srcX: number, srcY: number, dst: Rect, n: number): void {
    if (!this.graphical()) {
      throw new ProtocolError('invalid', 'a copy-rect in a terminal session')
    }
    this.inFrame(frameId)
    this.screen('copy-rect destination', dst)
    this.screen('copy-rect source', { x: srcX, y: srcY, width: dst.width, height: dst.height })
    if (dst.width === 0 || dst.height === 0) {
      return // copies nothing
    }
    this.add({ kind: 'copy', rect: dst, srcX, srcY, bytes: n, work: 4 * dst.width * dst.height })
  }

  private term(kind: 'term-data' | 'term-snapshot', data: Uint8Array, n: number): void {
    if (this.graphical()) {
      throw new ProtocolError('invalid', 'terminal data in a graphical session')
    }
    this.add({ kind, data, bytes: n, work: 0 })
  }

  // add adds o to the open frame. o holds its message's bytes and, as
  // the session counts it, OP_COST.
  private add(o: Op): void {
    this.open ??= { ops: [], id: 0, lastSeq: 0, serverTime: 0, arrived: 0, bytes: 0, work: 0, area: 0 }
    const f = this.open
    o.bytes += OP_COST
    f.ops.push(o)
    f.bytes += o.bytes
    f.work += o.work
    f.area = Math.max(f.area, this.wireW * this.wireH)
    const limit = this.maxWork(f.area)
    if (f.work > limit) {
      throw new ProtocolError('invalid', `a frame that writes ${f.work} bytes of pixels, more than ${limit}`)
    }
    this.backlog += o.bytes
    this.checkBacklog()
  }

  private frameEnd(id: number, serverTime: bigint, lastSeq: number): void {
    const want = (this.lastEnd + 1) >>> 0
    if (id !== want) {
      throw new ProtocolError('invalid', `frame-end ${id}, want ${want}`)
    }
    this.lastEnd = id
    const f = this.open ?? { ops: [], id: 0, lastSeq: 0, serverTime: 0, arrived: 0, bytes: 0, work: 0, area: 0 }
    this.open = null
    f.id = id
    f.lastSeq = lastSeq
    // Microseconds since the session started; a server that sends more
    // than 2⁵³ loses precision, not order.
    f.serverTime = Number(serverTime)
    f.arrived = this.now()
    f.area = Math.max(f.area, this.wireW * this.wireH)
    // With this one, the frames the server sent that the client has not
    // acknowledged: those queued, and the one taken from the queue whose
    // acknowledgement waits for the next call of next().
    if (this.frames + this.unacknowledged() > this.window) {
      throw new ProtocolError('invalid', `more than ${this.window + 1} frames unacknowledged`)
    }
    this.queue.push({ kind: 'frame', f })
    this.frames++
    this.poke()
  }

  // unacknowledged counts the frame next() took from the queue and has
  // not acknowledged: one that is being delivered, or was delivered last.
  private unacknowledged(): number {
    return this.shown !== null || this.pending.some((ev) => ev.type === 'frame') ? 1 : 0
  }

  // maxBacklog is the most the session holds of undelivered frames and
  // events. A server that follows the protocol keeps at most the frame
  // window and one more frame unacknowledged, each about the screen's
  // size, and sends the small state as it changes, which the session
  // coalesces; one that sends more than three screens a frame beyond that
  // is taken for broken. The screen is the largest of the frames held, so
  // that a frame of a large screen still queued does not count against
  // the smaller one the next frame resizes to. A terminal's frames hold
  // text, not pixels: it holds the floor.
  private maxBacklog(): number {
    if (!this.graphical()) {
      return this.minBacklog
    }
    let area = this.wireW * this.wireH
    if (this.open !== null) {
      area = Math.max(area, this.open.area)
    }
    for (const it of this.queue) {
      if (it.kind === 'frame') {
        area = Math.max(area, it.f.area)
      }
    }
    return Math.max(Math.min((this.window + 2) * 3 * 4 * area, MAX_BACKLOG), this.minBacklog)
  }

  // maxWork is the most bytes of pixels a frame of a screen of area
  // pixels may write or allocate: eight screens. A copy-rect or a resize
  // costs a few bytes to send and a screen to apply; this bounds the work
  // one next() can be made to do.
  private maxWork(area: number): number {
    return Math.max(8 * 4 * area, this.minBacklog)
  }

  private checkBacklog(): void {
    const limit = this.maxBacklog()
    if (this.backlog > limit) {
      throw new ProtocolError('invalid', `${this.backlog} bytes of frames and events undelivered, more than ${limit}`)
    }
  }

  // cursor queues a cursor's image, decoding it unless it is raw. Its
  // pixels are checked against the backlog before they are allocated: a
  // cursor of an image codec may claim max_texture_size² pixels in a few
  // bytes.
  private cursor(hotX: number, hotY: number, w: number, h: number, codec: number, payload: Uint8Array): void {
    if (w === 0) {
      return this.event({ type: 'cursor', image: null, hotX: 0, hotY: 0 }, 0)
    }
    if (!this.capabilities.codecs.includes(codec)) {
      throw new ProtocolError('invalid', `a cursor of codec ${codec}, which was not agreed`)
    }
    const n = 4 * w * h
    if (codec === Codec.Raw) {
      const pixels = new Uint8ClampedArray(payload)
      return this.event({ type: 'cursor', image: { width: w, height: h, pixels }, hotX, hotY }, n)
    }
    this.cursorFits(n)
    if (this.end !== null) {
      return
    }
    const decoded = this.decoder.decode({ codec, payload, width: w, height: h }).then(
      (pixels): CursorEvent => ({ type: 'cursor', image: { width: w, height: h, pixels }, hotX, hotY }),
    )
    decoded.catch((err: unknown) => {
      if (this.end === null) {
        this.fail(err)
      }
    })
    this.enqueue({ kind: 'cursor', ev: decoded, bytes: n })
  }

  // cursorFits checks that a cursor of n bytes of pixels, in place of an
  // undelivered one, keeps the backlog within its bound.
  private cursorFits(n: number): void {
    let backlog = this.backlog + n
    for (let i = this.queue.length - 1; i >= 0; i--) {
      const it = this.queue[i]
      if (it.kind === 'cursor' || (it.kind === 'event' && it.ev.type === 'cursor')) {
        backlog -= it.bytes
        break
      }
    }
    const limit = this.maxBacklog()
    if (backlog > limit) {
      throw new ProtocolError('invalid', `a cursor of ${n} bytes, with ${this.backlog} bytes undelivered, more than ${limit}`)
    }
  }

  // event queues a small event that holds n bytes.
  private event(ev: SessionEvent, n: number): void {
    if (this.end !== null) {
      return // nothing is delivered after the end
    }
    this.enqueue({ kind: 'event', ev, bytes: n })
  }

  // enqueue queues an item. An event that holds the latest value
  // replaces an undelivered one of its kind, a viewer that left cancels
  // its undelivered join, and bells beyond a few are dropped.
  private enqueue(item: Exclude<Item, { kind: 'frame' }>): void {
    const type = item.kind === 'cursor' ? 'cursor' : item.ev.type
    if (type === 'title' || type === 'clipboard' || type === 'cursor' || type === 'cursor-move') {
      for (let i = this.queue.length - 1; i >= 0; i--) {
        const it = this.queue[i]
        const itType = it.kind === 'cursor' ? 'cursor' : it.kind === 'event' ? it.ev.type : null
        if (itType === type) {
          this.backlog += item.bytes - (it as { bytes: number }).bytes
          this.queue[i] = item
          return this.checkBacklog()
        }
      }
    } else if (type === 'bell') {
      if (this.queue.filter((it) => it.kind === 'event' && it.ev.type === 'bell').length >= MAX_BELLS) {
        return
      }
    } else if (item.kind === 'event' && item.ev.type === 'viewer-left') {
      const id = item.ev.viewerId
      const i = this.queue.findIndex((it) => it.kind === 'event' && it.ev.type === 'viewer-joined' && it.ev.viewerId === id)
      if (i >= 0) {
        this.backlog -= (this.queue[i] as { bytes: number }).bytes
        this.queue.splice(i, 1)
        return
      }
    }
    if (this.queue.length - this.frames >= MAX_EVENTS) {
      throw new ProtocolError('invalid', `more than ${MAX_EVENTS} events undelivered`)
    }
    this.queue.push(item)
    this.backlog += item.bytes
    this.poke()
    this.checkBacklog()
  }

  private poke(): void {
    const w = this.waiter
    this.waiter = null
    w?.()
  }

  /**
   * next returns the session's next event, waiting until there is one.
   * A frame of a graphical session is applied to the framebuffer as
   * next() delivers it, a resize first if the frame resized the screen.
   * Calling next() again says the caller has shown the frame, and
   * acknowledges it. Once the session ended, next() delivers what arrived
   * before the end, then the end, and then rejects with ClosedError.
   * After close(), it delivers the end at once.
   *
   * signal cancels a wait for an event or for a frame's tiles to decode,
   * and leaves the frame queued for the next call. One call at a time.
   */
  async next(signal?: AbortSignal): Promise<SessionEvent> {
    if (this.nexting) {
      throw new Error('occamy: next() called while another call waits')
    }
    this.nexting = true
    try {
      return await this.nextEvent(signal)
    } finally {
      this.nexting = false
    }
  }

  private async nextEvent(signal?: AbortSignal): Promise<SessionEvent> {
    this.ackShown()
    for (;;) {
      if (this.delivered) {
        throw new ClosedError()
      }
      if (this.discard) {
        this.dropQueue() // what the reader added since close()
      }
      const ev = this.pending.shift()
      if (ev !== undefined) {
        this.delivering(ev)
        return ev
      }
      const it = this.queue[0] as Item | undefined
      if (it !== undefined) {
        if (it.kind === 'event') {
          this.queue.shift()
          this.backlog -= it.bytes
          return it.ev
        }
        if (it.kind === 'cursor') {
          let ev: CursorEvent | null = null
          try {
            ev = await abortable(it.ev, signal)
          } catch (err) {
            if (signal?.aborted === true) {
              throw err
            }
          }
          if (this.queue[0] !== it) {
            continue // replaced, or dropped by close()
          }
          if (ev === null) {
            // The cursor did not decode, which ended the session: what
            // follows is not delivered.
            this.dropQueue()
            continue
          }
          this.queue.shift()
          this.backlog -= it.bytes
          return ev
        }
        await abortable(Promise.all(it.f.ops.flatMap((o) => (o.kind === 'tile' && o.job !== null ? [o.job.done] : []))), signal)
        if (this.queue[0] !== it) {
          continue // dropped by close()
        }
        this.queue.shift()
        this.backlog -= it.f.bytes
        this.frames--
        try {
          this.pending = this.apply(it.f)
        } catch (err) {
          // What follows builds on the frame.
          this.dropQueue()
          this.fail(err)
        }
        continue
      }
      if (this.end !== null) {
        this.delivered = true
        if (this.ownsDecoder) {
          this.decoder.close()
        }
        return this.end
      }
      await new Promise<void>((resolve, reject) => {
        if (signal?.aborted === true) {
          reject(abortError(signal))
          return
        }
        const onAbort = (): void => {
          this.waiter = null
          reject(abortError(signal as AbortSignal))
        }
        signal?.addEventListener('abort', onAbort, { once: true })
        this.waiter = () => {
          signal?.removeEventListener('abort', onAbort)
          resolve()
        }
      })
    }
  }

  private dropQueue(): void {
    for (const it of this.queue) {
      if (it.kind === 'frame') {
        this.backlog -= it.f.bytes
        this.frames--
      } else {
        this.backlog -= it.bytes
      }
    }
    this.queue = []
    this.pending = []
  }

  // delivering records what delivering ev means: a frame is acknowledged
  // once the caller comes back for the next event, and a resize sets the
  // size.
  private delivering(ev: SessionEvent): void {
    if (ev.type === 'frame') {
      this.shown = { id: ev.id, arrived: this.pendingArrived }
    } else if (ev.type === 'resize') {
      ;[this.screenW, this.screenH] = [ev.width, ev.height]
    }
  }

  // ackShown acknowledges the frame next() delivered last, which the
  // caller has shown now that it asks for the next event. Its decode time
  // runs from its frame-end arriving until now.
  private ackShown(): void {
    const f = this.shown
    if (f === null) {
      return
    }
    this.shown = null
    const us = Math.round((this.now() - f.arrived) * 1000)
    this.write({ type: 'frame-ack', frameId: f.id, decodeTime: Math.min(Math.max(us, 0), 0xffffffff) })
  }

  // apply applies frame f, whose tiles are decoded, and returns its
  // events.
  private apply(f: Frame): SessionEvent[] {
    const frame: FrameEvent = { type: 'frame', id: f.id, lastSeq: f.lastSeq, serverTime: f.serverTime, dirty: [] }
    this.pendingArrived = f.arrived
    const evs: SessionEvent[] = []
    if (!this.graphical()) {
      for (const o of f.ops) {
        if (o.kind === 'resize') {
          evs.push({ type: 'resize', width: o.width, height: o.height })
        } else if (o.kind === 'term-data' || o.kind === 'term-snapshot') {
          evs.push({ type: o.kind, data: o.data })
        }
      }
      evs.push(frame)
      return evs
    }
    for (const o of f.ops) {
      if (o.kind === 'tile' && o.job !== null && o.job.error !== null) {
        throw o.job.error
      }
    }
    let resized = false
    for (const o of f.ops) {
      switch (o.kind) {
        case 'resize':
          this.fb = newFramebuffer(o.width, o.height)
          resized = true
          break
        case 'tile':
          blit(this.fb, o.rect, o.raw ?? (o.job?.pixels as Pixels))
          if (!resized) {
            frame.dirty.push(o.rect)
          }
          break
        case 'copy':
          copyRect(this.fb, o.rect, o.srcX, o.srcY)
          if (!resized) {
            frame.dirty.push(o.rect)
          }
          break
      }
    }
    if (resized) {
      evs.push({ type: 'resize', width: this.fb.width, height: this.fb.height })
      frame.dirty = [{ x: 0, y: 0, width: this.fb.width, height: this.fb.height }]
    }
    evs.push(frame)
    return evs
  }

  // fail ends the session over err: a protocol error by the server, which
  // it is told with close, a close by the server, or a failed connection.
  private fail(err: unknown): void {
    if (err instanceof CloseError) {
      this.terminate({ type: 'end', code: err.code, reason: err.reason }, null)
    } else if (err instanceof ProtocolError) {
      const code = closeCodeFor(err)
      const reason = reasonFor(code)
      this.terminate({ type: 'end', code, reason, error: err }, { code, reason })
    } else {
      this.terminate({ type: 'end', code: 0, reason: '', error: asError(err) }, null)
    }
  }

  // terminate ends the session with end, once. If close is set, the
  // server is told with a close message first; then the connection is
  // closed with the same code.
  private terminate(end: EndEvent, close: { code: number; reason: string } | null): void {
    if (this.end !== null) {
      return
    }
    this.end = end
    this.poke()
    if (close !== null) {
      this.write({ type: 'close', code: close.code, reason: close.reason })
    }
    this.stopped = true
    closeTransport(this.transport, end.code === 0 ? CloseCode.Normal : end.code, end.reason)
  }

  /**
   * close ends the session: it sends close with code and reason, normal
   * unless the client failed, such as with unsupported for a session it
   * cannot show, after the input sent so far, and closes the connection.
   * next() then delivers the end and nothing else. The promise resolves
   * once the connection closed, or after timeout milliseconds.
   */
  close(timeout = 5000, code: number = CloseCode.Normal, reason = ''): Promise<void> {
    this.discard = true
    this.dropQueue()
    this.terminate({ type: 'end', code, reason }, { code, reason })
    if (this.ownsDecoder) {
      this.decoder.close()
    }
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<void>((resolve) => {
      timer = setTimeout(resolve, timeout)
    })
    return Promise.race([this.closed, late]).finally(() => clearTimeout(timer))
  }

  private write(m: Message): void {
    if (this.stopped) {
      return
    }
    try {
      this.transport.send(encode(m))
    } catch (err) {
      this.fail(err)
    }
  }

  // send sends the message that build returns. An input takes the next
  // sequence number, wrapping from 2³²−1 to 1: 0 is never an input's
  // number, so last_seq 0 always means none.
  private send(build: (seq: number) => Message, input: boolean): number {
    if (this.stopped || this.end !== null) {
      throw new ClosedError()
    }
    if (input && this.role === Role.ReadOnly) {
      throw new ReadOnlyError()
    }
    const seq = input ? (this.seq === 0xffffffff ? 1 : this.seq + 1) : 0
    const msg = encode(build(seq))
    checkSize(msg.length, this.limits)
    if (input) {
      this.seq = seq
    }
    this.transport.send(msg)
    return seq
  }

  /**
   * key sends a key press (down) or release: keysym is the X11 keysym the
   * key produced, and code the physical key as a USB HID usage from the
   * keyboard page (keyboard.ts), or 0. It returns the input's sequence
   * number, which a frame's lastSeq reports once the server applied it.
   * A read-only viewer's input throws ReadOnlyError, and input after the
   * end ClosedError.
   */
  key(down: boolean, keysym: number, code: number): number {
    check('keysym', keysym, 0, 0xffffffff)
    check('code', code, 0, 0xffff)
    return this.send((seq) => ({ type: 'key', seq, down, keysym, code }), true)
  }

  /** text sends text that an input method composed. Ordinary typing goes as key. */
  text(text: string): number {
    return this.send((seq) => ({ type: 'text', seq, text }), true)
  }

  /** termInput sends the bytes the caller's terminal emulator produced, such as xterm.js's onData. */
  termInput(data: Uint8Array | string): number {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data
    return this.send((seq) => ({ type: 'term-input', seq, data: bytes }), true)
  }

  /** pointer sends the pointer's position on the remote screen, in pixels, and the buttons held (Buttons). */
  pointer(x: number, y: number, buttons: number): number {
    check('x', x, 0, 0xffff)
    check('y', y, 0, 0xffff)
    check('buttons', buttons, 0, 0xff)
    return this.send((seq) => ({ type: 'pointer', seq, x, y, buttons }), true)
  }

  /** wheel sends a scroll: positive dx scrolls right and positive dy down, in pixels or lines. */
  wheel(dx: number, dy: number, mode: number = WheelMode.Pixels): number {
    check('dx', dx, -0x80000000, 0x7fffffff)
    check('dy', dy, -0x80000000, 0x7fffffff)
    check('mode', mode, WheelMode.Pixels, WheelMode.Lines)
    return this.send((seq) => ({ type: 'wheel', seq, dx, dy, mode }), true)
  }

  /**
   * resize asks the remote for a screen size: pixels for a graphical
   * session, cells for a terminal. A resize event reports what it did.
   */
  resize(width: number, height: number): number {
    check('width', width, 0, 0xffff)
    check('height', height, 0, 0xffff)
    return this.send((seq) => ({ type: 'resize-request', seq, width, height }), true)
  }

  /** clipboard sends the caller's clipboard for the remote's. */
  clipboard(mime: string, data: Uint8Array | string): number {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data
    return this.send((seq) => ({ type: 'client-clipboard', seq, mime, data: bytes }), true)
  }

  /**
   * view tells the server whether the client can be seen, and which part
   * of the screen it shows, clamped to the protocol's coordinates; no
   * viewport, or an empty one, is the whole screen, and one that shows
   * nothing of a screen, such as one at negative coordinates only, is
   * refused. A hidden client gets no frames, and one frame of the current
   * screen when it shows again.
   */
  view(visible: boolean, viewport?: Rect): void {
    let x = 0
    let y = 0
    let w = 0
    let h = 0
    if (viewport !== undefined && viewport.width > 0 && viewport.height > 0) {
      const x0 = Math.max(Math.floor(viewport.x), 0)
      const y0 = Math.max(Math.floor(viewport.y), 0)
      const x1 = Math.min(Math.ceil(viewport.x + viewport.width), 0xffff)
      const y1 = Math.min(Math.ceil(viewport.y + viewport.height), 0xffff)
      if (x1 <= x0 || y1 <= y0) {
        // An empty viewport on the wire is the whole screen.
        throw new RangeError(`occamy: a viewport that shows nothing of a screen`)
      }
      ;[x, y, w, h] = [x0, y0, x1 - x0, y1 - y0]
    }
    this.send(() => ({ type: 'view', visible, x, y, w, h }), false)
  }

  /** promptReply answers the owner's prompt id, one answer per field. Answers may be secrets and are not logged. */
  promptReply(id: number, answers: string[]): void {
    if (this.role !== Role.Owner) {
      throw new NotOwnerError()
    }
    check('id', id, 0, 0xffffffff)
    this.send(() => ({ type: 'prompt-reply', id, answers }), false)
  }
}

function check(what: string, v: number, lo: number, hi: number): void {
  if (!Number.isInteger(v) || v < lo || v > hi) {
    throw new RangeError(`occamy: ${what} ${v} outside ${lo} to ${hi}`)
  }
}

function abortable<T>(p: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (signal === undefined) {
    return p
  }
  if (signal.aborted) {
    return Promise.reject(abortError(signal))
  }
  return new Promise<T>((resolve, reject) => {
    const onAbort = (): void => reject(abortError(signal))
    signal.addEventListener('abort', onAbort, { once: true })
    p.then(
      (v) => {
        signal.removeEventListener('abort', onAbort)
        resolve(v)
      },
      (err: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(asError(err))
      },
    )
  })
}

/**
 * newFramebuffer returns an opaque black screen of width × height. The
 * screen's content is undefined until its frame redraws it, which it
 * does; opaque black shows nothing of what is behind a canvas meanwhile.
 */
export function newFramebuffer(width: number, height: number): Framebuffer {
  const data = new Uint8ClampedArray(4 * width * height)
  new Uint32Array(data.buffer).fill(new Uint32Array(Uint8Array.of(0, 0, 0, 255).buffer)[0])
  return { width, height, data }
}

/** blit copies pixels, the rows of r, into fb. */
export function blit(fb: Framebuffer, r: Rect, pixels: Uint8Array | Uint8ClampedArray): void {
  const row = 4 * r.width
  for (let y = 0; y < r.height; y++) {
    const at = 4 * ((r.y + y) * fb.width + r.x)
    fb.data.set(pixels.subarray(y * row, (y + 1) * row), at)
  }
}

/**
 * copyRect copies the pixels at srcX, srcY to dst, as the screen is
 * before the copy, so an overlapping copy moves pixels as a scroll does:
 * rows go bottom up when the copy moves down, and copyWithin moves a row
 * that overlaps itself.
 */
export function copyRect(fb: Framebuffer, dst: Rect, srcX: number, srcY: number): void {
  const row = 4 * dst.width
  const stride = 4 * fb.width
  const down = dst.y > srcY
  for (let i = 0; i < dst.height; i++) {
    const y = down ? dst.height - 1 - i : i
    const from = (srcY + y) * stride + 4 * srcX
    fb.data.copyWithin((dst.y + y) * stride + 4 * dst.x, from, from + row)
  }
}
