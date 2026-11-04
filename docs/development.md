# Development

This page covers building Occamy from source and running each of its
test suites. [CONTRIBUTING.md](../CONTRIBUTING.md) covers how to send a
change. Run each command from the repository's root unless it says
otherwise.

## What you need

| What | Version | For |
|------|---------|-----|
| [Go](https://go.dev/dl/) | 1.26.4 or later, as `go.mod` says | `occamyd` and the Go tests |
| A C compiler, such as gcc | | The race detector, `-race`, on Linux and Windows. macOS needs none |
| [Podman](https://podman.io) with a compose provider, or Docker with Compose v2 | | The image, the demo stack, the end-to-end and integration tests |
| [Node.js](https://nodejs.org) with npm | 22.13 or later on 22, 24, or 26 and later | The web client |
| [golangci-lint](https://golangci-lint.run) | v2 (CI runs v2.15.0) | `make lint` |
| Chrome | | One end-to-end test, which skips without it |
| Xcode | 26 or later | Occamy for macOS |

`make` runs Podman unless `CONTAINER_ENGINE` names another engine. Give
every `make` command the same engine, which exporting it does:

```sh
export CONTAINER_ENGINE=docker
```

The integration tests read `OCCAMY_CONTAINER_ENGINE` instead.

## Building

```sh
make compile    # ./occamyd
make client     # the web client, in client/occamy-web/dist
make build      # the image occamy:latest
```

`make compile` runs `CGO_ENABLED=0 go build -trimpath`, so `occamyd`
needs no C toolchain. `make client` runs `npm ci` and `npm run build` in
`client/occamy-web`. `make build` builds both again inside the image
([docker/Dockerfile](../docker/Dockerfile)), and tags it `occamy:latest`
and `occamy:<version>`, where the version is `git describe`'s.
`OCCAMY_IMAGE` names another image, as on a machine that others share
([Next to another stack](try-it.md#next-to-another-stack)).

`./occamyd` has one flag, `-conf`, the configuration file, which is
`./conf.yaml` by default. A configuration's `web_client` names the web
client's directory, such as `client/occamy-web/dist`
([Configuration](configuration.md)).

[Occamy for macOS](#occamy-for-macos), below, covers the Mac app.

## The web client

For work on the web client, run Vite's dev server, which reloads the
page as you edit:

```sh
make build && make run      # an occamyd on localhost:5637
cd client/occamy-web
npm ci
npm run dev
```

The dev server passes `/api` and its WebSockets on to the `occamyd` at
`http://localhost:5637`, or at the URL in `OCCAMY_DEV_SERVER`. Open the
address it prints, and sign in as `demo` with the password `occamy`.

The web client's checks run in `client/occamy-web`, as CI runs them.
The interop tests run the TypeScript client against `occamyd`'s stream
code, served by `client/internal/webinterop`, built with the race
detector:

```sh
go build -race -o "${TMPDIR:-/tmp}/occamy-webinterop" ./client/internal/webinterop
cd client/occamy-web
npm ci
npm run lint
npm run typecheck
OCCAMY_INTEROP_SERVER="${TMPDIR:-/tmp}/occamy-webinterop" GORACE=halt_on_error=1 npm test
npm run build
cd ../..
```

Without a C compiler on Linux or Windows, leave `-race` out of the
`go build`. Without `OCCAMY_INTEROP_SERVER`, `npm test` skips the
interop tests. With it, `npm test` still reports two tests skipped.
They check that CI has the interop server, and run only in CI.

## Unit tests

```sh
go test -race ./...
```

The unit tests need no stack and no container engine, and take a few
minutes with the race detector. The end-to-end tests in `tests/` skip
unless `make e2e` points them at a stack. On Linux and Windows the race
detector needs a C compiler. Without one, `go test -race` stops with
`-race requires cgo` or `C compiler "gcc" not found`, and
`go test ./...` runs the same tests without it.

CI also runs `go vet ./...`. The codec measurement, `bench/codecs`, is
a module of its own, so run its tests from its directory:

```sh
(cd bench/codecs && go test ./...)
```

## Lint

```sh
golangci-lint version    # must say version 2
make lint
```

`make lint` runs golangci-lint with [.golangci.yml](../.golangci.yml),
then again on `bench/codecs` with its `measure` build tag.
golangci-lint v1 cannot read the configuration. The configuration also
checks that code is formatted with `gofmt`, and its imports grouped by
`goimports`.

CI has one more pass, on the RDP integration tests, which build only
under their tag:

```sh
golangci-lint run --build-tags=rdp_integration ./internal/rdp/... ./internal/backend/rdp/...
```

## End-to-end tests

The end-to-end tests run on the demo stack with the tests' own
configuration, [docker/occamy-v1-e2e.yaml](../docker/occamy-v1-e2e.yaml),
which [docker/docker-compose.e2e.yaml](../docker/docker-compose.e2e.yaml)
mounts in place of the demo's:

```sh
make build
make run E2E=1
make e2e
```

`make run E2E=1` starts that stack, or switches a running demo stack to
it. `make e2e` runs `go test ./tests/` against it. Give `make e2e` the
engine that `make run` used: some tests stop processes in the `rdp` and
`vnc` containers, or recreate `occamy-v1`, through that engine's
compose. `make run` without `E2E=1` returns to the demo's
configuration, and `make stop` stops either.

The tests open real sessions through `occamy-v1`, with the Go client of
protocol v1:

- `TestDemoDesktop`: on both demo desktops, XFCE draws its panel and
  wallpaper, and the panel follows the screen when it grows.
- `TestE2EV1RDP`: on `rdp`, the clipboard both ways, a resize, a viewer
  joined by a share, the owner leaving and coming back, and the
  clipboard again after its process in the `rdp` container is killed.
- `TestE2ETokenSurvivesRestart`: an access token still works after
  `occamy-v1` is recreated, since the signing key is on a volume.
- `TestX11vncClipboardRequestorGone`: x11vnc survives a clipboard
  request from a window that has gone.
- `TestVNCParity`: each VNC feature on x11vnc and on TigerVNC, logged as
  a table, and written to the file that `OCCAMY_PARITY_MATRIX` names.
- `TestWebScreen`: in Chrome, on `tigervnc` and `rdp`, the web client
  asks for the window's size in CSS pixels at device pixel ratios of 2,
  1 and 1.5, when the session opens and when the window changes. It
  finds Chrome on the host, or at `OCCAMY_E2E_CHROME`. Without Chrome it
  skips, except in CI.

A run takes a few minutes on an idle host, most of them in the parity
matrix. To run one test, such as the owner's return:

```sh
OCCAMY_E2E_V1_ENDPOINT=localhost:5637 go test -count=1 -v -run 'TestE2EV1RDP/reconnect' ./tests/
```

When one fails, the services' logs usually say why, such as
`(cd docker && podman compose logs rdp)`. If the RDP clipboard check
times out and the `rdp` log shows `error in trans_connect chan`, xrdp
found no channel server, the process that carries the clipboard, for
the session's display. The image starts one again on the next
connection, and logs `starting xrdp-chansrv again`.

### The test servers

Under `make run E2E=1`, **Connections** in the web client lists 14
connections: the demo's three, then the servers that the tests probe.
These show bare X desktops rather than a desktop to work in.

| Connection | What you see |
|------------|--------------|
| **x11vnc** | A black desktop with two xterms and no window manager. The small one at the top left is a probe: lines typed there reach the clipboard as hex. x11vnc keeps its 1024×768 screen, and the page scales it to the window. Keys go to the window under the pointer |
| **TigerVNC** | The same desktop, served by TigerVNC, which resizes its screen to the window |
| **RDP** | xrdp's X session: one xterm with the prompt `occamy@…:~$`. It resizes to the window. The first connection after the stack starts creates the X session, so wait a few seconds before you type |

The other connections reach the same servers with no password, over
VeNCrypt, and with the German and French keyboard layouts.
**TigerVNC, asking for the password** asks for it first: it is
`vncpassword`.

The xterms have `xclip`. With the pointer over one,
`echo hello | xclip -selection clipboard` puts text on the remote
clipboard, and `xclip -o -selection clipboard; echo` prints it. The
test servers also answer the clipboard by themselves. On **x11vnc** and
**TigerVNC**, send `owner: hi` from the **Clipboard** panel, and the
remote clipboard soon holds `echo: hi`. **RDP** answers
`occamy-e2e:hi` with `echo:occamy-e2e:hi`. x11vnc ignores clipboard
text from the browser until its latest client has been connected for 45
seconds, so on a fresh connection, wait and send it again.

## Debug routes

The demo stack's debug listener has no authentication, so it is off
unless `make run` gets `OCCAMY_DEBUG=1`. It then serves expvar and pprof
on `localhost:5638`, on the loopback interface only, whatever
`OCCAMY_BIND` says:

```sh
make run OCCAMY_DEBUG=1
curl -s localhost:5638/debug/vars | head -c 200; echo
curl -s -o /dev/null -w '%{http_code}\n' localhost:5638/debug/pprof/    # 200
```

`make run` without `OCCAMY_DEBUG=1` turns it off again, whatever the
shell or `docker/.env` holds, and `occamy-v1` logs `debug listener off`.
`go test -run TestDemoDebugGating ./internal/config` checks that the
demo's files gate it this way. [Debugging](debugging.md) covers
profiling `occamyd` through it.

## Pressure test and soak

Both read `occamyd`'s figures from the debug listener, so start the
stack with the tests' configuration and the listener on:

```sh
make run E2E=1 OCCAMY_DEBUG=1
```

The pressure test runs with the end-to-end tests when
`OCCAMY_PRESSURE_N` gives its number of rounds:

```sh
make e2e OCCAMY_PRESSURE_N=10
```

Each round opens two sessions at once to each of `ssh`, `vnc` and
`rdp`, one of which sends garbage once it is welcomed. Afterwards, `occamyd`'s
goroutines must return to their number before the rounds within a
minute. CI runs it with 10 rounds.

The soak runs sessions for a long time while it samples `occamyd`'s
goroutines, heap and open files, and fails if they keep growing or a
session fails. A short one:

```sh
make soak OCCAMY_SOAK_DURATION=5m OCCAMY_SOAK_INTERVAL=10s OCCAMY_SOAK_LIFETIME=2m
```

This runs 8 sessions to `tigervnc` for 5 minutes, each typing and
moving the pointer, replaces each after 2 minutes, and samples every 10
seconds. Plain `make soak` runs for an hour, and the full soak is:

```sh
make soak OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h
```

After a warm-up of a tenth of the run, the soak fails if the median of
a figure's last quarter of samples exceeds its first quarter's by more
than its bound, if a figure is not back within its bound of where it
started a minute after the last session closed, or if a session fails.
`OCCAMY_SOAK_BOUNDS` sets the bounds, `goroutines=50,heap=32,fds=25` by
default, the heap in MiB. `tests/soak_test.go` lists every variable.

## Integration tests

These run a backend against a real server in a container of its own,
and need no demo stack. Build tags keep them out of `go test ./...`.
Each run starts its own containers, and builds its own images, and
removes them when it ends. An image build can take longer than
`go test`'s default 10 minutes, hence `-timeout`.

| Tag | Command | Against |
|-----|---------|---------|
| `rdp_integration` | `go test -tags rdp_integration -run Integration -timeout 30m ./internal/rdp` | The RDP client, against xrdp in the bench desktop image |
| `rdp_integration` | `go test -tags rdp_integration -run Integration -timeout 30m ./internal/backend/rdp` | The RDP backend, against the same |
| `kubernetes_integration` | `go test -tags kubernetes_integration -run Integration -timeout 15m ./internal/backend/kubernetes` | The Kubernetes backend, against k3s with a busybox pod |
| `integration` | `go test -tags integration -run TestTigerVNC -timeout 30m ./internal/backend/vnc` | The VNC backend over VeNCrypt, against the demo's TigerVNC image |

The k3s test pulls busybox, so it needs the network. The test files
describe their settings in their comments.

The `integration` tag has three more tests, against a server that you
start:

- `TestBenchDesktop` runs the VNC backend against x11vnc on the bench
  desktop:

    ```sh
    podman build -t occamy-bench-desktop -f docker/bench/desktop/Dockerfile .
    podman run -d --rm --name occamy-vnc-test -p 127.0.0.1:15900:5900 occamy-bench-desktop vnc
    OCCAMY_VNC_ADDR=127.0.0.1:15900 go test -tags integration -run TestBenchDesktop ./internal/backend/vnc
    podman rm -f occamy-vnc-test
    ```

    On a machine that others share, give the image, the container and
    the port names of your own.

- `TestEncodings` takes the same `OCCAMY_VNC_ADDR`, and asks for one
  encoding at a time.
- `TestMacOS` needs a Mac with Screen Sharing on, at
  `OCCAMY_VNC_ARD_ADDR`. Its comment in
  `internal/backend/vnc/ard_integration_test.go` says how to set it up.

## Fuzz tests

The parsers of everything that arrives over a network have fuzz
targets. `go test -list '^Fuzz' ./...` lists them by package. Fuzz one
target at a time:

```sh
go test -run '^$' -fuzz '^FuzzSession$' -fuzztime 30s -parallel 4 ./client
```

`-parallel 4` keeps the fuzzer to four workers, which leaves the
machine usable. CI fuzzes every target for 30 seconds each night
([fuzz.yml](../.github/workflows/fuzz.yml)). The same here takes about
20 minutes:

```sh
for pkg in $(go list ./...); do
  for target in $(go test -list '^Fuzz' "$pkg" | grep '^Fuzz'); do
    go test -run '^$' -fuzz "^$target\$" -fuzztime 30s -parallel 4 "$pkg" || break 2
  done
done
```

A failure writes its input to the package's `testdata/fuzz/<target>/`,
where plain `go test` replays it from then on.

## Occamy for macOS

The Mac app runs on macOS 26 or later. It links the Go client as a C
archive, which `make swift-core` builds, for Apple silicon and Intel:

```sh
make swift-core
swift run --package-path client/occamy-swift Occamy    # run it from source
make swift-test    # its tests, which also start the interop server
make swift-app     # client/occamy-swift/build/app/Occamy.app
make swift-dmg     # and a disk image of it beside it
```

The app has the hardened runtime. Without a Developer ID it is signed
ad hoc: it runs on the Mac that built it, and Gatekeeper refuses a copy
downloaded on another Mac. To sign it for other Macs, name your
Developer ID certificate. To have Apple notarize the disk image too,
name the profile that `xcrun notarytool store-credentials` saved:

```sh
make swift-notarize DEVELOPER_ID="Developer ID Application: Your Name (TEAMID)" NOTARY_PROFILE=occamy
```

## The website

The README and `docs/` are also the website,
<https://changkun.github.io/occamy/>, which MkDocs builds
([mkdocs.yml](../mkdocs.yml)). To preview it:

```sh
python3 -m venv .venv && .venv/bin/pip install -r scripts/docs-requirements.txt
.venv/bin/mkdocs serve
```

CI builds it with `--strict` for a pull request that changes the docs,
so a link or an anchor that does not resolve fails the build. It
publishes the site from `master`.

The figures that aren't measurements or Mermaid diagrams, such as the
protocol page's byte layout, are SVGs in a light and a dark version, drawn by a module of
`scripts/figures/` named after the page. After you change one, draw
them all again:

```sh
python3 scripts/docs_figures.py
```

## Cleaning up

[Tear down](try-it.md#6-tear-down) removes the demo stack and its
images. To remove what the builds and tests left in the checkout:

```sh
rm -f occamyd
rm -rf client/occamy-web/dist client/occamy-web/node_modules
rm -f "${TMPDIR:-/tmp}/occamy-webinterop"
```

`podman rmi occamy-bench-desktop` removes the bench desktop image of
`TestBenchDesktop`.
