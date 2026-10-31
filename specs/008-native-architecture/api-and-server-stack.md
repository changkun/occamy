---
title: API v1 and server stack
status: validated
track: architecture
depends_on: []
affects:
  - server/
  - internal/config/
  - conf.yaml
  - go.mod
  - docs/
effort: large
created: 2026-10-29
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# API v1 and server stack

## Design Problem

Define the HTTP surface of `occamyd` v1 and the stack that serves it:

- How do callers authenticate?
- Where do connection targets and their credentials live?
- How are sessions created, shared and closed?
- What happens to the v0 routes?
- Should v1 stay on gin?

The answers fix the security model that [004](../004-security-hardening.md)
can only patch on v0.

## Context

- v0 (`server/connection.go`): gin with gin-jwt v2. `/api/v1/login`
  signs whatever `protocol`, `host`, `username` and `password` it receives,
  and `/api/v1/connect?token=` upgrades to a Guacamole WebSocket. The token
  carries the remote password in readable claims. Identical claims share a
  session.
- 004 lists the v0 problems: open proxy, credentials in tokens and logs,
  `jwt_secret: occamy` and `mode: debug` defaults, `/debug/pprof` on the
  public listener, and gin-jwt v2 on golang-jwt v4.
- v0 serves WebSockets with `gorilla/websocket`, whose last commit was
  in 2025-03. `github.com/coder/websocket` (ISC, active) reads and writes
  without allocating, supports permessage-deflate, and compiles to WASM,
  so the Go client core can share it.
- gin pulls a large indirect graph (quic-go, sonic, ugorji). Every
  Dependabot alert after the revival came from it (commit `b1bb2de`).
- Since Go 1.22, `net/http.ServeMux` routes by method and path pattern
  (`GET /api/v1/sessions/{id}`), which covers what gin is used for here.

## Proposed API

| Method and path | Purpose |
|-----------------|---------|
| `POST /api/v1/auth/token` | Exchange caller credentials (per auth provider) for a short-lived access token |
| `GET /api/v1/auth/oidc/login` | Start OIDC sign-in: redirect to the identity provider (authorization code with PKCE) |
| `GET /api/v1/auth/oidc/callback` | Finish OIDC sign-in: send the browser back to a configured return URL with a one-time code in the fragment |
| `POST /api/v1/auth/oidc/token` | Exchange that one-time code, with the client's PKCE verifier, for a short-lived access token |
| `GET /api/v1/connections` | Connections the caller may open (from the registry) |
| `GET /api/v1/connections/{id}/stream` | WebSocket upgrade that opens a session on the connection and streams it, in one round trip ([protocol-v1](protocol-v1.md), Handshake) |
| `POST /api/v1/sessions` | Open a session on a connection, or on an ad-hoc target when allowed, returning the session ID, the caller's role and a stream token that joins it as the owner |
| `GET /api/v1/sessions/{id}` | Session state: backend, size, viewers |
| `POST /api/v1/sessions/{id}/shares` | Mint a share token for viewer or read-only access, with an expiry |
| `DELETE /api/v1/sessions/{id}` | Close the session (owner only) |
| `GET /api/v1/sessions/{id}/stream` | WebSocket upgrade that joins a session, with the owner's access token, a session token (such as the stream token) or a share; the token travels in `Sec-WebSocket-Protocol` (or `Authorization` for native clients), never in the URL |
| `GET /healthz` | Liveness, on the public listener |

Debug and metrics endpoints move to a separate listener (`debug.address`,
off by default).

Connection registry: an administrator defines connections (`id`, display
name, protocol, target, credential reference, allowed users or groups) in
configuration for beta. Credentials stay server-side, and tokens reference
a connection ID only. Ad-hoc targets (the v0 behaviour) are behind
`allow_adhoc_targets: false`, with a target allowlist when enabled.

## Options

### Server stack

- **A. Standard library `net/http` plus golang-jwt v5 (recommended).**
  Method-and-pattern routing is built in, and middleware is plain
  `func(http.Handler) http.Handler`. golang-jwt v5 is the maintained JWT
  library with an explicit allowed-algorithms check.
  - Pros: drops gin and its indirect graph; nothing framework-specific in
    handlers; the same code serves tests through `httptest`.
  - Cons: request binding and validation are hand-written (a handful of
    JSON bodies); the move costs a rewrite of `server/`, which happens
    anyway.
- **B. gin with gin-jwt v3.** Smallest change from v0, and it does the v3
  move that 004 plans.
  - Pros: familiar; binding and middleware ready-made.
  - Cons: keeps the dependency graph and its alert churn. gin-jwt couples
    token issuance to a login handler shape that does not fit a registry
    model.
- **C. Opaque server-side session tokens instead of JWTs.** Random IDs
  looked up in memory, with no signing keys.
  - Pros: revocable at once, and nothing to leak in claims.
  - Cons: state lives in one process (fine for single-node beta), and
    third-party issuers cannot mint tokens.

Recommendation: A for the stack. Decide token format between JWT (A) and
opaque (C) under the open questions. If v0 routes survive past beta.1,
their gin-jwt v3 move happens in 004; otherwise skip it.

### v0 routes

- **i. Move to `/api/v0/login` and `/api/v0/connect` behind
  `legacy_v0: true`, and remove them at v1.0 (recommended).** Existing
  demos keep working through the betas. The new API owns `/api/v1`.
- **ii. Keep a Guacamole-compatible endpoint permanently** for third-party
  guacamole-common-js clients. It can only serve guacd-backed sessions,
  since native backends produce no Guacamole instructions, so it is a
  shrinking feature.
- **iii. Remove at beta.1.** Simplest, but breaks the demo client until
  [009](../009-react-web-client.md) is on protocol v1.

## Decisions

Settled on 2026-10-29. Items 1, 2 and 4 were the owner's call; the rest
follow the recommendations above, or the smallest choice that serves a
single-node beta.

- **Stack: A,** standard library `net/http` and golang-jwt v5. gin and
  gin-jwt leave `go.mod` once the v0 routes are ported to the new stack
  (2026-10-30: at beta.1, not at v1.0; see v0 routes under Task
  Breakdown).

1. **Tokens: JWTs (option A, not C).**
   - occamyd signs its own tokens with Ed25519 and accepts only the
     algorithms it is configured for.
   - Access tokens are short-lived (15 min by default) and name the user.
     Stream and share tokens name a session and a role.
   - Third-party issuance is supported: a gateway can mint tokens with a
     key listed under `auth.trusted_issuers`.
   - Closing a session ends its streams at once, whatever tokens are
     still valid.
2. **Sign-in: static users and OIDC.**
   - Static users have bcrypt hashes in the configuration.
   - OIDC uses the authorization code flow with PKCE, through
     `golang.org/x/oauth2` and a go-oidc style ID token verifier.
   - Registry permissions name users and groups. OIDC identities map to
     them through configured subject and group claims.
3. **Registry: configuration file only** for beta.
4. **v0 routes: i.** They move to `/api/v0/login` and `/api/v0/connect`
   behind `legacy_v0: true` and are removed at v1.0. The Vue client keeps
   working until [009](../009-react-web-client.md) moves to protocol v1.
5. **Configuration:** v1 files carry `version: 1` and reject unknown keys
   (`yaml.Decoder.KnownFields`). A file without `version` is read as v0
   configuration and served through the v0 routes.
6. **Metrics: expvar on the debug listener** for beta. Prometheus can
   come later if someone asks for it.
7. **TLS:** occamyd terminates TLS when `tls.cert` and `tls.key` are set.
   A reverse proxy is documented as the alternative. ACME comes later.
8. **Opening and streaming is one request** (2026-10-30).
   `GET /api/v1/connections/{id}/stream` opens a session and streams it,
   so a client reaches the first frame in one round trip after sign-in,
   where v0 needs two and `POST /api/v1/sessions` followed by a stream
   would need three. The token travels in `Sec-WebSocket-Protocol`, as
   [protocol-v1](protocol-v1.md) describes, and the server checks
   `Origin` against the configured origins on every upgrade.
   `POST /api/v1/sessions` stays for clients that want the session ID
   first.
9. **WebSocket library: `coder/websocket`** (2026-10-30) for the v1
   routes, for the reasons under Context; the v0 routes keep
   `gorilla/websocket` until they are removed.

## Task Breakdown

| Task | Delivers | Needs |
|------|----------|-------|
| Config v1 | `internal/config`: a file with `version: 1` decoded with `KnownFields`, and a file without it read as v0 as today. Sections: `listen`, `tls` (cert, key), `debug.address`, `origins`, `auth` (signing key file, access token lifetime, `trusted_issuers`, static `users` with bcrypt hashes and groups, `oidc`), `guacd` (address and per-protocol parameters), `connections` (ID, display name, protocol, target, credentials, allowed users and groups), `allow_adhoc_targets` with its allowlist, and `legacy_v0`. Credentials are references (`password_file`, `password_env`, `private_key_file`) or `prompt`, never inline secrets, so a configuration file can be committed. Validation reports every problem with its key path: unique connection IDs, known protocols, targets that parse, references that resolve, and, when static users are the only identities (no OIDC, no trusted issuers), allowed users and groups that exist | — |
| Tokens | `internal/auth`: Ed25519 signing keys loaded from a file (generated on first start when absent, with a warning), access tokens (subject, groups, 15 minutes by default) and stream and share tokens (session ID, role), issued and verified with golang-jwt v5 restricted to `EdDSA`; tokens from `trusted_issuers` verified against their listed keys. Tests reject `none`, an HMAC token signed with the public key, an unknown issuer, a wrong audience, expiry and tampering | — |
| HTTP server | `server/` on `net/http.ServeMux`: plain `func(http.Handler) http.Handler` middleware for recovery, request logging that never logs tokens or credentials, and authentication; JSON helpers and one JSON error shape; `/healthz`; `POST /api/v1/auth/token` for static users (bcrypt, constant-time on unknown users); TLS when configured; graceful shutdown; the debug listener with expvar and pprof, off unless `debug.address` is set. `occamy.go` starts it from a v1 configuration | Config v1, Tokens |
| OIDC | `GET /api/v1/auth/oidc/login` and `/callback`: authorization code with PKCE through `golang.org/x/oauth2`, state, nonce and verifier in a short-lived sealed cookie, the ID token checked by `coreos/go-oidc` v3, and subject and group claims mapped to registry users and groups as configured; the browser sent back to a configured return URL with a one-time code that `POST /api/v1/auth/oidc/token` exchanges for an access token; tests against an in-process identity provider | HTTP server |
| Sessions | `GET /api/v1/connections` filtered by the caller's users and groups; `POST /api/v1/sessions`, `GET` and `DELETE /api/v1/sessions/{id}` and `POST /api/v1/sessions/{id}/shares` on the session registry; a backend factory keyed by protocol that the native SSH and VNC backends and the guacd bridge register into; ad-hoc targets only when enabled and allowed; tests on the fake backend | HTTP server; session-core Registry |
| Streams | `GET /api/v1/connections/{id}/stream` and `/api/v1/sessions/{id}/stream` on coder/websocket: `Origin` checked against `origins`, the token taken from the `occamy.v1.token.` subprotocol or `Authorization`, `occamy.v1` answered alone, `hello` read from the query, and the socket handed to the viewer writer, `stream.Serve`, with `stream.SetLowWater` applied; a closed session ends its streams whatever tokens remain valid, and so does a shutdown. Tests cover one round trip to `welcome`, a bad origin, a missing or wrong token, and a token in the URL ignored | Sessions; session-core Viewer writer; protocol v1 Handshake |
| v0 routes | `/api/v0/login` and `/api/v0/connect` behind `legacy_v0`, served on the new mux through `net/http`, golang-jwt v5 (HS256 with the v0 secret, so v0 tokens keep their shape) and coder/websocket, so that gin, gin-jwt and gorilla/websocket leave `go.mod` at beta.1; a v0 configuration file implies `legacy_v0`; the Vue client moves to the new paths; the existing v0 tests keep passing against the new paths | HTTP server |
| Document | `docs/api.md` for every route above, the configuration reference, TLS through occamyd or a reverse proxy, and a v0 to v1 migration note; README and `conf.yaml` updated to a v1 example | all of the above |

Config v1 and Tokens can start at once and in parallel. Streams is the
task that joins this spec to [session-core](session-core.md) and
[protocol-v1](protocol-v1.md).

## Affects

`server/` is rewritten around the chosen stack. `internal/config` gains
the registry and auth sections. `go.mod` loses gin and gin-jwt under option
A. Most of 004's items are absorbed into this design; 004 remains the
place for fixes that ship on v0 before beta.

## Implementation notes: tokens

- **What was done:** `internal/auth` with `Signer` (`Access`,
  `Session`), `Verifier` (`VerifyAccess`, `VerifySession`),
  `LoadOrCreateKey` and `LoadPublicKey`, on golang-jwt v5.
- **Decisions:**
  - Every token carries a `kind` claim, `access` or `session`, so one is
    never accepted where the other is expected. Session tokens carry
    `sid` and `role`, the role as a string (`owner`, `viewer`,
    `read-only`) so that `internal/auth` does not import the session
    core.
  - The issuer claim picks the key, and only `EdDSA` is accepted, so an
    unknown issuer, `none` and an HMAC token keyed with the public key
    all fail before any claim is trusted. The audience is required.
  - A token must carry `iat`, and `exp - iat` may not exceed 12 hours, so
    a trusted issuer cannot mint tokens that live for years. Clock skew of
    30 seconds is allowed. Base64 decoding is strict.
  - The signing key is PKCS #8 PEM, created readable by its owner only
    through a temporary file and a hard link, so occamyd instances
    starting together agree on one key and a crash leaves no partial key.
  - Shares minted by `POST /api/v1/sessions/{id}/shares` stay the session
    registry's opaque tokens (session-core), which die with the session
    and are stored only as hashes. Session JWTs serve tokens minted by a
    trusted issuer and the stream token that `POST /api/v1/sessions`
    returns.
- **Follow-ups:** the HTTP server task wires the signer and verifier from
  the v1 configuration and logs a warning when the key is created (done;
  occamyd's issuer and audience are both `occamy`, `auth.Issuer` and
  `auth.Audience`).

## Implementation notes: config v1

- **What was done:** `config.Version` sniffs a file (no `version` key is
  v0, `version: 1` is v1, anything else, `version: 0` included, is an
  error). `config.ParseV1` decodes with `KnownFields`, fills in
  defaults, normalizes targets and origins, and returns `Problems`, every
  problem with its line and key path. The v0 path (`Init`, `parse`,
  `Runtime`, `JWT`) is unchanged. `Credentials.Password`, `PrivateKey`
  and `OIDC.ClientSecret` read secrets at use time. The documented
  example is `internal/config/testdata/v1.yaml`, and the reference is
  [docs/configuration.md](../../docs/configuration.md).
- **Decisions:**
  - A `guacd` section (address, per-protocol params, the existing `Guacd`
    type) serves VNC and RDP through the bridge. Its params may not set
    `hostname` or `port`, which come from the target, nor `password`,
    `passphrase` or `private-key`.
  - Keys that name key files end in `_file` (`signing_key_file`,
    `public_key_file`, `client_secret_file`), as `password_file` and
    `private_key_file` do, so a reader never takes a path for key
    material. `tls.cert` and `tls.key` keep the names of decision 7.
  - A YAML walk beside the decoder gives each problem its line and path,
    and explains what `KnownFields` rejects: a likely misspelling ("did
    you mean connections?"), v0 keys (`address`, `mode`, `jwt_secret`)
    with their v1 equivalents, and inline secrets (`password`,
    `private_key`, `client_secret`) with the references to use. The
    rejected value never reaches a struct or a message.
  - Credentials have exactly one source. `prompt: true` stores nothing
    and asks the owner for whatever the target requires, so it also
    covers targets with no authentication. `private_key_file` is for ssh
    only, and ssh needs a `username` unless it prompts.
  - References are checked by opening and closing each file and looking
    up each variable (set and not empty); secrets are read when used, so
    a rotated secret takes effect on the next session. A trusted issuer's
    public key is no secret, so it is parsed with `auth.LoadPublicKey`.
    The signing key may be missing, since `internal/auth` creates it, if
    its directory exists.
  - Allowed users and groups are checked only when static users are the
    only identities: OIDC claims and trusted issuers' tokens name users
    and groups the file does not list. The task row now says so. An
    `allow` with no entries is an error rather than "nobody".
  - Protocols are ssh, vnc, rdp and telnet (guacd-bridge's table;
    Kubernetes has no host:port target). A target without a port gets
    22, 5900, 3389 or 23, and is stored as `host:port`. IDs are 1 to 64
    of `[a-z0-9._-]` starting with a letter or digit, so `.` and `..`
    cannot be IDs.
  - `access_token_lifetime` defaults to 15m and lies between 1m and 12h,
    `auth.DefaultMaxLifetime`, so occamyd never mints tokens its own
    verifier refuses. bcrypt hashes must have the 60-character form and
    cost 10 or more; `bcrypt.Cost` alone accepts truncated hashes.
  - OIDC: the secret is optional (a public client with PKCE), scopes
    default to `openid profile email` and must include `openid`,
    `subject_claim` defaults to `sub`, and `groups_claim` is unset unless
    configured. Issuer and redirect URLs are https except on localhost.
  - `allow_adhoc_targets: true` requires an allowlist; entries are CIDR
    ranges, IPs, host names or `*.domain`.
  - `legacy_v0` stays a boolean. The v0 routes under a v1 file can sign
    their HS256 tokens with a key made per process, since the v0 client
    never reads them, so no v0 secret is needed in a v1 file.
  - Relative paths resolve against the working directory, since
    `ParseV1` takes bytes, not a path.
- **Deviations:** none from the task row beyond `guacd` and the wording
  of the groups check, both fixed in the row.
- **Surprises:** yaml.v3 skips a whole mapping that has a duplicate key,
  so validation stops after a duplicate rather than report everything in
  it as missing. Durations decode only from strings with a unit; a bare
  `900` is a type error, reported as "not a duration".
- **Follow-ups:**
  - HTTP server: start from `ParseV1`, reject a trusted issuer named
    like occamyd's own issuer, and make `Init` refuse a v1 file until
    then, since the lenient v0 decoder would read one with defaults
    (all three done by the HTTP server task).
  - Native SSH backend: add the host key policy (`host_keys`,
    `insecure_ignore_host_key`) and `term` to connection entries (the
    host key policy is done, by the Sessions task; `term` is not).
  - Sessions: matching rules for `adhoc_allowlist` (resolved addresses
    against ranges), and whether ad-hoc targets need a group (both
    decided; see Implementation notes: Sessions).
  - Per-connection guacd params (an RDP domain per host), a per-protocol
    backend choice (`backend: guacd` after a parity switch), and serving
    the web client from a v1 file have no keys yet.
  - Document: a password hashing command (`occamyd hash-password`) and
    `conf.yaml` as a v1 example.

## Implementation notes: v0 routes

- **What was done:** `server/` serves `POST /api/v0/login` (with
  `client: true`) and `GET /api/v0/connect` on a `net/http.ServeMux`
  behind a request log, with `/static/` and, in debug mode,
  `/debug/pprof/`. `server/jwt.go` issues and checks v0 tokens with
  golang-jwt v5; `accept` and `serveIO` run on coder/websocket.
  `internal/config` no longer imports gin. The Vue client, `tests/` and
  `bench/` use the new paths, and `tests/` and `bench/` use
  coder/websocket. gin, gin-jwt, gorilla/websocket and 28 of 29 indirect
  requirements left `go.mod` (`golang.org/x/sys` stays for the SSH
  backend); `go.sum` went from 115 lines to 18.
- **What v0 clients see:** the paths, nothing else. The login response
  is `{"code":200,"expire":"<RFC 3339>","token":"…"}` as gin rendered it
  (sorted keys, no trailing newline, `application/json; charset=utf-8`);
  the token has the same header and claims (`protocol`, `host`,
  `username`, `password`, `exp`, `orig_iat`) and lives an hour, gin-jwt's
  default timeout. The token is looked up in `Authorization: Bearer`, then
  `?token=`, then the `jwt` cookie, and a malformed header falls through
  to the next. Failures answer `{"code":401,"message":"…"}` with
  `WWW-Authenticate: JWT realm=occamy-proxy`, and 400 for a token without
  `exp`. The messages are gin-jwt's and the golang-jwt v4 texts it passed
  on (`Token is expired`, `signature is invalid`, `invalid signing
  algorithm`, `token contains an invalid number of segments`), mapped from
  v5's errors. As before, an `exp` of 0 or less is expired (v5 reads it
  as no expiry) and an `iat` in the future is refused (v5 ignores `iat`
  unless asked). The subprotocol, text frames, close codes, Guacamole statuses,
  the same-origin rule (an empty or repeated `Origin` header is refused
  with 403, as gorilla refused it; coder/websocket skips the check for an
  empty one), login only with `client: true` and pprof only in
  debug mode are as before. Tests in `server/jwt_test.go` pin each of
  these.
- **Decisions:**
  - The read limit is `guacamole.MaxInstructionSize`, about 4 MiB: the
    largest instruction `guacamole.ReadRaw` accepts (128 elements of 8192
    code points of 4 bytes, with prefixes). gorilla/websocket read without
    a limit, and coder/websocket's default of 32 KiB is short of a single
    such element. Over the limit, coder/websocket closes with 1009 itself,
    so the client sees no Guacamole status, and `clientError` logs it as a
    failure. The clients in `tests/` and `bench/` set the same limit on
    occamyd's messages.
  - Compression stays off, as it was with gorilla's defaults. Both
    libraries write one frame per message, so bytes on the wire are
    unchanged.
  - coder/websocket closes the connection when the context of a read or
    write ends, so `serveIO` runs I/O without a context, as gorilla ran
    without deadlines, and the read-deadline trick became
    `Close(code, status)` once the first side ends. That close carries the
    status and unblocks the reader and any blocked write. It is a closing
    handshake: up to 5 s to send the close frame and 5 s to receive the
    client's, where gorilla closed at once. A client that does not answer
    keeps its handler, and its session entry, up to about 10 s longer; a
    joiner in that window starts a new session, as for any ended one. The
    write mutex went, since coder/websocket serializes writes.
  - After a message over the read limit, which coder/websocket answers
    with 1009 itself, occamyd drops the connection with `CloseNow`
    instead of closing it: `Close` would first read the rest of the
    declared message with no deadline, so a client that declares a huge
    frame and goes quiet would hold its handler and session forever.
    `CloseNow` cannot interrupt a `Close` already under way, so a timer
    around `Close` would not help; the v0 routes keep no `net.Conn` to
    close under it, as v1 streams later do.
  - `clientError` reads `websocket.CloseStatus`: 1000, 1001 and 1005 mean
    the client left, any other close code and `ErrMessageTooBig` are
    failures, and any other error counts as leaving. coder/websocket
    reports a dropped connection as a plain error, not as 1006.
  - `mode` keeps gin's meaning: empty is debug, which serves pprof, and an
    unknown mode fails at start, as `gin.SetMode` panicked. Debug mode now
    logs a warning at start.
  - Start-up rejects an `auth.jwt_alg` other than HS256, HS384 or HS512
    (empty is HS256, as in gin-jwt; its RS algorithms needed key files
    that v0 configuration cannot name) and an empty `auth.jwt_secret`,
    which gin-jwt accepted and with which anyone can sign tokens. A v1
    file handed to `config.Init` has no `jwt_secret`, so occamyd now
    refuses it at start instead of serving with defaults.
  - `proxy.legacyV0` is always true, since `config.Init` reads only v0
    files, and a v0 file implies `legacy_v0`. `config.File.LegacyV0` from
    Config v1 was not wired yet, since nothing started occamyd from a v1
    file; the HTTP server task wired it.
  - The request log writes the quoted method and path, the status, the
    duration and the remote address: no query string, no headers, so
    neither the token nor the credentials in it. gin's logger wrote the
    query string. A request is logged when it ends, so a WebSocket's line
    appears with status 101 when its session ends.
- **Deviations:**
  - Decision 9's "the v0 routes keep `gorilla/websocket` until they are
    removed" is superseded by this task.
  - Login reads JSON and forms (URL-encoded or multipart), limited to
    1 MiB; gin's binding also took XML, YAML, TOML and protobuf bodies,
    which no v0 client sends.
  - A token without the four claims as strings gets 401; gin-jwt's v0
    panicked on it after the upgrade.
  - A wrong method gets 405 from the ServeMux where gin answered 404; a
    plain GET with a valid token gets 426 from coder/websocket where
    gorilla answered 400. The log names the peer address, not gin's
    `X-Forwarded-For` client IP.
  - There was no recovery middleware: `net/http` logged a panic and
    dropped the connection where gin answered 500. The HTTP server task
    added it back: a v0 route answers a panic with a bare 500, as gin's
    recovery did.
- **Surprises:**
  - gin-jwt's 400 for an `exp` of the wrong type was unreachable: golang-jwt
    v4 had already rejected such a token as expired, with 401. Only a
    missing `exp` reached the 400, which is kept.
  - gosec's G120 has no sanitizer, so `http.MaxBytesReader` does not
    silence it on `ParseMultipartForm`; the call carries a `#nosec`.
  - occamyd does not read the WebSocket during the guacd handshake, as
    before, so a client's close frame waits for the handshake. One test
    drops its stalled client instead of closing it, since coder/websocket's
    `Close` waits for the answer.
- **Benchmark:** the harness's `occamy` series now dials with
  coder/websocket, and occamyd's own WebSocket library changed too. Bytes
  are unaffected, as above, and the `guacd` series does not touch either
  library. The `occamy` series' server CPU in
  [benchmarks.md](../../docs/benchmarks.md) was measured on gorilla and
  has not been measured again.
- **Follow-ups:** the HTTP server task, starting from `ParseV1`, mounts
  these routes on its mux with its recovery middleware when `legacy_v0`
  is set, gives them a per-process HS256 key under a v1 file (see Config
  v1's notes; `newTokens` takes any secret), and decides whether v0's
  pprof moves to the debug listener (all done; see its notes). 004 keeps
  the token out of the URL and the other v0 items. The end-to-end and
  pressure tests ran in CI only.

## Implementation notes: HTTP server

- **What was done:**
  - `occamy.go` reads the file once (`config.Load`) and sniffs
    `config.Version`. A v0 file goes through `config.Init` and
    `server.RunV0` as before; `config.Init` now refuses a v1 file. A v1
    file goes through `config.ParseV1` and `server.Run`.
  - `server/` serves a v1 file on a `net/http.ServeMux` behind plain
    `func(http.Handler) http.Handler` middleware, outermost first:
    `limitBodies` (1 MiB), the request log, `recovery`, and
    `apiFallback`.
    `authenticate` is per route. Routes: `GET /healthz`, `POST
    /api/v1/auth/token`, and with `legacy_v0` the v0 login and connect.
  - The signing key comes from `auth.LoadOrCreateKey` with a warning
    when it is created; the verifier trusts occamyd's key under
    `auth.Issuer` and each trusted issuer's key under its name. TLS
    comes from `tls.cert` and `tls.key`. SIGINT and SIGTERM shut down
    with a drain of up to 5 s; a second signal ends occamyd at once.
    expvar and pprof are on the debug listener only, which is opened only
    when `debug.address` is set, on a port of its own.
  - `connContext`, the public server's `ConnContext`, keeps each
    connection in the request context; `connFrom` returns it below TLS,
    a `*net.TCPConn` on which `stream.SetLowWater` works.
- **Decisions:**
  - **API v1's error shape** is `{"error":{"code":"…","message":"…"}}`
    as `application/json`. `code` is what clients branch on and is one
    of `bad_request`, `unauthorized` (no credentials, or wrong ones),
    `invalid_token`, `token_expired`, `not_found`, `method_not_allowed`
    (with `Allow`), `too_large`, `unsupported_media_type` and `internal`;
    later tasks add codes, never change one. `message` is for people.
    `/api/v1` and paths under it that no route takes get this shape for
    404 and 405, where the ServeMux would answer in plain text
    (`apiFallback` asks `ServeMux.Handler` for the pattern; an empty one
    is the mux's own error). Bodies are compact without a trailing line
    break and carry `X-Content-Type-Options: nosniff`. The v0 routes'
    bodies are untouched.
  - **Sign-in:** `POST /api/v1/auth/token` takes
    `{"username":"…","password":"…"}` and answers OAuth 2.0's token
    response shape (RFC 6749, 5.1): `{"access_token":"…","token_type":
    "Bearer","expires_in":900}`, with `Cache-Control: no-store`. Only
    `application/json` is accepted (415 otherwise), so a cross-site HTML
    form cannot post to it without a CORS preflight; unknown fields and
    a second JSON value are 400, so the body can be extended later
    without guessing what old clients sent. Wrong password and unknown
    user get the same 401 (`unauthorized`, `WWW-Authenticate: Bearer
    realm="occamy"`). The body limit is 64 KiB.
  - **Constant time:** an unknown user's password is compared with a
    dummy hash, so bcrypt runs either way. bcrypt's time is set by its
    cost, so a fixed dummy at cost 10 would tell users at cost 12 from
    unknown ones. The dummy is generated at start, from random text, at
    the highest cost among the static users' hashes (10 without users),
    and occamyd warns when the users' costs differ, since then the time
    still tells some users apart. User names are compared through a map
    lookup, which leaks nothing measurable beside bcrypt.
  - **Authentication** takes the token from exactly one `Authorization:
    Bearer` header; the scheme is case-insensitive (RFC 9110), where v0
    kept gin-jwt's `Bearer`. The query string and cookies are never
    read. The claims go into the request context (`claimsFrom`), with
    the issuer, so later tasks can tell a static user from a trusted
    issuer's user of the same name. A failure is 401 with a RFC 6750
    challenge: `unauthorized` without a token, `token_expired` for an
    expired one, `invalid_token` for anything else (malformed, a session
    token, an unknown issuer, a wrong audience, a bad signature). The
    response never repeats the token.
  - **Issuer and audience:** both `occamy`. `ParseV1` rejects a trusted
    issuer named `occamy`, since the issuer picks the key and such an
    issuer would stand in for occamyd; `newServer` refuses it too, for a
    `File` built another way.
  - **Recovery** logs `panic serving "METHOD" "/path": value` with the
    stack, never the query. A string or error value is quoted; any other
    value is named by its type only, since `%v` of a request, a URL or a
    struct holding one prints the query and headers, so a handler must
    not panic with a string or error that carries a secret. It answers
    500 `internal` unless the handler had begun its response, such as
    an upgraded WebSocket.
    `http.ErrAbortHandler` passes through. The v0 routes are each
    wrapped in `recoverV0`, which answers a bare 500 as gin's recovery
    did, also on a v0 file's mux.
  - **Request log:** v0's `logRequests`, unchanged: quoted method and
    path, status, duration, peer address. A sign-in logs `user "alice"
    signed in` (since OIDC, with `, idp local`) or `sign-in failed from
    <address>`, without the name,
    since people type passwords into it. The v0 login logs why a body
    did not parse (too large, or the decoder's error type) without the
    decoder's message, which quotes the byte it stopped at in a JSON
    body and the malformed escape in a form, both possibly from the
    password.
  - **Body limit:** `limitBodies` is the outermost middleware, on
    `net/http`'s own response writer. `http.MaxBytesReader` tells the
    writer that a body went over the limit through an unexported method,
    `requestTooLarge`, which no wrapping writer can pass on; only on
    `net/http`'s writer does `net/http` then close the connection after
    the response instead of reading the rest of the body.
  - **Listener:** HTTP/1.1 only (`http.Server.Protocols`), with or
    without TLS. The streams are WebSockets, which coder/websocket
    upgrades by hijacking an HTTP/1.1 connection; Go's HTTP/2 server
    does not advertise RFC 8441 (behind `GODEBUG=http2xconnect=1`), but
    if a later Go does, a browser on HTTP/2 would try a WebSocket that
    coder/websocket cannot serve. It also keeps one request per
    connection at a time, so `TCP_NOTSENT_LOWAT` on a connection affects
    one stream. TLS 1.2 is the minimum. Timeouts: 10 s for headers, 30 s
    to read a request and to write a response, 2 min idle; `net/http`
    clears the read and write deadlines when a handler hijacks the
    connection, so they do not end WebSockets. Headers are limited to
    64 KiB.
  - **Shutdown** closes the listeners at once and drains requests for up
    to 5 s, inside the 10 s `docker stop` allows, then closes what is
    left with a warning. A drain that runs out is a normal stop: `Run`
    returns nil and occamyd exits with status 0; only a server that fails
    is an error. `http.Server.Shutdown` neither waits for nor closes
    hijacked connections. A listener that fails stops the other server
    too. `Run` stops handling signals once the first arrives, so a
    second SIGINT or SIGTERM gets Go's default handling and ends occamyd
    at once, as `signal.NotifyContext` alone would not.
  - **Debug listener:** expvar at `/debug/vars` and pprof under
    `/debug/pprof/`, with the request log and recovery, no write timeout
    (a CPU profile takes as long as asked), and a warning at start when
    `debug.address` is not on the loopback interface. It must not share
    the public listener's port where the two could take the same
    connections: `config.Overlap` compares ports as numbers and counts an
    empty or unspecified host as every address of both families (Go
    listens dual-stack on it), `localhost` as `127.0.0.1` and `::1`, and
    other names by name. `ParseV1` refuses such a pair; `listen` checks
    again after binding, against the bound address too, for a `File`
    built another way. Whether the system lets the pair bind varies
    (macOS lets a specific address bind beside a wildcard, and then
    hands it that address's connections), so the check does not rely on
    the bind failing. Under a v1 file, v0's pprof moves here; a v0
    file's `mode: debug` still serves it on the public listener.
  - **Version sniffing:** `config.Version` reads every YAML document. A
    `version` brought in by a merge key (`<<`) counts, as the decoder
    would take it; a key of the mapping wins over a merged one, and an
    earlier merge over a later one. A `version` key in any document but
    the first is an error, where it used to read the file as v0 from an
    empty first document.
  - **v0 routes under a v1 file:** login and connect, signed with
    `rand.Text()` as the HS256 secret, made per process, and a warning
    at start that the login signs any target for anyone. No `/static/`,
    since a v1 file has no key for the web client, so the Vue client
    reaches them through a reverse proxy that serves it on the same
    origin. `proxy.login` is now separate from `proxy.client` for this.
- **Deviations:**
  - The row's "graceful shutdown" also bounds the drain, and the row did
    not name `ConnContext`, which session-core's viewer writer notes
    assigned to this task.
  - A v0 file's mux gained the v0 recovery, as gin had; nothing else on
    the v0 path changed (its listener, timeouts and 5 s drain stay).
- **Surprises:**
  - The body limit closed no connection while the request log wrapped
    the writer, for the unexported `requestTooLarge` above; the old
    comment on `limitBodies` claimed it did. Go's client also takes
    `Connection: close` out of the response's headers into
    `Response.Close`, which is what the test reads.
  - gosec's taint analysis followed `TestRunChild`'s environment
    variable through `config.ParseV1` into two start-up warnings that
    log configuration values (G706); they carry a `#nosec`, since the
    values are the operator's.
  - `ConnContext` receives the `*tls.Conn` under TLS, which is no
    `syscall.Conn`, so `SetLowWater` would have silently done nothing;
    `connFrom` unwraps it with `NetConn`.
  - `bcrypt.CompareHashAndPassword` does not refuse passwords over 72
    bytes, unlike `GenerateFromPassword`; it compares the first 72, as
    `htpasswd` hashed them, so no length check was added.
  - gosec reads the challenge string `Bearer realm="occamy"` as a
    hard-coded credential (G101); it carries a `#nosec`.
- **Mutation checks:** each of these was reverted and its test failed:
  bcrypt skipped for an unknown user, the dummy at a fixed cost, a
  different reply for an unknown user, a wrong password accepted, a
  token taken from the query, a session token admitted, expiry not told
  apart, the query string in the request log, no recovery, the v0
  recovery answering JSON, pprof on the public mux, a debug listener
  without `debug.address`, a trusted issuer named `occamy` (in `ParseV1`
  and in `newServer`), `Init` reading a v1 file, `connFrom` returning
  the TLS connection, shutdown without a drain, and TLS not served.
  After review, also: `recovery` taken out of `routes`; `limitBodies`
  taken out of `routes`, or moved inside the request log; TLS 1.0
  allowed; no `ReadHeaderTimeout`; a drain that runs out returned as an
  error; the first signal left registered, which swallows the second;
  `listen` trusting the `File`; validation comparing the address
  strings; an empty host, an unspecified host or `localhost` compared by
  name; port 0 overlapping; merge keys ignored by `Version`; `Version`
  reading the first document only, or skipping a version in a later
  one; `/api/v1` answered in plain text; panic values logged with
  `%v`; and the v0 login logging the decoder's message.
- **Follow-ups:**
  - OIDC: register its routes on the same mux; the cookie carrying state
    and verifier needs `Secure` when TLS is served here or by a proxy,
    which the server cannot tell; issue tokens through the same signer
    (`staticUsers.signer` or a shared one). All done; `redirect_url`'s
    scheme tells what the browser sees (see OIDC's notes).
  - Sessions: wrap its routes in `authenticate(s.verifier)` and read
    `claimsFrom(r.Context())`; use `decodeJSON`, `respond` and
    `writeError`, adding codes such as `forbidden` and `conflict` (done;
    it added `forbidden` and needed no `conflict`).
  - Identity: decided with OIDC: a name belongs to one source unless
    the configuration links them (see OIDC's notes). Trusted issuers
    get the same rule with Sessions, in its review: a trusted issuer's
    `sub` that names a static user is refused unless the issuer has
    `link_static_users`. `claimsFrom` carries `iss`, and occamyd's
    access tokens carry `idp`, so the sources can be told apart.
  - Sign-in has no rate limit, per address or per user; each attempt
    costs one bcrypt comparison at the users' highest cost, so a flood
    of attempts is also a CPU cost.
  - CORS for browser clients on other origins (from `origins`) is not
    served; API v1 answers no preflight yet.
  - `connFrom` unwraps only `*tls.Conn`. A listener that wraps
    connections, such as one for the PROXY protocol or a connection
    limit, would hide the `*net.TCPConn`, and `stream.SetLowWater` would
    silently do nothing. Before occamyd takes such a listener, `connFrom`
    needs to unwrap it too, or the wrapper to pass on `syscall.Conn`.
  - Streams: call `stream.SetLowWater` on `connFrom(r.Context())`
    before `websocket.Accept`; end streams on shutdown through
    `http.Server.RegisterOnShutdown`, since `Shutdown` ignores hijacked
    connections (the drain does not cover them); check the subprotocol
    token with `s.verifier`; the public server lives in `server.Run`, so
    the hook needs a way in, such as a `server` field (all done;
    `server.publicServer` registers the hook).
  - Document: `docs/api.md` for `/healthz`, the token endpoint and the
    error shape above; certificate reload (today a restart) and the
    HTTP/1.1 decision in the TLS section.

## Implementation notes: Sessions

- **What was done:**
  - `server/backends.go`: the backend factory, a map from protocol to a
    maker. `ssh` makes the native SSH backend, `vnc` the native VNC
    backend, and `rdp` and `telnet` the guacd bridge, with
    `guacd.params.<protocol>` under the target, the user name and the
    stored password. A prompting ssh connection without a user name gets
    a backend that asks the owner for one, then dials.
  - `server/sessions.go`, every route behind
    `authenticate(s.verifier, s.ids)`:
    - `GET /api/v1/connections`: `{"connections":[{"id","name",
      "protocol"}],"adhoc_targets":false}`, the connections whose allow
      list names the caller or one of the caller's groups, in the
      file's order. Targets are left out.
    - `POST /api/v1/sessions`: `{"connection":"lab"}`, or an ad-hoc
      `{"protocol","target","username","host_keys",
      "insecure_ignore_host_key"}`. 201 with `Location` and
      `{"id","role":"owner","stream_token","expires_in":300}`.
    - `GET /api/v1/sessions/{id}`: `{"id","connection","protocol",
      "backend","class","size":{"width","height"},"viewers":[{"id",
      "role"}],"opened"}`; `connection` is absent for an ad-hoc target
      and `size` until the remote reports one.
    - `DELETE /api/v1/sessions/{id}`: 204 once the session has ended.
    - `POST /api/v1/sessions/{id}/shares`: `{"role":"viewer"}` or
      `"read-only"`, and `expires_in` in seconds, 60 to 43200, an hour
      if absent. 201 with `{"token","role","expires_in"}`.
    - Replies that carry a token or a session's state are
      `Cache-Control: no-store`.
  - `server/adhoc.go`: `adhoc_allowlist`'s matching.
  - The session core: `Session.Viewers` lists the viewers, the owner
    first, for the session's state, and `Registry.JoinShared` takes the
    session's ID and refuses a share of another session, which it
    accepted before.
  - Connections gained `host_keys` and `insecure_ignore_host_key`
    (native-ssh-backend, decision 2): the SSH backend refuses to start
    without a host key policy, so no ssh connection of a v1 file could
    have opened. An ssh connection needs exactly one, other protocols
    neither, and each key is checked with the backend's parser,
    `ssh.CheckHostKey`.
- **Decisions:**
  - **Backend per protocol:** native SSH and native VNC; the guacd
    bridge for RDP and telnet. No configuration key picked another
    backend then, since `backend:` was new configuration that no spec
    had settled beyond guacd-bridge's parity gate. (Superseded: `backend:`
    now picks it, and VNC defaults to the bridge; see Implementation
    notes: backends and the API reference.) RDP gets
    `disable-gfx: "true"` unless `guacd.params.rdp` sets it, trading the
    graphics pipeline's bandwidth, saved between guacd and the remote
    rather than on the viewers' links, for exact colours (guacd-bridge's
    follow-up). Kubernetes is not registered: it has no `host:port`
    target. The hello's graphical size reaches guacd capped at 8192, as
    v0's display hints are.
  - **Owners are principals (revised in review):** a session belongs to
    the issuer and `sub` of the token that opened it
    (`server/identity.go`), so a trusted issuer acts for its own users
    only. The first version took a trusted issuer's `sub` for the
    static user of that name, which let a gateway's `alice` take over,
    close and share static `alice`'s session. A trusted issuer's token
    whose `sub` names a static user is refused, unless the issuer has
    `link_static_users: true`, the switch OIDC sign-in (#110) adds under
    `auth.oidc`, and then acts as that user. The stream token of a
    trusted issuer's user carries the issuer in `uiss`, which only
    occamyd's own tokens may set. Allow lists and groups still match by
    name: they name no issuer.
  - **Where occamyd's users signed in (added once #110 merged):** for
    occamyd's own tokens the key is issuer, `idp` and `sub`, so an OIDC
    user and a static user are two owners, except that a static user's
    name is always `(occamy, local, sub)`: OIDC sign-in takes such a
    name only when `auth.oidc.link_static_users` says the two are one
    person, so the linked OIDC user owns the static user's sessions,
    whichever opened them. Its access token keeps `idp: oidc` for the
    log, and so does the stream token, which `sessionUser` signs with
    the access token's `idp`. A token without `idp` predates OIDC
    sign-in and is a static user's. A token that says `idp: oidc` with
    a static user's name, by `config.SameName` as sign-in compares, is
    refused once the link is off, as after a restart with tokens still
    out. Trusted issuers keep `(issuer, "", sub)`; a linked one's static
    names are `(occamy, local, sub)`. #110 refused every session token
    with `idp`; `auth.Verifier` now refuses it only from a trusted
    issuer, as `uiss`, since occamyd's own stream tokens carry it. A
    trusted issuer's access token may still carry an `idp`, which
    nothing reads. `auth.Signer.SessionFor` takes an `auth.User`
    (issuer, `idp`, subject). `TestOIDCOwners` covers the linked user,
    an OIDC user of another name, and the refusal after unlinking.
    A trusted issuer's subject now passes the checks OIDC sign-in
    makes of one: `validSubject`, and a static user's name by
    `config.SameName`, so `ALICE` or a decomposed `zoë` is refused
    without `link_static_users` as at sign-in. With it, only an exact
    static name is the static user; another case or form is the
    issuer's own user, as an OIDC user of that name is.
    `identities.takesStaticName` is the one comparison both paths
    use.
  - **A trusted issuer's session tokens (delta review):** `join`
    compared the principal with the owner only for the `owner` role,
    and `Claims.check` let a session token name no subject, so an
    unlinked gateway could mint a `viewer` token for no one, join
    static alice's session and type into it. Every token now names its
    user (`auth.Claims.check` refuses a session token without `sub`,
    and occamyd signs none), every subject passes
    `config.UsableName`, the rule `validSubject` was and that static
    users' names now pass too, and a trusted issuer's session token of
    any role joins only a session whose owner is one of that issuer's
    users, or a static user when it is linked
    (`identities.grants`). occamyd's own session tokens are signed
    only for the owner. `TestIssuerSessionTokens` runs every role
    against static alice's, the gateway's carol's and the linked
    issuer's dave's sessions.
  - **A trusted issuer's groups are opt-in (delta review):** an
    issuer's `groups` claim counts only for the groups in its
    `auth.trusted_issuers[].allowed_groups`, none by default. Before,
    an unlinked issuer could claim `ops` and open every connection
    allowed to `ops`, stored credentials included, which made trusting
    an issuer trust it with every group. `identities.of` drops the
    others from the claims, so every route that reads groups sees only
    the allowed ones. `TestIssuerGroups` and `TestListConnections`
    cover the REST and stream routes.
  - **Panics end a stream or a session, not occamyd (delta review):**
    `serve` recovered a panic of `stream.Serve`, but the reader runs on
    a goroutine of its own, and the session's backend on others, where
    net/http's recover does not reach, so a client's message that made
    either panic would crash occamyd. The reader now returns a
    `session.PanicError`, and `Serve` closes the stream with 1011. The
    session recovers a panic of the backend's `Run` and of every step
    of its owner goroutine, where commands such as an input reach the
    backend, and ends with the new reason `session.Internal`, which
    viewers get as `close` 1011 "internal error"; a command whose call
    panicked returns `ErrEnded` to its caller. Both log the stack.
    `TestReaderPanics`, `TestBackendPanics` and
    `TestBackendPanicEndsSession` inject the panics.
  - **Stalled viewers are dropped (delta review):** each viewer's
    writer keeps the screen it last sent, a terminal's whole screen of
    up to 1000 by 500 cells, about 11 MiB, and a viewer that read its
    welcome and then stopped kept it for as long as it stayed, while
    the owner's redraws gave each such viewer another screen:
    50 stalled viewers on each of a user's 10 sessions held about
    5 GiB. `streams.viewer_timeout`, 30 s by default and at least 1 s,
    now drops a viewer with the new close code `too-slow` 4008 once a
    frame has waited that long since its end, or since the last ack
    that acknowledged a frame, and wraps every write in a deadline of
    that length (`stream.Config.ViewerTimeout`, `stream.ErrStalled`,
    `pacer.Oldest`). A hidden viewer's time runs too: a hidden client
    gets no new frames but acknowledges those it was sent as they
    arrive, as docs/protocol.md now says, since a browser tab in the
    background still gets its messages though it does not draw. A
    first cut paused the time while the viewer was hidden, so that a
    client that acknowledged only what it drew kept its background
    tabs; but a viewer that acknowledged its snapshot and then hid
    kept a screen the session had left for as long as it stayed, the
    same attack for one more message. The terminal writer therefore
    also drops the screen it diffs against while the viewer is
    hidden, and sends a `term-snapshot` once it is visible again, so
    a hidden viewer holds no screen it was sent. One that may type
    still holds the screen of its last input, which the session keeps
    for each viewer to tell an input's effect from motion
    (`Viewer.inputScreen`), hidden or not. What remains at the
    defaults: every visible viewer that acknowledges within the
    timeout holds its last-sent screen, and every viewer that may type
    its last input's, so one user's 10 sessions of 51 viewers hold up
    to 510 screens of 1000 by 500 cells, about 5.5 GiB, if they are
    read-only, and up to 1020, about 11 GiB, if all may type; hidden,
    those that may type hold 510 and read-only ones none. `limits`
    bounds it, and docs/configuration.md says so.
    `TestStalledViewersRelease` holds eight stalled viewers at
    10.7 MiB each and finds 0.5 MiB each once they are dropped, and
    `TestHiddenViewersRelease` finds eight read-only viewers that
    acknowledged and hid holding about 1.3 MiB each while they stay,
    the writer's buffer of the last snapshot it encoded, not a
    screen; `TestStalledViewer`, `TestStalledViewerTimely`,
    `TestStalledAckRestarts`, `TestStalledHidden`, `TestBlockedWrite`,
    `TestTerminalHidden` and `TestStalledViewerTooSlow` cover the
    rule. Mutations of the write deadline, the stall timer, the ack's
    restart, the close, the server's timeout, the minimum and the
    hidden viewer's screen kept are each caught; the server's own case
    for `ErrStalled`, which only keeps the drop out of its error log,
    is not, as the close it would send has gone already.
  - **A limit for the whole server (delta review):**
    `limits.sessions`, 1000 by default, bounds the sessions occamyd
    runs whoever owns them, since `sessions_per_user` bounds each user
    and a trusted issuer can mint new subjects without end. It counts
    in `server.running` beside `owned`, under the same lock, from the
    check to the session's end. One more is `503 limit_exceeded`, as
    the server, not the caller, is full, and `close` `forbidden` on a
    connection's stream, as the user's own limit is.
  - **Absent rather than forbidden:** a connection the caller may not
    open answers 404, as an unknown one does, and so does another
    user's session on `GET`, `DELETE` and shares, so IDs cannot be
    probed. Only the owner reads, closes and shares a session; there is
    no administrator role yet.
  - **Opening without streaming:** `POST /api/v1/sessions` starts the
    session at once. The owner's viewer that the registry returns leaves
    at once, which starts the owner grace of 60 s (session-core) in
    which the owner's stream must join, with the stream token (a session
    token for the owner, 5 minutes) or an access token. The backend's
    prompts wait for that stream.
  - **Ad-hoc targets** are open to any signed-in user, on
    `POST /api/v1/sessions` only. The request is checked as `ParseV1`
    checks a connection (`config.NormalizeTarget`), an ssh target needs
    `host_keys` or `insecure_ignore_host_key: true` as a connection
    does, and the credentials are always prompted for, so a request
    carries no secret. Nor does the target get one from the file: it
    takes only the display's `guacd.params` (see "guacd parameters"
    below), and its VNC certificate is checked against the system's
    roots and the name the request gave. With ad-hoc targets off, or
    outside the allowlist, the answer is 403 `forbidden`, a code this
    task adds.
  - **Allowlist matching (revised in review):** entries may name ports,
    `:port` or `:first-last` (`config.ParseAllowEntry`); without, the
    protocol's own (ssh 22, vnc 5900-5999, rdp 3389, telnet 23), where
    the first version allowed any port. An IP address must lie in a
    listed range or be a listed address, IPv4-mapped addresses compared
    as IPv4, and an address with a zone is refused. Every host name is
    resolved, within 5 s, and dialled by the first address checked, so
    that a second lookup cannot lead elsewhere: a name that is listed,
    or ends in `.lab.internal` for `*.lab.internal` (the domain itself
    does not), may resolve anywhere; any other name only within the
    listed ranges. Loopback, link-local (169.254.169.254 among them),
    unspecified and multicast addresses are refused whatever range or
    name leads to them, unless an entry is that very address. The first
    version dialled listed names by name, which let DNS lead them to
    such addresses. A list without ranges resolves only the names it
    lists. The name is kept beside the address for a certificate's
    server name.
    (Delta review: the special set also holds the metadata services
    outside the link-local range, `fd00:ec2::254` and
    `100.100.100.200`, and, by address and port, guacd's address, its
    name resolved at start, and occamyd's `listen` and `debug.address`,
    a listener on no host standing for every interface address
    (`allowlist.reserve`), so that a range with explicit ports cannot
    reach them. A name that does not resolve at start is logged. The
    host's other addresses stay a documented warning: making every
    interface address special would refuse the hosts a range is
    usually written for when occamyd runs among them.)
  - **guacd parameters (added in review):** `guacd.params` refuses any
    parameter whose name holds `password`, `passphrase`, `private-key`
    or `secret`, where it refused those three names alone, so that
    `sftp-password` or `gateway-password` cannot sit in the file either.
    (Delta review: names are compared in lower case, so `Password` and
    `SFTP-PASSWORD` are refused, and `hostname` and `port` in any case;
    a name that ends in `-regex` is a prompt's pattern, so telnet's
    `password-regex`, which the first version refused, is allowed.)
    An ad-hoc target takes only the display's parameters
    (`adhocParams`: `color-depth`, `color-scheme`, `cursor`,
    `disable-gfx`, `enable-font-smoothing`, `font-name`, `font-size`,
    `resize-method`), so that an SFTP host, a gateway or a drive meant
    for the configured connections does not follow a target that a user
    chose. Ad-hoc RDP therefore gets guacd's own security and
    certificate defaults, not `ignore-cert` from the file.
  - **Failures of occamyd's own,** such as a password file it cannot
    read, are logged with the user and the target and answered 500
    `internal`, without the cause.
  - **Limits (added in review):** `limits.sessions_per_user` (10),
    `limits.viewers_per_session` (50, the owner aside) and
    `limits.shares_per_session` (100 unexpired), each raisable in the
    file. A session counts toward its owner's from the moment `open`
    reserves it, under the server's lock, until it ends, so concurrent
    requests cannot all pass. Over a limit, the JSON routes answer 429
    `limit_exceeded`, a code this review adds; a connection's stream
    gets `close` `forbidden`, and a viewer beyond the session's limit
    `viewer-limit`. The session core gained `Config.MaxViewers` and
    `ErrViewerLimit`; the registry gained `MaxShares` and
    `ErrShareLimit`, and keeps each session's shares in an index of its
    own, so that `Share` prunes and `remove` drops only that session's,
    where both scanned every share under the registry's lock.
  - A shutdown ends every session, through the registry's context.
- **Deviations:**
  - VNC defaults to the native backend, where guacd-bridge's parity gate
    keeps the bridge until beta.2 with the native backend behind
    `backend: native`; with no `backend:` key, a v1 file cannot send VNC
    through the bridge. (Resolved: VNC now defaults to the bridge, and
    `backend: native` opts in.) telnet goes through the bridge for any configured
    connection: the connection is the opt-in that guacd-bridge's
    Registration row asks for.
  - The row named the native SSH backend and the bridge; native VNC
    registers too. The API table did not say that
    `POST /api/v1/sessions` returns a stream token, nor that a session
    token joins a session's stream. Both are fixed above.
- **Surprises:** none in the routes themselves. The missing host key
  policy above was found when the factory first made an ssh backend
  from a connection.
- **Mutation checks:** each of these failed a test: the allow lists'
  users or groups ignored, every connection listed, any connection
  opened, a session's state, close and shares open to any user, a share
  with the owner's role, ad-hoc targets served while off, an ssh
  ad-hoc target without a host key policy, the allowlist allowing
  anything, allowing a name when one of its addresses is listed,
  `*.domain` matching without the dot, dialling the name it resolved,
  and allowing an address with a zone; `DELETE` that does not close.
  After review, also: the owner compared by subject alone in
  `ownSession`; a vnc connection's `tls` left unchecked; an ad-hoc vnc
  target's certificate checked against its address, not its name. Once
  #110 merged: a static user's name not mapped to `(occamy, local,
  sub)`; an `idp: oidc` token for a static name let through after
  unlinking; a stream token signed without the access token's `idp`;
  `idp` refused on occamyd's own session tokens; a trusted issuer's
  subject not checked by `validSubject`; and its static names compared
  exactly rather than by `config.SameName`. In the delta review, the
  survivors it listed now fail a test each: the session counts taken
  after the check (`TestConcurrentLimits`, over REST and streams, for
  `sessions_per_user` and `sessions`), an ended session left in the
  share index (`TestRegistryShareLimit`), `bridgeConfig`'s width
  unclamped (`TestBridgeConfig`), `askUser` taking a size over ssh's
  in either side (`TestAskUser`), the read limit left at 64 KiB once
  the client is let in (`TestReadLimitAfterHello`), and `*.domain`
  matched without the dot (`TestAllowlist`, whose `evillab.internal`
  now resolves). Allowing an address with a zone, listed above, no
  longer fails one: every allowlist entry is a `netip.Prefix`, and
  `Prefix.Contains` is false for a zoned address, so the check in
  `allow` is a second guard. The final review's survivors fail a test
  each too: `server.running` not given back at a session's end, or
  when its backend is not made or `registry.Open` fails
  (`TestSessionsCapReleased`); a linked issuer's session token
  granted on any session of occamyd's, an OIDC user's too, without
  `owner.idp == auth.IdPLocal` in `grants` (`TestPrincipalMatrix`,
  every principal, role, share and forged `idp` or `uiss` over every
  session path, with and without `auth.oidc.link_static_users`);
  `lastAck` set by an ack of a frame never sent, so that such acks
  kept a viewer forever (`TestStalledBogusAcks`); and `pacer.Oldest`
  reporting the newest frame (`TestOldest`).
- **Follow-ups:**
  - A per-connection `backend:` key (guacd-bridge's parity gate), `term`
    for ssh (the backend's `xterm-256color` until then) and per-connection
    guacd parameters.
  - VNC phase 3's user name and TLS policy (done in review, once #106
    merged): `credentials.username` reaches `vnc.Config.Username`, and a
    vnc connection's `tls` block (`config.ConnectionTLS`: `require`,
    `server_name`, `ca_file`, `pins`, `insecure_skip_verify`) its `TLS`
    through `vncConfig`, with `ca_file` read into `RootCAs` when the
    session starts, as passwords are. Validation refuses `tls` on other
    protocols, `ca_file` with `pins`, a CA file without a PEM
    certificate, and a pin that `vnc.CheckPin` refuses. Pins take the
    forms the backend parses (a PEM certificate, or a SHA-256
    fingerprint in hex or in base64 after `SHA256:`). The first version
    let `insecure_skip_verify` stand beside them, as the backend takes
    it, accepting what is not pinned; since the delta review it is
    refused beside `pins` or `ca_file`, as ssh refuses
    `insecure_ignore_host_key` beside `host_keys`, so that a file does
    not read as pinned while it accepts anything. An ad-hoc vnc target's
    certificate must name the host name the request gave
    (`target.name`), not the address dialled. Left over: an ad-hoc
    request carries no pins or CA, so a server with a self-signed
    certificate is reachable as a connection only.
  - Ad-hoc targets for some groups only, and a rate limit on
    `POST /api/v1/sessions`.
  - A list of the caller's own sessions, `GET /api/v1/sessions`, which
    the table does not have.
  - The session core's `Join` takes no name, so `viewer-joined` carries
    an empty name.

## Implementation notes: Streams

- **What was done:** `server/streams.go`. Both routes authenticate on
  their own rather than through `authenticate`, since a refusal must come
  as `close` after the upgrade. `upgrade` checks the subprotocol offer,
  then `Origin`, the token and `hello`, sets `TCP_NOTSENT_LOWAT` to
  16 KiB on `connFrom`'s connection, and accepts; `wsConn` adapts the
  coder/websocket connection to `stream.Conn`; `serve` hands it with the
  viewer to `stream.Serve` and closes the WebSocket. `server.Run` waits
  for streams after the drain.
- **Decisions:**
  - **Refusals:** a request that offers no `occamy.v1` gets 400
    `bad_request` as JSON. Every other refusal gets the upgrade, `close`
    and a WebSocket close with the same code: 4001 for `Origin`, 4000
    for the token, 1002 or 1003 for `hello` (1009 for one over 2048
    bytes), 4001 for a connection or
    session the token does not open. The read limit is 64 KiB until
    `serve` lets the client in, after the route has verified its token,
    then the agreed message size. A refused client gets 1 s from its
    `close` to the end of the closing handshake, after which the socket
    is closed under coder/websocket, rather than its 5 s each way.
    [docs/protocol.md](../../docs/protocol.md) now gives this order:
    `Origin`, the token found, `hello`, the upgrade, then the token
    verified.
  - **Origin:** none is accepted, since a client that is no browser can
    send any `Origin`, and the token is what protects the route. One
    `Origin`, `http` or `https` with a host, is accepted when its host
    is the request's `Host` and, when occamyd terminates TLS, its scheme
    is `https` (behind a proxy that ends TLS, the scheme cannot be
    told), or `scheme://host[:port]` is in `origins`. A proxy that
    rewrites `Host` needs occamyd's own origin in `origins`. `null`,
    an empty one and a repeated one are refused. coder/websocket's own
    check is off, since it refuses with 403 before `close` can go out.
  - **Token:** from the `occamy.v1.token.` subprotocol, else from
    `Authorization` (one `Bearer` header, as `authenticate` reads it),
    never from the URL. A subprotocol token wins: a bad one is refused
    even beside a good header.
  - **What each route takes:** the connection route takes an access
    token whose caller may open the connection. The session route takes
    the owner's access token; a session token whose `sid` is the route's
    session, with its role, as the owner only if its `sub` is the owner;
    or a share, a token without dots, through `Registry.JoinShared`,
    which now refuses a share of another session. Another
    user, another session's token and an unknown session get 4001 alike;
    a share that does not open the session gets 4000, since it is no
    valid token for it. Tokens are checked at the upgrade only: a
    session's end, not a token's expiry, ends a stream (decision 1).
  - **Owner takeover:** a newer stream of the owner takes over from an
    older one, which gets `close` with 1000 and "the owner connected
    again"; the newer one joins once the older has left, within 5 s.
    Otherwise a connection that died unnoticed would keep the owner out
    of their own session until TCP gives up.
  - **The owner's hello sizes the session:** on the connection route the
    backend is made at that size (columns and rows for ssh, pixels for
    guacd; native VNC took the server's size, and now asks the server
    for the hello's); on the session route the
    owner's hello becomes a `backend.Resize`. A viewer's hello resizes
    nothing.
  - **Sizes (added in review):** a terminal is at most 1000 columns by
    500 rows (`terminal.MaxCols`, `terminal.MaxRows`) and a graphical
    screen at most 8192 pixels a side (`backend.MaxResizeSide`), whether
    the owner's hello or anyone's `resize-request` asks. `screenOf` and
    the stream's reader clamp, the terminal emulator clamps on its own,
    and the SSH backend asks the server for no larger terminal
    (`ssh.MaxCols`, `ssh.MaxRows`). Before, a terminal of up to 65535
    by 65535 cells reached the emulator and the backend.
  - **A stream that panics (added in review):** `serveStream` recovers
    a panic of `stream.Serve`, which a terminal resize that grew the
    rows caused before #107: the viewer leaves the session, the owner's
    next stream does not wait for the dead one, the WebSocket closes
    with 1011 and the stack goes to the log. `net/http` would recover it
    too, but leave the hijacked connection and its reader running until
    the client left. `TestRev111PanicLeaks` injects a panic;
    `TestRev111GrowRowsPanics` and `TestRev111ViewerKillsOwnerStream`
    fail on the code before #107 merged.
  - **A session that ends before its stream (added in review):** when
    `View` before `welcome` finds the session ended, as when its backend
    fails at once, `Serve` sends the session's own end, such as
    `backend-failed`, rather than an internal error.
  - **Capabilities:** terminal level 1, and the codecs
    `internal/encoder` sends (`encoder.Codecs()`: webp, jpeg, deflate,
    raw), with a viewer's encoder from `encoder.New` for the codecs it
    agreed to (`stream.Config.NewEncoder`). Raw tiles alone until the
    Codecs task (#109) merged; `TestStreamCodecs` checks the agreement
    in the client's order, the tiles' codecs, and `unsupported` for a
    client that lists only `png` and `cached`. occamyd's binary now
    links vpx and jpegn, which `scripts/third-party-licenses.sh` picks
    up from the main package's dependencies.
  - **The WebSocket:** coder/websocket drops the connection when the
    context of a read or write ends, and `Serve` ends its contexts as
    soon as it is done, so `wsConn` reads in a goroutine without a
    context, and a write in progress gets 1 s to finish after its
    context ends. `Serve` sends `close` but cannot close the WebSocket;
    `wsConn` keeps the `close` it sent and closes the WebSocket with its
    code and reason, cut to 123 bytes at a character boundary. Compression
    is off.
  - **Shutdown:** `publicServer` registers the server's shutdown with
    `RegisterOnShutdown`, which cancels the registry's context; every
    session ends with `server-shutdown`, which `Serve` sends. `Run` then
    waits up to the drain timeout again for the streams, since
    `Shutdown` neither waits for nor closes hijacked connections.
- **Deviations:**
  - The row's "the session's viewer writer" is `stream.Serve`, now named
    in the row.
  - [docs/protocol.md](../../docs/protocol.md) said the subprotocol
    carries an access or share token; it now names session tokens too.
  - The end-to-end test, `TestEndToEnd`, signs in, opens a session,
    streams it, types and closes it. It first used a minimal client of
    its own in `server/streams_test.go`, since the Go client (`client/`,
    #107) was not on master; it moved to `client.Dial` in review, once
    #107 merged. The minimal client stays for the tests that send
    broken or hand-made messages, which the Go client does not.
- **Surprises:**
  - A frame being written when a client sent a text message or broke the
    protocol cut off the `close` that followed: coder/websocket closed
    the connection when the writer's context ended mid-write. Hence the
    second of grace above, which a test caught on its first runs.
  - `Serve` takes every failed read for a dead connection and sends no
    `close`. A text message is no such failure, so `serve` sends
    `unsupported` for it. After a message over the read limit,
    coder/websocket has sent 1009 itself, and the connection was
    dropped with `CloseNow`, as the v0 routes do. (Revised: a stream
    now completes the closing handshake within `refuseTimeout`, with a
    timer that closes the socket below, which does interrupt `Close`;
    see the follow-up on `TestStreamProtocolErrors`.)
- **Mutation checks:** each of these failed a test: any `Origin`
  accepted, a listed origin accepted with the other scheme, a host
  matched by prefix, a token read from the URL, `Authorization` read
  before the subprotocol, any user taken for the owner, another
  session's token accepted, a viewer's session token taken for the
  owner's, no `TCP_NOTSENT_LOWAT`, no shutdown hook, a connection
  stream made without the hello's size, a viewer's hello resizing the
  session, no takeover, and the WebSocket closed with another code than
  the `close` sent. After review, also: no recover in `serveStream`; no
  `refuseTimeout`; a session token's owner compared by subject alone in
  `join`; and the connection dropped after a message over the limit,
  which `TestFinishAfterTooLarge` failed 20 runs of 20.
- **Not checked live:** the demo SSH and VNC servers were not tried: the
  shared benchmark host's load average was 46 at the time. The tests run
  the routes against fake backends only.
- **Follow-ups:**
  - Recover gaps the final review found, for a PR of their own. A
    panic leaves the session's state locked where a lock is not
    released by `defer`:
    - The VNC and guacd decoders hold rows between `sink.Lock` and
      `sink.Unlock` without `defer` (vnc's `drawBand`, `fillRect`,
      `raw`, `drawCompact`, `drawPalette` and `tightJPEG`, guacd's
      `flush`), so a panic there, which the session's recover turns
      into its end, leaves rows locked, and the viewers' writers wait
      in `ReadTile` forever.
    - SSH's `pump` goroutines run outside the session's recover, and
      `sink.TermData` and `Terminal.Write` hold their locks without
      `defer`; the terminal's parser has no fuzzer.
    - VNC's cleanup after `readLoop` (`close(done)`, `nc.Close()`) is
      not deferred, so a panic in `readLoop` leaves the writer loop
      and the connection behind.
  - The viewer timeout bounds each message's write, so a link too slow
    to carry one message in 30 s, such as a terminal's snapshot of a
    few megabytes at under 100 kB/s, drops its viewer. Tiles are small;
    a terminal's frame goes whole. A deadline per byte, or a terminal
    frame cut into messages the writer can pace, would serve such
    links.
  - Document: `docs/api.md` for the five session routes and the two
    stream routes, with the replies above.
  - CORS is still not served, so a page on another origin in `origins`
    can open streams but not call the JSON routes.
  - ~~A terminal's hello may ask for up to 65535 columns or rows, which
    reach the backend unbounded.~~ Fixed in review: see "Sizes (added
    in review)".
  - ~~`TestStreamProtocolErrors` failed twice in the review's
    full-suite runs at a load average of 112 to 150.~~ Fixed once it
    failed again, with `broken pipe` in its oversized message: the
    write was the test client's ack of a frame it had read before the
    close, not a timing bound of the server's. After a message over the
    read limit, coder/websocket sends close 1009 itself, and `finish`
    then dropped the connection, since `ReadMessage` had marked it
    failed. With the rest of the message unread, closing the socket
    sends a reset, so a client still writing failed, or could lose the
    1009 to the reset. `finish` now completes the closing handshake:
    `Close` sends nothing more, reads the rest of the message and the
    client's close, and has `refuseTimeout` to do it, as a refused
    stream has. `TestFinishAfterTooLarge` writes before it reads the
    close; with the old `finish` it failed every time. Both tests then
    passed 200 runs with `-race`.
  - ~~`TestStreamWelcome` failed under load (delta review).~~ Fixed:
    it took the first `term-snapshot` for a frame of the hello's
    size, then read the session's size. The fake backend sizes its
    terminal when it runs, on a goroutine of its own, so the first
    frame may show the session's 80 by 24 terminal before that, and
    the session sets its size just after resizing its terminal and
    waking the writers. 300 runs with `-race` at `-cpu 1,4` and a load
    average of 157 failed 13 times with "session size (0,0)"; the test
    now waits for a frame of the hello's size and then for the
    session's, and passed the same 600 runs at a load average of 132.
    The first frame of 80 by 24 is the server's behaviour, not only
    the test's: a client may see the blank default terminal for a
    moment before the backend's size, which `resize` then corrects.
    Follow-up: start the session's terminal at the size the owner's
    hello asks for, with a size in `session.Config`, since the
    terminal's `Options.Reply` is the session's own.
  - [009](../009-react-web-client.md) and the Go client: send `hello` in
    the query and the token in the subprotocol; expect `close` before
    `welcome` for a refusal; reconnect as the owner with an access token
    (the stream token lasts 5 minutes); read `close` 1000 with "the
    owner connected again" as being replaced, not as an error.

## Implementation notes: OIDC

- **What was done:**
  - `server/oidc.go` and `server/oidc_cookie.go` serve `GET
    /api/v1/auth/oidc/login`, `GET /api/v1/auth/oidc/callback` and `POST
    /api/v1/auth/oidc/token` when `auth.oidc` is set, on
    `golang.org/x/oauth2` v0.37.0 and `github.com/coreos/go-oidc/v3`
    v3.21.0, which brings `github.com/go-jose/go-jose/v4` (Apache-2.0
    and BSD-3-Clause; the go-licenses check passes). Static and OIDC
    sign-in share one signer. `server.go` changed in five places only:
    the `oidc` field, the signer taken out of the `newStaticUsers` call
    so that both share it, the construction after TLS, `register` in
    `routes`, and a warm-up in `Run`.
  - `internal/config` gained `auth.oidc.return_urls`,
    `auth.oidc.require_known_users` and `auth.oidc.link_static_users`,
    and `File.Admitted`, the users and groups that
    `require_known_users` admits, and `File.KnownGroups`, the groups the
    file names. `internal/auth` gained the `idp` claim on access tokens
    and `Signer.AccessFrom`; `Signer.Access` keeps its signature and
    names `local`.
  - Sign-in: the client sends the browser to `login` with `return_url`
    and `code_challenge`, the S256 challenge of a verifier it keeps.
    occamyd seals state, nonce, its own PKCE verifier, the return URL
    and the client's challenge into a cookie and redirects to the
    provider. The callback matches the state to the cookie, exchanges
    the code, verifies the ID token, maps its claims, and sends the
    browser to `return_url#code=…`. The client posts
    `{"code":"…","code_verifier":"…"}` to `POST /api/v1/auth/oidc/token`
    and gets `/api/v1/auth/token`'s response.
- **Decisions:**
  - **Hand-off: a one-time code in the fragment of a configured return
    URL, exchanged by POST.** The access token in the fragment never
    reaches a server log or Referer, but it sits in history and is
    readable by every script on the page for its 15 minutes. An HttpOnly
    cookie would need the API to read tokens from cookies, which the
    HTTP server ruled out. A page that posts the token to
    `window.opener` puts it in an HTML body and makes every client run a
    popup. The callback's own response is a top-level navigation that no
    script reads. With the code, the access token appears only in the
    body of the exchange, with `Cache-Control: no-store`. The code
    reaches no log or Referer either, since browsers send no fragment;
    it does sit in history, but it lives a minute, is spent on its first
    try, right or wrong, and redeems only with the verifier of the
    challenge the client gave `login`. That binding stops a planted
    code: an attacker who signs in at the provider in their own browser
    and sends the victim to `return_url#code=…` holds the only verifier.
    A cookie could bind the code to the browser, but only on one site
    and not for native clients; the challenge works across origins and
    in the Swift client. Codes wait in memory, by their SHA-256, at most
    4096 at a time and three for one user, whose oldest a fourth drops,
    so that one account at the provider, signing in over and over,
    cannot fill the table for everyone; expired ones are swept when a
    new one comes. A cap per address was left out: behind a reverse
    proxy every sign-in has the proxy's address.
  - **Return URLs** match exactly as written, so no normalization is
    there for an attacker to play with. With one configured, `login`
    may leave `return_url` out. The default is `redirect_url`'s origin
    with the path `/`, the callback's own origin, so no default sends a
    browser anywhere the operator does not serve. Entries are https
    except on localhost, with no user name and no fragment.
  - **Errors:** `login` refuses an unlisted return URL, a missing,
    repeated or malformed challenge, or a `code_challenge_method` other
    than `S256`, with API v1's 400 before it sets a cookie. The
    callback answers `invalid_state`, 400 as JSON, until the state
    matches a cookie, since until then it has no return URL it can
    trust. After that, every outcome goes back to the return URL:
    `#code=…`, or `#error=access_denied` (the provider's
    `access_denied`, or occamyd refusing the user), `sign_in_failed`
    (any other provider error, or an answer that does not verify) or
    `unavailable` (discovery failed, or too many codes wait). A provider
    that cannot be discovered at `login` also sends the browser back
    with `unavailable`. Neither the provider's `error_description` nor
    the token endpoint's body reaches the client or the log, which gets
    the provider's error code and HTTP status code. Text from the
    provider or the client that does reach the log, such as the bodies
    go-oidc puts in its discovery and key set errors, is quoted and
    truncated to 256 bytes, so it can neither forge a log line nor flood
    the log. A bad code or verifier at
    the exchange gets 401 `unauthorized`, as a wrong password does. The
    four codes join API v1's and are defined in `server/oidc.go`, to
    keep `api.go` out of merges.
  - **Cookie:** `occamy_oidc`, or `__Secure-occamy_oidc` when it is
    Secure, so that the browser takes it only from https, sealed with
    AES-256-GCM
    (`cipher.NewGCMWithRandomNonce`) under a key made for the process,
    with its name as additional data. Sealing costs no more than
    signing, and the browser cannot read occamyd's PKCE verifier or the
    nonce. It holds state, nonce, verifier, return URL, the client's
    challenge and an expiry 10 minutes out, which the server checks,
    since `Max-Age` is the browser's to honour. HttpOnly; SameSite=Lax,
    which the provider's redirect back, a cross-site top-level GET,
    still carries where Strict would not; the path of `redirect_url`, so
    it goes only to the callback, behind a proxy prefix too; and Secure
    when `redirect_url` is https or occamyd serves TLS. That answers the
    HTTP server's follow-up without a key: `redirect_url` is where the
    browser meets the callback, so its scheme is what the browser sees,
    whoever terminates TLS. The callback clears the cookie when the
    state matches it, or when the browser holds no sign-in in progress
    (expired, or sealed before a restart); a callback with another
    state, as a link from another site makes, leaves a sign-in under way
    alone, which weakens nothing, since only the state completes it. It
    tries every cookie of the name, so that one set for a parent domain
    cannot shadow the sign-in's own. A restart between `login` and the
    callback therefore fails with `invalid_state`, and a second sign-in
    in one browser replaces the first. That includes a top-level
    navigation to `login` from another site, which ends a sign-in under
    way: a nuisance docs/configuration.md records, not a way in, since
    the second sign-in's code needs a verifier the client does not hold
    (`TestOIDCLoginReplacesFlow`).
  - **ID token:** go-oidc checks the issuer (the configured one exactly,
    which discovery must name too), the audience (`client_id`), the
    expiry (a missing `exp` is expired) and the signature, with
    occamyd's own algorithm list: RS, ES and PS at 256, 384 and 512, and
    EdDSA, whatever the provider advertises, never `none` or an HMAC.
    occamyd then compares the nonce in constant time, which go-oidc
    leaves to its caller, and checks `azp` as OpenID Connect Core 3.1.3.7
    says: with several audiences it must be there, and when there it
    must be `client_id`. An `iss` parameter on the callback (RFC 9207)
    must be the issuer, exactly and once, and must be there when
    discovery says `authorization_response_iss_parameter_supported`,
    which is read as a strict JSON bool. The check runs before the
    provider's `error` is believed, since an error answer carries `iss`
    too (RFC 9207, 2.4). `at_hash` is not checked, since occamyd never
    uses the provider's access token.
  - **Claims:** `subject_claim` names the user: a string of 1 to 255
    bytes of UTF-8 of graphic characters (`unicode.IsGraphic`) with
    U+0020 the only space, or the sign-in is `access_denied`. That
    refuses control and format characters (zero-width spaces and
    joiners, bidirectional overrides, the soft hyphen), the other
    default-ignorable code points, which are graphic but render as
    nothing (`unicode.Other_Default_Ignorable_Code_Point`, such as the
    combining grapheme joiner and the Hangul fillers, and
    `unicode.Variation_Selector`), U+2800 (the blank Braille pattern),
    the line and paragraph separators and the no-break and other spaces,
    which let a name look like another. U+0020 is allowed: an allow list
    cannot name such a user, but a group can admit one. With
    `subject_claim: email`,
    `email_verified` must be `true`, since an address the provider has
    not verified may be anyone's; for a provider that does not send it,
    such as Entra ID, pick another claim. `require_known_users` admits
    a user whom a connection's `allow.users` names, exactly, or who is in
    one of its `allow.groups` by `groups_claim`; static users, and names
    in `allow.users` that are a static user's by `config.SameName`, count
    only with `link_static_users`, since the callback refuses those names
    without it. `ParseV1` refuses it when that admits no one.
    `groups_claim` takes a list of strings or one string; a missing or
    null claim is no groups, and another type fails the sign-in, since
    it is a misconfiguration. Only the groups the file names, as static
    users' groups or in `allow.groups`, are kept, sorted and without
    duplicates. That keeps tokens small for users in hundreds of
    provider groups and changes no decision, since only named groups
    are checked. Claim names are top-level and literal, so namespaced
    claims (`https://example.com/groups`) work and nested ones
    (Keycloak's `realm_access.roles`) do not.
  - **Identity: a name belongs to one source unless the
    configuration links them.** occamyd's tokens say `iss: occamy`
    after either sign-in, and allow lists name users in one namespace,
    so an OIDC user named `alice` would be static user `alice`. By
    default the callback refuses an OIDC subject that names a static
    user, with `access_denied`, and logs it. It compares with
    `config.SameName`, equal under Unicode case folding once both are in
    NFC, so `Alice`, or `alice` with a decomposed character, cannot pass
    for static `alice` to a person reading a log or a session list;
    `golang.org/x/text`, already a dependency, does the NFC. Access
    itself, by allow lists and `require_known_users`, compares names
    exactly, so a looser rule there could only widen it.
    `auth.oidc.link_static_users: true` lets the name through, for a
    provider that holds the same people: the OIDC user takes the static
    user's place in allow lists, with the groups of `groups_claim`, not
    the static user's (`TestOIDCLinkedGroups`); docs/configuration.md
    warns that `subject_claim` must then be a claim users cannot choose
    (`sub`, or a verified email). Access tokens record the source in
    `idp`: `local` from `POST /api/v1/auth/token`, `oidc` from the OIDC
    exchange, and each sign-in's log line names it (`user "carol"
    signed in, idp oidc`). `idp` is optional in a token, so tokens
    signed before it, and trusted issuers' access tokens, still verify;
    a session token may not carry one. Trusted issuers get the same rule
    with Sessions (#111). (Revised by #111 once this merged: occamyd's
    own session tokens carry `idp`, and only a trusted issuer's are
    refused for it; see Sessions' notes.)
  - **Discovery: lazy.** `newServer` does not contact the provider, so a
    provider that is down cannot keep occamyd from starting with static
    users. `Run` starts one discovery in the background, so the log
    tells at start whether the provider answers; otherwise the first
    sign-in discovers it. One fetch runs at a time; a failure is
    remembered for 10 s (`oidcRetry`), so sign-ins while the provider is
    down do not each wait for it, and a success is kept until a restart.
    Every request to the provider (discovery, key set, exchange) goes
    through one `http.Client` with a 10 s timeout (`oidcTimeout`),
    responses limited to 1 MiB, and no redirects followed: a 3xx is the
    answer, which fails discovery, the key set or the exchange, so the
    code, the verifier and the client secret reach the discovered token
    endpoint only. go-oidc's discovery and key set fetches take that
    client from the context (`oidc.ClientContext`), and so does oauth2's
    exchange. The discovered `authorization_endpoint`, `token_endpoint`
    and `jwks_uri` must be https, or http on the loopback interface when
    the issuer is too, so a provider elsewhere cannot point occamyd at
    a port of its own host in the clear. With redirects refused, that
    holds for every hop. Their hosts are not restricted: the discovery
    document comes from the configured issuer, over https, and is
    trusted to name them, as it is trusted to name the keys.
  - **Client authentication** follows discovery's
    `token_endpoint_auth_methods_supported`: a public client sends
    `client_id` in the body; a confidential one sends a Basic header,
    the default when the list is absent, unless the provider lists only
    `client_secret_post`. oauth2's auto-detection would try both and
    send the code twice. The secret is read at each exchange, so a
    rotated secret takes effect at once.
- **Deviations:**
  - The row's "signed cookie" is sealed: encrypted as well as
    authenticated.
  - The row named no hand-off to the browser. `POST
    /api/v1/auth/oidc/token` is new; the Proposed API table and the row
    now list it.
  - Three keys the row did not name, `return_urls`,
    `require_known_users` and `link_static_users`, and the `idp` claim.
- **Surprises:**
  - go-oidc's `Verify` checks no nonce and no `azp` and allows no clock
    skew on `exp`; its key set fetches with a context that is never
    cancelled, and its discovery reads the whole body, so the client's
    timeout and the body limit are their only bounds.
  - go-jose does not take an RSA public key as an HMAC secret, so the
    classic algorithm confusion failed even with HS256 allowed. The test
    needs a key set that also lists a symmetric key to show that the
    algorithm list refuses HMAC by itself.
  - go-oidc refuses an ID token whose `sub` is not a string while it
    parses the claims, so that is `sign_in_failed`, not `access_denied`.
  - gosec flags the conditional `Secure` (G124), and `Seal` with a nil
    nonce (G407), which `NewGCMWithRandomNonce` fills in; both carry a
    `#nosec`. Its log injection check (G706) flags the two log lines
    that carry the provider's text even once `quote` has quoted it,
    since it does not see through the helper; they carry a `#nosec`
    that says so.
  - go-oidc puts the provider's response bodies in its discovery and
    key set errors (`503 Service Unavailable: <body>`), line breaks and
    all, so logging its errors with `%v` let a provider forge log lines
    until they were quoted.
  - The review found the first version followed the token endpoint's
    307 and 308, sending the code, the verifier and, with
    `client_secret_post`, the client secret to another origin, and that
    one provider account, looping through silent sign-ins, filled the
    code table so that everyone else got `unavailable`. Both are fixed
    above, with tests.
  - The second review found that `File.Admitted` counted a static
    user's name in `allow.users` without `link_static_users`, so a file
    that admitted no one through OIDC passed validation; that the
    license script piped `go list` into `sort`, so a failing `go list`
    wrote a file with the standard library's license only (it now
    writes each `go list` to a file and checks it, since the image's
    build stage runs dash, which has no `pipefail`, and it now also
    copies `PATENTS` files: Go's, x/crypto's, x/image's and x/text's);
    that `unicode.IsGraphic` passes code points that render as nothing,
    such as U+034F and the Hangul fillers, so `alice` followed by one
    signed in beside static `alice` and logged as her; that the static
    name check was exact, so `Alice` signed in beside her; that the
    `iss` check ran after the `error` branch; and that the docs said a
    linked user gets the static user's access, where the groups come
    from `groups_claim`. All are fixed above, with tests.
- **Mutation checks:** each of these was made and its test failed: the
  state not compared; the cookie's expiry not checked; the cookie not
  cleared; the cookie not HttpOnly, SameSite=None, on the path `/`, or
  Secure only under TLS; the nonce not compared; the signature not
  checked (`none` and a foreign key got through); HS256 allowed (caught
  only by the key set with a symmetric key); the audience, issuer,
  expiry or `azp` not checked; the `iss` parameter not checked; no
  verifier in the exchange; no challenge to the provider; the return URL
  not checked; the client's challenge not required; a code reusable,
  never expiring, or redeemed without its verifier; no limit on waiting
  codes; unknown users admitted; the subject not validated;
  `email_verified` not required; unmapped groups kept; a failed
  discovery not remembered; provider bodies unbounded; discovered
  endpoints not checked; no timeout to the provider (the hanging
  provider's test then hangs); client authentication auto-detected; no
  warm-up in `Run` (`TestRunOIDCProviderDown` runs occamyd as a child
  with a provider nothing serves, and signs a static user in). After
  review, also: redirects followed by the provider's client (caught for
  discovery, the key set and the token endpoint); the provider's text
  logged unquoted or untruncated (forged log lines from discovery, the
  key set and the callback's error); no cap per user on waiting codes,
  or the newest dropped instead of the oldest; `match` reading only the
  first cookie of the name (`TestOIDCPlantedCookie`); `redeem` deleting
  the code only after checking the verifier, and `redeem` looking the
  code up and deleting it under two locks taken back to back
  (`TestOIDCCodeRace`, 32 exchanges racing for one code, 500 times:
  the second mutation leaves a window of nanoseconds, which one round
  of 32 seldom hits, and 500 rounds hit in 19 of 20 runs, and in every
  run under `-race`); format characters and other spaces in a subject;
  a static user's name admitted, or `link_static_users` ignored;
  static users admitted by `require_known_users` without it, or groups
  admitting without `groups_claim`; admission by group not checked;
  `idp` missing from either token, or allowed on a session token; http
  endpoints allowed for an https issuer; the `iss` parameter not
  required when promised; the cookie without `__Secure-`, or sealed
  without its name; and the cookie cleared by a callback whose state
  does not match, or an expired one kept. After the second review,
  also: discovery checking endpoints with `config.SecureURLProblem`
  instead of `endpointProblem` (caught with an https issuer whose token
  endpoint is http on loopback); the `iss` value compared loosely, a
  repeated `iss` taken, or the check moved back after the `error`
  branch; `quote` not quoting short text, and the token endpoint's
  error code or `azp` logged raw; default-ignorable code points or
  U+2800 in a subject; the static name check exact, without NFC or
  without case folding; `File.Admitted` counting static users' names
  in `allow.users` without `link_static_users`; and `match` reading
  cookies of any name, which survived until a test put a sign-in sealed
  for `__Secure-occamy_oidc` in a cookie named `occamy_oidc`.
- **Follow-ups:**
  - Web client ([009](../009-react-web-client.md)): make a verifier of
    43 to 128 characters, keep it in `sessionStorage`, and navigate to
    `/api/v1/auth/oidc/login?return_url=…&code_challenge=…`; on the
    return URL, read `code` or `error` from `location.hash`, remove the
    fragment with `history.replaceState`, and post the code and verifier
    to `/api/v1/auth/oidc/token`. A code found with no verifier held is
    one the client did not start: spend it with a throwaway verifier, or
    ignore it, and never sign in with it. Until CORS is served, the
    client must be on occamyd's origin, since the exchange is a JSON
    POST, which needs a preflight from another origin.
  - Native clients ([010](../010-native-swift-client.md)): return URLs
    with a custom scheme, or on the loopback interface with any port
    (RFC 8252), are not accepted yet.
  - Several occamyd instances: the cookie key and the codes are per
    process, so `login`, the callback and the exchange must reach one
    instance (sticky sessions) until they share state.
  - The provider's refresh token is neither requested nor kept: a client
    signs in again when its access token expires. Signing out at the
    provider (RP-initiated logout) is not served.
  - Nested claims, and a map from provider groups to registry names, if
    a provider needs them.
  - OIDC sign-in has no rate limit either; a sign-in costs the provider
    a round trip, not occamyd a bcrypt comparison. With three codes per
    user, filling the 4096 waiting codes takes about 1400 accounts at
    the provider, each signing in within a minute; a rate limit per
    address (behind a trusted proxy's `X-Forwarded-For`) or per user
    would bound it further.
  - A loop of silent sign-ins (`prompt=none` at a provider that keeps
    a session) costs the provider an authorization and a token request
    each; a provider may throttle occamyd's `client_id` for it, which
    fails everyone's OIDC sign-ins with `sign_in_failed` until the
    throttle lifts. A rate limit as above is the remedy.
  - Starting a sign-in ends the one under way in that browser, also
    when another site navigates to `login`. One cookie per sign-in,
    named by its state, would let them run side by side, at the cost of
    a bound on how many a browser may hold.
  - Names that only look alike, such as `alice` with a Cyrillic `а` or
    a fullwidth `ａ`, are neither refused nor folded: confusable
    detection (Unicode TR 39) is not done. The docs ask for a
    `subject_claim` users cannot choose, which leaves the names to the
    provider.
  - Residual risks of binding the code to the client's challenge, not
    to the browser. First, `login` is a GET anyone can link to: an
    attacker who sends the victim's browser there with a challenge whose
    verifier the attacker holds gets a code for the victim's account
    (silently, where the provider keeps a session) into the victim's
    fragment, and redeems it if the code leaks (history, a script on the
    return page). A client that spends every code it did not start, by
    posting it with a throwaway verifier, closes that window, since a
    code is spent on its first try; docs/configuration.md asks clients
    to. Second, an attacker who learns the victim's challenge (it is in
    the `login` URL, so in a proxy's access log or the history) can bind
    the attacker's own sign-in to it and plant that code at the victim's
    return URL: a login CSRF, as the attacker. Both need a leak beyond
    the flow. Binding the code to the browser as well, with a cookie,
    would close both for clients on occamyd's origin.

## Implementation notes: backends and the API reference

- **Status.** Done in the change that started VNC's opt-in period
  ([guacd-bridge](guacd-bridge.md), Implementation notes: the opt-in
  period).
- **What was done.**
  - Configuration v1: `backend` on each connection (`guacd` or
    `native`, filled with the protocol's default: `native` for ssh,
    `guacd` for the others), `adhoc_backends` for ad-hoc targets by
    protocol, and `ard` on vnc connections
    ([native-vnc-backend](native-vnc-backend.md), Implementation notes:
    registration). ssh refuses `guacd`, rdp refuses `native` until NLA
    has run end to end, telnet has no native backend, and `tls` and
    `ard` need `backend: native`.
  - The backend factory is keyed by protocol and backend; `POST
    /api/v1/sessions`, the connection stream and the ad-hoc path look
    up the connection's or `adhoc_backends`' backend. The session's
    state keeps naming it `ssh`, `vnc` or `guacd`.
  - Routes register through a mux that records their patterns
    (`routeMux`), so that `TestAPIDoc` can check that
    [docs/api.md](../../docs/api.md), the reference of every route
    with its tokens, requests, replies, errors and limits, has one
    section per route of the public and debug listeners.
- **Deviations.** The behaviour change for v1 files on master: vnc
  connections without `backend` now go through guacd, and a vnc
  connection with `tls` must set `backend: native`, or the file fails
  to load with the key named. No release served v1 files' vnc
  connections, so no release changes.
- **Follow-ups.**
  - `decodeJSON` matches field names case-insensitively, as
    `encoding/json` does, so `{"Connection":"x"}` passes the unknown
    field check; docs/api.md does not promise either.
  - The 400s of `GET /api/v1/auth/oidc/login` carry no
    `Cache-Control: no-store`, while the callback's `invalid_state`
    does.
