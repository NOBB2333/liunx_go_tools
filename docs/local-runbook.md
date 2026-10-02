# 本地运行手册

## 构建

开发机需要 Go 1.24+、Node.js 20+ 和 pnpm。完整构建：

```bash
./build.sh
```

Windows：

```powershell
.\build.ps1
```

发行包会生成 macOS、Linux、Windows 的 amd64 和 arm64 目标，并包含 `snapshot.gti` 使用文档。

## 推荐流程

```bash
BIN=/path/to/golangtools
SNAPSHOT="$HOME/golangtools-snapshot-$(date +%Y%m%d-%H%M%S)"

"$BIN" filesystem scan \
  -root /data \
  -output "$SNAPSHOT" \
  -metadata basic \
  -progress-interval 5s

"$BIN" filesystem serve \
  -snapshot "$SNAPSHOT/snapshot.gti" \
  -listen 127.0.0.1:8080
```

扫描过程中会输出阶段、条目数、吞吐、错误和 elapsed。扫描完成后只生成 `snapshot.gti` 与 `run.log`。没有独立索引命令，也没有 SQLite 后台阶段。

## 迁移

将 `snapshot.gti` 复制到另一台电脑后：

```bash
golangtools filesystem serve -snapshot /mnt/share/snapshot.gti
```

如果只想浏览，目标机器不需要存在原始根目录。如果要使用网页的本机打开功能，使用 `-path-root` 映射到目标机器实际路径。

## 日志

- `~/.golangtools/logs/*.jsonl`：工具级命令日志。
- `<snapshot>/run.log`：扫描与发布阶段日志，随快照目录生成。

日志中包含命令开始、扫描、树生成、GTI 发布、完成或失败时间点。快照数据和日志可以分别迁移。

## 状态检查

```bash
golangtools filesystem status -snapshot /path/to/snapshot.gti
```

该命令读取 GTI manifest 并显示记录数、大小、扫描耗时、GTI 文件大小和发布状态。

## 中断处理

Ctrl-C 会取消扫描。未完成的临时记录会留在输出目录中，但不会作为完整 GTI 使用；重新扫描时使用新的输出目录。发布阶段采用临时文件和原子重命名，已完成快照不会被破坏。

## 常见问题

| 问题 | 处理 |
| --- | --- |
| `snapshot.gti unavailable` | 传入了原始目录或不完整输出；重新执行 `filesystem scan` |
| Windows 实际占用为未知 | 使用 NTFS MFT 后端并授予管理员权限；Win32 回退没有逐文件分配块信息 |
| 页面搜索较慢 | 任意子串搜索默认扫描名称记录，这是为了避免生成巨大的搜索副本；目录浏览不受影响 |
| 点击打开失败 | 目标机没有对应原始路径或当前用户权限不足；检查 `-path-root` |
| 旧快照无法打开 | 旧 segment/tree/SQLite 格式已移除；重新扫描生成 GTI |
