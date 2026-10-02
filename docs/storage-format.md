# 文件系统快照格式

每个快照目录包含：

```text
manifest.json
files.seg
directories.seg
errors.ndjson
query.db
```

## manifest.json

记录 schema 版本、根目录、扫描模式、扫描后端、worker 数、文件和目录数量、逻辑/分配字节数、错误数、起止时间、耗时和实际吞吐。

当前 `schema_version` 为 `3`。目录数量不包含根目录；查询索引包含根目录。`scanner_backend` 记录最终使用的扫描器，`allocated_bytes_known` 表示实际分配字节是否有效，`allocation_source` 记录其来源。Windows 原生枚举无法在不逐文件打开句柄的前提下获得分配块，因此将 `allocated_bytes_known` 写为 `false`；此时 0 是未知值占位。稀疏文件、APFS clone 或硬链接可能让逻辑大小大于磁盘容量；只有分配字节已知时，空间分析才默认使用实际分配字节数。

## segment

所有整数使用 Little Endian。新 segment 具有 16 字节文件头：

| Offset | 长度 | 内容 |
|---:|---:|---|
| 0 | 8 | magic `GTSSEG01` |
| 8 | 2 | segment 格式版本，当前为 `2` |
| 10 | 2 | 类型：`1` 文件，`2` 目录 |
| 12 | 4 | 保留 |

`files.seg` 的每条新记录为 72 字节固定头加 UTF-8 名称。索引器仍能读取无头旧记录和 v1 记录：

| 字段 | 类型 |
|---|---|
| parent_id | uint64 |
| inode | uint64 |
| device | uint64 |
| size | int64 |
| blocks | int64 |
| mtime_ns | int64 |
| ctime_ns | int64 |
| birthtime_ns | int64 |
| mode | uint32 |
| name_length | uint32 |
| name | bytes |

`directories.seg` 的每条记录为 60 字节固定头加 UTF-8 名称：

| 字段 | 类型 |
|---|---|
| id | uint64 |
| parent_id | uint64 |
| depth | uint32 |
| reserved | uint32 |
| logical_bytes | uint64 |
| allocated_bytes | uint64 |
| file_count | uint64 |
| directory_count | uint64 |
| name_length | uint32 |
| name | bytes |

索引器仍能读取版本化之前的无文件头 segment。

## errors.ndjson

每行一条 JSON 错误记录，包含 `path` 和 `error`。HTTP API 流式分页读取该文件，不会一次加载完整日志。

## query.db

SQLite sidecar 当前 `user_version=4`，包含：

- `metadata`：格式版本和扫描摘要
- `directories`：目录层级和聚合大小
- `files`：文件元数据，包括 mtime、ctime、birthtime 和分配块
- `extension_stats`：索引阶段物化的扩展名统计，同时保存逻辑和实际分配字节
- `file_search`：FTS5 trigram 文件名子串索引

索引使用临时文件构建，完成后在 macOS/Linux 原子替换目标。新索引使用 trigram 倒排表处理任意位置的文件名关键词；旧版 `query.db` 会自动回退到兼容查询，旧数据缺少的时间字段显示为“不可用”。扫描过程不依赖 `query.db`，可以用 `filesystem index` 随时重建。
