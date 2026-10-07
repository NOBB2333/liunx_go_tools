import { existsSync, readdirSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// 前端产物不入版本库，internal/filesystem/web/ 里只提交一个 keep.txt 占位文件，
// 让 go:embed 在没构建前端时也成立。Vite 的 emptyOutDir 会连占位文件一起删掉，
// 所以每次构建前由这里清目录，只保留占位文件，避免旧哈希分块越积越多。
const EMBED_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'filesystem', 'web')
const SENTINEL = 'keep.txt'

if (!existsSync(EMBED_DIR)) {
  console.error(`[clean-embed] 找不到 embed 目录：${EMBED_DIR}`)
  process.exit(1)
}

let removed = 0
for (const entry of readdirSync(EMBED_DIR)) {
  if (entry === SENTINEL) continue
  rmSync(join(EMBED_DIR, entry), { recursive: true, force: true })
  removed += 1
}
console.log(`[clean-embed] 已清理 ${removed} 项，保留 ${SENTINEL}`)
