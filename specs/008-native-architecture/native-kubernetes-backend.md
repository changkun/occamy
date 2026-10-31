---
title: Native Kubernetes backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/native-ssh-backend.md
  - specs/008-native-architecture/api-and-server-stack.md
affects:
  - internal/backend/kubernetes/
  - internal/config/
  - server/
effort: medium
created: 2026-10-31
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# Native Kubernetes backend

## Design Problem

Open a terminal in a container of a Kubernetes pod, as guacd's
`kubernetes` protocol does, without guacd. The owner decided on
2026-10-31 that v1.0 has a native Kubernetes backend, so that no
Guacamole is left: until now Kubernetes was reachable only through
guacd, and v1 never registered it, since v1 connections are `host:port`
only ([guacd-bridge](guacd-bridge.md), Implementation notes:
registration). Four things need deciding:

- how the backend talks to the API server without client-go;
- how a Kubernetes target fits configuration v1 next to `host:port`
  targets;
- how it authenticates and checks the API server;
- what keeps a hostile or broken API server from crashing `occamyd`,
  learning the token, or steering the request to another endpoint.

## Context

- The API server runs a command in a container with the `exec`
  subresource of a pod, and connects to its main process with `attach`:
  `GET /api/v1/namespaces/{namespace}/pods/{pod}/exec` with the
  query `command` (once per argument), `container`, `stdin`, `stdout`,
  `stderr` and `tty`, upgraded to a WebSocket or to SPDY. kubectl used
  SPDY until 1.30 and WebSockets since.
- The WebSocket subprotocols carry the streams as channels, one binary
  message each, the first byte naming the channel
  (`k8s.io/apimachinery/pkg/util/remotecommand`,
  `k8s.io/streaming/pkg/httpstream/wsstream`):

  | Byte | Channel | Direction |
  |------|---------|-----------|
  | 0 | stdin | to the server |
  | 1 | stdout | from the server |
  | 2 | stderr | from the server; not used with a TTY |
  | 3 | error | from the server: a `metav1.Status` in JSON since v4 |
  | 4 | resize | to the server: `{"Width":80,"Height":24}` since v3 |
  | 255 | close | either way, v5 only: `[255, channel]` ends a channel |

  `v4.channel.k8s.io` added the structured status, with the exit code;
  `v5.channel.k8s.io` added the close signal. The server picks the first
  subprotocol it knows from the client's list and echoes it, and sends an
  empty message on the lowest output channel once it is ready.
- The exit status, as client-go's `errorDecoderV4` reads it: `status`
  `Success` for exit 0; `Failure` with `reason` `NonZeroExitCode` and a
  cause whose `reason` is `ExitCode` and whose `message` is the code; any
  other `Failure` carries what went wrong in `message`.
- Versions. A v4 upgrade goes through the API server's upgrade-aware
  proxy to the kubelet, whose streaming server has spoken v4 over
  WebSockets since long before any maintained release. A v5 upgrade is
  terminated by the API server's stream translator, which talks SPDY to
  the kubelet: alpha in 1.29 behind `TranslateStreamCloseWebsocketRequests`,
  on by default since 1.30 (KEP-4006). From 1.36, with
  `ExtendWebSocketsToKubelet`, the API server passes it through to
  kubelets that declare it.
- RBAC. A WebSocket upgrade is a GET, which RBAC authorized as `get` on
  `pods/exec` until `AuthorizePodWebsocketUpgradeCreatePermission`, on
  by default since 1.35, asks for `create`, as SPDY's POST always did.
- `github.com/coder/websocket` is already a dependency of `occamyd` (its
  streams and the v0 routes). client-go would pull in most of
  Kubernetes's API types for one request.
- guacd's `kubernetes` protocol (`src/protocols/kubernetes`, 1.6) takes
  `hostname`, `port` (8080), `namespace` (`default`), `pod`,
  `container`, `exec-command`, `use-ssl` (off), `client-cert`,
  `client-key`, `ca-cert` and `ignore-cert`, plus its terminal's display
  and recording parameters. It attaches to the container's main process
  unless `exec-command` is set, and passes `exec-command` as a single
  `command` value, so a command with arguments is one argument with
  spaces in it. It has no bearer tokens: only client certificates.

## Proposed shape

- A terminal backend, `internal/backend/kubernetes`, built like the SSH
  backend: Run dials, sends the size, reports it with `Sink.Resize`,
  and feeds stdout (and stderr without a TTY) to `Sink.TermData`, which
  the session's emulator (`internal/terminal`) turns into screen diffs.
  `TermInput` and `Text` go to stdin, and `Resize` to the resize channel.
- One WebSocket per session to
  `https://{target}/api/v1/namespaces/{namespace}/pods/{pod}/exec` (or
  `attach`), offering `v5.channel.k8s.io` and then
  `v4.channel.k8s.io`.
- A connection of protocol `kubernetes` names the API server in
  `target` and the pod in a `kubernetes` block; its credentials are a
  bearer token, a client certificate, or a prompt; its `tls` is a vnc
  connection's.

## No Kubernetes SDK, and what that leaves out

The owner asked why there is no client-go. The backend makes one
request: pod exec (or attach), upgraded to a WebSocket whose messages
carry a one-byte channel prefix. `coder/websocket` is already a
dependency, and the framing is small enough to fuzz whole. client-go
would bring in the API machinery and most of the API types for that
one request, and its exec went over SPDY for most of its history
(transport option B below).

The limits that follow, which docs/configuration.md states too:

- **No kubeconfig files.** The connection names the API server in
  `target`, its CA or pins in `tls`, and the credential in
  `credentials`.
- **No exec credential plugins**, such as EKS's `aws eks get-token` or
  GKE's `gke-gcloud-auth-plugin`, and **no legacy `auth-provider`
  entries.** `occamyd` deliberately runs no external credential
  binaries: a session never starts a process on the server.
- **What works:** a bearer token, such as a ServiceAccount token, which
  managed clusters accept too, or a client certificate, with the API
  server checked against the system's roots, a CA or pins.
- **RBAC:** `create` on `pods/exec` (or `pods/attach`) in the
  namespace, with `get`. API servers before 1.35 authorize the
  WebSocket upgrade, a GET, as `get` alone; from 1.35 they also check
  `create`. Granting both covers every version. docs/configuration.md
  gives a minimal ServiceAccount, Role and RoleBinding.

## Options

### Transport

- **A. WebSocket subprotocols over `coder/websocket` (chosen).** One
  request, binary messages, the status on channel 3. No new module.
  - Pros: no new dependency; the API server's preferred transport since
    1.30; a frame format small enough to fuzz.
  - Cons: v4 has no way to close stdin on its own, which a terminal
    never needs.
- **B. client-go's `remotecommand`.** SPDY or WebSockets, as kubectl.
  - Pros: maintained by Kubernetes.
  - Cons: client-go and the API machinery as dependencies, for one
    request; SPDY is deprecated for this use.
- **C. SPDY by hand.** Out: deprecated, and nothing in Go's standard
  library speaks it.

### Default command

- **A. Attach, as guacd does when `exec-command` is unset.** Needs a
  container started with `stdin: true` and `tty: true`, which few are;
  otherwise the terminal shows nothing or the API server refuses.
- **B. Exec `/bin/sh`.** Works in nearly every image, but without line
  editing in most.
- **C. Exec a shell that prefers bash (chosen).**
  `/bin/sh -c "command -v bash >/dev/null && exec bash || exec sh"`,
  as k9s does: bash where the image has it, `sh` otherwise. `attach:
  true` keeps A.

### Target

- **A. `target` is the API server's `host:port`, always over HTTPS
  (chosen).** The pod goes in a `kubernetes` block.
  - Pros: every target stays `host:port`, as `ParseV1` promises, so the
    registry, the log and the allowlist's code need nothing new; matches
    guacd's `hostname` and `port`.
  - Cons: no path prefix, as Rancher's proxied clusters need.
- **B. `target` is a URL.**
  - Pros: path prefixes; an `http` URL for `kubectl proxy`.
  - Cons: a second target syntax, and `http` would send the token in
    clear.

## Decisions

Settled on 2026-10-31. The backend itself is the owner's call; the rest
follows the recommendations above and the owner's rule that native is a
protocol's default.

1. **Transport: A.** `coder/websocket`, offering v5 and then v4; a
   server that picks neither, or none, is refused. No client-go, no
   SPDY. Binary messages only, as client-go reads them.
2. **Kubernetes versions.** Every API server that serves pod exec over
   WebSockets with v4: all maintained releases (1.35 to 1.37 on this
   date) and older ones back to long before them; v5 where the API
   server translates (1.30 on, 1.29 with its gate). The integration
   test runs against k3s.
3. **Channels.** stdin 0, stdout 1, stderr 2, error 3, resize 4, and
   v5's close 255. Output on 1 and 2 goes to the terminal; the status on
   3 decides how Run ends; a size goes to 4 when the session starts and
   on every resize; a v5 close from the server ends that channel. The
   backend sends `[255, 0]` before it closes a v5 stream itself.
4. **Run's result**, as the SSH backend's: a status of `Success` or
   `NonZeroExitCode` ends the session normally, with the exit code
   logged at debug level; any other status is an error with its reason
   and message; a stream that ends without a status is an error, since
   that is how a lost connection shows. A refused upgrade is an error
   with the HTTP status and the `Status` message the server sent.
5. **Default command: C**, with `attach: true` for guacd's default and
   `command` for any other, one list element per argument.
6. **Configuration: target A.** A connection of protocol `kubernetes`:
   - `target`: the API server, `host:port` or a host alone for port 443.
     Always HTTPS.
   - `kubernetes`: `namespace` (default `default`), `pod` (required),
     `container` (default: the pod's default container), `command`
     (default C), `attach` (default false; not with `command`), `tty`
     (default true).
   - `credentials`: exactly one of `token_file`, `token_env`, the pair
     `client_cert_file` and `client_key_file`, or `prompt: true`, which
     asks the owner for a bearer token (an empty answer sends none). No
     `username`, password or SSH key.
   - `tls`: a vnc connection's keys. `server_name`, `ca_file` and
     `pins` as there; `require` is implied, since the API server is
     always reached over TLS; `insecure_skip_verify` accepts any
     certificate with a warning, for demos, and then sends the token to
     whoever answers.
   - `backend`: `native`, the default and the only one; `guacd` is
     refused, and so is `guacd.params.kubernetes`, since v1 never had a
     Kubernetes bridge.
7. **No ad-hoc Kubernetes targets.** An ad-hoc target is a `host:port`
   that any signed-in user names; a Kubernetes target is also a
   namespace, a pod and a container, and its credentials would have to
   come from the user. `POST /api/v1/sessions` refuses `kubernetes`, and
   `adhoc_backends` has no `kubernetes` key.
8. **Security.**
   - The token lives in one unexported field of a type whose `String`,
     `GoString`, `Format` and `LogValue` print `[redacted]`. It goes in
     the `Authorization` header only, never in the URL, an error or a
     log line, and is refused at `New` unless it is visible ASCII, so it
     cannot split a header.
   - The certificate is checked in `VerifyConnection`, during the TLS
     handshake, so nothing is written to an API server that fails the
     policy: the token is sent only after it passed. TLS 1.2 or later.
     Redirects are not followed, so the token never goes to another
     URL. No proxy from the environment.
   - Namespaces and container names must be DNS-1123 labels, and pod
     names DNS-1123 subdomains, at `ParseV1` and again at `New`. The
     URL is built from `url.URL` with the names checked, and the query
     from `url.Values`, so nothing in a name or a command can reach
     another path or add a parameter.
   - Output: a message is at most 1 MiB, and is streamed to the
     terminal in reads of at most 32 KiB, so no message is held whole.
   - The error channel keeps at most 16 KiB; more fails the session.
     Messages from the server are quoted and cut to 256 bytes in
     errors.

## Affects

New `internal/backend/kubernetes`. `internal/config` gains the
`kubernetes` protocol, its block and its credentials; `server` registers
the backend and refuses ad-hoc Kubernetes targets. Clients need nothing:
a Kubernetes session is a terminal like SSH's. guacd-bridge's
per-protocol table moves Kubernetes to native.
