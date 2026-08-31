---
title: 用語集
description: >-
  本製品の語彙を正確に: access map とその正直さの軸、観測種別、ガバナンスのプリミティブ、
  そして運用上の用語 —— それぞれをエンジンが実際に使うとおりに定義する。
---

用語はエンジンが使うとおりに定義される —— いくつかは業界での用法より意図的に狭く、その狭さこそが
要点である。

### Access map（R/RW map）

モジュール III が持つ、**origin**（エージェント、アイデンティティ、セッション）と、それらが触れる
**リソース**のグラフ。すべてのエッジは [mode](#mode) によって分類され、その [signal source](#signal-sourceシグナルソース)、
[attribution](#attributionconfidence)、[coverage tier](#coverage-tierカバレッジ階層) でタグ付けされる。差別化された
鍵となる能力の 1 つ —— 30 モジュールのうちの 1 つであって、製品全体ではない。
[Olivares AI とは？](/ja/start/what-is-olivares-ai/) を参照。

### Actuation states: `v1` / `on-demand` / `seam`

すべてのモジュールの*acting*（作用）側の 3 つの正直な状態。**`v1`** —— プロビジョニングなしで
デフォルトバイナリでライブ。**`on-demand`** —— 構築・配線済みだが、オペレーターがプロビジョニングする
（deploy apply/retire、orchestration fire、voice dispatch）まで deny-closed または劣化している。
**`seam`** —— バックエンドのない宣言されたインターフェース。
[モジュールカタログ](/ja/reference/modules/overview/) はすべてのモジュールにマークを付ける。CI の回帰
ガードがテーブルを正直に保つ。

### Agent（エージェント）

AI システム（コーディングエージェント、サービスエージェント、オーケストレーションされたワークフロー
ステップ）であり、ファーストクラスのエンティティとして統治され、それが動作するときの
[identity](#identity--nhi)（クレデンシャル）とは区別される。エージェントをアイデンティティに
バインドすることが [attribution](#attributionconfidence) を鋭くする。

### Agent sprawl（エージェントの蔓延）

AI エージェント、コパイロット、MCP サーバーが、誰もインベントリを維持できないほど速く組織全体に
増殖することを指すアナリスト用語 —— アクセスが不明な未知のエージェント。これは
[access map](#access-maprrw-map) とディスカバリが可視化するために存在する問題である。
[アナリストの語彙](/ja/explanation/positioning/analyst-vocabulary/) を参照。

### AI TRiSM

*AI Trust, Risk and Security Management* —— AI の信頼、リスク、セキュリティを統治するために
**Gartner が造語・所有する**フレームワーク。私たちは自らの能力をその**テーマ**（ガバナンス、ランタイム
検査、ランタイム強制、情報ガバナンス）にマップする。Gartner の正確なモデルを再現することも、適合を
主張することも、推奨を示唆することも**しない** —— その分類体系は Gartner 独自の研究である。
[アナリストの語彙](/ja/explanation/positioning/analyst-vocabulary/) を参照。

### Approval（HITL）

ゲートされたアクションを実行するための統治されたリクエスト。**deny-closed かつ時間制限付き**で開かれ、
正確なプランにバインドされ、職務分離と有効期限がサーバー側で強制された状態で認可された人間が決定し、
[ledger](#audit-ledger監査台帳) に記録される。[レシピ](/ja/how-to/cookbook/hitl-approvals/) を参照。

### Attribution（confidence）

観測されたアクセスが*特定の* origin にどれほど確固として紐付いているか:
**`attributed`**（エージェント単位のアイデンティティが証跡にある）または **`approximate`**（推論された
—— 共有サービスアカウント、lossy なストア、まだエージェントにバインドされていないカーネルプロセス）。
マップは確実性を捏造する代わりにそのレベルを示す。コンソールは attributed なエッジを*firm*として
レンダリングもする。attribution の引き上げはアイデンティティの問題である:
[SSO/SCIM & アイデンティティソース](/ja/how-to/connectors/sso-scim-identity/)。

### Audit ledger（監査台帳）

すべてのガバナンス決定とすべての特権読み取りの、追記のみ・hash-chained なレコード。Ed25519 署名で
保護される —— 各レコードは `seq`、`prev_hash`、`hash`、`sig` を運ぶため、履歴の書き換えは暗号学的に
検出可能である。PII を決して含まない。プルエクスポート、プッシュ sink、オフライン検証
（`olivares audit verify`）として公開される。

### Break-glass（緊急昇格）

*特定の*ゲートされたアクションのための、統治・監査された緊急昇格 —— 意図的にすべてに対して利用可能では
**ない**: [kill switch](#kill-switch) の再有効化やアイデンティティのライフサイクルの確定は、決して
break-glass で行うことはできない。

### Checkpoint（チェックポイント）

tenant の台帳チェーンにわたる署名付きアンカー。一定間隔（デフォルト 1h）で書き込まれる。チェックポイント
と公開鍵の**オフボックス**コピーこそが、ホスト侵害後の検証を攻撃者耐性のあるものにする。

### Collector（コレクター）

観測されたシステムの近くで [source](#sourceソース) を実行し、観測を gRPC（オプションで mTLS）でコアへ
プッシュする、プッシュのみのエッジプロセス（`olivares collector`）。コレクターは**インバウンド
リスナーを持たない**。

### Cooperative path（協調パス）

エージェントの報告に依存する観測 —— OTLP テレメトリ、フック。存在すれば最高忠実度だが、構造的に
回避可能であり、だからこそその傍らに [kernel backstop](#kernel-backstopカーネルバックストップ) とストアネイティブ監査が
存在する。

### Coverage tier（カバレッジ階層）

attribution と直交する、*リソース*のシグナルの忠実度:
**clean**（ネイティブ監査が R/W を逐語的に分類する —— pgAudit、CloudTrail）、
**lossy**（エッジは得られるが不正確）、**opaque / impossible passively**（利用可能な受動的監査面が
ない —— 製品は推測する代わりにそう言う）。**mixed** は複数の階層から構築されたエッジをマークする。

### Demo estate（デモ estate）

合成 estate `serve --seed-demo` は**本物の**イベントバスを通じてロードされる（loopback のみ、公開
ソースツリーのパスワード、非 loopback バインドを拒否）。学習ツールであって、決してインストールパス
ではない。

### Destination（出力コネクタ）

コネクタカタログの配信側: Slack、Teams、PagerDuty、webhook、Splunk HEC、ServiceNow、Jira、email
とその仲間 —— findings と通知を配信し、何も観測しないためカバレッジ階層を持たない。

### DR bundle / KEK

`olivares dr backup` が生成する、暗号化された**台帳の継続性が安全な**バックアップ。バンドルとは別に
移動しなければならない key-encryption key（パスフレーズ由来または KMS 提供）の下で封印される。
[バックアップとリストア](/ja/how-to/backup-and-restore/) を参照。

### Drift（least-privilege drift）

[Permitted と Observed](#permitted-vs-observed) の差分: 付与されたアクセスと行使されたアクセスの
ギャップ。3 つのクラス —— **unexpected access**（観測されたが、決して付与されていない）、
**unused grant**（付与されたが、決して観測されていない）、**reconciliation pending**（観測されたが、
アイデンティティリンクが未解決）。[トリアージレシピ](/ja/how-to/cookbook/drift-triage/)。

### Edge / cost / finding

ソースが出力できる観測種別の**閉じた集合**: アクセス関係、使用コスト事実、または detective finding。
設計上閉じている —— コネクタは新しい種別を発明できず、それが minimal-data 契約を強制可能に保つ。

### Estate

1 つのデプロイで統治するすべて: エージェント、アイデンティティ、MCP サーバー、モデル、リソースと
それらの関係、あなたのすべての組織にわたって。

### Finding

guardrail / posture / red-team / forensic な観測。機密な詳細そのものではなく、そのハッシュを運ぶ。
通知レールと [SIEM sink](/ja/how-to/cookbook/push-to-siem/) にルーティングされる。

### Guardian agent

*他の* AI エージェントを監視または介入する AI を指す **Gartner の**用語。Olivares AI はそのカテゴリの
**ガバナンス成果**を提供する —— observe、permitted-vs-observed を diff、deny-closed でゲート、不変に
記録 —— ただしデータパスの**外にある read-first な control plane** として行い、ガードに立つインライン
LLM としてではない。[アナリストの語彙](/ja/explanation/positioning/analyst-vocabulary/) を参照。
製品内の [guardian loop](#guardian-loop) と対比すること。

### Guardian loop

findings を監視し、コンテインメントを自動的に作動させる —— [kill switch](#kill-switch) を含む ——
ガバナンスルール。自動パスは人間による stop と正確に同じゲートを通る。

### Identity / NHI

クレデンシャルを持つプリンシパル: 人間、または**非人間アイデンティティ**（サービスアカウント、
ワークロードアイデンティティ、API キー、エージェントアイデンティティ）。名簿は
[アイデンティティソース](/ja/how-to/connectors/sso-scim-identity/) から到着する。それらをエージェントに
バインドすることが、観測からガバナンスへの架け橋である。

### Kernel backstop（カーネルバックストップ）

非協調的な観測パス: Tetragon がエージェントの制御の外でカーネルのファイル/ネットワークイベントを
キャプチャする。`ebpf` ソースがそのエクスポートを消費する。アイデンティティがプロセスをエージェントに
バインドするまで、常に [`approximate`](#attributionconfidence) である。
[eBPF/Tetragon](/ja/how-to/connectors/ebpf-tetragon/) を参照。

### Kill switch

estate（またはエージェント単位）の緊急停止: 1 回の admin-tier 呼び出しがすべての統治された actuation を
fail-closed で停止する。再有効化には 2 人の個別の人間と事後レビューが必要であり、その周りに break-glass は
ない。[ドリルレシピ](/ja/how-to/cookbook/kill-switch-drill/)。

### MCP annotation

サーバーが自己宣言する `readOnlyHint` / `destructiveHint` —— **MCP 仕様によって信頼できない**ものとして、
宣言された能力ヒント（`approximate`、観測でも許可でもない）としてのみ取り込まれ、裏付けは取るが決して
それ単独では信頼されない。[MCP ガバナンス](/ja/how-to/connectors/mcp-governance/) を参照。

### Minimal data

観測が識別子と分類を運び、ペイロード、SQL 本文、プロンプト、シークレット、PII を決して運ばないという
ワイヤーレベルの性質。設定ではなく、コネクタ語彙の性質である。

### Mode

エッジの読み取り/書き込み分類: `read`、`write`、`readwrite`、または `unknown` —— シグナルから逐語的に
取得され、**決して推論されない**。`unknown` は欠落した答えではなく、正直な答えである。

### Observed / Permitted

[Permitted vs Observed](#permitted-vs-observed) を参照。

### Opaque tokens（不透明トークン）

本製品のクレデンシャル: ランダムで失効可能、サーバー側で検証されるトークン（`olvs_…` セッション、
`olvk_…` API キー、`olst_…` ワンタイムセットアップトークン）—— 意図的に JWT ではないため、署名鍵の
所持が決してアクセスを発行できない。

### Organization（tenant）

分離境界。すべてのモジュールの読み取りと書き込みは tenant スコープである。Postgres 上では row-level
security がそれをバックストップする（エンジンは RLS をバイパスできるロールとして実行することを拒否する）。

### Permitted vs Observed

access map が diff する 2 つの側: **permitted** エッジは宣言された grant とポリシーから来る。
**observed** エッジはテレメトリとネイティブ監査から来る。その diff が [drift](#driftleast-privilege-drift)
である。

### Sealed admission（封印されたアドミッション）

プロセス外コネクタプラグインのための deny-closed な信頼ゲート: ピン留めされた digest + Sigstore
attestation を、オペレーターがピン留めした trust anchor に対して検証する。逃げ道はない。
[コネクタを構築する](/ja/how-to/build-a-connector/) を参照。

### Setup token（セットアップトークン）

初回ブート時に stdout に出力される単回使用の `olst_…` トークン —— ブートストラップクレデンシャルの
全体像であり、デフォルトクレデンシャルは存在しない。そのハッシュのみが保存される。

### Signal source（シグナルソース）

どのオブザーバーがエッジを生成したか: `pg_audit`、`cloudtrail`、`otel`、`ebpf`、`mcp_annotation`、
宣言されたポリシー grant、A2A シグナル。来歴は決して潰されない: pgAudit の READ と MCP ヒントは同じ
証跡ではない。

### Sink

イベントを SIEM へその方言（Splunk HEC、Sentinel DCR、Datadog、New Relic、または汎用の HMAC 署名付き
webhook）で、OCSF/CEF/LEEF/syslog/OTLP/JSON で配信するイベンティングサブスクリプション。
[SIEM へプッシュ](/ja/how-to/cookbook/push-to-siem/) を参照。

### SLI / SLO

公開されたサービスレベル: `/readyz` による可用性、リクエスト成功、API と取り込みのレイテンシ p99 ——
シングルノードと HA の階層を別々に正直に明示する。
[モニタリング](/ja/how-to/monitor-with-prometheus/) を参照。

### Source（ソース）

観測コネクタ: config で `Open` し、観測をエンジンの sink に `Gather` し、`Close` する。エンジンが所有する
スケジューリング、minimal-data 語彙、Apache-2.0、コアを決してインポートしない。
[ソースを接続する](/ja/how-to/connect-a-source/) を参照。

### Stop gate（ストップゲート）

すべての統治された actuation が [kill switch](#kill-switch) の状態に対して行う強制チェック ——
他のいかなるゲートよりも前にチェックされ、**closed** で失敗する（budget チェックの逆: budget は open で
失敗する —— 壊れたメーターが停止を引き起こしてはならないが、壊れた stop チェックは停止しなければならない）。
