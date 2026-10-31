import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

// The shell (src/main.tsx, src/views, src/lib) and the protocol v1
// client core (src/protocol) are TypeScript, linted with type
// information from tsconfig.test.json, which covers both and their
// tests.
const typescript = tseslint.configs.recommendedTypeChecked.map((c) => ({
  ...c,
  files: ['**/*.{ts,tsx}'],
}))

export default [
  { ignores: ['dist/'] },
  js.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser },
    },
  },
  ...typescript,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.test.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    // The rules of hooks, with the React Compiler's checks of purity,
    // refs and state in effects.
    files: ['src/**/*.{ts,tsx}'],
    ...reactHooks.configs.flat.recommended,
  },
  {
    // The shell uses the client core through its public API, the index,
    // so that the core's modules may change behind it.
    // src/lib/lint.test.ts checks this rule.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/protocol/**'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          { regex: '(^|/)protocol/.', message: 'The shell imports the client core through its index alone: import from \'../protocol\'.' },
        ],
      }],
    },
  },
  {
    // The library runs in browsers and Workers and depends on nothing:
    // it imports its own modules in src/protocol alone, and uses only
    // what browsers have. src/protocol/lint.test.ts checks this rule.
    files: ['src/protocol/*.ts'],
    ignores: ['src/protocol/**/*.test.ts'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          { regex: '^[^.]', message: 'The client core imports no package and no Node module, only its own modules.' },
          { regex: '(^|/)\\.\\.(/|$)', message: 'The client core imports only its own modules, in src/protocol.' },
        ],
      }],
    },
  },
  {
    // The demo page is the library's, one directory down.
    files: ['src/protocol/demo/*.ts'],
    ignores: ['src/protocol/demo/vite.config.ts'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          { regex: '^[^.]', message: 'The demo imports no package and no Node module, only the client core.' },
          { regex: '(^|/)\\.\\.(/.*)?/\\.\\.(/|$)', message: 'The demo imports only the client core, in src/protocol.' },
        ],
      }],
    },
  },
]
