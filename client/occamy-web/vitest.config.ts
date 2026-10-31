import { defineConfig } from 'vitest/config'

// The tests of the protocol v1 client core run in Node. The interop
// test also needs OCCAMY_INTEROP_SERVER, the path of a built
// client/internal/webinterop server (see src/protocol/interop.test.ts):
// without it, the test is skipped, and fails in CI.
export default defineConfig({
  test: {
    include: ['src/protocol/**/*.test.ts'],
    environment: 'node',
  },
})
