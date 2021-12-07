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

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)