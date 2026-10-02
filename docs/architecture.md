# 架构

项目采用“薄 CLI + 独立功能模块”结构。根 `main.go` 只启动 CLI，功能实现不互相引用 CLI。

```text
main.go
  -> internal/cli
       -> internal/archive
       -> internal/cleanup
       -> internal/document
       -> internal/filestore
       -> internal/filesystem
       -> internal/healthserver
       -> internal/mockdata
       -> internal/networkscan
       -> internal/process

web/
  -> Vite production build
  -> internal/filesystem/web
  -> Go embed
```

每个目录是一项功能模块，包含自己的实现和测试。Go 文件统一使用小写 ASCII 文件名；中文保留在界面、帮助和文档中。

## 文件系统数据流

```mermaid
flowchart LR
    A[目录树或 NTFS 卷] --> B[平台扫描后端]
    B --> C[版本化 segment]
    C --> D[批量索引构建器]
    D --> E[SQLite query.db]
    E --> F[只读 HTTP API]
    F --> G[Vue 按需分页]
```

扫描与查询分为两个阶段。扫描热路径不执行 SQL，也不拼接完整路径到每条记录；文件只保存父目录数字 ID 和名称。查询库可以随时从 segment 重建。

Linux 扫描器使用目录 fd、`getdents64` 和相对目录的 `statx`。macOS 使用有界并发 `readdir`；目录任务通过 dispatcher 排队，避免宽目录下 worker 相互阻塞。Windows 的 `auto` 后端优先读取本地 NTFS 卷的 `$MFT` 数据流，以大块顺序 I/O 解析 fixup、runlist、`$STANDARD_INFORMATION`、`$FILE_NAME` 和未命名 `$DATA`；无卷读取权限或不是 NTFS 时回退到 `FindFirstFileW/FindNextFileW` 原生枚举。两个 Windows 后端输出相同的 segment 合约。

HTTP 服务以只读模式打开 SQLite。默认只监听 `127.0.0.1`；非回环监听自动启用 token，首次 URL token 会交换为 HttpOnly Cookie。

所有 CLI 模块共用 `internal/runtime` 的结构化日志器。日志事件带有 `run_id`、模块、命令、阶段、事件、耗时和结构化字段；文件系统扫描通过附加日志把同一运行过程写入快照目录。新增模块只需要接入 CLI 路由即可获得统一日志格式。

## 扩展规则

新增功能时创建 `internal/<module>`，并只在 `internal/cli` 增加命令适配。模块不能依赖根 `main` 或前端源码。面向用户的结构化输出优先使用 JSON、NDJSON、SQLite 或带版本头的二进制格式。
