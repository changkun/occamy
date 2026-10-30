---
title: Native SSH backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/session-core.md
affects:
  - internal/backend/ssh/
effort: medium
created: 2026-10-29
updated: 2026-10-30
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

- `golang.org/x/crypto` is in `go.mod`, a direct dependency since the
  backend uses it. Its `ssh` package provides the client, `RequestPty`,
  `WindowChange`, the Password, PublicKeys and KeyboardInteractive auth
  methods, `knownhosts`, and `agent`.
- v0 renders SSH as pixels: a terminal emulator draws with Pango and
  Cairo into PNG tiles. Up to `0.4.0` this ran in libguac inside
  `occamyd`, where repeated sessions failed (#22). Since `v0.5.0` it runs
  in guacd, one process per connection
  ([005](../005-upstream-guacamole.md)).
- Protocol v1 carries `term-data`, `term-snapshot`, `term-input`,
  `resize-request` and `prompt` ([protocol-v1](protocol-v1.md)).

## Proposed shape

- The connection is defined in the registry
  ([api-and-server-stack](api-and-server-stack.md)): host, port,
  username, a credential reference (password or private key), the host
  key policy, and `TERM` (default `xterm-256color`).
- Run: dial, authenticate, request a PTY with the owner's size, start a
  shell, then feed stdout and stderr to `Sink.TermData` and `term-input`
  to stdin. The session keeps the terminal's screen in a headless
  emulator and sends viewers screen diffs, not the shell's bytes (see
  Screen diffs below). `resize-request` from a viewer holding input
  rights becomes `WindowChange`.
- Keyboard-interactive challenges and a missing password go to the owner
  through `Sink.Prompt`. Answers are never stored.
- Snapshot for late joiners: a byte sequence that, fed into a fresh
  terminal of the same size, reproduces the screen, cursor, attributes and
  alternate-screen state. Clients need no special snapshot logic.

### Screen diffs

Terminals follow the same rule as desktops: frames carry states, the
newest wins, and the frame after an input goes out at once
([session-core](session-core.md), Input to display).

- All output feeds one headless emulator per session, which holds the
  screen, cursor, attributes, modes and the alternate screen.
- Per viewer, the session remembers the screen it last sent. Each frame,
  it computes the escape sequences that turn that screen into the
  current one and sends them as `term-data`, closed by `frame-end`. A
  snapshot is the same computation from an empty screen, so late
  joiners need nothing else.
- Under bulk output, intermediate screens are never sent. The baseline's
  T2 prints 575 KiB; streaming those bytes would send 575 KiB, guacd's
  screen frames send 86 KiB, and the target is 8.6 KiB
  ([docs/benchmarks.md](../../docs/benchmarks.md#targets)).
- Scrollback is the client's own: lines that leave the top of the screen
  between two frames are sent as scrolled lines, not repainted, so
  `scroll-up` sequences carry them and the client's scrollback fills.
  Lines skipped while a viewer was behind are lost to its scrollback,
  which is the price of never queueing.

### Predicted echo

Clients show typed characters before the server's echo, as mosh does,
so that typing feels immediate at any RTT.

- The client predicts printable input at the cursor in a distinct style
  (underlined), and moves the cursor.
- `frame-end` names the last input it reflects. When the server's screen
  confirms a prediction, the style goes; when it contradicts one (a
  password prompt, a full-screen program), the client drops its
  predictions and shows the server's screen.
- Predictions are off while the server's screen shows no echo of
  recent input, the mosh heuristic for password prompts.
- `github.com/unixshells/mosh-go` (MIT) has a simplified predictor and a
  screen-diff layer, wire-compatible with mosh. It is a starting point to
  port and extend toward mosh's full prediction overlay.
- This lives in the clients ([009](../009-react-web-client.md),
  [client-core](client-core.md)). The server's part is the input
  sequence number in `frame-end`.

## Options

### Terminal state for snapshots

- **A. Headless emulator in Go.** Feed all output through an emulator
  and serialize its screen on demand. Candidates to evaluate, checking
  maintenance and VT coverage at evaluation time. As surveyed on
  2026-10-30:
  - `github.com/charmbracelet/x/vt` (MIT, active), with damage tracking
    and scrollback. It lacks insert mode (IRM), which nano needs: patch
    it upstream, or start from `github.com/unixshells/vt-go`, a fork
    that adds it.
  - `github.com/gdamore/tcell/v3/vt` (Apache-2.0, active), with left and
    right margins; its API is not yet stable.
  - A small in-house emulator.

  `hinshun/vt10x` and its forks have had no commits since 2022 and 2023,
  and `go-libghostty` needs cgo.
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

## Decisions

Settled on 2026-10-30. Item 1 was the owner's call; the rest follow the
recommendations above.

1. **Terminal state: A, a headless emulator in Go,** which also produces
   the screen diffs and snapshots. The candidate is picked by a
   conformance check (vttest-style sequences, `top`, `vim`, `less`,
   `htop`) and maintenance at implementation time.
2. **Host keys: A plus D.** A set of pinned keys or SHA-256 fingerprints
   per registry entry, and an explicit `insecure_ignore_host_key: true`
   for demos, logged at warning level. No trust on first use.
3. **Private keys: file paths in the configuration** for beta, readable
   only by the `occamyd` user. Encryption at rest can follow.
4. **No agent or port forwarding in beta.**
5. **No recording in beta,** as in session-core decision 6.
6. **No telnet.**

## Affects

New `internal/backend/ssh`. The web client gains a terminal view
([009](../009-react-web-client.md)), and so does the Swift client
([010](../010-native-swift-client.md)). After its parity gate
([guacd-bridge](guacd-bridge.md)), SSH connections no longer need guacd.

## Implementation notes

- **Status.** The SSH backend is done: `internal/backend/ssh` implements
  `backend.Backend`. The terminal state (decision 1: the headless
  emulator, screen diffs and snapshots) and predicted echo are not built
  yet; they belong to the session and the clients. Nothing wires the
  backend into `occamyd` yet: the registry entry and its credential
  reference come with [api-and-server-stack](api-and-server-stack.md).
- **What was built.** `Config`, `New` and `Backend`. Run dials with the
  context, checks the host key, authenticates, requests a PTY, starts a
  shell, reports the size with `Sink.Resize`, and copies stdout and
  stderr to `Sink.TermData` in reads of at most 32 KiB. The tests run an
  x/crypto server on the loopback interface and check every path for
  leaked goroutines.
- **Decisions made during implementation.**
  - Host key algorithms. The backend offers the pinned keys' algorithms
    first (an RSA key as `rsa-sha2-512`, `rsa-sha2-256`, `ssh-rsa`). If a
    fingerprint is pinned, or nothing is, OpenSSH's order follows:
    Ed25519, ECDSA, then RSA with SHA-2. The fingerprint that `ssh -v`
    shows is then the one of the key the server presents. Certificate
    algorithms are not offered, since a certificate never matches a
    pinned key.
  - A fingerprint carries no key type, so a fingerprint of a server's
    ECDSA or RSA key is refused when the server also has an Ed25519 key.
    Pin the full key then. The error, a `HostKeyError`, names the
    presented key's type and fingerprint.
  - Insecure mode accepts any key and warns once per Run; rekeys run the
    check again. With both pins and the insecure flag, a pinned key is
    accepted silently and any other key with the warning.
  - A shell that exits with any status or signal ends the session
    normally, and Run returns nil: the status is the last command's, so
    it is logged at debug level. A channel that closes without an exit
    status is an error, since that is how a dropped connection shows in
    x/crypto. A server that never sends an exit status therefore gets an
    error at logout.
  - Input that arrives while Run connects is queued. Keys typed during a
    prompt reach the shell once it starts, as typing ahead does with
    ssh(1), and a Resize sets the size the PTY is requested with. Input
    before Run or after it returns fails with `ErrNotRunning`.
  - stdin's queue holds 64 inputs, then `ErrInputFull`. Resize is not
    queued with stdin: the newest size wins, and its own goroutine sends
    `window-change` and then calls `Sink.Resize`. Writes to stdin block
    on the channel window when the remote stops reading;
    `window-change` does not.
  - `Key` is ignored, as are `Pointer`, `Wheel` and `Clipboard`: the
    viewer's terminal emulator turns keys into `term-input`, and
    translating keysyms would need the modes it holds. `Text` goes to
    stdin.
  - The PTY is requested with `IUTF8`, so that line editing erases whole
    UTF-8 characters.
  - `DialTimeout` (15 s by default) bounds the TCP connection only. Run's
    context bounds the handshake, which can wait for the owner.
  - `New` reads and parses the key files, so a missing key fails there,
    and a key protected by a passphrase is refused as not supported yet.
    File permissions are not checked. `New` also requires a user and a
    host key policy, and sizes from 1 to 65535.
  - The password prompt has the instruction `Password for user@host:port`
    and one secret field. Keyboard-interactive rounds without questions,
    which OpenSSH sends, are not passed to the owner. The server's
    pre-authentication banner is ignored.
- **Deviations from the spec.** None in shape. Option A names
  `ssh.FixedHostKey`; the backend has its own callback, which takes a set
  of keys and fingerprints.
- **Surprises / gotchas.**
  - x/crypto's default host key preference puts ECDSA ahead of Ed25519,
    so without the ordering above an Ed25519 pin fails against a stock
    OpenSSH server.
  - x/crypto uses only the first auth method of each name. `Password` and
    `PasswordCallback` are both `password`, so a configured password and
    a prompted one exclude each other.
  - `ssh.NewClientConn` takes no context. Closing the connection from
    `context.AfterFunc` is the only way to abort a handshake.
  - With `StdinPipe`, `StdoutPipe` and `StderrPipe`, `Session` starts no
    copy goroutines, so Run joins every goroutine it starts. x/crypto's
    own goroutines stop once the connection closes; Run waits for its
    reader with `Client.Wait`.
- **Follow-ups.**
  - A configured password against a server that offers only
    keyboard-interactive, as PAM setups often do: the owner is asked
    although a password is configured. guacd answers a one-question
    challenge with the configured password; whether to do the same is
    open.
  - Keys protected by a passphrase, and keys encrypted at rest
    (decision 3).
  - The session's terminal emulator (decision 1) and its conformance
    check, and wiring into `occamyd`.
