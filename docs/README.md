# Occamy Manual

Every document, by what you want to do.

## Get started

- [Try Occamy](./try-it.md): run the demo stack on your machine and open
  a desktop over VNC and RDP and a shell over SSH in the browser.
- [Deploy](./deploy.md): run `occamyd` for your users, with a
  configuration of your own.

## Use

- [The web client](./web-client.md): sessions, roles, sharing, the
  clipboard, resizing, and what each message means.
- [Occamy for macOS](./macos-app.md): install the Mac app, add a server,
  and open its connections in windows of their own.

## Operate

- [Configuration](./configuration.md): every key of the configuration
  file, its default, and how it is checked.
- [Debugging and monitoring](./debugging.md): the health check, the log,
  common problems, and profiling `occamyd`.

## Reference

- [HTTP API](./api.md): sign-in, connections, sessions, shares and
  streams, for writing a client. [openapi.yaml](./openapi.yaml) has
  the same routes in OpenAPI 3.1.
- [Protocol](./protocol.md): the wire protocol between `occamyd` and its
  clients, with test vectors.
- [Architecture](./architecture.md): how `occamyd`, its backends and the
  clients fit together.

## Performance

- [Benchmarks](./benchmarks.md): Occamy measured against Guacamole, with
  intervals and corrected tests.
- [Codecs](./benchmarks/codecs.md): which codecs, tile size and pixel
  layout `occamyd` uses, measured on frames from a real desktop.

## Develop

- [Development](./development.md): build from source and run every test
  suite.
- [Design specs](../specs/README.md): how each part was designed, and
  the work planned.

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019-2026 [Ou Changkun](https://changkun.de)
