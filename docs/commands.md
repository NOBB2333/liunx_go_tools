# 文件系统命令

## 命令总览

```text
golangtools filesystem probe -path <mount-or-directory>
golangtools filesystem scan -root <source-directory> [options]
golangtools filesystem serve -snapshot <snapshot.gti-or-directory> [options]
golangtools filesystem status -snapshot <snapshot.gti-or-directory>
```

旧的 `filesystem index`、`filesystem tree-index` 和 `-build-index` 已移除。扫描完成后不会再有第二个 SQLite 索引阶段。

## 扫描

macOS/Linux：

```bash
BIN=/path/to/golangtools-darwin-arm64
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"

"$BIN" filesystem scan \
  -root /System/Volumes/Data \
  -output "$SNAPSHOT" \
  -metadata basic \
  -progress-interval 5s
```

Windows PowerShell：

```powershell
$BIN = ".\golangtools-windows-amd64.exe"
$STAMP = Get-Date -Format yyyyMMdd-HHmmss
$SNAPSHOT = ".\golangtools-snapshots\download-$STAMP"

& $BIN filesystem scan `
  -root "D:\3_Dowload" `
  -output $SNAPSHOT `
  -metadata basic `
  -progress-interval 5s
```

`-metadata basic` 读取大小、实际占用和时间。`-metadata tree` 只收集目录树和名称，适合只关心层级的极限速度测试。`-backend auto` 在 Windows NTFS 上优先使用 MFT，无法读取时回退到 Win32 枚举。

布尔参数不需要写 `false`；当前版本没有 `-build-index` 参数。输出目录不要放在正在扫描的根目录内。

## 运行过程

扫描阶段会显示文件数、目录数、待处理目录、错误数、实时吞吐和 elapsed。扫描结束后会顺序构建固定宽度树段并发布 GTI。最终输出类似：

```text
snapshot: /path/to/snapshot/snapshot.gti
```

输出目录只有：

```text
snapshot.gti
run.log
```

`run.log` 是本次命令的 JSONL 日志；工具级日志仍在 `~/.golangtools/logs/`，可用 `GOLANGTOOLS_LOG_DIR` 覆盖。

## 启动查看器

```bash
"$BIN" filesystem serve \
  -snapshot "$SNAPSHOT/snapshot.gti" \
  -listen 127.0.0.1:8080
```

也可以直接传输出目录：

```bash
"$BIN" filesystem serve -snapshot "$SNAPSHOT"
```

`serve` 参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-snapshot` | 必填 | `snapshot.gti` 或其所在目录 |
| `-listen` | `127.0.0.1:8080` | 监听地址；非回环地址会自动生成令牌 |
| `-path-root` | 空 | 在另一台电脑上映射原始根路径 |
| `-token` | 空 | 固定 bearer 令牌；非回环监听时必填 |
| `-allow-remote-content` | `false` | 允许非回环客户端读取文件内容，仅预览功能需要 |

默认只监听本机回环地址。服务端分页读取 GTI，浏览器不会下载完整目录树。搜索默认并行扫描名称记录，不创建 SQLite 或 FTS 副本。

文件内容接口（`/api/v1/snapshots/active/content/...`）默认只接受回环请求：它会把磁盘上的原字节发出去，快照又常被拷到别的机器浏览，所以放宽必须显式打开。不加这个参数时，远程仍可浏览目录、统计和搜索，只是不能预览文件内容。

## 跨电脑迁移

只复制：

```text
snapshot.gti
```

目标电脑使用对应平台二进制：

```bash
golangtools filesystem serve \
  -snapshot /data/copied/snapshot.gti \
  -listen 127.0.0.1:8080
```

快照不包含原始文件内容。若目标电脑上的原始目录路径不同，使用：

```bash
golangtools filesystem serve \
  -snapshot /data/copied/snapshot.gti \
  -path-root /new/local/root
```

网页浏览不要求目标机存在原始目录；只有点击“打开文件/打开文件夹”时，目标路径必须存在并且当前用户有权限。打开操作由本机 Go 后端调用 Finder、Explorer 或 `xdg-open`，浏览器不会逐目录申请权限。

文件预览同样要求原始文件真实存在——GTI 不保存文件内容，预览是现读磁盘。扫描和 serve 不在同一台机器时，预览会提示路径不存在；`-path-root` 只改路径映射，不会凭空造出文件。

## 状态

```bash
golangtools filesystem status -snapshot /data/copied/snapshot.gti
```

状态输出包括扫描 manifest、GTI 文件路径、文件大小和是否已发布。

## 失败和中断

扫描收到 Ctrl-C 或系统终止信号时，当前任务会停止，临时文件不会作为完整快照发布。重新运行时使用新的输出目录。已存在的完整 `snapshot.gti` 不会被半成品覆盖。

## 性能建议

- 对需要空间分析的扫描使用 `basic`；只看树时使用 `tree`。
- 输出目录放在速度较快的本地磁盘，避免放在正在扫描的网络盘。
- Windows NTFS 使用管理员权限运行以启用 MFT 后端。
- 不要在扫描命令后再运行旧的 `index` 或 `tree-index`，这些命令已不存在。
