---
title: "PostgreSQL pgAudit (Clean-Tier R/RW)"
description: >-
  Erfassen Sie Lese-/Schreibzugriffe auf PostgreSQL aus seinem nativen
  pgAudit-Trail — das Clean-Tier-Signal: READ/WRITE wortgetreu aus dem
  Audit-CLASS übernommen, nie aus SQL abgeleitet, wobei der Connector nur die
  Logdatei liest.
sidebar:
  order: 1
---

Die `pgaudit`-Quelle verwandelt den eigenen Audit-Trail von PostgreSQL in
Access-Map-Kanten: eine Kante pro auditiertem Datenzugriff, wobei der
Lese-/Schreibmodus **wortgetreu aus dem CLASS-Feld von pgAudit** übernommen
wird — nie aus dem SQL-Text abgeleitet. Es ist die kanonische
**Clean-Tier-Quelle**: ein objekt-/relationaler Store, der Zugriffe in seinem
nativen Trail klassifiziert.

Der Connector ist **schreibgeschützt über eine Logdatei**. Er verbindet sich
nie mit der Datenbank, sieht nie Abfrageergebnisse und erfasst nie den
SQL-Body — die Identität, das Objekt und die Klassifikation sind allesamt
pgAudits eigene Ausgabe.

## Was er emittiert

| Feld | Wert |
|---|---|
| Signalquelle | `pg_audit` |
| Modus | aus CLASS, wortgetreu: READ → `read`, WRITE → `write`, DDL → `write` (ein Schema-Schreibvorgang), FUNCTION → `unknown` (pgAudit sagt es nicht); ROLE/MISC werden übersprungen, nicht erraten |
| Ursprung | der `application_name`, falls vorhanden (→ `attributed`), sonst die Session-Rolle |
| Confidence | `attributed`, oder `approximate` für von Ihnen als gemeinsam genutzt deklarierte Rollen/Apps |
| Coverage-Tier | clean |

## 1. pgAudit, strukturierte Logs und UTC aktivieren

Auf der PostgreSQL-Seite (das Standard-pgAudit-Setup — siehe die
pgAudit-Dokumentation für Ihre Hauptversion):

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

Zwei Einschränkungen ergeben sich daraus, wie der Connector parst — beide
gegen seine Implementierung verifiziert:

- **Der Server muss in UTC loggen.** PostgreSQL schreibt Zeitstempel mit einer
  Zonen-*Abkürzung*, und eine Nicht-UTC-Abkürzung lässt sich nicht zuverlässig
  in einen Offset auflösen — daher **überspringt** der Connector solche
  Records, statt einen falschen Zeitstempel zu raten. `log_timezone = 'UTC'`
  ist die unterstützte Konfiguration.
- **`csvlog` ist Batch; `jsonlog` kann folgen.** csvlog-Records können sich
  über Zeilenumbrüche erstrecken, daher wird dieses Format bei jedem Durchlauf
  als Batch gelesen; `jsonlog` ist zeilenbegrenzt und unterstützt
  kontinuierliches Tailing (`follow`, der Standard).

Damit die Attribution scharf wird, lassen Sie Anwendungen den
`application_name` pro Agent setzen — das ist es, was eine Kante von einer
gemeinsam genutzten Rolle zu einem attribuierten Ursprung hochstuft (siehe
[die Identitäts-Abhängigkeit](/de/how-to/connect-a-source/#die-harte-abhängigkeit-per-agent-identität)).

## 2. Die Quelle deklarieren

In Ihrer [Quellen-Konfiguration](/de/how-to/connect-a-source/#eine-echte-quelle-verkabeln)
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

Konfigurationsschlüssel (aus dem ausgelieferten Connector-Deskriptor):

| Schlüssel | Erforderlich | Standard | Bedeutung |
|---|---|---|---|
| `log_path` | ja | — | Pfad zur PostgreSQL-Logdatei, die der Engine-Host lesen kann |
| `format` | nein | `csvlog` | `csvlog` oder `jsonlog` |
| `follow` | nein | `true` | kontinuierliches Tailing (**nur jsonlog** — csvlog ist Batch) |
| `shared_accounts` | nein | — | kommagetrennte Rollen / application_names, die gemeinsam genutzt werden; ihre Kanten werden ehrlich als `approximate` markiert |

Starten Sie die Engine neu und bestätigen Sie die Boot-Zeile
`ingest: wired source … kind=pgaudit`.

## 3. Was Sie in der Konsole sehen

Öffnen Sie die **Access Map**. Jeder auditierte Zugriff wird als Kante von der
Rolle oder Anwendung zur Tabelle gerendert, eingefärbt als Lese- oder
Schreibzugriff, mit dem `CLEAN`-Coverage-Badge auf Postgres-Ressourcen. Das
Panel **Permitted vs observed** legt jeden Zugriff offen, der keinem Grant
entspricht — mit angebundenem pgAudit und noch ohne deklarierte Grants ist
*jeder* beobachtete Zugriff ehrlicher Drift, was der erwartete erste Zustand
ist.

## Ehrliche Grenzen

- **Es sieht, was pgAudit loggt.** Klassen, die Sie nicht aktivieren
  (`pgaudit.log`), werden nicht beobachtet; eine Abwesenheit von Kanten ist
  kein Beweis für ausbleibenden Zugriff, wenn die Klasse aus ist.
- **Die Attribution gehört der Datenbank.** Eine gemeinsam genutzte Rolle ohne
  `application_name` führt Aufrufer auf eine Identität zusammen — deklarieren
  Sie sie in `shared_accounts`, damit die Map `approximate` sagt, statt etwas
  vorzutäuschen.
- **FUNCTION ist `unknown` per Design** — das Ausführen einer Funktion kann
  lesen oder schreiben, und pgAudit sagt nicht, welches von beidem; das Produkt
  erzwingt kein Label. Nicht-Daten-Klassen (ROLE, MISC) werden übersprungen,
  statt als bedeutungslose Kanten emittiert zu werden.

## Verwandt

- [Eine Quelle verbinden](/de/how-to/connect-a-source/) — das Connector-Modell
  und die Honest-Tier-Taxonomie.
- [CloudTrail](/de/how-to/connectors/cloudtrail/) — dieselbe Clean-Tier-Idee
  für S3-Objekte.
- [Connectors & Coverage-Tiers](/de/reference/connectors/) — der vollständige
  Katalog.
