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
| [server/](../server) | HTTP routes (gin), JWT login and auth, the session table, and WebSocket to guacd proxying |
| [internal/guacd/](../internal/guacd) | guacd client: the handshake that creates or joins a connection |
| [internal/guacamole/](../internal/guacamole) | Guacamole instruction encoding, parsing, and stream I/O, and a display that applies drawing and the pointer as a client does, for the benchmark and the guacd bridge |
| [internal/config/](../internal/config) | `conf.yaml` loading, the JWT claim structure, and guacd parameters |
| [bench/](../bench) | Benchmark harness: scenarios, emulated networks, and the measurements in [benchmarks.md](./benchmarks.md) |
| [client/occamy-web/](../client/occamy-web) | Demo web client (Vue 3) with a fork of guacamole-common-js 0.9.14 (Apache-2.0) |

A connection goes through these steps:

1. The client POSTs `protocol`, `host`, `username`, and `password` to
   `/api/v1/login` and receives a JWT carrying them as claims.
2. The client opens a WebSocket (subprotocol `guacamole`) to `/api/v1/connect`
   with the token.
3. `occamyd` looks up a session by a hash of the claims. Users with identical
   claims share one session. The first user dials guacd and asks it for a new
   connection with the claims as parameters, plus the protocol's
   `guacd.params` from `conf.yaml`; later users join it with
   `select $id`. The session ends when its last user leaves. A user who
   joins just as guacd ends the connection starts a new session instead.
4. `occamyd` copies instructions between guacd and the WebSocket until either
   side closes, and then closes the WebSocket with the Guacamole status code.

## Occamy v1, in progress

v1 is being built beside v0 on `master`. Nothing below serves a route
yet; the released v0.5.0 behaves as described above. Each package names
the spec it implements.

| Path | Role |
|------|------|
| [protocol/](../protocol) | Protocol v1, a public package: framing, the message type registry, size limits, golden vectors ([protocol-v1](../specs/008-native-architecture/protocol-v1.md)) |
| [internal/backend/](../internal/backend) | The interface every backend implements (native SSH, native VNC, the guacd bridge), its input events and prompts, and scripted fakes for tests ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/backend/guacd/](../internal/backend/guacd) | The guacd bridge: a graphical backend that applies guacd's drawing, copies each frame into the session's framebuffer before it answers the frame's sync, and translates viewers' input into Guacamole instructions ([guacd-bridge](../specs/008-native-architecture/guacd-bridge.md)) |
| [internal/pacer/](../internal/pacer) | Per-viewer pacing: the frame window, the frame after an input sent at once, bytes in flight held near the bandwidth-delay product, and quality that adapts ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/framebuffer/](../internal/framebuffer) | A session's screen in tiles, versioned from one counter, so each viewer is sent only the tiles it has not seen ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [internal/auth/](../internal/auth) | Tokens of API v1: Ed25519 JWTs for access (a user and groups) and for sessions (a session and a role), from occamyd or a trusted issuer ([api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/session/](../internal/session) | The session core: one goroutine per session runs a backend, applies input by role, keeps each viewer's small state as latest values, and a registry holds sessions and share tokens ([session-core](../specs/008-native-architecture/session-core.md)) |

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)