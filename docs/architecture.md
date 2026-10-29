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
still relies on upstream `guacd` for the remote desktop protocols;
[specs/008](../specs/008-native-architecture.md) describes the path to native SSH and
VNC, with `guacd` kept only for RDP.

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
| [internal/protocol/](../internal/protocol) | Guacamole instruction encoding, parsing, and stream I/O |
| [internal/config/](../internal/config) | `conf.yaml` loading, the JWT claim structure, and guacd parameters |
| [client/occamy-web/](../client/occamy-web) | Demo web client (Vue 3) with a fork of guacamole-common-js 0.9.14 (Apache-2.0) |

A connection goes through these steps:

1. The client POSTs `protocol`, `host`, `username`, and `password` to
   `/api/v1/login` and receives a JWT carrying them as claims.
2. The client opens a WebSocket (subprotocol `guacamole`) to `/api/v1/connect`
   with the token.
3. `occamyd` looks up a session by a hash of the claims. Users with identical
   claims share one session. The first user dials guacd and asks it for a new
   connection with the claims as parameters; later users join it with
   `select $id`. The session ends when its last user leaves.
4. `occamyd` copies instructions between guacd and the WebSocket until either
   side closes, and then closes the WebSocket with the Guacamole status code.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)