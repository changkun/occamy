# Configuration

`occamyd` reads one YAML file, `./conf.yaml` unless `-conf` names
another. The file comes in two versions:

- **v0** has no `version` key. It is the format of the released v0.x
  servers and of the [conf.yaml](../conf.yaml) in this repository, and the
  only one `occamyd` serves today (see [v0 files](#v0-files)).
- **v1** starts with `version: 1`. It configures the API v1 server: sign-in,
  the connection registry, TLS and the debug listener. `internal/config`
  parses and validates it, but `occamyd` does not serve it yet; the HTTP
  server of
  [api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)
  will start from it.

Any other `version` is an error, and so is `version: 0`: a v0 file has no
`version` key.

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
| `tls.cert`, `tls.key` | unset | Certificate and key files, PEM. `occamyd` terminates TLS when both are set; set both or neither. Without them, put a reverse proxy that terminates TLS in front |
| `debug.address` | unset (off) | Address of the debug listener with expvar and pprof. Keep it on the loopback interface; it may not be the `listen` address |
| `origins` | none | Origins, `scheme://host[:port]`, whose web pages may open streams besides `occamyd`'s own. Written in lower case without a trailing slash, as browsers send them |
| `legacy_v0` | `false` | Serve the v0 routes, `/api/v0/login` and `/api/v0/connect`, for the Guacamole-based web client. They are removed at v1.0 |

### Sign-in: `auth`

| Key | Default | Meaning |
|-----|---------|---------|
| `auth.signing_key_file` | required | The Ed25519 key, PKCS #8 PEM, that signs `occamyd`'s tokens. If the file does not exist, `occamyd` creates it on first start, so its directory must exist |
| `auth.access_token_lifetime` | `15m` | How long an access token lasts, a duration with a unit (`15m`, `1h30m`) between `1m` and `12h` |
| `auth.trusted_issuers[].issuer` | required | Name of a third party, such as a gateway, whose tokens `occamyd` accepts. Unique |
| `auth.trusted_issuers[].public_key_file` | required | That issuer's Ed25519 public key, PKIX PEM |
| `auth.users[].name` | required | A static user, who signs in with a password. Unique |
| `auth.users[].password_hash` | required | bcrypt hash of the password, cost 10 or more. `htpasswd -nbBC 12 "" 'the password' \| cut -d: -f2` makes one |
| `auth.users[].groups` | none | Groups the user belongs to |
| `auth.oidc` | unset (off) | Sign-in through an OpenID Connect identity provider |
| `auth.oidc.issuer` | required | The provider's issuer URL. https, or http on localhost |
| `auth.oidc.client_id` | required | `occamyd`'s client ID at the provider |
| `auth.oidc.client_secret_file`, `client_secret_env` | unset | The client secret, from a file or an environment variable; at most one. Neither means a public client |
| `auth.oidc.redirect_url` | required | `occamyd`'s callback URL as the provider knows it, ending in `/api/v1/auth/oidc/callback` behind any proxy prefix. https, or http on localhost |
| `auth.oidc.scopes` | `[openid, profile, email]` | Scopes to request; must include `openid` |
| `auth.oidc.subject_claim` | `sub` | The ID token claim that names the user in allow lists |
| `auth.oidc.groups_claim` | unset | The claim that lists the user's groups. Unset, OIDC users have no groups |

### guacd: `guacd`

guacd serves the `vnc`, `rdp` and `telnet` connections until their native
backends replace it.

| Key | Default | Meaning |
|-----|---------|---------|
| `guacd.address` | `localhost:4822` | guacd's address, `host:port` |
| `guacd.params.<protocol>` | none | guacd connection parameters per protocol (`ssh`, `vnc`, `rdp`, `telnet`), such as `ignore-cert: "true"` for `rdp`. A connection's target and credentials override them. `hostname` and `port` come from the target, and secrets (`password`, `passphrase`, `private-key`) are not allowed here |

### The connection registry: `connections`

| Key | Default | Meaning |
|-----|---------|---------|
| `connections[].id` | required | The connection's ID, used in URLs and tokens: 1 to 64 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or digit. Unique |
| `connections[].name` | the ID | Display name |
| `connections[].protocol` | required | `ssh`, `vnc`, `rdp` or `telnet` |
| `connections[].target` | required | `host:port`, or a host alone for the protocol's default port: 22 for ssh, 5900 for vnc, 3389 for rdp, 23 for telnet. IPv6 addresses go in brackets with a port, `[fd00::22]:22` |
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
| `adhoc_allowlist` | none | Where ad-hoc targets may point: CIDR ranges (`10.0.0.0/8`), IP addresses, host names, or `*.` and a domain (`*.lab.internal`). Required when `allow_adhoc_targets` is `true` |

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
- connection IDs, user names and issuer names are unique;
- user, group and issuer names are not empty and have no spaces;
- with neither `auth.oidc` nor `auth.trusted_issuers` configured, every
  name in an `allow` list is a static user, and every group is a group of
  some static user. With either one configured, users and groups also come
  from token claims, so names the file does not list are accepted;
- a file holds one YAML document.

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
