<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**チームがすでに使っている AI を、他のインフラと同じように管理しながら実行できます。**

[できること](#できること) · [インストール](#インストール) · [コンソール](#コンソールの内部) · [エディション](#エディションと価格) · [ドキュメント](#ドキュメント) · [コミュニティ](#コミュニティ) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10](https://img.shields.io/badge/release-26.10-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

開発者は Claude Code や Codex で作業しています。エージェントは MCP サーバー、モデル、社内 API を呼び出し、スケジュールされたジョブは自動で動きます。それぞれが別々のログと権限を持つため、単純な疑問にもすぐには答えられません。このファイルを変更したエージェントはどれか、誰が承認したのか、今月の AI 費用はいくらだったのか。

Olivares AI は、その答えを一か所にまとめます。すでに使っているエージェントやツールに接続し、それぞれの動きを表示し、実行前にルールを適用して、すべての署名付き記録を残します。自社のサーバーで動く単一のプログラムで、製品全体を無料のオープンソースとして利用できます。

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>アクセスマップ</b> — 各エージェントが何を読み書きするか、そして誰も許可していない書き込み。</sub>
</div>

## できること

- **何が動いているかを把握する。** すべてのエージェント、セッション、モデル、MCP サーバー、ツールを一つのインベントリにまとめます。アクセスマップはそれぞれの読み書きを表示し、どのルールも許可していないアクセスを示します。
- **被害が出る前に操作を止める。** Olivares AI の **4 つの deny-closed エンフォースメントポイント**が、実行前に各操作を確認します。確認する場所は Claude Code 内、モデルプロキシ、各 MCP ツール呼び出し、エージェント間です。リスクのある操作は二人目の確認を待ち、禁止された操作は実行されません。一つのスイッチですべてのエージェントを同時に停止できます。確認で判断できない場合も、操作は実行されません。
- **AI の支出を管理する。** チーム、エージェント、モデルごとの予算で、請求書が届く前に警告し、支出のペースを落とすか、支出を止めます。
- **社内の知識を安全にエージェントへ渡す。** SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、PostgreSQL に接続します。各エージェントが閲覧できるのは、それを使う人に閲覧権限がある情報だけです。
- **セッションをまたいで作業を続ける。** セッションが終わっても、タスク、担当者、決定は残ります。SSH を使わずに、ブラウザーから Claude Code、Codex、Grok のセッションを開始し、参加し、停止できます。
- **求められたときに証拠を示す。** すべての決定は、後から変更できない署名付きログに記録されます。セキュリティチームや監査担当者はその記録からレポートを取得でき、証拠は 26 フレームワークカタログに対応付けられています。

Claude Code、Codex、Grok、Cursor、gemini-cli、opencode、OpenHands、Ollama 経由のローカルモデルなど、既存のツールと連携します。**31 のモジュール**と **159 件の統合**を、すべて無料エディションで利用できます。[全モジュール](docs-site/src/content/docs/reference/modules/overview.md) · [全コネクター](connectors/README.md)。

## インストール

方法を一つ選び、そのコードブロックをコピーしてください。最後に `olivares quickstart` がコンソールのアドレスと、最初の管理者を作成するための一回限りのトークンを表示します。すべてのリリースに署名が付いており、どの方法でもダウンロードしたものを検証してからインストールします（[ダウンロードを自分で検証する](INSTALL.md#verifying-a-release)）。

**Linux と macOS、コマンド一つで。** システムを検出し、リリースを検証して、バイナリだけをインストールします。`sudo` は使いません。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker。** マルチアーキテクチャ、distroless、非 root。 ホストのすべてのインターフェースで待ち受けます。ローカルに限定するには、各 `-p` の前に `127.0.0.1:` を付けてください。

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

**Docker Compose。** 単一ノードで SQLite を使用し、Postgres とバックアップは任意で追加できます。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**Kubernetes。** このリポジトリの Helm chart を使用します（chart はまだ OCI リリースとして公開されていません：`publication-unverified`）。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
```

Helm を使わない場合：

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**Debian と Ubuntu。** パッケージはログインできない `olivares` ユーザーと、セキュリティ設定を強化したサービスを追加します。サービスは自分で起動します。

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.deb
sudo dpkg -i olivares_26.10.0_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL、Fedora、SUSE。**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.rpm
sudo rpm -i olivares_26.10.0_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine。**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.apk
sudo apk add --allow-untrusted olivares_26.10.0_linux_amd64.apk && sudo rc-service olivares start
```

ARM サーバーでは `amd64` の代わりに `arm64` を使ってください。リリースの全ファイルは[リリースページ](https://github.com/olivaresai/olivares/releases/tag/26.10.0)にあります。

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

## コンソールの内部

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>アクセスマップ</b> — 誰が何を読み書きするか。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — 誰も許可していないアクセスと、誰も使っていない権限。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>セッション</b> — ブラウザーからエージェントのセッションを開始、参加、停止。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>作業</b> — セッション終了後も残るタスク、担当者、決定。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>セキュリティ</b> — ブロックされた操作、異常、改変できない記録。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>支出</b> — モデル・エージェント別の費用、予算、予測。</sub> |

全画面の説明は[コンソールリファレンス](docs-site/src/content/docs/reference/console.md)を参照してください。

## エディションと価格

Community は製品全体を無料のオープンソースとして提供します。Business は企業が本番運用に必要とする機能を追加します。Enterprise は、規模の大きいインフラや規制対象のインフラを持つ企業グループ向けです。

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **料金** | 無料、AGPL-3.0 | 月額 129 USD または年額 1,290 USD | 年間契約 |
| **提供内容** | 製品全体：ユーザー数無制限、すべての 4 つの deny-closed エンフォースメントポイント | Community のすべてに加え、Regulated Operations、AI Runtime Security、Compliance Packs、Identity & Scale、商用ライセンス、署名付きアップデート、メールサポート | Business のすべてに加え、企業・デプロイ・ID プロバイダー数の拡大、オフラインミラー、個別に合意したサポート条件 |
| **利用範囲** | 有効な ID プロバイダー一つ | 一つの企業、本番デプロイ二つ（それぞれにステージング一つ）、ID プロバイダー五つ | 契約で合意 |

**Regulated Operations** は、法的保全と変更できないアーカイブにより、法律が求める期間だけ記録を保持します。**AI Runtime Security** はエージェントが送信、受信、実行するものをフィルタリングします。**Compliance Packs** は ISO 42001、DORA、NIS 2 向けの証拠を用意します。**Identity & Scale** は複数の ID プロバイダーを同時に接続し、より大きなデプロイに対応します。

[olivares.ai/pricing](https://olivares.ai/pricing) · [オープンソースと商用の範囲](LICENSING.md)

## アーキテクチャ

コンソールを内蔵した単一の Go バイナリです。REST API、gRPC API、`olivares` コマンドライン、Terraform プロバイダーを提供します。コレクターはネットワーク内で動き、データは自社のサーバー上の SQLite または PostgreSQL に保存されます。[全体の構成](ARCHITECTURE.md)。

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
