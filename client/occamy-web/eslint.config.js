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
    // no Node module, no UI framework.
    files: ['src/protocol/**/*.ts'],
    ignores: ['src/protocol/**/*.test.ts', 'src/protocol/testing/**', 'src/protocol/demo/vite.config.ts'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [
          { group: ['node:*'], message: 'The client core runs in browsers.' },
          { group: ['vue', 'vue-*', 'react', 'react-*', 'element-plus', 'axios'], message: 'The client core depends on no framework.' },
        ],
      }],
    },
  },
]
