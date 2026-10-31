// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The web client of protocol v1: a React shell on the client core in
// src/protocol, speaking API v1 to the occamyd that serves it.

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { Api } from './lib/api'
import { readFragment } from './lib/fragment'
import { start } from './lib/targets'
import { App } from './views/App'
import './styles.css'

// The fragment is read once, before the first render, and removed from
// the address bar and the history entry: it may carry an OIDC one-time
// code or a share, neither of which should outlive this load.
const fragment = readFragment(location.hash)
if (fragment.kind !== 'none') {
  history.replaceState(null, '', location.pathname + location.search)
}

let storage: Storage | undefined
try {
  storage = sessionStorage
} catch {
  storage = undefined
}

const api = new Api()
const begin = start(fragment, api, storage)
const root = document.getElementById('root')
if (root === null) {
  throw new Error('occamy: the page has no #root')
}
createRoot(root).render(
  <StrictMode>
    <App api={api} start={begin} storage={storage} />
  </StrictMode>,
)
