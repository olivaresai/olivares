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
| `olivares_auth_login_attempts_total{outcome}` | ログインの成功 / 失敗 / ロックアウト |
| `olivares_http_ratelimit_decisions_total{decision}` | レートリミットの圧力 |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | コレクター→コアのインジェストプレーン |

## SLO ターゲット（公開済み、正直）

シングルノードのターゲット — デフォルトのトポロジーが実際に支えられるもの — と
HA ティアです。

| SLI | シングルノード | HA ティア（Postgres） |
|---|---|---|
| 可用性（`/readyz`） | **99.5% / 28d** | 99.9% / 28d |
| リクエスト成功（非 5xx） | **99.9%** | 99.95% |
| API レイテンシ p99 | **< 300 ms** | < 200 ms |
| インジェストレイテンシ p99 | **< 250 ms** | < 150 ms |
| インジェスト成功 | **99.9%** | 99.95% |

数値における誠実さ：1 ノード上の単一ライターは可用性のスリーナイン（99.9%）を約束できないので、
ドキュメントもそう約束しません。99.5%（28 日あたり約 3 時間 39 分のバジェット）がシングルノードの真実であり、
99.9% のティアは楽観ではなく [HA トポロジー](/ja/tutorials/getting-started/kubernetes/#3-active-passive-ha)
によって獲得されます。

## 同梱のアラートルールを読み込む

`deploy/monitoring/olivares-slo.rules.yaml` は、Prometheus にすぐ使える 14 個のアラートを同梱しています。
リクエスト成功バジェットに対するマルチウィンドウのバーンレートアラート（高速 14.4× ページ /
中速 6× ページ / 低速 1× チケット）、絶対的なレイテンシと可用性の発火（`OlivaresIngestP99High`、
`OlivaresApiLatencyP99High`、`OlivaresStoreDown`、`OlivaresControlPlaneUnscrapeable`）、飽和
（`OlivaresEventBusSaturated`、キュー >90% が 10 分継続）、ブリッジの健全性
（`OlivaresEventBusBridgeDropping`、`OlivaresEventBusBridgeDisconnected`）、そして
台帳の鮮度（`OlivaresAuditCheckpointStale`、経過時間 > 2h）です。

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
