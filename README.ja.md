<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**チームがすでに使っている AI を、他のインフラと同じように管理しながら実行できます。**

[できること](#できること) · [インストール](#インストール) · [コンソール](#コンソールの内部) · [エディション](#エディションと価格) · [ドキュメント](#ドキュメント) · [コミュニティ](#コミュニティ) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

次のリリースは <!-- release -->`0.1`<!-- /release --> で、GitHub ではまだ公開されていません。以下のコマンドは予定されている成果物を示します。公開まではソースからビルドし、公開後も使用前に各成果物を検証してください。観測した公開状況は <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release --> に記録されています。 Kubernetes OCI: `publication-unverified`.

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
- **求められたときに証拠を示す。** すべての決定は、後からの改ざんを検出できる署名付きログに記録されます。Business Compliance Packs は証拠を 26 フレームワークカタログに対応付け、セキュリティチームや監査担当者向けのレポートを生成します。Community は保存済みの証拠と JSON/CSV エクスポートを引き続き提供します。

Claude Code、Codex、Grok、Cursor、gemini-cli、opencode、OpenHands、Ollama 経由のローカルモデルなど、既存のツールと連携します。**32 のモジュール**と **136 件の統合**があります。[全モジュール](docs-site/src/content/docs/reference/modules/overview.md) · [全コネクター](connectors/README.md)。

Community はローカルの可観測性、保存済み設定、バックアップのエクスポートを保持します。SIEM/ITSM 配信、外部テレメトリー配信、ポスチャーのエクスポートは Business の基本版に含まれます。

## インストール

**Docker Compose.** The container qualification job exercises this installation path.
Set the release image explicitly so a cached `:latest` image cannot select an
older release.

<!-- release -->
```sh
set -e
cd /path/to/your/project   # the host folder your sessions will work on
export OLIVARES_PROJECT_DIR="$PWD"
git clone --depth 1 https://github.com/olivaresai/olivares.git "$HOME/olivares"
export OLIVARES_IMAGE=docker.io/olivaresai/olivares:0.1
# On Linux hosts whose AppArmor policy mediates user namespace creation:
if [ -r /sys/kernel/security/apparmor/features/namespaces/mask ] &&
   grep -qw userns_create /sys/kernel/security/apparmor/features/namespaces/mask; then
  sudo install -m 0644 "$HOME/olivares/deploy/apparmor/olivares-sessions.conf" /etc/apparmor.d/olivares-sessions
  sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions
  export OLIVARES_APPARMOR_PROFILE=olivares-sessions
fi
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" up --wait --wait-timeout 120
docker compose -f "$HOME/olivares/deploy/compose/docker-compose.yml" exec olivares \
  olivares first-boot --data-dir /var/lib/olivares --new-token
```
<!-- /release -->

Open the console address printed by `first-boot --new-token` and use the replacement
one-time setup token it prints to create the first administrator. This invalidates
the previous setup token and is available only before the first administrator exists.
The stack uses SQLite and a persistent data volume.
The AppArmor step needs an AppArmor 4 parser and runs on the Docker daemon host.
See [session confinement on AppArmor hosts](deploy/compose/README.md#session-confinement-on-apparmor-hosts).
It publishes ports on every host interface by default; set `OLIVARES_BIND=127.0.0.1`
to restrict access to this host.

Sessions in the container work on one host folder, mounted at `/project`: the absolute
path in `OLIVARES_PROJECT_DIR`, set before `up`. Without it, `/project` is an empty Docker
volume, never the directory you run Compose from. A session there can change everything in
that folder: never set the variable to your home directory. In the console, choose **Change folder**
on the New session form and enter `/project`. On a Linux host the container user
(UID 65532) needs write access; see
[work on a host project folder](deploy/compose/README.md#work-on-a-host-project-folder).

Gate coverage is not a passing release result: see the `qualify-compose-ready` job in
[container qualification](.github/workflows/compose-ready.yml). The release must also
pass its first-hour journey before it is qualified.

Other installation methods are **not qualified** by the first-hour gate. Their commands
and limits are in [INSTALL.md](INSTALL.md#installation-qualification), including the
shell installer, standalone Docker, Kubernetes, native packages, Homebrew, source builds
and offline installs. [Verify release artifacts](INSTALL.md#verifying-a-release) before
running them; see [upgrading and uninstalling](INSTALL.md#upgrading--uninstalling) for an
existing installation.

- Helm チャート、Kubernetes オペレーター、Terraform プロバイダーは Business で提供します。 [Editions](https://olivares.ai/pricing).
Install the chart from source; see [Kubernetes installation](INSTALL.md#kubernetes).

## コンソールの内部

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>アクセスマップ</b> — 誰が何を読み書きするか。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — 誰も許可していないアクセスと、誰も使っていない権限。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>セッション</b> — ブラウザーからエージェントのセッションを開始、参加、停止。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>作業</b> — セッション終了後も残るタスク、担当者、決定。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>セキュリティ</b> — ブロックされた操作、異常、改ざんを検出できる記録。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>支出</b> — モデル・エージェント別の費用、予算、予測。</sub> |

全画面の説明は[コンソールリファレンス](docs-site/src/content/docs/reference/console.md)を参照してください。

## エディションと価格

Community は製品全体を無料のオープンソースとして提供します。Business は企業が本番運用に必要とする機能を追加します。Enterprise は、規模の大きいインフラや規制対象のインフラを持つ企業グループ向けです。

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **料金** | 無料、AGPL-3.0 | 月額 129 USD または年額 1,290 USD | 年間契約 |
| **提供内容** | 製品全体：ユーザー数無制限、すべての 4 つの deny-closed エンフォースメントポイント | Community のすべてに加え、Regulated Operations、AI Runtime Security、Compliance Packs、Identity & Scale、商用ライセンス、署名付きアップデート、メールサポート | Business のすべてに加え、企業・デプロイ・ID プロバイダー数の拡大、オフラインミラー、個別に合意したサポート条件 |
| **利用範囲** | 有効な ID プロバイダー一つ | 一つの企業、同時に有効なインスタンス一つ | 契約で合意 |

**Regulated Operations** は、規制上の最低保存期間、アーカイブに対する法的保全の照合、Azure と GCS 上の WORM アーカイブを追加します。**AI Runtime Security** は、エージェントが送信、受信、実行するものをより深く検査します。**Compliance Packs** は、監査人向けに DORA の情報登録簿と ISO/IEC 42001 パックの草案を作成します。**Identity & Scale** は複数の ID プロバイダーを同時に接続し、より大きなデプロイに対応します。

[olivares.ai/pricing](https://olivares.ai/pricing) · [各エディションの内容](docs/editions.md) · [オープンソースと商用の範囲](LICENSING.md)

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
