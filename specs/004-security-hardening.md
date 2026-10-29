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
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Security hardening

## Overview

Occamy's authentication model was built for a demo. The JWT carries the
remote desktop credentials in readable form, the demo login endpoint lets
anyone point the proxy at any host, and the shipped configuration exposes
debug endpoints. Issue #54 asked whether Occamy is production ready; this
spec defines what has to change before the answer can be yes.

## Current State

- **Credentials in tokens.** `server/connection.go` `PayloadFunc` puts
  `protocol`, `host`, `username` and `password` from `internal/config.JWT`
  into the claims. JWTs are signed, not encrypted, so anyone holding a token
  can read the password.
- **Tokens in logs.** The web client connects with `?token=...`
  (`client/occamy-web/src/components/Desktop.vue`) and `gin.Default()` logs
  the full request URI, so every connect writes the credentials to the log.
  Observed in the revival's end-to-end runs.
- **Open proxy.** With `client: true`, `/api/v1/login` signs a token for any
  `host:port` without authenticating the caller, which makes `occamyd` a
  WebSocket-to-TCP bridge into whatever network it can reach.
- **Defaults.** `conf.yaml` ships `jwt_secret: occamy` and `mode: debug`.
  Debug mode registers `/debug/pprof` (`(*proxy).profile`) on the public
  listener.
- **Session sharing by credentials.** `config.JWT.GenerateID` is an MD5 of
  the concatenated claims without separators, and identical claims silently
  join the same session.
- **Transport.** No TLS options; the image runs as root.
- **Dependencies.** gin-jwt is on v2.10; v3 is current and changes the API.
- Cross-origin WebSockets are rejected by gorilla's default origin check
  (`websocket.Upgrader` without `CheckOrigin`), which is correct and should
  stay.

## Architecture

Split *who may connect* from *what to connect to*:

- **Connection registry (recommended).** Connection parameters live on the
  server, from configuration or an admin API, and tokens reference a
  connection ID plus the caller's identity. No remote credential ever
  leaves the server.
- **Encrypted tokens.** Keep self-contained tokens but use JWE so claims are
  confidential. Smaller change; tokens remain bearer credentials for the
  remote host.
- **External issuer.** Disable `/api/v1/login` outside demos (already the
  case with `client: false`) and document the token contract for an
  upstream identity provider.

Independent of the option chosen:

- A target allowlist (`hosts` / CIDRs) in `conf.yaml`, enforced at connect.
- `mode: release` by default; pprof only on a separate, opt-in listener.
- Refuse to start with the default secret unless in debug mode.
- Pass the token in the `Sec-WebSocket-Protocol` header or a cookie instead
  of the query string, and stop logging query strings.
- Explicit session sharing (a `share` claim or connection setting) instead
  of credential-hash matching.
- Run the image as a non-root user.
- Once [005](005-upstream-guacamole.md) puts guacd behind `occamyd`:
  optional TLS to guacd (`guacd.tls`, matching guacd's `-C`/`-K` flags),
  and a note that guacd must never be reachable from untrusted networks,
  because it connects anywhere it is asked to.
- Move to gin-jwt v3 together with the new token model, rather than porting
  the current claims first. Do this only if the v0 routes outlive
  `v1.0.0-beta.1`: the v1 API design
  ([api-and-server-stack](008-native-architecture/api-and-server-stack.md))
  recommends the standard library and golang-jwt v5 (or opaque tokens),
  with these properties built in.

## API Surface

- `conf.yaml`: `auth.allow` (target allowlist), a `debug.pprof_address`
  listener, and possibly `connections` for the registry option.
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
- [ ] Migrated to gin-jwt v3.
