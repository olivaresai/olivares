---
title: "ガバナンス対象コンテキストソースとしての Postgres"
description: "PostgreSQL database を読み取り専用のガバナンス対象ナレッジソースとして接続します。行を文書として実体化し、ACL を正確にマッピングし、機密列を分類し、構造によって読み取り専用を保証します。"
---

`postgres` コンテンツコネクタ（`olivares.pg-content`）を使うと、コントロールプレーン
を PostgreSQL database に接続し、その行を**ガバナンス対象のナレッジ文書**へ変換
できます。文書は他のコンテンツソースと同じ pipeline（秘匿化 → classify → chunk →
embed → index → MCP で提供）を通り、文書ごとの ACL と列ごとの classification を
持ちます。

これは SaaS/data warehouse のコンテンツソース（gdrive、confluence、s3content、
snowflake…）に対応する、業務 database 向けのコネクタです。ただし、次の 2 つ
**ではありません**。

- **`pgaudit` ではありません。** `pgaudit` は access map のために R/RW の
  *access edge* を観測しますが、行の内容は決して読みません。`pg-content` は
  *行を文書として実体化*します。両者は異なる目的のための別々のコネクタです。
- **NL-to-SQL ではありません。** このコネクタは行をコンテンツとして ingest
  します。query 時に自然言語から SQL を生成することは**ありません**。（一部の
  既存製品は text-to-SQL 機能を「structured data を持つ knowledge base」と呼び
  ますが、それは agent の query surface であって、ガバナンス対象コンテンツソース
  ではありません。このコネクタは意図的に後者です。）

## 構造によって保証される読み取り専用

コネクタが database へ書き込むことはありません。書き込みを単に非推奨とするのでは
なく不可能にするため、**独立した 3 つの層**で強制します。

1. **SELECT だけの query。** コネクタが*構築*するのは `SELECT` 文だけです。独自の
   `query` を指定した場合は、読み取り専用の単一 `SELECT`/`WITH` であることを検証
   します。2 つ目の statement、データを変更する CTE（`WITH x AS (DELETE …)`）、
   `COPY`、`SELECT … INTO`、または DDL は、`Open` 時にデニークローズドで拒否
   されます。
2. **読み取り専用 session。** 各 statement は、`default_transaction_read_only = on`
   で開かれた session 上の `READ ONLY` transaction 内で実行されるため、PostgreSQL
   自体が書き込みを拒否します。コネクタは `Open` 時に session が読み取り専用で
   あることを*検証*し、そうでなければ起動を拒否します。これは助言ではなく、
   posture の保証です。
3. **最小権限の role。** コネクタには `SELECT` だけを持ち、それ以外の権限を持たない
   role を与えます。以下のリファレンス role を参照してください。

これは、読み取り専用を文書上の*助言*としてしか示していない、すべての managed
incumbent よりも強い保証です。

### 最小権限の role

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Never grant INSERT/UPDATE/DELETE/DDL. Optionally pin the role read-only:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

scope を最も狭くするには、ingest する table だけに `SELECT` を grant します。

## 行から文書を作る方法を定義する

文書の定義は宣言的です。key、本文、title、ACL、classification、sync cursor に使う
列を指定します。

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "support-articles",
      "kind": "postgres",
      "config": {
        "mode": "live",
        "dsn": "vault:secret/data/pg-ro#dsn",   // secret-store REFERENCE, never inline
        "schema": "public",
        "table": "kb_articles",
        "key_columns": "id",                     // the stable document id
        "body_columns": "title,body",            // concatenated into the document body
        "title_column": "title",
        "updated_at_column": "updated_at",       // drives incremental (delta) sync
        "acl_columns": "owner_group",            // → ACL "group:<value>"
        "acl_prefix": "group:",
        "classification_column": "sensitivity",
        "sensitive_columns": "email,ssn",        // → external label "pii:<column>"
        "sensitive_label": "pii",
        "metadata_columns": "url_path",
        "sslmode": "require",
        "statement_timeout": "30s",
        "max_rows": "100000"
      }
    }
  ]
}
```

`table` の代わりに、読み取り専用の `query`（検証済み `SELECT`）を指定できます。
ACL table の join や、公開する行の filter に便利です。credential は常に
**secret-store の参照**（`vault:…`、`aws-secretsmanager:…` など）でなければならず、
平文の secret は拒否されます。

## ACL を*正確に*マッピングする仕組み

コネクタがマッピングするのは**行が表現している情報だけ**です。宣言した
`acl_columns` の値から文書の ACL を構築します（例: `owner_group` 列 →
`group:eng`）。source が持っていない行ごとの ACL を**捏造せず**、次の制限を明示
します。

| 状況 | コネクタの動作 |
|---|---|
| `owner_group` / role 列 | 各値を ACL ref（`<acl_prefix><value>`）にマッピングします。 |
| `acl_columns` を宣言していない | 文書はナレッジベースの**デフォルト ACL**を継承し、retrieval でも引き続き適用されます。 |
| table の **row-level security（RLS）** | 暗黙に尊重します。コネクタの role に見えるのは、RLS が許可した行だけです。コネクタは RLS を再実装せず、継承します。 |
| table が列としてモデル化して**いない**権限 | **導出不可** → マッピングしません。強制したい場合は列としてモデル化するか、`query` で ACL table を join してください。 |

これは、ACL 列を手作業で定義させながら RLS passthrough を提供しない managed
incumbent との意図的な違いです。ここでも ACL 列は手動でマッピングしますが、
コネクタは**さらに** RLS を尊重し、行にない権限を決して捏造しません。

## 列ごとの classification

機密列を `sensitive_columns` に列挙します。行の該当列に値があると、文書に
`"<sensitive_label>:<column>"`（例: `pii:ssn`）という external label が加わります。
これらの label は retrieval DLP に渡され、行の `classification_column` と並んで
デニークローズドで強制されます。

## live と export

- **`mode: live`** は読み取り専用 pool 経由で database を読み、`updated_at_column`
  cursor による**増分（delta）sync**をサポートします。cursor が設定されていない
  場合は、full-list reconciliation に fallback します。
- **`mode: export`** は静的な行の snapshot（別経路で作成した JSON dump）を解析
  します。snapshot を **live として提示することは決してなく**、source は mode を
  正確に通知します。

## 明示されている制限

- 文書の**本文は 1 MiB が上限**です。より大きな行は切り詰められます（非常に大きい
  列の streaming は今後の対応です）。
- 運用者が指定する `query` で、**SQL keyword とまったく同じ名前の列**（例:
  `update`）を使う場合は alias が必要です。読み取り専用 guard はデニークローズド
  です。
- コネクタはコンテンツを読み取ります。**database に対するアクションは対象外**で
  あり（設計上、書き込み経路はありません）、CDC streaming と NL-to-SQL も対象外
  です。

## 実配線での証明

コネクタには、実際の PostgreSQL に対して実行する実配線 E2E（`-tags e2e`、CI）が
あります。`Open` 時の読み取り専用 session を検証し、seed 済みの行を対応する
ACL/classification とともに ingest して、読み取り専用 session での書き込みが
PostgreSQL によって**拒否される**ことを証明します。
`connectors/pgcontent/testdata/docker-compose.e2e.yml` を参照してください。
