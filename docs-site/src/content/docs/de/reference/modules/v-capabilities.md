---
title: "Modul V — MCP, Skills & Capability-Management"
description: >-
  Das Capability-Management-Overlay: welcher MCP-Server welches Tool exponiert, was
  seine Transport- und Secret-Referenzen sind, welcher Agent an welche Capability
  verdrahtet ist, seine Versionshistorie und seine grundlegende Verbindungs-Gesundheit
  — gouverniert, auditiert und standardmäßig untrusted, wo die MCP-Spezifikation es
  vorschreibt.
---

Modul V ist das **Capability-Management-Overlay**: Es gouverniert die Tools und
Capabilities Ihrer Agenten — welcher MCP-Server welches Tool exponiert, was sein
Transport, sein Scope und seine Konfiguration sind, welcher Origin an welche
Capability verdrahtet ist, seine Versionshistorie und seine grundlegende
Verbindungs-Gesundheit. Es sitzt im **Management-Layer** und hat **keine
Aktuierungsfläche**: Es katalogisiert, gouverniert und auditiert, führt aber niemals
ein Tool aus und mutiert niemals eine laufende MCP-Runtime.

## Was es ist

Das Modul ist ein Overlay, das **auf** der passiven Discovery von Modul I und der
Introspektion der Connectors aufbaut. Es implementiert den MCP-Client **nicht** neu,
und es re-materialisiert bewusst **nicht** die Kern-Entitäten, die das Inventory
bereits besitzt (die Datensätze für MCP-Server, Skill, Tool und Resource).
Stattdessen liest es diese Kern-Entitäten und speichert nur seine **eigenen**
Overlays, geschlüsselt nach den bereits bereinigten natürlichen Referenzen der
Connectors und zur Lesezeit auf Kern-Entitäten aufgelöst — eine
Single-Writer-Disziplin, die es davon abhält, mit dem Materializer des Inventory zu
rennen.

Dies ist von Modul III abzugrenzen. Modul V beantwortet *„mit welcher Capability ein
Agent verbunden ist“*; [Modul III](/de/reference/modules/iii-access-map/) beantwortet
*„welche Ressource ein Origin gelesen oder geschrieben hat“*. Es sind getrennte
Sichten, und das Produkt vermengt sie niemals.

## Sein Vertrag & seine Entitäten

Modul V besitzt vier Overlay-Entitäten (jede mit dem Präfix `capabilities.`):

| Entität | Was sie hält |
|---|---|
| **`mcp_config`** | Die verwaltete Konfiguration eines MCP-Servers — Transport, Scope, eine Endpoint-**Referenz** und **Secret-Referenzen**. Es gibt keine Spalte, die ein verwendbares Credential halten kann. |
| **`config_revision`** | Ein Append-only-Snapshot pro Konfigurationsversion — die unveränderliche Versionshistorie, die das Löschen der Konfiguration überdauert. |
| **`wiring`** | Der Capability-Verbindungsgraph: eine `origin → capability`-Kante, gespeichert per natürlicher Referenz, niemals per Kern-Entitäts-ID. |
| **`health`** | Das zuletzt beobachtete Verbindungssignal einer Capability (`connected` / `degraded` / `down` / `unknown`) — ein grundlegendes Signal, **kein** SLA. |

Zwei Vertragseigenschaften sind nicht verhandelbar. **MCP-Tool-Annotationen sind
untrusted**: Die `readOnlyHint`/`destructiveHint` eines Tools sind ein *deklarierter*
Hinweis vom Server, den Clients laut MCP-Spezifikation als untrusted behandeln müssen
— jede Tool-Projektion trägt ein explizites Untrusted-Flag, niemals ein
Security-Badge. **Keine Secret-Werte auf dem Wire**: Eine Konfiguration referenziert
Secrets per Name, Art und maskiertem Hinweis; das Backend weist Inline-Credentials in
einem Endpoint oder einer Spec zurück, statt sie zu speichern. Minimaldaten ist eine
Eigenschaft des Wire, kein nachträglicher Gedanke.

Das Lesen des Katalogs ist RBAC-gegatet und tenant-scoped. Das Ändern einer
Konfiguration — und der Secrets, die sie referenziert — ist eine **privilegierte
Änderung**, die im Append-only-, hash-chained Ledger aufgezeichnet und dem realen
Principal zugeordnet wird.

## Was es konsumiert & produziert

Modul V wird vom [Event-Bus](/de/reference/events/) gespeist, nicht durch eigenes
Polling. Es reagiert auf zwei Kanäle:

- **`edge.observed`** — Laufzeit-Capability-Nutzung wird zu `wiring`-Kanten. Das
  `Source`-Feld unterscheidet **beobachtete** Signale (`otel`) von **deklarierten**
  (`mcp_annotation`), und ein neuerer Config-Discovery-Feeder kennzeichnet statisch
  deklarierte Capabilities mit einer `config`-Quelle.
- **`finding.reported`** — die Connection-Health-Findings der Connectors speisen den
  Last-Signal-Status des `health`-Overlays.

Es produziert keine eigenen Events und dispatcht nichts an laufende Infrastruktur;
seine Ausgabe wird vom Management-UI und von anderen Modulen über seine typisierten
Routen gelesen.

:::caution[Ehrliche Grenzen]
- **Keine Aktuierung.** Modul V gouverniert und katalogisiert; es führt niemals ein
  Tool aus, wählt einen MCP-Server an oder mutiert eine laufende Runtime. Es ist von
  Natur aus ein Management-Overlay.
- **Annotation-Trust-Decke.** `readOnlyHint`/`destructiveHint` sind *deklariert* und
  werden als **untrusted** ausgewiesen — die Korroboration von Read-/Write-Intent
  gegen reale Signale ist Aufgabe von Modul III, nicht dieses Moduls.
- **Connection-Health ist kein SLA.** Das `health`-Overlay ist nur das letzte
  Verbindungssignal; formales Uptime-, SLA- und Trend-Reporting gehört zu Modul XXII.
- **Discovery ist so tief, wie die Connectors es sind.** Laufzeitbeobachtete
  Capabilities tauchen erst auf, sobald ein Agent sie ausübt; statisch deklarierte
  Claude-Code-Flächen (Subagents, Skills, Plugins, Output Styles) werden nun durch
  einen dedizierten Config-Feeder vorab vor der Ausführung entdeckt, aber er emittiert
  **nur strukturelle Metadaten** — Namen, niemals Prompt-Inhalte, Skill-/Plugin-
  Inhalte oder Secrets.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul V sitzt und sein
  Aktuierungsstatus.
- [Modul III — Zugriffs- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — die
  R/RW-Sicht, von der sich dieses Modul bewusst abgrenzt.
- [Event-Bus-Referenz](/de/reference/events/) — die `edge.observed`- und
  `finding.reported`-Payloads, die es konsumiert.
- [Architektur-Überblick](/de/explanation/architecture/overview/) — die
  Engine-plus-Module-Komposition.
- [Gouvernieren und freigeben](/de/how-to/govern-and-approve/) — auf das reagieren, was
  der Katalog ausweist.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der ehrliche
  Govern-vs.-Actuate-Vertrag des Produkts.
