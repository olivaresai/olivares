---
title: Claude apps gateway と Olivares AI の共同デプロイ
description: >-
  Anthropic のセルフホスト型 Claude apps gateway を実行し、Olivares AI で
  もう 1 つの enterprise surface としてガバナンスする方法。inventory、
  posture、audit ingest、OTLP correlation、phase-1 gateway-protocol endpoint
  を扱います。
sidebar:
  order: 9
---

## Claude apps gateway とは

Anthropic の
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway) は、
v2.1.195 以降の `claude` binary に同梱されるセルフホスト型 service です。
`claude gateway --config gateway.yaml` で実行し、PostgreSQL を backend として
使用します。Amazon Bedrock、Claude Platform on AWS、Google Cloud Agent Platform、
Microsoft Foundry、または Anthropic API の前段に OIDC sign-in を置くため、開発者は
各 provider の local credential ではなく、企業 IdP の session を使用します。
`gateway.yaml` は IdP group を model allowlist と managed setting に対応付け、
spend-limits Admin API は user、group、または organization ごとの支出を制限できます。
gateway は OTLP 経由で telemetry を fan-out し、1 行の JSON audit event を出力
します。Anthropic は 2026 年 6 月 29 日の
[発表](https://claude.com/blog/introducing-the-claude-apps-gateway)で、これを
Claude Code 向けに Anthropic 自身が提供する gateway infrastructure と位置付けて
います。

## gateway を実行し、Olivares でガバナンスする

Anthropic gateway をすでに使用している場合も、導入予定の場合も、そのまま使用して
ください。方針は**「or」ではなく「and」**です。Anthropic gateway は Claude Code
の gateway session、model access、upstream routing path を担い、Olivares AI は
その deployment を、より広いコントロールプレーン内のガバナンス対象 surface に
します。

`claude-apps-gateway` connector は `gateway.yaml` から、issuer、IdP-group ごとの
model allowlist、spend-admin posture、OTLP destination、upstream を inventory します。
ガバナンス運用者にとって重要な config state を posture finding として提起し、
gateway の JSON audit event を ingest します。これにより、deny、session mint、
inference record が改竄検知可能な監査台帳へ入ります。gateway の OTLP fan-out を
Olivares OTLP receiver に向けると、`session.id` signal をガバナンス対象 session
runtime record と correlate できます。ただし、Olivares が保持するのは構造化データ
であり、prompt payload ではありません。

## 文書化されている制限

以下は、2026-07-03 時点の Anthropic 文書から引用した scope の決定です。欠陥では
なく scope の声明であり、共同デプロイの境界をどこに置くかを定めます。

| 機能 | 状態 | 注記 |
|---|---|---|
| SAML、LDAP、その他の非 OIDC auth | 未対応。 | OIDC のみ。必要なら OIDC bridge を前段に置く |
| Multi-tenant（複数の OIDC issuer） | 未対応。 | gateway ごとに issuer は 1 つ。別々の instance を実行する |
| Admin UI | なし。 | config は YAML file。変更時は再デプロイする |
| Helm chart | なし。 | gateway は標準的な stateless Deployment として実行する |
| CI pipeline | 無人 pipeline 用の service-token flow はない |  |
| OTLP/gRPC | 未対応。 | HTTP 上の OTLP のみ |
| Windows server | 未対応。 | Linux にデプロイする |
| Model catalog | Claude model のみ | gateway が upstream ごとに Claude ID を変換する |

## Olivares が隣で追加するもの

Olivares は Anthropic gateway のこれらの制限を取り除きません。その隣に不足している
ガバナンスプレーンを追加します。

| Anthropic gateway の制限 | 隣で提供する Olivares の機能 |
|---|---|
| SAML、LDAP、その他の非 OIDC auth | Olivares のコンソールとガバナンスプレーンについて、[SSO/SCIM ID](/ja/how-to/connectors/sso-scim-identity/)は OIDC/SAML federation を文書化し、[IdP architecture](/ja/explanation/architecture/where-it-fits-with-your-idp/)は人間と agent を SSO/SCIM および SPIFFE/WIF roster に対応付けます。これは Anthropic gateway に SAML を追加するものではありません。gateway は OIDC 専用のまま使用するか、OIDC bridge を前段に置いてください。 |
| Multi-tenant（複数の OIDC issuer） | Olivares の [multi-tenant control plane](/ja/reference/modules/xx-multi-tenancy/)は、entity、finding、session、監査台帳を tenant ごとに scope し、multi-tenant deployment では PostgreSQL RLS を使用します。issuer ごとに別の gateway instance を実行し、それぞれを独立した surface としてガバナンスしてください。1 つの Anthropic gateway を multi-issuer として扱ってはいけません。 |
| Admin UI | Olivares web console は、[module XIX](/ja/reference/modules/xix-api-manage-as-code/)が説明する API と同じ API の presentation layer です。ID の文書には live の **Identity & NHI -> SSO & SCIM** UI も示されています。これはコントロールプレーンの admin console であり、Anthropic の `gateway.yaml` を編集する UI ではありません。 |
| Helm chart | Olivares は独自の [Kubernetes Helm deployment](/ja/tutorials/getting-started/kubernetes/)と、別個の Kubernetes operator を提供します。これは Olivares コントロールプレーンをデプロイするものであり、Anthropic gateway を package 化するとは主張しません。 |
| CI pipeline | Olivares の automation は [manage-as-code](/ja/how-to/manage-as-code/)を通じて、opaque、revocable、tenant-bound な API token を使用できます。ガバナンス対象 runtime credential と deployment credential については、WIF/SPIFFE broker が短期 credential を mint します。これは Anthropic gateway とは別の仕組みであり、下記の Olivares proxy endpoint を意図的に使用しない限り、gateway 自身の CI guidance は provider への直接接続のままです。 |
| OTLP/gRPC | Olivares の `claude` receiver は、[OpenTelemetry GenAI](/ja/how-to/connectors/otel-genai/)が使用する通常の OTLP receiver path を HTTP と gRPC の両方で受け入れます。Anthropic gateway は引き続き OTLP/HTTP で送信します。他のガバナンス対象 agent は直接 gRPC を使用でき、得られた event は暗号学的な監査台帳と[コンプライアンス evidence pack](/ja/reference/modules/xiii-compliance/)へ送れます。 |
| Windows server | ここでは Windows server の機能を主張しません。server-side component は Linux、container、または Kubernetes で実行し、developer endpoint は telemetry、hook、connector evidence を通じてガバナンスしてください。 |
| Model catalog | [モジュール X](/ja/reference/modules/x-models/)は、Claude、OpenAI、Gemini、local inference という複数 vendor の model/provider estate をガバナンスします。Bedrock connector は Bedrock の usage/cost と Guardrails の observability を追加します。Anthropic gateway は Claude 専用のままですが、Olivares は [subscription-auth governance](/ja/explanation/positioning/governing-subscription-authed-agents/)による Codex posture を含め、より広い estate をガバナンスします。 |

## Protocol superset、phase 1

Anthropic は gateway protocol を公開し、第三者による実装を募っています。Olivares の
inference proxy は、apps-gateway protocol の engineering contract に記載された
phase-1 superset を実装しています。内容は、OAuth discovery、RFC 8628 device
authorization、認証済み承認後の sessions credential seam を通る token polling、
ETag 付きの single-document managed-settings delivery、読み取り専用の spend-limits
list shape、`GET /protocol` です。

descriptor 自体が相違点を文書化しています。managed setting は single-document
mode、version header は `x-olivares-version`、spend-limit の write/effective/audit
route は仕様に適合する `501` response を返します。また Olivares は、より詳細な
budget-deny mapping を維持しつつ `x-should-retry: false` を追加します。phase 1 には、
Anthropic の OIDC callback/browser `/device` page、group ごとの managed-settings
merge rule、spend-limit write path、`count_tokens`、
`x-claude-code-session-id` header attribution は含まれません。

## topology を選ぶ

- **gateway だけ。** 単一 issuer の OIDC organization で、Claude だけを利用し、
  YAML の管理と再デプロイに問題がなく、gateway 自身の spend limit、OTLP fan-out、
  JSON audit output で十分な場合に適しています。
- **gateway + Olivares。** Claude Code を規制対象の estate に導入するときに推奨する
  共同デプロイです。Anthropic gateway を維持し、`claude-apps-gateway` connector
  を追加し、OTLP を Olivares に向け、得られた posture、runtime、evidence の全体像
  をコントロールプレーンに保持します。
- **gateway-protocol endpoint としての Olivares proxy。** Olivares inference proxy
  から phase-1 gateway-protocol surface を提供したいと意図的に選ぶ場合に使用します。
  提供済みの subset で十分な場合には有用ですが、Anthropic gateway の browser OIDC
  flow や、書き込み経路を使う spend administration を完全に置き換えるものでは
  ありません。
