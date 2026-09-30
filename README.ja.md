<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — エンタープライズ AI のグラウンドトゥルース" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**すでに使っている AI を、自分のインフラ上で実行し、統治する。**

[Olivares AI とは](#olivares-ai-とは) · [できること](#できること) · [インストール](#インストール) · [クイックスタート](#クイックスタート) · [コンソール](#コンソールの内部) · [エディション](#エディションと価格) · [ドキュメント](#ドキュメント) · [セキュリティ](#セキュリティ) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **ベータ版。** 26.10.0 は署名付きアーカイブ、ネイティブパッケージ、コンテナイメージで提供されます。[正直さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)に、現在動作するもの、オンデマンドで動作するもの、まだ設計段階のものを記載しています。

## Olivares AI とは

Olivares AI は AI エージェント向けのセルフホスト型コントロールプレーンです。コンソールを内蔵した単一の Go バイナリで動作します。エージェントにはコンテキスト、リソースへのアクセス、管理されたセッションを与え、あなたには権限、ポリシー、予算、証跡を与えます。必須のテレメトリはなく、エアギャップ環境へのインストールにも対応します。

Claude Code は `PreToolUse`/`PostToolUse` フック、管理設定、コンソールからの起動と停止で接続します。公式の Codex CLI と Grok CLI は管理セッションとして動作します。gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes、Ollama などのセルフホスト型エンドポイントはコネクタであり、それぞれが強制できることと観測のみのことを明示します。

## できること

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="読み書きアクセスマップのモーション図: 左にエージェント・セッション・アイデンティティ、右に到達するリソース、読み取りは青、書き込みはオレンジ、許可されたことのない観察された書き込みがドリフト所見として旗付けされる。">
<br><sub><b>アクセスマップ</b> — 各エージェントが estate 全体で何を読み書きするか。許可対観察。</sub>
</div>

- **見る。** コネクタが観測したエージェント、セッション、モデル、MCP サーバー、ツール、アイデンティティのインベントリ。許可と観測の**ドリフト**を示す読み書きの**アクセスマップ**。ライブセッション、オーケストレーショングラフ、ヘルス、SLA。分類できないアクセスは `unknown` と表示します。
- **作業を動かす。** 担当者、依存関係、受け入れ基準、決定を持つ作業項目。2 つのエージェントが同じ項目を同時に持てないようにするフェンス付きリース。コンソールから起動、アタッチ、中断、停止する Claude Code、Codex、Grok のセッション。A2A による許可済みピアへの委任。
- **統治し、強制する。** Cedar 認可エンジンと **4 つの deny-closed エンフォースメントポイント**：Claude Code フック、インラインの `/v1/messages` 推論プロキシ、MCP の `tools/call` ゲート、A2A 委任ゲート。許可されていない操作は、実行前にブロックされるか、2 人の承認待ちで保留されるか、書き換えられます。予算は支出を拒否または制限し、ブレークグラスには 2 人が必要で、**キルスイッチ**はフェイルクローズします。
- **統治されたデータを与える。** SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL、ルートに閉じたファイルシステムが統治された検索にデータを供給し、アクセス資格は検索時に確認されます。
- **証明する。** ハッシュチェーンで連結され Ed25519 で署名された監査台帳。**26 のフレームワークカタログ**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR など）に対応付けた封印済みの証跡（自己評価の管理策ファミリーであり、認証ではありません）。SIEM と ITSM への送信（CEF、LEEF、syslog、OTLP、OCSF）。WebAuthn/FIDO2、PIV/CAC、SSO、SCIM、BYOK/CMEK、検証済みの消去権。いずれもデプロイごとに設定します。

**31 モジュール**、1 つのコンソール、**159 のインテグレーション**。数は [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) がコードから数えています。内訳は [`connectors/README.md`](connectors/README.md)、各モジュールの成熟度は[モジュールカタログ](docs-site/src/content/docs/reference/modules/overview.md)にあります。

## インストール

方法を 1 つ選んでください。その後、`olivares quickstart` がコンソールの URL と 1 回限りのセットアップトークンを表示します。リリースは cosign で署名され、SLSA 来歴と SBOM が付きます。以下のどの方法もインストール前に検証し、手動ダウンロードは `scripts/verify-release.sh` で確認できます（cosign と SHA-256、[手順](INSTALL.md#verifying-a-release)）。エンジンは HTTPS で起動し、既定の認証情報はなく、1 回限りのセットアップトークンを使います。

**1 · 1 つのコマンド（Linux と macOS）。** インストーラーは OS とアーキテクチャを検出し、署名付きチェックサムとアーカイブの SHA-256 を検証し、バイナリのみをインストールし、`sudo` は実行しません。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

ユーザーサービス（systemd ユーザーユニットまたは LaunchAgent）には `--user` を付けます。システムサービスには、特権シェルから `--system --start` を付けてスクリプトを実行します。手動の方法と OS ごとのマトリクス：[`INSTALL.md`](INSTALL.md)。

**2 · Docker。** マルチアーキテクチャ、distroless、非 root。ポートはホストのすべてのインターフェースで公開されます。ローカルに限定するには `-p` の指定の前に `127.0.0.1:` を付けます。

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` は同じイメージです。本番ではダイジェストで固定してください。FIPS と STIG のバリアント：[`INSTALL.md`](INSTALL.md#docker)。

**3 · Docker Compose。** 強化されたスタック：1 ノードの SQLite に、オプションで Postgres とバックアップ。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes。** このリポジトリの Helm チャート、または Helm を使わないフラットなマニフェスト。チャートはまだ OCI で公開されていません（`publication-unverified`）。

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux パッケージ。** [リリースページ](https://github.com/olivaresai/olivares/releases/tag/26.10.0)の `.deb`、`.rpm`、`.apk`：バイナリ、env ファイルの例、ログイン不可の `olivares` ユーザー、強化されたユニット。インストール時にサービスは起動しません。

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew。** macOS と Linux。署名付きチェックサムで確認します。

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · ソースから。** Go 1.26 以降、[Task](https://taskfile.dev)、pnpm。

```sh
task build && ./bin/olivares quickstart
```

**エアギャップ：** 署名済みのイメージ、チャート、検証資材をまとめ、`scripts/verify-release.sh --key … --offline` でオフライン検証します（[手順](docs-site/src/content/docs/how-to/air-gap-install.md)）。**Windows** にはまだネイティブビルドがありません。Linux コンテナか WSL2 を使ってください（[計画](INSTALL.md#windows)）。アップグレードとロールバック：[手順](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)。

## クイックスタート

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

デモのパスワードは公開されています。実データでデモを使わないでください。[完全なクイックスタート](docs-site/src/content/docs/start/quickstart.md)では実際の pgAudit ソースを接続します。

## コンソールの内部

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="アクセスマップ: 各エージェントが estate 全体で何を読み書きするか。左に起点、右にリソース。"></picture><br><sub><b>アクセスマップ</b> — 左に起点、右にリソース、読み取りと書き込みを色分け。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小権限ドリフト: アクセスマップ上に重ねた予期しないアクセスと未使用グラント。"></picture><br><sub><b>最小権限ドリフト</b> — 観察されたが許可されていないもの、誰も使わないグラント。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="コンソールから作成、接続、統治される Claude Code セッション。"></picture><br><sub><b>セッション</b> — SSH なしで、コンソールからセッションを作成、接続、統治。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="作業: 作業項目と意思決定の、セッションをまたぐ永続バックログ。"></picture><br><sub><b>作業</b> — セッションをまたぐ永続バックログ: 項目、所有権、受け入れ、意思決定。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="セキュリティとフォレンジック: ガードレール所見、異常キュー、改ざん検知可能なフォレンジック。"></picture><br><sub><b>セキュリティとフォレンジック</b> — ガードレール所見、異常、改ざん検知可能なフォレンジック。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: モデル支出、トークン使用量、予算、ランレート予測。"></picture><br><sub><b>FinOps</b> — モデルとエージェント別の支出、拒否またはスロットルする予算、ランレート。</sub> |

画面は、実行中のバイナリ上のデモから取得しています。すべての画面：[コンソールリファレンス](docs-site/src/content/docs/reference/console.md)。

## エディションと価格

Community は AGPL-3.0 の完全な製品で、ユーザー数は無制限、4 つの deny-closed エンフォースメントポイントをすべて含みます。Business と Enterprise は `-tags enterprise` でのみビルドされる商用コードを追加します。Community から削除または制限されるものはありません。

| エディション | 価格 | 含まれるもの |
|---|---|---|
| **Community** | 無料、AGPL-3.0 | セルフホストの完全な製品。ユーザー数無制限、アクティブなアイデンティティプロバイダー 1 つ。 |
| **Business** | 月額 129 USD または年額 1,290 USD | 商用ライセンス、署名付きリリースチャネル、営業時間内のメールサポート、そして **Regulated Operations**、**AI Runtime Security**、**Compliance Packs**、**Identity & Scale**。ユーザー数無制限、法人 1 社、本番デプロイ最大 2 つ（それぞれにステージング 1 つ）、アクティブなアイデンティティプロバイダー最大 5 つ。 |
| **Enterprise** | 契約 | 追加の法人、デプロイ、アイデンティティプロバイダー、エアギャップミラー、カスタム LTS とサポート条件。年間注文書による契約。 |

購入条件：[olivares.ai/pricing](https://olivares.ai/pricing)。オープンなものと商用のもの：[`LICENSING.md`](LICENSING.md)。

## アーキテクチャ

単一の静的 Go バイナリがコンソールを内蔵し、4 つのインターフェースを提供します：REST API、安定コアの gRPC ミラー、`olivares` CLI、Terraform プロバイダー。コレクターはあなたのインフラ内で動作します。ストアは行レベルセキュリティ付きの SQLite または Postgres で、ストア API と Postgres の両方で強制されます。作業プレーンを含む詳細：[`ARCHITECTURE.md`](ARCHITECTURE.md)。

## ドキュメント

[docs.olivares.ai](https://docs.olivares.ai) — テスト済みインストールチュートリアル（シングルノード、Docker Compose、Kubernetes/Helm、エアギャップ）、実際のコンソールキャプチャ付きコネクタガイド、クックブック（deny-closed ポリシー、予算、承認、kill-switch 訓練、SIEM プッシュ）、API リファレンス、用語集。[Olivares AI とは](docs-site/src/content/docs/start/what-is-olivares-ai.md)から始めてください。サイト上: [製品](https://olivares.ai/product) · [ソリューション](https://olivares.ai/solutions) · [仕組み](https://olivares.ai/how-it-works) · [アーキテクチャ](https://olivares.ai/architecture) · [セキュリティ](https://olivares.ai/security) · [信頼](https://olivares.ai/trust) · [比較](https://olivares.ai/compare) · [デモ](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [ステータス](https://olivares.ai/status) · [ロードマップ](https://olivares.ai/roadmap) · [ブランド](https://olivares.ai/brand) · [プレス](https://olivares.ai/press)。リリース: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

## セキュリティ

脆弱性は公開 issue ではなく、[`SECURITY.md`](SECURITY.md) から非公開で報告してください。アクセスマップはペイロードではなくエッジを保存し、開くと監査されます。ライセンス検証はオフラインで行われ、AGPL コアはライセンスの呼び出しを行いません。アドバイザリ：[`docs/security-advisories.md`](docs/security-advisories.md)、サプライチェーンの証跡：[`docs/openssf-badge.md`](docs/openssf-badge.md)。

## コミュニティ

[`CONTRIBUTING.md`](CONTRIBUTING.md)（セットアップ、DCO/CLA、SPDX、コネクタ境界） · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog、CalVer `YY.M.PATCH`）。

## ライセンス

`core/`、`modules/`、`web/` は **AGPL-3.0-only**、`sdk/`、`connectors/`、`clients/` は **Apache-2.0** で、コネクタがエンジンを import することはありません。商用コードは `-tags enterprise` でのみビルドされ、このリポジトリにはありません。商用ライセンス：`enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md)。コントリビューションには DCO の sign-off（`git commit -s`）と [CLA](CLA.md) が必要です。

> **無保証。** 本ソフトウェアは**現状のまま**提供され、**いかなる保証もなく**、**データ損失、業務中断、逸失利益について責任を負いません**。AGPL-3.0-only 第 15–16 条、Apache-2.0 第 7–8 条、および本プロジェクトの補足条項が適用されます — [`DISCLAIMER.md`](DISCLAIMER.md)。

## プロジェクトを支援する

GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) または [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — または Ko-fi での単発の支援で、プロジェクトを支援できます。スポンサーはサポート契約ではありません（[`SUPPORT.md`](SUPPORT.md)）。名前の掲載を希望したスポンサーは [`SUPPORTERS.md`](SUPPORTERS.md) に掲載します。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>エンタープライズ AI のグラウンドトゥルース。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
