---
title: "FinOps-Zulassung: Ablehnung und Abgleich lesen"
description: >-
  So sieht eine Budget-Ablehnung auf dem Draht aus, so setzen Sie die Haltung
  bei unerreichbarem Speicher, so lesen Sie Drift zwischen Reserve und Commit.
sidebar:
  order: 21
---

Ein kostenpflichtiger Effekt muss den geschätzten Aufwand **reservieren**,
bevor er läuft. Die Control Plane **bestätigt** danach die gemessenen Kosten
oder **gibt** die Reserve frei.

## So sieht eine Ablehnung aus

Eine harte Grenze (`action=block`) antwortet mit **HTTP 402**. Eine weiche
Grenze (`action=throttle`) antwortet mit **HTTP 429**. Der Body nennt die Aktion, das Budget
und den fehlenden Spielraum. Dieser Endpoint verlangt die
Budget-Schreibberechtigung und antwortet damit einem Administrator. Der
Inference-Proxy gibt diesen Grund nie weiter — ein Cap verweigert mit
`budget limit reached`, ein Sitzplatz-Limit mit `spend limit reached`, ohne
Budgetnamen und ohne Betrag.

```bash
olivares finops admission reserve --data @reserve.json -o json
```

## Unerreichbarer Speicher (Standard: ablehnen)

Wenn der Budget-Speicher nicht gelesen werden kann, **lehnt** die Zulassung
ab. Der feste Grund ist `budget store unreachable (deny-closed)`. HTTP **503**.
Es gibt auch eine Audit-Zeile `finops.admission.denied`.

Standard ist **deny**. Für Fail-Open setzen Sie `"unreachable": "allow"`.

## Abgleich lesen

```bash
olivares finops admission reconciliation -o json
```

`drift` ist wahr bei abgelaufenen, nicht abgeschlossenen Reserven.
`reconciliation` LIEST nur und ändert nichts; `reconcile` ist der Job: er räumt
diese Reserven ab, braucht das Budget-Schreibrecht und sendet den Fund
`finops_reservation_drift`. Das ist Haltung, keine stille Korrektur der
Ausgaben.
