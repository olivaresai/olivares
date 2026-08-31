---
title: "設定リファレンス"
description: "Olivares AI コントロールプレーンの検証済み設定サーフェス：serve フラグ、環境変数、ストア選択、そして標準で出荷されるセキュアなデフォルト。"
---

このページは、コントロールプレーンエンジン — `olivares` という名前の単一 Go バイナリ — の設定サーフェスを文書化します。`serve` サブコマンドが受け付けるフラグ、エンジンが起動時に読み取る環境変数、ストアとポリシー決定点がどのように選択されるか、そして設定がまったくない状態で有効になるセキュアなデフォルトをカバーします。

ここに列挙されたすべては、エンジン自身のコマンド定義とコンポジションルートから取られています。設定がソースで確認できない場合、それは列挙されません。これらのデフォルトの背後にある概念的なセキュリティ姿勢については [セキュリティモデル](/ja/explanation/security/security-model/) を、実行可能なエンドツーエンドのパスについては [セルフホスティング](/ja/how-to/self-hosting/) を参照してください。

:::note[設定の哲学]
エンジンは、肥大化した設定ファイルではなく、フラグと環境変数で設定されます。エンジンが読み取るすべての変数は以下に列挙され、ソース自体から生成されます。実際のソースを配線するシークレットは、環境変数で参照されるオペレータ保持のファイルに留まり — 決してストアには入りません。デフォルトは fail closed になるよう選ばれています：loopback バインド、TLS オン、デフォルト認証情報なし。
:::

## `serve` サブコマンド

`olivares serve` は REST/web HTTP サーバーと gRPC サーバーを 1 つのプロセスで実行し、Web UI は API と同じオリジンから提供されます。以下のフラグは、そのコマンドへの検証済み設定入力です。

| フラグ | デフォルト | 目的 |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | HTTP リッスンアドレス（REST API + 組み込み Web UI）。 |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC リッスンアドレス（コントロールプレーン / コレクタインジェスト API）。 |
| `--data-dir` | `$OLIVARES_DATA_DIR`、既存の `./olivares-data` インストール、なければ `$XDG_DATA_HOME/olivares` または `~/.local/share/olivares` | データディレクトリ：監査署名鍵、TLS 素材、そして（SQLite の場合）ストアファイル。 |
| `--engine` | `sqlite` | ストアエンジン：`sqlite` または `postgres`。 |
| `--dsn` | 空（データディレクトリ内の SQLite ファイル） | ストア接続文字列。 |
| `--checkpoint-interval` | `1h` | すべてのテナントチェーンにわたって署名済み監査チェックポイントを書き込む頻度。`0` で無効化。 |
| `--insecure` | オフ | HTTP/gRPC を平文で提供します。危険。ローカルホスト開発専用。 |
| `--seed-demo` | オフ | デモ/E2E 用に合成サンプル estate をロードします。非 loopback バインドでは起動を拒否します。 |

TLS はデフォルトでオンです。`--tls-cert`/`--tls-key` が供給されない場合、エンジンは、いずれのリスナーが接続を受け入れる前に、データディレクトリに自己署名証明書を一度だけ事前に確実に用意するため、HTTP サーバーと gRPC サーバーは同じ証明書を使い、どちらも平文にフォールバックしません。自己署名証明書を生成するとき、`cert_fingerprint_sha256`（証明書のダイジェスト。ブラウザーが表示するもの）と `pin_sha256`（リーフ証明書の SPKI のダイジェスト）をログに記録します。`--pin-sha256` は後者を base64 または16進で受け取ります。証明書フィンガープリントは別のダイジェストです。どちらの書き方でも 32 バイトなので解析自体は通り、その後ハンドシェイクで `TLS SPKI pin mismatch` として失敗し、そのエラーが使うべき値を示します。

:::caution[`--insecure` は意図的に loopback 専用]
`--insecure` は平文の HTTP と gRPC を提供し、これは Bearer トークンを wire 上にさらすことになります。gRPC のパスは **fail closed** です：`--insecure` の外では、サーバーは黙って劣化するのではなく、平文リスナーの構築を拒否します。`--insecure` は、ローカル開発中に `127.0.0.1` に対してのみ使用し、公開されたアドレスでは決して使用しないでください。
:::

:::danger[`--seed-demo` は合成であり、自己防衛する]
`--seed-demo` は、**公開された、ソースツリー内のパスワード** を持つデモ管理者と、捏造された estate データをプロビジョニングします。これはデモと E2E 専用です。エンジンは非 loopback リスナー上でのその起動を拒否します：`--listen` または `--grpc-listen` のいずれかが loopback アドレスでない場合、コマンドはエラーで終了します。使い捨てのデータディレクトリを使い、実際のデータには決して向けないでください。
:::

完全なフラグの一覧 — 分散デプロイメントで使われる Postgres 専用および相互 TLS のフラグを含む — は [CLI リファレンス](/ja/reference/cli/) にあります。このページは共通の設定サーフェスを文書化します。一部の高度なフラグは、[アーキテクチャ概要](/ja/explanation/architecture/overview/) で説明されるマルチノードトポロジを統治します。

## 環境変数

以下の 3 グループは、オペレーターが最初に遭遇する変数とその動作です。その後に、バイナリより古くならないようエンジン自身のソースから生成された完全な一覧が続きます。

### データディレクトリ

| 変数 | 効果 |
| --- | --- |
| `OLIVARES_DATA_DIR` | `--data-dir` が指定されない場合のデフォルトデータディレクトリ。どちらもない場合、エンジンは既存の `./olivares-data` インストールを使い、なければ `$XDG_DATA_HOME/olivares` または `~/.local/share/olivares` を使います。カレントワーキングディレクトリは決して使いません — そこに秘密鍵を残してしまうからです。 |

データディレクトリは、監査署名鍵、TLS 証明書と鍵、そして — SQLite エンジンの場合 — ストアファイルを保持します。再起動をまたいで永続化してください。

### 実際のソースの配線

| 変数 | 効果 |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | エンジン起動前に、実際の観測ソースと ID ロスタープロバイダを配線する JSON ファイルへのパス。 |

`OLIVARES_SOURCES_CONFIG` は、非デモのシグナルソースと ID ロスタープロバイダが解決される唯一の入力です。これはオペレータのシークレットを保持する設定であり、意図的にストアの外に置かれます。エンジンは起動中にこれを読み取り、ランタイムの起動 **前に** すべてのソースを登録します。

その扱いは fail-fast ではなく正直です：

- **欠落した** 変数は空の設定を生み、エンジンは実際には何も配線されていないと警告します。
- **読み取り不能または無効な JSON の** ファイルは警告し、空の設定を生みます — 起動を中止することは決してありません。
- 設定されたが **空の** ソースリストは、いかなるコネクタもインジェストしないと警告するため、estate はライブトラフィックなしで動作します。黙って健全に見えることはありません。
- 空の **ID** リストは、ロスターが空のままであり、ロスター同期が no-op であると警告します。

これは設計によるものです：未設定のソースは、コントロールプレーンをクラッシュさせたり動作しているふりをしたりするのではなく、警告を表面化します。アクセスマップを実際にデータで満たすには、少なくとも 1 つのソースを設定してください — [ソースを接続する](/ja/how-to/connect-a-source/) と、OpenTelemetry および MCP 経由の協調的な Claude Code パスについては [Claude Code を接続する](/ja/how-to/connect-claude-code/) を参照してください。

### 認可決定点（PDP）

認可ポリシー決定点（policy decision point）はコンポジションルートで環境によって選択されます。ネイティブの属性ベースアクセス制御（ABAC）エンジンとロールベースアクセス制御（RBAC）が常に統治します。外部 PDP は、選択された場合、アクセスを決して広げることのできない追加の **制限専用（restrict-only）** レイヤーです。

| 変数 | 効果 |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | 外部 PDP を選択します：`cedar`、`opa`、または `none`（空または `none` はネイティブ ABAC のみを意味します）。 |
| `OLIVARES_PDP_CEDAR_FILE` | Cedar エンジン専用：オペレータの Cedar ポリシーファイルへのパス。 |
| `OLIVARES_PDP_OPA_URL` | OPA エンジン専用：Open Policy Agent エンドポイントのベース URL。 |
| `OLIVARES_PDP_OPA_PATH` | OPA エンジン専用：そのエンドポイント配下で問い合わせる決定パス。 |
| `OLIVARES_PDP_OPA_TOKEN` | OPA エンジン専用：OPA エンドポイント用の Bearer トークン。 |

1 つの継ぎ目（seam）の背後に 2 つのアダプタが座っています：**組み込み Cedar** 評価器（プライマリの、純 Go のパス）と、**OPA-over-HTTP** アダプタです。オペレータは 1 つのエンジンを選びます。どちらも、組み込み RBAC がすでに行った決定を、制限することはできても決して広げることはできません。

:::note[不正なポリシーがプレーンを非統治にすることは決してない]
`OLIVARES_PDP_ENGINE` がエンジンを選択しているがその設定が無効な場合 — 読み取り不能な Cedar ファイル、不正な形式の OPA ターゲット — エンジンは **外部 PDP のみを無効化** し、ネイティブ ABAC エンジンと RBAC の強制を維持し、声高にログを記録します。壊れたポリシーファイルが、リクエストを黙って非統治のままにしたり、コントロールプレーンをクラッシュさせたりすることは決してありません。
:::

deny-by-default モデル、アクセスグラフの閲覧が持つ特権的な性質、そしてすべての認可読み取りがどのように監査されるかについては、[セキュリティモデル](/ja/explanation/security/security-model/) を参照してください。

<!-- BEGIN GENERATED olivares-env-reference — regenerate with `bash scripts/check-config-env-docs.sh --write`; do not edit by hand -->

### Complete variable reference

The table below is generated from the product's own sources: 266 variables and 17 runtime-constructed families, covering the engine, the CLI, the Kubernetes operator, the Terraform provider and the connectors. It is regenerated and checked against those sources on every change, so it does not fall behind the binary.

**Required** means the feature that reads the variable does not start without it; most variables are optional and the engine runs with none of them set.

| Variable | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_ACTOR` | No | — | Default `--actor` for the decision-bearing eventing verbs, so a scripted change still records who made it. |
| `OLIVARES_ADMIN_DSN` | No | — | Privileged connection string the Kubernetes operator uses for schema migration, separate from the least-privilege runtime role. |
| `OLIVARES_AGENTCORE_EXPORT_CONFIG` | No | — | Path to the JSON configuration of the AgentCore usage export. |
| `OLIVARES_AGENT_GATEWAY_CONFIG` | No | — | Path to the JSON configuration of the MCP agent gateway. |
| `OLIVARES_ALLOW_CLEARTEXT` | No | — | Dangerous opt-in: lets a request carrying a credential reach a NON-loopback host over plain HTTP, for surfaces with no --allow-cleartext flag of their own. |
| `OLIVARES_API_TOKEN` | No | — | API token the Terraform provider authenticates with, when the provider block does not set one. |
| `OLIVARES_APPROVAL_BRIDGE_CONFIG` | No | — | Path to the JSON configuration of the bridge that routes approvals to an external system. |
| `OLIVARES_AUDIT_ARCHIVE_CONFIG` | No | — | Path to the JSON settings for the `s3archive` sink. Secret-bearing, so it is a file rather than a value. |
| `OLIVARES_AUDIT_ARCHIVE_DIR` | No | — | Root directory for the `dir` archive sink. |
| `OLIVARES_AUDIT_ARCHIVE_INTERVAL` | No | `24h` | How often sealed audit segments are archived, as a Go duration. |
| `OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS` | No | `2555` | How long archived audit segments are retained, in days. |
| `OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS` | No | — | How many events a sealed archive segment holds before the next one is started. |
| `OLIVARES_AUDIT_ARCHIVE_SINK` | No | — | Where sealed audit segments are archived: unset for off, `dir` for a local directory, `s3archive` for object storage. |
| `OLIVARES_AUDIT_LEGALHOLD_INTERVAL` | No | — | How often the long-horizon legal-hold sweep runs, as a Go duration. |
| `OLIVARES_AUDIT_META_BLINDING` | No | — | Whether audit metadata commitments are written blinded, and how strictly that is required. |
| `OLIVARES_AUDIT_SIGNING_KEY` | No | — | Audit checkpoint signing key, inline. Prefer the file form so the key never sits in a process environment. |
| `OLIVARES_AUDIT_SIGNING_KEY_FILE` | No | — | Path to the audit checkpoint signing key. This is the operator-held form. |
| `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the audit signing key wrapped by a key management service, unwrapped at boot. |
| `OLIVARES_AUDIT_SPOOL_MAX_BYTES` | No | — | Upper bound on the on-disk audit spool before the full-spool rule applies. |
| `OLIVARES_AUDIT_SPOOL_ON_FULL` | No | — | What happens when the audit spool is full: the deny-closed posture refuses the write rather than dropping the record. |
| `OLIVARES_AUTHZEN_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to reach the AuthZEN endpoints. Unset leaves the endpoint reachable wherever the listener is. |
| `OLIVARES_AUTHZEN_DISABLED` | No | — | Set to a true value to turn the AuthZEN decision endpoint off. |
| `OLIVARES_AUTHZEN_EXPORT_DISABLED` | No | — | Set to a true value to turn the AuthZEN export endpoint off. |
| `OLIVARES_AUTHZEN_SEARCH_DISABLED` | No | — | Set to a true value to turn the AuthZEN search endpoints off while leaving decisions on. |
| `OLIVARES_BASE_URL` | No | — | Public base URL of this control plane, used where an absolute link back to it has to be produced. |
| `OLIVARES_BUS_CONFIG` | No | — | Path to the JSON configuration of the message bus the engine publishes on. |
| `OLIVARES_CAEP_TRANSMITTER_CONFIG` | No | — | Path to the JSON configuration of the CAEP transmitter that pushes shared-signal events. |
| `OLIVARES_CATALOG_SIGNING_KEY` | No | — | Catalog signing key, inline. Prefer the file form. |
| `OLIVARES_CATALOG_SIGNING_KEY_FILE` | No | — | Path to the catalog signing key. |
| `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the catalog signing key wrapped by a key management service. |
| `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG` | No | — | Path to the JSON configuration of the administrative actuator that applies changes at the model provider. |
| `OLIVARES_CLAUDE_ADMIN_KEY` | No | — | Administrative API key used to read identity posture from the model provider. |
| `OLIVARES_CLAUDE_ERASER_CONFIG` | No | — | Path to the JSON configuration of the erasure actuator that carries out deletion requests. |
| `OLIVARES_CLAUDE_FILES_CONFIG` | No | — | Path to the JSON configuration of the provider file inventory scan. |
| `OLIVARES_CLAUDE_INFERENCE_KEY` | No | — | API key the engine uses for its own inference calls. Unset leaves the inference-backed features off. |
| `OLIVARES_CLAUDE_WORKSPACE_ID` | No | — | Workspace whose identity posture is read, when the administrative key spans several. |
| `OLIVARES_CLI_CONFIG` | No | — | Path to the CLI configuration file, replacing the default per-user location. Used by hermetic automation. |
| `OLIVARES_CLI_TRAMPOLINE` | No | — | Set to `1` inside a re-executed child process so the binary runs the requested subcommand instead of the outer test harness. |
| `OLIVARES_CODEX_HOOK_ACCOUNT` | No | — | Account the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_AGENT` | No | — | Agent identity the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_ORG` | No | — | Organization the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Codex hook enforcement point server. |
| `OLIVARES_CODEX_HOOK_TENANT` | No | — | Tenant the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_TOKEN` | No | — | Token the Codex hook client presents to the enforcement point. |
| `OLIVARES_CODEX_HOOK_URL` | No | — | Base URL of the enforcement point the Codex hook client calls. |
| `OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE` | Yes | — | Path to the JSON keyring the communication content sealer loads at boot (cmd/olivares/boot.go). Secret-bearing, so it is a file rather than a value: sealed message bodies are verified against the keys it carries, and an engine started without it cannot open content sealed by a peer that had one. |
| `OLIVARES_COMMUNICATION_TOKEN` | Yes | — | NOT an operator setting, and documented here precisely so nobody sets it. The engine MINTS this bearer and injects it into a conducted session's child process exactly once (modules/sessions/runtime_bridge.go); its tuple travels inside the authenticated principal. It is RESERVED on the launch path: validateLaunchInjectedEnv (modules/sessions/runtime.go) refuses any launch whose injected environment carries it, so a caller-supplied value is rejected rather than honoured. It appears in the roster because that reserved-name check mentions it, not because the engine reads it. |
| `OLIVARES_CONFIG_STRICT` | No | — | Set to `1` to make `olivares config effective` and `config validate` reject any unrecognized `OLIVARES_*` key. |
| `OLIVARES_CONTEXT_MAX_TOKENS` | No | — | Upper bound on the context a governed session may assemble, in tokens. |
| `OLIVARES_CONTEXT_STRATEGY` | No | — | Which strategy assembles a governed session's context when the bound is reached. |
| `OLIVARES_DATA_DIR` | No | — | Data directory used when `--data-dir` is not given: audit signing key, TLS material and, for SQLite, the store file. |
| `OLIVARES_DB_MAX_CONNS` | No | — | Upper bound on pooled database connections. Unset leaves the driver default. |
| `OLIVARES_DEPLOY_EXECUTOR_CONFIG` | No | — | Path to the JSON configuration of the executor that applies deployment changes. |
| `OLIVARES_DR_KEK_FILE` | No | — | Path to a raw 32-byte key-encryption key for backups, for the path where a key management service does the unwrapping. |
| `OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE` | No | — | Path to the file holding the offsite access key id, so the credential stays out of the environment. |
| `OLIVARES_DR_OFFSITE_BUCKET` | No | — | Offsite bucket for disaster-recovery bundles. Setting it turns offsite replication on. |
| `OLIVARES_DR_OFFSITE_ENDPOINT` | No | — | S3-compatible endpoint for offsite replication. Unset means AWS S3 in the configured region. |
| `OLIVARES_DR_OFFSITE_PREFIX` | No | — | Key prefix for bundles inside the offsite bucket. |
| `OLIVARES_DR_OFFSITE_REGION` | No | — | Region for offsite replication. |
| `OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE` | No | — | Path to the file holding the offsite secret access key. |
| `OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE` | No | — | Path to the file holding an offsite session token, for temporary credentials. |
| `OLIVARES_DR_PASSPHRASE_FILE` | No | — | Path to the backup passphrase file, from which the backup key-encryption key is derived. |
| `OLIVARES_DR_SCHEDULE_INTERVAL` | No | — | How often the scheduled backup runs, as a Go duration. |
| `OLIVARES_DSN` | No | — | Store connection string injected by the Kubernetes operator into the engine it manages. |
| `OLIVARES_DURABLE_BUS_CONFIG` | No | — | Path to the JSON configuration of the durable bus, for at-least-once delivery across replicas. |
| `OLIVARES_EMBEDDINGS_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_BASE_URL` | No | — | Endpoint the openai embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_DIM` | No | — | Vector dimension the openai provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_GEO` | No | — | Region or data-residency hint sent to the openai provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_KEY` | No | — | Api key for the openai embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_MODEL` | No | — | Embedding model requested from the openai provider. |
| `OLIVARES_EMBEDDINGS_PROVIDER` | No | — | Which embeddings provider is used, pinning one instead of taking the first that is configured. |
| `OLIVARES_EMBEDDINGS_REQUIRE` | No | — | Set to a true value to make a missing or unusable embeddings provider a refusal rather than a degraded index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL` | No | — | Endpoint the self-hosted embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM` | No | — | Vector dimension the self-hosted provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO` | No | — | Region or data-residency hint sent to the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY` | No | — | Api key for the self-hosted embeddings provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL` | No | — | Embedding model requested from the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL` | No | — | Endpoint the voyage embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_VOYAGE_DIM` | No | — | Vector dimension the voyage provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_VOYAGE_GEO` | No | — | Region or data-residency hint sent to the voyage provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_KEY` | No | — | Api key for the voyage embeddings provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_MODEL` | No | — | Embedding model requested from the voyage provider. |
| `OLIVARES_ENDPOINT` | No | — | Control-plane base URL the Terraform provider talks to, when the provider block does not set one. |
| `OLIVARES_ENGINE` | No | — | Store engine the Kubernetes operator selects for the engine it manages: `sqlite` or `postgres`. |
| `OLIVARES_EVALS_MONITOR_WINDOW` | No | — | Time window the evaluation monitor scores, as a Go duration. |
| `OLIVARES_EVENTING_ALLOW_LOOPBACK` | No | — | Set to a true value to allow loopback destinations. Single-box development only, because the default refusal is what blocks server-side request forgery. |
| `OLIVARES_EVENTING_DISPATCH_INTERVAL` | No | `15s` | How often queued events are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_EVENTING_EGRESS_POLICY` | No | — | Path to the JSON policy that decides which destinations outbound events may reach. A policy that does not parse leaves eventing unwired rather than open. |
| `OLIVARES_EVENTING_RETENTION` | No | `168h` | How long delivered events are kept for replay, as a Go duration. |
| `OLIVARES_EVENTING_SECRET_KEY` | No | — | Key that encrypts eventing subscription signing secrets at rest. |
| `OLIVARES_EXTRA_ARGS` | No | — | Extra `serve` arguments appended by the packaged service unit, for operators who configure the daemon through an environment file. |
| `OLIVARES_GROK_HOOK_ACCOUNT` | No | — | Account the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_AGENT` | No | — | Agent identity the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_ORG` | No | — | Organization the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Grok Build hook enforcement point server. Absent mounts nothing; a path given must be readable and valid or startup fails closed. |
| `OLIVARES_GROK_HOOK_TENANT` | No | — | Tenant the Grok Build hook client acts in. |
| `OLIVARES_GROK_HOOK_TOKEN` | No | — | Bearer credential the Grok Build hook client presents to the enforcement point. |
| `OLIVARES_GROK_HOOK_URL` | No | — | Endpoint of the enforcement point the Grok Build hook client calls; unset denies, deny-closed. |
| `OLIVARES_GUARDIAN_SWEEP_INTERVAL` | No | — | How often the guardian sweep runs, as a Go duration. `0` disables it. |
| `OLIVARES_HA_LEADER_GATE` | No | — | Set to `1` to make background loops run only on the elected leader, so a multi-replica deployment does not run them twice. |
| `OLIVARES_HA_LEADER_LABEL` | No | — | Label this replica publishes when it holds leadership, so an operator can route to the leader. |
| `OLIVARES_HITL_CONFIG` | No | — | Path to the JSON configuration of the human-in-the-loop review path. |
| `OLIVARES_HOOK_FIREWALL_CONFIG` | No | — | Path to the JSON configuration of the data-loss firewall that runs inside the hook. Unset leaves that half off. |
| `OLIVARES_HOOK_PEP_ACCOUNT` | No | — | Account the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_AGENT` | No | — | Agent identity the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the hook enforcement point server. |
| `OLIVARES_HOOK_PEP_ORG` | No | — | Organization the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TENANT` | No | — | Tenant the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TOKEN` | No | — | Token the Claude Code hook client presents to the enforcement point. |
| `OLIVARES_HOOK_PEP_URL` | No | — | Base URL of the enforcement point the Claude Code hook client calls. |
| `OLIVARES_INCIDENTLOOP_CONFIG` | No | — | Path to the JSON configuration of the incident close-the-loop subscriber. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_INFERENCE_PROXY_CONFIG` | No | — | Path to the JSON configuration of the governed inference proxy. |
| `OLIVARES_INGEST_TOKEN` | No | — | Bearer token the collector ingest endpoint requires from telemetry senders. |
| `OLIVARES_INSECURE` | No | — | Set to `1` to let the CLI talk to a plaintext or untrusted-TLS endpoint. Local development only. |
| `OLIVARES_KEY_CUSTODY` | No | — | Custody posture required of the audit signing key: whether a raw on-disk key is accepted or a wrapped one is demanded. |
| `OLIVARES_KEY_WRAP_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_OLD` | No | — | Previous key management backend during a rewrap migration, so keys wrapped by it can still be unwrapped. |
| `OLIVARES_LEDGER_CUSTODY` | No | — | Custody posture required of the ledger checkpoint signer, the ledger counterpart of the audit key posture. |
| `OLIVARES_LEDGER_KMS_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG` | No | — | Signing algorithm requested from AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_SIGNER` | No | — | Off-box checkpoint signer to use: which key management backend signs audit checkpoints instead of a local key. |
| `OLIVARES_LICENSE` | No | — | License document itself, inline, for deployments that cannot mount a file. |
| `OLIVARES_LICENSE_PATH` | No | — | Path to the license document on disk. Takes effect before the inline form. |
| `OLIVARES_LICENSE_PUBKEY` | No | — | Public key the engine verifies the license signature against. |
| `OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS` | No | — | Set to `1` to make live ingest inspect observed references, which costs more per event. |
| `OLIVARES_LOG_LEVEL` | No | — | Minimum log level the engine emits: `debug`, `info`, `warn` or `error`. |
| `OLIVARES_MCP_TASK_KILLSWITCH_SWEEP` | No | — | How often a running MCP task is re-checked against the kill switch, as a Go duration. |
| `OLIVARES_METRICS_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to scrape the metrics endpoint. |
| `OLIVARES_METRICS_TOKEN` | No | — | Bearer token the metrics endpoint requires. Unset leaves the endpoint unauthenticated behind whatever the listener exposes. |
| `OLIVARES_NHI_ACTUATORS_CONFIG` | No | — | Path to the JSON configuration of the actuators that act on non-human identities. |
| `OLIVARES_NIS2INCIDENT_CONFIG` | No | — | Path to the JSON configuration of NIS2 incident reporting. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_NOTIFY_CONFIG` | No | — | Path to the JSON list of notification destinations. Secret-bearing, so it stays out of the store. |
| `OLIVARES_NOTIFY_DISPATCH_INTERVAL` | No | — | How often queued notifications are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_OIDC_CLIENT_ID` | Yes | — | OIDC client id for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_CLIENT_SECRET` | Yes | — | OIDC client secret for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_GROUPS_CLAIM` | No | — | ID-token or UserInfo claim carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_OIDC_ISSUER` | Yes | — | OIDC issuer URL. Required when the protocol is `oidc`. |
| `OLIVARES_ORCH_CADENCE_INTERVAL` | No | — | How often the orchestration cadence loop runs, as a Go duration. `0` disables it. |
| `OLIVARES_ORCH_DISPATCH_CONFIG` | No | — | Path to the JSON configuration for orchestration dispatch targets. |
| `OLIVARES_ORCH_WORKFLOW_INTERVAL` | No | `15s` | How often the orchestration workflow loop advances waiting runs, as a Go duration. |
| `OLIVARES_ORCH_WORKFLOW_MAX` | No | — | Upper bound on concurrently advancing workflow runs. |
| `OLIVARES_ORCH_WORKFLOW_STEPS_MAX` | No | — | Upper bound on the steps one workflow run may take, which stops a loop from running forever. |
| `OLIVARES_OTA_PUBKEY` | No | — | Public key the engine verifies a downloaded update bundle against. |
| `OLIVARES_OTEL_ENABLED` | No | — | Set to a true value to export traces. Setting an endpoint turns export on as well. |
| `OLIVARES_OTEL_ENDPOINT` | No | — | OTLP endpoint traces are exported to. Falls back to the standard `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `OLIVARES_OTEL_GENAI_COMPAT` | No | — | Set to a true value to also emit the generative-AI semantic-convention attributes on spans. |
| `OLIVARES_OTEL_INSECURE` | No | — | Set to a true value to export traces over plaintext. Local development only. |
| `OLIVARES_OTEL_PROTOCOL` | No | — | OTLP protocol used for export. Falls back to the standard `OTEL_EXPORTER_OTLP_PROTOCOL`. |
| `OLIVARES_OTEL_SAMPLE_RATIO` | No | — | Fraction of traces sampled, between 0 and 1. |
| `OLIVARES_OTEL_SERVICE_NAME` | No | — | Service name reported on exported traces. |
| `OLIVARES_PDP_CEDAR_FILE` | No | — | Path to the Cedar policy file, for the `cedar` decision point. |
| `OLIVARES_PDP_ENGINE` | No | — | External policy decision point to add on top of the native engine: `cedar`, `opa` or `none`. |
| `OLIVARES_PDP_OPA_PATH` | No | — | Decision path queried under the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | No | — | Bearer token for the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_URL` | No | — | Base URL of the Open Policy Agent endpoint, for the `opa` decision point. |
| `OLIVARES_PIV_CONFIG` | No | — | Path to the JSON configuration for smart-card privileged login. |
| `OLIVARES_PLUGIN` | No | — | Handshake cookie an out-of-process connector plugin must present. Set by the engine when it launches the plugin, not by the operator. |
| `OLIVARES_POLICY_MAX_STALENESS` | No | — | How stale a cached policy decision may be before it is refused, as a Go duration. |
| `OLIVARES_POLICY_SIGNING_KEY` | No | — | Policy bundle signing key, inline. Prefer the file form. |
| `OLIVARES_POLICY_SIGNING_KEY_FILE` | No | — | Path to the policy bundle signing key. |
| `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the policy signing key wrapped by a key management service. |
| `OLIVARES_RATELIMIT_CONFIG` | No | — | Path to the JSON rate-limit policy the engine applies to its own endpoints. |
| `OLIVARES_RATELIMIT_STORE` | No | — | Where rate-limit counters live, which decides whether limits are per replica or shared. |
| `OLIVARES_REPORTING_CONFIG` | No | — | Path to the JSON configuration of the reporting add-on. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_REPORTING_SCHEDULE_INTERVAL` | No | — | How often scheduled reports are generated, as a Go duration. |
| `OLIVARES_REPORT_CACHE_DIR` | No | — | Directory where generated report artifacts are cached. |
| `OLIVARES_RETENTION_SWEEP_INTERVAL` | No | — | How often the retention sweep deletes data past its retention window, as a Go duration. |
| `OLIVARES_SAML_ACS_URL` | Yes | — | Assertion consumer service URL of this service provider, where the identity provider posts the assertion. |
| `OLIVARES_SAML_EMAIL_ATTRIBUTE` | No | — | Assertion attribute carrying the user's email. Unset tries the common attribute names. |
| `OLIVARES_SAML_GROUPS_ATTRIBUTE` | No | — | Multi-valued assertion attribute carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_SAML_IDP_METADATA_URL` | No | — | Identity-provider metadata URL, from which the SAML endpoints and certificate are read. |
| `OLIVARES_SAML_IDP_SSO_URL` | No | — | Identity-provider single sign-on URL, for the path where metadata is not fetched. |
| `OLIVARES_SAML_SP_CERT_PEM` | No | — | Service-provider encryption certificate in PEM, published as the encryption key descriptor. |
| `OLIVARES_SAML_SP_ENTITY_ID` | Yes | — | Entity id this control plane presents as the SAML service provider. Required when the protocol is `saml`. |
| `OLIVARES_SAML_SP_KEY_PEM` | No | — | Service-provider encryption private key in PEM, which decrypts encrypted assertions. |
| `OLIVARES_SAML_SP_SIGN_CERT_PEM` | No | — | Service-provider signing certificate in PEM, published as the signing key descriptor. |
| `OLIVARES_SAML_SP_SIGN_KEY_PEM` | No | — | Service-provider signing private key in PEM, which signs authentication requests. |
| `OLIVARES_SANDBOX_RUNTIME_CONFIG` | No | — | Path to the JSON configuration of the sandbox runtime that isolates agent execution. |
| `OLIVARES_SECRETREF_AWS_REGION` | No | — | Region used for AWS Secrets Manager references. Falls back to `AWS_REGION` and `AWS_DEFAULT_REGION`. |
| `OLIVARES_SECRETREF_AZURE_API_VERSION` | No | — | Azure Key Vault API version requested. |
| `OLIVARES_SECRETREF_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. |
| `OLIVARES_SECRETREF_AZURE_VAULT_URL` | No | — | Default Azure Key Vault URL for references that do not name one. |
| `OLIVARES_SECRETREF_GCP_ENDPOINT` | No | — | Endpoint override for Google Secret Manager. |
| `OLIVARES_SECRETREF_GCP_PROJECT` | No | — | Default Google Cloud project for Secret Manager references that do not name one. |
| `OLIVARES_SECRETREF_GCP_TOKEN` | No | — | Token used against Google Secret Manager. |
| `OLIVARES_SECRETREF_INFISICAL_ENV` | No | — | Default Infisical environment for references that do not name one. |
| `OLIVARES_SECRETREF_INFISICAL_TOKEN` | No | — | Token used against Infisical. |
| `OLIVARES_SECRETREF_INFISICAL_URL` | No | — | Base URL of the Infisical server secret references resolve against. |
| `OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID` | No | — | Default Infisical workspace for references that do not name one. |
| `OLIVARES_SECRETREF_K8S_APISERVER` | No | — | Kubernetes API server secret references resolve against. |
| `OLIVARES_SECRETREF_K8S_CA_FILE` | No | — | Path to the certificate authority bundle used to verify the Kubernetes API. |
| `OLIVARES_SECRETREF_K8S_TOKEN_FILE` | No | — | Path to the service-account token file used against the Kubernetes API. |
| `OLIVARES_SECRETREF_VAULT_ADDR` | No | — | Address of the HashiCorp Vault server secret references resolve against. Falls back to `VAULT_ADDR`. |
| `OLIVARES_SECRETREF_VAULT_NAMESPACE` | No | — | Vault namespace secret references resolve in. Falls back to `VAULT_NAMESPACE`. |
| `OLIVARES_SECRETREF_VAULT_TOKEN` | No | — | Token used against HashiCorp Vault. |
| `OLIVARES_SECRET_STORE_KEY` | No | — | Key that encrypts operator secrets held in the store. |
| `OLIVARES_SERVER_URL` | No | — | Base URL of the control plane the CLI talks to, when `--server` is not given. |
| `OLIVARES_SESSION_BUDGET_AVAILABILITY` | No | — | Whether session budget enforcement is required, and what happens when the budget service cannot answer. |
| `OLIVARES_SESSION_CONTEXT_AVAILABILITY` | No | — | Whether session context governance is required, and what happens when the context service cannot answer. |
| `OLIVARES_SESSION_KILLSWITCH_SWEEP` | No | `15s` | How often an active session is re-checked against the kill switch, as a Go duration. `0` leaves only the check at launch. |
| `OLIVARES_SESSION_PEP_TOKEN_FILE` | No | — | Path to the file holding the token the session enforcement point requires. |
| `OLIVARES_SESSION_PEP_URL` | No | — | Base URL of the policy enforcement point a governed agent session calls before acting. |
| `OLIVARES_SESSION_RUNTIME_BASE_URL` | No | — | Base URL the launched session runtime calls back to. |
| `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | No | `claude` | Executable the session runtime launches. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` | No | — | Path to the file holding the session runtime's credential, refreshed by rotation. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_TTL` | No | `15m` | Lifetime of a minted session runtime credential, as a Go duration. |
| `OLIVARES_SESSION_RUNTIME_WIF` | No | — | Whether the session runtime takes its credential from workload identity federation instead of a token file. |
| `OLIVARES_SESSION_RUNTIME_WIF_RULE` | No | — | Which federation rule the session runtime exchanges its workload identity under. |
| `OLIVARES_SIEM_FORWARD_INTERVAL` | No | — | How often signed ledger records are forwarded to the configured SIEM, as a Go duration. |
| `OLIVARES_SOURCES_CONFIG` | No | — | Path to the JSON file that wires real observation sources and identity roster providers before the engine starts. |
| `OLIVARES_SSO_PROTOCOL` | No | — | Single sign-on protocol to wire: `oidc` or `saml`. Unset means no federation, and the endpoints report it rather than half-wiring one. |
| `OLIVARES_SSO_SECRET_KEY` | No | — | Key that encrypts the federation client secret and service-provider private keys at rest. |
| `OLIVARES_TARGET_BINDING_KEY` | No | — | Key that binds an orchestration target to this deployment, inline. Prefer the file form. |
| `OLIVARES_TARGET_BINDING_KEY_FILE` | No | — | Path to the orchestration target binding key. |
| `OLIVARES_TENANT` | No | — | Default tenant for CLI commands, when `--tenant` is not given. |
| `OLIVARES_THREATINTEL_CONFIG` | No | — | Path to the JSON configuration of threat-intelligence ingest. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_THREATINTEL_SIGNING_KEY` | No | — | Signing key for threat-intelligence bundles the engine publishes. |
| `OLIVARES_TOKEN` | No | — | API token the CLI authenticates with, when `--token` is not given. |
| `OLIVARES_UPDATE_CHANNEL` | No | — | Release channel the update check asks for, such as `stable`. |
| `OLIVARES_UPDATE_ENDPOINT` | No | — | Base URL the update check queries. Unset leaves the update check off. |
| `OLIVARES_UPGRADE_TOKEN` | No | — | Download token `olivares upgrade` presents when fetching a build from a credentialed repository. |
| `OLIVARES_VECTOR_API_KEY` | No | — | API key for the external vector index. |
| `OLIVARES_VECTOR_BACKEND` | No | — | Which vector index backs knowledge search. Unset keeps the in-process index, which is the air-gapped default. |
| `OLIVARES_VECTOR_DIM` | No | — | Vector dimension of the index, which has to match the embeddings model. |
| `OLIVARES_VECTOR_DSN` | No | — | Connection string for the external vector index. |
| `OLIVARES_VECTOR_NAMESPACE` | No | `knowledge_ann` | Table or collection the vector index writes to. |
| `OLIVARES_VECTOR_TIMEOUT` | No | — | Per-request timeout for the vector index, as a Go duration. |
| `OLIVARES_VOICE_CALL_CONFIG` | No | — | Path to the JSON configuration of the inbound voice webhook. |
| `OLIVARES_VOICE_DISPATCH_CONFIG` | No | — | Path to the JSON configuration of outbound voice dispatch. |
| `OLIVARES_WEBAUTHN_ORIGINS` | No | — | Comma-separated origins accepted for WebAuthn ceremonies. |
| `OLIVARES_WEBAUTHN_RPID` | No | — | WebAuthn relying-party id for the privileged-login flow. It has to match the site's registrable domain. |
| `OLIVARES_WEBAUTHN_RP_NAME` | No | — | Display name of the WebAuthn relying party, as shown by the authenticator. |
| `OLIVARES_WIF_BASE_URL` | No | — | Endpoint the workload identity exchange is performed against. |
| `OLIVARES_WIF_REFRESH_SLACK` | No | `60s` | How long before expiry a federated credential is refreshed, as a Go duration. |
| `OLIVARES_WIF_SPIFFE_SOCKET` | No | — | Path to the SPIFFE workload API socket the engine fetches its identity from. |
| `OLIVARES_WIF_TRUST_DOMAIN` | No | — | SPIFFE trust domain accepted for workload identity. |
| `OLIVARES_WORK_OUTBOX_INTERVAL` | No | — | How often the work-kernel outbox is drained, as a Go duration. `0` disables the pump. |
| `OLIVARES_WORK_RUN_REF` | No | — | Run reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_SESSION_ID` | No | — | Session reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_TOKEN` | No | — | Scoped token the engine passes to a launched work session. Set by the engine per run, not by the operator. |

### Variable families

These prefixes name families whose member variables are built at runtime — the per-provider and per-backend keys the engine composes from a provider name. The concrete members it composes are in the table above.

| Prefix | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_AUDIT_ARCHIVE_` | No | — | Family prefix for the audit archive settings listed above. |
| `OLIVARES_CODEX_HOOK_` | No | — | Family prefix for the Codex hook client and server settings listed above. |
| `OLIVARES_DR_OFFSITE_` | No | — | Family prefix for the offsite replication settings listed above. |
| `OLIVARES_EMBEDDINGS` | No | — | Family stem for the unprefixed embeddings settings, which configure the OpenAI-compatible provider. |
| `OLIVARES_EMBEDDINGS_` | No | — | Family prefix from which the per-provider embeddings keys are built, by appending the provider name and then the setting. |
| `OLIVARES_GROK_HOOK_` | No | — | Family prefix for the Grok Build hook client and server settings listed above. |
| `OLIVARES_HOOK_PEP_` | No | — | Family prefix for the Claude Code hook client and server settings listed above. |
| `OLIVARES_KEY_WRAP` | No | — | Family stem naming the key management backend that wraps signing keys. |
| `OLIVARES_KEY_WRAP_` | No | — | Family prefix from which the per-backend key-wrapping keys are built. |
| `OLIVARES_LEDGER_KMS_` | No | — | Family prefix from which the per-backend ledger signer keys are built. |
| `OLIVARES_OIDC_` | No | — | Family prefix for the OIDC federation settings listed above. |
| `OLIVARES_OTEL_` | No | — | Family prefix for the trace export settings listed above. |
| `OLIVARES_SAML_` | No | — | Family prefix for the SAML federation settings listed above. |
| `OLIVARES_SESSION_RUNTIME_` | No | — | Family prefix for the session runtime settings listed above. |
| `OLIVARES_VECTOR_` | No | — | Family prefix for the vector index settings listed above. |
| `OLIVARES_WIF_` | No | — | Family prefix for the workload identity federation settings listed above. |
| `OLIVARES_WORK_` | No | — | Family prefix for the per-run values the engine passes into a launched work session. |

<!-- END GENERATED olivares-env-reference -->

## ストア選択

エンジンは `--engine` からストアエンジンを選択します。

| エンジン | 使うべき場面 | 備考 |
| --- | --- | --- |
| `sqlite`（デフォルト） | 単一バイナリ、単一ノード、エアギャップ環境のインストール。 | 純 Go の組み込みストア。外部依存ゼロ。`--dsn` がない場合、ストアファイルはデータディレクトリに置かれます。 |
| `postgres` | マルチテナントおよびスケールアウトのデプロイメント。 | 行レベルセキュリティのテナント分離を追加します。最小権限のアプリケーションロールが必要です。 |

SQLite はデフォルトであり、外部サービスを必要としません。`postgres` を選ぶと、テナントを分離する行レベルセキュリティの最後の砦をオプトインします：エンジンは、Postgres スーパーユーザーまたは `BYPASSRLS` ロールに対しては、そのガードが明示的にオーバーライドされない限り **起動を拒否** します。なぜなら、そのようなロールはテナント分離の最後の砦を無効化してしまうからです。Compose の Postgres オーバーレイは初回 init 時に最小権限のアプリケーションロールをプロビジョニングするため、この最後の砦は本物です。

:::tip[デフォルトのストアは意図的に退屈である]
SQLite はここでおもちゃのデフォルトではありません。それは単一ノードトポロジのためのエアギャップ対応・依存ゼロのストアであり、ワンコマンドの Docker Compose デプロイメントが実行するストアです。マルチテナント分離や水平スケールが必要になったとき、それより前ではなく、そのとき Postgres に移行してください。[セルフホスティング](/ja/how-to/self-hosting/) と [アーキテクチャ概要](/ja/explanation/architecture/overview/) を参照してください。
:::

## 監査チェックポイント間隔

監査台帳（audit ledger）は追記専用、ハッシュチェーン化（hash-chained）されており、Ed25519 署名されたチェックポイントによって固定されます。`--checkpoint-interval` は、すべてのテナントチェーンにわたって署名済みチェックポイントが書き込まれる頻度を制御します（デフォルト `1h`、`0` でチェックポイント作成を無効化）。ストアが閉じる前に最終シャットダウンチェックポイントが書き込まれるため、チェーンは間隔のときだけでなくクリーンシャットダウン時にも固定されます。署名済みエクスポートと転送のパスは [監査を Splunk に転送する](/ja/how-to/forward-audit-to-splunk/) でカバーされています。

## セキュアなデフォルト

これらは、`serve` を超える設定がない状態で有効になる姿勢です。これらは製品のデフォルトのセキュリティスタンスであり、オプションの堅牢化ではありません。

| 領域 | デフォルト | 意味するところ |
| --- | --- | --- |
| 認証情報 | 出荷なし | デフォルトのユーザー名やパスワードは存在しません。ユーザーのいない初回起動時、エンジンは単回使用のセットアップトークンを発行し、それを標準出力のみに出力します — ログには決して出力しません。 |
| 初回起動セットアップ | ワンタイムトークン | 管理者はそのトークンで最初のユーザーを作成し、その後ログインします。トークンは一度だけ表示され、単回使用です。 |
| トランスポート | TLS オン | HTTP と gRPC はデフォルトで TLS 越しに提供されます。何も供給されない場合は自己署名証明書がデータディレクトリに生成され、その証明書フィンガープリントと `--pin-sha256` の値の両方がログに記録されます。 |
| バインドアドレス | Loopback | `--listen` と `--grpc-listen` はデフォルトで `127.0.0.1` です。エンジンは、あなたが意図的に公開するまでローカルホストにバインドします。 |
| 平文モード | オフ | `--insecure` が平文を提供する唯一の方法であり、gRPC のパスは劣化するのではなく fail closed します。ローカルホスト開発専用に意図されています。 |
| デモシード | オフ | `--seed-demo` はデフォルトでオフであり、公開パスワードのデモ管理者を発行するため、いかなる非 loopback バインドも拒否します。 |
| テレメトリのホームコール | オフ | エンジンはホームに電話をかけません。ベンダーへのテレメトリチャネルは存在せず、稼働の副作用として何かが送信されることもありません。アウトバウンド接続は、あなたが設定したソースに対して、および実行したときの `olivares upgrade` に対して存在します（`--endpoint` や `--bundle` で他を指さない限り更新チャネルに接続します）。これが、egress ゼロでの [エアギャップインストール](/ja/how-to/air-gap-install/) を可能にするものです。 |

:::caution[デフォルトで loopback、意図して公開]
デフォルトの loopback バインドは、あなたが変更するまでエンジンがホスト外から到達不能であることを意味します。それを公開するとき — 例えば Docker Compose でホストポートをマッピングするとき — それは意図的なオペレータの決定であり、それを保護するために TLS はすでにオンになっています。公開されたバインドを `--insecure` と組み合わせないでください。
:::

### 初回起動、実際には

新規インストールでは、エンジンはワンタイムのセットアップトークンを含む `FIRST-BOOT SETUP` ブロックを標準出力に出力します。管理者はそれを使って最初のユーザーを作成し、その後認証します。Docker Compose の下では、トークンはコンテナログから読み取られます：

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# then open https://localhost:8443 (self-signed TLS by default)
```

セットアップエンドポイントとログインエンドポイントは製品の OpenAPI 契約の一部です。[API リファレンス](/reference/api/) を参照してください。それらの背後にある不透明（opaque）なセッションおよび API キートークンモデルは、[セキュリティモデル](/ja/explanation/security/security-model/) で説明されています。

## このページがカバーしないこと

これは検証済みの共通設定サーフェスです。マルチノードおよび相互 TLS トポロジのためのすべての高度なフラグを列挙するものでは **ありません** — それらは、[アーキテクチャ概要](/ja/explanation/architecture/overview/) で説明され [CLI リファレンス](/ja/reference/cli/) に完全に列挙される、分散およびエアギャップのデプロイメントに属します。設定が設計段階またはトポロジ固有のものである場合、それは安定したつまみとしてここに提示されるのではなく、そちらで文書化されます。

製品が観測する範囲の境界と、カバレッジがどこでティア分けされるかについては、[正直さと限界](/ja/start/honesty-and-limits/) を読んでください。
