import { defineConfig } from 'vitest/config'

// The tests of the shell's logic (src/lib), of its views (src/views) and
// of the protocol v1 client core (src/protocol) run in Node. A view's
// markup is rendered to a string; a view whose effects are tested runs
// in happy-dom, which its test file names in a @vitest-environment
// comment, and under StrictMode, whose development effects run twice. The interop tests also need
// OCCAMY_INTEROP_SERVER, the path of a built client/internal/webinterop
// server (see src/protocol/interop.test.ts): without it, they are
// skipped, and fail in CI. A test that waits on a real timer polls with a
// deadline of 30 s, for a machine at a load of hundreds; its timeout stays
// above that deadline, so a failure reports what the test waited for.
export default defineConfig({
  test: {
    include: ['src/**/*.test.{ts,tsx}'],
    environment: 'node',
    testTimeout: 60_000,
  },
})
