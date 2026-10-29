---
title: guacd service in the demo stack
status: complete
track: server
depends_on:
  - specs/006-demo-stack-refresh.md
affects:
  - docker/docker-compose.yaml
  - README.md
effort: small
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# guacd service in the demo stack

## Goal

Run upstream guacd 1.6 next to `occamy` in the refreshed compose stack
from [006](../006-demo-stack-refresh.md), so that
[guacd-sessions](guacd-sessions.md) can be verified end to end the moment
it lands.

## What to do

1. `docker/docker-compose.yaml`: add

   ```yaml
   guacd:
     image: docker.io/guacamole/guacd:1.6.0
     healthcheck:
       test: ["CMD", "nc", "-z", "127.0.0.1", "4822"]
       interval: 1s
       retries: 30
   ```

   Use the fully qualified name: Podman, now the default engine, refuses
   unqualified short names unless aliases are configured. Do not publish
   port 4822. `occamy` reaches it by service name. The image defines no
   healthcheck and ships no bash (Alpine), so probe with busybox `nc`.
   Like the other services since [006](../006-demo-stack-refresh.md), it
   has no restart policy, so a crash fails the end-to-end tests.
2. Add `guacd` to `occamy.depends_on` with `condition: service_healthy`,
   as for `vnc` and `ssh`.
3. No multi-arch work is needed: `docker.io/guacamole/guacd:1.6.0` is
   published for linux/amd64 and linux/arm64 (checked with
   `podman manifest inspect` on 2026-10-29).
4. README "Demo" section: one sentence saying the stack now includes guacd
   and that, until [guacd-sessions](guacd-sessions.md) lands, `occamy`
   does not use it yet.

guacd, not `occamy`, opens the connection to the remote desktop, so the
hostnames in tokens (`vnc:5900`, `ssh:22`) must resolve from guacd's
container. In this stack they all share one network, so service names
work.

## Tests

- `make run` brings up `guacd` healthy; `podman compose logs guacd`
  shows `Listening on host 0.0.0.0, port 4822`.
- The existing 006 end-to-end test still passes; nothing uses guacd yet.

## Boundaries

- No `occamy` code or config changes.
- No RDP server (that is [rdp-xrdp](rdp-xrdp.md)).

## Outcome

**Summary.** Implemented directly in `5868291` (`docker: run upstream
guacd 1.6 in the demo stack`). Drift: Minimal. Every item shipped as
written.

**What shipped.**

- The `guacd` service in `docker/docker-compose.yaml`:
  `docker.io/guacamole/guacd:1.6.0`, an `nc -z` healthcheck, no published
  port and no restart policy. `occamy.depends_on` waits for it to be
  healthy.
- A README sentence saying the stack runs guacd, which occamy does not use
  yet.
- Verified locally on linux/arm64 with Podman:
  - `make run` brings guacd up healthy, and its log shows `Listening on
    host 0.0.0.0, port 4822`;
  - from the guacd container, `nc -z` reaches `vnc:5900` and `ssh:22`;
  - `make e2e` still passes.

**Design evolution.** None.

**Not implemented.** None.

**Unspecified work.**

- A comment on the service saying occamy does not use guacd yet, and why
  the hosts in tokens must resolve from guacd (improvement).

**Decisions, surprises, follow-ups.**

- Follow-up: [guacd-config](guacd-config.md) points `occamy` at
  `guacd:4822` through `docker/occamy.yaml`.

