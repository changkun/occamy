// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The default decoder: Workers where the platform has them, so that
// decoding stays off the thread that draws and takes input.

import { LocalDecoder, type TileDecoder, WorkerDecoder } from './decode'

/** workerDecoder decodes in count module Workers running decode.worker.ts. */
export function workerDecoder(count = defaultWorkers()): WorkerDecoder {
  // Vite bundles the Worker from this exact form.
  return new WorkerDecoder(() => new Worker(new URL('./decode.worker.ts', import.meta.url), { type: 'module' }), count)
}

/** defaultWorkers is a Worker for every two cores beyond the first, from one to four. */
export function defaultWorkers(): number {
  const cores = typeof navigator !== 'undefined' ? navigator.hardwareConcurrency : 1
  return Math.min(4, Math.max(1, Math.floor((cores - 1) / 2)))
}

/** defaultDecoder decodes in Workers where the platform has them, and on the calling thread otherwise. */
export function defaultDecoder(): TileDecoder {
  if (typeof Worker === 'function') {
    return workerDecoder()
  }
  return new LocalDecoder()
}
