> 机器翻译。英文版本为权威来源。

# ADR-0028: 托管云数据库——托管 PostgreSQL，以行级安全作为租户边界

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## 背景与问题陈述

ADR-0005 已经规定产品在规模化场景下使用 PostgreSQL，产品也已经具备用于 tenant scoping 的
row-level-security machinery。托管云不需要新的 data model；它需要决定**由谁运营数据库**，以及
**究竟依靠什么来阻止一个 tenant 的 row 被另一个 tenant 访问**。

后一个问题比前一个更重要。“我们使用 row-level security”只有在 role 的安排能让 policy 确实生效时，
才算是一项实际属性。PostgreSQL 会将两类 caller 排除在 table policy 之外：superuser 和带有
`BYPASSRLS` 属性的 role——而且在默认情况下，**table owner 完全不受该 table policy 的约束**，除非用
`FORCE ROW LEVEL SECURITY` 修改该 table。因此，如果 application 使用创建 schema 的 role 进行连接，
它实际上将**不存在任何** tenant isolation，却表面上看似具备隔离。这是本设计中代价最高的单一错误，
而且不会发出任何信号。

## 决策驱动因素

- Tenant isolation 必须**由数据库**强制执行，而不是依赖每一条未来 query 的严谨程度。
- 这位唯一的 operator 不应负责运营 PostgreSQL：patching、failover 与 point-in-time recovery 正是
  managed offering 要消除的工作。
- Recovery 必须是 platform 的一项属性，而不是依赖某人记得执行的 runbook。
- 对 isolation 作出的任何声明都必须能**从 application 外部测试**。

## 考虑过的选项

- **A——在 virtual machine 上自行管理 PostgreSQL。** control 完整、unit cost 最低，但每次 upgrade、
  failover drill 和 backup verification 都成为我们的责任。
- **B——cloud provider 的托管 PostgreSQL service，multi-AZ**，带 automated backup 和
  point-in-time recovery。
- **C——provider 的 PostgreSQL-compatible cluster service**（shared-storage architecture，标准
  configuration 采用 per-request I/O billing）。
- **D——第三方 PostgreSQL platform**，可从同一区域访问。

## 决策结果

选定方案：**B——托管 PostgreSQL，multi-AZ**，以 row-level security 作为 tenant boundary；下述 role
layout 是本决策的一部分，而非 implementation detail。

role layout 是规范性要求：

1. Application 使用一个**不拥有** tenant-scoped table 且**不具有 `BYPASSRLS`** 的 role 进行连接。
2. 每一个 tenant-scoped table 都带有 **`FORCE ROW LEVEL SECURITY`**，因此仅凭 ownership 不能绕过
   policy——这可以防范未来某次 migration 改变 table owner。
3. 用于 migration 的 administrative role，不得是 application connection string 中使用的 role。
4. **明确范围，以免任何人想当然：** 此记录治理的是 **tenant data plane**——即保存 tenant-owned
   row 的 schema；engine 已在其中生成 `ENABLE ROW LEVEL SECURITY`、`FORCE ROW LEVEL SECURITY`
   以及绑定到 session setting 的 per-tenant policy。managed plane **自身的 control metadata**（tenant
   registry、billing ledger、usage snapshot）位于一个**采用独立 posture 的独立 schema**中：目前它依赖
   application-level scoping，只使用单一 application role，且没有面向 tenant 的 SQL。这对 control
   metadata 而言很可能是正确答案——但目前这一 posture 是**继承而来，并非经过决策**，也并非读者
   从“我们使用 row-level security”这句话中所理解的含义。构建 managed plane 的人必须在该 schema 保存
   付费客户的记录之前，**以书面形式说明该 schema 采用何种 posture 及其原因**。

### 后果

- **好处：** patching、multi-AZ failover、automated backup 与 point-in-time recovery 成为 platform
  property。产品随附的 disaster-recovery runbook 仍用于 self-hosted deployment；它不再是 managed
  plane 的日常 operational duty。
- **好处：** isolation 变得可以从外部测试。验收标准是**以 application role 身份**运行一条试图读取
  另一 tenant row 的 query，并且得不到任何 row——而不是 design document 中的一句断言。
- **坏处 / 权衡：** 固定月度成本下限高于普通 virtual machine，而且 engine-version upgrade 按照
  provider 的日程到来，而不是由我们决定。
- **中性：** managed service 的 administrative role 是 privileged database role，**不是**
  PostgreSQL superuser——它没有 operating-system access，也不能重写 host authentication
  configuration。这确实缩小了 blast radius，但让 row-level security 成立的并非这一点，而是上述
  role layout。
- **明确未验证，且不得假定：** 该 administrative role 在实际运行的 engine 上是否带有 `BYPASSRLS`。
  这需要对一个真实 instance 执行一条 query
  （`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`）来检查，并应在首次创建 instance 的
  phase 完成。在执行这项检查之前，任何 document 都不得声称 administrative role 受 tenant policy 约束。

## 为何否决了其他选项

- **A（自行管理 PostgreSQL）**——被否决，因为它把 managed plane 本应吸收的 operational load 原样交还，
  并集中到一位 operator 身上：version upgrade、failover rehearsal，以及只有定期实际 restore 才能证明
  有效的 backup verification。它的成本优势确实存在，绝对金额也不大；但 operational exposure 并非如此。
- **C（PostgreSQL-compatible cluster service）**——因时机过早而被否决。该 workload 是一个 write
  rate 适中的小型 transactional schema；shared-storage architecture 解决的是该 workload 并不存在的
  scaling problem，却有更高的成本下限，而且标准 configuration 采用 per-request I/O billing。如果
  write rate 有朝一日足以证明其合理性，它仍是自然的 upgrade path。
- **D（第三方 PostgreSQL platform）**——作为 primary store 被否决。row-level security behaviour、
  superuser model 与可用的 role attribute 会因 vendor 而异，必须分别针对上述 isolation property 重新
  验证。对于绝不能失效的这一条 boundary，没有理由承担 vendor-specific risk。
