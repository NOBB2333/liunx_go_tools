# 文件系统快照格式

文件系统扫描结果的唯一正式格式是 `snapshot.gti`。它是跨 Windows、macOS 和 Linux 的只读二进制容器，服务端可以直接通过 `ReadAt` 读取，不需要解压、不需要导入数据库，也不需要访问扫描时的原始磁盘。

扫描目录通常还会保留一个 `run.log`，它是运行日志，不属于数据格式。最小迁移只复制 `snapshot.gti`。

## 容器布局

文件头包含 magic、格式版本、段数量和段表位置。段表记录每个数据段的偏移和长度。当前段包括：

| 段 | 内容 |
| --- | --- |
| manifest | 扫描根路径、平台后端、计数、大小、时间、完成状态 |
| directories | 固定宽度目录记录 |
| files | 固定宽度文件记录 |
| names | UTF-8 名称池 |
| children | 连续的子节点 ID 数组 |
| ranges | 每个目录的子节点起始位置和数量 |
| dirids | 外部目录 ID 到内部索引的排序映射 |
| extensions | 扩展名统计 |
| errors | 扫描错误的 NDJSON 内容 |

所有数值字段使用 Little Endian。段偏移是文件内绝对偏移，因此服务端可以直接对一个文件执行随机读取和 mmap 优化。

## 固定记录

目录记录为 56 字节：

```text
id              uint64
parent_index    uint32
name_length     uint32
name_offset     uint64
logical_bytes   uint64
allocated_bytes uint64
file_count      uint64
directory_count uint64
```

文件记录为 56 字节：

```text
parent_index    uint32
name_length     uint32
name_offset     uint64
logical_bytes   uint64
allocated_bytes uint64
mtime_ns        int64
ctime_ns        int64
birthtime_ns    int64
```

目录和文件的显示 ID 仍然由 API 暴露，但内部父子关系使用连续 `uint32` 索引。完整路径不重复保存，查询时沿父指针拼接名称。

目录记录里的 `logical_bytes`、`allocated_bytes`、`file_count` 和 `directory_count` 都是**该目录下递归累计**的值，由扫描期的聚合器逐层上卷。在字段被写入之前生成的快照里，`file_count` 和 `directory_count` 恒为 0，网页按「不可用」处理并提示重新扫描。

## 为什么不用 Protobuf

Protobuf 适合 RPC 和消息交换，不适合作为本项目的主随机访问格式：

- 每条重复 message 都有字段标签和长度前缀。
- 数百万条记录不能直接按数组下标 mmap 定位。
- 父子范围、名称池和固定列需要另外建立索引。
- 最终仍然需要二次物化才能满足网页分页读取。

GTI 使用固定宽度记录、集中名称池和段表，读取路径更短，也避免了重复副本。

CLP、Logdy Pro 的列式压缩和字典编码思路适合日志字段查询，但日志时间序列和模板压缩不能直接替代目录树的父子索引。未来可以借鉴其分块压缩做传输包，但默认格式保持不压缩，以保证 mmap、随机读取和最快启动。

## 搜索

默认不生成 FTS、trigram 或 SQLite 搜索副本。任意文件名子串搜索由服务端分块扫描文件记录并分页返回。这样不会因为生成搜索索引而增加数百 MB 到数 GB 的重复数据，也不会阻塞目录树启动。

## 原子发布与完整性

扫描和发布使用临时文件：

```text
snapshot.gti.tmp -> fsync -> rename -> snapshot.gti
```

只有发布完成后才会删除内部临时 segment。中断的扫描不会得到 `complete=true` 的 GTI 文件。不要手工修改 GTI；需要更新数据时重新运行 `filesystem scan`。

## 版本策略

当前 GTI 是新的破坏性格式。旧的 `files.seg`、`directories.seg`、`tree.*` 和 `query.db` 不再由服务读取，旧快照需要重新扫描。格式版本写入文件头，未来不兼容版本应明确报错，而不是静默解释错误数据。
