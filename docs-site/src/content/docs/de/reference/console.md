---
title: Konsolenreferenz — jeder Bildschirm und seine erforderliche Berechtigung
description: >-
  Alle von der Olivares-AI-Konsole veröffentlichten Routen, gruppiert nach den fünf
  Hubs, mit der jeweils erforderlichen RBAC-Berechtigung und der Referenzseite, die
  der Hilfe-Link im Produkt öffnet. Aus dem Routeninventar der Konsole generiert.
---

Diese Seite ist die Karte der Konsole. Sie listet **jede von der Anwendung gemountete
Route** — keine Auswahl und nicht nur die Routen, an deren Dokumentation sich jemand
erinnert hat — samt der Berechtigung, die ein Principal zum Öffnen benötigt, und der
weiterführenden Dokumentation.

Die Seite ist **generiert**. Das Verzeichnis stammt aus
`web/src/features/route-census.json`, dem append-only Inventar, das
`registry.route-conservation.test.ts` gegen den gebauten Router prüft. Ein Bildschirm
kann daher nicht hinzugefügt, verschoben oder verloren werden, ohne dass sich diese
Seite mitändert. Name und Kurzbeschreibung jedes Bildschirms sind die **eigenen Strings
der Konsole** aus demselben Übersetzungskatalog, den die Seitenleiste rendert. Was Sie
hier lesen, sehen Sie auch im Produkt.

:::note[Berechtigungen erzwingt die Engine, nicht diese Tabelle]
Die Spalte `Erforderlich` nennt die Berechtigung, die die Konsole prüft, bevor sie eine
Route anbietet, und spiegelt das RBAC der Engine. Maßgeblich bleibt die Engine: Ein
Deep-Link auf einen Bildschirm, für den Sie keine Berechtigung besitzen, wird von der
API abgelehnt und nicht nur in der Seitenleiste verborgen. Siehe
[Rollen und Berechtigungen](/de/reference/modules/vi-governance/).
:::

## So lesen Sie diese Seite

- **Bildschirm** — der Name in Seitenleiste und Befehlspalette.
- **Pfad** — die URL unter dem Origin Ihrer Konsole. Sie ist ein veröffentlichter
  Contract: Bookmark, Deep-Link im Runbook und Querverweis aus der Dokumentation
  verwenden alle genau diesen String.
- **Erforderlich** — die RBAC-Berechtigung. `any signed-in user` bedeutet,
  dass jeder authentifizierte Principal die Route öffnen kann; **no sign-in**
  bedeutet, dass sie bereits vor dem Bestehen einer Session bereitgestellt wird.
- **Referenz** — die Seite, die der eigene Hilfe-Link der Konsole für diesen Bildschirm
  öffnet.

Die fünf folgenden Überschriften sind die Hubs der Konsole in der Reihenfolge der
Seitenleiste.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Die Konsole veröffentlicht **59 Routen**. Jede steht mit der erforderlichen
Berechtigung und der vom Hilfe-Link geöffneten Referenzseite in den Tabellen unten.

### Betreiben

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Übersicht | `/` | Estate-Übersicht und Zustand auf einen Blick | any signed-in user | [Dokumentationsstart](/de/) |
| Claude Code | `/agentops` | Claude-Code-Sessions erstellen, anhängen und regeln — ohne SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/de/how-to/run-claude-code-with-olivares/) |
| Backups | `/backups` | Backups auslösen, planen, herunterladen und wiederherstellen, mit einer zweiten Bestätigung auf dem destruktiven Pfad. | `system:admin` | [how-to/backup-and-restore](/de/how-to/backup-and-restore/) |
| Health & SLA | `/health` | Uptime und SLAs für Agenten und MCP | `health:status:read` | [reference/modules/xxii-health](/de/reference/modules/xxii-health/) |
| Kill Switch | `/killswitch` | Notstopp, Wiederherstellung unter dualer Kontrolle und Guardian-Containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/de/how-to/cookbook/kill-switch-drill/) |
| Logs | `/logs` | Live-Log-Stream der Engine, nach Level und Modul filterbar, mit Suche und Pause. | `system:admin` | [how-to/troubleshooting](/de/how-to/troubleshooting/) |
| Observability | `/observability` | Ingestion-Zustand nach Standard und Trace-Drilldown | `health:status:read` | [reference/modules/observability](/de/reference/modules/observability/) |
| Sandbox | `/sandbox` | Isolierte Agententests und Replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/de/reference/modules/xvii-sandbox/) |
| Sessions | `/sessions` | Live-Agentenbetrieb und Timelines | `sessions:live:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Tenants | `/tenants` | Dienst eines Tenants entziehen oder wiederherstellen | `system:admin` | [how-to/troubleshooting](/de/how-to/troubleshooting/) |
| Voice | `/voice` | Voice- und Realtime-Sessions | `voice:session:read` | [reference/modules/xvi-voice](/de/reference/modules/xvi-voice/) |
| Work | `/work` | Dauerhafter Session-übergreifender Backlog: Elemente, Abhängigkeiten, Abnahme und Entscheidungen | `sessions:work:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Workspace | `/workspace` | Agenten, Sessions, Ressourcen und Aktivität im Scope eines Workspace | `tenant:read` | [reference/modules/xx-multi-tenancy](/de/reference/modules/xx-multi-tenancy/) |
| Workspace-Vorlagen | `/workspace-templates` | Wiederverwendbare Snapshots der Session-Konfiguration: Hooks, Einstellungen, Connectors und Policies. | `sessions:template:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |

### Automatisieren

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Alerting | `/alerting` | Findings an Ziele routen und Zustellungen prüfen | `notify:route:read` | [reference/modules/xv-notify](/de/reference/modules/xv-notify/) |
| Automatisierungen | `/automations` | Alle drei Automatisierungswege und ihr Trigger-Katalog | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/de/reference/modules/iv-orchestration/) |
| Webhooks & Events | `/eventing` | Ausgehende Webhook-Subscriptions, ihr Zustellungsprotokoll und die Dead-Letter-Queue. | `eventing:subscription:read` | [reference/modules/eventing](/de/reference/modules/eventing/) |
| Orchestrierung | `/orchestration` | Agent-zu-Agent-Koordination und Zeitpläne | `orchestration:graph:read` | [reference/modules/iv-orchestration](/de/reference/modules/iv-orchestration/) |

### Verbinden

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| API Playground | `/api-playground` | Control-Plane-API interaktiv erkunden und testen | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/de/reference/modules/xix-api-manage-as-code/) |
| MCP & Skills | `/capabilities` | MCP-Server, Skills und Tools regeln | `capabilities:catalog:read` | [reference/modules/v-capabilities](/de/reference/modules/v-capabilities/) |
| Katalog | `/catalog` | Kuratierte und genehmigte Agenten und Capabilities | `catalog:entry:read` | [reference/modules/xiv-catalog](/de/reference/modules/xiv-catalog/) |
| Protokollbindungen | `/communications/protocol-bindings` | Geregelte A2A- und MCP-Bindungen zusammenstellen und abgleichen | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/de/reference/modules/ii-sessions/) |
| Deployment | `/deploy` | Agenten für Infrastruktur bereitstellen und verdrahten | `deploy:deployment:read` | [reference/modules/vii-deploy](/de/reference/modules/vii-deploy/) |
| Inventar | `/inventory` | Jeden Agenten, jedes MCP und jedes Modell entdecken und katalogisieren | `inventory:catalog:read` | [reference/modules/i-inventory](/de/reference/modules/i-inventory/) |
| Wissen | `/knowledge` | Wissensbasen, RAG und Data Lineage | `knowledge:kb:read` | [reference/modules/viii-knowledge](/de/reference/modules/viii-knowledge/) |
| Modellbetrieb | `/model-operations` | Eigene Modelle, Zulassung und Deployments | `models:registry:read` | [reference/modules/xxiii-model-operations](/de/reference/modules/xxiii-model-operations/) |
| Modelle | `/models` | Modelle, Routing und Provider-Schlüssel | `models:catalog:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |
| Einrichtungsassistent | `/onboarding` | Schrittweise Konfiguration der Bereitstellung | `system:admin` | [start/quickstart](/de/start/quickstart/) |
| Plattformen | `/platforms` | Deployment-Oberflächen, Compliance-Matrix und Modell-Lifecycle pro Plattform | `models:platforms:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |

### Regeln

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Access Map | `/access-map` | Was jeder Agent liest und schreibt (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/de/reference/modules/iii-access-map/) |
| AgentCore-Export | `/agentcore-export` | Cedar-Policy-Export nach AWS AgentCore planen und anwenden sowie Änderungen vorab prüfen. | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Claude-Code-Governance | `/claude-policy` | Verwaltete Policy, Hooks, MCP, Sandbox und Policy-as-Code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/de/how-to/connectors/claude-code-hooks-pep/) |
| Control Console | `/console` | Benutzer onboarden, SSO/IdP verbinden und Workspaces sowie Agent-Groups gestalten. | `tenant:admin` | [reference/modules/xx-multi-tenancy](/de/reference/modules/xx-multi-tenancy/) |
| Identity & NHI | `/identity` | SSO, SCIM, NHI-Roster und WIF-Graph | `governance:identity:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Inference Proxy | `/inference-proxy` | Proxy-Gates, Egress-DLP-Regeln und Gerätefreigaben | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/de/reference/modules/inferenceproxy/) |
| Berechtigungen | `/permissions` | Identität, Rollen und Freigaben | `governance:identity:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |
| Rate Limits | `/rate-limits` | Anthropic-Rate-Limit-Inventar (schreibgeschützt) | `models:ratelimits:read` | [reference/modules/x-models](/de/reference/modules/x-models/) |
| Datenresidenz | `/residency` | Jede Organisation an eine Region binden oder ungebunden lassen | `system:admin` | [reference/modules/xiii-compliance](/de/reference/modules/xiii-compliance/) |
| Routine-Policies | `/routine-policies` | Taktuntergrenzen, Parallelitätsgrenzen, Freigabeanforderungen und Cron-Allowlists für Claude-Code-Routinen. | `governance:routine:read` | [reference/modules/vi-governance](/de/reference/modules/vi-governance/) |

### Nachweisen

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Claude-Code-Adoption | `/adoption` | Produktivität, Akzeptanz und Modellmix | `adoption:metrics:read` | [reference/modules/claudeadoption](/de/reference/modules/claudeadoption/) |
| Agentenartefakte | `/agent-artifacts` | Skills, MCP-Erweiterungen und Anweisungsdateien — Registry, Posture und Supply-Chain-BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/de/reference/modules/xxiii-model-operations/) |
| Supply Chain | `/attestation` | Release-Attestierung — SLSA, SBOM, VEX und Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/de/how-to/verify-a-release/) |
| Audit-Ledger | `/audit` | Manipulationserkennbares Evidenz-Ledger | `audit:read` | [reference/modules/ix-security](/de/reference/modules/ix-security/) |
| Compliance | `/compliance` | Frameworks, Controls und Evidenz | `compliance:framework:read` | [reference/modules/xiii-compliance](/de/reference/modules/xiii-compliance/) |
| Dashboards | `/dashboards` | Executive-KPIs und Reporting | any signed-in user | [reference/modules/xxi-executive-dashboards](/de/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Qualität, Evaluierungen und Regression | `evals:run:read` | [reference/modules/xii-evals](/de/reference/modules/xii-evals/) |
| Kosten & FinOps | `/finops` | Token-Kosten, Budgets und Ausgaben | `finops:spend:read` | [reference/modules/xi-finops](/de/reference/modules/xi-finops/) |
| Posture-Export | `/posture-export` | Ground-Truth-Posture für einen Control Tower exportieren | `posture:export:read` | [reference/modules/posture-export](/de/reference/modules/posture-export/) |
| Aufzeichnungen | `/recordings` | Aufzeichnung und Replay privilegierter Sessions | `recording:session:admin` | [reference/modules/recording](/de/reference/modules/recording/) |
| Red-Teaming | `/red-team` | Adversarial Tests Ihrer Agenten | `redteam:target:read` | [reference/modules/xviii-redteam](/de/reference/modules/xviii-redteam/) |
| Berichte | `/reporting` | Governance-Berichte erzeugen und herunterladen | `reporting:report:read` | [reference/modules/reporting](/de/reference/modules/reporting/) |
| Sicherheit | `/security` | Guardrails, Forensik und Anomalien | `security:finding:read` | [reference/modules/ix-security](/de/reference/modules/ix-security/) |
| Session-Viewer | `/session-viewer/$id` (nur Deep-Link) | Vollständige Timeline einer aufgezeichneten Session, aus einer Zeile in Aufzeichnungen statt aus der Seitenleiste geöffnet. | `recording:session:admin` | [reference/modules/recording](/de/reference/modules/recording/) |
| Teamkosten | `/team-costs` | Nach Team zugeordnete Ausgaben, aufklappbar nach Projekt und Modell. | `finops:spend:read` | [reference/modules/xi-finops](/de/reference/modules/xi-finops/) |

### Anmeldung, Einrichtung und Konto

Diese Routen werden außerhalb der Feature-Registry gemountet. Die mit **no sign-in**
gekennzeichneten Routen werden bereits vor dem Bestehen einer Session
bereitgestellt — nur diese Konsolenrouten verhalten sich so.

| Bildschirm | Pfad | Funktion | Erforderlich | Referenz |
|---|---|---|---|---|
| Einladung annehmen | `/accept-invite` | Ziel eines per E-Mail versendeten Einladungslinks: Der Eingeladene legt ohne vorherige Session ein Passwort fest und tritt dem Workspace bei. | **no sign-in** | — |
| Anmelden | `/login` | Seite zur Anmeldung mit Zugangsdaten oder Token für ein bereits bereitgestelltes Konto. | **no sign-in** | — |
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
