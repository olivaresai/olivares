---
title: Abonnement-authentifizierte Claude Code & Codex regeln
description: >-
  Wie Olivares AI Coding-Agenten regelt, die sich mit einem Abonnement
  authentifizieren — Claude Code auf Pro/Max, Codex auf ChatGPT — ohne jemals
  inmitten dieses Abonnements zu sitzen. Drei Mechanismen (observe, Managed
  Settings + Hooks, ein API-Key-Gateway), eine rote Linie: Wir routen Ihre
  Abonnement-Anmeldedaten niemals.
sidebar:
  order: 6
---

Der am schwersten zu regelnde Agent ist der, bei dem sich ein Entwickler mit einem
persönlichen oder firmeneigenen **Abonnement** angemeldet hat: Claude Code, eingeloggt
mit Pro/Max, oder Codex, eingeloggt mit ChatGPT. Dasselbe Muster gilt für Grok Build und
jeden CLI-Agenten, der die Person statt des Workloads authentifiziert — bei den folgenden
Mechanismen geht es um die *Form* dieser Anmeldung, nicht um einen bestimmten Anbieter. Er
läuft auf einem Laptop, er
authentifiziert sich mit einem OAuth-Credential, und er ist genau die Oberfläche, die
ein Guardrail eines Cloud-Providers im Inferenzpfad niemals sieht (siehe
[den Wedge](/de/explanation/positioning/where-olivares-fits-vs-your-gateway/)). Die
verlockende „Lösung“ — einen Dienst davorzuschalten, der das Abonnement hält und dessen
Traffic routet — ist eine, die Olivares AI **nicht** bauen wird, weil die Modellanbieter
sie untersagen und weil sie unsere Control Plane zu einem Single Point of
Credential-Kompromittierung machen würde.

Diese Seite ist die ehrliche Darstellung, wie wir diese Agenten regeln, **ohne jemals das
Abonnement zu vermitteln**: was wir beobachten, wo wir durchsetzen, und der eine enge
Pfad, auf dem ein Gateway angemessen ist (und es ist nie das des Abonnements).

:::danger[Die rote Linie: Wir routen Ihr Abonnement niemals]
Olivares AI **hält, proxyt oder routet niemals das Abonnement-Credential eines Dritten.**
Anthropics eigene Richtlinie besagt: *"Anthropic does not permit third-party developers
to offer Claude.ai login or to route requests through Free, Pro, or Max plan credentials
on behalf of their users"*
([Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance),
abgerufen am 2026-06-21 — das Verbot benennt die drei Consumer-Pläne **Free, Pro, Max**).
OpenAIs Bedingungen funktionieren genauso für einen Consumer-Login mit ChatGPT/Codex.
Unsere Haltung ist strenger als die Linie selbst: Wir routen **kein** Abonnement-OAuth,
welchen Plans auch immer. Governance geschieht *rund um* den Agenten, niemals *innerhalb*
seines Credentials.
:::

## Warum die Vermittlung des Abonnements vom Tisch ist

Es lohnt sich, bei der Regel präzise zu sein, denn die Rechtsabteilung eines Käufers wird
sie prüfen. Anthropics Richtlinie zieht zwei Listen, die nicht vermischt werden dürfen:

- **Wer OAuth überhaupt nutzen darf** — fünf Pläne: *"OAuth authentication is intended
  exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise
  subscription plans and is designed to support ordinary use of Claude Code and
  other native Anthropic applications."*
- **Was ein Dritter nicht tun darf** — im Namen von Nutzern routen: *"Anthropic does
  not permit third-party developers to offer Claude.ai login or to route requests
  through Free, Pro, or Max plan credentials on behalf of their users."*

Das Verbot benennt ausdrücklich die **Consumer**-Pläne (Free, Pro, Max). Die Seite
gewährt umgekehrt niemandem die Erlaubnis, Team- oder Enterprise-Seats zu routen — dazu
schweigt sie, und wir lesen Schweigen nicht als Lizenz. Für *Entwickler, die Tooling
bauen*, weist Anthropics eigene Leitlinie ganz von Abonnement-OAuth weg: *"Developers
building products or services that interact with Claude's capabilities, including those
using the Agent SDK, should use API key authentication through Claude Console or a
supported cloud provider."*
([Quelle](https://code.claude.com/docs/en/legal-and-compliance);
Plan-nach-Bedingungen-Aufteilung: Team/Enterprise/API unter Commercial Terms,
Free/Pro/Max unter Consumer Terms.)

Unser Codex-Connector kodiert dieselbe Disziplin im Code, by design: das
Automatisierungs-Credential ist ein OpenAI-**API-Key** oder ein **Workspace-Access-Token**,
niemals ein persönliches ChatGPT-Abonnement — *"proxying it for third-party/programmatic
use violates OpenAI's terms exactly as a consumer Claude subscription does for
Anthropic. There is no subscription config field by design"*
(`connectors/codex/codex.go`). Die rote Linie ist also kein nachträglich angeschraubtes
Marketing-Versprechen; sie ist die Form des Produkts.

## Drei Mechanismen, keiner davon das Abonnement

Wir regeln einen abonnement-authentifizierten Agenten über drei unabhängige Kanäle. Die
ersten beiden berühren Inferenz überhaupt nicht; der dritte berührt sie nur für Traffic,
der sich mit einem **API-Key** authentifiziert, niemals mit einem Abonnement.

### 1. Observe — Telemetrie, Nutzung und Posture

Claude Code emittiert OpenTelemetry, und ein Administrator kann es für die Flotte aus dem
Managed Tier aktivieren: *"Administrators can configure OpenTelemetry settings
for all users through the managed settings file"*
([Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)). Wir
ingestieren dieses **Gen-AI-Signal** — Sessions, Tokens, Kosten, Tool-Aktivität — und
verwandeln es in die Access Map und in Posture-Findings. Entscheidend ist, dass dies
**auch auf der Seite von Claude Code minimal-data by construction** ist: Prompt-Inhalt ist
*"redacted by default"* und Tool-Details, Tool-Inhalt sowie rohe API-Bodies sind jeweils
*"(default: disabled)"* (dieselbe Quelle). Wir konsumieren Nutzung und Metadaten, keine
Konversationen.

Für Codex ist derselbe Observe-Kanal das Ingest des Connectors für die Analytics- und
Compliance/Audit-APIs — Nutzung, Adoption und unveränderliche Audit-Datensätze, verwandelt
in Cost Samples und manipulationserkennbare Evidenz, die *"never prompt/diff
content or key values"* trägt (`connectors/codex/codex.go`).

→ [OpenTelemetry GenAI ingestieren](/de/how-to/connectors/otel-genai/) ·
[Enterprise-OTel für Claude Code](/de/how-to/claude-code-enterprise-otel/)

### 2. Managed Settings + Hooks — der In-Process-PEP

Beobachtung ist keine Durchsetzung. Der Durchsetzungskanal für Claude Code ist seine
**Managed-Settings**-Datei auf dem OS-Policy-Tier, die einen nicht überschreibbaren
`PreToolUse`-Hook trägt, der vor jedem Tool-Lauf an den Olivares-Entscheidungspunkt
zurückruft. Anthropic dokumentiert die Eigenschaft, auf die wir uns stützen: *"Environment
variables defined in the managed settings file have high precedence and cannot be
overridden by users"*, und Managed Settings *"can be distributed via MDM"*
([monitoring](https://code.claude.com/docs/en/monitoring-usage)).

Olivares rendert diese Datei (`olivares agent managed-settings`) mit
`allowManagedHooksOnly`, sodass der eigene Hook eines Entwicklers dem geregelten niemals
vorausgehen oder ihn untergraben kann, und der Per-Session-Endpunkt sowie das Bearer
werden beim Start injiziert — nicht in die statische Datei geschrieben. Die Entscheidung
selbst ist **an jeder Kante deny-closed**: Ein Tool-Call wird nur dann erlaubt, wenn eine
feste Identität aufgelöst wird, die Policy-Disposition nicht `deny` lautet, die Live-Policy-
Engine ihn nicht verbietet und — bei einem `ask` — eine menschliche Freigabe an den exakten
Plan-Hash gebunden ist. Ein Notaus
([Kill Switch](/de/reference/glossary/#kill-switch)) übertrumpft alles, einschließlich eines
aktiven Break-Glass-Grants.

Dies ist der Mechanismus, den die Seite
[Claude Code Hooks PEP](/de/how-to/connectors/claude-code-hooks-pep/) operativ dokumentiert,
und es ist das, was uns in die Lage versetzt, den lokalen Dev-Agenten zu *regeln*, nicht
ihn bloß zu beobachten — die zweite der
[drei Bahnen](/de/explanation/positioning/analyst-vocabulary/#die-drei-bahnen-auf-die-dieses-vokabular-zeigt).

### 3. Gateway für einen API-Key — niemals für OAuth

Es gibt genau einen Pfad, auf dem Olivares in der Inferenz-Anfragenkette sitzt, und er
existiert nur für Aufrufer, die den Managed-Settings-Kanal von Claude Code **nicht**
nutzen: roher SDK- oder `curl`-Traffic, authentifiziert mit einem **API-Key** (oder einem
Bedrock/Vertex-Äquivalent). Claude Code routet solche Anfragen mit
`ANTHROPIC_BASE_URL` — *"To route requests through a custom API endpoint, set the
`ANTHROPIC_BASE_URL` environment variable instead"* — und authentifiziert ein Gateway mit
einem Bearer über `ANTHROPIC_AUTH_TOKEN`, *"when routing through an LLM gateway or proxy
that authenticates with bearer tokens rather than Anthropic API keys"*
([Claude Code IAM](https://code.claude.com/docs/en/iam)). Auf den Olivares
Inline-Inferenz-Proxy gerichtet, erhält dieser Traffic eine geregelte Pipeline —
Residency, Modellzugriff, Context-Window, DLP, Budget, Recording — bevor er weitergeleitet
wird.

Die Grenze ist absolut: **Dieser Pfad trägt API-Key-/Bearer-Traffic, niemals das
OAuth-Credential eines Abonnements.** Er ist die Durchsetzungsnaht für die SDK-/`curl`-
Aufrufer, die Managed Settings nicht erreichen können, und nichts weiter.

## Die Ehrlichkeitsbox: verified-deployed, nicht unbypassable

:::caution[Durchsetzung, von der wir beweisen können, dass sie *deployt* ist, nicht Durchsetzung, die *nicht* umgangen werden kann]
Der Managed-Settings + Hook-PEP ist **deny-closed** und **vom Nutzer nicht über Settings
überschreibbar** — aber er ist keine Magie. Ein Entwickler, der
`ANTHROPIC_BASE_URL` auf seinen eigenen Endpunkt richtet, schickt Inferenz ganz woanders
hin; unsere eigene Engineering-Notiz sagt das unverblümt: *"a custom
`ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
(`modules/inferenceproxy/doc.go`). Wir behaupten also nie, der PEP sei unmöglich zu
entkommen. Stattdessen behaupten wir zwei Dinge, für die wir geradestehen können:

1. **Er ist verified-deployed.** Olivares attestiert, dass die Managed Settings und
   der PEP-Hook tatsächlich auf dem Host vorhanden sind — ein nicht provisionierter Host
   läuft ungoverned-but-observed, und das ist sichtbar, nicht verborgen.
2. **Die Umgehung ist selbst ein Finding.** Eine nicht-default `ANTHROPIC_BASE_URL` auf
   einem Host taucht als Posture-Finding auf, und eine Managed-Umgebung, die eine Base-URL
   pinnt, die vom autorisierten Olivares-Gateway abweicht, löst ein **Drift**-Finding aus
   (`connectors/claude-config`, `connectors/managedsettings`). Die Umgehung verstummt
   nicht; sie leuchtet auf.

„Verified-deployed, Umgehung-als-Finding“ ist die ehrliche Durchsetzungsgeschichte für
jeden Agenten, der auf einer Maschine läuft, die der Entwickler kontrolliert. Wir
verkaufen Ihnen nicht „unbypassable“.
:::

## Die Codex-Asymmetrie, ehrlich benannt

Claude Code und Codex sind nicht symmetrisch, und der Unterschied zählt. Für Codex,
authentifiziert durch ChatGPT, gibt es **kein dokumentiertes Äquivalent zu
`ANTHROPIC_BASE_URL`** — OpenAIs
[Managed-Configuration-Seite](https://developers.openai.com/codex/enterprise/managed-configuration)
dokumentiert keine Einstellung und keine Umgebungsvariable, um Inferenz über eine
benutzerdefinierte Base-URL oder ein Gateway zu routen (per Fetch verifiziert, 2026-06-21;
eine Abwesenheit auf jener Seite, kein Beweis, dass es anderswo keine gibt). Daher regeln
wir Codex **nicht**, indem wir seine Inferenz abfangen.

Stattdessen regeln wir es dort, wo OpenAI Administratoren *tatsächlich* durchgesetzte
Kontrollen gibt. Codex Managed Configuration erlaubt einem Unternehmen, *"Requirements:
admin-enforced constraints that users can't override"* zu setzen, die *"constrain
security-sensitive settings (approval policy, approvers reviewer, automatic review policy,
sandbox mode, permission profiles, web search mode, managed hooks, and optionally which
MCP servers users can enable)"* (dieselbe Quelle). Olivares verfasst und attestiert diese
Requirements (`connectors/codex-managed-config`) — Approval-Policy, Sandbox-Modus,
die MCP-Allowlist, bereinigte Telemetrie (`log_user_prompt = false`) — und ingestiert
Codex' Analytics- und Compliance-Evidenz. Governance durch Konfiguration und Evidenz,
nicht durch einen Man-in-the-Middle beim Modellaufruf.

## In einer Tabelle

| Kanal | Was er tut | Berührt Inferenz? | Das Credential |
|---|---|---|---|
| **Observe** | Nutzung, Kosten, Tool-Aktivität → Access Map + Posture; Codex Analytics/Compliance → Ledger | Nein | Keines — nur Telemetrie, Inhalt standardmäßig maskiert |
| **Managed Settings + Hooks** | Deny-closed `PreToolUse`-PEP auf Claude Code, nicht über Settings überschreibbar | Nein | Das des Agenten selbst; wir sehen es nie |
| **Gateway (nur API-Key)** | Geregelte Pipeline für rohe SDK-/`curl`-Aufrufer über `ANTHROPIC_BASE_URL` | Ja | **API-Key / Bearer — niemals Abonnement-OAuth** |
| **Codex Managed-Config** | Admin-durchgesetzte Requirements (Approval/Sandbox/MCP) + Evidenz-Ingest | Nein | Das der Organisation; Konfiguration, kein Abfangen |

## Verwandt

- [Wo Olivares vs. Ihr Gateway / Guardrails passt](/de/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — warum nichts davon mit Ihrem AI-Gateway konkurriert.
- [Olivares AI vs WitnessAI](/de/explanation/positioning/vs-witnessai/) — das
  Head-to-Head zur Regelung von Agenten in IDEs.
- [Claude Code Hooks & der PEP](/de/how-to/connectors/claude-code-hooks-pep/) und
  [Claude Code mit Olivares betreiben](/de/how-to/run-claude-code-with-olivares/) — das
  operative How-to.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die ständige Selbstverpflichtung,
  unter der diese Seite geschrieben ist.
