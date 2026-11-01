# Occamy Manual

You may interested in the following document:

- [Try Occamy](./try-it.md)
  + TL;DR: From a fresh checkout to the demo stack in a browser: one
    connection of each protocol, sharing, the clipboard, resizing,
    reconnecting, every test suite, and tearing it down.
- [Occamy Architecture](./architecture.md)
  + TL;DR: Occamy v0 authenticates with JWT and proxies each connection to
    guacd; v1, in progress, has its own protocol and backends.
- [HTTP API (v1)](./api.md)
  + TL;DR: Every route a v1 configuration file serves: sign-in, the
    connections and sessions, shares, the stream upgrades, their tokens,
    replies, errors and limits.
- [Occamy Protocol (v1)](./protocol.md)
  + TL;DR: The wire protocol of Occamy v1: binary messages over one
    WebSocket, a handshake in one round trip, and frames acknowledged
    under a small window. Normative, with golden vectors.
- [Occamy Protocol (v0)](./protocol-cn.md)
  + TL;DR: The Guacamole protocol that occamyd v0.x speaks, in Chinese.
    v1.0 drops it with the v0 routes.
- [Configuration](./configuration.md)
  + TL;DR: Every key of a v1 configuration file, its default and how it is
    validated; a file without `version` is read as v0.
- [Benchmarks](./benchmarks.md)
  + TL;DR: How Occamy is measured, and the numbers protocol v1 must beat.
- [Codec measurement](./benchmarks/codecs.md)
  + TL;DR: Which pure-Go codecs, tile size and pixel layout the graphics
    pipeline uses, measured on frames recorded from the bench desktop.
- [Debugging and measuring](./debugging.md)
  + TL;DR: Profiling `occamyd` in debug mode, guacd's processes, and the
    pressure test.
- [Design specs](../specs/README.md)
  + TL;DR: Current status of the project and planned work.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)