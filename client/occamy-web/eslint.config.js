import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'

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
]
