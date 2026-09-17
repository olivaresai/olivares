---
title: プロバイダーセッションを運用する
description: >-
  このノード上の既存の Claude / Codex / Grok ホームにプロバイダープロファイルを登録し、
  公式ドライバーバイナリを固定し、コンソールまたは CLI からガバナンス対象のセッションを
  起動し、ランナーをでっち上げずにターンを中断または停止する。
---

このページは公式プロバイダー CLI の **運用** 経路です。コントロールプレーンは
**プロバイダープロファイル** の下で所有する子プロセスを起動します。プロバイダーの
インストール、ホームの作成、対話型ブラウザサインインは行いません。

コネクター / hook 経路ではありません。Grok Build または Codex の設定ファイルを
インベントリまたは統治するには
[Grok Build を統合する](/how-to/integrations/grok/) または
[Codex を統合する](/how-to/integrations/codex/) を使います。同じホストで
Claude Code を同居させるには
[Olivares で Claude Code を実行する](/how-to/run-claude-code-with-olivares/)
を使います。

v26.9.0 の挙動の出典: `CHANGELOG.md` の `[26.9.0]`（プロバイダープロファイル、
Codex ドライバー、Grok ドライバー）、生成された
[コンソール](/reference/console/) と
[設定](/reference/configuration/) リファレンス、
`cmd/olivares/sessionruntime.go`、`web/src/features/agentops/types.ts`。

## 前提条件

起動の前に完了してください。欠けている項目はフォールバックではなく拒否です。

1. Olivares AI がインストール済みで、最初の管理者が存在する。
   セットアップトークンと AAL3 パスキーの壁は
   [最初の1時間](/how-to/first-hour/) を参照。ソース作成と特権セッション操作は
   AAL3 を要求します（`core/api/middleware.go` `requireAAL3`）。
2. 公式プロバイダー CLI が **このノード** に既にインストールされている。
   プロファイルは既に存在するホームを登録します。サーバーはパスを解決します
   （絶対、シンボリックリンク解決済み、既存ディレクトリ）。作成、インストール、
   ログインはしません（`web/src/features/agentops/types.ts`
   `CreateProfileRequest`）。
3. **Provider profiles**（`/provider-profiles`）を開くには
   `sessions:profile:read`、登録には `sessions:profile:write` が必要です。
   ソースのバインドには `sessions:profile-binding:write` とソース管理が必要です。
   ランの起動には `sessions:run:write` が必要です。
   権限: [コンソールリファレンス](/reference/console/)。
4. 対応するドライバーは、公式バイナリを固定することで **このノードに登録**
   されます。準備状態はドライバーごとです。共有スイッチはありません
   （`cmd/olivares/sessionruntime.go`）。

| ドライバー | この環境変数を固定 | 未設定のとき |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`（既定 `claude`） | Claude 経路は既定の実行ファイル名を使う |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex プロファイルは観測可能のままで起動できない |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok プロファイルは観測可能のままで起動できない |

値はこのノードが運用してよい公式バイナリです。エンジンは `PATH` から
`codex` や `grok` を解決しません。生成された設定表は同じ登録規則で
`OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` も列挙します。このページは
OpenCode についてそれ以上の主張をしません。

Claude の起動には推論資格情報のソースがなお必要です
（`OLIVARES_SESSION_RUNTIME_WIF` または
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`）。
[最初の1時間](/how-to/first-hour/) を参照。
Codex と Grok はプロファイルの AUTHORIZED な `auth_source` だけを使います:
`provider_account_home` または `managed_injection`。相互フォールバックも既定も
ありません（`CHANGELOG.md` `[26.9.0]`、`ProviderProfileDTO.auth_source`）。

:::caution[このページが主張しないこと]
`CHANGELOG.md` `[26.9.0]` は、Grok ドライバーの挙動が所有する偽 ACP 子に対して
実 HTTP、ランタイム、ストア、プロセスグループ経由で証明されると述べます。
**認証済みの公式 Grok アカウントとの互換性は別作業であり、ここでは主張しません。**
:::

## 1. プロバイダープロファイルを登録する

プロバイダープロファイルは、**1** つの実行環境上の **1** つの構成済み
プロバイダーインスタンスの永続的な識別です: ドライバー、所有環境、子が使う
正規の `config_home` / `user_home`。構成とストレージの識別であり、認証済み
プロバイダーアカウントではありません（`CHANGELOG.md` `[26.9.0]` B1、
コンソール文言 `agentops.profiles.subtitle`）。

### コンソール

1. **Provider profiles**（`/provider-profiles`）を開く。
2. **Register profile** を選ぶ。
3. ドライバー（`claude`、`codex`、または `grok`）、既存の `config_home`、
   既存の `user_home` を設定する。`environment_ref` は省略できる（このノード）。
4. 保存する。一覧は `profile_ref`、ドライバー、状態、この環境で有効かを示す。
   パスは通常の一覧に **含まれない**。
5. 保存済みホームを見るには **Reveal configuration**
   （`sessions:profile:admin`）。その読み取りはオンデマンドで、隠すと破棄される。
   資格情報の値は依然として含まない。

名前変更、無効化、有効化は同じ id とホームを保ちます。**Retire** は不可逆で、
入力で確認し、ホームを **新しい** id に解放します。

### エンジンが拒否すること

- このノードにドライバーが登録されていないプロファイルは表示されたままで
  起動できない（`operable` は起動保証ではない。
  `GET …/launch-readiness` が要件パネル）。
- 別の実行環境に属するプロファイルは外部として表示され、このノードからは
  起動されない。
- `HOME`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`GROK_HOME` を転送する起動は
  拒否される。これらの名前はプロファイルに属する
  （`agentops.create.profileEnvConflict`）。

この画面の公開キャプチャはありません。Connectors タブの画像をこのフォームと
みなさないでください。

## 2. ソースをバインドする（任意、観測された帰属用）

ソースは、このノードが適用した正確なロスターリビジョンでプロファイルに
専用化できます。キーは行の永続 id であり、編集可能な名前ではありません
（`CHANGELOG.md` `[26.9.0]` B1、**Source bindings** `/provider-bindings`）。

1. **Source bindings**（`/provider-bindings`）を開く。
2. ソースの永続 `id` と、このノードのリコンサイラが配線した
   `applied_revision` をバインドする。`GET /v1/console/sources` が両方を返す。
3. バインドを取り消して **新しい** プロファイル帰属を止める。以前の
   エンベロープは再生時に歴史的バインドを保つ。

バインドが無い既知の登録は `source` 観測行として残ります。管理ランには
マージされません。
[ライブ運用とセッション](/reference/modules/ii-sessions/) を参照。

## 3. 起動

本番の作成エンドポイントは `provider_profile_ref` を要求します。省略すると
古いリクエスト本体のままになり、この API はそれを拒否します
（`CHANGELOG.md` `[26.9.0]` B2、CLI フラグ `--provider-profile`）。

起動ダイアログは **active** なプロファイルを提示します。プロファイルは
事前選択されません。workspace と template の選択はクリアできるが、
プロファイルはできません（`CHANGELOG.md` `[26.9.0]` Fixed）。

### コンソール

1. **Operate sessions**（`/agentops`）または **Observe sessions**
   （`/sessions`）を開く。画面は共有
   （[コンソールリファレンス](/reference/console/)）。
2. 起動ダイアログを開く。
3. **Provider profile**（`agentops.create.profile`）を選ぶ。ヒントは
   プロファイル必須と述べる。
4. 任意で workspace、template、model、effort を設定する。Grok では model と
   effort は公式エージェントフラグ上のプロバイダー所有の開いた文字列のまま
   （`CHANGELOG.md` `[26.9.0]`）。
5. **Request launch** を送る。投稿されるのはプロファイルの **参照** だけ。
   サーバーがホームを解決する。

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

[CLI リファレンス](/reference/cli/) のとおり `--server`、`--tenant`、
`--token`（またはアクティブなクライアントコンテキスト）を付ける。
このリリースの isolation は `native`。`container` と `sandbox` は API が
受け付け、ランチャーはそれらのランナーが届くまで拒否する（生成 CLI ヘルプ）。

結果: ランリソース。管理ライブ行は観測スコープと外部 id ごとに一意。
行を指す読み取りは `live_ref` を使い、2 つのホームが共有し得る素の
プロバイダーセッション id は使わない。

## 4. 中断または停止

| 意図 | コンソール | CLI | 結果 |
|---|---|---|---|
| アクティブなターンを終え、プロセスと会話を残す | ライブセッションの interrupt 制御 | `olivares agent session interrupt <run-ref>` | ターンは終わる。プロセスは次のターンに使える（`CHANGELOG.md` `[26.9.0]`） |
| ランを終える | stop 制御 | `olivares agent session stop <run-ref>` | ランリソース。ランタイムは子をなお回収する |

作業拘束ランは正確なリースフェンスを送る。古い結果や不確かな結果は明示のまま。
再開は証明済みの同じホームでのみ続く。

Grok の interrupt は ACK の無い通知である ACP `session/cancel` を使う。
interrupt は保留中の承認を解決し、取り消し、プロンプト自身の相関結果が
戻るまでターンを開いたままにする（`CHANGELOG.md` `[26.9.0]`）。
無言のキャンセルを確定したプロバイダー受信とみなさないこと。

## 関連

- [最初の1時間](/how-to/first-hour/) — セットアップトークン、AAL3、Claude 資格情報ソース。
- [Olivares で Claude Code を実行する](/how-to/run-claude-code-with-olivares/) — 同居トポロジ。
- [Codex を統合する](/how-to/integrations/codex/) / [Grok Build を統合する](/how-to/integrations/grok/) — コネクターと PEP hook。
- [ライブ運用とセッション](/reference/modules/ii-sessions/) — `live_ref` と帰属。
- [設定](/reference/configuration/) — ドライバー固定変数。
- [CLI リファレンス](/reference/cli/) — `olivares agent session *`（バイナリから生成）。
