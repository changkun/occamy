// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Serves the protocol demo page, apart from the app:
//
//	npx vite --config src/protocol/demo/vite.config.ts
//
// and builds it, which bundles the decoder Worker as an app would:
//
//	npx vite build --config src/protocol/demo/vite.config.ts --outDir <dir>

import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vite'

const root = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  root,
  base: './',
  server: {
    // The checks read the golden vectors of protocol/testdata.
    fs: { allow: [fileURLToPath(new URL('../../../../../protocol/testdata', import.meta.url)), fileURLToPath(new URL('../..', import.meta.url))] },
  },
  build: { emptyOutDir: true },
})
