---
title: セッションランタイム API（公式 CLI）
description: >-
  所有する Claude Code、Codex、Grok CLI プロセスを一覧・接続・入力・停止する Community HTTP 面。
  権限、stdio 入出力、再開、再接続。
---

コントロールプレーンは **ベンダー CLI を起動する**。Claude Code、Codex、Grok Build を
置き換えない。セッションと端末はこの製品の一モジュールであって、製品そのものでは
ない。

このページは `/v1/m/sessions/runs` 下の Community operate ルートを文書化する。
これらのルートは既に [beta OpenAPI](/reference/api-beta/) にある。26.10 では、
ドライバ契約、各起動形式が必要とするトランスポート、そして旅程 J01–J08 をテストとして追加する。

## エディション境界

| エディション | 行うこと | 行わないこと |
|---|---|---|
| **Community（本ページ）** | 所有するローカル子プロセス：起動、stdin/stdout/stderr、カーソル付き attach、正確な会話の resume、生きているストリームの reconnect、観測した終了ステータスでの stop。セッション行と証拠はモジュール II に残る。 | Identity & Scale の複数ペインエンジン、mTLS エージェントリスナー、コックピットのペイン入力、xterm UI チャンク |
| **Identity & Scale オーバーレイ** | 商用のセッションコックピットエンジン（リスナー、ペイン、記録台帳）。モジュールが存在するとき、経路は `/v1/m/session-cockpit/` 下に載る。 | `/v1/m/sessions/runs` を置き換えない |

Community ビルドは、オーバーレイの名前空間に対して **不在**（404）で応答する。
501 のスタブは載せない。

Claude Code の hook 経路は `olivares claude-hook`（PreToolUse PEP）のままである。
これは観測と執行であり、代替の CLI ではない。

## 権限

モジュールの経路に既存の執行点がある：

| 権限 | 経路 |
|---|---|
| `sessions:run:read` | `GET /runs`、`GET /runs/{ref}`、`GET /runs/{ref}/events`、`GET /runs/{ref}/attach`、`GET /runs/{ref}/handoff`、`GET /runs/{ref}/activity`、`GET /runs/{ref}/activity/agents/{agent}`、`GET /runs/{ref}/messages`、`POST /runs/{ref}/pin`、`DELETE /runs/{ref}/pin` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/stop`、`POST /runs/{ref}/resume`、`POST /runs/{ref}/move`、`POST /runs/{ref}/handoff`, `PUT /runs/{ref}/name`, `POST /runs/{ref}/archive`, `DELETE /runs/{ref}/archive` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`、`DELETE /runs/{ref}` |

viewer は、自分自身のセッションを一覧し、ピン留めできる。作成、入力、停止には
write が要る。経路の authorizer が執行点であり、権限がなければ 403 となる。

## コンソールが読む経路

基点：`/v1/m/sessions`。認証する。`X-Olivares-Tenant` を送る。

| メソッド | 経路 | 結果 |
|---|---|---|
| `GET` | `/runs` | 管理対象の実行のページ。任意で `state`、`live_ref`、`claude_session_id`（レガシー） |
| `GET` | `/runs/{ref}` | 一件。`state` は導出される。`exit_code` は観測値。成功を示す捏造されたフィールドはない。セッションがアーカイブされている間は `archived_at`（RFC 3339、UTC）が含まれる |
| `GET` | `/runs/{ref}/events` | ライフサイクルの証拠行 |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE：`output` フレーム、リングがカーソルより下を追い出したときの `lag`、`end`、またはライブでないときの `notice`。vault シークレット、リポジトリ認証情報、管理された認証情報、またはパッケージレジストリトークンを与えられた実行の起動の後に resume された Grok、OpenCode、または Gemini CLI のセッションは、resume 時にそのツールが再生する会話をストリームせず、1 つの `olivares_notice` フレーム（`history_withheld`）でそのことを示す |
| `POST` | `/runs/{ref}/input` | stdin。stream-json は `line`/`message` を使い、Codex/Grok は `text` を使う。202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | プロセスグループへ SIGTERM、続いて SIGKILL。観測した終了ステータスを行に記録する |
| `POST` | `/runs/{ref}/resume` | 新しいプロセス世代。保存された正確な会話。黙って新しい会話にフォールバックすることはない |
| `POST` | `/runs/{ref}/interrupt` | 活動中のターンを取り消す。プロセスは残る |
| `POST`, `DELETE` | `/runs/{ref}/pin` | 呼び出し元のためにセッションをピン留めする、またはピンを外す。ピンは呼び出し元自身のものであり、別のブラウザーやマシンにも引き継がれる。他の人の一覧は決して変えない。どちらの呼び出しも冪等で、200 `{run_ref, pinned, pinned_at}` で応答する。1 人が保持できるピンは最大 20 件で、21 件目をピン留めすると最も古いものが外れる。セッションを解放または削除すると、そのセッションのすべてのピンが外れる。`GET /runs` と `GET /runs/{ref}` は呼び出し元自身の `pinned` と `pinned_at` を返す。`GET /runs` は、さらにそのページの外にある呼び出し元のピン留めセッションも返す（`limit` を超えて最大 20 行まで。`pagination=cursor` は、すべてのセッションに一度で到達するため、追加しない）。ピンは、それを作成した認証情報に属する。人のコンソールと `olivares login` のセッションはピンを共有し、API トークンは自身のピンを保持する。解放されたセッションは 409 で応答する |
| `POST`, `DELETE` | `/runs/{ref}/snooze` | 呼び出し元のために、セッションを指定の時刻までスヌーズする。または、スヌーズを終える。`POST` は `{until}`（将来の RFC 3339 時刻。それ以外は 400）を受け取り、2 回目の `POST` で時刻を移動できる。スヌーズはピンと同様に呼び出し元自身のものであり、コンソールは `until` までそのセッションを本人の一覧から外すが、他の人の一覧は変わらない。どちらも 200 `{run_ref, snoozed_until?, done}` で応答する。`GET /runs` と `GET /runs/{ref}` が呼び出し元の `snoozed_until` を返すのは、スヌーズが続いている間だけである。セッションを解放または削除すると、そのセッションのすべてのスヌーズが外れる。解放されたセッションは 409 で応答する |
| `POST`, `DELETE` | `/runs/{ref}/done` | 呼び出し元のためにセッションを完了にする、または完了の印を消す。コンソールは、完了したセッションをその本人の一覧の「以前」に分類する。どちらも冪等で、200 `{run_ref, snoozed_until?, done}` で応答する。`GET /runs` と `GET /runs/{ref}` は呼び出し元の `done` を返す。セッションを解放または削除すると、そのセッションのすべての印が外れる。解放されたセッションは 409 で応答する |
| `PUT` | `/runs/{ref}/name` | 本文 `{name}`。セッションの名前を変更する。名前は前後の空白が除かれ、空の名前は名前を消し、クライアントは独自の命名に戻る。200 文字を超える名前、制御文字または不可視文字（書式文字、行区切り・段落区切り：ゼロ幅スペース、方向の上書き）、または `name` がない場合は 400。200 で run を返す。名前はセッションのものなので、すべての読み手に見える。監査台帳に `sessions.run.renamed` として、名前ではなく run の参照とともに記録される。すでにセッションの名前と同じ名前は何も変えず、何も記録しない |
| `POST`, `DELETE` | `/runs/{ref}/archive` | セッションをアーカイブする、またはアーカイブを解除する。本文なし。200 で run を返し、アーカイブ中は `archived_at` を含む。アーカイブしてもセッションは停止も削除もされない。印は呼び出し元ではなくセッションのものなので、全員にとってアーカイブされる。`GET /runs` はこれで絞り込まれず、各クライアントがアーカイブ済みセッションを一覧から外す。どちらの呼び出しも冪等で、アーカイブ済みセッションをアーカイブしても `archived_at` は保たれ、何も記録されない。監査台帳に `sessions.run.archived` と `sessions.run.unarchived` として記録される |
| `POST` | `/runs/{ref}/move` | 本文 `{provider_profile_ref}`。停止または失敗した Claude Code セッションを、同じツールの別のプロファイルに付け替え、その会話を引き継ぐ。何も開始しない。200 で run を返す |
| `GET` | `/runs/{ref}/handoff` | ハンドオフが開始点とする要約：`summary`、`source_driver`、`turns`、`transcript_bytes`。読み取り専用で、監査台帳に記録される。実行中のセッションはそのままの状態で読み取られ、まだ開始していないセッションは 409 で応答する |
| `POST` | `/runs/{ref}/handoff` | 本文 `{provider_profile_ref, summary?, name?}`。そのプロファイルで、このセッションに関連付けた新しいセッションを開始する。201 で run と `summary_state`（`delivered` 以外のときは `first_turn` も）を返す。起動が承認を待っている間は 202 |
| `GET` | `/runs/{ref}/activity` | Claude Code または Codex セッションのツールが何をしているか。リクエストのたびにそのツール自身のネイティブな記録から読み取る：`agents`、`tasks`（コマンドとバックグラウンド作業）、`files`（path、`added`、`removed`、`created`）、`todos`、`skills`、`mcp_servers`、`model`、`context_tokens`、ツールが報告したときの null 許容の `context_window`、`context_percent`（Claude Code ではパーセンテージ、不明なときは null）、`compactions`、`transcript`（`read`、ツールがまだ書き込む前の `pending`、あるはずなのにない場合の `missing`）。agents と tasks の状態は `running`、`completed`、`failed`、`stopped`、`scheduled` のいずれか。Codex は、別の app-server を起動せずに rollout ファイルを使う。context tokens は累積使用量ではなく、最新のリクエストを表す。読み取り専用で、読み手とセッションごとに 15 分に 1 回、監査台帳に記録される。未対応のツールは 422 で応答する。vault シークレット、リポジトリ認証情報、または管理された認証情報を与えられたセッションは 409 で応答し、パッケージレジストリトークンを使ったセッションも、停止しているとき、またはそのトークンを使った起動の後の resume を経て実行されているときは 409 で応答する |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | Claude Code のサブエージェント 1 つのトランスクリプトを、平文のターン（`role`、`text`）で返す。読み取り専用で、監査台帳に記録される。未知のエージェントは 404。Codex は子の状態をルートの Activity の応答で報告し、子のトランスクリプトの読み取りは提供しない |
| `GET` | `/runs/{ref}/messages` | Claude Code または Codex セッションのメッセージを、リクエストのたびにツール自身のトランスクリプトから読み取り、ページ単位で返す：`items`（`id`、`role` は `user` または `assistant`、`at`、`first_line`）、`total`、`next`、`transcript`（`read`、`pending`、`missing`）。既定では人が送信したものだけで、`role=all` でツールの回答も加わる。`q` は、そのフレーズをどこかに含むメッセージに絞り込む（大文字小文字を区別しない、最大 256 バイト）。`at`（RFC 3339）は、その時刻以降のメッセージに絞り込むため、最初の項目が基点になる。`order` は `asc`（既定）または `desc`、`limit` は最大 200、`cursor` はページの続きの起点となる id（`next`）。項目は常に古い順。`id` は 1 件のメッセージ全体を読む（`text`、および `later_same`：同じ送信者の後続メッセージのうち、その最初の行を繰り返すものの数。フィルターは適用されず、`total` はセッションのメッセージ数）。`text=1` はページの各項目に `text` を加え（`id` での読み取りと同じくメッセージごとに 64 KiB まで）、`later_same` は付けない。その上限で切られたテキストは ` [...]` で終わり、その項目は `clipped: true` を持つ（`id` での読み取りでも同じ）。このようなページは最大 50 件のメッセージを含む。会話全体をコピーするには `next` をたどる。メッセージの `id` はセッションのメッセージ内での位置で、トランスクリプトが伸びても変わらない。読み取り専用で、読み手、セッション、15 分の窓ごとに 1 回、監査台帳に `sessions.run.messages` として記録され、`text=1` での読み取りは `sessions.run.messages.text` として記録される。未対応のツールは 422、vault シークレット、リポジトリ認証情報、または管理された認証情報を与えられたセッションは 409、未知の `id` は 404、無効なパラメーターは 400 で応答する |
| `DELETE` | `/runs/{ref}` | `sessions:run:admin` が必要。解放されたセッションの記録を削除する。解放されていないセッションは 409 `session must be cleaned before delete (state=…)` を返す。削除と同じトランザクションで、監査台帳に `sessions.run.deleted` として記録される |
| `GET` | `/runs/{ref}/diff` | ワークツリーセッションのブランチを、開始時点と比較したもの：`branch`、`base`、`head`、変更された `files`（`path`、`status`）。ワークツリーがなければ 404 |
| `GET` | `/runs/{ref}/diff/file?path=` | `base` と `head` における 1 パスのテキスト（`original`、`modified`。各々、ワークスペースの `max_read_bytes` と 64 KiB の小さい方が上限） |

Codex の Code Mode 呼び出しで、プラン引数が rollout に保持されないものについては、
Activity はライブブリッジがすでに受信した最新の `turn/plan/updated` 通知も読み取る。
ブラウザーを再読み込みしても同じサーバー側セッションを使い、更新のために app-server は起動されない。
ライブハンドルがなくなった後は、Codex 自身が保持したデータだけを読み取れる。

Claude Code では、ウィンドウはネイティブの result フレームの `modelUsage[model].contextWindow` から取得され、停止や再接続をまたいで保持される。
一致する曖昧さのない報告があれば、`context_percent` は `100 × context_tokens / context_window` になる。
そうでなければ両方のフィールドが `null` になり、CLI とコンソールは、ウィンドウ不明の注記とともにトークン数を表示する。
ツールを新たに起動すると、以前のウィンドウの報告は消去される。

ワークフローのタスクには `workflow` が含まれることがある：`run_id`、`name`、`duration_ms`、`agents_total`、`agents_done`、`agents_failed`、`phases`。
各フェーズは `index`、`title`、任意の `detail`、`state`、および同じエージェント数を持つ。
数は、これまでに観測されたエージェントを表す。
ライブの間、フェーズの state は、観測された実行中または失敗したエージェントを表す。
エージェントの完了は、スクリプトがそのフェーズを離れたことを証明しないため、フェーズを完了とは記録せず、現在のスクリプトのステップも特定しない。
完了したフェーズの state は、最終的なワークフローの要約が与える。

ライブのワークフロースナップショットは、Claude Code の `system/task_progress` 出力から得られ、attach ストリームと共有される。
ツールが完了した `workflows/<runId>.json` は、トランスクリプトのホーム、リンク、サイズの規則のもとで最終的な要約を埋める。
要約の読み取りは、ファイルごとに 8 MiB、リクエストごとに 64 MiB が上限である。
それより大きいネイティブのログや結果は、利用できるライブの進捗を保ったまま、要約を読み取り不能にする。
読み取り不能な要約と、上限のあるライブバッファーの欠落は、`unreadable` に寄与する。

切断した attach の再接続は、**同じ** 生きているプロセスへの
`GET …/attach?from={last+1}` である。プロセスが失われた後、attach はセッションがライブでないと伝える。
Resume は新しい世代を始める。Reconnect は代替プロセスを捏造しない（SDD R04）。

### セッションの移動とハンドオフ

セッションは 2 つの方法のいずれかで別のアカウントに続行でき、コンソールと
`olivares session move` / `olivares session handoff` の両方がそれらを提供する。
`move` は Claude Code セッションの保存済みの会話を同じツールの別のプロファイルへ運ぶため、
`move` できるのは Claude Code のセッションだけである。`handoff` は、Claude Code、Codex、Gemini CLI、
OpenCode のいずれかのセッションの会話から、新しいセッションを開始する。

実行は、そのツールが直近のターンを上限で終えたとき、`limit`
（`{kind, resets_at, account_ref}`）を持つ。`kind` は `usage`（アカウントのプランの
許容量を使い切った）または `rate`（プロバイダーがリクエストを制限している）である。
`resets_at` は RFC 3339 で、ツールが上限の解除時刻を報告したときにあり、報告しないときはない。
`account_ref` は、そのターンが実行されたプロファイルである。Claude Code は、
その事実を自身の結果（`api_error: usage_limit_reached` または HTTP 429）で供給し、
リセット時刻を `rate_limit_event` で供給する。Codex は、その事実を `usageLimitExceeded` または
`rateLimitExceeded` の型を持つ失敗したターンで供給し、リセット時刻を `account/rateLimits/updated`
のウィンドウで供給する。2 つのウィンドウを使い切っている場合は、より遅いリセットが記録され、
現在から 1 年を超えるリセット時刻は除外される。台帳には、その事実が最初に記録されたときに
`limit_reached` イベントが 1 件追加される。同じ事実に再び出会っても、追加されない。
次に成功したターンは `limit` を消去する。別のアカウントへの移動も同様に消去する。
それ以外の種類の結果が `limit` を設定することはない。

`POST /runs/{ref}/move` は同じセッションを維持する。必要なのは、会話がそのアカウントの
ホームにある `stopped` または `failed` のセッションと、その配下での起動が受け入れられる
同じツールの移動先プロファイル（active、このノードの環境、承認されたソース）である。
エンジンは、その 1 つの会話、すなわちトランスクリプトと、そのサブエージェントおよびツール結果の
ファイルを、移動先のホームにコピーする。ログイン、設定、別の会話は決してコピーしない。
そして、run の行、ライブ行、台帳を 1 つのトランザクションで付け替える。run は `run_ref` と
プロバイダーの会話 id を維持する。台帳には `moved` イベントが追加され、その詳細は
両方のプロファイルを参照で示し、引き継いだファイル数を数える。失敗したセッションは
`reason` を維持する。移動元のホームは読み取られるだけである。拒否された場合は何も書き込まず、
移動が作ったものを削除する。

- 409：実行中のセッション、vault シークレット、リポジトリ認証情報、管理された認証情報、またはパッケージレジストリトークンを
  与えられたセッション（出力したものにそれらが含まれないことは証明できない）、ホームがそのプロファイルの
  所有でない行、移動先で別のセッションにすでに紐づいている会話 id、移動先が会話の異なるバージョンを
  保持している場合、別のツールの移動先、または引き継ぎ中に変更されたファイル（再試行）。
- 422：会話内のリンク、ファイルの別名、または特殊ファイル、および別のツールのセッション。

以前の移動によって移動先に残された同じ会話のコピーは、新しいもので置き換えられる。
何も開始されない。次の `POST /runs/{ref}/resume` は通常のものなので、移動先プロファイルの
ポリシー、予算、承認がそれに適用される。まだ会話のないセッションは、引き継ぐものがないまま
移動し、移動先で改めて開始される。

`GET /runs/{ref}/handoff` と `POST /runs/{ref}/handoff` は、ネイティブな再開が存在しない
別のツールで続行する。どちらも Claude Code、Codex、Gemini CLI、OpenCode のセッションの保存済みの会話を読み取る（Grok のセッションは 422 で応答する）。
セッションは `stopped`、`failed`、`running`、`idle` のいずれでもよく、まだ開始していないものは 409 で応答する。
プレビューは実行中のセッションをそのままの状態で読み取る。POST は、実行中のセッションを、先に `POST /runs/{ref}/stop` と同じ方法で停止し、
そのツールが終了してから会話を読み取る。停止を必要としない拒否（移動先プロファイル、vault シークレット、リポジトリ認証情報）は、停止より前に行われる。
停止後の拒否では、セッションが停止されたこととそれを再開できることが示される。Codex はそのロールアウトから、Gemini CLI はそのチャットログから読み取られ、
OpenCode は、それ自身の `opencode export`（セッションのプロファイルの下で一度だけ、オフラインで実行）を通じて読み取られる。
プレビューは、モデルを使わずに保存済みの会話から生成されるため、人がレビューして編集できる。
その読み取りは毎回、監査台帳に `sessions.run.handoff_preview` として記録される。
POST は、選択したプロファイルで、同じワークスペース、同じテンプレートの下に新しいセッションを開始する。その最初のターンは、レビュー済みの `summary`（ない場合は生成される）と、
トランスクリプトのパスである。トランスクリプトは、移動先プロファイルの設定ホームの下に、
専用の名前で書き込まれる。トランスクリプトは、メッセージを 1 件あたり最大 256 KiB まで全文で含み、
ツール呼び出しと結果は短縮され、読み取れなかったレコードの数も示す。新しい run は
`handoff_of` を報告し、元の run の台帳には `handed_off` イベントが追加される。
応答には `summary_state` が含まれる：

| `summary_state` | 意味 |
|---|---|
| `delivered` | 最初のターンが新しいセッションに届いた。 |
| `not_delivered` | 届かなかった。`first_turn` に送信するテキストがある。 |
| `unknown` | 送信は試みられたが、結果は確認されていない。`first_turn` を再送する前に確認する。 |
| `waits_for_approval` | 起動は人の承認を待って保留される（202）。最初のターンは自動では送信されない。 |

`first_turn` は、添付ファイルのパスを含むため、状態が `delivered` でないときにのみ存在する。
ハンドオフは会話を別のプロバイダーに送るため、元の会話は、そのセッションに vault シークレット、
リポジトリ認証情報、管理された認証情報、またはパッケージレジストリトークンが与えられていた場合は 409 で
拒否される。256 MiB を超える場合、または 32 MiB を超える 1 件のレコードを含む場合は、
422 で拒否される。

### ワークツリーオプション

`POST /runs` は、任意の真偽値 `worktree` を受け付ける。これが true で、かつ `workspace_ref` が、
git リポジトリのトップフォルダーである登録済みのワークスペースを指すとき、セッションは、そのリポジトリの新しい git ワークツリーとブランチで作業する
（`workspace_path` がそのワークツリーになり、run は `worktree_branch` を報告する）。
省略、null、または false は、現在の動作を維持する。エンジンは、git ワークツリーではないワークスペース、リポジトリのトップフォルダーではないワークスペース、
コミットのないワークスペース、読み取り専用のワークスペースまたは読み取り専用のフォルダーを持つワークスペース、フィルターやファイルを含む git 設定を持つワークスペース、
ネイティブでない隔離、およびワークスペースのない起動に対して 422 で応答する。ノードにワークツリーのディレクトリがない場合は 503 で応答する。
`POST /runs/{ref}/resume` は同じワークツリーに戻る。

`POST /runs/{ref}/cleanup` は、任意の本文 `{"discard_worktree": true}` を受け付ける。
本文がない場合は、従来どおりの呼び出しである。サーバーは、それでも任意の本文を受け付けるが、
明示的な `true` だけが確認となる。ワークツリーを持つセッションは、そのブランチがワークスペースの現在のブランチにマージされ、
ワークツリーがそのブランチ上にあり、コミットされていないファイルがない場合に解放される。そうでなければ、呼び出しは 409 で拒否される
（マージされていない作業、detached HEAD、ノードが到達できないワークツリー）。実行は停止したままになるため、
`discard_worktree` を付けて繰り返すことで、ワークツリーとブランチを削除できる。または、ノードが到達できないワークツリーについては、
セッションを解放し、ワークツリーをそのままの場所に残すこともできる。台帳の `cleaned` イベントは、
ブランチの先端とともに、ワークツリーに何が起きたかを示す。

### フォルダーのセットアップコマンドを承認する

セットアップコマンドは、登録されたフォルダーに属し、エンジンのストレージに保存される。
リポジトリのファイルや環境変数では変更できない。現時点では、これらの操作でも、セッションの開始時でも、このコマンドを実行するものはない。

- `PUT /workspaces/{ref}/setup-command` は `{"command":"npm ci"}` を受け取る。
  空白を含め、UTF-8 のテキストがそのまま保持される。最大 8192 バイトで、NUL を含められない。
  空文字列はコマンドを消去する。ワークスペースの管理が必要である。
- `GET /workspaces/{ref}` は `setup_command`、`setup_command_sha256` を返し、承認後は、
  `sha256`、`actor`、`at` を持つ `setup_approval` も返す。`setup_approved` が true になるのは、
  その承認が現在のテキストを対象としている間だけである。編集後も `setup_approval` は証跡として
  残るため、`setup_approval` の有無ではなく、必ず `setup_approved` を確認する。
- `POST /workspaces/{ref}/setup-command/approve` は `{"sha256":"<reviewed hash>"}` を受け取る。
  ワークスペースの管理が必要で、エージェントのアイデンティティは拒否される。ハッシュは現在保存されている
  テキストと一致しなければならず、同時に編集された場合は 409 で応答する。承認と、その
  `sessions.workspace.approve-setup` 監査エントリは、一緒にコミットされる。

コマンドに一致する承認がない場合、ワークツリーの開始は、ワークツリーを作成する前に 409 で応答する。
その文面には、コマンドと承認の手順が示される。空白を含め 1 文字でも変更すると、再承認が必要になる。
最後の承認は証跡として残る。適用されるには、そのハッシュが `setup_command_sha256` と等しくなければならないため、
以前に承認されたテキストにコマンドを戻すと、その承認が再び適用される。
フォルダーを読める人は誰でもコマンドを読めるため、シークレットは含めないこと。
`"no_setup":true` を付けた `POST /runs` は、その開始に限ってセットアップをスキップする。
ワークツリーを持つセッションのハンドオフは、新しいワークツリーを開始し、同じ承認が必要である。
スキップのオプションはない。それ以外の開始と resume は、既存の動作を維持する。

CLI では、`olivares agent workspace setup <ref> --command 'npm ci'` で設定し、
`olivares agent workspace setup <ref>` で読み取り、表示されたハッシュを
`olivares agent workspace approve-setup <ref> --sha256 <hash>` で承認する。
`olivares session start --worktree --no-setup` は、1 回に限ってセットアップをスキップする。

### コミットまたはブランチからワークツリーを開始する

`worktree` を付けると、`POST /runs` は任意の `worktree_from` も受け付ける。値は、完全なコミット ID（40 または 64 桁の小文字の 16 進数）、
またはワークスペースのリポジトリのローカルブランチである。新しいワークツリーと、その専用の新しいブランチは、
ワークスペースの現在のコミットではなく、そこから開始する。指定されたブランチとワークスペースのチェックアウトは移動しない。
これは、ハンドオフの内容が任意の `branch` と `sha` を含む場合に、受信側がそのハンドオフが指定する作業を開く方法である。
Git が値を完全なコミット ID に解決し、その ID だけが使われる。エンジンは、何かが作成される前に、
リポジトリが保持していないコミット、存在しないブランチ、リビジョン式、範囲、オプション、
および `worktree` なしの `worktree_from` に対して 422 で応答する。省略した場合、起動は従来どおりワークスペースの現在のコミットから開始する。

`GET /runs/{ref}/diff` は、セッションのワークツリーブランチが、ワークスペースの現在のコミットから分岐して以降に変更したパスを一覧にする
（`base` はマージベース、`head` はブランチの先端で、どちらも完全なコミット ID である。上限は 200 パスで、`truncated` は、それ以上が存在するかどうかを示す）。
`GET /runs/{ref}/diff/file?path=` は、1 パスのテキストを `base` と `head` で返す。そのファイルがそこに存在しなければ空になる。
Git の plumbing コマンドでコミット済みのオブジェクトを読むため、未コミットの編集は含まれない。
実行を読むのと同じ権限が必要で、ワークスペース自身のファイル規則も維持する。許可されたサブパスの外にあるパスは一覧に含まれず、404 で応答する。
DLP ポスチャが拒否するワークスペースは 403 で応答する（読み取りはワークスペースのファイル読み取りと同様に監査される）。
16 MiB を超えるファイルは 413 で応答する。ワークツリーのないセッションと、別テナントの実行は 404 で応答する。
消失したブランチ、またはワークスペースと履歴を共有しないブランチは 409 で応答する。
`worktree_from` は、リポジトリのどのローカルブランチ、タグ、リモートブランチも保持していないコミット ID に対しても 422 で応答する。

切断した attach の後の再接続は、同じ生きているプロセスに対する `GET …/attach?from={last+1}` である。プロセスが失われた後、attach はセッションがライブでないことを示す。
Resume は新しい世代を始める。Reconnect は代替プロセスを捏造しない（SDD R04）。

### リポジトリが持つエージェント設定

クローンしたリポジトリは、プログラムを実行したり、トラフィックをリダイレクトしたりする
エージェント設定を持っていることがある。エンジンは、フォルダーでセッションを開始するとき、
それを信頼できない入力として扱う。以下のファイルを読み取り、決して実行せず、挙げられたキーだけを
収集する。空の値（`{}`、`[]`、`""`、`false`、`null`）は何も設定しないため、フラグは付かない。

| ツール | ファイル | フラグ対象 |
| --- | --- | --- |
| Claude Code | `.claude` フォルダーの `settings.json` と `settings.local.json` | `hooks`、`apiKeyHelper`、`statusLine`、`mcpServers`、`enableAllProjectMcpServers`、`enabledMcpjsonServers`、`awsAuthRefresh`、`awsCredentialExport`、`otelHeadersHelper`、`fileSuggestion`、`enabledPlugins`、`extraKnownMarketplaces`、および最終行に挙げた名前を持つ `env` エントリ |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`、`notify`、`mcp_servers`、`model_providers`、`otel`、最上位の任意の `*_base_url`、および最終行に挙げた名前を持つ `shell_environment_policy.set` エントリ。`profiles.<name>` 配下の同じキー |
| Gemini CLI | `.gemini/settings.json` | `hooks`、`mcpServers`、`toolDiscoveryCommand`、`toolCallCommand`、`proxy`、`tools.discoveryCommand`、`tools.callCommand`、`mcp.serverCommand`、`telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`、`.env` | 最終行に挙げた名前を持つエントリ |
| 環境変数名 | | `NODE_OPTIONS`、`NODE_EXTRA_CA_CERTS`、`NODE_TLS_REJECT_UNAUTHORIZED`、`LD_PRELOAD`、`LD_LIBRARY_PATH`、`LD_AUDIT`、`DYLD_INSERT_LIBRARIES`、`BASH_ENV`、`PATH`、`PYTHONPATH`、`GIT_SSH_COMMAND`、`GIT_EXTERNAL_DIFF`、`SSL_CERT_FILE`、`CLAUDE_CODE_SHELL_PREFIX`、`ANTHROPIC_CUSTOM_HEADERS`、`AWS_ENDPOINT_URL*`、および `_BASE_URL`、`_API_BASE`、`_ENDPOINT`、`_PROXY` で終わる任意の名前（`NO_PROXY` はプロキシを絞り込むだけなので、フラグは付かない） |

存在するが、その形式として読み取れないファイル（有効な JSON または TOML ではない、1 MiB を超える、
64 階層より深くネストしている、通常のファイルではない）は、安全であることを示せないため、
フラグ対象として数えられる。TOML では、ドット付きキーまたはテーブルヘッダーの各セグメントが、
パース後のドキュメントと同じく 1 階層となり、それを囲む括弧の階層に加算される。
35 個のネストしたインラインテーブルの上にある 30 セグメントのヘッダーは 65 階層である。
ファイルに `[[array]]` ヘッダーが 1 つでもあると、各ヘッダーのセグメントは、配列とその要素の両方を指しうるため 2 回数えられる。
サイズ超過のファイルは、チェックのたびに全体がハッシュされる。フラグ対象が何もないフォルダーは、従来どおり開始される。

- `GET /workspaces/{ref}/project-config` は、3 つのツールすべてのファイルについて、フラグ対象の
  キーをファイルごとにその値とともに一覧にし（URL 内のパスワードはマスクされる）、そのフラグ対象部分
  そのものの `sha256`、さらに `approved` と最後の `approval` を返す。フォルダー自体を読み取る。
  別のコミットから開始したワークツリーは異なる追跡ファイルを持つことがあり、その拒否は自身の
  ハッシュを示す。
- `POST /workspaces/{ref}/project-config/approve` は `{"sha256":"<reviewed hash>"}` を受け取る。
  ワークスペースの管理が必要で、エージェントのアイデンティティは拒否される。承認は、誰が、いつ、
  どのハッシュかを保存し、`sessions.workspace.approve-project-config` 監査エントリが同じ
  トランザクションで記録される。

フラグ対象のファイルを読むツールの開始または resume は、保存済みの承認が現在のハッシュと等しくない限り、
409 で応答する。フラグ対象の値を 1 文字変更する、またはフラグ対象のキーを追加・削除すると、
再承認が必要になる。フラグ対象でないキーの変更では不要である。このチェックをスキップするオプションはない。

エンジンのフックを持つ Claude Code セッションは `--setting-sources ""` で起動される。
Claude Code 2.1.299 では、そのセッションはプロジェクトやローカルの設定ファイルも、プロジェクトの
`.mcp.json` も読み込まないため、そのような開始は拒否されない。エンジンのフックを持たない
Claude Code の起動（リモートコントロール、またはフックのエンドポイントを持たないエンジン）は、
それらのファイルを読み込み、チェックされる。Codex は、そのアカウントホームで信頼されたプロジェクトの `.codex/config.toml` だけを尊重し、
信頼されていないプロジェクトのファイルは無視する。エンジンはツールに問い合わせることができないため、そのファイルをチェックし、
人が 1 回承認する。エンジンが起動するのと同じ方法（`gemini --acp`）で開始された Gemini CLI 0.64.0 は、
そのアカウントホームが信頼していないフォルダーの `.gemini/settings.json` と `.gemini/.env` を無視する。
信頼されたフォルダーでは、`tools.discoveryCommand`、`hooks`、`mcpServers` のコマンドを実行し、ベース URL を `.gemini/.env` から取得する。
エンジンは同じ理由でそれらのファイルをチェックする。Gemini CLI は親フォルダーのファイルも読み込むが、
このチェックはそれらを読み取らない。

CLI では、`olivares agent workspace project-config <ref>` で読み取り、
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` で承認する。

## トランスポート（Community）

各公式 CLI は、その operate 形式が必要とするトランスポートで起動される。どれかは
`cliruntime.LaunchTransport` が宣言する。現在の 3 つの形式はすべて stdio プロトコルなので、
composition root は `sessions.NewProcRunner()` を配線する。stdin、stdout、stderr はパイプであり、
別々のストリームのままである。

**Claude Code は stdin の端末を拒否する。** その stream-json の `--print` 形式は、
`Error: Input must be provided either through stdin or as a prompt
argument when using --print` と応答し、プロトコルフレームを一つも出さずに終了コード 1 で終了する。
Codex の app-server と Grok の stdio エージェントは、同じストリームの上で JSON-RPC を話す。
端末が必要になるのは対話形式であり、`sessions.NewPTYRunner()` は Linux でその用途のために残る。
stdin と stdout は raw なローカル PTY、stderr はパイプになる。どちらも container または sandbox の隔離は実行しない。
podman または Docker のあるホストでは、stdio 形式に対して `isolation=container` 用のコンテナランナーが追加される。sandbox は引き続き拒否される
（[Topology 4](/how-to/run-claude-code-with-olivares/#topology-4--olivares-native-claude-in-a-per-session-container) を参照）。

ドライバ契約は `modules/sessions/cliruntime` にある。種類は `claude`、`codex`、`grok`。
適合テストは、常にプロセス内の fake と、両方のトランスポートにおけるローカルのピアに対して走る。
`claude` / `codex` / `grok` が PATH にあるとき、別の一連のテストが、実際のバイナリに対して契約全体を走らせる。
起動、attach、入力、出力、reconnect、stop、resume を検証し、モデルのターンは送らない。
すべてのハンドシェイクはプロトコルのハンドシェイクであり、起動のたびに新しい HOME と設定ホームが与えられる。

## 関連

- [プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)
- [モジュール II — ライブ運用](/reference/modules/ii-sessions/)
- [Claude Code を接続する](/how-to/connect-claude-code/)
- [Beta OpenAPI](/reference/api-beta/)
