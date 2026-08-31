> 机器翻译。英文版本为权威来源。

# ADR-0016: 外部连接器生态 —— 公开 SDK、签名准入、releases/OCI 分发、精心整理的已验证索引

- **Status:** accepted
- **Date:** 2026-06-11
- **Deciders:** Fran Olivares（v1 范围于 2026-06-09 决定）
- **References:** `LICENSING.md`（许可边界）、ADR-0007（go-plugin 运行时）、
  ADR-0011（AGPL/Apache/商业）、ADR-0015（供应链）、
  `docs/contracts/S02-sdk-runtime-eventbus.md`、
  `docs/contracts/S142-external-connector-sdk.md`

## 背景与问题陈述

连接器 SDK（`sdk`、`sdk/plugin`）从第一天起就被设计为使得连接器永不链接 AGPL 引擎
（Apache-2.0，零依赖，gRPC 插件传输 —— ADR-0007、ADR-0011），并且 ADR-0007
明确预见了"第三方可以独立交付连接器"。但当时并不存在这样的机制：SDK 模块没有打标签，
仅通过 monorepo 工作区被消费；composition root 只启动**内嵌的第一方** 插件二进制文件
（`go:embed`）；`LoadSourcePlugin` 会执行交给它的任意路径，且**不做任何完整性或来源检查**；
而 module-XIV 目录只整理内部条目。"我的团队或合作伙伴能否构建并发布一个连接器？"这个问题没有答案。

开放生态不能意味着"宿主加载操作者所指向的任何 `.so` 风格二进制文件"：这是一个安全产品；
将一个未签名、未证明的可执行文件接入观测平面会成为一个供应链漏洞。

## 决策驱动因素

- 广度护城河（amplitude moat）只有在第三方能够安全地贡献连接器时才能**组合**成形
  （`ARCHITECTURE.md`、`LICENSING.md`）。
- 许可边界（连接器 = Apache，绝不导入 `/core`）必须**由第三方**可验证，而不仅在我们的 CI 中可验证。
- 签名 + 准入机制已经存在并经过验证（模型准入、MCP 条目准入、`core/secure/modelsign`）：
  应复用，绝不重新实现。
- v1 中不提供托管市场基础设施（商业决策延后）。

## 考虑过的方案

- **方案 A —— 托管市场服务**：由 Olivares.AI 运营的、带有上传 / 审核 / 服务的注册中心服务。
- **方案 B —— SDK + 认证 + 签名，通过 GitHub releases/OCI 分发，文档站中精心整理的静态"已验证连接器"
  索引；在宿主处采用默认拒绝（deny-closed）的签名准入。**
- **方案 C —— 开放式插件加载**（操作者提供路径，无签名），认证仅作为文档存在。

## 决策结果

所选方案：**方案 B**（于 2026-06-09 决定）。

1. **公开 SDK 契约。** `sdk` 与 `sdk/plugin` 被声明为面向连接器作者的**稳定 v1**，并带有明确的
   版本控制 / 弃用策略（`sdk/VERSIONING.md`，在文档站的稳定性页面中呈现）。Semver 标签
   （`sdk/v1.*`、`sdk/plugin/v1.*`）将随仓库的首次公开发布一同落地；在那之前，作者钉定某个 commit
   （脚手架的 `-sdk-path` 覆盖了开发循环）。
2. **脚手架 + 指南。** 一个零依赖的生成器（`sdk/scaffold`，CLI `olivares-connector-new`）会生成一个
   完整的、树外（out-of-tree）的连接器仓库 —— 契约正确的源码 / 输出骨架、生命周期测试、插件 `main`、
   README，以及一份**独立的边界检查**（与 `scripts/check-boundary.sh` 在我们 CI 中强制执行的
   `go list -deps` 规则相同，因此第三方可以在*他们的* CI 中验证 AGPL/Apache 边界）。
3. **分发渠道。** 一个已发布的连接器以 **GitHub release 制品**（二进制 + `sha256` + Sigstore 证明捆绑包）
   和 / 或 **OCI 制品**（ORAS，证明作为 referrer）的形式交付。v1 中不提供托管市场。
4. **签名准入，宿主处默认拒绝。** 一个外部插件仅在以下条件全部满足时才运行：操作者的 sources 配置
   钉定了它的摘要，并且一份覆盖该摘要的 Sigstore/DSSE 供应链证明（SLSA 来源 / SBOM predicate）
   能针对操作者配置的信任策略（`connector_trust`）通过验证，复用 `modelsign.VerifyAttestation`。
   加载器还会在执行时额外钉定校验和（go-plugin `SecureConfig`）。**对于外部二进制文件不存在观测模式
   （observe mode），也不存在允许未签名的逃生通道** —— 开发循环即"用你自己的密钥签名，信任你自己的公钥"
   （bare-key 模式）。
5. **认证记录（目录叠加层）。** Module XIV 新增一个 `connector` 条目类型，带有其自己的准入对
   （`catalog.connector_admission_policy` / `catalog.connector_admission`）：每个条目的已验证来源 / SBOM
   裁决、默认拒绝的批准门、默认观测模式 —— 面向租户的认证轨迹，与宿主执行门解耦
   （纵深防御，类似于模型准入的 admit-route + deployment-gate 对）。
6. **已验证连接器索引。** 文档站中一个**精心整理的静态页面**（`reference/verified-connectors`）列出
   其发布版本已被维护者重新验证（边界、签名、来源、最小化数据审查）的第三方连接器。列入由 pull request
   发起；该索引是已执行验证的文档，**而非**信任根 —— 操作者仍需在 `connector_trust` 中钉定发布者的
   身份 / 密钥。

### 后果

- **优点：** 第三方在不触碰 AGPL 引擎的情况下构建、签名并交付连接器；宿主永不执行未经证明的代码；
  认证复用了经过验证的机制；无需运营任何新服务。
- **缺点 / 权衡：** 除文档 + releases 外没有发现 / 安装的使用体验（托管市场则会提供一种）；
  操作者需手动管理信任锚；外部**输出**连接器以相同方式构建并交付，但宿主侧的外部装配优先覆盖观测源
  （notify composition 目前还没有外部插件路径）。
- **中性 / 后续事项：** 由宿主进行 OCI *拉取*（如今操作者将二进制文件放到磁盘上；摘要钉定使传输方式
  与信任无关）；进程外（out-of-process）模块仍未装配；一个从连接器准入探测的合规能力；
  npm scope `@olivaresai` 以及在公开导出时的 module-proxy 标签。

## 为何拒绝其他方案

- **方案 A** —— 运营一个市场是一项被明确延后的商业承诺；它会增加一个对信任至关重要的服务，
  而 v1 中并无此需求。
- **方案 C** —— "加载任意二进制文件"恰恰是本产品旨在堵上的供应链漏洞；没有强制执行的、
  仅作文字的认证将是为审计而做的形式化表演（`docs/SECURITY-HARDENING.md`）。
