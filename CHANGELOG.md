# Changelog

All notable changes to Occamy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Occamy follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases before 0.5.0 were tagged without the `v` prefix.

## [Unreleased]

### Added

- A website, <https://changkun.github.io/occamy/>: the README and the
  documents in `docs/`, with search, built by MkDocs and published from
  `master` by the `Pages` workflow ([CONTRIBUTING.md](CONTRIBUTING.md#development)).
- Pages for deploying `occamyd` ([docs/deploy.md](docs/deploy.md)), using
  the web client ([docs/web-client.md](docs/web-client.md)), and building
  and testing Occamy ([docs/development.md](docs/development.md)).

### Changed

- The documents are rewritten for their readers, one job a page. The
  benchmarks page opens with Occamy against Guacamole, in figures and
  tables with 95% intervals and corrected tests
  ([docs/benchmarks.md](docs/benchmarks.md#occamy-against-guacamole)).
- Each line a session logs, such as `session ended`, carries the
  session's ID as `session=`, as the line that opened it does
  ([docs/debugging.md](docs/debugging.md#the-log)).

## [1.0.0-beta.2] - 2026-11-04

Occamy 1.0.0-beta.2 serves every session itself. guacd, the v0 routes
with the Guacamole protocol they served, v0 files and telnet are gone,
and a file that still configures guacd is refused with an error that
names what to change. Occamy for macOS opens a server's connections in
native windows. Full-screen motion takes about half the bandwidth and
two thirds of the CPU it took in beta.1, frames on a long path follow
the screen's changes instead of two a round trip, and on Linux occamyd
asks for BBR, so that lost packets slow a session less. A vnc or rdp
connection can close the clipboard in either direction. This is a beta:
the API and the protocol may change before 1.0.0.

### Added

- `auth.oidc.return_urls` takes an app's own scheme, a domain name in
  reverse such as `de.changkun.occamy:/oidc`, so that an app signs in
  through OIDC as the web client does
  ([docs/configuration.md](docs/configuration.md#signing-in-through-oidc)).
- Occamy for macOS, an app for macOS 26 on Apple silicon and Intel that
  opens the connections of your `occamyd` servers in windows of their
  own, as VNC Viewer does: it signs in by password or OIDC, keeps
  tokens in the Keychain, lists each server's connections with a
  picture of each, opens ad-hoc addresses such as `vnc://host:5901`, and
  gives each session a window that scales, goes full screen, shares the
  clipboard and copies a share link ([docs/macos-app.md](docs/macos-app.md)).
  `make swift-app` builds it as a universal `Occamy.app` with the
  hardened runtime, `make swift-dmg` a disk image of it, and
  `make swift-notarize` signs both with a Developer ID and has Apple
  notarize the image
  ([docs/macos-app.md](docs/macos-app.md#building-it)).
- `disable_copy` and `disable_paste` on a vnc or rdp connection keep
  the remote's clipboard from the viewers, or theirs from the remote,
  as guacd's `disable-copy` and `disable-paste` did
  ([docs/configuration.md](docs/configuration.md#the-connection-registry-connections)).
- `make swift-core` builds the Go client core as `liboccamy.a`, a C
  archive for Apple silicon and Intel with its interface in `occamy.h`,
  which the macOS app links
  ([spec 010](specs/010-native-swift-client.md)).
- `make bench-compare` collects the comparison of the Guacamole protocol
  with Occamy's in one resumable command: a plan file
  (`bench/plans/compare.yaml`) measured in blocks, in a random order per
  run, behind a load gate, with each container's memory, occamyd's
  runtime and the harness's own CPU, and two slower networks, N3 (100 ms,
  5 Mbit/s) and N4 (250 ms, 1 Mbit/s)
  ([docs/benchmarks.md](docs/benchmarks.md#the-comparison)), and
  `make bench-essential`, an hour's subset of it. `make bench-report`
  analyses the results into tables, exact tests and figures with 95%
  intervals ([docs/benchmarks.md](docs/benchmarks.md#analysis)).
- The essential comparison with guacd 1.6, on the same remotes and
  without network delay: 575 KiB of terminal output finishes in 20 ms
  instead of 930, a photograph costs 108–122 KiB instead of 2.1–2.2 MiB,
  a key on a still desktop shows in 4.6–8.1 ms instead of 16–21 at p95,
  and a frame of full-screen motion costs 7.7–8.0 KiB instead of 50–66
  ([docs/benchmarks.md](docs/benchmarks.md#occamy-against-guacamole)).
- The benchmark measures a scene's cadence as well as its frame rate:
  when each of its frames was first shown, the share never shown, and
  the 95th percentile of the interval between those shown
  ([docs/benchmarks.md](docs/benchmarks.md#what-is-measured)). A
  `v1-native-w2` series offers the frame window of two frames that every
  client agreed on before, so that `make bench-realtime-1` measures the
  window sized from the path against it, on one image in one run, and
  the analysis compares the two as a family of their own
  ([docs/benchmarks.md](docs/benchmarks.md#the-comparison)).
- `make bench-realtime-2` measures what packet loss costs a key while TCP
  holds back what follows the lost packet: `v1-native` and `guacd` with
  none, 0.5% and 2% of the packets to the client lost, on a link that
  the kernel of each server emulates, since the harness's proxy carries
  a byte stream and cannot drop a packet
  ([docs/benchmarks.md](docs/benchmarks.md#the-comparison)). The report
  gives what each loss added beside the round trip
  ([results](docs/benchmarks.md#step-2-what-loss-costs)), and the
  analysis says when the runs are too few for any difference to be
  significant after Holm's correction.
- `make bench-realtime-3a` measures step 2's loss study with occamyd's
  TCP on BBR beside CUBIC: a `v1-native-cubic` series on a service of its
  own, `occamy-v1-cubic`, whose server asks for CUBIC, and each v1 result
  read with `-runtime` records the congestion control its server's
  connections use
  ([docs/benchmarks.md](docs/benchmarks.md#the-comparison);
  [results](docs/benchmarks.md#step-3a-the-congestion-control)).
- Codec 7, `scaled`, carries a tile as a JPEG or WebP image at a
  fraction of its width and height, which the client scales up
  ([docs/protocol.md](docs/protocol.md#the-scaled-codec)). occamyd sends
  tiles in motion at half size to a client that lists it, and at full
  size again once they stop. The Go and web clients list it; older
  clients do not, and get tiles in motion at full size as before.

### Changed

- The analysis's `comparisons.csv` names the estimate `times_better`.
  It was `factor`, as was the column of the study's setting, such as
  `loss=2%`, so a CSV reader kept only one of the two.
- The benchmark's load gate holds each result rather than each block of
  results, so that the load a scene leaves does not carry into the
  results after it
  ([docs/benchmarks.md](docs/benchmarks.md#the-comparison)).
- On a long path, frames follow the screen's changes instead of two a
  round trip. The server and clients offer a frame window of 16, and the
  server keeps as many frames unacknowledged as cover the time an
  acknowledgement takes: 2 on a local link, about 5 across 50 ms. Waiting
  for that window no longer lowers the image quality, which now drops only
  when the link's bandwidth runs short. Terminals keep a window of 2, so
  bulk output still coalesces, and older clients, which offer no window,
  get 2 as before ([docs/protocol.md](docs/protocol.md#the-frame-window)).
  Over VNC with 50 ms of delay, full-screen motion now shows all 30
  frames a second instead of about 19, and a key's effect in 96 ms
  instead of 119 at the 95th percentile
  ([docs/benchmarks.md](docs/benchmarks.md#step-1-the-frame-window-from-the-path)).
- On Linux, occamyd asks for BBR as the TCP congestion control of its
  connections, in place of the kernel's usual default, CUBIC, which
  takes every lost packet for congestion and slows down. Over VNC with
  50 ms of delay and 2% of the packets lost, full-screen motion now shows
  27.6 frames a second instead of 16.4, and a key's effect in 149 ms
  instead of 360 at the 95th percentile
  ([docs/benchmarks.md](docs/benchmarks.md#step-3a-the-congestion-control)).
  BBR needs the kernel's `tcp_bbr` module; without it occamyd keeps the
  kernel's choice and logs a warning. `congestion_control` in the v1 file
  names another, or `system` for the kernel's own
  ([docs/configuration.md](docs/configuration.md#tcp-congestion-control)).
- occamyd spends less CPU on full-screen motion: it counts a moving
  tile's colours only until it knows the tile goes lossy. Over VNC a
  frame of motion takes 17.6 ms of occamyd's CPU instead of 19.5.
- Full-screen motion takes about half the bandwidth: tiles in motion go
  at half their width and height to a client that lists the `scaled`
  codec, as the Go and web clients do, and at full size again once they
  stop. Over VNC a frame of motion is 8.0 KiB instead of 14.2, and
  takes 12.7 ms of occamyd's CPU instead of 19.3; over RDP 7.7 KiB
  instead of 14.1, and 14.6 ms instead of 24.7, at the same frame rate.
- The web client names a share's roles by what they allow: a link
  joins **with control**, to type, point and use the clipboard, or
  **view only**. API v1 still calls them `viewer` and `read-only`.
- The image and the release archives ship a [conf.yaml](conf.yaml) of
  version 1, to fill in with your users and connections; occamyd
  refuses it until it names someone who signs in.
- The demo stack's connections lose their `-native` suffix, since each
  protocol has one backend: `vnc-native` is `vnc`, `rdp-native` is
  `rdp`, and so on. The pressure test now runs through `occamy-v1`.
- The demo stack lists three connections, the XFCE desktop over VNC and
  over RDP, and SSH, so that a first look starts with a desktop rather
  than a list to scroll past. The connections to the bare servers that
  the end-to-end tests probe, `vnc`, `rdp` and one for each VNC security
  type and keyboard layout, moved to `docker/occamy-v1-e2e.yaml`, with
  the 100 sessions a user that the pressure test needs. `make run E2E=1`
  serves that file in place of the demo's, and `make e2e` and `make
  soak` run against that stack
  ([docs/development.md](docs/development.md#the-test-servers)).

### Removed

- guacd, from `occamyd`: every session is served by occamyd's own
  backend for its protocol. A v1 file with `guacd`, a connection's
  `backend` or `adhoc_backends` is refused, with what replaced each key
  ([docs/configuration.md](docs/configuration.md#files-written-for-guacd)).
  A file whose `guacd.params` recorded sessions (`recording-path` and
  guacd's other `recording-` and `typescript-` parameters) or
  restricted the clipboard (`disable-copy`, `disable-paste`) is refused
  with why, rather than served unrecorded or with the clipboard open:
  occamyd records no session until a later beta, and a connection's
  `disable_copy` and `disable_paste` restrict the clipboard instead.
- The v0 routes, `/api/v0/login` and `/api/v0/connect`, the Guacamole
  protocol through guacd, with `legacy_v0`. Clients use API v1 and
  protocol v1.
- v0 files. A file without `version: 1` is refused with an error that
  names the v1 format
  ([docs/configuration.md](docs/configuration.md#v0-files)).
- telnet, which only guacd served, as a connection's protocol and an
  ad-hoc target's.
- The demo stack's `occamy` and `guacd` services. guacd runs only in the
  bench overlay, for the benchmark's `guacd` series, the baseline.
- The benchmark's `occamy`, `v1-guacd` and `v1-guacd-gfx` series, their
  `occamy-v1-gfx` service and the harness's `-addr` and `-v1-gfx` flags.
  Their recorded results stay in `docs/benchmarks/`.

### Known issues

- Windows RDP hosts need `disable_gfx: true` on their connection until
  occamyd takes ClearCodec: with GFX, the default, their screen stays
  nearly black. An ad-hoc rdp target cannot set it, so reach a Windows
  host through a connection
  ([docs/configuration.md](docs/configuration.md#backends)).

## [1.0.0-beta.1] - 2026-11-02

Occamy 1.0 no longer puts guacd and the Guacamole protocol between the
browser and the remote. `occamyd` connects to VNC, RDP, SSH and
Kubernetes itself, in Go without C, and speaks its own binary protocol.
A terminal travels as its output, which the browser draws, rather than
as pictures of text rendered on the server; a desktop travels as binary
tiles rather than as base64 images in text instructions; and small
messages never queue behind tiles, while each viewer's queue is paced
to its own link, so that a keystroke's update is not stuck behind a
large frame. guacd is still used for telnet, and for a vnc or rdp
connection that sets `backend: guacd`. A v0 file is served as before,
under new paths. This is a beta: the API and the protocol may change
before 1.0.0, which removes guacd and the v0 routes.

### Added

- Version 1 configuration files (`version: 1`): sign-in for static
  users, through OIDC and from trusted issuers; a registry of
  connections with allow lists and credentials from files, environment
  variables or the session's owner; TLS; limits; and a debug listener
  ([docs/configuration.md](docs/configuration.md)). They serve API v1
  ([docs/api.md](docs/api.md)) and protocol v1, a binary protocol over
  WebSocket ([docs/protocol.md](docs/protocol.md)), with the public Go
  packages `changkun.de/x/occamy/protocol` and
  `changkun.de/x/occamy/client`.
- Sessions with one owner and viewers who may type or only watch, by
  share link. The owner can come back within 60 seconds. Each viewer is
  paced to its own link, so a slow one does not hold back the others.
  Tiles of few colours go lossless and others in WebP or JPEG,
  scrolls go as copies, still tiles are refined, and clients cache tiles.
- Native backends. VNC: RFB 3.3, 3.7 and 3.8, VNC Authentication, VeNCrypt
  with X.509, Tight, ZRLE and Hextile, remote resizing, and Apple Remote
  Desktop for macOS with `ard: true`. RDP: TLS and NLA, bitmaps and the
  graphics pipeline (GFX) with RemoteFX and RemoteFX Progressive,
  resizing, with `limits.decode_cores` bounding the decoding of all RDP
  sessions. SSH: pinned host keys, a key file or a password. Kubernetes:
  a terminal in a pod through `pods/exec` or `pods/attach`. VNC and RDP
  carry clipboard text both ways.
- A bridge to guacd 1.6 for telnet, and for vnc and rdp with `backend:
  guacd` or, for ad-hoc targets, `adhoc_backends`.
- The web client, rebuilt in React on protocol v1: sign-in with a
  password or OIDC, a desktop on a canvas or a terminal in xterm.js,
  resizing with the window, share links and a clipboard panel.
- `occamy-v1` in the demo stack, at http://localhost:5637/static/ as
  `demo` with the password `occamy`, with an XFCE desktop and test
  servers natively and through guacd ([docs/try-it.md](docs/try-it.md)).
  CI fuzzes every fuzz target nightly.
- An OpenAPI 3.1 description of the HTTP API,
  [docs/openapi.yaml](docs/openapi.yaml), which `go test ./server` checks
  against the routes, the Go types of the bodies and the error codes.

### Changed

- The v0 routes moved from `/api/v1/login` and `/api/v1/connect` to
  `/api/v0/login` and `/api/v0/connect`, which a v0 file serves (the
  login with `client: true`) and a v1 file with `legacy_v0: true`.
  Tokens, responses and close statuses are unchanged. `auth.jwt_alg`
  must be HS256, HS384 or HS512, and `auth.jwt_secret` must be set.
- `conf.yaml`, in the image and the release archives too, ships
  `mode: release`. It shipped `mode: debug`, which served `/debug/pprof`
  without a token. A v0 file without `mode` still runs in debug mode.
- `occamyd` serves a file with `version: 1` as v1, and one without as
  v0. The image still starts with `conf.yaml`, a v0 file, so pass
  `-conf` with a v1 file. A v1 file needs `auth.signing_key_file`: keep
  it at `/var/lib/occamy/signing.key` on a volume, or everyone signs in
  again after a new container
  ([docs/configuration.md](docs/configuration.md#sign-in-auth)).
- The web client needs a v1 file with `web_client`. A v0 file's
  `client: true` still serves `/static/`, but the client cannot sign in.
- In a v1 file, vnc and rdp use the native backends unless `backend:
  guacd`. They read no `guacd.params`, check certificates against the
  system's roots, `tls.ca_file` or `tls.pins`, and RDP asks for NLA
  ([docs/configuration.md](docs/configuration.md#backends)).
- The demo stack's compose project is `occamy`, not `docker`. Remove a
  0.6.0 stack once, before `make run`: `(cd docker && podman compose -p
  docker down --remove-orphans --volumes --rmi local)`, or `docker
  compose` with Docker. It publishes on
  the loopback interface, its debug routes are off unless `OCCAMY_DEBUG=1`,
  and `make clean` removes only Occamy's own.
- Building needs Go 1.26.4 or later, and the web client Node.js 22.13
  or later on 22, 24, or 26 and later.

### Removed

- The Vue web client, with its fork of guacamole-common-js and the
  fork's Apache LICENSE and NOTICE. Other Guacamole protocol clients
  still work through the v0 routes.

### Security

- The request log no longer records query strings, which carry the v0
  token and the remote credentials in it.
- A v1 file closes most of the v0 gaps that [SECURITY.md](SECURITY.md)
  lists: tokens carry no remote credentials and never travel in a URL,
  there is no default user, ad-hoc targets need an allowlist, and pprof
  is only on the debug listener. `legacy_v0` brings those gaps back.
- No v1 backend sends the Power key or power keysyms, and a panic in a
  backend ends its session, not `occamyd`.

### Known issues

- Native RDP has met Windows only on a Windows Server 2025 CI runner, not
  a real deployment ([#32](https://github.com/changkun/occamy/issues/32)).
  NLA, the keyboard and resizing worked there. With GFX, the default, the
  screen stays nearly black, since ClearCodec and the bitmap cache are
  not supported yet: set `disable_gfx: true` on Windows connections, and
  `adhoc_backends: {rdp: guacd}` for ad-hoc ones. The clipboard did not
  work in either direction.
- xrdp speaks no NLA, so an xrdp connection needs `security: tls`.
- The native backends cannot restrict the clipboard or record sessions.
  A connection that needs guacd's `disable-copy`, `disable-paste` or
  recording keeps `backend: guacd`.
- Apple Remote Desktop has not been checked against a Mac, and anyone in
  between can read the Mac account's password
  ([docs/configuration.md](docs/configuration.md#apple-remote-desktop-ard)).
- guacd's process for a connection can hang: it did once in 128
  connections on a very busy host. The session fails after 90 seconds
  and says why, but the process keeps running until guacd restarts.

## [0.6.0] - 2026-10-30

### Added

- A benchmark harness (`make bench`, `bench/`) that measures sessions
  under fixed scenarios: input latency, bytes, server CPU and client
  decoding, over emulated networks. Its numbers for the Guacamole
  protocol are the baseline that protocol v1 is designed against
  ([docs/benchmarks.md](docs/benchmarks.md)).
- Desktop scenarios for the harness over VNC and RDP: a still desktop,
  key latency, scrolling text, a dragged window, full-screen motion and a
  photograph. They run on a bench desktop (`docker/bench/desktop`) whose
  session is a scene program (`bench/scene`) drawing the same pixels in
  every run.
- The README documents connection parameters in `guacd.params` and how to
  reach a Windows RDP host. `conf.yaml` lists `domain` and `security` as
  comments ([#32](https://github.com/changkun/occamy/issues/32)).
- CI runs the pressure test, which crashed `occamyd` before 0.5.0
  ([#20](https://github.com/changkun/occamy/issues/20)), with 10 rounds.
  Afterwards, `occamyd`'s goroutines must return to their number before
  the storm, and CI warns if guacd keeps a process.
- A guide to profiling `occamyd` in debug mode, watching guacd's
  processes, and the pressure test
  ([docs/debugging.md](docs/debugging.md),
  [#18](https://github.com/changkun/occamy/issues/18)).

### Fixed

- A client that connects just as the last user of a session with the
  same credentials leaves gets a new session, instead of joining the
  ending one and being disconnected.

## [0.5.0] - 2026-10-29

Development resumed in 2026 after a pause from the end of 2021.

### Added

- The web client sends its window size, pixel density and time zone, so
  the remote desktop matches the browser window.
- Demo servers built from the repository for linux/amd64 and linux/arm64
  (`docker/demo/`), and end-to-end tests that CI runs on every push
  (`make e2e`).
- Releases: a multi-arch image on GHCR, `occamyd` archives for Linux, macOS
  and Windows on amd64 and arm64 with the licenses of their dependencies,
  the web client archive, and `SHA256SUMS`, all with build provenance
  attestations.
- A changelog, security policy, code of conduct and contribution guide.
- License and NOTICE files for the web client's fork of
  guacamole-common-js (`client/occamy-web/`), also shipped in the image
  under `/occamy/licenses/` and in the web client archive.
- Design specs for the road to v1 in `specs/`.

### Changed

- Sessions are served by upstream guacd 1.6 instead of the bundled
  libguac. `occamyd` needs a reachable guacd: set `guacd.address` in
  `conf.yaml` (default `localhost:4822`), and `guacd.params` for
  per-protocol connection defaults. The demo stack runs one.
- RDP runs through guacd, whose FreeRDP is current, and is verified against
  an xrdp server added to the demo stack. The bundled plugin needed
  FreeRDP 1.x, which current distributions no longer ship.
- Sessions start and join without holding a proxy-wide lock, and a
  session ends when its last user leaves. A failed connection no longer
  blocks later connections with the same credentials.
- Errors close the WebSocket with the Guacamole status code as the close
  reason, so that the client can show it.
- Go 1.26 or later is required. All Go dependencies have been upgraded and
  the `vendor/` directory removed. The module path is
  `changkun.de/x/occamy`.
- The web client uses Vue 3 and Vite.
- `occamyd` builds without cgo (`CGO_ENABLED=0`) and so on Linux, macOS
  and Windows ([#25](https://github.com/changkun/occamy/issues/25)). The
  image runs it on distroless static as an unprivileged user and is about
  35 MB instead of 172 MB. Podman is the default container engine; set
  `CONTAINER_ENGINE=docker` to use Docker.
- Instructions are framed by their declared lengths, with upstream
  Guacamole's limits on element length and count.
- The demo stack addresses its servers by service name (`vnc:5900`,
  `ssh:22`) instead of fixed IP addresses.
- Session IDs are shorter.
- Logging uses the standard library instead of logrus.

### Removed

- The bundled guacamole-server 1.0.0 fork and its cgo bindings
  (`guacamole/`, `internal/lib/`). guacd took their place.
- The `/api/v1/ping` endpoint.

### Fixed

- A failed join no longer leaves the proxy locked for every other
  connection.
- SSH sessions can be opened again after the first one ends
  ([#22](https://github.com/changkun/occamy/issues/22)).
- Ending a VNC session no longer crashes `occamyd`.
- Tokens no longer stop validating after one hour of uptime.
- Two tokens whose claims concatenate to the same string (username `ab`
  with password `c`, and username `a` with password `bc`) no longer share
  a session. Session keys are a SHA-256 hash of the length-prefixed claims
  instead of an MD5 hash of their concatenation.
- `occamyd` closes connections whose request headers take longer than
  10 seconds, so slow clients cannot hold connections open.
- `occamyd` shuts down cleanly on SIGTERM.
- Indirect dependencies with known vulnerabilities have been upgraded.

## [0.4.0] - 2020-02-17

### Added

- A Guacamole JavaScript client maintained in the repository, with
  per-protocol client plugins.
- Clipboard support.
- Instructions for building from source.

### Changed

- The bundled guacamole-server was trimmed further: audio support, the
  user handshake and the keep-alive thread were removed, and the VNC
  plugin was consolidated.
- The latest Go release is used.

### Fixed

- Keyboard input in the web client.

## [0.3.0] - 2019-11-12

### Changed

- Inputs are handled on the connection's goroutine instead of a new
  thread, and fewer parameters cross into cgo.
- The bundled guacamole-server dropped session recording, WebP, libjpeg,
  unused sockets and `wait-fd`.
- CI moved to GitHub Actions.

## [0.2.0] - 2019-11-05

### Added

- A connection pressure test (`tests/`).
- `pprof` in debug mode.
- Default credentials for the three demo protocols in the web client.

### Changed

- libguac is self-managed in `guacamole/` instead of a git submodule.
- `release` is the default running mode.

### Fixed

- Workarounds for segmentation faults in the SSH plugin.

## [0.1.0] - 2019-01-31

### Added

- First release: a Go proxy that drives libguac through cgo and serves the
  Guacamole protocol over WebSocket with JWT authentication.

[Unreleased]: https://github.com/changkun/occamy/compare/v1.0.0-beta.2...HEAD
[1.0.0-beta.2]: https://github.com/changkun/occamy/compare/v1.0.0-beta.1...v1.0.0-beta.2
[1.0.0-beta.1]: https://github.com/changkun/occamy/compare/v0.6.0...v1.0.0-beta.1
[0.6.0]: https://github.com/changkun/occamy/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/changkun/occamy/compare/0.4.0...v0.5.0
[0.4.0]: https://github.com/changkun/occamy/compare/0.3.0...0.4.0
[0.3.0]: https://github.com/changkun/occamy/compare/0.2.0...0.3.0
[0.2.0]: https://github.com/changkun/occamy/compare/0.1.0...0.2.0
[0.1.0]: https://github.com/changkun/occamy/releases/tag/0.1.0
