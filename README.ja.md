<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**チームがすでに使っている AI を、他のインフラと同じように管理しながら実行できます。**

[できること](#できること) · [インストール](#インストール) · [エディション](#エディションと価格) · [ドキュメント](#ドキュメント) · [コミュニティ](#コミュニティ) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.5](https://img.shields.io/badge/release-0.5-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.5)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>


開発者は Claude Code や Codex で作業しています。エージェントは MCP サーバー、モデル、社内 API を呼び出し、スケジュールされたジョブは自動で動きます。それぞれが別々のログと権限を持つため、単純な疑問にもすぐには答えられません。このファイルを変更したエージェントはどれか、誰が承認したのか、今月の AI 費用はいくらだったのか。

Olivares AI は、その答えを一か所にまとめます。すでに使っているエージェントやツールに接続し、それぞれの動きを表示し、実行前にルールを適用して、すべての署名付き記録を残します。自社のサーバーで動く単一のプログラムで、製品全体を無料のオープンソースとして利用できます。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-cockpit-dark.webp">
  <img src=".github/assets/console-cockpit-light.webp" width="840" alt="Olivares AI コンソールのコックピット画面">
</picture>
<br><sub><b>コックピット</b> — 各エージェントが今していること、あなたの承認を待つ操作、アカウントごとの使用量上限。</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-session-dark.webp">
  <img src=".github/assets/console-session-light.webp" width="840" alt="Olivares AI コンソールのセッション画面">
</picture>
<br><sub><b>セッション</b> — 会話の横に、計画、エージェント、バックグラウンドタスク、変更されたファイル、使用量上限。</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-parallel-sessions-dark.webp">
  <img src=".github/assets/console-parallel-sessions-light.webp" width="840" alt="Olivares AI コンソールの並列セッション画面">
</picture>
<br><sub><b>並列セッション</b> — 6 つのエージェントが並んで作業し、それぞれ専用のブランチで、最新の出力、コスト、使用量上限を表示。</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-access-map-dark.webp">
  <img src=".github/assets/console-access-map-light.webp" width="840" alt="Olivares AI コンソールのアクセスマップ画面">
</picture>
<br><sub><b>アクセスマップ</b> — 各エージェントが何を読み書きするかを、付与された権限と照らし合わせて表示。</sub>
</div>

<div align="center">
<img src=".github/assets/console-catalog-light.png" width="840" alt="The Catalog page: approved agents, MCP servers, skills, connectors, templates and models, each with its version, status and signing.">
<br><sub><b>カタログ</b>： 承認済みのエージェント、MCP サーバー、スキル、コネクタ。それぞれの状態と署名の有無を表示。</sub>
<br><br>
<img src=".github/assets/console-work-light.png" width="840" alt="The Work page: shared work items with their type, owner and status.">
<br><sub><b>ワーク</b>： セッションが終わっても残るタスク、担当者、決定。</sub>
</div>

## できること

- **何が動いているかを把握する。** すべてのエージェント、セッション、モデル、MCP サーバー、ツールを一つのインベントリにまとめます。アクセスマップはそれぞれの読み書きを表示し、どのルールも許可していないアクセスを示します。
- **被害が出る前に操作を止める。** Olivares AI の **4 つの deny-closed エンフォースメントポイント**が、実行前に各操作を確認します。確認する場所は Claude Code 内、モデルプロキシ、各 MCP ツール呼び出し、エージェント間です。リスクのある操作は二人目の確認を待ち、禁止された操作は実行されません。一つのスイッチですべてのエージェントを同時に停止できます。確認で判断できない場合も、操作は実行されません。
- **AI の支出を管理する。** チーム、エージェント、モデルごとの予算で、請求書が届く前に警告し、支出のペースを落とすか、支出を止めます。
- **社内の知識を安全にエージェントへ渡す。** Google Drive、Confluence、Notion、Snowflake、S3、PostgreSQL に接続します。Business では SharePoint、SAP、Salesforce も使えます。各エージェントが閲覧できるのは、それを使う人に閲覧権限がある情報だけです。
- **セッションをまたいで作業を続ける。** セッションが終わっても、タスク、担当者、決定は残ります。SSH を使わずに、ブラウザーから Claude Code、Codex、Grok のセッションを開始し、参加し、停止できます。
- **求められたときに証拠を示す。** すべての決定は、後からの改ざんを検出できる署名付きログに記録されます。Business Compliance Packs は証拠を 26 フレームワークカタログに対応付け、セキュリティチームや監査担当者向けのレポートを生成します。Community は保存済みの証拠と JSON/CSV エクスポートを引き続き提供します。

Claude Code、Codex、Grok、Cursor、gemini-cli、opencode、OpenHands、Ollama 経由のローカルモデルなど、既存のツールと連携します。**32 のモジュール**と **136 件の統合**があります。[全モジュール](docs-site/src/content/docs/reference/modules/overview.md) · [全コネクター](connectors/README.md)。

Community はローカルの可観測性、保存済み設定、バックアップのエクスポートを保持します。SIEM/ITSM 配信、外部テレメトリー配信、ポスチャーのエクスポートは Business の基本版に含まれます。

**セッションのネットワーク分離は、プロバイダーキーに紐づく Claude Code、Codex、OpenCode のセッションと、ツール自身のサインインを使う OpenCode のセッションを対象とします。** そのようなセッションは、自身のネットワーク名前空間にあるセッションごとのプロキシを通じて、プロバイダーのエンドポイント、またはサインイン先のホストにのみ到達し、そうでなければ開始しません。プロバイダーキーを使う OpenCode では、ホストは以前のインベントリに基づいており、プロキシの背後で示されたターンに基づくものではありません。Grok Build のセッションと、他のツールのアカウントログインによるセッションは、ネットワークが許可する任意のホストに引き続き到達できます。1 つのプロバイダーへの排他的なアクセスが必要なワークロードを実行する前に、[セッションのネットワーク分離](SECURITY.md#session-network-isolation)を確認してください。

## インストール

方法を一つ選び、そのコードブロックをコピーしてください。最後に `olivares quickstart` がコンソールのアドレスと、最初の管理者を作成するための一回限りのトークンを表示します。すべてのリリースに署名が付いており、どの方法でもダウンロードしたものを検証してからインストールします（[ダウンロードを自分で検証する](INSTALL.md#verifying-a-release)）。

**Linux と macOS、コマンド一つで。** システムを検出し、リリースを検証して、バイナリだけをインストールします。`sudo` は使いません。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker。** マルチアーキテクチャ。コンテナイメージは Debian 13 slim（エージェントツール用の Node.js 24 を含む）をベースとし、非 root ユーザーとして実行されます。 ホストのすべてのインターフェースで待ち受けます。ローカルに限定するには、各 `-p` の前に `127.0.0.1:` を付けてください。

<!-- release -->
```sh
docker pull docker.io/olivaresai/olivares:0.5 &&
img=$(docker image inspect --format '{{index .RepoDigests 0}}' docker.io/olivaresai/olivares:0.5) &&
cosign verify "$img" \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null &&
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  "$img" \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```
<!-- /release -->

**Docker Compose。** 単一ノードで SQLite を使用し、Postgres とバックアップは任意で追加できます。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

セッションはホスト上の 1 つのフォルダーで作業します。`up` の前に `OLIVARES_PROJECT_DIR` にその絶対パスを設定すると、Compose がそれを `/project` にマウントします。AppArmor ポリシーがユーザー名前空間を制限するホスト（Ubuntu 24.04 以降）では、先にセッションプロファイルを読み込んでください。[Docker Compose](INSTALL.md#docker-compose) を参照してください。

**Debian と Ubuntu。** パッケージはログインできない `olivares` ユーザーと、セキュリティ設定を強化したサービスを追加します。サービスは自分で起動します。

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.deb" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.deb$' checksums.txt | sha256sum -c &&
sudo dpkg -i olivares_0.5_linux_amd64.deb && sudo systemctl enable --now olivares
```
<!-- /release -->

**RHEL、Fedora、SUSE。**

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.rpm" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.rpm$' checksums.txt | sha256sum -c &&
sudo rpm -i olivares_0.5_linux_amd64.rpm && sudo systemctl enable --now olivares
```
<!-- /release -->

**Alpine。**

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.apk" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.apk$' checksums.txt | sha256sum -c &&
sudo apk add --allow-untrusted olivares_0.5_linux_amd64.apk && sudo rc-service olivares start
```
<!-- /release -->

ARM サーバーでは `amd64` の代わりに `arm64` を使ってください。リリースの全ファイルは<!-- release -->[リリースページ](https://github.com/olivaresai/olivares/releases/tag/0.5)<!-- /release -->にあります。

**Homebrew。** macOS と Linux。

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**ソースから。** Go 1.26 以降、[Task](https://taskfile.dev)、pnpm。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**オフラインのネットワーク：** 署名付きイメージ、chart、検証用の資料をまとめ、[隔離環境にインストール](docs-site/src/content/docs/how-to/air-gap-install.md)してください。**Windows** 向けのネイティブビルドはまだありません。Docker イメージか WSL2 を使ってください。アップグレードとロールバックは[手順](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)を、各方法の詳細は [`INSTALL.md`](INSTALL.md) を参照してください。

**まずはデモデータで試せます。** 自分のマシンだけで実行してください（デモのパスワードは公開されています）：

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

その後、http://127.0.0.1:8901 を開いてください。

Helm、Kubernetes オペレーター、Terraform、アプライアンス、FIPS/STIG のデプロイ成果物は Business で提供されます。オフライン環境へのインストールには Enterprise が必要です。社内インフラへのエージェントと MCP サーバーのデプロイ（`olivares deploy`）は Business の機能です。[エディション](docs/editions.md) を参照してください。

## エディションと価格

Community は製品全体を無料のオープンソースとして提供します。Business は企業が本番運用に必要とする機能を追加します。Enterprise は、規模の大きいインフラや規制対象のインフラを持つ企業グループ向けです。

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **料金** | 無料、AGPL-3.0 | 月額 99 USD または年額 990 USD | 年間契約 |
| **提供内容** | 製品全体：ユーザー数無制限、すべての 4 つの deny-closed エンフォースメントポイント | Community のすべてに加え、Regulated Operations、AI Runtime Security、Compliance Packs、Identity & Scale、商用ライセンス、署名付きアップデート、メールサポート | Business のすべてに加え、企業・デプロイ・ID プロバイダー数の拡大、オフラインミラー、個別に合意したサポート条件 |
| **利用範囲** | 有効な ID プロバイダー一つ | 一つの企業、同時に有効なインスタンス一つ | 契約で合意 |

**Regulated Operations** は、法的保全、鍵の破棄を伴う消去、規制上の最低保存期間、アーカイブに対する法的保全の照合、Azure と GCS 上の WORM アーカイブを追加します。**AI Runtime Security** は、エージェントが送信、受信、実行するものをより深く検査します。**Compliance Packs** は、監査人向けに DORA の情報登録簿と ISO/IEC 42001 パックの草案を作成します。**Identity & Scale** は複数の ID プロバイダーを同時に接続し、より大きなデプロイに対応します。

[olivares.ai/pricing](https://olivares.ai/pricing) · [各エディションの内容](docs/editions.md) · [オープンソースと商用の範囲](LICENSING.md)

## アーキテクチャ

コンソールを内蔵した単一の Go バイナリです。REST API、gRPC API、`olivares` コマンドライン、Terraform プロバイダーを提供します。エッジコレクター（Business）はネットワーク内で動き、データは自社のサーバー上の SQLite または PostgreSQL に保存されます。[全体の構成](ARCHITECTURE.md)。

## ドキュメント

[docs.olivares.ai](https://docs.olivares.ai) には、インストールガイド、各コネクターのガイド、よく使うポリシーのレシピ、API リファレンスがあります。[Olivares AI とは](docs-site/src/content/docs/start/what-is-olivares-ai.md)から始めてください。現在動く機能と今後の予定は[正直さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)に記載しています。リリース：[GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

ウェブサイト：[製品](https://olivares.ai/product) · [ソリューション](https://olivares.ai/solutions) · [仕組み](https://olivares.ai/how-it-works) · [アーキテクチャ](https://olivares.ai/architecture) · [セキュリティ](https://olivares.ai/security) · [信頼](https://olivares.ai/trust) · [比較](https://olivares.ai/compare) · [デモ](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [ステータス](https://olivares.ai/status) · [ロードマップ](https://olivares.ai/roadmap) · [ブランド](https://olivares.ai/brand) · [プレス](https://olivares.ai/press)。

## セキュリティ

脆弱性を見つけた場合は、[`SECURITY.md`](SECURITY.md) の手順で非公開で報告してください。Olivares AI は、どのエージェントがどのリソースにアクセスしたかを記録し、内容は記録しません。その記録の閲覧自体もログに残します。ライセンスはオフラインで検証され、オープンソースのコアが私たちに通信することはありません。

## コミュニティ

貢献を歓迎します。[`CONTRIBUTING.md`](CONTRIBUTING.md) で、環境の準備、サインオフ、コネクターの構成を説明しています。[行動規範](CODE_OF_CONDUCT.md) · [サポート](SUPPORT.md) · [ガバナンス](GOVERNANCE.md) · [変更履歴](CHANGELOG.md)。

## プロジェクトを支援する

Olivares AI は公開で開発しています。役に立ったら、GitHub Sponsors の [olivaresai](https://github.com/sponsors/olivaresai) または [fran-olivares](https://github.com/sponsors/fran-olivares) で開発を支援するか、Ko-fi でコーヒーをおごってください。名前の掲載を希望するスポンサーは [`SUPPORTERS.md`](SUPPORTERS.md) に記載します。スポンサーはサポート契約ではありません（[`SUPPORT.md`](SUPPORT.md)）。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## ライセンス

エンジン、モジュール、コンソールは **AGPL-3.0-only**、SDK、コネクター、クライアントは **Apache-2.0** です。商用コードは別途ビルドされ、このリポジトリには含まれません。商用ライセンスの問い合わせ先：`enterprise@olivares.ai`。コントリビューションには DCO の sign-off（`git commit -s`）と [CLA](CLA.md) が必要です。

> **現状のまま**提供され、いかなる保証もなく、データ損失、業務中断、逸失利益について責任を負いません。[`DISCLAIMER.md`](DISCLAIMER.md) を参照してください。

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>エンタープライズ AI のグラウンドトゥルース。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
