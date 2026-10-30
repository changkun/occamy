# Occamy Manual

You may interested in the following document:

- [Occamy Architecture](./architecture.md)
  + TL;DR: Occamy v0 authenticates with JWT and proxies each connection to
    guacd; v1, in progress, has its own protocol and backends.
- [Occamy Protocol (v0)](./protocol-cn.md)
  + TL;DR: The great thing about Occamy is that occamy has no handshake process 
    between client and Occamy, one can simply POST the connection 
    information to Occamy for  getting authentication tokens. 
- [Configuration](./configuration.md)
  + TL;DR: Every key of a v1 configuration file, its default and how it is
    validated; a file without `version` is read as v0.
- [Benchmarks](./benchmarks.md)
  + TL;DR: How Occamy is measured, and the numbers protocol v1 must beat.
- [Debugging and measuring](./debugging.md)
  + TL;DR: Profiling `occamyd` in debug mode, guacd's processes, and the
    pressure test.
- [Design specs](../specs/README.md)
  + TL;DR: Current status of the project and planned work.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)