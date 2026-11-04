# Deploy

Run Occamy for your team: `occamyd` on a server of yours, behind a
proxy that serves it over HTTPS, connected to the computers your users
work on. This page takes you from a release to that setup, and covers
Windows hosts and Kubernetes.

## How a deployment fits together

A deployment is one program, `occamyd`. It serves the web client, the
[HTTP API](api.md) and the WebSocket streams that carry sessions, and
it connects to each remote computer itself, with the credentials that
its configuration names. Your users reach it in a browser or in
[Occamy for macOS](macos-app.md).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="figures/deploy-topology-dark.svg">
  <img alt="A deployment. The web client in a browser and Occamy for macOS reach a TLS proxy, Caddy or nginx, over HTTPS on port 443. On the same server, the proxy reaches occamyd over HTTP/1.1 with the Upgrade header, on 127.0.0.1:5636. occamyd keeps conf.yaml, which it reads at start, signing.key on a volume, and an optional debug listener on localhost:6060. It connects to an identity provider for OIDC, optionally, and to each remote: a VNC server on port 5900, an RDP host on port 3389 with TLS, an SSH server on port 22, and a Kubernetes API server on port 443 or 6443." src="figures/deploy-topology.svg">
</picture>

`occamyd` keeps two files: its configuration, which it reads when it
starts, and the key it signs users' tokens with, which it creates when
it first starts. Sessions live in its memory, so a restart ends them,
but the signing key keeps your users signed in across restarts. Nothing
else runs beside it: there's no database and no other service.

### What each hop needs

| Hop | What it carries | What it needs |
|-----|-----------------|---------------|
| Users to the proxy | HTTPS on port 443: the web client, the API, and each session's WebSocket | A certificate that your users' devices trust, and nothing on the way that blocks WebSocket upgrades |
| The proxy to `occamyd` | HTTP/1.1, to port 5636 unless `listen` says otherwise | The `Upgrade` and `Connection` headers passed on, the `Host` header passed through, timeouts longer than 30 minutes, and Occamy at the root of its host name |
| `occamyd` to the remotes | VNC on port 5900, RDP on 3389, SSH on 22, and HTTPS to Kubernetes API servers on 443 or 6443 | Outgoing connections from `occamyd`'s host, and each remote's certificate or host key, pinned or signed by a CA that you name |
| `occamyd` to your identity provider | HTTPS, when users sign in through OIDC | Outgoing connections to the provider |

`occamyd` speaks HTTP/1.1 only, because its streams are WebSockets over
HTTP/1.1. A stream carries nothing while the remote's screen is still
and nobody types, and a session ends after 30 minutes without input, so
a proxy must keep a quiet connection open for at least that long. A
proxy that closes it sooner disconnects the session's viewers, whose
page then says **Connection lost**.
[Serve it over TLS](#serve-it-over-tls) shows a proxy configuration
that does all of this.

## Get Occamy

Each [release](https://github.com/changkun/occamy/releases) publishes:

| What | Name | Holds |
|------|------|-------|
| The image | `ghcr.io/changkun/occamy:<tag>` | `occamyd` and the web client, for Linux on amd64 and arm64 |
| An archive of `occamyd` | `occamyd-<tag>-<os>-<arch>.tar.gz`, or `.zip` for Windows | `occamyd`, a `conf.yaml` to fill in, and the licences, for `linux`, `darwin` and `windows` on `amd64` and `arm64` |
| The web client | `occamy-web-<tag>.tar.gz` | The built web client, in `occamy-web-<tag>/dist` |
| Checksums | `SHA256SUMS` | The SHA-256 of each archive |

The tag keeps its `v`, as in `v1.0.0-beta.2`. Use the image wherever
you run containers, since it holds everything. Use the archives to run
`occamyd` as a service of the host. The release also has Occamy for
macOS, an app for your users that opens their connections in windows
of their own.

To check a download, run these in the directory you downloaded it to.
`gh attestation verify` checks that the repository's release workflow
built the file or the image:

```sh
sha256sum -c --ignore-missing SHA256SUMS
gh attestation verify occamyd-v1.0.0-beta.2-linux-amd64.tar.gz --repo changkun/occamy
gh attestation verify oci://ghcr.io/changkun/occamy:v1.0.0-beta.2 --repo changkun/occamy
```

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
every key, and those of each protocol.

### Make password hashes

The file holds a bcrypt hash of each user's password, which `htpasswd`
makes:

```sh
htpasswd -nbBC 12 "" 'the password' | cut -d: -f2
```

Put the line it prints, which starts with `$2y$12$`, in double quotes as
the user's `password_hash`. Give every user the same cost, the `12`
after `-C`, because otherwise the time a sign-in takes tells which
users exist. `htpasswd` comes with Apache's tools: `apache2-utils` on
Debian and Ubuntu, `httpd-tools` on Fedora and RHEL, and macOS has it.

To sign users in through your identity provider instead, see
[Sign in through OIDC](#sign-in-through-oidc).

### Keep the signing key

`occamyd` signs the tokens it gives users with a key that it creates
when it first starts, at `auth.signing_key_file`. Keep that file on a
volume, so that tokens stay valid when you restart `occamyd` or move to
a new image. Without the volume, a new container makes a new key, and
everyone has to sign in again.

The image runs as the user `nonroot`, uid 65532, and its
`/var/lib/occamy` belongs to that user. An empty named volume mounted
there takes the same owner, so `-v occamy-data:/var/lib/occamy` needs
nothing else.

The warning `created a new signing key` is expected the first time
`occamyd` starts. On a server that has run before, it means the volume
is missing. If `occamyd` stops at start because it may not use the key,
give the directory to the uid that the error names, as [The signing
key](configuration.md#the-signing-key) shows for a host directory,
Docker and Podman.

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

The image reads `/occamy/conf.yaml`, so the command needs no arguments.
`-e WORKSTATION_PASSWORD` passes the variable on from your shell, and
`occamyd` refuses to start while a variable that the file names is
unset or empty. Mount every other file that the configuration names,
such as a `password_file` or a TLS key, and make it readable by uid
65532. On a host with SELinux, such as Fedora, add `Z` to a bind
mount's options, as in `:ro,Z`, so that the container may read it.

`occamyd` speaks plain HTTP here. Put TLS in front of it before users
reach it across a network, as [Serve it over TLS](#serve-it-over-tls)
shows.

`podman logs occamy` shows `occamyd`'s log: what it serves, its
warnings, and a line for each request
([The log](debugging.md#the-log)).

`podman stop occamy` sends `SIGTERM`. `occamyd` then stops accepting
connections and ends every session, which tells each viewer that the
server is shutting down, and it exits within 10 seconds, inside the
time that `podman stop` and `docker stop` allow. Tokens stay valid
across the restart.

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

A proxy must forward WebSocket upgrades, keep a quiet connection open
for longer than 30 minutes, and serve Occamy at the root of its host
name, because the web client loads `/static/` and calls `/api/v1/` on
its own origin. It must also pass the `Host` header through: `occamyd`
accepts a stream from a web page only when the page's origin matches
the request's `Host`. If your proxy rewrites `Host`, list the address
your users open in [`origins`](configuration.md#server).

Let only the proxy reach `occamyd`. On the same host, set
`listen: 127.0.0.1:5636`, or publish the container's port on the
loopback interface alone, with `-p 127.0.0.1:5636:5636`. `occamyd`
doesn't read `X-Forwarded-For`, so its log then names the proxy as each
request's peer.

### Behind Caddy

[Caddy](https://caddyserver.com/) forwards WebSockets and keeps them
open with its defaults, and gets a certificate for the name itself:

```
occamy.example.com {
	redir / /static/
	reverse_proxy 127.0.0.1:5636
}
```

### Behind nginx

nginx needs to be told to speak HTTP/1.1 to `occamyd`, to pass on the
upgrade, and to wait longer than its default of a minute on a quiet
connection:

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
later. It reads the files when it starts, so restart it after you renew
the certificate.

## Connect Windows hosts

`occamyd` connects to Windows over RDP, signing in with Network Level
Authentication (NLA). A Windows host needs Remote Desktop turned on, a
connection that turns off RDP's graphics pipeline, and its certificate
pinned.

1. On the Windows host, turn on Remote Desktop in **Settings** >
   **System** > **Remote Desktop**, and leave **Require devices to use
   Network Level Authentication to connect** on. Windows Home can't be
   a Remote Desktop host. Let `occamyd`'s host reach port 3389, which
   Windows opens in its firewall when you turn Remote Desktop on.

2. Add a connection with `disable_gfx: true`:

    ```yaml
      - id: windows
        name: Windows workstation
        protocol: rdp
        target: 10.0.4.20
        disable_gfx: true
        credentials: {username: 'CORP\alice', password_file: /run/secrets/windows-password}
        allow: {users: [alice]}
    ```

    Under the graphics pipeline, Windows draws nearly all of its screen
    with codecs that `occamyd` doesn't decode, so the screen would stay
    nearly black. A `username` of the form `DOMAIN\user` signs in to
    that domain. NLA signs in with NTLMv2, not Kerberos, so a domain that
    refuses NTLM refuses `occamyd` too.

3. Open the connection once. Windows signs its RDP certificate itself
   unless you give it one, so `occamyd` refuses it, and the web client
   says **Could not connect**: “The server doesn't trust the remote
   computer's certificate.” The log line that ends the session
   names the certificate and its SHA-256 fingerprint, as in
   `the server's certificate "CN=WS-0420" (SHA-256 3F:A1:07:…) is not trusted`.

4. Pin that fingerprint on the connection, restart `occamyd`, and open
   the connection again:

    ```yaml
        tls: {pins: ["3F:A1:07:5C:92:E4:6B:D8:21:0A:F3:8E:57:C6:19:B4:E2:7D:40:95:A8:1C:6F:D3:0B:72:E9:58:34:AC:66:1F"]}
    ```

An ad-hoc RDP target always asks for the graphics pipeline, since only
a connection can set `disable_gfx`, so reach Windows hosts through a
connection. [Windows hosts](configuration.md#windows-hosts) describes
the keys, and [RDP](configuration.md#rdp) the rest of what an RDP
connection takes.

## Kubernetes

`occamyd` can open shells in your cluster's pods, and it can run in a
cluster itself.

### Open shells in pods

A kubernetes connection opens a terminal in a pod, as `kubectl exec -it`
does. `occamyd` takes no kubeconfig. Give it the API server's address,
the CA that signed the API server's certificate, and the token of a
ServiceAccount that may exec into the pods:

```sh
kubectl -n web create serviceaccount occamy
kubectl -n web create role occamy-exec --verb=get,create --resource=pods/exec
kubectl -n web create rolebinding occamy-exec --role=occamy-exec --serviceaccount=web:occamy
kubectl -n web create token occamy --duration=24h > /etc/occamy/k8s-token
kubectl config view --raw --minify \
  -o 'jsonpath={.clusters[0].cluster.certificate-authority-data}' | base64 -d > /etc/occamy/k8s-ca.crt
```

```yaml
  - id: web-shell
    name: web-0
    protocol: kubernetes
    target: k8s.internal:6443
    kubernetes: {namespace: web, pod: web-0}
    tls: {ca_file: /etc/occamy/k8s-ca.crt}
    credentials: {token_file: /etc/occamy/k8s-token}
    allow: {users: [alice]}
```

`occamyd` reads the token file as each session starts, so a token that
you renew there serves the next session. [Kubernetes
pods](configuration.md#kubernetes-pods) describes the connection's
keys, a token that doesn't expire, and the permissions in full.

When `occamyd` runs in the same cluster, it can use its own pod's
ServiceAccount instead. Bind the Role to that ServiceAccount, and point
the connection at the token and the CA that Kubernetes mounts into
every pod. Kubernetes renews that token, and `occamyd` reads it afresh
for each session:

```yaml
    target: kubernetes.default.svc
    tls: {ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt}
    credentials: {token_file: /var/run/secrets/kubernetes.io/serviceaccount/token}
```

### Run occamyd in a cluster

The image runs in Kubernetes as it does anywhere else, and the hops it
needs are the same as on a host of its own. These parts of a
Deployment are particular to Occamy:

```yaml
spec:
  replicas: 1
  strategy: {type: Recreate}
  template:
    spec:
      securityContext: {fsGroup: 65532}
      containers:
        - name: occamyd
          image: ghcr.io/changkun/occamy:v1.0.0-beta.2
          ports: [{containerPort: 5636}]
          volumeMounts:
            - {name: config, mountPath: /occamy/conf.yaml, subPath: conf.yaml}
            - {name: data, mountPath: /var/lib/occamy}
          livenessProbe:
            httpGet: {path: /healthz, port: 5636}
```

- **One replica.** Each `occamyd` keeps its sessions, and the sign-ins
  of OIDC in progress, in its own memory, so every request about a
  session must reach the `occamyd` that runs it. `Recreate` stops the
  old pod before the new one starts, and with it the sessions, as any
  restart does.
- **The signing key on a volume.** Mount a PersistentVolumeClaim at
  `/var/lib/occamy`. `fsGroup: 65532` lets the image's user write to
  it.
- **The configuration from a ConfigMap** at `/occamy/conf.yaml`, and
  passwords from a Secret, as environment variables or files.
- **The ingress or Gateway** forwards WebSocket upgrades and keeps
  quiet connections open for longer than 30 minutes, as a proxy must
  ([What each hop needs](#what-each-hop-needs)).
- **The debug listener**, if you turn it on, stays on `localhost:6060`,
  where `kubectl port-forward` reaches it
  ([The debug listener](debugging.md#the-debug-listener)).

## Check its health

`GET /healthz` answers `200` with `{"status":"ok"}` while `occamyd`
serves requests. It needs no token, and it says nothing about whether
the remotes can be reached.

```sh
curl -fsS http://localhost:5636/healthz
```

The image has no shell and no `curl`, so check it from outside the
container, such as from a load balancer or with a Kubernetes probe, as
in [Run occamyd in a cluster](#run-occamyd-in-a-cluster). Add
`scheme: HTTPS` to the probe when `occamyd` serves TLS itself.
[Debugging and monitoring](debugging.md) describes the log and the
debug listener, with `occamyd`'s counters and profiles.

## Sign in through OIDC

To sign users in through your identity provider, add `auth.oidc` as
[Signing in through OIDC](configuration.md#signing-in-through-oidc)
describes. Two of its keys depend on the address where users reach
Occamy:

- `redirect_url` is the callback as browsers reach it, such as
  `https://occamy.example.com/api/v1/auth/oidc/callback`.
- `return_urls` must list the web client,
  `https://occamy.example.com/static/`. Its default, the origin with
  the path `/`, isn't the web client.

## TCP congestion control

On Linux, `occamyd` asks for BBR on its connections. BBR sends at the
rate it measures the path to carry, whereas the kernel's usual default,
CUBIC, cuts its rate at every lost packet. On a link that loses
packets, such as Wi-Fi or a long mobile path, a desktop in motion then
keeps its frame rate, and a key's effect shows sooner
([Benchmarks](benchmarks.md#bbr-against-cubic)).

The kernel needs BBR's module loaded, and then `occamyd` needs no
privilege to use it. In a container, it's the host's kernel, or the
node's, that loads it:

```sh
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/tcp_bbr.conf   # at every boot
```

When it starts, `occamyd` logs the congestion control that its
connections use, such as `TCP congestion control: bbr`, and publishes
it as `tcp_congestion` at the debug listener's `/debug/vars`. Without
the module, it warns and keeps the kernel's choice:

```
warning: congestion_control bbr: the kernel has not loaded it: load its module, tcp_bbr; connections use cubic
```

The kernel may also allow a congestion control only to privileged
processes, and the warning then says to add it to
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

Sessions end at the restart, and the key on the volume keeps users'
tokens valid.
