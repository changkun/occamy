---
title: Native RDP backend
status: validated
track: architecture
depends_on:
  - specs/008-native-architecture/session-core.md
  - specs/008-native-architecture/graphics-pipeline.md
  - specs/008-native-architecture/rdp-strategy.md
affects:
  - internal/backend/rdp/
  - internal/rdp/
  - internal/keymap/
effort: large
created: 2026-10-31
updated: 2026-10-31
author: Changkun Ou
dispatched_task_id: null
---

# Native RDP backend

## Design Problem

[rdp-strategy](rdp-strategy.md) chose a pure-Go RDP client (option D),
and its spike built the protocol client in `internal/rdp`. What is left
is the backend that runs it in a session, and the protocol features a
backend needs to pass the bridge's parity gate
([guacd-bridge](guacd-bridge.md)) without guacd, Guacamole or cgo:

- how the backend maps credentials, input, demand and the session's end
  onto RDP, and what it bounds;
- which of guacd 1.6's RDP features v1.0 needs, and in what order the
  missing protocol layers (virtual channels, GFX) come;
- how the work is checked against xrdp now and Windows later.

## Context

- `internal/rdp` (the spike, PR #102, and its two reviews) connects with
  TLS or NLA (CredSSP with its own NTLMv2), runs the X.224, MCS and GCC
  sequence, takes the STATUS_VALID_CLIENT licensing shortcut, and draws
  fast-path and slow-path bitmap updates (interleaved RLE and planar)
  and the server's pointer into an `Output`, a subset of `backend.Sink`.
  It bounds every length the server sends and the rate at which it
  decodes pixels. It advertises no orders, caches, bulk compression,
  bitmap codecs or virtual channels, so a server has nothing to send but
  bitmap updates. Live, it has run against xrdp 0.10.1 only.
- guacd 1.6's RDP plugin, the bridge's RDP today, offers:
  - security modes `any` (its default), `nla`, `nla-ext`, `tls`,
    `vmconnect` (Hyper-V's preconnection PDU) and `rdp` (standard RDP
    security), plus `ignore-cert` and `cert-fingerprints`;
  - credentials: it asks the user through `required` only under NLA,
    for the username, the domain when neither it nor the username is
    configured, and the password;
  - the end of a session by the server's Set Error Info reason: none,
    the user's logoff and the user's disconnect (0, 0xc, 0xb) end it
    normally; 1, 2 and 4 are SESSION_CLOSED, 3 (a time limit) TIMEOUT, 5
    (another connection) CONFLICT, and the rest upstream errors;
  - display: `color-depth`, `width` and `height`, `dpi`,
    `resize-method` (`display-update` through the Display Control
    channel, or `reconnect`), `disable-gfx`, and the performance flags
    (wallpaper, theming, font smoothing and others);
  - input: `server-layout` (the keyboard layout to announce, with
    client-side tables for keysyms), `disable-copy` and
    `disable-paste`, `normalize-clipboard`;
  - redirection: audio output and input, drives and file transfer,
    printing, static channels by name;
  - RemoteApp, an RD Gateway, load balancing info, the console session,
    an initial program, Wake-on-LAN, and session recording.
- The parity gate requires the [006](../006-demo-stack-refresh.md)
  matrix for authentication modes, resize, clipboard both ways, cursor,
  sharing roles, reconnect, and keyboard input with AltGr and dead keys;
  traffic and CPU no worse than the bridge; and a soak.
- Protocol v1 carries keys, text, pointer, wheel, clipboard and
  `resize-request`, but no audio and no files. The session core
  ([session-core](session-core.md)) expects RDP to pace the server with
  frame acknowledgements and Suppress Output.
- The spike's step 3 measured bitmap updates at 13 to 38 times guacd's
  link on drag and motion scenes, against guacd's GFX with RemoteFX
  Progressive: GFX is required for v1.0, not an optimization.
- Test servers: xrdp 0.10.1 in the bench desktop image
  (`docker/bench/desktop`), which has CLIPRDR, DRDYNVC, Display Control
  and GFX with RemoteFX Progressive, but no NLA and no H.264; Windows,
  for NLA, licensing, planar and ClearCodec under GFX, and the server
  layouts, has no host yet (#32).

## Scope for v1.0

- **Security:** `nla` (the default), `nla-ext`, `tls` and `any`, named
  as guacd names them. Standard RDP security (`rdp`) is refused, as the
  spike decided; `vmconnect` and the RD Gateway are follow-ups.
- **Credentials:** from the connection's configuration, and asked of
  the session's owner through `Sink.Prompt` when NLA needs what the
  configuration lacks.
- **Graphics:** bitmap updates (phase 1), then GFX without AVC over
  DRDYNVC with ZGFX, surfaces, the cache, RemoteFX, RemoteFX
  Progressive, ClearCodec, planar and uncompressed, and frame
  acknowledgements as the server's pacing (phase 3).
- **Input:** keys by their physical code as scancodes, text as Unicode
  keyboard events, the pointer with five buttons, and both wheels.
- **Channels:** static virtual channels, CLIPRDR for UTF-8 text both
  ways, DRDYNVC, and Display Control for `resize-request` (phase 2).
- **Demand:** Suppress Output while nobody can see.
- **Out of v1.0:** audio (protocol v1 has no audio stream), drive and
  printer redirection and file transfer (no files in protocol v1),
  RemoteApp, the RD Gateway, Kerberos, auto-reconnect cookies and H.264.

## Decisions

Settled on 2026-10-31 with the recommended options, under the owner's
standing approval to resolve open items; open to the owner's review.

1. **The backend is its own package over `internal/rdp`.** The client
   stays the protocol, as `internal/guacd` is beside
   `internal/backend/guacd`. It draws straight into the session's sink,
   since `Output` is a subset of `backend.Sink`; the backend maps
   input, demand, credentials and the session's end. `internal/rdp`
   changes only where the backend needs it: batched input, pointer
   pacing, drawing in strips.
2. **NLA by default, unlike guacd's `any`.** TLS without NLA sends the
   password in the Client Info PDU to whichever server passed the
   certificate check, and `any` lets the server choose TLS. `tls` and
   `any` stay available, since xrdp speaks nothing else.
3. **The owner is asked before the connection, for what NLA needs.**
   The prompt asks what guacd asks, in guacd's order, but before
   dialing, so that the routing cookie carries the prompted username
   and no TCP connection idles while the owner types. With `any`, a
   server that then selects TLS gets the prompted password in the
   Client Info PDU; without NLA in the mode nothing is asked, and empty
   credentials leave the server's logon screen to the user.
4. **One scan code table for VNC and RDP.** `internal/keymap` maps USB
   HID usages to scan code set 1; the VNC backend derives QEMU's key
   numbers from it, and the RDP client sends its codes with the 0xe0
   and 0xe1 prefixes as flags. Keys go out by their physical code, and
   the server maps them through the layout the client announced (US by
   default, `KeyboardLayout` otherwise); a key without a code goes out
   by its keysym, as a US scancode for function keys and as a Unicode
   event for characters. Text is Unicode events. Pause, whose make code
   is a sequence, is sent whole on its press.
5. **The server's deliberate ends are remote ends.** Run returns nil,
   which the session reports as `remote-ended` (4005), for no reason,
   the user's logoff or disconnect, and also for the reasons guacd
   reports as SESSION_CLOSED, TIMEOUT and CONFLICT (1 to 5): an
   administrator's disconnect or logoff, the session time limits, and
   another connection taking the session over. Protocol v1 has no close
   code that tells these apart, and none of them is the backend's
   failure; the backend logs the reason. Other reasons, a refused NLA
   logon and a connection that closes without a reason are errors
   (`backend-failed`, 4006).
6. **Bounds are the client's plus the backend's queue.** The client
   bounds what the server sends; the backend adds a queue of 256 inputs
   and 2^18 events, charged for the most events an input can make, so
   that a viewer cannot grow memory while the server stops reading.
   Pointer updates are charged against the client's decoding rate,
   since a two-byte cached pointer update would otherwise hand a shape
   to every viewer without bound.
7. **Channels before GFX.** GFX is a dynamic channel over DRDYNVC, so
   the channel layer comes first, and it also brings the clipboard and
   resize the parity gate requires. xrdp has all three, so phase 2 is
   checked live; GFX's codecs follow in phase 3.
8. **Audio is a follow-up.** It needs an audio stream in protocol v1
   first.

## Task Breakdown

| Phase | Delivers | Needs |
|-------|----------|-------|
| 1 | `internal/backend/rdp` over `internal/rdp`: configuration and its checks, the NLA prompt, connect, bitmap updates and the pointer into the session, input mapped and batched by a writer goroutine, `Demand` as Suppress Output, the session's end by the server's reason. Tests against a fake RDP server (`internal/rdp/rdptest`), the xrdp recordings replayed through the backend, fuzzing, an xrdp integration test | session-core, the rdp-strategy spike |
| 2 | Static virtual channels (GCC channel definitions, joins, chunked channel PDUs reassembled within a bound); CLIPRDR with UTF-8 text both ways; DRDYNVC with a bounded number of channels; Display Control for `resize-request`; the Heartbeat PDU and auto-detect answers. Checked live against xrdp | Phase 1 |
| 3 | GFX without AVC over DRDYNVC: the capability exchange, ZGFX, surfaces mapped onto the framebuffer, solid fills, surface-to-surface copies as `Sink.CopyRect`, the bitmap cache within a bound, frame acknowledgements as the server's pacing, and the RemoteFX, RemoteFX Progressive, ClearCodec, planar and uncompressed codecs, each fuzzed. RemoteFX Progressive is checked against xrdp, ClearCodec and planar against Windows | Phase 2 |
| 4 | Windows: live NLA, the licensing exchange, the logon errors of the Save Session Info PDU, server layouts with AltGr and dead keys, and the 006 matrix entries for RDP against Windows and xrdp, toward the parity gate | Phases 1 to 3, a Windows host (#32) |

The phases are those [rdp-strategy](rdp-strategy.md)'s "What remains"
lists, in this order because of decision 7. Registration in the API's
backend factory and the switch from the bridge follow the parity gate in
[guacd-bridge](guacd-bridge.md), not these phases.

## Affects

New `internal/backend/rdp` and `internal/keymap`; `internal/rdp` gains
what the backend needs, and `internal/rdp/rdptest`, a fake server for
tests. `internal/backend/vnc` takes its scan codes from
`internal/keymap`. After the parity gate, RDP no longer needs guacd,
and guacd leaves the default deployment.

## Implementation notes: phase 1

- **Status.** Phase 1 is done: `internal/backend/rdp` implements
  `backend.Backend` with `Config`, `New` and `Backend`, in about 790
  lines of code and 1,340 of unit tests, at 97% statement coverage,
  plus a 430-line integration test. Nothing wires it into `occamyd`:
  the sessions task registers backends.
- **What was built.**
  - `New` checks the configuration before anything connects: the
    address (port 3389 by default), the desktop size (200 to 8192 a
    side, 2^25 pixels at most), the colour depth (15, 16, 24 or 32), the
    security mode, and the certificate policy, through the client's new
    `TLS.Check`. `ServerName` defaults to the address's host.
  - Run asks for credentials if NLA needs them (decision 3), dials with
    the context, connects, and runs the client into the session's sink.
    A writer goroutine takes queued input and the demand: it maps each
    input into RDP events, and writes what is ready, up to 1,024
    events, in one write of PDUs of up to 255 events (the client's new
    `SendInput`). Input that comes while Run connects is queued and
    written once the connection is up. It sends Suppress Output when
    nobody can see and its end when a viewer can; xrdp then redraws.
  - Input: `Key` by its HID code through `internal/keymap`, or by
    keysym (decision 4); `Text` as Unicode keyboard events where the
    server takes them, and otherwise as keys of a US layout with Shift
    where needed, dropping characters off the layout; a line feed and
    the C0 controls with a key type that key, as the VNC backend does.
    `Pointer` sends an event for each button that changed, at the
    pointer, or a move; back and forward only where the server takes
    them. `Wheel` scales as the VNC backend does, 53 pixels or three
    lines a notch of 120, at most 32 notches an axis an input, the
    remainder carried. `Clipboard` and `Resize` are accepted and
    ignored until phase 2; `TermInput` is refused.
  - The client changed where the backend needed it: `SendInput` and its
    event constructors (`KeyEvent`, `PauseEvents`, `UnicodeEvent`,
    `MoveEvent`, `ButtonEvent`, `WheelEvent`), `MouseXInput`, bitmaps
    drawn under the sink's lock in strips of at most 64 Ki pixels, the
    VNC backend's strip, pointer updates paced (decision 6), a reason
    followed by a reset treated as one followed by the end of the
    stream, `HungUp`, and `TLS.Check`.
  - Tests run the backend against `internal/rdp/rdptest`, a fake server
    that runs the server's side of the sequence over TLS with a
    certificate the backend pins, builds the PDUs a test sends, and
    parses what the backend sends back. It does not import the client,
    so the two encodings check each other. The tests cover drawing,
    strips, a server-chosen desktop, the pointer's shapes, cache,
    hidden and default pointers and moves, each kind of input by
    fast-path and slow-path, with and without Unicode, batching,
    the queue's bounds and the budget returned, Suppress Output, the
    prompts, each way a session ends, a pinned certificate refused,
    cancellation while connecting and while prompting, and a second
    Run. The xrdp recordings of the spike replay through the backend.
    Every test that runs the backend checks that no goroutine outlives
    it.
- **Integration test.** Under the `rdp_integration` tag, as the spike's,
  it builds the bench desktop image and starts xrdp, with names of its
  own (`occamy-rdpbackend-it-until…`) and the spike's whole-name cleanup.
  Against xrdp 0.10.1 on 2026-10-31, it passed:
  - the wallpaper, every pixel outside the swatch, at 32 bits per pixel;
  - keys by HID code (F2 and letters) and text by Unicode events paint
    the swatch in their colours; xrdp takes Unicode input;
  - with the output suppressed, a key's swatch did not arrive for two
    seconds, and arrived when the output resumed;
  - the pointer lands where it was sent on the session's X display;
    clicks and both wheels keep the session up;
  - cancel returns at once, with no goroutine left;
  - with `any`, xrdp selects TLS; with `nla`, it selects standard RDP
    security rather than failing, which the backend refuses at once.
- **Mutation checks.** Each bound was broken in turn and a test failed:
  - the queue's length, and its event budget (`TestInputErrors`);
  - the writer returning the budget (`TestRunInputBudgetReturned`);
  - 255 events a PDU (`TestSendInputBatches`, `TestRunInputBatched`);
  - the most events each kind of input is charged: keys, text, pointer
    and wheel (`FuzzInput`'s seeds);
  - the wheel's notch cap (`TestWheel`) and the rotation's clamp
    (`TestWheelEventClamps`);
  - a pointer move's charge, a shape's least charge and its charge by
    pixels (`TestPointerPaced`), and a shape shown once
    (`TestPointerShownOnce`);
  - drawing in strips (`TestRunDrawsInStrips`);
  - the prompt's answer count, without which Run panics
    (`TestRunPromptFails`);
  - NLA without TLS by default (`TestNew`, `TestRunPromptsForNLA`), and
    no prompt without NLA (`TestRunPromptsOnlyForNLA`);
  - the reason before a hang-up (`TestRunErrorInfoThenHangUp`,
    `TestRunEnd`), and the reasons that end a session normally
    (`TestRunEnd`);
  - the desktop's area and the certificate policy in `New` (`TestNew`);
  - pointer positions clamped to the desktop (`TestFastPathInput`,
    `TestRunInput`);
  - Suppress Output on demand (`TestRunDemand`).

  The spike's guards that the refactor touched still fail their tests
  when removed: the pointer cache index (`TestPointerCacheIndex`), the
  decoding rate (`TestDecodeRate`), and the resize rate
  (`TestDemandActiveResizeRate`).
- **Fuzzing.** `FuzzRun` in `internal/rdp` runs the client, as the
  backend does, on what a server sends into `backendtest.Sink`, which
  panics where the lock contract breaks. Its seeds are the recordings'
  updates and rdptest's PDUs. 60 seconds with `-fuzzminimizetime 20x`
  made 9,314,276 executions without a finding (host load 6 before, 64
  after, the fuzzer's own included). `FuzzInput` in the backend checks
  that no input makes more events than it is charged: 6,718,164
  executions in 30 seconds, without a finding.
- **Decisions made during implementation.**
  - The queue holds 256 inputs of at most 2^18 events. A character of
    `Text` is charged four events (two UTF-16 units, or Shift around a
    key), a key four (Pause), a pointer five, a wheel 64. Each event is
    at most 12 bytes on the wire, so the queue holds at most 3 MiB of
    input.
  - Pointer updates cost a 64×64 tile of the decoding budget at least,
    and a shape its pixels: 8,192 a second at most at the client's
    rate. A shape that is already shown, as a cached pointer the server
    repeats, is not shown again and costs nothing.
  - Lang1 and Lang2, whose set 1 codes have no break codes, go out as
    0x72 and 0x71 with the release flag, as FreeRDP sends them.
  - `KeyboardLayout` and `ClientName` pass through to the client;
    `Sink.Title` is not set, since RDP has no desktop name to show.
- **Deviations.** From guacd: the default security mode (decision 2),
  the prompt before the connection (decision 3), and remote ends for
  reasons 1 to 5 (decision 5). From the phases as first planned: none.
- **Surprises.**
  - A server that closes with the client's input unread makes the
    client's kernel see a reset, not the end of the stream, and the
    reason the server sent before it may be lost with it. The client
    and the backend treat a reset as a hang-up; the backend's tests
    keep the backend quiet before the server closes, so that their
    results do not depend on the race.
  - xrdp takes Unicode keyboard events and honours Suppress Output.
  - xrdp offered NLA alone selects standard RDP security, as the spike
    found; the backend's `nla` default therefore fails at once against
    xrdp, which needs `tls` or `any`.
- **What was not done.**
  - `ErrAuthFailed` (a refused NLA logon) is not exercised through the
    backend: rdptest has no CredSSP acceptor, and the client's own is
    in its tests. The client's replay tests cover the refusal itself.
  - Nothing runs against Windows (phase 4).
- **Follow-ups.**
  - Phases 2 to 4.
  - A CredSSP acceptor in rdptest, so that the backend's NLA path,
    prompted credentials included, runs end to end in tests.
  - Lang1 and Lang2's scancodes, unchecked against Windows.
  - Registration in `occamyd`'s backend factory, with the connection
    configuration's `rdp` parameters.
