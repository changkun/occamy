import { defineConfig } from 'vitest/config'

// The tests of the shell's logic (src/lib), of its views' markup
// (src/views, rendered to a string without a DOM) and of the protocol v1
// client core (src/protocol) run in Node. The interop tests also need
// OCCAMY_INTEROP_SERVER, the path of a built client/internal/webinterop
// server (see src/protocol/interop.test.ts): without it, they are
// skipped, and fail in CI.
export default defineConfig({
  test: {
    include: ['src/**/*.test.{ts,tsx}'],
    environment: 'node',
  },
})
