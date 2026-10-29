---
title: Instruction I/O over any stream, with length-aware framing
status: complete
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
4. Add `ReadRaw(r *bufio.Reader) ([]byte, error)` as a package-level
   function that frames by length, and make `(*InstructionIO).ReadRaw`
   call it with its `input` reader. It is exported so that
   [guacd-sessions](guacd-sessions.md) can split a WebSocket message
   holding several instructions (`bufio.NewReader(bytes.NewReader(msg))`)
   without touching this package. Today `(*InstructionIO).ReadRaw` is
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
   Fail with a new `ErrInstructionTooLong` when an element's declared
   length exceeds `MaxInstructionLength` code points (8192). libguac
   (`src/libguac/parser.c`, 1.6.0) applies `GUAC_INSTRUCTION_MAX_LENGTH`
   per element the same way, so guacd's own output always fits. Fail with
   `ErrInstructionTooManyElements` past `InstructionMaxElements`.
5. `Read` keeps calling `ParseInstruction` on the raw bytes. Leave
   `ParseInstruction` and `Parser` alone apart from reusing the shared
   constants, and from accepting U+FFFD, which is valid UTF-8 even
   though it decodes to `utf8.RuneError`.
6. Fix the `ErrInstructionMissSemi` message typo ("withou") while there.
7. Update `TestNewInstructionIO` (`instruction_test.go`), which passes a
   raw fd, to the new constructor (`NewInstructionIO(protocol.NewIO(fd))`).

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
- `TestReadRawSplitsMessage`: the package-level `ReadRaw` over a
  `bytes.Reader` holding two instructions returns them one by one, then
  `io.EOF`.
- `FuzzReadRaw`: never panics; whatever it accepts is a prefix of the
  input and parses with `ParseInstruction`; and the canonical `String`
  form reads back unchanged. Raw input itself need not round-trip,
  because length prefixes may carry leading zeros.
- `TestParseInstructionReplacementCharacter`: U+FFFD parses, and `0xff`
  is still rejected.

Existing tests and `benchmark_test.go` keep passing; report the benchmark
delta of `ReadRaw` in the commit message.

## Boundaries

- No guacd code, no session or routing changes.
- Do not merge `Parser` and `ParseInstruction`, and do not change the
  instruction model.
- `internal/lib` is untouched.

## Outcome

**Summary.** Implemented directly in `8160431` (`protocol: frame
instructions by length and read from any stream`), plus `e593bf4`, which
records a pre-existing defect found while verifying it. Drift: Minimal.
12 of 14 items were satisfied as written. The 2 divergences are below,
and the body above now describes what was built.

**What shipped.**

- `internal/protocol/instruction.go`:
  - `NewInstructionIO(io.ReadWriteCloser)`.
  - A package-level `ReadRaw(*bufio.Reader)` that frames by length
    prefix, with `(*InstructionIO).ReadRaw` delegating to it.
  - `ErrInstructionTooLong` and `ErrInstructionTooManyElements`.
  - The "without semi" typo fixed.
- `server/session.go`: wraps the socketpair fd with `protocol.NewIO`.
- Tests in `internal/protocol`:
  - `TestInstructionIOOverPipe` (including a `;` inside a value),
    `TestReadRawSemicolonInValue`, `TestReadRawMultibyte`,
    `TestReadRawPartialReads`, `TestReadRawSplitsMessage`.
  - `TestReadRawLimits`: 13 cases, covering the exact limits, each error,
    and EOF handling.
  - `TestParseInstructionReplacementCharacter` and `FuzzReadRaw`.
  - `BenchmarkReadRaw`.
- Verified:
  - `go test` on macOS;
  - `go vet ./...` and `go test ./internal/... ./server/...` in the
    libguac build stage;
  - end-to-end VNC and SSH sessions through a built image;
  - 60 s of fuzzing (30M executions, no failures).

**Design evolution.**

- *Length limit per element, not per instruction.* The spec capped the
  whole instruction at 8192 code points. libguac's parser applies
  `GUAC_INSTRUCTION_MAX_LENGTH` to each element's declared length. A
  whole-instruction cap would be stricter than guacd itself and could
  reject an instruction that guacd accepts. The worst case now is 128
  elements of 8192 code points, about 4 MiB.
- *U+FFFD accepted by `ParseInstruction` and `Parser`.* The spec said to
  leave them alone. `FuzzReadRaw`'s parse property found that both
  rejected the valid character U+FFFD, because they treated any decoded
  `utf8.RuneError` as invalid. The fix checks the decoded size.
  `TestParseInstructionReplacementCharacter` fails without it.

**Not implemented.** None.

**Unspecified work.**

- `BenchmarkReadRaw` (scaffolding), added to report the delta that the
  spec asked for.
- The buffer is presized to 64 bytes and grown once per element
  (improvement). A short instruction went from 3 allocations to 1.

**Decisions, surprises, follow-ups.**

- `ReadRaw` returns `io.EOF` only when the stream ends between
  instructions, and `io.ErrUnexpectedEOF` inside one, so callers can
  tell a clean close from a truncated stream.
- Element bytes are read in bulk. Each code point takes at least one
  byte, so reading as many bytes as code points remain never passes the
  element. A code point cut at the end is then completed byte by byte,
  and the chunk is checked with `utf8.Valid`.
- Cost: an 8 KiB `blob` takes 2.8 µs against 0.95 µs for the old
  `ReadBytes(';')`, still about 2.9 GB/s. A `sync` takes 50 ns.
  Validation dominates. A single pass that validates and counts at once
  would close part of the gap if it ever matters.
- Surprise, pre-existing and recorded as
  [003](../003-session-lifecycle-hardening.md) defect 8: a failed connect
  leaves `serveIO`'s desktop reader blocked in `syscall.Read` on a closed
  fd. `Join` never returns, and those claims stay bound to a dead session
  until restart. A baseline image built from the previous commit behaves
  identically. [guacd-sessions](guacd-sessions.md) removes the cause.
- Follow-up: none for this task.
  [guacd-handshake-client](guacd-handshake-client.md) is now unblocked.

