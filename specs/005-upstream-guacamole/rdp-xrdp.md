---
title: RDP through guacd, verified against xrdp
status: validated
track: server
depends_on:
  - specs/005-upstream-guacamole/guacd-sessions.md
  - specs/006-demo-stack-refresh.md
affects:
  - docker/demo/rdp/
  - docker/docker-compose.yaml
  - tests/
  - README.md
  - specs/005-upstream-guacamole.md
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# RDP through guacd, verified against xrdp

## Goal

Bring RDP back. guacd 1.6 ships it, so the work is a demo server, an
end-to-end case, and a check against a current Windows host (#32).

## What to do

1. `docker/demo/rdp/Dockerfile`: Debian trixie with `xrdp`, `xorgxrdp`
   and a light session (xterm or a minimal window manager). Create a demo
   user whose password comes from the environment, as the 006 VNC and SSH
   images do. Expose 3389.
2. `docker/docker-compose.yaml`: add the `rdp` service built from it.
3. Defaults: `conf.yaml` and `docker/occamy.yaml` keep
   `guacd.params.rdp.ignore-cert: "true"` (xrdp uses a self-signed
   certificate). Leave `security` at guacd's default unless xrdp needs
   otherwise; if it does, record why in the config comment.
4. `tests/`: add an `rdp` case to the end-to-end table (log in, connect,
   expect `size`, `img`, `sync`).
5. `Login.vue` and the README: RDP host default `rdp:3389`; status table
   row "RDP: through guacd, verified against xrdp".
6. Connect once by hand to a current Windows host (Windows 11 or Server
   2022/2025) with NLA enabled, and record the result and any parameters
   it needed in [005](../005-upstream-guacamole.md)'s acceptance
   criteria.

## Tests

- End to end: `TestE2E/rdp` passes in CI.
- Manual: the Windows check above, recorded in the parent spec.

## Boundaries

- No changes to `server/` beyond config defaults.
- No native RDP work ([008](../008-native-architecture.md)).
