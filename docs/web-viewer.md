# Web 查看器

查看器采用本地 Go 后端加 Vue、TypeScript、Vue Router 和 Tailwind 前端。浏览器只请求当前目录、分页文件和搜索结果，不会加载完整目录树或读取 `snapshot.gti`。前端产物通过 Go `embed` 编译进可执行文件，运行时不依赖外部 CDN。

## API

```text
GET  /api/v1/health
GET  /api/v1/snapshots
GET  /api/v1/snapshots/active/summary
GET  /api/v1/snapshots/active/directories/{id}
GET  /api/v1/snapshots/active/directories/{id}/children
GET  /api/v1/snapshots/active/files
GET  /api/v1/snapshots/active/search?q=<term>
GET  /api/v1/snapshots/active/extensions
GET  /api/v1/snapshots/active/errors
GET  /api/v1/snapshots/active/content/{id}/{name}
POST /api/v1/snapshots/active/open
```

目录和文件接口支持 `limit`、`offset` 以及 `sort=name|mtime|size`。服务端从 GTI 固定记录和 child range 中读取页面。

## 页面布局

页面分成两块：左侧「目录浏览」卡片，右侧分析栏。

左侧卡片自上而下是标题与返回上级、面包屑（含复制路径、在本机打开、排序）、空间占用图、文件列表、分页。

- **空间占用图可以折叠**。点它的标题行即可收起，收起后原地保留一行摘要（`最大：a 1.1 GB · b 505 MB · c 185 MB`），点这一行重新展开。收起状态下文件列表会吃满整张卡片，同时页面头部和统计卡让位，整个页面不再滚动，只保留列表一条滚动条。
- 空间占用图只展示**当前一页** children 中的目录，默认最多 24 个方块，数量会写在标题行里（`空间占用图 · 9 / 13 个子目录`）。方块宽度按占用比例铺排，不是严格的树状图算法。
- 文件列表每行两行：第一行名称、容量和操作按钮，第二行路径和修改时间。容量只显示当前口径下的一个值，实际占用还是逻辑大小由上面的开关决定。修改时间精确到分钟，完整的修改、创建、元数据三个时间在鼠标悬停提示里。

右侧分析栏的每个面板都能单独折叠，折叠状态记在浏览器 `localStorage`：

- **当前目录最大文件**：当前目录的路径、递归大小、递归条目数、本层条目数，以及**直属**文件中占用最大的 8 个（带条形图）。注意这里取的是直属文件，不递归到子目录；要判断深层占用请进子目录或看空间占用图。
- **全盘类型占用**：整个快照的扩展名统计，与当前所在目录无关，面板上会写明这一点。支持按大小或按数量排序。

递归条目数来自索引里的目录记录。在这些字段被写入索引之前生成的旧快照不会显示数字，而是显示「需重新扫描」，重新执行一次 `filesystem scan` 即可。

## 文件预览

文件列表每行的眼睛图标会在右侧打开预览抽屉。抽屉左边缘可以拖动调整宽度（下限 380px，并且左侧至少保留 160px），宽度记在 `localStorage`。`Esc` 或点击遮罩关闭。

预览由浏览器内的 Worker 和 WASM 解析渲染，不需要转码服务，也不上传任何数据。覆盖 Office、PDF、OFD、压缩包、邮件、Markdown、代码文本、图片、音视频等常见格式；不支持的扩展名会明确提示，而不是静默失败。

按文件大小分四种情况处理：

| 情况 | 处理 |
| --- | --- |
| 可流式渲染的格式（mp4、webm、mkv、mov、avi、m4v、mp3、wav、flac、ogg、m4a、aac、pdf） | 不设上限，交给预览器按需拉流 |
| 其它格式，不超过 32MB | 由前端取回字节后交给预览器，响应只被当作数据解析 |
| 其它格式，32MB 到 1GB | 交给预览器自行读取 |
| 其它格式，超过 1GB | 提示改用「在本机打开」，这类格式需要整份读进内存解析 |

**预览读取的是磁盘上的当前文件，不是快照里的副本**。GTI 不保存文件内容，所以文件被修改后会看到新内容，被删除后会提示路径不存在。因此扫描和 serve 需要在同一台机器上，预览才可用。

## 文件内容接口

`GET /api/v1/snapshots/active/content/{id}/{name}` 返回索引指向的那个文件的原字节。这是预览的前置条件：浏览器无法自行读取 `file://` 路径。

`name` 只用于让浏览器和预览器识别格式，服务端完全不使用它，路径一律由索引里的 `id` 解析，因此不存在路径穿越。`/content/{id}` 这种不带文件名的形式也能用，但部分预览器无法从纯数字地址判断格式。

响应使用 `http.ServeContent`，因此支持 Range 请求，视频拖动进度条和 PDF 分片加载依赖这一点。同时返回：

```text
X-Content-Type-Options: nosniff
Content-Security-Policy: sandbox; default-src 'none'
```

这两个头用于兜底：`-token` 存在 HttpOnly cookie 里，如果磁盘上的 HTML 或 SVG 被直接当作页面打开，沙箱可以阻止它执行脚本并借 cookie 调用其它接口。

该接口**默认只允许回环浏览器请求**。需要在别的机器上查看内容时显式放开：

```bash
golangtools filesystem serve \
  -snapshot /path/to/snapshot.gti \
  -listen 0.0.0.0:8080 \
  -allow-remote-content
```

## 搜索

搜索保留任意位置子串语义。默认按文件记录分块扫描，不生成 FTS5、trigram 或 SQLite sidecar。搜索可能比目录切换慢，但不会拖延服务启动，也不会让快照体积膨胀。

## 路径与打开

每条结果返回绝对路径、逻辑大小、实际占用和系统可用的时间字段。复制路径只读取 API 返回值。

打开按钮由 Go 后端执行本机文件管理器命令。接口只接受回环浏览器请求，浏览器不申请每个文件夹的权限。快照在另一台电脑上浏览时，如果原始路径不存在，打开按钮会返回路径不可用，但不会影响离线分析。

## 视觉状态

当 `allocated_bytes_known=false` 时，前端以逻辑大小作为默认空间排序，并明确显示实际占用不可用。扫描错误通过错误分页接口显示，错误不会阻断其余目录浏览。

## 前端开发

```bash
cd web
pnpm install --frozen-lockfile
pnpm run dev
```

开发服务器默认监听 `5173`，`/api` 反向代理到 `127.0.0.1:8080`，所以调试时需要另开一个终端运行 `filesystem serve`。生产构建输出到 `internal/filesystem/web/`：

```bash
pnpm run typecheck
pnpm run build
```

前端产物**不随源码提交**。`internal/filesystem/web/` 只提交一个 `keep.txt` 占位文件，让 `go:embed` 在未构建前端时成立，其余内容全部由构建脚本生成。构建前会先跑 `prebuild` 清理目录（保留占位文件），因为 Vite 的 `emptyOutDir` 会把占位文件一起删掉。

这样一来：克隆后 `go build`、`go test` 都能过，但二进制里没有网页界面，访问会返回一页提示；要得到完整二进制，先跑 `./build.sh`、`.uild.ps1` 或 `make frontend`。

产物是编译期嵌入的，所以**改完前端必须重新编译 Go 二进制**，只跑 `pnpm run build` 不会更新正在运行的二进制。

预览器的 Worker、WASM 和字体由 `@file-viewer/vite-plugin` 在构建时复制到产物根目录的 `vendor/` 下，同样随 `go:embed` 进二进制。开发模式下它会额外生成一份到 `web/public/`，该目录也已被忽略。
