---
title: "レシピ: deny-closed ポリシー (Cedar / OPA)"
description: >-
  制限専用のポリシー決定点 (PDP) を組み込む: Cedar の forbid オーバーレイ、または
  permit-by-default の OPA ポリシーを、公開前に検証しドライランする —
  アクセスを縮小することだけができ、決して拡大できないポリシー。
sidebar:
  order: 1
---

**目的:** deny-by-default の RBAC の上に属性ベースの制限を追加する —
たとえば「ロールが何と言おうと、`secret` とタグ付けされたリソースには誰も触れない」。

頭に入れておくべき不変条件はただ一つ: PDP は **制限のみを行う**。
決定は RBAC ∩ ネイティブ ABAC ∩ 外部 PDP として合成される —
ポリシーはロールモデルが拒否したものを決して付与できない
([モデル](/ja/how-to/govern-and-approve/#ポリシーシームabacpdpは-restrict-のみ))。

## Cedar (組み込み、プライマリ)

エンジンを選択し、ポリシーファイルを指定してから再起動する:

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

Cedar ポリシーは **forbid オーバーレイ** である — ベースの permit は
「RBAC はすでに決定済み」を表し、`forbid` ルールがそこから差し引く:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

アダプターに対して検証済みの作成上の事実が二つ: `resource.kind` と
`resource.sensitivity` は決定入力に常に存在する
(無条件で参照可能)。それ以外の属性は `has()` でガードしなければ
ルールが一致できない。あなたが書く `permit` は決して決定を拡大できない。

## OPA (HTTP 経由)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Rego は **permit-by-default** で記述する:

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = 制限なし。`false`、結果の欠落、または **あらゆるトランスポート上または
非 2xx のエラーは fail closed する** — リクエストは拒否され、決して
気づかぬうちにガバナンスを外れることはない。

## 検証、ドライラン、公開

ガバナンスモジュールはポリシーのライフサイクルを公開しており、不正なポリシーが
盲目的に投入されることはない:

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` はデプロイされている内容を一覧表示する;
`POST /v1/m/governance/pdp/explain` は決定を説明する。

## 安全性プロパティを検証する

- **不正な** ポリシーファイルで再起動する: エンジンは外部 PDP のみを
  無効化してログに記録する — RBAC とネイティブ ABAC はガバナンスを継続し、
  control plane はダウンしない。
- PDP が適用するすべての制限は **監査される** — 拒否されたリクエストの後に
  ledger を確認すること。

## 注記

- ポリシーはバージョン管理されて公開されるものであり、本番でホット編集される
  ファイルではない — 公開はレビュー済みの変更として扱うこと。
- 拒否ではなく承認ゲート付きのアクションについては、
  [HITL 承認](/ja/how-to/cookbook/hitl-approvals/) を参照。
