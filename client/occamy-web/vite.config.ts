// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import { defineConfig } from 'vite'

// The dev server proxies API v1 and its streams to an occamyd serving a
// v1 file: the demo stack's occamy-v1 unless OCCAMY_DEV_SERVER names
// another. The Host header passes unchanged, so that occamyd takes the
// page's Origin for its own.
const server = process.env.OCCAMY_DEV_SERVER ?? 'http://localhost:5637'

export default defineConfig({
  // occamyd serves the built client under /static/ (web_client), so the
  // page addresses its scripts, styles and Worker relative to itself.
  base: './',
  oxc: {
    jsx: { runtime: 'automatic' },
  },
  build: {
    // The licences of the packages bundled in, beside the page, which
    // the image and the release archive carry.
    license: { fileName: 'THIRD_PARTY_LICENSES.md' },
  },
  server: {
    proxy: {
      '/api': { target: server, ws: true },
    },
  },
})
