---
title: gRPC リファレンス — サービス、メソッド、メッセージ型
description: >-
  Olivares AI エンジンとプラグインホストが登録するすべての rpc を、
  ストリーミング形態、リクエストとレスポンスのメッセージ、通信に使う完全な
  メソッド文字列とともに示します。サーバー自身の登録テーブルから生成されています。
---

Olivares AI は 2 か所で gRPC を使用し、それぞれ逆方向を向いています。

- **エンジンのコントロールプレーン API**（`olivares.api.v1.ControlPlane`）—
  型付き stub を使いたい呼び出し元向けの、REST サーフェスの小さなミラーです。
  [API リファレンス](/reference/api/)の REST contract の方が広範です。
- **プラグインのワイヤ contract**（`olivares.sdk.v1.*`）— すべての
  アウトオブプロセスのコネクタとモジュールが使うバージョン付き contract です。
  Go 以外の言語で[コネクタを構築する](/ja/how-to/build-a-connector/)場合に実装するのは
  こちらです。

このページは `.proto` ファイルではなく、**サーバーが gRPC に渡す登録テーブルから
生成されています**。この違いが重要です。再生成せずに編集された `.proto` は、
バイナリが提供しないサービスを記述します。このページを支えるチェックは、見栄えのよい
方を公開するのではなく、その不一致を報告します。ここに掲載されたメソッドは、クライアントが
呼び出せるメソッドです。

:::note[安定性]
プラグイン contract `olivares.sdk.v1` はバージョン管理され、buf の破壊的変更検出で
保護されています。互換性のない変更には新しいメジャーパッケージが必要です。何を、
どの期間保証するかは [API 安定性](/ja/reference/api-stability/)を参照してください。
:::

## トランスポートと認証

以下のサービスでは、`GetServerInfo` を除くすべてのメソッドに、認証され認可された
principal が必要です。2 つの例外は意図的であり、利用者に探させるのではなく、ここに
明記します。`GetServerInfo` は匿名で応答し、標準の
`grpc.health.v1.Health` サービス（`Check`、`List`、`Watch`）は principal
なしで同じ listener 上に提供されます。probe や service mesh は、kubelet が
`/livez` へ到達するのと同じように、すべての Pod でこれに到達する必要があるためです。
Bearer token がないリクエストは拒否されず匿名のままですが、存在する token が無効なら
拒否されます。コントロールプレーンサービスにはエンジンの gRPC listener から接続します。
プラグインサービスは go-plugin broker（同一ホスト内のコネクタ）を通じて、または
相互 TLS を使用する gRPC（リモートコレクタ）で dial します。listener は
[設定リファレンス](/ja/reference/configuration/)の `OLIVARES_*` 変数で設定します。

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

エンジンとプラグインホストは、**7 サービス**にわたり **28 rpc** を登録します。以下の表は、
サーバーが gRPC に渡す生成済み登録テーブルから読み取られます。ここに掲載されたメソッドは、
クライアントが呼び出せるメソッドです。

### `olivares.api.v1.ControlPlane`

`apiv1/api.proto` で定義。5 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | インベントリへ新しいエージェントを登録し、API の他の部分が使用する識別子を含む保存済みレコードを返します。 |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | 識別子で 1 つのエージェントを返します。フィールドは REST インベントリエンドポイントと同じです。 |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | バージョン、edition、readiness を報告します。このサービスで認証済み principal を必要としない唯一のメソッドです。 |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | 呼び出し元 principal に見えるエージェントをページ単位で列挙します。 |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | 指定範囲の監査チェーンを再検証し、checkpoint の状態を含め、ハッシュが引き続き連結しているか報告します。 |

### `olivares.sdk.v1.ContentSourceService`

`olivaresv1/v1.proto` で定義。7 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Open が開始したセッションを終了し、コネクタがそのために保持していたものを解放します。 |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | カーソル以降の変更をストリームします。コネクタが content.delta capability を広告する場合だけ呼び出されます。 |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | コネクタの descriptor（identity、設定フィールド、広告する capability）を返します。 |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | ホストが List ストリームから選んだ参照について、1 つの文書の本文とメタデータを返します。 |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | 1 つの文書を統制する権限参照を返します。空の結果は、ナレッジベースのデフォルトが適用されることを意味します。 |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | 文書参照を 1 ページずつストリームします。ホストが渡す上限で制限されるため、1 回の呼び出しで corpus 全体がホストのメモリへ読み込まれることはありません。 |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | content の呼び出し前に、ホストが渡す設定でセッションを開始します。 |

### `olivares.sdk.v1.HostService`

`olivaresv1/v1.proto` で定義。3 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | 構造化ログレコードをエンジン経由で 1 つ書き込み、アウトオブプロセスのモジュールがインプロセスのモジュールと同じ場所へ記録できるようにします。 |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | アウトオブプロセスのモジュールに代わって、エンジンのバスへ 1 つのイベントを公開します。 |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | 要求されたイベント型で絞り込み、バスイベントをモジュールへストリームします。空のフィルターはすべての型を意味します。 |

### `olivares.sdk.v1.IngestService`

`olivaresv1/v1.proto` で定義。1 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | コレクタデーモンから push された observation のストリームを受け入れ、それぞれをイベントバスへ載せ、ストリーム完了時に summary を返します。 |

### `olivares.sdk.v1.ModuleService`

`olivaresv1/v1.proto` で定義。4 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | モジュールの descriptor（identity と受け入れる設定）を返します。 |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | 何かを開始する前に、モジュールへ設定を渡して準備させます。 |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Init が成功した後、モジュールの処理を開始します。 |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | モジュールを停止し、保持していたものを解放させます。 |

### `olivares.sdk.v1.OutputService`

`olivaresv1/v1.proto` で定義。4 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Open が開始したセッションを終了し、コネクタがそのために保持していたものを解放します。 |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | コネクタの descriptor（identity、設定フィールド、広告する capability）を返します。 |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | 宛先へ通知を 1 件配信し、宛先での処理結果を報告します。その結果によってホストが再試行するか決まります。 |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | 配信前に、ホストが渡す設定でセッションを開始します。 |

### `olivares.sdk.v1.SourceService`

`olivaresv1/v1.proto` で定義。4 rpc。

| メソッド | 完全なメソッド | 種類 | リクエスト | レスポンス | 動作 |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Open が開始したセッションを終了し、コネクタがそのために保持していたものを解放します。 |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | コネクタの descriptor（identity、設定フィールド、広告する capability）を返します。 |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | observation をホストへストリームし、ホストがそれぞれをイベントバスへ載せます。バッチ実行の完了時、またはホストによるキャンセル時にストリームは終了します。 |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | observation を収集する前に、ホストが渡す設定でセッションを開始します。 |

<!-- END GENERATED olivares-grpc-reference -->

## メッセージの形状

表には各リクエストとレスポンスのメッセージ名が記載されています。フィールドは各サービスと
ともに示した `.proto` ファイルで宣言されています。これらはリポジトリに同梱され、
stub 生成のソースとなります。読む前に知っておくべき規則が 2 つあります。

- **語彙フィールドは閉じた enum ではなく文字列です** — access mode、signal source、
  confidence、severity、event type。サードパーティーのコネクタは SDK のリリースを
  待たずに独自の signal source を導入できます。
- **ペイロード形状は閉じています。** `Observation` または `Event` のペイロードは、
  既知のメッセージ型に、モジュール定義イベントペイロード用の JSON fallback を加えた
  `oneof` です。認識されないペイロードは contract エラーであり、黙って破棄されません。

## クライアントを生成する

`.proto` ファイルが contract です。プラグイン contract には
`sdk/plugin/proto/olivaresv1/v1.proto`、コントロールプレーンのミラーには
`core/api/proto/apiv1/api.proto` を、使用する言語の protobuf toolchain に指定します。
用意済みの Go および TypeScript クライアントは
[クライアント SDK を使う](/ja/how-to/use-the-client-sdks/)を参照してください。
