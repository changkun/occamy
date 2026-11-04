# HTTP API v1

The HTTP API of `occamyd` signs users in, opens remote sessions, shares
them, and streams each one to the people who watch it. This page
explains how connections, sessions and tokens fit together, walks
through what a client does, and specifies every route, so that you can
write a client, automate Occamy, or check what a token allows.

## How the API works

A *connection* is a remote computer that the configuration file lists,
such as an SSH server or a VNC desktop, together with the users and
groups who may open it. Opening a connection starts a *session*: a live
connection to the remote that runs inside `occamyd`, and that several
people can watch at once. The user who opens a session is its *owner*.
Other people join it as *viewers*, who type and point as the owner
does, or as *read-only viewers*, who only watch. The owner invites them
with a *share*, a token that joins one session in one role.

The API has two kinds of route. The JSON routes under `/api/v1` sign
users in, list connections, and open, inspect, share and close
sessions. The two *stream* routes are WebSockets that carry a session in
[protocol v1](protocol.md): the remote's screen to the viewer, and the
viewer's keys and pointer to the remote. Everyone who watches a session,
the owner included, does so through a stream of their own.

Every route that reaches connections or sessions takes a token, and the
kind of token decides what it opens. You start with an *access token*,
which says who the user is, and which you get by signing in. A *session
token* opens the stream of one session in one role: `POST
/api/v1/sessions` returns one for the owner, and a trusted issuer can
mint them for its own users. A share opens the stream of one session for
someone without an account. This is how the three come into play, from
signing in to a guest joining:

```mermaid
sequenceDiagram
  participant C as Your client
  participant O as occamyd
  participant G as A guest
  C->>O: POST /api/v1/auth/token, with a user name and password
  O-->>C: An access token, for 15 minutes
  C->>O: POST /api/v1/sessions, with the access token
  O-->>C: The session's ID, and a session token for 5 minutes
  C->>O: GET /api/v1/sessions/{id}/stream, with the session token
  O-->>C: welcome as the owner, then the screen
  C->>O: POST /api/v1/sessions/{id}/shares, with the access token
  O-->>C: A share, for an hour
  C-->>G: The share, by any means
  G->>O: GET /api/v1/sessions/{id}/stream, with the share
  O-->>G: welcome as a read-only viewer, then the screen
```

| Token | What it is | How you get one | Lifetime | What it opens |
|-------|------------|-----------------|----------|---------------|
| Access token | A JWT with `kind: access` that names a user and the user's groups | [`POST /api/v1/auth/token`](#post-apiv1authtoken), [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken), or a trusted issuer | `auth.access_token_lifetime`, 15 minutes by default. Up to 12 hours from a trusted issuer | The JSON routes, as its user. A connection's stream. The stream of a session that its user owns, as the owner |
| Session token | A JWT with `kind: session`, for one session (`sid`) and one `role` | The `stream_token` of [`POST /api/v1/sessions`](#post-apiv1sessions), or a trusted issuer | 300 seconds. Up to 12 hours from a trusted issuer | The stream of the session in its `sid`, in its role: `owner`, `viewer` or `read-only` |
| Share | 43 random characters, not a JWT | [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | 60 to 43200 seconds, an hour by default, and never past its session's end | The stream of its session, as `viewer` or `read-only` |

`occamyd` serves the API on its `listen` address, `:5636` by default. It
speaks HTTPS when `tls.cert` and `tls.key` are set, and plain HTTP
otherwise, for a reverse proxy that terminates TLS. It speaks HTTP/1.1
only, because the streams upgrade from HTTP/1.1.
[Configuration](configuration.md) describes the keys that turn routes on
and set their limits, and [openapi.yaml](openapi.yaml) describes the
same routes in OpenAPI 3.1, for tools that generate clients.
[Routes](#routes) lists every route that this page specifies.

## The life of a session

A session runs on its own from the moment it opens, whoever is
watching. Viewers come and go as they like, and a viewer who leaves
never ends the session. The owner may leave and come back too, but the
session waits for its owner only for the *owner grace*, 60 seconds. If
the owner's stream drops, such as when a laptop changes networks, the
owner opens the session's stream again within that time and finds the
session as they left it.

```mermaid
stateDiagram-v2
  state "Owner connected" as owner_connected
  state "Owner away" as owner_away
  state "Ended" as session_ended
  [*] --> owner_connected: Opened by a connection's stream
  [*] --> owner_away: Opened by POST /api/v1/sessions
  owner_connected --> owner_away: The owner's stream ends
  owner_away --> owner_connected: The owner's stream joins
  owner_connected --> session_ended: Any end in the table below
  owner_away --> session_ended: 60 s without the owner, or any end in the table below
  session_ended --> [*]: 60 s later, occamyd forgets it
```

A session that [`POST /api/v1/sessions`](#post-apiv1sessions) opens
starts without its owner, so the owner grace runs from the start: the
owner has 60 seconds to open the session's stream. A session that
[a connection's stream](#get-apiv1connectionsidstream) opens starts
with its owner connected. Either way, `occamyd` connects to the remote
at once, and asks the owner on the stream for any credentials that the
configuration file doesn't hold.

A session ends in one of these ways. Every stream of the session then
receives `close` with the [close code](protocol.md#close-codes) that
says why, and every share of the session stops working:

| The session ends when | Its streams receive |
|-----------------------|---------------------|
| The owner closes it with [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid) | 4002 `session-closed` |
| The owner stays away for longer than the owner grace, 60 seconds | 4003 `owner-left` |
| Nobody gives input for 30 minutes. Input from a read-only viewer doesn't count, since `occamyd` drops it | 4004 `idle` |
| The remote ends the session, such as a shell that exits | 4005 `remote-ended` |
| The connection to the remote fails, or can't be made | 4006 `backend-failed` |
| `occamyd` shuts down | 1001 `server-shutdown` |
| `occamyd` fails on its own | 1011 `internal-error` |

No key of the configuration sets the owner grace or the idle timeout.

A single viewer's stream can also end while the session runs on. A
viewer that falls behind, leaving its frames unacknowledged or a write
to it blocked for the *viewer timeout*, receives `close` 4008
`too-slow`. The viewer timeout is `streams.viewer_timeout`, 30 seconds
by default ([Configuration](configuration.md#streams-streams)). When
the owner opens a newer stream of the session, the newer one takes over
and the older one is closed
([`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream)).
Whenever the owner's stream ends, for whatever reason, the owner grace
starts.

After a session ends, `occamyd` remembers how it ended for another 60
seconds, for its owner alone. An owner who opens the session's stream in
that time receives the `close` that the session ended with instead of a
refusal, so a client learns why its session ended even when the remote
failed before the stream arrived. Anyone else is refused as if the
session had never existed.

## What a client does

This section walks through what a client does, in the order that it
does it. The examples use `curl` and `jq` in a POSIX shell, with
`occamyd`'s address in `OCCAMY`:

```sh
OCCAMY=https://occamy.example
```

### Sign in with a password

Post a static user's name and password to
[`POST /api/v1/auth/token`](#post-apiv1authtoken), and keep the access
token that comes back. The rest of the examples send it in `TOKEN`:

```sh
TOKEN=$(curl -s "$OCCAMY/api/v1/auth/token" \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"correct horse battery staple"}' |
  jq -r .access_token)
```

The access token lasts `auth.access_token_lifetime`, 15 minutes by
default, and `occamyd` issues no refresh tokens, so sign in again when
it expires. A stream that has joined keeps running when the token that
opened it expires.

To find out whether `occamyd` also offers OIDC, as a sign-in page does,
call [`GET /api/v1/auth/methods`](#get-apiv1authmethods).

### Sign in through OIDC

When `occamyd` has `auth.oidc`, users sign in at an OpenID Connect
provider instead. The browser travels from `occamyd` to the provider and
back, and the client ends with the same access token as from a
password. The client proves at the end that it started the sign-in,
with PKCE (RFC 7636), so a code that leaks from the browser is useless
to anyone else:

```mermaid
sequenceDiagram
  participant B as Your client, in a browser
  participant O as occamyd
  participant P as OIDC provider
  Note over B: Makes a PKCE verifier and keeps it
  B->>O: GET /api/v1/auth/oidc/login, with the verifier's challenge and a return URL
  O-->>B: 302 to the provider, and the sign-in cookie
  B->>P: The user signs in
  P-->>B: 302 to occamyd's callback, with the provider's code
  B->>O: GET /api/v1/auth/oidc/callback, with the cookie
  O->>P: Exchanges the provider's code for an ID token
  O-->>B: 302 to the return URL, with a one-time code in the fragment
  B->>O: POST /api/v1/auth/oidc/token, with the one-time code and the verifier
  O-->>B: An access token
```

1. Make a PKCE verifier, keep it, and compute its S256 challenge:

    ```sh
    VERIFIER=$(openssl rand 32 | openssl base64 -A | tr '+/' '-_' | tr -d '=')
    CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary |
      openssl base64 -A | tr '+/' '-_' | tr -d '=')
    ```

2. Send the browser to
   [`GET /api/v1/auth/oidc/login`](#get-apiv1authoidclogin) with the
   challenge and the URL that it should return to.
3. The user signs in at the provider. The browser comes back to the
   return URL with `#code=…`, or `#error=…`, in the fragment, which
   never reaches a server.
4. Post the one-time code from the fragment, here in `CODE`, and the
   verifier to
   [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken):

    ```sh
    TOKEN=$(curl -s "$OCCAMY/api/v1/auth/oidc/token" \
      -H 'Content-Type: application/json' \
      -d "{\"code\":\"$CODE\",\"code_verifier\":\"$VERIFIER\"}" |
      jq -r .access_token)
    ```

[Configuration](configuration.md#signing-in-through-oidc) describes how
to set OIDC up.

### Open a session

List the connections that the user may open:

```sh
curl -s "$OCCAMY/api/v1/connections" -H "Authorization: Bearer $TOKEN"
```

Then open a session in one of two ways:

| You want | Open |
|----------|------|
| The first frame as soon as possible | [A connection's stream](#get-apiv1connectionsidstream). It opens the session and streams it in one request, and its first message, `welcome`, carries the session's ID |
| The session's ID before its stream | [`POST /api/v1/sessions`](#post-apiv1sessions), then [the session's stream](#get-apiv1sessionsidstream) within 60 seconds |

The second way starts like this, and keeps the session's ID in
`SESSION`:

```sh
SESSION=$(curl -s "$OCCAMY/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"connection":"build-box"}' |
  jq -r .id)
```

The response also carries a stream token, a session token that opens
the session's stream as its owner for the next 5 minutes. The owner's
access token opens that stream too.

Either way, the client then opens a stream as [Streams](#streams)
describes, and speaks [protocol v1](protocol.md) on it. For the
credentials that a connection prompts for, and for all of an ad-hoc
target's, `occamyd` asks the owner on the stream, as
[`prompt`](protocol.md#prompt) messages.

### Share a session

The owner mints a share with a role and a lifetime, and hands it to
someone by any means, such as a chat message:

```sh
SHARE=$(curl -s "$OCCAMY/api/v1/sessions/$SESSION/shares" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"role":"read-only","expires_in":600}' |
  jq -r .token)
```

The holder opens [the session's
stream](#get-apiv1sessionsidstream) with the share as its token, as
many times as the session's viewer limit allows, until the share
expires. A share joins only the session it was minted for, and only
while that session runs.

> [!IMPORTANT]
> A single share can't be revoked. To stop every share of a session,
> close the session.

### Come back to a session

When the owner's stream drops, the session waits for the owner grace,
60 seconds. Within that time, the owner opens
[the session's stream](#get-apiv1sessionsidstream) again with an
access token, or with the stream token while it lasts, and continues
where they left off. A newer stream of the owner takes over from an
older one, so a client doesn't have to wait until `occamyd` notices that
the old connection has died.

If the session ended in the meantime, and no more than 60 seconds ago,
the new stream receives the `close` that the session ended with, such
as 4006 `backend-failed`, in place of `welcome`.

### Check on a session and close it

The owner reads the session's state, such as its size and who is
watching, with [`GET /api/v1/sessions/{id}`](#get-apiv1sessionsid):

```sh
curl -s "$OCCAMY/api/v1/sessions/$SESSION" -H "Authorization: Bearer $TOKEN"
```

When the owner is done, they close the session for everyone with
[`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid), or leave it
to end on its own:

```sh
curl -s -X DELETE "$OCCAMY/api/v1/sessions/$SESSION" -H "Authorization: Bearer $TOKEN"
```

## Requests and responses

Every route of API v1 follows the same rules for what it reads and what
it writes:

- **JSON in and out.** A response with a body holds one compact JSON
  value, with no line break at its end. It has `Content-Type:
  application/json; charset=utf-8` and `X-Content-Type-Options:
  nosniff`.
- **Request bodies of the JSON type.** A request body must have
  `Content-Type: application/json`, or the route answers `415
  unsupported_media_type`. `occamyd` ignores parameters such as
  `charset`. A body over 64 KiB gets `413 too_large`.
- **Bodies that decode exactly.** A body must hold exactly one JSON
  value. It may name only the fields that the route takes, in their
  exact case and each once, at any depth, and each of the right type.
  Objects and arrays nest at most 32 deep. Any other body gets `400
  bad_request`, so `{"Connection":"x"}` and
  `{"username":"a","username":"b"}` are both refused. This way, a proxy
  that reads the body never sees a different request from the one that
  `occamyd` sees.
- **Tokens never in a URL.** The routes read a token from the
  `Authorization` header, and the stream routes from a WebSocket
  subprotocol too. They never read one from the query string or a
  cookie, which proxies, logs and browser histories keep. The request
  log names each request's method, path, status, duration and peer
  address, and never its query string, headers or body.
- **No caching.** A response that carries a token or a session's state
  has `Cache-Control: no-store`. Each route says where it applies.
- **No CORS.** `occamyd` answers no preflight, so a web page that calls
  the JSON routes must come from `occamyd`'s own origin, such as through
  the same reverse proxy. Pages of the origins in `origins` may open
  streams. Because a route requires the JSON type, a cross-site HTML
  form can't post to the API either.
- **Unknown routes.** A path at or under `/api/v1` that no route takes
  answers `404 not_found`. A route asked with a method that it doesn't
  take answers `405 method_not_allowed`, with `Allow` listing the
  methods it takes. Both use the [error shape](#errors). Outside
  `/api/v1`, such as `/healthz` or `/static/`, the same cases answer in
  plain text.
- **`HEAD`.** A route that takes `GET` also answers `HEAD`.

## Authentication

### Token claims

Access tokens and session tokens are JWTs signed with Ed25519 (`alg:
EdDSA`). `occamyd` signs its own with its signing key, and names itself
in `iss: occamy`. A trusted issuer signs with its own key. Every token
names `aud: occamy` and a user in `sub`. Each carries `iat` and `exp`,
at most 12 hours apart, and its kind in `kind`, so that one kind is
never taken for the other.

An access token from `occamyd` also carries the user's groups in
`groups`, and in `idp` where the user signed in: `local` with a
password, or `oidc` through the OIDC provider. A session token carries
the session's ID in `sid` and the role in `role`.
[Configuration](configuration.md#tokens-from-a-trusted-issuer) lists the
claims that a trusted issuer must set.

### Sending a token

| Route | Takes | Sent in |
|-------|-------|---------|
| `GET /healthz`, `GET /api/v1/auth/methods`, the sign-in routes | nothing | |
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
them by what they allow:

| Role | The web client says | May |
|------|---------------------|-----|
| `owner` | owner | Send input, and answer `prompt` messages for credentials. The owner's `hello` sizes the session. Only the owner reads, shares and closes the session through the API |
| `viewer` | with control | Send [input](protocol.md#input-messages), as the owner does |
| `read-only` | view only | Watch. `occamyd` drops its input |

The owner is the user who opened the session. There is no administrator
role.

### Users

A user is a `sub` from one issuer. For `occamyd`'s own users, where
they signed in counts too: `idp: local` for a static user, and `idp:
oidc` for an OIDC user. So `carol` from a trusted issuer and `occamyd`'s
own `carol` are two users, and neither owns the other's sessions.

A static user's name always means the static user. An OIDC sign-in, or
a trusted issuer, with `link_static_users` that names exactly a static
user's name acts as that static user.

A connection's `allow.users` and `allow.groups` match names, whichever
issuer signed the token. A trusted issuer's `groups` count only for the
groups in its `allowed_groups`.
[Configuration](configuration.md#signing-in-through-oidc) gives the
rules in full.

### When a token fails

A JSON route answers every failure of a token with `401` and an RFC 6750
challenge in `WWW-Authenticate`, and never repeats the token:

| Request | `code` | `WWW-Authenticate` |
|---------|--------|--------------------|
| No `Authorization` header, more than one, another scheme, or an empty token | `unauthorized` | `Bearer realm="occamy"` |
| A token past its `exp` and the [clock skew allowed](#lifetimes) | `token_expired` | `Bearer realm="occamy", error="invalid_token"` |
| Any other token that fails, as listed below | `invalid_token` | `Bearer realm="occamy", error="invalid_token"` |

A token is `invalid_token` when it:

- is malformed, or its signature doesn't verify;
- comes from an unknown issuer, or names another audience;
- is signed with an algorithm other than EdDSA;
- has no `iat`, or lives longer than 12 hours;
- is a session token;
- names a `sub` that isn't a usable user name;
- names a static user's name, from a source that isn't linked to the
  static users.

A route checks the token before the body, so a request without a valid
token gets `401` whatever its body holds. A token that expires ends no
stream that has joined: only the session's end does.

## Errors

API v1 answers every error with the same shape:

```json
{"error":{"code":"invalid_token","message":"the access token is not valid"}}
```

Branch on `code`, which is stable: new codes may appear, but a code
never changes its meaning. `message` is for people. It may change, and
it never holds a secret.

| `code` | Status | When |
|--------|--------|------|
| `bad_request` | 400 | The body doesn't [decode](#requests-and-responses), or the route refuses what it says. Also a stream request without the `occamy.v1` subprotocol |
| `invalid_state` | 400 | The OIDC callback matches no sign-in that this browser started in the last 10 minutes |
| `unauthorized` | 401 | No access token, or wrong credentials at sign-in |
| `invalid_token` | 401 | A token that fails verification, or whose user `occamyd` refuses |
| `token_expired` | 401 | A token past its expiry and the clock skew allowed |
| `forbidden` | 403 | An ad-hoc target while ad-hoc targets are off, or one outside `adhoc_allowlist` |
| `not_found` | 404 | No such route, connection or session, or [one that the caller may not see](#connections-and-sessions) |
| `method_not_allowed` | 405 | The route doesn't take the method. `Allow` lists those it takes |
| `too_large` | 413 | A JSON body over 64 KiB |
| `unsupported_media_type` | 415 | A body that isn't `application/json` |
| `limit_exceeded` | 429 | The caller holds as much as `limits` allows: `sessions_per_user` sessions, or `shares_per_session` shares of the session |
| `limit_exceeded` | 503 | `occamyd` runs `limits.sessions` sessions, whoever owns them. Try again later |
| `internal` | 500 | `occamyd` failed on its own, such as on a password file that it can't read. The cause goes to its log, not to the client |
| `access_denied` | 302 | OIDC sign-in: the provider or `occamyd` refused the user |
| `sign_in_failed` | 302 | OIDC sign-in: the provider failed, or its answer didn't verify |
| `unavailable` | 302 | OIDC sign-in: the provider can't be reached, or too many sign-ins wait |

The last three reach the client as `#error=…` in the fragment of its
return URL, not in a JSON body
([`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback)).

A few failures don't use this shape:

- Request headers over 64 KiB get a plain-text `431`.
- A stream request that offers `occamy.v1` but isn't a valid WebSocket
  upgrade gets the WebSocket library's plain-text `426` or `400`.
- A stream refused after the upgrade gets a protocol v1 `close`
  ([Refusals](#refusals)).

## Limits

### Requests

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

### Lifetimes

| What | Lifetime |
|------|----------|
| An access token from `occamyd` | `auth.access_token_lifetime`: 15 minutes by default, from 1 minute to 12 hours |
| A trusted issuer's token | Up to 12 hours from its `iat` |
| The stream token of `POST /api/v1/sessions` | 300 seconds |
| A share | `expires_in`: 60 to 43200 seconds, 3600 by default. It ends with its session |
| An OIDC sign-in, from `login` to the callback | 10 minutes |
| An OIDC one-time code, from the callback to the exchange | 1 minute, and one try |
| The owner grace: how long a session waits for its owner's stream, after `POST /api/v1/sessions` or after the owner leaves | 60 seconds. Then the session ends with `owner-left` (4003). No key sets it |
| A session without input from the owner or a viewer | 30 minutes. Then the session ends with `idle` (4004). No key sets it |
| A viewer that leaves its frames unacknowledged, or a write to it blocked | `streams.viewer_timeout`: 30 seconds by default, at least 1 second. Then its stream ends with `too-slow` (4008), and the session runs on |
| An ended session, for its owner's stream | 60 seconds after the end, for at most `limits.sessions` ended sessions and at least 1024 |
| The owner's older stream, when a newer one takes over | 5 seconds to leave. Then the newer one is refused |
| A refused stream's closing handshake | 1 second. Then the connection is dropped |

`occamyd` allows 30 seconds of clock skew on every token of API v1: its
own access and stream tokens, and a trusted issuer's. So a token still
works for up to 30 seconds after its `exp`. Don't rely on that: renew a
token once its `exp` has passed, since `401 token_expired` follows
within 30 seconds. The ID tokens that `occamyd` receives from an OIDC
provider get no such allowance on their expiry.

### What users and sessions may hold

These keys of [`limits`](configuration.md#limits-limits) bound what one
user, one session, or `occamyd` as a whole may hold:

| Key | Default | Beyond it |
|-----|---------|-----------|
| `limits.sessions` | 1000 | `POST /api/v1/sessions` answers `503 limit_exceeded`. A connection's stream gets `close` 4001 `forbidden` |
| `limits.sessions_per_user` | 10 | `POST /api/v1/sessions` answers `429 limit_exceeded`. A connection's stream gets `close` 4001 `forbidden` |
| `limits.viewers_per_session` | 50, besides the owner, who can always join | A session's stream gets `close` 4007 `viewer-limit` |
| `limits.shares_per_session` | 100 that haven't expired | `POST /api/v1/sessions/{id}/shares` answers `429 limit_exceeded` |

A session counts toward its owner's limit and `occamyd`'s from the
moment it opens until it ends, so concurrent requests can't all slip
past a limit. Neither sign-in nor opening sessions is rate limited.

## Routes

Each route below has a section in the same shape: what it does, who may
call it, the request, the response, the errors, and an example to run
with curl. The examples use the variables that
[What a client does](#what-a-client-does) sets, such as `OCCAMY` and
`TOKEN`. The debug listener's routes, which serve Go's own tools, are
described more briefly.

| Route | What it does | Who may call it | Served |
|-------|--------------|-----------------|--------|
| [`GET /healthz`](#get-healthz) | Reports that `occamyd` is up | Anyone | Always |
| [`GET /api/v1/auth/methods`](#get-apiv1authmethods) | Says how users can sign in | Anyone | Always |
| [`POST /api/v1/auth/token`](#post-apiv1authtoken) | Signs a static user in with a password | Anyone | Always |
| [`GET /api/v1/auth/oidc/login`](#get-apiv1authoidclogin) | Starts an OIDC sign-in | A browser | With `auth.oidc` |
| [`GET /api/v1/auth/oidc/callback`](#get-apiv1authoidccallback) | Takes the browser back from the provider | The provider, through the browser | With `auth.oidc` |
| [`POST /api/v1/auth/oidc/token`](#post-apiv1authoidctoken) | Exchanges an OIDC sign-in's one-time code for an access token | Anyone | With `auth.oidc` |
| [`GET /api/v1/connections`](#get-apiv1connections) | Lists the connections that the user may open | Any user, with an access token | Always |
| [`POST /api/v1/sessions`](#post-apiv1sessions) | Opens a session | Any user, with an access token | Always |
| [`GET /api/v1/sessions/{id}`](#get-apiv1sessionsid) | Returns a session's state | The owner, with an access token | Always |
| [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid) | Closes a session | The owner, with an access token | Always |
| [`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares) | Mints a share of a session | The owner, with an access token | Always |
| [`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream) | Opens a session and streams it | Any user, with an access token | Always |
| [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) | Joins a running session | The owner with an access token, or anyone with a session token or a share | Always |
| [`GET /static/`](#get-static) | Serves the web client | A browser | With `web_client` |
| [`GET /debug/vars`](#get-debugvars) and `/debug/pprof/` | Serve expvar and pprof | Anyone who reaches the debug listener | With `debug.address`, on that listener only |

## Health

### `GET /healthz`

Reports that `occamyd` is up.

**Who may call it.** Anyone. It takes no token.

**Request.** No body.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"status":"ok"}
```

**Errors.** None.

**Example.**

```sh
curl -s "$OCCAMY/healthz"
```

## Sign-in

Sign-in turns a user's credentials into an access token. Static users
sign in with a password at
[`POST /api/v1/auth/token`](#post-apiv1authtoken). With `auth.oidc`,
users sign in at an OpenID Connect provider through the three OIDC
routes, in the order that [Sign in through
OIDC](#sign-in-through-oidc) shows. Both ways answer with the same token
response, in the shape of OAuth 2.0's (RFC 6749, section 5.1). A
trusted issuer's access tokens need no sign-in.

### `GET /api/v1/auth/methods`

Says how a user can sign in, so that a sign-in page shows the right
choices.

**Who may call it.** Anyone. It takes no token, and it asks nothing of
the OIDC provider.

**Request.** No body.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"oidc":true}
```

| Field | Meaning |
|-------|---------|
| `oidc` | Whether `occamyd` has `auth.oidc`, and so serves the three OIDC routes |

Password sign-in has no field, since its route is always served.

**Errors.** None.

**Example.**

```sh
curl -s "$OCCAMY/api/v1/auth/methods"
```

### `POST /api/v1/auth/token`

Signs a static user in with a password, and returns an access token.

**Who may call it.** Anyone. It takes no token.

**Request.** The body names the user and the password:

```json
{"username":"alice","password":"correct horse battery staple"}
```

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

| Field | Meaning |
|-------|---------|
| `access_token` | The access token. It names the user in `sub`, the user's groups in `groups`, and `idp: local` |
| `token_type` | Always `Bearer` |
| `expires_in` | The token's lifetime in seconds, `auth.access_token_lifetime` |

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `username` or `password` is missing or empty, or the body doesn't decode |
| 401 | `unauthorized` | A wrong password or an unknown user, alike. With `WWW-Authenticate: Bearer realm="occamy"` |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that isn't `application/json` |
| 500 | `internal` | The token couldn't be signed |

**Example.**

```sh
curl -s "$OCCAMY/api/v1/auth/token" \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"correct horse battery staple"}'
```

**Notes.** Without static users, every sign-in fails with `401`. An
unknown user takes as long as a known one, because `occamyd` compares
the password with a hash at the users' highest bcrypt cost either way,
so the time of an answer doesn't tell which users exist. A failed
sign-in is logged with the peer's address and never the user name,
since people sometimes type a password into the user name's field.

### `GET /api/v1/auth/oidc/login`

Starts an OIDC sign-in. A browser navigates here, and `occamyd` sends it
on to the provider.

**Who may call it.** A browser, without a token. Served only with
`auth.oidc`; without it, the route answers `404 not_found`.

**Request.** The query:

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

- It's named `__Secure-occamy_oidc` when it's `Secure`, and
  `occamy_oidc` otherwise.
- It's `HttpOnly`, `SameSite=Lax` and `Max-Age=600`, on the path of
  `auth.oidc.redirect_url`.
- It's `Secure` when `redirect_url` is https or `occamyd` terminates
  TLS.
- It holds the state, the nonce, `occamyd`'s verifier, the return URL
  and the client's challenge. They're sealed with AES-256-GCM under a
  key that `occamyd` makes when it starts, so the browser can neither
  read nor alter them.

A second sign-in in the same browser replaces the first. If `occamyd`
can't discover the provider, it sends the browser straight back to the
return URL with `#error=unavailable`, and sets no cookie.

**Errors.** Each comes with `Cache-Control: no-store`, and sets
nothing.

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | The query isn't valid, as listed below |
| 500 | `internal` | The cookie couldn't be sealed |

The query isn't valid when:

- `return_url` isn't one of `auth.oidc.return_urls`, is given twice, or
  is left out while several are configured;
- `code_challenge` is missing, given twice, or not an S256 challenge;
- `code_challenge_method` is given twice, or isn't `S256`.

**Example.** This prints where `occamyd` sends the browser, for the
challenge that [Sign in through OIDC](#sign-in-through-oidc) computes
and the return URL `$OCCAMY/`. That's the default of
`auth.oidc.return_urls` when `redirect_url` is on the same origin:

```sh
curl -s -o /dev/null -w '%{redirect_url}\n' -G "$OCCAMY/api/v1/auth/oidc/login" \
  --data-urlencode "return_url=$OCCAMY/" \
  --data-urlencode "code_challenge=$CHALLENGE"
```

### `GET /api/v1/auth/oidc/callback`

Takes the browser back from the provider once the user has signed in,
and on to the return URL with a one-time code. Its URL is
`auth.oidc.redirect_url`.

**Who may call it.** The provider, through the browser that started the
sign-in. It takes no token: the sign-in cookie from `login` ties the
request to that browser. Served only with `auth.oidc`; without it, the
route answers `404 not_found`.

**Request.** The query is what the provider sends: `state` and `code`,
or `error`, and `iss` when the provider names itself (RFC 9207).

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
at most three codes waiting, so a fourth sign-in drops the oldest.

If the sign-in failed, the fragment holds `#error=` and one of these:

| `error` | When |
|---------|------|
| `access_denied` | The provider or `occamyd` refused the user |
| `sign_in_failed` | The provider failed, or its answer didn't verify |
| `unavailable` | The provider can't be reached, or 4096 one-time codes wait to be exchanged |

`occamyd` refuses the user with `access_denied` when:

- the provider answered `error=access_denied`;
- the `subject_claim` is missing, or isn't a usable user name;
- the `subject_claim` is `email`, and the provider hasn't verified the
  address;
- the name is a static user's, and `link_static_users` is off;
- `require_known_users` doesn't admit the user.

The sign-in fails with `sign_in_failed` when:

- the provider answered with another error, or with no code;
- `iss` names another issuer, or is missing from a provider that says
  it sends one;
- the code exchange failed, or brought no ID token;
- the ID token's issuer, audience, authorized party, expiry, algorithm,
  signature or nonce doesn't verify;
- the `groups_claim` is neither a list of strings nor a string;
- `occamyd` can't read its client secret.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `invalid_state` | No sign-in cookie of this browser matches `state`. With `Cache-Control: no-store`, since no return URL can be trusted yet |

`state` matches no cookie when the sign-in started in another browser,
took longer than 10 minutes, outlived a restart of `occamyd`, or was
replaced by a later `login`.

**Example.** You don't call the callback yourself, since the provider
sends the browser there. Without the browser's sign-in cookie, it
answers `invalid_state`:

```sh
curl -s "$OCCAMY/api/v1/auth/oidc/callback?state=x&code=y"
```

**Notes.** The callback clears the sign-in cookie when `state` matches
it, or when the browser holds no sign-in in progress.

### `POST /api/v1/auth/oidc/token`

Exchanges the one-time code from the callback, with the verifier of the
challenge given to `login`, for an access token.

**Who may call it.** Anyone who holds the code and the verifier. It
takes no token. Served only with `auth.oidc`; without it, the route
answers `404 not_found`.

**Request.** The body holds the code and the verifier, here RFC 7636's
example verifier:

```json
{"code":"JX2E7OZK6V3QWBTLDM4PNRYH5A","code_verifier":"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}
```

**Response.** `200`, with `Cache-Control: no-store`, in the shape of
[`POST /api/v1/auth/token`](#post-apiv1authtoken)'s:

```json
{"access_token":"eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJ…","token_type":"Bearer","expires_in":900}
```

The access token names the user from `subject_claim`, the groups from
`groups_claim` that the configuration names, and `idp: oidc`.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | `code` or `code_verifier` is missing or empty, or the body doesn't decode |
| 401 | `unauthorized` | The code or the verifier is wrong, as described below. With `WWW-Authenticate: Bearer realm="occamy"` |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that isn't `application/json` |
| 500 | `internal` | The token couldn't be signed |

The route answers `401` alike when the code is unknown, used or
expired, and when the verifier isn't 43 to 128 characters of PKCE's
alphabet or doesn't match the challenge. The first try spends the code,
right or wrong.

**Example.** With the one-time code from the return URL's fragment in
`CODE`, and the verifier that the challenge came from in `VERIFIER`:

```sh
curl -s "$OCCAMY/api/v1/auth/oidc/token" \
  -H 'Content-Type: application/json' \
  -d "{\"code\":\"$CODE\",\"code_verifier\":\"$VERIFIER\"}"
```

## Connections and sessions

These routes list connections and open, inspect, share and close
sessions. Each takes an access token in `Authorization: Bearer`, and
answers `401` without a valid one
([When a token fails](#when-a-token-fails)).

They treat what the caller may not see as absent, so that nobody can
probe for IDs:

- A connection whose `allow` names neither the caller nor one of the
  caller's groups answers `404 not_found`, as an unknown one does.
- Another user's session answers `404 not_found` to `GET`, `DELETE` and
  shares, as an ended one does.

The stream routes answer both with `close` 4001 `forbidden`.

### `GET /api/v1/connections`

Lists the connections that the caller may open, in the order of the
configuration file.

**Who may call it.** Any user, with an access token.

**Request.** No body.

**Response.** `200`, with `Cache-Control: no-store`:

```json
{"connections":[{"id":"build-box","name":"Build box","protocol":"ssh"},{"id":"nas-desktop","name":"nas-desktop","protocol":"vnc"}],"adhoc_targets":false}
```

| Field | Meaning |
|-------|---------|
| `connections` | The connections that the caller may open, `[]` if none |
| `connections[].id` | The connection's ID |
| `connections[].name` | Its name, which is the ID unless the file gives one |
| `connections[].protocol` | `ssh`, `vnc`, `rdp` or `kubernetes` |
| `adhoc_targets` | Whether [`POST /api/v1/sessions`](#post-apiv1sessions) takes [ad-hoc targets](#ad-hoc-targets) |

The list leaves out each connection's target, credentials and pod.

**Errors.** Only the `401`s of a token that fails.

**Example.**

```sh
curl -s "$OCCAMY/api/v1/connections" -H "Authorization: Bearer $TOKEN"
```

### `POST /api/v1/sessions`

Opens a session owned by the caller, on a connection or on an
[ad-hoc target](#ad-hoc-targets), and returns its ID before any stream.
A client that wants the first frame at once opens
[`GET /api/v1/connections/{id}/stream`](#get-apiv1connectionsidstream)
instead.

**Who may call it.** Any user, with an access token: for a connection,
one whose `allow` names the user or one of the user's groups.

**Request.** The body names a connection:

```json
{"connection":"build-box"}
```

Or, with `allow_adhoc_targets`, an ad-hoc target, with the fields that
[Ad-hoc targets](#ad-hoc-targets) lists:

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
| 400 | `bad_request` | Both a connection and ad-hoc fields, or neither. An ad-hoc target that isn't valid ([Ad-hoc targets](#ad-hoc-targets)). A body that doesn't decode, such as one with a `password` field |
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 403 | `forbidden` | Ad-hoc fields while `allow_adhoc_targets` is off, or an ad-hoc target outside `adhoc_allowlist` |
| 404 | `not_found` | No connection of that ID that the caller may open |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that isn't `application/json` |
| 429 | `limit_exceeded` | The caller owns `limits.sessions_per_user` sessions. Close one first |
| 503 | `limit_exceeded` | `occamyd` runs `limits.sessions` sessions |
| 500 | `internal` | `occamyd` failed for a cause of its own, such as a password or key file that it can't read. The log names the user, the target and the cause |

**Example.**

```sh
curl -s "$OCCAMY/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"connection":"build-box"}'
```

**Notes.**

- The session starts at once, without its owner. The owner has the
  owner grace, 60 seconds, to open
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream)
  with the stream token or the owner's access token. Otherwise the
  session ends with `owner-left`.
- `occamyd` dials the remote after this answer. A target that can't be
  reached ends the session, and its stream gets `close` 4006
  `backend-failed`, even when the session ended before the stream came.
- `occamyd` asks the owner on that stream, as `prompt` messages
  ([Protocol](protocol.md#prompt)), for the credentials that the
  connection prompts for, and for all of an ad-hoc target's.
- The owner's `hello` sizes the session when the stream joins.

### `GET /api/v1/sessions/{id}`

Returns the state of a running session: what it connects to, its size,
and who is watching it.

**Who may call it.** The session's owner, with an access token.

**Request.** No body.

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
| `class` | `terminal` for ssh and kubernetes, and `graphical` for vnc and rdp: whether the stream carries a terminal or tiles of pixels |
| `size` | `width` and `height`: character cells for a terminal, and pixels for a graphical session. Absent until the remote reports a size |
| `viewers` | The viewers connected now: the owner first if connected, then the others in the order they joined. `[]` while nobody is connected |
| `viewers[].id` | The viewer's ID, as in `viewer-joined` |
| `viewers[].role` | `owner`, `viewer` or `read-only` |
| `opened` | When the session was opened, RFC 3339 in UTC, to the second |

A viewer who joined by a share has no user name, so the state lists
them by role alone.

**Errors.**

| Status | `code` | When |
|--------|--------|------|
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 404 | `not_found` | No running session of that ID that the caller owns |

**Example.**

```sh
curl -s "$OCCAMY/api/v1/sessions/$SESSION" -H "Authorization: Bearer $TOKEN"
```

### `DELETE /api/v1/sessions/{id}`

Closes a session for everyone in it.

**Who may call it.** The session's owner, with an access token.

**Request.** No body.

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

**Example.**

```sh
curl -s -X DELETE "$OCCAMY/api/v1/sessions/$SESSION" -H "Authorization: Bearer $TOKEN"
```

### `POST /api/v1/sessions/{id}/shares`

Mints a share of a session: a token that joins the session's stream as
a viewer or a read-only viewer, so that someone without an account can
watch the session or type into it.

**Who may call it.** The session's owner, with an access token.

**Request.** The body:

```json
{"role":"read-only","expires_in":600}
```

| Field | Meaning |
|-------|---------|
| `role` | Required. `viewer` or `read-only`. A share is never `owner` |
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
| 400 | `bad_request` | `role` isn't `viewer` or `read-only`, `expires_in` lies outside 60 to 43200, or the body doesn't decode |
| 401 | `unauthorized`, `invalid_token`, `token_expired` | No valid access token |
| 404 | `not_found` | No running session of that ID that the caller owns. Checked before the body |
| 413 | `too_large` | A body over 64 KiB |
| 415 | `unsupported_media_type` | A body that isn't `application/json` |
| 429 | `limit_exceeded` | The session has `limits.shares_per_session` shares that haven't expired |
| 500 | `internal` | The share couldn't be minted |

**Example.**

```sh
curl -s "$OCCAMY/api/v1/sessions/$SESSION/shares" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"role":"read-only","expires_in":600}'
```

**Notes.** A share is 32 random bytes in base64url: 43 characters with
no dots. It isn't a JWT, and it carries no claims. `occamyd` keeps only
its SHA-256 hash, in memory, so shares don't survive a restart, and a
share that one `occamyd` minted opens nothing on another.

The holder joins with the share as the token of
[`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream) of the
same session, sent as any token is sent there. A share opens only the
session it was minted for. It joins as many times as the session's
viewer limit allows, until it expires. When it expires, the next stream
that offers it is refused with `close` 4000 `unauthorized`, but the
streams that have already joined keep running.

Shares end with their session, however it ends: closed by its owner,
the owner not back within the owner grace, idle, ended by the remote,
failed, or `occamyd` shut down. Every stream that joined by a share
then ends with the session's close code. A single share can't be
revoked; closing the session ends them all.

## Ad-hoc targets

With `allow_adhoc_targets: true`, any signed-in user may open a session
on a target that isn't in the configuration file. Only
[`POST /api/v1/sessions`](#post-apiv1sessions) takes one, and
`GET /api/v1/connections` then says `"adhoc_targets":true`.
[Configuration](configuration.md#ad-hoc-targets) describes the keys.

An ad-hoc target is these fields of the request's body:

| Field | Meaning |
|-------|---------|
| `protocol` | Required. `ssh`, `vnc` or `rdp`. A kubernetes session needs a connection, which names its pod and credentials |
| `target` | Required. `host:port`, or a host alone for the protocol's default port: 22, 5900 or 3389. An IPv6 address alone takes the protocol's port too, such as `fd00::22`, and goes in brackets with a port, such as `[fd00::22]:2222` |
| `username` | Optional. The user name to log in as. Without one, the owner is asked for it if the remote needs one |
| `host_keys` | ssh only. The host keys that the target may present, as a connection's `host_keys` takes them: public keys in `authorized_keys` format, or `SHA256:` fingerprints |
| `insecure_ignore_host_key` | ssh only. `true` accepts any host key, with a warning in the log |

The request carries no secret, and the target takes none from the
configuration file. `occamyd` asks the owner for the target's
credentials on the session's stream, and doesn't keep them.

`occamyd` refuses the target with `400 bad_request` when:

- the protocol is unknown, or is `kubernetes`;
- the target doesn't parse;
- an ssh target has neither or both of `host_keys` and
  `insecure_ignore_host_key`;
- a host key doesn't parse;
- a vnc or rdp target has host key fields.

It refuses the target with `403 forbidden` when `adhoc_allowlist`
doesn't allow it. `occamyd` resolves a host name within 5 seconds,
checks every address against the list, and dials the first address
that it checked. It refuses these addresses unless an entry of the list
is that very address:

- loopback, link-local, unspecified and multicast addresses;
- the clouds' metadata services;
- `occamyd`'s own listeners.

An ad-hoc target uses `occamyd`'s SSH, VNC or RDP backend. A vnc target
checks the server's certificate if the server offers VeNCrypt, and an
rdp target always does. Both check it against the system's roots and
the host name that the request gave, or its address if it gave an
address. An rdp target logs on with NLA alone, so a server without
NLA, such as xrdp, needs a connection with `security: tls`. A session
on an ad-hoc target has no `connection` in its state.

## Streams

A stream is a WebSocket that carries one session in protocol v1. There
are two stream routes:

- [A connection's stream](#get-apiv1connectionsidstream) opens a new
  session and streams it, in one request.
- [A session's stream](#get-apiv1sessionsidstream) joins a running
  session.

Both take the token, `Origin` and `hello` in the same way. Both refuse
a request after the upgrade, with protocol v1's `close`, because a
browser can't read the HTTP status of a refused upgrade. Only a request
that can't become a stream of protocol v1 at all gets an HTTP error.
After the upgrade, the stream follows [Protocol](protocol.md), from
`welcome` and the first frame to `close`:

```mermaid
flowchart LR
  R["A stream<br/>request"] --> S{"Offers<br/>occamy.v1?"}
  S -- no --> H["400 bad_request,<br/>no upgrade"]
  S -- yes --> U["101 Switching<br/>Protocols"]
  U --> C{"Origin, token,<br/>hello and route<br/>let it in?"}
  C -- no --> X["close, with a<br/>code and a reason"]
  C -- yes --> W["welcome, then<br/>the first frame"]
```

### The upgrade request

A stream request is a WebSocket upgrade: a `GET` with `Upgrade:
websocket` that offers the subprotocol `occamy.v1` in
`Sec-WebSocket-Protocol`. It carries three more things:

- **The token.** A browser offers the subprotocol `occamy.v1.token.`,
  followed by the token in base64url without padding, beside
  `occamy.v1`. A native client may instead offer `occamy.v1` alone and
  send `Authorization: Bearer`. A token in the subprotocol wins over the
  header, so a bad one is refused even beside a good header. A token in
  the query string is ignored.
- **The `hello`.** The query parameter `hello` holds the whole `hello`
  message in base64url without padding, at most 2048 bytes before
  encoding ([Protocol](protocol.md#the-upgrade-request)).
- **`Origin`**, which a browser adds, as described below.

This is a browser's request:

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
comes from no browser. It takes a request with `Origin` only if the
request has exactly one, `http` or `https` with a host, and either:

- its host is the request's `Host`, and its scheme is `https` when
  `occamyd` terminates TLS; or
- its `scheme://host[:port]` is in `origins`.

`occamyd` refuses `null`, an empty `Origin` and a repeated one. Behind
a proxy that rewrites `Host`, list `occamyd`'s own origin in `origins`.

The stream examples below send the `hello` of a web client, the one in
the request above:

```sh
HELLO=AQEAAAEAgAc4BAAAAAABDgAAAG9jY2FteS13ZWIvMS4wBQABAAUAAAACBAMBBgIAAgAAAAAQAwABAAAAAQQAAgAAAMgABQACAAAAAAQ
```

curl doesn't decode the WebSocket here, so the examples print the
stream's first bytes in hexadecimal, and stop after two seconds. A
stream that `occamyd` lets in starts with `welcome`, in which you can
read the session's ID. A refused stream shows its `close` instead, with
the reason as text.

### Refusals

`occamyd` checks a stream request in this order: the subprotocol, then
`Origin`, whether a token is present, and the `hello`. Then it
upgrades, and the route verifies the token and opens or joins the
session. Only the first two rows answer in HTTP; the others answer
after the upgrade.

| Check | Answer |
|-------|--------|
| The request offers no `occamy.v1` | `400 bad_request`, as JSON, with no upgrade |
| The request isn't a valid WebSocket upgrade | The WebSocket library's `426` or `400`, as plain text |
| `Origin` | `close` 4001 `forbidden`, "origin not allowed" |
| No token | `close` 4000 `unauthorized`, "a token is required" |
| More than one token subprotocol, an empty one, or one that isn't base64url | `close` 4000 `unauthorized`, "invalid token subprotocol" |
| No `hello`, or one that doesn't decode | `close` 1002 `protocol-error`, "invalid hello" |
| A `hello` over 2048 bytes | `close` 1009 `too-large`, "invalid hello" |
| A `hello` of another protocol version | `close` 1003 `unsupported`, "unsupported protocol version" |
| A token that fails verification | `close` 4000 `unauthorized`, with "token expired", "invalid token", or "the token's subject is a static user's name" |

Each route's own refusals follow in its section. A refused stream gets
the upgrade, then `close` with the code and a reason, then a WebSocket
close with the same code. `occamyd` drops a client that doesn't
complete the closing handshake within a second. Until the route has
let a client in, `occamyd` reads no message over 64 KiB.
[Protocol](protocol.md#close-codes) lists the close codes.

### `GET /api/v1/connections/{id}/stream`

Opens a session on connection `{id}`, owned by the caller, and streams
it to the caller as its owner. `occamyd` sends `welcome` and the first
frame right after the upgrade, without waiting for a message from the
client.

**Who may call it.** A user whom the connection's `allow` names, by name
or by group, with an access token. A session token or a share is
refused.

**Request.** A [stream upgrade](#the-upgrade-request) with an access
token. The owner's `hello` sizes the session: a terminal's columns and
rows, or a desktop's pixels where the backend can ask the remote for
them.

**Response.** `101 Switching Protocols`, then protocol v1. The
session's ID is in `welcome`.

**Errors.** Besides the [refusals](#refusals) of every stream:

| Refusal | `close` |
|---------|---------|
| A session token, a share, or any token that isn't a valid access token | 4000 `unauthorized` |
| No connection `{id}` that the caller may open | 4001 `forbidden`, "no such connection" |
| The caller owns `limits.sessions_per_user` sessions | 4001 `forbidden`, "too many sessions open" |
| `occamyd` runs `limits.sessions` sessions | 4001 `forbidden`, "occamyd runs as many sessions as it takes" |
| `occamyd` couldn't open the session, for a cause of its own | 1011 `internal-error` |

**Example.**

```sh
curl -s --http1.1 --max-time 2 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Sec-WebSocket-Protocol: occamy.v1' -H "Authorization: Bearer $TOKEN" \
  "$OCCAMY/api/v1/connections/build-box/stream?hello=$HELLO" | xxd | head -n 4
```

**Notes.**

- A remote that fails at once can end the session before `welcome`,
  such as one whose certificate isn't the pinned one, or a Kubernetes
  API server that refuses the exec. The stream then gets `close` 4006
  `backend-failed` without `welcome`: the same `close` that it would
  have had after `welcome` ([Protocol](protocol.md#the-response)).
- Once the stream has joined, the session behaves like one that
  `POST /api/v1/sessions` opened. Its owner may leave and come back
  within the owner grace through
  [`GET /api/v1/sessions/{id}/stream`](#get-apiv1sessionsidstream), with
  an access token, and may read, share and close it through the session
  routes.

### `GET /api/v1/sessions/{id}/stream`

Joins running session `{id}`. If the session ended within the owner
grace, it tells the owner how it ended.

**Who may call it.** The session's owner, with an access token; anyone
with a session token for the session, in the token's role; and anyone
with a share of the session, in the share's role.

**Request.** A [stream upgrade](#the-upgrade-request) with one of these
tokens:

| Token | Joins as |
|-------|----------|
| An access token of the session's owner | `owner` |
| A session token whose `sid` is `{id}`: the stream token, or a trusted issuer's | Its `role`. `owner` only if its user is the session's owner |
| A share of `{id}` | The share's role, `viewer` or `read-only` |

`occamyd` takes a token without dots for a share, and any other token
for a JWT: an access token, or else a session token. A trusted issuer's
session token, of any role, joins only a session that one of the
issuer's own users owns. With the issuer's `link_static_users`, it may
also join a static user's.

**Response.** `101 Switching Protocols`, then protocol v1. The owner's
`hello` resizes the session, and a viewer's resizes nothing.

**Errors.** Besides the [refusals](#refusals) of every stream:

| Refusal | `close` |
|---------|---------|
| A share that is unknown, expired, of another session, or whose session has ended | 4000 `unauthorized`, "invalid token" |
| A JWT that fails verification | 4000 `unauthorized` |
| No running session `{id}`, and no ended one whose owner this is, as described below | 4001 `forbidden`, "no such session" |
| A session token for another session, or from an issuer that may not join this one | 4001 `forbidden`, "no such session" |
| An access token or `owner` session token of anyone but the owner | 4001 `forbidden`, "no such session" |
| A `viewer` or `read-only` session token of a session that has ended | 4001 `forbidden`, "no such session" |
| The owner's older stream didn't leave within 5 seconds of a newer one | 4001 `forbidden`, "the owner is connected already" |
| A viewer beyond `limits.viewers_per_session` | 4007 `viewer-limit` |

**Example.** This joins with the share that
[`POST /api/v1/sessions/{id}/shares`](#post-apiv1sessionsidshares)
minted, in `SHARE`:

```sh
curl -s --http1.1 --max-time 2 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Sec-WebSocket-Protocol: occamy.v1' -H "Authorization: Bearer $SHARE" \
  "$OCCAMY/api/v1/sessions/$SESSION/stream?hello=$HELLO" | xxd | head -n 4
```

**Notes.**

- **After the session ends.** For the owner grace, 60 seconds, after a
  session ends, its owner's stream gets the `close` that the session
  ended with, in place of `welcome`. Examples are 4006 `backend-failed`
  for a remote that failed before the stream came, as after
  [`POST /api/v1/sessions`](#post-apiv1sessions), and 4002
  `session-closed` after
  [`DELETE /api/v1/sessions/{id}`](#delete-apiv1sessionsid). The owner
  must use an access token or an `owner` session token. Anyone else
  gets the refusal for a session that never existed, so nobody else
  learns whether a session existed or how it ended.
- **Ended sessions kept.** `occamyd` keeps the owner and the `close` of
  as many ended sessions as `limits.sessions`, and at least 1024. It
  forgets the oldest first, so a client that opens sessions whose
  remotes fail can't grow them without bound. An owner whose session was
  forgotten gets "no such session" too.
- **Takeover.** A newer stream of the owner takes over from an older
  one, which gets `close` 1000 `normal` with "the owner connected again".
  Otherwise a connection that died without `occamyd` noticing would keep
  the owner out of their own session. The reason is fixed, so a client
  can tell a takeover from its own close. The web client says that the
  session was opened elsewhere, and offers to join it again.

## Web client

### `GET /static/`

Serves the web client from the directory that `web_client` names, as
`make client` builds it in `client/occamy-web/dist`.

**Who may call it.** A browser, without a token. The page signs in
through API v1 on this origin, since `occamyd` answers no CORS
preflight. Served only with `web_client`; without it, `/static/` is a
plain-text `404`. `GET /static` redirects here.

**Request.** No body.

**Response.** The file under the directory, with the type that its
extension names, or the `index.html` of a directory. Every response has
these headers:

| Header | Value |
|--------|-------|
| `Cache-Control` | `public, max-age=31536000, immutable` for a file served from `assets/`. `no-cache` for anything else, a missing asset too |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Security-Policy` | The policy below |
| `Referrer-Policy` | `no-referrer` |

The files in `assets/` are named by a hash of their content, so a new
build names new files. The browser revalidates the page itself on every
load, so a page of an earlier build never asks for scripts that a newer
build replaced.

The Content-Security-Policy, for a request to `host`, is this:

```text
default-src 'self'; script-src 'self'; worker-src 'self';
connect-src 'self' ws://host wss://host; img-src 'self' data:;
style-src 'self' 'unsafe-inline'; font-src 'self'; object-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

It allows what the page does, and nothing more:

- The page runs its own scripts, and the Worker that decodes tiles.
- It calls API v1 and opens streams on its own origin. `ws://host` and
  `wss://host` name that origin for browsers that don't count WebSockets
  as `'self'`.
- It shows the remote's pointer as a `data:` image.
- It takes inline styles, since the terminal emulator, xterm.js, adds
  `<style>` elements for its theme and cell size. React sets style
  attributes through the CSSOM, which the policy doesn't govern.
- No other page may frame it, so none can lay itself over a remote
  desktop and take its clicks.

`occamyd` leaves a `Host` that isn't a plain name or address, with an
optional port, out of `connect-src`, so a request can't add to the
policy.

**Errors.** A plain-text `404` for anything else: a missing file, a
directory without an `index.html`, which is never listed, and a
symbolic link that leads out of the directory, which isn't followed. A
link within the directory is followed.

**Example.** This prints the headers of the page:

```sh
curl -sI "$OCCAMY/static/"
```

**Notes.** `occamyd` opens the directory for each request. It warns at
start when the directory doesn't exist or has no `index.html`, and
serves the client once it's built there, without a restart.

## The debug listener

With `debug.address` set, `occamyd` opens a second listener that serves
expvar and pprof, and nothing else. Without it, these routes are served
nowhere. The public listener never serves them.

> [!WARNING]
> The debug listener has no authentication, TLS, body limit or write
> timeout, since a CPU profile takes as long as asked. Keep it on the
> loopback interface. `occamyd` warns at start when it isn't.

Its requests are logged as the public listener's are.
[Debugging](debugging.md#profiling-occamyd) shows how to read the
profiles. The examples here assume `debug.address: localhost:6060`:

```sh
DEBUG=http://localhost:6060
```

The routes below take no token, and anyone who can reach the debug
listener may call them.

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
| `fds` | The file descriptors open, counted in `/proc/self/fd` on Linux or `/dev/fd` on macOS. -1 where `occamyd` can't count them |
| `tcp_congestion` | The TCP congestion control of the public listener's connections ([Configuration](configuration.md#tcp-congestion-control)). Empty where `occamyd` can't tell |

**Example.**

```sh
curl -s "$DEBUG/debug/vars" | jq '{goroutines, fds}'
```

### `GET /debug/pprof/`

Returns pprof's index, as HTML. Under it are the named profiles, such
as `/debug/pprof/goroutine`, `/debug/pprof/heap`, `/debug/pprof/allocs`
and `/debug/pprof/threadcreate`. Each comes as a profile for
`go tool pprof`, or as text with `?debug=1` or `?debug=2`.
`/debug/pprof` without the slash redirects here.

**Example.** This prints the goroutines, grouped by stack:

```sh
curl -s "$DEBUG/debug/pprof/goroutine?debug=1"
```

### `GET /debug/pprof/cmdline`

Returns `occamyd`'s command line as plain text, its arguments separated
by NUL bytes.

**Example.**

```sh
curl -s "$DEBUG/debug/pprof/cmdline" | tr '\0' ' '
```

### `GET /debug/pprof/profile`

Returns a CPU profile over `seconds` seconds, 30 by default, for
`go tool pprof`.

**Example.** This samples the CPU for 10 seconds, and prints the
functions that took the most of it:

```sh
curl -s -o cpu.pprof "$DEBUG/debug/pprof/profile?seconds=10"
go tool pprof -top cpu.pprof
```

### `GET /debug/pprof/symbol`

Returns the number of symbols that the binary has, as plain text:
`num_symbols: 1` when it has any.

**Example.**

```sh
curl -s "$DEBUG/debug/pprof/symbol"
```

### `POST /debug/pprof/symbol`

Looks up the program counters in the body, in hexadecimal and separated
by `+`, and answers each with its function's name, as `go tool pprof`
asks for them.

**Example.** This looks up the first program counter of a goroutine
profile:

```sh
PC=$(curl -s "$DEBUG/debug/pprof/goroutine?debug=1" | awk '/^#/ {print $2; exit}')
curl -s -d "$PC" "$DEBUG/debug/pprof/symbol"
```

### `GET /debug/pprof/trace`

Returns an execution trace over `seconds` seconds, 1 by default, for
`go tool trace`.

**Example.**

```sh
curl -s -o trace.out "$DEBUG/debug/pprof/trace?seconds=5"
go tool trace trace.out
```

## Design

[The API and server stack](../specs/008-native-architecture/api-and-server-stack.md)
records how the API was designed: the options weighed and the decisions
made, such as tokens out of URLs, refusals as `close` after the
upgrade, and absent rather than forbidden.
