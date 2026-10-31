# Debugging and measuring

This page covers how to look inside a running `occamyd` and the guacd
behind it, and the tests that measure them under load.
[benchmarks.md](./benchmarks.md) covers the protocol benchmark.

## Debug mode

This section is about v0 files such as [conf.yaml](../conf.yaml); a v1
file serves the same routes on a listener of its own (see
[the debug listener](#the-debug-listener-of-a-v1-file)).

`mode` in [conf.yaml](../conf.yaml) is `debug`, `release` or `test`;
a file without `mode` runs in debug mode, and any other value stops
`occamyd` at start. `conf.yaml` sets `debug`. The demo stack's
[docker/occamy.yaml](../docker/occamy.yaml) sets `release`, and
`make run OCCAMY_DEBUG=1` mounts
[docker/occamy-debug.yaml](../docker/occamy-debug.yaml), the same file
in debug mode, in its place; the stack publishes the v0 service on the
host's loopback interface, and `make run OCCAMY_DEBUG=1` refuses an
`OCCAMY_BIND` that would publish it on another address. In debug mode,
`occamyd` serves Go's profiling routes under `/debug/pprof/` on its own
address, with no token, and says so in its log at start.
Anyone who can reach `occamyd` can then read its profiles and its
command line. Set `mode: release` wherever others can reach it: release
mode does not register the routes.
[004](../specs/004-security-hardening.md) asks for release mode by
default and pprof on a separate listener. A v1 file has that listener
(below); a v0 file still defaults to debug mode.

## The debug listener of a v1 file

A v1 file has no `mode`. `occamyd` serves pprof under `/debug/pprof/`
and expvar under `/debug/vars` only on the debug listener, at
`debug.address`, and opens that listener only when the key is set
([configuration](./configuration.md)). The public listener never serves
them. The listener has no authentication, so keep it on the loopback
interface, such as `localhost:6060`; `occamyd` warns at start when it is
not. It needs a port of its own: `occamyd` refuses a `debug.address` on
`listen`'s port where the two could take the same connections, such as
`localhost:5636` beside `127.0.0.1:5636`, or any host beside `:5636`.
A CPU profile or a trace still running when `occamyd` stops is cut after
the 5 second drain; a second SIGINT or SIGTERM stops `occamyd` at once.
The examples below work against it with that address in place of
`localhost:5636`. `/debug/vars` shows the command line, the runtime's
memory statistics, the number of goroutines and the open file
descriptors as JSON. `debug.address_env` names an environment variable
that holds the address instead, and leaves the listener off while it is
unset or empty. The demo stack's `occamy-v1` takes its address from
`OCCAMY_V1_DEBUG_ADDRESS`, which `make run OCCAMY_DEBUG=1` sets, and then
serves its debug listener on the host's `localhost:5638`
(`OCCAMY_V1_DEBUG_PORT`), for the soak. The stack's other services can
reach both debug routes while they are on; `make run` without
`OCCAMY_DEBUG=1` turns them off again. `make run` and `make bench` set
`OCCAMY_V0_CONF` and `OCCAMY_V1_DEBUG_ADDRESS` either way, so that the
same variables left in the shell or in `docker/.env` do not turn the
routes on; `compose up` run by hand reads them from there, and with
neither set leaves the routes off.

## Profiling occamyd

The examples use the demo stack's address, `localhost:5636`, which
serves them after `make run OCCAMY_DEBUG=1`.

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

guacd notices that a user has closed its connection only when it next
sends that user something. Until then the user still counts, for up to
15 s, after which guacd logs `User is not responding.` and drops it.

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
  `TestE2EV1RDP` also takes the demo xrdp server's `rdp-native` through
  the clipboard both ways, which the image's clipboard echo answers, a
  resize, a viewer joined by a share, and the owner leaving and coming
  back.
  `TestVNCParity`, among them, runs the guacd bridge's parity matrix for
  VNC: each feature on x11vnc and TigerVNC, through the native backend
  and through guacd. The demo desktops report what reached the server
  over the session itself: a probe puts each line typed at the top left
  on the clipboard in hex, and another echoes clipboard text that starts
  `owner: ` as `echo: `. The test first warms each server port, since a
  freshly started x11vnc drops the first clipboard text on its port, and
  logs how many tries each port took. It logs the matrix as a Markdown
  table, and writes it to the file `OCCAMY_PARITY_MATRIX` names. A cell
  expected to fail is listed in `parityKnown`, with the reason and a
  part of the error that only that failure gives. Any other failure
  fails the test, and so does a listed cell that fails differently or
  passes. A cell tries up to three times. A guacd cell tries again
  after any failure other than its listed one. A native cell that
  cannot reach the desktop runs the same check through guacd at once,
  and tries again, 15 s later, only if guacd cannot reach the desktop
  either. If guacd reaches it, the native failure stands, and so does
  any other native failure. A cell that settles at a later try shows
  the try, as in `pass (try 2)`. `TestParityMatrixDoc`, which needs no
  stack, checks that the matrix in the guacd bridge's spec is the one
  `parityFeatures` and `parityKnown` give. The demo x11vnc runs under a
  watchdog, which logs each restart as `[<port>] watchdog: …`, after
  the X error that caused it, if any (`podman compose logs vnc`).
- `make soak` runs `TestSoak` against the demo stack's `occamy-v1`,
  which `make run OCCAMY_DEBUG=1` started with its debug listener:
  `OCCAMY_SOAK_SESSIONS` sessions, 8 by default, to TigerVNC through the
  native backend (`OCCAMY_SOAK_CONNECTIONS`, `tigervnc-native` by
  default, since x11vnc's watchdog restarts would end sessions) for
  `OCCAMY_SOAK_DURATION`, 1 hour by default. Each session moves the
  pointer, below the typing probe, and types every 2 s, asks its desktop for a terminal that scrolls for 2 s
  once a minute, and closes after `OCCAMY_SOAK_LIFETIME`, 10 minutes by
  default, for another to open; every other session has a read-only
  viewer. Every 30 s the test samples `occamyd`'s goroutines, live heap
  (after a collection) and open files from the debug listener, trying a
  failed sample twice more, with the host's load average, and logs
  them, and writes them as CSV to the file `OCCAMY_SOAK_OUT` names.
  After a warm-up of a tenth of the run, it fails if the median of a
  metric's last quarter of samples exceeds the first quarter's by more
  than its bound, whatever the quarters between do
  (`OCCAMY_SOAK_BOUNDS`, `goroutines=50,heap=32,fds=25` by default, the
  heap in MiB), if a metric stays above its start by more than the
  bound a minute after the last session closed, if fewer than twelve
  samples follow the warm-up, or if a session fails. `tests/soak_test.go` lists the
  variables. The full soak is
  `make soak OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h`.
- `make e2e OCCAMY_PRESSURE_N=10` adds the pressure test, which counts
  `occamyd`'s goroutines from the v0 service's pprof routes, served
  after `make run OCCAMY_DEBUG=1`. Each of its N rounds opens two
  sessions to each demo server at once: one that waits for the desktop
  and one that sends garbage. All sessions to one server have the same
  claims, so they share a guacd connection, and the storm mostly
  exercises joining and leaving. Sessions may fail under the load.
  Afterwards, `occamyd`'s goroutines must return to their
  number before the storm within a minute, or the test fails and prints
  the goroutine profile. Then a session with the storm's own claims and
  one with new claims must work.
- `make bench` runs the protocol benchmark, described in
  [benchmarks.md](./benchmarks.md).

CI runs the unit tests with `-race`, and the end-to-end and pressure
tests with 10 rounds. It prints `docker stats` and guacd's processes
before and after them, and warns if guacd still runs more than its one
process 30 s after the tests.

## Pressure baseline

From CI run
[40969282182](https://github.com/changkun/occamy/actions/runs/40969282182),
on GitHub's `ubuntu-latest` runner (4 CPUs, Docker), with
`OCCAMY_PRESSURE_N=10`. The goroutines are counted around the storm;
the rest around the whole end-to-end step, which runs the storm after
the other end-to-end tests.

| | Before | After |
|---|---|---|
| Storm | | 60 sessions in 2.4 s, none failed |
| `occamyd` goroutines | 5 | 5, at once |
| `occamyd` memory (`docker stats`) | 2.6 MiB | 10.3 MiB |
| `occamyd` threads (`PIDS`) | 9 | 12 |
| guacd processes | 1 | 1, within 15 s |
| guacd threads (`PIDS`) | 1 | 7 |

The Go runtime keeps idle OS threads and returns freed memory to the
system gradually, so neither number drops back at once. The goroutine
count is what shows a leak.

In the first CI run of the test
([40968488199](https://github.com/changkun/occamy/actions/runs/40968488199)),
13 users joined a VNC connection before guacd had drawn its first frame,
and none of them ever got one. Six storm sessions timed out after 10 s,
and the storm took 10.1 s. guacd dropped each of those users as not
responding after 15 s, but never logged them as gone, and the
connection's process was still there 30 s after the tests. `occamyd`
had closed every session, and its goroutines were back at 5. It looks
like a hang in guacd 1.6, which is why CI only warns about a leftover
guacd process.
