# 文件系统扫描模块使用指南

本模块用于扫描目录树、分析文件大小并在本机浏览结果。它由三个独立阶段组成：扫描、单文件发布、HTTP 查看。扫描结束后进程可以退出，查看器只读取离线快照。

## 1. 输出格式

一次成功扫描的输出目录：

```text
snapshot.gti   唯一数据文件
run.log        本次运行日志
```

`snapshot.gti` 包含 manifest、目录记录、文件记录、名称池、父子索引、扩展名统计和错误记录。文件不包含原始文件内容。

目录记录里含有扫描时算好的递归统计值（该目录下累计的文件数、目录数、逻辑大小和分配大小），网页的「当前目录最大文件」面板直接读这些值。在它们被写入索引之前生成的旧快照没有这些数字，网页会显示「需重新扫描」，重新执行一次 `filesystem scan` 即可。

旧版 `manifest.json`、`files.seg`、`directories.seg`、`tree.*` 和 `query.db` 不再是正式格式，也不能由新服务读取。旧快照必须重新扫描。

## 2. 构建和运行

完整构建需要 Go 1.24+、Node.js 20+ 和 pnpm：

```bash
./build.sh
```

Windows：

```powershell
.\build.ps1
```

扫描示例：

```bash
BIN=/path/to/golangtools-darwin-arm64
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"

"$BIN" filesystem scan \
  -root /System/Volumes/Data \
  -output "$SNAPSHOT" \
  -metadata basic \
  -progress-interval 5s
```

Windows：

```powershell
$BIN = ".\golangtools-windows-amd64.exe"
$SNAPSHOT = ".\golangtools-snapshots\download-$(Get-Date -Format yyyyMMdd-HHmmss)"

& $BIN filesystem scan `
  -root "D:\3_Dowload" `
  -output $SNAPSHOT `
  -metadata basic `
  -progress-interval 5s
```

查看：

```bash
"$BIN" filesystem serve \
  -snapshot "$SNAPSHOT/snapshot.gti" \
  -listen 127.0.0.1:8080
```

浏览器访问 `http://127.0.0.1:8080/`。

## 3. 参数

| 参数 | 说明 |
| --- | --- |
| `-root` | 要扫描的真实目录或挂载点，必填 |
| `-output` | 输出目录，默认 `go-tool-result/filesystem-snapshots/<timestamp>` |
| `-workers` | 扫描并发数，0 使用平台默认值 |
| `-metadata basic` | 读取大小、分配空间、时间和平台元数据 |
| `-metadata tree` | 只读取名称和层级，速度优先 |
| `-backend auto` | 自动选择平台扫描器 |
| `-backend windows-mft` | 强制 Windows NTFS MFT |
| `-backend windows-native` | 强制 Win32 原生枚举 |
| `-progress-interval` | 进度刷新间隔，例如 `5s` |
| `-path-root` | 在另一台电脑上映射原始根路径 |
| `-listen` | 查看器监听地址，默认 `127.0.0.1:8080` |
| `-token` | 固定 bearer 令牌；非回环监听时必填 |
| `-allow-remote-content` | 允许非回环客户端读取文件内容，仅网页预览需要 |

当前版本已经移除 `-build-index`、`filesystem index` 和 `filesystem tree-index`。不要再把 `false` 作为布尔参数的独立位置参数传入。

## 4. 进度和日志

扫描阶段总目录数未知，因此显示：

```text
files dirs pending errors rate elapsed
```

扫描结束后会生成固定宽度树，随后发布 GTI。树阶段会显示已处理节点和百分比。日志阶段包含开始时间、扫描后端、每个阶段、完成时间、耗时和失败原因。

日志位置：

```text
~/.golangtools/logs/*.jsonl
<snapshot>/run.log
```

## 5. 性能

树聚合是线性内存访问，当前 700 多万个节点的快速树构建约为秒级。新方案不再把记录导入 SQLite，也不再生成 FTS5、trigram 和多个 B-tree。

默认 GTI 不压缩固定记录，便于 mmap 和目录随机访问。单文件体积通常显著小于 segment、tree 和 SQLite 三份副本之和。

机械盘建议从 `-workers 2`、`4`、`8` 对比；SSD、APFS 和 NTFS MFT 可测试更高并发。并发数只影响枚举阶段，不能消除磁盘本身的寻道和权限成本。

## 6. Windows 大小语义

`logical_bytes` 是文件逻辑长度。`allocated_bytes` 是文件实际占用的磁盘块，在稀疏文件、压缩文件、硬链接和 APFS clone 场景下可能不同。

Windows NTFS MFT 后端可以得到分配信息。Win32 回退通常没有逐文件分配块信息，会将 `allocated_bytes_known` 设为 `false`，网页自动使用逻辑大小，不会把 0 解释成真实占用。

## 7. 迁移

复制：

```text
snapshot.gti
```

目标电脑：

```bash
golangtools filesystem serve -snapshot /path/to/snapshot.gti
```

如果原始目录在目标机位于 `/data/new`：

```bash
golangtools filesystem serve \
  -snapshot /path/to/snapshot.gti \
  -path-root /data/new
```

浏览、统计、搜索和复制路径不要求原始文件存在。只有“打开文件/打开文件夹”和网页文件预览要求目标机路径可访问。快照不会复制原始文件内容，预览是现读磁盘上的当前文件，所以扫描和 serve 不在同一台机器时预览会提示路径不存在（`-path-root` 只改路径映射，不会凭空造出文件）。

在别的机器上需要预览时，文件内容接口默认拒绝非回环请求，需要显式放开：

```bash
golangtools filesystem serve -snapshot /path/to/snapshot.gti -listen 0.0.0.0:8080 -allow-remote-content
```

## 8. 中断和错误

Ctrl-C 或 SIGTERM 会取消扫描。未完成任务不会发布可用 GTI；使用新输出目录重试。单个受保护目录的错误会写入 GTI errors 段，不会阻断其余目录。

状态检查：

```bash
golangtools filesystem status -snapshot /path/to/snapshot.gti
```

常见问题：

| 错误 | 处理 |
| --- | --- |
| `snapshot.gti unavailable` | 传入了普通目录或旧快照；重新扫描 |
| 实际占用不可用 | Windows 回退后端没有分配块信息，使用 NTFS MFT 或接受逻辑大小 |
| 打开路径失败 | 检查 `-path-root`、文件是否存在及当前用户权限 |
| 端口占用 | 使用 `-listen 127.0.0.1:18080` |
| 搜索耗时较长 | 默认不生成搜索副本，任意子串搜索会扫描记录；目录浏览不受影响 |
| 预览提示路径不存在 | GTI 不保存文件内容，预览现读磁盘；确认扫描和 serve 在同一台机器，或该文件未被删除 |
| 远程访问预览返回 403 | 文件内容接口默认只接受回环请求，加 `-allow-remote-content` 显式放开 |
| 网页显示「需重新扫描」 | 旧快照没有写入目录递归统计值，重新执行一次 `filesystem scan` |
