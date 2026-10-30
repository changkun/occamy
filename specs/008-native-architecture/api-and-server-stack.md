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
updated: 2026-10-30
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
| `GET /api/v1/auth/oidc/callback` | Finish OIDC sign-in and issue an access token |
| `GET /api/v1/connections` | Connections the caller may open (from the registry) |
| `GET /api/v1/connections/{id}/stream` | WebSocket upgrade that opens a session on the connection and streams it, in one round trip ([protocol-v1](protocol-v1.md), Handshake) |
| `POST /api/v1/sessions` | Open a session on a connection, returning the session ID and the caller's role |
| `GET /api/v1/sessions/{id}` | Session state: backend, size, viewers |
| `POST /api/v1/sessions/{id}/shares` | Mint a share token for viewer or read-only access, with an expiry |
| `DELETE /api/v1/sessions/{id}` | Close the session (owner only) |
| `GET /api/v1/sessions/{id}/stream` | WebSocket upgrade that joins a session, with an access or share token; the token travels in `Sec-WebSocket-Protocol` (or `Authorization` for native clients), never in the URL |
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
| OIDC | `GET /api/v1/auth/oidc/login` and `/callback`: authorization code with PKCE through `golang.org/x/oauth2`, state and verifier in a short-lived signed cookie, the ID token checked by `coreos/go-oidc` v3, and subject and group claims mapped to registry users and groups as configured; tests against an in-process identity provider | HTTP server |
| Sessions | `GET /api/v1/connections` filtered by the caller's users and groups; `POST /api/v1/sessions`, `GET` and `DELETE /api/v1/sessions/{id}` and `POST /api/v1/sessions/{id}/shares` on the session registry; a backend factory keyed by protocol that the native SSH backend and the guacd bridge register into; ad-hoc targets only when enabled and allowed; tests on the fake backend | HTTP server; session-core Registry |
| Streams | `GET /api/v1/connections/{id}/stream` and `/api/v1/sessions/{id}/stream` on coder/websocket: `Origin` checked against `origins`, the token taken from the `occamy.v1.token.` subprotocol or `Authorization`, `occamy.v1` answered alone, `hello` read from the query, and the socket handed to the session's viewer writer; a closed session ends its streams whatever tokens remain valid. Tests cover one round trip to `welcome`, a bad origin, a missing or wrong token, and a token in the URL ignored | Sessions; session-core Viewer writer; protocol v1 Handshake |
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
  the v1 configuration and logs a warning when the key is created.

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
    then, since the lenient v0 decoder would read one with defaults.
  - Native SSH backend: add the host key policy (`host_keys`,
    `insecure_ignore_host_key`) and `term` to connection entries.
  - Sessions: matching rules for `adhoc_allowlist` (resolved addresses
    against ranges), and whether ad-hoc targets need a group.
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
  coder/websocket. gin, gin-jwt, gorilla/websocket and 29 of 30 indirect
  requirements left `go.mod` (`golang.org/x/sys` stays for the SSH
  backend); `go.sum` went from 115 lines to 16.
- **What v0 clients see:** the paths, nothing else. The login response
  is `{"code":200,"expire":"<RFC 3339>","token":"…"}` as gin rendered it
  (sorted keys, no trailing newline, `application/json; charset=utf-8`);
  the token has the same header and claims (`protocol`, `host`,
  `username`, `password`, `exp`, `orig_iat`) and lives an hour, gin-jwt's
  default timeout. The token is looked up in `Authorization: Bearer`, then
  `?token=`, then the `jwt` cookie, and a malformed header falls through
  to the next. Failures answer `{"code":401,"message":"…"}` with
  `WWW-Authenticate: JWT realm=occamy-proxy`, and 400 for a token without
  `exp`. The subprotocol, text frames, close codes, Guacamole statuses,
  the same-origin rule, login only with `client: true` and pprof only in
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
    Config v1 is not wired yet, since nothing starts occamyd from a v1
    file.
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
  - Messages for bad tokens are golang-jwt v5's (`token has invalid
    claims: token is expired`); the lookup messages (`cookie token is
    empty`) are gin-jwt's.
  - A token without the four claims as strings gets 401; gin-jwt's v0
    panicked on it after the upgrade.
  - A wrong method gets 405 from the ServeMux where gin answered 404; a
    plain GET with a valid token gets 426 from coder/websocket where
    gorilla answered 400. The log names the peer address, not gin's
    `X-Forwarded-For` client IP.
  - There is no recovery middleware: `net/http` logs a panic and drops
    the connection where gin answered 500. It arrives with the HTTP server
    task.
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
  pprof moves to the debug listener. 004 keeps the token out of the URL
  and the other v0 items. The end-to-end and pressure tests ran in CI
  only.
