---
title: セッションランタイム API（公式 CLI）
description: >-
  所有する Claude Code、Codex、Grok CLI プロセスを一覧・接続・入力・停止する Community HTTP 面。
  権限、PTY、再開、再接続。
---

コントロールプレーンは **ベンダー CLI を起動する**。Claude Code、Codex、Grok Build を
置き換えない。セッションと端末はこの製品の一モジュールであって製品そのものでは
ない。

このページは `/v1/m/sessions/runs` 下の Community operate ルートを文書化する。
ルートは既に [beta OpenAPI](/reference/api-beta/) にある。26.10 はドライバ契約、
ローカル PTY ランナー、旅程 J01–J08 をテストとして追加する。

## エディション境界

| エディション | 行うこと | 行わないこと |
|---|---|---|
| **Community（本ページ）** | 所有するローカル子プロセス：起動、stdin/stdout/stderr、カーソル付き attach、正確な会話の resume、生きているストリームの reconnect、観測した終了ステータスでの stop。セッション行と証拠はモジュール II に残る。 | Identity & Scale の複数ペインエンジン、mTLS リスナー、コックピットのペイン入力、xterm UI チャンク |
| **Identity & Scale オーバーレイ** | 商用 session-cockpit エンジン（リスナー、ペイン、台帳）。モジュールがあるとき `/v1/m/session-cockpit/` 下に載る。 | `/v1/m/sessions/runs` を置き換えない |

Community ビルドはオーバーレイ名前空間に **不在**（404）で答える。501 スタブは
載せない。

Claude Code の hook 経路は `olivares claude-hook`（PreToolUse PEP）のまま。
観測と執行であり、代替 CLI ではない。

## 権限

モジュール経路上の既存の執行点：

| 権限 | 経路 |
|---|---|
| `sessions:run:read` | `GET /runs`、`GET /runs/{ref}`、`GET /runs/{ref}/events`、`GET /runs/{ref}/attach`、`GET /runs/{ref}/handoff`、`GET /runs/{ref}/activity`、`GET /runs/{ref}/activity/agents/{agent}`、`GET /runs/{ref}/messages` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/stop`、`POST /runs/{ref}/resume`、`POST /runs/{ref}/move`、`POST /runs/{ref}/handoff` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`、`DELETE /runs/{ref}` |

viewer は一覧できる。作成、入力、停止は write が要る。経路の authorizer が
執行点である。権限が無ければ 403。

## コンソールが読む経路

基点：`/v1/m/sessions`。認証する。`X-Olivares-Tenant` を送る。

| メソッド | 経路 | 結果 |
|---|---|---|
| `GET` | `/runs` | 管理実行のページ |
| `GET` | `/runs/{ref}` | 一件。`state` は導出。`exit_code` は観測。成功を捏造する欄は無い |
| `GET` | `/runs/{ref}/events` | ライフサイクル証拠行 |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE：`output` フレーム、リングがカーソル未満を追い出したときの `lag`、`end` または非ライブ `notice` |
| `POST` | `/runs/{ref}/input` | stdin。stream-json は `line`/`message`。Codex/Grok は `text`。202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | プロセスグループへ SIGTERM、次に SIGKILL。行に観測した終了ステータス |
| `POST` | `/runs/{ref}/resume` | 新しいプロセス世代。保存した正確な会話 |
| `POST` | `/runs/{ref}/interrupt` | 活動中のターンを取り消す。プロセスは残る |
| `POST` | `/runs/{ref}/move` | 本文 `{provider_profile_ref}`。停止または失敗した Claude Code セッションを、同じツールの別のプロファイルに付け替え、その会話を引き継ぐ。何も開始しない。200 で run |
| `GET` | `/runs/{ref}/handoff` | ハンドオフの開始点になる要約：`summary`、`source_driver`、`turns`、`transcript_bytes`。読み取り専用で、監査台帳に記録される。実行中のセッションはそのままの状態で読み取られ、まだ開始していないセッションは 409 を返す |
| `POST` | `/runs/{ref}/handoff` | 本文 `{provider_profile_ref, summary?, name?}`。そのプロファイルで、このセッションに関連付けた新しいセッションを開始する。201 で run と `summary_state`（`delivered` 以外のときは `first_turn` も）。起動が承認待ちの間は 202 |
| `GET` | `/runs/{ref}/activity` | Claude Code または Codex セッションのツールが何をしているか。リクエストのたびにそのツール自身のネイティブな記録から読み取る：`agents`、`tasks`（コマンドとバックグラウンド作業）、`files`（path、`added`、`removed`、`created`）、`todos`、`skills`、`mcp_servers`、`model`、`context_tokens`、ツールが報告したときの null 許容の `context_window`、`context_percent`（Claude Code ではパーセンテージ、不明なときは null）、`compactions`、`transcript`（`read`、ツールがまだ書き込む前の `pending`、あるはずなのにない場合の `missing`）。agents と tasks の状態は `running`、`completed`、`failed`、`stopped`、`scheduled`。Codex は別の app-server を起動せずに rollout ファイルを使う。context tokens は累積使用量ではなく最新のリクエストを表す。読み取り専用で、読み手とセッションごとに 15 分に 1 回、監査台帳に記録される。未対応のツールは 422 を返す。vault シークレットまたはリポジトリ認証情報を与えられたセッションは 409 を返す。パッケージレジストリトークンを使ったセッションも、停止しているとき、またはそのトークンを使った起動の後の再開を経て実行されているときは 409 を返す |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | Claude Code のサブエージェント 1 つのトランスクリプトを、平文のターン（`role`、`text`）で返す。読み取り専用で、監査台帳に記録される。未知のエージェントは 404。Codex は子の状態をルートの Activity の応答で報告し、子のトランスクリプトの読み取りは提供しない |
| `GET` | `/runs/{ref}/messages` | Claude Code または Codex セッションのメッセージを、リクエストのたびにツール自身のトランスクリプトから読み取り、ページ単位で返す：`items`（`id`、`role` は `user` または `assistant`、`at`、`first_line`）、`total`、`next`、`transcript`（`read`、`pending`、`missing`）。既定では人が送信したものだけで、`role=all` でツールの回答も加わる。`q` はそのフレーズをどこかに含むメッセージに絞り込む（大文字小文字を区別しない、最大 256 バイト）。`at`（RFC 3339）はその時刻以降のメッセージに絞り込むため、最初の項目が基点になる。`order` は `asc`（既定）または `desc`、`limit` は最大 200、`cursor` はページの続きの起点となる id（`next`）。項目は常に古い順。`id` は 1 件のメッセージ全体を読む（`text`、および `later_same`：同じ送信者の後続メッセージのうち、その最初の行を繰り返すものの数。フィルターは適用されず、`total` はセッションのメッセージ数）。メッセージの `id` はセッションのメッセージ内での位置で、トランスクリプトが伸びても変わらない。読み取り専用で、読み手、セッション、15 分の窓ごとに 1 回、監査台帳に記録される。未対応のツールは 422、vault シークレットまたはリポジトリ認証情報を与えられたセッションは 409、未知の `id` は 404、無効なパラメーターは 400 を返す |
| `GET` | `/runs/{ref}/diff` | ワークツリーセッションのブランチ差分：`branch`、`base`、`head`、変更された `files`（`path`、`status`）。ワークツリーなしでは 404 |
| `GET` | `/runs/{ref}/diff/file?path=` | `base` と `head` における 1 パスのテキスト（`original`、`modified`、各々ワークスペースの `max_read_bytes` と 64 KiB の小さい方が上限） |

プラン引数が rollout に保持されない Codex の Code Mode 呼び出しについては、Activity はライブブリッジがすでに受信した最新の `turn/plan/updated` 通知も読み取ります。ブラウザーを再読み込みしても同じサーバー側セッションを使い、更新のために app-server は起動されません。ライブハンドルがなくなった後は、Codex 自身が保持したデータだけを読み取れます。

Claude Code では、ウィンドウはネイティブの result フレームの `modelUsage[model].contextWindow` から取得され、停止や再接続をまたいで保持されます。一致する曖昧さのない報告があれば、`context_percent` は `100 × context_tokens / context_window` になります。そうでなければ両方のフィールドが `null` になり、CLI とコンソールはウィンドウ不明の注記とともにトークン数を表示します。ツールを新たに起動すると、以前のウィンドウの報告は消去されます。

ワークフローのタスクには `workflow` が含まれることがあります：`run_id`、`name`、`duration_ms`、`agents_total`、`agents_done`、`agents_failed`、`phases`。各フェーズは `index`、`title`、任意の `detail`、`state`、および同じエージェント数を持ちます。数は、これまでに観測されたエージェントを表します。ライブの間、フェーズの state は観測された実行中または失敗したエージェントを表します。エージェントの完了は、スクリプトがそのフェーズを離れたことを証明しないため、フェーズを完了とは記録せず、現在のスクリプトのステップも特定しません。完了したフェーズの state は、最終的なワークフローの要約が与えます。

ライブのワークフロースナップショットは、Claude Code の `system/task_progress` 出力から得られ、attach ストリームと共有されます。ツールの完了した `workflows/<runId>.json` が、トランスクリプトのホーム、リンク、サイズの規則のもとで最終的な要約を埋めます。要約の読み取りはファイルごとに 8 MiB、リクエストごとに 64 MiB が上限で、それより大きいネイティブのログや結果は、利用できるライブの進捗を保ったまま要約を読み取り不能にします。読み取り不能な要約と、上限のあるライブバッファーの欠落は、`unreadable` に寄与します。

切断した attach の再接続は **同じ** 生きているプロセスへの
`GET …/attach?from={last+1}`。プロセス喪失の後、attach はライブでないと述べる。
Resume は新しい世代を始める。Reconnect は代替プロセスを捏造しない（SDD R04）。

### セッションの移動とハンドオフ

セッションは 2 つの方法のいずれかで別のアカウントで続行でき、コンソールと
`olivares session move` / `olivares session handoff` の両方がそれらを提供します。
`move` は Claude Code セッションの保存済みの会話を同じツールの別のプロファイルへ運ぶため、
`move` できるのは Claude Code のセッションだけです。`handoff` は、Claude Code、Codex、Gemini CLI、
OpenCode のいずれかのセッションの会話から新しいセッションを開始します。

実行は、そのツールが直近のターンを上限で終えたとき、`limit`
（`{kind, resets_at, account_ref}`）を持ちます。`kind` は `usage`（アカウントのプランの
許容量を使い切った）または `rate`（プロバイダーがリクエストを制限している）です。
`resets_at` は RFC 3339 で、ツールが上限の解除時刻を報告したときにあり、報告しないときは
ありません。`account_ref` はそのターンが実行されたプロファイルです。Claude Code は、
その事実を自身の結果（`api_error: usage_limit_reached` または HTTP 429）で、
リセット時刻を `rate_limit_event` で提供します。Codex は、その事実を `usageLimitExceeded` または
`rateLimitExceeded` の型を持つ失敗したターンで、リセット時刻を `account/rateLimits/updated` の
ウィンドウで提供します。2 つのウィンドウを使い切っている場合は、より遅いリセットが記録され、
現在から 1 年を超えるリセット時刻は除外されます。台帳には、その事実が最初に記録されたときに
`limit_reached` イベントが 1 件追加され、同じ事実に再び出会っても追加されません。
次に成功したターンは `limit` を消去し、別のアカウントへの移動も同様です。
それ以外の種類の結果が `limit` を設定することはありません。

`POST /runs/{ref}/move` は同じセッションを維持します。必要なのは、会話がそのアカウントの
ホームにある `stopped` または `failed` のセッションと、その配下での起動が受け入れられる
同じツールの移動先プロファイル（active、このノードの環境、承認されたソース）です。
エンジンは、その 1 つの会話、すなわちトランスクリプトとそのサブエージェントおよびツール結果の
ファイルを移動先のホームにコピーします。ログイン、設定、別の会話は決してコピーしません。
そして、run の行、ライブ行、台帳を 1 つのトランザクションで付け替えます。run は `run_ref` と
プロバイダーの会話 id を維持します。台帳には `moved` イベントが追加され、その詳細は
両方のプロファイルを参照で示し、引き継いだファイル数を数えます。失敗したセッションは
`reason` を維持します。移動元のホームは読み取られるだけです。拒否された場合は何も書き込まず、
移動が作ったものを削除します。

- 409：実行中のセッション、vault シークレット、リポジトリ認証情報、またはパッケージレジストリトークンを
  与えられたセッション（出力したものにそれらが含まれないことは証明できない）、ホームがそのプロファイルの
  所有でない行、移動先で別のセッションにすでに紐づいている会話 id、移動先が会話の異なるバージョンを
  保持している場合、別のツールの移動先、または引き継ぎ中に変更されたファイル（再試行）。
- 422：会話内のリンク、ファイルの別名、または特殊ファイル、および別のツールのセッション。

以前の移動によって移動先に残された同じ会話のコピーは、新しいもので置き換えられます。
何も開始されません。次の `POST /runs/{ref}/resume` は通常のものなので、移動先プロファイルの
ポリシー、予算、承認がそれに適用されます。まだ会話のないセッションは、引き継ぐものがないまま
移動し、移動先で改めて開始されます。

`GET /runs/{ref}/handoff` と `POST /runs/{ref}/handoff` は、ネイティブな再開が存在しない
別のツールで続行します。どちらも Claude Code、Codex、Gemini CLI、OpenCode のセッションの保存済みの会話を読み取ります（Grok のセッションは 422 を返します）。
セッションは `stopped`、`failed`、`running`、`idle` のいずれでもよく、まだ開始していないものは 409 を返します。
プレビューは実行中のセッションをそのままの状態で読み取ります。POST は、実行中のセッションを先に `POST /runs/{ref}/stop` と同じ方法で停止し、
そのツールが終了してから会話を読み取ります。停止を必要としない拒否（移動先プロファイル、vault シークレット、リポジトリ認証情報）は停止より前に行われ、
停止後の拒否では、セッションが停止されたこととそれを再開できることが示されます。Codex はそのロールアウトから、Gemini CLI はそのチャットログから、
OpenCode はそれ自身の `opencode export`（セッションのプロファイルの下で一度だけ、オフラインで実行）を通じて読み取られます。
プレビューは、モデルを使わずに保存済みの会話から生成されるため、人がレビューして編集でき、
その読み取りは毎回、監査台帳に `sessions.run.handoff_preview` として記録されます。
POST は、選択したプロファイルで、同じワークスペース、同じテンプレートの下に新しいセッションを
開始します。その最初のターンは、レビュー済みの `summary`（ない場合は生成される）と、
トランスクリプトのパスです。トランスクリプトは、移動先プロファイルの設定ホームの下に、
専用の名前で書き込まれます。トランスクリプトは、メッセージを 1 件あたり最大 256 KiB まで全文で含み、
ツール呼び出しと結果は短縮され、読み取れなかったレコードの数も示します。新しい run は
`handoff_of` を報告し、元の run の台帳には `handed_off` イベントが追加されます。
応答には `summary_state` が含まれます。

| `summary_state` | 意味 |
|---|---|
| `delivered` | 最初のターンが新しいセッションに届いた。 |
| `not_delivered` | 届かなかった。`first_turn` に送信するテキストがある。 |
| `unknown` | 送信は試みられたが、結果は確認されていない。`first_turn` を再送する前に確認する。 |
| `waits_for_approval` | 起動は人の承認待ちで保留される（202）。最初のターンは自動では送信されない。 |

`first_turn` は、添付ファイルのパスを含むため、状態が `delivered` でないときにのみ存在します。
ハンドオフは会話を別のプロバイダーに送るため、そのセッションが vault シークレット、
リポジトリ認証情報、またはパッケージレジストリトークンを与えられていた場合、元の会話は 409 で
拒否されます。256 MiB を超える場合、または 32 MiB を超える 1 件のレコードを含む場合は、
422 で拒否されます。

### フォルダーのセットアップコマンドを承認する

セットアップコマンドは、登録されたフォルダーに属し、エンジンのストレージに保存されます。
リポジトリのファイルや環境変数では変更できません。現時点では、これらの操作でもセッションの
開始時でも、このコマンドを実行するものはありません。

- `PUT /workspaces/{ref}/setup-command` は `{"command":"npm ci"}` を受け取ります。
  空白を含め、UTF-8 のテキストがそのまま保持されます。最大 8192 バイトで、NUL を含められません。
  空文字列はコマンドを消去します。ワークスペース管理が必要です。
- `GET /workspaces/{ref}` は `setup_command`、`setup_command_sha256` を返し、承認後は
  `sha256`、`actor`、`at` を持つ `setup_approval` も返します。`setup_approved` が true になるのは、
  その承認が現在のテキストを対象としている間だけです。編集後も `setup_approval` は証跡として
  残るため、`setup_approval` の有無ではなく、必ず `setup_approved` を確認してください。
- `POST /workspaces/{ref}/setup-command/approve` は `{"sha256":"<reviewed hash>"}` を受け取ります。
  ワークスペース管理が必要で、エージェントのアイデンティティは拒否されます。ハッシュは現在保存されている
  テキストと一致しなければならず、同時に編集された場合は 409 を返します。承認と、その
  `sessions.workspace.approve-setup` 監査エントリは、一緒にコミットされます。

コマンドに一致する承認がない場合、ワークツリーの開始は、ワークツリーを作成する前に 409 を返します。
その文面には、コマンドと承認の手順が示されます。空白を含め 1 文字でも変更すると、再承認が必要です。
最後の承認は証跡として残ります。適用されるにはそのハッシュが `setup_command_sha256` と等しい必要が
あるため、以前に承認されたテキストにコマンドを戻すと、その承認が再び適用されます。
フォルダーを読める人は誰でもコマンドを読めるため、シークレットを含めないでください。
`"no_setup":true` を付けた `POST /runs` は、その開始に限ってセットアップをスキップします。
ワークツリーを持つセッションのハンドオフは新しいワークツリーを開始し、同じ承認が必要です。
スキップのオプションはありません。それ以外の開始と再開は、既存の動作のままです。

CLI では、`olivares agent workspace setup <ref> --command 'npm ci'` で設定し、
`olivares agent workspace setup <ref>` で読み取り、表示されたハッシュを
`olivares agent workspace approve-setup <ref> --sha256 <hash>` で承認します。
`olivares session start --worktree --no-setup` は、1 回に限ってセットアップをスキップします。

### コミットまたはブランチからワークツリーを開始する

`POST /runs` の `worktree` オプションは専用のワークツリーとブランチを作成します。`worktree` とともに、任意の `worktree_from` を受け付けます。値は、完全なコミット ID（40 または 64 桁の小文字の 16 進数）、またはワークスペースのリポジトリのローカルブランチです。新しいワークツリーと自身の新しいブランチは、ワークスペースの現在のコミットではなくそこから開始します。指定されたブランチとワークスペースのチェックアウトは移動しません。これは、任意の `branch` と `sha` を内容に含むハンドオフの作業を受信者が開く方法です。Git が値を完全なコミット ID に解決し、その ID のみを使います。リポジトリにないコミット、存在しないブランチ、リビジョン式、範囲、オプション、`worktree` なしの `worktree_from` は、何も作成する前に 422 となります。省略すれば従来どおりワークスペースの現在のコミットから開始します。

`GET /runs/{ref}/diff` は、セッションのワークツリーブランチがワークスペースの現在のコミットから分岐した後に変更したパスを一覧にします。`base` はマージベース、`head` はブランチ先端で、どちらも完全なコミット ID です。上限は 200 パスで、それを超えると `truncated` が示します。`GET /runs/{ref}/diff/file?path=` は `base` と `head` の各パスのテキストを返し、そこにファイルがなければ空です。Git の plumbing コマンドでコミット済みオブジェクトを読むため、未コミットの編集は含まれません。実行の読み取りと同じ権限が必要で、ワークスペースのファイル規則も維持します。許可サブパスの外のパスは一覧に含まれず 404、DLP ポスチャが拒否するワークスペースでは 403（読み取りはワークスペースファイル読み取りと同様に監査）、16 MiB を超えるファイルは 413 です。ワークツリーのないセッションと別テナントの実行は 404、消失したブランチや共通履歴のないブランチは 409 です。リポジトリのどのブランチ、タグ、リモートブランチも保持しないコミット ID の `worktree_from` も 422 です。

### リポジトリが持つエージェント設定

クローンしたリポジトリは、プログラムを実行したりトラフィックをリダイレクトしたりする
エージェント設定を持っていることがあります。エンジンは、フォルダーでセッションを開始するとき、
それを信頼できない入力として扱います。以下のファイルを読み取り、決して実行せず、挙げたキーだけを
収集します。空の値（`{}`、`[]`、`""`、`false`、`null`）は何も設定しないため、フラグは付きません。

| ツール | ファイル | フラグ対象 |
| --- | --- | --- |
| Claude Code | `.claude` フォルダーの `settings.json` と `settings.local.json` | `hooks`、`apiKeyHelper`、`statusLine`、`mcpServers`、`enableAllProjectMcpServers`、`enabledMcpjsonServers`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`fileSuggestion`、`enabledPlugins`、`extraKnownMarketplaces`、および最終行に挙げた名前を持つ `env` エントリ |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`、`notify`、`mcp_servers`、`model_providers`、`otel`、最上位の任意の `*_base_url`、および最終行に挙げた名前を持つ `shell_environment_policy.set` エントリ。`profiles.<name>` 配下の同じキー |
| Gemini CLI | `.gemini/settings.json` | `hooks`、`mcpServers`、`toolDiscoveryCommand`、`toolCallCommand`、`proxy`、`tools.discoveryCommand`、`tools.callCommand`、`mcp.serverCommand`、`telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`、`.env` | 最終行に挙げた名前を持つエントリ |
| 環境変数名 | | `NODE_OPTIONS`、`NODE_EXTRA_CA_CERTS`、`NODE_TLS_REJECT_UNAUTHORIZED`、`LD_PRELOAD`、`LD_LIBRARY_PATH`、`LD_AUDIT`、`DYLD_INSERT_LIBRARIES`、`BASH_ENV`、`PATH`、`PYTHONPATH`、`GIT_SSH_COMMAND`、`GIT_EXTERNAL_DIFF`、`SSL_CERT_FILE`、`CLAUDE_CODE_SHELL_PREFIX`、`ANTHROPIC_CUSTOM_HEADERS`、`AWS_ENDPOINT_URL*`、および `_BASE_URL`、`_API_BASE`、`_ENDPOINT`、`_PROXY` で終わる任意の名前（`NO_PROXY` はプロキシを絞り込むだけなのでフラグは付かない） |

存在するがその形式として読み取れないファイル（有効な JSON または TOML ではない、1 MiB を超える、
64 階層より深くネストしている、通常のファイルではない）は、安全であることを示せないため、
フラグ対象として扱われます。TOML では、ドット付きキーまたはテーブルヘッダーの各セグメントが、
パース後のドキュメントと同じく 1 階層となり、それを囲む括弧の階層に加算されます。
35 個のネストしたインラインテーブルの上の 30 セグメントのヘッダーは 65 階層です。
ファイルに `[[array]]` ヘッダーが 1 つでもあると、各セグメントが配列とその要素を指す可能性が
あるため、ヘッダーのセグメントは 2 回数えられます。サイズ超過のファイルは、チェックのたびに
全体がハッシュされます。フラグ対象が何もないフォルダーは、従来どおり開始されます。

- `GET /workspaces/{ref}/project-config` は、3 つのツールすべてのファイルについて、フラグ対象の
  キーをファイルごとにその値とともに一覧にし（URL 内のパスワードはマスクされる）、そのフラグ対象部分
  そのものの `sha256`、さらに `approved` と最後の `approval` を返します。フォルダー自体を読み取ります。
  別のコミットから開始したワークツリーは異なる追跡ファイルを持つことがあり、その拒否は自身の
  ハッシュを示します。
- `POST /workspaces/{ref}/project-config/approve` は `{"sha256":"<reviewed hash>"}` を受け取り、
  ワークスペース管理が必要で、エージェントのアイデンティティは拒否します。承認は、誰が、いつ、
  どのハッシュかを保存し、`sessions.workspace.approve-project-config` 監査エントリが同じ
  トランザクションで記録されます。

フラグ対象のファイルを読むツールの開始または再開は、保存済みの承認が現在のハッシュと等しくない限り、
409 を返します。フラグ対象の値を 1 文字変更する、またはフラグ対象のキーを追加・削除すると、
再承認が必要です。フラグ対象でないキーの変更では不要です。このチェックをスキップする
オプションはありません。

エンジンのフックを持つ Claude Code セッションは `--setting-sources ""` で起動されます。
Claude Code 2.1.299 では、そのセッションはプロジェクトやローカルの設定ファイルも、プロジェクトの
`.mcp.json` も読み込まないため、そのような開始は拒否されません。エンジンのフックを持たない
Claude Code の起動（リモートコントロール、またはフックのエンドポイントを持たないエンジン）は、
それらのファイルを読み込み、チェックされます。Codex は、そのアカウントホームで信頼された
プロジェクトの `.codex/config.toml` だけを尊重し、信頼されていないプロジェクトのファイルは
無視します。エンジンはツールに問い合わせることができないため、そのファイルをチェックし、
人が 1 回承認します。エンジンが起動するのと同じ方法（`gemini --acp`）で開始された
Gemini CLI 0.64.0 は、そのアカウントホームが信頼していないフォルダーの `.gemini/settings.json` と
`.gemini/.env` を無視します。信頼されたフォルダーでは、`tools.discoveryCommand`、`hooks`、
`mcpServers` のコマンドを実行し、ベース URL を `.gemini/.env` から取得します。エンジンは
同じ理由でそれらのファイルをチェックします。Gemini CLI は親フォルダーのファイルも読み込みますが、
このチェックはそれらを読み取りません。

CLI では、`olivares agent workspace project-config <ref>` で読み取り、
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` で承認します。

## トランスポート（Community）

各公式 CLI は、その operate 形式が必要とするトランスポートで起動する。どれかは
`cliruntime.LaunchTransport` が宣言する。現在の三形式はすべて stdio プロトコル
なので、composition root は `sessions.NewProcRunner()` を配線する。stdin、
stdout、stderr はパイプであり、別々のストリームのままである。

**Claude Code は stdin の端末を拒否する。** stream-json の `--print` 形式は
`Error: Input must be provided either through stdin or as a prompt argument
when using --print` を返し、プロトコルフレームを一つも出さずに 1 で終了する。
端末が必要なのは対話形式であり、`sessions.NewPTYRunner()` は Linux でその用途に
残る。container と sandbox の隔離は両方で拒否したまま。

ドライバ契約は `modules/sessions/cliruntime`。種類は `claude`、`codex`、`grok`。
適合は常にプロセス内 fake とローカル PTY ピアに対して走る。PATH に
`claude` / `codex` / `grok` があるとき、別テストが実バイナリを所有し、停止し、
終了を記録する。モデルターンは送らない。

## 関連

- [プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)
- [モジュール II — ライブ運用](/reference/modules/ii-sessions/)
- [Claude Code を接続する](/how-to/connect-claude-code/)
- [Beta OpenAPI](/reference/api-beta/)
