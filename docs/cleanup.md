# 磁盘清理模块

先运行只读分析：

```bash
golangtools cleanup scan
golangtools cleanup scan -format json -output ./cleanup.json
```

规则覆盖 Docker、Snap、日志、1Panel、旧内核和常见开发缓存。报告会区分观测体积、预计可回收体积、风险和是否允许自动清理。

执行时必须显式选择目标：

```bash
golangtools cleanup run -target go-mod-cache,go-build-cache,pip-cache
```

当前执行目标：

```text
1panel-upgrade
go-mod-cache
go-build-cache
nuget-packages
pip-cache
snap-disabled-revisions
```

系统目录和 Snap 操作可能需要管理员权限。
