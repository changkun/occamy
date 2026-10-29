---
title: Native SSH backend
status: drafted
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/session-core.md
affects:
  - internal/backend/ssh/
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Native SSH backend

## Design Problem

Replace guacd for SSH with a pure-Go terminal backend. Terminal bytes go
to clients, which render them (xterm.js on the web, SwiftTerm in the Swift
app). This is the first native backend and the beta.1 proof that
terminals stay text. Three things need deciding:

- how the server keeps enough terminal state for late joiners;
- how host keys are verified;
- how credentials and prompts reach the backend.

## Context

- `golang.org/x/crypto/ssh` is already a dependency. It provides the
  client, `RequestPty`, `WindowChange`, the Password, PublicKeys and
  KeyboardInteractive auth methods, `knownhosts`, and `agent`.
- v0 renders SSH through libguac's terminal emulator with Pango and Cairo
  into PNG tiles. Repeated sessions crash in-process (#22) until
  [005](../005-upstream-guacamole.md) moves SSH to guacd.
- Protocol v1 carries `term-data`, `term-snapshot`, `term-input`,
  `resize-request` and `prompt` ([protocol-v1](protocol-v1.md)).

## Proposed shape

- The connection is defined in the registry
  ([api-and-server-stack](api-and-server-stack.md)): host, port,
  username, a credential reference (password or private key), the host
  key policy, and `TERM` (default `xterm-256color`).
- Run: dial, authenticate, request a PTY with the owner's size, start a
  shell, then stream stdout and stderr to `Sink.TermData` and
  `term-input` to stdin. `resize-request` from a viewer holding input
  rights becomes `WindowChange`.
- Keyboard-interactive and missing credentials go to the owner through
  `Sink.Prompt`. Answers are never stored.
- Snapshot for late joiners: a byte sequence that, fed into a fresh
  terminal of the same size, reproduces the screen, cursor, attributes and
  alternate-screen state. Clients need no special snapshot logic.

## Options

### Terminal state for snapshots

- **A. Headless emulator in Go.** Feed all output through an emulator
  and serialize its screen on demand. Candidates to evaluate, checking
  maintenance and VT coverage at evaluation time:
  `github.com/charmbracelet/x/vt`, `github.com/hinshun/vt10x` (and its
  forks), or a small in-house emulator.
  - Pros: exact snapshots; enables recording thumbnails and search later.
  - Cons: CPU per byte of output; emulator bugs show up as wrong
    snapshots.
- **B. Bounded replay log.** Keep the output since the last full-screen
  clear or alternate-screen switch, capped at N KiB, and replay it to
  joiners.
  - Pros: no emulator; trivially correct for line-oriented output.
  - Cons: approximate for full-screen programs that redraw partially
    (`top`, editors); the cap can cut a needed sequence.

### Host key verification

- **A. Fingerprint pinned in the registry entry (recommended).**
  `ssh.FixedHostKey` or a set of accepted keys.
- **B. A known_hosts file** managed by the administrator (`knownhosts.New`).
- **C. Trust on first use**, stored by `occamyd`.
- **D. An explicit `insecure_ignore_host_key: true`** per connection, for
  demos only, logged at warning level.

## Open Questions

1. Snapshot A or B? A is recommended if a maintained candidate passes a
   vttest-style conformance check; otherwise B for beta.
2. Host key policy: A plus D for beta, with B as an alternative? Is C
   wanted?
3. Private keys: file paths in configuration, or encrypted at rest with a
   key from the environment?
4. Agent forwarding and port forwarding: out of scope for beta?
5. Recording in asciicast v2 through a sink tap: in beta or later (see
   session-core question 6)?
6. Telnet as a second terminal backend: worth adding while the terminal
   path is fresh, or not at all?

## Affects

New `internal/backend/ssh`. The web client gains a terminal view
([009](../009-react-web-client.md)), and so does the Swift client
([010](../010-native-swift-client.md)). After its parity gate
([guacd-bridge](guacd-bridge.md)), SSH connections no longer need guacd.
