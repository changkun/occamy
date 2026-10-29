<img src="./docs/occamy.png" alt="logo" height="150" align="right" />

# Occamy

[![Builds](https://github.com/changkun/occamy/actions/workflows/dockerimage.yml/badge.svg?branch=master)](https://github.com/changkun/occamy/actions/workflows/dockerimage.yml)
[![Go Reference](https://pkg.go.dev/badge/changkun.de/x/occamy.svg)](https://pkg.go.dev/changkun.de/x/occamy)
[![Latest tag](https://img.shields.io/github/v/tag/changkun/occamy?sort=semver&label=latest)](https://github.com/changkun/occamy/tags)
[![Go version](https://img.shields.io/github/go-mod/go-version/changkun/occamy)](./go.mod)
[![License](https://img.shields.io/github/license/changkun/occamy)](./LICENSE)
Occamy is an open source protocol and proxy for modern remote desktop control that written in Go.

## Project status

Occamy is experimental. Development paused at the end of 2021 and resumed in
2026 with a toolchain and dependency refresh, and `occamyd` now serves
sessions through upstream [guacd](https://hub.docker.com/r/guacamole/guacd)
1.6. What works today:

| Protocol | Status |
|----------|--------|
| VNC | Through guacd, verified end to end |
| SSH | Through guacd, verified end to end, including repeated sessions |
| RDP | Through guacd, verified against xrdp |

Known gaps and planned work are tracked as design specs in [specs/](./specs/README.md).

## To start using Occamy

### Build

Occamy is deployed as a container image. To build it you need Podman (or Docker)
and `make`:

```
git clone https://github.com/changkun/occamy.git
cd occamy
make build                          # or: make build CONTAINER_ENGINE=docker
```

The image builds the web client and a static `occamyd` in separate stages, on a
distroless base (see [docker/Dockerfile](./docker/Dockerfile)).
Occamy serves sessions through [guacd](https://hub.docker.com/r/guacamole/guacd),
which it expects at `guacd.address` in [conf.yaml](./conf.yaml). To run both:

```
podman network create occamy
podman run -d --name guacd --network occamy docker.io/guacamole/guacd:1.6.0
podman run -d --network occamy -p 5636:5636 \
    -v ./docker/occamy.yaml:/occamy/conf.yaml:ro occamy:latest
```

[docker/occamy.yaml](./docker/occamy.yaml) points occamy at `guacd:4822`. guacd
opens the connections to the remote desktops, so their hosts must be
reachable from the guacd container.

### Releases

From v0.5.0 on, each [release](https://github.com/changkun/occamy/releases)
publishes a multi-arch image (linux/amd64 and linux/arm64) as
`ghcr.io/changkun/occamy:<version>`, the web client as an archive, and
`SHA256SUMS`, all with build provenance attestations. To verify them:

```
sha256sum -c SHA256SUMS
gh attestation verify occamy-web-<version>.tar.gz --repo changkun/occamy
gh attestation verify oci://ghcr.io/changkun/occamy:<version> --repo changkun/occamy
```

### Build from source

A native build needs Go 1.26+ and no C toolchain. It works on Linux, macOS and
Windows. `occamyd` needs a guacd at `guacd.address` in [conf.yaml](./conf.yaml)
(default `localhost:4822`):

```
podman run -d -p 4822:4822 docker.io/guacamole/guacd:1.6.0
make compile                 # CGO_ENABLED=0, produces ./occamyd
./occamyd -conf=./conf.yaml
```

### Web client

The demo web client lives in [client/occamy-web](./client/occamy-web) (Vue 3, Vite).
It needs Node.js 20.19+ or 22.12+:

```
make client     # npm ci && npm run build, output in client/occamy-web/dist
```

With `client: true` in [conf.yaml](./conf.yaml), `occamyd` serves it at `/static`.
For development, run `npm run dev` in `client/occamy-web`; it proxies `/api` to an
`occamyd` on `localhost:5636`.

### APIs

Occamy offers two APIs:

- `/api/v1/login` distributes JWT tokens for authentication and
- `/api/v1/connect` is used for WebSocket based Occamy connection.

If you build Occamy with web client, you can also access `/static` for web client demo.

### Demo

With compose (`podman compose` needs a compose provider such as
podman-compose or docker-compose installed), you should be able to run a
working demo with:

```
make build      # the occamy image
make run        # occamy plus the demo servers, on localhost:5636
make stop
```

[docker/docker-compose.yaml](./docker/docker-compose.yaml) builds small
Debian demo servers from [docker/demo](./docker/demo) for amd64 and arm64.
Occamy reaches them by compose service name:

| Protocol | Host | Username | Password |
|----------|------|----------|----------|
| VNC | `vnc:5900` | | `vncpassword` |
| SSH | `ssh:22` | `occamy` | `occamy` |
| RDP | `rdp:3389` | `occamy` | `occamy` |

Occamy connects to them through upstream guacd 1.6, which also runs in
the stack.

Here is a working video demo:

<div align="center">
  <a href="https://youtu.be/e24WHo4Kpx8"><img src="https://img.youtube.com/vi/e24WHo4Kpx8/0.jpg" alt="IMAGE ALT TEXT"></a>
</div>

### Tests

```
go test ./...
```

[tests/](./tests) holds end-to-end and pressure tests that open real
sessions to the demo servers. They are skipped unless
`OCCAMY_E2E_ENDPOINT` is set; `make e2e` sets it to `localhost:5636`:

```
make build
make run
make e2e
make stop
```

The pressure test also needs `OCCAMY_PRESSURE_N`, the number of rounds
(`make e2e OCCAMY_PRESSURE_N=10`). CI does not run it yet
([specs/003](./specs/003-session-lifecycle-hardening.md)).

## Contributing

Feedback, bug reports and pull requests are welcome. See
[CONTRIBUTING.md](./CONTRIBUTING.md) for how to build, test and send
changes, and the [code of conduct](./CODE_OF_CONDUCT.md). Report security
issues privately as described in [SECURITY.md](./SECURITY.md), which also
lists the current security caveats. Changes are recorded in
[CHANGELOG.md](./CHANGELOG.md).

## Why Occamy and how it works?

Occamy implements a generic remote desktop protocol with modern approaches. 
It currently performs [Guacamole](https://guacamole.apache.org/) protocol 
and eventually intends to redesign and propose Occamy protocol.

The benefits of Occamy that differ from Guacamole are:

- Authentication supports
- Simplified architecture
- Streaming compression and optimization
- Modern with Go

Occamy takes the place of the Guacamole web application: a single Go
binary issues JWTs and tunnels the Guacamole protocol over WebSocket.
Any client that speaks the Guacamole protocol over WebSocket can switch
to Occamy without changes. For now the remote desktop protocols
themselves run in upstream guacd. [specs/008](./specs/008-native-architecture.md)
describes the path back to a single binary, with native SSH and VNC and
guacd kept only for RDP.

Read more details in [docs](./docs/README.md).

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019-2026 [Ou Changkun](https://changkun.de)

The web client's [occamy.js](./client/occamy-web/src/components/occamy.js) is a
modified copy of [Apache guacamole-common-js](https://github.com/apache/guacamole-client)
0.9.14, licensed under the [Apache License 2.0](./client/occamy-web/LICENSE.guacamole)
(see [NOTICE](./client/occamy-web/NOTICE.guacamole)). The image and the web
client archive ship both.
