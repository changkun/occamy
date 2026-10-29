# Changelog

All notable changes to Occamy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Occamy follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases before 0.5.0 were tagged without the `v` prefix.

## [Unreleased]

Development resumed in 2026 after a pause from the end of 2021.

### Added

- The web client sends its window size, pixel density and time zone, so
  the remote desktop matches the browser window.
- Demo servers built from the repository for linux/amd64 and linux/arm64
  (`docker/demo/`), and end-to-end tests that CI runs on every push
  (`make e2e`).
- RDP sessions, through guacd, with an xrdp demo server in the demo stack.
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
- Ending a VNC session no longer crashes `occamyd` with a double free
  under libvncclient 0.9.15.
- Tokens no longer stop validating after one hour of uptime.
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

[Unreleased]: https://github.com/changkun/occamy/compare/0.4.0...HEAD
[0.4.0]: https://github.com/changkun/occamy/compare/0.3.0...0.4.0
[0.3.0]: https://github.com/changkun/occamy/compare/0.2.0...0.3.0
[0.2.0]: https://github.com/changkun/occamy/compare/0.1.0...0.2.0
[0.1.0]: https://github.com/changkun/occamy/releases/tag/0.1.0
