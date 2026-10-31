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
   dialing, so that no TCP connection idles while the owner types, and
   so that the routing cookie, where it is configured, carries the
   prompted username. With `any`, a server that then selects TLS gets
   the prompted password in the Client Info PDU; without NLA in the
   mode nothing is asked, and empty credentials leave the server's
   logon screen to the user. The routing cookie (`Cookie: mstshash=`
   and the first nine characters of the username, which mstsc and
   FreeRDP send) is off by default and a `RoutingCookie` option for
   load balancers that route by user: it goes before TLS, in the
   clear, and without it nothing about the user is sent before the
   certificate policy passes.
4. **One scan code table for VNC and RDP.** `internal/keymap` maps USB
   HID usages to scan code set 1; the VNC backend derives QEMU's key
   numbers from it, and the RDP client sends its codes with the 0xe0
   and 0xe1 prefixes as flags. Keys go out by their physical code, and
   the server maps them through the layout the client announced (US by
   default, `KeyboardLayout` otherwise); a key without a code goes out
   by its keysym, as a US scancode for function keys and as a Unicode
   event for characters. Text is Unicode events. Pause, whose make code
   is a sequence, is sent whole on its press, in one PDU; while either
   Control key is down it is Break, E0 46 pressed and released, as
   FreeRDP sends them. Power (HID 0x66, E0 5E) is dropped: Windows
   applies its power button action to the machine, which can shut it
   down or put it to sleep for every user, which is more than a
   viewer's input to one session should reach. Sleep and Wake are
   Generic Desktop usages, which keys do not carry, so there is nothing
   else to drop. Every backend now drops it, by `backend.PowerKey`
   (`internal/backend/power.go`), which also drops the X11 power
   keysyms (XF86PowerOff, PowerDown, Sleep, Suspend, Hibernate,
   Standby) that a key without a code may carry.
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
| 2 | Static virtual channels (GCC channel definitions, joins, chunked channel PDUs reassembled within a bound); CLIPRDR with UTF-8 text both ways; DRDYNVC with a bounded number of channels; Display Control for `resize-request`; the Heartbeat PDU and auto-detect answers. Checked live against xrdp. A CredSSP acceptor in `rdptest` (NTLMv2 with a known password, the public key binding, TSCredentials), so that the backend's NLA path, prompted credentials included, runs end to end in tests | Phase 1 |
| 3 | GFX without AVC over DRDYNVC: the capability exchange, ZGFX, surfaces mapped onto the framebuffer, solid fills, surface-to-surface copies as `Sink.CopyRect`, the bitmap cache within a bound, frame acknowledgements as the server's pacing, and the RemoteFX, RemoteFX Progressive, ClearCodec, planar and uncompressed codecs, each fuzzed. RemoteFX Progressive is checked against xrdp, ClearCodec and planar against Windows | Phase 2 |
| 4 | Windows: the logon errors of the Save Session Info PDU, server layouts with AltGr and dead keys, a keyboard Synchronize event with the lock states at connect and on focus, and the 006 matrix entries for RDP against Windows and xrdp, toward the parity gate | Phases 1 to 3, a Windows host (#32) |

The phases are those [rdp-strategy](rdp-strategy.md)'s "What remains"
lists, in this order because of decision 7. Two things do not wait for
them:

- **A live NLA and licensing smoke test** runs in parallel with phase 2
  as soon as #32 has a Windows host: NLA with the backend's own NTLMv2
  and CredSSP, a refused logon, and the licensing exchange beyond the
  STATUS_VALID_CLIENT shortcut, against Windows. Phase 4 keeps the rest
  of the Windows work.
- **Registration needs NLA end to end.** The backend is not registered
  in `occamyd` until NLA has run end to end, live against Windows or
  against the CredSSP acceptor of phase 2, since NLA is the default
  security mode and nothing has run it against a server that accepts
  it. Registration in the API's backend factory and the switch from
  the bridge then follow the parity gate in
  [guacd-bridge](guacd-bridge.md), not these phases. (Since the
  Sessions task of [api-and-server-stack](api-and-server-stack.md),
  `rdp` connections run on the guacd bridge in that factory,
  `server/backends.go`; registration replaces that entry.)

## Affects

New `internal/backend/rdp` and `internal/keymap`; `internal/rdp` gains
what the backend needs, and `internal/rdp/rdptest`, a fake server for
tests. `internal/backend/vnc` takes its scan codes from
`internal/keymap`. After the parity gate, RDP no longer needs guacd,
and guacd leaves the default deployment.

## Implementation notes: phase 1

- **Status.** Phase 1 is done: `internal/backend/rdp` implements
  `backend.Backend` with `Config`, `New` and `Backend`, in about 930
  lines of code and 2,030 of unit tests, at 99% statement coverage,
  plus a 430-line integration test. Nothing wires it into `occamyd`,
  and nothing will until NLA has run end to end (Task Breakdown).
- **What was built.**
  - `New` checks the configuration before anything connects: the
    address (port 3389 by default), the desktop size (200 to 8192 a
    side, 2^25 pixels at most), the colour depth (15, 16, 24 or 32), the
    security mode, and the certificate policy, through the client's new
    `TLS.Check`. `ServerName` defaults to the address's host.
  - Run asks for credentials if NLA needs them (decision 3), dials with
    the context, connects, and runs the client into the session's sink.
    A writer goroutine takes queued input and the demand: it maps each
    input into RDP events, and writes what is ready in writes of at
    most 1,024 events, each one write of PDUs of up to 255 events (the
    client's new `SendInput`). Inputs go whole, but a `Text` is typed a
    part at a time, so that the writer never holds more than 1,024
    events whatever the input. Input that comes while Run connects is
    queued and written once the connection is up. It sends Suppress
    Output when nobody can see and its end when a viewer can, between
    the parts of a long `Text` too; xrdp then redraws.
  - Input: `Key` by its HID code through `internal/keymap`, or by
    keysym (decision 4), with Pause as Break while Control is down by
    the keys sent, and Power dropped; `Text` as Unicode keyboard events where the
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
    `MoveEvent`, `ButtonEvent`, `WheelEvent`), Pause's four events kept
    in one PDU, `ScancodeBreak`, `MouseXInput`, bitmaps drawn under the
    sink's lock in strips of at most 64 Ki pixels, the VNC backend's
    strip, pointer updates paced (decision 6), a new shape charged by
    its declared size before it is decoded, a reason followed by a
    reset treated as one followed by the end of the stream, a reason
    held only until the session goes on after it, `HungUp`,
    `RoutingCookie`, and `TLS.Check`.
  - Tests run the backend against `internal/rdp/rdptest`, a fake server
    that runs the server's side of the sequence over TLS with a
    certificate the backend pins, builds the PDUs a test sends, and
    parses what the backend sends back. It does not import the client,
    so the two encodings check each other. The tests cover drawing,
    strips, a server-chosen desktop, the pointer's shapes, cache,
    hidden and default pointers and moves, each kind of input by
    fast-path and slow-path, with and without Unicode, batching,
    the queue's bounds and the budget returned, Suppress Output, the
    prompts, the routing cookie off and on, a refused NLA logon
    (`rdptest.TSRequestError`), each way a session ends, with the
    writer stalled or failing too, a pinned certificate refused, NLA
    stripped by a man in the middle under `any`, TLS refused by
    default, cancellation while connecting, while prompting, in
    CredSSP and with the writer stalled, and a second Run. The writer's
    batching runs against a fake client. The xrdp recordings of the
    spike replay through the backend. Every test that runs the backend
    checks that no goroutine outlives it.
- **Review of #113.** An independent review found no security bugs and
  three bugs, fixed with their tests:
  - A deliberate end was reported as a write failure. When the server
    ended the session while the writer was blocked writing input it
    did not read, Run's own close failed the write, and the writer's
    error won over the server's reason. Now a write that fails because
    the connection was closed under it, or after Run is done, is not
    the writer's failure, and the writer's error is Run's result only
    when it stopped the reader. A write also fails when the server
    hangs up before the reader has read what it sent, as at a high
    rate of input; the writer then gives the reader a second to end
    on its own, with the server's reason, before it closes the
    connection. The review's repro, a server that ends the session
    and closes while a viewer moves the pointer at 20 kHz, reported 1
    to 6 of 100 deliberate ends as errors before the fix on this host
    (the review saw 14 of 40 under load), and 0 of 100 in each of
    four runs after it. Without the grace it also gave 0 of 100 here,
    three times: the grace covers a write that fails on its own before
    the reader is done, which `TestRunWriteFailsThenServerEnds` makes
    happen and which this host's loopback did not.
  - Control and Pause (Break) could not be sent: Pause always sent
    its sequence. Now it is Break while Control is down by the keys
    sent (decision 4), and keysym Break maps to E0 46 too.
  - A Set Error Info reason was remembered for the whole connection,
    so a reset long after a reason the session went on from was taken
    for that end. Now a reason holds only until a share data PDU other
    than another reason, a fast-path update or a reactivation; a
    Deactivate All keeps it, since [MS-RDPBCGR] 1.3.1.4.2 puts one
    between the reason and the Disconnect Provider Ultimatum. FreeRDP
    never clears a reason, so this diverges from it: phase 4 checks
    Windows' logoff and disconnect sequences for any share data PDU
    between the reason and the end, which would clear it.

  It also led to: a `Text` typed a part at a time, so that the writer
  holds at most 1,024 events where one large `Text` used to keep
  10 MiB of events; Pause's sequence kept in one PDU, as FreeRDP sends
  it; the routing cookie off by default (decision 3); a pointer shape
  checked and charged by its declared size before it is decoded, as a
  bitmap is, where it used to be decoded first; Power dropped
  (decision 4); and tests for the guards its mutations showed
  untested.
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

  It passed again after the review's fixes, on the same day.
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

  After the review, each of these failed a test too, 39 mutations in
  all, none surviving:
  - the reasons that end a session normally, bounded at 5 (`TestEnd`,
    `TestRunEnd`), and a reason before a reset (`TestRunReasonThenReset`);
  - the writer's error as Run's result only when it stopped the
    reader, and the context's error first (`TestEnd`,
    `TestRunWriteFails`, `TestRunWriteFailsThenServerEnds`);
  - the writer closing the connection when its write fails, and its
    grace before it does (`TestRunWriteFails`,
    `TestRunWriteFailsThenServerEnds`);
  - a write that failed after Run was done or because the connection
    was closed under it, each alone (`TestWriterFails`);
  - writes of at most 1,024 events, the inputs that are ready taken
    together, and a `Text` in parts (`TestWriterBatches`, `TestText`);
  - the budget left as it was when the queue is full (`TestSendFull`);
  - `ErrAuthFailed` (`TestRunAuthFailed`);
  - the first pointer sent at the origin (`TestPointer`), the wheel's
    clamp before scaling and the distance carried after a notch
    (`TestWheel`);
  - an IPv6 address in brackets without a port (`TestNew`);
  - Break with either Control key, released after Control, keysym
    Break, and Power dropped (`TestKeyBreak`, `TestKey`, `TestScancodes`);
  - Pause's sequence in one PDU (`TestSendInputBatches`);
  - the routing cookie off by default, on when asked for, and passed to
    the client (`TestRunNoRoutingCookie`, `TestRunPromptsForNLA`,
    `TestNew`);
  - a hidden pointer charged a tile (`TestPointerPaced`), and a shape
    checked and charged before it is decoded
    (`TestPointerPacedBeforeDecoding`);
  - a reason cleared by a later update or reactivation and kept across
    a Deactivate All (`TestRunErrorInfoStale`, `TestRunEnd`).

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
  that no input makes more events than it is charged, and that a
  `Text` typed in parts makes the same events in parts of at most
  1,024: 6,718,164 executions in 30 seconds, without a finding. After
  the review, on 2026-10-31: `FuzzRun` 9,151,912 executions in 60
  seconds, `FuzzInput` 5,262,057 and `FuzzPointer`, which now checks
  that a shape that parses decodes, 7,442,535 in 30 seconds each,
  without a finding (host load 4 before, 80 after).
- **Decisions made during implementation.**
  - The queue holds 256 inputs of at most 2^18 events. A character of
    `Text` is charged four events (two UTF-16 units, or Shift around a
    key), a key four (Pause), a pointer five, a wheel 64. The writer
    takes inputs whole and types a `Text` a part at a time, so it holds
    at most 1,024 events, 40 KiB. Measured with the writer stalled on
    a server that does not read and the queue refilled to its budget
    behind it, the input path kept 108 KiB of heap after a collection
    with ASCII text, and 327 KiB with four-byte characters; its bound
    is about 600 KiB, the queue's 65,536 characters and one `Text`
    being typed, 256 KiB each, and the batch. Before the review it
    kept 3.4 to 3.9 MiB in the same test, the events of the `Text`
    being written, and 10 MiB for one `Text` of 65,536 characters.
    Between collections the heap rose up to 3.6 MiB above its base,
    the garbage of the writes before the stall.
  - Pointer updates cost a 64×64 tile of the decoding budget at least,
    and a shape its pixels: 8,192 a second at most at the client's
    rate. A new shape is charged by the size it declares, checked
    first, before it is decoded. A shape that is already shown, as a
    cached pointer the server repeats, is not shown again and costs
    nothing.
  - A write that fails gives the reader up to a second to end on its
    own before the writer closes the connection, since a server that
    hangs up fails the writer's next write, often before the reader
    has read the reason the server sent first.
  - Lang1 and Lang2, whose set 1 codes have no break codes, go out as
    0x72 and 0x71 with the release flag, as FreeRDP sends them.
  - `KeyboardLayout` and `ClientName` pass through to the client;
    `Sink.Title` is not set, since RDP has no desktop name to show.
- **Deviations.** From guacd: the default security mode (decision 2),
  the prompt before the connection (decision 3), no routing cookie by
  default (decision 3), Power dropped (decision 4), and remote ends for
  reasons 1 to 5 (decision 5). From the phases as first planned: the
  CredSSP acceptor moved into phase 2, a live NLA and licensing smoke
  test runs beside phase 2, and registration waits for NLA end to end
  (Task Breakdown).
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
  - `rdp.Client.Run` closes the connection before it returns, so a
    writer blocked on a server that does not read wakes before Run can
    tell it that it is done; the writer therefore also takes a write
    that failed on a closed connection as Run's doing.
- **What was not done.**
  - NLA has not run against a server that accepts it: rdptest refuses
    it (`TSRequestError`) or stops at the client's first TSRequest, and
    xrdp does not offer it. A refused logon now runs through the
    backend. The CredSSP acceptor (phase 2) or the smoke test against
    Windows comes before registration.
  - Nothing runs against Windows (phase 4).
- **Follow-ups.**
  - Phases 2 to 4, the NLA and licensing smoke test, and the CredSSP
    acceptor (Task Breakdown).
  - A keyboard Synchronize event with the lock states (phase 4):
    without it, the server's Caps Lock, Num Lock and Scroll Lock keep
    their own state rather than the viewer's.
  - The writer's `shown` is not reset when the server reactivates the
    session; a server that resumes output on reactivation while nobody
    can see keeps sending until the next change of demand. Done in
    phase 2: the client repeats Suppress Output after a reactivation.
  - Session-level dedupe of identical cursor shapes before they are
    encoded for each viewer, for the encode-cache task: each new shape
    is copied by the session and encoded losslessly per viewer, and a
    server can send 227 distinct 384×384 shapes a second within the
    decoding rate.
  - Inherited from the VNC backend's text mapping: U+0014 taps Scroll
    Lock, since the C0 controls map to keysyms 0xff00 and up, and CR LF
    types Return twice.
  - ~~The VNC backend sends Power (E0 5E) through QEMU's extended key
    events; it may want the same drop as RDP.~~ Done in the
    panic-safety PR: the VNC, RDP and guacd backends drop Power and the
    power keysyms through one helper, `backend.PowerKey`, whose
    `HIDPower` the web client's keyboard test reads. The web client
    still maps Power; no backend sends it.
  - Lang1 and Lang2's scancodes, unchecked against Windows.
  - At registration in `occamyd`'s backend factory, with the
    connection configuration's `rdp` parameters (`RoutingCookie` among
    them): a sentence in [docs/protocol.md](../../docs/protocol.md)'s
    4005 row that RDP's deliberate ends (decision 5) are `remote-ended`,
    and a CHANGELOG line.

## Implementation notes: phase 2

- **Status.** Phase 2 ships in two pull requests. The first has the
  static virtual channels, the clipboard (CLIPRDR) both ways, and the
  CredSSP acceptor in `rdptest`, with NLA run end to end through the
  backend; these notes are its. DRDYNVC and Display Control for
  `resize-request` are the second's, on top of it, with notes of their
  own below. The Heartbeat PDU and
  the auto-detect answers are deferred (Deviations). NLA now runs end
  to end against the acceptor, which registration waited for; the
  backend is still not registered in `occamyd`, which follows the
  parity gate (Task Breakdown).
- **What was built.**
  - Static channels (`internal/rdp/channel.go`). The client names the
    channels its configuration asks for in its network data (today
    only `cliprdr`, with the options INITIALIZED, ENCRYPT_RDP and
    SHOW_PROTOCOL and no compression), and joins the MCS channels the
    server gives them after the user and I/O channels. A server's list
    is checked: at most 31 channels in its network data, no more than
    the client asked for (fewer is fine, as FreeRDP takes it), none
    twice and none on the I/O or message channel; a channel it gives
    0 is not joined. Chunks are reassembled per channel up to the
    channel's bound: the client keeps the first bytes of a longer
    message and tells the handler it was cut. A first chunk starts a
    message anew (the unfinished one is counted), a chunk without its
    first is dropped and counted, and chunks that run past or stop
    short of the length they declared, or that are compressed, end
    the session. The client sends in chunks of 1,600 bytes, 32 chunks
    a write, so that input written meanwhile waits for one write; the
    small messages that are due at once share a write.
  - The reader never writes channel PDUs: it records what is due and
    wakes a goroutine of `Run` that writes it. `SetClipboard` writes
    from its caller under the same lock, so the backend's writer can
    order a Format List before the keys that paste it.
  - The clipboard (`internal/rdp/cliprdr.go`, [MS-RDPECLIP]):
    `Config.Clipboard` selects `ClipboardFromServer`,
    `ClipboardToServer`, both, or neither, which asks for no channel.
    After the server's Monitor Ready the client sends its general
    capabilities (version 2, long format names) and a Format List of
    its text, if any. Its text goes as CF_UNICODETEXT, or CF_TEXT where
    the server asks for that, with CR LF line ends; the server's comes
    from CF_UNICODETEXT, or CF_TEXT where the list has only that, with
    LF line ends, invalid UTF-16 as U+FFFD, up to its NUL. Text is at
    most `MaxClipboard` (4 MiB) of UTF-8 either way, as the VNC
    backend's Extended Clipboard, and the channel keeps at most
    8 + 2 × 4 MiB of a message. One request for the server's text is
    outstanding at a time; an answer for an announcement the server
    has since replaced is dropped and the newest is asked for, and
    unasked and failed answers are dropped and counted. A server that
    leaves more than 8 of its own requests unanswered ends the
    session. The Format List Responses the client owes are a count of
    64 bits, not a queue, and no number of them ends the session: the
    writer sends 64 a pass, in two writes, and looks again for the rest
    once a pass is written, so that what it builds stays bounded
    however much is owed, and a server that sends lists faster than it
    reads gets the responses as fast as it reads. A first version ended
    the session past 64 owed, for a server that floods lists without
    reading; the review found that this ended sessions with servers
    that read too: a burst of 65 lists in one write, or a server's user
    copying while a long paste held the writer up (xrdp does not wait
    for the responses). In a burst, a Format Data Request for the
    newest list can now go before the responses to the lists before
    it. By its source, xrdp should not mind, as xrdp-chansrv 0.10.1
    takes a response without acting on it
    (`clipboard_process_format_ack` only logs); the integration test
    has not run since this change, and Windows is to be checked in
    phase 4. The server's text is charged against the decoding rate as
    a pointer shape is, a tile at least, so that
    announcing it again and again makes the client wait. A Format List
    that does not parse is refused with a failed response; a message
    whose length is wrong is dropped; locks and file PDUs are ignored.
  - The backend (`internal/backend/rdp`): `DisableCopy` and
    `DisablePaste`, as guacd's `disable-copy` and `disable-paste`, map
    onto the two modes; with both, no channel. `Input` queues a
    `Clipboard` whose MIME type is `text/*` or empty, cut to
    `MaxClipboard` at a character, against a byte budget of two
    clipboards in the queue, as the VNC backend bounds its own; other
    types are dropped. The writer sets the text after the input queued
    before it and before the input after it. The server's text goes to
    `Sink.Clipboard` as `text/plain`.
  - Roles: a read-only viewer's input, its clipboard included, is
    dropped by the session core (`internal/session/viewer.go`), before
    any backend sees it; the backend needs no check of its own.
    `TestReadOnlyInputDropped` now covers a clipboard too. The
    server's clipboard goes to every viewer, as with VNC.
  - `rdptest` gained the server's side of CredSSP with NTLMv2 for an
    `Account` (`rdptest/credssp.go`): a challenge with both NetBIOS
    names, the DNS name and a timestamp; the NTLMv2 response and the
    MIC checked against the password; key exchange and sealing; the
    public key binding of CredSSP 5 and later both ways; the
    TSCredentials the client delegates recorded; STATUS_LOGON_FAILURE
    for a wrong password; and the Early User Authorization Result for
    HYBRID_EX. Options make it speak an older CredSSP version, drop
    sealing from the challenge, prove another public key, or stall
    after either of the client's first two messages. It also gives
    channels their MCS channels, reassembles what the client sends on
    them, refusing chunks over 1,600 bytes, sends its own in chunks,
    and builds and parses CLIPRDR PDUs. It still imports nothing from
    the client.
- **Tests.** Against `rdptest` over loopback: the join, the server's
  channel list (more than asked, the I/O channel, none, 0, 32 in its
  network data, two on one MCS channel), reassembly at chunk sizes of
  1, 7, 1,600 and 32 Ki bytes, a chunk straddling the bound, orphan,
  over- and under-length and compressed chunks, writes of 32 chunks;
  the clipboard's initialization before and after the owner's text,
  both ways, CF_TEXT alone, short format names, the same text not
  announced twice, no text, stale, unasked and failed answers, each
  mode alone, the bounds either way, the requests' bound, pacing, the
  conversions, format lists, and the order of capabilities and Format
  List under a race; the Format List Responses owed, a pass of 64 at a
  time with the writer looking again for the rest
  (`TestClipboardListResponsesPaced`), its allocations the same with
  65 owed as with a million (`TestClipboardListResponsesAllocs`), a
  flood of 100,000 lists the server does not read with the session
  kept and the heap not grown (`TestClipboardListFlood`), bursts of
  65 and 1,000 lists in one write each answered
  (`TestClipboardListBurst`), and a paste held up while the server's
  user copies 1,000 times (`TestClipboardPasteHeldUp`) or read slowly
  while it copies every 2 ms (`TestClipboardSlowPaste`); a write that
  fails, the last or one within a message, stopping the writer
  (`TestChannelWriteFailed`). The backend: both ways through the sink with the
  Format List before the paste's keys, non-text dropped, each of
  `DisableCopy` and `DisablePaste` and both, the byte budget, the
  writer's order against a fake client, and NLA end to end: prompted
  credentials with `nla` and `nla-ext`, then input; a wrong password
  (`ErrAuthFailed` with the server's status); CredSSP 4 refused before
  the client answers the challenge; a challenge without sealing; a
  wrong public key proof, each without credentials delegated; and
  cancel while the server stalls at either step. The client also logs
  on through the acceptor in `internal/rdp`. Coverage, with the
  reviews' tests: `internal/rdp` 88.7% of statements, `channel.go`
  98.0% and `cliprdr.go` 96.9%; `internal/backend/rdp` 98.5%.
- **Integration test.** `TestIntegrationClipboard`, under the
  `rdp_integration` tag, pastes text and reads the session's X
  CLIPBOARD selection, which xrdp-chansrv serves, and sets the
  selection from an X client to see it reach the sink, with
  `internal/rdp/testdata/xclipboard`, an xgb program the test copies
  into the container as it does `querypointer`. On 2026-10-31 against
  the bench desktop image's xrdp, it passed both ways, and the rest of
  the backend's and the client's integration tests passed with the
  channel joined. That the round trip works shows xrdp sends CLIPRDR
  PDUs under TLS without a security header, as the client reads them.
  It has not run again since the second review made the Format List
  Responses a count, as the shared host's load stayed above 8.
- **Mutation checks.** Each was broken in turn and a test failed, 26 in
  all, none surviving:
  - at most 31 channels in the server's network data, no more than
    asked, none twice, not the I/O channel (`TestChannelAssigned`);
  - the reassembly bound, both the cut and the whole check
    (`TestChannelLimit`; the first version of that test let the cut
    survive, since no chunk straddled the bound);
  - compressed, over- and under-length and orphan chunks
    (`TestChannelChunks`);
  - `MaxClipboard` going out and coming in (`TestClipboardBounds`,
    `TestClipboardText`); the data requests' bound
    (`TestClipboardRequestsBounded`); pacing (`TestClipboardPaced`);
  - each mode (`TestClipboardOneWay`), stale and failed answers
    (`TestClipboardStale`), and no Format List before Monitor Ready
    (`TestClipboardInit`, `TestClipboardOrder`);
  - `DisableCopy` and `DisablePaste` (`TestRunClipboardDisabled`),
    non-text dropped (`TestRunClipboard`), the backend's cut and its
    byte budget (`TestRunClipboardBounded`, `TestInputErrors`);
  - the read-only role (`TestReadOnlyInputDropped`), and CredSSP 5 at
    least, through the acceptor (`TestRunNLARefused`).

  A later check found that one of them, the failed answer's, had not
  compiled, which the script took for a failed test; rewritten so that
  it compiles, it is caught. The review of the pull request ran its own
  and found five more that survived, now caught: the clipboard
  channel's bound (`TestClipboardChannelBound`), writeDue's lock, so
  that one message's chunks interleave with another's
  (`TestChannelMessagesWhole`), the surrogate guard on an odd length
  (`TestClipboardText`), the drop of a CLIPRDR message whose length is
  wrong, text included (`TestClipboardLength`), and channel 0 in the
  lookup (`TestClipboardChannelBound`). A second review ran 9 more on
  the fixes and found two survivors: a failed write not recorded,
  either the last of a pass or one within a message, so that the
  writer would go on writing; `TestChannelWriteFailed` now catches
  both. The responses owed, sent a pass at a time, were broken in
  turn too, 6 mutants, all caught: no bound on a pass, a pass of 65,
  no look again for the rest, refusals sent as acceptances, an
  acceptance kept back, and the first version's bound
  (`TestClipboardListResponsesPaced`, `TestClipboardListResponsesAllocs`,
  `TestClipboardListBurst`, `TestClipboardPasteHeldUp`).
- **Fuzzing.** `FuzzChannel` feeds chunks to the reassembly and checks
  the bound; `FuzzCliprdr` feeds a CLIPRDR message, then sets the
  owner's text and writes what is due; `FuzzRun` now joins the
  clipboard channel and has CLIPRDR seeds. 60 seconds each with
  `-fuzzminimizetime 20x`, without a finding: `FuzzChannel` 15,172,432
  executions (host load 3 before, 53 after), `FuzzCliprdr` 12,868,402
  (48, 99), `FuzzRun` 8,956,571 (12, 35). Under that load the fuzzer
  reported no executions for seconds at a time; every input of
  `FuzzCliprdr`'s corpus ran in under 10 ms on its own.
- **Decisions made during implementation.**
  - The clipboard is on both ways by default, as guacd has it.
  - Line ends are converted, CR LF to the server and LF to viewers, as
    the VNC backend converts them, where guacd's `normalize-clipboard`
    preserves them by default. CF_UNICODETEXT has CR LF on Windows;
    xrdp passes them to X as they are, so X applications in an xrdp
    session see CR LF in pasted text.
  - CF_TEXT is taken as Latin-1, with `?` for characters beyond it,
    since the client does not know the server's ANSI code page;
    CF_UNICODETEXT is offered and asked for first.
  - The newest announcement wins: one request at a time, a stale
    answer dropped, as the VNC backend keeps only the latest value.
  - Compressed channel PDUs end the session: the client offers no
    compression.
- **Deviations.** From the Task Breakdown: phase 2 is two pull
  requests, the second with DRDYNVC and Display Control. The Heartbeat
  PDU and the auto-detect answers are deferred to phase 4: the client
  does not set their early capability flags, so a server does not send
  them, xrdp has neither, and only Windows can check them. From guacd:
  line ends (above).
- **Surprises.**
  - xrdp keeps CR LF in the X selection.
  - Under load, the backend's writer set pasted text before or after
    the server's Monitor Ready, and a test that took the first Format
    List to have it failed once in five runs; both orders are right.
- **Follow-ups.**
  - DRDYNVC and Display Control: done in the second pull request
    (below).
  - A `normalize-clipboard` option, if X sessions need LF.
  - The Heartbeat PDU and auto-detect, with #32's Windows host.
  - Formats beyond text (HTML, images) wait for protocol v1 to carry
    them; files are out of v1.0.
  - The owner's text goes to the server unpaced: up to 8 requests may
    wait, each answered with up to 16 MiB of UTF-16 (4 MiB of line
    ends, each a CR LF of four bytes).
  - `MaxClipboard` (4 MiB) equals `protocol.DefaultMaxMessageSize`,
    which counts the message's header, so a server's clipboard near
    4 MiB is decoded and then dropped by every viewer's writer. The VNC
    backend does the same.
  - NTLM sends neither `MsvAvChannelBindings` nor `MsvAvTargetName`,
    which a Windows server that enforces Extended Protection for
    Authentication may require; check in phase 4.
  - Nothing keeps `rdptest` out of the client's non-test imports: a
    depguard rule or an import test could.

## Implementation notes: phase 2, DRDYNVC and Display Control

- **Status.** The second pull request of phase 2: DRDYNVC and Display
  Control, through which a viewer's `Resize` asks the server for a
  desktop of its size. With it phase 2 is done but for the Heartbeat
  PDU and the auto-detect answers (deferred above). The backend is
  still not registered in `occamyd`, which follows the parity gate
  (Task Breakdown).
- **What was built.**
  - DRDYNVC (`internal/rdp/dynvc.go`, [MS-RDPEDYC]). With
    `Config.Resize` the client asks for the static channel `drdynvc`
    (INITIALIZED and ENCRYPT_RDP), whose messages it keeps up to
    8 KiB. It answers the server's capabilities with version 2 at
    most, so a server that speaks 3 falls back and sends neither
    compressed data nor soft-sync. It accepts the Display Control
    channel, one at a time, and refuses other names, an id that is
    open, and a second Display Control with STATUS_UNSUCCESSFUL, as
    FreeRDP does; so at most one dynamic channel is open. A close is
    answered with a close. A message comes in one Data PDU or in a Data
    First that declares its length and Data PDUs after it, reassembled
    up to the channel's own bound (1 KiB for Display Control): a longer
    message is dropped and counted, one that runs past the length it
    declared ends the session, as do compressed data, a Data First
    whose length has size code 3, a create, data or close whose channel
    id has size code 3, and a channel name without its NUL within 256
    bytes. Data for a channel that is not open, a static message cut at
    its bound, and soft-sync are dropped and counted. The answers the
    reader owes wait for the channel writer in a queue of at most 64; a
    server that opens channels faster than it reads the answers ends
    the session. Once a write of the channel writer has failed, this
    bound and the clipboard's bound on data requests report that
    write's error rather than the bound, since the failure is why the
    answers wait (found in review).
  - Display Control ([MS-RDPEDISP]). The server's capabilities give the
    most monitors and the area of each; the client takes their product,
    saturated at the 32 Mi pixels a desktop may have, as the area it
    may ask for. `rdp.Client.RequestSize` clamps a size to 200 to 8,192
    pixels a side, the width even, and keeps the newest. Once the
    channel is open and its capabilities have come, the channel writer
    sends it as a monitor layout of one primary monitor at the origin,
    its physical size unknown (0) and its scale 100%: a new size at
    most every half second, as guacd's `GUAC_RDP_DISP_UPDATE_INTERVAL`,
    and the same size again every two seconds while the desktop
    differs, at most eight times; a size asked for anew starts its
    eight again. The desktop's own size and a size beyond the area are
    not sent. There is no acknowledgement: the server reactivates the
    session at the new size, which `Run` reports to `Output.Resize` as
    any reactivation. As the server may apply a layout late, a size
    asked for back to the desktop's own while the last layout, of
    another size, is within its two seconds is kept until then, and
    sent should the desktop change (found in review: a viewer who
    resized and resized back before the server applied the first size
    was left with it). A server that applies the earlier layout after
    its two seconds still leaves the viewer at that size, as the client
    no longer wants the size asked for back by then; the viewer resizes
    again. Closing the channel forgets the capabilities and
    keeps the size, which goes again when a channel opens. `Run` stops
    the layout timer as it returns.
  - After a reactivation, the client tells a server it had asked to
    suppress its output to suppress it again, since the server's output
    state after Demand Active is its own (phase 1's follow-up). It reads
    the state as it sends, under a lock that `SuppressOutput` holds as
    it sends, so that a viewer's request for output while the server
    reactivates is not undone by a repeat of the state before it (found
    in review). A seam between the read and the send lets a test hold
    the repeat there.
  - The backend asks for DRDYNVC, and `Input` queues a `Resize` with
    both sides above zero, in order with the other input; the writer
    hands it to `RequestSize`, which keeps the newest and paces it.
  - `rdptest` speaks DRDYNVC as a server (`rdptest/dynvc.go`): it sends
    the capabilities of any version, opens and closes channels with
    ids of one, two and four bytes, sends a message in one Data PDU or
    in a Data First and Data PDUs, sends Display Control's
    capabilities, and parses the client's PDUs and monitor layouts.
- **Tests.** Against `rdptest` over loopback (`internal/rdp/dynvc_test.go`):
  no `drdynvc` without `Resize`; the version answered for servers of 1
  to 4; Display Control accepted, and other names and a second Display
  Control refused; a close answered and Display Control
  opened again under another id, the size not yet had sent on it;
  layouts on ids of one, two and four bytes; the first layout waiting
  for the capabilities, pacing with the newest of three winning, the
  clamping and the even width, the retries up to eight and then
  nothing, a size asked for anew sent again, nothing for the desktop's
  own size or beyond the server's area; reassembly in three PDUs, a
  message over its bound in one PDU or in several, data for a channel
  not open, a message longer than declared or than an empty one,
  compressed data, names without their NUL or too long, version 0, a
  length of size code 3 and ids of it in each command; a message of
  exactly the channel's bound whole and one a byte longer dropped, in
  one PDU and in several (`TestDynvcBound`); the answers' bound
  (`TestDynvcAnswersBounded`), and the bounds reporting the writer's
  failure (`TestOwedAfterWriteFailed`) and a write failing within a
  message stopping it (`TestChannelWriteFailed`); the area's saturation
  (`TestDisplayCapsArea`); a size asked for back kept until the server
  applies the size before, or until that size's retry is due, and given
  up once the desktop has it (`TestDisplayResizeBack`), the retry's
  wake kept when a size asked for in between took it
  (`TestLayoutWindow`), and the timer stopped with `Run`
  (`TestDisplayTimerStops`); and Suppress Output repeated after a
  reactivation (`TestSuppressOutputAfterReactivation`), not over a
  viewer's request for output during it (`TestSuppressOutputRace`,
  which holds the client's writes so that the request waits behind the
  reactivation's; it failed 3 runs of 3 on the client before the fix
  and passes whichever of the two goes first), nor when the request
  comes between the repeat's read and its send
  (`TestSuppressOutputReaderFirst`, through the seam). The backend's
  `TestRunResize`: sizes of zero or less ask for nothing, a `Resize`
  goes as a layout, the server's reactivation resizes the sink, and
  Suppress Output is repeated after it. Coverage: `internal/rdp` 90.0%
  of statements, `dynvc.go` 95.1%; `internal/backend/rdp` 98.5%.
- **Integration test.** Under the `rdp_integration` tag, on 2026-10-31
  against the bench desktop image's xrdp 0.10.1 with xorgxrdp 0.10.2:
  the client's `TestIntegrationResize` saw DRDYNVC version 2 and
  Display Control's capabilities (33,554,432 pixels) and resized the
  desktop to 1280×720, 800×600 and back to 1024×768, each drawn to its
  new edges; on a fresh session the first layout met xrdp's login and
  the retry two seconds later applied it. The backend's
  `TestIntegrationResize` resized the sink to 1280×720, 800×600 and
  back to 1024×768, a key painting the probe's swatch at each size.
  The rest of both packages' integration tests passed with the channel
  joined. After the review's fixes both packages' integration tests
  passed again (host load 3 before, 7 after).
- **Mutation checks.** Each was broken in turn, 34 in all, and a test
  failed for 32; a mutant that no longer compiled was rewritten until
  it did. Caught:
  - the answers' bound (`TestDynvcAnswersBounded`; changing the
    constant survived, since the test uses it, so the check itself was
    broken instead);
  - one Display Control at a time, the version answered, the close
    answered, the layouts' state reset on close (`TestDynvcChannels`;
    the capabilities kept on close survived, which the review found,
    below);
  - the channel's bound in several PDUs and in one, the length
    declared, compressed data, the name's bound, size code 3, a
    truncated static message (`TestDynvcData`,
    `TestDynvcAnswersBounded`; the bound in one PDU survived until
    `TestDynvcData` sent such a message);
  - the area with no monitors (`TestDisplayCapsArea`), the area check,
    waiting for the capabilities, the desktop's own size, the least
    width and height, the even width, the greatest height, pacing, the
    tries' bound, the retry, the earlier of two wake-ups, a size asked
    for anew (`TestDisplayControl`);
  - Suppress Output after a reactivation, and the state it is repeated
    from (`TestSuppressOutputAfterReactivation`);
  - the backend's width and height, `Resize` asked for, and sizes of
    zero or less, either side (`TestRunResize`; the width's check
    survived until a `Resize` of height alone was sent).

  Two survive, both equivalent: refusing an id that is open, which only
  Display Control could be and which a second Display Control is
  refused for anyway, kept so that a second handler cannot replace an
  open channel; and an id from 256 to 65,535 sent in four bytes rather
  than two, which DRDYNVC allows.

  The review ran 28 mutants of its own and found five more that
  survived, now caught: the bound of a Data First and of a single Data
  PDU one byte off (`TestDynvcBound` sends 1 KiB and 1 KiB and a byte);
  a Data First that declares 0 bytes taken as a whole message
  (`TestDynvcData`); the look at the layout after a reactivation
  removed (`TestDisplayResizeBack`, whose retry is an hour away, so
  that the reactivation alone has the client look); and the
  capabilities kept on close (`TestDynvcChannels` now checks that
  nothing goes on the reopened channel before its capabilities). Its
  sixth, refusing an id that is open, is the first equivalent one
  above. The review's fixes were broken in turn too, 15 mutants, which
  the tests caught: each condition of keeping a size asked for back
  and the look again when its window ends (`TestDisplayResizeBack`,
  `TestLayoutWindow`); size code 3 for each command and the check
  itself (`TestDynvcData`); the writer's failure recorded, read at the
  bounds, and reported at each (`TestOwedAfterWriteFailed`); and the
  timer stopped with `Run` (`TestDisplayTimerStops`). The Suppress
  Output fix was checked by running `TestSuppressOutputRace` on the
  client before it.

  Those 15 did not cover the fixes, though: a second review ran 19
  mutants on them, and 6 survived. Three are now caught: the Suppress
  Output repeat releasing its lock before it sends, or taking none
  (`TestSuppressOutputReaderFirst`; `TestSuppressOutputRace` holds the
  client's writes, so its reader blocks before it reads the state, and
  both mutants passed it); and a write failing within a message not
  recorded (`TestChannelWriteFailed`). Three still survive:
  - `SuppressOutput` releasing its lock before it sends is equivalent:
    the backend's writer is its only caller, so two never race, and
    the repeat, which would read the new state, sends what
    `SuppressOutput` sends;
  - the writer's failure set under its own lock alone, not under the
    channels' too, is a data race that no deterministic test sees, as
    is `Run` stopping the layout timer before the writer has ended,
    which a last pass of the writer may set again: both are timing.
- **Fuzzing.** `FuzzDynvc` feeds DRDYNVC PDUs and writes what is due
  after each, checking the answers' and the channels' bounds;
  `FuzzRun` now joins `drdynvc` beside the clipboard's channel, asks
  for a size, and has DRDYNVC and reactivation seeds. 60 seconds each
  with `-fuzzminimizetime 20x`, without a finding: `FuzzDynvc`
  14,576,219 executions (host load 3 before, 60 after), `FuzzRun`
  8,299,215 (57, 45).
- **Decisions made during implementation.**
  - One layout of one monitor, the primary, at the size asked for:
    viewers have one canvas.
  - The size is retried, where guacd sends a layout only when the size
    changes and keeps no state of the server's answer: xrdp drops a
    layout while its login is in progress ("Not allowing resize. Login
    in progress."), which a viewer's first `Resize` usually meets, and
    then the desktop would keep its size until the viewer resizes
    again. Eight tries two seconds apart bound it for a server that
    never applies a size.
  - At most one dynamic channel is open, so the bound on channels is
    the one Display Control rather than a count.
  - A channel id of size code 3, which [MS-RDPEDYC] 2.2.2.1 and 2.2.3.1
    call an invalid value, ends the session, as a length of it does:
    the client answers a create or a close in the size the server used,
    and has no valid one to answer this in. FreeRDP reads it as four
    bytes; no server is known to send it.
  - A second capabilities PDU is answered again, and a create before
    the capabilities is answered as any other, rather than ending the
    session: FreeRDP takes both, and what the client holds stays within
    the answers' bound either way (the review asked whether to end the
    session on them).
  - Any interactive viewer's `Resize` asks for a size, not only the
    owner's, and a viewer that resizes without end is bounded by the
    pacing: one layout every half second at most, the newest size
    winning, and eight tries of a size. Whether only the session's
    owner may resize waits on the project owner's decision (follow-up).
- **Deviations.** From the Task Breakdown: "DRDYNVC with a bounded
  number of channels" is one channel, Display Control; GFX in phase 3
  will be the second. From guacd: the retries (above).
- **Surprises.**
  - xrdp refuses a layout during its login and says so only in its
    log; nothing comes back on the channel.
  - xrdp logs "Physical desktop dimensions (835586x0) are invalid" for
    each connection: it reads the core data's physical size past the
    end of the client's block, from the header of the block after it
    (0x000CC002, the security data's type and length). It then ignores
    them, so nothing changes; the client's block is as long as its
    header says.
  - xrdp logs that a physical size of 0 is out of range for each
    layout, and uses none, as [MS-RDPEDISP] allows.
- **Follow-ups.**
  - The Heartbeat PDU and auto-detect, with #32's Windows host
    (above).
  - Display Control against Windows (phase 4), whose sizes and pacing
    may differ.
  - GFX over DRDYNVC (phase 3): a handler and a bound of its own, and
    the version 3 features it may need.
  - Resize for the session's owner alone, if the project owner decides
    so; any
    interactive viewer can resize now (above).
