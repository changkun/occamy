---
title: RDP through guacd, verified against xrdp
status: complete
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-sessions.md
  - specs/006-demo-stack-refresh.md
affects:
  - docker/demo/rdp/
  - docker/docker-compose.yaml
  - conf.yaml
  - docker/occamy.yaml
  - client/occamy-web/src/components/Login.vue
  - tests/
  - README.md
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# RDP through guacd, verified against xrdp

## Goal

Bring RDP back. guacd 1.6 ships it, so the work is a demo server and an
end-to-end case. It needs [guacd-sessions](guacd-sessions.md) and runs
before [remove-libguac](remove-libguac.md), which leaves the README's RDP
row alone. The check against
a current Windows host (#32) is a manual acceptance item on
[005](../005-upstream-guacamole.md), not part of this task.

## What to do

1. `docker/demo/rdp/Dockerfile`: Debian trixie with `xrdp`, `xorgxrdp`
   and a light session (xterm or a minimal window manager). Create a demo
   user whose password comes from the environment, as the 006 VNC and SSH
   images do. Expose 3389.
2. `docker/docker-compose.yaml`: add the `rdp` service built from it,
   with a port healthcheck, and add it to `occamy.depends_on` with
   `condition: service_healthy`. The healthcheck looks for the listening
   socket in `/proc/net/tcp*` rather than connecting, because xrdp logs
   errors for every connection that does not speak RDP.
3. Defaults: `conf.yaml` and `docker/occamy.yaml` keep
   `guacd.params.rdp.ignore-cert: "true"` (xrdp uses a self-signed
   certificate). `security` stays at guacd's default, which negotiates
   TLS with xrdp.
4. `tests/`: add an `rdp` entry to `credentials` in
   `tests/occamy_test.go` and an `rdp` case to the `TestE2E` table in
   `tests/e2e_test.go` (log in, connect, expect `size`, `img`, `sync`).
5. `Login.vue` already defaults RDP to `rdp:3389` with user and password
   `occamy`; use that demo user. README: add RDP to the demo credentials
   table, drop the "no RDP demo server yet" line, and set the status table
   row to "RDP: through guacd, verified against xrdp".

## Tests

- End to end: `TestE2E/rdp` passes in CI.

## Boundaries

- No changes to `server/` beyond config defaults.
- No native RDP work ([008](../008-native-architecture.md)).

## Outcome

**Summary.** Implemented directly:

- `f68bccc` (`docker: add an xrdp demo server`), built and checked against
  guacd by a parallel agent;
- `c661464` (`docker: probe the rdp listener without connecting`);
- `5e81115` (`ci: let dependabot update the rdp demo image`);
- `1017613` (`tests: open an RDP session end to end`).

Drift: Minimal. All 5 steps shipped. The healthcheck is a listen check
rather than a connect, and the task ran before remove-libguac instead of
after it. The body above is corrected for both.

**What shipped.**

- `docker/demo/rdp/`:
  - `debian:trixie-slim` with `xrdp` 0.10.1, `xorgxrdp` 0.10.2 and `xterm`;
  - the user `occamy`, whose password comes from `RDP_PASSWORD`;
  - console logging instead of syslog;
  - `startwm.sh` reduced to `exec xterm`;
  - `start.sh` runs `xrdp-sesman` and `xrdp` in the foreground, line
    buffered.
  - The image is 385 MB on arm64 (the vnc image is 342 MB). amd64 builds
    and starts under emulation.
- `docker/docker-compose.yaml`: the `rdp` service, with a healthcheck on
  the listening socket, and `rdp` in `occamy.depends_on`.
- `.github/dependabot.yml`: `/docker/demo/rdp`.
- Config: no change. `conf.yaml` and `docker/occamy.yaml` already set
  `rdp.ignore-cert` ([guacd-config](guacd-config.md)), and xrdp needs no
  other parameter.
- `tests/`: the `rdp` credential and the `TestE2E/rdp` case.
- README:
  - the RDP status row reads "Through guacd, verified against xrdp";
  - the demo table has `rdp:3389` with `occamy`/`occamy`;
  - the "no RDP demo server" line is gone.
- CHANGELOG: RDP sessions under Added.
- Verified on podman:
  - `make build run e2e` passes vnc, vnc-again, ssh, ssh-again and rdp;
  - `OCCAMY_PRESSURE_N=10` passes with RDP in the storm;
  - the xrdp log stays free of errors while idle.

**Design evolution.**

- **Healthcheck.** A `/dev/tcp` connect, as for `vnc` and `ssh`, made xrdp
  log 7 ERROR lines per probe, every second for the life of the container.
  The check greps `/proc/net/tcp` and `/proc/net/tcp6` for port 3389
  (`0D3D`) in state LISTEN (`0A`) instead. That is the same signal, and
  xrdp never sees it.
- **Order.** The task depended on remove-libguac only so that the two
  would not race on the README. remove-libguac had not started, so RDP
  went first and the edge became guacd-sessions, which RDP actually
  needs. remove-libguac's README step now leaves the RDP row alone.

**Not implemented.** None. The manual check against a current Windows
host (#32) stays on [005](../005-upstream-guacamole.md).

**Unspecified work.**

- The dependabot entry (scaffolding).
- `stdbuf -oL` in `start.sh` (improvement). xrdp and sesman log through
  stdio and fork per connection. Without line buffering their lines show
  up late and again in every child.

**Decisions, surprises, follow-ups.**

- **`security` modes** (agent's run against guacd 1.6.0):

  | `security` | Result |
  |---|---|
  | unset, `any`, `tls` | work (negotiation selects TLS 1.3) |
  | `rdp` | fails with 515; xrdp logs "MAC checksum error for non-FIPS PDU" |
  | `nla` | fails with 769; xrdp has no NLA |

  No default is needed. A Windows host with NLA will need `security: nla`
  or `any` in its token or `guacd.params`; record that with #32.
- **One session per user.** xrdp gives each user one X session. A second
  RDP connection for the same user takes it over, and guacd logs "Manually
  logged off." for the first. That is why the e2e case runs once. Sharing
  goes through guacd's join, which occamy uses for equal claims, so the
  pressure storm opens one RDP connection.
- **Idle clients.** guacd drops a client that sends nothing back for about
  15 s ("User is not responding."). The first frame arrives 2–3 s after
  `ready`. The e2e reader answers `sync` and leaves as soon as it has
  `size`, `img` and `sync`.
- **Harmless noise.** Expected log noise includes:
  - xorgxrdp `dbus-core` errors (no system bus);
  - `dynamic_monitor_open_response` errors;
  - TS_UD_HEADER warnings;
  - about 10 ERROR lines from xrdp when a session is torn down.
- Follow-up: None beyond #32.
