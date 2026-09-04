<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — エンタープライズ AI のグラウンドトゥルース" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**あなたが運用する AI を統合・管理・セキュアにする — 単一のセルフホスト型バイナリから。**

[インストール](#install) · [クイックスタート](#quickstart) · [サンプル](examples/) · [ドキュメント](#documentation) · [セキュリティ](#security) · [コントリビュート](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **ベータ版**、活発に開発中です。最初のタグ付きリリース **v26.8.0** は、署名済みアーカイブ、ネイティブパッケージ、コンテナイメージを提供します。API とモジュールサーフェスは 1.0 までに変更される可能性があります。今日動作するもの、オンデマンドのもの、設計段階のものは、[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)に、また各モジュールについては[モジュールカタログ](docs-site/src/content/docs/reference/modules/overview.md)に記載されています。

## Olivares AI とは

現在運用しているのは estate です — コーディングエージェント、MCP サーバー、モデルエンドポイント、サービスアカウント、スケジュール済みジョブが、ひとつのシステムとして設計されていない複数のマシンに分散しています。Olivares AI は、それらをまとめる、コンソール内蔵の単一のセルフホスト型 Go バイナリです。AI には作業に必要なもの（コンテキスト、リソースへのアクセス、管理されたセッション）を与え、あなたには、何が稼働し、誰が起動し、何に到達し、いくらかかり、誰が同意したのかを把握するための権限、ポリシー、予算、証拠を与えます。

**設計から multi-provider。** Claude Code は最も深いレベルで統合されています — `PreToolUse`/`PostToolUse` フック、managed settings、コンソールからの起動と停止、subject ごとの model access — その傍らに Codex と Grok Build を first-class command surface として置き、gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes はそれぞれ独自のコネクタとして扱います。各コネクタは、強制できることと観測しかできないことを明示します。Ollama とその他のセルフホスト型エンドポイントは、ローカルコネクタを通じてインベントリ化され、このコネクタは設計上読み取り専用です。

**誰が運用するか。** あらゆる規模で同じビルドを使用します。ホームサーバー（1 つのバイナリ、SQLite、ループバックへのバインド）、クライアントごとにテナントを持ち、請求書が届く前に支出を拒否する予算を設定するフリーランサー、共有の作業項目、SSO、誰も手作業で組み立てる必要のない監査証跡を持つエンジニアリングチーム、Postgres の行レベルセキュリティ、HA、エアギャップインストール、WORM アーカイブを使用する規制対象のエンタープライズです。オープンビルドはプラットフォーム全体であり、商用アドオンはその上に加わる追加コードであって、オープンビルドから削除された機能では決してありません。SSO、HA、WORM、実際に拒否する予算は、初回起動時のデフォルトではなく、自らプロビジョニングするものです。

必須のテレメトリはなく、デフォルトではコントロールプレーンからのエグレスもありません。あなたの境界を越えるのは、あなたがそのように設定したものだけです — あなたのモデル API への呼び出し、接続した SIEM／Webhook 出力、用意した場合の埋め込みプロバイダーです。コレクターはすでに運用しているシステムから読み取るため、障害が発生したコレクターが本番のデータパス上に置かれることはありません。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="あらゆる規模で 1 つのバイナリ。ホームサーバーから規制対象のエンタープライズまで、どこで動作し、何に到達するか。">
</picture>
<sub>ホームラボから規制対象企業まで、同じオープンビルド。</sub>
</div>

## 何をするか

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="アクセスマップ: 各エージェントが estate 全体で何を読み書きするか。左に起点、右にリソース。">
</picture>
<sub><b>アクセスマップ</b> — 各エージェントが estate 全体で何を読み書きするか。読み取りと書き込みを色分け。</sub>
</div>

- **可視化する。** 発見されたすべてのエージェント、セッション、モデル、MCP サーバー、ツール、アイデンティティのインベントリ。それぞれが実際に到達する対象を示す read/write **アクセスマップ**と Permitted-vs-Observed **ドリフト**ビュー、ライブセッション、オーケストレーショングラフ、ヘルス、SLA。見えないものは推測せず、`unknown` として示します。
- **作業を実行する。** 所有権、依存関係、受け入れ条件、意思決定を持つ永続的な作業項目。囲い込みリースにより、2 つのエージェント — または 2 人 — が同じ作業を同時に保持することはできません。コンソールからセッションを起動、接続、停止し、A2A を介して認可済みのピアへ委譲します。シャドウモードと最終権限は構築されておらず、不在として明記されています。[ワークプレーン](docs-site/src/content/docs/explanation/work-plane.md)を参照してください。
- **統治して強制する。** Cedar 認可エンジンと **4 つの deny-closed エンフォースメントポイント** — Claude Code フック、インライン `/v1/messages` 推論プロキシ、MCP `tools/call` ゲート、A2A 委譲ゲート — により、未認可のアクションはブロックされるか、2 人による承認のために保留されるか、フックでは実行前に書き換えられます。各ポイントは、テストが未設定時の経路を実行し、拒否されることをアサートしている場合にのみカウントされます。支出を拒否またはスロットルする予算、二重統制を備えた緊急昇格、fail closed する estate **キルスイッチ**も提供します。
- **統治して供給する。** コンテンツソース（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL、ルート配下に制限されたファイルシステム）をガバナンス下の検索へ取り込みます。標準でゼロエグレスの字句検索を使用し、埋め込みプロバイダーをプロビジョニングした場合はモデルベースの意味検索を使用します。検索時にはクリアランスが deny-closed で強制されます。
- **証明する。** ハッシュチェーン化され、Ed25519 で署名された監査台帳。**26 のフレームワークカタログ**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR…）にマッピングされた封印済みの証拠 — フレームワークカタログは自己評価によるコントロールファミリーであり、認証ではありません。SIEM/ITSM プッシュ（CEF/LEEF/syslog/OTLP/OCSF）。以下はデプロイごとに構成されます: 人間と非人間のアイデンティティ（WebAuthn/FIDO2、PIV/CAC、単一 IdP SSO、SCIM 照合、エージェントアイデンティティフェデレーション）、インラインガードレール、DLP、BYOK/CMEK 暗号化、検証済みの鍵破棄を伴う消去権。

**30 のモジュール**、1 つのコンソール、**158 の統合** — コードから導出され、プッシュのたびに [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) で強制されるカウントです。統合とは Go コードを含むコネクタディレクトリを指し、そのうち 12 は共有ライブラリパッケージです。内訳は [`connectors/README.md`](connectors/README.md) に記載されています。各モジュールの成熟度は[モジュールカタログ](docs-site/src/content/docs/reference/modules/overview.md)、配線済みコネクタの忠実度階層は[コネクタリファレンス](docs-site/src/content/docs/reference/connectors.md)に記載されています。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="エージェントが連携する仕組み: 作業項目、囲い込みリース、スコープ付きメッセージからなる単一の永続的なワークプレーン。委譲はエンフォースメントゲートを通ります。未構築のシャドウモードと最終権限は破線で示されます。">
</picture>
<sub>エージェントは 1 つの永続的なワークプレーンを共有します。未構築のものは不在として描かれます。</sub>
</div>

## コンソールの内部

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="コンソールから作成、接続、統制される Claude Code セッション。"></picture><br><sub><b>Claude Code</b> — コンソールからセッションを作成、接続、統制。SSH は不要。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="作業: セッションをまたぐ、作業項目と意思決定の永続的なバックログ。"></picture><br><sub><b>作業</b> — セッションをまたぐ永続的なバックログ: 作業項目、所有権、受け入れ条件、意思決定。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="オーケストレーションと A2A: 観測されたシグナルから導出されたエージェント間の委譲グラフ。"></picture><br><sub><b>オーケストレーション &amp; A2A</b> — 誰が誰に委譲するかを観測されたシグナルから導出。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="インベントリ: estate 全体で発見されたすべてのエージェント、セッション、MCP サーバー、モデル、アイデンティティ。"></picture><br><sub><b>インベントリ</b> — 発見されたすべてのエージェント、セッション、MCP サーバー、モデル、アイデンティティ。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小権限ドリフト: アクセスマップ上に重ねた、予期しないアクセスと未使用の付与。"></picture><br><sub><b>最小権限ドリフト</b> — 観測されたが許可されていないアクセスと、未使用の付与。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="セキュリティとフォレンジック: ガードレールの検出結果、異常キュー、改ざん検知可能なフォレンジック。"></picture><br><sub><b>セキュリティとフォレンジック</b> — ガードレールの検出結果、異常、改ざん検知可能なフォレンジック。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="キルスイッチ: 二重統制による復旧を備えた estate の緊急停止。"></picture><br><sub><b>キルスイッチ</b> — ワンクリックでガバナンス下のすべての作動面を停止。復旧には 2 つのアカウントが必要。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="セッション録画ビューア: 1 つのタイムライン上のエージェント活動とガバナンス証拠。チェーン検証済み。"></picture><br><sub><b>セッション録画</b> — 1 つのタイムライン上のエージェント活動とガバナンス証拠。チェーン検証済み。</sub> |

各静止画は、実行中のバイナリが提供するシード済みデモ estate のキャプチャです（`bash scripts/docs-captures.sh` で生の一式を再生成できます）。画面の完全な一覧は[コンソールリファレンス](docs-site/src/content/docs/reference/console.md)を参照してください。

<a name="install"></a>
## インストール

すべてのリリースは cosign 署名済みの信頼チェーンの下で提供され、アーティファクト種別ごとに検証されます。cosign 署名済みチェックサムマニフェストは、そこに列挙されたアーカイブ、パッケージ、アーカイブごとの SBOM をカバーします。アーカイブごとに in-toto attestation 付きの SPDX SBOM サイドカーがあり、コンテナイメージには cosign 署名とそのイメージ独自の SBOM attestation があり、セット全体には OpenVEX ステートメントと SLSA build provenance があります。セキュリティ製品ではサプライチェーンも信頼モデルの一部です。実行前に[検証してください](docs/RELEASE-VERIFICATION.md)。

**HTTPS の簡便な経路。** スクリプト本体は HTTPS 経由で届き、パイプ処理では事前検証されません。起動後は OS とアーキテクチャを検出し、`cosign` を必須とし、署名済みチェックサムマニフェストとアーカイブの SHA-256 を検証し、バイナリだけをインストールします。`sudo` は決して呼び出しません。シェルへパイプするときはバージョンを固定してください。

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**高保証の経路。** まずダウンロードし、検証してから実行します。アーカイブ、パッケージ、チェックサムマニフェストは[リリースページ](https://github.com/olivaresai/olivares/releases/tag/v26.8.0)にあり、[`scripts/verify-release.sh`](scripts/verify-release.sh) は存在するものを検証し、何をスキップしたかを明示します — デフォルトは keyless で、切断されたホストでは `--key … --offline` を使用します。[インストーラーの信頼コントラクト](docs/RELEASE-INSTALLER.md)には両方の経路が記載されています。オプトインのサービスアダプターを備えた署名済み・バージョン付きインストーラーは、その実装が入った後にカットされる最初のリリースから提供され、v26.8.0 はそれ以前です。

| 経路 | 入手できるもの |
|---|---|
| **Linux パッケージ** — `.deb`、`.rpm`、`.apk` | バイナリ、堅牢化された systemd ユニット、env ファイルの例、ログイン不可の `olivares` サービスユーザー。サービスは自動起動されません |
| **コンテナ** — `docker.io/olivaresai/olivares:26.8.0` | distroless、非 root、`v` プレフィックスのないタグ。`ghcr.io/olivaresai/olivares` はダイジェストが同じイメージです。デフォルトイメージはマルチアーキテクチャ（amd64/arm64）で、`-fips` と `-stig` のバリアントは amd64 のみです |
| **Homebrew** — `brew install olivaresai/tap/olivares` | macOS と Linux 向けのリリースバイナリ。署名済みチェックサムと照合され、Gatekeeper の隔離属性は解除されます。darwin ビルドはまだ Apple による公証を受けていません |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) または [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | ツリー内の Helm チャートソースと、フラットな Helm 不要のマニフェスト。チャートは**まだ OCI レジストリには公開されていません** |
| **ソースから** — `task build`（Go 1.26+、[Task](https://taskfile.dev)、pnpm） | `./bin/olivares quickstart`、同じセキュアバイデフォルトの初回起動 |

エンジンは**デフォルトでセキュア**です。ループバックにバインドし、初回起動時には自己署名証明書で HTTPS を提供し、デフォルト認証情報を持たず、単回使用のセットアップトークンを表示します。コンテナまたは Pod では、プロセスは自身のネットワーク上でリッスンし、ホストマッピングまたは Service によって非公開に保たれます。**Windows** はまだビルドされていません — Linux コンテナを実行するか、WSL2（[計画](INSTALL.md#windows)）を使用してください。OS ごとのマトリクスと本番セットアップは [`INSTALL.md`](INSTALL.md)、デプロイガイド（Compose、Kubernetes、エアギャップ）と[アップグレード](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)は [`docs-site/`](docs-site/)を参照してください。

<a name="quickstart"></a>
## クイックスタート

合成 estate を探索するか、実際の運用として起動します。どちらも同じバイナリを実行します。

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

デモシードは学習専用です（公開ソースツリーのパスワード）。実データには決して向けないでください。CI は `task smoke:quickstart` で同じ経路をたどり、アクセスマップとドリフトのカウント（20 nodes / 13 edges、8 unexpected accesses と 2 unused grants）をアサートするため、このページがコードから気づかれずにずれることはありません。[完全なクイックスタート](docs-site/src/content/docs/start/quickstart.md)では実際の pgAudit コネクタを接続し、本番インストールの各経路へリンクします。

## エディション

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="構成別のエディション: AGPL コアがプラットフォーム全体で、アドオンはその上に加わる追加コード、Cloud Standard はマネージドサービス。">
</picture>
<sub>構成別のエディション。パッケージングと価格はお問い合わせください。</sub>
</div>

AGPL ビルドはプラットフォーム全体であり、内部から機能制限されることはありません。商用アドオンは追加コードであり、オープン製品から削除された機能ではありません。サブスクリプションは、署名済みモジュールパックをダウンロードするための認証情報です — 配布方式であり、すでにディスク上にあるコードをアンロックするキーではありません。セルフホスト型エンジンのユーザーアカウント数は無制限で、4 つの deny-closed エンフォースメントポイントはすべてオープンです。領域ごとのオープン、商用、計画中の能力マトリクスは [`LICENSING.md`](LICENSING.md) と[オープンコアとライセンス](docs-site/src/content/docs/explanation/open-core-and-licensing.md)を参照してください。

## アーキテクチャ

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="アーキテクチャ: エージェントサーフェス、監査ソース、MCP と A2A のピア、コンテンツソースを、コンソール、REST API、gRPC、CLI、Terraform プロバイダーを提供する単一のセルフホスト型バイナリへ収集。クラウドコントロールプレーン（構築済み・未デプロイ）とライセンスポータル（デプロイ済み・販売は無効）は別プレーン。">
</picture>
</div>

単一の静的 Go バイナリがコンソールを組み込み、文書化されたカバレッジを持つ 4 つのサーフェスを公開します。REST API（主要サーフェス）、安定したコアに絞った gRPC ミラー、`olivares` CLI、Terraform プロバイダーです。コレクターはあなたのインフラ内で 3 つのモードで動作します。ストアは SQLite または行レベルセキュリティを備えた Postgres で、ストア API で一度、Postgres でもう一度強制されます。ワークプレーンの各要素を含む詳細は [`ARCHITECTURE.md`](ARCHITECTURE.md)を参照してください。

<a name="documentation"></a>
## ドキュメント

[docs.olivares.ai](https://docs.olivares.ai) — テスト済みのインストールチュートリアル（単一ノード、Docker Compose、Kubernetes/Helm、エアギャップ）、実際のコンソールキャプチャを備えたコネクタガイド、クックブック（deny-closed ポリシー、予算、承認、キルスイッチ訓練、SIEM プッシュ）、API リファレンス、用語集。[Olivares AI とは](docs-site/src/content/docs/start/what-is-olivares-ai.md)と[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)から始めてください。

<a name="security"></a>
## セキュリティ

脆弱性は [`SECURITY.md`](SECURITY.md) を通じて非公開で報告し、公開 issue には決して投稿しないでください。エンジンは read-first かつ minimal-data です。アクセスマップはペイロードではなくエッジを保存し、アクセスマップを開く操作も記録されます。アドバイザリーフローは [`docs/security-advisories.md`](docs/security-advisories.md)、サプライチェーンの証拠マップは [`docs/openssf-badge.md`](docs/openssf-badge.md)を参照してください。

## コミュニティ

[`CONTRIBUTING.md`](CONTRIBUTING.md)（セットアップ、DCO/CLA、SPDX、コネクタ境界） · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md)（Contributor Covenant 2.1） · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog 1.1、CalVer `vYY.M.PATCH`）。

## ライセンス

`core/`、`modules/`、`web/` は **AGPL-3.0-only**、`sdk/`、`connectors/`、`clients/` は **Apache-2.0** であり、コネクタがエンジンをインポートすることはありません。商用アドオンは分離された任意のクローズドコードです — `-tags enterprise` でのみビルドされ、このリポジトリにもオープンバイナリにも決して含まれません。商用ライセンスについては `enterprise@olivares.ai` までお問い合わせください — [`LICENSING.md`](LICENSING.md)。コントリビューションには DCO sign-off（`git commit -s`）と [CLA](CLA.md) が必要です。

> **無保証・無責任。** 本ソフトウェアは**現状有姿**で提供され、**いかなる種類の保証もなく**、**データ損失、事業中断、逸失利益について一切の責任を負いません**。コントロールプレーンでは、これは形式的な文言ではありません。設定ミスにより、正当な作業がブロックされることも、まさに止めるつもりだったものが通過することもあります。AGPL-3.0-only §§15–16、Apache-2.0 §§7–8、および本プロジェクトの補足条項が適用されます — [`DISCLAIMER.md`](DISCLAIMER.md)。

## プロジェクトを支援する

コアは無料であり、今後も無料のままです。すべてのリリースを署名・検証し、最新に保つには継続的な作業が必要です。Olivares AI が役に立つ場合は、GitHub Sponsors の [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) または [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) を通じて、あるいは Ko-fi で単発の支援ができます。スポンサーシップはサポート契約ではなく、優先対応を購入するものでもありません（[`SUPPORT.md`](SUPPORT.md)）。掲載を希望するスポンサーは [`SUPPORTERS.md`](SUPPORTERS.md) に記載されます。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>エンタープライズ AI のグラウンドトゥルース。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
