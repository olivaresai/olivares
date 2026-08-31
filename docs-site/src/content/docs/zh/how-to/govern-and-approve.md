---
title: "治理与审批（human-in-the-loop）"
description: "运营者如何治理 estate：身份与权限、deny-by-default 的 RBAC 模型、仅限收紧（restrict-only）的策略接缝，以及决策被记入审计账本的 human-in-the-loop 态势。"
---

本页面向已连接至少一个数据源、现在需要**治理** estate 的运营者：
决定谁与何物可以行动、审阅平台所呈现的内容并据此采取行动。治理位于
**模块 VI（身份、权限、治理）**中，建立在与 API 其余部分相同的授权核心之上，
并且**受完整审计**。

:::caution[诚实地界定范围：审批引擎已构建，运营者控制台仍在成熟中]
今天运行的是**授权核心**——deny-by-default 的 RBAC、一个仅限收紧的策略接缝、
租户范围的访问，以及一个仅追加、已签名、记录每次治理决策和每次特权读取的审计
账本——**外加一个可用的 human-in-the-loop 审批引擎**：受治理的审批请求绑定到
一个 plan hash，以 deny-closed 方式开启并设有时限，
其**职责分离（separation-of-duty）、重复决策者与过期都在服务器端强制**，
审批/拒绝端点位于治理模块的命名空间之下。**仍在成熟中**的是更丰富的
**运营者审阅界面**——一个完整的审批队列控制台和结构化审阅 UI。
本页描述模型、上线的端点以及决策被记录的保证；凡运营者 UI 仍处于设计阶段之处，
均会如实说明。
:::

## 你在其中进行治理的授权模型

每一项治理决策都由保护 control plane 其余部分的同一个授权核心做出。
在更改任何东西之前，先理解它的三个特性。

### RBAC 是 deny-by-default 的

授权**先跑 RBAC**。在某租户中没有任何成员资格的 principal 会被**拒绝**——
不存在隐式授予。权限按租户限定范围，处理器只对**请求解析到的那个单一租户**
行动，绝不对它自己重新推导出的租户行动，这从构造上消除了 confused-deputy
和 IDOR 这两类问题。

内置角色构成一个能力递增的阶梯：

| 角色 | 它能做什么 |
|---|---|
| `viewer` | 读取运营数据和审计轨迹 |
| `editor` | 以上，外加写入运营数据 |
| `admin` | 以上，外加租户 IAM——用户、成员资格、token、设置 |
| `owner` | 租户内的全部权限 |

一个模块声明它自己带命名空间的权限（`<namespace>:<resource>:<verb>`），
而角色按**动词层级**被授予这些权限（viewer 映射到 read、editor 到 write、
admin 和 owner 到 admin）。因此一个新模块在不发布引擎版本的情况下
即可引入治理界面。

:::note[查看 access graph 是一项特权操作——这是有意设计]
模块 III 的 R/RW access map 是产品中最敏感的单一资产：一张关于每个 agent
能触及什么的地图，对攻击者而言就是一份侦察路线图。所以**读取 access graph
是一项特权操作**，从 **editor 角色及以上**授予——**绝不**给最低的 viewer。
它是**租户范围的**（一次读取只能看到一个租户的图），并且**每次读取都被写入
审计账本**——谁查看了谁的访问、以及何时。特权、租户范围限定和自审计被有意地
层叠在一起；参见[安全模型](/zh/explanation/security/security-model/)。
:::

### 策略接缝（ABAC/PDP）只能收紧

在 RBAC 之上，运营者可以接入一个外部 **policy decision point（PDP）**
用于基于属性的规则。你用单个环境变量选择引擎：

```bash
# Choose one. Cedar is the embedded, pure-Go primary; OPA is an over-HTTP adapter.
OLIVARES_PDP_ENGINE=cedar   # or: opa | none
```

两个引擎都位于同一个接缝之后，而该接缝有一条决定你必须如何对其推理的不变式：

:::tip[PDP 只能收回访问，绝不能增加]
策略接缝以 **RBAC ∩ native ABAC ∩ external PDP** 的方式取交集组合。一个 PDP
**只收紧；它绝不放宽** RBAC 已经允许的东西。你不能用 Cedar 或 OPA 策略去*授予*
角色模型所拒绝的访问——只能去拒绝角色模型本会允许的访问。这是强制执行的，
而非一项约定。
:::

两个适配器以不同方式保持该不变式，你也据此编写策略：

- **Cedar（嵌入式、主选、pure-Go）。** 你编写 `forbid` 规则。一条匹配的规则
  即为一项收紧；空规则集意味着 RBAC 的决策成立。Cedar 中的 `permit`
  绝不能放宽决策。
- **OPA（经 HTTP）。** 你的 Rego 必须是 **permit-by-default**
  （`default allow := true`，用 `allow := false` 子句表达你的拒绝）。
  `true` 结果意味着不收紧；`false`、缺失结果，或任何传输错误或非 2xx 错误
  **均 fail closed**——请求被拒绝。

一个**无效的 PDP 配置只会禁用外部 PDP** 并记录此事实——native ABAC 和 RBAC
继续治理。一个配置错误的策略引擎绝不会让请求处于无治理状态，也绝不会让
control plane 宕机。**PDP 施加的每一项收紧都被审计。**

## 界面告诉你应据以行动的内容

human-in-the-loop 治理由平台所观测并呈现的内容驱动。两条流告诉运营者
什么值得做出一项决策：

| 流 | 模块 | 它呈现什么 |
|---|---|---|
| **least-privilege drift** | III（access map） | **permitted-vs-observed** 差异——一项被授予的能力以无人意图的方式被使用，或一条可达但从未被行使的路径 |
| **发现项** | IX（安全、guardrails、取证） | guardrail 和红队发现项，外加平台路由的通知流 |

模块 III，即 access map，是**读优先（read-first）**的——它通过日志、
OpenTelemetry 以及（作为非协作式内核兜底的）eBPF 进行观测，
并且**绝不在 agent 的数据路径上**，因此一次 collector 故障不会破坏生产。
它还是**最小数据（minimal-data）**的：它存储 `agent → resource (read/write)`
这一关系，绝不存储载荷、密钥或 PII。它所承载的信号对自身的置信度
（`attributed` 与 `approximate`）和自身的覆盖范围是诚实的。

:::caution[覆盖是分层的——drift 并非一致地完整]
access map 的保真度取决于资源。覆盖是**分层的**：对 SQL 数据库、对象存储和
数据仓库是*干净的*（原生审计原样区分读与写）；对文档数据库和向量数据库
这类存储是*有损的*；而对内存型和嵌入式存储则**无法被动观测**。请据此治理：
在覆盖有损或缺失之处，没有观测到访问并不能证明没有访问。
请阅读[威胁模型](/zh/explanation/security/threat-model/)，了解每一层能与不能证明什么。
:::

有一类信号需要明确的治理判断。MCP 工具注解
（`readOnlyHint` / `destructiveHint`）是有用的读/写提示，但按 MCP 规范
**不可信**——客户端必须将其视为不可信。平台会用可信信号去**佐证**它们，
绝不单独信任它们；当你对一个仅依赖某条注解的 drift 项行动时，你也应如此。

## human-in-the-loop 态势

设想中的治理闭环是：**界面呈现**（来自模块 III 的 drift、来自模块 IX 的
发现项）→ **一位经授权的运营者做出决策** → **该决策被记入审计账本**。

该闭环的三个部分今天都在运行。**界面是真实的**——模块 III 产出
permitted-vs-observed 差异，模块 IX 产出发现项。**审批引擎是真实的**——
一个受治理的审批请求针对治理模块开启（deny-closed、绑定 plan hash、设时限）；
一位经授权的运营者通过决策端点审批或驳回，且**职责分离、重复决策者与过期
都在服务器端强制**，因此请求者绝不能决策自己的请求，过期的请求也绝不能生效。
而**记录是真实且强健的**——参见下文的保证。**仍处于设计阶段**的是完整建成的
**运营者审阅控制台**——一个丰富的审批队列 UI；端点和引擎已交付，
打磨后的审阅界面是模块 VI 的前进路径。

使这个闭环可信的依赖是 **per-agent identity**。平台的审计将活动归因于一个
凭据或角色，而非天然地归因于某个 agent；一个带连接池的共享服务账户会让归因
坍塌。因此良好的治理意味着**为每个 agent 签发并强制其身份**——这是从观测
（模块 III）到治理（模块 VI）的桥梁。其身份一侧围绕不透明、可撤销的第一方
凭据和一份非人类身份（non-human identities）名册构建；产品中**唯一的凭据签发
原语**是 opt-in 的、经证明的、被审计的，且绝不持久化所签发的 token。
关于身份、权限与治理如何跨 estate 组合，参见
[模块目录](/zh/reference/modules/overview/)。

:::tip[决策被记录的保证]
无论其上方工作流的深度如何，**一项治理决策都是一项被记录的事实**。
变更性操作以**真实行动者**在与变更**同一事务**中被追加到审计账本，
而敏感读取（access graph、账本本身）在一次已提交的写入中自审计。
账本是**仅追加、hash 链式、并由 Ed25519 签名保护**的——每条记录携带
`seq`、`prev_hash`、`hash` 和 `sig`，因此改写历史可被加密学地检测到，
并且**它绝不包含 PII**。你无法做出一项让账本悄无声息忘记的无治理变更。
:::

### 开箱即用地获取记录

为了获得一份外部的、不可变的副本——企业审计师会索取而原生遥测无法提供的东西——
账本以**经认证的拉取导出**形式暴露：

```bash
# Pull the signed, hash-chained ledger for offline re-verification.
# Requires a token whose role can read the audit trail (viewer and up).
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

受支持的 `format` 值为 `cef`、`leef`、`syslog`、`otlp`、`otlp_envelope`、`otlp_log_record` 和 `ocsf`。
其中 `otlp` 输出完整的、可直接 POST 的导出请求，`otlp_envelope` 是它的精确别名，
`otlp_log_record` 则是每行一条 LogRecord 的裸投影。每条记录都
携带链完整性字段，因此你的 SIEM 或 WORM 存储可以**离线再验证该链**。
分离式签名（detached signature）防御仅限数据库的失陷（注入、被窃的备份或副本、
一个绕过 RLS 的角色）以及检查点删除；一份**离机副本**才是对主机被完全失陷的控制。
关于完整的文件 tail 流水线，参见
[转发审计到 Splunk](/zh/how-to/forward-audit-to-splunk/)。

这些决策据以行动的 least-privilege drift 就是 access map 的
permitted-vs-observed 结果。[zero-to-graph 教程](/zh/tutorials/zero-to-graph/)
在演示 estate 上具体演练如何抵达它；access map 模块界面与其他一切一样，
受同样的 deny-by-default RBAC、租户范围限定和逐次读取审计的约束，
这也是读取它属于 editor 及以上操作的原因。

## 接下来去哪里

- [安全模型](/zh/explanation/security/security-model/)——特权、租户范围限定、
  自审计，以及最小数据态势的完整说明。
- [威胁模型](/zh/explanation/security/threat-model/)——资产、信任边界，
  以及每一覆盖层能证明什么。
- [模块目录](/zh/reference/modules/overview/)——身份、权限与治理（模块 VI）
  如何与 access map（模块 III）和发现项（模块 IX）组合。
- [连接一个数据源](/zh/how-to/connect-a-source/)——接好 drift 和发现项据以构建的信号。
