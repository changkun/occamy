// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// A Worker that decodes tiles and cursors for a WorkerDecoder, with what
// the Worker's platform offers: DecompressionStream, createImageBitmap
// and OffscreenCanvas.

import { LocalDecoder, type Port, serveDecoder } from './decode'

serveDecoder(self as unknown as Port, new LocalDecoder())
