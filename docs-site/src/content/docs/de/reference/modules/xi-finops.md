---
title: "Modul XI — Kosten & KI-FinOps"
description: >-
  KI-Ausgaben aus dem Kosten-Stream verbuchen, sie nach jeder
  Attributionsdimension aufschlüsseln, die Periode prognostizieren und Budgets
  durchsetzen, die die Ausgabe an der Obergrenze verweigern — money-free auf der
  Leitung, opt-in und fail-open. Was es tut und seine Grenzen.
---

Modul XI ist die **Kosten- / FinOps**-Schicht für KI: Es verbucht, was die Modell- und
Provider-Konnektoren melden, lässt Sie Ausgaben nach jeder Attributionsdimension
aufschlüsseln, prognostiziert die aktuelle Periode und macht aus einem Budget echte
Durchsetzung, die die **Ausgabe an der Obergrenze verweigert**, statt sie nur zu
kennzeichnen. Diese Seite ist die Referenz dafür, was FinOps heute tut und wo seine Garantien
enden.

## Was es ist

FinOps implementiert die Provider-Integration **nicht** neu — es konsumiert den
Modell-/Provider-Kosten-Stream und **verbucht, was die Konnektoren maßgeblich abgeleitet oder
gelesen haben**. Geld ist immer ein **ganzzahliger Mikro-USD**-Wert (Millionstel eines
Dollars), niemals ein Float, sodass Summen niemals abdriften. Es ist ein Modul der
Intelligence-Schicht: Es besitzt Ingestion, Budgets und Analytics und stellt sie über seinen
eigenen RBAC-gegateten API-Namespace und seine UI-Ansichten bereit, ohne den Kern oder seine
Nachbarn zu berühren.

Das Modul ist **minimal-data by construction**: Es speichert Token-Zählungen, abgeleitete
Kosten und Attributions-*Referenzen* — niemals einen Prompt, eine Completion oder ein Secret.
Kosten sind Governance-Daten, daher sind Lesezugriffe an der API rollen-gegated, und **es
wird niemals ein USD-Betrag an einen Endnutzer offengelegt** (das ist eine Eigenschaft der
Leitung, keine UI-Einstellung).

## Seine Entitäten & sein Vertrag

Jedes `cost.sampled`-Event (ein `CostSample` — siehe den [Event-Bus](/de/reference/events/))
wird auf zwei Arten erfasst:

- das kanonische, normalisierte **CostRecord-Ledger** (eine Kern-Entität, mit ID als
  Schlüssel), **dedupliziert über einen natürlichen Schlüssel** — die *Identität* des Buckets
  (Provider / Modell / Session / Zeitpunkt plus jede Attributionsdimension und Provenance),
  niemals sein *Wert* — sodass ein erneut gezogener offener Bucket oder ein verspätet
  abgerechneter Report **an Ort und Stelle upsertet**, statt auf dem At-least-once-Stream
  doppelt zu zählen;
- eine denormalisierte **FinOps-Read-Model**-Zeile, mit den natürlichen Attributionsnamen als
  Schlüssel (Provider, Modell, Agent, Session, Team, Projekt), sodass Ausgaben effizient nach
  **jeder** dieser Dimensionen aggregieren — einschließlich des Provider-`service_tier`.

Ein **Budget** ist eine Kern-`Policy` der Art `budget`: eine Dimension (global / Modell /
Provider / Agent / Session / Team / Projekt), ein Limit, eine Periode und Alert-Schwellen.
Seine `action` ist eine von dreien — `alert` (showback-only, der sichere Default, der niemals
durchsetzt), `throttle` oder `block`. Analytics liefern eine Ausgaben-Aufschlüsselung nach
jeder Dimension, Summen, eine tägliche Trend-Serie, eine Run-Rate- und Trend-Prognose der
aktuellen Periode (mit einem expliziten Konfidenzband), eine Prompt-Cache-Effizienz-Ansicht
und Optimierungsempfehlungen — jede in erfassten Daten verankert und **ehrlich über ihre
Annahmen**.

## Was es konsumiert & erzeugt

FinOps **konsumiert** `cost.sampled` vom [Event-Bus](/de/reference/events/) und **erzeugt**
zwei Effekte. Beim Ingest, wenn der Verbrauch eine Budget-Schwelle überschreitet, die er in
dieser Periode noch nicht überschritten hat, erfasst es den Alert und **emittiert einen
`FindingReport`** (`finding.reported`) — nur das *Signal*; die Zustellung an Slack / SIEM /
PagerDuty ist Aufgabe des Output-Konnektor-Moduls, nicht von FinOps.

Der zweite Effekt ist **Durchsetzung**. Ein Budget, dessen `action` `throttle` oder `block`
ist, verweigert die Ausgabe an der Obergrenze über eine **`BudgetGate`-Naht**, deklariert in
den eigenen Begriffen jedes handelnden Moduls (das *fire* der Orchestrierung, das *open* von
Voice, das *resolve* des Modell-Routers); kein Modul importiert FinOps. Das Gate läuft
**orthogonal zum Freigabe-Gate** — eine Aktion kann human-approved und dennoch budget-denied
sein — und antwortet auf die cap-wirksame Ausgabe mit einer **money-free Begründung** (kein
USD, kein Budget-Name auf der read-only Route). Ein hartes `block` verweigert mit **HTTP
402**, ein weiches `throttle` mit **HTTP 429**, und die Verweigerung wird in das append-only
Ledger geschrieben und auditiert. Siehe [Steuern und freigeben](/de/how-to/govern-and-approve/).

:::caution[Ehrliche Grenzen]
- **Durchsetzung ist opt-in, nicht standardmäßig deny-closed.** Ohne durchsetzendes Budget,
  das eine Anfrage scoped, wird niemals etwas verweigert — diese Abwesenheit ist der
  Normalzustand, kein Sicherheitsloch. Nur ein Budget, das *definitiv* an seinem Limit ist,
  verweigert. Dies ist bewusst und das Gegenteil der deny-closed Haltung des Freigabe-Gates.
- **Das Gate fällt offen aus.** Ein FinOps-Lesefehler legt niemals eine laufende Aktion lahm —
  ein freigegebenes fire/open schreitet fort und der Router löst auf. Der dauerhafte Backstop
  ist das beim Ingest emittierte Budget-Cap-Finding, nicht das Pre-Flight-Gate.
- **Der Router setzt nur die Scopes durch, die er vor der Ausführung kennt** (global / Provider
  / Modell); feinere Scopes (Agent, Session, Team, Projekt) werden an den fire/open-Nähten und
  am Modell-Gateway durchgesetzt, nicht bei der Route-Auflösung.
- **FinOps verbucht; es rechnet nicht ab.** Es erfasst, was die Konnektoren melden — die
  Provenance `billed` vs. `estimated` wird getragen, nicht in eine Rechnung abgeglichen — und
  ein Sample mit null/leeren Feldern bedeutet *„nicht gemeldet“*, niemals *„null“*.
- **Keine Aktuierung über Verweigerung hinaus.** FinOps führt weder einen Modellaufruf aus
  noch bewegt es Geld; es beobachtet den Kosten-Stream und gated Ausgaben, die zu gaten es
  konfiguriert ist.
:::

## Verwandt

- [Referenz Event-Bus](/de/reference/events/) — die Payloads `cost.sampled` / `CostSample` und `finding.reported`.
- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XI sitzt und sein ehrlicher Aktuierungsstatus.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, die Schichten und der Kosten-Stream.
- [Steuern und freigeben](/de/how-to/govern-and-approve/) — Handeln bei einer budget-denied Aktion.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die Deny-closed-Seam-Policy über Module hinweg.
