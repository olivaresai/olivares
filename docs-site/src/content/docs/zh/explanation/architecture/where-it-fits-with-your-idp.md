---
title: Olivares AI 在你的 IdP 体系中处于何处
description: >-
  Olivares AI 不是身份提供方（IdP）。它以只读方式联邦化（federate）来自你已在运行的注册表的
  agent 身份——Entra Agent ID、AWS AgentCore Identity、Google Agent
  Identity——并用它来为访问图谱归因。它如何与你的 IdP、SSO/SCIM、SPIFFE/WIF
  以及 ID-JAG / XAA 标准协同。
sidebar:
  order: 3
---

安全架构师常问的第一个问题是：*“这是不是又一个我必须运行的身份系统？”*  不是。**Olivares AI 不是身份提供方，也不拥有身份。** 它**消费**你已经签发的身份——对人类，来自你经由 SSO/SCIM 的 IdP；对 agent，来自超大规模云厂商已正式发布（GA）的 agent 身份注册表——并用它们来为[访问图谱](/zh/explanation/)中的每一条边归因*谁或什么*位于其后。本说明确切阐明这道接缝（seam）所处之处。

## 分层

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **人类**通过**你的** IdP 认证。Olivares AI 为运维账户与组到角色的映射集成标准的 **SSO 与 SCIM**；它不存储凭据，也不会成为第二个目录。
  → [SSO 与 SCIM 身份](/zh/how-to/connectors/sso-scim-identity/)
- **Agent** 从你已采纳的注册表获取其身份。Olivares AI 以**只读**方式将这些名册（roster）联邦化到一个内部 **SPIFFE/WIF** 名册上，使每一次被观测到的访问都能被绑定到一个受治理、有名字的身份，而非一个匿名进程。

## agent 身份联邦实际做了什么

控制平面为各 GA 的 agent 身份注册表提供只读名册连接器，每个都对照其主要来源进行验证，且**默认拒绝（deny-closed）**（无凭据 → 空名册，绝不出现幻影错误）：

- **Microsoft Entra Agent ID**——经由 Microsoft Graph 导入 agent 身份、蓝图（blueprint）以及所有者/赞助者关系；显露注册表所声称的孤儿项。携带长期口令凭据的蓝图会触发一条**长期凭据漂移**发现项。
- **AWS AgentCore Identity**——导入 agent 名册；带有服务身份的 agent 映射到一种服务账户身份类别。
- **Google Agent Identity**——导入推理引擎（reasoning-engine）身份；其引用是一个完整的 **SPIFFE ID**，因此它通过外部 id 与 SPIFFE 名册收敛。

这些映射馈入[访问图谱的归因](/zh/reference/glossary/#attribution归因置信度)轴（`firm` / `approximate` / `unknown`）——它们并不重新实现归因。联邦严格为只读：Olivares AI **从不**变更某个远程注册表。所有权与孤儿信号被转发到非人类身份（NHI）生命周期，使一个注册表所声称的孤儿项经由既有的治理机制显现出来。

:::note[实验性与朝向设计，并如实标注]
跨生态系统的描述符（**OASF**）与 **AGNTCY Agent Badges** 在满足可验证凭据合规性之前，均被视为**实验性**。仍处于预览（preview）阶段的名册（例如 Google 的 Gemini Enterprise Agent Platform）被作为**接缝**接入，而非宣称已上线。我们标明哪些是 GA、哪些是预览、哪些是朝向设计（design-toward）——我们不会将它们混为一谈。
:::

## ID-JAG、XAA 与基于 SPIFFE 的客户端认证

面向*委派的、可归因的* agent 访问的企业标准正在收敛，而控制平面被构建为顺应它们，而非发明自己的：

- **ID-JAG**（Identity Assertion JWT Authorization Grant）与 **XAA**（Cross-App Access）是一种正在兴起的模式，用于让一个 IdP 为跨应用行动的 agent 签发**有范围限定、可归因的**授权——即 MCP 授权工作中由企业管理的授权扩展。随着它们落地，可归因的令牌就成为访问图谱可以绑定到受治理身份的又一个高保真信号。
- **基于 SPIFFE 的 OAuth 客户端认证**（`draft-ietf-oauth-spiffe-client-auth`）使得控制平面自身的 OAuth 流程能够在某个授权服务器一旦发布支持时，便用一个 **SVID** 进行认证——基于既有的默认拒绝（deny-by-default）mTLS。在该草案与服务器支持稳定下来之前，这处于**朝向设计**状态，不作任何合规性声明。
- **默认短期有效。** 在领地中发现的长期静态凭据被标记为一类漂移，这符合**五眼联盟（Five Eyes）**（2026）关于 agent 凭据应当短期有效的指引。

## 这对你意味着什么

- 你保留你的 IdP、你的 SSO、你的 SCIM，以及你所标准化采用的任何 agent 身份注册表。没有任何东西需要迁移。
- Olivares AI 成为那个让**所有**这些身份与你领地的**被观测行为**相遇的地方——唯一一个能够说出“这个 agent，来自这个注册表，由这个人类拥有，正在使用策略从未授予的访问”的层。
- 由于联邦是只读且自托管的，这种关联不要求统一的数据传输。产品没有强制遥测，控制平面默认也
  不产生出站流量。只有你明确配置为跨越边界的内容才会跨越你的边界——对你的模型 API 的调用、
  你接入的 SIEM/webhook 输出，以及你配置时使用的外部嵌入提供商。

## 相关

- [Agent / 身份 / NHI](/zh/reference/glossary/#identity--nhi)——术语表中的定义。
- [对比 AI 控制塔（control tower）](/zh/explanation/positioning/vs-control-towers/)——与生态系统管理面的双向集成。
