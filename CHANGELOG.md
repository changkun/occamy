# Changelog

All notable changes to Occamy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Occamy follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Releases before 0.5.0 were tagged without the `v` prefix.

## [Unreleased]

### Added

- The desktop baseline and the per-scenario targets for protocol v1
  ([docs/benchmarks.md](docs/benchmarks.md)). `bench/vncprobe` measures
  the bench desktop's VNC server on its own.
- The benchmark's v1 series, `v1-native` and `v1-guacd`: the same
  scenarios over protocol v1, driven by the Go client core through API
  v1 against native SSH, VNC and RDP and against VNC and RDP through
  guacd, on the bench stack's `occamy-v1` service
  ([docs/benchmarks.md](docs/benchmarks.md#the-v1-series)). The harness
  counts a v1 frame when the client core delivers it, and acknowledges a
  graphical frame on a 60 Hz screen's next tick, as the web client
  does, and a terminal's at once; the report records both, and
  `-ack-hz 0` is a diagnostic. A terminal's G7 ends at its first cell
  that is not blank. A desktop's key carries its HID usage, as the web
  client's does. CI runs them quickly after the v0 smoke run, every
  run with `-failfast`, which stops at the first error, stops the bench
  job at 30 minutes, and prints the stack's logs unless the job
  succeeded. Their full results wait for the rest of the graphics
  pipeline and for native RDP.
  `make bench` takes a compose project and ports for a second stack on a
  shared host.
- A public Go package for protocol v1, `changkun.de/x/occamy/protocol`,
  with its framing: the message header and its ignorable flag, the field
  encodings, a registry of message types, and the maximum message size.
  Golden vectors in `protocol/testdata/framing.json` let other
  implementations check their codecs against it.
- Every message of protocol v1 in the `protocol` package, with the
  handshake (`hello` in the upgrade query, the token in a subprotocol,
  capabilities agreed in `welcome`), codecs and close codes, golden
  vectors in `protocol/testdata/messages.json`, and
  [docs/protocol.md](docs/protocol.md), the protocol's normative
  description. `docs/protocol-cn.md` is now marked as the v0 protocol.
  A `max_message_size` below 64 KiB is refused.
- `internal/auth`, the tokens of API v1: Ed25519 JWTs for access
  (a user and groups) and for sessions (a session and a role), signed by
  occamyd or by a configured trusted issuer, with no other algorithm
  accepted. occamyd's own tokens name `occamy` as issuer and audience,
  so a trusted issuer may not be named `occamy`.
- `internal/session`, the session core of v1: one goroutine per session
  that runs a backend, draws into the framebuffer or the terminal, applies
  input by role (owner, viewer, read-only), keeps each viewer's small
  state as latest values, ends the session when the owner stays away or
  it idles, and a registry of sessions with share tokens that die with
  them.
- `internal/backend/guacd`, the guacd bridge of v1: a graphical backend
  that applies guacd's drawing and copies each frame into the session's
  framebuffer before it answers the frame's `sync`, so that guacd paces
  against the bridge alone. It turns guacd's pointer, title, clipboard
  and credential requests into session events, and viewers' keys
  (but for those without a keysym, which guacd cannot take), pointer,
  wheel, text, clipboard and resizes into Guacamole instructions.
  `internal/guacamole`'s display now applies `cursor`,
  reports damage as rectangles, and copies areas out.
- A conformance corpus for the guacd bridge: guacd 1.6's VNC, RDP and
  SSH sessions on the bench stack, recorded with `go test -tags record`
  and replayed by `go test` without containers, which fails on any
  error, unsupported operation or log line, on a checkpoint that does
  not show the scene's idle screen, on a changed pixel where a `copy`
  moved content, on prompts, handshake, credential answers or sync
  answers that differ from the recording's, and on a changed final
  frame. `OCCAMY_CONFORMANCE_DUMP` names a directory for the frames of
  failed checks. RDP is recorded with `disable-gfx`, since through the
  graphics pipeline xrdp's RemoteFX Progressive codec shifted the
  desktop's colours by up to 22 levels before guacd saw them.
- A native SSH backend for protocol v1, `internal/backend/ssh`, in pure
  Go. It accepts only pinned host keys, by key or SHA-256 fingerprint,
  unless told to ignore them, which it logs as a warning. It
  authenticates with key files or a password, which also answers a
  keyboard-interactive password challenge, and asks the session's owner
  for anything else. It feeds the shell's terminal output to the
  session.
- Version 1 configuration files (`version: 1`): sign-in through static
  users, OIDC and trusted issuers, a connection registry whose
  credentials name files or environment variables instead of holding
  secrets, TLS, the debug listener, origins and ad-hoc targets. Unknown
  keys are errors, and validation reports every problem with its line and
  key path ([docs/configuration.md](docs/configuration.md)). A file
  without `version` is read as v0, as before.
- `internal/terminal`, the headless terminal emulator of a terminal
  session: immutable, versioned screen snapshots that share unchanged
  rows, and the diffs that bring a viewer's terminal from the screen it
  last received to the current one, sending scrolled lines as line feeds
  so the client's scrollback fills.
- `internal/stream`, a viewer's connection in protocol v1: `welcome`,
  then the client's input and views applied as they come, and frames at
  the pace of the viewer's link. Small state never waits behind tiles,
  and an input's effect goes ahead of motion already on the screen.
  Graphical sessions get tiles in the codecs the viewer agreed to;
  terminal sessions get the diff between the screens a viewer was sent,
  so bulk output coalesces, an echo goes ahead of a status line or other
  output already on the screen, and the terminal's answers to the
  shell's queries reach the shell. A client's `close` with a code other
  than `normal` is logged at info, since a browser's close frame
  carries 1000 for most codes.
- A native VNC backend for protocol v1, `internal/backend/vnc`, in pure
  Go: RFB 3.3, 3.7 and 3.8 with the security types None and VNC
  Authentication, asking the session's owner for a password the
  configuration lacks, and the Raw, CopyRect, DesktopSize and Cursor
  encodings in the framebuffer's own pixel layout. It asks for screen
  updates only while a viewer can see the screen, sends keys, pointer,
  wheel and Latin-1 clipboard text, and reads the server's clipboard as
  UTF-8 when it is valid UTF-8. It checks every length the server sends
  before it reads or allocates, resizes the screen at most once an
  update and ten times a second, and bounds its queued input by bytes.
- The native VNC backend decodes Tight, ZRLE and Hextile, and asks for
  them in that order before Raw. Tight stays lossless, since the backend
  asks for no JPEG quality, but JPEG and the gradient filter are decoded
  when a server sends them. JPEG must be sequential (baseline or
  extended), with a scan for each component and at most 65536 pixels, as
  TigerVNC, libvncserver and QEMU send it. Compressed lengths, palettes
  and runs are checked before anything is read or allocated, a
  rectangle's zlib data is inflated only as far as its pixels need, and
  an update may draw at most four times the screen's pixels. The fuzz
  corpus holds sessions recorded from x11vnc and TigerVNC.
- RFB extensions in the native VNC backend: VeNCrypt with an X.509
  certificate and Plain, None or VNC Authentication inside TLS, asking
  the owner for a username or password the configuration lacks;
  ExtendedDesktopSize, so that a `Resize` resizes the remote screen,
  except to a size the server refused, or at all once it prohibits
  resizing; continuous updates with Fence, paused while nobody can
  see; QEMU extended key events with physical key codes; Extended
  Clipboard in UTF-8 both ways; and LastRect. The server's certificate
  must chain to the system's roots or a configured CA and name the
  server, or be pinned by its SHA-256 fingerprint; an insecure mode
  accepts any with a warning. The backend prefers VeNCrypt whenever a
  server offers it and fails closed, so a server with a self-signed
  certificate that VNC Authentication reached before is refused, with
  the fingerprint to pin in the error. VeNCrypt's anonymous TLS
  subtypes are not supported, so a server whose VeNCrypt offers only
  those, or Plain in the clear, is refused even if it offers VNC
  Authentication too. Cut text from the server allocates about
  three times the text kept, down from seven. An integration test runs
  against TigerVNC (`docker/demo/tigervnc`) in a container of its own.
- Apple Remote Desktop authentication (security type 30) in the native
  VNC backend, so that it can log in to macOS Screen Sharing as a user
  of the Mac, but only when the backend's `vnc.Config.ARD` allows it:
  its key exchange authenticates no server, so anyone between Occamy and
  the Mac can read the account's password, or, with `ARD` set, strip
  VeNCrypt from any server's offer to get it. A connection's `ard: true`
  sets it (below). A server that offers only type 30
  otherwise fails with an error that names the setting. The
  server's key length, prime, generator and public key are checked
  before anything else, a username or password longer than 63 bytes is
  refused rather than cut short, and the owner is asked for what the
  configuration lacks. It has not yet been checked against a Mac. The
  VNC integration tests remove only images and containers named for a
  run bare or under `localhost/`, never one pulled from a registry.
- The codec measurement of the graphics pipeline
  ([docs/benchmarks/codecs.md](docs/benchmarks/codecs.md)): a corpus of
  13 frames recorded from the bench desktop by `make corpus`, and
  `bench/codecs`, a module of its own that `make codecs` runs, which
  compares palette or raw pixels with deflate, PNG, QOI, lossless and
  lossy WebP and JPEG on them for size, time and SSIM. It picks deflate
  for lossless tiles, 64-pixel tiles, RGBA pixels, and WebP for lossy
  tiles with JPEG as the fallback.
- The codecs of the graphics pipeline. `internal/encoder` encodes a tile
  of up to 256 colours losslessly, in deflate over a palette, and one of
  more in lossy WebP, or in JPEG for a client that lists JPEG but not
  WebP, in pure Go with gen2brain/vpx and gen2brain/jpegn; adjacent
  lossy tiles share one message. `internal/stream` sends each viewer's
  tiles and cursor in the codecs it agreed to, cursors with straight
  alpha. `internal/codec` holds the deflate container and raw pixels
  both ways, with the standard library only, for clients to decode with.
  [docs/protocol.md](docs/protocol.md) fixes the deflate container's
  layout, with alpha for cursors, and the most a receiver inflates, and
  `protocol/testdata/codecs.json` holds a payload of each codec occamyd
  sends and deflate payloads a client must refuse. The image's
  `licenses/` carries vpx's, jpegn's and jpegli's notices.
- Scroll detection in the graphics pipeline. Each viewer's writer keeps
  a shadow of the screen its client shows. It finds the changed regions
  that client already shows elsewhere, moved up or down, by xxh3-64 row
  hashes and a pixel-for-pixel check, and sends a `copy-rect` for them
  with tiles only for what the copy leaves. On the recorded scrolling
  text this is 5.0 KiB a frame against 58.5, in a seventh of the encode
  time ([docs/benchmarks/scroll.md](docs/benchmarks/scroll.md)). A
  viewer that skipped frames, was hidden or has a viewport gets copies
  from what it has. A wheel's scroll counts as its input's effect. A
  copy completes a tile only at the pacer's current quality or above, so
  lossy pixels drawn at a lower quality are sent again as they would be
  without it. A frame searches at most 2h+3 runs for h copies found, so
  motion beside a scroll costs a few runs. The shadow costs about 4 bytes
  a pixel a viewer: 3.3 MiB at 1024×768, 8.5 MiB at 1080p and 33.5 MiB at
  4K. `stream.Config.NoScroll` turns it off. The image's `licenses/`
  carries the notices of zeebo/xxh3 and of klauspost/cpuid, which it
  links into amd64 builds, and `scripts/third-party-licenses.sh` lists
  both architectures of every release platform.
- `occamyd` serves a v1 file: `GET /healthz`, `POST /api/v1/auth/token`
  (static users sign in with a password for an access token), TLS when
  `tls.cert` and `tls.key` are set, expvar and pprof on the debug
  listener when `debug.address` is set, and the v0 routes with
  `legacy_v0: true`. API v1 errors share one JSON shape,
  `{"error":{"code":"…","message":"…"}}`. SIGINT and SIGTERM drain
  requests in flight for up to 5 s, then cut what is left with a
  warning and exit with status 0; a second signal stops occamyd at once.
  `debug.address` must not share a port with `listen`, counting a
  wildcard host and `localhost`. A `version` merged in with `<<` counts,
  and one outside the file's first YAML document is an error. A v0 file
  is served as before.
- `occamyd` serves sessions and streams from a v1 file.
  `GET /api/v1/connections` lists the connections the caller may open.
  `POST /api/v1/sessions` opens a session on one, or on an ad-hoc target
  that `adhoc_allowlist` covers when `allow_adhoc_targets` is on, and
  `GET` and `DELETE /api/v1/sessions/{id}` and
  `POST /api/v1/sessions/{id}/shares` read, close and share it for its
  owner. `GET /api/v1/connections/{id}/stream` opens a session and
  streams it in one request, and `GET /api/v1/sessions/{id}/stream`
  joins a running one, both as protocol v1 WebSockets with the token in
  a subprotocol, never in the URL, and `Origin` checked against
  `origins`. Graphical streams offer `webp`, `jpeg`, `deflate` and
  `raw`. ssh, vnc and rdp connections use the native backends, telnet
  the guacd bridge, and vnc and rdp connections the guacd bridge only
  when they set `backend: guacd` (below). ssh connections need `host_keys` or
  `insecure_ignore_host_key: true`. vnc connections take a `username`
  for VeNCrypt's Plain, and a `tls` block for the server's certificate:
  a CA file, pins by fingerprint, or, alone, an insecure mode for demos. A
  session belongs to a subject of the issuer that signed its owner's
  token, and of where the user signed in, and a trusted issuer may not
  name a static user, in any case or normal form, unless its
  `link_static_users` is on, nor use a subject that OIDC sign-in
  would refuse. Every token names its user, and a trusted issuer's
  session token, of any role, joins only its own users' sessions, or
  a static user's when it is linked. A trusted issuer's `groups` count
  only for the groups in its `allowed_groups`. A panic while handling
  a client's message, in a stream's reader or a session's backend, ends
  that stream or session with `internal-error` and is logged, rather
  than ending occamyd. A viewer that acknowledges no frame, or takes
  no message, for `streams.viewer_timeout` (30 s) gets the new
  `close` `too-slow` 4008, and a hidden viewer of a terminal no
  longer keeps the screen it was last sent, though one that may type
  still keeps the screen of its last input. A static user's name follows the
  same rule as an OIDC subject. An OIDC user
  linked to a static user owns the static user's sessions, and occamyd's
  own stream tokens carry `idp`. `limits` bounds the sessions occamyd
  runs (1000) and a user owns (10), and the viewers (50) and unexpired
  shares (100) of a session.
  An ad-hoc target is allowed only on its protocol's port or the ports
  its allowlist entry names, never on a loopback, link-local,
  unspecified or multicast address, a cloud's metadata service, guacd,
  or occamyd's own listeners unless an entry is that address,
  and it takes only the display's `guacd.params`. `guacd.params`
  refuses any parameter whose name holds `password`, `passphrase`,
  `private-key` or `secret` in any case, but for a prompt's pattern
  such as telnet's `password-regex`. A terminal is at most 1000 by 500 cells,
  and a screen 8192 pixels a side.
- Sign-in through an OpenID Connect provider, with `auth.oidc`: the
  authorization code flow with PKCE, the ID token's issuer, audience,
  expiry and signature checked by go-oidc with asymmetric algorithms
  only, and its nonce and `azp` by occamyd, and the user and groups
  taken from `subject_claim` and `groups_claim`. The browser goes back
  to one of `auth.oidc.return_urls` with a one-time code in the
  fragment, which the client exchanges at
  `POST /api/v1/auth/oidc/token`, with its own PKCE verifier, for an
  access token, so the token never appears in a URL. An OIDC subject
  that names a static user, in any case or Unicode normal form, is
  refused unless `link_static_users` links the two, and one with
  characters that render as nothing is refused always.
  `require_known_users` admits only the users whom an allow list names,
  exactly, or by group. occamyd follows no redirect from the provider,
  checks RFC 9207's `iss` on every answer to the callback, and a
  provider that is down fails OIDC sign-ins, not occamyd's start
  ([docs/configuration.md](docs/configuration.md#signing-in-through-oidc)).
- Access tokens name where the user signed in, in `idp`: `local` for a
  static user, `oidc` through OIDC. The sign-in's log line names it too.
- The image carries the license, notice and patent files of the Go
  code compiled into occamyd, in `/occamy/licenses/THIRD_PARTY_LICENSES`,
  as release archives do. `scripts/third-party-licenses.sh` also takes
  those of compiled packages that have their own, such as go-jose's
  fork of `encoding/json`, and fails, writing nothing, when `go list`
  fails, instead of writing the standard library's license alone.
- `changkun.de/x/occamy/client`, a public Go client of protocol v1 in
  pure Go, for tests, tools and the Swift client's export layer. `Dial`
  performs the handshake over a WebSocket, and `Next` delivers frames
  decoded into a client framebuffer with their dirty rectangles,
  terminal bytes untouched, and the small state, ending with the close
  code. A frame is applied as `Next` delivers it and acknowledged when
  the caller asks for the next event, so a slow caller holds the server
  back instead of queueing frames, and input never waits for `Next`.
  The frame shown and not yet acknowledged counts against the frame
  window, and each queued message against the backlog at its size and
  512 bytes more. Tiles of plugged codecs decode in parallel. A
  `welcome` the client cannot show, with no codec or terminal level in
  common, is refused with a `*RefusedError` that names the session the
  server made, so that the caller can end it. No API stability promise
  until the release candidate.
- The browser's client of protocol v1, a TypeScript library in
  `client/occamy-web/src/protocol` that depends on no framework: the
  handshake over a WebSocket, deflate through `DecompressionStream`,
  WebP and JPEG through `createImageBitmap`, decoding in Workers, whole
  frames presented to a canvas, keyboard input as keysyms with USB HID
  usages, pointer, wheel, clipboard and close codes. It bounds what a
  server can make it hold as the Go client does, feeds the inflater a
  slice at a time so that Chrome cannot inflate a bomb ahead of the
  cap, decodes images without their EXIF, which browsers apply
  whatever they are told, fails a session whose decoding Worker dies or
  falls silent, and in a hidden page acknowledges frames as they
  arrive. It runs every golden vector of `protocol/testdata` under
  vitest, and an interop test against occamyd's stream code, built with
  the race detector, in the `web` job. The web client runs on it
  (specs/009).
- The web client, rebuilt in React on the browser's client of protocol
  v1 (specs/009). It speaks API v1 alone: sign-in with a static user's
  password or through OIDC with PKCE, the connections the user may
  open, and a session on protocol v1, a graphical one on a canvas scaled
  to the window and a terminal one in xterm.js. The owner resizes the
  remote with the window, answers the remote's credential prompts,
  shares the session through a link as a viewer or read-only, and ends
  it, signing in again over the session when the sign-in has expired;
  the clipboard goes both ways as the role allows, and reaches the local
  clipboard on its own only for a role that gives input, so a read-only
  viewer copies it from the clipboard panel; a hidden page tells occamyd
  so and keeps acknowledging frames; and the end of a session says why
  it ended, a takeover by the owner's newer stream too. Its build
  writes the licenses of the packages it bundles to
  `dist/THIRD_PARTY_LICENSES.md`, which the image ships in
  `licenses/web/` and the web client archive in `dist/`.
- `web_client` in a v1 file: the directory of the built web client,
  which occamyd serves under `/static/` with a Content-Security-Policy
  that keeps the page to its own origin, without directory listings or
  symbolic links out of the directory, and with its hashed `assets/`
  cached for a year; a v0 file's `client: true` serves `/static/` the
  same way. Validation refuses a `web_client` that is a file or that
  occamyd cannot read; one not built yet is a warning. The demo stack's
  `occamy-v1` serves the image's at http://localhost:5637/static/, and
  opens the demo SSH server as the connection `ssh` through the native
  SSH backend.
- `internal/rdp`, the pure-Go RDP client of the RDP spike: TLS with the
  VNC backend's certificate policy, NLA through CredSSP 5 or later with
  NTLMv2 of its own, offered by default, the connection sequence, bitmap
  updates in interleaved RLE and planar, pointer updates, and keyboard
  and mouse input. It bounds the desktop, resizes and the rate of
  decoding a server can ask for. It is tested live against xrdp under
  the `rdp_integration` build tag, which starts a container, and by
  replaying xrdp's recorded PDUs; NLA is not yet tested against Windows.
- `internal/backend/rdp`, the native RDP backend's first phase
  ([native-rdp-backend](specs/008-native-architecture/native-rdp-backend.md)),
  on `internal/rdp`: NLA by default, or TLS, with the owner asked for
  the credentials NLA needs and the configuration lacks; no routing
  cookie, which names the user before TLS, unless `RoutingCookie` asks
  for one for a load balancer; bitmap updates and the pointer drawn
  into the session, pointer updates paced like bitmaps, a shape before
  it is decoded; keys by physical code, with Control and Pause sent as
  Break and the Power key dropped, text as Unicode keyboard events, and
  the pointer and wheels, batched into input PDUs from a bounded queue
  in writes of at most 1,024 events; Suppress Output while nobody can
  see. A server's reason for ending the session decides how it ended,
  even when a write fails as it hangs up. occamyd serves rdp with it
  by default (below). The VNC and RDP backends share one USB HID to
  scan code table, `internal/keymap`.
- A `backend` key on each v1 connection, `native` or `guacd`, and
  `adhoc_backends`, which picks the backend of ad-hoc targets by
  protocol. A protocol defaults to `native` wherever a native backend
  exists, so vnc and rdp connections and ad-hoc vnc and rdp targets
  default to occamyd's own VNC and RDP clients, and `backend: guacd`
  (or `adhoc_backends: {vnc: guacd}`) is an opt-in fallback through
  guacd, kept as the benchmark's baseline until the benchmark shows
  native at least 10x better. ssh is always native, and telnet always
  goes through guacd, and the session's state names the backend that
  serves it. guacd would not check a VeNCrypt
  certificate as `tls` says, so a vnc connection with `tls` cannot set
  `backend: guacd`, and a file that does fails to load, naming the
  key. Without `tls`, the native backend checks a VeNCrypt server's
  certificate against the system's roots. No release served v1 vnc or
  rdp connections. A native vnc session asks the
  server for the owner's size from the stream's `hello`, on a
  connection's stream as on a session's, and a resize that reaches the
  backend before it runs is no longer dropped. A size beyond the
  backend's limits, such as a hello's 8192×8192, which is more than
  2^25 pixels, is not asked for: the server keeps its size.
- `ard: true` on a vnc connection on the native backend allows Apple
  Remote Desktop authentication, for macOS Screen Sharing. It is
  refused with `backend: guacd` and with a `tls` that requires TLS,
  and the error for a Mac that offers only ARD names the key on the
  connection.
  [docs/configuration.md](docs/configuration.md#apple-remote-desktop-ard)
  describes the risk: anyone in between can read the Mac account's
  whole password.
- [docs/api.md](docs/api.md), the reference of API v1: every route with
  its tokens, requests, replies, errors and limits, ad-hoc targets,
  shares, the stream upgrades and the OIDC routes. A test fails when a
  route has no section in it, or a section names no route; routes are
  added only through the list that test reads, from which each
  listener's mux is built.
- API v1 refuses, with 400 `bad_request`, a JSON body that names a
  field twice, at any depth, or names one in a case other than its
  own, such as `Connection` for `connection`. encoding/json takes the
  last of two and matches names in any case, so a proxy in front of
  occamyd that reads the first, or matches names exactly, would have
  seen a different request. A body nests at most 32 deep. The
  refusals of `GET /api/v1/auth/oidc/login` carry
  `Cache-Control: no-store`, as its redirects do.
- `internal/backend/vnc/vnctest`, a fake RFB server for tests outside
  the backend, which counts the credentials clients send, and tests
  that drive native VNC sessions through occamyd's routes with the Go
  client: credentials stored and prompted, the screen, input, resizes,
  clipboard both ways, the cursor, visibility, the end reasons, the
  `tls` policy, which sends no credential to a server it refuses, and
  ad-hoc targets by address and by name, dialled at the address that
  was checked.
- The demo stack runs `occamy-v1` next to `occamy`, on port 5637, with
  the v1 file `docker/occamy-v1.yaml`: the demo VNC and RDP servers
  through the native backends and through guacd, which `make e2e`
  streams with the Go client. `OCCAMY_IMAGE`, `OCCAMY_PORT` and `OCCAMY_V1_PORT` move a
  second stack aside on a shared host, and `make e2e` follows the
  last two.
- TigerVNC in the demo stack, beside x11vnc, and the guacd bridge's
  parity matrix for VNC: `TestVNCParity` runs authentication, resize,
  clipboard both ways, the cursor, keyboard layouts with AltGr and dead
  keys, text input, sharing roles and reconnect on both servers,
  through the native backend and through guacd, and fails on any cell
  that differs from the matrix in the guacd bridge's spec. The demo
  desktops report what reaches them, typed lines and clipboard text,
  over the session's clipboard. The two TigerVNC test images became
  one, `docker/demo/tigervnc`, which the integration tests build too.
  x11vnc hangs now and then after an X error as a client connects; the
  demo x11vnc runs under a watchdog that kills it when it reports an X
  error or stops accepting connections, and starts it again. The matrix
  tries a guacd cell up to three times, and a native cell that cannot
  reach the desktop only while guacd cannot reach it either, and shows
  the try at which a cell settled. A listed failure must fail with its
  own error, and a unit test keeps the spec's matrix in step with the
  suite.
- A soak, `make soak`: sessions through the demo stack's `occamy-v1`
  for hours, while `TestSoak` samples `occamyd`'s goroutines, live heap
  and open files from its debug listener and fails on growth.
  `/debug/vars` publishes `goroutines` and `fds`. A v1 file's
  `debug.address_env` takes the debug listener's address from an
  environment variable, and leaves the listener off while it is unset
  or empty.
- The native RDP backend's clipboard, the first part of its second
  phase: `internal/rdp` joins static virtual channels and reassembles
  their chunks within a bound per channel, and carries text both ways
  on the clipboard channel (CLIPRDR), CF_UNICODETEXT or CF_TEXT, at
  most 4 MiB of UTF-8, as `Config.Clipboard` allows. The backend copies
  and pastes it, a paste set before the keys that follow it, unless
  `DisableCopy` or `DisablePaste` stops a direction, as guacd's
  `disable-copy` and `disable-paste` do. The Format List Responses the
  client owes are a count, written 64 at a time as fast as the server
  reads, so that neither a burst of lists nor copies during a long
  paste end the session, and a flood costs no memory; a server that
  leaves more than 8 of its data requests unanswered ends it.
  `rdptest` accepts NLA through CredSSP with NTLMv2, so that the
  backend's NLA path, prompted credentials included, runs end to end in
  tests.
- The native RDP backend's resize, the second part of its second
  phase: `internal/rdp` speaks DRDYNVC, version 2 at most, and accepts
  the Display Control channel, through which `RequestSize` asks the
  server for a desktop size within the area it takes: the newest size,
  at most one every half second, and again every two seconds, up to
  eight times, until the desktop has it, since xrdp ignores a size
  while its login is in progress. A size asked for back to the
  desktop's own before the server applied the one before it is kept
  while the server may yet apply that one. The backend asks for any
  interactive viewer's `Resize`; a server without Display Control keeps
  its size, which viewers scale. After a reactivation the client tells
  a server it had asked to suppress its output to suppress it again,
  unless a viewer has asked for output since. A dynamic channel id of
  size code 3 ends the session, and once the channel writer fails, a
  server that the client then owes too many answers ends it with the
  write's error.
- occamyd serves rdp connections and ad-hoc rdp targets with the native
  RDP backend by default, with `backend: guacd` (or
  `adhoc_backends: {rdp: guacd}`) as the fallback through guacd. A
  native rdp connection takes `tls`, the certificate policy of vnc's,
  checked on every connection since the backend always runs TLS, with
  pins of RDP certificates, and `security`, the mode as guacd names it:
  `nla` by default, `nla-ext`, `tls` or `any`. Both are refused with
  `backend: guacd`, which reads `guacd.params.rdp` instead. xrdp speaks
  no NLA, and a server that selects standard RDP security when offered
  NLA alone fails the session with an error that names the key to set,
  `security: tls on connection "…"`. A `username` of the form
  `DOMAIN\user`, with both parts, logs on to the domain, as does one the
  owner gives at the prompt with the domain left empty, and `\user` logs
  on as the user alone. The owner, and only the owner, is asked for the
  credentials NLA needs that the connection does not store; the hello's
  size is asked for within an RDP desktop's, 200 to 8192 pixels a side
  and 2^25 in all; clipboard text reaches the server from the owner and
  interactive viewers and every viewer from the server; a logoff ends
  the session with `remote-ended`, and a refused logon or certificate
  with `backend-failed`. occamyd warns when it loads the file of the
  `guacd.params` that no session reads, for every protocol, and of each
  connection whose `security: tls` or `any` comes with
  `tls.insecure_skip_verify`. guacd's `disable-copy`, `disable-paste`
  and recording parameters have no native equivalent yet, so a
  connection that needs them keeps `backend: guacd`. Tests drive native
  RDP sessions through occamyd's routes against `rdptest`, and the demo
  stack serves its xrdp server as `rdp-native` (`security: tls`) and
  `rdp-guacd`, both streamed by `make e2e`, which also takes
  `rdp-native` through the clipboard both ways, answered by a clipboard
  echo in the demo image, a resize, a viewer joined by a share, and the
  owner leaving and coming back. The native RDP backend is
  not yet verified against Windows, and does not speak RDP's graphics
  pipeline yet
  ([native-rdp-backend](specs/008-native-architecture/native-rdp-backend.md)).
- The graphics pipeline (GFX) in `internal/rdp`, the first part of the
  native RDP backend's third phase, behind `Config.GFX`, which the
  backend does not set yet: the capability exchange over DRDYNVC, ZGFX
  decompression with its 2.5 MB history, surfaces within a bound on
  their pixels composed onto the desktop, at most 64 of them mapped,
  solid fills, copies within and between surfaces (a copy within a
  surface goes to viewers as a copy where no other surface covers it
  and the output shows the surface's own pixels, which the surfaces
  under one deleted, replaced or moved, or under a bitmap update, draw
  again to keep), uncompressed and planar bitmaps (AYCoCg, colour loss
  and chroma subsampling included, planar within a bitmap update's
  bound), and frame acknowledgements, bounded, which the server waits
  for. Decompression's growth, each copied point, each redraw after a
  resize, and every cell that the surfaces draw again where a surface
  left the output or a bitmap update drew under them are charged to
  the decoding rate. A server that does not open the
  channel, or closes it, draws with bitmap updates. RemoteFX
  Progressive, RemoteFX, ClearCodec and the cache come next, and with
  them GFX by default and a key that turns it off. Against xrdp 0.10.1
  the channel, the capabilities, a reset to the size the desktop has,
  ZGFX's stored segments, a surface, planar bitmaps of xrdp's first,
  black screen, and frame acknowledgements (a frame a key) ran live;
  that the acknowledgements pace xrdp was not checked against a run
  without them. Compressed ZGFX, planar's
  AYCoCg, solid fills, surface-to-surface copies and uncompressed
  bitmaps, which xrdp did not send, are checked against the
  specification's examples, the tests' own compressor and encoder, and
  `rdptest` only, until a Windows host checks them.
- A native Kubernetes backend, `internal/backend/kubernetes`, in pure
  Go and without client-go: a `kubernetes` connection opens a terminal
  in a pod's container through the API server's `pods/exec` (or
  `pods/attach`), over the `v5.channel.k8s.io` WebSocket subprotocol
  with `v4.channel.k8s.io` as fallback. It authenticates with a bearer
  token (`token_file`, `token_env` or a prompt) or a client
  certificate, sends the token only after the API server's certificate
  is checked against `tls` (the system's roots, `ca_file` or `pins`),
  never logs it, and refuses names that are not Kubernetes's. It has no
  guacd backend and no ad-hoc targets.

### Changed

- The plan for v1.0 (owner, 2026-10-31): it waits until the benchmark
  shows native at least 10x better than guacd, and ships no guacd
  fallback for a protocol still short of it; release candidates may
  carry one. The gate is each scenario's target as defined, which may
  fall short of 10x only where a documented bound outside Occamy makes
  10x impossible, justified in `docs/benchmarks.md`
  ([specs/008-native-architecture/benchmark.md](specs/008-native-architecture/benchmark.md#targets)).
- Building occamyd needs Go 1.26.4 or later, which gen2brain/vpx and
  gen2brain/jpegn require.
- `make bench` builds occamyd's image from the working tree before
  measuring it, instead of measuring whatever `occamy:latest` was.
- The v0 routes moved from `/api/v1/login` and `/api/v1/connect` to
  `/api/v0/login` and `/api/v0/connect`, leaving `/api/v1` to API v1.
  The web client, the end-to-end tests and the benchmark use the new
  paths; tokens, responses and close statuses are unchanged. occamyd
  serves them with the standard library's `net/http`, golang-jwt v5 and
  coder/websocket, so gin, gin-jwt and gorilla/websocket and their
  indirect dependencies are gone from `go.mod`. `auth.jwt_alg` must be
  HS256, HS384 or HS512, and `auth.jwt_secret` must not be empty.
- Performance: `internal/rdp` takes the names it counts bitmaps under
  from a table of constants instead of formatting them, so a bitmap no
  longer allocates. A PDU of 64 bitmaps of 64×64 now makes 1 allocation
  of 120 bytes instead of 129 allocations of 804 bytes. Its time is
  unchanged within noise, because decoding dominates it.

### Removed

- The Vue web client, with its fork of guacamole-common-js
  (`occamy.js`) and the fork's Apache LICENSE and NOTICE, element-plus,
  vue-router and axios. The web client no longer speaks the v0 routes
  or the Guacamole protocol, which occamyd still serves to other
  clients until v1.0. A v0 file's `client: true` still serves
  `/static/`, but the web client there needs a v1 file.

### Fixed

- `internal/guacamole`'s display, which the benchmark and the guacd
  bridge draw with, decodes lossy WebP as libwebp, and so a browser,
  does. `golang.org/x/image/webp` alone read VP8's limited-range colours
  as full-range and did not interpolate chroma, which put guacd's WebP
  images up to 38 levels a channel off libwebp's on a first recording
  (320 images), and up to 34 on the 36 of the committed conformance
  corpus.
- The same display reads an image's size before decoding it and refuses
  one wider or taller than 16,384 pixels or larger than 8192×8192 in
  all, so that an `img` stream cannot make the guacd bridge allocate
  gigabytes.
- A panic while a backend draws, which a decoder's bug and a remote
  server's data can cause, no longer leaves the session's screen locked
  with every viewer's stream waiting for it: the VNC, RDP and guacd
  backends release the pixels they lock by `defer`, and a session closes
  its framebuffer when it ends, which releases any pixels still locked.
  The streams close with 1011, as for any panic the session recovers.
- Viewers get a terminal's title as the session holds it, even when the
  title has invalid UTF-8: each invalid byte becomes U+FFFD, three
  bytes, which could make the title too long for the escape sequence a
  terminal frame carries it in, so that a viewer got it cut there, with
  no regard for characters. Titles are now cut, between characters, at
  what that sequence carries.

### Security

- occamyd's request log no longer records query strings, which carry the
  v0 token and with it the remote credentials.
- A secret file, `password_file`, `token_file` or
  `auth.oidc.client_secret_file`, of more than 64 KiB is refused, so
  that a path such as `/dev/zero` cannot fill occamyd's memory.
- A panic on a goroutine a backend starts, such as the SSH backend's
  readers of the remote's output and the VNC, RDP and guacd backends'
  writers, ends its session with close 1011 instead of crashing occamyd
  and every other session with it. A panic in the terminal emulator,
  and one under the state the VNC and RDP backends share between their
  reader, their writer and the session's input, releases its locks, so
  that the session cannot hang on them, and a guacd writer's panic ends
  a prompt's wait for the owner's answer.
- No backend sends the Power key or the X11 power keysyms
  (XF86PowerOff, PowerDown, Sleep, Suspend, Hibernate, Standby), which
  the remote applies to the whole machine. The native VNC backend sent
  Power through QEMU's extended key events, and the VNC and guacd
  backends sent the keysyms.
- The demo stack's debug routes, which have no authentication, are off
  unless `make run OCCAMY_DEBUG=1` turns them on: `occamy-v1`'s debug
  listener on the host's loopback interface, port 5638
  (`OCCAMY_V1_DEBUG_PORT`), and the v0 service's pprof, from
  `docker/occamy-debug.yaml`. `docker/occamy.yaml`, which served pprof
  to anyone who could reach the v0 service, is now in release mode, and
  the v0 service is published on the loopback interface only.
  `make run` and `make bench` turn the routes off explicitly without
  `OCCAMY_DEBUG=1`, so a variable left in the shell or in `docker/.env`
  cannot turn them on. The pressure test and the soak need
  `OCCAMY_DEBUG=1`.

## [0.6.0] - 2026-10-30

### Added

- A benchmark harness (`make bench`, `bench/`) that measures sessions
  under fixed scenarios: input latency, bytes, server CPU and client
  decoding, over emulated networks. Its numbers for the Guacamole
  protocol are the baseline that protocol v1 is designed against
  ([docs/benchmarks.md](docs/benchmarks.md)).
- Desktop scenarios for the harness over VNC and RDP: a still desktop,
  key latency, scrolling text, a dragged window, full-screen motion and a
  photograph. They run on a bench desktop (`docker/bench/desktop`) whose
  session is a scene program (`bench/scene`) drawing the same pixels in
  every run.
- The README documents connection parameters in `guacd.params` and how to
  reach a Windows RDP host. `conf.yaml` lists `domain` and `security` as
  comments ([#32](https://github.com/changkun/occamy/issues/32)).
- CI runs the pressure test, which crashed `occamyd` before 0.5.0
  ([#20](https://github.com/changkun/occamy/issues/20)), with 10 rounds.
  Afterwards, `occamyd`'s goroutines must return to their number before
  the storm, and CI warns if guacd keeps a process.
- A guide to profiling `occamyd` in debug mode, watching guacd's
  processes, and the pressure test
  ([docs/debugging.md](docs/debugging.md),
  [#18](https://github.com/changkun/occamy/issues/18)).

### Fixed

- A client that connects just as the last user of a session with the
  same credentials leaves gets a new session, instead of joining the
  ending one and being disconnected.

## [0.5.0] - 2026-10-29

Development resumed in 2026 after a pause from the end of 2021.

### Added

- The web client sends its window size, pixel density and time zone, so
  the remote desktop matches the browser window.
- Demo servers built from the repository for linux/amd64 and linux/arm64
  (`docker/demo/`), and end-to-end tests that CI runs on every push
  (`make e2e`).
- Releases: a multi-arch image on GHCR, `occamyd` archives for Linux, macOS
  and Windows on amd64 and arm64 with the licenses of their dependencies,
  the web client archive, and `SHA256SUMS`, all with build provenance
  attestations.
- A changelog, security policy, code of conduct and contribution guide.
- License and NOTICE files for the web client's fork of
  guacamole-common-js (`client/occamy-web/`), also shipped in the image
  under `/occamy/licenses/` and in the web client archive.
- Design specs for the road to v1 in `specs/`.

### Changed

- Sessions are served by upstream guacd 1.6 instead of the bundled
  libguac. `occamyd` needs a reachable guacd: set `guacd.address` in
  `conf.yaml` (default `localhost:4822`), and `guacd.params` for
  per-protocol connection defaults. The demo stack runs one.
- RDP runs through guacd, whose FreeRDP is current, and is verified against
  an xrdp server added to the demo stack. The bundled plugin needed
  FreeRDP 1.x, which current distributions no longer ship.
- Sessions start and join without holding a proxy-wide lock, and a
  session ends when its last user leaves. A failed connection no longer
  blocks later connections with the same credentials.
- Errors close the WebSocket with the Guacamole status code as the close
  reason, so that the client can show it.
- Go 1.26 or later is required. All Go dependencies have been upgraded and
  the `vendor/` directory removed. The module path is
  `changkun.de/x/occamy`.
- The web client uses Vue 3 and Vite.
- `occamyd` builds without cgo (`CGO_ENABLED=0`) and so on Linux, macOS
  and Windows ([#25](https://github.com/changkun/occamy/issues/25)). The
  image runs it on distroless static as an unprivileged user and is about
  35 MB instead of 172 MB. Podman is the default container engine; set
  `CONTAINER_ENGINE=docker` to use Docker.
- Instructions are framed by their declared lengths, with upstream
  Guacamole's limits on element length and count.
- The demo stack addresses its servers by service name (`vnc:5900`,
  `ssh:22`) instead of fixed IP addresses.
- Session IDs are shorter.
- Logging uses the standard library instead of logrus.

### Removed

- The bundled guacamole-server 1.0.0 fork and its cgo bindings
  (`guacamole/`, `internal/lib/`). guacd took their place.
- The `/api/v1/ping` endpoint.

### Fixed

- A failed join no longer leaves the proxy locked for every other
  connection.
- SSH sessions can be opened again after the first one ends
  ([#22](https://github.com/changkun/occamy/issues/22)).
- Ending a VNC session no longer crashes `occamyd`.
- Tokens no longer stop validating after one hour of uptime.
- Two tokens whose claims concatenate to the same string (username `ab`
  with password `c`, and username `a` with password `bc`) no longer share
  a session. Session keys are a SHA-256 hash of the length-prefixed claims
  instead of an MD5 hash of their concatenation.
- `occamyd` closes connections whose request headers take longer than
  10 seconds, so slow clients cannot hold connections open.
- `occamyd` shuts down cleanly on SIGTERM.
- Indirect dependencies with known vulnerabilities have been upgraded.

## [0.4.0] - 2020-02-17

### Added

- A Guacamole JavaScript client maintained in the repository, with
  per-protocol client plugins.
- Clipboard support.
- Instructions for building from source.

### Changed

- The bundled guacamole-server was trimmed further: audio support, the
  user handshake and the keep-alive thread were removed, and the VNC
  plugin was consolidated.
- The latest Go release is used.

### Fixed

- Keyboard input in the web client.

## [0.3.0] - 2019-11-12

### Changed

- Inputs are handled on the connection's goroutine instead of a new
  thread, and fewer parameters cross into cgo.
- The bundled guacamole-server dropped session recording, WebP, libjpeg,
  unused sockets and `wait-fd`.
- CI moved to GitHub Actions.

## [0.2.0] - 2019-11-05

### Added

- A connection pressure test (`tests/`).
- `pprof` in debug mode.
- Default credentials for the three demo protocols in the web client.

### Changed

- libguac is self-managed in `guacamole/` instead of a git submodule.
- `release` is the default running mode.

### Fixed

- Workarounds for segmentation faults in the SSH plugin.

## [0.1.0] - 2019-01-31

### Added

- First release: a Go proxy that drives libguac through cgo and serves the
  Guacamole protocol over WebSocket with JWT authentication.

[Unreleased]: https://github.com/changkun/occamy/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/changkun/occamy/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/changkun/occamy/compare/0.4.0...v0.5.0
[0.4.0]: https://github.com/changkun/occamy/compare/0.3.0...0.4.0
[0.3.0]: https://github.com/changkun/occamy/compare/0.2.0...0.3.0
[0.2.0]: https://github.com/changkun/occamy/compare/0.1.0...0.2.0
[0.1.0]: https://github.com/changkun/occamy/releases/tag/0.1.0
