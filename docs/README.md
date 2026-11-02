# Occamy Manual

The documents, for using and for working on Occamy:

- [Try Occamy](./try-it.md)
  + TL;DR: From a fresh checkout to the demo stack in a browser: one
    connection of each protocol, sharing, the clipboard, resizing,
    reconnecting, every test suite, and tearing it down.
- [Occamy Architecture](./architecture.md)
  + TL;DR: How `occamyd`, its native backends, the guacd bridge and the
    clients fit together, package by package.
- [HTTP API (v1)](./api.md)
  + TL;DR: Every route a v1 configuration file serves: sign-in, the
    connections and sessions, shares, the stream upgrades, their tokens,
    replies, errors and limits.
- [Occamy Protocol (v1)](./protocol.md)
  + TL;DR: The wire protocol of Occamy v1: binary messages over one
    WebSocket, a handshake in one round trip, and frames acknowledged
    under a small window. Normative, with golden vectors.
- [Occamy Protocol (v0)](./protocol-cn.md)
  + TL;DR: The Guacamole protocol of the v0 routes, in Chinese. v1.0
    drops it with them.
- [Configuration](./configuration.md)
  + TL;DR: Every key of the configuration file, its default and how it is
    validated, and the v0 files of the 0.x servers.
- [Benchmarks](./benchmarks.md)
  + TL;DR: How Occamy is measured, and the numbers protocol v1 must beat.
- [Codec measurement](./benchmarks/codecs.md)
  + TL;DR: Which pure-Go codecs, tile size and pixel layout the graphics
    pipeline uses, measured on frames recorded from the bench desktop.
- [Debugging and measuring](./debugging.md)
  + TL;DR: Profiling `occamyd` in debug mode, guacd's processes, and the
    pressure test.
- [Design specs](../specs/README.md)
  + TL;DR: The design of each part, and the work planned.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019-2026 [Ou Changkun](https://changkun.de)