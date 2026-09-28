---
title: "Mit Prometheus überwachen (SLOs, Metriken, Alerts)"
description: >-
  Scrapen Sie das /metrics der Engine, übernehmen Sie die veröffentlichten
  SLO-Ziele und laden Sie die mitgelieferten Burn-Rate-Alert-Regeln — dieselben
  SLIs, auf die sich die Runbooks des Produkts selbst stützen, mit den
  Single-Writer-Zahlen ehrlich benannt.
---

Die Engine stellt auf dem HTTP-Listener drei operative Endpunkte bereit, alle
probe-freundlich:

| Endpunkt | Auth | Zweck |
|---|---|---|
| `/livez` | keine | Prozess-Liveness — **keine Abhängigkeitsprüfungen**, sodass ein Store-Ausfall nie eine Neustart-Schleife auslöst |
| `/readyz` | keine | Readiness — Store-Ping (und HA-Leadership): `200 {"status":"ok","store":"up","leader":true,…}`, `503 {"status":"unavailable","store":"down"}`, oder `503 {"status":"standby",…,"leader":false}` auf einem HA-Standby |
| `/metrics` | keine | Prometheus-Exposition. Bewusst unauthentifiziert: trägt operative Reihen, niemals Tenant-Daten |

Die Erreichbarkeit von `/readyz` **ist** der Verfügbarkeits-SLI.

## Der Metriksatz, auf den es ankommt

Alle Reihen werden von der Engine registriert (gegen den aktuellen Code verifiziert);
die tragenden:

| Reihe | Was sie Ihnen sagt |
|---|---|
| `olivares_store_up` | der Store beantwortet einen Ping — das Erste, was jedes Runbook prüft |
| `olivares_http_requests_total{code}` | Request-Erfolgs-SLI (`code!~"5.."`) |
| `olivares_http_request_duration_seconds` | API-Latenz (p99-Ziel unten) |
| `olivares_ingest_duration_seconds` | **der Backpressure-SLI** — die Ingest-p99 steigt, wenn ein Subscriber sättigt |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | Ingest-Durchsatz und Ablehnungen |
| `olivares_eventbus_queue_depth` / `_queue_capacity` (pro Subscriber) | welches Modul der langsame Konsument ist |
| `olivares_eventbus_publish_blocked_total` | Backpressure-Ereignisse (der Bus blockiert; er verwirft nicht) |
| `olivares_eventbus_bridge_*` | NATS-Bridge-Gesundheit, wenn der verteilte Bus aktiv ist — `_connected`, `_pending_messages`, `_dropped_total` (Cross-Node-Zustellung ist at-most-once; Drops werden gezählt, niemals verschwiegen) |
| `olivares_audit_checkpoint_age_seconds` | Aktualität des Manipulationsnachweises — alarmieren, wenn sie das 2-fache des Checkpoint-Intervalls überschreitet |
| `olivares_auth_login_attempts_total{outcome}` | Login-Erfolg / -Fehlschlag / -Sperre / -Abbruch |
| `olivares_http_ratelimit_decisions_total{decision}` | Rate-Limit-Druck |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | die Collector→Core-Ingest-Plane |

## SLO-Ziele (veröffentlicht, ehrlich)

Betriebsziele für Single-Node- und HA-Bereitstellungen, keine gemessenen Ergebnisse
der Topologie oder Kundenzusagen. Bevor Sie die Zielerreichung behaupten, qualifizieren
Sie die konkrete Bereitstellung und Last; `deploy/support-matrix.md` dokumentiert den Stand:

| SLI | Single Node | HA-Stufe (Postgres) |
|---|---|---|
| Verfügbarkeit (`/readyz`) | **99,5 % / 28 T** | 99,9 % / 28 T |
| Request-Erfolg (non-5xx) | **99,9 %** | 99,95 % |
| API-Latenz p99 | **< 300 ms** | < 200 ms |
| Ingest-Latenz p99 | **< 250 ms** | < 150 ms |
| Ingest-Erfolg | **99,9 %** | 99,95 % |

Über 28 Tage erlaubt ein Verfügbarkeitsziel von 99,5 % **201,6 Minuten
(3 h 21 min 36 s)** Ausfallzeit; bei 99,9 % sind es **40,32 Minuten**.
Die Bereitstellung der [HA-Topologie](/de/tutorials/getting-started/kubernetes/#3-active-passive-ha) allein erreicht keinen dieser Werte.
Messen Sie die Verfügbarkeit über das angegebene Zeitfenster und bewahren Sie
Nachweise zu Fehlerfällen und Wiederherstellung der konkreten Bereitstellung auf.
Diese Ziele sind keine vertragliche Verfügbarkeitszusage.

## Die mitgelieferten Alert-Regeln laden

`deploy/monitoring/olivares-slo.rules.yaml` liefert 14 Alerts, fertig für Ihr
Prometheus: Multi-Window-Burn-Rate-Alerts auf das Request-Erfolgs-Budget
(fast 14,4× Page / medium 6× Page / slow 1× Ticket), absolute Latenz- und
Verfügbarkeits-Auslöser (`OlivaresIngestP99High`, `OlivaresApiLatencyP99High`,
`OlivaresStoreDown`, `OlivaresControlPlaneUnscrapeable`), Sättigung
(`OlivaresEventBusSaturated` bei >90 % Queue für 10 m), Bridge-Gesundheit
(`OlivaresEventBusBridgeDropping`, `OlivaresEventBusBridgeDisconnected`) und
Ledger-Frische (`OlivaresAuditCheckpointStale` bei Alter > 2 h).

Das Burn-Rate-Rezept verwendet einen Referenzzeitraum von 30 Tagen; die obigen
Verfügbarkeitsziele gelten für 28 Tage. Unterscheiden Sie Alert-Raten und
Auswertefenster von diesem Zeitraum: 14,4× über 1 h, 6× über 6 h und 1× über 3 Tage
verbrauchen rund 2,14 %, 5,36 % beziehungsweise 10,71 % eines 28-Tage-Budgets.
Die mitgelieferten Alert-Werte bleiben unverändert.

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

Auf Kubernetes verdrahtet die `ServiceMonitor`-Option des Charts den Scrape für den
Prometheus-Operator. Eine Gatus-Statusseiten-Konfiguration für die externe `/readyz`-Probe
wird neben den Regeln ausgeliefert (`deploy/monitoring/status-page.gatus.yaml`).

## Wenn ein Alert auslöst

Symptom-für-Symptom-Diagnose — Store down, Ingest-p99 hoch, Bus gesättigt,
Checkpoint veraltet — steht auf der [Troubleshooting-Seite](/de/how-to/troubleshooting/),
destilliert aus denselben Runbooks, auf die sich die Alert-Annotationen beziehen.
