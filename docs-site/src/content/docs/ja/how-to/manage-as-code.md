---
title: "Olivares AI をコードとして管理する（Terraform）"
description: >-
  Olivares AI の Terraform/OpenTofu プロバイダーで、コントロールプレーンの
  オブジェクト（エージェント、ポリシー、アイデンティティのバインディング、
  デプロイメント）を宣言し調整する。エンジンの REST API に対して不透明な API
  トークンで認証する。
---

Olivares AI は **Terraform プロバイダー** を公開しており、コントロールプレーンを *コードとして*
管理できます。エージェント、ガバナンスポリシー、エージェント↔アイデンティティのバインディング、
デプロイメント定義を HCL で宣言し、稼働中のエンジンに対して REST API 経由で調整します。これは
モジュール XIX（独自 API + コードとしての管理）です。プロバイダーは [API リファレンス](/reference/api/)
が文書化しているのと同じ REST サーフェスの上にある薄いクライアントなので、HCL でできることは
すべて REST でも実行できます。

プロバイダーと CLI は Apache-2.0 であり、エンジン内部を一切 import しません。HCL は
ガバナンス対象 API のもう 1 つのフロントエンドにすぎません。

## プロバイダーを設定する

```hcl
terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

provider "olivares" {
  endpoint = "https://olivares.internal:8443" # or OLIVARES_ENDPOINT
  api_token = var.olivares_token                  # or OLIVARES_API_TOKEN (sensitive)
  # tenant   = "…"                                # optional; or OLIVARES_TENANT (sent as X-Olivares-Tenant)
  # insecure_skip_verify = true                   # dev self-signed cert only
}
```

| 設定 | 必須 | 環境変数フォールバック | 備考 |
|---|---|---|---|
| `endpoint` | はい | `OLIVARES_ENDPOINT` | コントロールプレーン API のベース URL |
| `api_token` | はい | `OLIVARES_API_TOKEN` | **不透明なベアラートークン**（本製品は JWT ではなく、不透明で失効可能なトークンを使用） |
| `tenant` | いいえ | `OLIVARES_TENANT` | テナント UUID。トークンがテナントに紐づいている場合は省略 |
| `insecure_skip_verify` | いいえ | — | 開発用の自己署名証明書で TLS 検証をスキップする。本番では決して使用しない |

認証はすべてのリクエストで送信されるベアラートークンであり、テナントは
`X-Olivares-Tenant` ヘッダーで運ばれます。API のその他の部分と同じ、デフォルト拒否（deny-by-default）の
RBAC、テナントスコープ、アクションごとの監査が適用されます。最小権限のサービスアイデンティティ向けに
トークンを発行し、state には含めないでください（変数とシークレットバックエンドを使用）。

## リソース

| リソース | 管理対象 | 主要な属性 |
|---|---|---|
| `olivares_agent` | インベントリ内のエージェントエンティティ | `name`（必須）、`kind`（必須）、`external_id`（任意）。計算値 `id`、`status`、`version` |
| `olivares_policy` | ガバナンスポリシー | `name`（必須）、`kind`（`abac` または `approval`、必須、変更不可）、`enabled`、`spec`（必須、JSON）。計算値 `spec_canonical` |
| `olivares_agent_identity_binding` | エージェントを非人間アイデンティティ（NHI）にバインド（R/RW の帰属を鋭くするブリッジ） | `agent_id`、`identity_id`/`identity_ref`、`mint`、`allow_unknown`。計算値 `minted`、`shared`、`agent_count` |
| `olivares_deployment` | デプロイメント **定義**（宣言的な望ましい状態） | `subject_kind`、`subject_ref`、`name`、`environment`、`runtime`、`target`、`source_ref`、`spec`、`desired_status`。計算値 `current_version`、`applied_version`、`spec_hash` |

## データソース

読み取り専用のビューにより、モジュールは REST 呼び出しを再実装せずにガバナンス対象の state を
参照できます。`olivares_policies`、`olivares_identities`、`olivares_deployment`、
`olivares_server_info`、`olivares_access_edges` があります。最後のものは R/RW のエッジを公開し、
`include_drift = true` を指定すると Permitted-vs-Observed のドリフト（まだ確実に帰属できないアクセスを
表す正直な `reconciliation_pending` フラグを含む）も公開します。

## 最小限の例

```hcl
resource "olivares_agent" "billing_bot" {
  name = "billing-reconciler"
  kind = "service"
}

resource "olivares_policy" "require_approval_for_prod" {
  name    = "prod-deploys-need-approval"
  kind    = "approval"
  enabled = true
  spec    = jsonencode({
    # policy body — see the API reference for the schema of each kind
  })
}

# Read the current Permitted-vs-Observed drift as data:
data "olivares_access_edges" "estate" {
  include_drift = true
}
```

`terraform plan` は HCL をエンジンに対して調整し、`terraform apply` はガバナンス対象 API を通じて
オブジェクトを作成または更新します。ポリシーとバインディングは認可サーフェスを変更するため、plan は
レビュー対象の変更として扱ってください。エンジンはすべての変更を実際のアクターとともに監査します。

:::caution[`olivares_deployment` は望ましい状態を宣言するが、ライブの適用はゲートされる]
`olivares_deployment` はデプロイメント **定義** を管理します。これは宣言的でバージョン管理された
望ましい状態です。モジュール VII（デプロイ）にマッピングされ、そのライブな実行は **拒否クローズドな
シーム（deny-closed seam）** です。エグゼキューターがプロビジョニングされるまで、エンジンはデプロイメントを
*計画しガバナンスする* ものの、インフラに対して動作する代わりに **`apply`/`retire` は `503` を返します**。
したがって `olivares_deployment` リソースは今日のところ意図を記録しガバナンスするものであり、それ自体で
実際のインフラを調整するものではありません。[モジュール VII](/ja/reference/modules/vii-deploy/) と
[誠実さと制限](/ja/start/honesty-and-limits/) を参照してください。
:::

:::note[プロバイダーは意図的に API のサブセットである]
プロバイダーは上記のコードとしての管理オブジェクトをカバーします。ガバナンス対象の完全なサーフェス、
そして各 `spec` のフィールドレベルのスキーマは REST API にあります。一部のモジュールルートは到達可能
ですが、意図的に提供される OpenAPI ドキュメントの外側にあります。リソースの属性に依存する前に、
`terraform providers schema -json` と [API リファレンス](/reference/api/) に照らして検証してください。
このページはコードと歩調を合わせて維持できないスキーマを再掲しません。
:::

## 関連

- [API リファレンス](/reference/api/) — プロバイダーが駆動する REST サーフェス。
- [API 安定性ポリシー](/ja/reference/api-stability/) — プロバイダーが依存するバージョニング/非推奨のコミットメント（レスポンスが非推奨シグナルを運ぶとき、実行ごとに 1 回警告する）。
- [モジュール XIX — 独自 API + コードとしての管理](/ja/reference/modules/xix-api-manage-as-code/)。
- [モジュール VII — デプロイメントと統合](/ja/reference/modules/vii-deploy/) — 上記の 503 シームに関する注意。
- [ガバナンスと承認](/ja/how-to/govern-and-approve/) — ポリシーと承認が、宣言した内容をどうガバナンスするか。
