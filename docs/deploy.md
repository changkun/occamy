# Deploy

This page takes you from a release to an Occamy that your users reach
over HTTPS. You run one program, `occamyd`, from the image or from a
release archive. It needs a configuration file and a place to keep its
signing key. Nothing else runs beside it.

## Get Occamy

Each [release](https://github.com/changkun/occamy/releases) publishes:

| What | Name | Holds |
|------|------|-------|
| The image | `ghcr.io/changkun/occamy:<tag>` | `occamyd` and the web client, for Linux on amd64 and arm64 |
| An archive of `occamyd` | `occamyd-<tag>-<os>-<arch>.tar.gz`, or `.zip` for Windows | `occamyd`, a `conf.yaml` to fill in, and the licences, for `linux`, `darwin` and `windows` on `amd64` and `arm64` |
| The web client | `occamy-web-<tag>.tar.gz` | The built web client, in `occamy-web-<tag>/dist` |
| Checksums | `SHA256SUMS` | The SHA-256 of each archive |

The tag keeps its `v`, such as `v1.0.0-beta.2`. Use the image wherever
you run containers, since it holds everything. Use the archives to run
`occamyd` as a service of the host.

To check a download, run these in the directory you downloaded to:

```sh
sha256sum -c --ignore-missing SHA256SUMS
gh attestation verify occamyd-v1.0.0-beta.2-linux-amd64.tar.gz --repo changkun/occamy
gh attestation verify oci://ghcr.io/changkun/occamy:v1.0.0-beta.2 --repo changkun/occamy
```

`gh attestation verify` checks that the repository's release workflow
built the file or the image.

The release also has [Occamy for macOS](macos-app.md), an app for your
users that opens their connections in windows of their own.

## Write the configuration

Write a file, `occamy.yaml`, that lists your users and the remotes they
may open:

```yaml
version: 1
listen: 0.0.0.0:5636
web_client: client/occamy-web/dist
auth:
  signing_key_file: /var/lib/occamy/signing.key
  users:
    - name: alice
      password_hash: "<the output of htpasswd>"
connections:
  - id: workstation
    name: Workstation
    protocol: vnc
    target: workstation.example.com:5900
    credentials: {password_env: WORKSTATION_PASSWORD}
    allow: {users: [alice]}
```

`web_client` is where the image keeps the web client. The file holds no
secrets: the workstation's password comes from the environment variable
`WORKSTATION_PASSWORD`. [Configuration](configuration.md) describes
every key, and each protocol's own.

## Make password hashes

The file holds a bcrypt hash of each user's password. Make it with
`htpasswd`:

```sh
htpasswd -nbBC 12 "" 'the password' | cut -d: -f2
```

Put the line it prints, which starts with `$2y$12$`, in double quotes as
the user's `password_hash`. `htpasswd` comes with Apache's tools:
`apache2-utils` on Debian and Ubuntu, `httpd-tools` on Fedora and RHEL,
and macOS has it. Give every user the same cost, the `12` after `-C`.

To sign users in through your identity provider instead, see
[Sign in through OIDC](#sign-in-through-oidc).

## Keep the signing key

`occamyd` signs the tokens it gives users with a key that it creates on
first start, at `auth.signing_key_file`. Keep that file on a volume.

- The image runs as the user `nonroot`, uid 65532, and its
  `/var/lib/occamy` belongs to that user. An empty named volume mounted
  there takes the same owner, so `-v occamy-data:/var/lib/occamy` needs
  nothing else.
- With the volume, tokens stay valid across restarts and new images.
  Without it, a new container makes a new key, and everyone signs in
  again.
- At start, the warning `created a new signing key` is expected the
  first time. On a server that has run before, it means the volume is
  missing.

If `occamyd` stops at start because it may not use the key, give the
directory to the uid that the error names.
[The signing key](configuration.md#the-signing-key) has the command for
a host directory, Docker and Podman.

## Run the container

```sh
podman run -d --name occamy -p 5636:5636 \
    -e WORKSTATION_PASSWORD \
    -v occamy-data:/var/lib/occamy \
    -v "$PWD/occamy.yaml:/occamy/conf.yaml:ro" \
    ghcr.io/changkun/occamy:v1.0.0-beta.2
```

With Docker, write `docker` in place of `podman`. Open
<http://localhost:5636/static/> and sign in as `alice`.

- The image reads `/occamy/conf.yaml`, so the command needs no
  arguments.
- `-e WORKSTATION_PASSWORD` passes the variable from your shell.
  `occamyd` refuses to start while a variable that the file names is
  unset or empty.
- Mount every file that the configuration names, such as a
  `password_file` or a TLS key, and make it readable by uid 65532.
- On a host with SELinux, such as Fedora, add `Z` to a bind mount's
  options, as in `:ro,Z`, so that the container may read it.
- `occamyd` speaks plain HTTP here. Put TLS in front before users reach
  it across a network, as the next section shows.

`podman logs occamy` shows `occamyd`'s log: what it serves, its
warnings, and one line per request, with the method, path, status,
duration and peer. It never logs a request's query string, headers or
body.

`podman stop occamy` sends `SIGTERM`. `occamyd` stops accepting
connections and ends every session, which tells each viewer that the
server is shutting down. It exits within 10 seconds, inside the time
that `podman stop` and `docker stop` allow. Sessions end at a restart,
and tokens stay valid.

## Run the binary

To run `occamyd` without a container, unpack `occamyd` and the web
client next to each other:

```sh
sudo mkdir -p /opt/occamy /etc/occamy
sudo tar -xzf occamyd-v1.0.0-beta.2-linux-amd64.tar.gz -C /opt/occamy
sudo tar -xzf occamy-web-v1.0.0-beta.2.tar.gz -C /opt/occamy
```

In the configuration, point `web_client` at the web client:

```yaml
web_client: /opt/occamy/occamy-web-v1.0.0-beta.2/dist
```

Then run it with `/opt/occamy/occamyd -conf /etc/occamy/occamy.yaml`.
On Linux, a systemd unit keeps it running, as a user of its own:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin occamy
sudo tee /etc/systemd/system/occamy.service >/dev/null <<'EOF'
[Unit]
Description=Occamy
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/occamy/occamyd -conf /etc/occamy/occamy.yaml
User=occamy
StateDirectory=occamy
EnvironmentFile=-/etc/occamy/env
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable --now occamy
```

`StateDirectory=occamy` gives `/var/lib/occamy`, where the signing key
lives, to the user `occamy`. Put variables such as
`WORKSTATION_PASSWORD=…` in `/etc/occamy/env`, readable by root only.
`journalctl -u occamy` shows the log.

On macOS and Windows, run `occamyd` the same way, with
`auth.signing_key_file` in a directory that its user may write.

## Serve it over TLS

`occamyd` speaks plain HTTP unless the configuration sets `tls`. Before
users reach it across a network, put TLS in front of it: a reverse
proxy, or `occamyd`'s own.

### Behind a reverse proxy

The proxy must:

- forward WebSocket upgrades, which carry every session;
- pass the `Host` header through, or else list the address that users
  open in [`origins`](configuration.md#server);
- keep a quiet WebSocket open for as long as a session lasts, since a
  stream carries nothing while the screen is still and nobody types;
- serve Occamy at the root of its host name, since the web client loads
  `/static/` and calls `/api/v1/` on its own origin.

[Caddy](https://caddyserver.com/) does all of it by default, and gets a
certificate for the name itself:

```
occamy.example.com {
	redir / /static/
	reverse_proxy 127.0.0.1:5636
}
```

With nginx:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name occamy.example.com;
    ssl_certificate     /etc/ssl/occamy/fullchain.pem;
    ssl_certificate_key /etc/ssl/occamy/privkey.pem;

    location = / {
        return 302 /static/;
    }
    location / {
        proxy_pass http://127.0.0.1:5636;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

Let only the proxy reach `occamyd`. On the same host, set
`listen: 127.0.0.1:5636`, or publish the container's port on the
loopback interface alone, with `-p 127.0.0.1:5636:5636`. `occamyd`'s log
then names the proxy as each request's peer, since it does not read
`X-Forwarded-For`.

### TLS in occamyd

To have `occamyd` serve HTTPS itself, name a certificate and its key,
in PEM:

```yaml
tls:
  cert: /etc/occamy/tls/fullchain.pem
  key: /etc/occamy/tls/privkey.pem
```

Mount the directory with `-v /etc/occamy/tls:/etc/occamy/tls:ro`, and
make both files readable by uid 65532. `occamyd` takes TLS 1.2 and
later. It reads the files at start, so restart it after you renew the
certificate.

## Check its health

`GET /healthz` answers `200` with `{"status":"ok"}` while `occamyd`
runs. It needs no token, and says only that `occamyd` answers.

```sh
curl -fsS http://localhost:5636/healthz
```

The image has no shell and no `curl`, so check it from outside the
container, such as from a load balancer or a Kubernetes probe:

```yaml
livenessProbe:
  httpGet: {path: /healthz, port: 5636}
```

Add `scheme: HTTPS` when `occamyd` serves TLS itself.
[Debugging and monitoring](debugging.md) describes the debug listener,
with `occamyd`'s counters and profiles.

## Sign in through OIDC

To sign users in through your identity provider, add `auth.oidc` as
[Signing in through OIDC](configuration.md#signing-in-through-oidc)
describes. Two of its keys depend on where users reach Occamy:

- `redirect_url` is the callback as browsers reach it, such as
  `https://occamy.example.com/api/v1/auth/oidc/callback`.
- `return_urls` must list the web client,
  `https://occamy.example.com/static/`. The default, the origin with the
  path `/`, is not the web client.

## TCP congestion control

On Linux, `occamyd` asks for BBR on its connections. BBR sends at the
rate it measures the path to carry, where the kernel's usual default,
CUBIC, cuts its rate at every lost packet. On a link that loses
packets, such as Wi-Fi or a long mobile path, a desktop in motion then
keeps its frame rate, and a key's effect shows sooner
([Benchmarks](benchmarks.md#step-3a-the-congestion-control)).

The kernel needs BBR's module loaded. `occamyd` needs no privilege to
use it then. In a container, it is the host's kernel, or the node's,
that loads it:

```sh
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/tcp_bbr.conf   # at every boot
```

At start, `occamyd` logs the congestion control its connections use,
such as `TCP congestion control: bbr`, and publishes it as
`tcp_congestion` at the debug listener's `/debug/vars`. Without the
module, it warns and keeps the kernel's choice:

```
warning: congestion_control bbr: the kernel has not loaded it: load its module, tcp_bbr; connections use cubic
```

Another congestion control may be one that the kernel allows only to
privileged processes. The warning then says to add it to
`net.ipv4.tcp_allowed_congestion_control`. To keep the host's own
choice, set `congestion_control: system`
([Configuration](configuration.md#tcp-congestion-control)).

## Upgrade

1. Read the release's notes on its
   [release page](https://github.com/changkun/occamy/releases).
2. Stop and remove the container, and start the new tag with the same
   volume and configuration:

    ```sh
    podman pull ghcr.io/changkun/occamy:<tag>
    podman stop occamy && podman rm occamy
    podman run -d --name occamy -p 5636:5636 \
        -e WORKSTATION_PASSWORD \
        -v occamy-data:/var/lib/occamy \
        -v "$PWD/occamy.yaml:/occamy/conf.yaml:ro" \
        ghcr.io/changkun/occamy:<tag>
    ```

3. If `occamyd` refuses the file, its errors name each key to change.
   [Upgrading](configuration.md#upgrading) lists what changed between
   versions.

Sessions end at the restart. The key on the volume keeps users' tokens
valid.
