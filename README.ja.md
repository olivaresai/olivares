<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — エンタープライズ AI のグラウンドトゥルース" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**すでに使っている AI を実行し、統治する — 自分のインフラ上で、ひとつのグラウンドトゥルースとともに。**

[Olivares AI とは](#olivares-ai-とは) · [できること](#できること) · [インストール](#インストール) · [クイックスタート](#クイックスタート) · [コンソール](#コンソールの内部) · [エディション](#エディションと価格) · [ドキュメント](#ドキュメント) · [セキュリティ](#セキュリティ) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.0](https://img.shields.io/badge/release-v26.9.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **ベータ版**、活発に開発中です。**v26.9.0** は、署名済みアーカイブ、ネイティブパッケージ、コンテナイメージを提供します。今日動作するもの、オンデマンドのもの、設計段階のものは、[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)に記載されています。

## Olivares AI とは

今日の AI estate は、コーディングエージェント、MCP サーバー、モデルエンドポイント、サービスアカウント、スケジュールジョブであり、ひとつのシステムになったことのないマシンに散在しています。何が動いているか、誰が起動したか、何に到達したか、いくらかかったか、誰が同意したかを、ひとつの場所から言える人はいません。

Olivares AI は、その estate をひとつにまとめる **コンソール込みのセルフホスト型 Go バイナリひとつ** です。AI が働くために必要なもの（コンテキスト、リソースへのアクセス、管理されたセッション）を与え、運用するための権限、ポリシー、予算、証拠をあなたに与えます。セルフホスト、必須テレメトリなし、エアギャップインストールに対応。

Claude Code は最も深いレベルで統合されています（`PreToolUse`/`PostToolUse` フック、管理設定、コンソールからの起動と停止）。公式の Codex および Grok CLI は第一級のセッションドライバーです。gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes、および Ollama のようなセルフホストエンドポイントはコネクタであり、それぞれが強制できることと観察しかできないことを明示します。AGPL ビルドが製品の全体であり、内部から機能制限されることはありません。どのプランもユーザー数を数えません。

## できること

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="読み書きアクセスマップのモーション図: 左にエージェント・セッション・アイデンティティ、右に到達するリソース、読み取りは青、書き込みはオレンジ、許可されたことのない観察された書き込みがドリフト所見として旗付けされる。">
<br><sub><b>アクセスマップ</b> — 各エージェントが estate 全体で何を読み書きするか。許可対観察。</sub>
</div>

- **見る。** 発見されたすべてのエージェント、セッション、モデル、MCP サーバー、ツール、アイデンティティのインベントリ。読み書き **アクセスマップ** と Permitted-vs-Observed の **ドリフト** ビュー。ライブセッション、オーケストレーショングラフ、ヘルス、SLA。見えないものは推測せず、`unknown` として示します。
- **作業を実行する。** 所有権、依存関係、受け入れ条件、意思決定を持つ永続的な作業項目。囲い込みリースにより、2 つのエージェントが同じ作業を同時に保持することはできません。Claude Code、Codex、Grok のセッションをコンソールから起動、接続、中断、停止。A2A を介して認可済みピアへ委譲します。
- **統治して強制する。** Cedar 認可エンジンと **4つの deny-closed エンフォースメントポイント** — Claude Code フック、インライン `/v1/messages` 推論プロキシ、MCP `tools/call` ゲート、A2A 委譲ゲート — により、未認可のアクションは実行前にブロックされるか、2 人承認のために保留されるか、書き換えられます。支出を拒否またはスロットルする予算、二重統制の break-glass、fail closed する estate **kill-switch**。
- **統治して供給する。** コンテンツソース（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL、ルートに閉じ込められたファイルシステム）をガバナンス下の検索へ。検索時にクリアランスが deny-closed で強制されます。
- **証明する。** ハッシュチェーン化され、Ed25519 で署名された監査台帳。**26 のフレームワークカタログ**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR…）にマッピングされた封印済みの証拠 — 自己評価によるコントロールファミリーであり、認証ではありません。SIEM/ITSM プッシュ（CEF/LEEF/syslog/OTLP/OCSF）。WebAuthn/FIDO2、PIV/CAC、SSO、SCIM、BYOK/CMEK、検証済みの消去権。いずれもデプロイごとに構成されます。

**30 のモジュール**、1 つのコンソール、**158 の統合** — コードから導出され、プッシュのたびに [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) で強制されるカウントです。内訳は [`connectors/README.md`](connectors/README.md)、各モジュールの成熟度は[モジュールカタログ](docs-site/src/content/docs/reference/modules/overview.md)にあります。

## インストール

方法をひとつ選んでください。1 つのコマンドがインストールし、その後 `olivares quickstart` がコンソール URL と一次性のセットアップトークンを表示します。すべてのリリースは cosign 署名済みで、SLSA 来歴と SBOM を伴います。以下の各経路はインストール前に検証し、`scripts/verify-release.sh` は手動ダウンロードを検査します（cosign + SHA-256、[方法](INSTALL.md#verifying-a-release)）。エンジンは **デフォルトで安全** です。loopback バインド、初回起動時の HTTPS、デフォルト資格情報なし、初回起動時に印字される一次性セットアップトークン。

**1 · コマンドひとつ、Linux と macOS** — 検証済みインストーラー。OS とアーキテクチャを検出し、署名済みチェックサムとアーカイブ SHA-256 を検証し、バイナリだけをインストールし、`sudo` は決して実行しません。

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

ユーザーサービス（systemd ユーザーユニットまたは LaunchAgent）には `--user` を付けます。システムサービスには、特権シェルから検証済みスクリプトを `--system --start` で実行します。手でダウンロード、検証、実行したい場合は、手動バイナリ経路と OS ごとの行列: [`INSTALL.md`](INSTALL.md)。

**2 · Docker** — マルチアーキ、distroless、非 root。ホストのすべてのインターフェースに公開されます（ローカルに限定するには `-p` のマッピングの前に `127.0.0.1:` を付けます）。

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` はダイジェストが同じイメージです。本番ではダイジェストでピン留めしてください。FIPS および STIG イメージバリアント: [`INSTALL.md`](INSTALL.md#docker)。

**3 · Docker Compose** — 硬化されたスタック。SQLite シングルノード、オプションの Postgres とバックアップ。

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — ツリー内の Helm チャート、または Helm なしのフラットマニフェスト。チャートの OCI 公開は未検証です（`publication-unverified`：このリポジトリはチャートの公開ワークフローを一度も実行しておらず、チャートのタグもリリースのチャート資産もなく、レジストリ側は観測できません）。

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux パッケージ** — [リリースページ](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)の `.deb`、`.rpm`、`.apk`。バイナリ、サンプル env ファイル、ログインなしの `olivares` ユーザー、硬化されたユニット。サービスは自動では起動されません。

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS と Linux。署名済みチェックサムに対して検査されます。

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · ソースから** — Go 1.26+、[Task](https://taskfile.dev)、pnpm。

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**: 署名済みイメージ、チャート、検証材料をまとめ、`scripts/verify-release.sh --key … --offline` でオフライン検証します（[手順](docs-site/src/content/docs/how-to/air-gap-install.md)）。**Windows** はまだビルドされていません。Linux コンテナまたは WSL2 を実行してください（[計画](INSTALL.md#windows)）。アップグレードとロールバック: [手順](docs-site/src/content/docs/how-to/upgrade-and-rollback.md)。

## クイックスタート

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

デモシードは学習専用です（ソースツリー上の公開パスワード）。実データに向けないでください。CI は `task smoke:quickstart` で同じ経路を歩き、アクセスマップとドリフトのカウントをアサートします（20 ノード / 13 エッジ、予期しないアクセス 8、未使用グラント 2）。[完全なクイックスタート](docs-site/src/content/docs/start/quickstart.md) は実際の pgAudit コネクタを配線します。

## コンソールの内部

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="アクセスマップ: 各エージェントが estate 全体で何を読み書きするか。左に起点、右にリソース。"></picture><br><sub><b>アクセスマップ</b> — 左に起点、右にリソース、読み取りと書き込みを色分け。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小権限ドリフト: アクセスマップ上に重ねた予期しないアクセスと未使用グラント。"></picture><br><sub><b>最小権限ドリフト</b> — 観察されたが許可されていないもの、誰も使わないグラント。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="コンソールから作成、接続、統治される Claude Code セッション。"></picture><br><sub><b>セッション</b> — SSH なしで、コンソールからセッションを作成、接続、統治。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="作業: 作業項目と意思決定の、セッションをまたぐ永続バックログ。"></picture><br><sub><b>作業</b> — セッションをまたぐ永続バックログ: 項目、所有権、受け入れ、意思決定。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="セキュリティとフォレンジック: ガードレール所見、異常キュー、改ざん検知可能なフォレンジック。"></picture><br><sub><b>セキュリティとフォレンジック</b> — ガードレール所見、異常、改ざん検知可能なフォレンジック。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: モデル支出、トークン使用量、予算、ランレート予測。"></picture><br><sub><b>FinOps</b> — モデルとエージェント別の支出、拒否またはスロットルする予算、ランレート。</sub> |

各静止画は、実行中のバイナリが提供するシード済みデモ estate のキャプチャです。画面の全体図: [コンソールリファレンス](docs-site/src/content/docs/reference/console.md)。

## エディションと価格

AGPL ビルドはプラットフォームの全体であり、内部から機能制限されることはありません。商用 add-on は上に載せる加算コードであり、取り除かれた機能ではありません。サブスクリプションは、署名済みモジュールパックをダウンロードするための資格情報です。セルフホストエンジンのユーザーアカウントは無制限で、**4つの deny-closed エンフォースメントポイント** はすべてオープンです。

| エディション | 対象 | 追加するもの |
|---|---|---|
| **Community** | 誰でも。無料、AGPL-3.0、ユーザー数無制限。 | 完全な製品、セルフホスト。コアにライセンスゲートはありません。 |
| **Business** | 採用する組織。課金はデプロイ単位であり、シート単位ではありません。 | サービスとオプションのパックであり、コア機能ではありません。商用ライセンス、維持される署名済みリリースチャネル、営業時間のメールサポート、および 4 つのオプション add-on: **Regulated Operations**、**Compliance Packs**、**AI Runtime Security**、**Identity & Scale**（公式ツール向けセッションコックピットを含む）。4 つすべてを合わせたものが **Business Max** です。 |
| **Cloud** | 同じプレーンを自分たちのために運用してほしいチーム。前払い、共有インフラ。 | 公開された上限付きのマネージドコントロールプレーン。Cloud のトライアルはありません。無料オプションはセルフホストの Community のままです。 |
| **Enterprise** | 規制下、複数エンティティ、大規模 estate。 | 契約。メールで合意し、年次注文書で署名します。 |

価格、add-on マトリクス、購入条件: [olivares.ai/pricing](https://olivares.ai/pricing)。オープン / 商用 / 計画中のマトリクス: [`LICENSING.md`](LICENSING.md)。

## アーキテクチャ

ひとつの静的 Go バイナリがコンソールを埋め込み、4 つのサーフェスを公開します。REST API（主）、安定コアの焦点を絞った gRPC ミラー、`olivares` CLI、Terraform プロバイダー。コレクターはあなたのインフラ内で動作します。ストアは SQLite または行レベルセキュリティ付き Postgres で、ストア API で一度、Postgres でもう一度強制されます。全体像（ワークプレーンを含む）: [`ARCHITECTURE.md`](ARCHITECTURE.md)。

## ドキュメント

[docs.olivares.ai](https://docs.olivares.ai) — テスト済みインストールチュートリアル（シングルノード、Docker Compose、Kubernetes/Helm、エアギャップ）、実際のコンソールキャプチャ付きコネクタガイド、クックブック（deny-closed ポリシー、予算、承認、kill-switch 訓練、SIEM プッシュ）、API リファレンス、用語集。[Olivares AI とは](docs-site/src/content/docs/start/what-is-olivares-ai.md)から始めてください。サイト上: [製品](https://olivares.ai/product) · [ソリューション](https://olivares.ai/solutions) · [仕組み](https://olivares.ai/how-it-works) · [アーキテクチャ](https://olivares.ai/architecture) · [セキュリティ](https://olivares.ai/security) · [信頼](https://olivares.ai/trust) · [比較](https://olivares.ai/compare) · [デモ](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [ステータス](https://olivares.ai/status) · [ロードマップ](https://olivares.ai/roadmap) · [ブランド](https://olivares.ai/brand) · [プレス](https://olivares.ai/press)。リリース: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md)。

## セキュリティ

脆弱性は [`SECURITY.md`](SECURITY.md) を通じて非公開で報告してください。公開 issue にはしないでください。エンジンは読み取り優先で最小データです。アクセスマップはペイロードではなくエッジを保存し、開くことは記録される操作です。ライセンスの検証が私たちを呼び出すことはありません。AGPL コアはライセンス呼び出しを行いません。アドバイザリの流れ: [`docs/security-advisories.md`](docs/security-advisories.md)。サプライチェーンの証拠: [`docs/openssf-badge.md`](docs/openssf-badge.md)。

## コミュニティ

[`CONTRIBUTING.md`](CONTRIBUTING.md)（セットアップ、DCO/CLA、SPDX、コネクタ境界） · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog、CalVer `vYY.M.PATCH`）。

## ライセンス

`core/`、`modules/`、`web/` は **AGPL-3.0-only**、`sdk/`、`connectors/`、`clients/` は **Apache-2.0** であり、コネクタがエンジンを import することはありません。商用 add-on は別個、任意、クローズドです。`-tags enterprise` でのみビルドされ、このリポジトリには決して入りません。商用ライセンス: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md)。貢献には DCO サインオフ（`git commit -s`）と [CLA](CLA.md) が必要です。

> **無保証、無責任。** ソフトウェアは **現状のまま** 提供され、**いかなる種類の保証もなく**、**データ損失、事業中断、逸失利益に対する責任もありません**。コントロールプレーンでは形式ではありません。設定ミスは正当な作業を止め、止めようとしたものちょうどを通すことがあります。AGPL-3.0-only §§15–16、Apache-2.0 §§7–8、および本プロジェクトの補足条項が適用されます — [`DISCLAIMER.md`](DISCLAIMER.md)。

## プロジェクトを支援する

コアは無料であり、無料のままです。すべてのリリースを署名、検証、最新に保つことは継続的な作業です。GitHub Sponsors で支援してください — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) または [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — または Ko-fi で単発。スポンサーシップはサポート契約ではありません（[`SUPPORT.md`](SUPPORT.md)）。名前の掲載を希望するスポンサーは [`SUPPORTERS.md`](SUPPORTERS.md) に記載されます。

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>エンタープライズ AI のグラウンドトゥルース。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
