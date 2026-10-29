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
2026 with a toolchain and dependency refresh. What works today:

| Protocol | Status |
|----------|--------|
| VNC | Builds and serves sessions, verified end to end |
| SSH | Builds and serves the first session; joining again fails ([#22](https://github.com/changkun/occamy/issues/22)) |
| RDP | Not built: the bundled guacamole-server only supports FreeRDP 1.x, which current distributions no longer ship |

Known gaps and planned work are tracked as design specs in [specs/](./specs/README.md).

## To start using Occamy

### Build

Occamy is deployed as a container image. To build it you need Docker (or Podman)
and `make`:

```
git clone https://github.com/changkun/occamy.git
cd occamy
make build                          # or: make build CONTAINER_ENGINE=podman
```

The image builds the web client, the bundled libguac with its VNC and SSH
plugins, and `occamyd` in separate stages (see [docker/Dockerfile](./docker/Dockerfile)).
Then you should be able to run occamy with:

```
docker run -itd -p 5636:5636 occamy:latest
```

### Build from source

A native build needs Go 1.26+ and the libguac build dependencies. On Debian or Ubuntu:

```
sudo apt install autoconf automake libtool make pkg-config \
    libcairo2-dev libpango1.0-dev libpng-dev \
    libssh2-1-dev libssl-dev libvncserver-dev
sudo ./guacamole/src/build-libguac.sh "$PWD/guacamole" /usr/local
export LD_LIBRARY_PATH=/usr/local/lib    # libguac dlopen()s its protocol plugins
make compile                             # produces ./occamyd
./occamyd -conf=./conf.yaml
```

Native builds on macOS are untested ([#25](https://github.com/changkun/occamy/issues/25)).

### Web client

The demo web client lives in [client/occamy-web](./client/occamy-web) (Vue 3, Vite).
It needs Node.js 20.19+ or 22.12+:

```
make client     # npm install && npm run build, output in client/occamy-web/dist
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

With compose, you should be able to run a working demo with:

```
make build
make run
make stop
```

The demo desktop images in [docker/docker-compose.yaml](./docker/docker-compose.yaml)
date from 2019 and are amd64 only; refreshing them is planned in
[specs/006-demo-stack-refresh.md](./specs/006-demo-stack-refresh.md).

Here is a working video demo:

<div align="center">
  <a href="https://youtu.be/e24WHo4Kpx8"><img src="https://img.youtube.com/vi/e24WHo4Kpx8/0.jpg" alt="IMAGE ALT TEXT"></a>
</div>

### Tests

```
go test ./internal/... ./server/...
```

`internal/lib` links against libguac, so run it where libguac is installed
(for example the `build` stage of the Dockerfile). [tests/](./tests) holds a
pressure test that needs the compose demo running.

## Contributing

Easiest way to contribute is to provide feedback! We would love to hear 
what you like and what you think is missing. PRs are welcome. 
Please follow the given PR template before you send your pull request.

## Why Occamy and how it works?

Occamy implements a generic remote desktop protocol with modern approaches. 
It currently performs [Guacamole](https://guacamole.apache.org/) protocol 
and eventually intends to redesign and propose Occamy protocol.

The benefits of Occamy that differ from Guacamole are:

- Authentication supports
- Simplified architecture
- Streaming compression and optimization
- Modern with Go

Occamy server side currently simplifies Guacamole proxy and 
Guacamole servlet client in a single middleware application. 
Any client that involves Guacamole protocol and uses WebSocket 
for authentication can directly switch to interact to Occamy 
without any changes.

Read more details in [docs](./docs/README.md).

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)
