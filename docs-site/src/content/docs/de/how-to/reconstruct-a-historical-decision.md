---
title: "Eine historische Autorisierungsentscheidung rekonstruieren"
description: >-
  Was eine Rekonstruktion beweist, was sie nicht beweist, und wie ein
  Prüfer Montags Allow nach Dienstags Widerruf erneut auswertet.
sidebar:
  order: 22
---

Ein Prüfer kann fragen, was eine Richtlinie an einem vergangenen Datum
**entschieden** hat. Die Control Plane antwortet aus dem
**Evidenz-Ledger**: der aufgezeichneten Richtlinienversion und den
Eingaben dieser Entscheidung. Sie wertet **nicht** die heute aktive
Richtlinie aus.

## Befehl

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --action SELECT
```

Oder eine gespeicherte Zeile:

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP: `POST /v1/m/governance/decisions/replay` und
`GET /v1/m/governance/decisions/{id}/reconstruct`. In diesem Schnitt gibt
es keine Konsolen-Schaltfläche. Nutzen Sie CLI oder HTTP.

## Was eine Rekonstruktion beweist

Status `reconstructed` bedeutet: eine aufgezeichnete
`live_authorization` existiert; das genannte Artefakt ist **behalten**;
dieser Build kann den Evaluator ausführen (Cedar); die Neuauswertung
nutzt das **originale** Artefakt; die Live-PDP wurde **nicht** gelesen.

`policy_version_id` ist die Artefakt-Id. Das ist nicht das
Route-Witness-`PolicyVersion` und nicht die Autorenrevision.

## Was sie nicht beweist

- Ehrlichkeit des damaligen Enforcement-Punkts. Ein gespeichertes Allow
  ist keine Erlaubnis, den Effekt zu wiederholen.
- Authentizität des Produzenten oder Integrität der Kette.
- Externe Aktivität ohne Olivares-Entscheidung. Ohne Datensatz lautet
  die Antwort `COULD NOT RECONSTRUCT`.
- Engines, die dieser Build nicht ausführt (OPA/Rego nur zum Verfassen).

## COULD NOT RECONSTRUCT

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

`missing` benennt die fehlende Tatsache (`authorization_decision`,
`policy_version_id`, `policy_artifact`, `policy_artifact.content`,
`evaluator`, `question`, `at`). Alte Zeilen bleiben **unknown**; der
Speicher füllt sie nicht aus der heutigen Richtlinie.

## Montag, Dienstag, Mittwoch

Montag Allow aufzeichnen, Dienstag widerrufen, Mittwoch
`--at <Montag>` ergibt weiter **allow** aus dem Montags-Artefakt.
