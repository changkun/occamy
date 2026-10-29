---
title: Native Gio client
status: archived
track: client
depends_on:
  - specs/001-status-quo.md
  - specs/008-native-architecture.md
affects:
  - cmd/
  - internal/
  - go.mod
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Native Gio client

## Overview

A native desktop client written in Go with [Gio](https://gioui.org) was
started in December 2021 and never merged. Before more work goes in, decide
whether a native client is still a goal, and if so how to bring the branch
onto the revived baseline without loading GUI dependencies onto the server.

## Current State

- PR #51 "Implement Gio client" (open since 2021-12-16) from branch
  `gio-client`: commit `a5c22a2` moves the server to `cmd/occamyd` and
  renames `internal/lib` to `internal/guacd`; commit `1d9a31d` adds
  `cmd/occamy-gui` and `internal/guac` (a Go Guacamole protocol client:
  client, display, layer, handlers, session, stream) and removes
  `internal/protocol/parser.go` and its benchmark.
- Branch `gio` carries the same two commits on an older base.
- The branches depend on `gioui.org v0.0.0-20211202105001-872b4ba41be0` and
  `github.com/tfriedel6/canvas v0.9.1`. The revival removed both from master,
  and Gio's API has changed substantially since 2021.

## Architecture

Questions to answer:

- Is a native client wanted, or is the web client enough?
- Layout: one module with `cmd/occamyd` and `cmd/occamy-gui`, or a separate
  module (`client/occamy-gio`) so the server's `go.mod` never pulls in Gio?
- Does `internal/guac` become a reusable, public Go client package
  (`pkg/guac` or similar) that tests and the pressure test can use too?
- It should speak the Occamy protocol from [008](008-native-architecture.md)
  through a shared Go client library, rather than Guacamole's.

If the answer is no: close PR #51 and delete the `gio` and `gio-client`
branches.

## Acceptance Criteria

- [ ] Decision recorded here (continue or retire).
- [ ] If continuing: refined to `drafted` with the layout chosen and the
      branch rebased onto master with current Gio.

## Outcome

Retired on 2026-10-29. The native client will be SwiftUI on top of the Go
client core rather than Gio. See [010](010-native-swift-client.md). PR #51
and branches `gio` and `gio-client` stay as they are until the maintainer
closes them. `internal/guac` on `gio-client` is a reuse candidate for the
guacd bridge ([guacd-bridge](008-native-architecture/guacd-bridge.md)).

