---
title: SSH sessions after the first one
status: archived
track: server
depends_on: []
affects:
  - guacamole/src/libguac/client.c
effort: small
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# SSH sessions after the first one

## Overview

Within one `occamyd` process only the first SSH session works. Once it ends,
every later SSH session connects to the server but sends the client nothing
(issue #22). VNC is unaffected. This makes SSH unusable in any long-running
deployment.

If [005](005-upstream-guacamole.md) adopts upstream guacd, the fork code this
spec touches is deleted and the spec becomes moot. It is worth doing only if
the fork stays in use for a while.

## Current State

Each new session in `server/session.go` `NewSession` allocates a libguac
client and calls `(*lib.Client).LoadProtocolPlugin`, which reaches
`guac_client_load_plugin` in `guacamole/src/libguac/client.c`. That function
`dlopen()`s `libguac-client-ssh.so` with `RTLD_LAZY`, and `guac_client_free`
`dlclose()`s the handle when the session ends.

Observed during the 2026 revival (see [001](001-status-quo.md)): when the
second SSH session starts, GLib logs

```
GLib-GObject-CRITICAL: cannot register existing type 'PangoFontMap'
GLib-GObject-CRITICAL: cannot register existing type 'PangoCairoFontMap'
```

followed by `User is not responding.` The SSH plugin is the only one that
links Pango (terminal rendering).

Working hypothesis: closing the last SSH client drops the plugin's reference
count to zero, so the loader unmaps the plugin and Pango with it, while
libgobject stays loaded and keeps Pango's type registrations. When Pango is
loaded again, its static type IDs start from zero and re-registration fails.

## Architecture

Keep protocol plugins, and the libraries they pull in, resident for the
lifetime of the process. Only three plugins exist, so never unloading them
costs little.

Options:

- **A. `RTLD_NODELETE`.** Add the flag to the `dlopen` call in
  `guac_client_load_plugin`. The handle can still be `dlclose()`d, but the
  object is never unmapped. One-line change; recommended.
- **B. Plugin cache.** Keep one handle per protocol in a process-wide table
  and skip `dlclose` in `guac_client_free`. More code, same effect.
- **C. Link Pango into `occamyd`.** Keeps Pango resident without touching
  libguac, but couples the Go binary to SSH-only dependencies.

## Components

### Plugin loading

- `guacamole/src/libguac/client.c` `guac_client_load_plugin`: open with
  `RTLD_LAZY | RTLD_NODELETE`.
- The `dlclose` in `guac_client_free` stays; with `RTLD_NODELETE` it only
  drops the reference.

## Testing Strategy

- End to end: start one `occamyd`, run SSH, SSH, VNC, SSH sessions in
  sequence against the demo servers, and assert each receives `size`,
  `img` and `sync`. Add the sequence to `tests/e2e_test.go`, the end-to-end
  suite from [006](006-demo-stack-refresh.md), which CI runs on every push.
- Confirm the GLib criticals are gone from the `occamyd` log.
- If the hypothesis is wrong (the criticals persist with `RTLD_NODELETE`),
  return this spec to `vague` and investigate Pango initialization in
  `guacamole/src/protocols/ssh/terminal_display.c`.

## Acceptance Criteria

- [ ] Ten sequential SSH sessions in one process all render a terminal.
- [ ] Mixed VNC and SSH sequences keep working.
- [ ] Issue #22 can be closed.

## Outcome

Archived on 2026-10-29 without implementation, as moot. `occamyd` no longer
loads libguac plugins in-process. Sessions go through upstream guacd
([guacd-sessions](005-upstream-guacamole/guacd-sessions.md), #62), which forks
a process per connection, and
[remove-libguac](005-upstream-guacamole/remove-libguac.md) deleted the fork this
spec would have patched. `TestE2E/ssh-again` opens a second SSH session in CI,
and #22 is closed.
