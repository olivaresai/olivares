---
title: "モジュール XI — コストと AI FinOps"
description: >-
  コストストリームから AI 支出を計上し、任意の attribution ディメンションでスライスし、
  期間を予測し、上限で支出を拒否する budget を強制する — ワイヤー上は金額フリー、opt-in
  かつ fail-open。何をするか、そしてその制限。
---

モジュール XI は AI のための**コスト／FinOps** レイヤーである。model および provider コネクタが
報告するものを計上し、支出を任意の attribution ディメンションでスライスでき、現在の期間を
予測し、budget を単なるフラグ付けではなく上限で**支出を拒否する**実際の強制（enforcement）へと
変える。本ページは、FinOps が今日何をし、その保証がどこで終わるかのリファレンスである。

## これは何か

FinOps はプロバイダー統合を再実装**しない** — model/provider のコストストリームを消費し、
**コネクタが権威をもって導出または読み取ったものを計上する**。金額は常に**整数の micro-USD**
値（1 ドルの 100 万分の 1）であり、float では決してないため、合計がドリフトすることは決して
ない。これは Intelligence レイヤーのモジュールである。ingestion、budget、analytics を所有し、
コアやその隣接モジュールに触れることなく、独自の RBAC でゲートされた API 名前空間と UI ビュー
を通じてそれらを公開する。

本モジュールは構成上**minimal-data** である。トークン数、導出されたコスト、attribution の
*参照*を保存する — プロンプト、completion、秘密情報は決して保存しない。コストは governance
データであるため、読み取りは API でロールゲートされ、**いかなる USD 金額もエンドユーザーに
公開されることは決してない**（これは UI 設定ではなくワイヤーの特性である）。

## そのエンティティと契約

各 `cost.sampled` イベント（`CostSample` — [イベントバス](/ja/reference/events/) 参照）は 2 通りに
記録される。

- 正規化された規範的（canonical）な **CostRecord ledger**（id をキーとするコアエンティティ）。
  これは**自然キー（natural key）で重複排除される** — バケットの*identity*（provider／model／
  session／instant に加え、すべての attribution ディメンションと provenance）であり、その
  *value* では決してない — したがって再取得されたオープンバケットや遅延確定された報告は、
  at-least-once ストリーム上で二重計上するのではなく**その場で upsert される**。
- attribution の自然な名前（provider、model、agent、session、team、project）をキーとする
  非正規化された **FinOps read-model** 行。これにより、provider の `service_tier` を含む
  それらのディメンションの**いずれ**によっても支出が効率的に集計される。

**budget** は kind が `budget` のコア `Policy` である。ディメンション（global／model／provider／
agent／session／team／project）、limit、period、そして alert のしきい値から成る。その
`action` は 3 つのうち 1 つ — `alert`（showback のみ、決して強制しない安全なデフォルト）、
`throttle`、または `block`。analytics は、任意のディメンションによる支出の内訳、合計、日次の
トレンド系列、現在期間の run-rate とトレンド予測（明示的な confidence band 付き）、
prompt-cache 効率ビュー、そして最適化の推奨を提供する — それぞれが記録されたデータに根ざし、
**その前提について正直**である。

## 消費するものと生成するもの

FinOps は [イベントバス](/ja/reference/events/) から `cost.sampled` を**消費し**、2 つの効果を
**生成する**。ingest 時、消費がこの期間にまだ越えていない budget しきい値を越えると、alert を
記録し **`FindingReport`（`finding.reported`）を発行する** — *シグナルのみ*である。Slack／
SIEM／PagerDuty への配信は output-connector モジュールの役目であり、FinOps のものではない。

第二の効果は**強制**である。`action` が `throttle` または `block` の budget は、各作用する
モジュール自身の用語で宣言された **`BudgetGate` シーム**を通じて上限で支出を拒否する
（orchestration の *fire*、voice の *open*、model router の *resolve*）。FinOps を import する
モジュールは無い。このゲートは**承認ゲートとは直交して（orthogonally）**動作し — あるアクション
は人間に承認されてもなお budget で拒否されうる — cap-effective な支出に対して**金額フリーの
理由（money-free reason）**で応答する（read-only ルート上には USD も budget 名も無い）。
ハードな `block` は **HTTP 402** で拒否し、ソフトな `throttle` は **HTTP 429** で拒否し、その
拒否は append-only な ledger に書き込まれ監査される。[統制と承認](/ja/how-to/govern-and-approve/)
を参照。

:::caution[正直な制限]
- **強制は opt-in であり、デフォルトで deny-closed ではない。** リクエストをスコープする
  強制 budget が無ければ、何も拒否されることはない — その不在は通常の状態であり、セキュリティ
  ホールではない。*明確に*上限にある budget のみが拒否する。これは意図的であり、承認ゲートの
  deny-closed な姿勢とは逆である。
- **ゲートは fail open する。** FinOps の読み取りエラーが in-flight なアクションを停止させる
  ことは決してない — 承認された fire/open は進行し、router は解決する。耐久性のあるバック
  ストップは、pre-flight ゲートではなく、ingest 時に発行される budget-cap finding である。
- **router は実行前に知るスコープのみを強制する**（global／provider／model）。より細かい
  スコープ（agent、session、team、project）は、ルート解決時ではなく fire/open シームと
  model ゲートウェイで強制される。
- **FinOps は計上するが、請求はしない。** コネクタが報告するものを記録する — `billed` 対
  `estimated` の provenance は運ばれるが、invoice へは調整（reconcile）されない — そして
  ゼロ／空のフィールドを持つサンプルは*「未報告」*を意味するのであって、決して*「ゼロ」*では
  ない。
- **拒否を超えるアクチュエーションは無い。** FinOps はモデル呼び出しを実行することも金額を
  動かすこともない。コストストリームを観測し、ゲートするよう構成された支出をゲートする。
:::

## 関連

- [イベントバスリファレンス](/ja/reference/events/) — `cost.sampled` / `CostSample` および `finding.reported` のペイロード。
- [モジュールカタログ](/ja/reference/modules/overview/) — モジュール XI の位置とその正直なアクチュエーションステータス。
- [アーキテクチャ概要](/ja/explanation/architecture/overview/) — エンジン、レイヤー、コストストリーム。
- [統制と承認](/ja/how-to/govern-and-approve/) — budget で拒否されたアクションに対して作用する。
- [正直さと制限](/ja/start/honesty-and-limits/) — モジュール横断の deny-closed-seam ポリシー。
