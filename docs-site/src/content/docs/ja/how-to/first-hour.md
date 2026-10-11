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
のは Community ランタイムの継ぎ目です。この 1 時間はそのドライバを複製しません。
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

スモークテストは `serve --insecure` を使い、`--listen 127.0.0.1:8443` を明示的に
渡します。このループバックのバインドはスモークテストのものであり、製品の
デフォルトではありません。対話オペレータは `olivares quickstart` を実行します
（TLS 有効、デフォルトの認証情報なし、単回使用のセットアップトークン）。その
デフォルトの待ち受けアドレスは `:8443` — **すべてのインターフェース** です。
これはサーバーだからです（`cmd/olivares/binddefaults.go`）。このマシンだけに
制限するには `--listen 127.0.0.1:8443` を渡してください。2026-09-17 にこの箱で
測定すると、`quickstart --quiet` は **3 秒** でトークンを印刷しました。

ネイティブのクイックスタートは、セットアップ状態に応じて次のいずれかの案内を表示します。
トークンが表示されるのは、新しく発行された場合だけです。

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

デフォルトのワイルドカードバインドでは、バナーは `https://localhost:8443` を
表示し、このホストが応答する他のすべてのアドレスを列挙します。
これらは別のマシンからコンソールに到達するためのものです。バナーが流れて
しまった場合は、`olivares first-boot` がコンソールのアドレスと初回セットアップ
の状態をもう一度表示します。パスキーを登録する前に `https://localhost:PORT` を
開いてください。ブラウザーは IP アドレスを WebAuthn のリライングパーティとして
拒否し、製品はそれをアドレスの助言で既に伝えています
（`cmd/olivares/consoleaddr.go`）。

### 2. 管理者とテナントを作成する

1 つのコマンドで、起動中のエンジンに対してセットアップを完了できます。起動パネルが
表示するのはこのコマンドです。トークンは標準入力から読み取られるため、プロセス一覧に
現れることはありません。

```bash
# エンジンが起動時に表示した olst_… トークンを貼り付けます
./bin/olivares auth bootstrap --server http://127.0.0.1:8443 \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` はサインインしてセッションを保存するため、次のコマンドはすでに認証
済みです。2026-09-18 にクリーンなデータディレクトリで測定: 263 ms。

エンドポイントに対して直接スクリプトを書く場合は、API でも同じことができます。

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

`POST /v1/agents` は管理操作の追加認証を要求しません。ソース、コネクタ、
ワークスペース、シークレットの追加認証は `admin_step_up` に従い、既定値は
`none` です。AAL3 を必須にする方法は、後述のポリシー設定を参照してください。

### 4. ガバナンス済みセッション: Read を許可し、Bash を拒否する

手順 1 のエンジンを停止します。同じ作業ディレクトリで、手順 1 と 2 の
`DATA` と `TENANT` を保持したまま以下を実行してください。既定で拒否する
ポリシーを書き込み、PEP を有効にしてエンジンを再起動します。

```bash
# TENANT is the tenant_id returned in step 2.
: "${TENANT:?Set TENANT to the tenant_id from step 2}"
cat > ./hook-pep.json <<JSON
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "$TENANT",
      "require_firm_identity": false,
      "policy": {
        "version": "first-hour/v1",
        "default": "deny",
        "rules": [
          { "tool": "Read", "decision": "allow", "reason": "reads are permitted in the first hour" },
          { "tool": "Bash", "decision": "deny", "reason": "shell execution is blocked in the first hour" }
        ]
      }
    }
  ]
}
JSON
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.json \
  ./bin/olivares serve --insecure --data-dir "$DATA" \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444
```

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

統制対象の各決定は、テナント台帳に `hook.tool.allow` または `hook.tool.deny` を追記します（`modules/sessions/hookpep/claudehookpep.go`）。スモークテストは両方の行の存在を確認します。

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
ワークステーションで実行します。公式 CLI のライブセッション (PTY) は Community ランタイムの領分であり、
このページではありません。

## 管理操作の追加認証ポリシー

`admin_step_up` の既定値は `none` です。サインイン済みの管理者は、現在の
セッションの認証強度で操作します。そのため、ソース、コネクタ、ワークスペース、
シークレットの作成に既定で AAL3 は要求されません。認証、権限、テナント分離、
監査は引き続き適用されます。API トークンはこの認証要件を満たしません。

これらの操作に最新の AAL3 を要求するには、`https://localhost:PORT` で
パスキーを登録し、そのコンソールのアドレスで新たにパスキー による追加認証を
完了してください。**Settings → Sign-in → Extra check for administrative
actions** で **Passkey** を選択します。API では
`PUT /v1/auth/step-up-policy` に `{"admin_step_up":"passkey"}` を送信します。
選択した認証要素が機能することを管理者が確認するまで、エンジンはポリシーの
強化を拒否します。`POST /v1/agents` はこの追加認証の対象外です。


## 3. コンソールから Claude Code セッションを起動する

推論資格情報ソースが無いと、stream-json 起動は deny-closed です。

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

プロバイダーを指定しない `managed_injection` の Claude プロファイルでは、
`OLIVARES_SESSION_RUNTIME_WIF` または `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` が
ホストの推論資格情報を提供します。プロバイダーに紐づいたプロファイルはその資格情報を使い、
取得できない場合はホストの資格情報に切り替えずに起動を拒否します。
`provider_account_home` のプロファイルは許可されたツールのログインを使い、
どちらの変数も必要ありません。
[プロバイダーを追加する](/ja/how-to/add-a-provider/) を参照してください。

## OpenCode の状態確認

セットアップ時に OpenCode のサインイン状態確認がタイムアウトまたは失敗した場合、エンジンは状態を読み取れなかったことを報告します。確認を再試行してください。確認の失敗は、ツールがサインアウトしていることを意味しません。同じ組織とアカウントに対する同時確認は、1 つのネイティブコマンドを共有します。成功した結果は最大 30 秒間再利用され、ログインファイルやインストール済み実行ファイルの変更時に更新されます。

## 関連ページ

- [誠実さと限界](/start/honesty-and-limits/)
- [Olivares AI をセルフホストする](/how-to/self-hosting/)
- [プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)
- [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/)
- [Docker でデプロイする](/how-to/docker-deployment/)
