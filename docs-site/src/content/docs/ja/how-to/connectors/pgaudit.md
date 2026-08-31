---
title: "PostgreSQL pgAudit（clean ティアの R/RW）"
description: >-
  PostgreSQL のネイティブな pgAudit 証跡から読み取り/書き込みアクセスを
  捕捉する——clean ティアのシグナル: READ/WRITE は監査 CLASS から逐語的に
  取得され、SQL から推測されることはなく、コネクタはログファイルのみを読む。
sidebar:
  order: 1
---

`pgaudit` ソースは、PostgreSQL 自身の監査証跡をアクセスマップのエッジに
変換する: 監査された各データアクセスごとに 1 本のエッジを生成し、その
読み取り/書き込みモードは **pgAudit の CLASS フィールドから逐語的に**取得される
——SQL テキストから推測されることはない。これは規範的な **clean ティア**の
ソースだ: ネイティブな証跡でアクセスを分類するオブジェクト/リレーショナル
ストアである。

このコネクタは**ログファイルに対する読み取り専用**だ。データベースに接続せず、
クエリ結果を見ず、SQL 本体を捕捉しない——アイデンティティ、オブジェクト、
分類はすべて pgAudit 自身の出力だ。

## 発行するもの

| フィールド | 値 |
|---|---|
| シグナルソース | `pg_audit` |
| モード | CLASS から逐語的に: READ → `read`、WRITE → `write`、DDL → `write`（スキーマ書き込み）、FUNCTION → `unknown`（pgAudit は明示しない）; ROLE/MISC はスキップされ、推測されない |
| 起点 | `application_name` があればそれ（→ `attributed`）、なければセッションロール |
| 信頼度 | `attributed`、または共有と宣言したロール/アプリには `approximate` |
| カバレッジティア | clean |

## 1. pgAudit、構造化ログ、UTC を有効にする

PostgreSQL 側で（標準的な pgAudit セットアップ——お使いのメジャーバージョンの
pgAudit ドキュメントを参照）:

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

コネクタのパース方法に由来する制約が 2 つあり、いずれもその実装に対して
検証済みだ:

- **サーバーは UTC でログを出力しなければならない。** PostgreSQL は
  タイムゾーンの*略称*付きでタイムスタンプを書き込むが、UTC 以外の略称は
  オフセットへ確実に解決できない——そのためコネクタは、誤ったタイムスタンプを
  推測するのではなく、そうしたレコードを**スキップ**する。
  `log_timezone = 'UTC'` がサポートされる構成だ。
- **`csvlog` はバッチ、`jsonlog` は追従可能。** csvlog レコードは複数行に
  またがる場合があるため、その形式は各実行時にバッチとして読まれる。`jsonlog`
  は行区切りであり、継続的なテーリング（`follow`、デフォルト）をサポートする。

帰属を鋭くするには、アプリケーションにエージェントごとの `application_name` を
設定させること——それがエッジを共有ロールから帰属済みの起点へ格上げするものだ
（[アイデンティティの依存関係](/ja/how-to/connect-a-source/#厳しい依存関係-エージェント単位のアイデンティティ)を参照）。

## 2. ソースを宣言する

[ソース設定](/ja/how-to/connect-a-source/#実際のソースを配線する)
（`OLIVARES_SOURCES_CONFIG`）内で:

```json
{
  "sources": [{
    "name": "salesdb-pgaudit",
    "kind": "pgaudit",
    "tenant": "<tenant-id>",
    "config": {
      "log_path": "/var/log/postgresql/postgresql.csv",
      "format": "csvlog",
      "shared_accounts": "etl_role,app_pool"
    }
  }]
}
```

設定キー（コネクタに同梱されたディスクリプタより）:

| キー | 必須 | デフォルト | 意味 |
|---|---|---|---|
| `log_path` | はい | — | エンジンホストが読める PostgreSQL ログファイルへのパス |
| `format` | いいえ | `csvlog` | `csvlog` または `jsonlog` |
| `follow` | いいえ | `true` | 継続的にテーリングする（**jsonlog のみ**——csvlog はバッチ） |
| `shared_accounts` | いいえ | — | 共有されているロール / application_name のカンマ区切り; それらのエッジは正直に `approximate` とマークされる |

エンジンを再起動し、起動行
`ingest: wired source … kind=pgaudit` を確認すること。

## 3. コンソールで見えるもの

**Access map** を開く。監査された各アクセスは、ロールまたはアプリケーションから
テーブルへのエッジとしてレンダリングされ、読み取りまたは書き込みで色分けされ、
Postgres リソースには `CLEAN` カバレッジバッジが付く。**Permitted vs observed**
パネルは、一致するグラントのないアクセスをすべて浮かび上がらせる——pgAudit が
配線され、まだグラントが宣言されていない状態では、観測される*すべて*のアクセスが
正直なドリフトであり、これが期待される最初の状態だ。

## 正直な限界

- **pgAudit がログ出力するものを見る。** 有効にしていないクラス
  （`pgaudit.log`）は観測されない。クラスがオフの場合、エッジの不在は
  アクセスがなかった証明にはならない。
- **帰属はデータベースのものだ。** `application_name` のない共有ロールは
  呼び出し元を 1 つのアイデンティティに集約する——`shared_accounts` で宣言して
  おけば、マップは取り繕う代わりに `approximate` と言う。
- **FUNCTION は設計上 `unknown`** ——関数の実行は読み取りまたは書き込みを
  行いうるが、pgAudit はどちらかを明示しない。本製品はラベルを強制しない。
  非データクラス（ROLE、MISC）は、無意味なエッジとして発行されるのではなく
  スキップされる。

## 関連

- [ソースを接続する](/ja/how-to/connect-a-source/) — コネクタモデルと正直さ
  ティアの分類法。
- [CloudTrail](/ja/how-to/connectors/cloudtrail/) — S3 オブジェクトに対する
  同じ clean ティアの考え方。
- [コネクタとカバレッジティア](/ja/reference/connectors/) — 完全なカタログ。
