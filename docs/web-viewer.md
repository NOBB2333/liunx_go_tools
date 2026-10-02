# Web 查看器

查看器是本地前后端架构：Go 提供只读分页 API，并嵌入 Vue 的生产资源。浏览器不会读取 segment 或 SQLite，也不会接收完整目录树。

## 页面

- `/`：扫描摘要、目录空间图、面包屑、上级导航、目录分页和扩展名统计
- `/search`：文件名搜索和分页

列表默认使用实际分配空间排序和绘图，同时保留逻辑大小。页面可以切换“实际占用/逻辑大小”。每条记录显示绝对路径、修改时间；悬停时间可查看创建时间和元数据变更时间。复制按钮只复制当前记录的绝对路径，不会把完整快照加载到浏览器。

目录切换、排序和分页会保留当前滚动位置。查看器是 Go 只读后端加 Vue 前端，浏览器只请求当前页，因此百万级目录树不会生成或下载巨型 HTML/JSON。

每条目录/文件记录还有一个文件管理器按钮。通过本机 `127.0.0.1` 查看器点击时，Go 后端使用当前用户权限调用 macOS Finder、Windows Explorer 或 Linux `xdg-open`；浏览器本身不申请目录读取权限，也不会要求你为每个文件夹逐个授权。文件按钮在 macOS/Windows 中定位文件，Linux 中打开文件所在目录。非回环连接不会开放这个动作。

## API

```text
GET /api/v1/health
GET /api/v1/snapshots
GET /api/v1/snapshots/active/summary
GET /api/v1/snapshots/active/directories/{id}
GET /api/v1/snapshots/active/directories/{id}/children
GET /api/v1/snapshots/active/files
GET /api/v1/snapshots/active/search
GET /api/v1/snapshots/active/extensions
GET /api/v1/snapshots/active/errors
POST /api/v1/snapshots/active/open
```

`POST /open` 的请求体为 `{"id": 123, "is_dir": true}`，只接受本机回环请求，路径由只读索引解析，不接受浏览器直接提交任意路径。

列表接口使用 `limit` 和 `offset`，`limit` 最大为 500。目录列表支持 `sort=size|name|mtime`。

新建快照的文件名搜索使用 SQLite FTS5 trigram 子串索引。版本化之前的 `query.db` 仍可打开，服务会自动回退到兼容查询。

## 访问控制

本机回环监听不要求 token。非回环监听必须认证；未提供 `-token` 时 CLI 自动生成随机 token，并输出带 `?token=` 的 URL。浏览器首次访问后使用 SameSite Strict、HttpOnly Cookie，API 客户端也可使用：

```text
Authorization: Bearer <token>
```

## 前端工程

```bash
cd web
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
```

技术栈为 pnpm、Vue 3、TypeScript、Vue Router、Tailwind CSS、Vite 和 Lucide。Vite 会清空并重建 `internal/filesystem/web/`。
