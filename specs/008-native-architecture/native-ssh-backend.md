---
title: Native SSH backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/protocol-v1.md
  - specs/008-native-architecture/session-core.md
affects:
  - internal/backend/ssh/
  - internal/terminal/
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
  emulator (`internal/terminal`) and sends viewers screen diffs, not the
  shell's bytes (see Screen diffs below). Viewers never see the shell's
  queries (device attributes, cursor position reports), so the emulator
  answers them, and its answers also go to stdin. `resize-request` from
  a viewer holding input rights becomes `WindowChange`.
- Keyboard-interactive challenges and a missing password go to the owner
  through `Sink.Prompt`, except that a configured password answers a
  challenge with a single hidden question, once per connection.
  Answers are never stored.
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
  between two frames are sent as scrolled lines, not repainted. Line
  feeds at the bottom row carry them, because xterm.js and SwiftTerm add
  a line to their scrollback only when a line feed scrolls the whole
  screen, not on `CSI S`.
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
   `htop`) and maintenance at implementation time. The check picked the
   in-house emulator (see Implementation notes: terminal state).
2. **Host keys: A plus D.** A set of pinned keys or SHA-256 fingerprints
   per registry entry, and an explicit `insecure_ignore_host_key: true`
   for demos, logged at warning level. No trust on first use.
3. **Private keys: file paths in the configuration** for beta, readable
   only by the `occamyd` user. Encryption at rest can follow.
4. **No agent or port forwarding in beta.**
5. **No recording in beta,** as in session-core decision 6.
6. **No telnet.**

## Affects

New `internal/backend/ssh` and `internal/terminal`. The web client
gains a terminal view ([009](../009-react-web-client.md)), and so does
the Swift client ([010](../010-native-swift-client.md)). After its
parity gate ([guacd-bridge](guacd-bridge.md)), SSH connections no longer
need guacd.

## Implementation notes

- **Status.** The SSH backend is done: `internal/backend/ssh` implements
  `backend.Backend`. The terminal state (decision 1: the headless
  emulator, screen diffs and snapshots) is built as `internal/terminal`
  (see Implementation notes: terminal state), but the session does not
  feed it yet. Predicted echo, which belongs to the clients, is not
  built. Nothing wires the backend into `occamyd` yet: the registry
  entry and its credential reference come with
  [api-and-server-stack](api-and-server-stack.md). (Done by its
  Sessions task: `ssh` connections and ad-hoc targets use this backend,
  with `host_keys` or `insecure_ignore_host_key` from the connection,
  and a prompting connection without a user name asks the owner for
  one before it dials.)
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
    presented key's type and fingerprint. Pinning a full public key is
    the recommended form, since a fingerprint pin of a non-Ed25519 key
    fails on servers that also hold an Ed25519 key.
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
  - A configured password also answers a keyboard-interactive challenge
    that asks a single hidden question, as guacd and sshpass do. PAM
    servers that disable the `password` method ask for the password
    this way, and the owner would otherwise be asked for it. The
    configured password is sent at most once per connection, by
    whichever method asks first. A second such challenge means it was
    wrong, and goes to the owner, which fails the connection if nobody
    can answer. A hidden question after the `password` method has sent
    it is another factor, such as a one-time code, and goes to the owner
    too. Challenges of any other shape always go to the owner.
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
  - Keys protected by a passphrase, and keys encrypted at rest
    (decision 3).
  - Wiring into `occamyd`. The session now feeds the backend's output
    to `internal/terminal`, gives the emulator's answers back to the
    backend as terminal input, and sends viewers its diffs
    ([session-core](session-core.md), Implementation notes: terminal
    frames). The API's Sessions task registers it (done); `TERM` stays
    the backend's `xterm-256color`, since connections have no `term`
    key yet.

## Implementation notes: terminal state

### Terminal state

Built on 2026-10-30 as `internal/terminal`, the emulator of decision 1.
The session does not feed it the SSH backend's output yet.

**Conformance.** A table of 83 cases in 15 categories
(`internal/terminal/conformance_table_test.go`) covers cursor movement,
erase, DECSTBM scrolling, IL, DL, ICH and DCH, IRM, origin mode, wrap and
pending wrap, tabs, SGR with 256 colours and truecolor, wide and
combining characters, 1049, DECSC and DECRC, bracketed paste, the mouse
modes, DECCKM and DECKPAM, DECSCUSR, and OSC 0 and 2. The expectations
follow xterm, and xterm.js and SwiftTerm where they differ from it. tmux
3.7c agrees with the 58 cases that check text and the cursor except for
nine, which are its own representation (pending wrap as x equal to the
width, tab cells captured as `\t`, line drawing captured raw) or its own
deviations (no CHT, a pending wrap kept across LF). nano, vim, less and
top were recorded under tmux with `TERM=xterm-256color`
(`internal/terminal/testdata`), and each emulator's final screen was
compared with tmux's. htop was not installed, so nano, which needs IRM,
took its place.

| Emulator | Licence | Table | Traces | `seq` output |
|---|---|---|---|---|
| `charmbracelet/x/vt` v0.1.0 | MIT | 74/83 | 3/4 | 0.8 MB/s |
| `unixshells/vt-go` v0.2.0 | MIT | 75/83 | 4/4 | 0.8 MB/s |
| `gdamore/tcell/v3/vt` v3.6.0 | Apache-2.0 | 50/83 | 3/4 | 0.7 to 1.5 MB/s |
| in-house | MIT | 83/83 | 4/4 | 107 MB/s |

- `charmbracelet/x/vt` has no IRM, so nano's inserts overwrite the line.
  ED 1 erases the wrong cells, a wide character in the last column does
  not wrap, 1049 moves the cursor home, the mouse modes are independent
  flags so resetting one leaves another on, DECSCUSR 4 reads as 3,
  DECKPAM is not exposed, and SO and SI are ignored.
- `unixshells/vt-go` adds IRM but keeps the other bugs, and does not
  join combining marks to their cell.
- `tcell/v3/vt`, read through its mock backend, has no IRM, origin mode,
  line drawing, REP or OSC 0, does not cancel a pending wrap on CR, and
  resolves the default colours to 7 and 0. The mock exposes neither the
  modes nor the alternate screen. Some of these are the mock's, but a
  diff cannot read what it does not expose.

**Choice: in-house.**

- It is the only candidate that passes the table. The forks of
  `charmbracelet/x/vt` share its bugs beyond IRM, and its main branch
  still lacks IRM.
- The charm emulators take about 850 ms for T2's 575 KiB, more than the
  whole target of 88 ms of CPU. The in-house one takes 6.4 ms, with 100
  allocations for the whole stream.
- Snapshots share rows copy-on-write and carry a hash per row, computed
  when the row is frozen. A snapshot costs one pointer per row, and the
  diff compares rows and detects scrolls by hash. A library would copy
  its grid through accessors for every snapshot.
- It exposes everything a diff needs: pending wrap, the modes, mouse
  tracking and encoding, the cursor's shape, and both screens.
- It adds no module. `golang.org/x/text`, already required, gives the
  East Asian widths (Unicode 17).
- The emulator is about 2,100 lines and the diff 850, against a v0.1.0
  API, a fork, or an API declared unstable.

**Emulation.**

- Every switch to the alternate screen clears it (47, 1047 and 1049), as
  in xterm.js and SwiftTerm.
- Pending wrap is set whenever the last column is written, with DECAWM
  on or off, which matches xterm.js. Erased cells take only the pen's
  background. Colours keep their kind (default, 16, 256 or RGB), because
  xterm.js brightens bold text only in the 16.
- Widths follow wcwidth. Characters whose width clients disagree on
  (emoji, symbol blocks, and any character with joined code points) are
  followed by an absolute cursor move in the diff, so a client that draws
  them at another width does not shift the rest of the row.
- No scrollback and no reflow: a resize cuts or pads lines, and a
  shorter screen drops lines from the top to keep the cursor on it.
- Answers to DA1, DA2, DSR 5 and 6, DECRQM, `CSI 18 t` and XTVERSION go
  to `Options.Reply`, called after the lock is released. They must reach
  stdin: fish waits for DA1 and vim asks for the cursor position. The
  session gives them to the backend as terminal input.

**Writers.** The package documentation shows the loop.

- The session's goroutine calls `Write` and `Resize`. `Snapshot` may be
  called from any goroutine at the same time. It holds the lock only to
  hash the changed rows and copy one pointer per row: 1.2 µs after a
  one-cell change at 200x50. Screens are immutable, and `Snapshot`
  returns the same pointer while nothing changes.
- A viewer's writer takes `Changed()` before `Snapshot()`, so a change
  between the two wakes it again. When the screen is not the one it last
  sent, it sends the size first if that changed, then `Diff(last, s)` as
  `term-data` closed by `frame-end`, and keeps `s`. A late joiner starts
  with `last` nil.

**Diff.**

- The encoder tracks the client's cursor, including whether its
  position is known and whether a wrap is pending, and its pen. Each
  diff starts and ends with the default pen.
- Rows are compared by hash, then by cell. Changed spans never start or
  end inside a wide character, and gaps of up to three unchanged cells
  are rewritten rather than skipped. An erased tail uses EL, and erased
  rows at the bottom use ED.
- The cheapest of CUP, CUU, CUD, CUF, CUB, CHA, VPA, CR, and up to three
  LF or BS moves the cursor. SGR is the shorter of a change from the
  current pen and a reset followed by the whole style.
- Scrolls:
  - A snapshot counts the lines that left the top of the primary screen
    while the scroll region was the whole screen. When that count grew,
    the diff sends that many line feeds at the bottom row (at most the
    screen's height), each followed by the new bottom line, so the
    client's scrollback fills. Lines beyond one screen are lost to it.
  - Otherwise `detectScroll` tries every shift and counts the non-blank
    rows that the shift moves into place, comparing hashes. The diff
    encodes three plans, no scroll, the region found (DECSTBM, `CSI S`
    or `CSI T`, then `CSI r`), and the whole screen, and keeps the
    shortest.
- A size change or a nil `from` repaints from a cleared screen. Leaving
  the alternate screen sends 1049l and then diffs the primary screen
  from what the client kept there.
- The `Diff` comment lists every sequence used, all supported by both
  xterm.js and SwiftTerm.

**Row versions.** Added with the terminal frames' review: each snapshot
records the version at which each row came to look as it does, and at
which the cursor last moved or changed (`Screen.Versions`), by comparing
the rows at each index with the snapshot before. A row redrawn the same,
or a line scrolled into a row that showed the same, keeps its version.
The viewer writer treats rows like tiles to tell an input's echo from
output already in motion ([session-core](session-core.md)).

**Tests.**

- The property: feeding the diffs between successive snapshots into a
  fresh terminal reproduces each snapshot.
  - 300 random streams in six sizes, including 1x1, with resizes.
  - Every conformance case, with a snapshot after every byte.
  - The four traces, with snapshots every 1, 7, 64 and 500 bytes.
  - `FuzzDiff`, run three times for 60 s, the last time over 8 million
    inputs, with no failure.
- tmux shows the screen encoded by 50 of 50 diff streams: a snapshot and
  a chain of diffs for each trace, 20 random streams and T2.
- Unit tests cover scroll detection, SGR minimality, wide characters at
  line ends, resizes, the alternate screen, and replies. A test under
  `-race` runs four writers against snapshots and resizes.

**Benchmarks** on an Apple M5 Pro, medians of five runs, at load
averages of 5 to 7 on 18 cores:

- `Write`: 107 MB/s for `seq` output and 372 MB/s for SGR-heavy output.
- `Diff`:

  | Change | 80x24 | 200x50 |
  |---|---|---|
  | none | 0.5 µs, 0 B | 2.1 µs, 0 B |
  | one line | 0.6 µs, 19 B | 2.4 µs, 19 B |
  | scroll by 1 | 0.8 µs, 23 B | 1.7 µs, 23 B |
  | scroll by a screen | 4.8 µs, 191 B | 23 µs, 399 B |
  | repaint | 9.5 µs, 2,092 B | 48 µs, 10,354 B |
  | late joiner | 10 µs, 1,884 B | 44 µs, 8,438 B |

- T2, with one diff per frame. At 60 frames a second, the rate at which
  the output arrives sets how much each frame covers:

  | Output per frame | Frames | Bytes |
  |---|---|---|
  | 11 KiB, arriving over guacd's 897 ms | 62 | 10,155 |
  | 107 KiB, arriving over the target's 89.7 ms | 7 | 1,155 |
  | 256 KiB | 3 | 498 |

  A frame costs about 163 bytes, mostly the line feeds for the
  scrollback. The shell itself prints T2 in about 30 ms, so the native
  backend sees the faster rows and stays well under the 8.6 KiB target.
  Only output as slow as guacd's would miss it, by 1.3 KiB.

**Not kept yet:**

- underline styles and colour (SGR 4:x and 58);
- OSC 8 links, OSC 52, the bell, and synchronized output (2026);
- left and right margins.

The soft-wrap flag is not compared, so a client may copy a wrapped line
as two.
