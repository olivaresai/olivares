---
title: "Observability — das Read-Model der Engine über sich selbst"
description: >-
  Ein reines Read-Model über das, was bereits existiert: welche
  Interop-Standards die Engine fixiert und bereitstellt, was das
  W3C-korrelierte Ledger über einen Trace aussagt und was über die Lieferkette
  des laufenden Binaries beweisbar wahr ist. Es besitzt keine Entitäten und
  persistiert nichts.
---

Observability (`modules/observability`) ist eines der 30 Module — wie
[live-ingest](/de/reference/modules/live-ingest/) erfüllt es eine
architektonische Rolle, statt einen Capability-Slot zu besetzen. Es ist das
**Read-Model der Engine über sich selbst**: drei schreibgeschützte Oberflächen
unter `/v1/m/observability/`, die jene Fragen beantworten, die der System-Bereich
der Admin-Konsole darstellt, ohne eine einzige Store-Entität zu besitzen.

## Die drei Oberflächen

| Route | Beantwortet |
|---|---|
| `GET /ingestion-health` | was **pro Interop-Standard** in die Engine hinein- und aus ihr herausfließt — die Standards, die die Engine fixiert (OTel-GenAI-semconv, OCSF, ASIM, die vereinheitlichten SIEM-Formate, der Ledger-Push, Prometheus-Text, W3C Trace Context), jeweils mit ihrer verifizierten Version |
| `GET /traces`, `GET /traces/{id}` | was das **W3C-korrelierte Ledger** über einen Trace aussagt — die Audit-seitige Sicht auf einen verteilten Trace, verknüpft über Trace Context |
| `GET /attestation` | was über die Lieferkette des laufenden Binaries **beweisbar wahr** ist — die Attestation-Oberfläche, die die [Kette zur Verifikation eines Releases](/de/how-to/verify-a-release/) speist |

Alle drei sind Lesevorgänge mit modul-gebundenen Berechtigungen; nichts hier
verändert irgendetwas.

## Warum es überhaupt ein Modul ist

Die Admin-Konsole benötigte eine maßgebliche Antwort auf die Frage „Was spricht
diese Engine tatsächlich, und in welcher fixierten Version?" — und der ehrliche
Weg, das bereitzustellen, ist aus der Engine selbst heraus, nicht aus einer
Dokumentation, die abdriften kann. Die ingestion-health-Tabelle wird aus
denselben Pins generiert, gegen die die Connectors und Exporter kompiliert
werden, sodass die Oberfläche mitwandert, wenn sich ein Pin bewegt.

## Bounded Context, klar benannt

- **Es besitzt keine Store-Entitäten und persistiert nichts** — ein reines
  Read-Model über Substrate, die bereits existieren (die Pins, das Ledger, die
  Attestation-Evidenz).
- Es ist **nicht** [Modul XXII (Health/SLA)](/de/reference/modules/xxii-health/),
  das auf die Zuverlässigkeit der Agenten und MCP-Server des *Estates* beschränkt
  ist. Dieses Modul betrifft die *Engine*.
- Es ist **nicht** der Metrics-Endpunkt: operative Zeitreihen leben auf
  [`/metrics`](/de/how-to/monitor-with-prometheus/); dieses Modul liefert
  strukturierte Antworten, keine Zeitreihen.

## Verwandt

- [Mit Prometheus überwachen](/de/how-to/monitor-with-prometheus/) — die
  operativen Metriken und SLOs.
- [Events-Referenz](/de/reference/events/) — das Bus-Vokabular, über das die
  ingestion-Tabelle berichtet.
- [Ein Release verifizieren](/de/how-to/verify-a-release/) — die
  Lieferketten-Evidenz, die die Attestation-Oberfläche widerspiegelt.
