---
title: "Olivares AI を使い始めて最初の1時間（v26.9.1、出荷時の状態）"
description: >-
  公開版 v26.9.1 バイナリをクリーンインストールした後、最初の1時間で実際に
  できること: セットアップトークン、AAL3 の壁、パスキー登録、プロバイダープロファイル
  によるセッション起動、デプロイ、ナレッジ、および Codex と Grok の PEP hook。
---

このページでは、**出荷時の v26.9.1** について説明します。計画中の初回実行
ウィザードでも、モックアップのスクリーンショットでもありません。以下の各手順は、
それを成立させるファイルまたは環境変数とともに、公開バイナリが現在実際に行う
ものです。製品が拒否する箇所は、そのとおり明記します。

番号付きの事実は、2026-09-04 に公開バイナリをクリーンインストールして測定
しました。このページでは、その測定結果と、処理が到達するコードを示します。

バイナリをホストへ配置する方法については、
[Olivares AI をセルフホストする](/how-to/self-hosting/) と
[リリースを検証する](/how-to/verify-a-release/) を参照してください。このページでは、
`https://olivares.ai/install` を shell に pipe するよう案内していません。バイナリを
ホストへ配置した後に推奨される最初のコマンドは `olivares quickstart` です。

:::note[これに該当しないもの]
`--seed-demo` は製品ツアーではありません。2026-09-04 に公開バイナリを
クリーンインストールして測定したところ、seed 付きで起動してもコンソールの
**54 ルート中 36 ルート**は空のままでした。この数字は測定日のセンサスです。
このツリーの生成された
[コンソールリファレンス](/reference/console/) は **75 ルート**を列挙します。
このページは v26.9.1 で `--seed-demo` 後の空タブを再計測しません。
デモ環境がデータを入れるのは、
[ゼロから読み取り/書き込みアクセスグラフへ](/tutorials/zero-to-graph/) のアクセス
グラフ手順です。コンソールの残りの部分にはデータを入れません。「製品を探索する」
目的では使用しないでください。
:::

## 1. 推奨される起動方法: `olivares quickstart`

新しいデータディレクトリには **デフォルトの認証情報がありません**。推奨される
最初のコマンドは `olivares quickstart` (`cmd/olivares/cmd_quickstart.go`) です。
これは、安全なデフォルトを適用した `serve` です。TLS は有効、デフォルトの認証情報は
なく、セットアップトークンは単回使用です。デフォルトの待ち受けアドレスは `:8443`
— すべてのインターフェースです。これはサーバーだからです（`cmd/olivares/binddefaults.go`）。2026-09-04 に、実際の TLS を使って公開バイナリを
クリーンインストールし、**:8460** での実行も含めて測定しました。パネルの文面は
同じです。

ウェルカムパネル (`announceQuickstart`, `:154-163`) には番号が付いています。
エンジンはこのバインドに対して `https://localhost:8443` を表示し、トークンの下に
このホストが応答する他のすべてのアドレスを列挙します。**パスキーのセレモニーに
IP アドレスを使用しないでください。** ブラウザーは IP アドレスを WebAuthn RP ID として拒否
します (`SecurityError`)。製品はリクエストのホスト名から RP ID を導出します
(`core/api/handlers_webauthn.go:33-50`)。パスキーを登録する前に、コンソールを
`127.0.0.1` ではなく `https://localhost:PORT`（または実際のホスト名）で開いて
ください。`--listen` を渡していなければ、`PORT` は `8443` です。

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

バナーはデフォルトのバインドに対して `localhost` を表示し、トークンの下にこのホストの
他のアドレスを列挙します。パスキーのセレモニーには、アドレスではなく名前が必要です。アドレス
バーでホスト部分を `localhost` に書き換えてください。

トークンのプレフィックスは `olst_` です
(`cmd/olivares/e2e_binary_test.go` は `olst_[A-Z0-9]+` に一致させます)。
コンソールのページは `/setup` です。ウィザードが POST する API は次のとおりです。

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` も同様のバナーを表示します (`cmd/olivares/cmd_serve.go` の
`announceSetup`)。最初の1時間には `quickstart` を使用してください。URL、自己署名
証明書の警告、1回限りのトークンが1つのパネルにまとまっています。その後、
`POST /v1/auth/login` でログインします。これで AAL1 のパスワードセッションが
得られます。

`README.md` とそのウェルカムパネルには、トークンと URL が記載されています。
パスキー登録については **記載されていません**。それは次の手順で、ボタンを押した
後にコンソールから案内されます。

## 2. AAL3 の壁 — コンソールが案内するのはボタンを押した後であり、その前ではない

セットアップ後、**セッションが AAL3 になるまで、ソース、コネクタ、ワークスペース、
シークレットの作成は拒否されます**。この gate は
`core/api/middleware.go:310` の `requireAAL3` です。AAL3 未満の principal には
`403
step_up_required` が返されます。**21 か所の呼び出し元**がこの gate を通ります。
このツリーにある書き込み経路には、次のものが含まれます。

| サーフェス | Handler | ファイル |
|---|---|---|
| ソース一覧の登録/削除/再読み込み | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| コネクタの書き込みとテスト | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| シークレットの登録/削除 | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| ワークスペースの作成/更新 | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| メンバーのオンボーディング | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**標準インストールでは、PIV/CAC を使ってもそこには到達できません。** 未設定の場合、
PIV ルートは **501** `piv_not_configured` を返します
(`core/api/handlers_piv.go`, `core/api/errors.go`)。

### コンソールが実際に行うこと

コンソールは登録画面へ **実際に** 案内します。ただし、その動作は **リアクティブ**
です。

1. 特権操作を実行しようとします。step-up パネルに
   **セキュリティキーで認証する** と表示されます
   (`web/src/features/identity/i18n/en.json` の `assurance.authenticate`、
   `web/src/features/identity/assurance.tsx:230-239` のボタン)。
2. クリックすると `POST /v1/auth/webauthn/authenticate/options` が呼び出されます
   （登録ではなく step-up）。パスキーがない場合、エンジンは **400**
   `no_webauthn_credential` を返します
   (`web/src/features/identity/api.ts:260-265` の `isNoWebAuthnCredential`)。
3. その後、パネルはまず **特権ログイン** タブで登録するよう案内します
   (`assurance.tsx:160-168` → `assurance.unenrolled`)。この文は **リンクではありません**。

手動で次のように移動します。

1. コンソールを `127.0.0.1` ではなく **`https://localhost:PORT`** で開きます
   (§1 を参照)。
2. `/identity` を開きます (`web/src/features/registry.tsx` —
   `path: '/identity'`)。
3. **特権ログイン** タブ (`tabs.login`) を開きます。
4. **パスキーを登録** (`passkeys.register`) を選びます。
   **プラットフォーム認証器で十分です**（ブラウザーまたは OS のプロンプトを使用でき、
   物理キーは不要です）。サーバーは **ユーザー検証を必須** とします
   (`core/auth/webauthn.go:74-85`, `UserVerification: VerificationRequired`)。
   2026-09-04 に公開バイナリをクリーンインストールし、WebAuthn セレモニーを最後まで
   実行して測定した結果は、登録 **200**、認証 `{"aal":3}`、その後の
   `PUT /v1/console/connectors` が **200** でした。

`POST /v1/auth/webauthn/register/options` は **セッション principal** として認証され、
`requireAAL3` を **呼び出しません**。成功時は `{publicKey: …}` とともに **200** を
書き込みます (`core/api/handlers_webauthn.go:76-88`)。
`POST /v1/auth/webauthn/register` でセレモニーを完了し、step-up を再試行してください。

`README.md` と `olivares quickstart` のウェルカムパネルには、特権ログインタブの
記載が **ありません**。コンソールがそのタブを示すのは、この 400 の **後だけ** です。

identity パネルは AAL3 (NIST SP 800-63B-4) と PIV/CAC (FIPS 201-3) を
**目標規格** として示し、**認証取得を主張しない** ことも明記しています
(`targetStandardsNote`)。このページも認証取得を主張しません。

### コネクタを追加: 壁が表示され、入力欄はない

**コネクタを追加** (`web/src/features/console/i18n/en.json` の
`connectors.add`) を選ぶと、`ConnectorForm` を
`<RequireAssurance minAal={AAL.HARDWARE}>` で囲んだダイアログが開きます
(`web/src/features/console/connectors-tab.tsx:348-357`)。AAL3 未満ではフォームは
マウントされません。2026-09-04 に公開バイナリをクリーンインストールして測定した
ところ、このダイアログには **入力欄がなく** (`inputs: []`)、step-up パネルだけが
ありました。種類のカタログは AAL1 で表示できます
(`ConnectorCatalog` は gate の外側、同じファイルの `:341-346`) が、種類の
**追加** は AAL1 ではできません。

### `/workspace` とプロトコルバインディング: ワークスペース切り替えはない

`/workspace` (`registry.tsx` の `path: '/workspace'`) と
`/communications/protocol-bindings` にはワークスペースが必要です。クリーン
インストールにはワークスペースが1つもなく、AAL3 になるまで作成できません
(`handleCreateWorkspace`)。ワークスペースが1つ以下の場合、`WorkspaceSwitcher` は
**レンダリングされません**
(`web/src/components/layout/workspace-switcher.tsx:43-44`:
`if (workspaces.length <= 1) return null`)。トップバーに残るのは
**組織を切り替え** (`web/src/lib/i18n/locales/en/auth.json` の `tenant.switch`) です。
クリックできるワークスペース選択機能はありません。

## 3. コンソールから Claude Code セッションを起動する

コンソールが `claude` プロセスを起動できるのは、**ホスト**に推論用認証情報の
ソースがある場合だけです。ソースがなければ、stream-json の起動は deny-closed で
拒否されます。2026-09-04 に公開バイナリをクリーンインストールして測定した結果は
**HTTP 503** でした。

次のうち **1つ** を設定します。

- `OLIVARES_SESSION_RUNTIME_WIF` — プロセス内での WIF 発行 (`cmd/olivares/sessionruntime.go`, `cmd/olivares/wifbroker.go`)
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — ローテーションされた短寿命トークンファイルへのパス

composition root は、接続されたソースをログに記録します。ソースがなければ、次を
記録します。

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go:74-76`)。関連する任意の変数は、
`OLIVARES_SESSION_RUNTIME_WIF_RULE`, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`,
`OLIVARES_SESSION_RUNTIME_BASE_URL`, `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` です
(`cmd/olivares/config_registry.go` および
[設定](/reference/configuration/) に記載)。

**Operate** の共同デプロイ構成（`claude` と同じホスト）は、
[Olivares とともに Claude Code を実行する](/how-to/run-claude-code-with-olivares/) を
参照してください。OTLP の observe 経路については、
[Claude Code を接続する](/how-to/connect-claude-code/) を参照してください。

### Provider key はセッションを起動するものではない

**モデル → Provider key** は、**参照**を管理するためのガバナンスレジストリです。
フォームは **シークレットを一切受け付けません**。

> このフォームはシークレットを一切受け付けません。Olivaresは参照とマスクされた
> ヒントのみを保存します。

(`web/src/features/models/i18n/en.json` の `keys.dialog.noSecretNote`)。このタブに入力しても、
`OLIVARES_SESSION_RUNTIME_WIF` と `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` のどちらも
満たしません。セッション起動は有効になりません。

## 4. Executor をプロビジョニングするまでデプロイ計画は 503 になる

デプロイモジュールの plan/apply に対する `POST` は、ホストが JSON ファイルを
`OLIVARES_DEPLOY_EXECUTOR_CONFIG` に設定するまで **503** を返します。設定がなければ、
モジュールは未接続の deny-closed executor を維持します
(`cmd/olivares/deployexec_load.go:16-20`)。ファイルを読み取れない場合は **起動に
失敗します**。

JSON オブジェクトは `cmd/olivares/deployexec_load.go:26-42` の
`deployExecutorConfig` です。任意の backend block は、`tofu`, `terraform`,
`gitops`, `k8s`, `docker`, `nomad`, `crossplane` と、`credential`, `blast_radius`,
`identity_binding`, `drift` です。

この環境変数は [設定](/reference/configuration/) に記載されています
(`docs-site/src/content/docs/reference/configuration.md:152`)。最初の1時間にコンソールで
クリックする項目としては記載されていません。このファイルの代わりになる選択肢は
UI に存在しません。

モジュールカタログでは、Deployment の actuate は **on-demand (503)** と記載されて
います ([モジュール](/reference/modules/overview/))。これは同じ事実を示す行です。

## 5. 公開ナレッジベースを検索するか、エージェント identity で検索する

デフォルトの embedder は、外部通信を行わない **LocalHashEmbedder** です。起動時には、
検索が **セマンティックではなく字句ベース** であることと、
`embed_model=local-hash` が警告されます (`cmd/olivares/claude_inference.go`,
`cmd/olivares/knowledgestatus.go`)。guard が許可すれば、字句検索でも chunk が返されます。

認証済みのエージェント identity がない場合、retrieval guard が許可するのは
**公開され、制限のない**コンテンツだけです (`modules/knowledge/query.go:120-127`)。
**内部** KB に対する人間の REST `/query` は拒否されます。2026-09-04 に公開バイナリを
クリーンインストールして測定したところ、**公開** KB は **1 件の結果**を返しました
(score 0.738)。公開ナレッジベースを検索するか、エージェント identity を使って
検索してください。

拒否された query は、すべてが除外された場合でも、現在は `excluded_chunks: 0` と
報告します。このカウンターが増えるのは、オペレーターが定義した
`excluded_sources` floor による除外だけです (`query.go:256-284`)。clearance または
ACL による拒否では決して増えません。

## 6. Codex と Grok のセッション: プロバイダープロファイル、その後の CLI hook

v26.9.1 は公式 Codex CLI と公式 Grok CLI をセッションドライバーとして運用します。
Claude Code に加えてです（`CHANGELOG.md` `[26.9.0]` Added）。コンソールは
**Provider profiles**（`/provider-profiles`、`sessions:profile:read`）と
**Source bindings**（`/provider-bindings`、`sessions:profile-binding:read`）で
それらの起動を管理します。両方のルートは生成された
[コンソールリファレンス](/reference/console/) にあります。

プロファイルは 1 つの実行環境上の 1 つの構成済みプロバイダーインスタンスの
永続的な識別です。認証済みプロバイダーアカウントではありません
（`web/src/features/agentops/types.ts`）。登録は、このノードに既にあるホームを
検証します。サーバーはインストール、作成、ログインをしません。

### 起動の前にホストへドライバーを登録する

準備状態はドライバーごとです。共有スイッチはありません
（`cmd/olivares/sessionruntime.go`）。対応する環境変数を設定すると、その
ドライバーがこのノードに登録されます。

| ドライバー | 環境変数 | 未設定のとき |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`（既定 `claude`） | Claude 経路は既定名を使う |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex プロファイルは観測可能のままで起動できない |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok プロファイルは観測可能のままで起動できない |

値は固定した公式バイナリです。エンジンは `PATH` から `codex` や `grok` を
解決しません。出典: [設定](/reference/configuration/)。

Claude の起動には §3 と同じ推論資格情報ソースがなお必要です。Codex と Grok は
プロファイルの AUTHORIZED な `auth_source`（`provider_account_home` または
`managed_injection`、フォールバックなし）で認証します。
`CHANGELOG.md` `[26.9.0]` は認証済み公式 Grok アカウントとの互換性を
**主張しません**。

起動ダイアログはプロバイダープロファイルを要求します。本番の作成エンドポイントは
`provider_profile_ref` を要求します。省略すると古いリクエスト本体のままになり、
この API はそれを拒否します（[CLI](/reference/cli/)
`olivares agent session create --provider-profile`）。

プロファイル登録、ソースのバインド、起動、中断、停止:
[プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)。

### CLI のみのまま残るもの（hook と管理設定）

これらのコマンドはコンソールからのセッション起動ではありません。今も存在します。

| コマンド | 内容 | ソース |
|---|---|---|
| `olivares codex` | Policy JSON から Codex の `requirements.toml` / `managed_config.toml` を生成します。**ファイルを書き込みますが、コントロールプレーンとは通信しません。** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Codex が呼び出す deny-closed PEP hook（stdin → コントロールプレーン → イベント形式の stdout）。 | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Grok Build が呼び出す deny-closed PEP hook。deny が **ブロック**するのは `pre_tool_use` の場合だけです。 | `cmd/olivares/cmd_grokhook.go` |

Codex hook のインストール（このファイルのコメントにある Codex の `hooks.json` 形式に
対して検証済み）では、`command` は **文字列**でなければなりません。その値は
`olivares codex-hook` です。環境変数:
`OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT`（任意で agent/org/account）。

Grok hook の環境変数: `OLIVARES_GROK_HOOK_URL`,
`OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`。Grok は
`~/.grok/disabled-hooks` を使い、名前で hook を無効化できます。これはコンソールの
制御ではありません。

「Codex を接続」または「Grok を接続」**ボタン**を探さないでください。
コネクター登録は **Control console → Connectors**（種類 `codex` または `grok`）です。
それは
[Codex を統合する](/how-to/integrations/codex/) と
[Grok Build を統合する](/how-to/integrations/grok/) の観測/統治経路です。
セッションドライバーは登録しません。

## 7. `--seed-demo` はコンソール全体にデータを入れない

`olivares serve --seed-demo` は、アクセスグラフのチュートリアルを実行できるように
デモ環境を読み込みます。2026-09-04 に公開バイナリをクリーンインストールして測定
したところ、その環境でもコンソールの **54 画面中 36 画面**は空のままでした。
この数字は測定日のセンサスです。生成されたコンソールリファレンスは今日
**75 ルート**を列挙します。`--seed-demo` は
[ゼロから読み取り/書き込みアクセスグラフへ](/tutorials/zero-to-graph/) の手順だけに
使ってください。`--seed-demo` の後に空のタブがあっても壊れたインストールだと判断
せず、このフラグを製品ツアーとして扱わないでください。

## 関連ページ

- [誠実性と制限](/start/honesty-and-limits/) — ドキュメントで主張できる内容。
- [Olivares AI をセルフホストする](/how-to/self-hosting/) — バイナリの実行方法。
- [プロバイダーセッションを運用する](/how-to/operate-provider-sessions/) — プロファイル、ドライバー固定、起動、中断。
- [設定](/reference/configuration/) — 上記の環境変数。
- [モジュール](/reference/modules/overview/) — on-demand (503) と稼働中の actuate の違い。
