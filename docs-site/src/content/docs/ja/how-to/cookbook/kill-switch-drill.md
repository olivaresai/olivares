---
title: "レシピ: estate キルスイッチ (とそのドリルの仕方)"
description: >-
  一度の呼び出しで estate 内のガバナンスされたすべての作動を — あるいは
  一つのエージェントを — 停止する。設計上すばやく作動できる; 再有効化には
  二人の人間が必要で、インシデントは証拠パックを残す。必要になる前にドリルせよ。
sidebar:
  order: 5
---

**目的:** エージェントがマシンの速度で暴走したとき、それを — あるいはすべてを —
*今すぐ* 認証付きの一度の呼び出しで停止し、後に dual control の下で停止を解除し、
インシデント全体を記録に残す。

この非対称性が設計である: **作動はすばやい** (admin 層、承認ゲートなし —
緊急停止がキューで待たされることは決してあってはならない)、
**再有効化は遅い** (相異なる二人の人間、そしてインシデントは事後レビュー用の証跡パックを残す)。
停止の周りには意図的に break-glass を設けていない: 停止された状態 *こそが*
安全な状態である。

## 作動させる

```bash
# Stop the whole estate:
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"scope_kind":"estate","reason":"runaway agent incident #1234"}'

# Or stop one agent (by UUID or external id):
curl -ks -X POST "$BASE/v1/m/governance/killswitch" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"scope_kind":"agent","scope_ref":"agent:billing-reconciler","reason":"…"}'
```

即座に、かつ fail-closed で停止するもの: ガバナンスされた **作動** 面 —
`claude.tool.use`、`mcp.tool.call`、`deploy.apply`、`deploy.retire`、
`orchestration.schedule.fire`、`voice.session.open`。スコープ内の保留中の
作動承認は **同一トランザクションでキャンセルされる** ため、承認済みだが
未実行のものが停止後にすり抜けることはない。

意図的に停止 *しない* もの: 観測、およびガバナンスそのもの
(findings、identity ライフサイクル、コンプライアンス) — 停止中でも
依然として閲覧およびガバナンスが可能である。すでに停止済みのスコープを
再作動させると `409` が返る (スコープに対して冪等であり、スタックではない)。

```bash
# Live posture — is anything stopped right now?
curl -ks "$BASE/v1/m/governance/killswitch/state" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Guardian ルールは、containment ルールが発火したときに同じ停止を自動で
作動させることができる (`stop_agent` / `stop_estate` アクション) —
自動経路と人間経路は同じゲートであり、自動停止は CRITICAL finding を発する。

## 再有効化 (dual control)

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/reenable" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"reason":"root cause fixed: …"}'
```

これは **承認を開く** のであって、決して停止を直接解除しない。このアクションは
あらかじめ CRITICAL に分類されている: **相異なる二人の人間の承認者**、
決定ごとに強力 (AAL3) 認証 — そして二人の人間というフロアは構造的であり、
承認ポリシーが層を引き下げようとしてもトランザクション内で強制される。
リクエスト者は判断者になれない; 拒否または期限切れのリクエストは
新たな定足数を開く。

再有効化の後、さらに別の人間 (作動させた者、リクエスト者、*および*
再有効化した者とは別の人) による **事後レビュー** がインシデントを締めくくる —
それが記録されるまでは、同じスコープをレビューなしで再び
停止して再有効化することはできない:

```bash
curl -ks -X POST "$BASE/v1/m/governance/killswitch/$STOP_ID/review" … 
curl -ks "$BASE/v1/m/governance/killswitch/$STOP_ID/evidence"   # the evidence pack
```

evidence エンドポイントはインシデントのパックを返す — 停止、キャンセルされた
承認、決定、そして追跡記録 — 監査人に提出できる状態で。

## コンソール

Management セクションの **Kill switch** は同じゲートのワンクリック版であり、
ライブ状態と再有効化フローを備えている:

<img class="light:sl-hidden" src="/console/killswitch-dark.png" alt="Kill switch コンソールビュー: estate の状態と停止ごとの履歴。" />
<img class="dark:sl-hidden" src="/console/killswitch-light.png" alt="Kill switch コンソールビュー: estate の状態と停止ごとの履歴。" />

## ドリルする

一度も引いたことのないキルスイッチは仮説にすぎない。四半期ごとに、
メンテナンスウィンドウで:

1. 影響の小さいエージェントに対して **agent スコープ** の停止を作動させる;
   そのツール呼び出しが拒否され、finding が発火することを検証する。
2. 再有効化を一通り歩く: 二人の承認者、事後レビュー、証拠パックの取得と
   アーカイブ。
3. ループ全体を端から端まで計時する — その数値があなたの本当の containment
   レイテンシであり、ドリルはそれを示す完全な ledger の追跡記録を残す。
