# Configuration

`occamyd` reads one YAML file, `./conf.yaml` unless `-conf` names
another. The file comes in two versions:

- **v0** has no `version` key. It is the format of the released v0.x
  servers and of the [conf.yaml](../conf.yaml) in this repository.
  `occamyd` serves it as before (see [v0 files](#v0-files)).
- **v1** starts with `version: 1`. It configures the API v1 server of
  [api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md):
  sign-in, the connection registry, TLS and the debug listener. The
  server is being built: so far it serves `/healthz`, sign-in for static
  users, the connections and sessions of API v1 and their streams and,
  with `legacy_v0`, the v0 routes (see
  [Serving a v1 file](#serving-a-v1-file)).

Any other `version` is an error, and so is `version: 0`: a v0 file has no
`version` key. A `version` brought in by a merge key (`<<`) counts, and a
`version` key in any YAML document but the first is an error.

## A v1 file

A v1 file holds no secrets. A password or key is named by the file or the
environment variable that holds it, so the configuration can be committed
and reviewed. A fuller example, with a comment on every key, is
[internal/config/testdata/v1.yaml](../internal/config/testdata/v1.yaml).

```yaml
version: 1
listen: 0.0.0.0:5636
tls: {cert: /etc/occamy/tls.crt, key: /etc/occamy/tls.key}
origins: [https://occamy.example.com]
auth:
  signing_key_file: /var/lib/occamy/signing.key
  users:
    - name: alice
      password_hash: "$2a$12$R6bvkO3lC3JV/OaHnySvquk.wobMPB3XleIdoBgEwlhjbcfaZi0km"
      groups: [ops]
connections:
  - id: build-box
    name: Build box
    protocol: ssh
    target: build.internal
    host_keys: ["SHA256:UtDMff7ThPohflGs6UL1iuiyvVrazbxQT9x0OtzIV7U"]
    credentials: {username: deploy, private_key_file: /etc/occamy/keys/deploy}
    allow: {groups: [ops]}
  - id: nas-desktop
    protocol: vnc
    target: nas.internal:5900
    credentials: {password_env: OCCAMY_NAS_VNC_PASSWORD}
    allow: {users: [alice]}
```

Unknown keys are errors, so a misspelt key is reported instead of being
ignored. Relative paths are resolved against `occamyd`'s working
directory.

### Server

| Key | Default | Meaning |
|-----|---------|---------|
| `version` | required | `1` |
| `listen` | `:5636` | Address of the public listener, `host:port`: the API, the streams and `/healthz` |
| `tls.cert`, `tls.key` | unset | Certificate and key files, PEM. `occamyd` terminates TLS when both are set; set both or neither. Without them, put a reverse proxy that terminates TLS in front. Read at start, so a renewed certificate needs a restart |
| `debug.address` | unset (off) | Address of the debug listener: expvar at `/debug/vars` and pprof under `/debug/pprof/`, without authentication. It needs a port of its own: not `listen`'s port on the same host, nor on `localhost` beside `127.0.0.1` or `::1`, nor beside a wildcard host (`:5636`, `0.0.0.0`, `::`), which takes every address. Keep it on the loopback interface; `occamyd` warns at start when it is not |
| `origins` | none | Origins, `scheme://host[:port]`, whose web pages may open streams besides `occamyd`'s own. Written in lower case without a trailing slash, as browsers send them. `occamyd`'s own origin is any whose host and port are the request's `Host`, and whose scheme is `https` when `occamyd` terminates TLS. Behind a proxy that rewrites `Host`, list the origin of `occamyd`'s own pages here too. A stream request without `Origin`, which no browser sends, is accepted on its token alone |
| `legacy_v0` | `false` | Serve the v0 routes, `/api/v0/login` and `/api/v0/connect`, for the Guacamole-based web client. Their tokens are signed with a key made at each start, so they do not survive a restart. The web client itself (`/static/`) is not served from a v1 file. The login signs any target for anyone, as in v0, and `occamyd` warns at start; `allow_adhoc_targets`, `adhoc_allowlist` and the connection registry do not restrict these routes. They are removed at v1.0 |

### Sign-in: `auth`

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.signing_key_file` | required | The Ed25519 key, PKCS #8 PEM, that signs `occamyd`'s tokens. If the file does not exist, `occamyd` creates it on first start, so its directory must exist |
| `auth.access_token_lifetime` | `15m` | How long an access token lasts, a duration with a unit (`15m`, `1h30m`) between `1m` and `12h` |
| `auth.trusted_issuers[].issuer` | required | Name of a third party, such as a gateway, whose tokens `occamyd` accepts. Unique, and not `occamy`, which is `occamyd`'s own. See [tokens from a trusted issuer](#tokens-from-a-trusted-issuer) |
| `auth.trusted_issuers[].public_key_file` | required | That issuer's Ed25519 public key, PKIX PEM |
| `auth.trusted_issuers[].link_static_users` | `false` | Lets the issuer's tokens act for a static user by naming them in `sub`. Off, such tokens are refused: see [tokens from a trusted issuer](#tokens-from-a-trusted-issuer) |
| `auth.users[].name` | required | A static user, who signs in with a password. Unique |
| `auth.users[].password_hash` | required | bcrypt hash of the password, cost 10 or more. `htpasswd -nbBC 12 "" 'the password' \| cut -d: -f2` makes one. Give every user the same cost: a sign-in as an unknown user is checked against a hash of the highest cost, so a user at a lower cost signs in measurably faster, and `occamyd` warns at start when the costs differ |
| `auth.users[].groups` | none | Groups the user belongs to |
| `auth.oidc` | unset (off) | Sign-in through an OpenID Connect identity provider |
| `auth.oidc.issuer` | required | The provider's issuer URL. https, or http on localhost |
| `auth.oidc.client_id` | required | `occamyd`'s client ID at the provider |
| `auth.oidc.client_secret_file`, `client_secret_env` | unset | The client secret, from a file or an environment variable; at most one. Neither means a public client |
| `auth.oidc.redirect_url` | required | `occamyd`'s callback URL as the provider knows it, ending in `/api/v1/auth/oidc/callback` behind any proxy prefix. https, or http on localhost |
| `auth.oidc.scopes` | `[openid, profile, email]` | Scopes to request; must include `openid` |
| `auth.oidc.subject_claim` | `sub` | The ID token claim that names the user in allow lists |
| `auth.oidc.groups_claim` | unset | The claim that lists the user's groups. Unset, OIDC users have no groups |

#### Tokens from a trusted issuer

A trusted issuer mints JWTs that `occamyd` accepts as its own:

- signed with the issuer's Ed25519 key (`alg: EdDSA`; no other algorithm
  is accepted);
- `iss` is the issuer's name as configured, and `aud` is `occamy`;
- `iat` and `exp` are set, at most 12 hours apart;
- `kind` is `access`, with the user in `sub` and optionally `groups`, a
  list of group names; or `session`, with `sid` (a session ID) and
  `role` (`owner`, `viewer` or `read-only`).

`occamyd`'s own tokens have the same shape with `iss: occamy`.

An issuer's users are its own. A session belongs to the issuer and the
`sub` of the token that opened it, so the gateway's `carol` and
`occamyd`'s `carol` are two users, and neither reads, closes, shares or
streams as the owner the other's session. The stream token that
`POST /api/v1/sessions` returns for a trusted issuer's user names that
issuer in a `uiss` claim; no trusted issuer may set `uiss` itself. A
trusted issuer's token whose `sub` names a static user is refused with
`invalid_token` (on a stream, `close` with `unauthorized`), unless the
issuer has `link_static_users: true`, in which case it acts as that
static user. Connections' `allow.users` and `allow.groups` name users
and groups whichever issuer signed the token.

### guacd: `guacd`

guacd serves the `rdp` and `telnet` connections. `ssh` and `vnc`
connections use `occamyd`'s native backends, and no key picks guacd for
them yet. For `rdp`, `occamyd` passes `disable-gfx: "true"` unless
`guacd.params.rdp` sets it: through RDP's graphics pipeline, xrdp's
colours reached guacd shifted.

| Key | Default | Meaning |
|-----|---------|---------|
| `guacd.address` | `localhost:4822` | guacd's address, `host:port` |
| `guacd.params.<protocol>` | none | guacd connection parameters per protocol (`ssh`, `vnc`, `rdp`, `telnet`), such as `ignore-cert: "true"` for `rdp`. A connection's target and credentials override them. `hostname` and `port` come from the target, and secrets are not allowed here: any parameter whose name holds `password`, `passphrase`, `private-key` or `secret`, such as `sftp-password`, is refused. Set credentials on each connection instead. An ad-hoc target takes only the display's parameters (see [ad-hoc targets](#ad-hoc-targets)) |

### The connection registry: `connections`

| Key | Default | Meaning |
|-----|---------|---------|
| `connections[].id` | required | The connection's ID, used in URLs and tokens: 1 to 64 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or digit. Unique |
| `connections[].name` | the ID | Display name |
| `connections[].protocol` | required | `ssh`, `vnc`, `rdp` or `telnet` |
| `connections[].target` | required | `host:port`, or a host alone for the protocol's default port: 22 for ssh, 5900 for vnc, 3389 for rdp, 23 for telnet. IPv6 addresses go in brackets with a port, `[fd00::22]:22` |
| `connections[].host_keys` | required for ssh | The host keys an ssh target may present: public keys in `authorized_keys` format, as `ssh-keyscan` prints them, or SHA-256 fingerprints, as `ssh-keygen -l` prints them (`SHA256:…`). `occamyd` refuses any other key. Not for other protocols |
| `connections[].insecure_ignore_host_key` | `false` | Accept any host key from an ssh target instead, logging its fingerprint as a warning. For demos only; set this or `host_keys`, not both |
| `connections[].credentials` | required | How `occamyd` authenticates to the target; see below |
| `connections[].allow.users`, `allow.groups` | required | Who may open the connection: users by name, and members of groups. At least one entry, since an empty list would let nobody in |

`credentials` holds an optional `username` and exactly one source:

| Key | Meaning |
|-----|---------|
| `password_file` | A file holding the password. Trailing line breaks are dropped |
| `password_env` | An environment variable holding the password. It must be set and not empty |
| `private_key_file` | An SSH private key file, ssh only. Keep it readable by the `occamyd` user only |
| `prompt: true` | Nothing stored: the session's owner is asked for whatever the target requires, and the answers are not kept. Use it also for a target that needs no credentials |

An ssh connection needs a `username` unless it prompts. A secret written
inline (`password:` under `credentials`, `client_secret:` under `oidc`) is
rejected with the keys to use instead.

Secrets are read when they are used, not when the file is loaded:
validation only checks that each file can be opened and each variable is
set. A secret rotated on disk is therefore picked up by the next session.

### Ad-hoc targets

| Key | Default | Meaning |
|-----|---------|---------|
| `allow_adhoc_targets` | `false` | Let signed-in users connect to targets that are not in the registry, as v0 does |
| `adhoc_allowlist` | none | Where ad-hoc targets may point, and on which ports: CIDR ranges (`10.0.0.0/8`), IP addresses, host names, or `*.` and a domain (`*.lab.internal`), each optionally followed by `:port` or `:first-last` (`10.0.0.0/8:22`, `*.lab.internal:5900-5910`; an IPv6 range or address goes in brackets, `[fd00::/8]:22`). Required when `allow_adhoc_targets` is `true` |

Any signed-in user may open an ad-hoc target, with
`POST /api/v1/sessions`. Its credentials are always asked of the user,
and an ssh target needs `host_keys` or `insecure_ignore_host_key: true`
in the request, as a connection does. An entry without ports allows the
protocol's own: 22 for ssh, 5900 to 5999 for vnc, 3389 for rdp and 23
for telnet. A target is allowed on a port that an entry allows when:

- it is an IP address in a listed range, or a listed address;
- it is a listed host name, or ends in a `*.` entry's domain
  (`a.lab.internal` for `*.lab.internal`, but not `lab.internal`
  itself);
- it is any other host name, every address of which lies in a listed
  range.

`occamyd` resolves every host name and dials the first address it
checked, so a second lookup cannot lead elsewhere. Loopback, link-local
(such as a cloud's metadata service at `169.254.169.254`), unspecified
and multicast addresses are refused, whatever range or listed name leads
to them, unless an entry is that very address (`127.0.0.1:2222`).

An ad-hoc target passes no `guacd.params` but the display's
(`color-depth`, `cursor`, `disable-gfx`, `enable-font-smoothing`,
`font-name`, `font-size`, `color-scheme`, `resize-method`), so that
parameters meant for the configured connections, such as an SFTP host,
do not reach a target that a user chose.

### Limits: `limits`

| Key | Default | Meaning |
|-----|---------|---------|
| `limits.sessions_per_user` | `10` | Sessions one user owns at once. One more is refused with `429 limit_exceeded`, or on a connection's stream with `close` `forbidden` |
| `limits.viewers_per_session` | `50` | Viewers a session takes besides its owner, who can always join. One more gets `close` `viewer-limit` |
| `limits.shares_per_session` | `100` | Shares of a session that have not expired. One more is refused with `429 limit_exceeded` |

A user is a subject of an issuer, as for owning sessions: a trusted
issuer's users count apart from `occamyd`'s own.

## Validation

A v1 file is checked as a whole, and every problem is reported with its
line and key path:

```
config: 3 problems:
	line 7: conections: unknown key, did you mean connections?
	line 9: connections[0].credentials: no credential source: set one of password_file, password_env, private_key_file or prompt: true
	line 13: connections[0].allow.users[0]: no static user is named "carol"
```

Beyond the types and the rules in the tables above, validation checks
that:

- every file the configuration names exists, is a regular file and can be
  opened; a trusted issuer's public key also parses;
- every environment variable it names is set and not empty;
- addresses are `host:port` with a numeric port, and origins and OIDC URLs
  parse;
- `debug.address` does not share a port with `listen` (see the table
  above);
- connection IDs, user names and issuer names are unique, and no trusted
  issuer is named `occamy`;
- user, group and issuer names are not empty and have no spaces;
- with neither `auth.oidc` nor `auth.trusted_issuers` configured, every
  name in an `allow` list is a static user, and every group is a group of
  some static user. With either one configured, users and groups also come
  from token claims, so names the file does not list are accepted;
- a file holds one YAML document.

## Serving a v1 file

`occamyd` serves a v1 file on `listen`, over TLS when `tls.cert` and
`tls.key` are set, and speaks HTTP/1.1 only, since the streams are
WebSockets over HTTP/1.1. So far it serves:

| Route | Meaning |
|-------|---------|
| `GET /healthz` | `200 {"status":"ok"}` while `occamyd` runs |
| `POST /api/v1/auth/token` | Sign-in for static users: `{"username":"alice","password":"…"}` as `application/json` in, `{"access_token":"…","token_type":"Bearer","expires_in":900}` out. A wrong password and an unknown user get the same `401` |
| `GET /api/v1/connections` | The connections whose `allow` names the caller or one of the caller's groups: `{"connections":[{"id":"build-box","name":"Build box","protocol":"ssh"}],"adhoc_targets":false}` |
| `POST /api/v1/sessions` | Open a session: `{"connection":"build-box"}`, or with ad-hoc targets on, `{"protocol":"ssh","target":"10.0.0.5","host_keys":["SHA256:…"]}`. `201` with `{"id":"…","role":"owner","stream_token":"…","expires_in":300}`; the owner has 60 seconds to open the session's stream |
| `GET /api/v1/sessions/{id}` | The owner's view of the session: connection, protocol, backend, class, size and viewers |
| `DELETE /api/v1/sessions/{id}` | Close the session, owner only; every stream of it ends |
| `POST /api/v1/sessions/{id}/shares` | A share for `{"role":"viewer"}` or `"read-only"`, with `expires_in` in seconds (60 to 43200, an hour by default), owner only |
| `GET /api/v1/connections/{id}/stream` | Open a session on the connection and stream it, as a protocol v1 WebSocket ([protocol.md](protocol.md)) |
| `GET /api/v1/sessions/{id}/stream` | Join a session as a protocol v1 WebSocket: as its owner with the owner's access token or the stream token, with the role of a session token for the session, which a trusted issuer may mint too, or with a share |
| `/api/v0/login`, `/api/v0/connect` | With `legacy_v0: true`, the v0 routes as a v0 file serves them |

`ssh` and `vnc` sessions use `occamyd`'s native backends, and `rdp` and
`telnet` sessions go through guacd. A connection the caller may not
open, and another user's session, answer `404` as if they did not exist.

API v1 answers every error with `{"error":{"code":"…","message":"…"}}`,
where `code` is stable, such as `unauthorized`, `invalid_token`,
`token_expired`, `forbidden`, `not_found` or `limit_exceeded`, and
`message` is for people. Routes that need a caller take the access token in
`Authorization: Bearer`, never in the URL. A stream takes it in the
`occamy.v1.token.` subprotocol instead, which a browser can send, or in
`Authorization`, and refuses a bad token, `Origin` or `hello` with a
protocol `close` after the upgrade. The request log names each
request's method, path, status, duration and peer, never its query
string, headers or body.

On SIGINT or SIGTERM, `occamyd` stops accepting connections, ends every
session, which sends its streams `close` with `server-shutdown`, and
gives requests in flight up to 5 seconds to finish, and streams as long
again. It then cuts what is left, such as a long CPU profile, with a
warning, and exits with status 0. A
second SIGINT or SIGTERM in those seconds ends it at once. If the signing key file
does not exist, `occamyd` creates it and warns: tokens signed with an
earlier key no longer verify.

## v0 files

A file without a `version` key is read as before, ignoring keys it does
not know:

| Key | Default | Meaning |
|-----|---------|---------|
| `address` | unset | Listen address |
| `mode` | `debug` | gin mode: `debug`, `release` or `test`. `debug` also serves `/debug/pprof` on the public listener |
| `auth.jwt_secret`, `auth.jwt_alg` | unset | The HMAC secret and algorithm of v0 tokens |
| `client` | `false` | Serve the demo web client |
| `guacd.address`, `guacd.params` | `localhost:4822`, none | As in v1; token values override the parameters |

A v0 file used as a v1 file reports the v0 keys it finds, with their v1
equivalents where there are any.
