# Occamy Manual

## Architecture

Figure 1 illustrates the architecture of an Guacamole application, it requires
end-user install a Guacamole Servlet for authentication proxy, and uses `guacd` as
a second proxy for the connection management between end-user and remote desktop server.
Futhermore, `guacd` manages connection in different processes, which can limits the maximum
connection of Guacamole application.

```
|-- Browser --|-------- Guacamole Server -----------|--- Intranet ---|

UserA --------+                                      +---- RDP server
              +------ Guacamole Servlet              |
UserB --------+                |                     +---- VNC server
                               +------- guacd -------+
                                                     +---- Others
```

_Figure 1: Guacamole Architecture_

Occamy solves these issues, and it uses JWT for authentication as default option, manages
all connection in mutiple thread rather than multiple processes, as shown in Figure 2.

```
|-- Browser --|---- Occamy Server -----|--- Intranet ---|


UserA --------+                        +---- RDP server
              +------ Occamy ----------+
UserB --------+                        +---- VNC server
                                       |
                                       +---- Others
```

_Figure 2: Occamy Architecture_

## Protocol Instructions

Refer to [Guacamole protocol reference](https://guacamole.apache.org/doc/gug/protocol-reference.html). Note that Occamy has no handshake process between client and Occamy, one can simply POST the connection information to Occamy for getting authentication tokens. 


## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)