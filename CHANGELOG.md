# Changelog

All notable changes to Occamy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Occamy follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases before 0.5.0 were tagged without the `v` prefix.

## [Unreleased]

Development resumed in 2026 after a pause from the end of 2021.

### Added

- Groundwork for running on upstream guacd 1.6: a guacd handshake client
  (`internal/guacd`), a `guacd` section in `conf.yaml`, and a guacd
  service in the demo stack. `occamyd` does not use guacd yet.
- Demo servers built from the repository for linux/amd64 and linux/arm64
  (`docker/demo/`), and end-to-end tests that CI runs on every push
  (`make e2e`).
- A changelog, security policy, code of conduct and contribution guide.
- License and NOTICE files for the bundled guacamole-server code, also
  shipped in the image under `/occamy/licenses/`.
- Design specs for the road to v1 in `specs/`.

### Changed

- Go 1.26 or later is required. All Go dependencies have been upgraded and
  the `vendor/` directory removed. The module path is
  `changkun.de/x/occamy`.
- The web client uses Vue 3 and Vite.
- The image is built on Debian trixie. Podman is the default container
  engine; set `CONTAINER_ENGINE=docker` to use Docker.
- Instructions are framed by their declared lengths, with upstream
  Guacamole's limits on element length and count.
- The demo stack addresses its servers by service name (`vnc:5900`,
  `ssh:22`) instead of fixed IP addresses.
- Session IDs are shorter.
- Logging uses the standard library instead of logrus.

### Removed

- RDP. The bundled guacamole-server needs FreeRDP 1.x, which current
  distributions no longer ship. RDP returns through upstream guacd.
- The `/api/v1/ping` endpoint.

### Fixed

- A failed join no longer leaves the proxy locked for every other
  connection.
- Ending a VNC session no longer crashes `occamyd` with a double free
  under libvncclient 0.9.15.
- Tokens no longer stop validating after one hour of uptime.
- `occamyd` shuts down cleanly on SIGTERM.
- The bundled guacamole-server builds with OpenSSL 3.
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
