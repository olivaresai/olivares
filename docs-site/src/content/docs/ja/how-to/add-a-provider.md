---
title: プロバイダーを追加して Claude Code・Codex・Grok を起動する
description: >-
  API キーをコントロールプレーンに登録し、何も消費せずに接続をテストし、
  プロバイダープロファイルに紐づけて最初のセッションを起動します
  — コンソールからも CLI からも。
---

このページは**プロバイダー**プレーンの最初の 1 時間です。API キーをどこに置くのか、
それが動くとどうやって分かるのか、そしてそれを使ってセッションがどう起動するのか。

26.10 でも、サーバー上の環境変数は最初の問いへの答えの 1 つであり、その変数は今も動き
ます。ただし、それはもう唯一の道ではなく、新しい運用者が最初に取る道でもありません。

## 3 つの言葉の意味

それぞれ 1 文で説明します。製品がこれらを混同していたこと、そして製品が返す拒否が
これらを別々の名前で呼ぶことが理由です。

| 言葉 | 何であるか |
|---|---|
| **プロバイダー** | API キーまたはローカルモデルのエンドポイント、任意の `base_url`、そして種類（`anthropic`、`openai`、`xai`、`gemini`、`openai_compatible`、`ollama`）。 |
| **プロバイダープロファイル** | このマシン上の識別子です。どの公式 CLI が、どの設定ホームとユーザーホームの下で動くか。 |
| **セッション** | 起動された 1 つの子プロセスです。1 つのプロファイルの下で、1 つのプロバイダーの資格情報を使います。 |

## 1. プロバイダーを追加する

### コンソールから

1. **プロバイダー**（AI → 環境 → プロバイダー）を開きます。
2. **プロバイダーを追加** を選びます。
3. プロバイダーを選び、選択肢の中で見分けられる名前を付け、キーを貼り付けます。自分
   のゲートウェイを指す場合を除き、エンドポイントは空のままにします。
   `openai_compatible` のプロバイダーはエンドポイントが必須です。想定できる公式の
   エンドポイントが存在しないからです。
4. 確定します。

エンジンはキーを保存時に封印し、4 文字のヒントを返します。**キーは二度と返されません。**
書き込んだ直後であっても同じです。失った場合は交換してください。取り戻す読み取りは
存在しません。

### CLI から

```sh
# キーは標準入力から読み取ります。フラグの値になることは決してありません。フラグは
# 資格情報をシェルの履歴とプロセステーブルに残します。
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# あるいは自分のシェルの環境変数から:
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. 接続をテストする

```sh
olivares provider test prv_01J8ABCDEF
```

このテストは、プロバイダーにどのモデルを提供しているかを尋ねます。**補完は一切送信せ
ず、何も消費しません。**

答えは 3 つのうちの 1 つで、それぞれ別の問いです。

| 結果 | 意味 | すべきこと |
|---|---|---|
| `ok` | プロバイダーが応答し、資格情報を受理しました。 | 何もありません。紐づけてください。 |
| `refused` | プロバイダーが応答し、資格情報を拒否しました。 | キーを交換してください。 |
| `unreachable` | 応答が得られませんでした。 | エンドポイント、ネットワーク、プロキシを確認してください。**これはキーについて何も示しません。**再生成しないでください。 |

テストしていないプロバイダーは **未テスト** と表示され、動作しているとは決して表示さ
れません。資格情報の登録は意図であり、テストは事実です。

## 3. プロファイルを登録してプロバイダーを紐づける

明示的に指定するホームのパスは、コントロールプレーンを動かしているマシン上に
あらかじめ存在している必要があります。サーバーはそこで検証し、明示的に指定された
ホームが欠けていても作成しません。空の代替ホームは、誰も設定していない
プロバイダー識別子をセッションに与えてしまいます。

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude (work)" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` は、子プロセスのプロバイダー識別子がどこから来るかを決めます。2 つの
値はフォールバックの連鎖ではありません。

- `provider_account_home`: プロファイル自身のホームに保存済みのログイン。Olivares は
  何も注入せず、そのファイルを読むこともありません。
- `managed_injection`: エンジンが供給する資格情報。プロバイダーが紐づいていれば、その
  プロバイダーのものです。

このショートカットは検出・登録・プロバイダーの紐づけをまとめて行います。
`--config-home` と `--user-home` のどちらも指定しない場合、エンジンがホームを管理します。
`--provider` がなければ、そのドライバーの新しいセッションが使うプロファイルを選びます。
`--provider` があれば、専用のホームを持つプロファイルを作成します。

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

このショートカットを使う前に、**AI ツール**からツールをインストールしてください。
プロバイダーのアカウントへのログインは行いません。

あとから紐づける（または紐づけ直す）には:

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

エンジンは、プロファイルのドライバーが読めない資格情報を拒否します。Claude のプロファ
イルに OpenAI のキーを紐づけると、両方の名前を挙げた拒否になります。紐づけの時点でも、
起動の時点でも同じです。ハンドシェイクの途中で失敗するセッションにはなりません。

| プロバイダーの種類 | 読み取れるドライバー | 独自の `base_url` を指定する場合 |
|---|---|---|
| `anthropic` | `claude`, `opencode` | `claude` |
| `openai` | `codex`, `opencode` | `codex` |
| `xai` | `grok`, `opencode` | `grok` |
| `gemini` | `gemini-cli` |  |
| `openai_compatible` | `codex` | `codex` |
| `ollama` | `codex`, `opencode` | `codex`, `opencode` |

`anthropic`、`openai`、`xai` では、OpenCode は提供元のエンドポイントのみを受け付けます。`base_url` は空のままにしてください。`openai_compatible` のプロバイダーには `codex` を、`ollama` には `codex` または `opencode` を使います。

## 4. 最初のセッションを起動する

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` はプロファイルを明示的に選択します。プロファイルを使った起動が
有効な場合、省略するとエンジンが既定のツールである Claude Code のプロファイルを
解決します。ツール自体でログイン済みならそのログイン用のプロファイルを、そうで
なければ互換性のあるプロバイダーレコード用のプロファイルを再利用または作成します。
これには `sessions:profile:write` 権限が必要です。この権限がなければプロファイルを
明示的に選択してください。プロファイルの解決時と起動時の拒否は引き続き適用されます。

コンソールでは同じ道筋が **オンボーディング → エージェントと最初のセッション**、または
**セッション → 新しいセッション** です。

## 交換と失効

```sh
olivares provider rotate prv_01J8ABCDEF < new-key.txt   # その場で封印し直す
olivares provider rm prv_01J8ABCDEF --yes               # 取り消せません
```

交換は同じ参照の下で値を置き換えるので、紐づいたすべてのプロファイルはそのまま動き、
**次の**起動が新しいキーを使います。すでに走っているセッションは、起動時の資格情報を
保持します。以前の接続テスト結果は消去されます。すでに存在しない資格情報について測った
判定は、それを置き換えた資格情報の証拠にはなりません。

失効は封印された値を破棄し、今後のすべての起動を**名前を挙げて**拒否します。レコードと
紐づけは意図的に残します。黙って何も指さなくなったプロファイルは、誰も設定していない
プロファイルのように読めてしまうからです。ここでの失効は、プロバイダー側のキーを失効さ
せません。それは各社のコンソールで行ってください。

## エンジンが拒否するもの、そしてその理由

| 表示 | 意味 |
|---|---|
| `provider credentials cannot be stored on this deployment` | 封印された保管庫が接続されていません。エンジンは保護できないキーを保存する代わりに、保存を拒否します。 |
| `the provider connection test is not available` | プローブが接続されていません。**起動には影響しません。** |
| `a … credential is not readable by driver …` | 種類とドライバーが一致していません。上の表を参照してください。 |
| `the provider this profile is bound to is revoked` | 有効なプロバイダーを紐づけてください。 |
| `this launch has two endpoints` | 配備の推論ゲートウェイと、プロバイダー自身の `base_url` の両方が適用されます。どちらかを外してください。エンジンは選びません。 |
| `the registered provider credential could not be opened` | 起動は拒否されます。**ホストの資格情報にフォールバックしません。**それは、あなたが選んでいないアカウントでセッションを走らせることになります。 |

## 環境変数と、それが今も効く場所

プロバイダーを指定しない `managed_injection` の Claude プロファイルでは、
`OLIVARES_SESSION_RUNTIME_WIF` または `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` が
ホストの推論資格情報を提供します。プロバイダーに紐づいたプロファイルはその資格情報を使い、
取得できない場合はホストの資格情報に切り替えずに起動を拒否します。
`provider_account_home` のプロファイルは許可されたツールのログインを使い、
どちらの変数も必要ありません。

## 関連

- [プロバイダーセッションを運用する](/ja/how-to/operate-provider-sessions/)
- [最初の 1 時間](/ja/how-to/first-hour/)
- [CLI リファレンス](/ja/reference/cli/)
