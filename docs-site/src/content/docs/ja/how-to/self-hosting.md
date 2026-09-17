---
title: Olivares AI をセルフホストする
description: >-
  単一バイナリ、Docker Compose、または Kubernetes で Olivares AI を自分で運用する。
  デフォルト認証情報なし、ワンタイムのセットアップトークン、TLS をデフォルトで有効化、
  必須のテレメトリなし、デフォルトではコントロールプレーンからのエグレスなし、という
  セキュアな設定を備える。境界を越えるのは、設定したモデル API への呼び出しや、接続した
  SIEM／Webhook 出力など、あなたがそのように設定したものだけである。
---

Olivares AI は **セルフホスト・ファースト** です。製品全体が Web UI を埋め込んだ
1 つの静的バイナリであるため、最も単純なデプロイは単一ファイルで済みます。マルチノードや
本番向けには Compose や Kubernetes の経路も用意されています。どの経路も同じセキュアな
デフォルト設定 — デフォルト認証情報なし、ワンタイムのセットアップトークン、TLS をデフォルトで
有効化 — を共有します。必須のテレメトリはなく、デフォルトではコントロールプレーンからのエグレス
もありません。あなたの境界を越えるのは、**あなた**がそのように設定したものだけです。具体的には、
あなたのモデル API への呼び出し、接続した SIEM／Webhook 出力、用意した場合の外部埋め込み
プロバイダーです。

このガイドはデプロイの **意思決定ページ** です — 選択肢とそのセキュアなデフォルト設定を
一目で把握できます。各シナリオの手順を追ったインストールについては、getting-started
チュートリアルがすべての経路をエンドツーエンドで解説しています:
[単一ノード (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[air-gapped (エアギャップ)](/tutorials/getting-started/air-gapped/)。まず成果物を暗号的に
検証するには [ダウンロードしたものを検証する](/how-to/verify-a-release/) を、
ネットワーク非接続のサイトについては
[エアギャップ環境でインストールする](/how-to/air-gap-install/) を参照してください。

## セキュアなデフォルト設定 (すべての経路)

| デフォルト | 動作 |
|---|---|
| **認証情報** | なし。初回起動時に **ワンタイムかつ単一利用のセットアップトークン** (`olst_…`) を表示する。これを使って最初の管理者を作成する。 |
| **TLS** | デフォルトで有効。`--insecure` (平文) は localhost での開発専用。 |
| **バインド** | デフォルトで**すべてのインターフェース**（`:8443`、`:8444`）。これはサーバーです。このホストだけに制限するには `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444` を渡します。 |
| **ライセンス** | オープン（AGPL）バイナリでは、ライセンスは **オフライン** で検証され（Ed25519）、証明（attestation）にのみ用いられる。オープン製品をゲートしたり劣化させたりすることは決してなく、この点は変わらない。商用アドオンは、**サブスクリプションによるエンタープライズリポジトリへのアクセス**として提供される、支払われた期間に対する権利である（SUSE/Novell モデル）。アドオンを取得し、その更新（セキュリティ更新を含む）を受け取るには、この権利が必要となる。エアギャップ環境への提供も SUSE と同じ方式で行われ、この権利が引き続き適用されるローカルミラーを経由する。 |
| **テレメトリ送信** | オフ。エンジンは起動時に必須の外向き通信を一切行わない。 |

## 選択肢 1 — 単一バイナリ

1 つの静的成果物 (純 Go の SQLite ストア、つまり C ツールチェーン不要) をビルドして実行します:

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

初回起動時、エンジンはセットアップバナーを表示します:

```text
=== FIRST-BOOT SETUP ===
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

最初の管理者を作成し、ログインします:

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

データディレクトリには SQLite データベース、監査署名鍵、TLS マテリアルが格納されます。
バックアップを取り、保護してください。

### カスタムデータディレクトリ (`layout: custom`)

既定のネイティブレイアウトは `/var/lib/olivares` です。署名済みサービスアダプタ
（`install.sh --data-dir`、`scripts/install-service.sh`）は **形状** により
**カスタム** データディレクトリを認めます。許可リストではありません。所有
マニフェストは `"layout": "custom"` を記録します（`CHANGELOG.md` `[26.9.0]`
Added、`INSTALL.md`）。

SDD 04 §6: 設定可能な各フィールドは owner、schema、accepted sources、validator を
宣言します。ここでの専用ディレクトリの owner はアダプタ、親の owner はオペレータです。
次はアダプタ自身の拒否文字列です（`scripts/install-service.sh`）:

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state…; choose a real directory` |
| Path under `/tmp` or `/var/tmp` on systemd older than 235 | `data directory $data_dir is under /tmp or /var/tmp and this host runs systemd $running: creating a BindPaths= destination inside the private /tmp needs systemd 235 or later…` |
| A BindPaths= path contains `:` | `$2 $1 contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it` |

`install-agentops.sh` は `OLIVARES_DATA_DIR` に同じ 2 階層規則を使います:
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`。`OLIVARES_DATA_DIR` と明示選択された
`OLIVARES_WORKSPACE_DIR` を尊重します。

`/home`、`/root`、`/run/user` 配下のパスは `ProtectHome=tmpfs` と、そのディレクトリ
だけの `BindPaths=` で描画されます。`/tmp` または `/var/tmp` 配下は
`PrivateTmp=true` を保ち、そのディレクトリだけの `BindPaths=` を受けます
（`scripts/install-service.sh` の `sandbox_access`）。

`olivares uninstall` は、索引付きパスのユニットがそのディレクトリでエンジンを実行する
とき、または preserve が既にサービス設定の横にアンインストール証人を残しているときに
限り、そのカスタムディレクトリを認めます。記録された AgentOps レイアウトは
`olivares doctor` で診断します —
[トラブルシューティング](/how-to/troubleshooting/#agentops-layout-check)。

パッケージインストールの既定は `/var/lib/olivares` のままです。
[パッケージからインストール](/how-to/install-from-packages/)。macOS は
[Homebrew でインストール](/how-to/install-from-homebrew/)。

## 選択肢 2 — Docker Compose (単一ノード、SQLite)

リポジトリには Compose スタックが同梱されています:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

マルチテナントの Postgres バックエンドを使う場合は、パスワードを設定して Postgres
オーバーライドを重ねます:

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[コンテナのデフォルトはコンテナ内でバインドする]
コンテナのデフォルトコマンドは *コンテナ内で* `0.0.0.0` にバインドするため、自分の
ingress を前面に立てることができます。Compose スタックはホストのポートを
`127.0.0.1` にマッピングします。素の `docker run` レシピはありません — データ
ボリューム、ポート、初回起動フローが正しく結線されるよう、Compose (または Helm
チャート) を使ってください。
:::

## 選択肢 3 — Kubernetes (Helm)

`deploy/helm/olivares` の Helm チャートは、control plane を **コア StatefulSet** (単一ライター。その
データディレクトリには監査署名鍵と TLS マテリアルが格納される) としてデプロイし、
分散トポロジー向けには、観測結果を **gRPC + mTLS** 経由でコアにプッシュする
**コレクター DaemonSet** をデプロイします。エンジンの v26.9.0 リリースは OCI
チャートを公開せず、独立した `chart-v*` タグもまだ workflow を実行していません。
レビュー済みのソースを checkout からインストールし、公開イメージを digest で固定します。

```bash
helm install olivares \
  deploy/helm/olivares \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> 将来チャートが公開されると、`release-chart.yml` は OCI manifest を cosign 署名し、GPG
> `.prov` layer は出力しません。その成果物は digest で別途検証します。ソースからの
> インストールを署名済み OCI download として扱いません。`deploy/helm/README.md` を参照。

チャートは Docker Hub (`docker.io/olivaresai/olivares`) からコンテナイメージを取得します。同じ
イメージは `ghcr.io/olivaresai/olivares` にもあり、ダイジェストは同一です。Docker Hub の
**匿名**プルのレート制限が障害になる場合は `image.repository` をそちらに向けてください
（ghcr.io は公開イメージに制限を課しません）。チャートは独立リリースまで
`deploy/helm/olivares` から取得します。

常に **ダイジェストで** デプロイし、可変タグは決して使わないでください。完全にネットワーク
非接続のクラスターでは、まずバンドルをミラーします — [エアギャップインストール](/how-to/air-gap-install/) を参照してください。

## トポロジーを選ぶ

| トポロジー | 用途 | ストア | イベントバス |
|---|---|---|---|
| **単一バイナリ** | 単一ノード、ラボ、小規模 estate、エアギャップ | SQLite (埋め込み) | インプロセス |
| **分散** | マルチホスト、スケール、マルチテナント | Postgres + RLS | インプロセス + **NATS ブリッジ** (`OLIVARES_BUS_CONFIG`。ノード間配信は正直なところ最大 1 回 (at-most-once)) |
| **エアギャップ** | egress が許可されない | SQLite または Postgres | インプロセス (境界内であれば NATS ブリッジは任意) |

**data-plane (コレクター) は常に自分のインフラ上で動作します** — control plane だけが
ホスト先を選べる対象です。トレードオフについては
[アーキテクチャ概要](/explanation/architecture/overview/) が説明しています。

## 実際のソースを接続する

新規インストールの estate は空です。実際のソース (Postgres pgAudit、CloudTrail、
エージェントからの OpenTelemetry、eBPF) を結線して access map を埋めましょう —
[ソースを接続する](/how-to/connect-a-source/) と
[Claude Code を接続する](/how-to/connect-claude-code/) を参照してください。設定の全体像は
[設定リファレンス](/reference/configuration/) を参照してください。
