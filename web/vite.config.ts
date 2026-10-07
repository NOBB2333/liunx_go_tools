import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileViewerRenderers } from '@file-viewer/vite-plugin'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [
    vue(),
    tailwindcss(),
    // 自动装配已安装的 @file-viewer/preset-*，并把 Worker / WASM / 字体等运行资源
    // 发布到 /file-viewer/ 下。只有装上这个插件，组件才能拿到已安装的 renderer。
    fileViewerRenderers({ copyAssets: true, chunkStrategy: 'none' }),
  ],
  base: '/',
  server: {
    // 让 pnpm dev 直接打到本地 serve 的 API，省掉「改一次前端就重新构建 + 重跑二进制」
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
  build: {
    outDir: fileURLToPath(new URL('../internal/filesystem/web', import.meta.url)),
    // 清目录由 pnpm 的 prebuild 负责：它会保住 keep.txt 占位文件。
    // Vite 自带的 emptyOutDir 会把占位文件一起删掉，所以这里关掉。
    emptyOutDir: false,
    sourcemap: false,
  },
})
