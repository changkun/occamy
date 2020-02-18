<img src="./docs/occamy.png" alt="logo" height="150" align="right" />

# Occamy

[![Latest relsease](https://img.shields.io/github/v/tag/changkun/occamy?label=latest)](https://github.com/changkun/occamy/releases)
[![Build Status](https://github.com/changkun/occamy/workflows/Builds/badge.svg)](https://github.com/changkun/occamy/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/changkun/occamy)](https://goreportcard.com/report/github.com/changkun/occamy)

Occamy is an open source protocol and proxy for modern remote desktop control that written in Go.

## To start using Occamy

### Build

```
./guacamole/src/build-libguac.sh ./guacamole/ $(pwd)/guacamole/build/
go build -a -mod vendor -x -i -ldflags \
    "-X /occamy/config.Version=v$(git describe --always --tags) \
    -X /occamy/config.BuildTime=$(date +%F) \
    -X /occamy/config.GitCommit=$(git rev-parse HEAD)" \
    -o occamyd occamy.go
./occamyd
```

### APIs

Occamy offers two APIs:

- `/api/v1/login` distributes JWT tokens for authentication and
- `/api/v1/connect` is used for WebSocket based Occamy connection.

If you build Occamy with web client, you can also access `/static` for web client demo.

### Demo

To run a demo, you need build an occamy client first:

```
cd client/occamy-web
npm install && npm run build
```

With docker-compose, you should be able to run a working demo with:

```
make build
make run
make stop
```

Here is a working video demo:

<div align="center">
  <a href="https://youtu.be/e24WHo4Kpx8"><img src="https://img.youtube.com/vi/e24WHo4Kpx8/0.jpg" alt="IMAGE ALT TEXT"></a>
</div>

## Contributing

Easiest way to contribute is to provide feedback! We would love to hear 
what you like and what you think is missing. PRs are welcome. 
Please follow the given PR template before you send your pull request.

## Why Occamy and how it works?

Occamy implements a generic remote desktop protocol with modern approaches. 
It currently performs [Guacamole](https://guacamole.apache.org/) protocol 
and eventually intends to redesign and propose Occamy protocol.

The benefits of Occamy that differ from Guacamole are:

- Authentication supports
- Simplified architecture
- Streaming compression and optimization
- Modern with Go

Occamy server side currently simplifies Guacamole proxy and 
Guacamole servlet client in a single middleware application. 
Any client that involves Guacamole protocol and uses WebSocket 
for authentication can directly switch to interact to Occamy 
without any changes.

Read more details in [docs](./docs/README.md).

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)