---
title: "Rezept: Least-Privilege-Drift triagieren"
description: >-
  Ein Permitted-vs-Observed-Ergebnis auf null abarbeiten: unerwartete Zugriffe,
  ungenutzte Grants und reconciliation-pending Kanten klassifizieren, jeden Fall
  entscheiden (gewähren, entziehen oder Identität korrigieren) und neu prüfen —
  ohne einem einzigen Hinweis blind zu vertrauen.
sidebar:
  order: 4
---

**Ziel:** das Drift-Ergebnis — die Lücke zwischen dem, was Agents tun *dürfen*,
und dem, was *beobachtet* wird — in Entscheidungen überführen, im festen Takt,
bis der Diff still ist.

## 1. Den Drift abrufen

```bash
curl -ks "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

(Oder in HCL, zur Prüfung in einem PR: die Terraform-Data-Source
`olivares_access_edges` mit `include_drift = true` —
[as Code verwalten](/de/how-to/manage-as-code/).)

Das Ergebnis hat drei Klassen, und das sind unterschiedliche Probleme:

| Klasse | Bedeutung | Die zu stellende Frage |
|---|---|---|
| **Unerwarteter Zugriff** | beobachtet, aber kein Grant deckt ihn ab | fehlt ein Grant, oder ist das eine echte Verletzung? |
| **Ungenutzter Grant** | gewährt, nie beobachtet ausgeübt | warum existiert diese Berechtigung? |
| **Reconciliation ausstehend** | beobachtet, aber die Verknüpfung Agent↔Identität ist ungelöst | ein Identitätsproblem, (noch) kein Sicherheitsproblem |

## 2. Jede Klasse triagieren

**Unerwarteter Zugriff** — lies die Honesty-Achsen der Kante, bevor du
handelst:

- `attribution_tier: firm` + `coverage_tier: clean` ist das hochwertigste
  Finding, das du bekommen wirst: eine konkrete Identität hat eine konkrete
  Ressource angefasst, und das eigene Audit des Stores hat es klassifiziert.
  Entscheide: ist er legitim, deklariere den Grant (Policy oder Binding), damit
  die Map die Absicht widerspiegelt; wenn nicht, entziehe den zugrunde
  liegenden Zugriff und behandle es als Vorfall.
- `approximate` Attribution bedeutet, dass der *Zugriff* stattfand, das *Wer*
  aber eine geteilte Credential ist. Verschwende keine Untersuchung auf „welcher
  Agent war es" — die dauerhafte Lösung ist
  [Per-Agent-Identität](/de/how-to/connectors/sso-scim-identity/), und bis dahin
  sagt die Kante ehrlich, was sie nicht beweisen kann.
- Eine Kante, die nur auf einem `mcp_annotation`-Hinweis ruht, ist **kein
  Beweis** — der Hinweis ist laut Spezifikation nicht vertrauenswürdig.
  Bestätige ihn über eine beobachtete Quelle, bevor du irgendetwas
  entscheidest.

**Ungenutzte Grants** sind kostenlos gefundenes Over-Provisioning: jeder ist ein
Kandidat für eine Revokation, mit dem Vorbehalt, dass die Abwesenheit von
Beobachtung nur dort aussagekräftig ist, wo Coverage existiert — prüfe den
Coverage-Tier der Ressource, bevor du feierst
([gestufte Coverage](/de/how-to/connect-a-source/#gestufte-abdeckung--sei-realistisch)).

**Reconciliation ausstehend** wird ins Identitäts-Backlog geleitet: verdrahte
oder korrigiere die Roster-Quelle, die diese Credential binden sollte, und die
Kante löst sich beim nächsten Durchlauf auf.

## 3. Entscheiden, festhalten, neu prüfen

Triff die Entscheidung dort, wo sie governt wird: deklariere Grants als Code
([Terraform](/de/how-to/manage-as-code/)) oder über die governte API, gate die
riskante Richtung hinter einer [Genehmigung](/de/how-to/cookbook/hitl-approvals/),
und lass das Ledger festhalten, wer was entschieden hat. Dann ziehe den Drift
erneut: reconcilierte Kanten fallen aus dem Diff heraus — nur echte Lücken
bleiben. Diese Konvergenz ist der ganze Sinn; das Demo-Estate zeigt sie im
Kleinen ([Quickstart](/de/start/quickstart/)).

In der Konsole ist das Panel *Permitted vs observed* der **Access Map** dieses
Rezept live gerendert.

## Takt

Drift-Triage funktioniert als kurze wöchentliche Schleife plus ein Alarmpfad
für die signalstarke Klasse (firm + clean unerwartete Writes). Leite diese
Findings über ein
[Benachrichtigungsziel](/de/how-to/forward-audit-to-splunk/) an deinen Bereitschaftsdienst,
statt auf den wöchentlichen Durchlauf zu warten.
