---
title: "Modul XIII — Compliance & Regulatorik"
description: >-
  Abbildung dessen, was der Control Plane bereits beobachtet und auditiert, auf
  regulatorische Frameworks und Export auditorenkonsumierbarer Nachweise, abgeleitet
  aus dem append-only Ledger. Auf Audit ausgelegt, niemals zertifiziert: Status +
  Nachweise, niemals „compliant".
---

Modul XIII öffnet Unternehmenstüren, indem es das, was der Control Plane bereits beobachtet
und auditiert, auf regulatorische Frameworks **abbildet**, und indem es **auditorenkonsumierbare
Nachweise** erzeugt, die aus dem append-only, hash-chained Ledger abgeleitet sind. Es ist ein
Modul der Intelligence-Schicht: es erfasst **nichts Neues** — es aggregiert und transformiert
das, was der Kern und die anderen Module bereits aufzeichnen, und es **beansprucht niemals eine
Zertifizierung**.

## Was es ist

Modul XIII hat fünf Flächen, alle lesen-und-ableiten über bestehende Daten:

- **Ein versionierter Control-Katalog**, im Repo als deterministische Quelle der Wahrheit
  gehalten — EU AI Act, NIST AI RMF, ISO/IEC 42001, SOC 2 / ISO 27001 und GDPR (plus
  GenAI-/agentische Cross-Walks), modelliert als versionierte **Controls**, jedes mit seiner
  Anforderung und seinem Erfüllungskriterium. Es ist eine **technische Abbildung, keine
  Rechtsberatung**, und ein Control, dessen Verpflichtung die Plattform nicht belegen kann,
  trägt einen expliziten Hinweis, damit eine teilweise Abdeckung niemals als vollständig gelesen wird.
- **Eine deklarative Control → Nachweis-Abbildung.** Jedes Control bildet auf
  Control-Plane-**Capabilities** ab. Eine Capability ist entweder **operational** — nur vorhanden,
  wenn echte Mandantendaten existieren (ein Ledger, der verifiziert, beobachtete Access-Edges,
  Security-Findings, Eval-Ergebnisse, Deployments, eine Risikoklassifizierung, eine
  Residency-Attestierung) — oder **architektural** — eine Plattform-Design-Garantie, die auf die
  Design-Dokumente verweist und als solche gekennzeichnet ist, niemals als Telemetrie.
- **Exportierbarer Audit-Nachweis** — ein versiegeltes, append-only Nachweispaket, abgeleitet
  aus dem Ledger.
- **Agenten-Risikoklassifizierung** in eine EU-AI-Act-Stufe, cross-gemappt auf NIST-AI-RMF-
  Funktionen, aus beobachteten Attributen — gesteuert und auditiert.
- **Datenresidenz** — eine Attestierung pro Region, wo das Deployment und seine Stores
  tatsächlich laufen, plus ein Scan, der bestehende Egress-Signale in ein Residency-Finding
  verwandelt.

## Control-Status & Entitäten

Der Status wird ehrlich berechnet, niemals behauptet. Ein Control ist **satisfied** nur, wenn
jede gemappte Capability vorhanden ist **und mindestens eine operational ist**; **by_design**,
wenn alle vorhandenen Capabilities architektural sind (design-ready, niemals satisfied); **partial**,
wenn einige vorhanden sind; **gap**, wenn keine vorhanden ist; **unmapped**, wenn überhaupt keine
Capability es stützt. `satisfied` ruht niemals allein auf Design-Nachweisen.

Das Modul deklariert vier append-only / auditierte Entitäten im gemeinsamen Datenmodell: ein
versiegeltes **Nachweispaket** (das die Sequenz und den Hash des Chain-Heads sowie das Ergebnis
der Live-Hash-Chain-Verifikation aufzeichnet), ein **Ergebnis** pro Control innerhalb dieses
Pakets, eine **Risikoklassifizierung** pro Subjekt und eine **Residency-Attestierung** pro Region.
Das Nachweispaket **referenziert** den Ledger über Sequenz und Hash und beweist seinen Body als
manipulationserkennbar mit einem deterministischen Manifest-Hash — es kopiert niemals den Ledger und
hält niemals Payloads oder PII.

## Was es konsumiert & produziert

Die Risikoklassifizierung liest Attribute, die andere Module bereits aufgezeichnet haben — ausgehende
read/write [Access-Edges](/de/reference/modules/iii-access-map/), high/critical Security-Findings und
ein optionales Autonomie-Signal — und produziert eine **vorgeschlagene** Stufe, die gesteuert ist:
ein Mensch muss sie prüfen und genehmigen, und die Vorschlagsmaschine **kann niemals die
unacceptable-Stufe zuweisen** (das ist eine rechtliche Bestimmung). Der Residency-Scan korreliert
bestehende Egress-Lineage gegen `self_hosted`-Attestierungen und erhebt pro Verstoß ein zentrales
Finding und veröffentlicht ein internes Bus-Signal, damit das Benachrichtigungsmodul (XV) es an
SIEM/Slack/PagerDuty zustellt. Das Lesen oder Exportieren eines Nachweispakets, das Versiegeln eines
solchen, das Klassifizieren oder Prüfen von Risiko und das Attestieren von Residenz sind
privilegierte, mandantengebundene Aktionen, die sich **in der eigenen Transaktion des Aufrufers
selbst zum Ledger auditieren**.

:::caution[Ehrliche Grenzen]
- **Auf Audit ausgelegt, niemals zertifiziert.** Jede Reporting-Antwort trägt den Disclaimer, dass
  sie **keine Zertifizierung und keine Rechtsberatung** ist. Der Output spricht von Control-Status
  und Nachweis; er sagt niemals „compliant" oder „certified". Opt-in-Garantien (wie
  At-Rest-Verschlüsselung) sind standardmäßig **absent**, bis attestiert.
- **Keine Aktuierung.** Dieses Modul bildet Controls ab und exportiert Nachweise — es behebt,
  erzwingt oder ändert nichts. Sein einziger Nebeneffekt ist das Residency-Finding und das
  Bus-Signal, auf die andere Module hin agieren.
- **Nachweis ist nur so gut wie seine Quellen.** Ein Control ohne stützende Mandantendaten ist eine
  ehrliche **gap**, kein vorgetäuschtes Pass; eine fehlende operationale Capability senkt den Status
  eines Frameworks, statt ihn aufzublähen. Der Nachweis zum Least-Privilege-Drift konsumiert den
  **abgeglichenen** Drift von Modul III (nicht den rohen Store-Pfad), sodass er die gestaffelten
  Abdeckungsgrenzen von Modul III erbt — eine fehlende Edge ist kein Beweis dafür, dass ein Zugriff
  nicht stattgefunden hat.
- **Architekturaler Nachweis ist Design, kein Beweis.** Capabilities, die auf die Design-Dokumente
  verweisen, attestieren, wie die Plattform gebaut ist, nicht dass ein Control in Ihrem Mandanten
  lief; sie produzieren `by_design`, was bewusst von `satisfied` unterschieden ist.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XIII sitzt und die ehrliche Trennung von
  Govern/Observe vs. Actuate.
- [Modul III — Access- & Resource-Map](/de/reference/modules/iii-access-map/) — das Drift-Signal, das
  der Risikoklassifizierer und die Drift-Capability konsumieren.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — warum Status, nicht Zertifizierung.
- [Steuern und genehmigen](/de/how-to/govern-and-approve/) — Prüfen einer vorgeschlagenen Risikostufe.
- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — der kontinuierliche Ledger-Feed,
  den der Auditor erneut dagegen verifiziert.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Intelligence-Schicht und der
  Event-Bus.
