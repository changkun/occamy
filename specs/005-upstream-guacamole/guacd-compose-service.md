---
title: guacd service in the demo stack
status: validated
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
     restart: always
   ```

   Use the fully qualified name: Podman, now the default engine, refuses
   unqualified short names unless aliases are configured. Do not publish
   port 4822. `occamy` reaches it by service name.
2. Add `guacd` to `occamy.depends_on`.
3. Check that the image is published for linux/arm64 as well as amd64
   (`podman manifest inspect docker.io/guacamole/guacd:1.6.0`). If arm64
   is missing, note it in the compose file and in
   [006](../006-demo-stack-refresh.md)'s multi-arch criterion rather than
   building guacd from source here.
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
