# Occamy Architecture

Figure 1 illustrates the architecture of a Guacamole application. It needs a
Guacamole Servlet (a Java web application) for authentication and for tunneling the
protocol to the browser, and `guacd` as a second proxy that connects to the remote
desktop servers, with one process per connection.

```
|-- Browser --|-------- Guacamole Server -----------|--- Intranet ---|

                                                     +---- RDP server
UserA --------+                                      |
              +------ Guacamole Servlet --- guacd ---+---- VNC server
UserB --------+                                      |
                                                     +---- Others
```

_Figure 1: Guacamole Architecture_

Occamy replaces both the Guacamole Servlet and guacd with a single Go
binary, `occamyd`, as shown in Figure 2. It signs users in through API
v1, reaches the remote desktops and shells itself, with a backend of
its own for each protocol (SSH, VNC, RDP and Kubernetes), and streams
each session to the browser in protocol v1 over WebSocket: a terminal
as text, and a desktop as tiles of pixels.
[specs/008](../specs/008-native-architecture.md) describes the design.
Occamy 0.x put a Go proxy in front of guacd, which reached the remotes;
v1.0.0-beta.2 removed guacd and the Guacamole protocol.

```
|-- Browser --|----- Occamy Server -----|--- Intranet ---|

                                        +---- RDP server
UserA --------+                         |
              +------ occamyd ----------+---- VNC server
UserB --------+                         |
                                        +---- SSH server
```

_Figure 2: Occamy Architecture_

A session goes through these steps ([api.md](api.md),
[protocol.md](protocol.md)):

1. The client signs in, with a password at `POST /api/v1/auth/token` or
   through OIDC, and receives an access token.
2. It opens a connection's stream, `GET /api/v1/connections/{id}/stream`:
   a WebSocket that carries protocol v1, with the token in a
   subprotocol.
3. `occamyd` checks that the connection admits the user, and opens a
   session, whose backend dials the remote with the connection's
   credentials and asks the owner, on the stream, for what the file
   does not store.
4. The backend draws into the session's framebuffer, or feeds its
   terminal, and each viewer's stream sends that viewer what it has not
   seen yet, at the pace its link allows. Others join through a share.
   The session ends when its owner ends it, when the remote does, or
   when the owner has been gone longer than its grace.

## Components

Each package names the spec it implements.

| Path | Role |
|------|------|
| [protocol/](../protocol) | Protocol v1, a public package: framing, the handshake (hello in the upgrade query, the token in a subprotocol, capabilities and their agreement), every message with its encoding and decoding, close codes, size limits, golden vectors; described in [protocol.md](protocol.md) ([protocol-v1](../specs/008-native-architecture/protocol-v1.md)) |
| [internal/backend/](../internal/backend) | The interface every backend implements (SSH, VNC, RDP and Kubernetes), its input events and prompts, and scripted fakes for tests ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/pacer/](../internal/pacer) | Per-viewer pacing: the frame window sized from the time an ack takes, the frame after an input sent at once, bytes in flight held near the bandwidth-delay product, and quality that adapts ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/framebuffer/](../internal/framebuffer) | A session's screen in tiles, versioned from one counter, so each viewer is sent only the tiles it has not seen ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [server/](../server) | The v1 server on a `net/http` ServeMux: middleware for the request log, recovery, body limits and access tokens; API v1's JSON errors; `/healthz`, `GET /api/v1/auth/methods` and `POST /api/v1/auth/token`; sign-in through OIDC (authorization code with PKCE on x/oauth2, ID tokens checked by go-oidc, state in a sealed cookie, and a one-time code that the browser's client exchanges for an access token); the connections and sessions of API v1 on the session registry, with the backend factory (SSH, VNC, RDP and Kubernetes, one backend for each protocol) and the ad-hoc allowlist; the streams, WebSocket upgrades checked for `Origin` and token and handed to `internal/stream`; TLS, the public listener's TCP congestion control, BBR by default on Linux, graceful shutdown that ends sessions and their streams, and expvar and pprof on the debug listener. Every route, with its tokens, replies and errors, is described in [api.md](api.md) and, for tools, in [openapi.yaml](openapi.yaml), which `TestOpenAPI` checks against the code ([api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/auth/](../internal/auth) | Tokens of API v1: Ed25519 JWTs for access (a user and groups) and for sessions (a session and a role), from occamyd or a trusted issuer ([api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/config/](../internal/config) | Version 1 configuration files: sign-in, the connection registry with credential references, TLS and the debug listener, decoded strictly and validated as a whole ([configuration](./configuration.md), [api-and-server-stack](../specs/008-native-architecture/api-and-server-stack.md)) |
| [internal/session/](../internal/session) | The session core: one goroutine per session runs a backend, applies input by role, closes the clipboard in the directions a connection asks, keeps each viewer's small state as latest values, and a registry holds sessions and share tokens ([session-core](../specs/008-native-architecture/session-core.md)) |
| [internal/backend/ssh/](../internal/backend/ssh) | The native SSH backend: a pure-Go client that verifies pinned host keys, logs in with key files or a password and asks the owner for what they cannot answer, and feeds a shell's PTY output to the session's terminal ([native-ssh-backend](../specs/008-native-architecture/native-ssh-backend.md)) |
| [internal/backend/kubernetes/](../internal/backend/kubernetes) | The native Kubernetes backend: runs a command in a pod's container, or attaches to it, through the API server's `pods/exec` over the `v5.channel.k8s.io` WebSocket subprotocol, falling back to `v4.channel.k8s.io`, with no client-go; checks the API server's certificate against the system's roots, a private CA or pins before it sends a bearer token, which it never logs; validates the pod's names so that none reaches another path; feeds the command's output to the session's terminal and sends resizes on the resize channel; `kubetest` is a fake API server for tests ([native-kubernetes-backend](../specs/008-native-architecture/native-kubernetes-backend.md)) |
| [internal/backend/vnc/](../internal/backend/vnc) | The native VNC backend: a pure-Go RFB client that logs in with None, VNC Authentication, VeNCrypt's X.509 subtypes with the server's certificate checked against the system's roots, a private CA or pins, or, only where a connection's `ard: true` allows it, Apple Remote Desktop authentication for macOS Screen Sharing, whose key exchange authenticates no server, asking the owner for credentials it lacks; asks for screen updates only while a viewer can see, by continuous updates where the server offers them; draws Tight (lossless, and sequential JPEG if a server sends it), ZRLE, Hextile, Raw and CopyRect rectangles, resizes and the cursor into the session's framebuffer; resizes the remote screen with ExtendedDesktopSize; and sends physical key codes and UTF-8 clipboard text where the server takes them ([native-vnc-backend](../specs/008-native-architecture/native-vnc-backend.md)) |
| [internal/terminal/](../internal/terminal) | The headless terminal emulator of a terminal session: immutable, versioned screen snapshots that share unchanged rows and carry each row's version, and the diffs that bring each viewer's terminal up to date ([native-ssh-backend](../specs/008-native-architecture/native-ssh-backend.md)) |
| [client/](../client) | The Go client of protocol v1, a public package: the handshake over a WebSocket, a client framebuffer with dirty rectangles and parallel decoding, a tile cache of 1024 slots by default, terminal bytes passed through, input numbered for latency, and events pulled with `Next`, for tests, tools and the Swift client's export layer ([client-core](../specs/008-native-architecture/client-core.md)) |
| [client/occamy-web/src/protocol/](../client/occamy-web/src/protocol) | The browser's client of protocol v1, TypeScript without a framework: the handshake over a WebSocket, tiles decoded in Workers (deflate through `DecompressionStream`, WebP and JPEG through `createImageBitmap`), whole frames presented to a canvas, a tile cache of 1024 slots by default, keyboard input as keysyms with USB HID usages, pointer, wheel and close codes; the web client's shell runs on it. `client/internal/webinterop` serves `internal/stream` to its interop test ([009](../specs/009-react-web-client.md)) |
| [internal/stream/](../internal/stream) | A viewer's connection: welcome, the reader of its input and view, and the writer that sends small state ahead of frames, tiles in the codecs the viewer agreed to or terminal screen diffs, and an input's effect ahead of motion, at the pace its pacer allows ([session-core](../specs/008-native-architecture/session-core.md)); the writer keeps a shadow of the screen its client shows, and sends what that client already has elsewhere, scrolled up or down, as `copy-rect`, found by row hashes and checked pixel for pixel, and keeps a heat per tile: tiles in motion go at the pacer's lowest quality, and once still for the refine delay are refined in background frames, exact up to 256 colours and at the quality SSIM picks above; and it mirrors its client's tile cache, by xxh3-128 hashes of the tiles each slot holds under a secret seed of its own, so that a tile the client has stored goes as `cached`; the viewers of a session share its encode cache, where each encoding of a tile, or of a run of tiles at a quality, is made once for all of them, and its workers, which encode a frame's rows ahead of the writers ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [internal/codec/](../internal/codec) | Payloads of the lossless codecs, both ways, with the standard library only: raw pixels, palettes, and the deflate container, whose decoder refuses malformed payloads and inflates at most one byte past the rectangle's longest container ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [internal/encoder/](../internal/encoder) | A viewer's tile encoder: counts each tile's colours, a moving one's only as far as 17, then encodes it in deflate when it has up to 256 (16 while it moves) and in lossy WebP or JPEG when it has more, a still one at the lowest quality whose SSIM reaches 0.91 on each tile, found by a search that starts where the last ended and gallops when that is far, and its cursor losslessly with straight alpha, in the best of the codecs the viewer agreed to ([graphics-pipeline](../specs/008-native-architecture/graphics-pipeline.md)) |
| [internal/rdp/](../internal/rdp) | The RDP spike's pure-Go client: TLS with the VNC backend's certificate policy, NLA (CredSSP with its own NTLMv2; xrdp has no NLA, and it has logged on only to a Windows Server 2025 CI runner over loopback), the connection sequence, bitmap updates in interleaved RLE and planar drawn into a backend's sink within bounds on size and on the rate of decoding, keyboard and mouse input, batched, static virtual channels with the clipboard's (CLIPRDR) for text, and dynamic virtual channels (DRDYNVC) with Display Control, through which it asks the server to resize the desktop, and, when asked (`Config.GFX`), the graphics pipeline (GFX): ZGFX, surfaces composed onto the desktop, solid fills, copies within and between surfaces, uncompressed, planar, RemoteFX and RemoteFX Progressive bitmaps, and frame acknowledgements; ClearCodec and the cache are not implemented yet; `rdptest` is a fake server for tests, which accepts NLA through CredSSP of its own and builds GFX messages with a ZGFX compressor and a RemoteFX encoder of its own ([rdp-strategy](../specs/008-native-architecture/rdp-strategy.md)) |
| [internal/backend/rdp/](../internal/backend/rdp) | The native RDP backend on `internal/rdp`: connects with NLA by default, or TLS, asking the owner for the credentials NLA needs that the configuration lacks; asks for the graphics pipeline unless the connection sets `disable_gfx`; draws bitmap updates, the graphics pipeline's surfaces and the server's pointer into the session's framebuffer; sends keys by physical code, text as Unicode keyboard events, and the pointer and wheels from a bounded queue; copies and pastes clipboard text through the clipboard channel unless the configuration disables a direction, and tells the viewers when text has waited a minute for the server's clipboard, which it goes on holding; asks the server through Display Control for the size a viewer resizes to; and tells the server to suppress its output while nobody can see. rdp's backend in occamyd, tested against xrdp and `rdptest`, and tried on a Windows Server 2025 CI runner, not a real Windows deployment ([native-rdp-backend](../specs/008-native-architecture/native-rdp-backend.md)) |
| [internal/keymap/](../internal/keymap) | The USB HID to scan code set 1 table that the VNC backend's physical key codes and the RDP client's scancodes share ([native-rdp-backend](../specs/008-native-architecture/native-rdp-backend.md)) |
| [client/occamy-web/](../client/occamy-web) | The web client: a React shell (`src/views`, with the session logic in `src/lib`, free of React) on the protocol v1 client core in `src/protocol`, which it imports through the core's index alone. It signs in through API v1 (a password, or OIDC with PKCE), lists the connections, and streams a session: a graphical one on a canvas, a terminal one in xterm.js ([009](../specs/009-react-web-client.md)) |
| [bench/](../bench) | Benchmark harness: scenarios, emulated networks, and the measurements in [benchmarks.md](./benchmarks.md); its guacd series speaks the Guacamole protocol to guacd straight, with the client in `bench/internal/guacamole` and `bench/internal/guacd`, the baseline that Occamy is measured against |

## License

[Occamy](https://github.com/changkun/occamy) | [MIT](../LICENSE) &copy; 2019 [Ou Changkun](https://changkun.de)