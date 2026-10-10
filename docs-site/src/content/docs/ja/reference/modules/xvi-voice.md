---
title: "モジュール XVI — voice とリアルタイムエージェント"
description: >-
  会話型/リアルタイムエージェントの観測・ガバナンスプレーン。デフォルト DENY の
  ポリシーの下で、誰がどのモデルとプロバイダーで voice セッションを開いてよいかを
  ガバナンスし — 音声やトランスクリプトの内容を一切禁じたうえでセッションの
  メタデータを追跡する。
---

モジュール XVI は**会話型およびリアルタイムエージェント**をガバナンスする。これは
**観測・ガバナンス**プレーンである。voice SDK（Realtime API、WebRTC、ASR、TTS）を
再実装することは**なく**、自らメディアストリームを開くことも決してない。本モジュールは
*誰が*、*どのモデルとプロバイダー*で、*どのポリシー*の下で voice セッションを
開いてよいかを判断し、そのセッションのメタデータを追跡する — その内容は決して
追跡しない。

## 概要

voice インターフェイスを開くことは、自由な操作ではなく**特権アクション**として
扱われる。ポリシーは**デフォルト DENY** である。許可するポリシーのないセッションは
拒否される。open は**二相**であり、[承認ゲート](/ja/how-to/govern-and-approve/)を通じて
**human-in-the-loop でゲートされる**。これは `plan_hash` にバインドされるため、承認が
より強力なモデルへ黙って格上げされることはなく（anti-TOCTOU）、**実際の principal**
（決して `system` ではない）に対して監査され、**追記専用**で証跡が残される。本モジュール
自身はプロバイダーを決して呼び出さない — 作動は別個のディスパッチシームを通じて
出ていく。

もう半分は**観測**である。本モジュールはセッションのメタデータのみを追跡する —
派生状態（live/idle/ended、活動の新しさから read 時に算出され、ライフサイクルの
カラムは保存しない）、ターン数、継続時間、レイテンシ（実サンプルからの正直な平均と
最大）、BCP-47 言語。ここからガバナンス **finding** を発火させる。テレメトリが
いかなるポリシーも許可しないエージェント/モデル/プロバイダーを名指しした場合の
ポリシー違反、レイテンシがポリシーの SLA を越えた場合の degraded-latency finding、
ゲートが配線されていない状態で open が試みられた場合の ungoverned-open finding —
このギャップは表面化され、open は依然として拒否される。

## 契約とエンティティ

本モジュールは共有データモデルに 3 つのエンティティを宣言する。

| エンティティ | 変更可能性 | 目的 |
|---|---|---|
| **session** | 変更可能（upsert） | セッションメタデータ。**内容ゼロ** |
| **policy** | 変更可能 | ガバナンス宣言 — 誰がどのモデル/プロバイダーで open してよいか（デフォルト DENY） |
| **decision** | **追記専用** | open/close 決定の不変台帳 |

ポリシーはエージェント、許可モデル、許可プロバイダー（それぞれ特定値または
ワイルドカード）でマッチし、オプションのセッション分数と latency-SLA の境界を持つ。
**マッチするポリシーがなければ DENY。** decision 台帳は各 `open_request`、`open`、
`close` を、そのポリシー判定、ゲートステータス、結果ステータスとともに記録する。read
アクセスは viewer ロール以上である。ポリシーの宣言とセッションの open は管理操作で
あり、テナントスコープかつ監査対象である。これらのモジュールルートは、
安定コア契約ではなく、別の **beta**
[module-route リファレンス](/reference/api-beta/) で公開されている — それらのフィールドレベルの
形状は製品の型付きインターフェイス内に存在する。金額はここには**なく**、コストは
FinOps（モジュール XI）が担う。

## 消費するものと生成するもの

本モジュールは deny-closed の取り込みシーム — 独自の `voice.telemetry.observed`
イベント — を所有し、これを通じて**インプロセス**のプローブがセッションメタデータを
供給しうる。ワイヤーは**構造的に最小データ**である。テレメトリパーサーは許可リストを
持ち、禁止されたキーを見つけると**イベント全体を拒否する**ので、音声、トランスクリプト
テキスト、ASR/TTS テキスト、プロンプト/レスポンスの内容、話者の PII が永続化される
ことは決してない。保持される唯一のトランスクリプトシグナルは、*外部*トランスクリプトの
**ロケーター**の一方向ハッシュである — トランスクリプトが存在することの証明であって、
トランスクリプトそのものではない。ガバナンス finding は commit 後に、ハッシュ化された
詳細を伴って [`finding.reported`](/ja/reference/events/) として発行される。

## Actuate ステータス

統制された open は **live** で dispatch されます。オペレーターが音声 dispatcher をプロビジョニングすると、承認済み open は**サーバー側の一時認証情報**を発行し、それと接続先情報を返します。音声とターン検出はオペレーターのセッション設定から供給され、設定されたモデルは要求モデルより優先されます。モデルの設定がなければ、dispatcher はテナントポリシーが許可する要求モデルを使います。プロバイダーのマスターキーはサーバーを離れません。プロビジョニングされていなければ dispatch seam は **deny-closed** であり、承認済み open も「宣言済み、未開始」と正直に記録され、偽装されません。

## 統制された open を設定してテストする

`olivares modules on voice` で既存のモジュールを有効にします。稼働するモジュール集合が変わる場合、エンジンは選択を保存して 1 回再起動します。`olivares modules ls` で稼働状況を確認できます。モジュール仕様により voice は FinOps と governance を必要とします。voice をオフにしても、ポリシー、セッションメタデータ、決定台帳は保持されます。

dispatcher はエンジンホスト上の `OLIVARES_VOICE_DISPATCH_CONFIG` でプロビジョニングします。値はオペレーター所有の JSON ファイルの絶対パスです。ファイルを読めるのはエンジンアカウントのみにしてください。プロバイダーのマスターキーはこのファイルに置き、CLI 引数、ポリシー行、クライアント接続バンドルには置きません。OpenAI adapter の形式は次のとおりです：

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

エンジンサービスに環境変数を設定し、再起動します。指定されたファイルが読み取り不能または不正な JSON なら起動は失敗します。dispatcher 設定がなければ「宣言済み、未開始」の動作を維持します。オペレーターファイルは provider adapter とセッション設定を選び、テナントの voice ポリシーは要求されたエージェント、モデル、プロバイダーを別途認可します。両方で同じモデルとプロバイダーの参照を使ってください。

`olivares login` でサインインした後、ポリシーを宣言して承認を要求します：

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

最初の要求は `op_status: requested`、`approval_ref`、CLI 終了コード 7 を返します。メディア接続を開かず、プロバイダーの認証情報も発行しません。必要な独立した承認者に、governance の承認ページまたは `olivares governance approvals approve <approval-ref>` でその参照を承認してもらいます。デフォルトのローカル承認ブリッジを経由する新しい要求では、要求者は同じアカウントの別の認証情報を使っても自身の要求を承認できません。同じ open を `--approval-ref <approval-ref>` とともに繰り返します。ポリシー拒否または承認保留は 403 と CLI 終了コード 3、adapter 障害は 502 を返します。予算と estate-stop の検査も引き続き適用されます。

設定済み要求が成功すると `op_status: dispatched` を返します。`dispatch_ref` は、有効期間の短い `credential`、`connect` 接続先、`transport`、モデル、有効期限を含む JSON 文字列です。このレスポンスは認証情報として扱い、レポートやログに貼り付けないでください。OpenAI ではクライアントが短期認証情報で返された `connect` URL に SDP offer を送って交換し、以後 WebRTC メディア接続を所有します。認証情報の発行だけではメディア接続の成立は証明できません。決定台帳は接続認証情報ではなく、認証情報を含むバンドルの SHA-256 フィンガープリントを保持します。古い保存済みバンドルも読み取り時にフィンガープリント化し、既存の追記専用行は書き換えません。通常の provider handle は値を保持します。

保持されたメタデータと決定を確認します：

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

エンジン再起動の前後で同じデータディレクトリを使ってください。ポリシーと追記専用の決定は、再起動後や voice をオフにして再度オンにした後も利用できます。プロバイダーの認証情報は引き続き別途プロビジョニングが必要です。上記の JSON コマンドは、API と同じテナントスコープの `/v1/m/voice` ルートを使います。

:::caution[正直な限界]
- **承認の帰属にはスコープがある。** デフォルトのローカルブリッジは、新しい人間起点の open で認証済み要求者を保持します。既存の承認は保存済みの帰属を維持します。明示的に設定されたサービストークン承認ブリッジは要求をサービス認証情報に帰属させ、起点となった個人の分離について同じ保証は提供しません。
- **観測には設定済みの生成元が必要。** 任意の OpenAI Realtime SIP call plane は `OLIVARES_VOICE_CALL_CONFIG` で webhook 検証、テナントとプロジェクトの帰属を設定し、dispatcher 設定内の provider 認証情報を併用します。この設定またはプロセス内のテレメトリ生成元がなければ、観測側は空のままです。WebRTC 認証情報の発行はターン数やレイテンシを入力しません。イベント RPC を公開しない gRPC コントロールプレーンを通して、プロセス外プラグインがモジュールのイベントを発行することはできません。
- **発行済みセッションのメディアはクライアントが所有する。** このモジュールは WebRTC クライアントを実装せず、クライアントの音声接続も閉じません。任意の SIP call controller は別の経路です。合成音声によるローカルのプロトコルテストは、ベンダーの音声、課金、SIP 観測、メディア切断を検証したことにはなりません。
- **コンソールのスコープは別。** voice ビューはポリシーを編集し、セッション、決定、メタデータストリームを表示します。dispatcher のプロビジョニングとクライアントのメディア接続はこのビューの外であり、API や CLI の確認はブラウザー操作の検証にはなりません。
- **内容は一切、決して残さない。** これは設定ではなくワイヤーの確固たる性質である。
  スキーマには content カラムがなく、パーサーは未知のキーを拒否する。レイテンシは
  実サンプルからの正直な平均/最大として示される — 捏造された p50/p95 は決して用いない。
- **「stall」finding は存在しない。** voice セッションの終了は正常な沈黙である
  （完了したエージェントと同様）。正直なベースラインがなければ、stall finding は偽陽性に
  なるので、意図的に省かれている。
- **Pre-1.0。** プラットフォームの多くと同様、本モジュールは深さの面で設計段階にある —
  [正直さと限界](/ja/start/honesty-and-limits/) を参照。
:::

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/) — モジュール XVI の位置づけとその actuate ステータス。
- [イベントバスリファレンス](/ja/reference/events/) — `finding.reported` が voice の finding を運ぶ。
- [モジュール IV — オーケストレーション](/ja/reference/modules/iv-orchestration/) — 姉妹となるディスパッチシーム（ライブ実行）。
- [モジュール X — モデル＆プロバイダールーティング](/ja/reference/modules/x-models/) — ポリシーがどのモデルを許可してよいか。
- [Govern and approve](/ja/how-to/govern-and-approve/) — 実践における二相の open ゲート。
- [正直さと限界](/ja/start/honesty-and-limits/) — observe/govern/actuate の区分。
