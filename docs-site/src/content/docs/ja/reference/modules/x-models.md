---
title: "モジュール X — モデルとプロバイダーの管理"
description: >-
  AI モデルスタック全体に対する governance レイヤー — Claude、OpenAI、Gemini、そして
  ローカル inference。バージョン管理された参照カタログ、capability マトリクス、そして
  primary + fallback チェーンを解決する routing ポリシー。経路を決めるが、まだモデル
  呼び出しを実行はしない。
---

モジュール X は **AI モデルとプロバイダーのスタック全体**を統制する — Claude、OpenAI、Gemini、
そしてローカル inference であり、単一ベンダーにとどまらない。これは model/provider コネクタの
*上に*位置する **Core レイヤー**のモジュールである。いかなるプロバイダー統合も inference
ゲートウェイも再実装しない。本モジュールが所有するのは **governance レイヤー** — バージョン
管理されたカタログ、クロスベンダーの capability マトリクス、そして名前付きの routing
ポリシーである。

## これは何か

本モジュールは、inventory（モジュール I）が発見した素の `Provider`/`Model` エンティティを、
統制されたカタログへと変える。2 つの半分から成る。

- **宣言された参照カタログ** — バージョン管理された（versioned-in-repo）、オペレーターが
  上書き可能なモデルファミリーのテーブルであり、宣言された API 機能 capability と
  **list-price のデフォルト**を伴う。価格は宣言された日付（`pricing_as_of`）が刻印され、
  明示的に*各プロバイダーの価格ページに対して検証すべきデフォルト*であり、捏造された
  テレメトリでは決してない。一致するエントリのないファミリーは、捏造された価格を得るのでは
  なく**価格未設定（unpriced）**のままとなる。
- **ライブ estate の enrichment** — 本モジュールは [`cost.sampled`](/ja/reference/events/) ストリーム
  をリッスンし、発見された `Model`/`Provider` エンティティを、ファミリー、context window、
  modality、トークンあたりの価格、そして capability セットで enrich する（inventory は価格
  フィールドを本モジュールに委ねる）。

capability の語彙は単一の**クロスベンダーマトリクス**である — Claude スタックの全体
（prompt caching、batch、Files、citations、extended thinking、computer use、memory tool、
context management、vision/PDF、structured outputs）に加え、他の各ベンダーが実際に公開して
いる類似機能 — したがって UI は単一のマトリクスをレンダリングし、routing ポリシーはベンダーを
*またいで* capability を要求できる。Claude ファミリーはファミリー単位でカタログ化され
（`claude-opus`、`claude-sonnet`、`claude-haiku`、`claude-fable`、`claude-mythos`）、非推奨／レガシーのバージョンはより長い
プレフィックス配下に保持されるため、現行 id は現行の価格ティアに解決される。

## その契約とエンティティ

routing がアクチュエーション面であり、それは **routing-only** である。

- **Routing ポリシー**はコアの `Policy` エンティティ（`Kind="routing"`）に永続化される。
  名前付きの選択／fallback／version-pinning ポリシー（cheapest-first、lowest-latency、
  capability-ordered、または pin されたモデル）である。`POST …/routing-policies/{id}/resolve`
  はポリシーを統制された estate に対して解決し、選択理由とともに **primary + fallback
  チェーン**を返す。これは **read-only** である。コネクタ／ゲートウェイがその後実行する選択を
  計算するのであって、本モジュールは **inference を行わない**。
- **API キー／workspace の governance** は **minimal-data なメタデータのみ**である — どの
  エージェントまたはチームがどの認証情報を使うかを、マスクされたヒントとして運び、秘密情報の
  値は決して含まない。
- read-only な **Anthropic レート制限インベントリ**（ゲートウェイまたはプロキシが同期を保つ
  べき上限）は、参照可能なインベントリとして提供される。本モジュールが変更する control では
  決してなく、read-only な Admin コネクタがプロビジョニングされていない場合は、正直な
  *理由付き利用不可（unavailable-with-reason）*レスポンスへと劣化する。

カタログと機能の読み取りはセンシティブではなく viewer ティアでゲートされる。routing と
キー governance の変更は editor ティアの監査される変更である。統制された実行
（governed-execution）パスは、read ティアの resolve とは別個の admin ティアのアクションで
ある。ルートは、安定コア契約ではなく、独立した **beta**
[module-route リファレンス](/reference/api-beta/) で公開される。そのフィールドレベルの形状は、
本製品の型付けされたインターフェースに存在する。

## 消費するものと生成するもの

本モジュールは [イベントバス](/ja/reference/events/) から `cost.sampled` を**消費し**、カタログを
実際のトークンあたり価格と使用量で enrich する。新しい観測タイプを導入することはない。
統制された実行パスにおいて、成功した呼び出しは秘匿化済みの `CostSample` を FinOps へと
**生成する**であろう — モデルの出力は呼び出し元へ渡るが、ここではどこにも永続化されない。
この面に金額が現れることは決してない。USD 金額は一切返されず、トークン数と提供したターゲット
のみが返される。

:::caution[正直な制限]
- **routing-only なアクチュエーション。** 本モジュールは経路（primary + fallback チェーン）を
  **解決する**が、**モデル呼び出しを実行しない**。統制された実行パスは **deny-closed の
  シーム**である。executor がプロビジョニングされていない状態では明確な `503` を返す —
  control plane はモデルを*選択*できるが、プロバイダーに対して*支出*することはない。executor
  が配線されると、上限にある FinOps budget が、いかなるプロバイダー呼び出しの*前に*支出を
  拒否する。
- **宣言された価格はデフォルトであり、保証ではない。** list 価格は日付が刻印されたオペレーター
  検証済みのデフォルトである。実使用の権威ある（authoritative）コストは常にコネクタ由来の
  `CostSample` であり、便宜的なトークンあたりの数値では決してない。一致しないファミリーは
  価格未設定として表示される — 捏造された価格を伴うことは決してない。
- **発表されたばかりのモデルはリスト化されるが、フラグが付けられる。** capability がまだ
  model card に対して検証されていないプレビューモデルは、データを捏造するのではなく、その
  capability セットに*要確認（to-confirm）*の印を付け、価格未設定のままカタログ化される。
- **キーインベントリはメタデータであり、決して秘密情報ではない。** 本モジュールは governance
  上の関係とマスクされたヒントを永続化する。認証情報の値はプロバイダーの Admin API を決して
  離れず、決して保存されない。一部のプロバイダーはキーインベントリをまったく公開しない —
  これは省略ではなく、文書化された制限である。
:::

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/) — モジュール X の位置とそのアクチュエーションステータス。
- [Access & resource map](/ja/reference/modules/iii-access-map/) — R/RW map と least-privilege drift。
- [イベントバスリファレンス](/ja/reference/events/) — 本モジュールが消費する `cost.sampled` イベント。
- [アーキテクチャ概要](/ja/explanation/architecture/overview/) — エンジン、レイヤー、コネクタ。
- [統制と承認](/ja/how-to/govern-and-approve/) — routing と governance に対して作用する。
- [正直さと制限](/ja/start/honesty-and-limits/) — 広く観測し／一部に対してアクチュエートする契約。
