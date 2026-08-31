---
title: "レシピ: human-in-the-loop 承認"
description: >-
  破壊的なアクションをガバナンスされた承認の背後にゲートする: 正確なプランに
  紐づけたリクエストを開き、職務分掌と有効期限をサーバー側で強制した上で
  権限を持つ人間に判断させ、その決定を ledger に記録させる。
sidebar:
  order: 3
---

**目的:** 「デプロイの apply (またはオーケストレーションの発火、あるいは
ボイスセッションの開始) は、リクエスト者 *ではない* 人間が承認するまで
起こらない — そしてその決定は記録された事実である。」

承認エンジンはデフォルトのバイナリで稼働している;
[ガバナンスモデル](/ja/how-to/govern-and-approve/#human-in-the-loop-の運用形態)
がその姿勢を説明する。このレシピは運用上の配線である。

## 1. 承認ゲートを配線する

インフラを変更し得るモジュールアクションは human-in-the-loop ブリッジを
通過する。これは設定によって有効化される — それなしではこれらの
アクションは deny-closed のままである:

```bash
OLIVARES_APPROVAL_BRIDGE_CONFIG=/etc/olivares/approval-bridge.json
```

承認を *開く* コンポーネントは、**承認者プールに決して属さない、それ自身の
サービスアカウント** として実行すること。職務分掌はエンジン側で強制される
(開いた者は自分自身のリクエストを判断できず、システムトークンはそもそも
承認できない) — もし開く側のアカウントが承認者でもあるなら、それは
コントロールではなく liveness デッドロックを作ったことになる。

## 2. リクエストを開く

```bash
curl -ks -X POST "$BASE/v1/m/governance/approvals" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "subject_kind": "deployment",
    "subject_ref": "deploy:payments-api",
    "action": "deploy.apply",
    "reason": "rollout v2.4.1",
    "expires_in_seconds": 3600
  }'
```

リクエストは **deny-closed かつ時間制限付き** で開かれ、それがカバーする
正確なプランに紐づけられる。有効化された承認 *ポリシー* が
`(action, subject_kind)` に一致する場合、そのポリシーの `required_approvals`
が権威を持つ — リクエスト者がリクエスト側から基準を下げることはできない。

## 3. 判断する

```bash
# The queue (filter by status / action):
curl -ks "$BASE/v1/m/governance/approvals?status=pending" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT"

# The decision (approval-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/approvals/$ID/decisions" \
  -H "Authorization: Bearer $APPROVER_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"decision":"approve","note":"reviewed the plan hash"}'
```

エンジンがサーバー側で強制する内容 — これらはどれもクライアントの慣習ではない:

- **職務分掌:** 判断者は安定したユーザー ID を鍵とする;
  リクエスト者は判断できず、同じ人間が二度判断することはできない
  (UI のルールではなく一意インデックス)。
- **有効期限:** 期限切れのリクエストは、sweeper が状態を具現化する前であっても、
  決して拘束力のある決定を受け取れない。
- **リスク層のフロア:** あらかじめ CRITICAL に分類されたアクション
  (kill-switch ファミリー、クレデンシャルのファイナライズおよびその類)
  は、**決定ごとに、強力 (AAL3) 認証を伴う相異なる少なくとも二人の人間の
  承認者** を必要とする — そしてこのフロアは構造的である:
  層を引き下げようとする承認ポリシーは、決定点で再びフロアが課される。

## 4. 記録

すべての決定は、同一トランザクション内で実際のアクターとともに監査 ledger に
追記される — `GET /v1/m/governance/approvals/{id}/decisions` が不変の追跡記録であり、
[pull エクスポート](/ja/how-to/forward-audit-to-splunk/) がそれを SIEM へ運ぶ。
ledger がひそかに忘れるようなガバナンスされた変更を行うことはできない。

## 注記

- `escalate_in_seconds` は、リクエストが未判断のまま残ると SoD チームに
  通知する — 本番クリティカルなアクションに使うこと。
- キャンセル (`POST …/{id}/cancel`) は、保留中のリクエストに対する
  リクエスト者または管理者のためのものである; これも記録される。
- まだ成熟途上にあるのは、より充実したレビュー用 **コンソール** である;
  上記のエンジン側の保証は稼働している
  ([正直なスコープ](/ja/how-to/govern-and-approve/))。
