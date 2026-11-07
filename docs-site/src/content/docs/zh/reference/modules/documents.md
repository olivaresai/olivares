---
title: 文档
description: 使用 MarkItDown 在本地转换为 Markdown；保留原始文件，不访问网络。
---

将 PDF、DOCX、PPTX、XLSX、HTML、CSV、JSON、XML、EPUB 或 ZIP 附加到会话、上传到“我的文件”，或使用 `@path` 指定会话文件夹内的文档。路径包含空格时使用 `@"path with spaces.pdf"`。Microsoft MarkItDown 在本地转换，智能体收到的 Markdown 被视为不可信文档内容。图片仍以图片形式传递。

原始文件保持不变，Markdown 保存在旁边，不替换已有文件。文档模块在新安装中默认启用，可在“设置 > 模块”中关闭。模块关闭或缺少转换器时，新的转换会返回明确错误。

`olivares files convert report.pdf` 输出 `report.pdf.md` 的路径；`--json` 返回 `{"path":"..."}`。退出码：0 表示成功，1 表示转换失败，2 表示参数无效。已有的 Markdown 文件不会被替换。

转换器不能访问网络、插件或提供商凭据。它仅通过 stdin 接收文档字节，需要 Linux Landlock ABI 3 或更新版本。限制为输入和输出各 32 MiB、总运行时间 30 秒、CPU 时间 25 秒、虚拟内存 768 MiB。附件仍受 3.75 MiB 的限制。服务器 ISO 和 Docker 镜像包含使用哈希锁定的依赖项，转换期间不会安装软件包。
