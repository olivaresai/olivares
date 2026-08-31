---
title: "PostgreSQL pgAudit (clean-tier R/RW)"
description: >-
  Capture read/write access to PostgreSQL from its native pgAudit trail — the
  clean-tier signal: READ/WRITE taken verbatim from the audit CLASS, never
  inferred from SQL, with the connector reading only the log file.
sidebar:
  order: 1
---

The `pgaudit` source turns PostgreSQL's own audit trail into access-map edges:
one edge per audited data access, with the read/write mode taken **verbatim
from pgAudit's CLASS field** — never inferred from SQL text. It is the canonical **clean tier** source: an
object/relational store that classifies access in its native trail.

The connector is **read-only over a log file**. It never connects to the
database, never sees query results, and never captures the SQL body — the
identity, the object and the classification are all pgAudit's own output.

## What it emits

| Field | Value |
|---|---|
| Signal source | `pg_audit` |
| Mode | from CLASS, verbatim: READ → `read`, WRITE → `write`, DDL → `write` (a schema write), FUNCTION → `unknown` (pgAudit does not say); ROLE/MISC are skipped, not guessed |
| Origin | the `application_name` if present (→ `attributed`), else the session role |
| Confidence | `attributed`, or `approximate` for roles/apps you declare shared |
| Coverage tier | clean |

## 1. Turn on pgAudit, structured logs, UTC

On the PostgreSQL side (the standard pgAudit setup — see the pgAudit docs for
your major version):

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

Two constraints come from how the connector parses, both verified against its
implementation:

- **The server must log in UTC.** PostgreSQL writes timestamps with a zone
  *abbreviation*, and a non-UTC abbreviation cannot be reliably resolved to an
  offset — so the connector **skips** such records rather than guess a wrong
  timestamp. `log_timezone = 'UTC'` is the supported configuration.
- **`csvlog` is batch; `jsonlog` can follow.** csvlog records may span
  newlines, so that format is read as a batch each pass; `jsonlog` is
  line-delimited and supports continuous tailing (`follow`, the default).

To make attribution sharp, have applications set `application_name` per agent
— that is what upgrades an edge from a shared role to an attributed origin
(see [the identity dependency](/how-to/connect-a-source/#the-hard-dependency-per-agent-identity)).

## 2. Declare the source

In your [sources config](/how-to/connect-a-source/#wiring-a-real-source)
(`OLIVARES_SOURCES_CONFIG`):

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

Configuration keys (from the connector's shipped descriptor):

| Key | Required | Default | Meaning |
|---|---|---|---|
| `log_path` | yes | — | path to the PostgreSQL log file the engine host can read |
| `format` | no | `csvlog` | `csvlog` or `jsonlog` |
| `follow` | no | `true` | tail continuously (**jsonlog only** — csvlog is batch) |
| `shared_accounts` | no | — | comma-separated roles / application_names that are shared; their edges are honestly marked `approximate` |

Restart the engine and confirm the boot line
`ingest: wired source … kind=pgaudit`.

## 3. What you'll see in the console

Open **Access map**. Each audited access renders as an edge from the role or
application to the table, colored read or write, with the `CLEAN` coverage
badge on Postgres resources. The **Permitted vs observed** panel surfaces any
access without a matching grant — with pgAudit wired and no grants declared
yet, *every* observed access is honest drift, which is the expected first
state.

## Honest limits

- **It sees what pgAudit logs.** Classes you do not enable
  (`pgaudit.log`) are not observed; an absence of edges is not proof of no
  access if the class is off.
- **Attribution is the database's.** A shared role with no
  `application_name` collapses callers onto one identity — declare it in
  `shared_accounts` so the map says `approximate` instead of pretending.
- **FUNCTION is `unknown` by design** — executing a function may read or
  write, and pgAudit does not say which; the product will not force a label.
  Non-data classes (ROLE, MISC) are skipped rather than emitted as
  meaningless edges.

## Related

- [Connect a source](/how-to/connect-a-source/) — the connector model and the
  honest-tier taxonomy.
- [CloudTrail](/how-to/connectors/cloudtrail/) — the same clean-tier idea for
  S3 objects.
- [Connectors & coverage tiers](/reference/connectors/) — the full catalog.
