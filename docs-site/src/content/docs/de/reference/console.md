---
title: Konsolenreferenz — jeder Bildschirm und seine erforderliche Berechtigung
description: >-
  Alle von der Olivares-AI-Konsole veröffentlichten Routen, gruppiert nach den
  Bereichen der Konsole, mit der jeweils erforderlichen RBAC-Berechtigung und
  der Referenzseite, die der Hilfe-Link im Produkt öffnet. Aus dem
  Routeninventar der Konsole generiert.
---

:::note[Edition]
Verzeichnisquellen (LDAP, Okta/Entra und Agent-/Workload-IdPs), der Lebenszyklus verwalteter Identitäten sowie der eingehende SCIM-Server benötigen Business/Enterprise-Erweiterungen. Community behält allgemeinen Source-Ingest, Ressourcenkatalog, Sicherheitsbefunde, Access Map, Identitätsinventar, Single-IdP-SSO, lokale Rollen und Bindungen, Policies und Freigaben. Kostenpflichtige Konsolenbereiche und CLI-Befehle sind ausgeblendet; ihre veröffentlichten API-Routen liefern HTTP `501`. Das Aktivieren eines Moduls fügt seine Business-Implementierung nicht hinzu.
:::

Diese Seite ist die Karte der Konsole. Sie listet **jede von der Anwendung gemountete
Route** — keine Auswahl und nicht nur die Routen, an deren Dokumentation sich jemand
erinnert hat — samt der Berechtigung, die ein Principal zum Öffnen benötigt, und der
weiterführenden Dokumentation.

Die Konsole hat **eine Navigation: die linke Seitenleiste**. Sie listet die Orte in vier
Gruppen — **Arbeit** (Start, Sitzungen, Dateien & Arbeitsbereiche, Arbeit &
Kommunikation), **Werkzeuge** (KI-Werkzeuge, Modelle, MCP & Skills, Quellen),
**Kontrolle** (Regeln & Genehmigungen, Nutzung & Kosten, Aktivität & Audit, Zugriffskarte) und
**Verwaltung** (Personen & Gruppen, Sicherheit, Betrieb, Lizenz) — und Einstellungen ganz
unten. Jeder Bildschirm gehört zu einem Ort, und die Linkzeile über einem Bildschirm
listet die anderen Bildschirme dieses Orts. Ein Ort erscheint nur, wenn Ihre
Berechtigungen mindestens einen seiner Bildschirme erlauben, und ein Business-Bildschirm
nur in der Business-Edition. Die Seitenleiste lässt sich auf ihre Symbole einklappen und
wieder öffnen; auf einem schmalen Bildschirm öffnet sie sich als Schublade über die
Menütaste. Die obere Leiste enthält den Seitentitel, die Suche (⌘K oder Strg+K), die
Benachrichtigungen und das Kontomenü. Die Befehlspalette durchsucht dieselben Bildschirme
mit denselben Berechtigungen. Die früheren Bereichsverzeichnis-Adressen (`/areas/…`)
öffnen den Ort, der ihre Bildschirme jetzt enthält, und behalten Abfrage und Fragment.

Eine neue Installation listet nur die erste Aufgabe: Start, Sitzungen, KI-Werkzeuge,
Genehmigungen, den Einrichtungsassistenten und Einstellungen. Jeder andere
Bildschirm ist dort eine Vorschau: Adresse, API und CLI funktionieren weiter, und Navigation,
Befehlspalette und Tastenkürzel listen ihn nicht. Eine Installation, die vor dem Upgrade
bestand, listet jeden Bildschirm, den sie bisher gelistet hat, ebenso eine Installation,
deren Administrator ihre Module gewählt hat (ab dem nächsten Start), oder eine, die die
Demo-Daten nutzt.

Die Seite ist **generiert**. Das Verzeichnis stammt aus
`web/src/features/route-census.json`, dem append-only Inventar, das
`registry.route-conservation.test.ts` gegen den gebauten Router prüft. Ein Bildschirm
kann daher nicht hinzugefügt, verschoben oder verloren werden, ohne dass sich diese
Seite mitändert. Name und Kurzbeschreibung jedes Bildschirms sind die **eigenen Strings
der Konsole** aus demselben Übersetzungskatalog, den die Konsole rendert. Was Sie
hier lesen, sehen Sie auch im Produkt. Die Tabellen darunter gruppieren diese Zeilen nach Bereichen, nach Start (der Übersicht)
und vor Anmeldung, Einrichtung und Konto. Die neun früheren Bereichsverzeichnis-Adressen stehen bei den
Routen, die außerhalb der Feature-Registry gemountet sind; jede leitet jetzt zu ihrem Ort weiter.

:::note[Berechtigungen erzwingt die Engine, nicht diese Tabelle]
Die Spalte `Erforderlich` nennt die Berechtigung, die die Konsole vor dem Anbieten einer
Route anhand der von der Engine gelieferten effektiven Berechtigungen prüft. Die Engine
autorisiert API-Anfragen unabhängig davon, auch Anfragen außerhalb der Konsole.
Ein sichtbarer Eintrag belegt nicht, dass das Modul konfiguriert oder betriebsbereit ist.
Siehe [Rollen und Berechtigungen](/de/reference/modules/vi-governance/).
:::

## So lesen Sie diese Seite

- **Bildschirm** — der Name in der Linkzeile über einem Bildschirm und in der Befehlspalette.
- **Pfad** — die URL unter dem Origin Ihrer Konsole. Sie ist ein veröffentlichter
  Contract: Bookmark, Deep-Link im Runbook und Querverweis aus der Dokumentation
  verwenden alle genau diesen String.
- **Erforderlich** — die RBAC-Berechtigung. `any signed-in user` bedeutet,
  dass jeder authentifizierte Principal die Route öffnen kann; **no sign-in**
  bedeutet, dass sie bereits vor dem Bestehen einer Session bereitgestellt wird.
- **Referenz** — die Seite, die der eigene Hilfe-Link der Konsole für diesen Bildschirm
  öffnet.

Die folgenden Überschriften sind die Registry-Bereiche, nach denen der Routenzensus die
Bildschirme gruppiert: Infrastruktur, KI, Daten & Kontext, Arbeit & Kommunikation, Automatisierung,
Sicherheit & Identität, Auslieferung, Aufsicht, dann System &
Einstellungen.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Die Konsole veröffentlicht **66 Routen**. Jede steht mit der erforderlichen
Berechtigung und der vom Hilfe-Link geöffneten Referenzseite in den Tabellen unten.

### Start

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Start | `/` | Estate-Übersicht und Zustand auf einen Blick | any signed-in user | [Dokumentationsstart](/de/) |

### Infrastruktur

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Umgebung | `/estate` | Prüfen Sie konfigurierte Ressourcen und ihre Beziehungen, einschließlich der Abhängigkeiten von Arbeitseinheiten. | `sessions:run:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Inventar | `/inventory` | Agenten, MCP-Server und Modelle, die Konnektoren beobachtet haben, erkennen und katalogisieren. | `inventory:catalog:read` | [reference/modules/i-inventory](/de/reference/modules/i-inventory/) |
| Workspace | `/workspace` | Agenten, Sessions, Ressourcen und Aktivität im Scope eines Workspace | `tenant:read` | [reference/modules/xx-multi-tenancy](/de/reference/modules/xx-multi-tenancy/) |

### KI

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Agentenwerkzeuge | `/agent-tools` | Die Konten und API-Schlüssel sehen, mit denen Sitzungen laufen; Werkzeuge installieren und anmelden nur Bereitstellungsadministratoren | `sessions:account:read` | [how-to/add-a-provider](/de/how-to/add-a-provider/) |
| Sitzungen betreiben | `/agentops` | Sitzungen starten und verfolgen, was jede tut, auch die, die Olivares findet | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/de/how-to/run-claude-code-with-olivares/) |
| Modellbetrieb | `/model-operations` | Eigene Modelle, Zulassung und Deployments | `models:registry:read` | [reference/modules/xxiii-model-operations](/de/reference/modules/xxiii-model-operations/) |
| Modelle | `/models` | Modelle, Routing und Governance | `models:catalog:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |
| Plattformen | `/platforms` | Deployment-Oberflächen, Compliance-Matrix und Modell-Lifecycle pro Plattform | `models:platforms:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |
| Anbieterkonten | `/provider-accounts` | Benannte Anbieterkonten auflisten und ein vorhandenes Anbieterprofil als Konto übernehmen | `sessions:account:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Quellenbindungen | `/provider-bindings` | Konfigurierte Quellen mit Anbieterprofilen verknüpfen | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Anbieterprofile | `/provider-profiles` | Die Anbieter-Home-Verzeichnisse registrieren und verwalten, unter denen Sessions starten, und ihre Konfiguration bei Bedarf auslesen | `sessions:profile:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Anbieter | `/providers` | Die API-Schlüssel und Endpunkte registrieren, mit denen Sessions starten; sie testen, wechseln und widerrufen | `sessions:provider:read` | [how-to/add-a-provider](/de/how-to/add-a-provider/) |
| Rate Limits | `/rate-limits` | Anthropic-Rate-Limit-Inventar (schreibgeschützt) | `models:ratelimits:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |
| Reichweite | `/reach` | Sehen Sie, welche Ziele jedes Werkzeug erreichen kann und welche auf Genehmigung warten. | `accessmap:graph:read` | [reference/modules/iii-access-map](/de/reference/modules/iii-access-map/) |
| Sandbox | `/sandbox` | Isolierte Agententests und Replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/de/reference/modules/xvii-sandbox/) |
| Sitzungen | `/sessions` | Sitzungen starten und verfolgen, was jede tut, auch die, die Olivares findet | `sessions:live:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Voice | `/voice` | Voice- und Realtime-Sessions | `voice:session:read` | [reference/modules/xvi-voice](/de/reference/modules/xvi-voice/) |
| Workspace-Vorlagen | `/workspace-templates` | Wiederverwendbare Snapshots der Session-Konfiguration: Hooks, Einstellungen, Connectors und Policies. | `sessions:template:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |

### Daten & Kontext

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Agentenartefakte | `/agent-artifacts` | Skills, MCP-Erweiterungen und Anweisungsdateien — Registry, Posture und Supply-Chain-BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/de/reference/modules/xxiii-model-operations/) |
| MCP & Skills | `/capabilities` | MCP-Server, Skills und Tools regeln | `capabilities:catalog:read` | [reference/modules/v-capabilities](/de/reference/modules/v-capabilities/) |
| Katalog | `/catalog` | Kuratierte und genehmigte Agenten und Capabilities | `catalog:entry:read` | [reference/modules/xiv-catalog](/de/reference/modules/xiv-catalog/) |
| Wissen | `/knowledge` | Wissensbasen, RAG und Data Lineage | `knowledge:kb:read` | [reference/modules/viii-knowledge](/de/reference/modules/viii-knowledge/) |

### Arbeit & Kommunikation

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Kommunikation | `/communications` | Kanäle, direkte Mitteilungen und der persönliche Posteingang des gewählten Workspace | `sessions:channel:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Kanalverwaltung | `/communications/administration` | Kanäle verwalten: Konfiguration und Berechtigungsverlauf | `sessions:channel:admin` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Übergaben | `/communications/handoffs` | An Sie gerichtete Angebote zur Übernahme von Arbeitsverantwortung: Kontext lesen und annehmen oder ablehnen | `sessions:delivery:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Kommunikations-Posteingang | `/communications/inbox` | An Sie adressierte Nachrichten: jede öffnen und bestätigen | `sessions:delivery:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Neuer Kanal | `/communications/new` | Einen Kanal mit ausdrücklichen Anfangsberechtigungen anlegen | `sessions:channel:write` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Protokollbindungen | `/communications/protocol-bindings` | Geregelte A2A- und MCP-Bindungen zusammenstellen und abgleichen | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Work | `/work` | Gemeinsame Arbeit: Einheiten, Abhängigkeiten, Abnahme und Entscheidungen | `sessions:work:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |

### Automatisierung

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Alerting | `/alerting` | Findings an Ziele routen und Zustellungen prüfen | `notify:route:read` | [reference/modules/xv-notify](/de/reference/modules/xv-notify/) |
| Webhooks & Events | `/eventing` | Ausgehende Webhook-Subscriptions, ihr Zustellungsprotokoll und die Dead-Letter-Queue. | `eventing:subscription:read` | [reference/modules/eventing](/de/reference/modules/eventing/) |

### Sicherheit & Identität

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Access Map | `/access-map` | Was jeder Agent liest und schreibt (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/de/reference/modules/iii-access-map/) |
| Richtlinienexport | `/agentcore-export` | Die Projektion der Governance-Regeln dieses Mandanten auf AWS AgentCore als Cedar-Richtlinien planen, prüfen und anwenden; das Planen schreibt nichts | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Verwaltete Richtlinie | `/claude-policy` | Verwaltete Policy, Hooks, MCP, Sandbox und Policy-as-Code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/de/how-to/connectors/claude-code-hooks-pep/) |
| Identität & Zugriff | `/identity` | SSO, SCIM und die Identitäten, die Ihre Agenten verwenden | `governance:identity:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Inference Proxy | `/inference-proxy` | Proxy-Gates, Egress-DLP-Regeln und Gerätefreigaben | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/de/reference/modules/inferenceproxy/) |
| Kill Switch | `/killswitch` | Notstopp, Wiederherstellung unter dualer Kontrolle und Guardian-Containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/de/how-to/cookbook/kill-switch-drill/) |
| Berechtigungen | `/permissions` | Identität, Rollen und Freigaben | `governance:identity:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Routine-Policies | `/routine-policies` | Taktuntergrenzen, Parallelitätsgrenzen, Freigabeanforderungen und Cron-Allowlists für Claude-Code-Routinen. | `governance:routine:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Sicherheit | `/security` | Guardrails, Forensik und Anomalien | `security:finding:read` | [reference/modules/ix-security](/de/reference/modules/ix-security/) |
| Tresor | `/vault` | Ihre Passwörter, Notizen und Authenticator-Codes | `tenant:read` | [reference/console](/de/reference/console/) |

### Auslieferung

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Git-Veröffentlichung | `/git-publication` | Commits pushen, Pull Requests öffnen und über freigegebene Git-Ziele mergen | `gitpublish:target:read` | [reference/modules/gitpublish](/de/reference/modules/gitpublish/) |

### Aufsicht

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Adoption | `/adoption` | Produktivität, Akzeptanz und Modellmix | `adoption:metrics:read` | [reference/modules/claudeadoption](/de/reference/modules/claudeadoption/) |
| Supply Chain | `/attestation` | Release-Attestierung — SLSA, SBOM, VEX und Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/de/how-to/verify-a-release/) |
| Audit-Ledger | `/audit` | Manipulationserkennbares Evidenz-Ledger | `audit:read` | [reference/modules/ix-security](/de/reference/modules/ix-security/) |
| Dashboards | `/dashboards` | Executive-KPIs und Reporting | any signed-in user | [reference/modules/xxi-executive-dashboards](/de/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Qualität, Evaluierungen und Regression | `evals:run:read` | [reference/modules/xii-evals](/de/reference/modules/xii-evals/) |
| Health & SLA | `/health` | Uptime und SLAs für Agenten und MCP | `health:status:read` | [reference/modules/xxii-health](/de/reference/modules/xxii-health/) |
| Observability | `/observability` | Ingestion-Zustand nach Standard und Trace-Drilldown | `health:status:read` | [reference/modules/observability](/de/reference/modules/observability/) |
| Aufbewahrung | `/retention` | Wie lange jede Datenklasse aufbewahrt wird, der Lauf, der das umsetzt, und seine versiegelten Zertifikate | `compliance:retention:read` | [reference/modules/xiii-compliance](/de/reference/modules/xiii-compliance/) |
| Budgets | `/stored-budgets` | Verbrauch gegenüber jedem gespeicherten Limit. Reservierte Kapazität wird auf das Limit angerechnet. | `finops:budget:read` | [reference/modules/xi-finops](/de/reference/modules/xi-finops/) |
| Nutzung | `/usage` | Die offiziellen 5-Stunden- und Wochenlimits jedes Kontos, das nächste zuerst, und die Tokens und Kosten von heute. | `sessions:run:read` | [reference/console](/de/reference/console/) |

### System & Einstellungen

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| API Playground | `/api-playground` | Control-Plane-API interaktiv erkunden und testen | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/de/reference/modules/xix-api-manage-as-code/) |
| Backups | `/backups` | Backups auslösen, planen, herunterladen und wiederherstellen, mit einer zweiten Bestätigung auf dem destruktiven Pfad. | `system:admin` | [how-to/backup-and-restore](/de/how-to/backup-and-restore/) |
| Verwaltung | `/console` | Benutzer, SSO/IdP, Arbeitsbereiche, Agentengruppen, Rollen, Geheimnisse, Konnektoren, API-Schlüssel und die Lizenz dieser Installation | `tenant:admin` | [reference/modules/xx-multi-tenancy](/de/reference/modules/xx-multi-tenancy/) |
| Quellvergleich | `/console/sources/diff` | Vergleichen Sie eine Basis- und eine Head-Revision eines verbundenen Git-Repositorys, Datei für Datei | `system:admin` | [reference/console](/de/reference/console/) |
| Logs | `/logs` | Live-Log-Stream der Engine, nach Level und Modul filterbar, mit Suche und Pause. | `system:admin` | [how-to/troubleshooting](/de/how-to/troubleshooting/) |
| Einrichtungsassistent | `/onboarding` | Schrittweise Konfiguration der Bereitstellung | `system:admin` | [start/quickstart](/de/start/quickstart/) |
| Updates | `/updates` | Die Version, die diese Engine ausführt, das neueste signierte Release ihres Kanals und eine Aktion zum Installieren | `system:admin` | [how-to/upgrade-and-rollback](/de/how-to/upgrade-and-rollback/) |

### Anmeldung, Einrichtung und Konto

Diese Routen werden außerhalb der Feature-Registry gemountet. Die mit **no sign-in**
gekennzeichneten Routen werden bereits vor dem Bestehen einer Session
bereitgestellt — nur diese Konsolenrouten verhalten sich so.

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Einladung annehmen | `/accept-invite` | Ziel eines per E-Mail versendeten Einladungslinks: Der Eingeladene legt ohne vorherige Session ein Passwort fest und tritt dem Workspace bei. | **no sign-in** | — |
| Anmelden | `/login` | Seite zur Anmeldung mit Zugangsdaten oder Token für ein bereits bereitgestelltes Konto. | **no sign-in** | — |
| Gerät koppeln | `/pair` | Ziel eines einmaligen Links zur Gerätekopplung: Das neue Gerät meldet sich ohne vorherige Sitzung an. | **no sign-in** | — |
| Einstellungen | `/settings` | Workspace- und Kontoeinstellungen | any signed-in user | — |
| Ersteinrichtung | `/setup` | Einmalige Seite, die eine frische Bereitstellung nutzbar macht: Sie verbraucht das Setup-Token und erstellt das erste Owner-Konto. | **no sign-in** | — |
| Öffentlicher Status | `/status-page` | Komponentenzustand für nicht angemeldete Personen; wird bei geöffneter Seite selbstständig aktualisiert. | **no sign-in** | — |

<!-- END GENERATED olivares-console-routes -->

## Was diese Seite nicht erklärt

Sie ist eine Karte, kein Handbuch. Sie nennt vorhandene Bildschirme, ihre Position und
wer sie öffnen darf; sie führt nicht durch eine Aufgabe. Beginnen Sie dafür bei den
[Pfaden nach Rolle](/de/start/paths-by-role/) oder den
[How-to-Anleitungen](/de/how-to/self-hosting/).

Bildschirme, deren Backend deny-closed bleibt, bis ein Betreiber es bereitstellt,
erscheinen hier wie alle anderen — die Route existiert und die Berechtigung ist real.
Welches Modul aktuiert und welches gegatet ist, steht in der
[Modulübersicht](/de/reference/modules/overview/); die Seite
[Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) nennt die allgemeine Regel.
Die Liste eines Bereichsverzeichnisses ist keine Live-Ablesung von Fähigkeit oder
Bereitschaft.
