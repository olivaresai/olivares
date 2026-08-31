---
title: コンソールリファレンス — 全画面と必要な権限
description: >-
  Olivares AI コンソールが公開するすべてのルートを 5 つのハブ別にまとめ、
  それぞれに必要な RBAC 権限と、製品内ヘルプリンクが開くリファレンスページを
  示します。コンソール自身のルート一覧から生成されています。
---

このページはコンソールの地図です。アプリケーションがマウントする**すべてのルート**を、
選抜でも、誰かが覚えていて文書化したものだけでもなく、principal が開くために必要な
権限と詳細情報の参照先とともに列挙します。

このページは**生成物**です。一覧は `web/src/features/route-census.json` から取得されます。
これは `registry.route-conservation.test.ts` がビルド済みルーターに対して固定する
append-only な一覧であり、画面が追加、移動、消失すれば、このページも必ず変わります。
各画面の名前と 1 行説明は、サイドバーと同じ翻訳カタログから取得した**コンソール自身の
文字列**です。ここで読む内容は製品で目にする内容と同じです。

:::note[権限を強制するのはこの表ではなくエンジン]
`必要な権限` 列は、コンソールがルートを提示する前に確認する権限であり、エンジンの
RBAC を反映します。権限の主体はあくまでエンジンです。持っていない権限が必要な画面への
ディープリンクは、サイドバーで隠されるだけでなく API によって拒否されます。
[ロールと権限](/ja/reference/modules/vi-governance/)を参照してください。
:::

## このページの読み方

- **画面** — サイドバーとコマンドパレットで使われる名前。
- **パス** — デプロイしたコンソールの origin 配下の URL。これは公開 contract です。
  ブックマーク、runbook のディープリンク、ドキュメントの相互参照はいずれもこの文字列を
  使います。
- **必要な権限** — RBAC 権限。`any signed-in user` は、すべての認証済み
  principal に開かれたルートを意味します。**no sign-in** は、セッションが存在する前に
  提供されることを意味します。
- **リファレンス** — その画面でコンソール自身のヘルプリンクが開くページ。

以下の 5 つの見出しは、サイドバーで表示される順に並べたコンソールのハブです。

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

コンソールは **59 ルート**を公開します。以下の表に、必要な権限と、製品内
ヘルプリンクが開くリファレンスページとともに、すべて掲載されています。

### 運用

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 概要 | `/` | 環境全体の概要と健全性を一覧表示 | any signed-in user | [ドキュメントホーム](/ja/) |
| Claude Code | `/agentops` | SSH を使わず Claude Code セッションを作成、接続、統制 | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ja/how-to/run-claude-code-with-olivares/) |
| バックアップ | `/backups` | バックアップの実行、スケジュール、ダウンロード、リストア。破壊的経路では 2 回目の確認を行う。 | `system:admin` | [how-to/backup-and-restore](/ja/how-to/backup-and-restore/) |
| 健全性と SLA | `/health` | エージェントと MCP の稼働時間および SLA | `health:status:read` | [reference/modules/xxii-health](/ja/reference/modules/xxii-health/) |
| キルスイッチ | `/killswitch` | 緊急停止、二重統制による復旧、guardian containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ja/how-to/cookbook/kill-switch-drill/) |
| ログ | `/logs` | レベルやモジュールで絞り込み、検索、一時停止ができるエンジンのライブログストリーム。 | `system:admin` | [how-to/troubleshooting](/ja/how-to/troubleshooting/) |
| オブザーバビリティ | `/observability` | 標準別の取り込み健全性とトレースのドリルダウン | `health:status:read` | [reference/modules/observability](/ja/reference/modules/observability/) |
| サンドボックス | `/sandbox` | 隔離されたエージェントのテストとリプレイ | `sandbox:run:read` | [reference/modules/xvii-sandbox](/ja/reference/modules/xvii-sandbox/) |
| セッション | `/sessions` | エージェントのライブ運用とタイムライン | `sessions:live:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| テナント | `/tenants` | テナントのサービスを停止または復旧 | `system:admin` | [how-to/troubleshooting](/ja/how-to/troubleshooting/) |
| 音声 | `/voice` | 音声およびリアルタイムセッション | `voice:session:read` | [reference/modules/xvi-voice](/ja/reference/modules/xvi-voice/) |
| 作業 | `/work` | セッションをまたぐ永続的なバックログ: 項目、依存関係、受け入れ、決定 | `sessions:work:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| ワークスペース | `/workspace` | 1 つのワークスペースにスコープされたエージェント、セッション、リソース、アクティビティ | `tenant:read` | [reference/modules/xx-multi-tenancy](/ja/reference/modules/xx-multi-tenancy/) |
| ワークスペーステンプレート | `/workspace-templates` | 再利用可能なセッション設定スナップショット: フック、設定、コネクタ、ポリシー。 | `sessions:template:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |

### 自動化

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アラート | `/alerting` | findings を宛先へルーティングし、配信を確認 | `notify:route:read` | [reference/modules/xv-notify](/ja/reference/modules/xv-notify/) |
| オートメーション | `/automations` | 3 つすべての自動化レールとトリガーカタログ | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/ja/reference/modules/iv-orchestration/) |
| Webhook とイベント | `/eventing` | アウトバウンド Webhook サブスクリプション、配信ログ、デッドレターキュー。 | `eventing:subscription:read` | [reference/modules/eventing](/ja/reference/modules/eventing/) |
| オーケストレーション | `/orchestration` | エージェント間の連携とスケジュール | `orchestration:graph:read` | [reference/modules/iv-orchestration](/ja/reference/modules/iv-orchestration/) |

### 接続

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| API Playground | `/api-playground` | コントロールプレーン API を対話的に探索、テスト | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ja/reference/modules/xix-api-manage-as-code/) |
| MCP とスキル | `/capabilities` | MCP サーバー、スキル、ツールを統制 | `capabilities:catalog:read` | [reference/modules/v-capabilities](/ja/reference/modules/v-capabilities/) |
| カタログ | `/catalog` | キュレーションされ承認されたエージェントと capability | `catalog:entry:read` | [reference/modules/xiv-catalog](/ja/reference/modules/xiv-catalog/) |
| プロトコルバインディング | `/communications/protocol-bindings` | 統制された A2A と MCP のバインディングを構成し reconcile | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ja/reference/modules/ii-sessions/) |
| デプロイメント | `/deploy` | エージェントをインフラへプロビジョニングして接続 | `deploy:deployment:read` | [reference/modules/vii-deploy](/ja/reference/modules/vii-deploy/) |
| インベントリ | `/inventory` | すべてのエージェント、MCP、モデルを発見してカタログ化 | `inventory:catalog:read` | [reference/modules/i-inventory](/ja/reference/modules/i-inventory/) |
| ナレッジ | `/knowledge` | ナレッジベース、RAG、データリネージ | `knowledge:kb:read` | [reference/modules/viii-knowledge](/ja/reference/modules/viii-knowledge/) |
| モデル運用 | `/model-operations` | 所有モデル、admission、デプロイメント | `models:registry:read` | [reference/modules/xxiii-model-operations](/ja/reference/modules/xxiii-model-operations/) |
| モデル | `/models` | モデル、ルーティング、プロバイダー鍵 | `models:catalog:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| セットアップウィザード | `/onboarding` | 段階的なデプロイメント設定 | `system:admin` | [start/quickstart](/ja/start/quickstart/) |
| プラットフォーム | `/platforms` | デプロイ面、コンプライアンスマトリクス、プラットフォーム別モデルライフサイクル | `models:platforms:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |

### 統制

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| アクセスマップ | `/access-map` | 各エージェントが読み書きするもの（R/RW） | `accessmap:graph:read` | [reference/modules/iii-access-map](/ja/reference/modules/iii-access-map/) |
| AgentCore エクスポート | `/agentcore-export` | AWS AgentCore への Cedar ポリシーエクスポートを計画、適用し、実行前に変更内容を確認。 | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| Claude Code ガバナンス | `/claude-policy` | 管理ポリシー、フック、MCP、サンドボックス、policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ja/how-to/connectors/claude-code-hooks-pep/) |
| コントロールコンソール | `/console` | ユーザーのオンボード、SSO/IdP の接続、ワークスペースとエージェントグループの構成。 | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ja/reference/modules/xx-multi-tenancy/) |
| アイデンティティと NHI | `/identity` | SSO、SCIM、NHI 一覧、WIF グラフ | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| 推論プロキシ | `/inference-proxy` | プロキシゲート、エグレス DLP ルール、デバイス承認 | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ja/reference/modules/inferenceproxy/) |
| 権限 | `/permissions` | アイデンティティ、ロール、承認 | `governance:identity:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |
| レート制限 | `/rate-limits` | Anthropic のレート制限インベントリ（読み取り専用） | `models:ratelimits:read` | [reference/modules/x-models](/ja/reference/modules/x-models/) |
| データレジデンシー | `/residency` | 各組織をリージョンへ固定、または未固定のままにする | `system:admin` | [reference/modules/xiii-compliance](/ja/reference/modules/xiii-compliance/) |
| ルーチンポリシー | `/routine-policies` | Claude Code ルーチンの頻度下限、同時実行上限、承認要件、cron allowlist。 | `governance:routine:read` | [reference/modules/vi-governance](/ja/reference/modules/vi-governance/) |

### 証明

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| Claude Code の導入状況 | `/adoption` | 生産性、受け入れ、モデル構成 | `adoption:metrics:read` | [reference/modules/claudeadoption](/ja/reference/modules/claudeadoption/) |
| エージェントアーティファクト | `/agent-artifacts` | スキル、MCP 拡張、指示ファイル — レジストリ、posture、サプライチェーン BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/ja/reference/modules/xxiii-model-operations/) |
| サプライチェーン | `/attestation` | リリースアテステーション — SLSA、SBOM、VEX、Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ja/how-to/verify-a-release/) |
| 監査台帳 | `/audit` | 改ざん検知可能な証跡台帳 | `audit:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |
| コンプライアンス | `/compliance` | フレームワーク、コントロール、証跡 | `compliance:framework:read` | [reference/modules/xiii-compliance](/ja/reference/modules/xiii-compliance/) |
| ダッシュボード | `/dashboards` | 経営指標とレポート | any signed-in user | [reference/modules/xxi-executive-dashboards](/ja/reference/modules/xxi-executive-dashboards/) |
| 評価 | `/evals` | 品質、評価、リグレッション | `evals:run:read` | [reference/modules/xii-evals](/ja/reference/modules/xii-evals/) |
| コストと FinOps | `/finops` | トークンコスト、予算、支出 | `finops:spend:read` | [reference/modules/xi-finops](/ja/reference/modules/xi-finops/) |
| Posture エクスポート | `/posture-export` | コントロールタワー向けにグランドトゥルース posture をエクスポート | `posture:export:read` | [reference/modules/posture-export](/ja/reference/modules/posture-export/) |
| 記録 | `/recordings` | 特権セッションの記録とリプレイ | `recording:session:admin` | [reference/modules/recording](/ja/reference/modules/recording/) |
| レッドチーム | `/red-team` | エージェントに対する敵対的テスト | `redteam:target:read` | [reference/modules/xviii-redteam](/ja/reference/modules/xviii-redteam/) |
| レポート | `/reporting` | ガバナンスレポートの生成とダウンロード | `reporting:report:read` | [reference/modules/reporting](/ja/reference/modules/reporting/) |
| セキュリティ | `/security` | ガードレール、フォレンジック、異常 | `security:finding:read` | [reference/modules/ix-security](/ja/reference/modules/ix-security/) |
| セッションビューアー | `/session-viewer/$id`（ディープリンクのみ） | 記録一覧の行から開く、1 つの記録済みセッションの完全なタイムライン。サイドバーには表示されない。 | `recording:session:admin` | [reference/modules/recording](/ja/reference/modules/recording/) |
| チームコスト | `/team-costs` | チーム別に帰属した支出。プロジェクト別、モデル別の内訳へ展開可能。 | `finops:spend:read` | [reference/modules/xi-finops](/ja/reference/modules/xi-finops/) |

### ログイン、セットアップ、アカウント

これらは機能レジストリの外側にマウントされます。**no sign-in** と記されたものは
セッションが存在する前に提供され、そのようなコンソールルートはこの 4 つだけです。

| 画面 | パス | 内容 | 必要な権限 | リファレンス |
|---|---|---|---|---|
| 招待を受け入れる | `/accept-invite` | メールで届いた招待リンクの遷移先。事前のセッションなしでパスワードを設定し、ワークスペースへ参加する。 | **no sign-in** | — |
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
一般原則は[正直さと限界](/ja/start/honesty-and-limits/)に記録されています。
