---
title: Catch up with upstream guacamole
status: drafted
track: server
depends_on:
  - specs/006-demo-stack-refresh.md
affects:
  - guacamole/
  - internal/lib/
  - server/
  - conf.yaml
  - docker/
  - .github/workflows/dockerimage.yml
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Catch up with upstream guacamole

## Overview

`guacamole/` is a fork of guacamole-server 1.0.0 (2019), and its age is what
holds Occamy back today: RDP cannot be built, and C bugs in the fork crash the
whole server (two libvncclient double frees were fixed during the revival).
Upstream's current release, 1.6.0, builds against FreeRDP 2 or 3
(`configure.ac` checks `freerdp2` and `freerdp3`; the official guacd
Dockerfile defaults to FreeRDP 2). This spec decides how Occamy gets onto
upstream and restores full protocol coverage. It is the bridge toward
[008](008-native-architecture.md).

## Current State

- 43 commits in this repository touch `guacamole/`. Known changes against
  1.0.0: guacd and the handshake removed, audio removed,
  `guac_client_add_user` reduced to `void guac_client_add_user(guac_user*)`,
  `guac_user_input_thread` exported
  (`guacamole/src/libguac/guacamole/user.h`), clipboard and cursor code merged
  into `guacamole/src/protocols/vnc/vnc.c`.
- RDP sources (`guacamole/src/protocols/rdp/`, `compat/`) and
  `guacamole/configure.ac` target FreeRDP 1.0/1.1, so configure disables RDP
  on every current distribution. FreeRDP 1.0 also cannot reach current
  Windows hosts (#32).
- SSH builds against OpenSSL 3 only at the 1.1.0 API level.
- Native macOS builds segfault in `rfbInitClient` (#25).
- `occamyd` embeds libguac through cgo (`internal/lib/`), loading plugins into
  its own process (`server/session.go` `NewSession`).

## Architecture

### Options

- **A. Drive unmodified upstream guacd over TCP (recommended).** `occamyd`
  connects to a guacd (the `guacamole/guacd` image as a sidecar, or a
  distribution package), performs the Guacamole handshake (`select`, `args`,
  `size`, `audio`, `video`, `image`, `connect`) from the JWT claims, and then
  proxies instructions as `(*Session).serveIO` does today. Shared sessions map
  to guacd's join-by-connection-ID (`select $<id>`).
  - Gains: RDP, VNC, SSH, telnet and Kubernetes from upstream at once, and
    upstream security fixes for free. `occamyd` builds with
    `CGO_ENABLED=0`, so native macOS and Windows builds work (#25), and
    `guacamole/` and `internal/lib/` can be deleted. #22 and the C crash
    class leave `occamyd`'s process.
  - Costs: this temporarily reverses the README's "no guacd" pitch. guacd
    runs one process per connection, and there is one extra local hop. The
    target architecture ([008](008-native-architecture.md)) removes guacd
    again as native backends land, keeping it only as an optional fallback.
- **B. Rebase the fork onto 1.6.0.** Reapply Occamy's changes as a small,
  documented patch series. Keeps a single process; keeps C in-process and an
  ongoing merge cost.
- **C. Port only the RDP plugin to FreeRDP 3.** Smallest change now, largest
  long-term debt.

Recommendation: A. Option B only makes sense if a single process is a hard
requirement before [008](008-native-architecture.md) delivers native backends.

## Components (option A)

### guacd client (`server/`)

New connector that dials `guacd.address`, runs the handshake with the
optimal size, DPI and mimetypes currently set by `set_user_info` in
`internal/lib/user.go`, and hands the connection to the existing I/O proxy.
The session table keys a share to guacd's connection ID.

### Configuration

`conf.yaml`: `guacd.address` (default `localhost:4822`), optional TLS to
guacd.

### Removal

Delete `guacamole/`, `internal/lib/`, and the libguac stage of
`docker/Dockerfile`; the runtime image can become a static or distroless
base. Drop the libguac build from the Go CI job.

### Demo and clients

Add a `guacd` service pinned to the 1.6 line in
`docker/docker-compose.yaml`; RDP works from the existing web client again.

## Testing Strategy

- Handshake unit tests against a fake guacd on `net.Pipe`.
- [006](006-demo-stack-refresh.md) end-to-end suite for VNC, SSH and RDP
  (xrdp) through guacd.
- `CGO_ENABLED=0 go build ./...` in CI.

## Acceptance Criteria

- [ ] Option chosen (A recommended) and recorded here.
- [ ] RDP connects to xrdp and to a current Windows host.
- [ ] `occamyd` builds with `CGO_ENABLED=0`; `guacamole/` and `internal/lib/`
      removed.
- [ ] README's architecture pitch updated to match.
