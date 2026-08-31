---
title: "Olivares で Claude Code を実行する（co-deployment）"
description: "Olivares のコントロールプレーンと Claude Code ランタイムを 1 台の Linux マシンに、デフォルトでセキュアに co-deploy する。エンジンがワークスペースを共有しながら Claude Code セッションを起動し、ガバナンスし、破棄する — 4 つのトポロジーで。"
---

これは Anthropic ファーストのストーリーの **Operate（運用）** の半分です。Claude Code を単に
*観測（observe）* し *ガバナンス（govern）* するだけでなく、それを **指揮（conduct）** します。
コントロールプレーンは実際の `claude` プロセスを起動し、その I/O をガバナンス対象のストリームに
ブリッジし、すべてのライフサイクル遷移を監査台帳（audit ledger）にアンカーし、そして破棄します。
共有ワークスペース上で、API/CLI（そして後にはポータル）から、**SSH なしで** 行います。
このページは両方の半分を 1 台の Linux ホストに、4 つのトポロジーで、デフォルトでセキュアに
co-deploy します。

*協調的な観測（cooperative observe）* の経路（OTLP テレメトリ → アクセスマップ）については
[Claude Code を接続する](/how-to/connect-claude-code/) を、*ガバナンス* の経路（PEP としての
PreToolUse フック）については [govern-claude-code の例](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code)
を参照してください。このページは **co-deployment** です。2 つのランタイムを一緒に動かすことを扱います。

:::note[ガバナンスは実際にどうセッションに届くか]
セッションがガバナンスされるのは、**エンジンが `claude` の stdin/stdout を所有する** からです —
`stream-json` のヘッドレストランスポートです。エンジンは `claude` を子プロセスとして spawn し
（ネイティブの procRunner）、すべての NDJSON フレームをブリッジします。これはエンジンと `claude` が
実行コンテキストを共有する場合（同じホスト、または同じコンテナ）にのみ機能します。推奨されるトポロジーは
まさにこの理由から両者を一緒に配置します。混在トポロジーとその正直な制約は以下に示します。
:::

## 始める前の 2 つの原則

1. **オプトイン。** ベースの Olivares イメージは distroless であり、**`claude` を含みません**。
   Operate-Claude-Code レイヤーは *別個の* アーティファクトです — 結合イメージ
   （`Dockerfile.agentops`）またはネイティブインストールのアドオンです。ガバナンス対象の Claude
   Code を実行しない場合、それを pull することは決してなく、その追加のサーフェスがコントロール
   プレーンに触れることもありません。
2. **公式ソースから、再配布は決してしない。** Anthropic の利用規約は `claude` バイナリの再配布を
   許可しないため、ビルド時/初回実行時に **Anthropic の公式・GPG 署名済みソース** から
   インストールします（署名済みの apt/dnf/apk リポジトリ）。バージョンは固定（pin）し、
   自動アップデーターは無効化します。サードパーティのバイナリは一切同梱しません。独自の `claude` を
   **持ち込み（BYO）**、エンジンにそれを指すこともできます。

## 4 つのトポロジー一覧

| # | Olivares | Claude Code | エンジンがどう指揮するか | ステータス |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **同一コンテナ**（結合イメージ）、procRunner の子プロセス | **推奨**（2 と同じガバナンス経路） |
| 2 | ネイティブ | ネイティブ | 同一ホスト（systemd）、procRunner の子プロセス | **推奨**、エンドツーエンドのスモークテスト済み |
| 3 | Docker | ネイティブ（ホスト） | クロス名前空間 — そのままではガバナンス不可 | 代わりに co-locate（下記参照） |
| 4 | ネイティブ | Docker（セッションごと） | Docker API 経由のセッションごとのコンテナ | フォローアップ（文書化済み） |

2 つの **co-located（同居）** トポロジー（1、2）がセキュアなデフォルトです。トポロジー 2（ネイティブ）は
[`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh)
によってエンドツーエンドでテストされています。トポロジー 1 は **同じ** ガバナンス対象 procRunner 経路を
再利用します（結合イメージのビルド/実行はまだ自動テストに配線されていません）。トポロジー 3 と 4 は、
ガバナンスする側とされる側を *別々の* コンテナに置こうとします。その境界を越えて stdio をブリッジするには
Docker API へのアクセスが必要です（エンジンが意図的にデフォルトでは **取得しない** 特権です）。
それらの正直な経路は [混在トポロジー](#混在トポロジー3-と-4) で詳述します。

---

## トポロジー 1 — 両方とも Docker（推奨）

1 つのハードニング済みコンテナがエンジン **と** `claude` を実行し、ワークスペースボリュームが
共有作業ディレクトリになります。ループバックのみ、非 root、読み取り専用のルートファイルシステム —
ベースの compose と同一のポスチャに、指揮されるランタイムが加わります。

### 結合イメージをビルドする

`claude` は Anthropic の **署名済み apt リポジトリ** からビルド時にインストールされ、署名鍵の
フィンガープリントが固定（`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`）され、自動更新は
無効化されます。エンジンのベースをダイジェストで固定し、まず検証します。

```sh
# verify the engine image you build FROM (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --build-arg CLAUDE_CHANNEL=stable \
  -t olivares-agentops:26.8.0 .
```

代わりに `--build-arg CLAUDE_INSTALL=byo` で独自の `claude` を持ち込めます（イメージは `claude` なしで
出荷されます。実行時に自分のものをマウントし、`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` を設定します）。

### 起動する

```sh
export OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.agentops.yml up -d
```

オーバーライドは Operate に必要なものだけを変更します。結合イメージ、4 つの書き込み可能ボリューム
（エンジンデータ、**ワークスペース**、claude の `~/.claude` ホーム、短命の推論トークン）、そして
セッションランタイムの環境変数です。それ以外のすべて — `127.0.0.1` バインドのポート、uid 65532、
`read_only` ルート、`cap_drop: ALL`、`no-new-privileges` — はベースから継承されます。

:::caution[最初のガバナンス対象セッションには推論クレデンシャルが必要]
クレデンシャルソースは **拒否クローズド（deny-closed）** です。`stream-json` の起動は
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`（`/run/olivares/session-token`、`olivares-runtime` ボリューム上）から
*短命の* ベアラートークンを読み取り、それを破棄します。保存されるのは機密でない `credential_id` だけです。
WIF/SPIFFE/OIDC のリフレッシャーをそのボリュームに向けてください。トークンが存在するまで、`stream-json` の
起動は **クローズドで失敗** します。エンジンは依然として動作し、その他の点ではガバナンス可能です。
認証の配線はあなたの意図的なステップです。（プロセス内のライブなトークン交換は別途配線されます。）
:::

---

## トポロジー 2 — 両方ともネイティブ（Docker なし）

エンジンと `claude` がホスト上にあり、systemd がエンジンを実行し、それが `claude` を指揮します。
ワークスペースは `/var/lib/olivares/workspaces` に置かれます。

### 1 つのコマンド

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

これはネイティブトポロジーを自動検出し、**検証済み** のエンジンバイナリ（cosign ゲート付きの
`install.sh`）をインストールし、署名済みの apt/dnf/apk リポジトリから `claude` をインストールし
（鍵フィンガープリント検証付き — またはスキップするには `OLIVARES_CLAUDE_INSTALL=byo`）、ログイン不可の
`olivares` サービスユーザーとワークスペースディレクトリを作成し、ハードニング済みの systemd オーバーライドと
環境変数の例を配置します。ガバナンスプレーンを自動起動は **しません** — 1 つを実行するのはあなたの
明示的な判断です。

### インストーラーが配線するもの（とその理由）

- `packaging/systemd/olivares.service.d/agentops.conf` — 指揮される `claude` に `~/.claude` 用の
  書き込み可能な `HOME` を与えるドロップイン（`/var/lib/olivares` 配下に保持されるので、
  `ProtectHome=true` は依然として実ユーザーを保護します）。ワークスペースディレクトリの存在を保証し、
  サンドボックスのプロパティをちょうど **1 つ** だけ解除します。`MemoryDenyWriteExecute` です
  （`claude` ランタイムは JIT コンパイルを行い、W→X メモリを必要とします）。ベースユニットの他の
  ハードニングディレクティブはすべて有効なままです。
- `/etc/olivares/agentops.env` — セッションランタイムの設定（トークンファイル、TTL、任意の
  ゲートウェイベース URL、任意の BYO `claude` パス）。

その後、意図的に次を行います。

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # loopback-only by default
```

:::note[なぜ別個の `claude` サービスが存在しないのか]
長時間実行される `claude` デーモンは、その stdin/stdout をエンジンの手の届かないところに置いてしまいます —
そしてガバナンス対象のトランスポートは stdio *そのもの* です。だからエンジンは `claude` プロセスを
自ら起動し所有します。「ランタイムユニット」はエンジン自身のサービスであり、ドロップインによって
Operate の役割向けに設定されます。
:::

---

## 最初のガバナンス対象セッションを起動する

どちらの co-located トポロジーでも同じ手順です。CLI を認証し、共有ワークスペースを登録し、起動します。

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

すべての遷移（`created → launched → … → stopped`）は **署名済み監査台帳にアンカー** されます
（`olivares agent session events <run-ref>`）。ワークスペースのファイル API
（`olivares agent workspace files|get|put|…`）は jail され、監査されます。これらすべての再現性契約は
[`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh)
であり、ハーメチックな偽の `claude` に対してネイティブの co-deployment を起動し、セッションが
エンドツーエンドでガバナンス可能であることをアサートします。

:::note[本リリースで機能するのは `--isolation native` のみ]
`--isolation container` と `--isolation sandbox` は **前方互換のためのシーム値であり、まだ配線されていません**
（セッションごとのコンテナ Runner は [トポロジー 4](#トポロジー-4--olivares-ネイティブclaude-はセッションごとのコンテナ)
で文書化されたフォローアップです）。ネイティブランナーは、要求された分離なしに黙って `claude` を実行する
代わりに、コンテナ/サンドボックスの起動を **拒否** します（明確なエラー）。`native` を使用してください —
結合イメージ / systemd の co-deployment の下では、それがエンジン自身のハードニング済みコンテナ/ホスト境界です。
:::

:::caution[`bypassPermissions` はガバナンスの背後に置くべき]
許容的な `--permission-mode`（`bypassPermissions`、`dontAsk`）でヘッドレスに `claude` を実行することは、
まさにガバナンスプレーンが欲しいときです。エンジンのアローリストされた環境はエージェントに
`OLIVARES_*`/`ANTHROPIC_*` シークレットを決して漏らさず、PreToolUse PEP / バジェット / キルスイッチが
セッションが実際に何をできるかを決定します。
:::

---

## 混在トポロジー（3 と 4）

これらはガバナンスする側とされる側をコンテナ境界で分割します。それが何を犠牲にするかを冷静に
認識してください。

### トポロジー 3 — Olivares が Docker、Claude がホスト上

**クリーンなガバナンス経路は存在しません**。コンテナ化されたエンジンは、ホストの名前空間にある
プロセスの stdio を所有できず、ガバナンス対象のトランスポートは stdio です。ホスト上の `claude` に
到達するには、ホストの PID 名前空間とマウントをエンジンコンテナに共有する必要があります — エンジンを
封じ込める意義を打ち消す、大規模で意図的な脱分離です。**代わりに co-locate してください**。両方を結合
イメージで実行する（それが *まさに* トポロジー 1 です）か、両方をネイティブで実行する（トポロジー 2）かです。
これは取り繕うのではなく明示する、実在する制限です。

### トポロジー 4 — Olivares ネイティブ、Claude はセッションごとのコンテナ

これは **セッションごとの新鮮なコンテナ分離** の自然な居場所です。各セッションは真新しい
ハードニング済みの `claude` コンテナ（ワークスペースをバインドマウント、読み取り専用ルート、非 root、
cap-drop）を得て、エンジンが Docker API を通じて作成し破棄し、stdio は Docker の attach/hijack 経由で
ブリッジされます。データモデルのシームはすでにそれを **モデル化** しています（`--isolation container` は
有効な値であり、それが消費するエグゼキューターのマウントプリミティブはすでに出荷されています）— ただし
その背後のランナーはまだ配線されていないので、ネイティブランナーは今日その値を拒否します（上記の注を参照）。

**これは文書化されたフォローアップであり、本リリースには出荷されていません。** 兄弟コンテナを駆動する
ことは、エンジンに Docker API アクセスを与えることを意味します（理想的には最小権限のソケットプロキシ
経由）— ソケットフリーの結合イメージを優先して本リリースが意図的に避けている信頼サーフェスです。
このトポロジーを選ぶことは、その Docker API 付与 *を代償として* より強いガバナンス側/される側の分離を
選ぶことです。これは既存の `isolation=container` シームの背後に到着します。それまでは、セキュアな
デフォルトは co-location です。

---

## セキュリティポスチャ（全トポロジー）

- **デフォルトでループバック。** ホストポートは `127.0.0.1` のみで公開されます。コンテナ内ではエンジンは
  コンテナ *内部の* `0.0.0.0` でリッスンするため、**ホストのポートマッピングが露出境界** です。自前の
  TLS 終端認証プロキシなしに、非ループバックのホストアドレスで公開しないでください。ネイティブ/systemd の
  デフォルトバインドはループバックです。意図的に公開してください。
- **非 root、最小権限。** uid/gid 65532、読み取り専用ルートファイルシステム、`cap_drop: ALL`、
  `no-new-privileges`（Docker）/ 文書化された 1 つの W^X 緩和を除く完全な `Protect*`/`Restrict*` セット
  （systemd）。
- **最小データ、アローリストされた環境。** 子の `claude` は明示的なアローリスト（PATH、HOME、ロケール…）
  に加えてメモリ内の推論トークンのみを継承します — `OLIVARES_*` 署名鍵は **なし**、発行された
  クレデンシャルを覆い隠しうる周辺の `ANTHROPIC_*`/`CLAUDE_CODE_*` も **なし** です。
- **検証済みサプライチェーン。** エンジンは cosign 署名済みです（検証する / ダイジェストで固定する）。
  `claude` は鍵フィンガープリントを固定した Anthropic の署名済みリポジトリからインストールされます。
  インストーラーは、明示的にオプトアウトしない限り **未検証のエンジンの実行を拒否** します。
- **アンカーされた監査。** すべてのライフサイクル遷移とすべてのワークスペース変更は、ハッシュ連鎖された
  署名済み台帳に `PayloadHash` によって封印されます — ファイルのバイト列やフレームの内容が永続化される
  ことは決してありません。

## 関連項目

- [Claude Code を接続する](/how-to/connect-claude-code/) — 協調的な観測の経路。
- [セキュリティとハードニング](/how-to/security-hardening/) — エンジンのベースラインポスチャ。
- [リリースを検証する](/how-to/verify-a-release/) — cosign / SBOM / SLSA の検証。
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — この co-deployment を含むインストールマトリクス。
