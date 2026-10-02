import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  base: '/',
  build: {
    outDir: fileURLToPath(new URL('../internal/filesystem/web', import.meta.url)),
    emptyOutDir: true,
    sourcemap: false,
  },
})
