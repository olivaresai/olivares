---
title: "レシピ: least-privilege drift をトリアージする"
description: >-
  Permitted-vs-Observed の結果をゼロまで処理する: 予期しないアクセス、未使用の
  grant、reconciliation 保留中のエッジを分類し、それぞれを判断し (grant、revoke、
  または identity を修正)、再確認する — 単一のヒントも信用せずに。
sidebar:
  order: 4
---

**目的:** drift の結果 — エージェントが *できる* ことと、実際に *観測された*
こととのギャップ — を、diff が静かになるまで、一定の周期で判断に変えていく。

## 1. drift を取得する

```bash
curl -ks "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

(または、PR でのレビュー用に HCL で: Terraform データソース
`olivares_access_edges` に `include_drift = true` を指定 —
[コードとして管理する](/ja/how-to/manage-as-code/)。)

結果には三つのクラスがあり、それぞれ異なる問題である:

| クラス | 意味 | 問うべき問い |
|---|---|---|
| **Unexpected access** | 観測されたが、どの grant もそれをカバーしていない | これは欠落した grant か、それとも本物の違反か? |
| **Unused grant** | 付与されたが、行使が一度も観測されていない | なぜこの権限が存在するのか? |
| **Reconciliation pending** | 観測されたが、エージェント↔identity のリンクが未解決 | これは identity の問題であり、(まだ) セキュリティの問題ではない |

## 2. 各クラスをトリアージする

**Unexpected access** — 行動する前にエッジの honesty 軸を読む:

- `attribution_tier: firm` + `coverage_tier: clean` は得られる中で最も
  高品質な finding である: 特定の identity が特定のリソースに触れ、
  そのストア自身の監査がそれを分類した。判断する: 正当であれば grant
  (ポリシーまたは binding) を宣言してマップが意図を反映するようにする;
  そうでなければ基底のアクセスを revoke し、インシデントとして扱う。
- `approximate` の attribution は *アクセス* が起きたことは意味するが、
  *誰が* は共有クレデンシャルであることを意味する。「どのエージェントだったか」
  に調査を費やしてはならない — 永続的な修正は
  [エージェントごとの identity](/ja/how-to/connectors/sso-scim-identity/) であり、
  それまではエッジは証明できないことを正直に述べる。
- `mcp_annotation` のヒントのみに依拠するエッジは **証拠ではない** —
  そのヒントは仕様上信用されていない。何かを判断する前に、観測された
  ソースで裏付けを取ること。

**Unused grants** はタダで見つかる過剰プロビジョニングである: それぞれが
revoke の候補だが、観測の不在が意味を持つのはカバレッジが存在する場合に
限るという但し書きがある — 喜ぶ前にリソースのカバレッジ層を確認すること
([階層化されたカバレッジ](/ja/how-to/connect-a-source/#段階的なカバレッジ--現実的であること))。

**Reconciliation pending** は identity のバックログへ回る: そのクレデンシャルを
バインドすべき roster ソースを組み込むか修正すれば、次のパスでエッジは解決する。

## 3. 判断し、記録し、再確認する

判断はそれがガバナンスされる場所で下す: grant をコードとして宣言する
([Terraform](/ja/how-to/manage-as-code/)) か、ガバナンスされた API 経由で宣言し、
リスクの高い方向は [承認](/ja/how-to/cookbook/hitl-approvals/) の背後にゲートし、
誰が何を判断したかを ledger に記録させる。それから drift を再取得する:
reconcile されたエッジは diff から外れ — 本物のギャップだけが残る。その収束こそが
眼目である; デモ estate がそれを小規模に示している
([quickstart](/ja/start/quickstart/))。

コンソールでは、**Access map** の *Permitted vs observed* パネルがこのレシピを
ライブで描画したものである。

## 周期

drift のトリアージは、短い週次ループに加えて、高シグナルのクラス
(firm + clean の予期しない書き込み) 向けのアラート経路として機能する。
それらの finding は週次パスを待たずに、
[通知先](/ja/how-to/forward-audit-to-splunk/) 経由で on-call に回すこと。
