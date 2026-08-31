---
title: "治理文件服务器"
description: "将一个目录树（本地、NFS 或 SMB）作为只读的受治理知识源连接：文件成为文档，POSIX ownership 和 ACL 映射为文档 ACL，读取则从构造上被限制在 root 内。"
---

`filesystem` 内容 connector（`olivares.fs-content`）把目录树（本地 path、NFS
export 或 SMB mount）转为**受治理知识文档**。它们与所有其他内容源经过同一条
pipeline（redact → classify → chunk → embed → index → 通过 MCP 提供），文档 ACL
由 POSIX ownership 映射而来，classification 则来自 xattr。它是内容 **source**，
不同于把日志转发到*外部*的 `filelog` 日志 **sink**。

对于 self-hosted 运营者，文件服务器通常是最古老、最大的文档存储，因此这是
catalog 中价值最高的 connector 之一。

## 从构造上保证读取安全

connector 的读取被**限制在所配置的 root 内**，由 Go 标准库的 `os.Root` 保证：

- **指向 root 外的 symlink**、**绝对 path** 或通过 **`..` traversal** 的访问会被
  **拒绝**；connector 在物理上无法读取你未指定的文件。
- walk 时**不跟随** symlink（会计数，但绝不解析）。
- 每个文件 body 都有**大小上限**（更大文件会截断并标记），只读取**文本/文档
  类型**（binary 会跳过并计数），内容**绝不执行**；walk 还受**文件数量和总
  byte budget**约束，因此无法耗尽大型或缓慢的 NFS mount。

对抗性测试证明了对 symlink escape 和 path traversal 的拒绝。

## 指向一棵目录树

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "file-server",
      "kind": "filesystem",
      "config": {
        "root": "/mnt/fileserver/shared",   // local path or an NFS/SMB mount
        "include": "*.md,*.txt,docs/*",       // globs (path or basename); empty = all text
        "exclude": "**/archive/*,*.tmp",
        "max_file_bytes": "1048576",          // per-file cap (hard-capped at 1 MiB)
        "max_files": "100000",                // walk budget
        "max_total_bytes": "1073741824",      // read budget
        "text_only": "true",                  // skip binaries (counted)
        "map_posix_acl": "true",              // owner/group + POSIX.1e ACL → Document ACL
        "classification": "internal",         // default label (an xattr overrides per file)
        "classification_xattr": "user.classification",
        "labels_xattr": "user.olivares.labels"
      }
    }
  ]
}
```

每个文件成为一份 Document：body 是文件内容，DocID 是相对于 root 的 path，
provenance attribute 携带 `owner`、`group`、`mode`、`size`、`world_readable`
和 `path`。

## Ownership 与 ACL 如何映射 — 如实矩阵

connector **只映射文件系统实际表达的内容**，并明确声明它不能映射什么：

| 文件系统 | owner / group / mode | POSIX.1e ACL（`getfacl`） | Windows / NFSv4 ACL |
|---|---|---|---|
| **本地**（ext4/xfs/btrfs） | 映射：owner → `user:<name>`；group（若 group-readable）→ `group:<name>` | 映射：每个带 read bit 的具名 user/group entry → principal ref | 不适用 |
| **NFS** | **如果 uid/gid 映射一致**则映射（idmapd / 两端使用同一 directory） | mount 暴露 `system.posix_acl_access` 时映射 | **不解析 NFSv4-native ACL**（已声明限制） |
| **SMB / CIFS** | 从 **mount 的** `uid=/gid=/file_mode=`，即 mount option 映射，**不是**真实 Windows owner | 通常没有 | **不解析 Windows security descriptor**（`system.cifs_acl` 是 binary SD；已声明限制） |

principal 名称通过 host 的 name service 解析；其中可以包括 **LDAP**，使
`uid`→username 与你的 directory 一致。无法解析的 uid/gid 会回退到其**数字** id。
没有可推导 ACL 的文件继承知识库默认 ACL，retrieval 仍会强制执行。connector
**绝不虚构**文件没有携带的 ACL。

### Classification

- 默认 `classification` 应用于每个文件。
- 每文件 **xattr**（默认 `user.classification`）覆盖它。
- **external-labels xattr**（`user.olivares.labels`，逗号分隔）添加 sensitivity
  label，供 retrieval DLP 使用；这些 label 与 classification 一起以 deny-closed
  方式强制执行。

## 如实声明的限制

- **仅处理文本/文档文件。** binary 会跳过并计数。需要 extraction 的 rich format
  （PDF/DOCX）**不会**由此 connector ingest（这是明确列出的后续工作，不是静默
  跳过）。
- body **上限为 1 MiB**；更大文件会截断并标记 `truncated`。
- **SMB**：connector 看到的是 mount 的合成 POSIX view，不是真实 Windows ACL。
- connector **只读**，绝不写入目录树（设计上没有写入 path）。

## 实际集成证明

安全保证由对抗性测试覆盖：symlink escape、traversal、大小上限、binary skip、
POSIX owner/group/ACL 映射和 xattr classification。完整的实际集成证明是在一个
folder binding 后放置 fixture tree，并通过 MCP 提供，使 Claude Code session 只能
看到其 binding 与文件 ACL 共同允许的内容，同时证明某个 subtree 被拒绝；这是
组合 engine 的 CI integration job。
