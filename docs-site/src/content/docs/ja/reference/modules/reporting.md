---
title: "Reporting — プロフェッショナルな HTML/PDF レポート"
description: >-
  プラットフォームのコンプライアンス、監査、FinOps データからダウンロード可能な
  HTML/PDF レポートを生成します。5 種類の組み込みレポートをオンデマンドで提供し、
  スケジュールレポートは enterprise add-on として提供します。
---

Reporting（`modules/reporting`）は **LIVE** です。プラットフォームのコンプライアンス、
監査、FinOps データを 1 つのプロフェッショナルな文書にまとめ、監査担当者が複数の API
から JSON をコピー＆ペーストする代わりに、証拠をダウンロードできるようにします。

## 組み込みレポート

open-core モジュールは、次の 5 種類をオンデマンドで提供します。

- `compliance-evidence` — フレームワーク別のコンプライアンス状況、統制の状態と証拠。
- `audit-summary` — 監査イベントの集計と ledger の完全性検証。
- `finops-report` — モデル別・プロバイダー別の AI 支出。
- `access-review` — 定期レビュー向けのユーザーおよびアクセスデータ。
- `executive-summary` — ガバナンス、リスク、コスト、導入状況の簡潔な概要。

`GET /v1/m/reporting/reports` は種類と形式を一覧表示します。
`GET /v1/m/reporting/reports/{type}` で生成し、既定は HTML、
`?format=pdf` で PDF をダウンロードします。ルートには
`reporting:report:read` が必要です。

## Open core と enterprise

オンデマンド HTML は open-core バイナリに含まれます。オンデマンド PDF は Chromium
互換の実行ファイルが利用できる場合に含まれます。**Enterprise add-on:** スケジュール
レポート生成は build tag でゲートされ、community runtime には含まれません。

## 境界と制限

- PDF 生成は Chromium を headless モードで起動します。`PATH` に `chromium`、
  `chromium-browser`、`google-chrome`/`chrome` のいずれもなければ PDF リクエストは `501` を返し、
  HTML は引き続き利用できます。
- compliance-evidence にはコンプライアンスデータソースが必要です。未接続の場合は証拠を
  捏造せず、文書に「Data source not configured」と明記します。
- このモジュールはプラットフォームが保持する既存データから文書を生成します。監査 ledger、
  コンプライアンス評価、FinOps の正本を置き換えるものではありません。

## 関連項目

- [コンプライアンスと規制](/ja/reference/modules/xiii-compliance/) — 状況と証拠のデータソース。
- [コストと AI FinOps](/ja/reference/modules/xi-finops/) — 支出の正本。
- [モジュールカタログ](/ja/reference/modules/overview/) — 接続済み 30 モジュールと正直な成熟度。
