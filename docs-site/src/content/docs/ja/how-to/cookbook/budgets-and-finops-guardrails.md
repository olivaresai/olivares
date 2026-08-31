---
title: "レシピ: 予算と FinOps ガードレール"
description: >-
  AI の支出にハードな金額上限を設定する——モデル、チーム、ワークスペース、あるいは
  単一のアイデンティティごとに: しきい値でアラートを出し、上限でスロットルまたは
  ブロックする。さらに、支出に分母を与えるコスト・パー・アウトカムも。
sidebar:
  order: 2
---

**目標:** 「このチームのエージェントは月 $500 で支出を止める」——一度宣言すれば
ライブで強制され、上昇途中のしきい値でアラートが出る。

予算の強制は、**デフォルトバイナリでライブ**になっているアクチュエーションの
1 つだ: 上限に達した強制予算は、追加のプロビジョニングなしに支出を拒否する
（[モジュールカタログ](/ja/reference/modules/overview/)はこれを `v1 | v1` と
マークしている）。

## 予算を作成する

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **金額はマイクロ USD** だ（`limit_micro_usd: 500000000` = $500）。これにより
  契約上の浮動小数点の曖昧さがない。
- **`dimension` + `key`** が予算のスコープを定める。スコープ対象の dimension には
  `global`、`model`、`provider`、`agent`、`session`、`team`、`project`、
  `workspace`、`api_key`、`actor`、`service_tier`、`context_window`、
  `inference_geo`、`gateway`、`identity` が含まれる。
- **`action`** は強制モードだ:

| `action` | 上限到達時 |
|---|---|
| `alert`（デフォルト） | ショーバックのみ——アラートは発火するが、何も拒否されない |
| `throttle` | アクチュエーションシームが新規支出を減速させる |
| `block` | アクチュエーションシームが新規支出を拒否する |

## 単一のアイデンティティに予算を設定する

`dimension: "identity"` は、**確かなロスターアイデンティティの external id** に
スコープを定める——[アイデンティティソース](/ja/how-to/connectors/sso-scim-identity/)
が登録したワークロードまたはエージェントのアイデンティティだ:

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

アイデンティティは、コスト取り込み時にサンプルのエージェントバインディング、
API キー、または actor から解決される——そのため予算は、1 つの API キーではなく
複数の面にまたがってアイデンティティに追随する。

## 動作を確認する

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

上限に達すると、強制予算のチェックは `allowed: false` を、アクション
（`throttle` または `block`）と発火した予算とともに返す——拒否はその理由を
名指しする。アラートは通知ストリームにも乗るため、Slack や PagerDuty の
[デスティネーション](/ja/how-to/forward-audit-to-splunk/)が 100% の拒否より前に
80% の到達を知る。

コンソールでは、**Cost & FinOps** が dimension 別の支出を予算ステータスと
インラインで表示する:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="支出トレンドと予算態勢を表示する Cost & FinOps ビュー。" />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="支出トレンドと予算態勢を表示する Cost & FinOps ビュー。" />

## 支出に分母を与える: アウトカム

コスト・パー・アウトカムこそが、予算をビジネスの会話にするものだ。アウトカム
（解決したチケット、マージされた PR、クローズしたケース）を報告し、価値パネルを
読む:

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

価値サマリーには**キャンセルリスク**——アウトカムのないバーン——が含まれる。
これは成功指標の正直な逆数だ。

## ノート

- **意図的なフェイルオープン:** 予算チェック自体がエラーになった場合
  （FinOps の読み取り失敗）、推論は無言でブロックされるのではなく許可される
  ——壊れたメーターが障害になってはならない。その失敗はログに記録され、
  可視化される。
- 予約済みキャパシティ（`reserved_micro_usd`）は上限に算入されるため、
  事前予約によって予算を回避することはできない。
- `cost_type` は意図的に予算の dimension に**なっていない**——見積もりフォール
  バックの行は、並行プールを形成するのではなく、本来属する dimension に乗る。
