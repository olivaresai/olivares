---
title: Claude apps gateway 与 Olivares AI 联合部署
description: >-
  如何运行 Anthropic 的 self-hosted Claude apps gateway，并让 Olivares AI
  把它作为另一项 enterprise surface 治理：inventory、posture、audit ingest、
  OTLP correlation，以及 phase-1 gateway-protocol endpoint。
sidebar:
  order: 9
---

## Claude apps gateway 是什么

Anthropic 的
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway)
是一项 self-hosted service，从 v2.1.195 起随 `claude` binary 一同提供；使用
`claude gateway --config gateway.yaml` 运行，并以 PostgreSQL 作为 backend。它在
Amazon Bedrock、Claude Platform on AWS、Google Cloud Agent Platform、Microsoft
Foundry 或 Anthropic API 前放置 OIDC sign-in，让开发者使用企业 IdP session，
而不是本地 provider credential。其 `gateway.yaml` 把 IdP group 映射到 model
allowlist 和 managed setting；spend-limits Admin API 则可限制每位 user、每个
group 或整个 organization 的支出。gateway 通过 OTLP fan-out telemetry，并输出
单行 JSON audit event。Anthropic 在 2026 年 6 月 29 日的
[公告](https://claude.com/blog/introducing-the-claude-apps-gateway)中，把它定位为
面向 Claude Code、由 Anthropic 官方提供的第一方 gateway infrastructure。

## 运行它，由 Olivares 治理

如果你已经运行或计划运行 Anthropic gateway，请保留它。原则是**“并且”，不是
“或者”**：Anthropic gateway 负责 Claude Code 的 gateway session、model access
与 upstream-routing path；Olivares AI 则让该 deployment 成为更广泛 control plane
中的受治理 surface。

`claude-apps-gateway` connector inventory `gateway.yaml` 中的 issuer、
IdP-group → model allowlist、spend-admin posture、OTLP destination 和 upstream。
它会针对 governance 运营者关心的 config state 提出 posture finding，并 ingest
gateway 的 JSON audit event，让 deny、session mint 和 inference record 进入
可检测篡改的审计台账。把 gateway 的 OTLP fan-out 指向 Olivares OTLP receiver，
即可将 `session.id` signal 与受治理 session runtime record 关联；Olivares 仍只
保留结构化数据，不保留 prompt payload。

## 文档所述限制

以下 scope 决策引自 Anthropic 截至 2026-07-03 的文档。这些是 scope 声明，
不是缺陷；它们界定联合部署的边界。

| 功能 | 状态 | 说明 |
|---|---|---|
| SAML、LDAP 及其他非 OIDC auth | 不支持。 | 仅 OIDC。需要时在前面放置 OIDC bridge |
| Multi-tenant（多个 OIDC issuer） | 不支持。 | 每个 gateway 一个 issuer。运行独立 instance |
| Admin UI | 没有。 | config 即 YAML file；更改时重新部署 |
| Helm chart | 没有。 | gateway 作为标准 stateless Deployment 运行 |
| CI pipeline | 没有面向无人值守 pipeline 的 service-token flow |  |
| OTLP/gRPC | 不支持。 | 仅支持 HTTP 上的 OTLP |
| Windows server | 不支持。 | 部署在 Linux 上 |
| Model catalog | 仅 Claude model | gateway 按 upstream 转换 Claude ID |

## Olivares 在旁边增加什么

Olivares 不会消除 Anthropic gateway 的这些限制，而是在旁边补充缺失的治理平面。

| Anthropic gateway 限制 | Olivares 在旁提供的能力 |
|---|---|
| SAML、LDAP 及其他非 OIDC auth | 对于 Olivares 控制台与治理平面，[SSO/SCIM identity](/zh/how-to/connectors/sso-scim-identity/)说明 OIDC/SAML federation，[IdP architecture](/zh/explanation/architecture/where-it-fits-with-your-idp/)则把人类与 agent 映射到 SSO/SCIM 和 SPIFFE/WIF roster。这不会给 Anthropic gateway 加装 SAML；应让 gateway 保持仅 OIDC，或在前面放置 OIDC bridge。 |
| Multi-tenant（多个 OIDC issuer） | Olivares 的 [multi-tenant control plane](/zh/reference/modules/xx-multi-tenancy/)按 tenant 限定 entity、finding、session 和审计台账；multi-tenant deployment 使用 PostgreSQL RLS。为每个 issuer 运行独立 gateway instance，并把每个作为自己的 surface 加以治理；不要把单个 Anthropic gateway 当成 multi-issuer。 |
| Admin UI | Olivares web console 是 [模块 XIX](/zh/reference/modules/xix-api-manage-as-code/)所述同一 API 之上的 presentation layer；identity 文档展示了 live 的 **Identity & NHI -> SSO & SCIM** UI。它是 control plane 的 admin console，不是 Anthropic `gateway.yaml` 的 UI editor。 |
| Helm chart | Olivares 提供自己的 [Kubernetes Helm deployment](/zh/tutorials/getting-started/kubernetes/)和一个独立 Kubernetes operator。这会部署 Olivares control plane；并未声称会 package Anthropic gateway。 |
| CI pipeline | Olivares automation 可通过 [manage-as-code](/zh/how-to/manage-as-code/)使用 opaque、revocable、tenant-bound API token。对于受治理 runtime 与 deployment credential，WIF/SPIFFE broker 会 mint 短期 credential。这与 Anthropic gateway 相互独立；除非刻意使用下述 Olivares proxy endpoint，否则 gateway 自己的 CI 指引仍是直连 provider。 |
| OTLP/gRPC | Olivares `claude` receiver 接受 [OpenTelemetry GenAI](/zh/how-to/connectors/otel-genai/)使用的常规 OTLP receiver path，包括 HTTP 与 gRPC。Anthropic gateway 仍发送 OTLP/HTTP；其他受治理 agent 可直接使用 gRPC，所得 event 可进入密码学审计台账和[合规 evidence pack](/zh/reference/modules/xiii-compliance/)。 |
| Windows server | 此处不主张任何 Windows-server 能力。server-side component 应在 Linux、container 或 Kubernetes 上运行，并通过 telemetry、hook 和 connector evidence 治理 developer endpoint。 |
| Model catalog | [模块 X](/zh/reference/modules/x-models/)治理跨 vendor 的 model/provider estate：Claude、OpenAI、Gemini 与 local inference；Bedrock connector 增加 Bedrock usage/cost 和 Guardrails observability。Anthropic gateway 仍仅支持 Claude，而 Olivares 治理更广的 estate，包括通过 [subscription-auth governance](/zh/explanation/positioning/governing-subscription-authed-agents/)治理 Codex posture。 |

## Protocol superset，phase 1

Anthropic 发布 gateway protocol，并邀请第三方实现。Olivares inference proxy 实现了
apps-gateway protocol engineering contract 所述的 phase-1 superset：OAuth
discovery、RFC 8628 device authorization、经认证审批后通过 session credential
seam 进行 token polling、带 ETag 的 single-document managed-settings delivery、
只读 spend-limits list shape，以及 `GET /protocol`。

descriptor 自行记录差异：managed setting 为 single-document mode，version header
为 `x-olivares-version`，spend-limit write/effective/audit route 返回符合协议的
`501` response；Olivares 保留更丰富的 budget-deny mapping，并添加
`x-should-retry: false`。phase 1 不提供 Anthropic OIDC callback/browser `/device`
page、每 group managed-settings merge rule、spend-limit write path、`count_tokens`
或 `x-claude-code-session-id` header attribution。

## 选择 topology

- **仅 gateway。** 适合只使用 Claude、只有单一 issuer 的 OIDC organization；
  该组织能够接受管理 YAML 与重新部署，并且 gateway 自身的 spend limit、OTLP
  fan-out 与 JSON audit output 已经足够。
- **Gateway + Olivares。** Claude Code 进入受监管 estate 时推荐的联合部署：
  保留 Anthropic gateway，添加 `claude-apps-gateway` connector，把 OTLP 指向
  Olivares，并在 control plane 中保留所得 posture、runtime 与 evidence 全貌。
- **把 Olivares proxy 用作 gateway-protocol endpoint。** 当你刻意希望 Olivares
  inference proxy 提供 phase-1 gateway-protocol surface 时使用。若随附 subset
  已足够，它会很有用；但它不能完全替代 Anthropic gateway 的 browser OIDC flow，
  也不能替代写入 path 的 spend administration。
