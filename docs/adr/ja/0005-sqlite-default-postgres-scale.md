> 機械翻訳です。正式な情報源は英語版です。

# ADR-0005: デフォルトは埋め込み SQLite、スケール時は Postgres + RLS

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T4); data-model design

## 背景と課題

control plane はマルチテナントのデータモデルを格納する（access graph はその上の*ビュー*である）。
小規模／air-gap のインストールでは依存関係ゼロの単一バイナリとして動作しなければならず、それでいて
マルチホスト・マルチテナントのデプロイへとスケールできなければならない。

## 意思決定の要因

- 単一バイナリ／air-gap の経路のための、外部依存関係ゼロ。
- スケール時の強力なマルチテナント分離。
- pure-Go の静的バイナリを維持するための、CGO なし。

## 検討した選択肢

- **SQLite（pure-Go） → Postgres + row-level security。**
- access graph のための **グラフデータベース**（Neo4j、Dgraph）。

## 決定の結果

選択した選択肢：シングルノードおよび air-gap には **埋め込み SQLite**（`modernc.org/sqlite`、
pure-Go、CGO なし）。マルチホスト・スケール・マルチテナントには、`tenant_id` をキーとする
**row-level security** を備えた **Postgres**（`pgx` 経由）。access graph は別個のストアではなく、
**一般データモデル上のビュー**としてモデル化される。

### 結果として生じること

- **良い点：** 単一バイナリにはインストールすべきデータベースがない。同じモデルが、テナントごとの
  RLS 分離を伴って Postgres へとスケールする。
- **悪い点／トレードオフ：** サポートすべきストレージバックエンドが 2 つになる。RLS の正しさは
  テストされなければならない（実際にテストされている。CI 内で forced RLS のもとで）。
- **中立的な点：** access graph はビューであるため、専用のグラフエンジンを必要としない。

## 代替案を却下した理由

- **グラフデータベース** — セルフホストには重く、過剰である。access graph はリレーショナルモデル上の
  ビューであり、専用のグラフエンジンを必要とするワークロードではない。
