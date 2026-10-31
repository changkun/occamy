import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'
import tseslint from 'typescript-eslint'

// The protocol v1 client core (src/protocol) is TypeScript, linted with
// type information from tsconfig.test.json, which covers the library and
// its tests.
const typescript = tseslint.configs.recommendedTypeChecked.map((c) => ({
  ...c,
  files: ['**/*.ts'],
}))

export default [
  { ignores: ['dist/'] },
  js.configs.recommended,
  ...pluginVue.configs['flat/essential'],
  {
    languageOptions: {
      globals: { ...globals.browser },
    },
    rules: {
      // Login and Desktop are route views, not reusable components.
      'vue/multi-word-component-names': 'off',
    },
  },
  ...typescript,
  {
    files: ['**/*.ts'],
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.test.json'],
        tsconfigRootDir: import.meta.dirname,
      },
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
