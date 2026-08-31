> 机器翻译。英文版本为权威来源。

# ADR-0022: 按 subject 轴（session / agent / user / user-group / role）限定 source，具备 row-level effect 和版本化、双人控制的 enforcement posture

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## 背景与问题陈述

source binding（`modules/sourcescope`）将连接的 source——MCP server、model、provider、knowledge
base、data source——精确绑定到三个**包含关系** scope-tree 之一：`workspace`、`agent_group` 或
`folder`（`schema.go:52-62`, `binding.go:33`）。它回答“处于此 scope **之内的** actor 可以访问该 source”。

产品愿景还需要四个包含模型难以自然表达的轴：

- **“此 SESSION 可见 source X”**——单个正在运行的 session。
- **“此 USER / user group 可访问 sources Y”**——指定的人及其 directory group。
- **“此特定 AGENT（而非其 group）仅可见 Z”**——单个 agent，而非其所属 agent-group。

目前这些轴只能通过编写 raw Cedar grant 来*近似*：没有 binding 易用性、可列举/可审计 row、
access-map projection；对于反向问题“subject S 可以访问哪些 source？”，仍有未解决的 reverse-query
问题（`accessmap.go:44`）。与此同时，**model** governance 已有丰富的 SUBJECT 模型：
`subject_kind ∈ {user, role, agent_group}`、allow/forbid row，以及 `forbid-overrides-allow`
algebra（`modelgovernance.go:98-100`, `modelaccessgate.go:204`）。治理存在不对称：**model 按 subject
得到丰富治理，而 source 仅按 containment 得到狭窄治理。** 本决策将消除这一差异。

第二层需求来自对 incumbent 的分析（已对 vendor docs 于 2026-07-07 验证）：AWS Q Business 将
*放宽* ACL 设为专用、单向、受审计的 IAM operation（`qbusiness:DisableAclOnDataSource`）；Google
data-store ACL posture **创建后不可变**。我们的差异化是 posture **可变、版本化且受审计**，但其放宽
必须是**特权、dual-control、受审计**的 operation，绝不能是 silent toggle。没有 incumbent 能表达
per-agent 或 per-session source scoping——这是已验证的空白，而非假设。

## 决策驱动因素

- **与 model-access 一致。** 使用相同的 subject vocabulary 和 `forbid-overrides-allow` algebra，
  让 operator 以理解“谁可使用 model”的同一方式理解“谁可访问 source”。
- **hot-path cost。** Resolver 在 model EXECUTE path（`ScopeGate`）和 knowledge retrieval path
  （`RetrievalScopeGate`）上运行。identity axis 不得为每次 resolve 增加 policy round-trip。
- **可审计性与 reverse-query。** “列出限定到 session S / user U / group G 的所有 source”必须是一条
  indexed query，而不是 Cedar reverse-walk（该问题未解决）。
- **UI。** console（后续工作）可呈现和编写的一种 binding shape。
- **向后兼容与 security。** 没有新 binding 的 deployment 必须与之前作出完全相同的决策；identity axis
  应尽可能绑定到 **authenticated principal** 而非 caller-declared string；control plane 绝不能获得第二个
  authorization engine，以保持较小的 attack surface。

## 决策结果

**原位丰富现有 source binding（候选 A1）：增加 subject scope-tree 和 row-level `effect`，让
`sourcescope` 在自己的 table 上采用与 model-access 对称的 subject-scoped allow/forbid algebra，同时
保持 containment model 与 cross-scope Cedar override 完全不变。** 不为新轴编写 raw Cedar（候选 B），
也不建立平行的 model_access-twin decision plane（候选 C）。一个 control plane、一个 query surface、
一个正确实施 authorization 的位置。

### 1. 与现有 containment tree 一致的五个新 subject scope-tree

`scope_tree` 从 `{workspace, agent_group, folder}` 扩展为还包含：

| tree | `scope_ref` | 匹配条件 | identity source | 可伪造？ |
|---|---|---|---|---|
| `session` | session `external_id` | acting session == ref | session-aware caller ref, agent-identity-hardened | route-gated（见 §4） |
| `agent` | agent `external_id` | acting agent == ref | `principal.AgentIdentity` ∨ session's agent ∨ agent ref | route-gated / authenticated |
| `user` | user id | `principal.UserID` == ref | **authenticated principal** | 否 |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **authenticated principal**（directory-group-gated nested closure） | 否 |
| `role` | tenant role name | `principal.RoleIn(tenant)` == ref | **authenticated principal** | 否 |

`user_group` 是 **directory group**，以 **group id** 与 `principal.GroupsIn(tenant)` 匹配；后者已经
随 authenticated principal 传递，并折叠了完整的 nested-ancestor closure
（`principal.go:67-77,151-164`），不会为每次 resolve 增加 group read。`UserGroup` 没有 slug
（`model/auth.go:122`），所以 id 是稳定标识符。为实现**完整 model-access parity** 增加 `role`
（Fran Olivares, 2026-07-07）：按 tenant role 治理 source，是 model-access 也提供的粗粒度“用户组”lever。

三个 identity-of-one 轴（`session`, `agent`, `user`）是退化 containment（相等），`user_group`
与 `role` 则是真正的 membership。它们都作为 actor 上统一的 **scope predicate** 求值，不引入新 decision engine。

**Validation 遵循 containment-vs-subject 二分法（已验证约束）。** module write handler 持有
business-tenant `store.Scope`；auth subject（`model.User`, `model.UserGroup`, role）位于
`store.AuthScope`（system tenant），从前者**不可访问**（`core/store/store.go` vs `auth.go:24-36`）。因此：

- **Containment tree** `workspace` / `agent_group` / `folder` **以及** store-resident subject tree
  仍像目前一样在 bind 时验证存在（deny-closed，“无 dangling scope”）。但为了维持统一规则并支持在
  ephemeral session 之前绑定 source，本决策将**所有五个 subject tree 在 authoring 时视为 shape-only**：
  正确 kind 的非空 `scope_ref`，不进行 store lookup。
- 正确性不依赖存在性验证：未知 subject ref 不会匹配 authenticated actor ⇒ deny-closed——**这正是
  model-access pattern**（`modelaccessgate.go` 仅验证 subject 的 *shape*；`validateGrantRefs` 只检查
  store-resident TARGET）。防止拼写错误是 console（从 directory/agent picker 编写）的职责，而非 binding layer。
  Containment tree 继续保持现有 existence validation。

### 2. Row-level `effect`（allow | forbid），采用**绝对** forbid-overrides-allow

每个 binding 携带 `effect ∈ {allow (default; empty stored value = allow), forbid}`（与 model-access
的 `normalizeEffect` 约定相同）。针对一个 `(actor, source)` 的 resolver algebra 变为：

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**行为变化，已记录（如 ADR-0019 记录自身变化）。** 目前 source binding 的 forbid 是*逐 binding*的：
一个 binding 上的 cross-scope `EffectForbid` 会被 `continue`，而*另一个* binding 仍可 allow
（`resolver.go:243-248`）。本决策让**所有** forbid（row-level `effect=forbid` 与 cross-scope
`EffectForbid`）均为**绝对**：任何匹配 forbid 都拒绝 source，覆盖 containment、cross-scope grant
**以及** tenant RBAC。这与 model-access（`modelaccessgate.go:204`）和 Cedar core（`EffectForbid`
“OVERRIDES everything”，`authorizer.go:101`）采用同一 algebra。方向严格更安全（forbid 只会拒绝），
现有 single-binding forbid test 不会 regression；变化只在此前未定义的 multi-binding case 中可见。

**Confinement trigger。** Source 仅在拥有 ≥1 个 enabled **allow** binding 时才是 *confined*。
已有 binding 全是 allow，因此与当前“bound ⇔ has bindings”完全相同。只携带 **forbid** 的 source
除该 forbid 指定的 subject 外仍为 global——model-access 的“限制某些 subject”posture 现在也可用于 source。
Connector-assignment gate 以“无 allow binding”取代“无 binding”，因此 forbid-only source 仍遵守
connector assignment。

### 3. Precedence：forbid 绝对；credential 取自最 specific 的 allow

forbid 是绝对的（§2），所以 precedence 不决定 allow-vs-deny，只在多个 allow binding 匹配时决定
获准 actor 得到哪个 **credential**。most-specific → least 的顺序为：

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

依次为 identity-of-one、directory group、RBAC role、acting agent 的 group、resource containment。
该全序让 credential selection 具备确定性（取代 `loadEnabledBindings` 的 lexical sort），并针对五个轴
细化已记录的 `session > agent > group > workspace` precedence。

### 4. 轴的可用性因 enforcement point 而异，并如实说明

resolver 有两个携带不同 actor context 的 entrypoint：

| axis | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ acting session ref | ❌ context 中无 session → 从不匹配 |
| agent | ✅ session's agent (agent-identity override) | ✅ agent ref (agent-identity override) |
| user / user_group / role | ✅ authenticated principal | ✅ authenticated principal |
| workspace / agent_group / folder | ✅ (existing) | ✅ (existing) |

knowledge base 上的 `session` binding **不会**在 agent-only retrieval path 强制执行，因为该路径没有
session；它并非被静默“允许”，只是不属于该 actor 的 scope，同一 source 上其他 binding/axis 仍然适用。
这种不对称在 contract 中明确说明。`session`/`agent` 轴保持 route-gated，受 caller 影响的 ref 通过
agent-identity check 加固（`principal.AgentIdentity` 覆盖 caller-declared ref）；`user`/`user_group`/`role`
绑定到**不可伪造的 authenticated principal**，因此是更强的轴。

### 5. Enforcement posture 可变、版本化、受审计，放宽时实行 dual-control

source 的 *posture* 是其 enabled binding 与 effect 的集合。根据 Fran Olivares（2026-07-07，
“robust without duplication”），**governance 的 `revision.go` 和 `approvals.go` 是 module-internal，
无法从 `sourcescope` 复用**（已验证：unexported helper、自有 entity、REST approval flow）。将它们 fork
进 `sourcescope` 会形成重复技术债，因此 posture control **自包含**，并复用现有唯一的共享 immutable
primitive——audit ledger：

- **通过 audit chain 审计与版本化。** 每次 posture mutation 都将 posture **delta** 记录到 append-only、
  hash-chained audit ledger（ADR-0009）：create/update/delete 使用 `sourcescope.binding.*`
  （扩展 `auditBinding` 以包含 `effect`），dual-control lifecycle 使用
  `sourcescope.posture.{propose,approve,reject}`。ledger 本身就是 immutable、sequenced version history；
  不增加专用 numbered-revision *table with rollback*（否则会重复 `governance/revision.go`）。pending/decided
  **posture-request** row 是每次*放宽*的 first-class、queryable record（谁提出、谁批准）。
- **仅放宽方向实行 dual-control，且自包含。** 可能**扩大** source 访问范围的 mutation 是 *relaxation*：
  不由 actor 直接应用，而是记录为 pending `sourcescope_posture_request`，仅当**第二个、不同的** principal
  批准后才应用（`proposer != approver` check 实现 two-person integrity）；approver 必须持有 admin-tier
  `sourcescope:posture:admin` permission（与 editor-tier proposer 职责分离）。

  > **状态修订，2026-08-07。** 下面的列举已更正。原文只列出*拓宽 allow* 与*移动 allow*，
  > **完全没有**列出针对 `forbid` 的任何 scope 操作，还把「收窄到 more-specific tree」**不按 effect 加以限定**地
  > 归入单个 actor 的普通 write。代码忠实地实现了这一点：一个仍是 enabled `forbid`、只改变所覆盖 population 的
  > 写入会当场由单个 actor 生效——而 DELETE 同一个 forbid 却需要两个人。二人制 gate 通过「编辑而非删除」即可绕过。
  > 将 classifier 反转为 whitelist 后，又暴露出同一类的另外三处泄漏：被移到「更 specific」tree 的 `allow`；
  > 被改成 `forbid` 的**最后一个** enabled `allow`；以及在**已经** confined 的 source 上创建 `allow`
  > （创建根本没有被任何东西分类）。本条开头的一般规则从未改变，
  > 正是它 authorize 了这次更正：这份列举始终比它声称要精确化的规则更窄。

  **classifier 是 WHITELIST。** 它们列举可证明不会扩大访问范围的 write，并把**其余一切——包括任何它们无法识别的
  形态——都视为 relaxation**。放宽形态的 blacklist 在构造上必然泄漏，而这一份在四处泄漏。三处是对既有 binding 的修改——收窄 scope 的
  `forbid`、被移到「更 specific」tree 的 `allow`，以及被改成 `forbid` 的**最后一个** enabled `allow`；
  第四处是创建，它根本没有被分类。前两处源于以 `allow` 的极性去理解 scope 操作。第三处源于只看了行的 EFFECT，
  却忘了同一行还承担着 CONFINEMENT：source 只有在拥有 enabled `allow` 时才是 confined，
  因此那个读起来像「这一行从此只能拒绝」的写入，正是让 source 变成全局的写入。

  **`forbid` 会反转任何 scope 操作的极性，这正是陷阱所在。** 对 `allow` 而言，scope 越小触及的 actor 越少，
  是 tightening；对 `forbid` 而言，scope 越小**保护**的 actor 越少：所有不再被覆盖者，都被这一次写入解除了拒绝。

  **两个 scope 只有在是同一个 scope 时才可比较。** `specificityRank`（`resolver.go`）
  **是为在匹配的 allow binding 中挑选 CREDENTIAL 而对 tree 排序的**，**它不是包含关系**，绝不可当作包含关系使用。
  `role:admin` 与 `user_group:g1`、`workspace:eng` 与 `agent_group:core`、一个 folder 与它的子节点，都是不同的
  POPULATION，彼此互不包含——而 folder binding 根本没有包含维度（它依托 cross-scope Cedar grant）。成员关系也并非固定：
  今天读取 row 证明出来的 superset，明天就不再是 superset。因此「这次写入不可能扩大访问范围」的凭证是
  **scope 的同一性，不能更弱**；而「我无法比较这两个 scope」一律归结为 *relaxation*——false positive 只多花一次批准，
  false negative 却是绕过二人制 gate。

  **Relaxation** 的精确定义（`classifyCreate`/`classifyUpdate`/`classifyDelete`）：delete/disable enabled
  **forbid**；`forbid→allow`；**对 enabled forbid 的任何 scope 变更**（解除其部分 population 的拒绝）；
  **启用** allow；disable/delete **最后一个** enabled allow（使 source unconfined → global）；
  **对 enabled allow 的任何 scope 变更**——无论变宽、变窄还是平移；**在已经 confined 的 source 上创建 allow**
  （为原本触及不到该 source 的 population 增加 grant）；以及专用单向 operation
  **`POST /sources/disable-scoping`**（对应 AWS `qbusiness:DisableAclOnDataSource`）。

  **Tightening / neutral** mutation 是普通 single-actor write——受审计但不 gated：添加 **forbid**；
  `allow→forbid`；在尚未 confined 的 source 上创建**第一个** enabled allow（它把 source 纳入治理——本模块中最大的
  tightening，刻意不设 gate，以免安全的操作反而代价最高）；创建 **disabled** 的 row；启用被停用的 **forbid**；
  delete/disable **非**最后一个 allow；以及不触及 effect、enabled 与 scope 的 note/credential 编辑
  （credential locator 只决定已获授权的 actor 拿到哪一份引用，从不决定它是否获授权）。前后都是 disabled 的 row
  不 enforce 任何东西，因此对它的任何写入都是 neutral。

  这种不对称与 AWS（放宽是 privileged op）一致，并优于 Google 不可变 posture：我们的 posture 可变且 governed。
  Endpoint：放宽的 create/update/delete 通过现有 `POST /bindings` 与 `PUT`/`DELETE /bindings/{id}` 提出
  （返回带 pending request 的 `202`）；
  `POST /posture-requests/{id}/{approve,reject}` 作出决定；`GET /posture-requests` 是 reviewer queue。

### 6. Access-map 投影新的发起方（ADR-0003）

`publishBindingEdges` 投影 RRW map 的 permitted side。`EdgeObservation` 已支持
`OriginKind ∈ {agent, session, identity}`（`sdk/model/observation.go:55`），所以三个 identity-of-one
轴各投影一条 edge：`session` binding → 属于**该** session 的 `session` 发起方 edge；`agent` →
`agent` 发起方 edge；`user` → `identity` 发起方 edge。GROUP subject 轴（`user_group`, `role`）需要枚举 MEMBER
才能投影，但 member 是 module 的 tenant `store.Scope` 无法访问的 auth-scope entity（directory group、user）。
因此与 folder binding 的 reverse-grant projection 一样，它们 **DEFER**：记录日志且不投影。Forbid binding
不投影任何内容（forbid 不是 permitted edge）。Enforcement 始终是 resolver 针对 live principal 的 live decision；
map 是 best-effort drift observability，deferred/absent edge 从不削弱 enforcement。

## 后果

- **好处：** 四个 vision axis（含 `role` 则五个）可表达，在两个真实 PEP 上以 deny-closed 强制执行，
  并在 scope resolution 与 access-map 中可见；console 使用一种可审计/可列举 binding shape；identity axis
  绑定 authenticated principal（不可伪造）；无第二 authorization engine（attack surface 小）；hot path 只付出
  一次廉价 membership check，identity axis 的新 policy round-trip 为 **zero**；mutable-yet-governed posture
  是相对 AWS（one-way）与 Google（immutable）的已验证差异化。
- **坏处 / 权衡：** `scope_tree` 同时承担“containment scope”和“subject identity”语义（缓解：contract
  将两者描述为统一 *scope predicate*）；posture/dual-control machinery 增加了 minimal deployment 在创建
  relaxation 前不会使用的真实 surface；forbid 绝对化是已记录的安全方向行为变化。
- **中性：** `role` 在概念上与 tenant-RBAC soft-isolation bypass（`rbacAllows`）重叠，但二者可 compose
  （`role` binding 是 positive scope，RBAC bypass 是 tenant-operator visibility rule），forbid 覆盖**二者**。

## 为何否决了其他选项

- **(B) 为新轴生成 Cedar policy 的 high-level API。** 否决：(1) 它将成为*唯一*编写 raw Cedar 的 plane，
  而一致性目标 model-access 并不生成 Cedar，而是在自己的 row 上决策（`modelaccessgate.go:11-14`）；
  (2) hot path 每次 resolve 都需 Cedar round-trip；(3) console 所需的反向问题仍是未解决的 Cedar reverse-query，
  UI/access-map 将被阻塞或近似；(4) 审计“谁限定了什么”需读取 policy text 而非 row。
- **(C) 单独设置 model_access-twin table 存放 source-subject grant，再与 containment binding compose。**
  作为会*降低* robustness 的 over-engineering 否决：两个 decision plane 必须在每个 PEP compose 并保持一致，
  这是 security drift（一个更新另一个未更新、跨 plane precedence 模糊）的经典来源。“最完整/enterprise”应通过
  **一个 plane 的深度**（所有 axis + effect + versioned dual-controlled posture + 完整 test matrix）实现，而不是
  重复 plumbing。具有统一 algebra 的单一 control plane 更易审计（“治理 source X 的一切”= one query）和证明正确。
- **扩展 custom-role scopeSpec vocabulary 而非 local enum。** 否决：`sourcescope` 的 `scope_tree` 是仅
  *镜像* custom-role catalog 的 module-local constant（`schema.go:49`）；扩展 shared catalog 会让 source axis
  泄漏到 custom role 可 target 的范围。新 tree 保持 `sourcescope` local。
