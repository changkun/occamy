# HTTP API v1

This document describes every route that `occamyd` serves for a v1
configuration file: API v1 under `/api/v1`, `/healthz`, the v0 routes
that `legacy_v0` keeps, and the debug listener. The keys that turn
routes on and set their limits are described in
[configuration.md](configuration.md); the stream that the two stream
routes carry after the upgrade is protocol v1, described in
[protocol.md](protocol.md). The design and its decisions are in
[specs/008-native-architecture/api-and-server-stack.md](../specs/008-native-architecture/api-and-server-stack.md).
Where this document and the code disagree, the code is right and this
document has a bug. `go test ./server` checks that every route has a
section here, and that every section here is a route (`TestAPIDoc`).

- [Overview](#overview)
- [Authentication](#authentication)
- [Errors](#errors)
- [Limits](#limits)
- [Health](#health)
- [Sign-in](#sign-in)
- [Connections and sessions](#connections-and-sessions)
- [Ad-hoc targets](#ad-hoc-targets)
- [Shares](#shares)
- [Streams](#streams)
- [v0 routes](#v0-routes)
- [The debug listener](#the-debug-listener)

## Overview

`occamyd` serves the public routes on `listen`, over TLS 1.2 or later
when `tls.cert` and `tls.key` are set, and over plain HTTP otherwise,
for a reverse proxy that terminates TLS in front of it. It speaks
HTTP/1.1 only, with or without TLS: the streams are WebSockets, which
`occamyd` upgrades from HTTP/1.1. A route that takes `GET` also answers
`HEAD`.

| Route | Caller | Served |
|-------|--------|--------|
| [`GET /healthz`](#get-healthz) | anyone | always |
| [`POST /api/v1/auth/token`](#post-apiv1authtoken) | anyone | always |
| [`GET /api/v1/auth/oidc/login`](#get-apiv1authoidclogin) | a browser | with `auth.oidc` |
| [`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback) | the provider, through the browser | with `auth.oidc` |
| [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken) | anyone | with `auth.oidc` |
| [`GET /api/v1/connections`](#get-apiv1connections) | an access token | always |
| [`POST /api/v1/sessions`](#post-apiv1sessions) | an access token | always |
| [`GET /api/v1/sessions/{id}`](#get-apiv1sessionsid) | the owner's access token | always |
| [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid) | the owner's access token | always |
| [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | the owner's access token | always |
| [`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream) | an access token | always |
| [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) | an access token, a session token or a share | always |
| [`POST /api/v0/login`](#post-apiv0login) | anyone | with `legacy_v0` |
| [`GET /api/v0/connect`](#get-apiv0connect) | a v0 token | with `legacy_v0` |
| [`GET /debug/vars`](#get-debugvars) and `/debug/pprof/` | anyone who reaches the debug listener | with `debug.address`, on that listener only |

API v1's conventions:

- **JSON.** Every response with a body is one compact JSON value, with
  no line break at its end, as `Content-Type: application/json;
  charset=utf-8` and `X-Content-Type-Options: nosniff`. A request body
  must be `Content-Type: application/json` (parameters such as
  `charset` are ignored), hold exactly one JSON value, and name no field
  that the route does not take; otherwise the route answers `415
  unsupported_media_type` or `400 bad_request`. Requiring the JSON type
  also keeps a cross-site HTML form from posting to the API, since a
  form cannot send it without a CORS preflight.
- **No caching of secrets.** Successful responses that carry a token or
  a session's state have `Cache-Control: no-store`: those of both token
  endpoints, `GET /api/v1/connections`, `POST /api/v1/sessions`,
  `GET /api/v1/sessions/{id}` and `POST /api/v1/sessions/{id}/shares`.
  So do `/healthz`, every redirect of the OIDC browser routes, which also
  has `Referrer-Policy: no-referrer`, and the callback's
  `invalid_state`.
- **Tokens never in a URL.** Routes read a token from one
  `Authorization: Bearer` header, and the stream routes from a WebSocket
  subprotocol too; never from the query string or a cookie, which
  proxies, logs and browser histories keep. The request log names each
  request's method, path, status, duration and peer address, never its
  query string, headers or body.
- **No CORS.** `occamyd` answers no preflight, so a web page that calls
  the JSON routes must be served from `occamyd`'s origin, such as
  through the reverse proxy in front of both. Pages of the origins in
  `origins` may open streams.
- **Unknown routes.** A path at or under `/api/v1` that no route takes
  answers `404 not_found`, and a route asked with a method it does not
  take answers `405 method_not_allowed` with `Allow` listing the methods
  it takes, both in API v1's [error shape](#errors). Elsewhere, such as
  `/healthz` or `/api/v0/`, Go's `net/http` answers in plain text.

## Authentication

### Tokens

`occamyd` takes three kinds of token. Access tokens and session tokens
are JWTs signed with Ed25519 (`alg: EdDSA`), by `occamyd`'s signing key
(`iss: occamy`) or by a trusted issuer's; every one names `aud: occamy`,
carries `iat` and `exp` at most 12 hours apart, names a user in `sub`,
and says its kind in `kind`, so that one kind is never taken for the
other. Shares are not JWTs. The claims a trusted issuer must set are in
[configuration.md](configuration.md#tokens-from-a-trusted-issuer).

| Token | Comes from | Lives | Grants |
|-------|-----------|-------|--------|
| Access token, `kind: access` | [`POST /api/v1/auth/token`](#post-apiv1authtoken), [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken), or a trusted issuer | `auth.access_token_lifetime`, 15 minutes by default; a trusted issuer's up to 12 hours | The JSON routes as its user, a connection's stream, and the stream of a session its user owns, as the owner |
| Session token, `kind: session` | The `stream_token` of [`POST /api/v1/sessions`](#post-apiv1sessions), or a trusted issuer | The stream token 300 seconds; a trusted issuer's up to 12 hours | The stream of the one session in its `sid`, with its `role`: `owner`, `viewer` or `read-only` |
| Share | [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | 60 to 43200 seconds, an hour by default, and never past its session's end | The stream of its session, as `viewer` or `read-only` |

Which route takes which:

| Route | Takes |
|-------|-------|
| `GET /healthz`, the sign-in routes | nothing |
| `GET /api/v1/connections`, `POST /api/v1/sessions`, `GET` and `DELETE /api/v1/sessions/{id}`, `POST /api/v1/sessions/{id}/shares` | an access token in `Authorization: Bearer` |
| `GET /api/v1/connections/{id}/stream` | an access token, in the `occamy.v1.token.` subprotocol or `Authorization` |
| `GET /api/v1/sessions/{id}/stream` | the owner's access token, a session token for the session, or a share of it, in the `occamy.v1.token.` subprotocol or `Authorization` |

A stream joins as one of three roles: the `owner`, who sends input and
is the only one asked for credentials (`prompt`); a `viewer`, who sends
input too; or a `read-only` viewer, whose input is dropped
([protocol.md](protocol.md#input-messages)).

### Users

A user is the issuer of a token and its `sub`, and for `occamyd`'s own
users also where they signed in (`idp`: `local` for a static user,
`oidc` for OIDC): the gateway's `carol` and `occamyd`'s `carol` are two
users, and neither owns the other's sessions. A static user's name is
the static user's, also when an OIDC sign-in or a trusted issuer that
`link_static_users` links to the static users names exactly that user.
Connections' `allow.users` and `allow.groups` match names whichever
issuer signed the token, and a trusted issuer's `groups` count only for
the groups in its `allowed_groups`.
[configuration.md](configuration.md#signing-in-through-oidc) gives the
rules in full.

### The `Authorization` header

The JSON routes that need a caller read exactly one `Authorization`
header with the `Bearer` scheme, in any case, and a token without
spaces:

```http
GET /api/v1/connections HTTP/1.1
Host: occamy.example
Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…
```

Every failure is `401` with an RFC 6750 challenge, and the response
never repeats the token:

| Request | Status | `code` | `WWW-Authenticate` |
|---------|--------|--------|--------------------|
| No `Authorization`, more than one, another scheme, or an empty token | 401 | `unauthorized` | `Bearer realm="occamy"` |
| An access token past its `exp` | 401 | `token_expired` | `Bearer realm="occamy", error="invalid_token"` |
| Anything else that fails: malformed, a bad signature, an unknown issuer, another audience, an algorithm other than EdDSA, no `iat`, a lifetime over 12 hours, a session token, a `sub` that is no usable name, or a `sub` that names a static user from a source not linked to the static users | 401 | `invalid_token` | `Bearer realm="occamy", error="invalid_token"` |

The routes check the token first, so a request with no valid token
gets `401` whatever its body holds. A token's expiry ends no stream
that has joined: only the session's end does. `occamyd` issues no
refresh tokens; a client signs in again when its access token expires.

## Errors

API v1 answers every error with one shape:

```json
{"error":{"code":"invalid_token","message":"the access token is not valid"}}
```

`code` is stable, and is what clients branch on; new codes may be added,
but a code never changes its meaning. `message` is for people, may
change, and never holds a secret.

| `code` | Status | When |
|--------|--------|------|
| `bad_request` | 400 | The body does not decode: not JSON, a second JSON value, a field the route does not take, a field of the wrong type. Or the request is refused for what it says, as each route lists. Or a stream request offers no `occamy.v1` subprotocol |
| `invalid_state` | 400 | `GET /api/v1/auth/oidc/callback` matches no sign-in this browser started in the last 10 minutes |
| `unauthorized` | 401 | No access token, or wrong credentials: a sign-in with a wrong password or an unknown user, or an OIDC code exchange that fails |
| `invalid_token` | 401 | A token that fails verification, or whose user `occamyd` refuses |
| `token_expired` | 401 | An access token past its expiry |
| `forbidden` | 403 | An ad-hoc target while ad-hoc targets are off, or one outside `adhoc_allowlist` |
| `not_found` | 404 | No such route, connection or session, or one the caller may not see ([absent rather than forbidden](#connections-and-sessions)) |
| `method_not_allowed` | 405 | The route does not take the method; `Allow` lists those it takes |
| `too_large` | 413 | A JSON body over 64 KiB |
| `unsupported_media_type` | 415 | A body that is not `application/json` |
| `limit_exceeded` | 429 | The caller holds as much as `limits` allows: `sessions_per_user` sessions, or `shares_per_session` shares of the session |
| `limit_exceeded` | 503 | `occamyd` runs `limits.sessions` sessions, whoever owns them; try again later |
| `internal` | 500 | `occamyd` failed on its own, such as a password file it cannot read; the cause goes to `occamyd`'s log, not to the client |
| `access_denied` | 302, in the return URL's fragment | OIDC sign-in: the provider or `occamyd` refused the user |
| `sign_in_failed` | 302, in the return URL's fragment | OIDC sign-in: the provider failed, or its answer did not verify |
| `unavailable` | 302, in the return URL's fragment | OIDC sign-in: the provider cannot be reached, or too many sign-ins wait |

The last three reach the client as `#error=…` in the fragment of its
return URL, not in a JSON body; they are described under
[`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback).

Some failures are not API v1's to shape: headers over the limit get
`net/http`'s plain-text `431`; a stream request that offers `occamy.v1`
but is no valid WebSocket upgrade gets the WebSocket library's
plain-text `426` or `400`; a stream refused after the upgrade gets a
protocol `close` (see [Streams](#streams)); and the v0 routes keep v0's
bodies.

## Limits

| What | Limit | Beyond it |
|------|-------|-----------|
| Request headers | 64 KiB | `431`, plain text |
| A JSON request body | 64 KiB | `413 too_large` |
| Any request body | 1 MiB | The handler's read fails, and the connection is closed after the response |
| Time to send the request headers | 10 seconds | The connection is closed |
| Time to read a whole request, and to write a response | 30 seconds each | The connection is closed. Streams are exempt: the upgrade clears both deadlines |
| An idle keep-alive connection | 2 minutes | The connection is closed |
| A stream's `hello` | 2048 bytes, so at most 2731 characters of base64url | `close` 1009 `too-large` |
| A stream's messages, until the route has let the client in | 64 KiB | WebSocket close 1009 |

Lifetimes:

| What | Lifetime |
|------|----------|
| An access token from `occamyd` | `auth.access_token_lifetime`: 15 minutes by default, between 1 minute and 12 hours |
| A trusted issuer's token | Up to 12 hours from its `iat`; 30 seconds of clock skew are allowed |
| The stream token of `POST /api/v1/sessions` | 300 seconds |
| A share | `expires_in`: 60 to 43200 seconds (12 hours), 3600 by default; it dies with its session |
| An OIDC sign-in, from `login` to the callback | 10 minutes |
| An OIDC one-time code, from the callback to the exchange | 1 minute, and one try |
| The owner grace: how long a session waits for its owner's stream, after `POST /api/v1/sessions` or after the owner leaves | 60 seconds, then the session ends with `owner-left` (4003) |
| A session without input | 30 minutes, then the session ends with `idle` (4004); no key sets it |

What one user, one session or `occamyd` may hold, set in
[`limits`](configuration.md#limits-limits):

| Key | Default | Over it |
|-----|---------|---------|
| `limits.sessions` | 1000 | `POST /api/v1/sessions` answers `503 limit_exceeded`; a connection's stream gets `close` 4001 `forbidden` |
| `limits.sessions_per_user` | 10 | `POST /api/v1/sessions` answers `429 limit_exceeded`; a connection's stream gets `close` 4001 `forbidden` |
| `limits.viewers_per_session` | 50, besides the owner, who can always join | A session's stream gets `close` 4007 `viewer-limit` |
| `limits.shares_per_session` | 100 that have not expired | `POST /api/v1/sessions/{id}/shares` answers `429 limit_exceeded` |

A session counts toward its owner's and `occamyd`'s from the moment it
is opened until it ends, so that concurrent requests cannot all pass a
limit. Neither sign-in nor opening sessions is rate limited.

## Health

### `GET /healthz`

The liveness check: `occamyd` answers. It takes no token and is always
served.

Response: `200`, with `Cache-Control: no-store`.

```json
{"status":"ok"}
```

## Sign-in

Sign-in turns a user's credentials into an access token. Static users
sign in with a password at `POST /api/v1/auth/token`; with `auth.oidc`,
users sign in at an OpenID Connect provider through the three OIDC
routes. Both answer with the same token response, in the shape of
OAuth 2.0's (RFC 6749, section 5.1). A trusted issuer's access tokens
need no sign-in.

### `POST /api/v1/auth/token`

Signs a static user in. It takes no token and is always served; without
static users, every sign-in fails with `401`.

Request:

```json
{"username":"alice","password":"correct horse battery staple"}
```

Response: `200`, with `Cache-Control: no-store`. `expires_in` is the
token's lifetime in seconds, `auth.access_token_lifetime`.

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

The access token names the user in `sub`, the user's groups in
`groups`, and `idp: local`.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `username` or `password` is missing or empty, or the body does not decode |
| 401 | `unauthorized` | A wrong password or an unknown user, alike, with `WWW-Authenticate: Bearer realm="occamy"`. An unknown user's password is compared with a hash at the users' highest bcrypt cost, so an unknown user takes as long as a user at that cost. The log names the peer address, not the user name, which people type passwords into |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 500 | `internal` | The token could not be signed |

### `GET /api/v1/auth/oidc/login`

Starts an OIDC sign-in: a browser navigates here, and is sent on to the
provider. Served only with `auth.oidc`; without it, `404 not_found`. It
takes no token. The client's side of the four steps is in
[configuration.md](configuration.md#signing-in-through-oidc).

Query:

| Parameter | Meaning |
|-----------|---------|
| `return_url` | Where the callback sends the browser back: one of `auth.oidc.return_urls`, exactly as written there. It may be left out when only one is configured |
| `code_challenge` | Required: the S256 challenge of a verifier that the client keeps, the SHA-256 of the verifier in base64url without padding, 43 characters (PKCE, RFC 7636) |
| `code_challenge_method` | Optional; `S256` if given |

For example, with RFC 7636's example challenge:

```text
GET /api/v1/auth/oidc/login?return_url=https%3A%2F%2Foccamy.example%2F&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM
```

Response: `302 Found` to the provider's authorization endpoint, with
`Cache-Control: no-store` and `Referrer-Policy: no-referrer`. The
redirect asks for the authorization code flow with `occamyd`'s own PKCE
challenge, a state and a nonce. It sets the sign-in cookie,
`__Secure-occamy_oidc` when it is `Secure` and `occamy_oidc` otherwise:
`HttpOnly`, `SameSite=Lax`, `Max-Age=600`, on the path of
`auth.oidc.redirect_url`, and `Secure` when `redirect_url` is https or
`occamyd` terminates TLS. The cookie holds the state, the nonce,
`occamyd`'s verifier, the return URL and the client's challenge, sealed
with AES-256-GCM under a key made at each start, so the browser can
neither read nor alter it. A second sign-in in the same browser replaces
the first.

If the provider cannot be discovered, the browser goes straight back to
the return URL with `#error=unavailable`, and no cookie is set.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `return_url` is not one of `auth.oidc.return_urls`, is given twice, or is left out while several are configured; or `code_challenge` is missing, given twice or not an S256 challenge; or `code_challenge_method` is given twice or is not `S256`. Nothing is set |
| 500 | `internal` | The cookie could not be sealed |

### `GET /api/v1/auth/oidc/callback`

The provider sends the browser here once the user has signed in, at
`auth.oidc.redirect_url`. Served only with `auth.oidc`; without it,
`404 not_found`. It takes no token; the sign-in cookie of `login` ties
the answer to the browser that started the sign-in.

Query: what the provider sends, `state` and `code`, or `error`, and
`iss` when the provider names itself (RFC 9207).

Response: once `state` matches the browser's sign-in cookie, `302
Found` to the sign-in's return URL, with `Cache-Control: no-store` and
`Referrer-Policy: no-referrer`, and the outcome in the fragment, which
the browser keeps to itself:

```text
Location: https://occamy.example/#code=JX2E7OZK6V3QWBTLDM4PNRYH5A
```

The one-time code works once, for a minute, and only with the verifier
of the challenge that `login` was given, at
[`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken). Or the
fragment holds `#error=` and one of:

| `error` | When |
|---------|------|
| `access_denied` | The provider refused, or `occamyd` refused the user: a `subject_claim` that is missing or not a usable name, an email the provider has not verified, a static user's name without `link_static_users`, or a user that `require_known_users` does not admit |
| `sign_in_failed` | The provider answered with another error, or its answer did not verify or could not be read: the `iss` parameter, the code exchange, the ID token's issuer, audience, authorized party, expiry, algorithm, signature or nonce, or a `groups_claim` that is neither a list of strings nor a string |
| `unavailable` | The provider cannot be reached, or 4096 one-time codes wait to be exchanged |

The callback clears the sign-in cookie when `state` matches it, or when
the browser holds no sign-in in progress. A user has at most three
codes waiting; a fourth sign-in drops the oldest.

| Status | `code` | When |
|--------|--------|------|
| 400 | `invalid_state` | No sign-in cookie of this browser matches `state`: the sign-in started in another browser, took longer than 10 minutes, outlived a restart of `occamyd`, or was replaced by a later `login`. Answered with `Cache-Control: no-store`, since no return URL can be trusted yet |

### `POST /api/v1/auth/oidc/token`

Exchanges the one-time code from the callback, with the verifier of the
challenge given to `login`, for an access token. Served only with
`auth.oidc`; without it, `404 not_found`. It takes no token.

Request, with RFC 7636's example verifier:

```json
{"code":"JX2E7OZK6V3QWBTLDM4PNRYH5A","code_verifier":"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}
```

Response: `200` with `Cache-Control: no-store`, as from
[`POST /api/v1/auth/token`](#post-apiv1authtoken):

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

The access token names the user from `subject_claim`, the groups from
`groups_claim` that the configuration names, and `idp: oidc`.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `code` or `code_verifier` is missing or empty, or the body does not decode |
| 401 | `unauthorized` | The code is unknown, used or expired, or the verifier is not 43 to 128 characters of PKCE's alphabet or does not match the challenge, alike, with `WWW-Authenticate: Bearer realm="occamy"`. The code is spent on its first try, right or wrong |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 500 | `internal` | The token could not be signed |

## Connections and sessions

A connection is an entry of the registry in the configuration file, and
a session is a running connection to a remote, which its owner opened
and others may join. Only the owner reads, closes and shares a session;
there is no administrator role.

These routes treat what the caller may not see as absent rather than
forbidden: a connection whose `allow` names neither the caller nor one
of the caller's groups answers `404 not_found`, as an unknown one does,
and so does another user's session on `GET`, `DELETE` and shares, as an
ended one does, so that IDs cannot be probed. The stream routes answer
both with `close` 4001 `forbidden`.

Every route here takes an access token in `Authorization: Bearer`, and
answers the [`401`s](#the-authorization-header) above without one.

### `GET /api/v1/connections`

The connections the caller may open, in the order of the configuration
file.

Response: `200`, with `Cache-Control: no-store`. Each connection has its
`id`, its `name`, which is the ID unless the file gives one, and its
`protocol`: `ssh`, `vnc`, `rdp` or `telnet`. Its target, credentials and
backend are left out. `adhoc_targets` tells whether
[`POST /api/v1/sessions`](#post-apiv1sessions) takes ad-hoc targets. A
caller who may open nothing gets `"connections":[]`.

```json
{"connections":[{"id":"build-box","name":"Build box","protocol":"ssh"},{"id":"nas-desktop","name":"nas-desktop","protocol":"vnc"}],"adhoc_targets":false}
```

### `POST /api/v1/sessions`

Opens a session, owned by the caller, on a connection or, with
`allow_adhoc_targets`, on an [ad-hoc target](#ad-hoc-targets). A client
that wants the first frame at once opens
[`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream)
instead, which opens and streams in one request; this route is for
clients that want the session's ID before its stream.

Request, for a connection:

```json
{"connection":"build-box"}
```

Or for an ad-hoc target, with the fields of [Ad-hoc
targets](#ad-hoc-targets):

```json
{"protocol":"ssh","target":"10.0.0.5","username":"deploy","host_keys":["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}
```

Response: `201 Created`, with `Location: /api/v1/sessions/{id}` and
`Cache-Control: no-store`. `id` is the session's ID, 22 characters of
base64url. `role` is always `owner`. `stream_token` is a session token
that joins the session's stream as its owner, valid for `expires_in`
seconds, 300.

```json
{"id":"mZ3k1tYb0Q2fJw8aXr5LsA","role":"owner","stream_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","expires_in":300}
```

The session starts at once, and its owner has the owner grace, 60
seconds, to open
[`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) with
the stream token or the owner's access token; otherwise the session
ends with `owner-left`. The remote is dialled after this answer, so a
target that cannot be reached ends the session, and its stream gets
`close` 4006 `backend-failed`. Credentials that the connection prompts
for, and every ad-hoc target's, are asked of the owner on that stream,
as `prompt` messages ([protocol.md](protocol.md#prompt)). The owner's
`hello` sizes the session when the stream joins.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | Both a connection and ad-hoc fields, or neither; or an ad-hoc target that is not valid (see [Ad-hoc targets](#ad-hoc-targets)); or the body does not decode, such as one with a `password` field |
| 403 | `forbidden` | Ad-hoc fields while `allow_adhoc_targets` is off, or an ad-hoc target outside `adhoc_allowlist` |
| 404 | `not_found` | No connection of that ID that the caller may open |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 429 | `limit_exceeded` | The caller owns `limits.sessions_per_user` sessions; close one first |
| 503 | `limit_exceeded` | `occamyd` runs `limits.sessions` sessions |
| 500 | `internal` | The session could not be opened for a cause of `occamyd`'s own, such as a password or key file it cannot read; the log names the user, the target and the cause |

### `GET /api/v1/sessions/{id}`

The state of a running session, for its owner.

Response: `200`, with `Cache-Control: no-store`.

```json
{"id":"mZ3k1tYb0Q2fJw8aXr5LsA","connection":"build-box","protocol":"ssh","backend":"ssh","class":"terminal","size":{"width":120,"height":40},"viewers":[{"id":2,"role":"owner"},{"id":3,"role":"read-only"}],"opened":"2026-10-31T09:00:00Z"}
```

| Field | Meaning |
|-------|---------|
| `id` | The session's ID |
| `connection` | The connection's ID; absent for an ad-hoc target |
| `protocol` | `ssh`, `vnc`, `rdp` or `telnet` |
| `backend` | What serves the session: `ssh`, `occamyd`'s native SSH backend; `vnc`, its native VNC backend; or `guacd`, the guacd bridge. ssh is always native, and rdp and telnet always go through guacd. vnc goes through guacd unless the connection's `backend` key, or `adhoc_backends` for an ad-hoc target, says `native` ([configuration.md](configuration.md#backends)) |
| `class` | `terminal` (ssh) or `graphical` (the others, telnet through guacd included): whether the stream carries a terminal or tiles of pixels |
| `size` | `width` and `height`, in character cells for a terminal and in pixels for a graphical session; absent until the remote reports a size |
| `viewers` | The viewers connected now, the owner first if connected, then the others in the order they joined: each one's `id`, as in `viewer-joined`, and `role`, `owner`, `viewer` or `read-only`. Empty, `[]`, while nobody is connected |
| `opened` | When the session was opened, RFC 3339 in UTC, to the second |

| Status | `code` | When |
|--------|--------|------|
| 404 | `not_found` | No running session of that ID that the caller owns |

### `DELETE /api/v1/sessions/{id}`

Closes a session, for its owner. It answers once the session has ended,
and every stream of the session gets `close` 4002 `session-closed`,
whatever tokens and shares are still valid.

Response: `204 No Content`, with no body.

| Status | `code` | When |
|--------|--------|------|
| 404 | `not_found` | No running session of that ID that the caller owns |

### `POST /api/v1/sessions/{id}/shares`

Mints a [share](#shares) of a session, for its owner: a token that
joins the session's stream as a viewer or a read-only viewer.

Request: `role`, `viewer` or `read-only`, and optionally `expires_in`,
the share's lifetime in seconds, from 60 to 43200; absent or 0 is 3600.

```json
{"role":"read-only","expires_in":600}
```

Response: `201 Created`, with `Cache-Control: no-store`: the share in
`token`, 43 characters of base64url, with its `role` and `expires_in`.

```json
{"token":"q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty","role":"read-only","expires_in":600}
```

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `role` is not `viewer` or `read-only`, `expires_in` lies outside 60 to 43200, or the body does not decode |
| 404 | `not_found` | No running session of that ID that the caller owns. Checked before the body |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 429 | `limit_exceeded` | The session has `limits.shares_per_session` shares that have not expired |
| 500 | `internal` | The share could not be minted |

## Ad-hoc targets

With `allow_adhoc_targets: true`, any signed-in user may open a session
on a target that is not in the registry, through
[`POST /api/v1/sessions`](#post-apiv1sessions) only:
`GET /api/v1/connections` then says `"adhoc_targets":true`. The keys
are described in [configuration.md](configuration.md#ad-hoc-targets).

| Field | Meaning |
|-------|---------|
| `protocol` | Required: `ssh`, `vnc`, `rdp` or `telnet` |
| `target` | Required: `host:port`, or a host alone for the protocol's default port (22, 5900, 3389, 23). An IPv6 address goes in brackets with a port, `[fd00::22]:22` |
| `username` | Optional: the user name to log in as. Without one, the owner is asked for it if the remote needs one |
| `host_keys` | ssh only: the host keys the target may present, as a connection's `host_keys` takes them: public keys in `authorized_keys` format or `SHA256:` fingerprints |
| `insecure_ignore_host_key` | ssh only: `true` accepts any host key, with a warning in the log. An ssh target needs this or `host_keys`, not both |

The request carries no secret, and the target takes none from the file:
its credentials are always asked of the owner on the session's stream,
and are not kept. The target is refused with `400 bad_request` for an
unknown protocol, a target that does not parse, an ssh target without
exactly one of `host_keys` and `insecure_ignore_host_key`, a host key
that does not parse, or host key fields on another protocol. It is
refused with `403 forbidden` when `adhoc_allowlist` does not allow it:
`occamyd` resolves a host name, within 5 seconds, checks every address
against the list, and dials the first address it checked; loopback,
link-local, unspecified and multicast addresses, the clouds' metadata
services, guacd's address and `occamyd`'s own listeners are refused
unless an entry is that very address.

ssh targets use the native SSH backend, and rdp and telnet targets go
through guacd. vnc targets go through guacd unless `adhoc_backends`
says `vnc: native`. A vnc target served by the native backend checks the
server's certificate, if the server offers VeNCrypt, against the
system's roots and the host name the request gave, or its address if
it gave an address. Through guacd, an ad-hoc target gets none of
`guacd.params` but the display's. A session on an ad-hoc target has no
`connection` in its state.

## Shares

A share lets someone without an account of their own watch a session,
or type into it.

- **Minting.** The owner calls
  [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares)
  with the role, `viewer` or `read-only`, and a lifetime. A share is
  never `owner`.
- **What it is.** 32 random bytes in base64url, 43 characters with no
  dots: not a JWT, and it carries no claims. `occamyd` keeps only its
  SHA-256 hash, in memory, so shares do not survive a restart, and a
  share minted by one `occamyd` opens nothing on another.
- **Joining.** The holder opens
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) of
  the same session, with the share as the token, in the
  `occamy.v1.token.` subprotocol, base64url-encoded as every token
  there is, or in `Authorization: Bearer`. A share opens only the
  session it was minted for, and joins as many times as the session's
  viewer limit allows, until it expires. A viewer joined by a share is
  named by no user; the session's state lists it by role.
- **Expiry.** An expired share is refused at the upgrade with `close`
  4000 `unauthorized`. Expiry ends no stream that has joined.
- **The session's end.** Shares die with their session, however it
  ends: closed by its owner, the owner not back within the grace, idle,
  ended by the remote, failed, or `occamyd` shut down. Every stream
  joined by a share then ends with the session's close code. A single
  share cannot be revoked; closing the session ends them all.

## Streams

A stream is a WebSocket that carries one session in protocol v1. The two
stream routes differ in what they open: a connection's stream opens a
new session and streams it, in one round trip; a session's stream joins
a running one. Both take the token, `Origin` and `hello` the same way,
and both answer a refusal after the upgrade with protocol v1's `close`,
because a browser cannot read the HTTP status of a refused upgrade.
After the upgrade, the stream is [protocol.md](protocol.md)'s: from
`welcome` and the first frame to `close`.

### The upgrade request

A stream request is a WebSocket upgrade, `GET` with `Upgrade:
websocket`, that offers the subprotocol `occamy.v1` in
`Sec-WebSocket-Protocol`, and carries:

- **The token**, in the subprotocol `occamy.v1.token.` followed by the
  token in base64url without padding, offered beside `occamy.v1`, which
  is how a browser sends it; or else in `Authorization: Bearer`, for a
  native client that offers `occamy.v1` alone. A token in the
  subprotocol wins over the header: a bad one is refused even beside a
  good header. A token in the query string is ignored.
- **The `hello`**, in the query parameter `hello`: the whole `hello`
  message in base64url without padding, at most 2048 bytes before
  encoding ([protocol.md](protocol.md#the-upgrade-request)).
- **`Origin`**, which a browser adds. A request without `Origin` comes
  from no browser and is taken on its token alone. A request with one
  is taken only if it has exactly one `Origin`, `http` or `https` with a
  host, and either its host is the request's `Host` and, when `occamyd`
  terminates TLS, its scheme is `https`, or `scheme://host[:port]` is in
  `origins`. `null`, an empty `Origin` and a repeated one are refused.
  Behind a proxy that rewrites `Host`, list `occamyd`'s own origin in
  `origins`.

```http
GET /api/v1/connections/build-box/stream?hello=AQEAAAEAgAc4BAAAAAABDgAAAG9jY2FteS13ZWIvMS4wBQABAAUAAAACBAMBBgIAAgAAAAAQAwABAAAAAQQAAgAAAMgABQACAAAAAAQ HTTP/1.1
Host: occamy.example
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Version: 13
Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==
Sec-WebSocket-Protocol: occamy.v1, occamy.v1.token.ZXlKaGJHY2lPaUpGWkVSVFFTSjkuZXlKemRXSWlPaUpoYkdsalpTSjkuYzJsbg
Origin: https://occamy.example
```

The response is `101 Switching Protocols` with `Sec-WebSocket-Protocol:
occamy.v1` alone, and no compression.

### Refusals

`occamyd` checks a stream request in this order: the subprotocol, then
`Origin`, the token's presence and the `hello`, then it upgrades, then
the route verifies the token and opens or joins the session. Only the
first two rows below answer in HTTP; the others answer after the
upgrade:

| Check | Answer |
|-------|--------|
| The request offers no `occamy.v1` | `400 bad_request`, as JSON, with no upgrade |
| The request is no valid WebSocket upgrade | The WebSocket library's `426` or `400`, plain text |
| `Origin` | `close` 4001 `forbidden`, "origin not allowed" |
| No token | `close` 4000 `unauthorized`, "a token is required" |
| More than one token subprotocol, an empty one, or one that is not base64url | `close` 4000 `unauthorized`, "invalid token subprotocol" |
| No `hello`, or one that does not decode | `close` 1002 `protocol-error`, "invalid hello" |
| A `hello` over 2048 bytes | `close` 1009 `too-large`, "invalid hello" |
| A `hello` of another protocol version | `close` 1003 `unsupported`, "unsupported protocol version" |
| A token that fails verification | `close` 4000 `unauthorized`: "token expired", "invalid token", or "the token's subject is a static user's name" |

Each route's own refusals follow in its section. A refused stream gets
the upgrade, then `close` with the code and a reason, then a WebSocket
close with the same code; a client that does not complete the closing
handshake within a second is dropped. Until the route has let a client
in, `occamyd` reads no message over 64 KiB. Codes and their meanings are
in [protocol.md](protocol.md#close-codes).

### `GET /api/v1/connections/{id}/stream`

Opens a session on connection `{id}`, owned by the caller, and streams
it to the caller as its owner, so that the first frame comes one round
trip after the upgrade. The owner's `hello` sizes the session: a
terminal's columns and rows, or a desktop's pixels where the backend
can ask the remote for them.

Token: an access token whose user the connection's `allow` names, by
name or group. A session token or a share is refused.

| Refusal | `close` |
|---------|---------|
| A session token, a share, or any token that is not a valid access token | 4000 `unauthorized` |
| No connection `{id}` that the caller may open | 4001 `forbidden`, "no such connection" |
| The caller owns `limits.sessions_per_user` sessions, or `occamyd` runs `limits.sessions` | 4001 `forbidden`, with the limit in the reason |
| The session could not be opened for a cause of `occamyd`'s own | 1011 `internal-error` |

Once the stream has joined, the session is as one that
`POST /api/v1/sessions` opened: its owner may leave and come back
within the owner grace through
[`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream), with
an access token, and may read, share and close it through the session
routes. The session's ID is in `welcome`.

### `GET /api/v1/sessions/{id}/stream`

Joins running session `{id}`.

Token, and the role it joins with:

| Token | Joins as |
|-------|----------|
| An access token of the session's owner | `owner` |
| A session token whose `sid` is `{id}`: the stream token, or a trusted issuer's | Its `role`; `owner` only if its user is the session's owner. A trusted issuer's session token, of any role, joins only a session that one of the issuer's own users owns, or, with the issuer's `link_static_users`, a static user |
| A share of `{id}` | The share's role, `viewer` or `read-only` |

A token without dots is taken for a share; any other for a JWT, an
access token or else a session token.

| Refusal | `close` |
|---------|---------|
| A share that is unknown, expired, of another session, or whose session has ended | 4000 `unauthorized`, "invalid token" |
| A JWT that fails verification | 4000 `unauthorized` |
| No running session `{id}`, a session token for another session or from an issuer that may not join it, or an access token or `owner` session token of anyone but the owner | 4001 `forbidden`, "no such session" |
| The owner's older stream did not leave within 5 seconds of a newer one | 4001 `forbidden`, "the owner is connected already" |
| A viewer beyond `limits.viewers_per_session` | 4007 `viewer-limit` |

A newer stream of the owner takes over from an older one, which gets
`close` 1000 `normal` with "the owner connected again"; a connection
that died without `occamyd` noticing would otherwise keep the owner out
of their own session. The owner's `hello` resizes the session; a
viewer's resizes nothing.

## v0 routes

With `legacy_v0: true`, a v1 file also serves the v0 routes that the
Guacamole-based web client uses, as a v0 file serves them, under
`/api/v0`. They are removed at v1.0. Their bodies and errors are v0's,
not API v1's, and neither the connection registry nor
`allow_adhoc_targets` and `adhoc_allowlist` restrict them: the login
signs any target for anyone, and `occamyd` warns at start. The web
client itself, `GET /static/`, is served only by a v0 file with
`client: true`, never by a v1 file. Protocol v0 is described in
[protocol-cn.md](protocol-cn.md), and v0 files in
[configuration.md](configuration.md#v0-files).

### `POST /api/v0/login`

Signs a token for a target. It takes no token.

Request, as JSON or as a URL-encoded or multipart form, up to 1 MiB:
`protocol`, `host` and `password` are required, `username` is optional.

```json
{"protocol":"vnc","host":"desktop.internal:5900","username":"admin","password":"secret"}
```

Response: `200`, `application/json; charset=utf-8`. The token is an
HS256 JWT signed with a key made at each start, so it does not survive
a restart; it lives an hour, and carries the target and its password in
readable claims.

```json
{"code":200,"expire":"2026-10-31T10:00:00Z","token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ…"}
```

A missing field or a body that does not parse answers `401` with
`WWW-Authenticate: JWT realm=occamy-proxy`:

```json
{"code":401,"message":"incorrect Username or Password"}
```

### `GET /api/v0/connect`

A WebSocket, with the subprotocol `guacamole`, that proxies one
Guacamole connection between the client and guacd. Every client whose
token carries the same target and credentials shares one session: the
first creates it, and the others join it.

The token comes from `Authorization: Bearer`, else the query parameter
`token`, else the cookie `jwt`. A missing or invalid token answers `401`
with `{"code":401,"message":"…"}` and `WWW-Authenticate: JWT
realm=occamy-proxy`, and a token without `exp` answers `400`. The query
parameters `width` and `height` (1 to 8192, 1024 by 768 by default),
`dpi` (48 to 600, 96 by default) and `timezone` (an IANA name) describe
the client's display. An `Origin`, if present, must name the request's
host; an empty or repeated one is refused with `403`. The WebSocket
closes with a Guacamole status code, in decimal, as its reason.

## The debug listener

With `debug.address` set, `occamyd` opens a second listener that serves
expvar and pprof, and nothing else; without it, these routes are served
nowhere, and the public listener never serves them. The debug listener
has no authentication, TLS, body limit or write timeout, since a CPU
profile takes as long as asked. Keep it on the loopback interface;
`occamyd` warns at start when it is not. Its requests are logged
as the public listener's are. How to read the profiles is in
[debugging.md](debugging.md#profiling-occamyd).

### `GET /debug/vars`

Go's expvar: a JSON object with `cmdline`, `occamyd`'s command line, and
`memstats`, the runtime's memory statistics. `occamyd` publishes no
variables of its own. Abbreviated:

```json
{"cmdline":["occamyd","-conf","/etc/occamy/conf.yaml"],"memstats":{"Alloc":4194304,"HeapInuse":6291456,"NumGC":12}}
```

### `GET /debug/pprof/`

pprof's index, as HTML, and under it the named profiles, such as
`/debug/pprof/goroutine`, `/debug/pprof/heap`, `/debug/pprof/allocs`
and `/debug/pprof/threadcreate`: as a profile for `go tool pprof`, or
as text with `?debug=1` or `?debug=2`. `/debug/pprof` without the
slash redirects here.

### `GET /debug/pprof/cmdline`

`occamyd`'s command line, as plain text, its arguments separated by NUL
bytes.

### `GET /debug/pprof/profile`

A CPU profile over `seconds` seconds, 30 by default, for
`go tool pprof`.

### `GET /debug/pprof/symbol`

The number of symbols the binary has, as plain text: `num_symbols: 1`
when it has any.

### `POST /debug/pprof/symbol`

Looks up the program counters in the body, hexadecimal and separated by
`+`, and answers each with its function's name, as `go tool pprof` asks
for them.

### `GET /debug/pprof/trace`

An execution trace over `seconds` seconds, 1 by default, for
`go tool trace`.
