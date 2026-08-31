---
title: "モジュール XV — 出力統合と通知"
description: >-
  control plane の通知ルーター。どのシグナルを、誰に、どのチャネルで、いつ
  届けるかを判断し、秘匿化済みの結果を出力コネクター — Slack/Teams、PagerDuty/Opsgenie、
  署名付き webhook、SIEM — を通じてディスパッチする。実証されたエンドツーエンドの
  作動シームであり、deny-closed をデフォルトとし、証跡台帳を備える。
---

モジュール XV は control plane の**通知ルーター**である。いずれかのモジュールが
アラートをイベントバス上の finding に変換すると、本モジュールはそれがどのテナント
ルートに一致するかを判断し、秘匿化済みの通知を構築し、重複やストームを抑制したうえで、
企業がすでに運用しているチャネルへ**ライブでディスパッチ**する。本モジュールは
*何を/誰に/いつ*を決める役割を担い、配信の*どのように*は出力コネクターが担う —
そのトランスポートを利用するのであって、決して再実装はしない。

## 概要

製品内のすべてのモジュールは、アラートを最小データの finding として、名前空間付きの
`Kind` を伴ってバス上に報告する（[`finding.reported`](/ja/reference/events/)）— 信頼性
（`health_subject_down`）、支出（`finops_budget`）、セキュリティ
（`security_guardrail`）、eval リグレッション（`eval_regression`）、レジデンシー
（`compliance_residency_violation`）、オーケストレーションのケイデンス、voice など。
モジュール XV は、その製品全体に共通する唯一のアラートチャネル**のみ**を購読し、
`Kind`、重大度、ソースモジュール、サブジェクトでルーティングする。本モジュールは
`cost.sampled` や `edge.observed` のような生のテレメトリを意図的に購読**しない** —
支出*アラート*は cost サンプルとしてではなく `finops_budget` finding として到着する。
これは製品全体の finding を実行可能な通知へと変えるシームである。

## 契約とエンティティ

本モジュールは共有データモデルに、テナントスコープのエンティティを 2 つ宣言する。

| エンティティ | モード | 保持する内容 |
|---|---|---|
| **route** | 変更可能、監査対象 | ルーティングルール。イベント型、finding-kind の glob（例: `health_*`）、最小重大度、ソースモジュール、サブジェクト kind に対する述語 → 名前付き**宛先**であり、route ごとの dedup・スロットルウィンドウと優先度を伴う。**宛先のクレデンシャルは保持しない** — 機密でない宛先名のみを保持する。 |
| **delivery** | 追記専用 | すべての配信*試行*の証跡台帳。route、宛先、finding kind、重大度、サブジェクト参照、短いタイトル、相関ハッシュ、結果クラス（`delivered`、`failed`、`no_dispatcher`、`unknown_destination`）を記録する。 |

各 finding に対して本モジュールは、テナントの有効な route を優先度順に評価する。
空のままの述語次元はすべて*任意*を意味し、glob マッチは完全一致または `prefix*`
形式をサポートする。マッチングは read ビュー内で行われ、**ネットワーク配信は
いかなるストアトランザクションの外側で厳密に実行され**、その結果が追記専用台帳に
書き込まれる。route の作成・変更・削除、およびテスト通知の送信は、実際の principal に
帰属づけられる**特権かつ自己監査**アクションである。route と delivery のルートは、
安定コア契約ではなく、別の **beta**
[module-route リファレンス](/reference/api-beta/) で公開されている。それらのフィールドレベルの形状は、
製品の型付きインターフェイス内に存在する。

## 消費するものと生成するもの

- **消費**: [`finding.reported`](/ja/reference/events/) — 製品全体に共通する唯一の
  アラートチャネル。本モジュールはルーターであって、プローブでもメーターでもない。
  インフラをポーリングすることも、計測することも決してない。
- **生成**: 出力コネクター（Slack/Teams、PagerDuty/Opsgenie、署名付き webhook、
  および CEF/LEEF/syslog/OTLP 経由で Splunk/Elastic をカバーする SIEM 宛先）に
  支えられたディスパッチシームを通じた外向き通知。通知は finding のすでに安全な
  表示フィールド — タイトル、kind、重大度、サブジェクト参照、相関ハッシュ — のみを
  運び、ペイロード、プロンプト、シークレット、PII は**決して**運ばない。
  **最小データはワイヤーの性質**であり、事後のフィルターではない。宛先シークレットは
  オペレーターがプロビジョニングするコネクター設定内にのみ存在し、ここでは機密でない
  名前で参照される。

:::caution[正直な限界]
- **デフォルトのバイナリは deny-closed のディスパッチャーを同梱する。** オペレーターが
  宛先をプロビジョニングするまで、ディスパッチャーは配線されているが空である。
  マッチしない配信は `no_dispatcher` として記録され、設定ミスや未知の kind の宛先は
  台帳上で `unknown_destination` に解決される。本モジュールは**決して成功を偽らない** —
  非配信は常に可視である。
- **外向き webhook は OpenAPI の webhook ではなく宛先コネクターである。** これは
  control plane がプッシュする出力チャネルであり、製品の API に対して登録する
  コールバックではない。
- **dedup とスロットルは*送信*を抑制するのであって、結果を抑制するのではない。**
  重複排除またはスロットルされた通知は、意図的に delivery 台帳に書き込まれ**ない**
  （したがって決して水増しされない）。これに対して、実際の配信*試行*はすべて記録される —
  `delivered`、`failed`、`no_dispatcher`、`unknown_destination` のいずれも同様に — ので、
  非配信は常に可視であり、決して黙って破棄されることはない。
- **コネクターの生エラーは決して永続化もログ記録もされない** — 機密でない結果クラスのみが
  記録される — なぜなら、トランスポートエラーはその URL 内に宛先シークレットを運びうる
  からである。
:::

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/) — モジュール XV の位置づけと Govern/Actuate の区分。
- [SIEM へのプッシュ](/ja/how-to/cookbook/push-to-siem/) — finding と封印された監査台帳を
  タワーのネイティブ方言（OCSF/CEF/LEEF/syslog/OTLP）へと再整形し、eventing プラットフォームの
  耐久性のある配信に乗せる S2S プッシュドライバー（`modules/siemforward`） — 上記の宛先に対する
  プッシュの補完。
- [イベントバスリファレンス](/ja/reference/events/) — `finding.reported` イベントとその `FindingReport` ペイロード。
- [アクセス＆リソースマップ](/ja/reference/modules/iii-access-map/) — 姉妹となる Core/Intelligence リファレンス。
- [監査を Splunk へ転送](/ja/how-to/forward-audit-to-splunk/) — SIEM 宛先の配線。
- [Govern and approve](/ja/how-to/govern-and-approve/) — 本モジュールがルーティングする finding への対応。
- [正直さと限界](/ja/start/honesty-and-limits/) — 製品全体にわたる deny-closed-by-default の姿勢。
