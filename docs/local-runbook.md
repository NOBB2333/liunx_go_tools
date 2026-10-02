# 本地运行手册

这份手册用于在另一台机器（尤其是 Ubuntu）直接运行已经编译好的工具。工具由 Go 后端和嵌入式 Vue 查看器组成，不需要 Node.js 才能使用发行包。

完整的跨平台参数、Windows PowerShell 示例、快照目录结构和常见错误请参阅 [文件系统扫描模块使用指南](filesystem-user-guide.md)。最容易混淆的一点是：`filesystem scan -root` 的 `root` 是原始目录，`filesystem index -snapshot` 的 `snapshot` 必须是 scan 的输出目录。

## 1. 选择二进制

```bash
# Ubuntu Intel/AMD 64 位
BIN="$PWD/dist/golangtools-linux-amd64"

# Ubuntu ARM64
# BIN="$PWD/dist/golangtools-linux-arm64"

# 当前 Apple Silicon Mac
# BIN="$PWD/dist/golangtools-darwin-arm64"
```

如果从源码构建，需要 Go 1.24+、Node.js 20+ 和 pnpm 12+。macOS/Linux 在项目根目录执行 `./build.sh`；Windows 执行 `./build.ps1` 或 `build.cmd`。构建完成后会得到 macOS、Linux、Windows 的 amd64/arm64 六个文件和 `dist/SHA256SUMS`。

## 2. 扫描并观察过程

```bash
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"
"$BIN" filesystem scan \
  -root / \
  -output "$SNAPSHOT" \
  -metadata basic \
  -progress-interval 5s
```

终端输出分为这些阶段：

- `startup`：命令和日志文件已创建。
- `config`：根目录、worker、元数据模式和输出目录已确定。
- `scan`：目录枚举、文件数、目录数、待处理目录、错误数、实时条目/秒和耗时。
- `directories`、`files`：索引导入进度。
- `extensions`：扩展名统计。
- `search`：FTS5 trigram 文件名索引。该阶段总量不易预估，会以心跳表示仍在工作。
- `sqlite`：SQLite 查询索引和元数据完成。
- `complete`：命令成功或失败。

扫描阶段不能准确显示百分比，因为总目录数事先未知。索引阶段使用 manifest 中的记录总数显示百分比。扫描和索引都在同一次 `filesystem scan` 中完成；也可以用 `-build-index=false` 只生成 segment，稍后再执行 `filesystem index`。

查看实时日志时可以另开终端：

```bash
tail -f "$SNAPSHOT/run.log"
```

## 3. 日志位置

每个命令都会写工具级 JSONL 日志，默认目录为：

```text
~/.golangtools/logs/
```

可以在运行前改到指定位置：

```bash
export GOLANGTOOLS_LOG_DIR="$HOME/.local/state/golangtools/logs"
```

文件系统扫描另外写入：

```text
<snapshot>/run.log
```

JSONL 每行包含时间、run id、模块、命令、级别、阶段、事件、消息、耗时和结构化字段。`errors.ndjson` 只记录扫描时无法读取的路径，不会因为单个受保护目录中断全盘扫描。

## 4. worker 调优

先用默认值得到基准，再只改变 `-workers` 做对比。建议起点如下：

| 存储 | worker 起点 |
|---|---:|
| 机械硬盘 | 2、4、8 |
| SATA SSD | 8、16 |
| NVMe SSD | 16、32 |
| macOS APFS | 默认值，当前最多 64 |

机械盘的瓶颈通常是寻道，64 并发未必更快。`-metadata tree` 会跳过文件 stat，适合只想快速获得目录/文件名的场景；空间大小和时间字段需要 `-metadata basic`。

示例：

```bash
"$BIN" filesystem scan -root /mnt/data -output "$SNAPSHOT" -workers 8 -metadata basic -progress-interval 5s
```

## 5. 启动图形化查看器

扫描成功后：

```bash
"$BIN" filesystem serve -snapshot "$SNAPSHOT" -listen 127.0.0.1:8080
```

打开 `http://127.0.0.1:8080/`。页面默认使用实际分配空间，切换按钮可以查看逻辑大小。目录和搜索结果会显示绝对路径、修改时间，并可查看创建时间/元数据变更时间和复制路径。浏览器只加载当前页，不会把整个目录树放进前端内存。

每条记录旁的文件管理器按钮会让本机 Go 进程调用 Finder、Explorer 或 `xdg-open`。这是操作系统已有的用户权限，不是浏览器文件访问，因此不会为每个目录弹授权框。该按钮只对 `127.0.0.1` 回环连接开放；如果使用 `0.0.0.0` 给其他设备访问，远程页面仍然可以浏览和搜索，但不能让远程设备打开主机上的文件管理器。

让局域网其他设备访问时：

```bash
"$BIN" filesystem serve -snapshot "$SNAPSHOT" -listen 0.0.0.0:8080
```

命令会生成 token 并打印带 token 的 URL。不要把带 token 的 URL 发布到不受信任的网络。

## 6. 现有快照和状态检查

```bash
"$BIN" filesystem status -snapshot "$SNAPSHOT"
"$BIN" filesystem index -snapshot "$SNAPSHOT" -progress-interval 5s
```

`filesystem index` 会从 `files.seg` 和 `directories.seg` 重建 `query.db`，不会重新扫描磁盘。旧快照可以继续打开；旧数据库缺少 ctime/birthtime 时，页面会显示“不可用”。

## 7. 为什么逻辑大小可能超过磁盘容量

逻辑大小是每个文件的 `st_size` 总和；实际占用是分配块总和。稀疏文件有大逻辑长度但少量实际块，APFS clone 可能共享物理块，硬链接也可能被多个目录项看到。因此总逻辑大小大于卷容量并不表示扫描重复读取了文件。空间图和默认列表使用实际占用，排查这类差异时切换到逻辑大小即可。

## 8. 结果目录结构

```text
manifest.json       扫描摘要、计数、字节数、时间和吞吐
files.seg           文件记录（二进制、追加友好）
directories.seg     目录聚合记录（二进制）
errors.ndjson       无法访问的路径
query.db            SQLite 查询索引和 FTS5 搜索索引
run.log             本次文件系统命令的 JSONL 运行日志
```

不要手工编辑 segment 或 `query.db`。需要重新导入时运行 `filesystem index`；需要重新获取元数据时重新运行 `filesystem scan`。
