// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The demo page: the client core in a browser, against a protocol v1
// server such as client/internal/webinterop, and, with ?check, the
// checks that need a browser, which Node lacks: lossy decoding, Workers,
// the canvas. It is a development page, served by Vite with
// demo/vite.config.ts and in no build of the app.
//
// The checks resolve window.occamyChecks with their results, which
// specs/009-react-web-client.md's by-hand verification reads.

import {
  CanvasSurface,
  Codec,
  Display,
  LocalDecoder,
  ProtocolError,
  type CursorEvent,
  type Session,
  bindKeyboard,
  bindPointer,
  bindVisibility,
  canvasDecodeImage,
  connect,
  cursorStyle,
  streamInflate,
  workerDecoder,
} from '../index'
import { blit, newFramebuffer } from '../session'
import { codecs, fromHex, psnr } from '../testing/vectors'

interface Check {
  name: string
  ok: boolean
  detail: string
}

declare global {
  interface Window {
    occamyChecks?: Promise<Check[]>
    occamyFocus?: () => string
  }
}

const params = new URLSearchParams(location.search)
const server = params.get('server')
const route = params.get('route') ?? '/graphical'
const token = params.get('token') ?? 'interop'
const canvas = document.getElementById('screen') as HTMLCanvasElement
const logEl = document.getElementById('log') as HTMLPreElement

function log(line: string): void {
  logEl.textContent += `${line}\n`
}

function maxDiff(a: ArrayLike<number>, b: ArrayLike<number> | undefined): number {
  if (b === undefined || a.length !== b.length) {
    return Infinity
  }
  let d = 0
  for (let i = 0; i < a.length; i++) {
    d = Math.max(d, Math.abs(a[i] - b[i]))
  }
  return d
}

// alphaDiff is maxDiff over the opaque and the translucent pixels of want.
function alphaDiff(got: ArrayLike<number>, want: ArrayLike<number>): [number, number] {
  if (got.length !== want.length) {
    return [Infinity, Infinity]
  }
  let opaque = 0
  let translucent = 0
  for (let i = 0; i < want.length; i += 4) {
    for (let c = 0; c < 4; c++) {
      const d = Math.abs(got[i + c] - want[i + c])
      if (want[i + 3] === 255) {
        opaque = Math.max(opaque, d)
      } else {
        translucent = Math.max(translucent, d)
      }
    }
  }
  return [opaque, translucent]
}

async function checks(): Promise<Check[]> {
  const results: Check[] = []
  const record = (name: string, ok: boolean, detail = ''): void => {
    results.push({ name, ok, detail })
    log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail === '' ? '' : `: ${detail}`}`)
  }

  record('DecompressionStream inflates raw DEFLATE', streamInflate() !== null)
  record('createImageBitmap and a 2D canvas decode images', canvasDecodeImage() !== null)
  const local = new LocalDecoder()
  const offered = await local.codecs()
  record('the codecs offered', JSON.stringify(offered) === JSON.stringify([Codec.Deflate, Codec.WebP, Codec.JPEG, Codec.Raw]), JSON.stringify(offered))

  const decoded = new Map<string, Uint8ClampedArray>()
  for (const v of codecs.vectors) {
    try {
      const got = await local.decode({ codec: v.codec, payload: fromHex(v.payload), width: v.width, height: v.height })
      decoded.set(v.name, got)
      const want = fromHex(v.pixels)
      if (v.exact) {
        // A 2D canvas keeps premultiplied pixels, so a translucent pixel
        // of an image codec reads back within rounding (decode.ts).
        const [opaque, translucent] = alphaDiff(got, want)
        const image = v.codec === Codec.WebP || v.codec === Codec.JPEG
        record(
          `vector ${v.name}`,
          opaque === 0 && (translucent === 0 || (image && translucent <= 1)),
          translucent === 0 && opaque === 0 ? 'exact' : `opaque pixels differ by up to ${opaque}, translucent ones by up to ${translucent}`,
        )
      } else {
        const p = psnr(want, got)
        record(`vector ${v.name}`, p >= (v.min_psnr ?? 0), `PSNR ${p.toFixed(1)} dB, ${v.min_psnr} required`)
      }
    } catch (err) {
      record(`vector ${v.name}`, false, String(err))
    }
  }
  {
    // An EXIF orientation of 6, rotate 90°, in an APP1 segment after
    // SOI: the image decodes as encoded, not turned into 16×24.
    const v = codecs.vectors.find((x) => x.name === 'jpeg: 24×16')
    if (v !== undefined) {
      const jpeg = fromHex(v.payload)
      const exif = Uint8Array.of(
        0xff, 0xe1, 0x00, 0x22, 0x45, 0x78, 0x69, 0x66, 0x00, 0x00,
        0x4d, 0x4d, 0x00, 0x2a, 0x00, 0x00, 0x00, 0x08,
        0x00, 0x01, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x06, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00,
      )
      const rotated = new Uint8Array(jpeg.length + exif.length)
      rotated.set(jpeg.subarray(0, 2))
      rotated.set(exif, 2)
      rotated.set(jpeg.subarray(2), 2 + exif.length)
      try {
        const got = await local.decode({ codec: v.codec, payload: rotated, width: v.width, height: v.height })
        const diff = maxDiff(got, decoded.get(v.name))
        record('a JPEG with an EXIF orientation decodes as encoded', diff === 0, `differs by up to ${diff}`)
      } catch (err) {
        record('a JPEG with an EXIF orientation decodes as encoded', false, String(err))
      }
    }
  }
  for (const v of codecs.invalid) {
    const err: unknown = await local.decode({ codec: v.codec, payload: fromHex(v.payload), width: v.width, height: v.height }).then(
      () => null,
      (e: unknown) => e,
    )
    record(`invalid ${v.name}`, err instanceof ProtocolError && err.kind === 'invalid', String(err))
  }

  const workers = workerDecoder(2)
  try {
    const got = await workers.codecs()
    record('Workers offer the same codecs', JSON.stringify(got) === JSON.stringify(offered), JSON.stringify(got))
    let same = 0
    for (const v of codecs.vectors) {
      const p = await workers.decode({ codec: v.codec, payload: fromHex(v.payload), width: v.width, height: v.height })
      if (maxDiff(p, decoded.get(v.name)) === 0) {
        same++
      }
    }
    record('Workers decode as the page does', same === codecs.vectors.length, `${same} of ${codecs.vectors.length}`)
  } catch (err) {
    record('Workers decode', false, String(err))
  } finally {
    workers.close()
  }

  {
    const c = document.createElement('canvas')
    const surface = new CanvasSurface(c)
    const fb = newFramebuffer(7, 5)
    for (let i = 0; i < fb.data.length; i += 4) {
      fb.data.set([(i * 7) & 0xff, (i * 13) & 0xff, (i * 29) & 0xff, 255], i)
    }
    surface.present(fb, [{ x: 0, y: 0, width: 7, height: 5 }])
    const ctx = c.getContext('2d') as CanvasRenderingContext2D
    const whole = maxDiff(ctx.getImageData(0, 0, 7, 5).data, fb.data)
    const r = { x: 2, y: 1, width: 3, height: 2 }
    blit(fb, r, new Uint8Array(4 * 6).fill(200))
    surface.present(fb, [r])
    const part = maxDiff(ctx.getImageData(0, 0, 7, 5).data, fb.data)
    record('CanvasSurface presents frames exactly', c.width === 7 && whole === 0 && part === 0, `whole ${whole}, a rectangle ${part}`)
  }

  {
    const pixels = new Uint8ClampedArray(4 * 16 * 16)
    for (let i = 0; i < pixels.length; i += 4) {
      pixels.set([255, 0, 0, i % 3 === 0 ? 255 : 128], i)
    }
    const style = cursorStyle({ width: 16, height: 16, pixels }, 3, 4)
    record('cursorStyle draws a cursor image', style.startsWith('url(data:image/png;base64,') && style.endsWith(') 3 4, default'), `${style.slice(0, 32)}…`)
    record('cursorStyle hides an empty cursor', cursorStyle(null, 0, 0) === 'none')
  }

  if (server !== null) {
    try {
      await checkLive(server, record)
    } catch (err) {
      record('a session with the server', false, String(err))
    }
  }
  return results
}

// checkLive runs a session with the server's scripted desktop, through
// the Workers and the canvas, and compares what it shows with the
// desktop's screen.
async function checkLive(base: string, record: (name: string, ok: boolean, detail?: string) => void): Promise<void> {
  const s = await connect(base.replace(/^http/, 'ws') + route, token, { width: 320, height: 200 })
  try {
    record('a session with the server: codecs agreed', s.capabilities.codecs.includes(Codec.WebP), JSON.stringify(s.capabilities.codecs))
    const scratch = document.createElement('canvas')
    const surface = new CanvasSurface(scratch)
    const seen: { cursor: CursorEvent | null } = { cursor: null }
    // Frames until the screen and the cursor are shown, or the server
    // has sent nothing for a second.
    const shows = async (keysym: number): Promise<number> => {
      for (;;) {
        const screen = (await (await fetch(`${base}/screen`)).json()) as { width: number; height: number; pixels: string }
        const want = Uint8Array.from(atob(screen.pixels), (ch) => ch.charCodeAt(0))
        const fb = s.framebuffer
        const p = fb.width === screen.width && fb.height === screen.height ? psnr(want, fb.data) : 0
        const swatch = Array.from(fb.data.subarray(0, 3))
        if (p >= 30 && seen.cursor !== null && Math.abs(swatch[0] - (keysym & 0xff)) <= 4 && Math.abs(swatch[2] - 0x40) <= 4) {
          return p
        }
        const ev = await s.next(AbortSignal.timeout(1000)).catch(() => null)
        if (ev === null) {
          return p
        }
        if (ev.type === 'frame') {
          surface.present(s.framebuffer, ev.dirty)
        } else if (ev.type === 'cursor') {
          seen.cursor = ev
        }
      }
    }
    let p = await shows(0)
    const fb = s.framebuffer
    record('a session with the server: the screen', p >= 30, `PSNR ${p.toFixed(1)} dB over ${fb.width}x${fb.height}`)
    const shown = (scratch.getContext('2d') as CanvasRenderingContext2D).getImageData(0, 0, fb.width, fb.height).data
    record('a session with the server: the canvas shows the framebuffer', maxDiff(shown, fb.data) === 0)
    s.key(true, 0x71, 0x14)
    s.key(false, 0x71, 0x14)
    p = await shows(0x71)
    record('a session with the server: a key paints the swatch', p >= 30 && s.framebuffer.data[0] > 0x60, `PSNR ${p.toFixed(1)} dB, swatch ${Array.from(s.framebuffer.data.subarray(0, 4)).join(',')}`)
    const c = (await (await fetch(`${base}/cursor`)).json()) as { pixels: string }
    const cw = Uint8Array.from(atob(c.pixels), (ch) => ch.charCodeAt(0))
    const got = seen.cursor?.image?.pixels
    record('a session with the server: the cursor', maxDiff(cw, got) === 0, `differs by up to ${maxDiff(cw, got)}`)
  } finally {
    await s.close()
  }
}

// run shows a session on the page's canvas, with its input.
async function run(base: string): Promise<void> {
  const session: Session = await connect(base.replace(/^http/, 'ws') + route, token, {
    width: 640,
    height: 400,
    pixelRatio: Math.round(devicePixelRatio * 100),
  })
  log(`connected: viewer ${session.viewerId}, codecs ${JSON.stringify(session.capabilities.codecs)}`)
  const unbind = [
    bindKeyboard(canvas, session),
    bindPointer(canvas, session, () => session.size),
    bindVisibility(document, session),
  ]
  const display = new Display(session, new CanvasSurface(canvas), {
    onEvent: (ev) => {
      if (ev.type === 'cursor') {
        canvas.style.cursor = cursorStyle(ev.image, ev.hotX, ev.hotY)
      } else if (ev.type !== 'frame') {
        log(`${ev.type} ${JSON.stringify(ev, (_k, v: unknown) => (v instanceof Uint8Array ? `${v.length} bytes` : v))}`)
      }
    },
  })
  const end = await display.run()
  unbind.forEach((u) => u())
  log(`ended: ${end.code} ${end.reason}`)
}

window.occamyFocus = () => document.activeElement?.id ?? ''

async function main(): Promise<void> {
  if (params.has('check')) {
    window.occamyChecks = checks()
    await window.occamyChecks
  }
  if (server !== null) {
    await run(server)
  }
}

main().catch((err: unknown) => log(`error: ${String(err)}`))
