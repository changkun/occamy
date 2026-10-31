// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Tile and cursor decoding. A TileDecoder turns a payload into RGBA
// pixels; LocalDecoder decodes on the calling thread with what the
// platform offers, and WorkerDecoder hands the same work to Workers
// (decode.worker.ts) over the same interface, so the session does not
// know which it has and the tests run LocalDecoder in Node.

import {
  type Inflate,
  type Pixels,
  checkImageSize,
  decodeDeflate,
  decodeRaw,
  imageTypes,
  isImageCodec,
} from './codecs'
import { Codec } from './messages'
import { type ErrorKind, ProtocolError } from './wire'

/** A payload to decode: an image of width × height pixels. */
export interface DecodeJob {
  codec: number
  payload: Uint8Array
  width: number
  height: number
}

/** TileDecoder decodes the payloads of tiles and cursors into RGBA pixels with straight alpha. */
export interface TileDecoder {
  /** codecs returns the codecs it decodes, in the order a client offers them: raw last. */
  codecs(): Promise<number[]>
  /**
   * decode decodes a payload of width × height pixels. It rejects with a
   * ProtocolError if the payload is not such an image, which ends the
   * session with protocol-error. It checks the size an image's header
   * claims before decoding, so a few bytes cannot make it allocate more
   * than the rectangle.
   */
  decode(job: DecodeJob): Promise<Pixels>
  /** close releases the decoder; pending decodes reject. */
  close(): void
}

/** decodeImage decodes an image payload whose header was checked to claim width × height. */
export type DecodeImage = (codec: number, payload: Uint8Array, width: number, height: number) => Promise<Pixels>

/** What a platform offers to decode with; null where it offers nothing. */
export interface Platform {
  inflate: Inflate | null
  decodeImage: DecodeImage | null
}

/**
 * INFLATE_SLICE is the most payload bytes an inflater is given at a
 * time. DEFLATE inflates a byte to at most 1032, so a slice inflates to
 * at most about 1 MiB, whatever the payload.
 */
export const INFLATE_SLICE = 1024

/**
 * streamInflate inflates with DecompressionStream('deflate-raw'), the
 * raw DEFLATE that the deflate codec carries, or returns null where the
 * platform lacks it.
 */
export function streamInflate(): Inflate | null {
  if (typeof DecompressionStream !== 'function') {
    return null
  }
  try {
    new DecompressionStream('deflate-raw')
  } catch {
    return null
  }
  return slicedInflate(() => new DecompressionStream('deflate-raw'))
}

/**
 * slicedInflate inflates through the transform that make returns, such
 * as a DecompressionStream, feeding it the payload INFLATE_SLICE bytes
 * at a time as it asks for them, and cancelling it when the consumer
 * stops. A transform need not wait for its output to be read: Chrome's
 * DecompressionStream inflates a whole written chunk into its queue at
 * once. Fed a slice at a time, it inflates at most a slice beyond what
 * was read, about 1 MiB, rather than the whole payload, which may
 * inflate to 1032 times max_message_size.
 *
 * The transform refuses trailing bytes and a stream without its final
 * block, which it reports when the input ends, so a caller reads it to
 * its end before taking what it yielded.
 */
export function slicedInflate(make: () => ReadableWritablePair<Uint8Array, BufferSource>): Inflate {
  return async function* inflate(payload: Uint8Array): AsyncGenerator<Uint8Array> {
    const ds = make()
    let at = 0
    const source = new ReadableStream<BufferSource>(
      {
        pull(c) {
          if (at >= payload.length) {
            c.close()
            return
          }
          const end = Math.min(at + INFLATE_SLICE, payload.length)
          c.enqueue(payload.slice(at, end))
          at = end
        },
      },
      // Nothing is read ahead of what the transform asks for.
      { highWaterMark: 0 },
    )
    // The readable side reports the stream's errors; the pipe would only
    // repeat them, or report the cancel below.
    source.pipeTo(ds.writable).catch(() => undefined)
    const reader = ds.readable.getReader()
    let done = false
    try {
      for (;;) {
        const r = await reader.read()
        if (r.done) {
          done = true
          return
        }
        yield r.value
      }
    } finally {
      if (!done) {
        // The consumer stopped early: cancelling the readable side
        // errors the writable side, which ends the pipe and cancels the
        // source, so nothing more is fed.
        await reader.cancel().catch(() => undefined)
      }
    }
  }
}

// A 1×1 lossy WebP, to find out whether the platform decodes WebP.
const webpProbe = 'UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA'

type Context2D = OffscreenCanvasRenderingContext2D | CanvasRenderingContext2D

function scratchContext(): Context2D | null {
  const options: CanvasRenderingContext2DSettings = { willReadFrequently: true }
  if (typeof OffscreenCanvas === 'function') {
    return new OffscreenCanvas(1, 1).getContext('2d', options)
  }
  if (typeof document !== 'undefined') {
    return document.createElement('canvas').getContext('2d', options)
  }
  return null
}

/**
 * canvasDecodeImage decodes images with createImageBitmap and reads the
 * pixels back through a 2D canvas, or returns null where the platform
 * lacks either.
 *
 * The browser's decoders convert to RGB themselves: libwebp turns VP8's
 * BT.601 limited-range YCbCr into RGB as the encoder meant, and the
 * JPEG decoder JFIF's full-range YCbCr. This code never sees YCbCr, so
 * it cannot fall into the trap that Go's standard library sets, whose
 * conversion of an image.YCbCr is JPEG's full-range one and reads lossy
 * WebP about 10 dB short (docs/protocol.md, Codecs).
 * colorSpaceConversion 'none' keeps the decoded values as they are,
 * without a colour profile's correction, as libwebp and libjpeg give
 * them.
 *
 * A 2D canvas keeps premultiplied pixels, so a translucent pixel reads
 * back within rounding of its colour; opaque ones, which every tile is,
 * read back exactly. Cursors with alpha go as deflate or raw to a
 * client that lists either, as this one always does.
 */
export function canvasDecodeImage(): DecodeImage | null {
  if (typeof createImageBitmap !== 'function' || typeof Blob !== 'function') {
    return null
  }
  const ctx = scratchContext()
  if (ctx === null) {
    return null
  }
  return async (codec, payload, width, height) => {
    const blob = new Blob([payload as Uint8Array<ArrayBuffer>], { type: imageTypes[codec as Codec] })
    let bitmap: ImageBitmap
    try {
      bitmap = await createImageBitmap(blob, { premultiplyAlpha: 'none', colorSpaceConversion: 'none' })
    } catch (err) {
      throw new ProtocolError('invalid', `an image that does not decode: ${String(err)}`)
    }
    try {
      if (bitmap.width !== width || bitmap.height !== height) {
        throw new ProtocolError('invalid', `an image of ${bitmap.width}x${bitmap.height} for ${width}x${height}`)
      }
      // Drawing and reading back are synchronous, so decodes that wait
      // on createImageBitmap together share the canvas safely.
      const canvas = ctx.canvas
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width
        canvas.height = height
      }
      ctx.globalCompositeOperation = 'copy'
      ctx.drawImage(bitmap, 0, 0)
      return ctx.getImageData(0, 0, width, height).data
    } finally {
      bitmap.close()
    }
  }
}

/** browserPlatform is what the browser, or the Worker, offers. */
export function browserPlatform(): Platform {
  return { inflate: streamInflate(), decodeImage: canvasDecodeImage() }
}

/** LocalDecoder decodes on the calling thread. */
export class LocalDecoder implements TileDecoder {
  private readonly platform: Platform
  private webp: Promise<boolean> | null = null

  constructor(platform: Platform = browserPlatform()) {
    this.platform = platform
  }

  async codecs(): Promise<number[]> {
    const out: number[] = []
    if (this.platform.inflate !== null) {
      out.push(Codec.Deflate)
    }
    if (this.platform.decodeImage !== null) {
      if (await this.decodesWebP()) {
        out.push(Codec.WebP)
      }
      out.push(Codec.JPEG)
    }
    out.push(Codec.Raw)
    return out
  }

  private decodesWebP(): Promise<boolean> {
    const decodeImage = this.platform.decodeImage
    if (decodeImage === null) {
      return Promise.resolve(false)
    }
    this.webp ??= (async () => {
      try {
        const p = Uint8Array.from(atob(webpProbe), (c) => c.charCodeAt(0))
        await decodeImage(Codec.WebP, p, 1, 1)
        return true
      } catch {
        return false
      }
    })()
    return this.webp
  }

  async decode(job: DecodeJob): Promise<Pixels> {
    const { codec, payload, width, height } = job
    if (codec === Codec.Raw) {
      return decodeRaw(payload, width, height)
    }
    if (codec === Codec.Deflate && this.platform.inflate !== null) {
      return decodeDeflate(payload, width, height, this.platform.inflate)
    }
    if (isImageCodec(codec) && this.platform.decodeImage !== null) {
      checkImageSize(codec, payload, width, height)
      const pixels = await this.platform.decodeImage(codec, payload, width, height)
      if (pixels.length !== 4 * width * height) {
        throw new ProtocolError('invalid', `an image of ${pixels.length} bytes for ${width}x${height}`)
      }
      return pixels
    }
    throw new ProtocolError('invalid', `codec ${codec}, which this client does not decode`)
  }

  close(): void {}
}

/**
 * Port is the end of a channel to or from a Worker: a Worker, the
 * Worker's own global scope, or a MessagePort.
 */
export interface Port {
  postMessage(message: unknown, transfer: Transferable[]): void
  onmessage: ((ev: MessageEvent) => unknown) | null
}

type Request =
  | { id: number; op: 'codecs' }
  | { id: number; op: 'decode'; codec: number; payload: Uint8Array; width: number; height: number }

type Response =
  | { id: number; codecs: number[] }
  | { id: number; pixels: Pixels }
  | { id: number; error: { kind?: ErrorKind; message: string } }

/** serveDecoder answers a WorkerDecoder's requests on port with decoder. */
export function serveDecoder(port: Port, decoder: TileDecoder): void {
  port.onmessage = (ev: MessageEvent) => {
    const req = ev.data as Request
    const fail = (err: unknown): void => {
      const error = err instanceof ProtocolError ? { kind: err.kind, message: err.message } : { message: String(err) }
      port.postMessage({ id: req.id, error } satisfies Response, [])
    }
    if (req.op === 'codecs') {
      decoder.codecs().then((codecs) => port.postMessage({ id: req.id, codecs } satisfies Response, []), fail)
      return
    }
    decoder.decode(req).then(
      (pixels) => port.postMessage({ id: req.id, pixels } satisfies Response, [pixels.buffer]),
      fail,
    )
  }
}

interface Pending {
  resolve: (v: Response) => void
  reject: (err: Error) => void
  worker: number
}

/** WorkerDecoder decodes in Workers, each running serveDecoder with a LocalDecoder. */
export class WorkerDecoder implements TileDecoder {
  private readonly ports: Port[] = []
  private readonly load: number[] = []
  private readonly pending = new Map<number, Pending>()
  private readonly terminate: (() => void)[] = []
  private nextID = 1
  private closed = false

  /**
   * spawn starts a Worker, or returns another port that serveDecoder
   * answers, and count is the number of them, one by default.
   */
  constructor(spawn: () => Port & { terminate?: () => void; onerror?: ((ev: ErrorEvent) => unknown) | null }, count = 1) {
    for (let i = 0; i < Math.max(1, count); i++) {
      const port = spawn()
      port.onmessage = (ev: MessageEvent) => this.answer(ev.data as Response)
      if ('onerror' in port) {
        port.onerror = (ev: ErrorEvent) => this.failWorker(i, new Error(`decoder worker failed: ${ev.message}`))
      }
      this.ports.push(port)
      this.load.push(0)
      if (port.terminate !== undefined) {
        this.terminate.push(port.terminate.bind(port))
      }
    }
  }

  private request(req: Request, transfer: Transferable[]): Promise<Response> {
    if (this.closed) {
      return Promise.reject(new Error('decoder closed'))
    }
    // The worker with the fewest decodes in hand.
    let w = 0
    for (let i = 1; i < this.load.length; i++) {
      if (this.load[i] < this.load[w]) {
        w = i
      }
    }
    this.load[w]++
    return new Promise<Response>((resolve, reject) => {
      this.pending.set(req.id, { resolve, reject, worker: w })
      this.ports[w].postMessage(req, transfer)
    })
  }

  private answer(res: Response): void {
    const p = this.pending.get(res.id)
    if (p === undefined) {
      return
    }
    this.pending.delete(res.id)
    this.load[p.worker]--
    p.resolve(res)
  }

  private failWorker(worker: number, err: Error): void {
    for (const [id, p] of this.pending) {
      if (p.worker === worker) {
        this.pending.delete(id)
        this.load[worker]--
        p.reject(err)
      }
    }
  }

  async codecs(): Promise<number[]> {
    const res = await this.request({ id: this.nextID++, op: 'codecs' }, [])
    if ('error' in res) {
      throw new Error(res.error.message)
    }
    return 'codecs' in res ? res.codecs : []
  }

  async decode(job: DecodeJob): Promise<Pixels> {
    // The payload is a view into its message; the worker gets a copy of
    // its bytes only.
    const payload = job.payload.slice()
    const res = await this.request(
      { id: this.nextID++, op: 'decode', codec: job.codec, payload, width: job.width, height: job.height },
      [payload.buffer],
    )
    if ('error' in res) {
      const { kind, message } = res.error
      throw kind === undefined ? new Error(message) : Object.assign(new ProtocolError(kind), { message })
    }
    if (!('pixels' in res)) {
      throw new Error('decoder worker answered without pixels')
    }
    return res.pixels
  }

  close(): void {
    if (this.closed) {
      return
    }
    this.closed = true
    for (const t of this.terminate) {
      t()
    }
    for (const p of this.pending.values()) {
      p.reject(new Error('decoder closed'))
    }
    this.pending.clear()
  }
}
