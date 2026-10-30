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

Occamy is experimental. Its authentication model was built for a demo and
has known gaps, tracked in
[specs/004-security-hardening.md](specs/004-security-hardening.md). Until
those are closed, an operator should know the following:

- **Tokens carry the remote credentials.** `/api/v0/login` puts the
  remote host, username and password into a signed but unencrypted JWT.
  Anyone holding a token can read them, so treat tokens as secrets.
- **Tokens travel in URLs.** The web client passes the token in the
  WebSocket URL. `occamyd` does not log query strings, but a reverse
  proxy in front of it may; keep them out of its access log.
- **No TLS.** `occamyd` speaks plain HTTP and WebSocket. Put a
  TLS-terminating reverse proxy in front of it.
- **The login endpoint is open.** It signs a token for any host and port
  without authenticating the caller, which makes `occamyd` a bridge into
  every network it can reach. Restrict access to it, or run it only on
  trusted networks.
- **Change the defaults.** `conf.yaml` ships `jwt_secret: occamy` and
  `mode: debug`, and debug mode serves `/debug/pprof`. Set a random secret
  and `mode: release`.
- **Demo credentials are public.** The servers in `docker/demo/` exist
  for local testing only.
- **The image runs as root.**
