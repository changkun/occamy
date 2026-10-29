---
title: Session lifecycle hardening
status: complete
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
connection that is ending. guacd 1.6 fails the join in one of three ways,
depending on how far the teardown has got:

- guacd has removed the connection. It answers `select $id` with
  `No such connection.` and status 516.
- The connection's process has stopped taking users, but guacd has not
  reaped it yet. guacd logs "Unable to add user." and closes the socket
  with no error instruction, so the joiner reads EOF where it expects
  `args`.
- The process took the joiner just before the last user left. The
  handshake completes with `ready`, and guacd disconnects the joiner at
  once.

The joiner falls back in the first two cases. When its handshake fails
with 516 or EOF, it removes the entry if it is still the same session and
enters again: as the owner of a new session, or as a joiner of the one
that another such client has started. It repeats this until it owns a
session or has joined a live one. That closes the window from the
client's point of view, with no protocol change. The third case comes
after the handshake, where `occamyd` cannot tell it from a session that
ends (see the Outcome). Calling `leave` before closing the guacd
connection narrows the window too, but it cannot close it. A joiner whose
handshake is in flight when the last established user closes still finds
the connection gone.

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
is a joiner whose handshake fails with 516 or EOF: it enters again, as
the owner of a new session or a joiner of a newer one. Every other joiner
failure is returned as it is.

## Testing Strategy

- The stress test above, under `-race`.
- A fallback test with the fake guacd: a joiner whose `select $id` gets
  516, or a closed socket, starts a new session as its owner, and the
  stale entry is gone. The fake answered unknown connections with 519;
  it now answers 516, as guacd 1.6 does.
- The pressure test in the e2e job, with the storm-claims check and the
  goroutine baseline.
- The e2e suite passing several times in a row with `checkSession` failing
  on any retry.

## Open Questions

Answered in the Outcome.

- Should the pressure test also check that guacd's process count returns
  to its baseline? The test would need access to the guacd container, or
  the workflow could check it in a step after the tests.
- Is N=10 the right CI budget, given how long the e2e job takes now?
- Whether the session key should include the password decides who shares
  a session. That belongs to [004](004-security-hardening.md).

## Acceptance Criteria

- [x] No connection can leave `proxy.mu` locked.
- [x] A failed connect does not stick its claims (defect 8): `started`
  removes the entry at once. The pressure test's storm-claims check
  covers it.
- [x] Concurrent `routeConn` on shared keys is `-race` clean and leaves the
  table empty.
- [x] A client that connects as the last user of its session leaves gets a
  working session without retrying (defect 6), when guacd refuses its
  join. A join that guacd accepts just before the last user leaves is
  still disconnected; see the Outcome.
- [x] The pressure test runs in the e2e job, and `occamyd`'s goroutines
  return to their baseline afterwards.
- [x] Debugging and measurement are documented with baseline numbers.
- [ ] #20 and #18 are closed, with a comment on what fixed each. #79 closes
  both when it merges; the comments follow the merge.

## Outcome

**Summary.** Implemented in
[#79](https://github.com/changkun/occamy/pull/79): `6d8844e` (join
fallback), `ddade16` (stress test), `b0fa380` and `54077f8` (end-to-end
and pressure tests), `fbe5fe6` and `d106222` (CI), `bfa3d2e` and
`3c1ee88` (docs). Drift: Moderate. All four components shipped. The join
fallback also covers guacd closing the socket and repeats until the
client has a live session, guacd's process count is a warning in CI
rather than a check, and memory comes from `docker stats` rather than
RSS. The body above is corrected for the fallback.

**What shipped.**

- `server/routers.go`:
  - `routeConn` runs the handshake through `connect`, which gives the
    owner's or a joiner's handshake its own timeout;
  - `ended` reports a join refused with status 516 or cut off with EOF;
  - a joiner whose handshake ended so calls `abandon`, which counts it out
    of the stale session and removes the entry if it is still the same
    session, and then enters again;
  - `statusResourceNotFound` (516) in `server/session.go`.
- `server/routers_test.go`:
  - the fake guacd answers joins to a connection that is not live with
    516, or with what a test sets in `unknown` (nil closes the socket),
    and records the claims each connection was created with;
  - `TestRouteConnJoinEndedSession`: `removed` (516) and `closing` (EOF)
    start a new session, which the stale session's last leave does not
    remove; `failed` (512) reaches the client as it is;
  - `TestRouteConnStress`: 40 clients, 10 rounds each, on 5 keys, 2 of
    which fail; each client must get a connection created for its own
    claims, or status 520, and the table must end empty.
- `tests/occamy_test.go`: `checkSession` keeps its retries and fails the
  test on any of them.
- `tests/pressure_test.go`: reads the goroutine count from
  `/debug/pprof/goroutine?debug=1` before the storm, waits up to a minute
  for it to come back within 5, and fails with the profile otherwise;
  then checks a session with the storm's claims and one with fresh
  claims. It logs the storm's wall time and how many sessions failed.
  The doc comment is current.
- `.github/workflows/dockerimage.yml`: the e2e job runs
  `make e2e OCCAMY_PRESSURE_N=10`, prints `docker stats` and guacd's
  processes before and after, and warns if guacd keeps more than one
  process 30 s after the tests.
- `docs/debugging.md`: debug mode and the pprof routes, goroutines,
  threads, CPU, memory and traces, guacd's processes and logs, the unit,
  parser, end-to-end and pressure tests, the protocol benchmark, and a
  pressure baseline. Linked from `docs/README.md` and the README, whose
  pressure paragraph is current. `(*proxy).profile`'s comment points to
  it.
- `CHANGELOG.md`: Added (pressure test in CI, debugging guide) and Fixed
  (joining an ending session).

**Validation.**

- `go test -race -count=20 ./server/` passes. With the fallback removed,
  `TestRouteConnJoinEndedSession/removed` and `/closing` fail (the client
  closes with 516 and 512), and `TestRouteConnStress` fails with clients
  closed with 516. With one retry instead of the loop,
  `TestRouteConnStress` failed in at least two of ten runs.
- The stress test makes about 330 handshakes with the fake guacd per run,
  about 260 of them joins, and logs about 145 fallbacks.
- CI run
  [40969282182](https://github.com/changkun/occamy/actions/runs/40969282182):
  the end-to-end tests passed at the first attempt; the storm ran 60
  sessions in 2.4 s with none failed; goroutines were 5 before and 5
  after; guacd was back to one process within 15 s.
  [docs/debugging.md](../docs/debugging.md) has the full baseline.
- CI run
  [40968488199](https://github.com/changkun/occamy/actions/runs/40968488199)
  passed every test, with 6 storm sessions failed, and failed the guacd
  process check (see Surprises).
- Before this change, 25 consecutive e2e jobs since the guacd table
  landed logged no `checkSession` retry, so failing on a retry does not
  make CI flaky.

**Design evolution.**

- *EOF as well as 516.* guacd 1.6.0 refuses a join to an ending
  connection in two ways. Once the connection's process has shut its fd
  socket (`guacd_proc_stop`, `src/guacd/proc.c`), `guacd_send_fd` fails,
  and `guacd_route_connection` logs "Unable to add user." and closes the
  joiner's socket with no error instruction
  (`src/guacd/connection.c`, `guacd_add_user`). Only after `waitpid`
  removes the connection from the map does a join get `No such
  connection.` with 516. The spec had only the second; the guacd-sessions
  Outcome had seen both under the storm.
- *Repeat, not once.* After dropping the stale entry, the client enters
  again. It becomes the owner of a new session, or a joiner of the one
  that another such client started, and that one can end as well when
  its owner's client leaves at once. The fallback repeats until the
  client owns a session or has joined a live one. Each pass drops a
  session that guacd has ended, and an owner never repeats. The stress
  test failed with one retry.
- *The goroutine wait comes first* in the pressure test, so that the
  session checks run once every storm session has ended and cannot join
  a straggler.
- *Memory from `docker stats`.* The image is distroless with no shell, so
  `occamyd`'s RSS cannot be read inside the container. `docker stats`
  reports the container's memory, which for one static process is close.
- *guacd's process count is a warning.* It was a failing check in the
  first push. See Surprises.

**Not implemented.**

- The third way a join races the last user: guacd's process takes the
  joiner just before the last user leaves. `guac_user_handle_connection`
  sends `ready` before `guac_client_add_user`, so the handshake succeeds,
  and the joiner is disconnected at once because the client has stopped.
  It happens in `serveIO`, after the handshake, where `occamyd` cannot
  tell it from a session that ends. Closing it would need `occamyd` to
  keep the last user's connection open until guacd has added the joiner,
  which guacd does not report. `checkSession` failing on any retry is the
  test that would show it; no CI run has.
- #20 and #18 are closed by the merge, and the comments on them follow
  it.

**Unspecified work.**

- `TestRouteConnJoinEndedSession/failed`: other join failures still
  reach the client (improvement).
- `internal/guacd`'s `TestHandshakeNoSuchConnection` uses 516, as guacd
  1.6 does (consistency).
- `docs/architecture.md` mentions the fallback (docs).
- [session-core](008-native-architecture/session-core.md)'s context no
  longer lists the race, the stress test and the pressure test as open,
  and names the case that remains (dependents).

**Decisions, surprises, follow-ups.**

- *guacd kept a VNC process.* In run 40968488199, 13 users joined a VNC
  connection before guacd had drawn its first frame. x11vnc had accepted
  the connection and sent the desktop size. None of the users got a
  frame, so six storm sessions timed out after 10 s. guacd dropped each
  user as not responding after 15 s, but never logged them as gone or
  the connection's last user, and the process was still there 30 s
  after the tests, with 36 threads in the container. `occamyd` had closed
  every session, and its goroutines were back at 5. It looks like a hang
  in guacd 1.6's VNC support when many users join a starting connection.
  A failing check on it would fail on correct code, so CI warns and
  prints guacd's log instead. Follow-up: reproduce it without `occamyd`
  and report it upstream.
- *guacd notices a user's close only when it writes to that user.*
  guacd's parent process copies each user's socket to the connection's
  process, and does not pass on a close from the client. A user of a
  connection that sends nothing is dropped only after 15 s. That is why
  the users above timed out instead of leaving.
- *Open questions.*
  - guacd's process count: printed by the workflow before and after the
    tests, and a warning if it stays above one.
  - N=10: the storm takes 2–10 s, bounded by the sessions' 10 s timeout,
    and the e2e job about 2 min in all. It stays.
  - The session key and the password stay with 004.
- `benchmark.md`'s harness spaces sessions 2 s apart because of defect 6.
  That is still right for the case that remains, so it is unchanged.
