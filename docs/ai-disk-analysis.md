# 用 AI 分析磁盘快照

AI 可以直接查询 `snapshot.gti`，无需启动网页、导出几百万条记录或重新扫描磁盘。`filesystem inspect` 输出带版本号的 JSON，适合有终端访问能力的 AI 助手，也可以被脚本或其他程序调用。它使用普通命令行和 JSON，不依赖特定 AI 服务，也不是 MCP 服务。

命令只读取已有快照的元数据：路径、文件大小、修改时间、目录汇总、扩展名和扫描异常。它不读取原始文件内容，也不删除或修改被扫描文件。快照本身不包含文件内容，但文件名和目录名可能包含私人信息；分享快照或查询结果前应留意接收方。

## 直接交给新会话

将以下内容复制给可以访问本机终端和项目的 AI 会话，并按实际位置替换两个路径：

```text
请阅读 docs/ai-disk-analysis.md，使用 golangtools 的只读 JSON 查询分析磁盘空间。

程序：/path/to/golangtools
快照：/path/to/snapshot.gti

先查询总览，再搜索较大的缓存、构建产物和其他可疑目录；只对有必要的目录继续查询。请按“可能可再生、需要我确认、不宜直接清理”分组，给出路径、快照记录的大小、判断依据和可能的影响。标明扫描时间、扫描范围和读取失败的限制；不要把目录与其子目录的大小相加，也不要把虚拟磁盘的逻辑大小当成可回收空间。

快照可能已经过时，必要时只读检查候选路径是否仍存在；不要仅凭目录大就断定它可以清理。这次只需要分析和建议，不要删除、移动或修改文件。如果需要确认项目、备份、虚拟机或软件仍在使用，先问我。
```

其中 `程序` 是由当前源码构建的对应平台可执行文件；`快照` 可以是 `.gti` 文件或其所在目录。新旧快照均可查询，不需要因为增加这个命令而重新扫描。

## 查询示例

macOS/Linux 的示例；Windows PowerShell 可以用 `& $BIN filesystem inspect ...` 调用对应的 `.exe`。下面的变量只在当前终端生效：

```bash
BIN=/path/to/golangtools
SNAPSHOT=/path/to/snapshot.gti
```

查询整个快照的概况：

```bash
"$BIN" filesystem inspect -snapshot "$SNAPSHOT"
```

默认每组返回前 20 条，包含扫描范围和时间、根目录下最大的直接子目录、全快照最大的文件、扩展名统计和扫描异常。`manifest.errors` 是扫描时的错误数，并不代表路径现在仍有错误。`-limit 10` 可减少输出；结果应保留 `total` 以区分截断与完整列表。

全快照按名称找目录或文件，按大小降序返回：

```bash
"$BIN" filesystem inspect -snapshot "$SNAPSHOT" \
  -view largest -kind directories -query node_modules -limit 20

"$BIN" filesystem inspect -snapshot "$SNAPSHOT" \
  -view largest -kind directories -query cache -limit 20

"$BIN" filesystem inspect -snapshot "$SNAPSHOT" \
  -view largest -kind files -min-bytes 1000000000 -limit 20
```

`-query` 是**文件或目录名称**的不区分大小写的子串，不搜索完整路径，也不读取文件内容。`largest` 在指定范围内递归查找，并排除范围目录自身。多个命中的目录可能存在父子关系，不能将它们的大小直接相加。

每个目录结果都有 `id`。拿结果中的目录 ID 查询直属子项，或在该目录的全部后代中继续搜索：

```bash
"$BIN" filesystem inspect -snapshot "$SNAPSHOT" \
  -view children -directory-id 123 -limit 30

"$BIN" filesystem inspect -snapshot "$SNAPSHOT" \
  -view largest -directory-id 123 -kind files -query log -limit 20
```

`123` 必须替换为实际返回的目录 ID；根目录 ID 是 `1`。ID 仅在当前快照内有效，文件 ID 和目录 ID 的数字可能相同，应按 `is_dir` 区分。`children` 只列直属条目，`largest` 递归查询后代。子项查询也可使用 `-kind files`、`-kind directories`、`-query` 和 `-min-bytes`。

其他分页查询：

```bash
"$BIN" filesystem inspect -snapshot "$SNAPSHOT" -view errors -limit 20 -offset 0
"$BIN" filesystem inspect -snapshot "$SNAPSHOT" -view extensions -limit 20 -offset 20
```

## JSON 约定

- `schema` 为 `golangtools.filesystem.inspect.v1`；`view` 标识查询类型；`snapshot_file` 指向读取的快照。
- `manifest` 包含扫描根路径、`finished_at`、文件数、目录数、字节总量、扫描错误数以及大小是否可用。`scope.directory_id` 和 `scope.path` 表示本次查询范围。路径是扫描时记录的原始路径，复制快照到其他机器不会自动映射。
- `size_metric` 是本次排序和 `-min-bytes` 使用的字段：优先 `allocated_bytes`，无法取得分配空间时回退到 `size_bytes`。所有字节数都是字节整数，展示为 GB 时应除以 1,000,000,000。
- `overview` 的 `data` 有 `top_directories`、`largest_files`、`extensions` 和 `errors` 四个分页对象；其他视图的 `data` 直接是一个分页对象。每个分页对象都有 `items`、`total`、`limit` 和 `offset`。`limit` 范围为 1–500，`offset` 为 0–10000；`overview` 不接受 `offset`。
- 条目中的 `size_bytes` 是逻辑大小，`allocated_bytes` 是扫描时各文件报告的分配空间汇总。目录数字包含其后代，文件的 `mtime_ns` 是 Unix 纳秒时间戳；目录记录不保证有修改时间。数据反映扫描时刻而不是实时状态。
- 常规 `tree` 模式没有空间元数据，不能用来做空间排行；需用 `-metadata basic` 重新扫描。Windows NTFS MFT 后端即使在 `tree` 模式也可能带有大小信息。Windows 原生回退可能没有分配空间，此时只能按逻辑大小排序，不能将结果视为实际可回收容量。
- 文件大小与实际释放空间不一定相同：硬链接、APFS 克隆、稀疏文件、虚拟磁盘、快照和正在使用的数据都可能影响释放量。扫描可能跨文件系统；文件系统容量也不等于扫描目录总量。扫描异常和未覆盖的目录应在结论中保留。

查询在本地扫描 GTI 的索引记录而非源磁盘。`largest` 的结果分页使用有界内存，但按名称查找仍需遍历相应类型的索引；将 `-kind` 限定为 `directories` 或 `files` 可以减少工作量。针对子目录的递归查询也会读取该子目录树的索引关系。
