---
title: React web client
status: vague
track: client
depends_on: []
affects:
  - client/occamy-web/
  - docker/Dockerfile
  - .github/workflows/dockerimage.yml
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# React web client

## Overview

The demo web client was moved to Vue 3 during the revival. Rebuilding it with
React is under consideration. This spec captures the recommended shape so that
the decision does not block on [008](008-native-architecture.md).

## Current State

- `client/occamy-web/`: Vue 3, Vite, vue-router, element-plus. Two views:
  `Login.vue` (protocol, host, username and password form posting to
  `/api/v1/login`) and `Desktop.vue` (wires `occamy.Client`, `Display`,
  `Mouse` and `Keyboard` to a WebSocket at `/api/v1/connect`).
- `src/components/occamy.js` is a 9k-line fork of guacamole-common-js and
  holds all protocol and rendering logic; the Vue code around it is under
  200 lines.

## Architecture

Recommended in two steps:

1. **React shell.** Vite with React and TypeScript. A login view and a
   desktop view that wrap the existing `occamy.js` objects, at parity with
   the Vue client: protocol selection, display scaling, mouse and keyboard.
   Small, and independent of protocol work.
2. **Protocol client swap.** When [008](008-native-architecture.md) phase 3
   lands, replace `occamy.js` with an Occamy protocol client: tile decoding
   in a Worker (OffscreenCanvas, WebCodecs where needed) and a terminal view
   (for example xterm.js) for terminal backends.

Open questions:

- React or stay on Vue 3? React has the larger ecosystem, including terminal
  widgets; the current Vue code is small, so either choice is cheap now.
- UI component library, or plain components? The client has one form.

## Testing Strategy

- Component tests for the login form and token handling.
- Browser end-to-end test (Playwright or similar) against the
  [006](006-demo-stack-refresh.md) stack: log in, see a non-blank canvas,
  send a key.

## Acceptance Criteria

- [ ] Decision recorded (React or Vue).
- [ ] If React: shell at parity with the Vue client, with lint and build in
      CI.
