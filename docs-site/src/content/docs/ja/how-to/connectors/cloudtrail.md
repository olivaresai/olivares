---
title: "S3 向け AWS CloudTrail（clean ティアの R/RW）"
description: >-
  CloudTrail のデータイベントから S3 オブジェクトへの read/write アクセスを取り込む —
  readOnly フラグをそのまま使い、IAM プリンシパルを起点とし、assumed role が実際の呼び出し元を
  隠す場合には正直に近似的な帰属を行う。
sidebar:
  order: 2
---

`s3cloudtrail` ソースは、AWS CloudTrail の **S3 データイベント** を access-map の
エッジに変換する。S3 イベント 1 件につき 1 エッジで、read/write モードは **CloudTrail の
`readOnly` フィールドからそのまま取得され** — 決して推測されない — CloudTrail がその呼び出しを
帰属させる IAM プリンシパルを起点とする。これはオブジェクトストレージ向けの clean ティアであり、
Postgres 向けの [pgAudit](/ja/how-to/connectors/pgaudit/) に対応する存在である。

コネクタは **ローカルのログファイルを読み、AWS を呼び出すことは決してない**：あなたが
CloudTrail ファイル（あなたのトレイルが既に生成している標準的な S3 配信レイアウト）を渡し、
コネクタがそれをパースする。処理されるのは `eventSource == s3.amazonaws.com` のイベントのみ —
管理プレーンのイベントは、このコネクタではなく
[`aws` クラウド検出コネクタ](/ja/reference/connectors/)に属する。

## 出力するもの

| フィールド | 値 |
|---|---|
| Signal source | `cloudtrail` |
| Mode | `readOnly: true` → `read`、`false` → `write`、不在 → `unknown` — そのまま、決して推測しない |
| Origin | IAM プリンシパル（ユーザー、assumed-role セッション、AWS サービス） |
| Confidence | `attributed`；共有された assumed role やサービス起動の呼び出しでは `approximate` |
| Coverage tier | clean |

## 1. AWS 側の前提条件

- ガバナンス対象のバケットに対して **S3 データイベントが有効化された CloudTrail トレイル**
  （データイベントはデフォルトの管理トレイルには含まれない）。
- トレイルのログファイルを、エンジンホストが読める場所へ配信すること —
  標準的な S3 配信バケットを、ローカルに同期またはマウントする。コネクタは
  古典的な `{"Records":[…]}` ファイル（プレーンまたは `.json.gz`）と
  改行区切りのレコードを受け付ける。

## 2. ソースを宣言する

```json
{
  "sources": [{
    "name": "prod-s3-trail",
    "kind": "s3cloudtrail",
    "tenant": "<tenant-id>",
    "config": {
      "path": "/var/lib/cloudtrail/prod/",
      "shared_accounts": "arn:aws:iam::123456789012:role/app-runtime"
    }
  }]
}
```

| キー | 必須 | 意味 |
|---|---|---|
| `path` | はい | 1 つの CloudTrail ファイル、または `*.json` / `*.json.gz` ファイルのディレクトリ |
| `shared_accounts` | いいえ | 多数の呼び出し元が共有するロール ARN のカンマ区切り — それらのエッジは正直に `approximate` となる |

（`s3-cloudtrail` は `kind` のエイリアスとして受け付けられる。）

## 3. コンソールで見えるもの

S3 バケットとオブジェクトは clean ティアのバッジ付きで **Access map** に加わる。read と write は
`readOnly` フラグから色分けされる。ドリフトパネルは、他のどのソースとも同様に、宣言された
グラントとそれらを突き合わせる。

**Inventory** では、CloudTrail が呼び出しを帰属させるプリンシパルが identity として現れ、
エージェントに束縛できる状態になる — その束縛こそが、共有ロールの `approximate` を
エージェント単位の `attributed` に変えるものである。

## 正直な限界 — マップを信頼する前に読むこと

- **多数の呼び出し元が共有する assumed role は、実際の呼び出し元を名指しできない。**
  CloudTrail はその呼び出しをロールセッションに帰属させる。ロールが共有されている場合、
  そのエッジは意図的に `approximate` である。ロールを `shared_accounts` に宣言すると、
  それが明示される。恒久的な解決策はエージェント単位の identity である
  （[identity への依存](/ja/how-to/connect-a-source/#厳しい依存関係-エージェント単位のアイデンティティ)）。
- **有効化していないデータイベントは存在しない。** CloudTrail はトレイルが記録するよう
  設定されたものだけを記録する。バケットでデータイベントがオフの場合、エッジの不在は
  アクセスの不在を意味しない。
- **配信遅延は CloudTrail のものである。** データイベントは CloudTrail の配信スケジュール
  （通常は数分）で到着する。このソースはリアルタイムのタップではない。

## 関連

- [pgAudit](/ja/how-to/connectors/pgaudit/) — PostgreSQL 向けの同じ clean ティアの規律。
- [ソースを接続する](/ja/how-to/connect-a-source/) — コネクタモデル。
- [コネクタとカバレッジティア](/ja/reference/connectors/) — すべてのストアが正直にどこに位置するか。
