> 机器翻译。英文版本为权威来源。

# ADR-0011: 许可边界——AGPL 产品、Apache SDK/连接器、商业版企业模块

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); stack license boundary

## 背景与待解决的问题

该产品需要一种许可模型，既要让产品保持真正开放，又要让第三方连接器生态摆脱 copyleft（著佐权）摩擦，同时留出一条清晰的商业路径——且不依赖功能限制（见 ADR-0010）。

## 决策驱动因素

- 一个真正开放、采用 copyleft 的产品（既非"源码可见"，也未被削弱）。
- 一个宽松许可（permissive）的连接器生态，让第三方可以自由扩展。
- 为有需要者提供一条清晰的商业例外路径。

## 考虑过的选项

- **纯双重许可：** AGPL 产品 + Apache-2.0 SDK/连接器 + 商业例外。
- **功能受限的开放核心（feature-gated open core）**（MIT/Apache 核心 + 付费功能）。
- **全部宽松许可（permissive everything）**（MIT/Apache 核心）。
- **源码可见（source-available）**（BSL、SSPL、PolyForm）。

## 决策结果

选定方案：**纯双重许可**。`core/`、`modules/`、`web/` 采用 **AGPL-3.0-only**；`sdk/` 和 `connectors/` 采用 **Apache-2.0**；`enterprise/` 为**商业版**（`LicenseRef-Olivares-Commercial`）。该边界从第一次提交起就通过逐文件的 SPDX 头部以及一项 CI 检查强制执行：一个 Apache-2.0 连接器**绝不**导入 AGPL 引擎。

### 后果

- **好处：** 产品真正开放且采用 copyleft；连接器保持宽松许可、无摩擦；边界由机制强制执行；存在一条不限制任何功能的商业路径。
- **坏处／权衡：** 贡献者必须保持 SPDX 头部正确，并遵守导入边界（CI 会捕获违规）。
- **中性：** 商业例外为自助式（self-serve），外加一个企业联系渠道。

## 为何否决了其他选项

- **功能受限的开放核心**——会限制产品（见 ADR-0010），否决。
- **全部宽松许可**——等于把核心白送出去，缺乏商业立足点。
- **源码可见（BSL/SSPL/PolyForm）**——并非 OSS；会扼杀连接器生态所依赖的采用度。

## 修订 (2026-06-23) ——该模型是开放核心

上述**许可边界并未改变，且仍然正确**：`core/`+`modules/`+`web/` 均为 AGPL-3.0-only，
`sdk/`+`connectors/` 为 Apache-2.0，`enterprise/` 为商业版，且 Apache 连接器绝不导入
AGPL 引擎。需要修正的是*表述框架*：交付的产品是**开放核心**（GitLab 的 `ee/` 模式），
**不是**没有功能差异的「纯双重许可」。AGPL 构建是完整的治理平台，绝不会为了促销升级而从内部
削弱；但它与商业版**并不相同**：`enterprise/` 系列（多 IdP 联邦、内容防火墙/DLP、hook 加固、
威胁情报 feed、服务器工具 egress、CyberArk Conjur、事件闭环）是**从未存在于开放构建中的附加
新代码**（没有 rug-pull）。因此，「考虑／选定：纯双重许可」应理解为 AGPL/Apache *边界*的
决定；开放版与商业版这一*版本*的决定是开放核心。见
`LICENSING.md`。

本 ADR 中的许可**边界**没有被取代。另行改变的是商业系列的**分发**：`enterprise/` 源码不再随
公开仓库发布，而是移至私有仓库，使 build-tag 门控成为真正的边界而不是表面机制。这是一项记录在
**ADR-0020** 中的分发决定；许可边界和仅用于证明的许可证（ADR-0010）均未改变。

## 修订 (2026-07-28) ——2026-06-23 注释中的两项失效说法

上述许可边界和开放核心框架仍然成立。2026-06-23 修订的 enterprise 列表中的两项已不再描述该
产品；但注释本身保持原文不变，因为它是对当时认知的带日期记录。

1. **「解除 community 用户上限的席位授权」已不再存在。** 决策 B10（2026-07-27）彻底移除了
   用户上限：所有版本中的自托管帐户均无限，`core/auth.CommunitySeatLimit` 为 `0`，
   `enforceSeatCapTx` 是无条件 no-op，且任何构建——开放或商业——均不会读取许可证来限制用户。
   当前决策：商业定价规范（私下维护）（`self_hosted.users: unlimited`）和
   `LICENSING.md`。
2. **「威胁情报 feed」并不描述该 add-on 可以如何销售。** `enterprise/threatintel` 提供编译进
   构建的基础目录，以及可选的、已签名且带版本的 feed 工件；运营者为这些工件固定发布者密钥并
   应用它们。Olivares 不运营精选 feed 分发，也不发布任何发行节奏。商业准则
   （商业定价规范（私下维护）、`self_hosted.business.preset`）禁止将其营销为「feed」，除非
   实际运营了已签名的 feed。运营者 CLI 保留该词来指代它验证和应用的工件
   （`olivares threatintel verify|apply|pull`）；它是工件的名称，而不是关于谁发布它的声明。

这两项均不影响本 ADR 所决定的许可边界。
