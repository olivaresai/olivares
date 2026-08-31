---
title: "Postgres as a governed context source"
description: "Connect a PostgreSQL database as a read-only, governed knowledge source: materialize rows as documents, map ACLs honestly, classify sensitive columns, and keep the read-only guarantee by construction."
---

The `postgres` content connector (`olivares.pg-content`) lets you point the control plane
at a PostgreSQL database and turn its rows into **governed knowledge documents** that flow
through the same pipeline as every other content source — redact → classify → chunk →
embed → index → serve over MCP — with per-document ACLs and per-column classification.

It is the operational-database counterpart to the SaaS/warehouse content sources
(gdrive, confluence, s3content, snowflake…). Two things it is **not**:

- **Not `pgaudit`.** `pgaudit` observes R/RW *access edges* for the access map; it never
  reads row content. `pg-content` materializes *rows as documents*. They are different
  connectors for different jobs.
- **Not NL-to-SQL.** This connector ingests rows as content; it does **not** generate SQL
  from natural language at query time. (Some incumbents brand a text-to-SQL feature a
  "knowledge base with structured data" — that is an agent query surface, not a governed
  content source. This connector is deliberately the latter.)

## Read-only by construction

The connector never writes to your database, and it enforces that on **three independent
layers** so a write is impossible, not merely discouraged:

1. **SELECT-only queries.** The connector only ever *builds* `SELECT` statements. If you
   supply your own `query`, it is validated to be a single read-only `SELECT`/`WITH` — a
   second statement, a data-modifying CTE (`WITH x AS (DELETE …)`), `COPY`, `SELECT …
   INTO`, or any DDL is rejected at `Open`, fail-closed.
2. **A read-only session.** Every statement runs in a `READ ONLY` transaction on a session
   opened with `default_transaction_read_only = on`, so PostgreSQL itself refuses a write.
   At `Open` the connector *verifies* the session is read-only and refuses to start if it
   is not — a posture guarantee, not advice.
3. **A least-privilege role.** You give the connector a role that has `SELECT` and nothing
   else. See the reference role below.

This is stronger than every managed incumbent, which documents read-only only as *advice*.

### The least-privilege role

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Never grant INSERT/UPDATE/DELETE/DDL. Optionally pin the role read-only:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

Grant `SELECT` on only the tables you intend to ingest for the tightest scope.

## Define how a row becomes a document

The document definition is declarative — you say which columns are the key, the body, the
title, the ACL, the classification, and the sync cursor:

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

Instead of a `table` you may give a read-only `query` (a validated `SELECT`) — useful to
join an ACL table or filter to the rows you want to expose. The credential is always a
**secret-store reference** (`vault:…`, `aws-secretsmanager:…`, …); a cleartext secret is
rejected.

## How the ACL mapping is *honest*

The connector maps **only what the row expresses**. It builds a document's ACL from the
values in your declared `acl_columns` (e.g. an `owner_group` column → `group:eng`). It does
**not** invent a per-row ACL the source does not carry, and it makes these limits explicit:

| Situation | What the connector does |
|---|---|
| An `owner_group` / role column | Maps each value to an ACL reference (`<acl_prefix><value>`). |
| No `acl_columns` declared | The document inherits the knowledge base's **default ACL** — retrieval still enforces it. |
| **Row-level security (RLS)** on the table | Respected implicitly: the connector's role sees exactly the rows RLS permits it to see. The connector does not re-implement RLS; it inherits it. |
| A permission the table does **not** model as a column | **Not derivable** → not mapped. Model it as a column (or a joined ACL table via `query`) if you want it enforced. |

This is the deliberate difference from the managed incumbents, which make you hand-author
ACL columns *and* offer no RLS passthrough. Here you also hand-map ACL columns, **but** the
connector additionally respects RLS and never fabricates permissions the row lacks.

## Per-column classification

List sensitive columns in `sensitive_columns`. When a row has a value in one, the document
gains an external label `"<sensitive_label>:<column>"` (e.g. `pii:ssn`). These labels feed
the retrieval DLP and are enforced deny-closed alongside the row's `classification_column`.

## Live vs export

- **`mode: live`** reads the database through the read-only pool and supports **incremental
  (delta) sync** by the `updated_at_column` cursor, with full-list reconciliation as the
  fallback when no cursor is configured.
- **`mode: export`** parses a static row snapshot (a JSON dump you produce out of band). A
  snapshot is **never presented as live** — the source signals its mode honestly.

## Honest limits

- A document **body is capped at 1 MiB**; a larger row is truncated (streaming very large
  columns is a follow-up).
- A **column literally named after a SQL keyword** (e.g. `update`) in an operator-supplied
  `query` must be aliased — the read-only guard is fail-closed.
- The connector reads content; **acting on the database is out of scope** (there is no write
  path, by design), and so is CDC streaming and NL-to-SQL.

## Wire-proof

The connector ships a wire-proof E2E (`-tags e2e`, CI) that runs against a real PostgreSQL:
it verifies the read-only session at `Open`, ingests seeded rows with their mapped
ACL/classification, and proves a write on the read-only session is **rejected** by
PostgreSQL. See `connectors/pgcontent/testdata/docker-compose.e2e.yml`.
