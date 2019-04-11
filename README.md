# Occamy

Occamy is a modern remote desktop proxy written in Go.

## Why Occamy and how it works?

Occamy implements a generic remote desktop protocol with a modern approach, i.e. Go. 
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

Read more details in [docs](./docs/README.md).

## Demo

<div align="center">
  <a href="https://www.youtube.com/watch?v=I3OJkFiebR4"><img src="https://img.youtube.com/vi/I3OJkFiebR4/0.jpg" alt="IMAGE ALT TEXT"></a>
</div>

## Routers

Occamy offers two APIs `/api/v1/login`, 
which distribute JWT tokens for authentication and `/api/v1/connect` 
for WebSocket based Guacamole connection. 
These two APIs are simple enough to serve all users.

If you build Occamy with web client, you can also access `/static` for web client demo.

## Contributing

Easiest way to contribute is to provide feedback! We would love to hear what you like and what you think is missing. PRs are welcome. Please follow the given PR template before you send your pull request.

## Development

- Build web client if you need:

    ```
    cd client/occamy-web
    npm install && npm run build
    ```

- Build Occamy docker image:

    ```
    make build
    make run
    make stop
    ```

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](./LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)