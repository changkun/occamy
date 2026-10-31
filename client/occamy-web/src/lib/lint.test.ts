// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The lint rule that keeps the shell to the client core's public API
// (eslint.config.js), run on imports in place of a shell file's.

import { fileURLToPath } from 'node:url'

import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

// As in src/protocol/lint.test.ts: the test lints texts, not files, so
// it turns typescript-eslint's single-pass inference off.
const eslint = new ESLint({
  cwd: fileURLToPath(new URL('../..', import.meta.url)),
  overrideConfig: { files: ['**/*.{ts,tsx}'], languageOptions: { parserOptions: { disallowAutomaticSingleRunInference: true } } },
})

async function restricted(filePath: string, source: string): Promise<boolean> {
  const [result] = await eslint.lintText(`import * as m from '${source}'\nexport const used = m\n`, { filePath })
  return result.messages.some((m) => m.ruleId === 'no-restricted-imports')
}

describe('the shell’s imports of the client core', () => {
  it.each([
    ['src/lib/controller.ts', '../protocol/session'],
    ['src/lib/controller.ts', '../protocol/index'],
    ['src/lib/controller.ts', '../protocol/demo/demo'],
    ['src/views/App.tsx', '../protocol/dom'],
    ['src/main.tsx', './protocol/connect'],
  ])('refuse a module behind the index: %s importing %s', async (file, source) => {
    expect(await restricted(file, source)).toBe(true)
  })

  it.each([
    ['src/lib/controller.ts', '../protocol'],
    ['src/lib/controller.ts', './ending'],
    ['src/views/App.tsx', '../protocol'],
    ['src/views/App.tsx', 'react'],
    ['src/main.tsx', './lib/api'],
  ])('take the index and the shell’s own modules: %s importing %s', async (file, source) => {
    expect(await restricted(file, source)).toBe(false)
  })
}, 60_000)
