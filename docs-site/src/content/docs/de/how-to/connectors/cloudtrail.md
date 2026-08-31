---
title: "AWS CloudTrail für S3 (Clean-Tier R/RW)"
description: >-
  Lese-/Schreibzugriff auf S3-Objekte aus CloudTrail-Data-Events erfassen — das
  readOnly-Flag wörtlich übernommen, der IAM-Principal als Ursprung, ehrlich
  approximative Attribution, wenn eine assumed Role den echten Aufrufer
  verbirgt.
sidebar:
  order: 2
---

Die Quelle `s3cloudtrail` verwandelt AWS-CloudTrail-**S3-Data-Events** in
Access-Map-Edges: eine Edge pro S3-Event, mit dem Lese-/Schreibmodus
**wörtlich aus CloudTrails `readOnly`-Feld** übernommen — niemals abgeleitet —
und dem IAM-Principal, dem CloudTrail den Aufruf zuschreibt, als Ursprung. Es
ist das Clean-Tier für Objektspeicher, das S3-Gegenstück zu
[pgAudit](/de/how-to/connectors/pgaudit/) für Postgres.

Der Connector **liest lokale Log-Dateien und ruft niemals AWS auf**: Du
lieferst die CloudTrail-Dateien (das Standard-S3-Delivery-Layout, das dein
Trail bereits erzeugt), er parst sie. Es werden nur Events mit
`eventSource == s3.amazonaws.com` verarbeitet — Management-Plane-Events
gehören zum
[`aws`-Cloud-Discovery-Connector](/de/reference/connectors/), nicht zu diesem.

## Was er emittiert

| Feld | Wert |
|---|---|
| Signalquelle | `cloudtrail` |
| Modus | `readOnly: true` → `read`, `false` → `write`, fehlt → `unknown` — wörtlich, niemals geraten |
| Ursprung | der IAM-Principal (Benutzer, Assumed-Role-Session, AWS-Service) |
| Konfidenz | `attributed`; `approximate` für geteilte assumed Roles und service-aufgerufene Calls |
| Coverage-Tier | clean |

## 1. Voraussetzungen auf AWS-Seite

- Ein CloudTrail-**Trail mit aktivierten S3-Data-Events** für die Buckets, die
  du governst (Data-Events sind nicht im Standard-Management-Trail enthalten).
- Lieferung der Log-Dateien des Trails an einen Ort, den der Engine-Host lesen
  kann — das standardmäßige S3-Delivery-Bucket, lokal synchronisiert oder
  gemountet. Der Connector akzeptiert die klassischen `{"Records":[…]}`-Dateien
  (unkomprimiert oder `.json.gz`) sowie newline-delimited Records.

## 2. Die Quelle deklarieren

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

| Schlüssel | Erforderlich | Bedeutung |
|---|---|---|
| `path` | ja | eine CloudTrail-Datei oder ein Verzeichnis mit `*.json`- / `*.json.gz`-Dateien |
| `shared_accounts` | nein | kommaseparierte Role-ARNs, die viele Aufrufer teilen — ihre Edges sind ehrlich `approximate` |

(`s3-cloudtrail` wird als Alias für das `kind` akzeptiert.)

## 3. Was du in der Konsole siehst

S3-Buckets und -Objekte treten der **Access map** mit Clean-Tier-Badges bei;
Reads und Writes werden anhand des `readOnly`-Flags eingefärbt. Das
Drift-Panel kreuzt sie genau wie bei jeder anderen Quelle gegen deklarierte
Grants.

In **Inventory** erscheinen die Principals, denen CloudTrail Aufrufe
zuschreibt, als Identitäten, bereit, an Agenten gebunden zu werden — diese
Bindung ist es, die aus einem Shared-Role-`approximate` ein
Per-Agent-`attributed` macht.

## Ehrliche Grenzen — lies sie, bevor du der Map vertraust

- **Eine von vielen Aufrufern geteilte assumed Role kann den echten Aufrufer
  nicht benennen.** CloudTrail schreibt den Aufruf der Role-Session zu; ist die
  Role geteilt, ist die Edge bewusst `approximate`. Die Role in
  `shared_accounts` zu deklarieren macht das explizit. Die dauerhafte Lösung
  ist Per-Agent-Identität ([die Identitäts-Abhängigkeit](/de/how-to/connect-a-source/#die-harte-abhängigkeit-per-agent-identität)).
- **Data-Events, die du nicht aktiviert hast, existieren nicht.** CloudTrail
  zeichnet nur auf, wozu der Trail konfiguriert ist; das Fehlen einer Edge ist
  kein Fehlen von Zugriff, wenn Data-Events für ein Bucket aus sind.
- **Die Lieferlatenz ist CloudTrails.** Data-Events treffen nach CloudTrails
  Lieferplan ein (typischerweise Minuten); diese Quelle ist kein
  Echtzeit-Tap.

## Verwandt

- [pgAudit](/de/how-to/connectors/pgaudit/) — dieselbe Clean-Tier-Disziplin für
  PostgreSQL.
- [Eine Quelle anbinden](/de/how-to/connect-a-source/) — das Connector-Modell.
- [Connectors & Coverage-Tiers](/de/reference/connectors/) — wo jeder Speicher
  ehrlich einzuordnen ist.
