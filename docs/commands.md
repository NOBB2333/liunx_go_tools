# 命令

统一形式：

```text
golangtools <module> <command> [options]
```

## filesystem

```bash
golangtools filesystem probe -path <path>
golangtools filesystem scan -root <source-directory> [-output snapshot] [-workers N] [-metadata basic|tree] [-backend auto|windows-mft|windows-native|portable] [-build-index=true] [-progress-interval 2s]
golangtools filesystem index -snapshot <scan-output-snapshot-dir> [-database query.db] [-batch-size 10000] [-progress-interval 2s]
golangtools filesystem status -snapshot <snapshot>
golangtools filesystem serve -snapshot <scan-output-snapshot-dir> [-listen 127.0.0.1:8080] [-token token]
```

参数中的路径有严格区别：`scan -root` 接受要读取的原始目录，`scan -output` 输出一个新的快照目录；`index -snapshot` 和 `serve -snapshot` 只能接受这个快照目录。普通目录没有 `manifest.json`、`files.seg` 和 `directories.seg`，不能直接传给 `index`。

Windows PowerShell 完整流程：

```powershell
$BIN = ".\golangtools-windows-amd64.exe"
$SNAPSHOT = "D:\golangtools-snapshots\download-$(Get-Date -Format yyyyMMdd-HHmmss)"
& $BIN filesystem scan -root "D:\3_Dowload" -output $SNAPSHOT -metadata basic -progress-interval 5s
& $BIN filesystem serve -snapshot $SNAPSHOT -listen 127.0.0.1:8080
```

Windows 的 `auto` 后端优先顺序读取本地 NTFS 的 MFT，需要读取卷设备的权限；不可用时自动回退到 `windows-native`。强制 `windows-mft` 时不会回退，适合确认管理员权限和 NTFS 快速路径是否生效。最终使用的后端会写入 `manifest.json` 的 `scanner_backend`。

需要延迟建索引时，在 `scan` 添加 `-build-index false`，之后单独执行 `filesystem index -snapshot $SNAPSHOT`。

## archive

```bash
golangtools archive encode -path <file-or-directory> [-result-dir go-tool-result] [-output path]
golangtools archive decode -input <encoded-file> [-result-dir go-tool-result] [-output path]
```

## document

```bash
golangtools document read -path <file.docx>
golangtools document extract [-process names] [-type word,excel,ppt,pdf,all] [-ext .txt,.pdf] [-mode copy|b64|pdf|bin|mem|all] [-output directory]
```

内存扫描在 macOS 需要原生 CGO 构建和系统授权，在 Windows 需要相应进程权限。

## filestore

```bash
golangtools filestore query -name <name>
golangtools filestore upload-local -path <path>
golangtools filestore upload-server -path <path>
golangtools filestore download -name <name>
```

该模块的连接信息全部来自环境变量，程序内不含账号、密码或固定业务地址。

## process

```bash
golangtools process usage -target <pid-or-name> [-output metrics.csv] [-interval 1s]
golangtools process kill -target <pid-or-name> [-log monitor.log] [-interval 3s]
golangtools process guard -name <name> [-cmd command] [-log guard.log] [-interval 3s]
```

## network

```bash
golangtools network ping -range '10.0.[1-2].[1-254]' [-workers 256] [-timeout 1s] [-max-targets 1000000]
golangtools network ping -config <pattern-file>
```

## cleanup

```bash
golangtools cleanup scan [-format text|json] [-output path] [-home path]
golangtools cleanup run -target <comma-separated-targets> [-format text|json] [-output path] [-home path]
```

## mockdata

```bash
golangtools mockdata generate -dsn <mysql-dsn> -tables <t1,t2> [-rows 10] [-dry-run]
```

表名只允许 MySQL 普通标识符，单次每表最多生成 100,000 行。

## health

```bash
golangtools health serve [-listen 127.0.0.1:8080]
```

检查地址为 `/health`。

## 文件系统扫描阶段与日志

`filesystem scan` 是两个连续阶段：

1. `scan`：原生目录枚举、元数据读取和 segment 写入。目录总数未知，因此首次扫描只显示已发现条目、已完成目录、待处理目录、实时吞吐和耗时，不伪造百分比。
2. `index`：批量导入目录、批量导入文件、扩展名统计、FTS5 文件名索引、逐项 SQLite B-tree 索引、元数据写入和 `query.db` 发布。导入阶段按已知记录总数显示百分比；搜索和单个 SQLite 索引创建期间按 `-progress-interval` 输出心跳，不会静默卡住。

`-progress-interval` 控制阶段日志和进度刷新周期。默认 `2s`；想观察小目录可以使用 `200ms`，大盘不建议设置得过短。

每次命令都会写一份工具级 JSONL 日志：

```text
~/.golangtools/logs/<时间>-<模块>-<命令>-<run-id>.jsonl
```

也可以通过 `GOLANGTOOLS_LOG_DIR` 指定目录。文件系统扫描还会把相同事件追加到 `<snapshot>/run.log`，便于把快照和运行过程一起交付。终端 stderr 同时打印人类可读的阶段行。索引结果同时提供 `duration_ns` 和人类可读的 `duration` 字段。

## 并发调优起点

下表是测试起点，不是固定吞吐承诺。机械盘通常受寻道限制，增加 worker 可能变慢；SSD 和 APFS 可以逐步增加并发后用实际耗时选择。

| 存储 | 建议起点 |
|---|---:|
| 机械硬盘 | 2-8 |
| SATA SSD | 8-16 |
| NVMe SSD | 16-32 |
| macOS APFS | 使用默认值，当前上限 64 |

例如：

```bash
golangtools filesystem scan -root /data -output /tmp/data-snapshot -workers 8 -progress-interval 5s
```

Linux amd64 发行包使用 `dist/golangtools-linux-amd64`；Ubuntu ARM64 使用 `dist/golangtools-linux-arm64`。
