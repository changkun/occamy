---
title: Instruction I/O over any stream, with length-aware framing
status: validated
track: server
depends_on: []
affects:
  - internal/protocol/instruction.go
  - internal/protocol/io.go
  - internal/protocol/instruction_test.go
  - server/session.go
effort: medium
created: 2026-10-29
updated: 2026-10-29
author: Changkun Ou
dispatched_task_id: null
---

# Instruction I/O over any stream, with length-aware framing

## Goal

Let `protocol.InstructionIO` run over a `net.Conn` to guacd, not only over a
socketpair file descriptor, and read whole instructions by their length
prefixes instead of splitting at the first `;`. Every later task in
[005](../005-upstream-guacamole.md) reads guacd through this type.

## What to do

1. `internal/protocol/instruction.go`: change the constructor to
   `NewInstructionIO(rw io.ReadWriteCloser) *InstructionIO`. The `conn`
   field becomes an `io.ReadWriteCloser`; `Close` closes it.
2. `internal/protocol/io.go`: keep `IO` (the fd wrapper) for the libguac
   path until [remove-libguac](remove-libguac.md) deletes it. Add a
   `Close` doc comment saying it exists for the socketpair only.
3. `server/session.go` `Join`: construct with
   `protocol.NewInstructionIO(protocol.NewIO(fds[1]))`. No other change.
4. Rewrite `(*InstructionIO).ReadRaw` to frame by length. Today it is
   `io.input.ReadBytes(';')`, which cuts an instruction in two whenever an
   element contains `;` (a password, clipboard text or an `error` message),
   and the client then receives two broken WebSocket messages. Read loop:
   - read ASCII digits up to `.` (at most `InstructionMaxDigits`, else
     `ErrInstructionBadDigit`);
   - read exactly that many UTF-8 code points (lengths count code points,
     not bytes; reject invalid UTF-8 with `ErrInstructionBadRune`);
   - read one terminator byte: `,` continues, `;` ends the instruction,
     anything else is `ErrInstructionMissComma`;
   - append every byte read to the returned slice so the result is the raw
     instruction including `;`, exactly as today.
   Fail with a new `ErrInstructionTooLong` once the raw size passes
   `MaxInstructionLength` code points (8192, the same as
   `GUAC_INSTRUCTION_MAX_LENGTH` in guacamole-server 1.6.0, so guacd's
   own output always fits), and with `ErrInstructionTooManyElements`
   past `InstructionMaxElements`.
5. `Read` keeps calling `ParseInstruction` on the raw bytes. Leave
   `ParseInstruction` and `Parser` alone apart from reusing the shared
   constants.
6. Fix the `ErrInstructionMissSemi` message typo ("withou") while there.

## Tests

In `internal/protocol/instruction_test.go`:

- `TestInstructionIOOverPipe`: write three instructions into one end of
  `net.Pipe()` and read them back from the other with `ReadRaw`.
- `TestReadRawSemicolonInValue`: `4.args,3.a;b;` comes back as one
  instruction.
- `TestReadRawMultibyte`: lengths count code points (`2.日本`).
- `TestReadRawLimits`: six-digit length, 129 elements, an over-long element
  and a bad terminator each return the matching error.
- `TestReadRawPartialReads`: an `io.Reader` that returns one byte per call
  still yields whole instructions.
- `FuzzReadRaw`: never panics, and whatever it accepts round-trips through
  `ParseInstruction` and `String`.

Existing tests and `benchmark_test.go` keep passing; report the benchmark
delta of `ReadRaw` in the commit message.

## Boundaries

- No guacd code, no session or routing changes.
- Do not merge `Parser` and `ParseInstruction`, and do not change the
  instruction model.
- `internal/lib` is untouched.
