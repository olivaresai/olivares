---
title: "Postgres als governte Kontextquelle"
description: "Binden Sie eine PostgreSQL-Datenbank als read-only governte Wissensquelle an: Zeilen werden zu Dokumenten materialisiert, ACLs ehrlich abgebildet, sensible Spalten klassifiziert und die Read-only-Garantie konstruktionsbedingt gewahrt."
---

Mit dem Content-Connector `postgres` (`olivares.pg-content`) können Sie die
Kontrollebene auf eine PostgreSQL-Datenbank verweisen und deren Zeilen in
**governte Wissensdokumente** verwandeln. Sie durchlaufen dieselbe Pipeline wie
jede andere Inhaltsquelle — maskieren → klassifizieren → in Chunks teilen →
einbetten → indexieren → über MCP bereitstellen — mit ACLs pro Dokument und
Klassifizierung pro Spalte.

Er ist das Gegenstück für operative Datenbanken zu den
SaaS-/Warehouse-Inhaltsquellen (gdrive, confluence, s3content, snowflake …).
Zwei Dinge ist er **nicht**:

- **Nicht `pgaudit`.** `pgaudit` beobachtet R/RW-*Zugriffskanten* für die
  Access Map; es liest niemals Zeileninhalte. `pg-content` materialisiert
  *Zeilen als Dokumente*. Das sind unterschiedliche Connectoren für
  unterschiedliche Aufgaben.
- **Nicht NL-to-SQL.** Dieser Connector nimmt Zeilen als Inhalte auf; er
  generiert zur Abfragezeit **kein** SQL aus natürlicher Sprache. (Einige
  etablierte Anbieter bezeichnen eine Text-to-SQL-Funktion als „Knowledge Base
  mit strukturierten Daten“ — das ist eine Abfrageoberfläche für Agenten, keine
  governte Inhaltsquelle. Dieser Connector ist bewusst Letzteres.)

## Konstruktionsbedingt read-only

Der Connector schreibt niemals in Ihre Datenbank und erzwingt dies auf **drei
unabhängigen Ebenen**, sodass ein Write unmöglich und nicht nur unerwünscht
ist:

1. **Nur SELECT-Abfragen.** Der Connector *erstellt* ausschließlich
   `SELECT`-Statements. Wenn Sie eine eigene `query` angeben, wird sie als
   einzelnes read-only `SELECT`/`WITH` validiert — ein zweites Statement, ein
   datenverändernder CTE (`WITH x AS (DELETE …)`), `COPY`, `SELECT … INTO`
   oder beliebiges DDL wird bei `Open` fail-closed abgelehnt.
2. **Eine read-only Session.** Jedes Statement läuft in einer `READ ONLY`
   Transaction in einer Session, die mit
   `default_transaction_read_only = on` geöffnet wurde, sodass PostgreSQL
   selbst einen Write ablehnt. Bei `Open` *verifiziert* der Connector, dass
   die Session read-only ist, und verweigert andernfalls den Start — eine
   Posture-Garantie, kein Ratschlag.
3. **Eine Least-Privilege-Rolle.** Sie geben dem Connector eine Rolle, die
   `SELECT` und sonst nichts besitzt. Siehe die Referenzrolle unten.

Das ist stärker als bei jedem gemanagten etablierten Anbieter, der read-only
nur als *Ratschlag* dokumentiert.

### Least-Privilege-Rolle

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Never grant INSERT/UPDATE/DELETE/DDL. Optionally pin the role read-only:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

Gewähren Sie für den engsten Scope `SELECT` nur auf den Tabellen, die Sie
aufnehmen möchten.

## Definieren, wie eine Zeile zum Dokument wird

Die Dokumentdefinition ist deklarativ — Sie legen fest, welche Spalten Key,
Body, Titel, ACL, Klassifizierung und Sync-Cursor bilden:

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

Statt einer `table` können Sie eine read-only `query` (ein validiertes
`SELECT`) angeben — nützlich, um eine ACL-Tabelle zu joinen oder die
bereitzustellenden Zeilen zu filtern. Die Zugangsdaten sind immer eine
**Secret-Store-Referenz** (`vault:…`, `aws-secretsmanager:…`, …); ein
Klartext-Secret wird abgelehnt.

## Wie ACLs *ehrlich* abgebildet werden

Der Connector bildet **nur das ab, was die Zeile ausdrückt**. Er erstellt die
ACL eines Dokuments aus den Werten der von Ihnen deklarierten `acl_columns`
(z. B. eine Spalte `owner_group` → `group:eng`). Er **erfindet keine**
zeilenbezogene ACL, die die Quelle nicht trägt, und legt diese Grenzen offen:

| Situation | Verhalten des Connectors |
|---|---|
| Eine `owner_group`-/Rollenspalte | Bildet jeden Wert auf eine ACL-Referenz ab (`<acl_prefix><value>`). |
| Keine `acl_columns` deklariert | Das Dokument erbt die **Standard-ACL** der Knowledge Base — beim Retrieval wird sie weiterhin erzwungen. |
| **Row-Level Security (RLS)** auf der Tabelle | Wird implizit respektiert: Die Rolle des Connectors sieht genau die Zeilen, die RLS ihr erlaubt. Der Connector implementiert RLS nicht erneut, sondern erbt es. |
| Eine Berechtigung, die die Tabelle **nicht** als Spalte modelliert | **Nicht ableitbar** → nicht abgebildet. Modellieren Sie sie als Spalte (oder über `query` als gejointe ACL-Tabelle), wenn sie erzwungen werden soll. |

Das ist der bewusste Unterschied zu gemanagten etablierten Anbietern, bei
denen ACL-Spalten manuell definiert werden müssen und die zugleich kein
RLS-Passthrough anbieten. Auch hier bilden Sie ACL-Spalten manuell ab, **aber**
der Connector respektiert zusätzlich RLS und erfindet niemals Berechtigungen,
die der Zeile fehlen.

## Klassifizierung pro Spalte

Führen Sie sensible Spalten in `sensitive_columns` auf. Hat eine Zeile in
einer solchen Spalte einen Wert, erhält das Dokument ein externes Label
`"<sensitive_label>:<column>"` (z. B. `pii:ssn`). Diese Labels speisen das
Retrieval-DLP und werden neben der `classification_column` der Zeile
deny-closed erzwungen.

## Live vs. Export

- **`mode: live`** liest die Datenbank über den read-only Pool und unterstützt
  einen **inkrementellen (Delta-)Sync** mit dem Cursor `updated_at_column`;
  ohne konfigurierten Cursor dient eine vollständige Listen-Reconciliation als
  Fallback.
- **`mode: export`** parst einen statischen Zeilen-Snapshot (einen von Ihnen
  out-of-band erzeugten JSON-Dump). Ein Snapshot wird **niemals als live
  dargestellt** — die Quelle meldet ihren Modus ehrlich.

## Ehrliche Grenzen

- Ein Dokument-**Body ist auf 1 MiB begrenzt**; eine größere Zeile wird
  abgeschnitten (Streaming sehr großer Spalten ist eine Folgemaßnahme).
- Eine vom Operator bereitgestellte `query` muss eine **Spalte, die wörtlich
  wie ein SQL-Schlüsselwort heißt** (z. B. `update`), mit einem Alias versehen
  — der Read-only-Guard ist fail-closed.
- Der Connector liest Inhalte; **Aktionen auf der Datenbank liegen außerhalb
  des Scopes** (es gibt konstruktionsbedingt keinen Write-Pfad), ebenso
  CDC-Streaming und NL-to-SQL.

## Wire-Proof

Der Connector enthält ein Wire-Proof-E2E (`-tags e2e`, CI), das gegen eine
echte PostgreSQL-Instanz läuft: Es verifiziert bei `Open` die read-only
Session, nimmt vorbereitete Zeilen mit ihren abgebildeten ACLs und
Klassifizierungen auf und weist nach, dass PostgreSQL einen Write in der
read-only Session **ablehnt**. Siehe
`connectors/pgcontent/testdata/docker-compose.e2e.yml`.
