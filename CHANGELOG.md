# Changelog

All notable changes to Occamy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Occamy follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases before 0.5.0 were tagged without the `v` prefix.

## [Unreleased]

### Added

- The desktop baseline and the per-scenario targets for protocol v1
  ([docs/benchmarks.md](docs/benchmarks.md)). `bench/vncprobe` measures
  the bench desktop's VNC server on its own.
- A public Go package for protocol v1, `changkun.de/x/occamy/protocol`,
  with its framing: the message header and its ignorable flag, the field
  encodings, a registry of message types, and the maximum message size.
  Golden vectors in `protocol/testdata/framing.json` let other
  implementations check their codecs against it.
- Every message of protocol v1 in the `protocol` package, with the
  handshake (`hello` in the upgrade query, the token in a subprotocol,
  capabilities agreed in `welcome`), codecs and close codes, golden
  vectors in `protocol/testdata/messages.json`, and
  [docs/protocol.md](docs/protocol.md), the protocol's normative
  description. `docs/protocol-cn.md` is now marked as the v0 protocol.
- `internal/auth`, the tokens of API v1: Ed25519 JWTs for access
  (a user and groups) and for sessions (a session and a role), signed by
  occamyd or by a configured trusted issuer, with no other algorithm
  accepted. Not served by any route yet.
- `internal/session`, the session core of v1: one goroutine per session
  that runs a backend, draws into the framebuffer or the terminal, applies
  input by role (owner, viewer, read-only), keeps each viewer's small
  state as latest values, ends the session when the owner stays away or
  it idles, and a registry of sessions with share tokens that die with
  them. Not served by any route yet.
- `internal/backend/guacd`, the guacd bridge of v1: a graphical backend
  that applies guacd's drawing and copies each frame into the session's
  framebuffer before it answers the frame's `sync`, so that guacd paces
  against the bridge alone. It turns guacd's pointer, title, clipboard
  and credential requests into session events, and viewers' keys,
  pointer, wheel, text, clipboard and resizes into Guacamole
  instructions. `internal/guacamole`'s display now applies `cursor`,
  reports damage as rectangles, and copies areas out. Not served by any
  route yet.
- A native SSH backend for protocol v1, `internal/backend/ssh`, in pure
  Go. It accepts only pinned host keys, by key or SHA-256 fingerprint,
  unless told to ignore them, which it logs as a warning. It
  authenticates with key files or a password, which also answers a
  keyboard-interactive password challenge, and asks the session's owner
  for anything else. It feeds the shell's terminal output to the
  session. `occamyd` does not use it yet.
- Version 1 configuration files (`version: 1`): sign-in through static
  users, OIDC and trusted issuers, a connection registry whose
  credentials name files or environment variables instead of holding
  secrets, TLS, the debug listener, origins and ad-hoc targets. Unknown
  keys are errors, and validation reports every problem with its line and
  key path ([docs/configuration.md](docs/configuration.md)). A file
  without `version` is read as v0, as before. `occamyd` does not serve
  v1 files yet.
- `internal/terminal`, the headless terminal emulator of a terminal
  session: immutable, versioned screen snapshots that share unchanged
  rows, and the diffs that bring a viewer's terminal from the screen it
  last received to the current one, sending scrolled lines as line feeds
  so the client's scrollback fills. Not served by any route yet.
- `internal/stream`, a viewer's connection in protocol v1: `welcome`,
  then the client's input and views applied as they come, and frames at
  the pace of the viewer's link. Small state never waits behind tiles,
  and an input's effect goes ahead of motion already on the screen. It
  serves graphical sessions with raw tiles for now. Not served by any
  route yet.
- A native VNC backend for protocol v1, `internal/backend/vnc`, in pure
  Go: RFB 3.3, 3.7 and 3.8 with the security types None and VNC
  Authentication, asking the session's owner for a password the
  configuration lacks, and the Raw, CopyRect, DesktopSize and Cursor
  encodings in the framebuffer's own pixel layout. It asks for screen
  updates only while a viewer can see the screen, sends keys, pointer,
  wheel and Latin-1 clipboard text, and checks every length the server
  sends before it reads or allocates. `occamyd` does not use it yet.

### Changed

- `make bench` builds occamyd's image from the working tree before
  measuring it, instead of measuring whatever `occamy:latest` was.

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

[Unreleased]: https://github.com/changkun/occamy/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/changkun/occamy/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/changkun/occamy/compare/0.4.0...v0.5.0
[0.4.0]: https://github.com/changkun/occamy/compare/0.3.0...0.4.0
[0.3.0]: https://github.com/changkun/occamy/compare/0.2.0...0.3.0
[0.2.0]: https://github.com/changkun/occamy/compare/0.1.0...0.2.0
[0.1.0]: https://github.com/changkun/occamy/releases/tag/0.1.0
