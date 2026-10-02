# Web 查看器

查看器采用本地 Go 后端加 Vue、TypeScript、Vue Router 和 Tailwind 前端。浏览器只请求当前目录、分页文件和搜索结果，不会加载完整目录树或读取 `snapshot.gti`。

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
POST /api/v1/snapshots/active/open
```

目录和文件接口支持 `limit`、`offset` 以及 `sort=name|mtime|size`。服务端从 GTI 固定记录和 child range 中读取页面。

## 搜索

搜索保留任意位置子串语义。默认按文件记录分块扫描，不生成 FTS5、trigram 或 SQLite sidecar。搜索可能比目录切换慢，但不会拖延服务启动，也不会让快照体积膨胀。

## 路径与打开

每条结果返回绝对路径、逻辑大小、实际占用和系统可用的时间字段。复制路径只读取 API 返回值。

打开按钮由 Go 后端执行本机文件管理器命令。接口只接受回环浏览器请求，浏览器不申请每个文件夹的权限。快照在另一台电脑上浏览时，如果原始路径不存在，打开按钮会返回路径不可用，但不会影响离线分析。

## 视觉状态

当 `allocated_bytes_known=false` 时，前端以逻辑大小作为默认空间排序，并明确显示实际占用不可用。扫描错误通过错误分页接口显示，错误不会阻断其余目录浏览。
