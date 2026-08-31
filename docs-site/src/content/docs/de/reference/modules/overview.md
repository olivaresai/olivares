---
title: Modulkatalog
description: >-
  Die 30 Module von Olivares AI — organisiert nach den neun Capability-Bereichen,
  mit der ehrlichen Reife jedes Moduls. Olivares AI integriert, verwaltet und
  sichert KI im Unternehmen, eine Ground Truth: Claude Code auf der tiefsten Stufe, Codex und Grok Build daneben; dies ist
  die Referenz pro Modul.
---

Olivares AI integriert, verwaltet und sichert KI im Unternehmen, eine Ground Truth:
Claude Code auf der tiefsten Stufe, Codex und Grok Build daneben. Es ist eine **modulare Plattform** — eine Engine,
eine Konsole und **30 Module**, verdrahtet in einem einzigen Binary — die
beobachtet, wo Agenten laufen, regelt, was sie tun dürfen, und (auf einer
wachsenden Teilmenge) auf Ihrer realen Infrastruktur agiert. Jedes Modul (a)
konsumiert normalisierte Events/Daten aus dem Core, (b) deklariert seine
Entitäten im gemeinsamen Datenmodell und (c) stellt seine eigenen
API-Endpunkte und UI-Ansichten bereit — ohne den Core oder andere Module zu
berühren.

Die 30 Module sind nach den **neun Capability-Bereichen** unten organisiert.
Lesen Sie den Status jedes Moduls als **zwei Hälften**: *Govern/Observe*
(katalogisieren, beobachten, gaten, berichten) ist heute gebaut und verdrahtet;
*Actuate* (das Agieren auf realer Infrastruktur — deployen, dispatchen, senden,
durchsetzen, ausführen) fällt in ehrliche Zustände — **live** im Standard-Binary
für eine Teilmenge, **on-demand** für mehrere (das Backend ist gebaut und an
einen Injektionspunkt verdrahtet, bleibt aber deny-closed oder degradiert, bis
ein Operator es per Env-Konfiguration bereitstellt), **PARTIAL**, wo die
Oberfläche gegatet/opt-in ist, und eine deklarierte **deny-closed Naht** für den
Rest. Insbesondere **plant und regelt** deploy Deployments, **wendet** sie aber
**nicht** auf Live-Infrastruktur an, bis ein Executor bereitgestellt ist:
`apply`/`retire` liefern ein klares `503`. Die Tiefe variiert je nach Modul, und
ein Großteil des Produkts ist pre-1.0 / im Designstadium, wo vermerkt (siehe
[Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/)).

Die **access map** (`iii-access-map`) — der Read-/Read-Write-Graph dessen, was
jeder Agent berühren kann und tatsächlich berührt, mit Least-Privilege-Drift =
`Permitted ≠ Observed` — ist **eine der nützlichsten Capabilities unter den 30**,
nicht das gesamte Produkt. Die Breite ist der Punkt: neun Bereiche, eine Engine,
eine Konsole.

## Die 30 Module, nach Capability-Bereich

Jede Zeile verlinkt auf ihre Modulseite (`/reference/modules/<slug>/`). Die
Spalte **Actuate** ist der ehrliche Zustand der agierenden Hälfte; `—` bedeutet,
dass das Modul von Natur aus regelt/beobachtet und keine Actuation-Oberfläche
hat.

### Observe

| Modul | Actuate | Zweck |
|---|---|---|
| [Inventar & Discovery](/de/reference/modules/i-inventory/) | — | Entdecken und katalogisieren Sie jeden Agenten/jede Session/jeden MCP-Server/jedes Tool/Modell/jede Identität im Estate. |
| [Live-Betrieb & Sessions](/de/reference/modules/ii-sessions/) | — | Echtzeitzustand jedes Agenten und jeder Session; beherbergt außerdem die geregelte Claude-Code-Session-Runtime. |
| [Access- & Resource-Map (R/RW)](/de/reference/modules/iii-access-map/) | — | Worauf jeder Agent zugreift und ob er liest oder schreibt; Least-Privilege-Drift = `Permitted ≠ Observed`. |
| [Orchestrierung & A2A](/de/reference/modules/iv-orchestration/) | on-demand | Beobachten und regeln des Live-Delegations-/Kommunikationsgraphen; Dispatch ist on-demand verdrahtet, deny-closed bis bereitgestellt. |
| [MCP, Skills & Capabilities](/de/reference/modules/v-capabilities/) | — | Die Tools und Capabilities der Agenten visuell regeln. |
| [Health, SLA & Uptime](/de/reference/modules/xxii-health/) | — | Zuverlässigkeit der Agenten und MCP-Server des Estates; Checks, Incidents, Abhängigkeitskarte. |
| [Observability-Read-Model](/de/reference/modules/observability/) | — | Das Read-Model der Engine über sich selbst: fixierte Interop-Standards, W3C-korrelierte Ledger-/Trace-Sicht, Lieferketten-Attestation. |
| [Claude-Code-Adoption](/de/reference/modules/claudeadoption/) | — | Read-Model der Claude-Code-Adoption/-Produktivität: Sessions, Lines of Code, Commits, PRs, Tool-Accept-Reject, Tokens pro Modell, nach Team/Entwickler/Tag; per Team als Standard, Drill-down pro Entwickler opt-in. Nur-Claude-API-Grenze; trägt nie Kosten. |
| [Live-ingest](/de/reference/modules/live-ingest/) | PARTIAL | In-Process-Produzent detektivischer Events, die ein Connector nicht emittieren kann; env-gegatet, deny-closed, minimal-data. |

### Govern & enforce

| Modul | Actuate | Zweck |
|---|---|---|
| [Identität, Berechtigungen & Governance](/de/reference/modules/vi-governance/) | — | Wer und was was tun darf, granular: Cedar RBAC + Deny-Overlay + scoped Grants, Roster-Abgleich, scoped Admin-/Custom-Rollen, Break-Glass, Kill-Switch. |
| [Source- & Credential-Scoping](/de/reference/modules/sourcescope/) | — | Quellen an einen Workspace/eine Agent-Gruppe binden; deny-closed scoped Resolver + scoped Credentials zum Auflösungszeitpunkt. |
| [Deployment & Integration](/de/reference/modules/vii-deploy/) | on-demand (503) | Deployments auf reale Infrastruktur planen und regeln; der Executor ist on-demand — live `apply`/`retire` liefern `503`, bis bereitgestellt. |

> **Identität & Zugriff** lebt innerhalb der [Governance](/de/reference/modules/vi-governance/) —
> es gibt kein separates Modul. NHI-Lifecycle, Agent-Identity-Federation,
> AAL3-Step-up und SSO/SCIM sind Governance-Capabilities.

### Claude- & Agenten-Ökosystem

| Modul | Actuate | Zweck |
|---|---|---|
| [Modell- & Provider-Verwaltung](/de/reference/modules/x-models/) | on-demand (503) | Über den gesamten Modell-/Provider-Stack regeln: Model-Access, Kontextfenster pro Oberfläche, Model-Group-Gate; die Modell-*Ausführung* ist on-demand — `503`, bis ein Inferenz-Credential bereitgestellt ist. |
| [Inline-Inferenz-Proxy](/de/reference/modules/inferenceproxy/) | PARTIAL | Inferenz-Egress-Konfiguration pro Tenant + DLP für den Inline-`/v1/messages`-PEP-Proxy; die Modulkonfiguration ist live, der Listener ist opt-in, loopback-default, fail-CLOSED. |
| [Interner Katalog & Marketplace](/de/reference/modules/xiv-catalog/) | — | Kuratierter Marketplace genehmigter/signierter Agenten, MCP-Server und Skills. |
| [Voice- & Realtime-Agenten](/de/reference/modules/xvi-voice/) | on-demand | Konversations-/Realtime-Agenten beobachten und regeln (default-DENY, zweiphasiges HITL); öffnet nie einen Media-Stream; Dispatch on-demand. |

### Sicherheit & Datenschutz

| Modul | Actuate | Zweck |
|---|---|---|
| [Sicherheit, Guardrails & Audit](/de/reference/modules/ix-security/) | live | Guardrails (PII/Injection/Jailbreak), Anomalien, Incident-Timelines; BYOK/DLP/RTBF/Retention/WORM/Residency leben in dieser Ebene. |
| [Aufzeichnung privilegierter Sessions](/de/reference/modules/recording/) | live | PAM-konforme Aufzeichnung privilegierter Sessions: hash-verkettete Frames, Maskierung beim Schreiben, ledger-verankert. |
| [Daten, Wissen & Kontext](/de/reference/modules/viii-knowledge/) | on-demand | Geregelte Datenebene: KBs + RAG, geregeltes Retrieval, Lineage, Prompt-Registry, Agent-Memory; modellgestützte semantische Embeddings sind on-demand. |

### Compliance & Evidenz

| Modul | Actuate | Zweck |
|---|---|---|
| [Compliance & Regulatorik](/de/reference/modules/xiii-compliance/) | — | 26 Framework-Kataloge + versiegelte, ledger-abgeleitete Evidenz mit Live-Chain-Verify. |
| [SIEM/ITSM-Forwarder](/de/reference/modules/siemforward/) | live | Versendet das versiegelte Ledger + Findings an SIEM-Towers (OCSF 1.8/CEF/LEEF/syslog/OTLP), leader-gegateter Cursor-Walk, at-least-once. |
| [Posture-Export](/de/reference/modules/posture-export/) | PARTIAL | Schreibgeschützter Posture-/Inventar-Pull für Control-Towers (neutrales JSON); beansprucht **keinen** verifizierten Downstream-Push. |
| [Reporting](/de/reference/modules/reporting/) | — | Professionelle PDF-/HTML-Berichte aus den Compliance-, Audit- und FinOps-Daten der Plattform — fünf integrierte Berichtstypen; Auditoren laden ein Dokument herunter, statt JSON zu kopieren. |

### FinOps

| Modul | Actuate | Zweck |
|---|---|---|
| [Cost & AI-FinOps](/de/reference/modules/xi-finops/) | live | Agierende Budgets, die am Cap verweigern/drosseln, Cost-per-Outcome, Cancellation-Risk; Budget fest an die Identität gebunden. |

### Evals & Safety

| Modul | Actuate | Zweck |
|---|---|---|
| [Qualität, Evals & Testing](/de/reference/modules/xii-evals/) | — | Kalibrierter LLM-Judge + ein blockierendes CI-Regressions-Gate; Offline-Judge → SKIPPED, nie ein stiller Pass. |
| [Agent-Sandbox](/de/reference/modules/xvii-sandbox/) | on-demand | Sichere Umgebung zum Testen von Agenten vor der Produktion; echte OS-Isolation (gVisor/Firecracker) ist on-demand. |
| [Red-Teaming & Adversarial Testing](/de/reference/modules/xviii-redteam/) | on-demand | Consent-gegatete Adversarial-Batterie; DEGRADED — nie ein falscher Pass — bis eine Sandbox-Runtime bereitgestellt ist. |

### Plattform & Integrationen

| Modul | Actuate | Zweck |
|---|---|---|
| [Output-Integrationen & Benachrichtigungen](/de/reference/modules/xv-notify/) | live | Benachrichtigungs-Router zu den Systemen, die das Unternehmen bereits betreibt; Dispatch ist live verdrahtet, Ziele vom Operator bereitgestellt. |
| [Eventing](/de/reference/modules/eventing/) | live | Externe Subscription-Oberfläche über den Bus: typisierte Subscriptions, durable At-least-once-Delivery, Retry/Backoff, DLQ, Cursor-Replay. |
| [Gespeicherte Konsolenansichten](/de/reference/modules/consoleviews/) | — | Benannte, teilbare Snapshots des Konsolen-View-Zustands (Filter, Zeiträume), serverseitig pro Mandant gespeichert: eine Untersuchung speichern, mit dem Team teilen. Akzeptiert ein größenbegrenztes JSON-Objekt (4096 Bytes) für View-Parameter — keine sensiblen Daten oder Abfrageergebnisse darin speichern. Erstellen/Aktualisieren nur durch den Owner; Mandanten-Admins/-Owner und Superadmins können zum Aufräumen löschen; jede Mutation wird auditiert. |

Spalte **Actuate**: `live` = Actuation ist verdrahtet und live im Standard-Binary,
keine Bereitstellung nötig (z. B. die FinOps-Budgetdurchsetzung verweigert am
Cap, der Benachrichtigungs-Router dispatcht); `on-demand` / `on-demand (503)` =
das Backend ist gebaut und an einen Injektionspunkt verdrahtet, bleibt aber
**deny-closed oder degradiert, bis ein Operator es bereitstellt** per
Env-Konfiguration (deploy antwortet mit `503`, bis ein Executor existiert;
Orchestrierungs-/Voice-Dispatch ist deny-closed, bis konfiguriert; Red-Team läuft
DEGRADED, bis eine Sandbox-Runtime bereitgestellt ist; Modell-Ausführung und
semantische Embeddings liefern `503`, bis ein Credential bereitgestellt ist);
`PARTIAL` = die Oberfläche ist real, aber gegatet/opt-in oder beansprucht keinen
verifizierten Downstream (der Inferenz-Proxy-Listener ist opt-in und
loopback-default; live-ingest ist env-gegatet; posture-export ist eine neutrale
schreibgeschützte Projektion); `—` = das Modul regelt/beobachtet von Natur aus
und hat keine Actuation-Oberfläche. Dieser Split ist der ehrliche Vertrag: das
Produkt **beobachtet und regelt heute breit und agiert auf einer wachsenden,
überwiegend bereitstellungs-gegateten Teilmenge** — siehe
[Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/). Der Katalog ist aus dem
Composition-Root abgeleitet (`cmd/olivares/wire.go`): alle 30 Module werden dort
konstruiert und via `rt.AddModule` registriert (verifiziert am 2026-08-01,
main @ f632f03f).

## Plattform- & Core-Capabilities (nicht zu den 30 Modulen gezählt)

Dies sind reale, ausgelieferte Capabilities, aber es sind
**Engine-/Core-/Web-Capabilities**, keine Module aus dem `modules/`-Set — daher
werden sie nicht zu den 30 gezählt:

- [Eigene API + Manage-as-Code](/de/reference/modules/xix-api-manage-as-code/) —
  **Engine-/Core-Capability.** Die eigene versionierte REST-/gRPC-API der Engine
  plus der Terraform-Provider; verwalten Sie die Plattform selbst per API und
  IaC.
- [Mandantenfähigkeit & Org-Verwaltung](/de/reference/modules/xx-multi-tenancy/) —
  **Engine-/Core-Capability.** Org-Hierarchie und delegierte Administration, mit
  Postgres-Row-Level-Security-Mandantentrennung.
- [Executive-Dashboards](/de/reference/modules/xxi-executive-dashboards/) —
  **Web-Capability.** Leadership-Konsolenansichten neben der technischen UI.
  (Das Backend für die Berichtserzeugung ist das Modul
  [Reporting](/de/reference/modules/reporting/), das zu den 30 gezählt wird.)
- [Modellbetrieb (eigene Modelle)](/de/reference/modules/xxiii-model-operations/) —
  **Capability des Models-Moduls** (über die Zeile von Modul X gezählt, keine
  eigene Zeile): die governte Registry eigener Modelle, Admission signierter
  Modelle, Lineage-Datensätze für Datasets/Fine-Tuning-Jobs, Governance lokaler
  Inferenz-Deployments und AIBOM-/Model-Card-Belege.

**Geplant:** die **Ausführung** von Eigenmodell-Fine-Tuning und lokaler Inferenz
([xxiii-fine-tuning](/de/reference/modules/xxiii-fine-tuning/)) — die Plattform
governt und erfasst diese Arbeit heute (siehe Modellbetrieb oben), führt aber
selbst kein Training aus und bedient keine Inferenz; die ausführende Hälfte ist
dokumentierte **geplante** Arbeit, **nicht ausgeliefert** und keines der 30.

## Wie Module in der API und im Bus erscheinen

- **REST.** Die [API-Referenz](/reference/api/) rendert die stabile Core-REST-Oberfläche
  aus dem OpenAPI-3.1-Vertrag des Produkts. Die Modulrouten (`/v1/m/<ns>/…`) werden
  separat als **Beta**-Dokument veröffentlicht — in der
  [Modulrouten-Referenz](/reference/api-beta/); ihre feldgenauen Verträge leben in den
  typisierten Schnittstellen des Produkts.
- **Events.** Module reagieren auf den [Event-Bus](/de/reference/events/): die
  access map konsumiert `edge.observed`, FinOps konsumiert `cost.sampled`, und
  Security konsumiert `finding.reported` und `guardrail.observed`.

## Schichten

Die 30 Module bauen auf Schichten über der Engine auf, neben den oben genannten
Engine-/Core- und Web-Capabilities:

- **Engine (Schicht 0)** — die Capabilities Eigene-API/Manage-as-Code und
  Mandantenfähigkeit (Core, nicht zu den 30 gezählt).
- **Core (Schicht 1)** — inventory, sessions, access-map, models, health,
  observability.
- **Management (Schicht 2)** — capabilities, governance, sourcescope, deploy,
  knowledge.
- **Intelligence (Schicht 3)** — orchestration, security, recording, inference
  proxy, finops, evals, compliance, reporting, siemforward, posture-export, catalog, notify,
  eventing, voice, sandbox, redteam, live-ingest, consoleviews.
- **Web (Schicht 4)** — die UI und die Executive-Dashboards-Capability.

Siehe die [Architektur-Übersicht](/de/explanation/architecture/overview/) dazu,
wie die Engine und diese Schichten zusammengesetzt werden.
