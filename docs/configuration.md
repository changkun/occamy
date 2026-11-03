# Configuration

`occamyd` reads one YAML file, `./conf.yaml` unless `-conf` names
another, which starts with `version: 1`. It configures the API v1
server of
[api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md):
sign-in, the connection registry, TLS and the debug listener. `occamyd`
serves `/healthz`, sign-in for static users and through OIDC, the
connections and sessions of API v1 and their streams, and with
`web_client`, the web client (see
[Serving a v1 file](#serving-a-v1-file)). The
[conf.yaml](../conf.yaml) in this repository, which the image and the
release archives ship, is a v1 file to fill in.

A file without a `version` key, the format of the v0.x servers, is
refused with an error that names version 1: v1.0.0-beta.2 removed v0
files (see [v0 files](#v0-files)). Any other `version` is an error, and
so is `version: 0`. A `version` brought in by a merge key (`<<`)
counts, and a `version` key in any YAML document but the first is an
error.

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
      password_hash: "<the htpasswd recipe's output>"
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
  - id: web-shell
    protocol: kubernetes
    target: k8s.internal:6443 # the API server, always over HTTPS
    kubernetes: {namespace: web, pod: web-0, container: app}
    tls: {ca_file: /etc/occamy/k8s-ca.crt}
    credentials: {token_file: /run/secrets/k8s-token}
    allow: {groups: [ops]}
```

Unknown keys are errors, so a misspelt key is reported instead of being
ignored. Relative paths are resolved against `occamyd`'s working
directory.

`occamyd` has no default user and no default password, so this file has
to say who signs in, and `occamyd` refuses one that does not: see
[Sign-in](#sign-in-auth). The only user with a known password is the
demo stack's `demo`, in [docker/occamy-v1.yaml](../docker/occamy-v1.yaml),
for that stack alone.

### Server

| Key | Default | Meaning |
|-----|---------|---------|
| `version` | required | `1` |
| `listen` | `:5636` | Address of the public listener, `host:port`: the API, the streams and `/healthz` |
| `congestion_control` | `bbr` | The TCP congestion control of the public listener's connections, on Linux: a name the kernel knows, such as `bbr` or `cubic`, or `system` for the kernel's own choice. Where the kernel refuses it, `occamyd` keeps the kernel's choice and warns at start. Elsewhere it has no effect. See [TCP congestion control](#tcp-congestion-control) |
| `tls.cert`, `tls.key` | unset | Certificate and key files, PEM. `occamyd` terminates TLS when both are set; set both or neither. Without them, put a reverse proxy that terminates TLS in front. Read at start, so a renewed certificate needs a restart |
| `debug.address` | unset (off) | Address of the debug listener: expvar at `/debug/vars` and pprof under `/debug/pprof/`, without authentication. It needs a port of its own: not `listen`'s port on the same host, nor on `localhost` beside `127.0.0.1` or `::1`, nor beside a wildcard host (`:5636`, `0.0.0.0`, `::`), which takes every address. Keep it on the loopback interface; `occamyd` warns at start when it is not |
| `debug.address_env` | unset | An environment variable holding the debug listener's address, in place of `debug.address`; at most one of the two. The listener stays off while the variable is unset or empty, so that one file serves with the listener and without, and `occamyd` then logs `debug listener off:` with the variable's name at start, so that a misspelt or unexported variable shows. The address is checked as `debug.address` is |
| `origins` | none | Origins, `scheme://host[:port]`, whose web pages may open streams besides `occamyd`'s own. Written in lower case without a trailing slash, as browsers send them. `occamyd`'s own origin is any whose host and port are the request's `Host`, and whose scheme is `https` when `occamyd` terminates TLS. Behind a proxy that rewrites `Host`, list the origin of `occamyd`'s own pages here too. A stream request without `Origin`, which no browser sends, is accepted on its token alone |
| `web_client` | unset (off) | A directory holding the built web client, which `occamyd` serves under `/static/` ([api.md](api.md#get-static)): `client/occamy-web/dist`, where `make client` builds it and where the image has it, or the `dist` directory of the web client's release archive. A relative path is resolved against `occamyd`'s working directory, as other paths are. A path that is not a directory, or a directory `occamyd` cannot read, is an error; a directory that does not exist yet, or holds no `index.html`, is a warning at start, and `occamyd` serves the client once it is built there. The client signs in through API v1 on `occamyd`'s origin; with OIDC, list its URL, such as `https://occamy.example/static/`, in `auth.oidc.return_urls` |

### Sign-in: `auth`

A v1 file names how people sign in: static users in `auth.users`,
`auth.oidc`, or `auth.trusted_issuers`. With none of them, nobody could
sign in to API v1, so `occamyd` refuses to start.

To add a static user, make the hash of the password they chose and put
it in the file:

```sh
htpasswd -nbBC 12 "" 'the password' | cut -d: -f2
```

The signing key belongs at `/var/lib/occamy/signing.key`, on a
persistent volume. `occamyd` creates it on first start, readable by its
owner only (`0600`), and tokens survive a restart for as long as the
file does. If the file is lost, `occamyd` makes a new one and warns, and
everyone signs in again. Sessions end at a restart either way.

The image runs as the unprivileged user `nonroot` (65532), and its
`/var/lib/occamy` belongs to that user, so an empty named volume takes
that owner:

```sh
podman run -d --name occamy -p 5636:5636 \
  -v occamy-data:/var/lib/occamy \
  -v ./occamy.yaml:/occamy/v1.yaml:ro \
  occamy:latest /occamy/occamyd -conf /occamy/v1.yaml
```

Nothing refuses a container without that volume. The image's directory
is writable, so `occamyd` starts quietly and keeps the key in the
container's own layer, which a restart keeps and a recreate loses. A
new image or `--force-recreate` then replaces the key, everyone signs
in again, and the only sign is the warning that a first start gives,
`created a new signing key`. On a server that has run before, that
warning means the volume is missing.

If `occamyd` may not use the key or its directory, it stops at start and
says so. The directory, or whatever is mounted on it, has to belong to
the user that the error names, and how to give it depends on what it
is. The error's path is the one `occamyd` sees, which in a container is
not a path on the host:

| What holds the key | The step, with the error's uid: 65532 in the image |
|---|---|
| A directory, with no container | `chown -R <uid> <the directory>`, as root |
| A directory mounted from the host, on Docker or rootful podman | `chown -R 65532 <the directory on the host>`, as root |
| A directory mounted from the host, on rootless podman | `podman unshare chown -R 65532 <the directory on the host>`. A plain `chown` is wrong here: uid 65532 of a rootless container is another number on the host, such as 165531 |
| A named volume, on Docker | `docker run --rm -v <the volume>:/data busybox chown -R 65532 /data` |
| A named volume, on podman | `podman run --rm -v <the volume>:/data busybox chown -R 65532 /data` |

A named volume keeps its owner once it has files in it, such as those
of another image or a root container that used it first. An empty one
that a root container used first keeps that owner on Docker, while on
podman it takes the owner of the next container to use it.

The demo stack keeps its key at the same path on the volume
`occamy-v1-data`, which `make stop` keeps and `make clean` removes.

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.signing_key_file` | required | The Ed25519 key, PKCS #8 PEM, that signs `occamyd`'s tokens: `/var/lib/occamy/signing.key` on a persistent volume, as above. If the file does not exist, `occamyd` creates it on first start, `0600`, so its directory must exist and be writable by `occamyd`'s user |
| `auth.access_token_lifetime` | `15m` | How long an access token lasts, a duration with a unit (`15m`, `1h30m`) between `1m` and `12h` |
| `auth.trusted_issuers[].issuer` | required | Name of a third party, such as a gateway, whose tokens `occamyd` accepts. Unique, and not `occamy`, which is `occamyd`'s own. See [tokens from a trusted issuer](#tokens-from-a-trusted-issuer) |
| `auth.trusted_issuers[].public_key_file` | required | That issuer's Ed25519 public key, PKIX PEM |
| `auth.trusted_issuers[].link_static_users` | `false` | Lets the issuer's tokens act for a static user by naming them in `sub`. Off, such tokens are refused: see [tokens from a trusted issuer](#tokens-from-a-trusted-issuer) |
| `auth.trusted_issuers[].allowed_groups` | none | The groups the issuer's tokens may claim in `groups`. Others are dropped, so that an issuer cannot claim `ops` and open every connection that allows `ops`, stored credentials and all, unless the file lets it. A linked issuer acting as a static user gets these groups from its token too, not the static user's |
| `auth.users[].name` | required | A static user, who signs in with a password. Unique |
| `auth.users[].password_hash` | required | bcrypt hash of the password, cost 10 or more, which the `htpasswd` line above makes. Give every user the same cost: a sign-in as an unknown user is checked against a hash of the highest cost, so a user at a lower cost signs in measurably faster, and `occamyd` warns at start when the costs differ |
| `auth.users[].groups` | none | Groups the user belongs to |
| `auth.oidc` | unset (off) | Sign-in through an OpenID Connect identity provider; see [signing in through OIDC](#signing-in-through-oidc) |
| `auth.oidc.issuer` | required | The provider's issuer URL, exactly as its discovery document names it (a trailing slash counts). https, or http on localhost |
| `auth.oidc.client_id` | required | `occamyd`'s client ID at the provider |
| `auth.oidc.client_secret_file`, `client_secret_env` | unset | The client secret, from a file of at most 64 KiB or an environment variable; at most one. Neither means a public client |
| `auth.oidc.redirect_url` | required | `occamyd`'s callback URL as the browser and the provider know it, ending in `/api/v1/auth/oidc/callback` behind any proxy prefix. https, or http on localhost. Its path is also the sign-in cookie's, and with https the cookie is `Secure`, whoever terminates TLS |
| `auth.oidc.return_urls` | the origin of `redirect_url`, with the path `/` | Where browsers are sent back after a sign-in, with a one-time code or an error in the fragment: your web client's sign-in page, or an app's own scheme, such as `de.changkun.occamy:/oidc` for [Occamy for macOS](macos-app.md). A sign-in names one of them, matched exactly as written. https, http on localhost, or an app's scheme that is a domain name in reverse, with no user name and no fragment |
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
  list of group names, of which only those in the issuer's
  `allowed_groups` count; or `session`, with the user in `sub`, `sid` (a
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
`Alice` cannot pass for static `alice` in a log or to a person. With
`link_static_users`, only the exact name is the static user, and
another case or form is a user of its own. A trusted issuer's `sub` is
compared the same way, under its own `link_static_users`. Names
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
from `occamyd`'s origin: with `web_client`, or through the reverse proxy
in front of both. The web client that `web_client` serves returns to
its own URL, `/static/`, which `return_urls` must list.

A native app takes the same steps in a browser window of its own, such
as macOS's `ASWebAuthenticationSession`, keeps the verifier in memory,
and returns to a scheme of its own, a domain name in reverse as RFC 8252
asks. Occamy for macOS returns to `de.changkun.occamy:/oidc`: list it in
`return_urls` to let the app sign in through OIDC. Another app on the
same machine may claim the scheme, but the code it would receive signs
no one in without the verifier.

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
sessions, whichever of the two opened them. Only a `sub` that is
exactly the static user's name is linked; one in another case or
normal form, such as `Alice` or a decomposed `zoë`, is an OIDC user of
its own, with sessions of its own, as for a trusted issuer. Its access
token still says `idp: oidc`, for the log. A token of such a user, whose `sub` is a
static user's name in any case or normal form, is refused with
`invalid_token` once `link_static_users` is off. The OIDC user keeps
the groups of `groups_claim`, not the static user's.

### The connection registry: `connections`

| Key | Default | Meaning |
|-----|---------|---------|
| `connections[].id` | required | The connection's ID, used in URLs and tokens: 1 to 64 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or digit. Unique |
| `connections[].name` | the ID | Display name |
| `connections[].protocol` | required | `ssh`, `vnc`, `rdp` or `kubernetes`, each served by `occamyd`'s own backend (see [backends](#backends)) |
| `connections[].target` | required | `host:port`, or a host alone for the protocol's default port: 22 for ssh, 5900 for vnc, 3389 for rdp, 443 for kubernetes. IPv6 addresses go in brackets with a port, `[fd00::22]:22`. A kubernetes target is the API server's, which `occamyd` always reaches over HTTPS, so it has no `https://` |
| `connections[].host_keys` | required for ssh | The host keys an ssh target may present: public keys in `authorized_keys` format, as `ssh-keyscan` prints them, or SHA-256 fingerprints, as `ssh-keygen -l` prints them (`SHA256:…`). `occamyd` refuses any other key. Not for other protocols |
| `connections[].insecure_ignore_host_key` | `false` | Accept any host key from an ssh target instead, logging its fingerprint as a warning. For demos only; set this or `host_keys`, not both |
| `connections[].tls` | system roots | How the target's certificate is checked: a vnc target's when the server offers VeNCrypt, which the VNC backend then always chooses, an rdp target's always, since the RDP backend always runs TLS, and a kubernetes target's API server's. Without `tls`, the certificate must chain to the system's roots and name the target's host, as a browser checks it. Not for ssh. See below |
| `connections[].security` | `nla` | An rdp connection's security mode, named as Guacamole names them: `nla`, NLA (CredSSP with NTLMv2) over TLS; `nla-ext`, the same with the early user authorization result only; `tls`, TLS without NLA, which sends the password to whichever server passed the certificate check; or `any`, which offers TLS and NLA and lets the server choose. xrdp speaks no NLA, so an xrdp target needs `tls` or `any`. Standard RDP security, without TLS, is never used. Only for rdp. See [backends](#backends) |
| `connections[].disable_gfx` | `false` | Keep an rdp connection from asking for RDP's graphics pipeline (GFX), so that the server draws with plain bitmap updates. For a server whose GFX codecs `occamyd` does not take yet: **set it for Windows hosts** until `occamyd` takes ClearCodec, which they draw nearly all of their screen in. Only for rdp. See [backends](#backends) |
| `connections[].disable_copy` | `false` | Keep the remote's clipboard from the session's viewers: text copied on the remote stays there. Only for vnc and rdp: a terminal's text is drawn by the client, which copies it itself |
| `connections[].disable_paste` | `false` | Keep the viewers' clipboard from the remote: text copied on a viewer's side does not reach the remote's clipboard. A viewer can still type it. Only for vnc and rdp: a paste reaches a terminal as typing |
| `connections[].ard` | `false` | Allow Apple Remote Desktop authentication, which macOS Screen Sharing needs to log in a user of the Mac. A vnc connection only, and never with a `tls` that requires TLS. Read [the risk](#apple-remote-desktop-ard) first |
| `connections[].kubernetes` | required for kubernetes | The pod a kubernetes connection opens a terminal in, and what runs there; see [Kubernetes](#kubernetes-pods) |
| `connections[].credentials` | required | How `occamyd` authenticates to the target; see below |
| `connections[].allow.users`, `allow.groups` | required | Who may open the connection: users by name, and members of groups. At least one entry, since an empty list would let nobody in |

`credentials` holds an optional `username` and exactly one source:

| Key | Meaning |
|-----|---------|
| `password_file` | A file holding the password, at most 64 KiB. Trailing line breaks are dropped |
| `password_env` | An environment variable holding the password. It must be set and not empty |
| `private_key_file` | An SSH private key file, ssh only. Keep it readable by the `occamyd` user only |
| `token_file` | A file holding a bearer token, kubernetes only, such as a service account's, at most 64 KiB. Trailing line breaks are dropped |
| `token_env` | An environment variable holding a bearer token, kubernetes only. Trailing line breaks are dropped |
| `client_cert_file` and `client_key_file` | A client certificate and its key, PEM, kubernetes only: one source, so set both. Keep the key readable by the `occamyd` user only |
| `prompt: true` | Nothing stored: the session's owner is asked for whatever the target requires, and the answers are not kept. Use it also for a target that needs no credentials |

An ssh connection needs a `username` unless it prompts. A vnc
connection's `username` is for VeNCrypt's Plain authentication, which
is then chosen over the server's other types; without one, the owner is
asked if the server wants it. An rdp connection logs on with `username`
and the password; a `username` of the form
`DOMAIN\user`, with both parts, logs on to that domain, as does such a
username the owner gives at the prompt with the domain left empty;
`\user` logs on as the user alone. With NLA, the owner is asked for
what `credentials` does not store: the username and the domain, if no
`username` is stored, and the password. Without NLA
(`security: tls`), nothing is asked, and a missing username or
password leaves the server's logon screen to the owner. A kubernetes
connection authenticates with a bearer token or a client certificate,
never a `username` or a password; with `prompt: true` the owner is
asked for a token, and an empty answer sends none. A secret written
inline (`password:` or `token:` under `credentials`, `client_secret:`
under `oidc`) is
rejected with the keys to use instead.

Secrets are read when they are used, not when the file is loaded:
validation only checks that each file can be opened and each variable is
set. A secret rotated on disk is therefore picked up by the next session.

A vnc, rdp or kubernetes connection's `tls` takes:

| Key | Default | Meaning |
|-----|---------|---------|
| `require` | `false` | Refuse a vnc server that does not offer VeNCrypt with a certificate, so that nobody on the way can strip VeNCrypt from what the server offers. `ca_file` and `pins` imply it. The RDP backend and a kubernetes connection always require TLS, so for rdp and kubernetes it changes nothing |
| `server_name` | the target's host | The name the certificate must carry |
| `ca_file` | none | PEM certificates of the CAs that replace the system's roots, as for a private CA. Read when a session starts |
| `pins` | none | Certificates accepted as they are, as an ssh connection's `host_keys` are: their chain, names and dates are not checked. Each is a certificate in PEM or its SHA-256 fingerprint, in hex as `openssl x509 -noout -fingerprint -sha256` prints it or in base64 after `SHA256:`. Set this or `ca_file`, not both |
| `insecure_skip_verify` | `false` | Accept any certificate, logging its fingerprint as a warning. Set this or `ca_file` or `pins`, not both, as `host_keys` and `insecure_ignore_host_key` for ssh. For demos only |

A refused certificate fails the session, and `occamyd`'s log names the
certificate's fingerprint, ready for `pins`. A kubernetes connection's
token is sent only once the certificate has been accepted.

#### Kubernetes pods

A kubernetes connection opens a terminal in a container through the API
server's `pods/exec`, or `pods/attach`, as `kubectl exec -it` does. Its
`kubernetes` takes:

| Key | Default | Meaning |
|-----|---------|---------|
| `namespace` | `default` | The pod's namespace |
| `pod` | required | The pod's name |
| `container` | the pod's only container | The container's name. Required for a pod with more than one container: the API server refuses the stream with 400 without it, since it does not read kubectl's `kubectl.kubernetes.io/default-container` annotation |
| `command` | a shell: `bash`, or `sh` where the image has no `bash` | What runs, one element per argument, such as `[/bin/ls, -l]`. Not with `attach` |
| `attach` | `false` | Attach to the container's main process instead of running a command, as `kubectl attach -it` does. The container needs `stdin: true` in its spec, and `tty: true` unless `tty` is `false` |
| `tty` | `true` | Run the command in a terminal. Without one, its output and errors are shown together, and resizes are not sent |

The names must be Kubernetes's: lowercase letters, digits and `-`, and
`.` in a pod's name, so that none can reach another path of the API
server. Another protocol's connection cannot have `kubernetes`.

```yaml
  - id: web-shell
    protocol: kubernetes
    target: k8s.internal:6443
    kubernetes: {namespace: web, pod: web-0, container: app}
    tls: {ca_file: /etc/occamy/k8s-ca.crt}
    credentials: {token_file: /run/secrets/k8s-token}
    allow: {groups: [ops]}
```

**Authentication.** `occamyd` sends a bearer token or presents a client
certificate, and checks the API server's certificate against the
system's roots, `ca_file` or `pins`. A ServiceAccount token works on
every cluster, managed ones included: `kubectl -n web create token
occamy --duration=24h` makes one that expires, and a Secret of type
`kubernetes.io/service-account-token` holds one that does not. The
token file is read as each session starts, so a rotated token is picked
up by the next session. `occamyd` does not support:

- kubeconfig files: name the API server in `target`, its CA in `tls`
  and the token or certificate in `credentials` instead;
- exec credential plugins, such as EKS's `aws eks get-token` or GKE's
  `gke-gcloud-auth-plugin`;
- the legacy `auth-provider` entries of a kubeconfig;
- an API server behind a path prefix, such as a cluster that Rancher
  proxies at `https://rancher.example/k8s/clusters/c-m-abc12`: `target`
  is a `host:port`, and `occamyd` asks for `/api/v1/...` at its root.

`occamyd` deliberately runs no external credential binaries: a session
never starts a process on the server, and a credential is one file or
one variable that the configuration names.

**RBAC.** The token's, or the certificate's, user needs `create` on
`pods/exec` (or `pods/attach`) in the pod's namespace, and `get` too.
The WebSocket upgrade is an HTTP `GET`, which API servers authorize as
`get`; from Kubernetes 1.35 they also check `create`, as they always
did for kubectl's SPDY `POST`. Granting both covers every version. A
minimal Role and RoleBinding for a ServiceAccount:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: {name: occamy, namespace: web}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: occamy-exec, namespace: web}
rules:
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [get, create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: occamy-exec, namespace: web}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: occamy-exec}
subjects:
  - {kind: ServiceAccount, name: occamy, namespace: web}
```

`occamyd` needs no other permission: it does not list or read pods.

**Protocol.** `occamyd` speaks the `v5.channel.k8s.io` WebSocket
subprotocol of Kubernetes 1.30 and later, and falls back to
`v4.channel.k8s.io`, which older API servers proxy to the kubelet. The
command's exit ends the session with `remote-ended`, whatever its
status; a refused request or an API server that cannot be reached ends
it with `backend-failed`.

**Why there is no Kubernetes SDK.** The backend makes one request: pod
exec, upgraded to a WebSocket whose messages carry a one-byte channel
prefix. `coder/websocket`, which `occamyd` already uses for its own
streams, speaks it in a few hundred lines that the tests fuzz. client-go
would bring in the API machinery and most of the API types for that one
request, and its exec has gone over SPDY for most of its history. The
cost of going without it is the list above: no kubeconfig and no
credential plugins.

#### Backends

`occamyd` serves each protocol with a backend of its own:

| Protocol | Backend |
|----------|---------|
| ssh | `occamyd`'s SSH client |
| vnc | `occamyd`'s RFB client ([native VNC backend](../specs/008-native-architecture/native-vnc-backend.md)) |
| rdp | `occamyd`'s RDP client ([native RDP backend](../specs/008-native-architecture/native-rdp-backend.md)) |
| kubernetes | `occamyd`'s client of the API server's exec stream ([native Kubernetes backend](../specs/008-native-architecture/native-kubernetes-backend.md)) |

There is no other: v1.0.0-beta.2 removed guacd, and telnet, which only
guacd served. A file written for guacd is refused with what replaced
each of its keys (see [files written for guacd](#files-written-for-guacd)).
A vnc session:

- logs in with None, VNC Authentication or VeNCrypt (X509None, X509Vnc or
  X509Plain), checking the server's certificate as `tls` says, and asks the
  session's owner for a password or username that `credentials` does not
  store;
- asks for screen updates only while a viewer can see the session, and
  asks the server for the owner's size, from the stream's `hello` or a
  later resize, where the server supports ExtendedDesktopSize;
- carries clipboard text both ways, unless `disable_copy` or
  `disable_paste` closes a direction, and the server's cursor;
- ends with `remote-ended` when the server ends the session, and with
  `backend-failed` when it cannot connect or log in, or the server's
  certificate is refused.

An rdp session:

- connects over TLS, checking the server's certificate as `tls` says,
  and logs on with NLA (CredSSP with NTLMv2) unless `security` says
  otherwise, asking the session's owner, and only the owner, for the
  credentials NLA needs that `credentials` does not store;
- asks for the owner's size from the stream's `hello`, from 200 to
  8192 pixels a side and at most 2^25 pixels in all, and for a later
  resize through the Display Control channel, where the server has it;
  a server that keeps another size is scaled by the viewers;
- asks for RDP's graphics pipeline (GFX), unless `disable_gfx` says
  otherwise, and draws its surfaces, fills, copies and bitmaps in the
  uncompressed, planar, RemoteFX and RemoteFX Progressive codecs, and,
  from a server that does not open GFX, its bitmap updates, in
  interleaved RLE and planar; draws the pointer; sends keys as scancodes by their physical key, text as
  Unicode keyboard events, and the pointer and wheel as mouse events;
  tells the server to suppress its output while no viewer can see the
  session;
- carries clipboard text both ways through the clipboard channel: the
  server's to every viewer, and the owner's and interactive viewers'
  to the server, as their other input, unless `disable_copy` or
  `disable_paste` closes a direction;
- ends with `remote-ended` when the server ends the session, as when
  the user logs off, and with `backend-failed` when it cannot connect
  or log on, or the server's certificate is refused.

The RDP backend has been tested against xrdp, against occamy's
own test server, and on a Windows Server 2025 CI runner connecting to
itself over loopback. That is not a real deployment: Windows servers
are not verified until the backend's fourth phase runs against one
([native-rdp-backend](../specs/008-native-architecture/native-rdp-backend.md)),
and [#32](https://github.com/changkun/occamy/issues/32) stays open. On
that runner it logs on with NLA, and the keyboard and resize work. The
clipboard fails both ways; the suspected cause, that the client does
not set `CHANNEL_FLAG_SHOW_PROTOCOL` on the channel's chunks, is
unproven.

It asks for RDP's graphics pipeline (GFX) by default, through which
xrdp draws its sessions in RemoteFX Progressive. Of GFX it does not
yet take ClearCodec, which that Windows server drew nearly all of its
screen in, nor the bitmap cache, which the backend's third phase adds
next and which, with ClearCodec, blocks v1.0, nor AVC (H.264), which
it does not ask for: until then, a bitmap in ClearCodec, or from the
cache, is skipped, and leaves its region as it was, so a Windows
server's screen stays nearly black under GFX. At the first such PDU of
a session, `occamyd` logs a warning that names the way around it.
**Set `disable_gfx: true` for Windows hosts** until `occamyd` takes
ClearCodec: on the connection, it turns GFX off, so that the server
sends plain bitmap updates, and on the runner the whole desktop then
drew correctly. An ad-hoc rdp target always asks for GFX, since
`disable_gfx` is a connection's key, so reach a Windows host through a
connection.

xrdp speaks no NLA: offered NLA alone, it selects standard RDP
security, which `occamyd` refuses, so an xrdp connection
needs `security: tls` (or `any`). The session's error in `occamyd`'s
log then names the key to set, such as
`security: tls on connection "lab-desktop"`. With `security: tls`, the
password goes to the server inside TLS, so `tls` must check the
certificate: pin a self-signed one by its fingerprint. A connection
that sets `security: tls` or `any` with
`tls: {insecure_skip_verify: true}`, as the demo does, may send the
password to whoever is in between, and `occamyd` warns of it by name
when it starts.

#### Files written for guacd

A v1 file written for v1.0.0-beta.1 may still configure guacd, which
v1.0.0-beta.2 removed. `occamyd` refuses it, and names each key it
finds with what replaced it:

- `guacd`, a connection's `backend` and `adhoc_backends`: remove them.
  `occamyd`'s own backend serves each protocol, and reads nothing of
  `guacd.params`. A vnc or rdp connection that went through guacd:
  - has its certificate checked as `tls` says, against the system's
    roots without `tls`, in place of guacd's `ignore-cert` and
    `cert-fingerprints`: pin a self-signed certificate with
    `tls.pins`, by the fingerprint the refusal logs;
  - has `security` for its security mode, NLA by default, in place of
    guacd's `security`: an xrdp target needs `security: tls`;
  - has `disable_gfx` in place of guacd's `disable-gfx`, and asks for
    the graphics pipeline unless it is set;
  - has `disable_copy` and `disable_paste` in place of guacd's
    `disable-copy` and `disable-paste`.
- `legacy_v0`: the v0 routes are gone; clients use API v1 and
  protocol v1.
- A telnet connection: telnet is gone with guacd, which served it.

One of guacd's controls has no counterpart in `occamyd` yet: session
recording, by `recording-path` and guacd's other `recording-` and
`typescript-` parameters. `occamyd` records no session until a later
beta. A file that sets any of them under `guacd.params` is refused with
an error that says so, for each parameter, rather than served with its
sessions unrecorded. guacd's `disable-copy` and `disable-paste` are
refused too, each with the connection key that replaces it, so that no
clipboard opens that the file closed. A file whose RDP connection went
through guacd, with its sessions recorded and copying from them
disabled, is refused with:

```
config: 4 problems:
	line 7: guacd: guacd was removed in v1.0.0-beta.2: remove guacd, and occamyd serves ssh, vnc, rdp and kubernetes with its own backends
	line 10: guacd.params.rdp.recording-path: occamyd records no sessions until a later beta: guacd recorded them, and was removed in v1.0.0-beta.2; occamyd does not serve unrecorded what a file wants recorded, so remove the parameter to serve the sessions unrecorded, or keep v1.0.0-beta.1 until recording returns
	line 11: guacd.params.rdp.disable-copy: guacd was removed in v1.0.0-beta.2: set disable_copy: true on each vnc and rdp connection whose clipboard stays with the remote; occamyd does not open a clipboard that a file closes
	line 15: connections[0].backend: guacd was removed in v1.0.0-beta.2: remove backend, and the connection's protocol is served by occamyd's own backend
```

Set `disable_copy` or `disable_paste` on the connections in place of
guacd's parameter. Remove a recording parameter to serve the sessions
unrecorded, or keep v1.0.0-beta.1 until `occamyd` records them.

#### Apple Remote Desktop: `ard`

macOS Screen Sharing logs in a user of the Mac with Apple Remote
Desktop authentication (RFB security type 30), and offers no other type
a vnc connection could use when the Mac's VNC password is off.
`ard: true` allows it, with the Mac account's `username` in
`credentials` and its password from `credentials` or the owner's
prompt:

```yaml
  - id: studio-mac
    protocol: vnc
    ard: true
    target: studio.internal:5900
    credentials: {username: studio, prompt: true}
    allow: {users: [alice]}
```

**The risk.** ARD's Diffie-Hellman key exchange authenticates no server.
Anyone between `occamyd` and the Mac can therefore read the username and
the whole password, which are the Mac account's own, and the session
that follows is not encrypted. `ard: true` also raises what such an
attacker gets from any server on the connection's path, not only from a
Mac: it can strip VeNCrypt from what the server offers and offer type 30
alone, and `occamyd` sends it the whole password, where without `ard`
the most a downgrade gets is VNC Authentication's DES response, from
the password's first 8 bytes. Set `ard: true` only for Macs, on
networks you trust, such as a VPN to the Mac. It cannot be combined with
a `tls` that requires TLS (`require`, `ca_file` or `pins`): ARD runs
without TLS, so such a policy would refuse it anyway.

Without `ard: true`, a Mac that offers only ARD fails the session, and
`occamyd`'s log names the key that allows it:
`ard: true on connection "studio-mac"`. Ad-hoc targets cannot have it.

### Ad-hoc targets

| Key | Default | Meaning |
|-----|---------|---------|
| `allow_adhoc_targets` | `false` | Let signed-in users connect to targets that are not in the registry |
| `adhoc_allowlist` | none | Where ad-hoc targets may point, and on which ports: CIDR ranges (`10.0.0.0/8`), IP addresses, host names, or `*.` and a domain (`*.lab.internal`), each optionally followed by `:port` or `:first-last` (`10.0.0.0/8:22`, `*.lab.internal:5900-5910`; an IPv6 range or address goes in brackets, `[fd00::/8]:22`). Required when `allow_adhoc_targets` is `true` |

Any signed-in user may open an ad-hoc target, with
`POST /api/v1/sessions`, of any protocol but kubernetes, whose pod and
credentials only a connection names. Its credentials are always asked
of the user, and an ssh target needs `host_keys` or
`insecure_ignore_host_key: true` in the request, as a connection does.
Ad-hoc targets use the same backends as connections.

A vnc target checks the server's certificate, if the server offers
VeNCrypt, against the system's roots and the name the target was
given, or its address if it was given one. An ad-hoc target cannot require TLS,
though: a server that offers no VeNCrypt is used without it, so an
active attacker between `occamyd` and the target can strip VeNCrypt
from the server's offer and capture the VNC Authentication response, a
DES encryption of a random challenge with the password's first 8 bytes,
from which an offline search can recover a weak password. Reach targets
across networks you do not trust through a connection whose `tls`
requires TLS (`require`, `ca_file` or `pins`).

An rdp target always runs TLS, with the
certificate checked against the system's roots and the name the target
was given, or its address if it was given one, and logs on with NLA
alone, its default `security`: the user is asked for the password, and,
when the request carries no `username`, for the username and the
domain too. A request's `username` of the form `DOMAIN\user`, with both
parts, names the domain. A server whose certificate does not chain to
the system's roots, as a self-signed one, or that speaks no NLA, as
xrdp, needs a connection, whose `tls` and `security` can say so. It
also always asks for RDP's graphics pipeline (GFX), since only a
connection can set `disable_gfx`. A Windows server draws nearly all of
its screen under GFX in ClearCodec and from its bitmap cache, which
`occamyd` does not take until the RDP backend's third phase adds them,
so the screen stays nearly black; `occamyd` logs a warning when that
happens. Reach such servers through a connection with
`disable_gfx: true` meanwhile.

An entry of `adhoc_allowlist` without ports allows the protocol's own:
22 for ssh, 5900 to 5999 for vnc and 3389 for rdp. A
target is allowed on a port that an entry allows when:

- it is an IP address in a listed range, or a listed address;
- it is a listed host name, or ends in a `*.` entry's domain
  (`a.lab.internal` for `*.lab.internal`, but not `lab.internal`
  itself);
- it is any other host name, every address of which lies in a listed
  range.

`occamyd` resolves every host name and dials the first address it
checked, so a second lookup cannot lead elsewhere. Loopback, link-local
(such as a cloud's metadata service at `169.254.169.254`), unspecified
and multicast addresses, and the metadata services at `fd00:ec2::254`
(AWS over IPv6) and `100.100.100.200` (Alibaba Cloud), are refused,
whatever range or listed name leads to them, unless an entry is that
very address (`127.0.0.1:2222`). So are `occamyd`'s own `listen` and
`debug.address`, at their ports: a listener on no host stands for
every address of the host.

A range that holds the host's own addresses still reaches the host's
other services on the ports it allows, such as a database on a private
address. List ranges that leave the host out, or the host's addresses
with the ports meant, rather than a range that holds them.

### Limits: `limits`

| Key | Default | Meaning |
|-----|---------|---------|
| `limits.sessions` | `1000` | Sessions `occamyd` runs at once, whoever owns them, since a trusted issuer can name new users without end. One more is refused with `503 limit_exceeded`, or on a connection's stream with `close` `forbidden` |
| `limits.sessions_per_user` | `10` | Sessions one user owns at once. One more is refused with `429 limit_exceeded`, or on a connection's stream with `close` `forbidden` |
| `limits.viewers_per_session` | `50` | Viewers a session takes besides its owner, who can always join. One more gets `close` `viewer-limit` |
| `limits.shares_per_session` | `100` | Shares of a session that have not expired. One more is refused with `429 limit_exceeded` |
| `limits.decode_cores` | half of `GOMAXPROCS`, at least `1` | Cores of decoding that all of `occamyd`'s RDP sessions share. A session over its share waits, with its server's updates queued behind it, and nothing is dropped |

A user is a subject of an issuer, as for owning sessions: a trusted
issuer's users count apart from `occamyd`'s own.

An RDP session may take about one core decoding what its server
sends, whatever that is, so one user's 10 sessions to hostile servers,
such as ad-hoc targets they chose, would take about 10 cores, and
`limits.sessions` sessions that many. `limits.decode_cores` holds them
together to about that many cores, measured on the benchmarks' host,
and by default leaves half of them to encoding for viewers and to the
rest of `occamyd`. Raise it where many sessions show full-screen motion,
which at 1920 × 1080 and 30 frames a second takes about 0.8 of a core
each, and lower it on a host that does other work.

Sessions that want more than it has share it evenly. Of n such
sessions, each gets at least an nth of it, or a session's core if that
is less. A session that wants less than its share gets all it wants,
hardly waiting for the others once it has started, and what it leaves
goes to those that want it. The share is a session's, not a user's, so
a user's 10 sessions have 10 shares: lower `limits.sessions_per_user`
where users are not trusted with that much, or keep
`allow_adhoc_targets` off.

The limit counts what sessions decode and draw, and what they hand to
viewers, not what they read and ignore: a server that floods a session
with what the client does not ask for, such as a fast-path update of
an unknown kind, can keep that session's one reader busy outside it,
for as long as it sends them. It bounds the rate of decoding, so
sessions may still decode at once what they each waited for.

### Streams: `streams`

| Key | Default | Meaning |
|-----|---------|---------|
| `streams.viewer_timeout` | `30s` | How long a viewer, hidden or not, may leave the frames it was sent unacknowledged, or one write to it block, before it gets `close` `too-slow`. At least `1s` |

Each visible viewer holds the screen it was last sent until it
acknowledges a newer one, and a viewer that may type, hidden or not,
also holds the screen of its last input; a hidden viewer holds no
screen it was sent. A terminal of 1000 by 500 cells, the largest,
takes about 11 MiB a screen. `viewer_timeout` drops a viewer that stops reading or
acknowledging, but a viewer that acknowledges within it keeps its
screens for as long as it stays: at the defaults one user's 10
sessions, with their owners and 50 viewers each, may hold 510 such
screens, about 5.5 GiB, and twice that, 1020 screens or about 11 GiB,
if every viewer may type. Hidden viewers that may type hold one each,
and read-only ones none. Lower
`limits.viewers_per_session` or `limits.sessions_per_user` where users
are not trusted with that much.

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
- `congestion_control` is a name of at most 15 lower-case letters,
  digits and underscores, as Linux's are;
- connection IDs, user names and issuer names are unique, and no trusted
  issuer is named `occamy`;
- `tls` is set only on vnc, rdp and kubernetes connections; `ard` only
  on vnc connections, `security` and `disable_gfx` only on rdp
  connections, and `disable_copy` and `disable_paste` only on vnc and
  rdp connections; `ard` never with a `tls` that requires TLS, and
  `security` is `nla`, `nla-ext`, `tls` or `any`;
- a kubernetes connection names a pod, with Kubernetes's names, and
  either `attach` or a `command`; it authenticates with `token_file`,
  `token_env`, `client_cert_file` with `client_key_file`, or
  `prompt: true`, and other protocols use none of these;
- user, group and issuer names are not empty and have no spaces;
- `auth.users`, `auth.oidc` or `auth.trusted_issuers` lets someone sign
  in;
- with neither `auth.oidc` nor `auth.trusted_issuers` configured, every
  name in an `allow` list is a static user, and every group is a group of
  some static user. With either one configured, users and groups also come
  from token claims, so names the file does not list are accepted;
- `auth.oidc.return_urls` are https, http on localhost, or an app's
  scheme that is a domain name in reverse, with no user name and no
  fragment, and `auth.oidc.require_known_users` has someone
  to admit: a user in an `allow.users`, a group in an `allow.groups`
  with `groups_claim` set, or, with `link_static_users`, a static user.
  Without `link_static_users`, a static user's name in an `allow.users`
  does not count, since OIDC refuses it;
- a file holds one YAML document.

## Serving a v1 file

`occamyd` serves a v1 file on `listen`, over TLS when `tls.cert` and
`tls.key` are set, and speaks HTTP/1.1 only, since the streams are
WebSockets over HTTP/1.1. It serves:

| Route | Meaning |
|-------|---------|
| `GET /healthz` | `200 {"status":"ok"}` while `occamyd` runs |
| `GET /api/v1/auth/methods` | How a user can sign in, for a sign-in page: `{"oidc":true}` with `auth.oidc`, `{"oidc":false}` without |
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
| `/static/` | With `web_client`, the web client ([api.md](api.md#get-static)) |

`ssh`, `vnc`, `rdp` and `kubernetes` sessions use `occamyd`'s own
backends ([backends](#backends)). [api.md](api.md) describes every
route in full. A connection the caller may not
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

### TCP congestion control

On Linux, `occamyd` asks for `congestion_control`, BBR by default, on
its public listener, and the connections it accepts inherit it. BBR
sends at the rate it measures the path to carry, where the kernel's
usual default, CUBIC, cuts its rate at every lost packet. On a link
that loses packets at random, such as Wi-Fi or a long mobile path, a
desktop in motion then keeps its frame rate, and a key's effect shows
sooner ([benchmarks](benchmarks.md#step-3a-the-congestion-control)).

The kernel needs BBR's module loaded. `occamyd` needs no privilege to
use it then, and in a container it is the host's kernel, or the
node's, that loads it:

```sh
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/tcp_bbr.conf # at every boot
```

At start `occamyd` logs the congestion control its connections use,
such as `TCP congestion control: bbr`, and publishes it as
`tcp_congestion` at `/debug/vars`. If the module is not loaded, it says
so in a warning and keeps the kernel's choice. `congestion_control:
system` keeps the kernel's choice without asking, for a host whose
operator set it, and any other name the kernel allows asks for that
one.

## v0 files

v1.0.0-beta.2 removed v0 files, the format of the v0.x servers, with
guacd, which served all their sessions. A file without a `version` key
is refused:

```
config: the file has no version: 1; occamyd reads only version 1 files since v1.0.0-beta.2, and serves no v0 file: write a version 1 file, as docs/configuration.md describes
```

A v0 file's keys, in a v1 file:

| v0 | v1 |
|----|----|
| `address` | `listen` |
| `mode: debug` | `debug.address`, the debug listener, apart from the public one |
| `auth.jwt_secret`, `auth.jwt_alg` | `auth.signing_key_file`: `occamyd` signs its tokens with an Ed25519 key, and users sign in as `auth` says |
| `client` | `web_client` |
| `guacd` | none: `occamyd` serves ssh, vnc and rdp itself |
| a v0 token's target | a connection in `connections`, or with `allow_adhoc_targets`, an ad-hoc target |
