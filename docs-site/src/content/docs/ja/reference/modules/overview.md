---
title: モジュールカタログ
description: >-
  Olivares AI の 30 個のモジュール —— 9 つの能力領域で整理し、各モジュールの
  率直な成熟度を示す。Olivares AI は ひとつのグラウンドトゥルース: Claude Code が最も深いレベル、Codex と Grok Build がその隣、
  エンタープライズにおける AI を統合・管理・保護する。これはモジュール単位の
  リファレンスである。
---

Olivares AI は、ひとつのグラウンドトゥルース（Claude Code が最も深いレベル、Codex と Grok Build がその隣）で
エンタープライズにおける AI を統合・管理・保護する。これは **モジュール型プラットフォーム** であり——1 つの
エンジン、1 つのコンソール、そして単一バイナリに配線された **30 個のモジュール**——
エージェントがどこで稼働しているかを観測し、何を許可されるかをガバナンスし、
（拡大中のサブセットにおいて）実際のインフラに対して作用する。すべてのモジュールは、
(a) コアから正規化されたイベント／データを消費し、(b) 自身のエンティティを共有データ
モデルで宣言し、(c) 自身の API エンドポイントと UI ビューを公開する——コアや他の
モジュールに一切触れることなく。

30 個のモジュールは、以下の **9 つの能力領域** で整理される。各モジュールのステータスは
**2 つの半分** として読むこと。すなわち *Govern/Observe*（カタログ化・観測・ゲート・
レポート）は今日すでに構築・配線済みである。一方 *Actuate*（実際のインフラに対して作用
する——deploy・dispatch・send・enforce・run）は率直な状態に分かれる——サブセットでは
デフォルトバイナリにおいて **live**、いくつかでは **on-demand**（バックエンドは構築・
配線済みで注入ポイントに接続されているが、オペレータが env 設定でプロビジョニングする
までは deny-closed あるいは縮退状態にとどまる）、サーフェスがゲート／オプトインのところ
では **PARTIAL**、残りについては宣言された **deny-closed seam**。特に **deploy** は
デプロイメントを計画・ガバナンスするが、エグゼキュータがプロビジョニングされるまでは
ライブインフラに **適用しない**。すなわち `apply`／`retire` は明確な `503` を返す。
深さはモジュールによって異なり、製品の多くは注記のとおり pre-1.0／設計段階にある
（[誠実さと限界](/ja/start/honesty-and-limits/) を参照）。

**access map**（`iii-access-map`）——各エージェントが何に触れることができ、実際に何に
触れているかの read/read-write グラフであり、最小権限ドリフト = `Permitted ≠ Observed`——
は、**30 個のうち最も有用な能力のひとつ** であって、製品の全体ではない。広がりこそが
要点である。すなわち 9 つの領域、1 つのエンジン、1 つのコンソールである。

## 30 個のモジュール、能力領域別

各行はそのモジュールのページ（`/reference/modules/<slug>/`）にリンクする。**Actuate**
列は作用する半分の率直な状態であり、`—` はそのモジュールが本質的にガバナンス／観測を
行い、アクチュエーション・サーフェスを持たないことを意味する。

### Observe

| モジュール | Actuate | 目的 |
|---|---|---|
| [インベントリと発見](/ja/reference/modules/i-inventory/) | — | estate 内のすべてのエージェント／セッション／MCP サーバー／ツール／モデル／アイデンティティを発見しカタログ化する。 |
| [ライブ運用とセッション](/ja/reference/modules/ii-sessions/) | — | 各エージェントとセッションのリアルタイム状態。ガバナンス対象の Claude Code セッションランタイムもホストする。 |
| [アクセスとリソースマップ（R/RW）](/ja/reference/modules/iii-access-map/) | — | 各エージェントが何にアクセスするか、そして読むのか書くのか。最小権限ドリフト = `Permitted ≠ Observed`。 |
| [オーケストレーションと A2A](/ja/reference/modules/iv-orchestration/) | on-demand | ライブの委任／通信グラフを観測しガバナンスする。dispatch は on-demand で配線され、プロビジョニングされるまで deny-closed。 |
| [MCP、スキル、ケイパビリティ](/ja/reference/modules/v-capabilities/) | — | エージェントのツールとケイパビリティを視覚的にガバナンスする。 |
| [Health、SLA、稼働時間](/ja/reference/modules/xxii-health/) | — | estate のエージェントと MCP サーバーの信頼性。チェック、インシデント、依存関係マップ。 |
| [オブザーバビリティ read-model](/ja/reference/modules/observability/) | — | エンジン自身を表す read-model。ピン留めされた相互運用標準、W3C 相関の台帳／トレースビュー、サプライチェーン・アテステーション。 |
| [Claude Code アダプション](/ja/reference/modules/claudeadoption/) | — | Claude Code のアダプション／生産性の read-model。セッション、コード行数、コミット、PR、ツールの accept-reject、モデル別トークンを、チーム／開発者／日ごとに集計。デフォルトはチーム単位、開発者単位のドリルダウンは opt-in。Claude-API のみの境界。コストは決して持たない。 |
| [Live-ingest](/ja/reference/modules/live-ingest/) | PARTIAL | コネクタが発行できない detective イベントのインプロセス・プロデューサー。env ゲート、deny-closed、最小データ。 |

### Govern & enforce

| モジュール | Actuate | 目的 |
|---|---|---|
| [アイデンティティ、権限、ガバナンス](/ja/reference/modules/vi-governance/) | — | 誰が・何が・何をできるかを粒度高く。Cedar RBAC + deny-overlay + scoped grants、ロスター調整、scoped admin／カスタムロール、break-glass、kill-switch。 |
| [ソースと資格情報のスコーピング](/ja/reference/modules/sourcescope/) | — | ソースをワークスペース／エージェントグループにバインドする。解決時の deny-closed scoped resolver + scoped 資格情報。 |
| [デプロイメントと統合](/ja/reference/modules/vii-deploy/) | on-demand (503) | 実際のインフラへのデプロイメントを計画・ガバナンスする。エグゼキュータは on-demand —— ライブの `apply`／`retire` はプロビジョニングされるまで `503` を返す。 |

> **アイデンティティとアクセス** は [governance](/ja/reference/modules/vi-governance/) の内部に存在する——
> 独立したモジュールはない。NHI ライフサイクル、エージェント・アイデンティティ・フェデレーション、AAL3
> ステップアップ、SSO/SCIM はガバナンスのケイパビリティである。

### Claude & エージェントエコシステム

| モジュール | Actuate | 目的 |
|---|---|---|
| [モデルとプロバイダの管理](/ja/reference/modules/x-models/) | on-demand (503) | モデル／プロバイダのスタック全体にわたってガバナンスする。model-access、サーフェスごとの context-window、model-group ゲート。モデルの *実行* は on-demand —— 推論資格情報がプロビジョニングされるまで `503`。 |
| [インライン推論プロキシ](/ja/reference/modules/inferenceproxy/) | PARTIAL | インラインの `/v1/messages` PEP プロキシ向けのテナント単位の inference-egress 設定 + DLP。モジュール設定は live、リスナーは opt-in、loopback デフォルト、fail-CLOSED。 |
| [内部カタログとマーケットプレイス](/ja/reference/modules/xiv-catalog/) | — | 承認済み／署名済みのエージェント、MCP サーバー、スキルのキュレーション済みマーケットプレイス。 |
| [ボイスとリアルタイム・エージェント](/ja/reference/modules/xvi-voice/) | on-demand | 会話型／リアルタイム・エージェントを観測しガバナンスする（default-DENY、two-phase HITL）。メディアストリームを開くことはない。dispatch は on-demand。 |

### セキュリティとデータ保護

| モジュール | Actuate | 目的 |
|---|---|---|
| [セキュリティ、ガードレール、監査](/ja/reference/modules/ix-security/) | live | ガードレール（PII／インジェクション／ジェイルブレイク）、アノマリー、インシデント・タイムライン。BYOK/DLP/RTBF/retention/WORM/residency はこのプレーンに存在する。 |
| [特権セッションの記録](/ja/reference/modules/recording/) | live | PAM 準拠の特権セッション記録。hash-chained フレーム、書き込み時の秘匿化、ledger-anchored。 |
| [データ、ナレッジ、コンテキスト](/ja/reference/modules/viii-knowledge/) | on-demand | ガバナンス対象のデータプレーン。KB + RAG、ガバナンス対象のリトリーバル、リネージ、プロンプトレジストリ、エージェントメモリ。モデルベースの意味的エンベディングは on-demand。 |

### コンプライアンスと証跡

| モジュール | Actuate | 目的 |
|---|---|---|
| [コンプライアンスと規制](/ja/reference/modules/xiii-compliance/) | — | 26 のフレームワーク・カタログ + 封印された台帳由来の証跡。ライブのチェーン検証付き。 |
| [SIEM/ITSM フォワーダー](/ja/reference/modules/siemforward/) | live | 封印された台帳 + findings を SIEM タワーに送出する（OCSF 1.8/CEF/LEEF/syslog/OTLP）。leader-gated なカーソルウォーク、at-least-once。 |
| [Posture export](/ja/reference/modules/posture-export/) | PARTIAL | コントロールタワー向けの読み取り専用の posture／インベントリ・プル（ニュートラル JSON）。検証済みのダウンストリーム・プッシュを主張 **しない**。 |
| [Reporting](/ja/reference/modules/reporting/) | — | プラットフォームのコンプライアンス、監査、FinOps データからプロフェッショナルな PDF/HTML レポートを生成する。5 種類を内蔵し、監査担当者は JSON をコピー＆ペーストせず文書をダウンロードできる。 |

### FinOps

| モジュール | Actuate | 目的 |
|---|---|---|
| [コストと AI FinOps](/ja/reference/modules/xi-finops/) | live | 上限で deny／throttle する作用型予算、cost-per-outcome、キャンセルリスク。予算はアイデンティティに固定される。 |

### Evals & safety

| モジュール | Actuate | 目的 |
|---|---|---|
| [品質、evals、テスト](/ja/reference/modules/xii-evals/) | — | キャリブレーション済みの LLM-judge + ブロッキングな CI 回帰ゲート。オフラインの judge → SKIPPED であり、決して暗黙のパスにはならない。 |
| [エージェント・サンドボックス](/ja/reference/modules/xvii-sandbox/) | on-demand | 本番投入前にエージェントをテストする安全な環境。実際の OS 分離（gVisor/Firecracker）は on-demand。 |
| [レッドチーミングと敵対的テスト](/ja/reference/modules/xviii-redteam/) | on-demand | 同意ゲート付きの敵対的バッテリー。サンドボックス・ランタイムがプロビジョニングされるまで DEGRADED —— 決して偽のパスにはならない。 |

### プラットフォームと統合

| モジュール | Actuate | 目的 |
|---|---|---|
| [出力統合と通知](/ja/reference/modules/xv-notify/) | live | 企業がすでに運用しているシステムへの通知ルーター。dispatch はライブで配線され、宛先はオペレータがプロビジョニングする。 |
| [Eventing](/ja/reference/modules/eventing/) | live | バス上の外部サブスクリプション・サーフェス。型付きサブスクリプション、durable な at-least-once 配信、retry/backoff、DLQ、カーソルリプレイ。 |
| [保存済みコンソールビュー](/ja/reference/modules/consoleviews/) | — | コンソールビューの状態（フィルター、範囲）に名前を付けて共有できるスナップショット。テナントごとにサーバー側に保存される。調査を保存し、チームと共有する。ビューパラメータ用の JSON オブジェクト（上限 4096 バイト）を受け付けます——機密データやクエリ結果は保存しないでください。作成・更新はオーナーのみ。テナントの管理者/オーナーとスーパー管理者はクリーンアップのため削除できます。すべての変更は監査されます。 |

**Actuate** 列：`live` = アクチュエーションが配線され、デフォルトバイナリでライブで動作し、
プロビジョニング不要（例：FinOps の予算強制は上限で deny し、通知ルーターは dispatch する）。
`on-demand`／`on-demand (503)` = バックエンドは構築・配線済みで注入ポイントに接続されている
が、オペレータが env 設定でプロビジョニングするまでは **deny-closed あるいは縮退状態に
とどまる**（deploy はエグゼキュータが存在するまで `503` を返す。orchestration／voice の
dispatch は設定されるまで deny-closed。red-team はサンドボックス・ランタイムがプロビジョ
ニングされるまで DEGRADED で動作する。モデル実行と意味的エンベディングは資格情報が
プロビジョニングされるまで `503` を返す）。`PARTIAL` = サーフェスは実在するがゲート／
オプトインであるか、検証済みのダウンストリームを主張しない（inference-proxy リスナーは
opt-in かつ loopback デフォルト。live-ingest は env ゲート。posture-export はニュートラルな
読み取り専用の射影である）。`—` = そのモジュールは本質的にガバナンス／観測を行い、
アクチュエーション・サーフェスを持たない。この分割こそが率直な契約である。すなわち製品は
**今日、広範に観測しガバナンスし、拡大中の——大部分がプロビジョニング・ゲート付きの——
サブセットに対して作用する**——[誠実さと限界](/ja/start/honesty-and-limits/) を参照。この
カタログは合成ルート（`cmd/olivares/wire.go`）から導出される。すなわち 30 個のモジュール
すべてがそこで構築され、`rt.AddModule` を介して登録されている（2026-08-01、
main @ f632f03f で検証）。

## プラットフォームとコアのケイパビリティ（30 モジュールには数えられない）

これらは実在し、出荷済みのケイパビリティであるが、**エンジン／コア／Web のケイパビリティ**
であって `modules/` セット内のモジュールではない——したがって 30 には数えられない。

- [独自 API + manage-as-code](/ja/reference/modules/xix-api-manage-as-code/) ——
  **エンジン／コアのケイパビリティ。** エンジン自身のバージョン管理された REST/gRPC API に
  加えて Terraform プロバイダ。プラットフォーム自体を API と IaC で管理する。
- [マルチテナンシーと組織管理](/ja/reference/modules/xx-multi-tenancy/) ——
  **エンジン／コアのケイパビリティ。** 組織階層と委任された管理。Postgres の行レベル
  セキュリティによるテナント分離付き。
- [エグゼクティブ・ダッシュボード](/ja/reference/modules/xxi-executive-dashboards/) ——
  **Web のケイパビリティ。** 技術系 UI と並ぶリーダーシップ向けコンソールビュー。
  （レポート生成バックエンドは [reporting](/ja/reference/modules/reporting/) モジュールであり、
  30 のひとつとして数えられる。）
- [モデルオペレーション（自社モデル）](/ja/reference/modules/xxiii-model-operations/) ——
  **models モジュールのケイパビリティ**（モジュール X の行を通じて数えられ、独立した
  行ではない）：自社モデルの統治されたレジストリ、署名済みモデルのアドミッション、
  データセット／ファインチューニングジョブのリネージ記録、ローカル推論デプロイメントの
  統治、AIBOM／モデルカード証跡。

**計画中：** 自社モデルのファインチューニングとローカル推論の**実行**
（[xxiii-fine-tuning](/ja/reference/modules/xxiii-fine-tuning/)）—— プラットフォームは
今日その作業を統治し記録する（上記のモデルオペレーション参照）が、自ら訓練を実行したり
推論を提供したりはしない。実行する側の半分は文書化された**計画中**の作業であり、
**未出荷**で、30 のひとつでもない。

## モジュールが API とバスにどう現れるか

- **REST。** [API リファレンス](/reference/api/) は、製品の OpenAPI 3.1 契約から安定コアの
  REST サーフェスを描画する。モジュールルート（`/v1/m/<ns>/…`）は、独立した **beta**
  ドキュメントとして [module-route リファレンス](/reference/api-beta/) で公開される。
  そのフィールドレベルの契約は製品の型付きインターフェイスに存在する。
- **イベント。** モジュールは [イベントバス](/ja/reference/events/) に反応する。すなわち
  access map は `edge.observed` を消費し、FinOps は `cost.sampled` を消費し、security は
  `finding.reported` と `guardrail.observed` を消費する。

## レイヤー

30 個のモジュールは、上記のエンジン／コアおよび Web のケイパビリティと並んで、エンジン上の
レイヤーの上に構築される。

- **Engine（layer 0）** —— 独自 API／manage-as-code とマルチテナンシーのケイパビリティ
  （コアであり、30 には数えない）。
- **Core（layer 1）** —— inventory、sessions、access-map、models、health、observability。
- **Management（layer 2）** —— capabilities、governance、sourcescope、deploy、knowledge。
- **Intelligence（layer 3）** —— orchestration、security、recording、inference proxy、
  finops、evals、compliance、reporting、siemforward、posture-export、catalog、notify、eventing、
  voice、sandbox、redteam、live-ingest、consoleviews。
- **Web（layer 4）** —— UI とエグゼクティブ・ダッシュボードのケイパビリティ。

エンジンとこれらのレイヤーがどう構成されるかは
[アーキテクチャ概要](/ja/explanation/architecture/overview/) を参照。
