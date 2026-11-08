---
title: "モジュール II — ライブ運用とセッション"
description: >-
  エージェントセッション単位のライブ運用オーバーレイ。現在のアクション、ライブな
  トークン/コスト、導出された Claude Code 状態、そしてリプレイ可能なタイムラインを、
  server-sent events でストリーミングする。何を導出し、何が誠実に空のままとなり、その限界は何か。
---

モジュール II は、エステートの**ライブ運用**ビューである。すなわち、各エージェント
セッションが今この瞬間に何をしているか、そのライブなトークン・コストの合計、導出された
Claude Code 状態、そして再構成可能なタイムラインである。モジュール I(インベントリ)が
永続的なエステートを具現化するのに対し、モジュール II は同じ観測ストリーム上で、セッション
ごとの**ライブ運用オーバーレイ**を保持し、そのストリームが誠実に運ぶものだけを表示する。

Olivares <!-- release -->0.9<!-- /release --> は公式プロバイダー CLI も
[プロバイダープロファイル](/how-to/operate-provider-sessions/) の下で所有する子として
**起動**します。その管理経路は同じモジュールです。オーバーレイを置き換えず、同じ
プロバイダーセッション id を告げる 2 つのホームをマージしません
（`CHANGELOG.md` `[26.9.0]` B1/B2）。

## それは何か

モジュール II はバス駆動の Core レイヤーモジュールであり、インベントリの兄弟である。
各セッションの外部参照をキーとするライブレコードを、協調的な観測ストリームから構築して
維持する。ポーリングは決して行わず、捏造も決して行わない。セッションごとに以下を追跡する。

- **現在のアクション**(最後に使用されたツール)と、それが触れたリソース/モード。
- **ライブなトークンとコストの合計**。コストサンプルから読み取られる(正規の
  コスト台帳と FinOps はモジュール XI であり、ここではない。これはライブな数値のみ)。
- **導出された Claude Code 状態**(`cc_state`)。
- 観測された各イベントが取り込み順に追加される**タイムライン**。

## コントラクトとエンティティ

本モジュールはテナントスコープのエンティティを 2 つ登録する。`sessions.live` は
セッションごとのライブレコードを保持する。すなわち、現在のアクション/リソース/モード、
モデル参照、ライブな入力/出力トークン、ライブコスト、イベント数とツールコール数、そして
初回/最終イベントのタイムスタンプである。`sessions.timeline` はイベントごとに 1 つの
リプレイ可能な行を保持し、取り込み順に並べられる。**保存されたライフサイクル列は存在しない**。
協調的なストリームは end-or-fail シグナルを運ばないため、唯一の誠実な生存性シグナルは
導出された `cc_state` である。

`cc_state` はイベントの新しさから**読み取り時に**導出される(`active` / `idle` / `ended`)。
そして、コネクターがその検出結果を上げたときに silent-evasion(沈黙による回避)状態に
切り替わる(モジュール自身が書き込むことは決してない)。読み取りはモジュールルート
(ライブ一覧、単一セッション、セッションごとのタイムライン)とライブ SSE ストリームの下で
提供される。すべての読み取りはセッション読み取りパーミッションを必要とし、
**ストリームを開くことは自動監査される**。SSE チャネルは厳密に**テナント分離**されており
(クライアントは認可されたテナントのスナップショットのみを受信する)、**ベストエフォート**である
(遅いクライアントは中間フレームをドロップして次のフレームを取得する。取り込みが
ブロックされることは決してない)。

## Provider profiles and `live_ref`

**プロバイダープロファイル**は、1 つの実行環境上の 1 つの設定済みプロバイダー
インスタンスの永続的な識別です: ドライバー、環境、および正規の `config_home` /
`user_home`。認証済みプロバイダーアカウントではありません。登録、改名、
無効化/有効化、退役は `/v1/m/sessions/provider-profiles` の下にあります。パスは
admin の `configuration` 読み取りにだけ現れます。起動は `provider_profile_ref`
を指名します。サーバはホームを解決し、spawn 前に非秘密スナップショットを run に
永続化します（`CHANGELOG.md` `[26.9.0]` B1、
`web/src/features/agentops/types.ts`）。

観測はその **チャネル** のライブロウに折り畳まれます。サーバがホスト印のソース
登録から計算します（`CHANGELOG.md` `[26.9.0]` B2）:

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

各ライブロウは `live_ref` と `attribution` を公開します。同じプロバイダー
セッション id を告知する 2 つのホームは、2 つのタイムラインを持つ 2 行です。
1 行は `GET /v1/m/sessions/live/by-id/{live_ref}`（およびその timeline /
stream / runs クエリ）で読みます。裸の外部 id ルートは残り、**legacy** です:
legacy 行にだけ答えます。

ドライバーは各ノードの起動時に登録されます。セッション起動時には、エンジンは
最新の検証済み管理インストール、次に自身の `PATH` 上の CLI を使用します。
`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`、`_CODEX_BIN`、`_GROK_BIN`、`_OPENCODE_BIN`
はこの解決順序を明示的に上書きします（[設定](/reference/configuration/)）。
実行ファイルが見つからなければ、起動を拒否します。プロファイルの認証や起動ポリシー
など、ほかの準備状態チェックも適用されます。オペレータ手順:
[プロバイダーセッションを運用する](/how-to/operate-provider-sessions/)。

メッセージには画像を添付できます。`olivares session send <session> --attach shot.png "what
is wrong here?"` とコンソールの入力欄（貼り付け、ドロップ、クリップアイコン）は、PNG、JPEG、WebP、
GIF の各画像をセッションとともに保存します。1 メッセージにつき最大 4 枚、1 枚あたり 3.75 MB までで、
その後テキストとともに送信します。メッセージにはテキストが必要なので、画像だけでは送信されません
（CLI と入力欄がテキストを求めます）。Claude Code は画像コンテンツブロックを受け取り、Codex は
独自の画像入力を受け取ります。画像入力のないツールは、何かを送信する前に、ツール名を挙げてメッセージを
拒否します。API は `POST /v1/m/sessions/runs/{ref}/attachments`（画像、base64）、
`GET .../attachments/{id}`、および `.../input` の `attachments` です。画像はデータディレクトリの下に
テナントごと、セッションごとに保存され、セッションとともに削除されます。

## NFS または SMB 共有上のセッションフォルダー

セッションフォルダーはエンジンホスト上のパスなので、ネットワーク共有はホストがマウントした後にフォルダーになります。`olivares netfolder add` は systemd 自身のマウントユニットでそれを行い、マウントポイントをフォルダーとして登録します。マウントの管理は systemd のものであり、ユニットは読んで編集できるプレーンなファイルです。エンジンホスト上で root として実行します:

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

ソースは、`mount(8)` が書くとおり、SMB では `//server/share`、NFS では `server:/export` です。名前は小文字と数字で、マウントポイント `/srv/olivares/mnt/<name>` の名前になります。`--mode` はフォルダーのアクセスモードです（既定は `rw`）。ユニットは、SMB フォルダーが見せるすべてのファイルの所有者としてアカウント `olivares` を指定するため、エンジンはそのアカウントで実行される必要があります。`ls` は各フォルダーを `mounted`、`unmounted`（初回使用時にマウントされます。`ls` はそれを引き起こしません）、または `not responding` として表示します。既に存在するフォルダーは決して上書きされません。

SMB の例で、このコマンドが書き出すユニットは次のとおりです。Debian 13（Linux 6.12）上で、NFSv4.2 エクスポートと Samba 4.22 共有（SMB 3.1.1）に対して測定しました。ファイル名は、`systemd-escape -p /srv/olivares/mnt/files` が出力するものに `.mount` と `.automount` を付けたものです。

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

NFS の場合は、`Type=nfs4`、`What=fileserver:/export` と `Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2` を書き、資格情報ファイルは作りません。アプライアンスのポリシーで SELinux を enforce しているホストでは、オプションに `context=system_u:object_r:olivares_workspace_t:s0` を追加します。これは、エンジンとそのセッションツールがフォルダーに既に使っているラベルです。フォルダーを削除するには、2 つのユニットに対して `systemctl disable --now` を実行し、それらと資格情報ファイルを削除して、フォルダーの登録を解除します。

- **共有にアクセスできる全員が 1 つのアカウントとして動作します。** ファイルは、マウントが提示するアカウントとして読み書きされます。SMB では `uid=` と `gid=`、NFS ではエクスポートのユーザーマッピングです。共有は人を区別しません。誰にフォルダーを渡すかは Olivares が決めます。
- **個人専用のフォルダーは、その本人として読み取ります。** `--owner <ユーザー ID>` は、個人フォルダーと同じように、SMB フォルダーを 1 人専用として登録します。内容は本人だけのもので、管理者は管理のためにフォルダー自体は引き続き見つけられます。`--user` と、その本人自身の SMB ログインのパスワードを指定すると、ファイルサーバーがその人の権限を適用します。同じ共有をマウントした 2 人は、それぞれ自分のアカウントで開けるものだけを見ます。フォルダーは 1 人につき 1 つ、それぞれ別の名前で追加します。NFS は人を区別できないため、所有者を受け付けません。管理者は本人のパスワードを 1 回入力し、共有アカウントのパスワードと同じく root だけが読めるクレデンシャルファイルに保存されます。本人のセッションがそれを保持することはありません。本人が誰にもパスワードを渡さなくて済むセッション単位の Kerberos チケットは、まだ作られていません。Samba AD ドメインの 2 つのアカウントで 1 つの共有を確認しました。各人のフォルダーには、自分のディレクトリと共通ディレクトリが表示され、自分のファイルを読め、もう一方のファイルはファイルサーバーが拒否しました。
- **資格情報はファイルに保持されます。** コマンドは、標準入力から受け取ったパスワードを、`credentials=` ファイルに `username=` と `password=` として書き込みます。所有者は root、モードは 0600 です。パスワードはどのユニット、マウントオプション、プロセス引数、ジャーナルの行にも現れません。`mount.cifs` にはパスだけが渡されます。`sec=sys` の NFS には資格情報は不要です。
- **automount ユニットにより、起動はファイルサーバーから独立します。** 起動時に共有を待つものはありません。初回使用でマウントされ、その時点でサーバーがダウンしていれば、起動ではなく使用が失敗します。
- **`soft` を使用してください。** 既定の `hard` NFS マウントでは、停止したサーバーに触れたプロセスが止められない状態で止まってしまいます。上記のオプションでは、マウントされた共有への読み書きは、20 から 30 秒後に入出力エラーまたは「Host is down」で失敗し、サーバーが応答するようになると共有は再び動作します。
- **応答しないフォルダーは報告されます。** フォルダーの登録、開く、一覧表示、そのフォルダーでのセッション起動は、それぞれ共有を 10 秒間待ち、その後「The folder ... is not responding. If it is on a network share, check that its file server is up and reachable.」というメッセージと 424 ステータスで拒否されます。セッションは起動しません。
- **`write_no_delete` は保持されます。** Landlock がカーネルのファイルシステム層で強制します。それはネットワークファイルシステムより上で働きます。両方の共有で実際のセッション起動を行ったところ、セッションはファイルを作成して変更でき、名前変更、unlink、rmdir は、ローカルディスクとまったく同じように拒否されました。`rw` ではそれらが許可されました。閉じ込めができないホストでは、それでも起動は拒否されます。
- **フォルダーが境界です。** 同じ起動において、両方のモードで、同じ共有上にあるフォルダーの親への書き込みは拒否され、ファイルは残りませんでした。セッションはフォルダーの中にのみ書き込みます。

## 何を消費するか(そして何を導出するか)

モジュール II は、インベントリと同じ minimal-data の観測ストリーム
([`edge.observed`](/ja/reference/events/)、`cost.sampled`、`finding.reported`)を消費する。
起点が**セッション**であるエッジのみがライブ運用を生む。セッションに紐づくコストサンプルは
ライブなトークン/コストの数値に加算される(ここで `CostRecord` が書き込まれることはない)。
セッションを subject とする検出結果は注釈付けされ、anti-evasion(回避対策)検出結果は
evasion 状態をマークする。2 つのフィールドが同じシグナルから**ライブに導出**される。
`agent_ref` はセッションに帰属するエージェントから、`summary` はコンテキスト圧縮
(フォレンジック)検出結果(そのタイトルはコントラクトにより summary-safe である)から
導出される。LLM が捏造したサマリーでは決してない。

:::caution[誠実な限界]

- **`goal` は空のままである — 誠実に。** 協調的なストリームは minimal-data であり、
  セッションの目標やタスクリストを**運ばない**。これらはコネクターでレダクトされ、
  ワイヤー上にインプロセスのプロンプトテキストは存在しない。ライブレコードはこの
  フィールドをモデル化しており、コントラクトと UI は準備済みで、将来のメタデータ
  チャネルがあればポピュレートできるが、モジュールが**それを発明することは決してない**。
- **保存されたライフサイクルはない。** ストリームには end/fail シグナルがないため、
  セッションの生存性は新しさによる**導出された** `cc_state` であり、永続化された
  ステータスではない。`ended` 状態は*最近イベントがない*ことを意味し、確認された
  クリーンなシャットダウンを意味しない。
- **ライブな数値は台帳ではない。** ライブなトークン/コストはコストサンプルからの
  運用上の読み取りである。権威ある、照合可能なコストレコードはモジュール XI の
  FinOps 台帳である。ライブな数値を課金の真実として扱ってはならない。
- **minimal-data はワイヤーの特性である。** 運ばれ永続化されるのは参照、分類、
  生存性/コストカウンターのみである。ペイロード、プロンプト、コマンド、PII は
  決して扱われない。
:::

## 関連

- [イベントバスリファレンス](/ja/reference/events/) — 本モジュールが消費する
  `edge.observed`、`cost.sampled`、`finding.reported` イベント。
- [モジュールカタログ](/ja/reference/modules/overview/) — モジュール II の位置づけと、
  誠実な actuate の区分。
- [アクセス＆リソースマップ](/ja/reference/modules/iii-access-map/) — R/RW アクセスグラフを
  所有する兄弟 Core モジュール。
- [アーキテクチャ概要](/ja/explanation/architecture/overview/) — エンジンとレイヤー。
- [Claude Code を接続する](/ja/how-to/connect-claude-code/) — ライブストリームの生成を開始する。
- [誠実さと限界](/ja/start/honesty-and-limits/) — 本製品が今日できること、できないこと。
