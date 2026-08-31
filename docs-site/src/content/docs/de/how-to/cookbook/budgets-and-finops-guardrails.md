---
title: "Rezept: Budgets & FinOps-Guardrails"
description: >-
  Setzen Sie ein hartes Dollar-Limit auf KI-Ausgaben — pro Modell, Team,
  Workspace oder einer einzelnen Identität: bei Schwellenwerten alarmieren, dann
  am Cap drosseln oder blockieren. Plus Cost-per-Outcome, damit die Ausgaben
  einen Nenner haben.
sidebar:
  order: 2
---

**Ziel:** „die Agenten dieses Teams hören bei 500 $/Monat auf zu geben“ —
einmal deklariert, live durchgesetzt, mit Alert-Schwellenwerten auf dem Weg
nach oben.

Die Budget-Durchsetzung ist eine der Aktuierungen, die **im Standard-Binary
live** sind: ein durchsetzendes Budget an seinem Cap verweigert die Ausgabe
ohne zusätzliche Provisionierung ([der Modul-Katalog](/de/reference/modules/overview/)
markiert es mit `v1 | v1`).

## Ein Budget erstellen

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **Geld ist in Mikro-USD** (`limit_micro_usd: 500000000` = 500 $), sodass es
  im Vertrag keine Float-Ambiguität gibt.
- **`dimension` + `key`** scopen das Budget. Gescopte Dimensionen umfassen
  `global`, `model`, `provider`, `agent`, `session`, `team`, `project`,
  `workspace`, `api_key`, `actor`, `service_tier`, `context_window`,
  `inference_geo`, `gateway` und `identity`.
- **`action`** ist der Durchsetzungsmodus:

| `action` | Am Cap |
|---|---|
| `alert` (Standard) | nur Showback — Alerts feuern, nichts wird verweigert |
| `throttle` | der Aktuierungs-Seam bremst neue Ausgaben |
| `block` | der Aktuierungs-Seam verweigert neue Ausgaben |

## Eine einzelne Identität budgetieren

`dimension: "identity"` scopt auf die **External ID einer festen
Roster-Identität** — die Workload- oder Agent-Identität, die Ihre
[Identitätsquellen](/de/how-to/connectors/sso-scim-identity/) registriert
haben:

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

Die Identität wird beim Cost-Ingest aus der Agent-Bindung, dem API-Schlüssel
oder dem Actor des Samples aufgelöst — sodass das Budget der Identität über
Oberflächen hinweg folgt, nicht einem API-Schlüssel.

## Es bei der Arbeit beobachten

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Am Cap gibt der Check eines durchsetzenden Budgets `allowed: false` zurück, mit
der Aktion (`throttle` oder `block`) und dem Budget, das gefeuert hat — die
Verweigerung benennt ihren Grund. Alerts reiten auch auf dem
Benachrichtigungs-Stream, sodass ein Slack- oder PagerDuty-[Ziel](/de/how-to/forward-audit-to-splunk/)
die 80%-Überschreitung hört, bevor die 100%-Verweigerung erfolgt.

In der Konsole zeigt **Cost & FinOps** die Ausgaben nach Dimension mit dem
Budget-Status inline:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Die Cost-&-FinOps-Ansicht mit Ausgabentrends und Budget-Posture." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Die Cost-&-FinOps-Ansicht mit Ausgabentrends und Budget-Posture." />

## Den Ausgaben einen Nenner geben: Outcomes

Cost-per-Outcome ist das, was aus einem Budget ein Business-Gespräch macht.
Melden Sie Outcomes (ein gelöstes Ticket, ein gemergter PR, ein
abgeschlossener Fall) und lesen Sie die Wert-Panels:

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Die Wert-Zusammenfassung enthält **Cancellation Risk** — Burn ohne Outcomes —
was die ehrliche Umkehrung einer Erfolgsmetrik ist.

## Hinweise

- **Bewusst Fail-open:** Wenn der Budget-Check selbst Fehler wirft (ein
  FinOps-Lesefehler), wird die Inferenz erlaubt statt still blockiert — ein
  defekter Zähler darf nicht zum Ausfall werden. Der Fehler wird geloggt und
  ist sichtbar.
- Reservierte Kapazität (`reserved_micro_usd`) zählt zum Limit, sodass ein
  Budget nicht durch Vorab-Buchung umgangen werden kann.
- `cost_type` ist bewusst **keine** Budget-Dimension — Estimated-Fallback-Zeilen
  reiten auf der Dimension, zu der sie gehören, statt einen parallelen Pool zu
  bilden.
