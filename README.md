# golangtools

`golangtools` 是一个模块化的跨平台系统工具集。文件系统模块使用原生目录读取、单文件 GTI 快照和本地只读 Web 服务，可以处理百万级目录树，而不生成巨型 CSV 或单文件 HTML。

## 直接构建

需要 Go 1.24+、pnpm 12+ 和 Node.js 20+。macOS/Linux 使用 `build.sh`；Windows 使用 `build.ps1` 或双击 `build.cmd`：

```bash
./build.sh
```

```powershell
.\build.ps1
```

构建脚本会依次执行：

1. `pnpm install --frozen-lockfile`
2. Vue 类型检查和生产构建
3. `go test ./...` 和 `go vet ./...`
4. macOS、Linux、Windows 的 amd64/arm64 交叉编译
5. 生成 `dist/SHA256SUMS`

可执行文件位于 `dist/`：

```text
golangtools-darwin-amd64
golangtools-darwin-arm64
golangtools-linux-amd64
golangtools-linux-arm64
golangtools-windows-amd64.exe
golangtools-windows-arm64.exe
```

## 文件系统扫描

先探测目标卷：

```bash
./dist/golangtools-darwin-arm64 filesystem probe \
  -path /System/Volumes/Data
```

扫描并生成可立即浏览的快速树索引：

```bash
./dist/golangtools-darwin-arm64 filesystem scan \
  -root /System/Volumes/Data \
  -output /tmp/filesystem-snapshot \
  -metadata basic
```

`basic` 会读取文件大小、分配块、mtime、ctime、birthtime、inode 和 mode，用于空间分析。只需要文件名和目录结构时可使用 `-metadata tree`，它会跳过文件元数据读取。页面默认展示实际分配空间，同时保留逻辑大小；稀疏文件、APFS clone、硬链接会让逻辑大小重复计数，这是正常现象。

Windows 默认使用 `-backend auto`：本地 NTFS 优先走 MFT 顺序读取，权限或卷类型不满足时回退到 Win32 原生枚举。可用 `-backend windows-mft` 强制 MFT，或用 `-backend windows-native` 做回退路径测试。原生回退没有逐文件 NTFS 分配块信息，会在 manifest 中标记 `allocated_bytes_known=false`，查看器会使用逻辑大小，不会把 0 显示成真实占用。

默认 worker 数经过平台限制：macOS 最多 64，其他平台最多 32。机械盘随机寻道成本较高，可以显式测试 `-workers 2`、`4`、`8`；SSD/APFS 通常适合更高并发。

扫描内部使用顺序临时记录，完成后发布一个 `snapshot.gti`。segment、`tree.*` 和 SQLite 都不会作为输出保留。终端会持续打印阶段、实时吞吐、条目数量、错误数量和耗时；默认每 2 秒刷新，可用 `-progress-interval 5s` 调整。搜索在服务端按记录分块扫描，不再生成巨大的搜索副本。

启动图形化查看器：

```bash
./dist/golangtools-darwin-arm64 filesystem serve \
  -snapshot /tmp/filesystem-snapshot/snapshot.gti \
  -listen 127.0.0.1:8080
```

打开 `http://127.0.0.1:8080/`。浏览器只分页读取当前目录和搜索结果，不加载完整目录树。

快照是可复制的离线结果。迁移时只需要复制 `snapshot.gti`，再使用目标电脑对应的可执行文件运行：

```bash
./golangtools-linux-amd64 filesystem serve \
  -snapshot /path/to/snapshot.gti \
  -path-root /new/location/of/source \
  -listen 127.0.0.1:8080
```

展示、空间统计、目录分页和路径文本不需要原始磁盘在线；如果原始目录在目标机被复制到了不同位置，使用 `-path-root` 做根路径映射即可让“打开本机文件管理器”使用新路径。快照不会把文件内容复制过去，也不会改变另一台电脑的文件权限。

扫描会生成两类日志：工具级日志在 `~/.golangtools/logs/`（可用 `GOLANGTOOLS_LOG_DIR` 覆盖），输出目录内还有 `run.log`。快照页面显示绝对路径、实际占用、逻辑大小、修改/创建/元数据变更时间，并提供复制路径和在本机文件管理器中打开的按钮。通过 `127.0.0.1` 使用时由 Go 后端调用系统文件管理器，不需要浏览器逐个目录授权。

局域网监听会自动生成访问 token，并输出可直接打开的 URL：

```bash
./dist/golangtools-darwin-arm64 filesystem serve \
  -snapshot /tmp/filesystem-snapshot/snapshot.gti \
  -listen 0.0.0.0:8080
```

## 实机结果

参考一次 Mac APFS 数据卷 `/System/Volumes/Data` 扫描（worker 64）：

| 指标 | 结果 |
|---|---:|
| 文件 | 6,638,590 |
| 目录 | 1,113,755 |
| worker | 64 |
| 扫描耗时 | 63.5 秒 |
| 吞吐 | 约 122,000 条目/秒 |
| 逻辑大小 | 约 9.7 TiB |
| 实际分配 | 约 712 GiB |
| 受保护目录错误 | 222 |

macOS TCC 拒绝访问的目录会写入 `errors.ndjson`，不会中断其余扫描。

## 其它模块

```bash
# Base64 文件或目录归档
golangtools archive encode -path ./data -output ./data.b64.txt
golangtools archive decode -input ./data.b64.txt -output ./restored

# Word 文本读取和进程文档提取
golangtools document read -path ./demo.docx
golangtools document extract -type all -mode copy -output ./documents

# 进程监控
golangtools process usage -target nginx -output ./metrics.csv
golangtools process guard -name worker -cmd './worker --serve'

# 网络扫描
golangtools network ping -range '192.168.1.[1-254]'

# 磁盘清理分析和执行
golangtools cleanup scan -format json
golangtools cleanup run -target go-build-cache,pip-cache

# MySQL Mock 数据
golangtools mockdata generate -dsn 'user:pass@tcp(host:3306)/db' -tables users -rows 100

# 健康检查服务
golangtools health serve -listen 127.0.0.1:8080
```

完整参数见：

```bash
golangtools help
```

Ubuntu 快速使用：

```bash
BIN="$PWD/dist/golangtools-linux-amd64"
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"
"$BIN" filesystem scan -root / -output "$SNAPSHOT" -metadata basic -progress-interval 5s
"$BIN" filesystem serve -snapshot "$SNAPSHOT" -listen 127.0.0.1:8080
```

详细阶段、日志、并发起点和旧快照兼容说明见 [本地运行手册](docs/local-runbook.md)。

跨平台完整使用方式、Windows `scan/index/serve` 示例、快照路径区别和常见错误见 [文件系统扫描模块使用指南](docs/filesystem-user-guide.md)。

文件服务模块不再包含固定账号或服务器地址。使用前通过环境变量配置：

```text
DB_DSN
FILE_API_BASE_URL
FILE_USER_ID
FILE_USER_NAME
FILE_PROJECT_NAME
FILE_PROJECT_CODE
FILE_DEFAULT_PATH
SERVER_ROOT_DIR
SERVER_FALLBACK_DIR
SERVER_MIRROR_DIR
SERVER_RECORD_ID
```

## 前端开发

```bash
cd web
pnpm install --frozen-lockfile
pnpm run dev
pnpm run typecheck
pnpm run build
```

前端使用 Vue 3、TypeScript、Vue Router、Tailwind CSS、Vite 和 Lucide。生产构建输出到 `internal/filesystem/web/`，通过 Go `embed` 编译进可执行文件。

## 文档

- [架构](docs/architecture.md)
- [命令](docs/commands.md)
- [文件系统扫描模块使用指南](docs/filesystem-user-guide.md)
- [存储格式](docs/storage-format.md)
- [Web 查看器](docs/web-viewer.md)
- [清理模块](docs/cleanup.md)
- [文档模块](docs/document.md)
