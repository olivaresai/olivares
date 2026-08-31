---
title: An Splunk weiterleiten (einen Universal Forwarder absetzen + tailen)
description: >-
  Bringen Sie die Governance-Findings der Control Plane und ihr manipulationserkennbares Audit-Ledger
  in Splunk, indem Sie eine Datei mit einem Universal Forwarder tailen — ohne einen nativen
  Splunk-zu-Splunk-Emitter. Ehrlich darüber, welcher Stream welcher ist.
---

Sie können Olivares-AI-Daten **heute** in Splunk bekommen, ohne auf einen nativen
Connector zu warten: Schreiben Sie die Daten in eine Datei und richten Sie einen **Splunk Universal Forwarder (UF)**
darauf. Der UF übernimmt den Splunk-zu-Splunk-Hop (S2S) zu Ihrem Indexer.

:::caution[Es gibt keinen nativen Splunk-S2S-Emitter]
Olivares AI implementiert das proprietäre S2S-Forwarder-Protokoll von Splunk **nicht**. Ein
nativer S2S-Emitter ist post-v1. Die unterstützten Postures sind **File-Tail-Forwarding**
(ein UF tailt eine Datei, die Olivares schreibt), der **Pull-Export** (für WORM-Archivierung und
Offline-Neuverifizierung) und ein **HTTP-Push über Splunk HEC** — einschließlich, seit
der SIEM-Interop-Arbeit, eines Pushs des **Ledgers selbst** über einen Eventing-Sink
([An Ihr SIEM pushen](/de/how-to/cookbook/push-to-siem/)). Diese Seite dokumentiert die
Datei- und Pull-Pfade; das Rezept behandelt den Push.
:::

Es gibt **zwei verschiedene Streams**, und sie sind nicht dasselbe. Wählen Sie
bewusst:

| Stream | Was es ist | Wege nach Splunk |
|---|---|---|
| **Governance / Findings** | der Notification-Stream, den Modul IX routet (Health-, Spend-, Security-, Compliance-Findings) | der `filelog`-Output-Connector hängt ihn an eine Datei an; oder `splunkhec` pusht ihn; oder ein [Eventing-Sink](/de/how-to/cookbook/push-to-siem/), der auf `finding.reported` abonniert ist |
| **Manipulationserkennbares Audit-Ledger** | der append-only, hash-verkettete, signierte Audit-Trail | der **Pull**-Export `GET /v1/audit/export` (diese Seite); oder die **Push**-Pumpe — ein Eventing-Sink, der auf `audit.recorded` abonniert ist, mindestens einmal (at-least-once) zugestellt. Es gibt keinen nativen *File*-Sink; materialisieren Sie eine Datei mit dem geplanten Export unten |

## Stream A — Findings, über den `filelog`-Connector

Der `filelog`-Output-Connector hängt den Notification-/Findings-Stream **ein Datensatz
pro Zeile** an eine Datei an (oder `stdout`/`stderr`), die ein UF tailen kann. Konfigurieren Sie eine
Notification-Destination der Art `filelog` mit diesen Feldern:

| Feld | Bedeutung |
|---|---|
| `path` | Append-Ziel: ein Dateipfad, oder `stdout`/`stderr`/`-` |
| `format` | Pro-Zeilen-Format: `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim` (Standard `json`) |
| `hostname` | syslog-`HOSTNAME`-Feld (für das `syslog`-Format) |
| `fsync` | jeden Datensatz auf die Platte flushen (Durability für eine WORM-Kopie; langsamer) |

Für Splunk funktionieren sowohl `format: json` (reichhaltige Felder) als auch `format: cef`/`syslog` (Zeilenformate, die Splunk
nativ parst). Die Datei wird append-only geöffnet, sodass dieselbe Datei zugleich
als unveränderliche externe Kopie dient, wenn sie auf WORM-Storage abgelegt wird.

:::note[`filelog` trägt Findings, nicht das signierte Ledger]
Der `filelog`-Connector leitet den **Findings**-Stream weiter — er sieht niemals das
manipulationserkennbare Audit-Ledger. Um das verifizierbare Ledger weiterzuleiten, verwenden Sie Stream B.
:::

### Schlüsselfertige Alternative: Splunk HEC

Wenn Sie lieber über HTTP pushen, als eine Datei zu tailen, postet der `splunkhec`-Connector
denselben Findings-Stream an Splunks HTTP Event Collector (`/services/collector`)
mit einem `Authorization: Splunk <token>`-Header — ein schlüsselfertiger HTTP-Pfad, weiterhin nicht S2S
und weiterhin der Findings-Stream, nicht das Ledger.

## Stream B — das manipulationserkennbare Ledger, über den Pull-Export

Das Audit-Ledger wird als **authentifizierter Pull-Export** bereitgestellt, nicht als Datei, die die
Engine von sich aus schreibt. Jeder Datensatz trägt die Felder zur Ketten-Integrität
(`seq`, `prev_hash`, `hash`, `sig`), sodass Ihr SIEM die Hash-Kette **offline neu verifizieren**
kann; PII wird niemals exportiert.

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Unterstützte `format`-Werte sind `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`,
`otlp_log_record` und `ocsf`. `otlp` ist eine vollständige, postbare OTLP/HTTP-Exportanfrage
pro Datensatz, `otlp_envelope` ist ihr exakter Alias, und
`otlp_log_record` ist die reine LogRecord-Projektion (ein LogRecord pro Zeile). Zeilen-
formate (`cef`/`leef`/`syslog`) streamen als `text/plain`; `otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` streamen als
NDJSON (`application/x-ndjson`), ein JSON-Objekt pro Zeile.

:::note[`ocsf` ist OCSF v1.8.0 API Activity]
Die früheren Ausgaben dieser Seite vermerkten, dass der Fehlertext der Engine
`ocsf` aus der beworbenen Liste auslässt — diese Lücke wurde upstream behoben; die Zusammenfassung und
die Bad-Request-Meldung werden beide aus der Format-Registry der Engine gebaut und nennen daher stets jedes akzeptierte Format.
:::

### Inkrementelles Tailing mit einem Cursor

Der Export paginiert die lückenlose Kette nach Sequenznummer über `?from=`. Um eine Datei
kontinuierlich angehängt zu halten, damit der UF sie tailt, führen Sie einen kleinen geplanten Job aus, der ab der
letzten gesehenen Sequenz fortsetzt:

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

Jeder Export endet mit einem Abschluss-Terminator — einem
`# olivares-audit-export-complete count=N last_seq=M`-Kommentar für die Textformate,
oder einer `{"export_complete":true,...}`-JSON-Zeile für
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf`. **Sein Fehlen bedeutet,
dass der Stream abgeschnitten wurde** — rücken Sie den Cursor nicht vor, wenn er fehlt.

## Den Universal Forwarder auf die Datei richten

Welchen Stream Sie auch gewählt haben, installieren Sie einen Splunk UF auf dem Host und fügen Sie einen
`monitor://`-Input hinzu. Es wird keine `inputs.conf` mit Olivares AI ausgeliefert — dies ist die Stanza, die Sie
hinzufügen:

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

Der UF leitet über S2S an Ihren Indexer weiter; Olivares AI spricht selbst niemals S2S.

## Zusammenfassung dessen, was unterstützt wird und was nicht

- **Unterstützt:** File-Tail-Forwarding (UF tailt eine Datei) — für beide Streams.
- **Unterstützt:** Splunk-HEC-Push — für den Findings-Stream (`splunkhec`-
  Destination) **und** für das Ledger und die Findings über einen Eventing-**Sink**
  (`sink_kind: splunk_hec`, Events `audit.recorded` / `finding.reported`,
  at-least-once) — siehe [An Ihr SIEM pushen](/de/how-to/cookbook/push-to-siem/).
- **Unterstützt:** Offline-Neuverifizierung des Ledgers — sowohl der Pull-Export als auch die Push-Pumpe
  tragen die Hash-Ketten-Felder wörtlich, sodass ein SIEM die Integrität neu verifizieren kann.
- **Nicht unterstützt:** Nativer Splunk-S2S-Emitter — nicht implementiert (post-v1).
- **Nicht unterstützt:** Automatischer Ledger-*File*-Sink — um das Ledger in eine lokale Datei zu bekommen,
  materialisieren Sie es mit dem geplanten Pull-Export oben (die Push-Pumpe zielt auf HTTP-
  Sinks, nicht auf Dateien).
