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
- **No path prefix.** An API server behind one, such as a cluster that
  Rancher proxies at `/k8s/clusters/<id>`, cannot be reached yet, since
  `target` is a `host:port` (option A below).
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
   test runs against k3s v1.34.1.
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
     `container`, `command` (default C), `attach` (default false; not
     with `command`), `tty` (default true). `container` may be left out
     only for a pod with one container: the API server then picks it,
     and refuses a pod with more with 400, since it does not read
     kubectl's `kubectl.kubernetes.io/default-container` annotation.
     (Corrected after review: this said the pod's default container, as
     kubectl picks it.)
   - `credentials`: exactly one of `token_file`, `token_env`, the pair
     `client_cert_file` and `client_key_file`, or `prompt: true`, which
     asks the owner for a bearer token (an empty answer sends none). No
     `username`, password or SSH key. A token file holds at most 64 KiB,
     as every secret file does, and trailing line breaks are dropped
     from a token in a file or a variable.
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
     Whatever the server chose is cut to 256 bytes in errors, with its
     control characters escaped or replaced: a `Status`'s reason and
     message, a subprotocol, the headers of a failed upgrade that
     `coder/websocket` or `net/http` quote, and a refused certificate's
     subject and the names x509 lists.

## Affects

New `internal/backend/kubernetes`. `internal/config` gains the
`kubernetes` protocol, its block and its credentials; `server` registers
the backend and refuses ad-hoc Kubernetes targets. Clients need nothing:
a Kubernetes session is a terminal like SSH's. guacd-bridge's
per-protocol table moves Kubernetes to native.

## Implementation notes

- **Status.** Built on 2026-10-31 in the same change as this spec:
  `internal/backend/kubernetes` with its fake API server `kubetest`, the
  `kubernetes` protocol in `internal/config`, and its registration in
  `server/backends.go` (`kubernetes/native`; the session state names the
  backend `kubernetes`, class `terminal`). Ad-hoc targets refuse
  `kubernetes` with `400`, by `config.AdhocProtocol`. Every decision
  above is built as written, and the k3s integration test passes (see
  below).
- **What was built.** `Config`, `New`, `Backend` and `Token`, with
  `CheckNamespace`, `CheckPod`, `CheckContainer` and `CheckPin` for the
  configuration. Run asks for a token if it prompts, dials, sends the
  size and reports it with `Sink.Resize`, then runs three goroutines: a
  reader that demultiplexes the channels into `Sink.TermData` and the
  status, a writer for stdin and resizes, and a keepalive. Each one
  recovers its own panic. Input queues up to 64 writes, so a resize or
  keys sent while the stream opens are written once it is open.
- **Decisions made during implementation.**
  - The certificate policy is a copy of the VNC backend's (`cert.go`),
    as `internal/rdp` copied it. It accepts the same `tls` keys, and its
    pins are checked by `kubernetes.CheckPin`. The three copies could
    become one package.
  - A WebSocket ping every 30 seconds keeps idle sessions alive through
    load balancers. A missing pong does not end the session: only the
    stream's end or a failed write does.
  - Without a TTY, line feeds become CRLF (ONLCR) before the terminal
    sees them, since no TTY translated them, and stderr is shown with
    stdout.
  - An empty prompt answer sends no `Authorization` header, as the
    decision says, and the API server's 401 then ends the session.
  - The API server's refusal (a non-101 response) is read from the at
    most 1 KiB of body that `coder/websocket` keeps, and its `Status` is
    quoted in the error.
  - coder/websocket closes the connection when a write's context ends
    while the write is in progress. A cancel during a write could
    therefore end the reader before Run saw the context, and Run then
    returned "the stream ended without an exit status". CI caught this
    once on v4. Run now returns the context's error whenever the reader
    fails after the cancel. Over v5, the close of stdin is best effort:
    a cancel that lands in a write closes the connection before it can
    be sent.
- **Tests.**
  - The unit tests run against `kubetest`, a TLS `httptest` server that
    speaks v4 and v5 framing and records each request. They cover exec
    and attach, v4 and v5, the initial and later sizes, exit statuses,
    failure statuses, a stream that ends without a status, cancel (v5
    sends `[255, 0]`, v4 does not), no TTY, queued and full input,
    keepalive, and the output and status bounds.
  - Refusals: unknown messages and text messages; a subprotocol that is
    neither v5 nor v4; 401, 403, 404 and 502 with the token absent from
    every error and log line; a redirect not followed; TLS 1.1 refused;
    no proxy from the environment.
  - Certificates: the system's roots, pins in hex, base64 and PEM,
    another pin, a CA with the right and the wrong name, and insecure
    mode with its warning. Every refused certificate leaves zero
    requests on the server, so no token was sent.
  - Also client certificates, the prompt, names that would reach
    another path, token redaction in fmt and slog, panics on every
    goroutine, and deferred unlocks.
  - `server/kubernetes_e2e_test.go` streams a kubernetes connection
    through occamyd's routes with the Go client. It checks the request's
    path and query, the token header, v5, the hello's size, input,
    output, a resize, the session state, the exit as `remote-ended`, the
    prompt (and an empty answer's 401), a wrong pin with no request on
    the server, and a 403 as `backend-failed`.
- **Coverage** (`go test -cover`): `internal/backend/kubernetes` 95.5%,
  `internal/config` 94.7%, `server` 92.4%; after the review's fixes
  97.0%, 94.6% and 92.4%.
- **Fuzzing**, 60 seconds each with `-parallel 4` on a shared host
  (load about 13):
  - `FuzzDemux`: 3,016,408 executions, 22 new interesting inputs, no
    failure. It checks that stdout and stderr pass whole in pieces of at
    most the buffer, that at most one byte past the status bound is
    kept, and that only an oversized status fails.
  - `FuzzParseStatus`: 4,330,576 executions, 312 new interesting inputs,
    no failure. It checks that it never panics, that an exit code falls
    in 0 to 255, and that an error is short, valid UTF-8 and free of
    control characters, whatever the server wrote.
- **Mutation check**: 25 security mutants, each applied to a copy of
  the tree with the tests of its package run against it.
  - The first pass killed 20 of the 25: certificate acceptance, the
    server name, insecure mode's warning, VerifyConnection removed,
    redirects, a slash in a label, `..` in a pod, unescaped paths,
    token redaction in fmt and slog, control characters in a token, the
    status cap, the read limit, quoting, any subprotocol, a URL as a
    target, a failure status taken as an exit, the configuration's
    namespace check, guacd allowed for kubernetes, and ad-hoc kubernetes
    in the server.
  - "Any pin matches" did not build in the first pass and was rewritten.
    The other four survived: TLS 1.0 allowed, a proxy from the
    environment, an unbounded label length inside a pod name, and the
    status read without its limit (the cap still failed the session but
    held the whole message). TestTLSVersion, TestNoProxy, a pod name
    with a 64-character label, and a 1 MiB status message were added.
    The second pass killed all five.
  - Result: 25 of 25 killed.
- **Integration test.** `integration_test.go`, behind the
  `kubernetes_integration` tag, starts k3s in a privileged container of
  its own name, applies a namespace, a busybox pod, a ServiceAccount
  with the Role of docs/configuration.md, and one without it. It then
  checks a command's output, input and non-zero exit, the default
  shell's `stty size` before and after a resize, no TTY, a missing
  container, 403, 401, the system roots refusing the cluster's CA, and
  cancel. Run it with
  `go test -tags kubernetes_integration -run Integration -timeout 15m ./internal/backend/kubernetes`.
  - It ran on 2026-10-31 against k3s v1.34.1 (`rancher/k3s:v1.34.1-k3s1`)
    in podman, with the host's load at 7.6 at the start and 7.0 at the
    end. All eight subtests passed in 23 s, and the container was
    removed.
  - The first run, at load 6.6, failed two subtests on the test's own
    race: they sent input right after starting Run in a goroutine, and
    Input before Run has begun is `ErrNotRunning`, as SSH's is. They now
    wait for Run to begin before their first input.
  - The ServiceAccount with `get` and `create` on `pods/exec` is
    allowed; the one without them gets 403. Since v1.34 predates the
    1.35 `create` check, this run exercised the `get` path.
  - On d80310e, with the review's fixes, it passed 8 of 8 again against
    k3s v1.34.1, at load 6.7 over v5, and at load 7.9 over v4 alone, in
    a copy that offered only v4.
- **Independent review** of the PR at a4db355. It found the security
  design sound, and the certificate checked before any byte is sent.
  Its findings, each fixed with a test that fails without the fix:
  - The integration test's race on `ErrNotRunning`, fixed as above.
  - A panic in the first `sink.Resize`, which serve makes on Run's own
    goroutine, outside the recovering wrappers, left the stream open,
    and the command running until the kubelet's idle timeout. serve
    now closes the stream by `defer`, however it ends, which also
    replaces the close after a failed write of the first size.
    `TestGoroutinePanics/first_resize` and `TestCancelBeforeSize`.
  - Errors of a failed upgrade quoted the server whole, up to the 64 KiB
    of headers the transport reads: `coder/websocket`'s errors for
    `Sec-WebSocket-Protocol`, `Sec-WebSocket-Accept`, `Upgrade`,
    `Connection` and extensions, and `net/http`'s for a malformed status
    line. They are cut to 256 bytes with control characters replaced,
    and a refused certificate's subject and reason are cut the same
    way. The cut applies to what failed, not to the request's URL that
    a `url.Error` repeats, so that the URL, which is ours and long,
    cannot push the reason out of it. `TestForgedUpgrade`,
    `TestCertificateErrorCut`, `TestTLSVersion` and `TestCut`.
  - The default container was documented wrong (decision 6).
  - Tests for three surviving mutants: `TestCancelRace` reproduces the
    race that e43ee00 fixed, with a hook that cancels, closes the stream
    as `coder/websocket` does and waits for the reader to end before
    serve waits; `TestCertificateChain` checks a leaf that an
    intermediate signed, with and without the intermediate; and
    `TestCancelBeforeSize` cancels between the dial and the first
    write.
  - Hardening: a secret file of more than 64 KiB is refused, for every
    kind of secret (`TestSecretFileBound`, and `TestEndlessSecretFile`
    with a FIFO kept open in place of `/dev/zero`). A token from
    `token_env` loses its trailing line breaks, as one from
    `token_file` does. docs/configuration.md says that an API server
    behind a path prefix is not supported.
  - The reviewer also ran the integration test over v4 alone, through
    the kubelet's proxy path, against k3s v1.34.1: 8 of 8 passed.
  - Mutation check of the fixes: 13 mutants, each killed. They undo
    the close by `defer`, e43ee00's fix, the intermediates, the cut of
    dial errors, the unwrapping of `url.Error`, the certificate error
    returned on its own, the cut of its subject and of its reason,
    `cut`'s length and its replacement of control characters, the
    secret file's length check and its limited read, and `token_env`'s
    trim. Three of the reviewer's mutants still survive, none of them
    among the review's findings: the dial's own `ctx.Err()` (the error
    it would return wraps `context.Canceled` anyway), input not copied
    before it is queued, and compression allowed in the upgrade.
- **Deviations.** None from the decisions. One addition: `tls.require`
  is accepted and means nothing for kubernetes, since TLS is always
  required.
- **Follow-ups.**
  - Run the integration test against a 1.35 or later API server too,
    where the `create` check applies.
  - Merge the three copies of the certificate policy (VNC, RDP,
    Kubernetes) into one package, and give the VNC copy a test of a
    certificate chain, as the Kubernetes copy now has.
  - Check the mode of secret and key files, as ssh does, and refuse
    one that others can read.
  - Bound the other files read at a session's start: an SSH private
    key, a client certificate and its key, and CA files.
