# Configuration

`occamyd` reads one YAML file: the file that `-conf` names, or
`./conf.yaml` without it. The image reads `/occamy/conf.yaml`.
[Deploy](deploy.md) shows how to run `occamyd` with it. This page
describes every key.

- The file starts with `version: 1`.
- Unknown keys are errors, so a misspelt key is reported, not ignored.
- The file holds no secrets. A password, key or token is named by the
  file or the environment variable that holds it, so you can commit the
  file and review changes to it.
- Relative paths are resolved against `occamyd`'s working directory.
- `occamyd` has no default user and no default password. The file says
  who signs in, and `occamyd` refuses a file that lets nobody in.

## Example

```yaml
version: 1
listen: 0.0.0.0:5636
web_client: client/occamy-web/dist
auth:
  signing_key_file: /var/lib/occamy/signing.key
  users:
    - name: alice
      password_hash: "<the output of htpasswd>"
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
    name: NAS desktop
    protocol: vnc
    target: nas.internal
    credentials: {password_env: NAS_VNC_PASSWORD}
    allow: {users: [alice]}
  - id: windows
    name: Windows workstation
    protocol: rdp
    target: 10.0.4.20
    disable_gfx: true
    tls: {pins: ["3F:A1:07:5C:92:E4:6B:D8:21:0A:F3:8E:57:C6:19:B4:E2:7D:40:95:A8:1C:6F:D3:0B:72:E9:58:34:AC:66:1F"]}
    credentials: {username: 'CORP\alice', prompt: true}
    allow: {users: [alice]}
```

Replace the password hash with your own, as
[Static users](#static-users) shows. The `conf.yaml` that the image and
the release archives ship is a version 1 file to fill in.

## Server

| Key | Default | Meaning |
|-----|---------|---------|
| `version` | required | Always `1` |
| `listen` | `:5636` | The address of the API, the streams, the web client and `/healthz`, as `host:port` |
| `tls.cert`, `tls.key` | unset | A certificate and its key, in PEM, with which `occamyd` serves HTTPS |
| `web_client` | unset | The directory of the built web client, which `occamyd` serves under `/static/` |
| `origins` | none | Other origins whose web pages may open streams |
| `congestion_control` | `bbr` | The TCP congestion control of the listener's connections, on Linux |
| `debug.address` | unset | The address of the debug listener, which is off without it |
| `debug.address_env` | unset | An environment variable that holds the debug listener's address |

**TLS.** Set both `tls.cert` and `tls.key`, or neither. Without them,
`occamyd` speaks plain HTTP, so put a reverse proxy that terminates TLS
in front of it ([Deploy](deploy.md#serve-it-over-tls)). `occamyd` reads
the files at start, so a renewed certificate takes a restart. It speaks
HTTP/1.1 only, since the streams are WebSockets over HTTP/1.1.

**The web client.** In the image, `web_client` is
`client/occamy-web/dist`, where the image has the client. With a
release archive, it is the `dist` directory of the web client's
archive. A path that is not a directory, or a directory `occamyd` cannot
read, is an error. A directory that does not exist yet, or has no
`index.html`, gets a warning at start, and `occamyd` serves the client
once it is built there. The web client signs in on `occamyd`'s own
origin. With OIDC, list its URL, such as
`https://occamy.example.com/static/`, in
[`auth.oidc.return_urls`](#signing-in-through-oidc).

**Origins.** A browser sends the origin of the page that opens a
stream, and `occamyd` refuses origins it does not know. It knows its
own: an origin whose host and port are the request's `Host`, with the
scheme `https` when `occamyd` terminates TLS. List any other origin
here, as `scheme://host` or `scheme://host:port`. Behind a proxy that
rewrites `Host`, list the address your users open. A stream request
without `Origin`, which no browser sends, is accepted on its token
alone.

**The debug listener.** It serves expvar at `/debug/vars` and pprof
under `/debug/pprof/`, without authentication
([Debugging and monitoring](debugging.md)). Keep it on the loopback
interface, such as `localhost:6060`. `occamyd` warns at start when it is
not. It needs a port of its own: `occamyd` refuses a debug address that
could take connections on `listen`'s port. Set `debug.address` or
`debug.address_env`, not both. With `debug.address_env`, one file
serves with the listener and without it, since the listener stays off
while the variable is unset or empty. `occamyd` then logs
`debug listener off:` with the variable's name, so that a misspelt or
unexported variable shows.

### TCP congestion control

On Linux, `congestion_control` names the congestion control that
`occamyd` asks for on its listener, and every connection it accepts
inherits it. Elsewhere the key has no effect.

| Value | What `occamyd` does |
|-------|---------------------|
| `bbr` | The default: asks for BBR, which keeps a desktop's frame rate on a link that loses packets |
| Another name the kernel knows, such as `cubic` | Asks for that one |
| `system` | Keeps the kernel's choice without asking |

A name has at most 15 lower-case letters, digits and underscores. When
the kernel refuses the name, `occamyd` keeps the kernel's choice and
warns at start. [Deploy](deploy.md#tcp-congestion-control) shows how to
load BBR and check which one the connections use.

## Sign-in: `auth`

Users sign in with a password, as [static users](#static-users), or
through an [OIDC provider](#signing-in-through-oidc), or come with tokens
from a [trusted issuer](#tokens-from-a-trusted-issuer). Set at least one
of `auth.users`, `auth.oidc` and `auth.trusted_issuers`. Without them,
nobody could sign in, and `occamyd` refuses to start.

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.signing_key_file` | required | [The Ed25519 key](#the-signing-key) that signs `occamyd`'s tokens, in PKCS #8 PEM |
| `auth.access_token_lifetime` | `15m` | How long an access token lasts, from `1m` to `12h`, written with a unit, such as `1h30m` |
| `auth.users` | none | [Static users](#static-users), who sign in with a password |
| `auth.oidc` | unset | Sign-in [through an OpenID Connect provider](#signing-in-through-oidc) |
| `auth.trusted_issuers` | none | Third parties [whose tokens `occamyd` accepts](#tokens-from-a-trusted-issuer) |

### Static users

To add a user, make the bcrypt hash of the password they chose, and put
it in the file:

```sh
htpasswd -nbBC 12 "" 'the password' | cut -d: -f2
```

`htpasswd` comes with Apache's tools: `apache2-utils` on Debian and
Ubuntu, `httpd-tools` on Fedora and RHEL, and macOS has it.

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.users[].name` | required | The unique name the user signs in with, without spaces |
| `auth.users[].password_hash` | required | The bcrypt hash of the user's password, cost 10 or more |
| `auth.users[].groups` | none | The groups the user belongs to |

Give every user the same bcrypt cost. A sign-in as an unknown user is
checked against a hash of the highest cost, so a user with a lower cost
signs in measurably faster, which tells who exists. `occamyd` warns at
start when the costs differ.

### The signing key

`occamyd` signs its tokens with the key in `auth.signing_key_file`. Keep
it at `/var/lib/occamy/signing.key`, on a persistent volume.

- If the file does not exist, `occamyd` creates it at start, readable by
  its owner only (`0600`). It writes a temporary file in the same
  directory first, so the directory must exist and be writable by
  `occamyd`'s user.
- Tokens stay valid across a restart for as long as the file does.
  Sessions end at a restart either way.
- If the file is lost, `occamyd` makes a new one and warns
  `created a new signing key`, and everyone signs in again. On a server
  that has run before, that warning means the key's volume is missing.

If `occamyd` may not read the key or write its directory, it stops at
start and names the user it runs as. Give the directory, or what is
mounted on it, to that user. The path in the error is the one `occamyd`
sees, which in a container is not a path on the host.

| Where the key is | What to run, with the uid from the error (65532 in the image) |
|------------------|----------------------------------------------------------------|
| A directory, with no container | `sudo chown -R <uid> <the directory>` |
| A host directory mounted into Docker or rootful Podman | `sudo chown -R 65532 <the directory on the host>` |
| A host directory mounted into rootless Podman | `podman unshare chown -R 65532 <the directory on the host>` |
| A named volume on Docker | `docker run --rm -v <the volume>:/data busybox chown -R 65532 /data` |
| A named volume on Podman | `podman run --rm -v <the volume>:/data busybox chown -R 65532 /data` |

With rootless Podman, a plain `chown` is wrong. Uid 65532 in the
container is another number on the host, such as 165531, and
`podman unshare` translates it.

A named volume keeps its owner once it holds files. An empty volume
that a root container used first stays root's on Docker, while on Podman
it takes the owner of the next container that uses it.

### Signing in through OIDC

`occamyd` signs users in at an OpenID Connect provider with the
authorization code flow and PKCE. Register `occamyd` at the provider as
such a client, with `redirect_url` as its redirect URI. Then add
`auth.oidc`:

```yaml
auth:
  oidc:
    issuer: https://accounts.example.com
    client_id: occamy
    client_secret_file: /run/secrets/oidc-client-secret
    redirect_url: https://occamy.example.com/api/v1/auth/oidc/callback
    return_urls:
      - https://occamy.example.com/static/
      - de.changkun.occamy:/oidc
    groups_claim: groups
```

The keys of `auth.oidc`:

| Key | Default | Meaning |
|-----|---------|---------|
| `issuer` | required | The provider's issuer URL, exactly as its discovery document names it, trailing slash included |
| `client_id` | required | `occamyd`'s client ID at the provider |
| `client_secret_file`, `client_secret_env` | unset | The client secret, from a file of at most 64 KiB or an environment variable, or neither for a public client |
| `redirect_url` | required | `occamyd`'s callback, `/api/v1/auth/oidc/callback`, as browsers reach it |
| `return_urls` | `redirect_url`'s origin, with the path `/` | Where browsers go back to after signing in |
| `scopes` | `[openid, profile, email]` | The scopes to ask for, `openid` among them |
| `subject_claim` | `sub` | The ID token's claim that names the user |
| `groups_claim` | unset | The ID token's claim that lists the user's groups |
| `require_known_users` | `false` | Admit only users whom an allow list names |
| `link_static_users` | `false` | Let a user whose name is a static user's sign in as that static user |

`issuer` and `redirect_url` use https, or http on localhost, and so does
every return URL that is not an app's scheme. When `redirect_url` is
https, the sign-in cookie is `Secure`, whoever terminates TLS.

**Return URLs.** After a sign-in, `occamyd` sends the browser back to a
return URL with a one-time code, or an error, in the fragment. A client
names one of `return_urls`, matched exactly as written. A return URL may
also be an app's own scheme, which must be a domain name in reverse.
None may hold a user name or a fragment.

- The web client that `occamyd` serves returns to its own URL, `/static/`.
  The default, the origin with the path `/`, does not reach it, so list
  `https://<your host>/static/`.
- [Occamy for macOS](macos-app.md) returns to `de.changkun.occamy:/oidc`.
  List it to let the app sign in through OIDC.

**The user's name.** `subject_claim` names a top-level claim whose value
is a string of up to 255 bytes, of characters that show, with no space
but U+0020. A token without such a value is refused. Allow lists grant
access by this name, so pick a claim that users cannot set themselves,
such as `sub` or `email`. Never use a display name, or
`preferred_username` where users can edit it. With `email`, the token
must also say `email_verified: true`. [User names](#user-names) says how
names are compared.

**Groups.** `groups_claim` names a top-level claim that holds a list of
strings or one string. `occamyd` keeps only the groups that the file
names, in `auth.users[].groups` or an `allow.groups`. Without
`groups_claim`, users from the provider have no groups.

**Who gets in.** Without `require_known_users`, anyone the provider
signs in gets an access token, and allow lists still decide which
connections they may open. With it, `occamyd` admits a user only if a
connection's `allow.users` names them exactly, or one of its
`allow.groups` holds one of their groups from `groups_claim`. A static
user's name counts only with `link_static_users`.

**Static users' names.** Without `link_static_users`, a user whose name
equals a static user's, in any case or Unicode normal form (`Alice` for
`alice`), is refused with `access_denied` and logged. A name belongs to
one source unless the file links them. With `link_static_users`, a user
whose name is exactly a static user's signs in as that static user. They
share the static user's place in every allow list and the same sessions,
but their groups still come from `groups_claim`. Their access token says
`idp: oidc`, for the log. Turn it on only with a `subject_claim` that
users cannot choose, such as `sub` or a verified `email`.

**The provider.** `occamyd` discovers the provider in the background at
start, so a provider that is down does not keep `occamyd` from starting.
Until it answers, OIDC sign-ins fail, and static users sign in as usual.
After a failed discovery, the first sign-in 10 seconds later tries
again. Each request to the provider times out after 10 seconds.
`occamyd` also:

- accepts ID tokens signed with RS, ES or PS at 256, 384 or 512 bits, or
  EdDSA, and refuses `none` and HMAC whatever the provider advertises;
- talks only to the endpoints that the discovery document names, which
  must be https, or http on localhost when the issuer is too, and
  follows no redirect;
- compares the `iss` that the provider sends back (RFC 9207) with
  `issuer` exactly, and reads
  `authorization_response_iss_parameter_supported` only as a JSON bool.

Several `occamyd` instances behind one name need sticky sessions, since
each process keeps its own sign-ins and one-time codes.

**Errors.** A failed sign-in returns to the return URL with `#error=`
and one of these codes:

| `error` | Meaning |
|---------|---------|
| `access_denied` | The provider refused the user, or `occamyd` did: a missing or unusable `subject_claim`, an unverified email, a static user's name without `link_static_users`, or a user that `require_known_users` does not admit |
| `sign_in_failed` | The provider answered with another error, or its answer did not verify |
| `unavailable` | The provider cannot be reached, or too many sign-ins wait for their clients |

[The HTTP API](api.md#get-apiv1authoidclogin) describes the three OIDC
routes and every answer they give.

#### A client's side

A web client signs a user in in four steps:

1. Make a random verifier of 43 to 128 characters (`A-Z`, `a-z`, `0-9`,
   `-`, `.`, `_`, `~`), and keep it in `sessionStorage`. Its challenge is
   the SHA-256 of the verifier in base64url without padding (PKCE's
   S256, RFC 7636).
2. Send the browser to
   `GET /api/v1/auth/oidc/login?return_url=<one of return_urls>&code_challenge=<challenge>`.
   `return_url` may be left out when only one is configured. `occamyd`
   sets a cookie for its callback and redirects to the provider.
3. Once the user has signed in, the provider sends the browser to the
   callback, which sends it on to the return URL with
   `#code=<one-time code>`, or `#error=<code>`.
4. On the return URL, read the fragment and remove it with
   `history.replaceState`. Post
   `{"code":"…","code_verifier":"<the verifier>"}` as `application/json`
   to `POST /api/v1/auth/oidc/token`. The answer is the same as from
   `POST /api/v1/auth/token`:
   `{"access_token":"…","token_type":"Bearer","expires_in":900}`. Then
   forget the verifier.

A client that finds a `#code` but holds no verifier did not start that
sign-in, so it must not sign in with the code. It should spend the code
by posting it with a throwaway verifier, since a code works once, or
else remove it from the URL and ignore it.

The access token never appears in a URL, so it reaches no server log,
`Referer` or browser history. The one-time code does sit in the history,
but it lasts a minute, works once, and only with the verifier of the
client that started the sign-in. `occamyd` answers no CORS preflight, so
serve a web client from `occamyd`'s origin: with `web_client`, or
through a reverse proxy in front of both.

A native app takes the same steps in a browser window of its own, such
as macOS's `ASWebAuthenticationSession`. It keeps the verifier in memory
and returns to a scheme of its own, a domain name in reverse, as
RFC 8252 asks. Another app on the same machine may claim the scheme, but
the code it would receive signs no one in without the verifier.

### Tokens from a trusted issuer

A trusted issuer, such as a gateway in front of `occamyd`, mints tokens
that `occamyd` accepts as its own.

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.trusted_issuers[].issuer` | required | The unique name the issuer puts in `iss`, other than `occamyd`'s own, `occamy` |
| `auth.trusted_issuers[].public_key_file` | required | The issuer's Ed25519 public key, in PKIX PEM |
| `auth.trusted_issuers[].link_static_users` | `false` | Let the issuer's tokens act for a static user that `sub` names |
| `auth.trusted_issuers[].allowed_groups` | none | The groups that the issuer's tokens may claim |

`occamyd` drops any group a token claims that `allowed_groups` does not
list. Otherwise an issuer could claim `ops` and open every connection
that allows `ops`, with its stored credentials. A linked issuer acting
as a static user also gets its groups from its token, not the static
user's.

The issuer's tokens are JWTs:

- signed with the issuer's Ed25519 key (`alg: EdDSA`); no other algorithm
  is accepted;
- with `iss` the issuer's name as configured, and `aud` `occamy`;
- with `iat` and `exp`, at most 12 hours apart;
- with `sub` a usable name, by the rules of
  [`subject_claim`](#signing-in-through-oidc);
- with `kind` either `access` or `session`. An access token names the
  user in `sub`, and may list groups in `groups`. A session token names
  the user in `sub`, a session ID in `sid`, and a `role`: `owner`,
  `viewer` or `read-only`.

A session token joins only a session that one of the issuer's own users
opened, or, with `link_static_users`, a static user. An `owner` token
works only when its `sub` is the session's owner. `occamyd` refuses a
trusted issuer's token that sets `uiss`, and its session tokens that
carry `idp`.

A `sub` that equals a static user's name, in any case or Unicode normal
form, is refused with `invalid_token`, or on a stream with a `close` of
`unauthorized`, unless the issuer has `link_static_users`. Then a `sub`
that is exactly a static user's name acts as that static user, and one
in another case or form is the issuer's own user.

`occamyd`'s own tokens have the same shape, with `iss: occamy`. Its
access tokens also carry `idp`, where the user signed in: `local` for a
static user and `oidc` for OIDC. `occamyd` logs it at each sign-in.

### User names

A user is a name from one source: a static user, a user from OIDC, or a
user of one trusted issuer. The gateway's `carol` and `occamyd`'s
`carol` are two users. Neither reads, closes, shares or owns the other's
sessions. A user from OIDC and a static user are two users as well,
unless `auth.oidc.link_static_users` links the exact name.

Allow lists compare names exactly, byte for byte, whichever source signed
the user in. `allow.users: [alice]` admits neither `Alice` nor an
`alice` with a decomposed character, so write names as the provider
sends them.

Keeping sources apart compares more loosely. Without
`link_static_users`, a name from OIDC or a trusted issuer is refused
when it equals a static user's under Unicode case folding, once both
are in NFC. So `Alice` cannot pass for the static user `alice` in a log
or to a person. With `link_static_users`, only the exact name is the
static user, and another case or form is a user of its own.

Names that only look alike, such as one with a Cyrillic `а` or a
fullwidth `ａ`, are neither refused nor the same user. Pick a
`subject_claim` that users cannot choose.

## Connections: `connections`

Each connection is a remote that users may open by its ID.

| Key | Default | Meaning |
|-----|---------|---------|
| `connections[].id` | required | A unique ID of 1 to 64 lower-case letters, digits, `.`, `_` and `-`, starting with a letter or digit |
| `connections[].name` | the ID | The name users see |
| `connections[].protocol` | required | `ssh`, `vnc`, `rdp` or `kubernetes` |
| `connections[].target` | required | `host:port`, or a host alone for the protocol's port: 22, 5900, 3389 or 443 |
| `connections[].credentials` | required | [How `occamyd` signs in](#credentials) to the target |
| `connections[].allow.users`, `allow.groups` | required | Who may open the connection, by user name and by group, with at least one entry |

An IPv6 target goes in brackets with a port, such as `[fd00::22]:22`. A
kubernetes target is the API server's `host:port`, which `occamyd`
always reaches over HTTPS, so write it without `https://`.

Each protocol adds keys of its own. A key on a connection of another
protocol is an error.

| Key | Protocols | See |
|-----|-----------|-----|
| `host_keys`, `insecure_ignore_host_key` | ssh | [SSH](#ssh) |
| `tls` | vnc, rdp, kubernetes | [Certificates](#certificates-tls) |
| `disable_copy`, `disable_paste` | vnc, rdp | [The clipboard](#the-clipboard) |
| `ard` | vnc | [Apple Remote Desktop](#apple-remote-desktop-ard) |
| `security`, `disable_gfx` | rdp | [RDP](#rdp) |
| `kubernetes` | kubernetes | [Kubernetes pods](#kubernetes-pods) |

### Credentials

`credentials` holds an optional `username` and exactly one source:

| Key | Meaning |
|-----|---------|
| `password_file` | A file of at most 64 KiB that holds the password, without its trailing line breaks |
| `password_env` | An environment variable that holds the password, set and not empty |
| `private_key_file` | An SSH private key file, for ssh only |
| `token_file` | A file of at most 64 KiB that holds a bearer token, for kubernetes only |
| `token_env` | An environment variable that holds a bearer token, for kubernetes only |
| `client_cert_file` and `client_key_file` | A client certificate and its key, both in PEM, for kubernetes only |
| `prompt: true` | Nothing stored: the session's owner is asked for what the target needs, and the answers are not kept |

Use `prompt: true` also for a target that needs no credentials. Keep a
private key readable by `occamyd`'s user only. `occamyd` drops trailing
line breaks from a password or token file, and from a token in a
variable.

`occamyd` reads a secret when a session uses it, not when it loads the
file. At start, it checks only that each file can be opened and each
variable is set. A secret rotated on disk is therefore picked up by the
next session. A secret written into the file, such as `password:` under
`credentials` or `client_secret:` under `oidc`, is refused with the keys
to use instead.

Each protocol's section says what it does with `username`.

### Certificates: `tls`

A vnc, rdp or kubernetes connection checks its target's certificate:
a vnc server's when it offers VeNCrypt, an rdp server's always, and a
kubernetes API server's always. Without `tls`, the certificate must chain
to the system's roots and name the target's host, as a browser checks
it.

| Key | Default | Meaning |
|-----|---------|---------|
| `tls.require` | `false` | Refuse a vnc server that offers no VeNCrypt with a certificate |
| `tls.server_name` | the target's host | The name the certificate must carry |
| `tls.ca_file` | none | PEM certificates of the CAs that replace the system's roots, such as a private CA |
| `tls.pins` | none | Certificates accepted as they are, with no check of their chain, names or dates |
| `tls.insecure_skip_verify` | `false` | Accept any certificate, for demos only, and log its fingerprint as a warning |

A pin is a certificate in PEM or its SHA-256 fingerprint: in hex, as
`openssl x509 -noout -fingerprint -sha256` prints it, or in base64 after
`SHA256:`. Pin a self-signed certificate this way. Set `ca_file` or
`pins` or `insecure_skip_verify`, at most one of them.

`tls.require` keeps anyone on the way from stripping VeNCrypt from what
a vnc server offers. `ca_file` and `pins` imply it. rdp and kubernetes
connections always require TLS, so it changes nothing for them.
`occamyd` reads `ca_file` when a session starts.

A refused certificate fails the session, and `occamyd`'s log names the
certificate's fingerprint, ready for `pins`.

### The clipboard

| Key | Default | Meaning |
|-----|---------|---------|
| `disable_copy` | `false` | Keep the remote's clipboard from the session's viewers: text copied on the remote stays there |
| `disable_paste` | `false` | Keep the viewers' clipboard from the remote: text copied on a viewer's side does not reach it |

A viewer can still type the text. Both keys are for vnc and rdp only.
In a terminal, the client draws the text and copies it itself, and a
paste reaches the terminal as typing.

## Backends

`occamyd` connects to each protocol with a client of its own, its
backend. This section says what each one does, and which keys and
credentials it takes. Every backend ends a session with `remote-ended`
when the remote ends it, and with `backend-failed` when it cannot
connect or sign in, or the remote's certificate is refused.

### SSH

An ssh connection opens a shell, which the web client draws as a
terminal.

| Key | Default | Meaning |
|-----|---------|---------|
| `host_keys` | required | The only host keys that `occamyd` accepts from the server |
| `insecure_ignore_host_key` | `false` | Accept any host key instead, for demos only, and log its fingerprint as a warning |

Set `host_keys` or `insecure_ignore_host_key`, not both. A host key is a
public key, as `ssh-keyscan` prints it, or its SHA-256 fingerprint, as
`ssh-keygen -l` prints it (`SHA256:…`):

```sh
ssh-keyscan build.internal | ssh-keygen -lf -
```

An ssh connection needs a `username` in `credentials`, unless it
prompts. It signs in with `private_key_file` or a password.

### VNC

A vnc connection signs in with None, VNC Authentication or VeNCrypt.
Of VeNCrypt, it takes X509None, X509Vnc and X509Plain, which run TLS
with the certificate checked as [`tls`](#certificates-tls) says.

- A `username` in `credentials` is for VeNCrypt's Plain, which is then
  chosen over the server's other types. Without one, the owner is asked
  for it if the server wants one.
- The owner is asked for a password that `credentials` does not store.
- The server's screen follows the size of the owner's window, where the
  server supports ExtendedDesktopSize.
- `occamyd` asks for screen updates only while a viewer can see the
  session.
- Clipboard text goes both ways, unless `disable_copy` or
  `disable_paste` closes a direction. The server's cursor is shown.

#### Apple Remote Desktop: `ard`

macOS Screen Sharing signs a user of the Mac in with Apple Remote
Desktop authentication. When the Mac's VNC password is off, it offers no
other type that a vnc connection could use. `ard: true` allows it, with
the Mac account's `username` in `credentials`, and its password from
`credentials` or the owner's prompt:

```yaml
  - id: studio-mac
    protocol: vnc
    ard: true
    target: studio.internal:5900
    credentials: {username: studio, prompt: true}
    allow: {users: [alice]}
```

**The risk.** ARD's key exchange authenticates no server. Anyone between
`occamyd` and the Mac can read the username and the whole password,
which are the Mac account's own, and the session that follows is not
encrypted. `ard: true` also raises what such an attacker gets from any
server on the connection, not only a Mac. The attacker can strip
VeNCrypt from what the server offers and offer ARD alone, and `occamyd`
then sends the whole password. Without `ard`, the most such a downgrade
gets is VNC Authentication's DES response, from the password's first 8
bytes.

Set `ard: true` only for Macs, on networks you trust, such as a VPN to
the Mac. It cannot be combined with a `tls` that requires TLS
(`require`, `ca_file` or `pins`), since ARD runs without TLS.

Without `ard: true`, a Mac that offers only ARD fails the session, and
`occamyd`'s log names the key to set, such as
`ard: true on connection "studio-mac"`. Ad-hoc targets cannot have it.

### RDP

An rdp connection always runs TLS, with the certificate checked as
[`tls`](#certificates-tls) says.

| Key | Default | Meaning |
|-----|---------|---------|
| `security` | `nla` | How the connection signs in, as the next table says |
| `disable_gfx` | `false` | Do not ask for RDP's graphics pipeline (GFX), as [Windows hosts](#windows-hosts) need |

| `security` | What it does |
|------------|--------------|
| `nla` | NLA (CredSSP with NTLMv2) over TLS, the default |
| `nla-ext` | NLA with the early user authorization result only |
| `tls` | TLS without NLA, which sends the password to whichever server passes the certificate check |
| `any` | Offers TLS and NLA, and lets the server choose |

Standard RDP security, without TLS, is never used. NLA signs in with
NTLMv2, not Kerberos. The names follow Guacamole's.

**Credentials.** The connection signs in with `username` and the
password. A `username` of the form `DOMAIN\user` signs in to that
domain, and `\user` as the user alone. With NLA, the owner is asked for
what `credentials` does not store: the password, and the username and
domain if no `username` is stored. A username that the owner gives at
the prompt can name the domain the same way. Without NLA
(`security: tls`), nothing is asked, and a missing username or password
leaves the server's sign-in screen to the owner.

**The screen.** The server's desktop takes the size of the owner's
window, from 200 to 8192 pixels a side and at most 2²⁵ pixels in all.
It follows later resizes through the Display Control channel, where the
server has it. Viewers scale a desktop that keeps another size. While
no viewer can see the session, `occamyd` asks the server to stop
drawing.

**Drawing.** Through GFX, `occamyd` takes surfaces, fills, copies, and
bitmaps in the uncompressed, planar, RemoteFX and RemoteFX Progressive
codecs. It does not take ClearCodec, the bitmap cache, or AVC (H.264),
which it does not ask for. Without GFX, it takes bitmap updates in
interleaved RLE and planar.

**Input and the clipboard.** Keys go as scancodes by their physical key,
text as Unicode keyboard events, and the pointer and wheel as mouse
events. Clipboard text goes both ways through the clipboard channel,
unless `disable_copy` or `disable_paste` closes a direction. The owner
and the viewers who may type can paste.

#### Windows hosts

Set `disable_gfx: true` on a connection to a Windows host. Under GFX,
Windows draws nearly all of its screen with ClearCodec and its bitmap
cache, which `occamyd` does not decode, so the screen stays nearly
black. With `disable_gfx: true`, the server sends plain bitmap updates,
and the whole desktop draws. `occamyd` logs a warning that names
`disable_gfx: true` at the first part of a session's screen it cannot
draw.

An ad-hoc rdp target always asks for GFX, since only a connection can
set `disable_gfx`. Reach a Windows host through a connection:

```yaml
  - id: windows
    protocol: rdp
    target: 10.0.4.20
    disable_gfx: true
    tls: {pins: ["3F:A1:07:5C:92:E4:6B:D8:21:0A:F3:8E:57:C6:19:B4:E2:7D:40:95:A8:1C:6F:D3:0B:72:E9:58:34:AC:66:1F"]}
    credentials: {username: 'CORP\alice', password_file: /run/secrets/windows-password}
    allow: {users: [alice]}
```

Windows signs its RDP certificate itself unless it is given one, so pin
the certificate by its fingerprint. The first session that `occamyd`
refuses logs the fingerprint.

#### xrdp

xrdp speaks no NLA. Offered NLA alone, it chooses standard RDP security,
which `occamyd` refuses. Set `security: tls` (or `any`) on an xrdp
connection. The session's error in `occamyd`'s log names the key to set,
such as `security: tls on connection "lab-desktop"`.

With `security: tls`, the password goes to the server inside TLS, so
`tls` must check the certificate: pin a self-signed one by its
fingerprint. A connection with `security: tls` or `any` and
`tls: {insecure_skip_verify: true}` may send the password to whoever is
in between, and `occamyd` warns of it by name at start.

### Kubernetes pods

A kubernetes connection opens a terminal in a pod's container through
the API server, as `kubectl exec -it` does.

```yaml
  - id: web-shell
    protocol: kubernetes
    target: k8s.internal:6443
    kubernetes: {namespace: web, pod: web-0, container: app}
    tls: {ca_file: /etc/occamy/k8s-ca.crt}
    credentials: {token_file: /run/secrets/k8s-token}
    allow: {groups: [ops]}
```

| Key | Default | Meaning |
|-----|---------|---------|
| `kubernetes.namespace` | `default` | The pod's namespace |
| `kubernetes.pod` | required | The pod's name |
| `kubernetes.container` | the pod's only container | The container's name, required for a pod with more than one |
| `kubernetes.command` | `bash`, or `sh` where the image has no `bash` | What runs, one element per argument, such as `[/bin/ls, -l]` |
| `kubernetes.attach` | `false` | Attach to the container's main process instead of running a command, as `kubectl attach -it` does |
| `kubernetes.tty` | `true` | Run the command in a terminal |

The names must be valid Kubernetes names: lower-case letters, digits
and `-`, and `.` in a pod's name. The API server refuses a stream to a
pod with several containers when `container` is unset, since it does not
read kubectl's default-container annotation. `attach` runs no command,
so set `attach` or `command`, not both. An attached container needs
`stdin: true` in its spec, and `tty: true` unless `tty` is `false`.
Without a terminal, the command's output and errors show together, and
resizes are not sent.

**Credentials.** `occamyd` sends a bearer token, from `token_file` or
`token_env`, or presents a client certificate, from `client_cert_file`
and `client_key_file`. It never sends a `username` or a password. With
`prompt: true`, the owner is asked for a token, and an empty answer
sends none. `occamyd` sends the token only once the API server's
certificate has been accepted.

A ServiceAccount token works on every cluster, managed ones included.
`kubectl -n web create token occamy --duration=24h` makes one that
expires, and a Secret of type `kubernetes.io/service-account-token`
holds one that does not. `occamyd` reads the token file as each session
starts, so the next session picks up a rotated token.

`occamyd` runs no credential program, so that a session never starts a
process on the server. It does not take:

- kubeconfig files: name the API server in `target`, its CA in `tls`,
  and the token or certificate in `credentials` instead;
- exec credential plugins, such as EKS's `aws eks get-token` or GKE's
  `gke-gcloud-auth-plugin`;
- a kubeconfig's `auth-provider` entries;
- an API server behind a path prefix, such as a cluster that Rancher
  proxies at `https://rancher.example/k8s/clusters/c-m-abc12`. `occamyd`
  asks for `/api/v1/…` at the root of `target`.

**RBAC.** The token's or the certificate's user needs `get` and `create`
on `pods/exec`, or on `pods/attach` with `attach`, in the pod's
namespace. API servers authorize the WebSocket upgrade, an HTTP `GET`,
as `get`, and from Kubernetes 1.35 they also check `create`. Grant both.
A minimal Role and RoleBinding for a ServiceAccount:

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

`occamyd` needs no other permission. It does not list or read pods.

**The stream.** `occamyd` speaks the `v5.channel.k8s.io` WebSocket
protocol of Kubernetes 1.30 and later, and falls back to
`v4.channel.k8s.io` for older API servers. The command's exit ends the
session with `remote-ended`, whatever its status. A refused request, or
an API server that cannot be reached, ends it with `backend-failed`.

## Ad-hoc targets

With ad-hoc targets on, a signed-in user can connect to a host that is
not a connection, within `adhoc_allowlist`.

| Key | Default | Meaning |
|-----|---------|---------|
| `allow_adhoc_targets` | `false` | Let signed-in users connect to targets that are not connections |
| `adhoc_allowlist` | none | Where ad-hoc targets may point, and on which ports, required with `allow_adhoc_targets` |

An entry of `adhoc_allowlist` is one of these, with an optional `:port`
or `:first-last` after it:

| Entry | Example |
|-------|---------|
| A CIDR range | `10.0.0.0/8`, `10.0.0.0/8:22` |
| An IP address | `10.0.4.20:3389` |
| A host name | `build.internal` |
| `*.` and a domain | `*.lab.internal:5900-5910` |
| An IPv6 range or address, in brackets with ports | `[fd00::/8]:22` |

An entry without ports allows the protocol's own: 22 for ssh, 5900 to
5999 for vnc, and 3389 for rdp. A target is allowed on a port that an
entry allows when:

- it is an IP address in a listed range, or a listed address;
- it is a listed host name, or ends in a `*.` entry's domain
  (`a.lab.internal` for `*.lab.internal`, but not `lab.internal`
  itself);
- it is another host name, every address of which lies in a listed
  range.

`occamyd` resolves every host name and dials the address it checked, so
a second lookup cannot lead elsewhere. It refuses loopback, link-local,
unspecified and multicast addresses, and the cloud metadata services at
`169.254.169.254`, `fd00:ec2::254` and `100.100.100.200`, whatever range
or name leads to them. An entry that is that very address allows it,
such as `127.0.0.1:2222`. `occamyd`'s own `listen` and `debug.address`
are refused too, at their ports. A listener on no host, such as
`:5636`, stands for every address of the host.

A range that holds the host's own addresses still reaches the host's
other services on the ports it allows, such as a database on a private
address. List ranges that leave the host out, or the host's addresses
with the ports you mean.

**Each protocol.** A user opens an ad-hoc target with
`POST /api/v1/sessions` ([API](api.md#ad-hoc-targets)), with any
protocol but kubernetes, whose pod and credentials only a connection
names. `occamyd` always asks the user for the credentials.

- **ssh.** The request needs `host_keys`, or
  `insecure_ignore_host_key: true`, as a connection does.
- **vnc.** If the server offers VeNCrypt, `occamyd` checks its
  certificate against the system's roots and the name or address the
  target was given. An ad-hoc target cannot require TLS, though. An
  attacker in between can
  strip VeNCrypt from the server's offer and capture the VNC
  Authentication response, from which an offline search can recover a
  weak password. Reach targets across networks you do not trust through
  a connection whose `tls` requires TLS.
- **rdp.** `occamyd` checks the certificate against the system's roots
  and the name or address the target was given, and signs in with NLA
  alone. The user is asked for the password, and for the username and
  domain when the request has no `username`. A request's `username` of the form `DOMAIN\user`
  names the domain. An ad-hoc rdp target always asks for GFX. A server
  with a self-signed certificate, an xrdp server, or a
  [Windows host](#windows-hosts) needs a connection.

## Limits: `limits`

| Key | Default | Meaning |
|-----|---------|---------|
| `limits.sessions` | `1000` | Sessions that `occamyd` runs at once, whoever owns them |
| `limits.sessions_per_user` | `10` | Sessions that one user owns at once |
| `limits.viewers_per_session` | `50` | Viewers that a session takes besides its owner, who can always join |
| `limits.shares_per_session` | `100` | Shares of a session that have not expired |
| `limits.decode_cores` | half of `GOMAXPROCS`, at least 1 | Cores of decoding that all RDP sessions share |

`0` means the default. A user counts by name and source, so a trusted
issuer's users count apart from `occamyd`'s own. `limits.sessions` holds
even when a trusted issuer names new users without end.
[The HTTP API](api.md#limits) lists the answer at each limit.

**Decoding.** An RDP session may take about a core decoding what its
server sends. So one user's 10 sessions to hostile servers, such as
ad-hoc targets they chose, could take about 10 cores. `decode_cores`
caps all RDP sessions together at about that many cores. By default it
leaves half of the cores to encoding for viewers and to the rest of
`occamyd`. Raise it where many sessions show full-screen motion, which
takes about 0.8 of a core each at 1920 × 1080 and 30 frames a second.
Lower it on a host that does other work.

A session over its share waits, with its server's updates queued behind
it, and nothing is dropped. Sessions that want more than the cap share
it evenly: of n such sessions, each gets at least an nth, or a core if
that is less. A session that wants less gets all it wants, and what it
leaves goes to the others. The share is a session's, not a user's, so a
user's 10 sessions have 10 shares. Where users are not trusted with that
much, lower `limits.sessions_per_user`, or keep `allow_adhoc_targets`
off.

The cap counts what sessions decode and draw, and what they hand to
viewers, not what they read and ignore. A server that floods a session
with what the client does not ask for, such as an update of an unknown
kind, can keep that session's reader busy outside the cap.

## Streams: `streams`

| Key | Default | Meaning |
|-----|---------|---------|
| `streams.viewer_timeout` | `30s` | How long, at least `1s`, a viewer may leave its frames unacknowledged, or one write to it block, before it gets `close` `too-slow` |

**Memory.** Each visible viewer holds the screen it was last sent until
it acknowledges a newer one. A viewer that may type also holds the
screen of its last input, hidden or not. The largest terminal, 1000 by
500 cells, takes about 11 MiB a screen. `viewer_timeout` drops a viewer
that stops reading, but a viewer that keeps acknowledging keeps its
screens for as long as it stays. At the defaults, one user's 10
sessions, with their owners and 50 viewers each, may hold 510 such
screens, about 5.5 GiB, or twice that if every viewer may type. Where
users are not trusted with that much, lower
`limits.viewers_per_session` or `limits.sessions_per_user`.

## How occamyd checks the file

`occamyd` checks the whole file at start. It reports every problem with
its line and key path, and does not start until the file is right. This
file has three:

```yaml
version: 1
auth:
  signing_key_file: /var/lib/occamy/signing.key
  users:
    - name: alice
      password_hash: "$2y$12$…" # a whole hash
connections:
  - id: desktop
    protocol: vnc
    target: desktop.example.com
    credentails: {password_env: DESKTOP_PASSWORD}
    allow: {users: [carol]}
```

```
config: 3 problems:
	line 11: connections[0].credentails: unknown key, did you mean credentials?
	line 8: connections[0].credentials: no credential source: set one of password_file, password_env, private_key_file or prompt: true
	line 12: connections[0].allow.users[0]: no static user is named "carol"
```

Besides the rules in the tables, `occamyd` checks that:

- every file the configuration names exists, is a regular file, and can
  be opened, without reading a secret;
- a trusted issuer's public key parses;
- every environment variable it names is set and not empty;
- addresses are `host:port` with a numeric port, and origins and OIDC
  URLs parse;
- user, group and issuer names are not empty and have no spaces or
  control characters;
- without `auth.oidc` and `auth.trusted_issuers`, every name in an
  `allow` list is a static user, and every group is a static user's.
  With either, users and groups also come from tokens, so the file need
  not list them;
- with `auth.oidc.require_known_users`, the file admits someone;
- the file holds one YAML document.

## Upgrading

A release can rename or remove a key. `occamyd` then refuses the old
key with what to write instead, so read its errors after an upgrade.
[Deploy](deploy.md#upgrade) shows how to move a running server to a new
release.

### v0 files

The v0.x servers read files without a `version` key. Version
1.0.0-beta.2 removed them, together with guacd, which served all their
sessions. Such a file is refused:

```
config: the file has no version: 1; occamyd reads only version 1 files since v1.0.0-beta.2, and serves no v0 file: write a version 1 file, as docs/configuration.md describes
```

Any `version` other than `1` is an error too. A v0 file's keys map to
these:

| v0 | Version 1 |
|----|-----------|
| `address` | `listen` |
| `mode: debug` | `debug.address`, a listener of its own |
| `auth.jwt_secret`, `auth.jwt_alg` | `auth.signing_key_file`, an Ed25519 key, with users signing in as [`auth`](#sign-in-auth) says |
| `client` | `web_client` |
| `guacd` | none: `occamyd` serves ssh, vnc, rdp and kubernetes itself |
| a v0 token's target | a connection in `connections`, or an [ad-hoc target](#ad-hoc-targets) |

### Files written for guacd

Version 1.0.0-beta.2 also removed guacd, and telnet, which only guacd
served. `occamyd` refuses a version 1 file that still configures them,
and names each key with what replaced it:

- `guacd`, a connection's `backend` and `adhoc_backends`: remove them.
  `occamyd` serves each protocol itself. A vnc or rdp connection that
  went through guacd now:
    - has its certificate checked as `tls` says, against the system's
      roots without `tls`, in place of guacd's `ignore-cert` and
      `cert-fingerprints`. Pin a self-signed certificate with `tls.pins`,
      by the fingerprint that the refusal logs;
    - has `security` in place of guacd's `security`, NLA by default. An
      xrdp target needs `security: tls`;
    - has `disable_gfx` in place of guacd's `disable-gfx`, and asks for
      the graphics pipeline unless it is set;
    - has `disable_copy` and `disable_paste` in place of guacd's
      `disable-copy` and `disable-paste`.
- `legacy_v0`: the v0 routes are gone. Clients use API v1 and protocol
  v1.
- A telnet connection: telnet is gone.

`occamyd` records no sessions. A file that sets one of guacd's
recording parameters under `guacd.params`, `recording-path` or another
`recording-` or `typescript-` parameter, is refused rather than served
unrecorded. guacd's `disable-copy` and `disable-paste` are refused too,
each with the connection key that replaces it, so that no clipboard
opens that the file closed. A file whose rdp connection went through
guacd, with recording on and copying off, is refused with:

```
config: 4 problems:
	line 7: guacd: guacd was removed in v1.0.0-beta.2: remove guacd, and occamyd serves ssh, vnc, rdp and kubernetes with its own backends
	line 10: guacd.params.rdp.recording-path: occamyd records no sessions until a later beta: guacd recorded them, and was removed in v1.0.0-beta.2; occamyd does not serve unrecorded what a file wants recorded, so remove the parameter to serve the sessions unrecorded, or keep v1.0.0-beta.1 until recording returns
	line 11: guacd.params.rdp.disable-copy: guacd was removed in v1.0.0-beta.2: set disable_copy: true on each vnc and rdp connection whose clipboard stays with the remote; occamyd does not open a clipboard that a file closes
	line 15: connections[0].backend: guacd was removed in v1.0.0-beta.2: remove backend, and the connection's protocol is served by occamyd's own backend
```

Set `disable_copy` or `disable_paste` on the connections in place of
guacd's parameters. Remove the recording parameters to serve the
sessions unrecorded, or keep version 1.0.0-beta.1, which records them
through guacd.
