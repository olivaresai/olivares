---
title: "Modul IX — Sicherheit, Guardrails & Audit"
description: >-
  Die defensive Control Plane: deterministische Guardrails, die Findings mit
  minimalen Daten erzeugen, priorisierte Anomalien und durch Hash-Chain
  verifizierte Incident-Timelines — detektivisch per Default, mit Inline-Enforcement
  als opt-in, kontrolliertem, standardmäßig deaktiviertem Seam.
---

Modul IX ist die **defensive, querschnittliche Ebene** von Olivares AI. Es
verwandelt die Events des Estate und das manipulationserkennbare Evidenz-Ledger in
**Findings**, **priorisierte Anomalien** und **rekonstruierbare
Incident-Timelines**, sodass ein Verteidiger *sehen* und *beweisen* kann, was
jeder Agent getan hat. Es ist **detektivisch per Default**: es beobachtet und
übergibt Evidenz und sitzt niemals im Datenpfad des Agenten.

## Was es ist

Das Modul umspannt drei abgegrenzte Verantwortlichkeiten:

- **Guardrails** — eine Kette deterministischer, erklärbarer Detektoren
  inspiziert Agententext auf den Oberflächen `input`, `output` und `tool_args`
  auf Secrets/PII, Prompt-Injection, Jailbreak, unzulässige Inhalte,
  Output-Schema-Verletzungen und die OWASP Agentic Top 10. Detektionen tragen
  Framework-Referenzen (OWASP LLM Top 10 2025, OWASP Agentic Top 10 2026, MITRE
  ATLAS) wortgetreu aus Primärquellen, niemals erfunden. Ein optionaler,
  einsteckbarer Klassifikator (ein gehostetes Guardrail-LLM) läuft *hinter* den
  deterministischen Detektoren: er kann Detektionen nur **hinzufügen**, niemals
  eine unterdrücken, und sein Ausfall wird protokolliert und ignoriert.
- **Anomalie-Erkennung** — es korreliert den Permitted-vs-Observed-Drift, den
  [Modul III](/de/reference/modules/iii-access-map/) berechnet, mit Findings hoher
  Schwere und verknüpft Anti-Evasion-Signale auf Kernel-Seite und auf
  kooperativer Seite: ein Agent, der seine eigene Telemetrie verstummen lässt,
  wird als Signal behandelt, nicht als blinder Fleck.
- **Forensik / IR** — es gruppiert Evidenz zu einem **Case** und rekonstruiert
  dessen **Timeline** aus dem append-only, hash-chained Ledger, *verifiziert* die
  Chain und ihre signierten Checkpoints, statt ihnen zu vertrauen. Ein
  manipuliertes Ledger wird gemeldet, nicht verborgen.
- **Aufzeichnung privilegierter Sitzungen** — eine unveränderliche,
  wiederabspielbare Aufzeichnung dessen, was eine privilegierte Operator-Sitzung
  auf den sensibelsten Moduloberflächen des Produkts tatsächlich getan hat: ein
  append-only Frame je aufgezeichneter Aktion (wer, wann, Routenform,
  Berechtigung, Ziele, Ergebnis, Request-Digest), je Sitzung hash-chained und im
  Evidenz-Ledger verankert (open → periodische Anker → seal), sodass das
  Umschreiben eines Frames sowohl die Sitzungs-Chain als auch ihre signierten
  Ledger-Anker bricht. Das Gate läuft *vor* der Aktion und ist deny-closed: auf
  einer aufgezeichneten Oberfläche bedeutet kein anhängbarer Evidenz-Trail keine
  privilegierte Aktion.

## Vertrag & Entitäten

Modul IX ist der **erste Produzent der zentralen `Finding`-Entität**; es besitzt
weder ein Ledger noch eine Erfassung, es konsumiert sie. Aufbauend auf `Finding`
besitzt es drei Entitäten: einen veränderlichen **Case** (Lifecycle `open` →
`investigating` → `contained` → `closed`, mit einem zur Open-Zeit erstellten
Integritäts-Snapshot), ein **append-only Case-Link**, das die Chain of Custody
bildet (die Evidenzmenge eines Incidents ist selbst Evidenz und kann nicht
umgeschrieben werden), und eine klassenspezifische **Enforcement-Policy** — wobei
das Fehlen einer Zeile *detektivisch* bedeutet.

Seine Routen sind unter der Modul-API gemountet und mit authn + tenant + authz
umhüllt, mit namespaced read/write/admin-Berechtigungen. Das Lesen von Findings
ist schlicht (ein Finding ist der Alert selbst); die **rekon-sensiblen**
Lesezugriffe — die verifizierte Timeline, der SIEM-Export, die Anomalie-Ansicht
und die eigenständige Integritätsverifikation — sind **privilegiert und
selbst-auditiert**: der Akt des Hinsehens wird in derselben Chain festgehalten,
die er inspiziert. Jede Mutation (Triage, Case-Lifecycle, Enforcement-Haltung)
ist ebenfalls selbst-auditiert. Exporte nach WORM/SIEM (CEF, syslog, OTLP)
tragen Integritätsfelder je Zeile, sodass die Chain **offline** von einem
externen unveränderlichen Store erneut verifiziert werden kann.

## Was es auf dem Bus konsumiert & produziert

Modul IX reagiert auf [`finding.reported`](/de/reference/events/) (es persistiert
Findings hoher Schwere anderer Module in die Security-Sicht des Mandanten) und
auf [`guardrail.observed`](/de/reference/events/), den Detektiv-Eingangskanal
bereits geschwärzten beobachteten Texts. Es produziert je Detektion einen
`FindingReport` auf namespaced `security_*`-Routing-Keys, die das nachgelagerte
Delivery an SIEM/Slack/PagerDuty routet und die Compliance auf Controls abbildet.
Der Live-Feed `guardrail.observed` stammt aus der Runtime-Ingestion-Ebene, die in
der [Event-Bus-Referenz](/de/reference/events/) beschrieben ist: er ist
**deny-closed und opt-in** (aus, sofern ein Betreiber ihn nicht aktiviert), und
der inspizierte Text ist die *bereits bereinigte Ressourcenreferenz* des
Connectors einer `tool_args`-Kante — niemals das rohe Argument.

:::caution[Ehrliche Grenzen]
- **Detektivisch per Default; Enforcement ist ein opt-in Seam.** Das Modul
  beobachtet und liefert Evidenz. Inline-Enforcement (das Blockieren eines
  Outputs oder einer Aktion) ist **standardmäßig aus**, Admin-Tier und — wo ein
  HITL-Freigabe-Gate verdrahtet ist — kontrolliert. Es zu aktivieren ist die
  einzige Fähigkeit, die Produktion berührt; das Deaktivieren (der sichere
  Default) ist stets erlaubt. Ein Guardrail, der ausfällt, darf niemals die
  Produktion brechen.
- **Der Live-Feed hat eine reale Coverage-Grenze.** Auf der Live-Oberfläche
  `guardrail.observed` ist nur **PII oder ein in eine Ressourcenreferenz
  eingebettetes Secret** (sowie anomale/sensible Ressourcenmuster) detektierbar.
  Prompt-Injection und Jailbreak benötigen den *Inhalt* des Arguments, der an der
  kooperativen Quelle verworfen wird und niemals den Bus erreicht; die
  Oberflächen `input` / `output` / `tool_result` erfordern eine
  In-Process-Inhaltsquelle, die dieser Build nicht bereitstellt. Das wird
  deklariert, nicht vorgetäuscht.
- **Integritätsverifikation kann nicht verfügbar sein, niemals vorgetäuscht.**
  Die Hash-Chain wird stets auf interne Konsistenz verifiziert, aber die
  Attestierung *signierter Checkpoints* erfordert den verdrahteten öffentlichen
  Schlüssel des Ledgers; ohne ihn wird die Checkpoint-Verifikation als **nicht
  verfügbar** gemeldet, statt vorgegeben. Ein gefälschter Checkpoint wird
  erkannt, nicht vertraut.
- **Coverage erbt die Tiers der Access Map.** Anomalien, die auf Drift aufbauen,
  sind durch die abgestufte Audit-Coverage von Modul III begrenzt; der
  Inhaltskatalog (unzulässige Inhalte) ist ein konservatives, nicht
  erschöpfendes Starter-Set und wird als solches dargestellt.
:::

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — `finding.reported`,
  `guardrail.observed` und der Runtime-Ingestion-Kanal.
- [Live-Ingest — der In-Process-Observe-Produzent](/de/reference/modules/live-ingest/) —
  das deny-closed Modul, das den Live-Feed `guardrail.observed` publiziert, den
  dieses Modul konsumiert.
- [Modul III — die Read/Write Access Map](/de/reference/modules/iii-access-map/) —
  der Drift, den dieses Modul korreliert.
- [Modulkatalog](/de/reference/modules/overview/) — die Ebene und der
  Aktuationsstatus von Modul IX.
- [Regieren und freigeben](/de/how-to/govern-and-approve/) — das Handeln auf
  Findings und Enforcement.
- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — der Export
  verifizierbarer Evidenz an einen SIEM-/WORM-Store.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was heute gebaut,
  beobachtet und aktuiert ist.
