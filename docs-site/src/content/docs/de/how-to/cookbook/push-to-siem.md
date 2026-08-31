---
title: "Rezept: Findings und das Ledger an Ihr SIEM pushen"
description: >-
  Erstellen Sie einen Push-Sink — Splunk HEC, Microsoft Sentinel, Datadog oder New Relic,
  oder einen generischen HMAC-signierten Webhook — und abonnieren Sie damit Findings und das
  versiegelte Audit-Ledger, mindestens einmal zugestellt (at-least-once) in OCSF, CEF oder dem Format,
  das Ihr Tower spricht.
sidebar:
  order: 6
---

**Ziel:** Ihr SIEM empfängt die Findings der Control Plane *und* ihr
manipulationserkennbares Audit-Ledger als Push, ohne dass ein Forwarder Dateien tailt.

Dies ist der S2S-Push-Pfad (Service-to-Service) auf der Eventing-Plattform. Die
[Pull-Export- und File-Tail-Postures](/de/how-to/forward-audit-to-splunk/) bleiben
vollständig unterstützt — Pull ist weiterhin die richtige Form für WORM-Archivierung und Offline-
Neuverifizierung; Push ist die richtige Form für die Live-SIEM-Ingestion.

## 1. Das Sink-Abonnement erstellen

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

- **`sink_kind`** wählt den Tower-Dialekt: `splunk_hec`, `sentinel_dcr`,
  `datadog`, `newrelic` — oder lassen Sie es ganz weg für den **generischen Webhook**
  (ein HTTPS-Endpunkt, der das JSON-Event empfängt, authentifiziert durch die
  HMAC-Signatur der Engine; rotieren mit `…/{id}/rotate-secret`).
- **`sink_format`**: `ocsf` (der Standard für SIEM-Sinks — das KI-bewusste
  Schema), `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope` oder `json`.

  :::caution[`sink_format` braucht ein `sink_kind`]
  Ein Format wird nur angewendet, wenn eine Sink-Art gesetzt ist. **`sink_kind`
  weglassen ist NICHT "die HTTPS-Option"** — es wählt den generischen Webhook, der
  das Olivares-Event-JSON sendet und `sink_format` nie validiert. Um einen
  SIEM-Dialekt an einen eigenen Endpunkt zu posten, `sink_kind: "https"` explizit
  setzen:

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  Für `otlp` (und `otlp_envelope`, seinen exakten Alias) muss der Endpunkt der
  exakte `/v1/logs`-Pfad des Collectors sein — der Body wird wortgetreu an die
  URL gepostet.
  :::
- **`sink_cred`** (das HEC-Token / DCR-Bearer / der API-Key) wird einmal akzeptiert,
  **im Ruhezustand versiegelt, niemals zurückgegeben oder geloggt**. Die Vendor-Kinds benötigen es
  bei der Erstellung; der generische Webhook braucht keines.
- **`event_types`** ist Ihre Stream-Auswahl: `finding.reported` für die
  Findings-Schiene, `audit.recorded` für das Ledger (unten), oder beides.

Testen Sie die Zustellung, bevor Sie ihr vertrauen:

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. Der Ledger-Push, ehrlich beschrieben

Das Abonnieren von **`audit.recorded`** aktiviert die Ledger-Pumpe: Der Forwarder
durchläuft das versiegelte Audit-Ledger jedes Tenants ab einem Tenant-spezifischen Cursor und stellt
jeden Datensatz in die dauerhafte Zustellungs-Engine ein — **mindestens einmal (at-least-once)**, in Reihenfolge,
fortsetzbar. Jeder Datensatz trägt seine Felder zur Ketten-Integrität unverändert, sodass die
SIEM-Kopie genau das erlaubt, was der Pull-Export erlaubt: die
Ketten-VERKNÜPFUNG (`prev_hash` von n+1 gleich `hash` von n) und eine
Checkpoint-Signatur über `hash` sind offline prüfbar, und der `hash` eines Datensatzes
lässt sich jetzt aus EINER exportierten Zeile NEU BERECHNEN — jede Eingabe des
Kettenhashs steht auf der Leitung, einschließlich des kanonischen
`occurred_at`-Texts und des Metadaten-Commitments. Dieses Commitment ist pro
Datensatz geblendet: Es vervollständigt das Preimage, ohne etwas über die
dahinterliegenden Metadaten preiszugeben. Drei Aussagen bleiben getrennt — den Hash
neu zu berechnen ist weder eine Prüfung der AUTHENTIZITÄT (dafür braucht es einen
extern vertrauenswürdigen Schlüssel) noch der VOLLSTÄNDIGKEIT (dafür braucht es
benachbarte Datensätze und einen Checkpoint). Das Audit-*Archiv* bleibt das stärkere
Artefakt: Es trägt die Metadaten selbst samt ihrem Blend und kann daher auch
beantworten, WELCHE Metadaten ein Commitment abdeckt.

Drei Eigenschaften, die man kennen sollte:

- **Kein Abonnement, keine Arbeit.** Ohne einen `audit.recorded`-Abonnenten schreibt die Pumpe
  nichts — der Pfad kostet nichts, bis Sie ihn anfordern.
- **At-least-once bedeutet, dass Duplikate möglich sind** bei der erneuten Zustellung; deduplizieren Sie
  über die Sequenznummer des Datensatzes pro Tenant.
- **Die Pumpe ist Leader-gated** in HA — genau ein Knoten leitet weiter.

## 3. ITSM: Findings als Tickets

Derselbe Abonnement-Mechanismus steuert ITSM-Ziele über die
Notification-Schiene — ServiceNow-Incidents und Jira-Issues aus Findings, mit
auf Priorität gemapptem Schweregrad. Konfigurieren Sie diese als Notification-
**Destinations** (die Output-Connectors `servicenow` / `jira`) statt als
SIEM-Sinks; die [Destination-Tabelle der Splunk-Seite](/de/how-to/forward-audit-to-splunk/)
zeigt das Muster.

## End-to-End verifizieren

1. `…/test` gibt „delivered“ zurück.
2. Lösen Sie etwas Beobachtbares aus (einen Schwellenwert eines [Budget-Alerts](/de/how-to/cookbook/budgets-and-finops-guardrails/),
   ein verweigertes Tool) und beobachten Sie, wie das Finding ankommt.
3. Für das Ledger: Vergleichen Sie die SIEM-seitige `seq`-Hochwassermarke mit
   `GET /v1/audit/export?from=<seq>` — die Streams müssen übereinstimmen.

## Hinweise

- Endpunkte müssen **HTTPS** sein; die Engine lehnt Klartext-Sinks ab.
- Posture-Snapshots (Compliance-/NHI-/Finding-Roll-ups) haben ihr eigenes Export-
  Modul, das auf denselben Schienen läuft — siehe das
  [Compliance-Modul](/de/reference/modules/xiii-compliance/).
- Die vollständige Entscheidungstabelle — wann pullen, wann tailen, wann pushen — finden Sie auf
  der [Splunk-Weiterleitungsseite](/de/how-to/forward-audit-to-splunk/).
