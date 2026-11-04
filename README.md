<div align="center">

<img src="./docs/occamy.png" alt="" height="150" />

# Occamy

[![Builds](https://github.com/changkun/occamy/actions/workflows/dockerimage.yml/badge.svg?branch=master)](https://github.com/changkun/occamy/actions/workflows/dockerimage.yml)
[![Go Reference](https://pkg.go.dev/badge/changkun.de/x/occamy.svg)](https://pkg.go.dev/changkun.de/x/occamy)
[![Release](https://img.shields.io/github/v/release/changkun/occamy?sort=semver&include_prereleases)](https://github.com/changkun/occamy/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/changkun/occamy)](./go.mod)
[![License](https://img.shields.io/github/license/changkun/occamy)](./LICENSE)

</div>

Occamy is a remote desktop gateway written in Go. It connects to
desktops and terminals over VNC, RDP, SSH and Kubernetes, and puts them
in your web browser.

![Signing in, opening a Linux desktop over RDP, running top in a terminal, and sharing the session with a read-only viewer, shown in the corner](./docs/demo.webp)

## Why Occamy

Apache Guacamole, the best-known gateway of this kind, reaches remote
desktops through guacd, a daemon written in C, and talks to the browser
in a text protocol of drawing instructions. Occamy has its own clients
for each remote and its own protocol, and differs in four ways:

- **A terminal travels as text.** guacd runs the terminal on the server
  and sends pictures of the rendered text. Occamy sends the terminal's
  output, and the browser draws it with xterm.js: traffic follows the
  characters rather than the pixels, and you can select the text.
- **A desktop travels as binary tiles.** Guacamole's text instructions
  carry each image as base64, a third larger than the image itself.
  Occamy's binary protocol sends only the tiles that changed, each
  lossless or as WebP or JPEG, moving content at half size until it
  stops, sends scrolls as copies, and lets the browser reuse tiles it
  has seen.
- **Input comes first.** Small messages never queue behind tiles, and
  each viewer has a short queue paced to its own connection, so a
  keystroke's update is not stuck behind a large frame, and a slow
  viewer does not hold up the others.
- **One process.** The VNC, RDP, SSH and Kubernetes clients run inside
  `occamyd`, a static Go binary without C, for Linux, macOS and
  Windows. There is no daemon to run beside it.

Against guacd 1.6, on the same remotes and without network delay,
Occamy prints 575 KiB of terminal output in 20 ms instead of about a
second, sends a photograph in about 120 KiB instead of 2 MiB, shows a
key press on a still desktop in 5–8 ms instead of 16–21 ms, and sends a
frame of full-screen motion in 8 KiB instead of 50–66
([benchmarks](docs/benchmarks.md#the-essential-comparison)).

## Features

- **Sharing.** Send a link that lets someone watch a session or take
  part in it. Links expire after an hour unless you choose otherwise.
- **Clipboard and resizing.** Copy and paste both ways, and the remote
  screen follows the size of your window.
- **Access control.** Users sign in with a password or through OIDC,
  each connection names who may open it, and the credentials for your
  servers stay on the server.
- **A Mac app.** [Occamy for macOS](docs/macos-app.md) opens your
  connections in windows of their own, as VNC Viewer does, with the
  same protocol as the browser.

## Protocols

| Protocol | Served by | Notes |
|----------|-----------|-------|
| VNC | Occamy | Tested with x11vnc and TigerVNC, VeNCrypt included |
| RDP | Occamy | Tested with xrdp. Windows hosts need `disable_gfx: true` for now; see [Backends](./docs/configuration.md#backends) |
| SSH | Occamy | The terminal is drawn in the browser |
| Kubernetes | Occamy | A shell in a pod, through the API server |

Occamy 1.0 is in beta: the API and the protocol may still change before
1.0.0.

## Try it

You need Podman or Docker with compose, and `make`. The demo stack runs
Occamy with an XFCE desktop, over VNC and RDP, and an SSH server:

```sh
git clone https://github.com/changkun/occamy.git
cd occamy
make build
make run
```

Open http://localhost:5637/static/ and sign in as `demo` with the
password `occamy`. `make stop` stops the stack. With Docker, add
`CONTAINER_ENGINE=docker` to each `make` command.

[docs/try-it.md](./docs/try-it.md) takes you through every connection,
sharing, the clipboard and resizing.

## Install

Each [release](https://github.com/changkun/occamy/releases) publishes a
multi-arch image, `ghcr.io/changkun/occamy:<version>`, and `occamyd`
archives for Linux, macOS and Windows on amd64 and arm64, with
`SHA256SUMS` and build provenance attestations.

Write a configuration file that lists your users and your connections:

```yaml
version: 1
listen: 0.0.0.0:5636
web_client: client/occamy-web/dist
auth:
  signing_key_file: /var/lib/occamy/signing.key
  users:
    - name: alice
      password_hash: "<output of the htpasswd line below>"
connections:
  - id: workstation
    name: Workstation
    protocol: vnc
    target: workstation.example.com:5900
    credentials: {password_env: WORKSTATION_PASSWORD}
    allow: {users: [alice]}
```

Make the password hash with
`htpasswd -nbBC 12 "" 'the password' | cut -d: -f2`, then run the image
with the file and a volume for the signing key:

```sh
podman run -d --name occamy -p 5636:5636 \
    -e WORKSTATION_PASSWORD \
    -v occamy-data:/var/lib/occamy \
    -v ./occamy.yaml:/occamy/v1.yaml:ro \
    ghcr.io/changkun/occamy:<version> /occamy/occamyd -conf /occamy/v1.yaml
```

To check a download against the release:

```sh
sha256sum -c SHA256SUMS
gh attestation verify occamyd-<version>-linux-amd64.tar.gz --repo changkun/occamy
gh attestation verify oci://ghcr.io/changkun/occamy:<version> --repo changkun/occamy
```

Open http://localhost:5636/static/. `occamyd` speaks plain HTTP unless
the file sets `tls`, so put it behind a TLS proxy or give it a
certificate before you expose it. [docs/configuration.md](./docs/configuration.md)
covers every key: OIDC, groups, SSH host keys, Kubernetes, sharing,
limits and TLS.

## Documentation

- [Try Occamy](./docs/try-it.md): the demo stack, step by step.
- [Configuration](./docs/configuration.md): every key of the
  configuration file.
- [HTTP API](./docs/api.md): sign-in, connections, sessions and shares,
  also as [OpenAPI](./docs/openapi.yaml).
- [Protocol](./docs/protocol.md): the wire protocol between `occamyd`
  and its clients.
- [Architecture](./docs/architecture.md): how the pieces fit together.
- [Debugging](./docs/debugging.md): profiling and measuring `occamyd`.

## Build from source

You need Go 1.26.4 or later, and Node.js 22.13+, 24 or 26+ for the web
client. No C toolchain is needed.

```sh
make compile    # ./occamyd
make client     # the web client, in client/occamy-web/dist
go test ./...
```

[CONTRIBUTING.md](./CONTRIBUTING.md) describes how to send a change, and
[docs/try-it.md](./docs/try-it.md) every test suite.

## Contributing

Bug reports and pull requests are welcome; see
[CONTRIBUTING.md](./CONTRIBUTING.md) and the
[code of conduct](./CODE_OF_CONDUCT.md). Report security issues
privately as [SECURITY.md](./SECURITY.md) describes. Changes are
recorded in [CHANGELOG.md](./CHANGELOG.md).

## License

[MIT](./LICENSE) &copy; 2019-2026 [Ou Changkun](https://changkun.de)
