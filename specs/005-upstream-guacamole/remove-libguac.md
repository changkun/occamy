---
title: Remove libguac and cgo
status: complete
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
   stage on `gcr.io/distroless/static-debian13:nonroot`, without the C
   runtime libraries or `LD_LIBRARY_PATH`. Keep the web client stage.
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

## Outcome

**Summary.** Implemented directly in four commits:

- `566b35e` (`docker: build occamyd without cgo on a static base`);
- `8695462` (`internal: delete libguac, its cgo bindings and protocol.IO`);
- `a52f6b6` (`ci: build without cgo, and on macOS and Windows`);
- `19b444b` (`docs: describe occamy on guacd without libguac`).

Drift: Minimal. All 7 steps shipped. The one addition the spec missed is
keeping the Apache license for `occamy.js`, which still ships after
`guacamole/` is gone.

**What shipped.**

- **Deleted:**
  - `guacamole/` (the guacamole-server 1.0.0 fork) and `internal/lib/`;
  - `internal/uuid/`, which only `internal/lib` used;
  - `internal/protocol/io.go`. `TestNewInstructionIO` runs over `net.Pipe`
    instead of a socket pair.
  - The commit removes 47,955 lines. `go mod tidy` changed nothing.
- **`docker/Dockerfile`:**
  - the build stage runs `CGO_ENABLED=0 go build -trimpath`;
  - the runtime is `gcr.io/distroless/static-debian13:nonroot`, running as
    `nonroot`.
  - The image went from 172 MB to 34.7 MB (podman, linux/arm64).
- **`.github/workflows/dockerimage.yml`:**
  - job `go`: `CGO_ENABLED=0 go build ./...`, `go vet ./...`,
    `go test -race ./...` and govulncheck, with no apt or libguac steps;
  - new job `cross` (`build (macos-latest)`, `build (windows-latest)`):
    `CGO_ENABLED=0 go build ./...`.
- **`Makefile`:** `compile` runs `CGO_ENABLED=0 go build -trimpath`.
- **Docs:**
  - README: the status table, image paragraph, native build (Go plus a
    guacd), tests section, pitch (linking 008) and license section;
  - `docs/architecture.md`: the figure, components and flow show guacd;
  - `docs/README.md`: the TL;DR;
  - `docs/protocol-cn.md`: a note that guacd loads the plugins now;
  - CONTRIBUTING and CHANGELOG.
- **Specs:**
  - [002](../002-ssh-session-rejoin.md) is archived as moot;
  - [001](../001-status-quo.md)'s known issues mark #22 and #25 resolved by
    005;
  - [003](../003-session-lifecycle-hardening.md) has a note at the top.
- **Verified:**
  - `CGO_ENABLED=0 go build ./...`, `go vet ./...` and `go test -race ./...`
    pass;
  - `occamyd` cross-compiles for windows/amd64, darwin/amd64,
    freebsd/amd64, linux/amd64 and linux/arm64;
  - `make build run e2e` passes vnc, vnc-again, ssh, ssh-again and rdp on
    the new image;
  - `OCCAMY_PRESSURE_N=10` passes;
  - `/static/` is served.

**Design evolution.**

- **Runtime base.** The spec offered distroless static or
  `debian:trixie-slim`. It is distroless with the `:nonroot` tag. occamyd
  writes no files and listens above 1024. Nothing in compose or CI runs a
  shell in the occamy container.
- **`.dockerignore`.** It had no entries that existed only for
  `guacamole/`. It gained `.claude`, so that agent worktrees there never
  enter the build context.

**Not implemented.** None.

**Unspecified work.**

- **Apache license for `occamy.js`** (scaffolding; it blocked the PR).
  `client/occamy-web/src/components/occamy.js` is a modified copy of
  guacamole-common-js 0.9.14 (`Occamy.API_VERSION`) with its Apache
  headers stripped. `guacamole/LICENSE` and `NOTICE` were the only Apache
  license in the repository, so deleting `guacamole/` would have shipped
  Apache-2.0 code without its license. The changes:
  - the files moved to `client/occamy-web/LICENSE.guacamole` and
    `NOTICE.guacamole`, and the NOTICE now names the fork;
  - `occamy.js` gained a header;
  - the image keeps them under `/occamy/licenses/guacamole/`;
  - `release.yml` adds them to the web client archive.
- `go test -race ./...` rather than `go test ./...`, which keeps
  [011](../011-release-readiness.md)'s race gate (improvement).
- `make compile` lost `-x` and gained `-trimpath`, matching the image
  (improvement).
- CHANGELOG, CONTRIBUTING and `docs/README.md` updates, and the README
  pressure-test line: it no longer crashes occamyd, and CI enabling it
  stays with 003 (improvement).

**Decisions, surprises, follow-ups.**

- **Ordering.** [rdp-xrdp](rdp-xrdp.md) ran first, so the README's RDP row
  was already set; this task set the VNC and SSH rows only.
- **Vite drops the license header.** It drops the `/*!` comment from
  the bundle, so the shipped notice is the LICENSE/NOTICE pair next to
  `dist`, not the comment.
- **Race detector.** `go test -race` needs cgo for the race runtime on
  Linux. The `go` job keeps it, while the build itself is checked with
  `CGO_ENABLED=0` on Linux, macOS and Windows.
- **Follow-ups:**
  - [011](../011-release-readiness.md) phase 4 (GoReleaser binaries,
    third-party license report) is unblocked;
  - #25 is closed by this PR;
  - the Windows RDP check (#32) remains the last open item on
    [005](../005-upstream-guacamole.md).

