---
title: Docker でデプロイする
description: >-
  Docker Hub からイメージをプルして検証し、control plane を Docker で本番運用する ──
  堅牢化したシングルノード SQLite、マルチテナント Postgres、スケジュール済み DR バックアップ、
  リバースプロキシによる TLS 終端、アップグレード、ダイジェスト固定。
---

このガイドは、Olivares AI の control plane を Docker で本番運用に投入するエンジニアと SRE 向けです。
製品全体は単一の distroless イメージ ── Web UI を組み込んだエンジン ── なので、
単一ホストで外部依存なしに SQLite トポロジを実行でき、必要なときには Postgres オーバーライドで
マルチテナントトポロジを構成できます。どの経路でも同じセキュアなデフォルトを維持します:
デフォルト認証情報なし、ワンタイムのセットアップトークン、TLS デフォルト有効、
そしてホストポートはループバックにバインド。

:::note[ベータ ── リリースはまだ作成されていません]
Olivares AI は **ベータ** です。以下のイメージ座標は **最初のリリース
（CalVer `26.8.0`）が出荷された後** にのみ解決します。それまでレジストリにはプルできるものがありません。
これは本番運用可能であることの保証ではなく、あなたが使うことになるデプロイの形だと捉えてください。
:::

すべてのデプロイ選択肢とそのデフォルトを俯瞰する判断ページについては、
[control plane をセルフホストする](/how-to/self-hosting/) を参照してください。
切り離されたサイトについては
[エアギャップ環境にインストールする](/how-to/air-gap-install/) を、
スケールアウトについては下記の Kubernetes/Helm 経路を参照してください。

## 1. イメージをプルして検証する

主要なコンテナのプル元は **Docker Hub** です:

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

同じ内容は `ghcr.io/olivaresai/olivares` にも公開されています ── ダイジェストで同一であり、
バックアップ兼ビルドレジストリとして使われます。Docker Hub は**匿名**プルにレート制限を課しますが、
ghcr.io は公開イメージの匿名プルにレート制限を課しません。CI ノードや大規模なフリートが上限に達した
場合は `docker login` するか、ghcr.io の座標に切り替えてください。タグには **先頭に `v` が付きません**:
`:26.8.0` はリリースを固定し、`:latest` は浮動、`:26.8.0-fips` / `:26.8.0-stig` は
堅牢化されたバリアントです。ベースと `:latest` タグはマルチアーキ
（`linux/amd64`、`linux/arm64`）で、`fips`/`stig` は `amd64` 専用です。

control plane はセキュリティ製品なので、実行前に検証してください。署名は **キーレス**
（Sigstore）で、プロジェクトの GitHub Actions アイデンティティに対して行われ、どちらの
レジストリに対しても同一に機能します ── 署名とアテステーションは `cosign copy` で
Docker Hub にコピーされるため、ダイジェストは同じです:

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

完全なチェーン ── チェックサム署名、SBOM、OpenVEX、SLSA provenance ── は
[ダウンロードしたものを検証する](/how-to/verify-a-release/) にあります。検証したら、
可変タグではなく、検証した **ダイジェスト** でデプロイしてください
（[§8](#8-本番ではダイジェストで固定する) を参照）。

## 2. シングルノード、SQLite

### `docker run` を使う（堅牢化）

イメージのデフォルトコマンドは **コンテナ内** で `0.0.0.0` にバインドするため、
イングレスで前段に置けます。下記のホスト側ポートマッピングは公開をループバックに固定します。
非 root、読み取り専用、すべての capability を削除して実行してください:

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| フラグ | 理由 |
|---|---|
| `--user 65532:65532` | distroless イメージに焼き込まれた非 root の `nonroot` UID として実行する |
| `--read-only` | ルートファイルシステムを不変にする。書き込み可能なのはデータボリュームと `/tmp` のみ |
| `--tmpfs /tmp` | 書き込み可能なスクラッチ tmpfs。rootfs が読み取り専用のため必須 |
| `--cap-drop ALL` | エンジンは Linux capability を一切必要としない |
| `--security-opt no-new-privileges` | setuid バイナリ経由の権限昇格をブロックする |
| `-v olivares-data:/var/lib/olivares` | データディレクトリを永続化する（[§5](#5-運用上の注記) を参照） |
| `-p 127.0.0.1:8443:8443` | HTTPS（REST + Web UI）を **ループバックのみ** に公開する |
| `-p 127.0.0.1:8444:8444` | gRPC（取り込み / ControlPlane API）をループバックのみに公開する |

ログからワンタイムのセットアップトークンを読み取り、最初の管理者を作成します:

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` は、エンジンが初回起動時に発行する自己署名証明書を受け入れます。リバースプロキシ
（[§6](#6-リバースプロキシ--tls-終端)）またはあなた自身の TLS マテリアルで
本物の証明書に置き換えてください。トークンは **一度だけ** 表示され、使い切りです。

### Docker Compose を使う

リポジトリには、ボリューム、ループバックポートマッピング、上記と同じ堅牢化フラグを
配線した Compose スタックが同梱されています:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

ベースファイルはイメージを `docker.io/olivaresai/olivares:latest`（Docker Hub）にデフォルト設定します。
検証可能な本番デプロイでは、`deploy/compose/.env` の `OLIVARES_IMAGE` を
ダイジェスト固定の参照に設定してください（[§8](#8-本番ではダイジェストで固定する) を参照）。
データは `olivares-data` ボリュームに永続化されます。

## 3. マルチテナント Postgres

マルチテナントトポロジでは、ベースファイルの上に Postgres オーバーライドを重ねます。
まず2つのパスワードを設定してから、スタックを立ち上げます:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

オーバーライドは `postgres:16-alpine` を立ち上げ、初回 init 時に **最小権限** の
`olivares_app` ロールと `olivares` データベースをプロビジョニングし
（`initdb/10-app-role.sh` 経由で正規の `deploy/postgres/01-app-role.sql` を実行）、
`--engine=postgres` でエンジンをその非スーパーユーザーロールに向けます。これにより
FORCE-RLS テナントバックストップが実効的になります: エンジンはスーパーユーザー /
`BYPASSRLS` ロールに対しては **起動を拒否** します。

:::caution[`sslmode=disable` はネットワーク内デモ専用]
オーバーライドの DSN は、両コンテナが Docker ネットワークを共有するため `sslmode=disable` を
使います。**本番では `sslmode=verify-full` で TLS を使います。** 堅牢化されたデプロイでは、
DSN Secret とマネージド（またはあなた自身の）Postgres を備えた Helm チャートを優先してください ──
[§8](#8-本番ではダイジェストで固定する) を参照。
:::

## 4. ディザスタリカバリ用バックアップ

バックアッププロファイルは、スケジュールされ、台帳の連続性を損なわない DR バンドルを生成します:
ストアのスナップショットに加え、署名鍵を KEK で暗号化し、テナントごとのチェーンの先端を記した
マニフェストを添えます。パスフレーズを **リポジトリとイメージの外** に保持したファイルへ書き込み、
ワンショットの `backup` プロファイルを実行してください:

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

このジョブはエンジンのデータボリュームを共有し、バンドルを `olivares-backups` ボリュームへ
書き込みます。そして ── イメージが distroless のため ── 保持期間管理はホストに委ねます:
古いバンドルはホストの cron で間引いてください
（`find <backups> -name '*.drbundle' -mtime +14 -delete`）。スケジュールされた RPO のために
実行をホストの cron でラップし、**`olivares-backups` ボリュームをオフサイトにミラーリング** してください ──
同一ホスト上のバックアップはディザスタリカバリではありません。次のコマンドで復元・検証します:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

完全な RPO/RTO、鍵の保管、DR ドリルの手順はリポジトリの DR ランブックにあります。
より上位のウォークスルーは [バックアップと復元](/how-to/backup-and-restore/) です。

## 5. 運用上の注記

**ヘルスはコンテナではなくホストからプローブする。** イメージは **distroless** で ──
シェルも `curl` もないため、コンテナ内の `HEALTHCHECK` は意図的に存在しません。
エンジンは HTTPS ポートで `/livez` と `/readyz` を公開します。これらをホスト
（またはオーケストレータ）からプローブしてください:

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

`/readyz` の到達可能性が可用性シグナルです ── 外部監視に組み込んでください
（[Prometheus で監視する](/how-to/monitor-with-prometheus/) を参照）。

**セットアップトークンはログに一度だけ現れる。** 初回起動時、コンテナ出力に使い切りの
`olst_…` トークンが出力されます。バッファがローテートする前に
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'`（または Compose の同等コマンド）で
捕捉してください。最初の管理者を作成すると消費されます。

**データディレクトリをバックアップする。** `/var/lib/olivares`（`olivares-data` ボリューム）には
**SQLite ストア、監査署名鍵、TLS マテリアル** が格納されています。これを失うと台帳の署名
アイデンティティを失い、監査の連続性が壊れるため、ボリュームを保護しバックアップしてください ──
ライブストアの場当たり的なコピーではなく、[§4](#4-ディザスタリカバリ用バックアップ) の DR プロファイルを
使ってください。

## 6. リバースプロキシ / TLS 終端

そのままではエンジンは自身の **自己署名** 証明書を提供します。評価には十分ですが、
信頼を検証するクライアントには適しません。本番では、ループバックにバインドされたエンジンの前段に、
運用者が提供する証明書（あなたの CA または ACME から）で TLS を終端するリバースプロキシを置き、
ネットワークに公開するのはプロキシだけにしてください。

エンジン自身が TLS を話すため、プロキシはループバックポート上で HTTPS でエンジンに接続します。
最小構成の nginx server ブロック:

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

公開証明書を自動でプロビジョニングする Caddy での同等構成:

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

エンジンのホストポートを `127.0.0.1`（上記のデフォルト）にバインドしたまま保ち、
到達可能なのがプロキシだけになるようにしてください。gRPC 取り込みポート（`8444`）は
コレクタ向けです。分散トポロジを運用する場合のみ、独自の TLS 経路とともに意図的に公開してください。

## 7. アップグレード

データボリュームはコンテナの置き換えをまたいで永続するため、アップグレードは次のとおりです:
バックアップし、新しい固定タグをプルし、コンテナを作り直す。

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

コンテナの作り直しは名前付きボリュームには触れないため、ストア・署名鍵・TLS マテリアルは
引き継がれます。常に **アップグレード前にバックアップ** し、作り直す前に新しいイメージを
再検証してください。

## 8. 本番ではダイジェストで固定する

可変タグ（`:26.8.0`、`:latest`）は評価用です。本番では検証した **ダイジェスト** を固定してください ──
ダイジェストは不変であり、まさにあなたが承認したものです:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Compose では、`deploy/compose/.env` にダイジェスト参照を設定します:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

スケールアウトとマルチノードには Helm チャートを使ってください ── OCI アーティファクトとして
`oci://ghcr.io/olivaresai/charts/olivares` に公開され、cosign 署名済みで、イメージダイジェストで
固定されています。チャートコマンドについては
[control plane をセルフホストする](/how-to/self-hosting/) を、完全に切り離されたサイトについては
[エアギャップ環境にインストールする](/how-to/air-gap-install/) を参照してください。
