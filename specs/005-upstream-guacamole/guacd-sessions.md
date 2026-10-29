---
title: Serve sessions through guacd
status: validated
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-handshake-client.md
  - specs/005-upstream-guacamole/guacd-compose-service.md
affects:
  - server/session.go
  - server/routers.go
  - server/routers_test.go
  - server/connection.go
  - internal/config/config.go
  - internal/config/config_test.go
  - conf.yaml
  - docker/occamy.yaml
  - docker/docker-compose.yaml
  - client/occamy-web/src/components/Desktop.vue
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Serve sessions through guacd

## Goal

Switch `occamyd` from in-process libguac to upstream guacd. After this
task the `server` package no longer imports `internal/lib`. VNC and SSH
work through guacd, repeated SSH sessions work (#22), and the session
table never holds the proxy lock across network I/O.

## What to do

1. **Config** (`internal/config/config.go`, `conf.yaml`):

   ```yaml
   guacd:
     address: localhost:4822
     params:            # per-protocol defaults, merged under token values
       rdp:
         ignore-cert: "true"
   ```

   The type is `Guacd struct { Address string; Params map[string]map[string]string }`.
   Add `docker/occamy.yaml`, a copy of `conf.yaml` with
   `address: guacd:4822`, and mount it read-only over `/occamy/conf.yaml`
   for the `occamy` service in `docker/docker-compose.yaml`.

2. **Parameters**: build `guacd.Config.Params` from the token.
   `net.SplitHostPort(jwt.Host)` gives `hostname` and `port` (a split
   error rejects the connection before any dial, as today). `username`
   and `password` come from the claims. Then copy in
   `config.Runtime.Guacd.Params[jwt.Protocol]` for names the token does
   not set.

3. **Display hints**: `serveWS` reads optional `width`, `height`, `dpi`
   and `timezone` query parameters (clamped to 1–8192 and 48–600),
   defaulting to 1024×768 at 96. `Desktop.vue` appends them from
   `window.innerWidth`, `window.innerHeight`, `devicePixelRatio * 96` and
   `Intl.DateTimeFormat().resolvedOptions().timeZone` to the data passed
   to `guac.connect`.

4. **Session table** (`server/session.go`, `server/routers.go`):

   ```go
   type Session struct {
       Key    string        // jwt.GenerateID(), unchanged sharing rule
       ConnID string        // guacd connection ID, set once ready
       ready  chan struct{} // closed when ConnID is known or err is set
       err    error
       users  int           // guarded by proxy.mu
   }
   ```

   `routeConn`:
   - Lock, look up `Key`. If absent, insert a new `Session` with an open
     `ready` channel and mark this caller as the owner. Increment `users`
     and unlock. No network I/O happens under `p.mu`.
   - Owner: `guacd.Dial` with `Protocol`; on success set `ConnID`; on
     failure set `err`. Close `ready` in both cases.
   - Joiner: wait for `ready` (bounded by the handshake timeout). If the
     owner failed, return its error. Otherwise `guacd.Dial` with
     `ConnectionID: s.ConnID`.
   - On return, lock, decrement `users`, delete the entry when it reaches
     zero, and unlock. That mirrors guacd, which ends a connection when
     its last user leaves. A joiner that races a dying connection gets
     guacd's `No such connection.` (519), which is sent to the client as
     a close reason.

   Delete `NewSession`, `Join`'s `unlock` parameter, the socketpair,
   `runtime.LockOSThread`, `connectedUsers` and the unused `once`.

5. **Proxy loop** (`serveIO`, now taking `*guacd.Conn`):
   - guacd → client: send the internal-opcode instruction
     `0.,<n>.<ConnID>;` first. guacamole-client 1.6.0 sends the same kind
     of message with its tunnel UUID
     (`GuacamoleWebSocketTunnelEndpoint`), and `occamy.js` already accepts
     it (`Occamy.Tunnel.INTERNAL_DATA_OPCODE`). Then forward each
     `ReadRaw` result as one text message.
   - client → guacd: a message may hold several instructions. Split them
     with the length-aware reader over a `bytes.Reader`. Never forward
     internal-opcode instructions (opcode `""`). Answer
     `0.,4.ping,<ts>;` with the same instruction, as
     `GuacamoleWebSocketTunnelEndpoint` does in 1.6.0. The bundled
     `occamy.js` sends no pings, but current guacamole-common-js clients
     do. Forward the rest.
   - Keep the first error and close both sides as today.

6. Log the guacd connection ID with every session log line. Never log
   the parameters (they contain the password).

## Tests

`server/routers_test.go` becomes hermetic: an in-process fake guacd on
`127.0.0.1:0` that speaks select/args/connect/ready, records what it
received, and echoes `sync`. Drive `routeConn` through an `httptest`
server and a real WebSocket dialer.

- `TestRouteConnReleasesLockOnJoinError`: kept; the portless host fails,
  `p.mu.TryLock` succeeds, and the table is empty.
- `TestRouteConnNoLockDuringHandshake`: a fake guacd that stalls `args`
  does not block a second connection with different claims.
- `TestRouteConnJoin`: a second client with the same claims sends
  `select,$<id>` of the first.
- `TestRouteConnOwnerFailureFailsJoiners`: the owner's handshake fails,
  and a waiting joiner gets the same error without dialing.
- `TestRouteConnLastUserRemovesEntry`: the entry exists while either user
  is connected and is gone after both leave.
- `TestServeIOInternalOpcodes`: the client first receives
  `0.,<n>.$id;`, a client `ping` is answered and not forwarded, and a
  message carrying two instructions reaches guacd as two.
- `internal/config`: `TestGuacdParams` merges per-protocol defaults under
  token values.

End to end: the [006](../006-demo-stack-refresh.md) suite passes for VNC
and SSH through guacd, plus a new case that opens two SSH sessions one
after another (#22).

## Boundaries

- Do not delete `internal/lib`, `guacamole/` or the libguac Docker stage
  ([remove-libguac](remove-libguac.md)).
- Do not change `/api/v1/login`, JWT claims or gin-jwt
  ([004](../004-security-hardening.md)).
- No RDP demo server ([rdp-xrdp](rdp-xrdp.md)).
- Keep the `guacamole` WebSocket subprotocol and text frames.
