# Debugging and measuring

This page covers how to look inside a running `occamyd` and the guacd
behind it, and the tests that measure them under load.
[benchmarks.md](./benchmarks.md) covers the protocol benchmark.

## Debug mode

`mode` in [conf.yaml](../conf.yaml) is `debug`, `release` or `test`.
`conf.yaml` and the demo stack's [docker/occamy.yaml](../docker/occamy.yaml)
set `debug`. In debug mode, `occamyd` serves Go's profiling routes under
`/debug/pprof/` on its own address, with no token.
Anyone who can reach `occamyd` can then read its profiles and its
command line. Set `mode: release` wherever others can reach it: release
mode does not register the routes.
[004](../specs/004-security-hardening.md) plans to make release the
default and move pprof to a separate listener.

## Profiling occamyd

The examples use the demo stack's address, `localhost:5636`.

| Route | What it shows |
|-------|---------------|
| `/debug/pprof/` | An index of the profiles |
| `/debug/pprof/goroutine` | Every goroutine and its stack |
| `/debug/pprof/heap` | Memory in use, by allocation site |
| `/debug/pprof/allocs` | Every allocation since start |
| `/debug/pprof/profile` | CPU, sampled for `seconds` (default 30) |
| `/debug/pprof/trace` | An execution trace, for `seconds` (default 1) |
| `/debug/pprof/threadcreate` | The stacks that created OS threads |
| `/debug/pprof/block`, `/debug/pprof/mutex` | Blocking and lock contention; empty, since `occamyd` does not turn them on |

Goroutines:

```sh
# The first line is the total, then one entry for each distinct stack.
curl -s 'localhost:5636/debug/pprof/goroutine?debug=1' | head -1
# Every goroutine with its full stack and how long it has waited.
curl -s 'localhost:5636/debug/pprof/goroutine?debug=2'
```

An idle `occamyd` runs a handful of goroutines. Each connected client
adds three: the HTTP handler that runs its session, and one for each
direction of the proxy loop. Each idle keep-alive HTTP connection adds
one. Once every client has left, the count is back where it was.

CPU and memory:

```sh
# Sample the CPU for 30 s while sessions run, then open pprof's web UI.
go tool pprof -http=:8080 'http://localhost:5636/debug/pprof/profile?seconds=30'
# Memory in use now.
go tool pprof -http=:8080 http://localhost:5636/debug/pprof/heap
# A 5 s execution trace: scheduling, network waits and GC.
curl -o trace.out 'localhost:5636/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

Go runs goroutines on a small pool of OS threads, so the number of
threads does not follow the number of sessions. The total in
`threadcreate?debug=1` counts the threads created since start, which the
runtime seldom ends. The current number is the `PIDS` column of
`podman stats` for the `occamy` container.

## guacd in the demo stack

guacd forks a process for each connection, and the process exits after
the connection's last user has left. With no sessions, guacd runs a
single process. The stack's containers are named `docker-<service>-1`:

```sh
podman stats --no-stream        # CPU, memory and PIDs of each container
podman top docker-guacd-1       # guacd's processes
cd docker && podman compose logs -f guacd occamy
```

Use `docker` in place of `podman` with Docker. guacd logs each step of
a connection: `Creating new client for protocol`, `Joining existing
connection`, `Last user of connection ... disconnected` and `Connection
... removed`. `occamyd` logs `session $id: owner joined`, `joiner
joined` and `left`. It logs `ended while joining` when a client joins a
session just as guacd ends it, and then starts a new session for that
client.

## Tests and benchmarks

- `go test -race ./...` runs the unit tests. `TestRouteConnStress` in
  `server/` sends many clients through the session table at once,
  against a fake guacd.
- `go test -run '^$' -bench . -benchmem ./internal/guacamole/` runs the
  benchmarks of the instruction parser and of `ReadRaw`, which frames
  instructions on a stream.
- `make e2e` runs the end-to-end tests against the demo stack
  (`make build run` first). They open a session to each demo server and
  check that the remote desktop arrives at the first attempt.
- `make e2e OCCAMY_PRESSURE_N=10` adds the pressure test. Each of its N
  rounds opens two sessions to each demo server at once: one that waits
  for the desktop and one that sends garbage. Sessions may fail under
  the load. Afterwards, `occamyd`'s goroutines must return to their
  number before the storm within a minute, or the test fails and prints
  the goroutine profile. Then a session with the storm's own claims and
  one with new claims must work.
- `make bench` runs the protocol benchmark, described in
  [benchmarks.md](./benchmarks.md).

CI runs the unit tests with `-race`, and the end-to-end and pressure
tests with 10 rounds. After them it checks that guacd is down to its one
process, and prints `docker stats` before and after.
