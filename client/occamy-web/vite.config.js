import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  // occamyd serves the built client under /static
  base: './',
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:5636', ws: true },
    },
  },
})
