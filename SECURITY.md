# Security Policy

## Supported versions

Occamy is pre-1.0. Security fixes go to `master` and to the next release;
older releases are not patched.

## Reporting a vulnerability

Please do not open a public issue. Report privately through GitHub's
[private vulnerability reporting](https://github.com/changkun/occamy/security/advisories/new),
or email [hi@changkun.de](mailto:hi@changkun.de) if you cannot use GitHub.

Include what you found, how to reproduce it, and the version or commit you
tested. Occamy is maintained by volunteers, so responses are best effort.
You will be credited in the advisory unless you ask otherwise.

## Security notes

A configuration file ([docs/configuration.md](docs/configuration.md))
holds no secrets: it names the files or environment variables that do.
Tokens name a user or a session and carry no remote credentials, and
are signed with an Ed25519 key in `auth.signing_key_file`, created if
missing, readable by its owner only, not a shared secret. `occamyd` has
no default user or password, and refuses a file that lets nobody sign
in. Requests and streams take their tokens in headers or a WebSocket
subprotocol, never in the URL. Sessions open only on the registry's
connections that allow the user, or on ad-hoc targets that
`adhoc_allowlist` covers once `allow_adhoc_targets` is on.

An operator should still know the following:

- **TLS.** `tls.cert` and `tls.key` turn on TLS. Without them,
  `occamyd` speaks plain HTTP and WebSocket: put a TLS-terminating
  reverse proxy in front of it.
- **The debug listener has no authentication.** pprof and expvar are
  served only on it, when `debug.address` is set. Keep it on the
  loopback interface.
- **Ad-hoc targets reach into the network.** With `allow_adhoc_targets`,
  any signed-in user may open a session to whatever `adhoc_allowlist`
  covers, on the ports it allows. List only the ranges and ports meant.
- **Demo credentials are public.** The demo user and the servers in
  `docker/demo/` exist for local testing only.

The 0.x servers' v0 routes, whose tokens carried the remote credentials
in readable claims and whose login signed a token for any host for
anyone ([specs/004-security-hardening.md](specs/004-security-hardening.md)),
were removed in v1.0.0-beta.2.
