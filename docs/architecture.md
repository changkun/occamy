# Occamy Architecture

Figure 1 illustrates the architecture of a Guacamole application. It needs a
Guacamole Servlet (a Java web application) for authentication and for tunneling the
protocol to the browser, and `guacd` as a second proxy that connects to the remote
desktop servers, with one process per connection.

```
|-- Browser --|-------- Guacamole Server -----------|--- Intranet ---|

                                                     +---- RDP server
UserA --------+                                      |
              +------ Guacamole Servlet --- guacd ---+---- VNC server
UserB --------+                                      |
                                                     +---- Others
```

_Figure 1: Guacamole Architecture_

Occamy replaces the Guacamole Servlet with a single Go binary that uses JWT for
authentication and tunnels the protocol over WebSocket, as shown in Figure 2. It
still relies on upstream `guacd` for the remote desktop protocols.
[specs/008](../specs/008-native-architecture.md) describes Occamy v1, which
replaces guacd one protocol at a time (SSH at beta.1, VNC at beta.2, RDP by
the release candidate) and ships v1.0 with neither guacd nor the Guacamole
protocol; see [Occamy v1, in progress](#occamy-v1-in-progress).

```
|-- Browser --|-------- Occamy Server ---------|--- Intranet ---|

                                               +---- RDP server
UserA --------+                                |
              +------ Occamy ------ guacd -----+---- VNC server
UserB --------+                                |
                                               +---- SSH server
```

_Figure 2: Occamy Architecture_

## Components

| Path | Role |
|------|------|
| [server/](../server) | The v0 routes on a `net/http` ServeMux, v0 tokens (HMAC JWTs, golang-jwt v5), the session table, and WebSocket (coder/websocket) to guacd proxying; the v1 server below shares the package |
| [internal/guacd/](../internal/guacd) | guacd client: the handshake that creates or joins a connection |
| [internal/guacamole/](../internal/guacamole) | Guacamole instruction encoding, parsing, and stream I/O, and a display that applies drawing and the pointer as a client does, decoding WebP as libwebp does, for the benchmark and the guacd bridge |
| [internal/config/](../internal/config) | `conf.yaml` loading, the JWT claim structure, and guacd parameters |
| [bench/](../bench) | Benchmark harness: scenarios, emulated networks, and the measurements in [benchmarks.md](./benchmarks.md) |
| [client/occamy-web/](../client/occamy-web) | Demo web client (Vue 3) with a fork of guacamole-common-js 0.9.14 (Apache-2.0) |

A connection goes through these steps:

1. The client POSTs `protocol`, `host`, `username`, and `password` to
   `/api/v0/login` and receives a JWT carrying them as claims.
2. The client opens a WebSocket (subprotocol `guacamole`) to `/api/v0/connect`
   with the token in the `Authorization` header, the `token` query
   parameter or the `jwt` cookie.
3. `occamyd` looks up a session by a hash of the claims. Users with identical
   claims share one session. The first user dials guacd and asks it for a new
   connection with the claims as parameters, plus the protocol's
   `guacd.params` from `conf.yaml`; later users join it with
   `select $id`. The session ends when its last user leaves. A user who
   joins just as guacd ends the connection starts a new session instead.
4. `occamyd` copies instructions between guacd and the WebSocket until either
   side closes, and then closes the WebSocket with the Guacamole status code.

## Occamy v1, in progress

v1 is being built beside v0 on `master`. `occamyd` picks the server by
the configuration file: a v0 file, without `version`, is served as
described above, and a v1 file (`version: 1`) by the v1 server, which so
far serves `/healthz`, static users' sign-in and, with `legacy_v0`, the
v0 routes. Released versions behave as described above, with the v0
routes under `/api/v1` through 0.6.0. Each package names the spec it
implements.

| Path | Role |
|------|------|
| [protocol/](../protocol) | Protocol v1, a public package: framing, the handshake (hello in the upgrade query, the token in a subprotocol, capabilities and their agreement), every message with its encoding and decoding, close codes, size limits, golden vectors; described in [protocol.md](protocol.md) ([protocol-v1](../specs/008-native-architecture/protocol-v1.md)) |
| [internal/backend/](../internal/backend) | The interface every backend implements (native SSH, native VNC, the guacd bridge), its input events and prompts, and scripted fakes for tests ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/backend/guacd/](../internal/backend/guacd) | The guacd bridge: a graphical backend that applies guacd's drawing, copies each frame into the session's framebuffer before it answers the frame's sync, and translates viewers' input into Guacamole instructions; recorded guacd 1.6 VNC, RDP and SSH sessions in `testdata/conformance` replay through it in tests ([guacd-bridge](../specs/008-native-architecture/guacd-bridge.md)) |
| [internal/pacer/](../internal/pacer) | Per-viewer pacing: the frame window, the frame after an input sent at once, bytes in flight held near the bandwidth-delay product, and quality that adapts ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/framebuffer/](../internal/framebuffer) | A session's screen in tiles, versioned from one counter, so each viewer is sent only the tiles it has not seen ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [server/](../server) | The v1 server on a `net/http` ServeMux: middleware for the request log, recovery, body limits and access tokens; API v1's JSON errors; `/healthz` and `POST /api/v1/auth/token`; TLS, graceful shutdown, and expvar and pprof on the debug listener; the v0 routes with `legacy_v0` ([api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/auth/](../internal/auth) | Tokens of API v1: Ed25519 JWTs for access (a user and groups) and for sessions (a session and a role), from occamyd or a trusted issuer ([api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/config/](../internal/config) | Version 1 configuration files: sign-in, the connection registry with credential references, TLS and the debug listener, decoded strictly and validated as a whole ([configuration](./configuration.md), [api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/session/](../internal/session) | The session core: one goroutine per session runs a backend, applies input by role, keeps each viewer's small state as latest values, and a registry holds sessions and share tokens ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/backend/ssh/](../internal/backend/ssh) | The native SSH backend: a pure-Go client that verifies pinned host keys, logs in with key files or a password and asks the owner for what they cannot answer, and feeds a shell's PTY output to the session's terminal ([native-ssh-backend](../specs/008-native-architecture/native-ssh-backend.md)) |
| [internal/backend/vnc/](../internal/backend/vnc) | The native VNC backend: a pure-Go RFB client that logs in with None, VNC Authentication, VeNCrypt's X.509 subtypes with the server's certificate checked against the system's roots, a private CA or pins, or, only where the connection allows it, Apple Remote Desktop authentication for macOS Screen Sharing, whose key exchange authenticates no server, asking the owner for credentials it lacks; asks for screen updates only while a viewer can see, by continuous updates where the server offers them; draws Tight (lossless, and sequential JPEG if a server sends it), ZRLE, Hextile, Raw and CopyRect rectangles, resizes and the cursor into the session's framebuffer; resizes the remote screen with ExtendedDesktopSize; and sends physical key codes and UTF-8 clipboard text where the server takes them ([native-vnc-backend](../specs/008-native-architecture/native-vnc-backend.md)) |
| [internal/terminal/](../internal/terminal) | The headless terminal emulator of a terminal session: immutable, versioned screen snapshots that share unchanged rows and carry each row's version, and the diffs that bring each viewer's terminal up to date ([native-ssh-backend](../specs/008-native-architecture/native-ssh-backend.md)) |
| [internal/stream/](../internal/stream) | A viewer's connection: welcome, the reader of its input and view, and the writer that sends small state ahead of frames, tiles or terminal screen diffs, and an input's effect ahead of motion, at the pace its pacer allows ([session-core](../specs/008-native-architecture/session-core.md)) |

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)