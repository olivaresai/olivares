---
title: "Modul XV — Output-Integrationen & Benachrichtigungen"
description: >-
  Der Benachrichtigungs-Router der Control Plane: Er entscheidet, WELCHES Signal
  WEN erreicht, über WELCHEN Kanal und WANN, und versendet das bereinigte Ergebnis
  über die Output-Konnektoren — Slack/Teams, PagerDuty/Opsgenie, signiertes Webhook,
  SIEM. Die erprobte End-to-End-Actuation-Naht, mit einem deny-closed-Standard und
  einem Beweis-Ledger.
---

Modul XV ist der **Benachrichtigungs-Router** der Control Plane: Sobald ein Modul
eine Warnung in ein Finding auf dem Event-Bus verwandelt, entscheidet dieses Modul,
welche Mandanten-Route es trifft, baut eine bereinigte Benachrichtigung, unterdrückt
Duplikate und Stürme und **versendet sie live** an die Kanäle, die das Unternehmen
ohnehin betreibt. Es besitzt das *Entscheiden was/wem/wann*; die Output-Konnektoren
besitzen das *Wie* der Zustellung — es nutzt diesen Transport, es implementiert ihn
nie neu.

## Was es ist

Jedes Modul im Produkt meldet eine Warnung als datenminimales Finding auf dem Bus
([`finding.reported`](/de/reference/events/)) mit einer namespace-versehenen `Kind` —
Zuverlässigkeit (`health_subject_down`), Kosten (`finops_budget`), Sicherheit
(`security_guardrail`), Eval-Regression (`eval_regression`), Residenz
(`compliance_residency_violation`), Orchestrierungs-Kadenz, Voice und mehr. Modul XV
abonniert **ausschließlich** diesen einen produktweiten Warnkanal und routet nach
`Kind`, Schweregrad, Quellmodul und Subjekt. Es abonniert bewusst **keine**
Roh-Telemetrie wie `cost.sampled` oder `edge.observed`: Eine Kosten-*Warnung* trifft
als `finops_budget`-Finding ein, nicht als Kosten-Sample. Dies ist die Naht, die die
Findings des gesamten Produkts in handlungsfähige Benachrichtigungen verwandelt.

## Vertrag & Entitäten

Das Modul deklariert zwei mandantengebundene Entitäten im gemeinsamen Datenmodell:

| Entität | Modus | Was sie enthält |
|---|---|---|
| **route** | veränderlich, auditiert | Eine Routing-Regel: ein Prädikat über Event-Typen, Finding-Kind-Globs (z. B. `health_*`), minimaler Schweregrad, Quellmodule und Subjektarten → ein benanntes **Ziel**, mit Dedup- und Throttle-Fenstern pro Route und einer Priorität. Enthält **kein Ziel-Credential** — nur einen geheimnisfreien Zielnamen. |
| **delivery** | append-only | Das Beweis-Ledger jedes Zustellungs-*Versuchs*: Route, Ziel, Finding-Kind, Schweregrad, Subjektreferenz, kurzer Titel, ein Korrelations-Hash und eine Ergebnisklasse (`delivered`, `failed`, `no_dispatcher`, `unknown_destination`). |

Bei jedem Finding wertet das Modul die aktivierten Routen des Mandanten in
Prioritätsreihenfolge aus; jede leer gelassene Prädikat-Dimension bedeutet *beliebig*,
und das Glob-Matching unterstützt exakte oder `prefix*`-Formen. Das Matching geschieht
innerhalb einer Read-View, **die Netzwerkzustellung läuft strikt außerhalb jeder
Store-Transaktion**, und das Ergebnis wird anschließend in das append-only-Ledger
geschrieben. Das Anlegen, Ändern oder Löschen einer Route sowie das Senden einer
Test-Benachrichtigung sind **privilegierte, selbst-auditierte** Aktionen, die dem
realen Principal zugerechnet werden. Die Route- und Delivery-Routen werden in der separaten
**Beta**-[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht, nicht im stabilen
Kernvertrag; ihre feldgenauen Formen leben in den typisierten Interfaces des Produkts.

## Was es konsumiert & produziert

- **Konsumiert** [`finding.reported`](/de/reference/events/) — den einzigen produktweiten
  Warnkanal. Es ist ein Router, keine Sonde und kein Messgerät: Es pollt nie die
  Infrastruktur und misst nie.
- **Produziert** ausgehende Benachrichtigungen über eine Dispatch-Naht, gestützt auf
  die Output-Konnektoren (Slack/Teams, PagerDuty/Opsgenie, signiertes Webhook und ein
  SIEM-Ziel, das Splunk/Elastic über CEF/LEEF/syslog/OTLP abdeckt). Eine
  Benachrichtigung trägt nur die ohnehin sicheren Anzeigefelder des Findings — Titel,
  Kind, Schweregrad, Subjektreferenz und einen Korrelations-Hash — **niemals** eine
  Payload, einen Prompt, ein Secret oder PII. **Datenminimierung ist eine Eigenschaft
  der Leitung**, kein nachträglicher Filter. Das Ziel-Secret lebt ausschließlich in der
  Konnektor-Konfiguration, die der Betreiber bereitstellt, hier referenziert über einen
  geheimnisfreien Namen.

:::caution[Ehrliche Grenzen]
- **Das Standard-Binary liefert einen deny-closed-Dispatcher aus.** Bis ein Betreiber
  Ziele bereitstellt, ist der Dispatcher verdrahtet, aber leer: Eine ungematchte
  Zustellung wird als `no_dispatcher` festgehalten, und ein fehlkonfiguriertes oder
  ein Ziel unbekannter Art löst sich im Ledger zu `unknown_destination` auf. Es
  **täuscht nie einen Erfolg vor** — eine Nicht-Zustellung ist stets sichtbar.
- **Das ausgehende Webhook ist ein Ziel-Konnektor, kein OpenAPI-Webhook.** Es ist ein
  Ausgabekanal, an den die Control Plane pusht, kein Callback, den du gegen die API des
  Produkts registrierst.
- **Dedup und Throttle unterdrücken das *Senden*, nicht ein Ergebnis.** Eine deduplizierte
  oder gedrosselte Benachrichtigung wird absichtlich **nicht** in das delivery-Ledger
  geschrieben (damit es nie aufgebläht wird). Jeder tatsächliche Zustellungs-*Versuch*
  wird im Gegensatz dazu festgehalten — `delivered`, `failed`, `no_dispatcher` und
  `unknown_destination` gleichermaßen — sodass eine Nicht-Zustellung stets sichtbar ist,
  nie still verworfen.
- **Der Rohfehler des Konnektors wird nie persistiert oder geloggt** — nur eine
  nicht-sensible Ergebnisklasse — denn ein Transportfehler kann das Ziel-Secret in seiner
  URL tragen.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XV sitzt und die Govern/Actuate-Aufteilung.
- [Push to your SIEM](/de/how-to/cookbook/push-to-siem/) — der S2S-Push-Treiber
  (`modules/siemforward`), der Findings und das versiegelte Audit-Ledger in den nativen
  Dialekt eines Towers (OCSF/CEF/LEEF/syslog/OTLP) umformt und auf der dauerhaften
  Zustellung der Eventing-Plattform reitet — die Push-Ergänzung zu den obigen Zielen.
- [Event-Bus-Referenz](/de/reference/events/) — das `finding.reported`-Event und seine `FindingReport`-Payload.
- [Zugriffs- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — eine verwandte Core/Intelligence-Referenz.
- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — Verdrahten eines SIEM-Ziels.
- [Govern and approve](/de/how-to/govern-and-approve/) — auf die Findings reagieren, die dieses Modul routet.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die deny-closed-by-default-Haltung im gesamten Produkt.
