---
title: Serve sessions through guacd
status: complete
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-handshake-client.md
  - specs/005-upstream-guacamole/guacd-config.md
affects:
  - server/session.go
  - server/routers.go
  - server/routers_test.go
  - server/connection.go
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

1. **Address**: dial `config.Runtime.Guacd.Address`
   ([guacd-config](guacd-config.md)).

2. **Parameters**: `jwt.GuacdParams(config.Runtime.Guacd.Params)`. Its
   error (a host without a port) rejects the connection before any
   dial, as today.

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
     guacd's `No such connection.` (516 in guacd 1.6), which is sent to
     the client as the close reason.

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
     with `protocol.ReadRaw(bufio.NewReader(bytes.NewReader(msg)))`. Never forward
     internal-opcode instructions (opcode `""`). Answer
     `0.,4.ping,<ts>;` with the same instruction, as
     `GuacamoleWebSocketTunnelEndpoint` does in 1.6.0. The bundled
     `occamy.js` sends no pings, but current guacamole-common-js clients
     do. Forward the rest.
   - Keep the first error and close both sides. Close the WebSocket with
     the Guacamole status code as the reason, which `occamy.js` reads.

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
- `TestDisplayHints`: missing query parameters give 1024×768 at 96 DPI,
  out-of-range values are clamped, and garbage is ignored.
- `TestSessionLogsOmitPassword`: with `log.SetOutput` on a buffer, a full
  connect and disconnect never writes the token's password.

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

## Outcome

**Summary.** Implemented directly in `3105911` (`server: serve sessions
through upstream guacd`) and `eeee20b` (`client: send display hints, fit
the desktop to the window`), merged in
[#62](https://github.com/changkun/occamy/pull/62). Drift: Minimal. All
6 steps and 8 named tests shipped. guacd answers the dying-connection
race with status 516, not 519, and the close status mapping was more
than "as today". The body above is corrected for both.

**What shipped.**

- `server/session.go`:
  - `Session` as specified;
  - `closeStatus`, which maps the first error to a WebSocket close code
    and a Guacamole status;
  - `displayHints` and `intParam`;
  - `serveIO` on `*guacd.Conn`, with `forward` for client messages and
    `clientError` for client-side endings.
- `server/routers.go`:
  - `serveWS` builds the claims and display hints;
  - `serveConn` closes the WebSocket with the status as the reason;
  - `routeConn` handles owner and joiner;
  - `enter`, `started`, `leave` and `remove` manage the table. The lock
    is never held across I/O.
- `server/connection.go`: `proxy.guacd`, set from `config.Runtime.Guacd`.
  `server` no longer imports `internal/lib`, and
  `CGO_ENABLED=0 go build .` succeeds.
- `client/occamy-web/src/components/Desktop.vue`: the connect data carries
  the display hints, and `fit()` scales the display on both display and
  window resize.
- `server/routers_test.go`: the 8 named tests, plus `TestDisplayHintsReachGuacd`,
  `TestClientError` and `TestClientLeaveIsNotAFailure`. The fake guacd
  runs on `127.0.0.1:0`. The suite passes `go test -race -count=20`.
- `tests/e2e_test.go`: `ssh-again` (#22).
- Verified on podman:
  - `make build run e2e` passes vnc, vnc-again, ssh and ssh-again through
    guacd 1.6.0. guacd logs "Creating new client", "joined connection" and
    "Connection removed" in step with occamy's "owner joined/left".
  - `OCCAMY_PRESSURE_N=10` passed 3 times and `=50` passed twice, with
    occamy serving afterwards. Before this change the image crashed at
    N=3 ([003](../003-session-lifecycle-hardening.md) defect 2).

**Design evolution.**

- **Close status.** "Close both sides as today" became `serveConn` plus
  `closeStatus`. `occamy.js` parses the WebSocket close reason as a
  Guacamole status (`new Occamy.Status(parseInt(event.reason))`), so the
  reason is the status code, and a guacd `error` keeps its own status.
  The mapping:
  - no error → 1000/0;
  - guacd error → 1011 with guacd's status;
  - bad parameters or a malformed client message → 1008/0x300;
  - handshake timeout → 1011/0x202;
  - anything else → 1011/0x200.
- **Status 516, not 519.** guacd 1.6.0 answers `select` for an unknown
  connection with `error "No such connection." 516`. The spec took 519
  from the protocol reference.
- **Client endings are not failures.** These all count as the client
  leaving: close codes 1000, 1001, 1005 and 1006, and a reset or EOF with
  no close frame. Abrupt loopback closes show up as "connection reset by
  peer". `websocket.IsCloseError` does not unwrap, so `clientError` uses
  `errors.As`.
- **Owner failure removes the entry at once** (`started`), not when the
  last waiting joiner leaves. A retry with the same claims then starts a
  new session instead of inheriting the error.

**Not implemented.** None.

**Unspecified work.**

- `TestDisplayHintsReachGuacd` (the hints arrive in guacd's `size`
  instruction), `TestClientError` and
  `TestClientLeaveIsNotAFailure` (improvement).
- `Desktop.vue` `fit()`. The old code scaled once by 1024/768 with width
  and height swapped. It now scales whenever the display or window
  resizes, and only once the display has a size (improvement).
- The timezone hint is checked against an IANA-name pattern and 64
  bytes, so arbitrary text never reaches the handshake (improvement).
- README: running the image now needs a guacd. The run instructions
  create a network and a guacd container and mount `docker/occamy.yaml`
  (scaffolding).

**Decisions, surprises, follow-ups.**

- The connection ID is announced as `0.,<n>.$<id>;` from the guacd-to-client
  goroutine before any guacd output, so the two never interleave.
- `serveIO` stops the WebSocket reader with a read deadline in the past
  rather than a close. The socket stays open for the close frame that
  `serveConn` sends afterwards.
- Under the pressure storm, joiners racing a connection whose last user
  just left fail in one of two ways. Either guacd answers 516, or it
  accepts the `select`, logs "Unable to add user." and closes, which
  occamy sees as `read args: EOF` (0x200). Both are expected. The
  storm's garbage clients log `bad request: instruction with bad digit`.
- guacd notices a client leaving in the same millisecond, because
  `serveIO` closes the guacd connection. A raw client that leaves its
  socket half-open is dropped only by guacd's 15 s "User is not
  responding." timeout.
- Follow-ups:
  - [remove-libguac](remove-libguac.md) deletes `internal/lib`, since
    nothing imports it now.
  - [003](../003-session-lifecycle-hardening.md) can enable the pressure
    test in CI, which no longer crashes on the guacd path.
