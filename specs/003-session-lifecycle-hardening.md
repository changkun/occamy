---
title: Session lifecycle hardening
status: testing
track: server
depends_on:
  - specs/005-upstream-guacamole.md
  - specs/006-demo-stack-refresh.md
affects:
  - server/routers.go
  - server/routers_test.go
  - tests/pressure_test.go
  - tests/occamy_test.go
  - .github/workflows/dockerimage.yml
  - README.md
  - docs/
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Session lifecycle hardening

## Overview

`occamyd` gives every client with the same claims one shared guacd
connection. Most of what this spec was first written against went away in
[005](005-upstream-guacamole.md): libguac was deleted, and
[guacd-sessions](005-upstream-guacamole/guacd-sessions.md) rebuilt the
session table so that it never holds its lock across I/O. Four gaps remain:

- the pressure test does not run in CI;
- the table has no concurrent stress test;
- a client can still join a session as its last user leaves;
- the debugging and measurement tools asked for in #18 are undocumented.

Closing them lets #20 and #18 be closed.

## Current State

### Session table

`server/routers.go`, as shipped in v0.5.0:

- `routeConn` validates the claims (`config.JWT.GuacdParams`) before
  touching the table. A malformed token is rejected with status 768 and
  holds nothing.
- `enter` counts a user under `proxy.mu` and creates the `Session` if
  there is none; the creator is the owner.
- The owner dials guacd with `select <protocol>` outside the lock.
  `started` then records the connection ID or the error and closes
  `ready`. Joiners wait on `ready`, bounded by `guacd.DefaultTimeout`, and
  dial with `select $id`.
- A failed owner handshake removes the entry at once, and the waiting
  joiners fail with the owner's error. The next connection with the same
  claims starts a new session.
- `leave`, deferred in `routeConn`, decrements the count and removes the
  entry at zero. `remove` deletes an entry only if it is still the one in
  the table.
- Each user has its own TCP connection to guacd, which `serveIO` closes.
  There is no shared client object that two users could close twice.

`server/routers_test.go` covers the table against a fake guacd:

- `TestRouteConnReleasesLockOnJoinError`
- `TestRouteConnNoLockDuringHandshake`
- `TestRouteConnJoin`
- `TestRouteConnOwnerFailureFailsJoiners`
- `TestRouteConnLastUserRemovesEntry`

CI runs `go test -race ./...`. Nothing yet runs `enter`, `started` and
`leave` concurrently on shared keys.

### Pressure test

`tests/pressure_test.go` (`TestConnectionPressure`) runs N rounds of
concurrent sessions against every demo server, half of them sending
garbage, and then checks that a new session still works.

- It passed at N=10 (3 runs) and N=50 (2 runs) against the guacd table,
  locally.
- It is skipped unless `OCCAMY_PRESSURE_N` is set, and the e2e job in
  `.github/workflows/dockerimage.yml` does not set it.
- Its doc comment still says it crashes `occamyd`.
- Its final check uses claims the storm never used, a workaround for
  defect 8 below.

### Dying-session race

`tests/occamy_test.go` `checkSession` retries up to five times when a
session is disconnected before the desktop arrives. It does this because
of defect 6, which the guacd table narrowed but did not close (see
[Join a session as its last user leaves](#join-a-session-as-its-last-user-leaves)).

### Debugging and measurement

- `server/connection.go` serves `/debug/pprof/*` in debug mode. `conf.yaml`
  and the demo stack run in debug mode. The usage notes are only a code
  comment.
- `internal/guacamole/benchmark_test.go` has the parser benchmarks.
- Nothing in `docs/` mentions either.

### Issues

- #20 (panics under pressure): a cgo crash in libguac, open.
- #18 (benchmarking and debugging tools): open.
- #17 (concurrent join panic): closed.

### History: defects in the libguac session table

These were found during the 2026 revival. Other specs and the tests cite
them by number.

1. **Proxy-wide deadlock.** `Join` returned without unlocking `proxy.mu`
   when the JWT `host` had no port. Fixed during the revival. Claims are
   now validated before the table (`TestRouteConnReleasesLockOnJoinError`).
2. **Double free on concurrent leave.** Two leaving users both closed the
   libguac client, which segfaulted in `guac_client_remove_user` at
   `OCCAMY_PRESSURE_N=3`. Gone with libguac (005).
3. **Leak on `NewUser` failure.** Gone with libguac.
4. **Lost I/O errors.** `serveIO` shadowed `err`, so it always reported
   nil. Fixed in guacd-sessions: `serveIO` returns the first side's error.
5. **Thread pinning** in `NewSession`. Gone with libguac.
6. **Joining a dying session.** A client with the same claims that
   connects as the last user leaves joins the ending session and is
   disconnected. Narrowed but still open; this spec closes it.
7. **Unused fields** (`Session.once`, several `lib.Client` fields). Gone.
8. **A failed connect stuck its claims until restart.** A desktop read
   blocked on a socket pair kept the entry alive forever. Fixed in
   guacd-sessions: reads are on a `net.Conn`, which `Close` unblocks, and a
   failed owner handshake leaves the table at once.

## Components

### Pressure test in CI

- The e2e job runs the pressure test with a bounded round count:
  `OCCAMY_PRESSURE_N=10` gives 60 concurrent sessions over three
  protocols. `make e2e OCCAMY_PRESSURE_N=10` already passes the variable
  through.
- After the storm, check one session with the storm's own claims, as a
  regression test for defect 8, as well as the fresh-claims session.
- Check that `occamyd`'s goroutine count returns to its pre-storm baseline,
  within a small margin. Read it from `/debug/pprof/goroutine?debug=1`
  (`goroutine profile: total N`), which the demo stack serves.
- Update the test's doc comment, and the README paragraph that says CI does
  not run it.

### Session table stress test

Add a test to `server/routers_test.go` that runs many goroutines through
`routeConn` on a handful of keys against the fake guacd, with some owner
handshakes failing. Under `-race` it asserts:

- every joiner of a successful session gets its owner's connection ID;
- every joiner of a failed session gets the owner's error;
- the table is empty once all of them leave.

### Join a session as its last user leaves

The window: `serveIO` closes the user's guacd connection, and only then
does the deferred `leave` remove the entry. When that user is the last
one, guacd ends the connection as soon as it sees the close ("Last user of
connection ... disconnected"), while the entry still counts one user. A
client with the same claims that enters in between becomes a joiner of a
connection that is gone. guacd 1.6 answers its `select $id` with
`No such connection.` and status 516, and the client is closed with 516.

The joiner can be made to fall back. When a joiner's handshake fails with
516, it removes the entry if it is still the same session and retries
once, as the owner of a new session. That closes the window from the
client's point of view, with no protocol change. Calling `leave` before
closing the guacd connection narrows the window too, but it cannot close
it. A joiner whose handshake is in flight when the last established user
closes still finds the connection gone.

Once the fallback is in, `checkSession` no longer needs to retry. Keep the
retry loop but fail on a retry, so that a regression shows.

### Debugging and measurement (#18)

Write `docs/debugging.md`. It covers:

- the pprof routes and how to switch debug mode on and off; release mode
  must not expose them (see [004](004-security-hardening.md));
- the protocol benchmarks (`go test -bench . ./internal/guacamole/`);
- the end-to-end and pressure tests;
- how to see guacd's per-connection processes in the demo stack.

Record baseline numbers from one pressure run:

- wall time;
- `occamyd`'s goroutines and RSS before and after;
- guacd's process count before and after.

#18 asks that threads exit once all connections close. Under guacd that
means `occamyd`'s goroutines and guacd's processes return to their
baselines.

## Error Handling

Failures still close the WebSocket with the Guacamole status as the close
reason, and a failed join does not affect other sessions. The one change
is a joiner that gets 516: it retries once as an owner. Every other joiner
failure is returned as it is.

## Testing Strategy

- The stress test above, under `-race`.
- A fallback test with the fake guacd: a joiner whose `select $id` gets
  516 starts a new session as its owner, and the stale entry is gone. The
  fake answers unknown connections with 519 today; switch it to 516 to
  match guacd 1.6.
- The pressure test in the e2e job, with the storm-claims check and the
  goroutine baseline.
- The e2e suite passing several times in a row with `checkSession` failing
  on any retry.

## Open Questions

- Should the pressure test also check that guacd's process count returns
  to its baseline? The test would need access to the guacd container, or
  the workflow could check it in a step after the tests.
- Is N=10 the right CI budget, given how long the e2e job takes now?
- Whether the session key should include the password decides who shares
  a session. That belongs to [004](004-security-hardening.md).

## Acceptance Criteria

- [x] No connection can leave `proxy.mu` locked.
- [x] A failed connect does not stick its claims (defect 8): `started`
  removes the entry at once. No test checks it yet; the pressure test's
  storm-claims check will.
- [ ] Concurrent `routeConn` on shared keys is `-race` clean and leaves the
  table empty.
- [ ] A client that connects as the last user of its session leaves gets a
  working session without retrying (defect 6).
- [ ] The pressure test runs in the e2e job, and `occamyd`'s goroutines
  return to their baseline afterwards.
- [ ] Debugging and measurement are documented with baseline numbers.
- [ ] #20 and #18 are closed, with a comment on what fixed each.
