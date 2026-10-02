# 文件系统扫描模块使用指南

这份文档是 `golangtools filesystem` 的完整操作手册，适用于 Windows、macOS 和 Linux。发行包已经把 Vue 查看器嵌入 Go 二进制，不需要在目标机器安装 Node.js 或 pnpm。

## 1. 先理解三个路径

文件系统模块会使用三类不同的路径：

| 名称 | 含义 | 示例 |
| --- | --- | --- |
| 原始目录 | 需要被读取的磁盘目录或挂载点 | `D:\3_Dowload`、`/mnt/data`、`/System/Volumes/Data` |
| 快照目录 | `filesystem scan` 生成的结果目录，保存扫描记录 | `D:\golangtools-snapshots\download-20261002-090000` |
| `query.db` | 快照内的 SQLite 查询数据库，由索引阶段生成 | `<快照目录>\query.db` |

`scan` 读取原始目录并生成快照；`index` 只读取已经生成的快照并建立 `query.db`；`serve` 读取快照和 `query.db`，启动网页查看器。`index` 和 `serve` 的 `-snapshot` 参数必须指向快照目录，不能指向普通原始目录。

## 2. 你遇到的 Windows 错误

你执行的是：

```powershell
.\golangtools-windows-amd64.exe filesystem index `
  -snapshot "D:/3_Dowload" `
  -progress-interval 5s
```

`D:/3_Dowload` 是原始下载目录，不是工具生成的快照，所以里面没有 `manifest.json`。`index` 因此报：

```text
open D:\3_Dowload\manifest.json: The system cannot find the file specified.
```

正确做法是先扫描，再索引或直接查看：

```powershell
$BIN = ".\golangtools-windows-amd64.exe"
$STAMP = Get-Date -Format "yyyyMMdd-HHmmss"
$SNAPSHOT = "D:\golangtools-snapshots\download-$STAMP"

& $BIN filesystem scan `
  -root "D:\3_Dowload" `
  -output $SNAPSHOT `
  -metadata basic `
  -progress-interval 5s
```

扫描默认会自动构建 `query.db`。扫描完成后直接启动查看器：

```powershell
& $BIN filesystem serve `
  -snapshot $SNAPSHOT `
  -listen 127.0.0.1:8080
```

浏览器打开 `http://127.0.0.1:8080/`。

如果只想先生成快照、稍后再索引：

```powershell
& $BIN filesystem scan `
  -root "D:\3_Dowload" `
  -output $SNAPSHOT `
  -metadata basic `
  -build-index false `
  -progress-interval 5s

& $BIN filesystem index `
  -snapshot $SNAPSHOT `
  -progress-interval 5s
```

Windows 路径可以写成 `D:\data` 或 `D:/data`。含空格的路径必须加引号，例如 `-root "D:\My Files"`。PowerShell 的续行符是反引号 `` ` ``，不要把它漏掉；也可以把命令写成一行。

建议把 `-output` 放在 `-root` 之外，例如扫描 `D:\3_Dowload` 时使用 `D:\golangtools-snapshots\...`。不要把快照目录放进正在扫描的目录树中，避免生成中的 `manifest.json`、segment 和日志被再次枚举。

## 3. macOS 和 Linux 示例

```bash
BIN="./dist/golangtools-darwin-arm64"  # Linux 使用 golangtools-linux-amd64 或 linux-arm64
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"

"$BIN" filesystem scan \
  -root /System/Volumes/Data \
  -output "$SNAPSHOT" \
  -metadata basic \
  -progress-interval 5s

"$BIN" filesystem serve \
  -snapshot "$SNAPSHOT" \
  -listen 127.0.0.1:8080
```

Ubuntu 示例：

```bash
BIN="./golangtools-linux-amd64"
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"
"$BIN" filesystem scan -root / -output "$SNAPSHOT" -metadata basic -progress-interval 5s
"$BIN" filesystem serve -snapshot "$SNAPSHOT" -listen 127.0.0.1:8080
```

## 4. 扫描阶段和索引阶段

一次完整的 `filesystem scan` 包含以下流程：

1. `startup`：创建本次运行 ID 和工具级 JSONL 日志。
2. `config`：确定根目录、输出目录、worker、元数据模式和进度间隔。
3. `scan`：枚举目录、读取文件元数据、写入 segment 文件。
4. `directories`：批量导入目录记录到 SQLite。
5. `files`：批量导入文件记录并统计扩展名。
6. `extensions`：完成扩展名空间统计。
7. `search`：建立 SQLite FTS5 trigram 文件名搜索索引。
8. `sqlite`：建立目录、文件和排序所需的 B-tree 索引。
9. `metadata`：写入快照摘要和 schema 元数据。
10. `finalize`：刷新并关闭临时数据库，原子发布 `query.db`。
11. `complete`：输出命令成功或失败。

扫描阶段事先不知道总目录数，因此不能显示可靠的总百分比；它会显示文件数、目录数、待处理目录数、错误数、条目吞吐和 elapsed。索引阶段已经知道 manifest 中的文件和目录总数，会显示 `current/total` 和百分比。FTS5 重建和单个 SQLite 索引创建的内部工作量不适合伪造百分比，因此使用定时心跳，表示进程仍在工作。

`-progress-interval` 控制终端日志和 JSONL 进度事件的最小刷新间隔，默认 `2s`。大盘建议 `5s` 或 `10s`；观察小目录可以用 `200ms`，间隔过短会增加终端和日志开销。

命令结束 JSON 中的 `duration_ns` 是 Go 的纳秒整数，适合程序计算；`duration` 是面向人的四舍五入时间，例如 `1m3s`。两者表示同一段索引耗时。

## 5. 日志在哪里

每次命令都会写工具级日志：

```text
Windows: %USERPROFILE%\.golangtools\logs\
macOS/Linux: ~/.golangtools/logs/
```

可以用 `GOLANGTOOLS_LOG_DIR` 改目录：

```powershell
$env:GOLANGTOOLS_LOG_DIR = "D:\golangtools-logs"
```

```bash
export GOLANGTOOLS_LOG_DIR="$HOME/.local/state/golangtools/logs"
```

文件系统命令还会把事件写入 `<快照目录>/run.log`。这是 JSONL 文件，每行一个事件，包含时间、run ID、模块、命令、级别、阶段、事件名、消息和结构化字段。扫描无法读取的路径单独写在 `errors.ndjson`，单个权限错误不会中断整个扫描。

Linux/macOS 可以另开终端观察：

```bash
tail -f "$SNAPSHOT/run.log"
```

Windows 可以使用 PowerShell：

```powershell
Get-Content "$SNAPSHOT\run.log" -Wait
```

## 6. 并发和速度调优

Windows 有两条扫描路径。`-backend auto`（默认）先尝试本地 NTFS 的 MFT 顺序读取；它一次读取 MFT 大块数据并从记录中恢复目录、文件名、大小和时间，成功时不需要对每个文件调用 `CreateFile`。这条路径需要本地 NTFS 卷和读取卷设备的权限，通常需要以管理员身份运行。不能直接读卷、目标是 ReFS/FAT/网络映射盘或权限不足时，`auto` 会自动回退到 `windows-native`，使用 `FindFirstFileW/FindNextFileW` 的原生目录枚举。

可以显式选择路径：

```powershell
# 自动选择：优先 MFT，失败后回退
& $BIN filesystem scan -root "D:\3_Dowload" -output $SNAPSHOT -backend auto

# 强制 NTFS MFT；不可用时直接报出权限/卷类型错误
& $BIN filesystem scan -root "D:\3_Dowload" -output $SNAPSHOT -backend windows-mft

# 强制普通 Win32 枚举，适合验证回退路径
& $BIN filesystem scan -root "D:\3_Dowload" -output $SNAPSHOT -backend windows-native
```

MFT 路径是顺序读取和解析，`-workers` 对它不会产生同样的并发效果；原生路径的 worker 主要影响目录枚举。先用默认值跑一次基准，再只调整 `-workers` 比较同一个卷的耗时：

| 存储 | 建议起点 | 说明 |
| --- | ---: | --- |
| 机械硬盘 | 2、4、8 | 寻道是瓶颈，并发过高可能变慢 |
| SATA SSD | 8、16 | 逐步增加并发并观察吞吐 |
| NVMe SSD | 16、32 | 可以测试到平台默认上限 |
| macOS APFS | 默认值 | 当前平台上限为 64 |

示例：

```powershell
& $BIN filesystem scan -root "D:\3_Dowload" -output $SNAPSHOT -workers 8 -metadata basic -progress-interval 5s
```

`-metadata tree` 会跳过文件 `stat`，适合只需要目录、文件名和层级的快速目录树；空间大小、分配块、权限和时间字段需要 `-metadata basic`。改变 worker 不会改变快照格式，可以用不同快照目录做对比。

Windows 原生回退能从 Win32 查找结果直接得到逻辑大小和时间，但 `FindFirstFileW` 不提供每个文件的 NTFS 分配块。为了避免一千万次句柄调用拖慢扫描，回退快照会把实际占用标记为未知，记录中的 `allocated_bytes` 为 0 只是占位，不是文件真的占用 0 字节；网页会自动使用逻辑大小并显示“实际占用不可用”。只有 `scanner_backend=windows-mft` 且 `allocated_bytes_known=true` 时，0 才代表 MFT 解析得到的真实分配结果。

## 7. 快照目录结构

```text
manifest.json       扫描摘要、计数、字节数、时间、吞吐和 complete 标记
files.seg           文件记录的追加式二进制 segment
directories.seg     目录聚合记录的追加式二进制 segment
errors.ndjson       无法读取的路径及错误信息
query.db            SQLite 表、FTS5 搜索索引和排序索引
run.log             本次文件系统命令的 JSONL 运行日志
```

不要手工编辑 `manifest.json`、segment 或 `query.db`。如果数据库损坏或想重新建立索引，运行：

```powershell
& $BIN filesystem index -snapshot $SNAPSHOT -progress-interval 5s
```

它只读取快照，不重新访问原始磁盘。只有重新运行 `scan` 才会获取磁盘当前状态。

## 8. 网页查看器

查看器是 Go 只读后端加嵌入式 Vue、TypeScript、Vue Router 和 Tailwind 前端。浏览器只请求当前目录页或当前搜索页，不会下载百万级完整目录树，也不会因为目录切换把页面滚动位置跳回顶部。

页面提供：

- 目录空间概览、面包屑、分页和扩展名统计。
- 实际分配空间与逻辑大小切换。
- 文件和目录的绝对路径、修改时间、创建时间、元数据变更时间（系统提供时）。
- 复制绝对路径。
- 搜索文件名并分页查看结果。
- 在本机文件管理器中打开记录。

页面读取 `manifest.allocated_bytes_known`：Windows 原生回退或旧快照没有实际块信息时，默认展示逻辑大小并禁用“实际占用”切换，避免把 0 误认为真实占用。NTFS MFT 快照会显示并默认使用实际分配空间。

“打开”按钮由本机 Go 进程调用 Finder、Windows Explorer 或 Linux `xdg-open`。浏览器不会申请文件系统权限，也不会让你为每一个目录点击授权。接口只接受回环请求，并且路径来自只读索引；远程设备即使能浏览页面，也不能让它控制主机文件管理器。

默认监听 `127.0.0.1`。如确实需要局域网访问：

```bash
"$BIN" filesystem serve -snapshot "$SNAPSHOT" -listen 0.0.0.0:8080
```

非回环监听会自动生成访问 token 并打印带 token 的 URL。不要把这个 URL 发到不受信任的网络。macOS 的 Finder、Windows 的 Explorer 和 Linux 的 `xdg-open` 仍按当前登录用户权限工作；系统权限不足时只能由操作系统决定是否打开。

## 9. 大小和时间字段

逻辑大小是文件 `st_size` 的总和；实际分配空间是文件占用的磁盘块总和，网页默认使用实际分配空间。稀疏文件可以有很大的逻辑长度但只占少量块；APFS clone 可能共享物理块；硬链接可能在多个目录项中出现。因此逻辑大小超过卷容量并不表示扫描重复读取了文件，也不代表磁盘真的有那么多可用空间。

时间字段按系统能力写入纳秒时间戳：

- `mtime`：内容最后修改时间，所有平台通常都有。
- `ctime`：Unix 上通常是 inode/元数据变更时间，不是创建时间；Windows 的语义由平台兼容层提供。
- `birthtime`：创建时间或文件出生时间，文件系统不支持时为空或显示不可用。

不同文件系统和挂载方式提供的时间字段不同，页面不会把缺失字段伪造成有效时间。

## 10. 状态检查和常见问题

查看快照是否完成：

```powershell
& $BIN filesystem status -snapshot $SNAPSHOT
```

常见错误处理：

| 错误 | 原因和处理 |
| --- | --- |
| `missing manifest.json` | 把原始目录传给了 `index`；先用 `scan -root ... -output ...` 生成快照 |
| `missing files.seg` 或 `directories.seg` | 快照不完整、被移动时漏文件或手工删除；重新扫描 |
| `snapshot scan is not complete` | 扫描被中断；使用新的输出目录重新扫描 |
| `query.db ... run filesystem index first` | 快照只有 segment；执行 `filesystem index` |
| `access denied` / 权限错误 | 用有权限的用户运行；受保护路径会进入 `errors.ndjson` |
| 端口已占用 | 改用 `-listen 127.0.0.1:18080` 并访问对应端口 |
| Linux 没有 `xdg-open` | 安装桌面环境的 `xdg-utils`，或继续使用复制路径 |
| macOS 无法读取受保护目录 | 在系统隐私与安全设置中授予终端或二进制所需权限 |

如果索引看起来在 `search` 或 `sqlite` 阶段停留，先观察 `run.log`：这些阶段会按 `-progress-interval` 输出心跳；磁盘繁忙时可能没有线性百分比，但结束后会打印 `query index` 和 `complete`。

## 11. 从源码构建

开发机需要 Go 1.24+、Node.js 20+ 和 pnpm 12+：

```bash
./build.sh
```

Windows：

```powershell
.\build.ps1
```

构建会执行前端类型检查、Vue 生产构建、Go 测试和 vet，并生成六个目标：macOS/Linux/Windows 的 amd64 和 arm64。发行包中的 `docs/` 目录会同时包含本指南，便于离线查阅。
