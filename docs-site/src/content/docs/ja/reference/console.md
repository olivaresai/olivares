---
title: コンソールリファレンス — 全画面と必要な権限
description: >-
  Olivares AI コンソールが公開するすべてのルートを、コンソールのエリアごとに
  まとめ、それぞれに必要な RBAC 権限と、製品内ヘルプリンクが開くリファレンスページを
  掲載します。コンソール自身のルート一覧から生成されています。
---

このページはコンソールの地図です。アプリケーションがマウントする**すべてのルート**を、
選抜でも、誰かが覚えていて文書化したものだけでもなく、principal が開くために必要な
権限と詳細情報の参照先とともに列挙します。

コンソールのナビゲーション構造は**ひとつ、セクション付きの 9 エリア**です。各画面は
1 つのエリアとその 1 つのセクションに属し、画面の場所を示すものはすべて同じ場所を
示します。「すべての領域」（サイドバー、スマートフォンでは「その他」）、エリアのディレクトリ
ページ、パンくずリスト、コマンドパレット、そして画面の上にあるリンクの列です。この列は
同じセクションのほかの画面を並べます。サイドバーは画面の短い一覧を固定し、最初の作業を
先頭に置き（ホーム、セッション、AI ツール、承認）、その後に作業を置きます。固定された画面は、同じセクションのほかの画面でも選択中として
表示されます。ナビゲーションのフィルタは、グループ化されたツリーを、権限のある一致結果の
順位付き一覧に置き換えます。コマンドパレットも同じ索引、同じ順位、同じ権限の投影を
使います。エリアのディレクトリはリンクのページであり、権限が許すエントリを列挙します。
能力、可用性、準備状態のライブな読み取りではありません。

新規インストールでは、最初の作業だけが一覧に表示されます（ホーム、セッション、AI ツールと
プロバイダー、承認、セットアップウィザード、設定）。それ以外の画面はプレビューです。
アドレス、API、CLI はそのまま動作し、ナビゲーション、コマンドパレット、ショートカットには
表示されません。アップグレード前から存在するインストールでは、これまでどおりすべての画面が
表示されます。管理者がモジュールを選択したインストール（次回の起動から）と、デモデータを
使うインストールも同様です。

このページは**生成物**です。一覧は `web/src/features/route-census.json` から取得されます。
これは `registry.route-conservation.test.ts` がビルド済みルーターに対して固定する
append-only な一覧であり、画面が追加、移動、消失すれば、このページも必ず変わります。
各画面の名前と 1 行説明は、サイドバーと同じ翻訳カタログから取得した**コンソール自身の
文字列**です。ここで読む内容は製品で目にする内容と同じです。以下の表は、それらの行をエリアごとにまとめ、ホーム（概要）を先頭に、サインイン、
セットアップ、アカウントを最後に置きます。9 つのエリアディレクトリページは、機能
レジストリの外にマウントされたルートと同じ表にあります。

:::note[権限を強制するのはこの表ではなくエンジン]
`必要な権限` 列は、エンジンが返す実効権限に基づき、コンソールがルートを提示する前に
確認する権限を示します。エンジンは、コンソール外からのリクエストも含め、API
リクエストを独立して認可します。項目が表示されることは、そのモジュールが設定済みで
実行準備が整っていることを保証しません。
[ロールと権限](/ja/reference/modules/vi-governance/)を参照してください。
:::

## このページの読み方

- **画面** — サイドバー、エリアディレクトリ、コマンドパレットで使われる名前。
- **パス** — デプロイしたコンソールの origin 配下の URL。これは公開 contract です。
  ブックマーク、runbook のディープリンク、ドキュメントの相互参照はいずれもこの文字列を
  使います。
- **必要な権限** — RBAC 権限。`any signed-in user` は、すべての認証済み
  principal に開かれたルートを意味します。**no sign-in** は、セッションが存在する前に
  提供されることを意味します。
- **リファレンス** — その画面でコンソール自身のヘルプリンクが開くページ。

以下の見出しはエリアで、「すべての領域」と同じ順です。インフラ、AI、データと
コンテキスト、作業とコミュニケーション、自動化、セキュリティとアイデンティティ、
デプロイ、可観測性とエビデンス、システムと設定。

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

コンソールは **83 ルート**を公開します。以下の表に、必要な権限と、製品内
ヘルプリンクが開くリファレンスページとともに、すべて掲載されています。

### ホーム

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| ホーム | `/` | 環境全体の概要と健全性を一覧表示 | any signed-in user | [ドキュメントホーム](/ja/) |

### インフラ

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 環境 | `/estate` | 設定済みリソースとその関係を確認し、作業項目の依存関係を調べます。 | `sessions:run:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| インベントリ | `/inventory` | コネクタが観察したエージェント、MCP サーバー、モデルを検出してカタログ化 | `inventory:catalog:read` | [reference/modules/i-inventory](/ja/reference/modules/i-inventory/) |
| ワークスペース | `/workspace` | 1 つのワークスペースにスコープされたエージェント、セッション、リソース、アクティビティ | `tenant:read` | [reference/modules/xx-multi-tenancy](/ja/reference/modules/xx-multi-tenancy/) |

### AI

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| エージェントツール | `/agent-tools` | このホスト上のエージェントツールを検出・インストール・更新し、各インストールの状況を確認します（デプロイ管理者のみ） | `system:admin` | [how-to/add-a-provider](/ja/how-to/add-a-provider/) |
| セッション | `/agentops` | セッションを開始し、Olivares が見つけたものも含めて、それぞれの動きを追います | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ja/how-to/run-claude-code-with-olivares/) |
| MCP サーバー | `/mcp-servers` | リモートの MCP サーバーをこの組織に接続してテストし、セッションが使えるツールを選びます | `tenant:admin` | [how-to/connectors/mcp-governance](/ja/how-to/connectors/mcp-governance/) |
| モデル運用 | `/model-operations` | 所有モデル、admission、デプロイメント | `models:registry:read` | [reference/modules/xxiii-model-operations](/ja/reference/modules/xxiii-model-operations/) |
| モデル | `/models` | モデル、ルーティング、プロバイダー鍵 | `models:catalog:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| プラットフォーム | `/platforms` | デプロイ面、コンプライアンスマトリクス、プラットフォーム別モデルライフサイクル | `models:platforms:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| プロバイダーアカウント | `/provider-accounts` | 名前付きのプロバイダーアカウントを一覧し、既存のプロバイダープロファイルをアカウントとして採用する | `sessions:account:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| ソースバインディング | `/provider-bindings` | 構成済みのソースを、このノードが適用したリビジョンのまま、プロバイダープロファイルに専用で割り当てる | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロバイダープロファイル | `/provider-profiles` | セッションの起動元となるプロバイダーのホームを登録、管理し、その設定を必要に応じて読み取る | `sessions:profile:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロバイダー | `/providers` | セッションの起動に使う API キーとエンドポイントを登録し、テスト・交換・失効を行う | `sessions:provider:read` | [how-to/add-a-provider](/ja/how-to/add-a-provider/) |
| レート制限 | `/rate-limits` | Anthropic のレート制限インベントリ（読み取り専用） | `models:ratelimits:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| サンドボックス | `/sandbox` | 隔離されたエージェントのテストとリプレイ | `sandbox:run:read` | [reference/modules/xvii-sandbox](/ja/reference/modules/xvii-sandbox/) |
| セッション | `/sessions` | セッションを開始し、Olivares が見つけたものも含めて、それぞれの動きを追います | `sessions:live:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| 音声 | `/voice` | 音声およびリアルタイムセッション | `voice:session:read` | [reference/modules/xvi-voice](/ja/reference/modules/xvi-voice/) |
| ワークスペーステンプレート | `/workspace-templates` | 再利用可能なセッション設定スナップショット: フック、設定、コネクタ、ポリシー。 | `sessions:template:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |

### データとコンテキスト

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| エージェントアーティファクト | `/agent-artifacts` | スキル、MCP 拡張、指示ファイル — レジストリ、posture、サプライチェーン BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/ja/reference/modules/xxiii-model-operations/) |
| MCP とスキル | `/capabilities` | MCP サーバー、スキル、ツールを統制 | `capabilities:catalog:read` | [reference/modules/v-capabilities](/ja/reference/modules/v-capabilities/) |
| カタログ | `/catalog` | キュレーションされ承認されたエージェントと capability | `catalog:entry:read` | [reference/modules/xiv-catalog](/ja/reference/modules/xiv-catalog/) |
| ナレッジ | `/knowledge` | ナレッジベース、RAG、データリネージ | `knowledge:kb:read` | [reference/modules/viii-knowledge](/ja/reference/modules/viii-knowledge/) |
| スキルカタログ | `/skills` | スキルパックを閲覧し、部門、エージェントグループ、エージェントに割り当てる | `skills:catalog:read` | [reference/console](/ja/reference/console/) |

### 作業とコミュニケーション

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| コミュニケーション | `/communications` | 選択中ワークスペースのチャネル、ダイレクト通知、個人受信箱 | `sessions:channel:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| チャネル管理 | `/communications/administration` | チャネルを管理：設定と付与履歴、各操作はチャネルの現在の ETag のもとで | `sessions:channel:admin` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| ハンドオフ | `/communications/handoffs` | あなた宛ての作業責任のオファー: コンテキストを読んで承諾または拒否します | `sessions:delivery:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| コミュニケーション受信箱 | `/communications/inbox` | 自分宛ての配信だけを新規に読み取り、明示的に確認する正確な受信箱 | `sessions:delivery:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| 新しいチャネル | `/communications/new` | 明示的な初期付与でチャネルを作成 | `sessions:channel:write` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロトコルバインディング | `/communications/protocol-bindings` | 統制された A2A と MCP のバインディングを構成し reconcile | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| 作業 | `/work` | 共有作業：項目、依存関係、受け入れ、決定 | `sessions:work:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |

### 自動化

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アラート | `/alerting` | findings を宛先へルーティングし、配信を確認 | `notify:route:read` | [reference/modules/xv-notify](/ja/reference/modules/xv-notify/) |
| オートメーション | `/automations` | 3 つすべての自動化レールとトリガーカタログ | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/ja/reference/modules/iv-orchestration/) |
| Webhook とイベント | `/eventing` | アウトバウンド Webhook サブスクリプション、配信ログ、デッドレターキュー。 | `eventing:subscription:read` | [reference/modules/eventing](/ja/reference/modules/eventing/) |
| オーケストレーション | `/orchestration` | エージェント間の連携とスケジュール | `orchestration:graph:read` | [reference/modules/iv-orchestration](/ja/reference/modules/iv-orchestration/) |

### セキュリティとアイデンティティ

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アクセスマップ | `/access-map` | 各エージェントが読み書きするもの（R/RW） | `accessmap:graph:read` | [reference/modules/iii-access-map](/ja/reference/modules/iii-access-map/) |
| AgentCore エクスポート | `/agentcore-export` | このテナントのガバナンスルールを Cedar ポリシーとして AWS AgentCore へ投影する計画・確認・適用。計画では何も書き込みません | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| Claude Code ガバナンス | `/claude-policy` | 管理ポリシー、フック、MCP、サンドボックス、policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ja/how-to/connectors/claude-code-hooks-pep/) |
| アイデンティティと NHI | `/identity` | SSO、SCIM、NHI 一覧、WIF グラフ | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| 推論プロキシ | `/inference-proxy` | プロキシゲート、エグレス DLP ルール、デバイス承認 | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ja/reference/modules/inferenceproxy/) |
| キルスイッチ | `/killswitch` | 緊急停止、二重統制による復旧、guardian containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ja/how-to/cookbook/kill-switch-drill/) |
| 権限 | `/permissions` | アイデンティティ、ロール、承認 | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| レッドチーム | `/red-team` | エージェントに対する敵対的テスト | `redteam:target:read` | [reference/modules/xviii-redteam](/ja/reference/modules/xviii-redteam/) |
| データレジデンシー | `/residency` | 各組織をリージョンへ固定、または未固定のままにする | `system:admin` | [reference/modules/xiii-compliance](/ja/reference/modules/xiii-compliance/) |
| ルーチンポリシー | `/routine-policies` | Claude Code ルーチンの頻度下限、同時実行上限、承認要件、cron allowlist。 | `governance:routine:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| セキュリティ | `/security` | ガードレール、フォレンジック、異常 | `security:finding:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |

### デプロイ

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| デプロイメント | `/deploy` | エージェントをインフラへプロビジョニングして接続 | `deploy:deployment:read` | [reference/modules/vii-deploy](/ja/reference/modules/vii-deploy/) |
| Git 公開 | `/git-publication` | 承認済みの Git ターゲットを通じてコミットのプッシュ、プルリクエストの作成、マージを行う | `gitpublish:target:read` | [reference/modules/gitpublish](/ja/reference/modules/gitpublish/) |

### 可観測性とエビデンス

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| Claude Code の導入状況 | `/adoption` | 生産性、受け入れ、モデル構成 | `adoption:metrics:read` | [reference/modules/claudeadoption](/ja/reference/modules/claudeadoption/) |
| サプライチェーン | `/attestation` | リリースアテステーション — SLSA、SBOM、VEX、Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ja/how-to/verify-a-release/) |
| 監査台帳 | `/audit` | 改ざん検知可能な証跡台帳 | `audit:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |
| コンプライアンス | `/compliance` | フレームワーク、コントロール、証跡 | `compliance:framework:read` | [reference/modules/xiii-compliance](/ja/reference/modules/xiii-compliance/) |
| ダッシュボード | `/dashboards` | 経営指標とレポート | any signed-in user | [reference/modules/xxi-executive-dashboards](/ja/reference/modules/xxi-executive-dashboards/) |
| 評価 | `/evals` | 品質、評価、リグレッション | `evals:run:read` | [reference/modules/xii-evals](/ja/reference/modules/xii-evals/) |
| コストと FinOps | `/finops` | トークンコスト、予算、支出 | `finops:spend:read` | [reference/modules/xi-finops](/ja/reference/modules/xi-finops/) |
| 健全性と SLA | `/health` | エージェントと MCP の稼働時間および SLA | `health:status:read` | [reference/modules/xxii-health](/ja/reference/modules/xxii-health/) |
| オブザーバビリティ | `/observability` | 標準別の取り込み健全性とトレースのドリルダウン | `health:status:read` | [reference/modules/observability](/ja/reference/modules/observability/) |
| Posture エクスポート | `/posture-export` | コントロールタワー向けにグランドトゥルース posture をエクスポート | `posture:export:read` | [reference/modules/posture-export](/ja/reference/modules/posture-export/) |
| 記録 | `/recordings` | 特権セッションの記録とリプレイ | `recording:session:admin` | [reference/modules/recording](/ja/reference/modules/recording/) |
| レポート | `/reporting` | ガバナンスレポートの生成とダウンロード | `reporting:report:read` | [reference/modules/reporting](/ja/reference/modules/reporting/) |
| セッションビューアー | `/session-viewer/$id`（ディープリンクのみ） | 記録一覧の行から開く、1 つの記録済みセッションの完全なタイムライン。サイドバーには表示されない。 | `recording:session:admin` | [reference/modules/recording](/ja/reference/modules/recording/) |
| チームコスト | `/team-costs` | チーム別に帰属した支出。プロジェクト別、モデル別の内訳へ展開可能。 | `finops:spend:read` | [reference/modules/xi-finops](/ja/reference/modules/xi-finops/) |

### システムと設定

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| API Playground | `/api-playground` | コントロールプレーン API を対話的に探索、テスト | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ja/reference/modules/xix-api-manage-as-code/) |
| バックアップ | `/backups` | バックアップの実行、スケジュール、ダウンロード、リストア。破壊的経路では 2 回目の確認を行う。 | `system:admin` | [how-to/backup-and-restore](/ja/how-to/backup-and-restore/) |
| 管理 | `/console` | ユーザー、SSO/IdP、ワークスペース、エージェントグループ、ロール、シークレット、コネクター、API キー、このインストールのライセンス | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ja/reference/modules/xx-multi-tenancy/) |
| ソースの差分 | `/console/sources/diff` | 接続された Git リポジトリのベースリビジョンとヘッドリビジョンをファイルごとに比較します | `system:admin` | [reference/console](/ja/reference/console/) |
| ログ | `/logs` | レベルやモジュールで絞り込み、検索、一時停止ができるエンジンのライブログストリーム。 | `system:admin` | [how-to/troubleshooting](/ja/how-to/troubleshooting/) |
| セットアップウィザード | `/onboarding` | 段階的なデプロイメント設定 | `system:admin` | [start/quickstart](/ja/start/quickstart/) |
| テナント | `/tenants` | テナントのサービスを停止または復旧 | `system:admin` | [how-to/troubleshooting](/ja/how-to/troubleshooting/) |

### ログイン、セットアップ、アカウント

これらは機能レジストリの外側にマウントされます。**no sign-in** と記されたものは
セッションが存在する前に提供され、そのようなコンソールルートはこの 4 つだけです。

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 招待を受け入れる | `/accept-invite` | メールで届いた招待リンクの遷移先。事前のセッションなしでパスワードを設定し、ワークスペースへ参加する。 | **no sign-in** | — |
| AI | `/areas/ai` | AI 領域のディレクトリです。セッションの監視と運用、プロバイダーのプロファイルと環境、モデル、専用実行、プロバイダーリファレンスを扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| 自動化 | `/areas/automation` | 自動化 領域のディレクトリです。フローとオーケストレーション、イベントと通知を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| データとコンテキスト | `/areas/data-context` | データとコンテキスト 領域のディレクトリです。能力、ナレッジ、成果物を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| デプロイ | `/areas/deployment` | デプロイ 領域のディレクトリです。デプロイの準備と制御を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| インフラ | `/areas/infrastructure` | インフラ 領域のディレクトリです。環境のインベントリとワークスペースを扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| 可観測性とエビデンス | `/areas/observation` | 可観測性とエビデンス 領域のディレクトリです。状態とアクティビティ、コストと導入、監査、評価とエビデンスを扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| セキュリティとアイデンティティ | `/areas/security-identity` | セキュリティとアイデンティティ 領域のディレクトリです。アイデンティティとアクセス、ポリシーとガバナンスの境界、保護と対応を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| システムと設定 | `/areas/system` | システムと設定 領域のディレクトリです。管理、インストールと保守、開発者ツール、個人設定を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| 作業とコミュニケーション | `/areas/work-communications` | 作業とコミュニケーション 領域のディレクトリです。セッション間で永続的に記録される作業とガバナンス下の通信を扱います。権限で許可された項目を一覧表示します。 | ログイン済みのすべてのユーザー | — |
| ログイン | `/login` | プロビジョニング済みアカウントのクレデンシャルまたはトークンによるログインページ。 | **no sign-in** | — |
| 設定 | `/settings` | ワークスペースとアカウントの設定 | any signed-in user | — |
| 初回セットアップ | `/setup` | 新規デプロイメントを利用可能にする 1 回限りのページ。セットアップトークンを消費して最初の owner アカウントを作成する。 | **no sign-in** | — |
| 公開ステータス | `/status-page` | ログインしていない人向けのコンポーネント健全性。ページを開いている間は自動更新される。 | **no sign-in** | — |

<!-- END GENERATED olivares-console-routes -->

## このページでは分からないこと

これは地図であって手順書ではありません。どの画面があり、どこにあり、誰が開けるかを
示しますが、タスクの手順は説明しません。タスク別の案内は
[ロール別の経路](/ja/start/paths-by-role/)または
[ハウツーガイド](/ja/how-to/self-hosting/)から始めてください。

バックエンドが operator によってプロビジョニングされるまで deny-closed である画面も、
他と同様にここへ表示されます。ルートは存在し、権限も実在します。どのモジュールが
作動し、どれがゲートされるかは[モジュール概要](/ja/reference/modules/overview/)に、
一般原則は[正直さと限界](/ja/start/honesty-and-limits/)に記録されています。エリア
ディレクトリの一覧は、能力や準備状態のライブな読み取りではありません。
