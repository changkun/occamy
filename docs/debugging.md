# Debugging and monitoring

This page shows how to watch a running `occamyd` and find out what went
wrong. It covers the health check, the log, the debug listener and its
profiles, and the problems you are most likely to meet.

## The health check

`occamyd` answers `GET /healthz` on its public listener while it runs.
The check takes no token.

```sh
curl -s http://localhost:5636/healthz    # {"status":"ok"}
```

It is a liveness check. It tells you that `occamyd` serves requests, not
that the remote machines can be reached. The image has no shell, so
probe it from outside, as a Kubernetes probe does:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 5636
```

When `occamyd` terminates TLS, add `scheme: HTTPS`. Each check adds a
request line to the log.

## The log

`occamyd` writes its log to standard error. In a container,
`podman logs` or `docker logs` shows it. Each line starts with the date
and time, the source file and line, and `occamy:`.

```
2026/11/04 10:15:02 server.go:135: occamy: serving API v1 at http://[::]:5636
2026/11/04 10:15:40 signin.go:131: occamy: user "alice" signed in, idp local
2026/11/04 10:15:40 middleware.go:41: occamy: "POST" "/api/v1/auth/token" 200 61.234ms 10.0.0.7:51544
2026/11/04 10:15:41 sessions.go:406: occamy: session kX2v9QeT0bLmR4sWzN8yPg: user "alice" opened connection "build-box" through ssh
2026/11/04 10:42:13 session.go:483: occamy: INFO session ended session=kX2v9QeT0bLmR4sWzN8yPg reason=closed err=<nil>
```

Some lines carry a level, `INFO`, `WARN` or `ERROR`, after `occamy:`,
and their details as `key=value`. `occamyd` writes no debug lines, and
the level cannot be changed.

### What the lines say

| Line | When |
|------|------|
| `serving API v1 at http://[::]:5636` | At start, with the public listener's address |
| `serving expvar and pprof at http://127.0.0.1:6060/debug/` | At start, when the debug listener is on |
| `TCP congestion control: bbr` | At start, on Linux |
| `OIDC provider https://idp.example.com discovered` | With OIDC, once the provider has answered |
| `"GET" "/api/v1/connections" 200 1.234ms 10.0.0.7:51544` | After each request: method, path, status, duration and the client's address |
| `user "alice" signed in, idp local` | A sign-in with a password. Through OIDC, the line ends `idp oidc` |
| `sign-in failed from 10.0.0.7:51544` | A wrong user name or password |
| `OIDC sign-in failed from 10.0.0.7:51544: …` | An OIDC sign-in failed, with the reason |
| `session <id>: user "alice" opened connection "build-box" through rdp` | A session opened. An ad-hoc target shows as `ad-hoc target "vnc://10.0.0.5:5900"` |
| `user "alice" opening connection "build-box": …` | A session could not open, with the reason |
| `session <id>: shared with role viewer for 1h0m0s` | The owner made a share link |
| `session <id>: closed by its owner` | The owner ended the session |
| `INFO session ended session=… reason=… err=…` | A session ended, for the reason below. `session=` is the ID that the session's opening line names |
| `stream refused from 10.0.0.7:51544: origin not allowed` | A stream was refused before it joined a session |
| `INFO viewer dropped viewer=2 err=…` | A viewer acknowledged no frame for `streams.viewer_timeout`, 30 s by default, and `occamyd` disconnected it |
| `INFO client closed code=… reason=…` | A client closed its stream because of a fault |

The request line never holds the query string, the headers or the body,
so tokens and passwords stay out of the log. A failed sign-in does not
log the user name either, since people type passwords into it. The
client's address is the one `occamyd` sees, which behind a reverse proxy
is the proxy's. A stream's request line appears when the stream ends,
with the whole time it was open.

A session ends for one of these reasons:

| `reason=` | Why |
|-----------|-----|
| `closed` | The owner ended it |
| `owner left` | The owner left and did not come back within a minute |
| `idle` | Nobody gave input for 30 minutes |
| `remote ended` | The remote ended it, as when its user logs out |
| `failed` | The backend could not connect, or lost the remote. `err=` says why |
| `shutdown` | `occamyd` is stopping |
| `internal error` | `occamyd` failed on its own. An `ERROR session panicked` line before it has the stack |

When `occamyd` stops on SIGINT or SIGTERM, it logs
`received terminated; another one stops occamyd at once` and
`shutting down: draining requests for up to 5s`, then
`occamy is down, good bye!`. A request still running after the drain,
such as a long CPU profile, is cut with a warning.

## The debug listener

`occamyd` can serve Go's runtime statistics and profiles on a second
listener. Turn it on with `debug.address` in the configuration:

```yaml
debug:
  address: localhost:6060
```

At start `occamyd` logs
`serving expvar and pprof at http://127.0.0.1:6060/debug/`. Without
`debug.address`, these routes are served nowhere. The public listener
never serves them.

The debug listener has no authentication, so keep it on the loopback
interface. `occamyd` warns at start when it is not. Give it a port of
its own. `occamyd` refuses a `debug.address` on `listen`'s port when the
two could take the same connections: the same host, `localhost` beside
`127.0.0.1` or `::1`, or either one on every address, such as `:5636`.

To turn the listener on without editing the file, name an environment
variable that holds the address instead:

```yaml
debug:
  address_env: OCCAMY_DEBUG_ADDRESS
```

While the variable is unset or empty, the listener stays off, and
`occamyd` logs `debug listener off:` with the variable's name.

In a container, a published port reaches the container's own address,
not its loopback interface. Listen on `0.0.0.0:6060` there, and publish
the port on the host's loopback interface only, as in
`-p 127.0.0.1:6060:6060`. The warning at start is then expected. In
Kubernetes, keep `localhost:6060` and reach it with
`kubectl port-forward`.

The demo stack of [Try Occamy](try-it.md) serves its debug listener at
`localhost:5638` after `make run OCCAMY_DEBUG=1`.

### `/debug/vars`

`/debug/vars` returns one JSON object:

| Field | What it holds |
|-------|---------------|
| `cmdline` | `occamyd`'s command line |
| `memstats` | Go's memory statistics, such as `HeapInuse`, `Sys` and `NumGC` |
| `goroutines` | The number of goroutines |
| `fds` | The open file descriptors on Linux and macOS, or -1 where `occamyd` cannot count them |
| `tcp_congestion` | The TCP congestion control of the public listener's connections, such as `bbr`, or empty where `occamyd` cannot tell, as off Linux |

To watch the numbers that matter most:

```sh
curl -s localhost:6060/debug/vars | jq '{goroutines, fds, tcp_congestion, heap: .memstats.HeapInuse}'
```

## Profiling occamyd

The debug listener serves Go's profiles under `/debug/pprof/`.
The [HTTP API](api.md#the-debug-listener) lists each route.

| Route | What it shows |
|-------|---------------|
| `/debug/pprof/` | An index of the profiles |
| `/debug/pprof/goroutine` | Every goroutine and its stack |
| `/debug/pprof/heap` | Memory in use, by allocation site |
| `/debug/pprof/allocs` | Every allocation since start |
| `/debug/pprof/profile` | CPU, sampled for `seconds` (30 by default) |
| `/debug/pprof/trace` | An execution trace, for `seconds` (1 by default) |
| `/debug/pprof/threadcreate` | The stacks that created OS threads |
| `/debug/pprof/block`, `/debug/pprof/mutex` | Blocking and lock contention. Both are empty, since `occamyd` does not record them |

### Goroutines

```sh
# The first line is the total, then one entry for each distinct stack.
curl -s 'localhost:6060/debug/pprof/goroutine?debug=1' | head -1
# Every goroutine with its full stack and how long it has waited.
curl -s 'localhost:6060/debug/pprof/goroutine?debug=2'
```

An idle `occamyd` runs a handful of goroutines. Each session adds
several, for its backend and for each viewer's stream. Each idle
keep-alive HTTP connection adds one. Once every session has ended, the
count is back where it was. A count that keeps growing is a leak.
`?debug=1` groups the goroutines by stack, so the stack that grows is
the one to look at.

### CPU and memory

```sh
# Sample the CPU for 30 s while sessions run, then open pprof's web UI.
go tool pprof -http=:8080 'http://localhost:6060/debug/pprof/profile?seconds=30'
# Memory in use now.
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/heap
# A 5 s execution trace: scheduling, network waits and garbage collection.
curl -o trace.out 'localhost:6060/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

### Threads

Go runs goroutines on a small pool of OS threads, so the number of
threads does not follow the number of sessions. The total in
`threadcreate?debug=1` counts the threads created since start, and the
runtime seldom ends one. For the number running now, look at the
process. On Linux, `grep Threads /proc/$(pidof occamyd)/status` shows
it. In a container, the `PIDS` column of `podman stats` or
`docker stats` does.

## Common problems

### occamyd does not start

`occamyd` checks the whole configuration before it serves. It stops
with every problem it found, each with its line and key:

```
config: 2 problems:
	line 7: conections: unknown key, did you mean connections?
	line 31: debug.address: "localhost:5636" shares a port with listen ":5636": give the debug listener a port of its own, since whatever reaches the one could reach the other
```

[Configuration](configuration.md) describes each key. Other reasons it
stops at start:

| It says | What to do |
|---------|------------|
| `cannot open given config file` | Check the path. `-conf` names the file, `./conf.yaml` by default |
| `auth: signing key …: occamyd runs as uid 65532, which may not use …` | Give the key's directory, or the volume mounted on it, to that user. [Configuration](configuration.md#sign-in-auth) has the step for a host, Docker and podman |
| `listen tcp :5636: bind: address already in use` | Another process has the port. Stop it, or change `listen` |
| `tls: …` | `tls.cert` or `tls.key` does not load. Check that the two files are PEM and belong together |

### Warnings at start

Each starts with `warning:`.

| Warning | What to do |
|---------|------------|
| `created a new signing key in …; tokens signed with any earlier key no longer verify` | Expected at the first start. On a server that ran before, the key's volume is missing, and everyone has to sign in again. Keep the key on a persistent volume |
| `the debug listener on … serves profiles and the command line without authentication beyond the loopback interface` | Move `debug.address` to `localhost`. In a container, publish the port on the host's loopback interface only, and the warning is expected |
| `congestion_control bbr: the kernel has not loaded it: load its module, tcp_bbr; connections use cubic` | Load the module, with `sudo modprobe tcp_bbr`, on the host or the node. See [TCP congestion control](configuration.md#tcp-congestion-control) |
| `congestion_control bbr: the kernel allows it only to privileged processes: …` | Add it to the `net.ipv4.tcp_allowed_congestion_control` sysctl |
| `web_client: …; /static/ serves no web client until it is built there (make client)` | Build the web client into that directory, or point `web_client` at the `dist` directory of its release archive |
| `the static users' password hashes have different bcrypt costs, …` | Hash every user's password at the cost the warning names. Otherwise how long a sign-in takes tells which users exist |
| `connection "…": security: tls may send the password inside TLS whose certificate tls.insecure_skip_verify accepts unchecked, …` | Pin the server's certificate with `tls.pins` in place of `insecure_skip_verify` |
| `OIDC discovery at … failed, retried on the next sign-in after 10s: …` | `occamyd` cannot reach the provider, and OIDC sign-ins fail until it can. Check `auth.oidc.issuer` and the network to it |
| `adhoc_allowlist: not every address of occamyd's listeners is known, so a range may open it: …` | `occamyd` could not resolve the host of `listen` or `debug.address`, so an ad-hoc target in a range of `adhoc_allowlist` may reach `occamyd` itself. Use IP addresses there, or keep them out of the ranges |

### A session fails

When a session fails, its `session ended … reason=failed` line carries the
error. Some errors name the setting that fixes them:

| The error says | What to do |
|----------------|------------|
| `the server's certificate "…" (SHA-256 AB:CD:…) is not trusted` | Add the fingerprint to the connection's `tls.pins`, or give the issuing CA in `tls.ca_file` |
| `set ard: true on connection "…" to allow it` | The Mac offers only Apple Remote Desktop sign-in. Read [the risk](configuration.md#apple-remote-desktop-ard) before you set `ard: true` |
| `allow TLS with security: tls on connection "…"` | The RDP server speaks no NLA, as xrdp does. Set `security: tls`, and pin its certificate |

A session that could not open at all logs `user "…" opening …:` with
the reason. `too many sessions open` means the user already owns
`limits.sessions_per_user` sessions. `occamyd runs as many sessions as
it takes` means `limits.sessions` is reached.

### Warnings during a session

| Warning | What it means |
|---------|---------------|
| `WARN ssh: accepting an unverified host key` | The connection sets `insecure_ignore_host_key`, or the user accepted any host key for an ad-hoc target. Pin the logged fingerprint in `host_keys` |
| `WARN vnc: accepting an unverified certificate`, and the same for rdp and kubernetes | The connection sets `tls.insecure_skip_verify`. Pin the logged fingerprint in `tls.pins` |
| `WARN vnc: logged in with Apple Remote Desktop authentication, …` | The session runs unencrypted, as `ard: true` allows |
| `WARN rdp: the server draws with GFX that the client does not decode yet, …` | Parts of the screen may stay missing or stale, as on a Windows server. Set `disable_gfx: true` on the connection |
| `WARN rdp: the server's clipboard has not taken the owner's text, …` | The RDP server's clipboard does not answer. The text waits until it does |
| `WARN vnc: the server prohibits resizing the screen; …` | The VNC server keeps its own screen size |

### Streams refused

`stream refused from …:` names the reason. `origin not allowed` means
the web page that opened the stream is not on `occamyd`'s origin. Behind
a reverse proxy that rewrites the `Host` header, list the page's origin
in `origins`. `unsupported protocol version` means the client speaks a
version of the protocol that `occamyd` does not, so update the client.

## Tests and benchmarks

The pressure test and the soak read the debug listener to check that
`occamyd` does not leak.
[Development](development.md#pressure-test-and-soak) describes them.
