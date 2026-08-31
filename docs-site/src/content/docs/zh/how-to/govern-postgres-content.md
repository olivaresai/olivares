---
title: "把 Postgres 用作受治理上下文源"
description: "将 PostgreSQL database 作为只读、受治理的知识源连接：把 row 实体化为文档，如实映射 ACL，对敏感 column 分类，并从构造上维持只读保证。"
---

`postgres` 内容 connector（`olivares.pg-content`）让 control plane 可以连接一个
PostgreSQL database，并把其中的 row 转为**受治理知识文档**。文档与其他所有内容源
经过同一条 pipeline：redact → classify → chunk → embed → index → 通过 MCP 提供，
并带有每文档 ACL 和每 column classification。

它是 SaaS/data warehouse 内容源（gdrive、confluence、s3content、snowflake…）在
运营 database 一侧的对应物。但它**不是**以下两类产品：

- **不是 `pgaudit`。** `pgaudit` 为 access map 观测 R/RW *access edge*，绝不读取
  row 内容。`pg-content` 把 *row 实体化为文档*。两者是服务于不同工作的不同
  connector。
- **不是 NL-to-SQL。** 此 connector 把 row 作为内容 ingest；它**不会**在 query 时
  从自然语言生成 SQL。（一些现有产品把 text-to-SQL 功能称为“带 structured data
  的 knowledge base”；那是 agent query surface，不是受治理内容源。本 connector
  刻意选择后者。）

## 从构造上保证只读

connector 绝不写入你的 database，并在**三个相互独立的层面**强制这一点，使写入
不可能发生，而不仅仅是不建议：

1. **只允许 SELECT query。** connector 只会*构建* `SELECT` statement。如果提供
   自己的 `query`，系统会验证它是单条只读 `SELECT`/`WITH`。第二条 statement、
   修改数据的 CTE（`WITH x AS (DELETE …)`）、`COPY`、`SELECT … INTO` 或任何 DDL
   都会在 `Open` 时以 deny-closed 方式拒绝。
2. **只读 session。** 每条 statement 都在一个 `READ ONLY` transaction 中执行；
   session 以 `default_transaction_read_only = on` 打开，因此 PostgreSQL 自身会
   拒绝写入。connector 在 `Open` 时会*验证* session 为只读；否则拒绝启动。这是
   posture 保证，不是建议。
3. **最小权限 role。** 你为 connector 提供一个只有 `SELECT`、没有其他权限的 role。
   参考 role 如下。

这比每一家 managed incumbent 的保证都更强；后者在文档中只把只读作为*建议*。

### 最小权限 role

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Never grant INSERT/UPDATE/DELETE/DDL. Optionally pin the role read-only:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

若要把 scope 收到最紧，只对计划 ingest 的 table 授予 `SELECT`。

## 定义 row 如何成为文档

文档定义是声明式的：指定哪些 column 是 key、body、title、ACL、classification 和
sync cursor：

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "support-articles",
      "kind": "postgres",
      "config": {
        "mode": "live",
        "dsn": "vault:secret/data/pg-ro#dsn",   // secret-store REFERENCE, never inline
        "schema": "public",
        "table": "kb_articles",
        "key_columns": "id",                     // the stable document id
        "body_columns": "title,body",            // concatenated into the document body
        "title_column": "title",
        "updated_at_column": "updated_at",       // drives incremental (delta) sync
        "acl_columns": "owner_group",            // → ACL "group:<value>"
        "acl_prefix": "group:",
        "classification_column": "sensitivity",
        "sensitive_columns": "email,ssn",        // → external label "pii:<column>"
        "sensitive_label": "pii",
        "metadata_columns": "url_path",
        "sslmode": "require",
        "statement_timeout": "30s",
        "max_rows": "100000"
      }
    }
  ]
}
```

可以不用 `table`，改为提供只读 `query`（经验证的 `SELECT`），例如 join 一张 ACL
table，或 filter 要暴露的 row。credential 始终必须是**secret-store 引用**
（`vault:…`、`aws-secretsmanager:…` 等）；明文 secret 会被拒绝。

## ACL 映射如何做到*如实*

connector **只映射 row 实际表达的内容**。它从声明的 `acl_columns` 值构建文档 ACL
（例如 `owner_group` column → `group:eng`）。它**不会虚构** source 没有携带的
每-row ACL，并明确说明以下限制：

| 情况 | Connector 的行为 |
|---|---|
| `owner_group` / role column | 把每个值映射到 ACL ref（`<acl_prefix><value>`）。 |
| 未声明 `acl_columns` | 文档继承知识库的**默认 ACL**，retrieval 仍会强制执行。 |
| table 上的 **row-level security（RLS）** | 隐式遵守：connector role 恰好只能看到 RLS 允许它看到的 row。connector 不重新实现 RLS，而是继承它。 |
| table **未**以 column 建模的权限 | **无法推导** → 不映射。若要强制该权限，请把它建模为 column（或通过 `query` join ACL table）。 |

这是与 managed incumbent 的刻意区别：后者要求手工编写 ACL column，*同时*不提供
RLS passthrough。这里同样需要手工映射 ACL column，**但** connector 还会遵守 RLS，
且绝不捏造 row 中没有的权限。

## 每 column classification

在 `sensitive_columns` 中列出敏感 column。当某个 row 的对应 column 有值时，文档
获得一个 external label：`"<sensitive_label>:<column>"`（例如 `pii:ssn`）。这些
label 进入 retrieval DLP，并与 row 的 `classification_column` 一起以 deny-closed
方式强制执行。

## Live 与 export

- **`mode: live`** 通过只读 pool 读取 database，并支持由 `updated_at_column` cursor
  驱动的**增量（delta）sync**；若未配置 cursor，则 fallback 到 full-list
  reconciliation。
- **`mode: export`** 解析静态 row snapshot（你在系统外生成的 JSON dump）。snapshot
  **绝不冒充 live**；source 会如实标示自己的 mode。

## 如实声明的限制

- 文档 **body 上限为 1 MiB**；更大的 row 会被截断（非常大的 column streaming 是
  后续工作）。
- 运营者提供的 `query` 中，**名称恰好是 SQL keyword 的 column**（例如 `update`）
  必须使用 alias；只读 guard 以 deny-closed 方式工作。
- connector 读取内容；**对 database 执行 action 不在 scope 内**（设计上没有写入
  path），CDC streaming 和 NL-to-SQL 也不在 scope 内。

## 实际集成证明

connector 附带一个针对真实 PostgreSQL 运行的实际集成 E2E（`-tags e2e`、CI）：
它在 `Open` 时验证只读 session，ingest 带有已映射 ACL/classification 的 seed row，
并证明 PostgreSQL 会**拒绝**只读 session 上的写入。参见
`connectors/pgcontent/testdata/docker-compose.e2e.yml`。
