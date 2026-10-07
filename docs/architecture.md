# 架构说明

## 文件系统模块

```mermaid
flowchart LR
  A[原始目录] --> B[平台扫描器]
  B --> C[临时顺序记录]
  C --> D[固定宽度树构建]
  D --> E[单文件 snapshot.gti]
  E --> F[Go 只读 HTTP 服务]
  F --> G[Vue TypeScript 查看器]
```

扫描和展示是两个独立生命周期。扫描过程结束后只发布 `snapshot.gti`；服务端只读该文件，扫描进程退出也不会影响查看。

## 扫描后端

- macOS 使用有界并发 `readdir` 和平台元数据读取。
- Linux 使用相对目录枚举和 `statx` 能力。
- Windows NTFS `auto` 优先使用 `$MFT` 顺序读取；没有卷读取权限或不是 NTFS 时回退到 Win32 枚举。
- 后端输出统一进入内部记录管道，最终格式与平台无关。

内部 segment 只用于扫描期间的顺序写入和最终归并，发布 GTI 后立即删除。它们不是公共 API，也不是迁移格式。

## GTI 读取路径

GTI 使用段表定位固定记录。目录分页流程是：

1. 按外部目录 ID 查询内部目录索引。
2. 读取目录的 child range。
3. 批量 `ReadAt` 子节点 ID。
4. 读取对应固定记录和名称池。
5. 在内存中排序并分页。

该路径不执行 SQL，不构建 B-tree，不加载整个目录树，也不生成完整路径副本。文件名搜索只扫描文件记录并响应取消信号。

## Web 服务

Go 提供只读分页 API，并嵌入 Vue、TypeScript、Vue Router 和 Tailwind 生产资源。浏览器不会直接读取 GTI，也不会获得完整快照文件。

嵌入的不只是前端代码：预览器需要的 Worker、WASM 和字体由 Vite 插件在构建时复制到产物根目录的 `vendor/` 下，随 `go:embed` 一起进二进制，运行时不依赖外部 CDN。这是发行包体积的主要来源（约 21MB）。

这些产物不随源码提交。`internal/filesystem/web/` 只提交一个占位文件让 `go:embed` 成立，其余由构建脚本生成；未构建前端时服务端返回一页提示，而不是 404。

`POST /api/v1/snapshots/active/open` 仅允许回环请求，由 Go 后端调用 Finder、Explorer 或 `xdg-open`。远程查看可以浏览快照，但不能控制主机文件管理器。

`GET /api/v1/snapshots/active/content/{id}/{name}` 是文件预览的字节来源：GTI 只存元数据，浏览器又读不了 `file://`，所以预览必须经由此接口现读磁盘。它使用 `http.ServeContent` 以支持 Range，并默认同样只接受回环请求，由 `-allow-remote-content` 显式放宽。地址末尾的 `name` 仅供浏览器和预览器识别格式，服务端不使用，路径一律由索引里的 `id` 解析。

## 数据边界

GTI 保存扫描时的元数据和路径组成信息，不保存文件内容。迁移 GTI 不会把原始文件复制到目标机器；`-path-root` 只改变查看和本机打开时的路径映射。

## 扩展规则

新的文件系统能力应放在 `internal/filesystem`，CLI 只负责参数解析和生命周期日志。新增字段必须更新 GTI 版本或明确向后兼容规则，不能通过再添加 SQLite 或散落 sidecar 文件解决。
