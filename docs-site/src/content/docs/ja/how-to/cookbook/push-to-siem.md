---
title: "レシピ: 検出結果と台帳を SIEM へプッシュする"
description: >-
  プッシュシンク（Splunk HEC、Microsoft Sentinel、Datadog、New Relic、
  または汎用の HMAC 署名付き Webhook）を作成し、検出結果と封緘済み監査台帳を
  サブスクライブして、OCSF・CEF、あるいは運用基盤が解釈する形式で
  少なくとも1回（at-least-once）配信する。
sidebar:
  order: 6
---
SIEM and ITSM push, OTLP downloads, external trace and metric delivery, and posture export require Business. Community keeps local observability, trace-context propagation, saved settings and `olivares dr backup`. Generic chat, email and webhook notifications remain available in Community.


:::note[Business]
監査エクスポート（`GET /v1/audit/export`、`olivares audit export`）、ディレクトリアーカイブ、外部アーカイブの検証には Business が必要です。Community では署名付き台帳、`olivares audit verify`、`olivares dr backup` を引き続き利用できます。エクスポートは HTTP 501 または終了コード 9 を返します。監査転送および監査セグメントを含む DDIL 転送にも Business が必要です。
:::

**目的:** ファイルを追従するフォワーダーを使わずに、control plane の検出結果 *と*
改竄検知可能な監査台帳をプッシュ配信で SIEM に受信させる。

これはイベンティングプラットフォーム上の S2S（サービス間）プッシュ経路です。
[プル方式のエクスポートとファイル追従の運用形態](/ja/how-to/forward-audit-to-splunk/) は
引き続き完全にサポートされます。プルは WORM アーカイブとオフライン再検証に適した形であり、
プッシュはライブの SIEM 取り込みに適した形です。

## 1. シンクサブスクリプションを作成する

まず選択可能な台帳フォワーダーを有効にします。依存する eventing も有効になります:

```bash
olivares modules on siemforward
```

エンジンの再起動が完了するまで待ち、`olivares modules ls` を実行してください。
サブスクリプションを作成する前に、`siemforward` と `eventing` が稼働中であることを確認します。

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

- **`sink_kind`** は運用基盤の方言を選択します: `splunk_hec`、`sentinel_dcr`、
  `datadog`、`newrelic` ── または完全に省略すると **汎用 Webhook** になります
  （JSON イベントを受信する HTTPS エンドポイントで、エンジンの HMAC 署名で認証されます。
  `…/{id}/rotate-secret` でローテートします）。
- **`sink_format`**: `ocsf`（SIEM シンクのデフォルト ── AI を意識したスキーマ）、
  `cef`、`leef`、`syslog`、`otlp`、`otlp_envelope`、`json`。

  :::caution[`sink_format` には `sink_kind` が必要]
  フォーマットはシンク種別が設定されている場合にのみ適用されます。**`sink_kind` の省略は
  「HTTPS を選ぶこと」ではありません** — 汎用 Webhook が選ばれ、Olivares のイベント JSON が
  送信され、`sink_format` は検証すらされません。自前のエンドポイントへ SIEM 方言を POST する
  には `sink_kind: "https"` を明示してください。

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  `otlp`（およびその厳密なエイリアス `otlp_envelope`）ではエンドポイントをコレクターの
  `/v1/logs` に正確に合わせる必要があります（本文は URL にそのまま POST されます）。
  :::
- **`sink_cred`**（HEC トークン / DCR ベアラ / API キー）は一度だけ受け付けられ、
  **保管時に封緘され、返却もログ出力もされません**。ベンダー種別では作成時に必須です。
  汎用 Webhook では不要です。
- **`event_types`** はストリームの選択です: 検出結果レールには `finding.reported`、
  台帳には `audit.recorded`（下記参照）、あるいはその両方を指定します。

信頼する前に配信をテストしてください:

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. 台帳プッシュ、正直に説明すると

**`siemforward` が有効な場合**、台帳ポンプは各テナントのカーソルから封緘済み台帳を辿ります。
**`audit.recorded`** サブスクリプションは、作成後に永続的な配信エンジンへ投入されたレコードを
**少なくとも1回**配信し、
再開可能です。
各レコードはチェーン整合性フィールドをそのまま保持するため、SIEM 側のコピーはプル方式の
エクスポートが許すことを正確に許し、それ以上ではありません。すなわち連鎖のリンク
（n+1 の `prev_hash` が n の `hash` に等しいこと）と `hash` に対するチェックポイント
署名はオフラインで検証できます。さらに、レコードの `hash` は **1 行の**
エクスポート内容から再導出できるようになりました ── 正規の `occurred_at` テキストと
メタデータ・コミットメントを含め、チェーンハッシュの入力はすべて配線上を流れます。
このコミットメントはレコードごとにブラインドされているため、背後のメタデータを一切
明かさずにプリイメージを完成させます。3 つの主張は区別したままです ── ハッシュの
再導出は、AUTHENTICITY（外部で信頼された鍵が必要）でも COMPLETENESS（隣接レコードと
チェックポイントが必要）でもありません。監査*アーカイブ*は依然としてより強い成果物です。
メタデータ本体をそのブラインドとともに携えるため、あるコミットメントが**どの**
メタデータを覆っているかにも答えられます。

知っておく価値のある3つの性質:

- **サブスクリプションがなくてもカーソルは前進します。** `audit.recorded` の購読者がいない場合、
  配信はキューに入りませんが、有効なポンプはカーソルを保存します。
  新しいシンクはカーソルが通過済みのレコードを遡って配信しません。
  過去のレコードにはプルエクスポートを使用してください。
- **少なくとも1回は重複の可能性を意味します**（再配信時）。テナントごとに
  レコードのシーケンス番号で重複排除してください。
- **ポンプは HA でリーダーゲート方式**です ── ちょうど1つのノードだけが転送します。

## 3. ITSM: チケットとしての検出結果

同じサブスクリプション機構が、通知レール経由で ITSM 宛先を駆動します ──
検出結果から ServiceNow インシデントや Jira 課題を生成し、重大度を優先度にマッピングします。
これらは SIEM シンクではなく、通知 **宛先**（`servicenow` / `jira` の出力コネクタ）として
設定してください。
[Splunk ページの宛先テーブル](/ja/how-to/forward-audit-to-splunk/) にそのパターンが示されています。

## エンドツーエンドで検証する

1. `…/test` が配信済みを返す。
2. 観測可能な何か（[予算アラート](/ja/how-to/cookbook/budgets-and-finops-guardrails/) の
   しきい値、拒否されたツール）を起動し、検出結果が到着するのを確認する。
3. 台帳について: SIEM 側の `seq` ハイウォーターマークを
   `GET /v1/audit/export?from=<seq>` と比較する ── 両ストリームは一致しなければなりません。

## 注記

- エンドポイントは **HTTPS** でなければなりません。エンジンは平文シンクを拒否します。
- ポスチャースナップショット（コンプライアンス / NHI / 検出結果のロールアップ）は、
  同じレールに乗る独自のエクスポートモジュールを持っています ──
  [コンプライアンスモジュール](/ja/reference/modules/xiii-compliance/) を参照してください。
- 完全な判断表 ── いつプルし、いつ追従し、いつプッシュするか ── は
  [Splunk 転送ページ](/ja/how-to/forward-audit-to-splunk/) にあります。
