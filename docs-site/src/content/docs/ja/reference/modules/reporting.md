---
title: "Reporting — プロフェッショナルな HTML/PDF レポート"
description: >-
  プラットフォームのコンプライアンス、監査、FinOps データからダウンロード可能な
  HTML/PDF レポートを生成します。5 種類の組み込みレポートをオンデマンドで提供し、
  スケジュールレポートは Business に含まれます。
---

Reporting（`modules/reporting`）は **LIVE** です。プラットフォームのコンプライアンス、
監査、FinOps データを 1 つのプロフェッショナルな文書にまとめ、監査担当者が複数の API
から JSON をコピー＆ペーストする代わりに、証拠をダウンロードできるようにします。

**エディション:** Business Compliance Packs は、フレームワークカタログ、評価、規制カレンダー、DORA/HIPAA ビュー、エビデンスの封印、OSCAL エクスポート、オンデマンド HTML/PDF レポートを提供します。Community はこれらの機能に `501` を返し、リスク、データ所在地、記録管理、保存済みエビデンスの JSON/CSV エクスポートを維持します。アップグレードで既存の記録は保持されます。

## 組み込みレポート

Business Compliance Packs は、次の 5 種類をオンデマンドで提供します。

- `compliance-evidence` — フレームワーク別のコンプライアンス状況、統制の状態と証拠。
- `audit-summary` — 監査イベントの集計と ledger の完全性検証。
- `finops-report` — モデル別・プロバイダー別の AI 支出。
- `access-review` — 定期レビュー向けのユーザーおよびアクセスデータ。
- `executive-summary` — ガバナンス、リスク、コスト、導入状況の簡潔な概要。

`GET /v1/m/reporting/reports` は種類と形式を一覧表示します。
`GET /v1/m/reporting/reports/{type}` で生成し、既定は HTML、
`?format=pdf` で PDF をダウンロードします。ルートには
`reporting:report:read` が必要です。

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
- [モジュールカタログ](/ja/reference/modules/overview/) — 接続済み 32 モジュールと正直な成熟度。
