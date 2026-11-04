# HTTP API v1

This page is the reference for writing a client of `occamyd`. A client
signs a user in, lists the connections the user may open, opens or
joins a session, and shares it. The JSON routes live under `/api/v1`.
A session itself streams over a WebSocket that carries
[protocol v1](protocol.md).

`occamyd` serves the API on `listen` (`:5636` by default). It speaks
HTTPS when `tls.cert` and `tls.key` are set, and plain HTTP otherwise,
for a reverse proxy that terminates TLS. It speaks HTTP/1.1 only,
because it upgrades the streams from HTTP/1.1.
[configuration.md](configuration.md) describes the keys that turn
routes on and set their limits. [openapi.yaml](openapi.yaml) describes
the same routes in OpenAPI 3.1, for tools that generate clients.

| Route | Token | Served |
|-------|-------|--------|
| [`GET /healthz`](#get-healthz) | none | always |
| [`GET /api/v1/auth/methods`](#get-apiv1authmethods) | none | always |
| [`POST /api/v1/auth/token`](#post-apiv1authtoken) | none | always |
| [`GET /api/v1/auth/oidc/login`](#get-apiv1authoidclogin) | none, from a browser | with `auth.oidc` |
| [`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback) | none, from the provider through the browser | with `auth.oidc` |
| [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken) | none | with `auth.oidc` |
| [`GET /api/v1/connections`](#get-apiv1connections) | access token | always |
| [`POST /api/v1/sessions`](#post-apiv1sessions) | access token | always |
| [`GET /api/v1/sessions/{id}`](#get-apiv1sessionsid) | the owner's access token | always |
| [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid) | the owner's access token | always |
| [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | the owner's access token | always |
| [`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream) | access token | always |
| [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) | access token, session token or share | always |
| [`GET /static/`](#get-static) | none, from a browser | with `web_client` |
| [`GET /debug/vars`](#get-debugvars) and `/debug/pprof/` | none | with `debug.address`, on that listener only |

## A typical client

```mermaid
sequenceDiagram
  participant C as Client
  participant O as occamyd
  participant V as Viewer
  C->>O: POST /api/v1/auth/token
  O-->>C: access token
  C->>O: GET /api/v1/connections
  O-->>C: the connections the user may open
  C->>O: GET /api/v1/connections/{id}/stream
  O-->>C: welcome with the session's ID, then frames
  C->>O: POST /api/v1/sessions/{id}/shares
  O-->>C: share
  C-->>V: the share, by any means
  V->>O: GET /api/v1/sessions/{id}/stream
  O-->>V: welcome, then frames
  C->>O: DELETE /api/v1/sessions/{id}
  O-->>V: close 4002 session-closed
```

1. **Sign in.** Post a user name and password, or sign in through
   [OIDC](#sign-in). Keep the access token:

    ```sh
    OCCAMY=https://occamy.example
    TOKEN=$(curl -s "$OCCAMY/api/v1/auth/token" \
      -H 'Content-Type: application/json' \
      -d '{"username":"alice","password":"correct horse battery staple"}' |
      jq -r .access_token)
    ```

2. **List the connections** the user may open:

    ```sh
    curl -s "$OCCAMY/api/v1/connections" -H "Authorization: Bearer $TOKEN"
    ```

3. **Open a stream.** Open a connection's stream, a WebSocket that opens
   a session and streams it in one request
   ([Streams](#streams)). Its first message, `welcome`, carries the
   session's ID. A client that wants the ID before the stream calls
   [`POST /api/v1/sessions`](#post-apiv1sessions) first.
4. **Share.** With the session's ID in `SESSION`, the owner mints a
   [share](#shares) and hands it to someone, who joins the session's
   stream with it:

    ```sh
    curl -s "$OCCAMY/api/v1/sessions/$SESSION/shares" \
      -H "Authorization: Bearer $TOKEN" \
      -H 'Content-Type: application/json' \
      -d '{"role":"read-only","expires_in":600}'
    ```

5. **End.** The owner closes the session with
   [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid), or
   leaves it to end on its own ([Limits](#limits)).

The access token lasts 15 minutes by default, and `occamyd` issues no
refresh tokens. When it expires, sign in again. A stream that has
joined keeps running.

## Conventions

- **JSON in and out.** A response with a body is one compact JSON value,
  with no line break at its end. It has `Content-Type:
  application/json; charset=utf-8` and `X-Content-Type-Options:
  nosniff`.
- **Strict request bodies.** A request body must have `Content-Type:
  application/json`, or the route answers `415 unsupported_media_type`.
  Parameters such as `charset` are ignored. A body over 64 KiB gets
  `413 too_large`.
- **Bodies that decode.** A body must hold exactly one JSON value. It may
  name only the fields the route takes, in their exact case, each once,
  at any depth, and each of the right type. Objects and arrays nest at
  most 32 deep. Any other body gets `400 bad_request`. So
  `{"Connection":"x"}` and `{"username":"a","username":"b"}` are
  refused. This keeps a proxy that reads the body from seeing another
  request than `occamyd` does.
- **Tokens never in a URL.** Routes read a token from the
  `Authorization` header, and the stream routes from a WebSocket
  subprotocol too. They never read one from the query string or a
  cookie, which proxies, logs and browser histories keep. The request
  log names each request's method, path, status, duration and peer
  address, never its query string, headers or body.
- **No caching.** A response that carries a token or a session's state
  has `Cache-Control: no-store`. Each route below says so where it
  applies.
- **No CORS.** `occamyd` answers no preflight. A web page that calls the
  JSON routes must come from `occamyd`'s origin, such as through the
  same reverse proxy. Pages of the origins in `origins` may open
  streams. Requiring the JSON type also keeps a cross-site HTML form
  from posting to the API.
- **Unknown routes.** A path at or under `/api/v1` that no route takes
  answers `404 not_found`. A route asked with a method it does not take
  answers `405 method_not_allowed`, with `Allow` listing the methods it
  takes. Both use the [error shape](#errors). Outside `/api/v1`, such as
  `/healthz` or `/static/`, the same cases answer in plain text.
- **`HEAD`.** A route that takes `GET` also answers `HEAD`.

## Authentication

### Tokens

`occamyd` takes three kinds of token.

| Token | What it is | Comes from | Lifetime |
|-------|------------|------------|----------|
| Access token | A JWT with `kind: access` | [`POST /api/v1/auth/token`](#post-apiv1authtoken), [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken), or a trusted issuer | `auth.access_token_lifetime`, 15 minutes by default. Up to 12 hours from a trusted issuer |
| Session token | A JWT with `kind: session`, for one session (`sid`) and one `role` | The `stream_token` of [`POST /api/v1/sessions`](#post-apiv1sessions), or a trusted issuer | 300 seconds. Up to 12 hours from a trusted issuer |
| Share | 43 random characters, not a JWT | [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | 60 to 43200 seconds, an hour by default, and never past its session's end |

What each one opens:

| Token | Opens |
|-------|-------|
| Access token | The JSON routes, as its user. A connection's stream. The stream of a session its user owns, as the owner |
| Session token | The stream of the session in its `sid`, with its `role`: `owner`, `viewer` or `read-only` |
| Share | The stream of its session, as `viewer` or `read-only` |

Access tokens and session tokens are JWTs signed with Ed25519
(`alg: EdDSA`). `occamyd` signs its own with its signing key
(`iss: occamy`), and a trusted issuer signs with its own key. Every
one names `aud: occamy` and a user in `sub`. Each carries `iat` and
`exp`, at most 12 hours apart, and its kind in `kind`, so that one kind
is never taken for the other. [configuration.md](configuration.md#tokens-from-a-trusted-issuer)
lists the claims a trusted issuer must set.

### Sending a token

| Route | Takes | Sent in |
|-------|-------|---------|
| `GET /healthz`, the sign-in routes | nothing | |
| `GET /api/v1/connections`, `POST /api/v1/sessions`, `GET` and `DELETE /api/v1/sessions/{id}`, `POST /api/v1/sessions/{id}/shares` | an access token | `Authorization: Bearer` |
| `GET /api/v1/connections/{id}/stream` | an access token | the `occamy.v1.token.` subprotocol, or `Authorization: Bearer` |
| `GET /api/v1/sessions/{id}/stream` | the owner's access token, a session token for the session, or a share of it | the `occamy.v1.token.` subprotocol, or `Authorization: Bearer` |

A JSON route reads exactly one `Authorization` header with the `Bearer`
scheme, in any case, and a token without spaces:

```http
GET /api/v1/connections HTTP/1.1
Host: occamy.example
Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…
```

[The upgrade request](#the-upgrade-request) shows how a stream sends
its token.

### Roles

A stream joins a session in one of three roles. The web client names
them by what they allow.

| Role | The web client says | May |
|------|---------------------|-----|
| `owner` | owner | Send input, and answer `prompt`s for credentials. Its `hello` sizes the session. Only the owner reads, shares and closes the session through the API |
| `viewer` | with control | Send [input](protocol.md#input-messages), as the owner does |
| `read-only` | view only | Watch. `occamyd` drops its input |

The owner is the user who opened the session. There is no
administrator role.

### Users

A user is a `sub` from one issuer. For `occamyd`'s own users, where they
signed in counts too: `idp: local` for a static user, `idp: oidc` for an
OIDC user. So `carol` from a trusted issuer and `occamyd`'s own
`carol` are two users, and neither owns the other's sessions.

A static user's name always means the static user. An OIDC sign-in, or
a trusted issuer, with `link_static_users` that names exactly a static
user acts as that static user.

A connection's `allow.users` and `allow.groups` match names whichever
issuer signed the token. A trusted issuer's `groups` count only for the
groups in its `allowed_groups`.
[configuration.md](configuration.md#signing-in-through-oidc) gives the
rules in full.

### When a token fails

A JSON route answers every failure with `401`, an RFC 6750 challenge in
`WWW-Authenticate`, and never repeats the token.

| Request | `code` | `WWW-Authenticate` |
|---------|--------|--------------------|
| No `Authorization` header, more than one, another scheme, or an empty token | `unauthorized` | `Bearer realm="occamy"` |
| A token past its `exp` and the [clock skew allowed](#limits) | `token_expired` | `Bearer realm="occamy", error="invalid_token"` |
| Any other token that fails (below) | `invalid_token` | `Bearer realm="occamy", error="invalid_token"` |

A token is `invalid_token` when it:

- is malformed, or its signature does not verify;
- comes from an unknown issuer, or names another audience;
- is signed with an algorithm other than EdDSA;
- has no `iat`, or lives longer than 12 hours;
- is a session token;
- names a `sub` that is not a usable user name;
- names a static user's name from a source not linked to the static
  users.

A route checks the token before the body, so a request without a valid
token gets `401` whatever its body holds. A token that expires ends no
stream that has joined. Only the session's end does.

## Errors

API v1 answers every error with one shape:

```json
{"error":{"code":"invalid_token","message":"the access token is not valid"}}
```

Clients branch on `code`. It is stable: new codes may be added, but a
code never changes its meaning. `message` is for people. It may change,
and it never holds a secret.

| `code` | Status | When |
|--------|--------|------|
| `bad_request` | 400 | The body does not [decode](#conventions), or the route refuses what it says. Also a stream request without the `occamy.v1` subprotocol |
| `invalid_state` | 400 | The OIDC callback matches no sign-in this browser started in the last 10 minutes |
| `unauthorized` | 401 | No access token, or wrong credentials at sign-in |
| `invalid_token` | 401 | A token that fails verification, or whose user `occamyd` refuses |
| `token_expired` | 401 | A token past its expiry and the clock skew allowed |
| `forbidden` | 403 | An ad-hoc target while ad-hoc targets are off, or one outside `adhoc_allowlist` |
| `not_found` | 404 | No such route, connection or session, or [one the caller may not see](#connections-and-sessions) |
| `method_not_allowed` | 405 | The route does not take the method. `Allow` lists those it takes |
| `too_large` | 413 | A JSON body over 64 KiB |
| `unsupported_media_type` | 415 | A body that is not `application/json` |
| `limit_exceeded` | 429 | The caller holds as much as `limits` allows: `sessions_per_user` sessions, or `shares_per_session` shares of the session |
| `limit_exceeded` | 503 | `occamyd` runs `limits.sessions` sessions, whoever owns them. Try again later |
| `internal` | 500 | `occamyd` failed on its own, such as on a password file it cannot read. The cause goes to its log, not to the client |
| `access_denied` | 302 | OIDC sign-in: the provider or `occamyd` refused the user |
| `sign_in_failed` | 302 | OIDC sign-in: the provider failed, or its answer did not verify |
| `unavailable` | 302 | OIDC sign-in: the provider cannot be reached, or too many sign-ins wait |

The last three reach the client as `#error=…` in the fragment of its
return URL, not in a JSON body
([`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback)).

Some failures do not use this shape:

- Request headers over 64 KiB get a plain-text `431`.
- A stream request that offers `occamy.v1` but is no valid WebSocket
  upgrade gets the WebSocket library's plain-text `426` or `400`.
- A stream refused after the upgrade gets a protocol v1 `close`
  ([Refusals](#refusals)).

## Limits

Requests:

| What | Limit | Beyond it |
|------|-------|-----------|
| Request headers | 64 KiB | A plain-text `431` |
| A JSON request body | 64 KiB | `413 too_large` |
| Any request body | 1 MiB | The read fails, and the connection closes after the response |
| Sending the request headers | 10 seconds | The connection closes |
| Reading a whole request, and writing a response | 30 seconds each | The connection closes. A stream is exempt once upgraded |
| An idle keep-alive connection | 2 minutes | The connection closes |
| A stream's `hello` | 2048 bytes, at most 2731 characters of base64url | `close` 1009 `too-large` |
| A stream's messages, until the route has let the client in | 64 KiB | WebSocket close 1009 |

Lifetimes:

| What | Lifetime |
|------|----------|
| An access token from `occamyd` | `auth.access_token_lifetime`: 15 minutes by default, from 1 minute to 12 hours |
| A trusted issuer's token | Up to 12 hours from its `iat` |
| The stream token of `POST /api/v1/sessions` | 300 seconds |
| A share | `expires_in`: 60 to 43200 seconds, 3600 by default. It ends with its session |
| An OIDC sign-in, from `login` to the callback | 10 minutes |
| An OIDC one-time code, from the callback to the exchange | 1 minute, and one try |
| The owner grace: how long a session waits for its owner's stream, after `POST /api/v1/sessions` or after the owner leaves | 60 seconds. Then the session ends with `owner-left` (4003) |
| A session without input | 30 minutes. Then the session ends with `idle` (4004). No key sets it |

`occamyd` allows 30 seconds of clock skew on every token of API v1:
its own access and stream tokens, and a trusted issuer's. So a token
still works for up to 30 seconds after its `exp`. A client should not
rely on that: renew a token once its `exp` has passed, since
`401 token_expired` follows within 30 seconds. The ID tokens that
`occamyd` receives from an OIDC provider get no such allowance on their
expiry.

What one user, one session or `occamyd` may hold, set in
[`limits`](configuration.md#limits-limits):

| Key | Default | Beyond it |
|-----|---------|-----------|
| `limits.sessions` | 1000 | `POST /api/v1/sessions` answers `503 limit_exceeded`. A connection's stream gets `close` 4001 `forbidden` |
| `limits.sessions_per_user` | 10 | `POST /api/v1/sessions` answers `429 limit_exceeded`. A connection's stream gets `close` 4001 `forbidden` |
| `limits.viewers_per_session` | 50, besides the owner, who can always join | A session's stream gets `close` 4007 `viewer-limit` |
| `limits.shares_per_session` | 100 that have not expired | `POST /api/v1/sessions/{id}/shares` answers `429 limit_exceeded` |

A session counts toward its owner's limit and `occamyd`'s from the
moment it opens until it ends, so concurrent requests cannot all pass a
limit. Neither sign-in nor opening sessions is rate limited.

## Health

### `GET /healthz`

Reports that `occamyd` is up.

**Request.** No token.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"status":"ok"}
```

## Sign-in

Sign-in turns a user's credentials into an access token. Static users
sign in with a password at
[`POST /api/v1/auth/token`](#post-apiv1authtoken). With `auth.oidc`,
users sign in at an OpenID Connect provider instead:

1. The client makes a PKCE verifier (RFC 7636) and keeps it.
2. It sends the browser to
   [`GET /api/v1/auth/oidc/login`](#get-apiv1authoidclogin) with the
   verifier's S256 challenge and a return URL.
3. The user signs in at the provider. The browser comes back to the
   return URL with `#code=…`, or `#error=…`, in the fragment.
4. The client posts the code and its verifier to
   [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken), and gets
   an access token.

Both ways answer with the same token response, in the shape of OAuth
2.0's (RFC 6749, section 5.1). A trusted issuer's access tokens need no
sign-in. [configuration.md](configuration.md#signing-in-through-oidc)
describes how to set OIDC up.

### `GET /api/v1/auth/methods`

Says how a user can sign in, for a sign-in page to show.

**Request.** No token. It asks nothing of the OIDC provider.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"oidc":true}
```

| Field | Meaning |
|-------|---------|
| `oidc` | Whether `occamyd` has `auth.oidc`, and so serves the three OIDC routes |

Password sign-in has no field, since its route is always served.

### `POST /api/v1/auth/token`

Signs a static user in with a password.

**Request.** No token. The body:

```json
{"username":"alice","password":"correct horse battery staple"}
```

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

`expires_in` is the token's lifetime in seconds,
`auth.access_token_lifetime`. The access token names the user in `sub`,
the user's groups in `groups`, and `idp: local`.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `username` or `password` is missing or empty, or the body does not decode |
| 401 | `unauthorized` | A wrong password or an unknown user, alike. With `WWW-Authenticate: Bearer realm="occamy"` |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 500 | `internal` | The token could not be signed |

**Notes.** Without static users, every sign-in fails with `401`. An
unknown user takes as long as a known one: `occamyd` compares the
password with a hash at the users' highest bcrypt cost. The log names
the peer address, never the user name, since people type passwords into
it.

### `GET /api/v1/auth/oidc/login`

Starts an OIDC sign-in. A browser navigates here and is sent on to the
provider.

**Request.** No token. Served only with `auth.oidc`. Without it, the
route answers `404 not_found`. The query:

| Parameter | Meaning |
|-----------|---------|
| `return_url` | Where the browser comes back to: one of `auth.oidc.return_urls`, exactly as written there. It may be left out when only one is configured |
| `code_challenge` | Required. The S256 challenge of the client's verifier: the SHA-256 of the verifier in base64url without padding, 43 characters |
| `code_challenge_method` | Optional. `S256` if given |

For example, with RFC 7636's example challenge:

```text
GET /api/v1/auth/oidc/login?return_url=https%3A%2F%2Foccamy.example%2F&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256
```

**Response.** `302 Found` to the provider's authorization endpoint,
with `Cache-Control: no-store` and `Referrer-Policy: no-referrer`. The
redirect asks for the authorization code flow, with `occamyd`'s own
PKCE challenge, a state and a nonce.

The response also sets the sign-in cookie, which ties the callback to
this browser:

- It is named `__Secure-occamy_oidc` when it is `Secure`, and
  `occamy_oidc` otherwise.
- It is `HttpOnly`, `SameSite=Lax` and `Max-Age=600`, on the path of
  `auth.oidc.redirect_url`.
- It is `Secure` when `redirect_url` is https or `occamyd` terminates
  TLS.
- It holds the state, the nonce, `occamyd`'s verifier, the return URL
  and the client's challenge. They are sealed with AES-256-GCM under a
  key that `occamyd` makes when it starts, so the browser can neither
  read nor alter them.

A second sign-in in the same browser replaces the first. If the
provider cannot be discovered, the browser goes straight back to the
return URL with `#error=unavailable`, and no cookie is set.

**Errors.** Each comes with `Cache-Control: no-store`, and sets nothing.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | The query is not valid (below) |
| 500 | `internal` | The cookie could not be sealed |

The query is not valid when:

- `return_url` is not one of `auth.oidc.return_urls`, is given twice,
  or is left out while several are configured;
- `code_challenge` is missing, given twice, or not an S256 challenge;
- `code_challenge_method` is given twice, or is not `S256`.

### `GET /api/v1/auth/oidc/callback`

Where the provider sends the browser back once the user has signed in.
Its URL is `auth.oidc.redirect_url`.

**Request.** No token: the sign-in cookie from `login` ties the request
to the browser that started the sign-in. Served only with
`auth.oidc`. Without it, the route answers `404 not_found`. The query
is what the provider sends: `state` and `code`, or `error`, and `iss`
when the provider names itself (RFC 9207).

**Response.** Once `state` matches the browser's sign-in cookie,
`302 Found` to the sign-in's return URL, with `Cache-Control: no-store`
and `Referrer-Policy: no-referrer`. The outcome is in the fragment,
which the browser keeps to itself:

```text
Location: https://occamy.example/#code=JX2E7OZK6V3QWBTLDM4PNRYH5A
```

The one-time code works once, for a minute, and only with the verifier
of the challenge given to `login`, at
[`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken). A user has
at most three codes waiting. A fourth sign-in drops the oldest.

If the sign-in failed, the fragment holds `#error=` and one of these:

| `error` | When |
|---------|------|
| `access_denied` | The provider or `occamyd` refused the user |
| `sign_in_failed` | The provider failed, or its answer did not verify |
| `unavailable` | The provider cannot be reached, or 4096 one-time codes wait to be exchanged |

`occamyd` refuses the user with `access_denied` when:

- the provider answered `error=access_denied`;
- the `subject_claim` is missing or not a usable user name;
- the `subject_claim` is `email`, and the provider has not verified the
  address;
- the name is a static user's, and `link_static_users` is off;
- `require_known_users` does not admit the user.

The sign-in fails with `sign_in_failed` when:

- the provider answered with another error, or with no code;
- `iss` names another issuer, or is missing from a provider that says
  it sends one;
- the code exchange failed, or brought no ID token;
- the ID token's issuer, audience, authorized party, expiry, algorithm,
  signature or nonce does not verify;
- the `groups_claim` is neither a list of strings nor a string;
- `occamyd` cannot read its client secret.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `invalid_state` | No sign-in cookie of this browser matches `state` (below). With `Cache-Control: no-store`, since no return URL can be trusted yet |

`state` matches no cookie when the sign-in started in another browser,
took longer than 10 minutes, outlived a restart of `occamyd`, or was
replaced by a later `login`.

**Notes.** The callback clears the sign-in cookie when `state` matches
it, or when the browser holds no sign-in in progress.

### `POST /api/v1/auth/oidc/token`

Exchanges the one-time code from the callback, with the verifier of
the challenge given to `login`, for an access token.

**Request.** No token. Served only with `auth.oidc`. Without it, the
route answers `404 not_found`. The body, with RFC 7636's example
verifier:

```json
{"code":"JX2E7OZK6V3QWBTLDM4PNRYH5A","code_verifier":"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}
```

**Response.** `200`, with `Cache-Control: no-store`, as from
[`POST /api/v1/auth/token`](#post-apiv1authtoken):

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

The access token names the user from `subject_claim`, the groups from
`groups_claim` that the configuration names, and `idp: oidc`.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `code` or `code_verifier` is missing or empty, or the body does not decode |
| 401 | `unauthorized` | The code or the verifier is wrong (below). With `WWW-Authenticate: Bearer realm="occamy"` |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 500 | `internal` | The token could not be signed |

The route answers `401` alike when the code is unknown, used or
expired, or when the verifier is not 43 to 128 characters of PKCE's
alphabet or does not match the challenge. The code is spent on its
first try, right or wrong.

## Connections and sessions

A connection is an entry of the configuration file's registry. A
session is a running connection to a remote, which its owner opened and
others may join. Only the owner reads, shares and closes a session.

These routes treat what the caller may not see as absent, so that IDs
cannot be probed:

- A connection whose `allow` names neither the caller nor one of the
  caller's groups answers `404 not_found`, as an unknown one does.
- Another user's session answers `404 not_found` on `GET`, `DELETE` and
  shares, as an ended one does.

The stream routes answer both with `close` 4001 `forbidden`.

Every route here takes an access token in `Authorization: Bearer`, and
answers `401` without a valid one ([When a token fails](#when-a-token-fails)).

### `GET /api/v1/connections`

Lists the connections the caller may open, in the order of the
configuration file.

**Request.** An access token.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"connections":[{"id":"build-box","name":"Build box","protocol":"ssh"},{"id":"nas-desktop","name":"nas-desktop","protocol":"vnc"}],"adhoc_targets":false}
```

| Field | Meaning |
|-------|---------|
| `connections` | The connections the caller may open, `[]` if none |
| `connections[].id` | The connection's ID |
| `connections[].name` | Its name, which is the ID unless the file gives one |
| `connections[].protocol` | `ssh`, `vnc`, `rdp` or `kubernetes` |
| `adhoc_targets` | Whether [`POST /api/v1/sessions`](#post-apiv1sessions) takes [ad-hoc targets](#ad-hoc-targets) |

A connection's target, credentials and pod are left out.

**Errors.** Only the `401`s of a token that fails.

### `POST /api/v1/sessions`

Opens a session owned by the caller, on a connection or on an
[ad-hoc target](#ad-hoc-targets). A client that wants the first frame
at once opens
[`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream)
instead. This route is for a client that wants the session's ID before
its stream.

**Request.** An access token. The body names a connection:

```json
{"connection":"build-box"}
```

Or, with `allow_adhoc_targets`, an ad-hoc target, with the fields of
[Ad-hoc targets](#ad-hoc-targets):

```json
{"protocol":"ssh","target":"10.0.0.5","username":"deploy","host_keys":["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]}
```

**Response.** `201 Created`, with `Location: /api/v1/sessions/{id}`
and `Cache-Control: no-store`:

```json
{"id":"mZ3k1tYb0Q2fJw8aXr5LsA","role":"owner","stream_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","expires_in":300}
```

| Field | Meaning |
|-------|---------|
| `id` | The session's ID, 22 characters of base64url |
| `role` | Always `owner` |
| `stream_token` | A session token that joins the session's stream as its owner |
| `expires_in` | The stream token's lifetime in seconds, 300 |

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | Both a connection and ad-hoc fields, or neither. An ad-hoc target that is not valid ([Ad-hoc targets](#ad-hoc-targets)). A body that does not decode, such as one with a `password` field |
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 403 | `forbidden` | Ad-hoc fields while `allow_adhoc_targets` is off, or an ad-hoc target outside `adhoc_allowlist` |
| 404 | `not_found` | No connection of that ID that the caller may open |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 429 | `limit_exceeded` | The caller owns `limits.sessions_per_user` sessions. Close one first |
| 503 | `limit_exceeded` | `occamyd` runs `limits.sessions` sessions |
| 500 | `internal` | `occamyd` failed for a cause of its own, such as a password or key file it cannot read. The log names the user, the target and the cause |

**Notes.**

- The session starts at once. Its owner has the owner grace, 60
  seconds, to open
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream)
  with the stream token or the owner's access token. Otherwise the
  session ends with `owner-left`.
- `occamyd` dials the remote after this answer. A target that cannot be
  reached ends the session, and its stream gets `close` 4006
  `backend-failed`, even when the session ended before the stream came.
- `occamyd` asks the owner on that stream, as `prompt` messages
  ([protocol.md](protocol.md#prompt)), for the credentials the
  connection prompts for, and for all of an ad-hoc target's.
- The owner's `hello` sizes the session when the stream joins.

### `GET /api/v1/sessions/{id}`

Returns the state of a running session, for its owner.

**Request.** The owner's access token.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"id":"mZ3k1tYb0Q2fJw8aXr5LsA","connection":"build-box","protocol":"ssh","backend":"ssh","class":"terminal","size":{"width":120,"height":40},"viewers":[{"id":2,"role":"owner"},{"id":3,"role":"read-only"}],"opened":"2026-10-31T09:00:00Z"}
```

| Field | Meaning |
|-------|---------|
| `id` | The session's ID |
| `connection` | The connection's ID. Absent for an ad-hoc target |
| `protocol` | `ssh`, `vnc`, `rdp` or `kubernetes` |
| `backend` | The `occamyd` backend that serves the session. Each protocol has its own, of the same name |
| `class` | `terminal` for ssh and kubernetes, `graphical` for vnc and rdp: whether the stream carries a terminal or tiles of pixels |
| `size` | `width` and `height`: character cells for a terminal, pixels for a graphical session. Absent until the remote reports a size |
| `viewers` | The viewers connected now: the owner first if connected, then the others in the order they joined. `[]` while nobody is connected |
| `viewers[].id` | The viewer's ID, as in `viewer-joined` |
| `viewers[].role` | `owner`, `viewer` or `read-only` |
| `opened` | When the session was opened, RFC 3339 in UTC, to the second |

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 404 | `not_found` | No running session of that ID that the caller owns |

### `DELETE /api/v1/sessions/{id}`

Closes a session, for its owner.

**Request.** The owner's access token. No body.

**Response.** `204 No Content`, once the session has ended and
`occamyd` has let it go. Every stream of the session gets `close` 4002
`session-closed`, whatever tokens and shares are still valid.

After the answer, a `GET`, `DELETE` or share of the session gets `404`.
The session no longer counts toward `limits.sessions_per_user` or
`limits.sessions`, so its owner may open another at once.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 404 | `not_found` | No running session of that ID that the caller owns |

### `POST /api/v1/sessions/{id}/shares`

Mints a [share](#shares) of a session, for its owner: a token that
joins the session's stream as a viewer or a read-only viewer.

**Request.** The owner's access token. The body:

```json
{"role":"read-only","expires_in":600}
```

| Field | Meaning |
|-------|---------|
| `role` | Required. `viewer` or `read-only` |
| `expires_in` | Optional. The share's lifetime in seconds, from 60 to 43200. Absent or 0 is 3600 |

**Response.** `201 Created`, with `Cache-Control: no-store`:

```json
{"token":"q8Gf2Lw0pX3nVb7Rk1eYtZcJ5uHs9MaDoQ4iWfNl6Ty","role":"read-only","expires_in":600}
```

| Field | Meaning |
|-------|---------|
| `token` | The share, 43 characters of base64url |
| `role` | The role it joins with |
| `expires_in` | Its lifetime in seconds |

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `role` is not `viewer` or `read-only`, `expires_in` lies outside 60 to 43200, or the body does not decode |
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 404 | `not_found` | No running session of that ID that the caller owns. Checked before the body |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that is not `application/json` |
| 429 | `limit_exceeded` | The session has `limits.shares_per_session` shares that have not expired |
| 500 | `internal` | The share could not be minted |

## Ad-hoc targets

With `allow_adhoc_targets: true`, any signed-in user may open a session
on a target that is not in the registry. Only
[`POST /api/v1/sessions`](#post-apiv1sessions) takes one, and
`GET /api/v1/connections` then says `"adhoc_targets":true`.
[configuration.md](configuration.md#ad-hoc-targets) describes the keys.

| Field | Meaning |
|-------|---------|
| `protocol` | Required. `ssh`, `vnc` or `rdp`. A kubernetes session needs a connection, which names its pod and credentials |
| `target` | Required. `host:port`, or a host alone for the protocol's default port: 22, 5900 or 3389. An IPv6 address goes in brackets with a port, such as `[fd00::22]:22` |
| `username` | Optional. The user name to log in as. Without one, the owner is asked for it if the remote needs one |
| `host_keys` | ssh only. The host keys the target may present, as a connection's `host_keys` takes them: public keys in `authorized_keys` format, or `SHA256:` fingerprints |
| `insecure_ignore_host_key` | ssh only. `true` accepts any host key, with a warning in the log |

The request carries no secret, and the target takes none from the
configuration file. `occamyd` asks the owner for its credentials on the
session's stream, and does not keep them.

`occamyd` refuses the target with `400 bad_request` when:

- the protocol is unknown, or is `kubernetes`;
- the target does not parse;
- an ssh target has neither or both of `host_keys` and
  `insecure_ignore_host_key`;
- a host key does not parse;
- a vnc or rdp target has host key fields.

It refuses the target with `403 forbidden` when `adhoc_allowlist` does
not allow it. `occamyd` resolves a host name within 5 seconds, checks
every address against the list, and dials the first address it
checked. These addresses are refused unless an entry is that very
address:

- loopback, link-local, unspecified and multicast addresses;
- the clouds' metadata services;
- `occamyd`'s own listeners.

An ad-hoc target uses `occamyd`'s SSH, VNC or RDP backend. A vnc target
checks the server's certificate if the server offers VeNCrypt, and an
rdp target always does. Both check it against the system's roots and
the host name the request gave, or its address if it gave an address.
An rdp target logs on with NLA alone, so a server without NLA, such as
xrdp, needs a connection with `security: tls`. A session on an ad-hoc
target has no `connection` in its state.

## Shares

A share lets someone without an account of their own watch a session,
or type into it.

- **Minting.** The owner calls
  [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares)
  with a role, `viewer` or `read-only`, and a lifetime. A share is
  never `owner`.
- **What it is.** 32 random bytes in base64url: 43 characters with no
  dots. It is not a JWT, and carries no claims. `occamyd` keeps only its
  SHA-256 hash, in memory. So shares do not survive a restart, and a
  share minted by one `occamyd` opens nothing on another.
- **Joining.** The holder opens
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) of
  the same session with the share as its token, as any token is sent
  there. A share opens only the session it was minted for. It joins as
  many times as the session's viewer limit allows, until it expires. A
  viewer that joined by a share has no user name. The session's state
  lists it by role.
- **Expiry.** An expired share is refused at the upgrade with `close`
  4000 `unauthorized`. Expiry ends no stream that has joined.
- **The session's end.** Shares end with their session, however it
  ends: closed by its owner, the owner not back within the grace, idle,
  ended by the remote, failed, or `occamyd` shut down. Every stream that
  joined by a share then ends with the session's close code.
- **Revoking.** A single share cannot be revoked. Closing the session
  ends them all.

## Streams

A stream is a WebSocket that carries one session in protocol v1. There
are two stream routes:

- [A connection's stream](#get-apiv1connectionsidstream) opens a new
  session and streams it, in one request.
- [A session's stream](#get-apiv1sessionsidstream) joins a running
  session.

Both take the token, `Origin` and `hello` the same way. Both answer a
refusal after the upgrade with protocol v1's `close`, because a browser
cannot read the HTTP status of a refused upgrade. After the upgrade,
the stream follows [protocol.md](protocol.md), from `welcome` and the
first frame to `close`.

### The upgrade request

A stream request is a WebSocket upgrade: a `GET` with `Upgrade:
websocket` that offers the subprotocol `occamy.v1` in
`Sec-WebSocket-Protocol`. It carries:

- **The token.** A browser offers the subprotocol `occamy.v1.token.`,
  followed by the token in base64url without padding, beside
  `occamy.v1`. A native client may instead offer `occamy.v1` alone and
  send `Authorization: Bearer`. A token in the subprotocol wins over the
  header: a bad one is refused even beside a good header. A token in the
  query string is ignored.
- **The `hello`.** The query parameter `hello` holds the whole `hello`
  message in base64url without padding, at most 2048 bytes before
  encoding ([protocol.md](protocol.md#the-upgrade-request)).
- **`Origin`**, which a browser adds (below).

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

The response is `101 Switching Protocols`, with
`Sec-WebSocket-Protocol: occamy.v1` alone and no compression.

`occamyd` takes a request without `Origin` on its token alone, since it
comes from no browser. A request with `Origin` is taken only if it has
exactly one, `http` or `https` with a host, and either:

- its host is the request's `Host`, and its scheme is `https` when
  `occamyd` terminates TLS; or
- its `scheme://host[:port]` is in `origins`.

`null`, an empty `Origin` and a repeated one are refused. Behind a proxy
that rewrites `Host`, list `occamyd`'s own origin in `origins`.

### Refusals

`occamyd` checks a stream request in this order: the subprotocol, then
`Origin`, the token's presence and the `hello`. Then it upgrades, and
the route verifies the token and opens or joins the session. Only the
first two rows answer in HTTP. The others answer after the upgrade.

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
| A token that fails verification | `close` 4000 `unauthorized`, with "token expired", "invalid token", or "the token's subject is a static user's name" |

Each route's own refusals follow in its section. A refused stream gets
the upgrade, then `close` with the code and a reason, then a WebSocket
close with the same code. A client that does not complete the closing
handshake within a second is dropped. Until the route has let a client
in, `occamyd` reads no message over 64 KiB.
[protocol.md](protocol.md#close-codes) lists the close codes.

### `GET /api/v1/connections/{id}/stream`

Opens a session on connection `{id}`, owned by the caller, and streams
it to the caller as its owner. `occamyd` sends `welcome` and the first
frame right after the upgrade, without waiting for a message from the
client.

**Request.** A [stream upgrade](#the-upgrade-request) with an access
token whose user the connection's `allow` names, by name or group. A
session token or a share is refused. The owner's `hello` sizes the
session: a terminal's columns and rows, or a desktop's pixels where the
backend can ask the remote for them.

**Response.** `101 Switching Protocols`, then protocol v1. The
session's ID is in `welcome`.

**Errors.** Besides the [refusals](#refusals) of every stream:

| Refusal | `close` |
|---------|---------|
| A session token, a share, or any token that is not a valid access token | 4000 `unauthorized` |
| No connection `{id}` that the caller may open | 4001 `forbidden`, "no such connection" |
| The caller owns `limits.sessions_per_user` sessions | 4001 `forbidden`, "too many sessions open" |
| `occamyd` runs `limits.sessions` sessions | 4001 `forbidden`, "occamyd runs as many sessions as it takes" |
| `occamyd` could not open the session, for a cause of its own | 1011 `internal-error` |

**Notes.**

- A remote that fails at once can end the session before `welcome`,
  such as one whose certificate is not the pinned one, or a Kubernetes
  API server that refuses the exec. The stream then gets `close` 4006
  `backend-failed` without `welcome`: the same `close` it would have had
  after `welcome` ([protocol.md](protocol.md#the-response)).
- Once the stream has joined, the session is like one that
  `POST /api/v1/sessions` opened. Its owner may leave and come back
  within the owner grace through
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream), with
  an access token. The owner may read, share and close it through the
  session routes.

### `GET /api/v1/sessions/{id}/stream`

Joins running session `{id}`. If the session ended within the owner
grace, it tells the owner how it ended.

**Request.** A [stream upgrade](#the-upgrade-request) with one of these
tokens:

| Token | Joins as |
|-------|----------|
| An access token of the session's owner | `owner` |
| A session token whose `sid` is `{id}`: the stream token, or a trusted issuer's | Its `role`. `owner` only if its user is the session's owner |
| A share of `{id}` | The share's role, `viewer` or `read-only` |

A token without dots is taken for a share. Any other is taken for a
JWT: an access token, or else a session token. A trusted issuer's
session token, of any role, joins only a session that one of the
issuer's own users owns. With the issuer's `link_static_users`, it may
also join a static user's.

**Response.** `101 Switching Protocols`, then protocol v1. The owner's
`hello` resizes the session. A viewer's resizes nothing.

**Errors.** Besides the [refusals](#refusals) of every stream:

| Refusal | `close` |
|---------|---------|
| A share that is unknown, expired, of another session, or whose session has ended | 4000 `unauthorized`, "invalid token" |
| A JWT that fails verification | 4000 `unauthorized` |
| No running session `{id}`, and no ended one whose owner this is (below) | 4001 `forbidden`, "no such session" |
| A session token for another session, or from an issuer that may not join this one | 4001 `forbidden`, "no such session" |
| An access token or `owner` session token of anyone but the owner | 4001 `forbidden`, "no such session" |
| A `viewer` or `read-only` session token of a session that has ended | 4001 `forbidden`, "no such session" |
| The owner's older stream did not leave within 5 seconds of a newer one | 4001 `forbidden`, "the owner is connected already" |
| A viewer beyond `limits.viewers_per_session` | 4007 `viewer-limit` |

**Notes.**

- **After the session ends.** For the owner grace, 60 seconds, after a
  session ends, its owner's stream gets the `close` the session ended
  with instead of `welcome`. Examples are 4006 `backend-failed` for a
  remote that failed before the stream came, as after
  [`POST /api/v1/sessions`](#post-apiv1sessions), and 4002
  `session-closed` after
  [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid). The owner
  must use an access token or an `owner` session token. Anyone else
  gets the refusal for a session that never existed, so nobody else
  learns whether a session existed or how it ended.
- **Ended sessions kept.** `occamyd` keeps the owner and the `close` of
  as many ended sessions as `limits.sessions`, and at least 1024. It
  forgets the oldest first, so a client that opens sessions whose
  remotes fail cannot grow them. An owner whose session was forgotten
  gets "no such session" too.
- **Takeover.** A newer stream of the owner takes over from an older
  one, which gets `close` 1000 `normal` with "the owner connected again".
  Otherwise a connection that died without `occamyd` noticing would keep
  the owner out of their own session. The reason is fixed, so a client
  can tell a takeover from its own close. The web client says the
  session was opened elsewhere, and offers to join it again.

## Web client

### `GET /static/`

Serves the web client from the directory that `web_client` names, as
`make client` builds it in `client/occamy-web/dist`.

**Request.** No token. The page signs in through API v1 on this origin,
since `occamyd` answers no CORS preflight. Served only with
`web_client`. Without it, `/static/` is a plain-text `404`.
`GET /static` redirects here.

**Response.** The file under the directory, with the type its
extension names, or the `index.html` of a directory. Every response has
these headers:

| Header | Value |
|--------|-------|
| `Cache-Control` | `public, max-age=31536000, immutable` for a file served from `assets/`. `no-cache` for anything else, a missing asset too |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Security-Policy` | The policy below |
| `Referrer-Policy` | `no-referrer` |

The files in `assets/` are named by a hash of their content, so a new
build names new files. The page itself is revalidated on every load, so
a page of an earlier build never asks for scripts that a newer build
replaced.

The Content-Security-Policy, for a request to `host`:

```text
default-src 'self'; script-src 'self'; worker-src 'self';
connect-src 'self' ws://host wss://host; img-src 'self' data:;
style-src 'self' 'unsafe-inline'; font-src 'self'; object-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

It allows what the page does, and nothing more:

- The page runs its own scripts, and the Worker that decodes tiles.
- It calls API v1 and opens streams on its own origin. `ws://host` and
  `wss://host` name it for browsers that do not count WebSockets as
  `'self'`.
- It shows the remote's pointer as a `data:` image.
- It takes inline styles, since the terminal emulator, xterm.js, adds
  `<style>` elements for its theme and cell size. React sets style
  attributes through the CSSOM, which the policy does not govern.
- No other page may frame it, so none can lay itself over a remote
  desktop and take its clicks.

A `Host` that is not a plain name or address, with an optional port, is
left out of `connect-src`, so a request cannot add to the policy.

**Errors.** A plain-text `404` for anything else: a missing file, a
directory without an `index.html`, which is never listed, and a
symbolic link that leads out of the directory, which is not followed.
A link within the directory is followed.

**Notes.** `occamyd` opens the directory for each request. It warns at
start when the directory does not exist or has no `index.html`, and
serves the client once it is built there, without a restart.

## The debug listener

With `debug.address` set, `occamyd` opens a second listener that
serves expvar and pprof, and nothing else. Without it, these routes are
served nowhere. The public listener never serves them.

The debug listener has no authentication, TLS, body limit or write
timeout, since a CPU profile takes as long as asked. Keep it on the
loopback interface. `occamyd` warns at start when it is not. Its
requests are logged as the public listener's are.
[debugging.md](debugging.md#profiling-occamyd) shows how to read the
profiles.

### `GET /debug/vars`

Returns Go's expvar, a JSON object, abbreviated here:

```json
{"cmdline":["occamyd","-conf","/etc/occamy/conf.yaml"],"fds":14,"goroutines":23,"memstats":{"Alloc":4194304,"HeapInuse":6291456,"NumGC":12},"tcp_congestion":"bbr"}
```

| Field | Meaning |
|-------|---------|
| `cmdline` | `occamyd`'s command line |
| `memstats` | The Go runtime's memory statistics |
| `goroutines` | The number of goroutines |
| `fds` | The file descriptors open, counted in `/proc/self/fd` on Linux or `/dev/fd` on macOS. -1 where `occamyd` cannot count them |
| `tcp_congestion` | The TCP congestion control of the public listener's connections ([configuration.md](configuration.md#tcp-congestion-control)). Empty where `occamyd` cannot tell |

### `GET /debug/pprof/`

Returns pprof's index, as HTML. Under it are the named profiles, such
as `/debug/pprof/goroutine`, `/debug/pprof/heap`, `/debug/pprof/allocs`
and `/debug/pprof/threadcreate`. Each comes as a profile for
`go tool pprof`, or as text with `?debug=1` or `?debug=2`.
`/debug/pprof` without the slash redirects here.

### `GET /debug/pprof/cmdline`

Returns `occamyd`'s command line as plain text, its arguments separated
by NUL bytes.

### `GET /debug/pprof/profile`

Returns a CPU profile over `seconds` seconds, 30 by default, for
`go tool pprof`.

### `GET /debug/pprof/symbol`

Returns the number of symbols the binary has, as plain text:
`num_symbols: 1` when it has any.

### `POST /debug/pprof/symbol`

Looks up the program counters in the body, hexadecimal and separated by
`+`, and answers each with its function's name, as `go tool pprof` asks
for them.

### `GET /debug/pprof/trace`

Returns an execution trace over `seconds` seconds, 1 by default, for
`go tool trace`.

## Design

[The API and server stack](../specs/008-native-architecture/api-and-server-stack.md)
records how the API was designed: the options weighed and the decisions
made, such as tokens out of URLs, refusals as `close` after the upgrade,
and absent rather than forbidden.
