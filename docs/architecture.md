# Occamy Architecture

Figure 1 illustrates the architecture of an Guacamole application, it requires
end-user install a Guacamole Servlet for authentication proxy, and uses `guacd` as
a second proxy for the connection management between end-user and remote desktop server.
Furthermore, `guacd` manages connection in different processes, which can limits the maximum
connection of Guacamole application.

```
|-- Browser --|-------- Guacamole Server -----------|--- Intranet ---|

                                                     +---- RDP server
UserA --------+                                      |
              +------ Guacamole Servlet --- guacd ---+---- VNC server
UserB --------+                                      |
                                                     +---- Others
```

_Figure 1: Guacamole Architecture_

Occamy solves these issues, and it uses JWT for authentication as default option, manages
all connection in multiple thread rather than multiple processes, as shown in Figure 2.

```
|-- Browser --|---- Occamy Server -----|--- Intranet ---|

                                       +---- RDP server
UserA --------+                        |
              +------ Occamy ----------+---- VNC server
UserB --------+                        |
                                       +---- Others
```

_Figure 2: Occamy Architecture_

## Components

| Path | Role |
|------|------|
| [server/](../server) | HTTP routes (gin), JWT login and auth, the session table, and WebSocket to libguac I/O proxying |
| [internal/lib/](../internal/lib) | cgo bindings to libguac: clients, users, sockets, errors |
| [internal/protocol/](../internal/protocol) | Guacamole instruction encoding, parsing, and fd-based I/O |
| [internal/config/](../internal/config) | `conf.yaml` loading and the JWT claim structure |
| [guacamole/](../guacamole) | Bundled guacamole-server 1.0.0 fork: libguac plus VNC, SSH, and RDP plugins |
| [client/occamy-web/](../client/occamy-web) | Demo web client (Vue 3) with a fork of guacamole-common-js |

A connection goes through these steps:

1. The client POSTs `protocol`, `host`, `username`, and `password` to
   `/api/v1/login` and receives a JWT carrying them as claims.
2. The client opens a WebSocket (subprotocol `guacamole`) to `/api/v1/connect`
   with the token.
3. `occamyd` looks up a session by a hash of the claims. Users with identical
   claims share one session; otherwise a new libguac client is created and its
   protocol plugin is loaded with `dlopen`.
4. A Unix socket pair connects the libguac user to `occamyd`, which copies
   instructions between the socket and the WebSocket until either side closes.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)