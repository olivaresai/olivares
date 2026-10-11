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

この挙動の出典: `CHANGELOG.md` の `[26.9.0]`（プロバイダープロファイル、
Codex ドライバー、Grok ドライバー）、生成された
[コンソール](/reference/console/) と
[設定](/reference/configuration/) リファレンス、
`cmd/olivares/sessionruntime.go`、`web/src/features/agentops/types.ts`。

## 前提条件

起動の前に完了してください。欠けている項目はフォールバックではなく拒否です。

1. Olivares AI がインストール済みで、最初の管理者が存在する。
   セットアップトークンは [最初の1時間](/how-to/first-hour/) を参照。
   管理操作の追加認証（`admin_step_up`）はデフォルトで `none` です。
   管理者が `totp` または `passkey` を有効にした場合、特権操作の前に
   そのポリシーの要件を満たしてください（`core/api/middleware.go` `requireStepUp`）。
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
4. 対応するドライバーの実行ファイルが **このノード** に必要です。明示的に
   固定したバイナリが優先され、固定していなければ起動時にエンジンが検索します。
   ドライバーごとの準備状態とポリシーの確認は引き続き適用されます。

| ドライバー | この環境変数を固定 | 未設定のとき |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | 検証済みの最新の管理対象インストール、次にエンジンの `PATH` 上の `claude`。 |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | 検証済みの最新の管理対象インストール、次にエンジンの `PATH` 上の `codex`。 |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | 検証済みの最新の管理対象インストール、次にエンジンの `PATH` 上の `grok`。 |

値はこのノードで使用する公式実行ファイルを固定します。固定していない場合、
ツールをインストールするとエンジンの再起動なしで利用できます。管理対象
インストールも `PATH` 上の実行ファイルもなければ、起動は拒否されます。
`OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` も同じ順序で検索します。

プロバイダーを指定しない `managed_injection` の Claude プロファイルでは、
`OLIVARES_SESSION_RUNTIME_WIF` または `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` が
ホストの推論資格情報を提供します。プロバイダーに紐づいたプロファイルはその資格情報を使い、
取得できない場合はホストの資格情報に切り替えずに起動を拒否します。
`provider_account_home` のプロファイルは許可されたツールのログインを使い、
どちらの変数も必要ありません。
[プロバイダーを追加する](/ja/how-to/add-a-provider/) を参照してください。
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

起動ダイアログは **active** なプロファイルを提示します。active なプロファイルが
1 つだけならそれが事前選択されます。複数ある場合はどれも選択されず、**Start**
が選択を求めます。workspace と template の選択はクリアできるが、
プロファイルはできません（`CHANGELOG.md` `[26.9.0]` Fixed）。

### コンソール

1. **セッション**（`/sessions`）を開く。`/agentops` も同じ画面を開く
   （[コンソールリファレンス](/reference/console/)）。
2. **New session** を開き、**Advanced launch options** を開く（ツールが
   準備済みなら **More options** の中）。
3. **Provider profile**（`agentops.create.profile`）を確認し、必要なら
   **First message** を入力する。
4. 任意で **Advanced options** で workspace、template、model、effort を設定
   する。Grok では model と effort は公式エージェントフラグ上の
   プロバイダー所有の開いた文字列のまま（`CHANGELOG.md` `[26.9.0]`）。
5. **Start** を押す。開始できない間は、その下の行に足りないものが表示される。
   投稿されるのはプロファイルの **参照** だけ。サーバーがホームを解決する。

### ハンドオフに示された作業を開く

ワークツリーはファイルを分離しますが、リポジトリの git メタデータは共有します。ハンドオフの内容には、作業の所在として任意の `branch` と `sha`（完全なコミット ID）を指定できます。API、または両方を含む JSON を受け取る `olivares message handoff offer --context-file` で提供します。どちらも指定しないハンドオフの動作は変わりません。

指定されたハンドオフを読むと、パネルにブランチとコミットがテキストで表示されます。**新しいセッションワークツリーで開く**を選ぶと、ワークツリーが選択され、開始地点が表示された起動画面を開きます。コミットを保持するリポジトリのワークスペースを選ぶまで開始は待機します。フォルダーの選択を解除しても要求は保持されます。通常のセッションを開始するには、ワークツリーの選択を明示的に解除してください。CLI では次を使います：

```sh
olivares session start . --worktree-from <commit or branch> --name review
```

`--worktree-from` は `--worktree` を含意します。セッションはそのコミットから自身の新しいブランチで作業するため、送信者のブランチやあなたのチェックアウトは移動しません。ワークスペースのリポジトリにコミットがなければ、何も作成する前に起動が拒否されます（422）。先にそのリポジトリへ fetch してください。どのブランチ、タグ、リモートブランチも保持しないコミットも拒否されます。セッションの**ブランチの変更**ペインは、そのブランチにあり、ワークスペースの現在のコミットにない変更を一覧にし、各パスのマージベースとブランチ先端のテキストを並べて開きます。表示するのはコミット済みの作業のみで、ワークスペースの許可サブパスと DLP ポスチャに従います。未コミットの編集は**変更**にあります。

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

[CLI リファレンス](/reference/cli/) のとおり `--server`、`--tenant`、
`--token-file`（またはアクティブなクライアントコンテキスト）を付ける。
このリリースの isolation は `native`。`container` と `sandbox` は実行レコードを
作成する前に HTTP 422 で拒否される。組み込みランナーでは `native` を選ぶ。

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

- [最初の1時間](/how-to/first-hour/) — セットアップトークン、管理操作の追加認証、Claude 資格情報ソース。
- [Olivares で Claude Code を実行する](/how-to/run-claude-code-with-olivares/) — 同居トポロジ。
- [Codex を統合する](/how-to/integrations/codex/) / [Grok Build を統合する](/how-to/integrations/grok/) — コネクターと PEP hook。
- [セッションランタイム API](/reference/session-runtime-api/) — 一覧、attach、input、stop。Community PTY とエディション境界。
- [ライブ運用とセッション](/reference/modules/ii-sessions/) — `live_ref` と帰属。
- [設定](/reference/configuration/) — ドライバー固定変数。
- [CLI リファレンス](/reference/cli/) — `olivares agent session *`（バイナリから生成）。
