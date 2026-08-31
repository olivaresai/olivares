---
title: Splunk へ転送する（Universal Forwarder + tail を配置）
description: >-
  control plane のガバナンス検出結果と改竄検知可能な監査台帳を、ネイティブな
  Splunk-to-Splunk エミッタなしで、Universal Forwarder にファイルを追従させることで
  Splunk に取り込む。どのストリームがどれかを正直に。
---

Olivares AI のデータは、ネイティブコネクタを待たずに **今すぐ** Splunk に取り込めます:
データをファイルに書き出し、そこに **Splunk Universal Forwarder（UF）** を向けるだけです。
UF が Splunk-to-Splunk（S2S）でインデクサーへのホップを処理します。

:::caution[ネイティブな Splunk S2S エミッタは存在しません]
Olivares AI は Splunk 独自の S2S フォワーダープロトコルを **実装していません**。ネイティブな
S2S エミッタは v1 以降です。サポートされる運用形態は **ファイル追従転送**
（UF が Olivares の書き出すファイルを追従）、**プル方式エクスポート**（WORM アーカイブと
オフライン再検証向け）、そして **Splunk HEC 経由の HTTP プッシュ** です ── これには、
SIEM 相互運用の作業以降、イベンティングシンク経由での **台帳そのもの** のプッシュも含まれます
（[SIEM へプッシュする](/ja/how-to/cookbook/push-to-siem/)）。本ページはファイルとプルの経路を
説明します。プッシュはレシピが扱います。
:::

**2つの異なるストリーム** があり、それらは同じものではありません。意図的に選んでください:

| ストリーム | 何であるか | Splunk への経路 |
|---|---|---|
| **ガバナンス / 検出結果** | モジュール IX がルーティングする通知ストリーム（ヘルス、支出、セキュリティ、コンプライアンスの検出結果） | `filelog` 出力コネクタがファイルに追記する。または `splunkhec` がプッシュする。または `finding.reported` をサブスクライブした [イベンティングシンク](/ja/how-to/cookbook/push-to-siem/) |
| **改竄検知可能な監査台帳** | 追記専用・ハッシュチェーン・署名済みの監査証跡 | **プル** エクスポート `GET /v1/audit/export`（本ページ）。または **プッシュ** ポンプ ── `audit.recorded` をサブスクライブしたイベンティングシンクで、少なくとも1回配信。ネイティブな *ファイル* シンクは存在しない。下記のスケジュール済みエクスポートでファイルを生成すること |

## ストリーム A ── 検出結果、`filelog` コネクタ経由

`filelog` 出力コネクタは、通知 / 検出結果ストリームを **1レコード1行** でファイル
（または `stdout`/`stderr`）に追記し、UF がこれを追従できます。種別 `filelog` の通知宛先を
次のフィールドで設定してください:

| フィールド | 意味 |
|---|---|
| `path` | 追記先: ファイルパス、または `stdout`/`stderr`/`-` |
| `format` | 1行ごとの形式: `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim`（デフォルト `json`） |
| `hostname` | syslog の `HOSTNAME` フィールド（`syslog` 形式向け） |
| `fsync` | 各レコードをディスクへフラッシュする（WORM コピー向けの耐久性。低速） |

Splunk には、`format: json`（リッチなフィールド）でも `format: cef`/`syslog`（Splunk が
ネイティブに解析する行形式）でもどちらも機能します。ファイルは追記専用で開かれるため、
WORM ストレージに置けば同じファイルが不変の外部コピーを兼ねます。

:::note[`filelog` は検出結果を運び、署名済み台帳は運ばない]
`filelog` コネクタは **検出結果** ストリームを転送します ── 改竄検知可能な監査台帳を
見ることは決してありません。検証可能な台帳を転送するには、ストリーム B を使ってください。
:::

### ターンキーな代替手段: Splunk HEC

ファイルを追従するよりも HTTP でプッシュしたい場合、`splunkhec` コネクタが同じ検出結果
ストリームを `Authorization: Splunk <token>` ヘッダー付きで Splunk の HTTP Event Collector
（`/services/collector`）に POST します ── ターンキーな HTTP 経路ですが、依然として S2S ではなく、
依然として台帳ではなく検出結果ストリームです。

## ストリーム B ── 改竄検知可能な台帳、プル方式エクスポート経由

監査台帳は、エンジンが自ら書き出すファイルではなく、**認証付きのプル方式エクスポート** として
公開されます。各レコードはチェーン整合性フィールド（`seq`、`prev_hash`、`hash`、`sig`）を保持するため、
SIEM が **ハッシュチェーンをオフラインで再検証** できます。PII がエクスポートされることはありません。

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

サポートされる `format` 値は `cef`、`leef`、`syslog`、`otlp`、`otlp_envelope`、
`otlp_log_record`、`ocsf` です。`otlp` はレコードごとに完全な、そのまま POST できる
OTLP/HTTP エクスポートリクエストで、`otlp_envelope` はその厳密なエイリアス、
`otlp_log_record` は素の LogRecord 射影（1行1 LogRecord）です。行形式
（`cef`/`leef`/`syslog`）は `text/plain` でストリーミングされ、`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` は NDJSON
（`application/x-ndjson`）として1行1 JSON オブジェクトでストリーミングされます。

:::note[`ocsf` は OCSF v1.8.0 API Activity]
本ページの以前の版では、エンジンのエラーテキストが広告するリストから `ocsf` を省いていると
記していました ── そのギャップは上流で修正済みで、サマリーとバッドリクエストメッセージの
両方がエンジンの形式レジストリから生成されるため、受け付ける形式を常にすべて列挙します。
:::

### カーソルによる増分追従

エクスポートは `?from=` でシーケンス番号によりギャップのないチェーンをページングします。
UF が追従するファイルを継続的に追記し続けるには、最後に見たシーケンスから再開する小さな
スケジュール済みジョブを実行してください:

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

各エクスポートは完了ターミネータで終わります ── テキスト形式では
`# olivares-audit-export-complete count=N last_seq=M` コメント、
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` では
`{"export_complete":true,...}` JSON 行です。**その不在はストリームが切り詰められたことを
意味します** ── 欠けている場合はカーソルを進めないでください。

## Universal Forwarder をファイルに向ける

どのストリームを選んだとしても、ホストに Splunk UF をインストールし、`monitor://` 入力を
追加します。Olivares AI は `inputs.conf` を同梱しません ── 次のスタンザをあなたが追加します:

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

UF は S2S でインデクサーへ転送します。Olivares AI 自身が S2S を話すことは決してありません。

## サポート対象とそうでないもののまとめ

- **サポート対象：** ファイル追従転送（UF がファイルを追従）── 両ストリーム向け。
- **サポート対象：** Splunk HEC プッシュ ── 検出結果ストリーム（`splunkhec` 宛先）向け、**および**
  イベンティング **シンク** 経由での台帳と検出結果向け
  （`sink_kind: splunk_hec`、イベント `audit.recorded` / `finding.reported`、少なくとも1回）
  ── [SIEM へプッシュする](/ja/how-to/cookbook/push-to-siem/) を参照。
- **サポート対象：** オフライン台帳再検証 ── プル方式エクスポートとプッシュポンプの両方がハッシュチェーン
  フィールドをそのまま運ぶため、SIEM が整合性を再検証できます。
- **非対応：** ネイティブな Splunk S2S エミッタ ── 未実装（v1 以降）。
- **非対応：** 自動の台帳 *ファイル* シンク ── 台帳をローカルファイルに取り込むには、上記の
  スケジュール済みプルエクスポートで生成してください（プッシュポンプはファイルではなく HTTP
  シンクを対象とします）。
