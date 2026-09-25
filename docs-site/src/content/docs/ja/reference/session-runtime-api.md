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
ルートは既に [beta OpenAPI](/reference/api-beta/) にある。未リリースの v26.10 はドライバ契約、
ローカル PTY ランナー、旅程 J01–J08 をテストとして追加する。

## エディション境界

| エディション | 行うこと | 行わないこと |
|---|---|---|
| **Community（本ページ）** | 所有するローカル子プロセス：起動、stdin/stdout/stderr、カーソル付き attach、正確な会話の resume、生きているストリームの reconnect、観測した終了ステータスでの stop。セッション行と証拠はモジュール II に残る。 | Identity & Scale の複数ペインエンジン、mTLS リスナー、商用入力セッション、xterm UI チャンク |
| **Identity & Scale オーバーレイ** | 商用 session-cockpit エンジン（リスナー、ペイン、台帳）。アドオンがあるとき `/v1/m/session-cockpit/` 下に載る。 | `/v1/m/sessions/runs` を置き換えない |

Community ビルドはオーバーレイ名前空間に **不在**（404）で答える。501 スタブは
載せない。

Claude Code の hook 経路は `olivares claude-hook`（PreToolUse PEP）のまま。
観測と執行であり、代替 CLI ではない。

## 権限

モジュール経路上の既存の執行点：

| 権限 | 経路 |
|---|---|
| `sessions:run:read` | `GET /runs`、`GET /runs/{ref}`、`GET /runs/{ref}/events`、`GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`、`POST /runs/{ref}/input`、`POST /runs/{ref}/interrupt`、`POST /runs/{ref}/stop`、`POST /runs/{ref}/resume` |
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

切断した attach の再接続は **同じ** 生きているプロセスへの
`GET …/attach?from={last+1}`。プロセス喪失の後、attach はライブでないと述べる。
Resume は新しい世代を始める。Reconnect は代替プロセスを捏造しない（SDD R04）。

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
