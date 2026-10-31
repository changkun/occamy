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
  users and through OIDC, the connections and sessions of API v1 and their streams and,
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
| `auth.oidc` | unset (off) | Sign-in through an OpenID Connect identity provider; see [signing in through OIDC](#signing-in-through-oidc) |
| `auth.oidc.issuer` | required | The provider's issuer URL, exactly as its discovery document names it (a trailing slash counts). https, or http on localhost |
| `auth.oidc.client_id` | required | `occamyd`'s client ID at the provider |
| `auth.oidc.client_secret_file`, `client_secret_env` | unset | The client secret, from a file or an environment variable; at most one. Neither means a public client |
| `auth.oidc.redirect_url` | required | `occamyd`'s callback URL as the browser and the provider know it, ending in `/api/v1/auth/oidc/callback` behind any proxy prefix. https, or http on localhost. Its path is also the sign-in cookie's, and with https the cookie is `Secure`, whoever terminates TLS |
| `auth.oidc.return_urls` | the origin of `redirect_url`, with the path `/` | Where browsers are sent back after a sign-in, with a one-time code or an error in the fragment: your web client's sign-in page. A sign-in names one of them, matched exactly as written. https, or http on localhost, with no user name and no fragment |
| `auth.oidc.scopes` | `[openid, profile, email]` | Scopes to request; must include `openid` |
| `auth.oidc.subject_claim` | `sub` | The ID token claim that names the user in allow lists: a top-level string claim of up to 255 bytes, of printable characters that show, with no space but U+0020: no control or format characters (zero-width spaces, bidirectional overrides), no other characters that render as nothing (variation selectors, the combining grapheme joiner, Hangul fillers, the blank Braille pattern U+2800). See [user names](#user-names). Pick one that users cannot set themselves, such as `sub` or `email`, never a display name or `preferred_username` where users can edit it, since allow lists grant by this name. With `email`, the token must also say `email_verified: true` |
| `auth.oidc.groups_claim` | unset | The top-level claim that lists the user's groups, as a list of strings or one string. Only the groups the file names, in `auth.users[].groups` or an `allow.groups`, are kept. Unset, OIDC users have no groups |
| `auth.oidc.require_known_users` | `false` | Admit only the users whom an allow list names: in a connection's `allow.users`, exactly, or in one of its `allow.groups` by `groups_claim`. Static users, and names in `allow.users` that are a static user's, count only with `link_static_users`, since OIDC refuses those names without it. Off, anyone the provider signs in gets an access token, and allow lists still decide what they may open |
| `auth.oidc.link_static_users` | `false` | Let an OIDC user whose subject names a static user sign in under that name, so that the allow lists naming the static user name the OIDC user too. The OIDC user's groups still come from `groups_claim`, not from the static user's `groups`. Off, a subject that is a static user's name, in any case or Unicode normal form (`Alice` for `alice`), is refused with `access_denied` and logged: a name belongs to one source unless the file links them. Turn it on only with a `subject_claim` that users cannot choose, such as `sub` or a verified `email`: whoever the provider names `alice` then has static user `alice`'s place in every allow list |

#### Tokens from a trusted issuer

A trusted issuer mints JWTs that `occamyd` accepts as its own:

- signed with the issuer's Ed25519 key (`alg: EdDSA`; no other algorithm
  is accepted);
- `iss` is the issuer's name as configured, and `aud` is `occamy`;
- `iat` and `exp` are set, at most 12 hours apart;
- `kind` is `access`, with the user in `sub` and optionally `groups`, a
  list of group names; or `session`, with the user in `sub`, `sid` (a
  session ID) and `role` (`owner`, `viewer` or `read-only`). A session
  token of any role joins only a session that one of the issuer's own
  users opened, or, with `link_static_users`, a static user; an
  `owner` token only for that user.
- `sub` is a usable name, by the rules that `auth.oidc.subject_claim`
  gives. A token without one is refused, whoever signed it.

`occamyd`'s own tokens have the same shape with `iss: occamy`. An access
token may also carry `idp`, where the user signed in: `occamyd` puts
`local` on a static user's and `oidc` on one from
[OIDC](#signing-in-through-oidc), and logs it at each sign-in. The stream
token of one of `occamyd`'s own users carries the `idp` of the access
token that opened the session. A trusted issuer's session token that
carries `idp` is refused, and the `idp` of its access token is ignored.

#### User names

Allow lists and `require_known_users` compare user names exactly, byte
for byte: `allow.users: [alice]` admits neither `Alice` nor an `alice`
with a decomposed character, so write names as the provider sends them.
Telling OIDC users from static users casts a wider net: without
`link_static_users`, an OIDC subject is refused when it equals a static
user's name under Unicode case folding, once both are in NFC, so that
`Alice` cannot pass for static `alice` in a log or to a person. A
trusted issuer's `sub` is compared the same way, unless the issuer has
its own `link_static_users`. Names
that only look alike, such as one with a Cyrillic `а` or a fullwidth
`ａ`, are neither refused nor the same user; pick a `subject_claim` that
users cannot choose.

#### Signing in through OIDC

Register `occamyd` at the provider as a client of the authorization code
flow with PKCE, with `redirect_url` as its redirect URI. `occamyd`
discovers the provider in the background at start, or at the first
sign-in, so a provider that is down does not keep `occamyd` from
starting: OIDC sign-ins fail until it answers, a failed discovery is
tried again after 10 seconds, and static users sign in as ever. Each
request to the provider times out after 10 seconds.

A web client signs a user in in four steps:

1. Make a random verifier of 43 to 128 characters (`A-Z`, `a-z`, `0-9`,
   `-`, `.`, `_`, `~`), keep it in `sessionStorage`, and compute its
   challenge: the SHA-256 of the verifier in base64url without padding
   (PKCE's S256, RFC 7636).
2. Send the browser to
   `GET /api/v1/auth/oidc/login?return_url=<one of return_urls>&code_challenge=<challenge>`.
   `return_url` may be left out when only one is configured. `occamyd`
   sets a cookie for its callback and redirects to the provider.
3. Once the user has signed in, the provider sends the browser to the
   callback, which sends it on to the return URL with
   `#code=<one-time code>`, or `#error=<code>`.
4. On the return URL, read the fragment, remove it with
   `history.replaceState`, and post
   `{"code":"…","code_verifier":"<the verifier>"}` as `application/json`
   to `POST /api/v1/auth/oidc/token`. The answer is that of
   `POST /api/v1/auth/token`:
   `{"access_token":"…","token_type":"Bearer","expires_in":900}`. Then
   forget the verifier.

A client that finds a `#code` but holds no verifier did not start that
sign-in: someone may have sent the browser to `login` with a challenge
of their own. It must not sign in with the code. It should spend it, by
posting it with a throwaway verifier, since a code works once, or else
remove it from the URL and ignore it.

The access token never appears in a URL, so it reaches no server log,
`Referer` or browser history. The one-time code sits in the history, but
it lasts a minute, works once, and only with the verifier of the client
that started the sign-in, so a code planted in another browser signs no
one in. Until `occamyd` answers CORS preflights, serve the web client
from `occamyd`'s origin, such as through the reverse proxy in front of
both.

Errors in the fragment:

| `error` | Meaning |
|---------|---------|
| `access_denied` | The provider refused, or `occamyd` refused the user: a `subject_claim` that is missing or not a usable name, an email the provider has not verified, a static user's name, in any case or normal form, without `link_static_users`, or with `require_known_users`, a user whom no allow list names |
| `sign_in_failed` | The provider answered with another error, or its answer did not verify: the ID token's issuer, audience, authorized party (`azp`), expiry, signature, algorithm or nonce, the answer's `iss` (RFC 9207), also on an error answer, or a `sub` that is not a string, which the ID token's parser refuses before `subject_claim` is read |
| `unavailable` | The provider cannot be reached, or too many sign-ins wait for their clients |

`login` answers `400` with API v1's error for a return URL that is not
listed, or a challenge that is missing, given twice or not an S256
challenge (43 characters of unpadded base64url), or a
`code_challenge_method` other than `S256`. The callback answers `400`
with `invalid_state` when the browser has no sign-in cookie that matches
the provider's answer: the sign-in started in another browser, took
longer than 10 minutes, or outlived a restart of `occamyd`. It leaves a
sign-in that is under way alone, so a link to the callback from another
site cannot end it. Starting a sign-in can: `login` replaces the
browser's sign-in cookie, so a second `login`, including a top-level
navigation to it from another site, ends the sign-in under way, whose
callback then answers `invalid_state`. That is a nuisance, not a way
in, since the second sign-in's code needs a verifier the client does
not hold, and the user can start again. The exchange answers `401` for
a code that is unknown, used or expired, or a verifier that does not
match. A user has
at most three codes waiting for exchange; a fourth sign-in drops the
oldest.

ID tokens must be signed with RS, ES or PS at 256, 384 or 512 bits, or
EdDSA; `none` and HMAC are refused whatever the provider advertises.
`occamyd` follows no redirect from the provider, and talks only to the
endpoints its discovery document names, which must be https, or http on
localhost when the issuer is too. Their hosts are the discovery
document's to choose, since it comes from the issuer you configure:
`occamyd` checks only the scheme. It reads
`authorization_response_iss_parameter_supported` as a JSON bool; any
other value fails discovery. When the provider names itself in its
answer to the callback (`iss`, RFC 9207), `occamyd` compares it with the
issuer exactly, also in an error answer, before it takes the error as
the provider's. The sign-in cookie is
`__Secure-occamy_oidc` when it is `Secure`, and `occamy_oidc` on plain
http.
Several `occamyd` instances behind one name need sticky sessions, since
the sign-in cookie's key and the one-time codes live in each process.

An issuer's users are its own. A session belongs to the issuer and the
`sub` of the token that opened it, so the gateway's `carol` and
`occamyd`'s `carol` are two users, and neither reads, closes, shares or
streams as the owner the other's session. The stream token that
`POST /api/v1/sessions` returns for a trusted issuer's user names that
issuer in a `uiss` claim; no trusted issuer may set `uiss` itself. A
trusted issuer's token whose `sub` names a static user, in any case or
Unicode normal form (`Alice` for `alice`), is refused with
`invalid_token` (on a stream, `close` with `unauthorized`), unless the
issuer has `link_static_users: true`. Then a `sub` that is exactly a
static user's name acts as that static user, and one in another case
or form is the issuer's own user. A `sub` that is not a usable name,
by the rules that `auth.oidc.subject_claim` gives, is refused as well,
so that a trusted issuer and OIDC sign-in refuse the same names. Connections' `allow.users` and `allow.groups` name users
and groups whichever issuer signed the token.

Among `occamyd`'s own users, a session also belongs to where its owner
signed in: an OIDC user and a static user are two users. The exception
is a static user's name: with `auth.oidc.link_static_users`, the OIDC
user who signs in under it is the static user, and owns the same
sessions, whichever of the two opened them. Its access token still
says `idp: oidc`, for the log. A token of such a user, whose `sub` is a
static user's name in any case or normal form, is refused with
`invalid_token` once `link_static_users` is off. The OIDC user keeps
the groups of `groups_claim`, not the static user's.

### guacd: `guacd`

guacd serves the `rdp` and `telnet` connections. `ssh` and `vnc`
connections use `occamyd`'s native backends, and no key picks guacd for
them yet. For `rdp`, `occamyd` passes `disable-gfx: "true"` unless
`guacd.params.rdp` sets it: through RDP's graphics pipeline, xrdp's
colours reached guacd shifted.

| Key | Default | Meaning |
|-----|---------|---------|
| `guacd.address` | `localhost:4822` | guacd's address, `host:port` |
| `guacd.params.<protocol>` | none | guacd connection parameters per protocol (`ssh`, `vnc`, `rdp`, `telnet`), such as `ignore-cert: "true"` for `rdp`. A connection's target and credentials override them. `hostname` and `port` come from the target, and secrets are not allowed here: any parameter whose name holds `password`, `passphrase`, `private-key` or `secret`, in any case, such as `sftp-password`, is refused. A name that ends in `-regex`, such as telnet's `password-regex`, is a prompt's pattern and is allowed. Set credentials on each connection instead. An ad-hoc target takes only the display's parameters (see [ad-hoc targets](#ad-hoc-targets)) |

### The connection registry: `connections`

| Key | Default | Meaning |
|-----|---------|---------|
| `connections[].id` | required | The connection's ID, used in URLs and tokens: 1 to 64 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or digit. Unique |
| `connections[].name` | the ID | Display name |
| `connections[].protocol` | required | `ssh`, `vnc`, `rdp` or `telnet` |
| `connections[].target` | required | `host:port`, or a host alone for the protocol's default port: 22 for ssh, 5900 for vnc, 3389 for rdp, 23 for telnet. IPv6 addresses go in brackets with a port, `[fd00::22]:22` |
| `connections[].host_keys` | required for ssh | The host keys an ssh target may present: public keys in `authorized_keys` format, as `ssh-keyscan` prints them, or SHA-256 fingerprints, as `ssh-keygen -l` prints them (`SHA256:…`). `occamyd` refuses any other key. Not for other protocols |
| `connections[].insecure_ignore_host_key` | `false` | Accept any host key from an ssh target instead, logging its fingerprint as a warning. For demos only; set this or `host_keys`, not both |
| `connections[].tls` | system roots | How a vnc target's certificate is checked when the server offers VeNCrypt, which `occamyd` then always chooses. By default the certificate must chain to the system's roots and name the target's host, as a browser checks it. Not for other protocols; see below |
| `connections[].credentials` | required | How `occamyd` authenticates to the target; see below |
| `connections[].allow.users`, `allow.groups` | required | Who may open the connection: users by name, and members of groups. At least one entry, since an empty list would let nobody in |

`credentials` holds an optional `username` and exactly one source:

| Key | Meaning |
|-----|---------|
| `password_file` | A file holding the password. Trailing line breaks are dropped |
| `password_env` | An environment variable holding the password. It must be set and not empty |
| `private_key_file` | An SSH private key file, ssh only. Keep it readable by the `occamyd` user only |
| `prompt: true` | Nothing stored: the session's owner is asked for whatever the target requires, and the answers are not kept. Use it also for a target that needs no credentials |

An ssh connection needs a `username` unless it prompts. A vnc
connection's `username` is for VeNCrypt's Plain authentication, which
is then chosen over the server's other types; without one, the owner is
asked if the server wants it. A secret written
inline (`password:` under `credentials`, `client_secret:` under `oidc`) is
rejected with the keys to use instead.

Secrets are read when they are used, not when the file is loaded:
validation only checks that each file can be opened and each variable is
set. A secret rotated on disk is therefore picked up by the next session.

A vnc connection's `tls` takes:

| Key | Default | Meaning |
|-----|---------|---------|
| `require` | `false` | Refuse a server that does not offer VeNCrypt with a certificate, so that nobody on the way can strip VeNCrypt from what the server offers. `ca_file` and `pins` imply it |
| `server_name` | the target's host | The name the certificate must carry |
| `ca_file` | none | PEM certificates of the CAs that replace the system's roots, as for a private CA. Read when a session starts |
| `pins` | none | Certificates accepted as they are, as an ssh connection's `host_keys` are: their chain, names and dates are not checked. Each is a certificate in PEM or its SHA-256 fingerprint, in hex as `openssl x509 -noout -fingerprint -sha256` prints it or in base64 after `SHA256:`. Set this or `ca_file`, not both |
| `insecure_skip_verify` | `false` | Accept any certificate, logging its fingerprint as a warning. Set this or `ca_file` or `pins`, not both, as `host_keys` and `insecure_ignore_host_key` for ssh. For demos only |

A refused certificate fails the session, and `occamyd`'s log names the
certificate's fingerprint, ready for `pins`.

### Ad-hoc targets

| Key | Default | Meaning |
|-----|---------|---------|
| `allow_adhoc_targets` | `false` | Let signed-in users connect to targets that are not in the registry, as v0 does |
| `adhoc_allowlist` | none | Where ad-hoc targets may point, and on which ports: CIDR ranges (`10.0.0.0/8`), IP addresses, host names, or `*.` and a domain (`*.lab.internal`), each optionally followed by `:port` or `:first-last` (`10.0.0.0/8:22`, `*.lab.internal:5900-5910`; an IPv6 range or address goes in brackets, `[fd00::/8]:22`). Required when `allow_adhoc_targets` is `true` |

Any signed-in user may open an ad-hoc target, with
`POST /api/v1/sessions`. Its credentials are always asked of the user,
and an ssh target needs `host_keys` or `insecure_ignore_host_key: true`
in the request, as a connection does. A vnc target's certificate, if the
server offers VeNCrypt, must chain to the system's roots and carry the
name the target was given, or its address if it was given one. An entry without ports allows the
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
- `auth.oidc.return_urls` are https, or http on localhost, with no user
  name and no fragment, and `auth.oidc.require_known_users` has someone
  to admit: a user in an `allow.users`, a group in an `allow.groups`
  with `groups_claim` set, or, with `link_static_users`, a static user.
  Without `link_static_users`, a static user's name in an `allow.users`
  does not count, since OIDC refuses it;
- a file holds one YAML document.

## Serving a v1 file

`occamyd` serves a v1 file on `listen`, over TLS when `tls.cert` and
`tls.key` are set, and speaks HTTP/1.1 only, since the streams are
WebSockets over HTTP/1.1. So far it serves:

| Route | Meaning |
|-------|---------|
| `GET /healthz` | `200 {"status":"ok"}` while `occamyd` runs |
| `POST /api/v1/auth/token` | Sign-in for static users: `{"username":"alice","password":"…"}` as `application/json` in, `{"access_token":"…","token_type":"Bearer","expires_in":900}` out. A wrong password and an unknown user get the same `401` |
| `GET /api/v1/auth/oidc/login`, `GET /api/v1/auth/oidc/callback` | With `auth.oidc`, sign-in through the provider, from a client's `return_url` and `code_challenge` to the return URL with a one-time code in the fragment; see [signing in through OIDC](#signing-in-through-oidc) |
| `POST /api/v1/auth/oidc/token` | With `auth.oidc`: `{"code":"…","code_verifier":"…"}` as `application/json` in, the access token out, as from `POST /api/v1/auth/token` |
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
