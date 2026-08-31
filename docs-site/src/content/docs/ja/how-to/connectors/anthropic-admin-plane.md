---
title: "Anthropic admin プレーン（使用量、コスト、コンプライアンス）"
description: >-
  Claude 組織そのものをガバナンスする: Admin API による権威ある請求コストと使用量、
  permitted エッジとしての API 側 MCP およびサーバーツール allow-set、コンプライアンス
  活動フィードと組織ディレクトリ — 各認証情報はスコープされ、各盲点は名指される。
sidebar:
  order: 6
---

Claude Code テレメトリは、開発者マシン上で何が実行されるかを伝えます。
**Anthropic admin プレーン** は、*組織* が何をするかを伝えます。請求コスト、
ワークスペース単位の使用量、組織メンバーとキー、コンプライアンス活動フィードです。
4 つの読み取り専用ソースがそれをカバーします。本ページは中心となる 2 つを配線し、
それらの名簿側の伴走ソースを要約します。

| ソース（`kind`） | 読むもの | 認証情報 |
|---|---|---|
| `claude-api` | 使用量と請求コスト、モデル/ワークスペースインベントリ、Claude Code アナリティクス、API 側 MCP/サーバーツールガバナンス | Admin API キー（`admin_key`） |
| `claude-compliance` | コンプライアンス活動フィード（証拠グレードのイベント）+ 組織ディレクトリ | 活動フィードキー + **別個の** Compliance Access Key |
| `claude-console` | 組織 IAM 名簿（メンバー、ロール）→ SSO/SCIM 態勢の finding | console 認証情報 |
| `claude-wif` | 非人間アイデンティティ（サービスアカウント `svac_…`、フェデレーテッドアイデンティティ）+ それらの **permitted** スコープエッジ | WIF エンドポイント認証情報 |

すべて **読み取り専用かつ deny-closed** です。空の認証情報はそのフィードがオフで
あることを意味し、製品はそう述べます — 捏造された空のインベントリは決して出しません。

## `claude-api`: コスト、使用量、API 側ガバナンス

```json
{
  "sources": [{
    "name": "anthropic-org",
    "kind": "claude-api",
    "tenant": "<tenant-id>",
    "config": {
      "admin_key": "<admin-api-key-reference>",
      "cost_report": "true",
      "claude_code": "true"
    }
  }]
}
```

重要なキー（出荷されたディスクリプターより。デフォルトは括弧内）:

- **`admin_key`**（シークレット）— Anthropic Admin API キー。空 = オフラインカタログのみ。
- **`cost_report`**（`true`）— 派生した使用量推定と並んで、**請求** コストレポート（日次、
  権威あり）を取得します。製品は両者を分けて保ちます。推定は請求数値に対して照合され、
  セッションあたりのコストの出典は 1 つであり、決して両方ではありません。
- **`lookback`**（`24h`）/ **`cost_lookback`**（`48h`）/
  **`bucket_width`**（`1d`; `1h`、`1m` も可）/ **`max_pages`** — 取得ウィンドウと
  ページネーション境界。
- **`claude_code`**（`false`）— チャージバック向けに Claude Code Analytics フィード
  （モデル別の開発者単位推定コスト）も取得します。
- **`claude_code_shadow_auth`**（`true`）— アナリティクスフィードが有効なとき、Claude Code
  の使用が `customer_type=api` として請求される各開発者をフラグします — 個人/API キーが
  **組織サブスクリプションの外** にある、すなわちガバナンスされていないキーに乗った
  アイデンティティと支出です。組織が意図的に Claude Code を API 課金で実行している場合のみ
  `false` に設定してください。
- **`gateway`**（`direct`）— この組織が稼働するデプロイ面
  （`direct | claude-platform-aws | bedrock-mantle | bedrock-legacy | vertex |
  foundry`）。Admin API を持たない面（Bedrock/Vertex/Foundry）では、ガバナンス取り込みは
  空のインベントリを装うのではなく **態勢 finding を伴って正直に劣化** します。
- **`mcp_toolsets`** / **`server_tool_grants`** — API 駆動の Claude エージェント向けに
  オペレーターが宣言する allow-set（どの MCP ツール、どの Anthropic サーバーツール型を
  エージェントが使ってよいか）。許可された各エントリはモジュール III の **permitted エッジ**
  になり、観測されたアクセスと突き合わされます — 他の場所と同じ permitted-vs-observed 差分です。
  `agent_ref` はランタイムで発見されたエージェントの external id でなければなりません。さもなければ
  グラントは誤った一致ではなく正直な no-op になります。

:::caution[アナリティクスフィードには名指された境界がある]
Claude Code Analytics フィードは **Claude API** 上の使用量のみを追跡します。
Claude Platform on AWS、Bedrock、Gemini Enterprise Agent Platform (formerly Vertex AI)、Microsoft Foundry のフリートは
**そこに含まれません** — そこに finding がないことは不在の証拠ではありません。それらの
面については、[OTel プレーン](/ja/how-to/claude-code-enterprise-otel/) があなたの持つ
観測です。
:::

## `claude-compliance`: 証拠フィードとディレクトリ

```json
{
  "sources": [{
    "name": "anthropic-compliance",
    "kind": "claude-compliance",
    "tenant": "<tenant-id>",
    "config": {
      "api_key": "<activity-feed-key-reference>",
      "compliance_access_key": "<compliance-access-key-reference>"
    }
  }]
}
```

意図的に **別個の** 2 つの認証情報です:

- **`api_key`** — `read:compliance_activities` を持つ Admin API キー。活動フィード
  （証拠グレードのイベント）を取得します。
- **`compliance_access_key`** — `read:compliance_org_data` /
  `read:compliance_user_data` を持つ別個のキー。組織 **ディレクトリ** 取り込み（組織、
  ユーザー、ロール、グループ — Admin API が見られない SCIM プロビジョニングシグナルを含む）を
  有効にします。空 = ディレクトリオフ、deny-closed。

削除スコープ（`delete:compliance_user_data`、消去権の経路で使用）は別途プロビジョニングされ、
dual-control でゲートされます — この読み取りコネクタは決してそれを保持しません。

## console で見えるもの

請求済みおよび推定の支出を、テレメトリが運ぶ次元（チームとプロジェクトのラベルが第一級になる）で
切り分けたものが **Cost & FinOps** に。組織メンバー、非人間アイデンティティとそのスコープが
**Identity & NHI** に。態勢 finding（シャドウ認証、面の劣化、WIF の落とし穴）が **Security** に:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Cost & FinOps ビュー: モデルと次元別の支出、予算とアラート付き。" />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Cost & FinOps ビュー: モデルと次元別の支出、予算とアラート付き。" />

## 正直な限界

- **コストの権威は請求レポート。** 使用量から派生した数値は推定であり、照合されます。
  決して二重計上されません。
- **admin プレーンは Anthropic 運用の面を見る。** サードパーティがホストする Claude
  （Bedrock/Vertex/Foundry）はそれには見えません — `gateway` 経由で明示的に名指され、
  OTel プレーンでカバーされます。
- **`claude-console` の態勢 finding には盲点が含まれる:** console は SSO/SCIM が上流で
  強制されているかを観測できません — finding は推測するのではなくそう述べます。

## 関連

- [Claude Code 向けエンタープライズ OTel](/ja/how-to/claude-code-enterprise-otel/) —
  これらの組織レベルフィードが補完するセッション単位のプレーン。
- [予算 & FinOps guardrail](/ja/how-to/cookbook/budgets-and-finops-guardrails/)
  — コストストリームを強制される上限に変える。
- [コネクタ & カバレッジ層](/ja/reference/connectors/) — 完全なカタログ。
