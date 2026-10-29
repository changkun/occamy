---
title: guacd handshake client
status: validated
track: server
depends_on:
  - specs/005-upstream-guacamole/instruction-stream-io.md
affects:
  - internal/guacd/
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# guacd handshake client

## Goal

A pure-Go package that dials guacd 1.6, performs the Guacamole handshake
for a new connection or a join, and returns a connection ready to proxy.
It is not wired into the server yet.

## Handshake (verified against guacamole-server 1.6.0)

From `src/guacd/connection.c`, `src/libguac/user-handshake.c` and
`src/libguac/user-handlers.c` at tag `1.6.0`:

1. Client sends `select` with one argument: a protocol name (`vnc`, `ssh`,
   `rdp`, ...) for a new connection, or a connection ID starting with `$`
   to join one. An unknown ID gets
   `error,"No such connection.",519` (`RESOURCE_NOT_FOUND`), then the
   socket closes.
2. guacd replies `args`. The first element is the protocol version
   (`VERSION_1_5_0` in 1.6.0); the rest are parameter names.
3. Client sends, in any order, `size` (width, height, dpi), `audio`,
   `video`, `image` (each a list of mimetypes), `timezone` and `name`,
   then `connect`. The first element of `connect` is the client's protocol
   version; the rest are one value per parameter name, in `args` order.
   guacd rejects the join if the count differs.
4. guacd replies `ready` with the connection ID (`$` plus a UUID). It
   sends `ready` before it checks the argument count and before the
   backend connects, so a `ready` does not mean the remote desktop is
   reachable. Later failures arrive as `error` and `disconnect` in the
   normal stream.
5. guacd ends a connection when its last user leaves (`src/guacd/proc.c`),
   not when the owner leaves.

## What to do

1. New package `internal/guacd`:

   ```go
   type Config struct {
       Protocol     string            // used when ConnectionID is empty
       ConnectionID string            // "$..." to join an existing connection
       Params       map[string]string // values for the names guacd sends in args
       Width, Height, DPI int
       ImageMimetypes, AudioMimetypes, VideoMimetypes []string
       Timezone, Name string
   }

   type Conn struct {
       ID      string // connection ID from ready
       Version string // version guacd sent in args
       // wraps *protocol.InstructionIO
   }

   func Dial(ctx context.Context, addr string, c Config) (*Conn, error)
   func Handshake(ctx context.Context, nc net.Conn, c Config) (*Conn, error)
   func (c *Conn) ReadRaw() ([]byte, error)
   func (c *Conn) WriteRaw(b []byte) (int, error)
   func (c *Conn) Close() error

   type Error struct { Message string; Status int } // from an error instruction
   ```

2. `Handshake` sets a deadline on `nc` from `ctx`, or 15 s if `ctx` has
   none (guacd's own `GUACD_USEC_TIMEOUT`), and clears it after `ready`.
3. Send `connect` with the constant `ProtocolVersion = "VERSION_1_5_0"`.
   Fill values from `Params` by name and send `""` for names it lacks.
   Fail if `args` does not start with `VERSION_` (guacd before 1.1 is not
   supported).
4. Send `timezone` and `name` only when set. Defaults: size 1024×768 at 96
   DPI; image `image/png`, `image/jpeg`, `image/webp`; no audio or video.
5. An `error` instruction at any point before `ready` becomes `*Error`.
   Anything else unexpected is a protocol error naming the opcode.
6. Build every instruction with `protocol.NewInstruction(...).String()`.
   No hand-built strings.

## Tests

`internal/guacd/handshake_test.go`, with a fake guacd on `net.Pipe()`:

- `TestHandshakeNewConnection`: asserts the exact instruction sequence,
  that `connect` carries the version and then values in `args` order, and
  that missing names become empty strings.
- `TestHandshakeJoin`: `ConnectionID: "$abc"` sends `select,$abc` and
  returns `ID == "$abc"`.
- `TestHandshakeNoSuchConnection`: an `error` reply returns `*Error` with
  status 519.
- `TestHandshakeTimeout`: a fake that never answers fails when the
  context deadline passes.
- `TestHandshakeSpecialValues`: a password containing `;`, `,` and
  multibyte characters reaches the fake intact.
- `TestHandshakeOldGuacd`: `args` without a `VERSION_` element fails.
- `TestDialRealGuacd`: runs only when `OCCAMY_GUACD_ADDR` is set, and
  completes a `vnc` handshake against a real guacd (no backend needed,
  since `ready` comes before the connect attempt).

## Boundaries

- Not used by `server/` yet; no config keys.
- No WebSocket code, no session table.
- No TLS to guacd (a later option in [004](../004-security-hardening.md)).
