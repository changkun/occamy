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
   else to drop.
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
  [guacd-bridge](guacd-bridge.md), not these phases.

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
    connection.
  - Control and Pause (Break) could not be sent: Pause always sent
    its sequence. Now it is Break while Control is down by the keys
    sent (decision 4), and keysym Break maps to E0 46 too.
  - A Set Error Info reason was remembered for the whole connection,
    so a reset long after a reason the session went on from was taken
    for that end. Now a reason holds only until a share data PDU other
    than another reason, a fast-path update or a reactivation; a
    Deactivate All keeps it, since [MS-RDPBCGR] 1.3.1.4.2 puts one
    between the reason and the Disconnect Provider Ultimatum.

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
    can see keeps sending until the next change of demand.
  - Session-level dedupe of identical cursor shapes before they are
    encoded for each viewer, for the encode-cache task: each new shape
    is copied by the session and encoded losslessly per viewer, and a
    server can send 227 distinct 384×384 shapes a second within the
    decoding rate.
  - Inherited from the VNC backend's text mapping: U+0014 taps Scroll
    Lock, since the C0 controls map to keysyms 0xff00 and up, and CR LF
    types Return twice.
  - The VNC backend sends Power (E0 5E) through QEMU's extended key
    events; it may want the same drop as RDP.
  - Lang1 and Lang2's scancodes, unchecked against Windows.
  - At registration in `occamyd`'s backend factory, with the
    connection configuration's `rdp` parameters (`RoutingCookie` among
    them): a sentence in [docs/protocol.md](../../docs/protocol.md)'s
    4005 row that RDP's deliberate ends (decision 5) are `remote-ended`,
    and a CHANGELOG line.
