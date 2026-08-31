---
title: "面向 Claude 的受治理数据"
description: "通过语义知识库和 MCP retrieval endpoint 向 Claude Code 提供 Drive 或 S3 内容，并以 identity、clearance、ACL 和 source scope 加以治理。"
sidebar:
  order: 7
---

这条路径让 Claude Code 可以查询**你自己的** Google Drive 或 S3 内容，而不会把
Olivares 变成 AI gateway。control plane 把内容导入受治理知识库，为每份文档记录
provenance，并且只通过 MCP 提供 retrieval tool：

| 默认行为 | 含义 |
|---|---|
| 语义知识库 | `embed_policy=model_backed`；ingest 前，`/status` 必须显示 `retrieval_semantic=true`。 |
| 可见的 fallback | 若未配置语义 embedder，知识库创建/ingest 会拒绝，而不会假称 local-hash vector 具有语义。 |
| 感知 ACL 的 guard | 发起请求的 agent 必须解析到一个已绑定的 identity，且其 `attr_clearance` 足够、group ACL 匹配。 |
| Source scope | 把知识库绑定到 Claude Code agent；scope 外的主体以 deny-closed 方式拒绝。 |
| 如实的 live mode | live connector response 携带 `source_mode=live`；静态 export 保持 `source_mode=export`，绝不冒充 live。 |

## 1. 存储 source credential

把 live source credential 保存在 runtime secret store 中。source config 只以
`store:<name>` 引用它，绝不 inline 保存。

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

对于 Google Drive，请把 deployment 用于只读访问 Drive 的 OAuth bearer/refresh
材料存入另一个 secret 名称。

## 2. 生成受治理 RAG config

对于 S3：

```sh
olivares quickstart governed-rag \
  --data-dir /var/lib/olivares \
  --tenant-id ten_... \
  --source s3 \
  --source-name prod-runbooks-live \
  --bucket prod-runbooks \
  --prefix claude/ \
  --credential-ref store:s3/prod-runbooks-read \
  --mcp-issuer https://idp.example.com/ \
  --mcp-jwks-url https://idp.example.com/.well-known/jwks.json
```

对于 Google Drive，使用 `--source gdrive --drive-id <shared-drive-id>` 和一个 Drive
credential 引用。

该命令写入：

| 文件 | 用途 |
|---|---|
| `sources.json` | 在 `documents[]` 下以 `mode=live` 注册内容 source。 |
| `agent-gateway.json` | 以 `retrieval.enabled=true` 启用 MCP resource server。 |
| `bootstrap-after-login.sh` | 创建语义知识库、ingest live source、绑定 agent，并添加 source-scope binding。 |

如果命令警告 `retrieval_semantic=false`，请先配置 `OLIVARES_EMBEDDINGS_*`。
model-backed 知识库在只有 local-hash fallback 时会刻意拒绝 ingest。

## 3. 使用生成的 config 启动

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

如果这是全新安装，请完成首次运行的控制台设置。然后使用 admin token 运行
bootstrap script：

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Identity 前提

retrieval guard 从 roster/SCIM graph 读取 identity fact。Claude Code 要取得受限内容，
其绑定 identity 必须已经存在：

| Identity fact | 示例 |
|---|---|
| Agent token subject / `agent_ref` | `claude-code-governed` |
| 已绑定 NHI identity | `agent:claude-code-governed` |
| Clearance metadata | `attr_clearance=confidential` 或更高 |
| Group membership | 与文档 ACL 匹配的 `group:engineering` |

如果 agent 没有 identity、clearance 或匹配的 group，受限 chunk 不会返回。如果 agent
没有通过 source scope 绑定到知识库，MCP retrieval call 会以 deny-closed 方式失败。

## 5. 把 Claude Code 指向 MCP

在 Claude Code 中配置 quickstart 输出的受保护 resource URL，通常为：

```text
http://127.0.0.1:8446/mcp
```

向该 MCP resource server 提交的 access token 必须具有：

| Claim/control | 必需值 |
|---|---|
| `iss` | 由 `--mcp-issuer` 配置的 issuer。 |
| `sub` | agent external id，例如 `claude-code-governed`。 |
| Scope | `knowledge:retrieval:read`。 |
| Audience/resource | `agent-gateway.json` 中配置的 MCP resource URL。 |

## 6. 验证

运行参考 E2E demo：

```sh
task demo:governed-rag
```

它检查 semantic status、live-source provenance、一次获准且符合 scope 的 retrieval、
低 clearance 时不返回结果、scope 外拒绝，以及 MCP result 中的
`source_mode=live`。

对于现有 deployment，还要验证一份真实文档：

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

每份从 live source ingest 的文档都应显示 `source_mode: "live"`。如果显示 `export`，
说明知识库来自 export file，向运营者描述时也必须如实说明。
