// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The lint rule that keeps the client core to its own modules
// (eslint.config.js), run on imports in place of a library file's and
// the demo's.

import { fileURLToPath } from 'node:url'

import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

// With CI set, typescript-eslint infers a single pass over the files on
// disk, and the first text linted here came back without a message; the
// test lints texts, not files, so it turns that inference off.
const eslint = new ESLint({
  cwd: fileURLToPath(new URL('../..', import.meta.url)),
  overrideConfig: { files: ['**/*.ts'], languageOptions: { parserOptions: { disallowAutomaticSingleRunInference: true } } },
})

async function restricted(filePath: string, source: string): Promise<boolean> {
  const [result] = await eslint.lintText(`import * as m from '${source}'\nexport const used = m\n`, { filePath })
  return result.messages.some((m) => m.ruleId === 'no-restricted-imports')
}

describe('the client core’s imports', () => {
  it.each(['pako', 'fs', 'node:fs', 'vue', '@vueuse/core', '../main.js', '../api/client', './demo/../../main.js'])('refuse %s in a library file', async (source) => {
    expect(await restricted('src/protocol/session.ts', source)).toBe(true)
  })

  it.each(['./codecs', './decode', './demo/demo'])('take %s in a library file', async (source) => {
    expect(await restricted('src/protocol/session.ts', source)).toBe(false)
  })

  it.each(['pako', '../../main.js', '../testing/../../main.js'])('refuse %s in the demo', async (source) => {
    expect(await restricted('src/protocol/demo/demo.ts', source)).toBe(true)
  })

  it.each(['../index', '../testing/vectors', './other'])('take %s in the demo', async (source) => {
    expect(await restricted('src/protocol/demo/demo.ts', source)).toBe(false)
  })
}, 60_000)
