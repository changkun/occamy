# All documents

Every page of Occamy's documentation, grouped as the website's menu is,
with what each one helps you do.

## Get started

- [Try Occamy](./try-it.md): run the demo on your machine, and open a
  desktop over VNC and RDP, a shell over SSH, and a Kubernetes pod in
  your browser.
- [Deploy](./deploy.md): run `occamyd` for your users, with your own
  configuration, a signing key and TLS.

## Use

- [The web client](./web-client.md): sign in, open a session, share it,
  copy and paste, resize it, and understand why a session ended.
- [Occamy for macOS](./macos-app.md): install the Mac app, add a
  server, and open its connections in windows of their own.

## Operate

- [Configuration](./configuration.md): look up any key of the
  configuration file, its default, and what `occamyd` refuses.
- [Debugging and monitoring](./debugging.md): check `occamyd`'s health,
  read its log, solve common problems, and profile it.

## Reference

- [HTTP API](./api.md): sign in, list connections, and open, join and
  share sessions from a client of your own. [openapi.yaml](./openapi.yaml)
  describes the same routes in OpenAPI 3.1.
- [Protocol](./protocol.md): implement the wire protocol that carries a
  session between a client and `occamyd`, with test vectors.
- [Architecture](./architecture.md): learn how `occamyd` is built, and
  how a key press and a frame travel through it.

## Performance

- [Benchmarks](./benchmarks.md): see how Occamy compares with Guacamole
  on the same remotes and networks, and run the comparison yourself.
- [Codecs](./benchmarks/codecs.md): see how `occamyd` picks each tile's
  codec, and the measurements on frames from a real desktop behind each
  choice.

## Develop

- [Development](./development.md): build Occamy from source, run each
  of its test suites, preview this website, and cut a release.
