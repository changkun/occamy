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

- `golang.org/x/crypto` is already in `go.mod`, as an indirect
  dependency. Its `ssh` package provides the client, `RequestPty`,
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
- Keyboard-interactive and missing credentials go to the owner through
  `Sink.Prompt`. Answers are never stored.
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
2. **Host keys: A plus D.** A pinned fingerprint or set of keys per
   registry entry, and an explicit `insecure_ignore_host_key: true` for
   demos, logged at warning level. No trust on first use.
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

## Implementation notes: terminal state

### Terminal state

Built on 2026-10-30 as `internal/terminal`, the emulator of decision 1.
No backend uses it yet.

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
  to `Options.Reply`, called after the lock is released. The backend
  must write them to stdin: fish waits for DA1 and vim asks for the
  cursor position.

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

**Tests.**

- The property: feeding the diffs between successive snapshots into a
  fresh terminal reproduces each snapshot.
  - 300 random streams in six sizes, including 1x1, with resizes.
  - Every conformance case, with a snapshot after every byte.
  - The four traces, with snapshots every 1, 7, 64 and 500 bytes.
  - `FuzzDiff`, run for 60 s twice, about 3.7 million inputs, with no
    failure.
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
