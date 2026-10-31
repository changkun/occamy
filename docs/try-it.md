# Try Occamy

This guide takes a fresh checkout of `master` to a running Occamy v1.
It covers these steps:

1. build `occamyd` and the web client;
2. start the demo stack;
3. open one connection of each protocol in the browser;
4. try sharing, the clipboard, resizing and reconnecting;
5. run every test suite;
6. tear it all down.

Run each command from the repository's root unless a step says
otherwise. v1 is not released yet. The released v0.x servers are
described in the [README](../README.md#v0-released).

## 1. What you need

| What | Version | For |
|------|---------|-----|
| [Go](https://go.dev/dl/) | 1.26.4 or later, as `go.mod` says | `occamyd` and the Go tests. No C toolchain: `occamyd` builds with `CGO_ENABLED=0` |
| [Podman](https://podman.io) with a compose provider, or Docker with Compose v2 | | the image, the demo stack and the integration tests |
| [Node.js](https://nodejs.org) with npm | 22.12 or later | the web client and its tests |
| `make`, `git`, `curl` | | |
| [golangci-lint](https://golangci-lint.run) | v2 | `make lint` only |
| A browser | | the web client |

`make` runs `podman` unless `CONTAINER_ENGINE` names another engine:

```
make build CONTAINER_ENGINE=docker    # for one command
export CONTAINER_ENGINE=docker        # for every make command in this shell
```

The integration tests in [section 7](#integration-tests) read
`OCCAMY_CONTAINER_ENGINE` instead. The commands in this guide say
`podman`; with Docker, write `docker` in its place. `podman compose` hands its work to a
compose provider, docker-compose or podman-compose, which must be
installed; `podman compose version` names the one it found. On macOS
and Windows, the engine runs in a virtual machine, which needs a few GiB
of memory for the stack's seven containers.

Check what you have:

```
go version
podman version
podman compose version
node --version
```

## 2. Build

```
make compile    # ./occamyd: CGO_ENABLED=0 go build -trimpath
make client     # the web client: npm ci and npm run build, into client/occamy-web/dist
make build      # the image occamy:latest, which the demo stack runs
```

`make build` builds `occamyd` and the web client again, in stages of
its own ([docker/Dockerfile](../docker/Dockerfile)), so the demo stack
needs only the image. `./occamyd` and `client/occamy-web/dist` are for
running `occamyd` outside a container, as [the Kubernetes
step](#kubernetes) does. `./occamyd -h` lists its one flag, `-conf`, the
configuration file, `./conf.yaml` by default.

## 3. Start the demo stack

```
make run
```

`make run` builds the demo servers' images, which takes several
minutes the first time. It then starts the stack in
[docker/docker-compose.yaml](../docker/docker-compose.yaml) and returns
once every service is healthy. Run `make build` before it: the stack
runs the image that `make build` makes.

| Service | On the host | What it is |
|---------|-------------|------------|
| `occamy-v1` | `127.0.0.1:5637` | `occamyd` with the v1 file [docker/occamy-v1.yaml](../docker/occamy-v1.yaml): API v1, protocol v1 and the web client |
| `occamy` | `127.0.0.1:5636` | `occamyd` with the v0 file [docker/occamy.yaml](../docker/occamy.yaml), for clients of the Guacamole protocol. The web client does not use it, and v1.0 removes it |
| `guacd` | not published | guacd 1.6, for the v0 service and for the v1 connections that set `backend: guacd` |
| `vnc`, `tigervnc`, `ssh`, `rdp` | not published | the demo servers: x11vnc, TigerVNC, OpenSSH and xrdp, on Debian |

The stack publishes both `occamyd` services on the host's loopback
interface only, since anyone who has read this guide knows the demo
password. To reach it from another machine, publish them on every
interface with `make run OCCAMY_BIND=0.0.0.0`, or on one address of
the host's, and only on a network you trust. Set `OCCAMY_BIND` on
`make`'s command line or export it, since `make run` does not read it
from `docker/.env`. `make run OCCAMY_DEBUG=1` refuses any address but
`127.0.0.1`, since the debug routes have no authentication.

Open <http://localhost:5637/static/> and sign in as `demo` with the
password `occamy`. **Connections** lists 23 connections, and the bar
says you are signed in as demo. The same sign-in from the command line
returns an access token:

```
curl -s localhost:5637/api/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo","password":"occamy"}'
```

Where things live:

- **The configuration** is [docker/occamy-v1.yaml](../docker/occamy-v1.yaml).
  The container mounts it read-only at `/occamy/v1.yaml`.
  [configuration.md](configuration.md) describes every key. To apply an
  edit, restart the service: `(cd docker && podman compose restart occamy-v1)`.
- **The signing key** is `/run/occamy/signing.key` in the container,
  on a tmpfs. `occamyd` creates it when it starts, so a restart of
  `occamy-v1` signs everyone out and ends every session.
- **The demo servers' passwords** are in the environment that
  `docker-compose.yaml` gives `occamy-v1` (`OCCAMY_DEMO_*`). The v1 file
  names the variables in `password_env`, so it holds no password.
- **The logs**: `(cd docker && podman compose logs -f occamy-v1)`.
  Each session's first line names the connection and the backend that
  serves it: `user "demo" opened connection "vnc-native" through vnc`.

### Next to another stack

On a host where another stack already uses the ports or the names, as
on a shared machine, give this stack its own:

| Variable | Default | What it sets |
|----------|---------|--------------|
| `COMPOSE_PROJECT_NAME` | `docker`, the compose file's directory | the compose project: the names of the containers, the network, the volume and the demo images |
| `OCCAMY_PORT` | `5636` | the v0 service's port |
| `OCCAMY_V1_PORT` | `5637` | `occamy-v1`'s port |
| `OCCAMY_V1_DEBUG_PORT` | `5638` | `occamy-v1`'s debug listener, when it is on |
| `OCCAMY_IMAGE` | `occamy:latest` | the image both `occamyd` services run |

Export them in the shell. Every `make` target and `podman compose`
command in this guide then uses them, `make e2e` and `make soak`
included:

```
export COMPOSE_PROJECT_NAME=occamy-try OCCAMY_PORT=15636 OCCAMY_V1_PORT=15637 \
  OCCAMY_V1_DEBUG_PORT=15638 OCCAMY_IMAGE=occamy-try:latest
make build IMAGE=occamy-try
make run
```

The web client is then at <http://localhost:15637/static/>. Replace the
ports in this guide's URLs and `curl` commands to match.

## 4. One connection per protocol

Open each connection from **Connections**. The session's bar shows the
connection's name and your role, `owner`. **End session** closes it
and returns to the list.

| Protocol | Connection | What you should see |
|----------|------------|---------------------|
| VNC | **x11vnc, native backend** (`vnc-native`) | A black desktop with two xterms and no window manager. The small one at the top left says "Lines typed here reach the clipboard as hex"; it is a probe of the end-to-end tests. The large one has a root shell, `root@…:/#`. x11vnc does not resize its screen, so the page scales the 1024×768 desktop to the window. Keys go to the window under the pointer, as there is no window manager |
| VNC | **TigerVNC, native backend** (`tigervnc-native`) | The same desktop, served by TigerVNC, which resizes its screen to the window |
| SSH | **SSH** (`ssh`) | A terminal with Debian's login banner and the prompt `occamy@…:~$`. It fills the window, and its rows and columns follow the window (`stty size` shows them) |
| RDP | **RDP, native backend** (`rdp-native`) | xrdp's X session: a black desktop with one xterm at the top left and the prompt `occamy@…:~$`. The desktop resizes to the window. The first connection after the stack or the `rdp` service starts creates the X session, so the xterm takes a few seconds to appear; wait for it before you type. Later connections return to the same X session |
| RDP | **RDP, through guacd** (`rdp-guacd`) | The same X session through guacd. It keeps the size it opened at, and the page scales it |
| Kubernetes | none in the demo stack | A terminal in a pod. See [Kubernetes](#kubernetes) below for a cluster to try it with |

The other VNC connections reach the servers' other screens: no
password, VeNCrypt, and the German and French keyboard layouts.
**TigerVNC, asking for the password** opens a form for the password
first, which is `vncpassword`.

The native RDP backend asks xrdp for plain bitmap updates, not the
graphics pipeline (GFX), and has been tested against xrdp, not yet
against Windows.

### Kubernetes

The demo stack has no cluster. This step starts one, k3s in a
container, with a busybox pod and a ServiceAccount that may only exec
into it. It then runs `./occamyd` from [section 2](#2-build) on
`localhost:5639` with a v1 file of its own. The k3s container is
privileged and needs the network to pull busybox. It is the setup of
the backend's integration test. On a shared host, as in [Next to
another stack](#next-to-another-stack), give the container a name of
its own in place of `occamy-k3s` in every command of this step and of
[Tear down](#8-tear-down), and other ports if something already
listens on 16443 or 5639.

```
K8S=$(mktemp -d)
podman run -d --name occamy-k3s --privileged -p 127.0.0.1:16443:6443 \
  docker.io/rancher/k3s:v1.34.1-k3s1 server \
  --disable=traefik --disable=metrics-server --disable=servicelb --tls-san=127.0.0.1
until podman exec occamy-k3s kubectl get --raw /readyz; do sleep 2; done
```

The loop prints errors while k3s starts, and `ok` once its API server
is ready, usually within half a minute. Then add the pod,
the ServiceAccount `occamy`, and a Role that lets it exec into pods of
the namespace and nothing else. `pods/exec` and `pods/attach` take
`get` and `create`, as [configuration.md](configuration.md#kubernetes-pods)
says. The first `apply` may fail while the cluster starts, so the loop
tries again:

```
cat > "$K8S/manifest.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata: {name: occamy-try}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: occamy, namespace: occamy-try}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: occamy-exec, namespace: occamy-try}
rules:
  - apiGroups: [""]
    resources: [pods/exec, pods/attach]
    verbs: [get, create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: occamy-exec, namespace: occamy-try}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: occamy-exec}
subjects: [{kind: ServiceAccount, name: occamy, namespace: occamy-try}]
---
apiVersion: v1
kind: Pod
metadata: {name: shell, namespace: occamy-try}
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: box
      image: docker.io/library/busybox:1.37
      command: [sleep, "86400"]
EOF
until podman exec -i occamy-k3s kubectl apply -f - < "$K8S/manifest.yaml"; do sleep 2; done
podman exec occamy-k3s kubectl -n occamy-try wait --for=condition=Ready pod/shell --timeout=300s
```

`occamyd` takes no kubeconfig. Give it the cluster's CA, a token of the
ServiceAccount, and a v1 file:

```
podman exec occamy-k3s kubectl config view --raw \
  -o 'jsonpath={.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$K8S/ca.pem"
podman exec occamy-k3s kubectl -n occamy-try create token occamy --duration=1h > "$K8S/token"
cat > "$K8S/occamy.yaml" <<EOF
version: 1
listen: 127.0.0.1:5639
web_client: client/occamy-web/dist
auth:
  signing_key_file: $K8S/signing.key
  users:
    - name: demo
      password_hash: "\$2a\$10\$DXEVjCzdy7mEP0VBuqhlqunMrnP3lSGh0/kl6yLwA5Rhrl4hLhjPu"
connections:
  - id: k3s-shell
    name: busybox in k3s
    protocol: kubernetes
    target: 127.0.0.1:16443
    kubernetes: {namespace: occamy-try, pod: shell}
    tls: {ca_file: $K8S/ca.pem}
    credentials: {token_file: $K8S/token}
    allow: {users: [demo]}
EOF
./occamyd -conf "$K8S/occamy.yaml"
```

`occamyd` creates the signing key in `$K8S`, logs `serving API v1 at
http://127.0.0.1:5639` and keeps running. In a browser, open
<http://localhost:5639/static/>, sign in as `demo` with the password
`occamy`, and open **busybox in k3s**. Busybox has no bash, so
`occamyd` runs `sh`, and you should see BusyBox's prompt, `/ #`.
`hostname` prints `shell`, the pod's name. The token expires after an
hour; a new one in `$K8S/token` serves the next session. Stop `occamyd`
with Ctrl-C.

## 5. Features

### Sharing

1. Open **x11vnc, native backend**. You are its owner.
2. Click **Share**. Under **Others join**, pick **Read-only: they
   watch**, keep **Link expires after** as it is, and click **Create
   link**, then **Copy link**. The link is the page's address with
   `#join=<session>&token=<share>`.
3. Open the link in a private window or another browser, so that it
   does not share your sign-in. It joins without a sign-in, and the
   bar's role says `read-only`. The owner's bar shows **1 other
   viewer**; click it to list them.
4. In the read-only window, type and click: nothing reaches the
   desktop.
5. In the owner's window, click **Create another**, pick **Viewer:
   they also type, point and use the clipboard**, and open that link
   too. Its role is `viewer`, and its keys and clicks reach the
   desktop.
6. Click **End session** in the owner's window. Both viewers see
   "Session ended" and "The owner ended the session."

When the owner closes the tab instead of ending the session,
`occamyd` keeps the session for 60 seconds, the owner's grace. The
viewers keep watching. If the owner does not come back in time, they
see "The owner left and did not come back in time."

### Clipboard

On **x11vnc, native backend**, **TigerVNC, native backend** or
**RDP, native backend**:

1. **From the remote to the browser.** With the pointer over the
   remote xterm, type `echo hello from the remote | xclip -selection
   clipboard` there. Click **Clipboard**: the box shows `hello from the
   remote`. **Copy** puts it on your own clipboard.
2. **From the browser to the remote.** Replace the box's text with
   `hello from the browser` and click **Send**. The panel says "Sent to
   the remote clipboard." In the remote xterm, type `xclip -o
   -selection clipboard; echo`, and it prints `hello from the browser`.
   **Paste local** fills the box from your own clipboard where the
   browser allows it. When its window gets the focus, the page also
   sends your clipboard to the remote, if the browser lets it read the
   clipboard without asking.

The VNC desktops also answer the clipboard by themselves: send
`owner: hi`, and the remote's clipboard becomes `echo: hi` a moment
later. The RDP desktop does the same with `occamy-e2e:hi`, which comes
back as `echo:occamy-e2e:hi`. The panel keeps the text you sent in its
box and says "Edited: the remote clipboard holds other text."; **Show
it** shows the answer. A read-only viewer sees the remote clipboard
but cannot send to it.

If the RDP desktop's **Clipboard** panel never changes, xrdp's
clipboard process, `xrdp-chansrv`, has probably exited. xrdp starts it
only with a new X session, and it can exit when a connection closes
within moments of opening. Restart the RDP server to get a new one:
`(cd docker && podman compose restart rdp)`. The next connection then
creates a new X session, so wait for its xterm before you type.

The demo x11vnc runs under a watchdog, which restarts it when it hits
an X error. A freshly started x11vnc drops the first clipboard text it
gets, so if nothing arrives on a VNC desktop, send it again.

### Resize and reconnect

**Resize.** Resize the browser window, or click **Full screen**:

- On **TigerVNC, native backend** and **RDP, native backend**, the
  remote desktop changes size to fill the window.
- On **SSH**, the terminal gains or loses rows and columns: run
  `stty size` before and after.
- **x11vnc, native backend** keeps 1024×768, and the page scales it.
  x11vnc refuses to resize, and `occamyd` logs `the server prohibits
  resizing the screen` once and stops asking. The demo x11vnc runs
  under a watchdog that restarts it after an X error, which can come
  right after the refusal. A session that x11vnc drops ends with
  "Remote unreachable"; open it again.
- The connections through guacd keep the size they opened at.

Only the owner's window sets the size. A viewer sees the owner's
screen scaled to its own window.

**Reconnect.** A session outlives the owner's connection to `occamyd`.
When the page loses its connection while the session runs on, it says
"Connection lost" and offers **Join again**. The owner then has 60
seconds to come back to the same session, with the remote's state as
it was. A newer connection of the owner takes the session over from
an older one. The older page then says "Opened elsewhere" and offers
**Join again** to take it back. Reloading the page does not rejoin: it
asks for a sign-in, and the session ends when the 60 seconds pass.

To watch an owner leave and come back without breaking a network, run
the end-to-end test that does it against the running stack:

```
OCCAMY_E2E_V1_ENDPOINT=localhost:5637 go test -count=1 -v -run 'TestE2EV1RDP/reconnect' ./tests/
```

It opens **RDP, native backend**, closes the owner's stream, waits
until the session lists no viewer, and opens the session's own stream
again. It passes when it returns to the same session as its owner.

### The guacd fallback

vnc and rdp connections get `occamyd`'s own clients, its native
backends, by default. A connection that sets `backend: guacd` goes
through guacd instead. That is the fallback for a server that the
native backend cannot serve yet, such as a Windows RDP server until
the native RDP backend is verified against one. It is also the
baseline that the benchmark compares the native backends against. The
fallback goes away in v1.0, with guacd
([configuration.md](configuration.md#backends)).

To compare the two, open **x11vnc, through guacd** (`vnc-guacd`) and
**RDP, through guacd** (`rdp-guacd`) next to their native twins. Each
shows the same desktop. Through guacd, the first frame takes a second
or two instead of a fraction of one, and the desktop keeps the size it
opened at. The log names the backend of each session:

```
(cd docker && podman compose logs occamy-v1) | grep 'opened connection'
```

The native ones say `through vnc` or `through rdp`, and the others say
`through guacd`. To send a connection of your own through guacd, add
`backend: guacd` to it in `docker/occamy-v1.yaml` and restart
`occamy-v1`. A connection with `tls` cannot do so, since guacd does not
check certificates as `tls` asks.

## 6. Unit tests, lint and the web client's tests

```
go test -race ./...
```

The unit tests need no stack and no container engine. They take a few
minutes with the race detector.

```
golangci-lint version    # must say version 2
make lint
```

`make lint` runs golangci-lint with [.golangci.yml](../.golangci.yml),
as CI does, and again on the codec measurement in `bench/codecs` with
its `measure` build tag. golangci-lint v1 cannot read the
configuration.

The web client's tests run in `client/occamy-web`, as CI's web job
runs them. The interop test runs the TypeScript client against
`occamyd`'s stream code, served by `client/internal/webinterop` and
built with the race detector. It is skipped unless
`OCCAMY_INTEROP_SERVER` names the server:

```
go build -race -o "${TMPDIR:-/tmp}/occamy-webinterop" ./client/internal/webinterop
cd client/occamy-web
npm ci
npm run lint
npm run typecheck
OCCAMY_INTEROP_SERVER="${TMPDIR:-/tmp}/occamy-webinterop" GORACE=halt_on_error=1 npm test
npm run build
cd ../..
```

`npm test` reports two tests skipped. They fail a CI run that lacks the
interop server, and skip everywhere else.

## 7. Tests against the demo stack and real servers

### End-to-end tests

With the stack from [section 3](#3-start-the-demo-stack) running:

```
make e2e
```

The tests open real sessions to every demo server:

- through the v0 service and guacd;
- through `occamy-v1`, natively and through guacd, with the Go client
  of protocol v1;
- on `rdp-native`: the clipboard both ways, a resize, a viewer joined
  by a share, and the owner's return;
- the VNC parity matrix, each feature on x11vnc and TigerVNC through
  both backends, logged as a table.

A run takes about four minutes, most of it the parity matrix. The
pressure test and the soak are skipped unless their variables are set
(below).

### Debug routes

The stack's debug routes have no authentication, so they are off
unless `make run` gets `OCCAMY_DEBUG=1`, and then they are on the
host's loopback interface only. Without it, `occamy-v1` logs `debug
listener off` and its debug port answers nothing:

```
make run
curl -s -m 5 localhost:5638/debug/vars      # fails: nothing serves it
curl -s -o /dev/null -w '%{http_code}\n' localhost:5636/debug/pprof/    # 404
```

With it, `occamy-v1` serves expvar and pprof on port 5638, and the v0
service serves pprof on its own port, in debug mode
([docker/occamy-debug.yaml](../docker/occamy-debug.yaml)):

```
make run OCCAMY_DEBUG=1
curl -s localhost:5638/debug/vars | head -c 200; echo
curl -s -o /dev/null -w '%{http_code}\n' localhost:5636/debug/pprof/    # 200
```

`make run` without `OCCAMY_DEBUG=1` turns them off again, whatever is
left in the shell or in `docker/.env`. `go test -run TestDemoDebugGating
./internal/config` checks that the demo files gate them this way.
[debugging.md](debugging.md) covers profiling `occamyd` through them.

### Pressure test and soak

Both need the debug routes, so start the stack with
`make run OCCAMY_DEBUG=1` first.

```
make e2e OCCAMY_PRESSURE_N=10
```

That adds the pressure test to the end-to-end tests. Each of its 10
rounds opens two sessions to every demo server at once through the v0
service. Afterwards, `occamyd`'s goroutines must return to their
number before the storm within a minute.

The pressure test runs before the v1 tests, and its RDP sessions can
leave xrdp without `xrdp-chansrv`, as in [Clipboard](#clipboard). Then
`TestE2EV1RDP/clipboard` fails after waiting 60 seconds for the echo,
and the other RDP subtests each take about 10 seconds longer. That is
the demo server, not `occamyd`: run `(cd docker && podman compose
restart rdp)`, and `make e2e` again.

```
make soak OCCAMY_SOAK_DURATION=5m OCCAMY_SOAK_INTERVAL=10s OCCAMY_SOAK_LIFETIME=2m
```

That is a short soak. It runs 8 sessions at a time to
`tigervnc-native` through `occamy-v1` for 5 minutes, each typing and
moving the pointer, and replaces each session after 2 minutes. Every
10 seconds it samples `occamyd`'s goroutines, heap and open files from
the debug listener. It fails if they keep growing, or if a session
fails. Plain `make soak` runs for an hour, and the full soak is `make
soak OCCAMY_SOAK_SESSIONS=20 OCCAMY_SOAK_DURATION=8h`.
[debugging.md](debugging.md#tests-and-benchmarks) lists its settings.

### Integration tests

These run the backends against real servers in containers of their
own. Build tags keep them out of `go test ./...`. Each container, and
each image a run builds, is the run's own, and the run removes it when
it ends. They need no demo stack. Building an image can take longer
than `go test`'s default 10 minutes, hence `-timeout`.

| Tag | Command | Against |
|-----|---------|---------|
| `rdp_integration` | `go test -tags rdp_integration -run Integration -timeout 30m ./internal/rdp` | the RDP client, against xrdp in the bench desktop image |
| `rdp_integration` | `go test -tags rdp_integration -run Integration -timeout 30m ./internal/backend/rdp` | the RDP backend, against the same |
| `kubernetes_integration` | `go test -tags kubernetes_integration -run Integration -timeout 15m ./internal/backend/kubernetes` | the Kubernetes backend, against k3s, as in [the Kubernetes step](#kubernetes) |
| `integration` | `go test -tags integration -run TestTigerVNC -timeout 30m ./internal/backend/vnc` | the VNC backend over VeNCrypt, against the demo TigerVNC image |

The `integration` tag has three more tests, which need a server that
you start:

- **`TestBenchDesktop`** runs the VNC backend against x11vnc on the
  bench desktop:

  ```
  podman build -t occamy-bench-desktop -f docker/bench/desktop/Dockerfile .
  podman run -d --rm --name occamy-vnc-test -p 127.0.0.1:15900:5900 occamy-bench-desktop vnc
  OCCAMY_VNC_ADDR=127.0.0.1:15900 go test -tags integration -run TestBenchDesktop ./internal/backend/vnc
  podman rm -f occamy-vnc-test
  ```

- **`TestEncodings`** takes the same `OCCAMY_VNC_ADDR` and asks for one
  encoding at a time.
- **`TestMacOS`** needs a Mac with Screen Sharing on; its doc comment,
  in `internal/backend/vnc/ard_integration_test.go`, says how.

The `record` tag (`internal/backend/guacd`) records guacd sessions
again for the conformance corpus. The `measure` tag (`bench/codecs`)
measures codecs on recorded frames. Neither is a test to run here;
[CONTRIBUTING.md](../CONTRIBUTING.md#development) and
[benchmarks/codecs.md](benchmarks/codecs.md) describe them. The
integration tests take `OCCAMY_CONTAINER_ENGINE=docker` for Docker.

### Fuzz tests

The parsers of everything that comes over a network have fuzz
targets, 33 of them. `go test -list '^Fuzz' ./...` lists them by
package. `go test` runs one target at a time, so fuzz each for a while
like this:

```
go test -run '^$' -fuzz '^FuzzSession$' -fuzztime 30s -parallel 4 ./client
```

`-parallel 4` keeps the fuzzer to four workers, which leaves the
machine usable. To fuzz them all, 30 seconds each, which takes about
25 minutes:

```
for pkg in $(go list ./...); do
  for target in $(go test -list '^Fuzz' "$pkg" | grep '^Fuzz'); do
    go test -run '^$' -fuzz "^$target\$" -fuzztime 30s -parallel 4 "$pkg" || break 2
  done
done
```

A failure writes its input to the package's
`testdata/fuzz/<target>/`, where plain `go test` replays it from then
on.

## 8. Tear down

```
make stop
```

`make stop` stops and removes the stack's containers and its network.
To also remove what `make build` and `make run` built and made:

```
make clean
podman rmi occamy:latest "occamy:$(git describe --always --tags)"
```

`make clean` removes nothing that is not Occamy's. It runs `compose
down` with `--volumes --rmi local` for the stack's project, which
removes the demo servers' images and the volume of TigerVNC's CA. It
then prunes the images of earlier `make build` runs that a later build
left untagged, which `make build` labels. The tagged image of `make
build` stays, so `podman rmi` removes it.

With the variables of [Next to another stack](#next-to-another-stack),
keep them exported for `make stop` and `make clean`, and name your
image instead: `podman rmi occamy-try:latest "occamy-try:$(git describe
--always --tags)"`.

If you followed the Kubernetes step, stop `occamyd` with Ctrl-C, then
remove the cluster and its files:

```
podman rm -f occamy-k3s
rm -r "$K8S"
```

Remove what the build left in the checkout:

```
rm occamyd
rm -r client/occamy-web/dist client/occamy-web/node_modules
```

The images that were pulled stay for the next run:
`docker.io/guacamole/guacd:1.6.0`, `docker.io/rancher/k3s:v1.34.1-k3s1`,
the base images `debian:trixie-slim`, `golang:1.27-trixie`,
`node:25-trixie-slim` and `gcr.io/distroless/static-debian13:nonroot`.
So does `occamy-bench-desktop`, if you built it for `TestBenchDesktop`.
`podman rmi` removes them.
