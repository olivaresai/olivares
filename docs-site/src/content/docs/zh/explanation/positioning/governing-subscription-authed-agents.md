---
title: 治理以订阅认证的 Claude Code 与 Codex
description: >-
  Olivares AI 如何治理那些以订阅方式认证的编码智能体 ——
  使用 Pro/Max 的 Claude Code、使用 ChatGPT 的 Codex —— 而绝不置身于
  该订阅的中间环节。三种机制（观察、managed-settings + hooks、一条
  API-key 网关），一条红线：我们绝不路由你的订阅凭据。
sidebar:
  order: 6
---

最难治理的智能体，是开发者用个人或公司**订阅**登录的那一个：以 Pro/Max
登录的 Claude Code，或以 ChatGPT 登录的 Codex。同样的形态也适用于 Grok Build，以及
任何认证的是**人**而非工作负载的 CLI 智能体：下述机制针对的是这种登录的*形态*，
而非某一家供应商。它运行在笔记本电脑上，以一个
OAuth 凭据进行认证，而它恰恰是推理路径中的云服务商护栏所永远看不见的那一面
（参见[那道楔子](/zh/explanation/positioning/where-olivares-fits-vs-your-gateway/)）。
那个诱人的"解决方案" —— 在它前面放一个持有订阅并路由其流量的服务 —— 是
Olivares AI **不会**去构建的，因为模型供应商明令禁止，也因为那会让我们的
control plane 成为凭据被攻陷的单点。

本页诚实地说明我们如何**在绝不经手该订阅**的前提下治理这些智能体：我们观察
什么、在何处强制执行，以及那一条狭窄而合适的网关路径（而它从来不是订阅的那条）。

:::danger[红线：我们绝不路由你的订阅]
Olivares AI **绝不持有、代理或路由任何第三方订阅凭据。** Anthropic 自己的政策
声明：*"Anthropic does not permit third-party developers to offer Claude.ai login
or to route requests through Free, Pro, or Max plan credentials on behalf of their
users"*
（[Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance)，
抓取于 2026-06-21 —— 该禁令点名了三种消费者套餐 **Free、Pro、Max**）。对于消费者
ChatGPT/Codex 登录，OpenAI 的条款也以同样方式运作。我们的态度比这条线本身更严格：
我们对**任何**套餐的订阅 OAuth 一律**不**路由。治理发生在智能体*周围*，绝不发生在
其凭据*内部*。
:::

## 为何经手订阅根本不在考虑之列

值得对这条规则说精确些，因为买方的法务会去核对它。Anthropic 的政策划出两份不可
混为一谈的清单：

- **谁可以使用 OAuth** —— 五种套餐：*"OAuth authentication is intended
  exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise
  subscription plans and is designed to support ordinary use of Claude Code and
  other native Anthropic applications."*
- **第三方不可做什么** —— 代用户路由：*"Anthropic does not permit third-party
  developers to offer Claude.ai login or to route requests through Free, Pro, or
  Max plan credentials on behalf of their users."*

该禁令明确点名了**消费者**套餐（Free、Pro、Max）。反过来，该页面并未授予任何人
路由 Team 或 Enterprise 席位的许可 —— 它对此保持沉默，而我们不把沉默解读为许可。
对于*构建工具的开发者*，Anthropic 自己的指引彻底指向远离订阅 OAuth：*"Developers
building products or services that interact with Claude's capabilities, including
those using the Agent SDK, should use API key authentication through Claude Console
or a supported cloud provider."*
（[来源](https://code.claude.com/docs/en/legal-and-compliance)；按条款划分套餐：
Team/Enterprise/API 适用 Commercial Terms，Free/Pro/Max 适用 Consumer Terms。）

我们的 Codex 连接器按设计在代码中编码了相同的纪律：自动化凭据是 OpenAI 的
**API key** 或一个 **workspace access token**，绝非个人 ChatGPT 订阅 ——
*"proxying it for third-party/programmatic use violates OpenAI's terms exactly as
a consumer Claude subscription does for Anthropic. There is no subscription config
field by design"*（`connectors/codex/codex.go`）。所以这条红线不是事后拴上去的
营销承诺；它就是产品的形状。

## 三种机制，没有一个经手订阅

我们通过三条独立的通道来治理一个以订阅认证的智能体。前两条根本不触碰推理；
第三条仅对以 **API key** 认证的流量触碰它，绝不针对订阅。

### 1. 观察 —— 遥测、用量与态势

Claude Code 会发出 OpenTelemetry，而管理员可以从受管层级为整个机群将其打开：
*"Administrators can configure OpenTelemetry settings for all users through the
managed settings file"*
（[Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)）。
我们摄入这份 **gen-ai 信号** —— 会话、token、成本、工具活动 —— 并将其转化为访问图
与态势发现。关键在于，**这在 Claude Code 一侧同样是经设计的最小化数据**：prompt
内容*"redacted by default"*，而工具细节、工具内容与原始 API 主体各自皆为*"(default:
disabled)"*（同一来源）。我们消费的是用量与元数据，而非对话。

对于 Codex，同一条观察通道是该连接器对 Analytics 与 Compliance/Audit API 的摄入
—— 用量、采用度与不可变审计记录被转化为成本样本与篡改可检测证据，携带*"never
prompt/diff content or key values"*（`connectors/codex/codex.go`）。

→ [摄入 OpenTelemetry GenAI](/zh/how-to/connectors/otel-genai/) ·
[面向 Claude Code 的企业级 OTel](/zh/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + hooks —— 进程内的 PEP

观察不是强制执行。Claude Code 的强制执行通道，是它位于 OS 策略层级的 **managed
settings** 文件，该文件携带一个不可被覆盖的 `PreToolUse` hook，在每个工具运行之前
回调到 Olivares 决策点。Anthropic 记录了我们所依赖的特性：*"Environment variables
defined in the managed settings file have high precedence and cannot be overridden
by users"*，而 managed settings *"can be distributed via MDM"*
（[monitoring](https://code.claude.com/docs/en/monitoring-usage)）。

Olivares 以 `allowManagedHooksOnly` 渲染该文件（`olivares agent managed-settings`），
使得开发者自己的 hook 永远无法先于或削弱受治理的那一个，而每会话的端点与 bearer
在启动时注入 —— 而非写入静态文件。决策本身是**每一条边都默认拒绝**的：仅当一个
确凿的身份解析成功、策略处置不为 `deny`、实时策略引擎不予禁止，并且 —— 对于一个
`ask` —— 一项人工审批被绑定到确切的 plan hash 时，一次工具调用才被允许。一个紧急
停止（[kill switch](/zh/reference/glossary/#kill-switch终止开关)）凌驾于一切之上，包括一项
处于活动状态的 break-glass 授权。

这正是 [Claude Code hooks PEP](/zh/how-to/connectors/claude-code-hooks-pep/) 页面在
运维层面所记录的机制，也正是它使我们能够*治理*本地开发智能体，而不仅仅是观察它
—— 即[这套术语所指向的三条赛道](/zh/explanation/positioning/analyst-vocabulary/#这套术语所指向的三条赛道)
中的第二条。

### 3. 面向 API key 的网关 —— 绝不面向 OAuth

恰有一条路径让 Olivares 置身于推理请求线之中，而它仅为那些**不**使用 Claude Code
managed-settings 通道的调用方而存在：以 **API key**（或 Bedrock/Vertex 等价物）
认证的原始 SDK 或 `curl` 流量。Claude Code 用 `ANTHROPIC_BASE_URL` 路由这类请求
—— *"To route requests through a custom API endpoint, set the `ANTHROPIC_BASE_URL`
environment variable instead"* —— 并以 `ANTHROPIC_AUTH_TOKEN` 用一个 bearer 对网关
进行认证，*"when routing through an LLM gateway or proxy that authenticates with
bearer tokens rather than Anthropic API keys"*
（[Claude Code IAM](https://code.claude.com/docs/en/iam)）。指向 Olivares 内联推理
代理后，这类流量在被转发之前会经过一条受治理的流水线 —— 驻留、模型访问、上下文
窗口、DLP、预算、记录。

这条边界是绝对的：**此路径承载 API-key / bearer 流量，绝不承载订阅的 OAuth
凭据。** 它是面向 managed settings 触及不到的 SDK/`curl` 调用方的强制执行接缝，
仅此而已。

## 诚实之框：已验证已部署，而非无法绕过

:::caution[我们能证明*已部署*的强制执行，而非*无法*被规避的强制执行]
managed-settings + hook PEP 是**默认拒绝**且**用户无法通过 settings 覆盖**的 ——
但它不是魔法。一个把 `ANTHROPIC_BASE_URL` 指向自己端点的开发者，会把推理发往完全
别处；我们自己的工程注记直言不讳：*"a custom `ANTHROPIC_BASE_URL` bypasses
server-managed-settings entirely"*（`modules/inferenceproxy/doc.go`）。所以我们
绝不声称该 PEP 无法逃脱。相反，我们声称两件我们能站得住脚的事：

1. **它已验证已部署。** Olivares 证实 managed settings 与 PEP hook 确实存在于
   主机上 —— 一台未配置的主机以未治理但被观察的状态运行，而这是可见的，不是隐藏的。
2. **绕过本身即是一项发现。** 主机上一个非默认的 `ANTHROPIC_BASE_URL` 会浮现为
   一项态势发现，而一个钉住了与授权 Olivares 网关相异的 base URL 的受管环境会触发
   一项**漂移**发现（`connectors/claude-config`、`connectors/managedsettings`）。
   规避不会悄无声息；它会亮起来。
:::

"已验证已部署、规避即发现"是面向任何运行在开发者所控制机器上的智能体的诚实
强制执行叙事。我们不会向你兜售"无法绕过"。

## 诚实陈述 Codex 的不对称性

Claude Code 与 Codex 并不对称，而这一差异举足轻重。对于以 ChatGPT 认证的 Codex，
**不存在 `ANTHROPIC_BASE_URL` 的有文档记载的等价物** —— OpenAI 的
[managed-configuration 页面](https://developers.openai.com/codex/enterprise/managed-configuration)
没有记录任何用于将推理路由经由自定义 base URL 或网关的设置或环境变量（经抓取验证，
2026-06-21；这是该页面上的一处缺失，而非别处不存在的证明）。所以我们**不会**通过
拦截推理来治理 Codex。

相反，我们在 OpenAI *确实*为管理员提供强制控制之处治理它。Codex 受管配置让企业
得以设置*"Requirements: admin-enforced constraints that users can't override"*，
用以*"constrain security-sensitive settings (approval policy, approvers reviewer,
automatic review policy, sandbox mode, permission profiles, web search mode,
managed hooks, and optionally which MCP servers users can enable)"*（同一来源）。
Olivares 编写并证实这些 requirements（`connectors/codex-managed-config`）—— 审批
策略、sandbox 模式、MCP 允许列表、经脱敏的遥测（`log_user_prompt = false`）—— 并
摄入 Codex 的 Analytics 与 Compliance 证据。通过配置与证据来治理，而非在模型调用上
做中间人。

## 汇于一表

| 通道 | 它做什么 | 是否触碰推理？ | 凭据 |
|---|---|---|---|
| **观察** | 用量、成本、工具活动 → 访问图 + 态势；Codex Analytics/Compliance → 账本 | 否 | 无 —— 仅遥测，内容默认脱敏 |
| **Managed settings + hooks** | 在 Claude Code 上的默认拒绝 `PreToolUse` PEP，无法通过 settings 覆盖 | 否 | 智能体自己的；我们从不看见它 |
| **网关（仅 API key）** | 经由 `ANTHROPIC_BASE_URL` 为原始 SDK/`curl` 调用方提供的受治理流水线 | 是 | **API key / bearer —— 绝非订阅 OAuth** |
| **Codex managed-config** | admin 强制的 requirements（审批/sandbox/MCP）+ 证据摄入 | 否 | 组织的；是配置，而非拦截 |

## 相关阅读

- [Olivares 相对于你的网关 / Guardrails 的位置](/zh/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  —— 为何这一切都不与你的 AI 网关竞争。
- [Olivares AI 与 WitnessAI 的对比](/zh/explanation/positioning/vs-witnessai/) ——
  关于在 IDE 中治理智能体的正面对决。
- [Claude Code hooks 与 PEP](/zh/how-to/connectors/claude-code-hooks-pep/) 以及
  [用 Olivares 运行 Claude Code](/zh/how-to/run-claude-code-with-olivares/) ——
  运维操作指南。
- [诚实与限度](/zh/start/honesty-and-limits/) —— 本页所遵循的长期承诺。
