---
title: Security hardening
status: drafted
track: server
depends_on: []
affects:
  - server/connection.go
  - server/routers.go
  - internal/config/
  - go.mod
  - conf.yaml
  - docker/Dockerfile
  - client/occamy-web/src/components/
effort: medium
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# Security hardening

> **Note (2026-10-31).** Version 1 configuration files
> ([api-and-server-stack](008-native-architecture/api-and-server-stack.md))
> deliver most of the Architecture below; each item says so. The
> Current State and the acceptance criteria are about v0 files, such as
> the `conf.yaml` that the image and the release archives ship, until
> v1.0 removes the v0 routes.

## Overview

Occamy's authentication model was built for a demo. The JWT carries the
remote desktop credentials in readable form, the demo login endpoint lets
anyone point the proxy at any host, and the shipped configuration exposes
debug endpoints. Issue #54 asked whether Occamy is production ready; this
spec defines what has to change before the answer can be yes.

## Current State

- **Credentials in tokens.** `/api/v0/login` (`(*tokens).issue` in
  `server/jwt.go`) puts `protocol`, `host`, `username` and `password`
  from `internal/config.JWT` into the claims. JWTs are signed, not encrypted, so anyone holding a token
  can read the password.
- **Tokens in logs.** The web client connects with `?token=...`
  (`client/occamy-web/src/components/Desktop.vue`). `gin.Default()` logged
  the full request URI, so every connect wrote the credentials to the log.
  Since the v0 routes moved to `net/http`
  ([api-and-server-stack](008-native-architecture/api-and-server-stack.md),
  v0 routes), occamyd's log leaves out query strings; a reverse proxy in
  front can still log the token.
- **Open proxy.** With `client: true`, `/api/v0/login` signs a token for any
  `host:port` without authenticating the caller, which makes `occamyd` a
  WebSocket-to-TCP bridge into whatever network it can reach.
- **Defaults.** `conf.yaml` ships `jwt_secret: occamy` and `mode: debug`.
  Debug mode registers `/debug/pprof` (`profile` in
  `server/connection.go`) on the public listener.
- **Session sharing by credentials.** Identical claims silently join the
  same session. `config.JWT.GenerateID` keys it by a SHA-256 of the
  length-prefixed claims since v0.5.0 (#70). Before that it was an MD5 of
  the claims concatenated, so different credentials could share a session.
- **Transport.** A v0 file has no TLS options; a v1 file terminates TLS
  when `tls.cert` and `tls.key` are set. Neither has TLS to guacd. Since
  v0.5.0 the image runs as the distroless `nonroot` user, and request
  headers time out after 10 seconds (`ReadHeaderTimeout`).
- **guacd.** Since [005](005-upstream-guacamole.md), a v0 file hands
  every connection to guacd (`guacd.address`), which connects anywhere
  it is asked to; a v1 file hands it every connection but ssh and vnc
  with `backend: native`. Nothing in the docs yet says it must stay off
  untrusted networks.
- **Dependencies.** The v0 routes run on golang-jwt v5, `net/http` and
  coder/websocket; gin, gin-jwt and gorilla/websocket are gone.
- Cross-origin WebSockets to the v0 routes are rejected by
  coder/websocket's default origin check (`websocket.Accept` without
  `OriginPatterns`), the same rule as gorilla's default, which is correct
  and should stay. v1 streams check `Origin` themselves, against
  occamyd's own origin and `origins`.

## Architecture

Split *who may connect* from *what to connect to*:

- **Connection registry (recommended).** Connection parameters live on the
  server, from configuration or an admin API, and tokens reference a
  connection ID plus the caller's identity. No remote credential ever
  leaves the server. (Done for v1 files: `connections`, whose
  credentials name files or environment variables; access and session
  tokens name a user or a session, never a remote credential.)
- **Encrypted tokens.** Keep self-contained tokens but use JWE so claims are
  confidential. Smaller change; tokens remain bearer credentials for the
  remote host.
- **External issuer.** Disable `/api/v0/login` outside demos (already the
  case with `client: false`) and document the token contract for an
  upstream identity provider. (Done for v1 files: `auth.trusted_issuers`
  and `auth.oidc`; a v1 file serves `/api/v0/login` only with
  `legacy_v0`.)

Independent of the option chosen:

- A target allowlist (`hosts` / CIDRs) in `conf.yaml`, enforced at connect.
  (Done for v1 files: registry connections have fixed targets, and
  ad-hoc targets need `allow_adhoc_targets` and an `adhoc_allowlist`
  entry, checked when the session opens.)
- `mode: release` by default; pprof only on a separate, opt-in listener.
  (Done for v1 files: they have no `mode`, and pprof is served only on
  the debug listener, when `debug.address` is set.)
- Refuse to start with the default secret unless in debug mode. (Moot
  for v1 files: they sign with an Ed25519 key in `signing_key_file`, and
  refuse `jwt_secret`.)
- Pass the token in the `Sec-WebSocket-Protocol` header or a cookie instead
  of the query string, and stop logging query strings. (Done: the request
  log leaves out query strings, and v1 streams take the token in the
  `occamy.v1.token.` subprotocol or `Authorization`.)
- Explicit session sharing (a `share` claim or connection setting) instead
  of credential-hash matching. (Done for v1 files: shares for the
  `viewer` or `read-only` role, from `POST /api/v1/sessions/{id}/shares`.)
- Run the image as a non-root user. (Done in
  [remove-libguac](005-upstream-guacamole/remove-libguac.md): distroless
  `:nonroot`.)
- Optional TLS to guacd (`guacd.tls`, matching guacd's `-C`/`-K` flags),
  and a note in `SECURITY.md` and the README that guacd must never be
  reachable from untrusted networks.
- ~~Move to gin-jwt v3 together with the new token model.~~ Superseded:
  the v0 routes outlive `v1.0.0-beta.1` and were ported to the standard
  library and golang-jwt v5 instead, with their claims unchanged
  ([api-and-server-stack](008-native-architecture/api-and-server-stack.md),
  v0 routes).

## API Surface

- `conf.yaml`: `auth.allow` (target allowlist), a `debug.pprof_address`
  listener, and possibly `connections` for the registry option. (A v1
  file has them as `allow_adhoc_targets` with `adhoc_allowlist`,
  `debug.address` and `connections`.)
- Token claims change; the web client and `tests/pressure_test.go` follow.

## Testing Strategy

- Unit tests for the allowlist and claim validation in `internal/config`.
- HTTP tests in `server/` with `httptest`: login rejected without
  authentication when configured, connect rejected for disallowed targets,
  pprof absent in release mode.
- Check that request logs contain no tokens.

## Acceptance Criteria

- [ ] No remote credential is readable from a token or a log line.
- [ ] The default configuration exposes neither pprof nor an open login.
- [ ] Connections are limited to configured targets.
- [x] Off gin-jwt v2: superseded by golang-jwt v5 (api-and-server-stack,
  v0 routes) instead of gin-jwt v3.
