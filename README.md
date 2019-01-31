# Occamy

Occamy is a modern remote desktop proxy written in Go.

## Why Occamy and how it works?

Occamy implements a generic remote desktop protocol with a modern approach, Go. 
It currently performs [Guacamole](https://guacamole.apache.org/) protocol and eventually 
intends to redesign and propose Occamy protocol.

The benefits of Occamy that differ from Guacamole are:

- Authentication supports
- Simplified architecture
- Modern with Go

Occamy server side currently simplifies Guacamole proxy and Guacamole servlet client 
in a single middleware application. Any client that involves Guacamole protocol and 
uses WebSocket for authentication can directly switch to interact to Occamy 
without any changes.

Read more details in [docs](./docs/README.md)

## Contributing

Easiest way to contribute is to provide feedback! 
We would love to hear what you like and what you think is missing.
PRs are welcome. Please follow the given PR template before you send your pull request.

## Development

```
make build
make run
make stop
```

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)