---
title: クイックスタート
description: >-
  ゼロから、実際の Permitted-vs-Observed ドリフト結果を伴う read/write
  アクセスグラフが構築されるまで、おおよそ5分。まずは同梱のデモエステートで、
  続いて実物の pgAudit コネクターで、これがデモではないことを証明します。
---

これは Olivares AI が *何のためにあるのか* を最短で体感する経路です。エステートの
**read/write アクセスマップ**と、その上に重ねた **Permitted-vs-Observed ドリフト** —
エージェントに*付与された*アクセスと、実際に使用していると*観測された*アクセスとの差分です。

その結果には2回、合計でおおよそ5分で到達します。

1. **1分で、同梱のデモエステート上で** — 「そもそもどんな見た目なのか」を即座に確認する
   オンランプ（合成された観測データが、実物のエンジンを通って流れます）。
2. **続いて実物のコネクターで** — 同じグラフとドリフトを、今度は PostgreSQL の
   **pgAudit** ログから一字一句そのまま解析し、このヒーローがデモではなく本物のデータ上で
   動くことを証明します。

以下のすべてのコマンドは、書かれたとおりそのまま `scripts/quickstart-smoke.sh`
（[再現性](#5-自分で再現する)）が実行します。そのため、このページがバイナリから知らぬ間に
ずれていくことはありません。

これは学習用の経路であり、本番デプロイではありません。実物のインストール（デフォルト認証情報なし、
ワンタイムのセットアップトークン、TLS）については、[セルフホスティング](/ja/how-to/self-hosting/)
を参照してください。UI のガイド付きウォークスルーは、
[zero-to-graph チュートリアル](/ja/tutorials/zero-to-graph/)を参照してください。

:::caution[デモモードは学習専用]
`--seed-demo` は、**公開された、ソースツリー上のパスワード**を持つデモ管理者と合成データを
プロビジョニングし、**非ループバックアドレスでは起動を拒否します**。実物のインストールには
決して使用しないでください。本物の初回起動の経路は、以下のステップ3と
[セルフホスティング](/ja/how-to/self-hosting/)にあります。
:::

## 1. 単一バイナリをビルドする

リポジトリのチェックアウトから（Go 1.26+、[Task](https://taskfile.dev)、pnpm が必要 —
`task build` はコンパイル前に Web UI をバンドルします。ストアは pure-Go の SQLite なので、
C ツールチェーンは不要です）：

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` は `./bin/olivares` に1つの自己完結型アーティファクトを生成します —
エンジン、組み込みの Web UI、そしてファーストパーティのコネクタープラグインです。
**コンテナおよび Kubernetes のインストールはこの同じバイナリをラップします**：
公開イメージと Compose ファイル（[セルフホスティング](/ja/how-to/self-hosting/)）、または
`kubectl apply -f deploy/manifests/install.yaml` するフラットなマニフェスト（Helm 不要）。
以下に見るヒーローは3つすべてで同一です — 異なるのはデモシードだけです
（ループバック専用であり、実物のインストールには決して含まれません）。

## 2. デモエステートを起動する（ループバックのみ）

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` はループバック上でプレーンテキストの HTTP を提供します（ローカルデモには問題ありません。
それ以外では **TLS がデフォルトで有効**です）。最初は、デフォルトで deny-closed になっている
シーム（ジャッジなし、エンベッダーなし、承認ゲートなし、実物のソースなし）に対する正直な
`WARN` 行が表示され、続いて認証情報を含む **DEMO MODE** バナーが表示されます：

```text
demo@olivares.local / olivares-demo-estate
```

合成エステートは、ライブの pgAudit や OpenTelemetry コレクターとまったく同じように、**実物の**
イベントバスを通って流れます — シードされているのは観測データだけです。

## 3. アクセスグラフとそのドリフトに到達する（ヒーロー）

サーバーを起動したまま、2つ目のターミナルでログインし、デモテナントを解決して、グラフとその
ドリフトを取得します：

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

デモエステートはきっかり **20個のノードと13個のエッジ**を返し、ドリフトは
**8件の予期しないアクセス**と **2件の未使用の付与**を浮かび上がらせます。すべてのエッジは
本製品の正直さの軸を備えており、各 finding を当て推量なしで読み取れます：

- **`mode`** — `read` / `write` / `readwrite` / `unknown`：R/W 分類で、シグナルから一字一句
  そのまま取得され、推論されることは決してありません。
- **`attribution_tier`** — `firm` / `approximate` / `unknown`：そのアクセスが*特定の*
  エージェントまたはワークロード ID にどれだけ確実に紐づいているか。デモでは、**6個のエッジが
  firm、7個が approximate** です — 例えば、付与されたことのないリソースを読むエージェント
  （`appdb.public.secrets`、*firm*）に対し、共有プールの ID がログを書く場合
  （`appdb.public.logs`、正直に *approximate*）。
- **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed`：*リソース側の*シグナルの
  忠実度で、attribution とは直交します。

:::tip[差別化された主要な能力]
**Permitted と Observed の差分**こそが *least-privilege ドリフト* です — 監査人や攻撃者よりも
先に見つけたいものです。シードはそれが「すべてがドリフト」ではなく本物であることを証明します：
付与され、**かつ**観測された3個のエッジは突き合わせられてドリフト結果から外れ、本物のギャップ
だけが残ります（8件の予期しないアクセス + 宣言されているが一度も行使されていない2件の付与）。
そして本製品は、証明できないラベルを決して捏造しません — 単に `approximate` でしかない attribution
はそう言い、`firm` なエージェントをでっち上げたりはしません。
:::

同じグラフは、`http://127.0.0.1:8901` の組み込み Web UI でもレンダリングされます
（デモ認証情報でログインし、**Demo Estate** 組織に切り替えてください）。

次のステップに進む前に、デモサーバーを停止してください（`Ctrl-C`）。

## 4. 実物のコネクターで証明する（デモではない）

このヒーローはシードされた魔法ではありません。あなたのソースが観測するもの上で動きます。ここでは
**実物の pgAudit コネクター** — 本番インストールが使うのと同じコードパス — を、PostgreSQL の
監査ログに対して、**デモシードなしで**配線します。

まず、小さな `pgAudit` の csvlog（1つのアプリケーションによる本物の監査ログ3行：2件の read と
1件の write）を作ります。本番では pgAudit がこれを Postgres のログに書き込みます。ここではその
末尾の代わりにファイルを使います：

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

次に、**実物の初回起動**を行います：デフォルト認証情報なしで一度起動し、ワンタイムのセットアップ
トークンをクレームし、コネクターを取り付けるテナントを作成します。

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

コネクターは1つのオペレーター設定ファイルから、値で配線され、エンジンによって永続化されることは
決してありません。pgAudit をあなたのテナントのログに向け、その設定で**再起動**します：

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

起動ログは `ingest: wired source … kind=pgaudit` を出力します。2つ目のターミナルで再度ログインし、
グラフを読みます — 今度はエッジが**本物として解析されたもの**であり、シードされたものではありません：

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

**3個のエッジ**が得られます — `salesdb.public.customers`（read）、`…orders`（write）、
`…secrets`（read） — それぞれ `signal_source: pg_audit` と `coverage_tier: clean`
（pgAudit は R/W を一字一句そのまま報告します）を持ち、ドリフトは**3件すべてを予期しない
アクセスとしてフラグ**します（まだ付与が配線されていないため、観測されたすべてのアクセスが
ドリフトです）。

:::note[デフォルトで正直に：identity を配線するまでは approximate]
これらの実物のエッジは `attribution_tier: firm` ではなく `approximate` として着地します —
pgAudit シグナルが名指しするのは*統制されたエージェント*ではなく、データベースのロール／
アプリケーションだからです。それが正直なデフォルトです：本製品は、証明できないアクセスを
エージェントに確実に attribution したと主張しません。`firm` を得るには、認証情報をエージェント
またはワークロード ID に紐づける identity ソース（LDAP/IdP/SPIFFE）を配線します —
[ソースを接続する](/ja/how-to/connect-a-source/)を参照してください。デモエステートが `firm` な
エッジを示すのは、まさにそのエージェントを事前に紐づけているからです。
:::

:::note[エンドポイントの形]
Permitted-vs-Observed の結果は `/v1/m/accessmap/drift` で提供されます（`/diff` は存在しません）。
`/v1/m/accessmap/*` のルートは 53 パスの安定コア契約には含まれず、別の **beta**
ドキュメントとして [module-route リファレンス](/reference/api-beta/) で公開されます。
[API リファレンス](/reference/api/)は安定コアサーフェスを記述します。
:::

## 5. 自分で再現する

上記のすべては、実物のバイナリに対してエンドツーエンドでアサートされています：

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

これはデモエステート**と**実物の pgAudit 経路の両方を起動し、このページの正確なコマンドを実行し、
数字（20ノード／13エッジ、8件の予期しない + 2件の未使用、3件の実物 pgAudit エッジ）を検証します。
install→value の経路、またはドリフト結果が真でなくなった瞬間、スモークは失敗します — それがこの
ページを正直に保つ契約です。実時間で数秒で完了します。上記の人間が歩く経路が、ドキュメント化された
**5分**です。

## 次のステップ

- **実物で動かす：** getting-started チュートリアルは、すべてのインストールシナリオを
  エンドツーエンドで歩きます —
  [シングルノード（systemd）](/ja/tutorials/getting-started/single-node/)、
  [Docker Compose](/ja/tutorials/getting-started/docker-compose/)、
  [Kubernetes/Helm](/ja/tutorials/getting-started/kubernetes/)、そして
  [エアギャップ](/ja/tutorials/getting-started/air-gapped/)。
  [セルフホスティング](/ja/how-to/self-hosting/)はそれらを横断する意思決定ページです。
- **実物のシグナルを供給する：** [ソースを接続する](/ja/how-to/connect-a-source/)と
  [コネクターカタログ](/ja/reference/connectors/) — 各ソースが何を観測するか、その正直な
  coverage tier、そして attribution を `firm` にするための identity の配線方法。
- **堅牢化する：** [セキュリティ堅牢化](/ja/how-to/security-hardening/) — 安全なデフォルト、
  human-in-the-loop の承認、そして実行前のリリース検証。
- **限界を知る：** [正直さと限界](/ja/start/honesty-and-limits/) — 今日動くもの、設計段階のもの、
  そして本製品が意図的に行わないこと。
