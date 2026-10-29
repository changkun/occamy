---
title: Remove libguac and cgo
status: validated
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-sessions.md
affects:
  - guacamole/
  - internal/lib/
  - internal/protocol/io.go
  - docker/Dockerfile
  - .dockerignore
  - .github/workflows/dockerimage.yml
  - Makefile
  - README.md
  - docs/architecture.md
  - specs/001-status-quo.md
  - specs/002-ssh-session-rejoin.md
  - specs/003-session-lifecycle-hardening.md
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Remove libguac and cgo

## Goal

Delete the forked guacamole-server and the cgo bindings now that nothing
imports them. `occamyd` then builds with `CGO_ENABLED=0` on every platform
Go supports (#25), and the image carries no C libraries.

## What to do

1. Delete `guacamole/` and `internal/lib/`. Delete `protocol.IO` from
   `internal/protocol/io.go` (fd wrapper, no users left) and the file if
   empty. `internal/uuid` is used only by `internal/lib`. Delete it unless
   [guacd-sessions](guacd-sessions.md) started using it.
2. `docker/Dockerfile`: drop the apt block and `build-libguac.sh`. Build
   with `CGO_ENABLED=0 go build -trimpath -o occamyd .`. Base the runtime
   stage on a static image (`gcr.io/distroless/static-debian13`, or
   `debian:trixie-slim` if a shell is wanted for debugging) without the
   C runtime libraries or `LD_LIBRARY_PATH`. Keep the web client stage.
3. `.dockerignore`: drop entries that only existed for `guacamole/`.
4. CI (`.github/workflows/dockerimage.yml`), job `go`: remove
   `LD_LIBRARY_PATH`, the apt step and the libguac build. Run
   `CGO_ENABLED=0 go build ./...`, `go vet ./...` and `go test ./...`
   (the `tests/` package skips without `OCCAMY_E2E_ENDPOINT`, per 006).
   Add a build-only matrix on `macos-latest` and `windows-latest`.
5. `Makefile`: `compile` sets `CGO_ENABLED=0`.
6. Docs: README status table (VNC and SSH through guacd; the RDP row
   was already set by [rdp-xrdp](rdp-xrdp.md), which ran first). "Build from source" becomes `go build` plus a guacd to point
   at. Rewrite the "Why Occamy" pitch: it says Occamy
   needs no guacd, which is false for now, so link
   [008](../008-native-architecture.md) for the path back to a single
   binary. Update the `docs/architecture.md` components table and
   connection flow.
7. Specs: set [002](../002-ssh-session-rejoin.md) to `archived` with a
   one-line outcome (moot: libguac no longer runs in-process). In
   [001](../001-status-quo.md), mark the libguac-specific known issues
   resolved by 005. Add a note at the top of
   [003](../003-session-lifecycle-hardening.md) saying its libguac defects
   are gone and the rest should be refined against the new session table.

## Tests

- `CGO_ENABLED=0 go build ./...` and `go test ./...` pass on a machine
  without any C toolchain.
- `make build` produces a smaller image; record the size before and after
  in the commit message.
- The 006 end-to-end suite passes against the new image.

## Boundaries

- No behaviour change in `server/`.
- Do not touch the web client or the API.
