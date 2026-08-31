---
title: "面向 S3 的 AWS CloudTrail（clean 层 R/RW）"
description: >-
  从 CloudTrail 数据事件中捕获对 S3 对象的读/写访问 —— readOnly 标志逐字采用，
  IAM 主体作为发起方，当承担角色掩盖了真实调用者时给出诚实的近似归属。
sidebar:
  order: 2
---

`s3cloudtrail` source 将 AWS CloudTrail 的 **S3 数据事件**转化为访问图边：
每个 S3 事件对应一条边，其读/写模式**逐字取自 CloudTrail 的 `readOnly` 字段** ——
从不推断 —— 并以 CloudTrail 将该调用归属到的 IAM 主体作为发起方。
它是对象存储的 clean 层，是面向 Postgres 的 [pgAudit](/zh/how-to/connectors/pgaudit/)
在 S3 上的对应物。

该连接器**读取本地日志文件，从不调用 AWS**：你交付 CloudTrail 文件
（你的 trail 已在生成的标准 S3 投递布局），它来解析这些文件。只有
`eventSource == s3.amazonaws.com` 的事件会被处理 —— 管理平面事件属于
[`aws` 云发现连接器](/zh/reference/connectors/)，而非本连接器。

## 它发出什么

| 字段 | 取值 |
|---|---|
| 信号来源 | `cloudtrail` |
| 模式 | `readOnly: true` → `read`，`false` → `write`，缺失 → `unknown` —— 逐字采用，从不猜测 |
| 发起方 | IAM 主体（用户、承担角色会话、AWS 服务） |
| 置信度 | `attributed`；对于共享的承担角色和由服务发起的调用为 `approximate` |
| 覆盖层级 | clean |

## 1. AWS 侧前置条件

- 一个为你所治理的存储桶**启用了 S3 数据事件**的 CloudTrail **trail**
  （数据事件不在默认的管理 trail 中）。
- 将该 trail 的日志文件投递到引擎主机可读取的位置 ——
  标准的 S3 投递桶，在本地同步或挂载。连接器接受经典的 `{"Records":[…]}`
  文件（纯文本或 `.json.gz`）以及换行分隔的记录。

## 2. 声明 source

```json
{
  "sources": [{
    "name": "prod-s3-trail",
    "kind": "s3cloudtrail",
    "tenant": "<tenant-id>",
    "config": {
      "path": "/var/lib/cloudtrail/prod/",
      "shared_accounts": "arn:aws:iam::123456789012:role/app-runtime"
    }
  }]
}
```

| 键 | 必填 | 含义 |
|---|---|---|
| `path` | 是 | 单个 CloudTrail 文件，或一个包含 `*.json` / `*.json.gz` 文件的目录 |
| `shared_accounts` | 否 | 由多个调用者共享的角色 ARN，以逗号分隔 —— 它们的边会被诚实地标为 `approximate` |

（`s3-cloudtrail` 被接受为 `kind` 的别名。）

## 3. 你将在控制台中看到什么

S3 存储桶和对象会带着 clean 层徽标加入**访问图（Access map）**；
读与写依据 `readOnly` 标志着色。漂移面板将它们与已声明的授权交叉比对，
方式与任何其他 source 完全一致。

在 **Inventory** 中，CloudTrail 将调用归属到的主体会作为身份出现，
随时可绑定到 agent —— 正是这种绑定，把一个共享角色的 `approximate`
变成按 agent 的 `attributed`。

## 诚实的局限 —— 在信任这张图之前请先读这里

- **由多个调用者共享的承担角色无法指明真实调用者。**
  CloudTrail 把调用归属到角色会话；如果该角色被共享，那条边会被刻意标为 `approximate`。
  在 `shared_accounts` 中声明该角色会使其变得明确。持久的修复方式是按 agent
  的身份（[身份依赖](/zh/how-to/connect-a-source/#硬性依赖每代理身份)）。
- **你未启用的数据事件并不存在。** CloudTrail 只记录 trail 被配置为记录的内容；
  如果某个存储桶的数据事件已关闭，缺少一条边并不等于不存在访问。
- **投递延迟是 CloudTrail 的。** 数据事件按 CloudTrail 的投递时间表到达
  （通常为分钟级）；本 source 不是实时探针。

## 相关

- [pgAudit](/zh/how-to/connectors/pgaudit/) —— 面向 PostgreSQL 的同一套 clean 层准则。
- [连接一个 source](/zh/how-to/connect-a-source/) —— 连接器模型。
- [连接器与覆盖层级](/zh/reference/connectors/) —— 每种存储诚实归位之处。
