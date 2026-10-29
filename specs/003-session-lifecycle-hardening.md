---
title: Session lifecycle hardening
status: drafted
track: server
depends_on:
  - specs/006-demo-stack-refresh.md
affects:
  - server/routers.go
  - server/session.go
  - internal/lib/client.go
  - internal/lib/user.go
  - tests/pressure_test.go
effort: large
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Session lifecycle hardening

> **Note (2026-10-29).** [005](005-upstream-guacamole.md) moved sessions to
> upstream guacd and deleted libguac, so the libguac defects below (2, 3, 5,
> 8) are gone with the code they were in. The new session table
> ([guacd-sessions](005-upstream-guacamole/guacd-sessions.md)) never holds
> the lock across I/O. It drops an entry when its last user leaves, and it
> reports I/O errors, which covers 4, 6 and 7. The pressure test passes against
> it (`OCCAMY_PRESSURE_N=10` and `=50`, locally) but is not in CI yet. Refine
> the rest of this spec against `server/routers.go` and `server/session.go` as
> they are now.

## Overview

The session table and its locking in `server/` are fragile. Any authenticated
caller can wedge the whole proxy with one malformed token, and concurrent
joins and leaves race on teardown, which is the likely source of the panics
under load in issues #20 and #17. This spec makes session creation, sharing
and teardown correct under concurrency and failure, and sets up the
measurement tooling asked for in #18.

## Current State

`server/routers.go` `routeConn` takes `proxy.mu`, looks up or creates a
`Session` keyed by `config.JWT.GenerateID()`, and hands an `unlock` callback
to `(*Session).Join` in `server/session.go`, which is expected to call it
once the user is prepared.

Defects found by reading the code during the 2026 revival:

1. **Proxy-wide deadlock (fixed).** `Join` returned without calling
   `unlock` when `lib.NewSocket` or `lib.NewUser` fails. `lib.NewUser` fails
   whenever the JWT `host` has no port (`net.SplitHostPort`), and with
   `client: true` anyone can mint such a token from `/api/v1/login`. After one
   such connection `proxy.mu` stayed locked and every later connection
   blocked forever. Fixed: `Join` wraps `unlock` in `sync.OnceFunc` and
   defers it, `lib.NewUser` validates the host before allocating, and the
   socket pair is closed on early returns. `server/routers_test.go` covers
   it; `config.Init` moved from `init()` into `server.Run` so the package is
   testable.
2. **Double free on concurrent leave.** `Join` defers the user-count
   decrement after `s.close`, so it runs first. Two users leaving together
   can both observe zero users and both call `(*lib.Client).Close`, which has
   no guard and passes a nil `guac_client` the second time. Observed on
   2026-10-29 under the [006](006-demo-stack-refresh.md) pressure test
   (`OCCAMY_PRESSURE_N=3`, 3 of 8 runs on a fresh `occamyd`): SIGSEGV in
   `guac_client_remove_user` from a joiner's `HandleConnection` while the
   owner's `Join` ran `guac_client_free`, and in `join_handler_bridge` and
   `guac_client_stop`.
3. **Leak on `NewUser` failure.** Fixed for the invalid-host path (the host
   is parsed before `guac_user_alloc`); any later failure in `NewUser` must
   free the user too.
4. **Lost I/O errors.** In `(*Session).serveIO` both goroutines shadow
   `err` inside the loop, so the error sent on `exit` is always nil.
5. **Thread pinning.** `NewSession` calls `runtime.LockOSThread` and never
   unlocks, deliberately terminating the thread when the handler returns.
   Later joiners of a shared session run on other threads, so the guarantee
   it was meant to give is unclear.
6. **Joining a dying session (observed).** The owner's entry stays in the
   table until its teardown finishes, so a client with the same claims that
   connects right after the owner leaves joins the dying session and is
   disconnected moments later.
7. `Session.once` is unused; `lib.Client` carries several unused fields
   (`users`, `owner`, `handler*`).
8. **A failed connect poisons its claims until restart (observed).** When
   the remote desktop is unreachable, libguac sends `error`, and the client
   then disconnects. `serveIO` closes its end of the socket pair, but its
   desktop reader stays blocked in `syscall.Read` on that descriptor: a
   close does not wake a read in another thread, and the stopped VNC client
   never writes again. `Join` never returns and the entry is never
   removed. Every later connection with the same claims joins the dead
   session and gets only `disconnect`. Reproduced on 2026-10-29 with a VNC
   token for a closed port, identically before and after
   [instruction-stream-io](005-upstream-guacamole/instruction-stream-io.md).
   [guacd-sessions](005-upstream-guacamole/guacd-sessions.md) removes the
   cause: reads are on a `net.Conn`, which `Close` does unblock, and the
   entry goes when its last user leaves.

If [005](005-upstream-guacamole.md) replaces the in-process libguac with
upstream guacd, the socket pair and cgo user in `Join` become a TCP
connection to guacd; the locking and reference-counting design below applies
unchanged.

`tests/pressure_test.go` exercises concurrent connects against the
[006](006-demo-stack-refresh.md) demo stack. It is skipped unless
`OCCAMY_PRESSURE_N` is set, and CI does not set it because it still crashes
`occamyd` (defect 2).

## Architecture

Separate the session table from the cgo lifecycle:

- `proxy.mu` guards only the map and is never held across cgo calls or
  network I/O.
- Each `Session` owns a reference count and its own mutex. Joining
  increments under the session lock; leaving decrements and, at zero,
  removes the session from the map and closes the libguac client exactly
  once (`sync.Once` or state under lock).
- Session creation that may block (plugin load, user prepare) happens
  outside `proxy.mu`; concurrent first joiners for the same key wait on the
  session's readiness rather than on the global lock.
- Every early return releases what was acquired; prefer `defer` over
  callbacks passed down the stack.

Open questions:

- Should the session key include the password at all? It currently decides
  sharing (see [004](004-security-hardening.md)).
- Is the thread-pinning in `NewSession` required by libguac (thread-local
  state in the plugins) or a leftover from early debugging?

## Components

### Session table (`server/routers.go`)

Replace the `unlock` callback protocol with a lookup-or-create that returns a
referenced session, plus a release function.

### Session (`server/session.go`)

Reference counting, single close, I/O error propagation, user-facing error
instruction on join failure.

### libguac bindings (`internal/lib/`)

`(*Client).Close` idempotent; `NewUser` validates `host` before allocating
and frees on every failure path. Remove dead fields.

### Measurement (#18)

Document the existing `/debug/pprof` routes and the parser benchmark in
`internal/protocol/benchmark_test.go`, and record baseline numbers from the
pressure test.

## Error Handling

A failed join must send an `error` instruction and close the WebSocket
without affecting other sessions. Malformed claims (`host` without port,
unknown protocol) are rejected before any libguac allocation.

## Testing Strategy

- Unit tests for the session table with a fake client behind a small
  interface, run with `-race`: concurrent join/leave on one key, failing
  creation, failing join.
- Regression test for defect 1: a portless host followed by a valid connect
  succeeds.
- `tests/pressure_test.go` against the [006](006-demo-stack-refresh.md) demo
  stack, run in CI with a bounded concurrency and duration, with no panics
  and a stable goroutine count afterwards.

## Acceptance Criteria

- [x] No connection can leave `proxy.mu` locked.
- [ ] Concurrent leaves never close a client twice (`-race` clean).
- [ ] Pressure test passes in CI; issues #20 and #18 can be closed.
