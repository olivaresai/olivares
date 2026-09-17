---
title: "Olivares AI を使い始めて最初の 1 時間"
description: >-
  コントロールプレーンをインストールし、コンソールを開き、コーディングエージェントを 1 つ接続し、
  1 つの操作を許可して別の操作を拒否するガバナンス済みセッションを実行し、証拠を読む。
  3 つの形: ローカル (SQLite)、チーム (Postgres と Docker)、ハイブリッド。
---

このページはこのツリー上の最初の 1 時間です。モックアップではありません。ローカル形の
各コマンドは `task smoke:first-hour` が `./bin/olivares` に対して実行するコマンドです。
形が Docker または Postgres を必要とする場合、ページはそのことを述べます。

製品は次の手順を印刷します。`olivares quickstart` はセットアップトークンの後に
パスキー登録、`olivares agent tool detect`、インベントリ、hook PEP、
`olivares doctor` を示します。`olivares doctor` は
`first-hour-coding-agent`、`first-hour-hook-pep`、`first-hour-next-step` を報告します。
これらの検査は任意です。健全なインストールを失敗にはしません。

## この 1 時間が何か

インストール → コンソール → コーディングエージェントを **1 つ** 接続 → インベントリで
確認 → **1 つの操作を許可し、別の操作を拒否する** ガバナンス済みセッション →
証拠を読む。

このページは、このツリーが既に出荷する **Claude Code hook PEP**
(`olivares claude-hook`) を使います。公式 CLI をセッションプロセスとして起動する
のは D04 の継ぎ目 (PR #2547) です。この 1 時間はそのドライバを複製しません。
モデルターンは送りません。

`--seed-demo` はこの 1 時間ではありません。

## 形 1 — ローカル (SQLite、この箱)

このコンテナには **Docker がありません**。PostgreSQL は **稼働していません**。
ローカル形は組み込み SQLite とループバック HTTP を使います。スモークテストが
再生するのはこの形です。

### 1. ビルドと起動

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

スモークテストは `serve --insecure` を使います。対話オペレータは
`olivares quickstart` を実行します。2026-09-17 にこの箱で測定すると、
`quickstart --quiet` は **3 秒** でトークンを印刷しました。

ウェルカムパネルは次を印刷します。

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

パスキーを登録する前に `https://localhost:PORT` を開いてください。

### 2. 管理者とテナントを作成する

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. コーディングエージェントを 1 つ接続し、インベントリで確認する

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` は AAL3 を **要求しません**。ソース、コネクタ、ワークスペース、
シークレットの作成は **要求します**。

### 4. ガバナンス済みセッション: Read を許可し、Bash を拒否する

`OLIVARES_HOOK_PEP_CONFIG` と deny-closed ポリシーで再起動します。その後:

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. 証拠を読む

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## 形 2 — チーム (Postgres と Docker)

このコンテナには **Docker がありません**。PostgreSQL は **ここでは稼働して
いません**。次のコマンドをこの箱で測定済みとして扱わないでください。

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

`olivares_admin` プールが無いと、`POST /v1/setup` は
`501 cross_tenant_admin_pool_not_configured` を返します。
[Docker でデプロイする](/how-to/docker-deployment/) を参照。

## 形 3 — ハイブリッド (ローカル制御プレーン、ワークステーション上のエージェント)

制御プレーンはローカル SQLite のままにします。エージェントは `claude` がある
ワークステーションで実行します。公式 CLI のライブセッション (PTY) は D04 であり、
このページではありません。

## AAL3 の壁 (今も正しい)

ソース、コネクタ、ワークスペース、シークレットの作成は AAL3 まで **拒否**
されます。標準インストールで PIV/CAC は **501** です。
`https://localhost:PORT` を開き、**Identity → Privileged login** に進みます。

## 3. コンソールから Claude Code セッションを起動する

推論資格情報ソースが無いと、stream-json 起動は deny-closed です。

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`)。`OLIVARES_SESSION_RUNTIME_WIF` または
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE` の **どちらか 1 つ** を設定します。
v26.10 以降、これは唯一の道ではありません。コンソールで資格情報を登録し、
プロファイルに紐づけてください。
[プロバイダーを追加してエージェントを起動する](/ja/how-to/add-a-provider/) と
[プロバイダーセッションを運用する](/how-to/operate-provider-sessions/) を参照。

## 関連ページ

- [誠実さと限界](/start/honesty-and-limits/)
- [Olivares AI をセルフホストする](/how-to/self-hosting/)
- [プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)
- [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/)
- [Docker でデプロイする](/how-to/docker-deployment/)
