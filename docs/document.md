# 文档模块

读取 DOCX/DOCM 正文：

```bash
golangtools document read -path ./document.docx
```

从正在运行的办公软件进程中定位已打开文件：

```bash
golangtools document extract \
  -process wps,word,excel \
  -type all \
  -mode copy \
  -output ./documents
```

输出模式：

| 模式 | 说明 |
|---|---|
| `copy` | 复制原文件 |
| `b64` | 写入可由 `archive decode` 还原的 Base64 文件 |
| `pdf` | 使用 `.pdf` 目标名复制原始字节，不做格式转换 |
| `bin` | 使用 `.bin` 目标名复制原始字节 |
| `mem` | 扫描进程内存中的 ZIP 文档片段并输出 Base64 |
| `all` | 执行全部模式 |

macOS 内存扫描使用 Mach API，需要原生 CGO 构建、调试 entitlement 和系统权限。Windows 打开文件检测优先使用系统 API，并可使用发布目录中的 Sysinternals `Handle` 工具作为回退。
