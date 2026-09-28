---
title: "Prometheus で監視する（SLO、メトリクス、アラート）"
description: >-
  エンジンの /metrics をスクレイプし、公開されている SLO ターゲットを採用し、
  同梱のバーンレートアラートルールを読み込む。製品自身のランブックが鍵とする
  ものと同じ SLI を、単一ライターの数値を正直に明示したうえで使用する。
---

エンジンは HTTP リスナー上で 3 つの運用エンドポイントを公開しており、いずれも
プローブに適しています。

| エンドポイント | 認証 | 目的 |
|---|---|---|
| `/livez` | なし | プロセスの生存確認 — **依存関係チェックなし**。そのためストア障害が再起動ループを引き起こすことはない |
| `/readyz` | なし | レディネス — ストアへの ping（および HA リーダーシップ）：`200 {"status":"ok","store":"up","leader":true,…}`、`503 {"status":"unavailable","store":"down"}`、または HA スタンバイ時に `503 {"status":"standby",…,"leader":false}` |
| `/metrics` | なし | Prometheus エクスポジション。意図的に未認証：運用系列を運び、テナントデータは決して運ばない |

`/readyz` への到達性が可用性 SLI **そのもの** です。

## 重要なメトリクスセット

すべての系列はエンジンによって登録されています（現行コードに照らして検証済み）。
中核を担うものは次のとおりです。

| 系列 | 何を示すか |
|---|---|
| `olivares_store_up` | ストアが ping に応答する — あらゆるランブックが最初に確認するもの |
| `olivares_http_requests_total{code}` | リクエスト成功 SLI（`code!~"5.."`） |
| `olivares_http_request_duration_seconds` | API レイテンシ（下記の p99 ターゲット） |
| `olivares_ingest_duration_seconds` | **バックプレッシャー SLI** — サブスクライバーが飽和するとインジェスト p99 が上昇する |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | インジェストのスループットと拒否 |
| `olivares_eventbus_queue_depth` / `_queue_capacity`（サブスクライバーごと） | どのモジュールが遅いコンシューマーか |
| `olivares_eventbus_publish_blocked_total` | バックプレッシャーイベント（バスはブロックする。ドロップはしない） |
| `olivares_eventbus_bridge_*` | 分散バスが有効なときの NATS ブリッジの健全性 — `_connected`、`_pending_messages`、`_dropped_total`（ノード間配信は at-most-once。ドロップはカウントされ、決して黙殺されない） |
| `olivares_audit_checkpoint_age_seconds` | 改ざん検知の鮮度 — チェックポイント間隔の 2 倍を超えたらアラート |
| `olivares_auth_login_attempts_total{outcome}` | ログインの成功 / 失敗 / ロックアウト / 中断 |
| `olivares_http_ratelimit_decisions_total{decision}` | レートリミットの圧力 |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | コレクター→コアのインジェストプレーン |

## SLO ターゲット（公開済み、正直）

以下はシングルノードと HA デプロイメントの運用目標であり、トポロジーの実測結果や
顧客への約束ではありません。達成を主張する前に、対象のデプロイメントとワークロードを
検証してください。検証状況は `deploy/support-matrix.md` に記録されています。

| SLI | シングルノード | HA ティア（Postgres） |
|---|---|---|
| 可用性（`/readyz`） | **99.5% / 28d** | 99.9% / 28d |
| リクエスト成功（非 5xx） | **99.9%** | 99.95% |
| API レイテンシ p99 | **< 300 ms** | < 200 ms |
| インジェストレイテンシ p99 | **< 250 ms** | < 150 ms |
| インジェスト成功 | **99.9%** | 99.95% |

28 日間では、可用性目標 99.5% の停止許容時間は **201.6 分（3 時間 21 分 36 秒）**、
99.9% では **40.32 分**です。[HA トポロジー](/ja/tutorials/getting-started/kubernetes/#3-active-passive-ha) を導入するだけで、この割合を達成できるわけではありません。
指定した期間の可用性を測定し、対象デプロイメントの障害・復旧検証の証拠を保存してください。
これらの目標は、契約上の可用性保証ではありません。

## 同梱のアラートルールを読み込む

`deploy/monitoring/olivares-slo.rules.yaml` は、Prometheus にすぐ使える 14 個のアラートを同梱しています。
リクエスト成功バジェットに対するマルチウィンドウのバーンレートアラート（高速 14.4× ページ /
中速 6× ページ / 低速 1× チケット）、絶対的なレイテンシと可用性の発火（`OlivaresIngestP99High`、
`OlivaresApiLatencyP99High`、`OlivaresStoreDown`、`OlivaresControlPlaneUnscrapeable`）、飽和
（`OlivaresEventBusSaturated`、キュー >90% が 10 分継続）、ブリッジの健全性
（`OlivaresEventBusBridgeDropping`、`OlivaresEventBusBridgeDisconnected`）、そして
台帳の鮮度（`OlivaresAuditCheckpointStale`、経過時間 > 2h）です。

バーンレートの設定例は 30 日の基準期間を使用しますが、上記の可用性目標は 28 日です。
アラートのレートと評価窓を、この基準期間と区別してください。14.4× を 1 時間、6× を 6 時間、
1× を 3 日間継続すると、28 日のバジェットをそれぞれ約 2.14%、5.36%、10.71% 消費します。
同梱アラートの設定値は変更していません。

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

Kubernetes では、チャートの `ServiceMonitor` オプションが Prometheus オペレーター向けに
スクレイプを配線します。外部からの `/readyz` プローブ用の Gatus ステータスページ設定が、
ルールと並んで同梱されています（`deploy/monitoring/status-page.gatus.yaml`）。

## アラートが発火したら

症状ごとの診断 — ストアダウン、インジェスト p99 高、バス飽和、チェックポイント陳腐化 — は
[トラブルシューティングページ](/ja/how-to/troubleshooting/) にあります。これはアラートのアノテーションが
参照するのと同じランブックから抽出されています。
