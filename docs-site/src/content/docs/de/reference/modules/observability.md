---
title: "Observability — das Read-Model der Engine über sich selbst"
description: >-
  Ein reines Read-Model über das, was bereits existiert: welche
  Interop-Standards die Engine fixiert und bereitstellt, was das
  W3C-korrelierte Ledger über einen Trace aussagt und was über die Lieferkette
  des laufenden Binaries beweisbar wahr ist. Es besitzt keine Entitäten und
  persistiert nichts.
---

Observability (`modules/observability`) ist eines der 32 Module — wie
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

## Die Read-Model-Journey reproduzieren

Führen Sie auf einer **frischen, wegwerfbaren Enterprise-SQLite-Compose-Installation**
`scripts/qualify-observability.py` mit Python 3.11 oder neuer aus. Geben Sie das installierte
Binary, die HTTPS-Adresse, den Containernamen, den vollständigen Quell-Commit und ein neues
Evidenzverzeichnis an:

```sh
python3 scripts/qualify-observability.py /path/to/olivares \
  https://127.0.0.1:8443 olivares <source-sha> /path/to/new-evidence
```

Die Probe nutzt die Organisationsverwaltung von Enterprise, um einen zweiten Mandanten für ihre Isolationsprüfungen bereitzustellen. Die Oberflächen ingestion-health, Trace-Read-Models und Attestation bleiben in Community verfügbar.

Die Probe legt über die API Fixture-Konten und -Agenten an, aktiviert die vorhandenen
Module Observability und Security und startet den Container physisch neu. Sie vergleicht den
SHA-256 der gestarteten ausführbaren Datei mit dem angegebenen Binary, verifiziert die Ingestion
anhand eines First-Party-Findings, prüft Mandanten-Ablehnungen und prüft die Aufbewahrung von
Ledger-Trace-Details und -Export über Neustart und Modul aus/ein hinweg. Außerdem verifiziert
sie, dass sich die Ingestion-Zähler zurücksetzen und dass die Attestation die gemessene
Binary-Identität vom deklarierten Pipeline- und unveröffentlichten Release-Zustand trennt.
CLI-Prüfungen verwenden JSON-Ausgabe. Zugangsdaten bleiben im Speicher oder in
Kindprozess-Umgebungen; aufbewahrte Engine-Logs maskieren Setup-Token.

Verwenden Sie die Installationsschritte im README mit einem ausdrücklich gewählten
Kandidaten-Image. Führen Sie die Probe niemals gegen eine bestehende Installation aus.
Python-Optimierung wird abgelehnt, weil sie Assertions deaktiviert. Entfernen Sie den
wegwerfbaren Compose-Stack und seine Volumes nach dem Lauf. Die Probe überlässt diese
Bereinigung dem Aufrufer und schreibt ihr Ergebnis und das Engine-Log in das
Evidenzverzeichnis. Sie prüft weder Konsolenaktionen noch PostgreSQL, Vendor-Collectors oder
die Verifikation von Release-Signaturen.

## Verwandt

- [Mit Prometheus überwachen](/de/how-to/monitor-with-prometheus/) — die
  operativen Metriken und SLOs.
- [Events-Referenz](/de/reference/events/) — das Bus-Vokabular, über das die
  ingestion-Tabelle berichtet.
- [Ein Release verifizieren](/de/how-to/verify-a-release/) — die
  Lieferketten-Evidenz, die die Attestation-Oberfläche widerspiegelt.
