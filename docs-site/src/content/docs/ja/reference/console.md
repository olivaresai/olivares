---
title: コンソールリファレンス — 全画面と必要な権限
description: >-
  Olivares AI コンソールが公開するすべてのルートを、コンソールのエリアごとに
  まとめ、それぞれに必要な RBAC 権限と、製品内ヘルプリンクが開くリファレンスページを
  掲載します。コンソール自身のルート一覧から生成されています。
---

:::note[エディション]
ディレクトリソース（LDAP、Okta/Entra、エージェント・ワークロード IdP）、管理対象アイデンティティのライフサイクル、受信 SCIM サーバーには Business/Enterprise アドオンが必要です。一般的なソース取り込み、リソースカタログ、セキュリティ検出結果、Access Map、アイデンティティインベントリ、単一 IdP の SSO、ローカルロールとバインディング、ポリシーと承認は Community でも利用できます。有料コンソールパネルと CLI コマンドは表示されず、その公開 API ルートは HTTP `501` を返します。モジュールを有効にしても Business 実装は追加されません。
:::

このページはコンソールの地図です。アプリケーションがマウントする**すべてのルート**を、
選抜でも、誰かが覚えていて文書化したものだけでもなく、principal が開くために必要な
権限と詳細情報の参照先とともに列挙します。

コンソールのナビゲーションは**左サイドバーひとつ**です。場所を 4 つのグループ（**作業**（ホーム、セッション、ファイルとワークスペース、作業とコミュニケーション）、**ツール**（AI ツール、モデル、MCPとスキル、ソース）、**コントロール**（ルールと承認、使用量とコスト、アクティビティと監査、アクセスマップ）、**管理**（ユーザーとグループ、セキュリティ、運用、ライセンス））に分けて並べ、設定を一番下に置きます。各画面は 1 つの場所に属し、画面の上のリンク行はその場所のほかの画面を並べます。場所は、権限で少なくとも 1 つの画面を開けるときだけ表示され、Business の画面は Business エディションでだけ表示されます。サイドバーはアイコンだけに折りたたんで、また開けます。狭い画面では、同じサイドバーがメニューボタンからドロワーとして開きます。上部バーには、ページのタイトル、検索（⌘K または Ctrl+K）、通知、アカウントメニューがあります。コマンドパレットは同じ画面を同じ権限で検索します。以前のエリアディレクトリのアドレス（`/areas/…`）は、その画面を今持つ場所を、クエリとフラグメントを保ったまま開きます。

新規インストールでは、最初の作業だけが一覧に表示されます（ホーム、セッション、AI ツール、
承認、セットアップウィザード、設定）。それ以外の画面はプレビューです。
アドレス、API、CLI はそのまま動作し、ナビゲーション、コマンドパレット、ショートカットには
表示されません。アップグレード前から存在するインストールでは、これまでどおりすべての画面が
表示されます。管理者がモジュールを選択したインストール（次回の起動から）と、デモデータを
使うインストールも同様です。

このページは**生成物**です。一覧は `web/src/features/route-census.json` から取得されます。
これは `registry.route-conservation.test.ts` がビルド済みルーターに対して固定する
append-only な一覧であり、画面が追加、移動、消失すれば、このページも必ず変わります。
各画面の名前と 1 行説明は、コンソールと同じ翻訳カタログから取得した**コンソール自身の
文字列**です。ここで読む内容は製品で目にする内容と同じです。以下の表は、それらの行をエリアごとにまとめ、ホーム（概要）を先頭に、サインイン、
セットアップ、アカウントを最後に置きます。以前の 9 つのエリアディレクトリのアドレスは、機能
レジストリの外にマウントされたルートと同じ表にあり、それぞれ対応する場所へリダイレクトします。

:::note[権限を強制するのはこの表ではなくエンジン]
`必要な権限` 列は、エンジンが返す実効権限に基づき、コンソールがルートを提示する前に
確認する権限を示します。エンジンは、コンソール外からのリクエストも含め、API
リクエストを独立して認可します。項目が表示されることは、そのモジュールが設定済みで
実行準備が整っていることを保証しません。
[ロールと権限](/ja/reference/modules/vi-governance/)を参照してください。
:::

## このページの読み方

- **画面** — 画面の上のリンク行とコマンドパレットで使われる名前。
- **パス** — デプロイしたコンソールの origin 配下の URL。これは公開 contract です。
  ブックマーク、runbook のディープリンク、ドキュメントの相互参照はいずれもこの文字列を
  使います。
- **必要な権限** — RBAC 権限。`any signed-in user` は、すべての認証済み
  principal に開かれたルートを意味します。**no sign-in** は、セッションが存在する前に
  提供されることを意味します。
- **リファレンス** — その画面でコンソール自身のヘルプリンクが開くページ。

以下の見出しは、ルート一覧が画面をまとめるレジストリのエリアです。インフラ、AI、データと
コンテキスト、作業とコミュニケーション、自動化、セキュリティとアイデンティティ、
デリバリー、監督、システムと設定。

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

コンソールは **66 ルート**を公開します。以下の表に、必要な権限と、製品内
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
| エージェントツール | `/agent-tools` | セッションが使うアカウントと API キーを確認します。ツールのインストールとサインインはデプロイ管理者のみです | `sessions:account:read` | [how-to/add-a-provider](/ja/how-to/add-a-provider/) |
| セッションを運用 | `/agentops` | セッションを開始し、Olivares が見つけたものも含めて、それぞれの動きを追います | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ja/how-to/run-claude-code-with-olivares/) |
| モデル運用 | `/model-operations` | 所有モデル、admission、デプロイメント | `models:registry:read` | [reference/modules/xxiii-model-operations](/ja/reference/modules/xxiii-model-operations/) |
| モデル | `/models` | モデル、ルーティング、ガバナンス | `models:catalog:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| プラットフォーム | `/platforms` | デプロイ面、コンプライアンスマトリクス、プラットフォーム別モデルライフサイクル | `models:platforms:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| プロバイダーアカウント | `/provider-accounts` | 名前付きのプロバイダーアカウントを一覧し、既存のプロバイダープロファイルをアカウントとして採用する | `sessions:account:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| ソースバインディング | `/provider-bindings` | 構成済みソースをプロバイダープロファイルにリンクする | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロバイダープロファイル | `/provider-profiles` | セッションの起動元となるプロバイダーのホームを登録、管理し、その設定を必要に応じて読み取る | `sessions:profile:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロバイダー | `/providers` | セッションの起動に使う API キーとエンドポイントを登録し、テスト・交換・失効を行う | `sessions:provider:read` | [how-to/add-a-provider](/ja/how-to/add-a-provider/) |
| レート制限 | `/rate-limits` | Anthropic のレート制限インベントリ（読み取り専用） | `models:ratelimits:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| リーチ | `/reach` | 各ツールが到達できる接続先と、承認待ちの接続先を確認できます。 | `accessmap:graph:read` | [reference/modules/iii-access-map](/ja/reference/modules/iii-access-map/) |
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

### 作業とコミュニケーション

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| コミュニケーション | `/communications` | 選択中ワークスペースのチャネル、ダイレクト通知、個人受信箱 | `sessions:channel:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| チャネル管理 | `/communications/administration` | チャネルを管理：設定と付与履歴 | `sessions:channel:admin` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| ハンドオフ | `/communications/handoffs` | あなた宛ての作業責任のオファー: コンテキストを読んで承諾または拒否します | `sessions:delivery:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| コミュニケーション受信箱 | `/communications/inbox` | 自分宛てのメッセージ：1 件ずつ開いて確認します | `sessions:delivery:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| 新しいチャネル | `/communications/new` | 明示的な初期付与でチャネルを作成 | `sessions:channel:write` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| プロトコルバインディング | `/communications/protocol-bindings` | 統制された A2A と MCP のバインディングを構成し reconcile | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| 作業 | `/work` | 共有作業：項目、依存関係、受け入れ、決定 | `sessions:work:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |

### 自動化

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アラート | `/alerting` | findings を宛先へルーティングし、配信を確認 | `notify:route:read` | [reference/modules/xv-notify](/ja/reference/modules/xv-notify/) |
| Webhook とイベント | `/eventing` | アウトバウンド Webhook サブスクリプション、配信ログ、デッドレターキュー。 | `eventing:subscription:read` | [reference/modules/eventing](/ja/reference/modules/eventing/) |

### セキュリティとアイデンティティ

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アクセスマップ | `/access-map` | 各エージェントが読み書きするもの（R/RW） | `accessmap:graph:read` | [reference/modules/iii-access-map](/ja/reference/modules/iii-access-map/) |
| ポリシーのエクスポート | `/agentcore-export` | このテナントのガバナンスルールを Cedar ポリシーとして AWS AgentCore へ投影する計画・確認・適用。計画では何も書き込みません | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| 管理ポリシー | `/claude-policy` | 管理ポリシー、フック、MCP、サンドボックス、policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ja/how-to/connectors/claude-code-hooks-pep/) |
| アイデンティティとアクセス | `/identity` | SSO、SCIM、エージェントが使うアイデンティティ | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| 推論プロキシ | `/inference-proxy` | プロキシゲート、エグレス DLP ルール、デバイス承認 | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ja/reference/modules/inferenceproxy/) |
| キルスイッチ | `/killswitch` | 緊急停止、二重統制による復旧、guardian containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ja/how-to/cookbook/kill-switch-drill/) |
| 権限 | `/permissions` | アイデンティティ、ロール、承認 | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| ルーチンポリシー | `/routine-policies` | Claude Code ルーチンの頻度下限、同時実行上限、承認要件、cron allowlist。 | `governance:routine:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| セキュリティ | `/security` | ガードレール、フォレンジック、異常 | `security:finding:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |
| ボールト | `/vault` | パスワード、メモ、認証コード | `tenant:read` | [reference/console](/ja/reference/console/) |

### デリバリー

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| Git 公開 | `/git-publication` | 承認済みの Git ターゲットを通じてコミットのプッシュ、プルリクエストの作成、マージを行う | `gitpublish:target:read` | [reference/modules/gitpublish](/ja/reference/modules/gitpublish/) |

### 監督

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 導入状況 | `/adoption` | 生産性、受け入れ、モデル構成 | `adoption:metrics:read` | [reference/modules/claudeadoption](/ja/reference/modules/claudeadoption/) |
| サプライチェーン | `/attestation` | リリースアテステーション — SLSA、SBOM、VEX、Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ja/how-to/verify-a-release/) |
| 監査台帳 | `/audit` | 改ざん検知可能な証跡台帳 | `audit:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |
| ダッシュボード | `/dashboards` | 経営指標とレポート | any signed-in user | [reference/modules/xxi-executive-dashboards](/ja/reference/modules/xxi-executive-dashboards/) |
| 評価 | `/evals` | 品質、評価、リグレッション | `evals:run:read` | [reference/modules/xii-evals](/ja/reference/modules/xii-evals/) |
| 健全性と SLA | `/health` | エージェントと MCP の稼働時間および SLA | `health:status:read` | [reference/modules/xxii-health](/ja/reference/modules/xxii-health/) |
| オブザーバビリティ | `/observability` | 標準別の取り込み健全性とトレースのドリルダウン | `health:status:read` | [reference/modules/observability](/ja/reference/modules/observability/) |
| 保持期間 | `/retention` | 記録クラスごとの保持期間、それを適用するスイープ、封印された証明書 | `compliance:retention:read` | [reference/modules/xiii-compliance](/ja/reference/modules/xiii-compliance/) |
| 予算 | `/stored-budgets` | 保存済みの各上限に対する消費を表示します。予約済みキャパシティは上限にカウントされます。 | `finops:budget:read` | [reference/modules/xi-finops](/ja/reference/modules/xi-finops/) |
| 使用量 | `/usage` | 各アカウントの公式の 5 時間と週の上限（上限に近い順）と、今日のトークンとコスト。 | `sessions:run:read` | [reference/console](/ja/reference/console/) |

### システムと設定

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| API Playground | `/api-playground` | コントロールプレーン API を対話的に探索、テスト | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ja/reference/modules/xix-api-manage-as-code/) |
| バックアップ | `/backups` | バックアップの実行、スケジュール、ダウンロード、リストア。破壊的経路では 2 回目の確認を行う。 | `system:admin` | [how-to/backup-and-restore](/ja/how-to/backup-and-restore/) |
| 管理 | `/console` | ユーザー、SSO/IdP、ワークスペース、エージェントグループ、ロール、シークレット、コネクター、API キー、このインストールのライセンス | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ja/reference/modules/xx-multi-tenancy/) |
| ソースの差分 | `/console/sources/diff` | 接続された Git リポジトリのベースリビジョンとヘッドリビジョンをファイルごとに比較します | `system:admin` | [reference/console](/ja/reference/console/) |
| ログ | `/logs` | レベルやモジュールで絞り込み、検索、一時停止ができるエンジンのライブログストリーム。 | `system:admin` | [how-to/troubleshooting](/ja/how-to/troubleshooting/) |
| セットアップウィザード | `/onboarding` | 段階的なデプロイメント設定 | `system:admin` | [start/quickstart](/ja/start/quickstart/) |
| アップデート | `/updates` | このエンジンが実行しているバージョン、チャンネルの最新の署名付きリリース、そしてインストールする操作です | `system:admin` | [how-to/upgrade-and-rollback](/ja/how-to/upgrade-and-rollback/) |

### ログイン、セットアップ、アカウント

これらは機能レジストリの外側にマウントされます。**no sign-in** と記されたものは
セッションが存在する前に提供され、そのようなコンソールルートはこの 4 つだけです。

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 招待を受け入れる | `/accept-invite` | メールで届いた招待リンクの遷移先。事前のセッションなしでパスワードを設定し、ワークスペースへ参加する。 | **no sign-in** | — |
| ログイン | `/login` | プロビジョニング済みアカウントのクレデンシャルまたはトークンによるログインページ。 | **no sign-in** | — |
| デバイスをペアリング | `/pair` | 1 回限りのデバイスペアリングリンクの遷移先です。新しいデバイスは既存のセッションなしでログインします。 | **no sign-in** | — |
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
