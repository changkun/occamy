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
  gin-jwt leave `go.mod` once the v0 routes are gone.

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
