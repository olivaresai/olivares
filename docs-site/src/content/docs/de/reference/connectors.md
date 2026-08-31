---
title: Connector-Katalog & Coverage-Stufen
description: >-
  Die First-Party-Connectors, die die Control Plane heute verdrahten kann,
  gruppiert nach der ehrlichen Coverage-Stufe, die jeder unterstützt — clean,
  lossy, impossible-passively, cooperative und approximate-by-attribution —
  sowie die Ausgabeziele.
---

Diese Seite ist der **Katalog** der First-Party-Connectors und nennt für jeden die **ehrliche
Coverage-Stufe**, die er unterstützen kann. Sie ist der Begleiter zu
[Eine Quelle anbinden](/de/how-to/connect-a-source/), die das Connector-*Modell* erklärt
(observe-only, minimal-data, die drei Beobachtungsarten) — lies das zuerst. Diese Seite
beantwortet die nächste Frage: *Welche Quellen existieren, und wie gut ist das Signal jeder einzelnen?*

Coverage ist **gestuft nach dem, was die Audit-Oberfläche eines Systems dir ehrlich sagen kann**, niemals
danach, wie sehr wir uns wünschten, es könnte mehr. Die Stufen, wie sie überall in den Docs verwendet werden:

- **Cooperative** — ein Agent oder eine Plattform, die berichtet, was sie getan hat (OpenTelemetry, eine
  Vendor-Admin-API). Höchste Genauigkeit *wenn vorhanden*; hängt von der Kooperation der Quelle ab.
- **Clean** — ein Store, der read vs. write **nativ** klassifiziert, wortgetreu übernommen aus
  seinem eigenen Audit-Trail (SQL-Audit, Object-Store-/Warehouse-Datenzugriffslogs).
- **Lossy** — ein Store, dessen Audit read nicht sauber von write oder Aufrufer von
  Aufrufer trennen kann (Document Stores, Lineage). Kanten landen, aber oft `approximate`.
- **Impossible passively** — ein System ohne nutzbare passive Audit-Oberfläche (In-Memory-
  Caches, eingebettete Einzeldatei-Datenbanken). Es gibt kein ehrliches read-first-Signal; das
  Produkt tut nicht so, als wäre es anders.
- **Approximate-by-attribution** — der Zugriff ist real, aber die Attribution erfolgt auf eine Rolle,
  einen Prozess oder eine geteilte Credential, nicht auf einen aufgelösten Agenten, daher ist die Kante `approximate`.
- **Untrusted hint** — eine deklarierte Fähigkeit (eine MCP-Tool-Annotation), bestätigt,
  niemals allein vertraut.

:::caution[Was dieser Katalog widerspiegelt: in den aktuellen Build verdrahtete Connectors]
Dies listet Connectors **auf, die heute im Connector-Set des Standard-Binarys registriert sind** —
also Kinds, die du in `OLIVARES_SOURCES_CONFIG` benennen und von der Engine verdrahten lassen kannst. Das
Produkt ist pre-1.0. Die kanonischen R/RW-Access-Map-Connectors — **pgAudit**,
**S3/CloudTrail**, der **eBPF/Tetragon**-Backstop, das **runtime**-Inventar und die **MCP**-
Introspektion — sowie die **Knowledge-Document-Quellen** sind jetzt verdrahtet und konfigurierbar
in einem Standard-`serve`; einige bringen **Deployment-Anforderungen** mit (ein Tetragon-Sensor, Host-
Zugriff), behandelt in [Deployment-Anforderungen](#deployment-anforderungen-und-ehrliche-attribution)
weiter unten. Coverage ist **ehrlich gestuft**: die Anwesenheit eines Connectors hier ist keine Behauptung
einer festen Per-Agent-Attribution, die die harte Abhängigkeit bleibt (ein geteiltes Konto lässt
selbst einen Clean-Tier-Store auf `approximate` zusammenbrechen).
:::

## Cooperative — Claude & Vendor-Telemetrie

Die Quellen mit der höchsten Genauigkeit, wenn vorhanden. Die Claude-Code-Runtime-Quelle läuft
**out-of-process** als eingebettetes Plugin (ein einfacher Dev-Build lässt sie weg und der Boot warnt
ehrlich, statt gesund zu erscheinen).

| Kind | Beobachtet | Hinweise |
|---|---|---|
| `claude` | Claude Code OTLP Tool-Telemetrie + MCP-Introspektion → Kanten / Kosten / Findings | Out-of-process-Plugin; `attributed` wenn eine Per-Agent-Identität vorhanden ist, sonst `approximate` |
| `claude-api` | Claude Admin-API Kostenproben + Governance-Posture-Findings | In-process; ein No-op offline (kein Admin-Key) |
| `claude-compliance` | Claude Compliance Activity-Feed-Evidence → Findings | GET-only per Konstruktion; No-op offline |
| `claude-config` | Statischer Claude-Config-Baum (Subagents / Skills / Plugins) → **declared-capability**-Kanten | Nur Metadaten — eine Capability-Oberfläche, kein beobachteter Zugriff |
| `claude-console` | Claude-Org-IAM → SSO/SCIM-Posture-Findings (Identitäts-Roster + Quelle) | |
| `claude-wif` | Anthropic Non-Human-Identity / Workload-Identity-Roster + permitted-scope-Kanten | Modelliert vom Operator deklarierte Föderation; markiert Static-Key-Footguns |
| `claude-managed-agents` | Inventar verwalteter Claude-Agents + Thread-Events (Webhook-Empfänger + GET-Poller) | Streaming-Quelle (`poll_seconds: 0`); offline ein No-op |
| `claude-projects` | Inventar von Claude Organization Projects (Mitgliedschaft / API-Keys) + vom Operator deklarierte Projekt-Policy | Read-only Admin API; offline ein No-op |
| `claude-apps-gateway` | Claude-Apps-Gateway-Posture, deklarierte Modell-Grants und Audit-Event-Ingest → Topologie + Findings | Liest eine vorhandene `gateway.yaml` und optionalen JSONL-Audit-Export |
| `claude-batch` | Inventar von Anthropic Message Batches + Files API, Durchsetzung der Batch-Policy, Ablauf der Upload-Aufbewahrung | Liest niemals Payloads oder Dateiinhalte; ohne Admin-Key ein ehrliches Offline-Finding |
| `claude-routines` | Inventar von Claude Code Routines (geplante Trigger) → Kanten + Cadence-/Review-Findings | Nur GET; Prompt-Inhalt wird nur gehasht; Streaming (`poll_seconds: 0`) |
| `cowork` | Claude-Cowork-OTLP/HTTP-Log-Empfänger → Aktivitätsnachweise | Out-of-process-Plugin (Isolation der OTel-Proto-Abhängigkeit) |
| `cowork-analytics` | Claude-Cowork-Engagement-Analysen | In-process (nur Modelprovider-Client) |
| `codex` | OpenAI-Codex-Kostenproben, Nutzungs-/Auth-/Admin-Audit-Nachweise, Adoption-Findings | Read-only Admin API; vertrieblich beschränkte Oberflächen degradieren zu einem Posture-Finding |
| `cursor` | Von der Cursor Admin API abgerechnete Kosten, Team-Audit-Logs, Mitgliederinventar, Budget-Posture | Tarifbedingte 403/404 degradieren zu einem Finding und schlagen niemals fehl |

### Vendor-neutrales GenAI-Framework-Profil (`gen_ai.*`) — opt-in

Die Agent-Frameworks, die der Katalog verspricht — **LangGraph / LangChain, CrewAI,
AutoGen / Microsoft Agent Framework, Google ADK** (und das OpenAI SDK, LlamaIndex,
Pydantic-AI, Strands, …) — emittieren **nicht** Claudes `claude_code.*`-Schema. Sie
konvergieren auf die [OpenTelemetry **GenAI** Semantic Conventions](https://github.com/open-telemetry/semantic-conventions-genai)
(`gen_ai.*`). Dieselbe `claude`-Quelle nimmt auch dieses Profil auf, sodass eine OTel-instrumentierte
Flotte die **Access Map** und **FinOps** durch einen Ingest speist statt durch einen maßgeschneiderten
Connector pro Framework — die Integration mit der höchsten Hebelwirkung.

**Dieses Profil ist OPT-IN und ehrlich als experimentell gekennzeichnet.** Der gesamte `gen_ai`-
Bereich hat OpenTelemetry-Status **Development** (nicht Stable, Jun-2026), sodass er sich nur
aktiviert, wenn du das Gate der Spezifikation selbst spiegelst. Setze `semconv_opt_in` des Connectors auf eine
kommagetrennte Liste, die das Token `gen_ai_latest_experimental` enthält (spiegelt
`OTEL_SEMCONV_STABILITY_OPT_IN`). Standardmäßig aus, speist ein `gen_ai.*`-Signal weiterhin den
Silence-Watchdog, mappt aber keine Kante/keine Kosten — wir behaupten niemals eine Stabilität, die die Conventions
nicht haben.

Da die Conventions sich mitten im Umbruch befinden, ist der Ingest **dual-name** (er liest den
aktuellen Key *und* den deprecateten Vorgänger, der in freier Wildbahn noch emittiert wird) und
**multi-signal** (er mappt Trace-**Spans**, das `gen_ai.client.inference.operation.details`
Log-**Event** und erkennt die Client-**Metrics**):

| Was er liest | Aktueller Key | Auch akzeptiert (deprecated, noch emittiert von) |
|---|---|---|
| Provider | `gen_ai.provider.name` | `gen_ai.system` (v1.36.0-oder-früher Standard; **Google ADK**, z. B. `gcp.gemini`) |
| Input-Tokens | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens` (**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI) |
| Output-Tokens | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens` (dasselbe) |

| gen_ai-Attribut | mappt auf | Konfidenz |
|---|---|---|
| `gen_ai.usage.*` (Tokens) | `CostSample` (Provenienz **estimated** — Tokens, nicht abgerechnete Kosten) | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | Kosten-Provider + Modell (response bevorzugt) | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | agent→tool **access edge** (Modus `unknown`) | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | conversation→agent **attribution edge** + Session-Ref | `attributed` |

#### Unterstützte Dialekt-Matrix (Multi-Generation-Normalizer)

Die GenAI-Conventions änderten sich in **drei Generationen, die koexistieren** in realen 2026-er
Flotten. Der Ingest erkennt die Generation **pro Signal** anhand generationsexklusiver
Marker und stempelt das normalisierte Event mit dem entsprechenden semconv-Pin
(`genai.semconv` Posture-Finding zeichnet das aktive Set pro Run auf; ein Info-`drift`-
Finding pro Run markiert jeden gesehenen **deprecateten** Dialekt, sodass du weißt, welche Flotten
ihre Instrumentierung upgraden müssen). Nachrichten-**Inhalt wird niemals aus irgendeiner
Generation gelesen** — Content-Keys dienen nur als Dialekt-Marker (Minimal-Data-Posture).

| Erkannter Dialekt | Gestempelter Pin | Exklusive Marker (verifiziert) | Emittiert von (verifiziert Jun-2026) |
|---|---|---|---|
| Legacy **OpenLLMetry/Traceloop** (pre-semconv) | `openllmetry` | indizierte `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*`, `gen_ai.usage.prompt_tokens`/`completion_tokens`, `llm.usage.total_tokens`, `llm.request.type`, `llm.vendor`, `traceloop.span.kind` | Traceloop-instrumentiertes LangChain / LangGraph / CrewAI gepinnt **< openllmetry v0.55.0** (veröffentlicht 2026-03-29). Großgeschriebene Provider (`OpenAI`, `Langchain`) werden kleingeschrieben, damit FinOps nicht nach Groß-/Kleinschreibung splittet |
| **v1.36-oder-früher Events** (der eigene Name der Spezifikation) | `1.36.0` | `gen_ai.system`; die fünf Per-Message-Log-Events `gen_ai.{system,user,assistant,tool}.message`, `gen_ai.choice` (erkannt **nach Name** — ihr eines Attribut ist optional) | Google ADK LLM-Spans (`gcp.vertex.agent`), AutoGen (`autogen`), Microsoft Agent Framework — alle emittieren weiterhin `gen_ai.system` |
| **v1.37+ Messages** (aktuell) | `1.41.1` | `gen_ai.provider.name`, `gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`, das `gen_ai.client.inference.operation.details` Event, `gen_ai.workflow.name` | OTel-offizielle Instrumentierungen; openllmetry **≥ v0.55.0** |

Ein Signal, das nur Keys trägt, deren Namen über Generationen hinweg identisch sind (z. B. ein
ADK `invoke_agent`-Span: Operation + Agent + Conversation, gar kein Provider-Key)
wird unter dem aktuellen Pin normalisiert — das angewandte Mapping ist byte-identisch, und das
tatsächliche Release des Producers ist aus dem Wire nicht erkennbar.

#### MCP-Conventions (`mcp.*`, semconv v1.39 — Development)

Es existieren upstream genau vier `mcp.*`-Attribute (`mcp.method.name`,
`mcp.protocol.version`, `mcp.resource.uri`, `mcp.session.id`); das Tool reitet auf
`gen_ai.tool.name` und der Prompt auf `gen_ai.prompt.name`. Der Ingest verbindet diese
Traces mit den eigenen MCP-Governance-Fakten des Produkts, indem er dieselben Resource-
Kinds wiederverwendet, die der Claude-Pfad emittiert:

| MCP-Signal | mappt auf |
|---|---|
| jeder client-seitige `mcp.*`-Span mit `server.address` | session→`mcp.server`-Kante (verbindet sich mit den `claude_code.mcp_server_connection`-Kanten) |
| `tools/call` + `gen_ai.tool.name` | `mcp.tool` access edge (`server.address/tool` wenn der Endpoint bekannt ist) — dieselbe Art wie Claudes `mcp__server__tool`-Aufrufe |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | **read-mode** `mcp.resource`-Kante (URI bereinigt: Credentials/Query entfernt) |
| `prompts/get` + `gen_ai.prompt.name` | **read-mode** `mcp.prompt`-Kante (Prompt-Oberfläche) |
| SERVER-Kind-Spans / `mcp.client|server.*.duration`-Metrics | nur Liveness (saubere Degradierung — die Sicht des Servers attribuiert keine Agent-Identität) |

#### Agent-Spans (`invoke_agent` client/internal-Split + `invoke_workflow`, semconv v1.41 — Development)

v1.41.0 teilte `invoke_agent` in eine **CLIENT**-Variante (Remote-Agent-Service) und eine
**INTERNAL**-Variante (in-process). Reale Frameworks verletzen die Kind-Angabe heute (AutoGen und
Microsoft Agent Framework hardcoden CLIENT für In-Process-Agents; Google ADK verwendet
INTERNAL), sodass der Ingest einen Aufruf nur dann als **remote** klassifiziert, wenn der Span
CLIENT ist **und** eine `server.address` trägt — das ergibt eine conversation→`genai.agent.remote`-
Delegationskante. Alles andere bleibt ein In-Process-Aufruf, abgedeckt durch die
conversation→`genai.agent`-Attributionskante: sauber degradiert, niemals ein fabriziertes
„remote“. `invoke_workflow` (neu in v1.41; CrewAI-artige Crews) mappt eine
conversation→`genai.workflow`-Kante. Agent-Spans bleiben upstream **Development**
(experimentell) — es wird keine Stabilität behauptet.

**Stable vs. experimental, ehrlich:** der **Mechanismus** (Opt-in-Gate, Dialekt-
Erkennung + Dual-Name-Reads, Span-/Event-/Metric-Mapping, die versiegelten
`CostSample`-/`EdgeObservation`-Shapes) ist in diesem Produkt stabil. Das **Vokabular**,
das er mappt (`gen_ai.*`/`mcp.*`-Keys, das Operation-Enum), ist upstream **Development** und
könnte erneut umbenannt werden; genau deshalb normalisiert der Ingest jede Generation, statt
eine zu pinnen. v1.41.1 ist das letzte *versionierte* Release der gen-ai-Conventions
(sie zogen nach `open-telemetry/semantic-conventions-genai` um, das keine Releases
mit Stand Jun-2026 hat). Hinweise:

- **Kosten werden über die W3C-Span-ID dedupliziert.** Wenn eine Operation Usage auf *sowohl*
  ihrem Span als auch ihrem `operation.details`-Event meldet (sie teilen eine Span-ID), wird sie einmal
  berechnet, nicht zweimal.
- **Metrics speisen Liveness, niemals Kosten.** `gen_ai.client.token.usage` ist ein Aggregat;
  der Span/das Event ist die autoritative Per-Operation-Usage, sodass das Berechnen auch der Metric
  doppelt zählen würde. Die v1.39 `mcp.*`-Dauer-Histogramme werden genauso erkannt.
- **Provider kann `unknown` sein.** Wenn ein Span ein Modell, aber keinen Provider/kein System trägt, werden die
  Kosten `unknown` zugeordnet, statt aus der Model-ID geraten.
- **Eine reine Gesamt-Token-Zahl wird nicht aufgeteilt.** Legacy `llm.usage.total_tokens` ohne einen
  Prompt-/Completion-Split wird niemals in Input/Output geraten (keine fabrizierten Kosten).
- **OpenInference (Arize/Phoenix) ist eine andere Convention** und wird von
  diesem Profil *nicht* aufgenommen — die hier gelesenen `llm.*`-Keys (`llm.request.type`, `llm.usage.total_tokens`,
  `llm.vendor`) sind **OpenLLMetry-Legacy-Marker**, nicht OpenInferences `llm.*`-Namespace.

## Cooperative — Konfiguration lokaler Agent-Oberflächen

Diese Quellen lesen die deklarierte Konfiguration eines lokalen Agents und emittieren
**permitted**-Kanten sowie Posture-Findings. Sie sind keine Live-Ausführungstraces; wenn ein
Framework natives OTEL besitzt, kommt Live-Nutzung weiterhin über den obigen `gen_ai.*`-Ingest an.

| Kind | Beobachtet | Ehrliche Coverage |
|---|---|---|
| `opencode` | Lokale JSONC-Layer `opencode.json` / `opencode.jsonc` → Permission-Posture, Managed-/Admin-Override-Posture, permitted MCP-/Tool-/Custom-Agent-Kanten, Findings zu Credentials-in-Config/Sharing/Autoupdate/OTEL und ein Authoring-Fragment | Nur per Konfiguration deklariert. Der Managed-Layer wird lokal erkannt, ist aber keine unveränderliche Sperre: Laufzeit-`OPENCODE_PERMISSION`, Testverzeichnis-Umleitung und Remote-Organisationskonfiguration bleiben außerhalb dieses Readers. Natives OTEL kann, wenn aktiviert, Live-`gen_ai.*`-Nutzung über den Out-of-band-`OTEL_*`-Exporter liefern |
| `gemini-cli` | `settings.json`-Layer der Gemini CLI (System/User/Workspace) → permitted MCP-/Tool-Kanten, Enforcement-Gap-Posture, Inventar der effektiven Konfiguration | Nur per Konfiguration deklariert; Live-Nutzung läuft über den `gen_ai.*`-Ingest (die CLI emittiert sie nativ). Nicht die Gemini API (das ist die Hosted-Provider-Oberfläche) |
| `openhands` | OpenHands `config.toml` + Umgebung → Sandbox-/Model-Pinning-/Credential-/Telemetry-Posture, permitted MCP-/Action-Kanten | Nur per Konfiguration deklariert; Live-Nutzung über natives OTEL `gen_ai.*` |
| `goose` | Goose (Block) `profiles.yaml` + Umgebung → Admin-Settings-/Model-Pinning-/Extension-/Tool-Approval-Posture, permitted Extension-Kanten | Nur per Konfiguration deklariert |
| `cline` | Cline-/Kilo-Code-VSCode-Namespaces in `settings.json` → Auto-Approve-/MCP-Allowlist-/Credential-/Model-Pinning-Posture | Nur per Konfiguration deklariert; upstream kein natives OTEL |
| `grok` | Grok Build (xAI) — der Terminal-Coding-Agent, gelesen aus seiner LOKALEN Konfiguration: Hook-Verdrahtung, Events mit dokumentiertem Veto und deklarierbare Governance-Posture | **Nicht der xAI-API-Connector** (`xai` liest Katalog und Kosten, darunter `grok-build-0.1` als MODELL). Dieser liest den AGENT, die beiden überschneiden sich nicht. Die BEOBACHTUNGSHÄLFTE läuft über den OTLP-Ingest, den Grok Build bereits emittiert. `PostureEnforced` beansprucht nur `PreToolUse`, das einzige Event mit dokumentiertem Veto; der Rest ist `observed` |
| `openclaw` | OpenClaw `openclaw.json` (JSON5-Erkennung, begrenztes `$include`) → Gateway-/Channel-/Tool-/Sandbox-/Skill-/Model-Posture pro Agent, deklarierte Channel-/Skill-/Model-Kanten | Nur per Konfiguration deklariert; upstream kein Inline-PEP-Hook verifiziert |
| `hermes` | Hermes Agent `config.yaml` + Profilbäume + Managed-Scope → Terminal-/Channel-/Skill-/Security-/Model-/MCP-Posture, deklarierte Kanten | Nur per Konfiguration deklariert; upstream weder Inline-PEP-Hook noch natives OTEL verifiziert |
| `google-adk` | Exportierte Google-ADK-2.0-Session-JSON → Agent-/App-Inventar, Subagents, Tool-Funktionsaufrufe, Transfers, Approved-Tool-Drift, Vertex-`reasoningEngine`-Korrelation | Read-only-Export; niemals Nachrichteninhalt. Verschieden von der `google-agent`-Plattformoberfläche |
| `agents-md` | Repo-Walk von Agent-Instruktionsdateien (AGENTS.md und Agent-spezifische Memory-/Instruktionsdateien) → SHA-256-Baseline-Drift + Scan auf Instruction Injection / verstecktes Unicode / Secrets | Minimal Data: bereinigte Pfade + gehashte Details, niemals Inhalt |
| `mcpb` | Installierte / verteilte `.mcpb`-Desktop-Erweiterungen → Manifest-Posture-Scan, Enterprise-Allowlist-Drift, PKCS#7-Signaturprüfung | PERMITTED-vs-OBSERVED auf der Extension-Oberfläche |
| `codex-managed-config` | OpenAI-Codex-Managed-Config-Dateien → Enforcement-Posture + Drift gegen die verfasste Baseline | Nur Beobachtung: kann einen Entwickler nicht daran hindern, den Managed-Layer zu umgehen (das `managed-settings`-Gegenstück für Codex) |

## Clean — natives Store-Audit (wortgetreues read/write)

Diese lesen den **eigenen** Audit-Trail eines Stores und übernehmen die read/write-Klassifizierung wortgetreu
— niemals aus Query-Text abgeleitet. `pgaudit` und `s3cloudtrail` sind die kanonischen R/RW-
Quellen, um die herum die [Access Map](/de/reference/modules/iii-access-map/) gebaut ist (ihre
mit Bindestrich geschriebenen Aliase `pg-audit` / `s3-cloudtrail` lösen ebenfalls auf).

| Kind | Beobachtet |
|---|---|
| `pgaudit` | PostgreSQL **pgAudit**-Trail (csvlog/jsonlog) → R/RW-Tabellenzugriff, `READ`/`WRITE` wortgetreu aus pgAudits CLASS |
| `s3cloudtrail` | AWS **CloudTrail** S3-Events → Objekt-R/RW, read/write aus CloudTrails `readOnly`-Flag (zeigt auch Claude-on-Bedrock-Modellaufrufe) |
| `snowflake-audit` | Snowflake native Access History |
| `databricks-uc` | Databricks Unity Catalog Audit |
| `bigquery-audit` | BigQuery Data-Access-Audit |
| `redshift-audit` | Amazon Redshift Audit |
| `mssql-audit` | SQL Server Audit |
| `oracle-audit` | Oracle Unified Audit |
| `gcs-audit` | Google Cloud Storage Data-Access-Audit |
| `azure-blob-audit` | Azure Blob Storage Audit |

## Cloud-Management-Plane — Org-/Tenant-Inventar + Control-Plane-Aktivität

Die Tri-Cloud-Parität für die **Management**-Plane — verschieden von der Per-Resource-
**Data**-Plane, die die Store-Audit-Connectors oben abdecken. Jeder ist ein live, **read-only**-API-
Client der Org-/Tenant-Control-Plane einer Cloud: er entdeckt die Resource-**Topologie**
(Inventar-Kanten, `mode=unknown`, attributed) und liest den nativen **Audit-Feed** der Cloud
für Control-Plane-**Aktivität** (`identity→…api`-Kanten, read/write klassifiziert). Sie
vervollständigen die Matrix, die AWS bereits mit `s3cloudtrail` (Data Plane) plus dem
Account-Level-IAM/CloudTrail-`aws`-Connector verankert. Beide laufen **in-process** und sind
**offline-safe** (keine Credential ⇒ Gather ist ein No-op); beide beobachten nur die Control Plane —
niemals einen Payload, ein Secret, einen Key oder eine Resource-Eigenschaft.

| Kind | Beobachtet | Ehrliche Coverage |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM** (org→folder→project→service-account-Topologie) + **Cloud Audit Logs** (Admin Activity + Data Access) → `identity→gcp.api` | **Clean** wo geloggt: Admin Activity ist per Definition des Log-Typs ein write, Data Access ist read/write aus dem Standard-Method-Verb. **Lossy** wo Data-Access-Logging deaktiviert ist (standardmäßig aus in GCP) oder ein Method-Verb nicht-standard ist (`unknown`, niemals geraten). `approximate` für deklarierte geteilte Principals; die `principalEmail` konvergiert mit dem SPIFFE/SA-Roster |
| `azure-activity` | Azure **Resource Graph** (tenant→subscription→resource-Topologie) + **Azure Monitor Activity Log** (Control-Plane-Operationen) → `identity→azure.api` | **Clean** für Control-Plane-Writes/-Deletes (wortgetreu aus der RBAC-Action). Das generische `action`-Suffix ist **lossy** (`unknown` — es kann lesen oder schreiben). Data-Plane-**Reads sind nicht im** Activity Log (die `azure-blob-audit` / `azurekeyvault` Data Plane deckt diese ab). `approximate` für geteilte Aufrufer; die `objectId`/`appId` des Aufrufers konvergiert mit dem Entra-Roster |
| `cloudflare` | Cloudflare-Edge-Bestand — **Workers, R2-Buckets, Logpush-Jobs** über die REST API v4 → Topologie-Kanten | Nur Inventar (kein Audit-Feed in diesem Connector); begrenztes Read-only-Token. Verschieden von den AI-Oberflächen `cloudflare-ai-gateway` / MCP-Portals |

Das GCP-**Data-Access**-Opt-in und die Azure-**read-not-logged**-Lücken sind die ehrlichen
**opaken** Kanten dieser Plane: eine fehlende Aktivitätskante ist kein Beweis für keinen Zugriff, wo
diese Logs aus sind. Die vollständige Per-Cloud-Stufen-Tabelle steht im ausgelieferten
Cloud-Management-Connector-Contract `docs/contracts/S165-connectors-cloud-management.md`.

## Hosted-Model-Provider — Katalog, Posture und Metering

Diese Quellen governieren Konten und Kataloge gehosteter Modellanbieter. Sie proxien
**keine** Inferenz; wo einem Anbieter eine nutzbare Usage-API fehlt, schätzt der `Meter`
des Connectors die Ausgaben um den Inferenzpfad, statt sie aus einem aggregierten Billing-Feed zu ziehen.

| Kind | Beobachtet | Ehrliche Coverage |
|---|---|---|
| `openai` | OpenAI-Platform-Nutzung und -Kosten (Org API) sowie Modell- und API-Key-Katalog | Read-only Org-/Admin-Key; keine Data-Plane-Payloads. Verschieden von `azure-openai`, das die echten Azure-Oberflächen statt OpenAI-Org-Pfaden anspricht |
| `gemini` | Von Gemini (Google) gehosteter Modellkatalog und ein vom Operator verdrahteter Nutzungs-Export | Die Hosted-Provider-Oberfläche. Verschieden von `gemini-cli`, das lokale CLI-Einstellungen beobachtet, und von `vertex`, das Enterprise-Vertex-Oberflächen abdeckt. Google bietet auf diesem Pfad keine aggregierte Usage-API; Nutzung ist daher das, was der Operator verdrahtet |
| `deepseek` | Gehosteter DeepSeek-Katalog, Verfügbarkeit des Kontosaldos und PRC-Souveränitäts-Posture | Keine aggregierte Usage-API; Kosten werden um die Inferenz aus deklarierter Preisgestaltung gemessen |
| `mistral` | Mistral-Katalog und Governance-Posture | Keine öffentliche Usage-/Billing-/Spending-Cap-API; Kosten werden um die Inferenz aus Listenpreisen gemessen |
| `xai` | Live-Katalog von xAI/Grok, Billing-Endpunkte, Key-/ACL-Inventar, Credit- und Spending-Limit-Posture | Nutzt die read-only Management-Billing-Endpunkte für Kosten; Management- und Inferenz-Credentials sind getrennt |
| `glm` | Deklarierter Katalog von Zhipu GLM / Z.ai, `Meter` mit USD-Listenpreisen, Entitlement-Probe und Souveränitäts-Posture | Nur Katalog + Meter: GLM bietet keine verifizierte Usage-, Billing-, Balance-, Admin-, Key- oder Organisations-API. Der Vorbehalt zu PRC-Bezug / Entity List gilt für die Oberflächen `z.ai` und `bigmodel.cn` |
| `vertex` | Google-Vertex-AI-Katalog, Token-Nutzung pro Modell (Cloud Monitoring), Opt-in-Billingkosten (Billing Export) und optionale Model-Armor-Safety-Posture | Die Enterprise-Google-Oberfläche, die der AI-Studio-Pfad nicht abdeckt; GCP hat keine Echtzeit-Kosten-API |
| `azure-openai` | Azure-OpenAI-/AI-Foundry-Deployments + Modelle (ARM), Azure-Monitor-Token-Nutzung und Kostenoberflächen | Read-only Management-Plane-Client; keine Data-Plane-Payloads |
| `openrouter` | Live-Katalog von OpenRouter (Preis in USD/MTok), Account-Usage-/Limit-Posture, Drift der Approved-Model-Policy | Abgerechnete Kosten über den exportierten `MeterCall`; offline ein No-op |
| `cohere` | Live-Modellkatalog von Cohere (cursor-paginierte Models API) | Keine öffentliche Usage-/Billing-/Org-API (nur Dashboard) — ein ehrlicher Coverage-Vorbehalt; Kosten werden um die Inferenz aus Listenpreisen gemessen |
| `fal` | Lebenszyklus-Inventar von fal.ai-API-Keys + Rotations-Posture; Kostenmessung um die Queue API | Keine öffentliche Usage-/Audit-API — Governance erfolgt über den Key-Lebenszyklus; tiefe Oberflächen sind vertrieblich beschränkt und als UNVERIFIED markiert |

## Self-hosted Inference — lokale Kataloge und Nutzung

Self-hosted Inference ist immer im Scope und daher eine First-Class-Quelle statt eines
Gateway-Nachgedankens. Diese Stufe beobachtet, was eine lokale Runtime tatsächlich bereitstellt.

| Kind | Beobachtet | Ehrliche Coverage |
|---|---|---|
| `local` | Ollama-Modellkatalog (`/api/tags`), **Ollama-Residency (`/api/ps`)** — welche Modelle jetzt geladen sind, samt GPU-/CPU-Aufteilung und Unload-Deadline — und vLLM-Token-Nutzung über seine OpenAI-kompatible Oberfläche | Residency wird als Posture gemeldet; der Schweregrad entspricht dem PLACEMENT: ein vollständig im VRAM befindliches Modell ist informativ, eines auf der CPU oder zwischen CPU und GPU GETEILTES wird markiert, weil der Operator dabei Latenz bezahlt, ohne informiert zu werden. Ollama veröffentlicht keine aggregierten Token-Metriken und trägt daher kein Metering bei. Diese Quelle liefert weiterhin keine Identität oder Policy pro Aufruf für lokale Inferenz; deren Governance erfordert den Gateway- oder OTel-Pfad. Ollama auf localhost benötigt keine Credential, sodass eine leere Konfiguration ein funktionierender Read-only-Standard ist; ein Server wird durch eine EXPLIZIT leere URL deaktiviert, und sind beide leer, ist dies ein No-op |

## Kernel-Backstop — eBPF / Tetragon (clean signal, approximate attribution)

Die **non-cooperative** Hälfte des Moats: wo der kooperative Pfad sieht, was ein Agent
*berichtet*, sieht dieser, was der Kernel *tat* — Datei-Reads/-Writes und ausgehende Verbindungen —
selbst wenn ein Agent seine eigene Telemetrie abschaltet. Der **Zugriff** ist Kernel-Ground-Truth (ein
Clean-Tier-Signal von *was geschah*); die **Attribution** ist bewusst ehrlich über
ihre Grenze — der Kernel attribuiert auf eine Runtime-Identität (Process/cgroup/Container), niemals
auf einen aufgelösten Agenten, sodass jede eBPF-Kante `approximate` ist. Er entschlüsselt oder inspiziert niemals
Payloads (er ist blind für den TLS-Body).

| Kind | Beobachtet | Ehrliche Grenze |
|---|---|---|
| `ebpf` | Tetragon-Kernel-Events → Datei-R/RW (`MAY_*`-Maske) und Netzwerk-Kanten; optionales Anti-Evasion-Finding, wenn ein Agent am Kernel ohne kooperative Telemetrie agiert | Agent-anonym → immer `approximate`; ein Streaming-Backstop, kein Per-Agent-Ledger |

Er lädt **nicht** selbst eBPF-Programme: die Kernel-Erfassung erfolgt durch
[Tetragon](https://tetragon.io/) (ein separater, gehärteter DaemonSet). Siehe
[Deployment-Anforderungen](#deployment-anforderungen-und-ehrliche-attribution).

## Lossy — Kanten landen, oft approximate

| Kind | Beobachtet | Warum lossy |
|---|---|---|
| `mongo-audit` | MongoDB-Audit | Document-Store; Aufrufer-Trennung ist schwach |
| `openlineage` | OpenLineage-Run-Events → Dataset-Lineage | Lineage ist kein Per-Call-Audit |
| `delta-sharing` | Delta-Sharing-Recipient-Aktivität | Shared-Recipient-Attribution |

## Approximate-by-attribution & Permitted-Side-Quellen

Diese emittieren entweder die **permitted**-Seite (deklarierte Grants) oder Zugriffe, die einer
Rolle / einem Prozess / einer geteilten Credential statt einem aufgelösten Agenten zugeordnet werden.

| Kind | Beobachtet | Stufe |
|---|---|---|
| `iceberg-catalog` | Iceberg REST Catalog → permitted Grants + vended-credential-Identitäten | permitted |
| `inference-gateway` | K8s Gateway API Inference-Extension-Routing → permitted Inference-Routes | permitted |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | Cloud KMS Audit → Key-Access-Kanten (niemals Key-Material) | approximate |
| `external-secrets` / `sops` / `kmip` | Secret-Management-Manifeste / KMIP-Locate → Provisioning-/Custody-Kanten | approximate (Existenz, nicht Nutzung) |
| `istio-telemetry` | Istio Telemetry CRDs → L7-Mesh-Kanten | approximate (geparste CRDs, keine Live-Flows) |
| `egress-proxy` | Egress-Proxy-Verdict-Log → L7-Egress-Kanten | approximate |
| `kong-audit` | Kong-Audit-Logs → Config-Change-Findings | approximate |
| `ai-gateway` | Envoy AI Gateway Usage-Records → **Kosten**-Proben (FinOps) | Cost-Stream |
| `github` | GitHub-Repositories als Agent-Datenquellen → beobachtete R/RW-Zugriffskanten (Webhook-first, API-Poll-Reconciliation) + permitted ACL-Kanten | observed + permitted; Streaming (`poll_seconds: 0`) |
| `gitlab` | GitLab-Repositories → beobachtete R/RW-Zugriffskanten + permitted ACL-Kanten | observed + permitted; Streaming (`poll_seconds: 0`) |

## Posture-Observer — Findings, keine Access-Kanten

Read-first-Observer, die Posture (Sync/Health/Drift, Auth-Anomalien) als
Findings sichtbar machen; sie mutieren niemals das Estate.

| Kind | Beobachtet |
|---|---|
| `runtime` | Wo AI-Workloads laufen (Linux procfs, Docker-Daemon, Kubernetes-API) → Containment-Kanten + Health-Findings (benötigt Host-Zugriff — siehe [Deployment-Anforderungen](#deployment-anforderungen-und-ehrliche-attribution)) |
| `argocd` / `flux` / `crossplane` | GitOps / Control-Plane-CRDs → Sync-, Health-, Drift-, Composition-Posture |
| `kerberos` | KDC-Auth-Telemetrie → Kerberoasting-Findings |
| `aaa` | RADIUS / TACACS+ AAA-Beobachtungen |
| `ssf` | Shared-Signals / CAEP-Receiver (Agent-Kill-Switch) |
| `edugain` / `openidfed` | Federation-Aggregat / OpenID-Federation-Trust-Chains → Federation-Posture |
| `managed-settings` | Claude `managed-settings`-Policy → permitted Kanten + Drift-Findings |
| `envoy-ai-gateway` | Export der **deklarierten Konfiguration** von Envoy AI Gateway → Gateway-Posture + Drift zwischen Gateway- und Olivares-Policy (das Config-Geschwister des `ai-gateway`-Usage-Streams) |
| `kong-agent-gateway` | Export der deklarierten Kong-Agent-Gateway-Konfiguration → Posture + Policy-Drift |
| `litellm` | Export der deklarierten LiteLLM-Proxy-Konfiguration → Posture + Policy-Drift |
| `bedrock-kb` | Retrieval-Health/-Konfiguration von Amazon Bedrock Knowledge Bases (Health-Check mit Agent Runtime Retrieve) → Posture-Findings pro KB + KB→Datenquellen-Kanten. Niemals `RetrieveAndGenerate` (keine kostenpflichtige Inferenz), niemals vollständiger Dokumentinhalt |
| `tak` | TAK-Server-`CoreConfig.xml`-Posture (+ optionale mTLS-Probe) und governierter Minimal-Data-Cursor-on-Target-Ingest (Positionen als Digest, UID gehasht) |
| `a2a` | Agent2Agent-(A2A-)v1.0-Peers → Agent-Card-Erkennung + JWS-/JCS-Signaturprüfung (Peer-Trust-Level) sowie beobachtete Task-/Message-Interaktionen als Agent↔Agent-Kanten. Nur Beobachtung — dispatcht niemals eine Task; signierte Cards zu emittieren ist eine eigene Fähigkeit |

## Untrusted hint — MCP-Introspektion

Die `mcp`-Quelle introspiziert MCP-Server (stdio + Streamable HTTP) und emittiert **Capability-
Kanten**, die die *deklarierten* R/RW-Hinweise des Servers tragen, plus Protocol-Revision-, Feature-Surface-
und Registry-Provenance-Findings. Gemäß der MCP-Spezifikation ist eine Tool-Annotation eine
**untrusted** Deklaration — ein Capability-*Claim*, bestätigt gegen eine beobachtete Quelle,
**niemals allein vertraut**. (Die kooperative `claude`-Quelle introspiziert MCP ebenfalls als Teil
ihres OTLP-Pfads; `mcp` ist der eigenständige Introspector, den du auf eine Server-Liste oder eine
`.mcp.json` richtest.)

| Kind | Beobachtet | Stufe |
|---|---|---|
| `mcp` | MCP-Server-Tools/-Resources/-Prompts → declared-capability-Kanten + Posture-Findings | untrusted hint |

## Out-of-process-Broker- & Mesh-Observer

Diese tragen schwere Wire-Protocol-Abhängigkeitsbäume, sodass jeder **out-of-process** läuft (die
Abhängigkeit linkt niemals in den Core). Ein Connector erreicht viele Ziele.

| Kind | Beobachtet |
|---|---|
| `kafka` | Kafka / Event Hubs / Redpanda / MSK Topic-Aktivität |
| `amqp` | AMQP-Broker (RabbitMQ, Azure Service Bus) |
| `nats` / `mqtt` / `cloudqueue` | NATS-, MQTT-, Cloud-Queue-Aktivität |
| `debezium` | Debezium Change-Data-Capture-Streams |
| `envoy` | Envoy ALS / ext_authz / ext_proc Observation Services |
| `hubble` | Cilium Hubble Flow-Daten |

## Identitäts-Roster-Provider

Diese füllen das Non-Human-Identity-**Roster**, das die Attribution schärft (verwandelt
`approximate`-Kanten in `attributed`). Jede Quelle mit einer Grant-Oberfläche emittiert auch
ihre **permitted-access**-(`SignalPolicy`)-Kanten aus `Gather` — die PERMITTED-
Seite des permitted-vs-observed-Diffs:

| Kind | Roster | Permitted-Kanten |
|---|---|---|
| `vault` | Entities, Groups, Policies | ACL-Policy-Path-Grants (`vault.path`), erweitert pro gebundener Entity |
| `ldap` | Users, Service-/Computer-Accounts, Groups | Privileged-Group-Membership → Directory-Grants (`ldap.directory`) |
| `idp` (Okta / Entra) | Users, Apps/Service-Principals, Groups | App-Assignment-/Scope-Grants (`okta.app` / `entra.app`) |
| `infisical` | Machine-Identities, Org-Members, Projects | Project-Grants (`infisical.project`) |
| `keycloak` | Realms, Clients, Roles, Groups, Users | nur Roster (No-op `Gather`) |
| `pingone` / `forgerock` | PingOne-/ForgeRock-Directory-Roster über denselben Multi-Provider-Reader (der Kind setzt den passenden `provider`; `ping` ist Alias von `pingone`) | nur Roster (No-op `Gather`) |
| `spiffe` | SPIRE-Registration-Entries | nur Roster (No-op `Gather`) |

Verdrahte `as_source: true` auf dem `identity`-Eintrag für einen einmaligen Permitted-Grant-Pass
pro Boot, oder einen separaten `sources`-Eintrag mit `poll_seconds` für periodische Re-Scans —
niemals beides für ein Kind (`okta`/`entra` teilen sich den einen `idp`-Connector, sodass nur eine
idp-Familien-Instanz pro Prozess als Quelle registriert werden kann). Group-/Role-Memberships
reisen nur im typed Roster-Snapshot, niemals als Kanten.

### Agent-Identity-Federation

Die Hyperscaler-**Agent-Registries** föderieren read-only gegen das SPIFFE/WIF-
Roster der Plane. Ihre Per-Agent-Rows (`agent_identity` / `workload_identity` Kinds) sind
dedizierte, nicht geteilte Identitäten, sodass die Access Map sie als **feste** Per-Agent-
Attribution behandelt; ergänzende Rows aus denselben Quellen (Blueprint-Principals, Credential-
Provider, Service-Account-gestützte Agents) bleiben approximate. Federation schreibt niemals in
eine Registry; *Export* zu den Control-Towers ist eine separate, spätere Fähigkeit.

| Kind | Föderiert | Gather |
|---|---|---|
| `entra-agent` | Microsoft Entra Agent ID (Agent-Identities, Agent Users, Blueprints, Blueprint-Principals, Owners/Sponsors, In-Snapshot-Orphan-Computation, opt-in soft-deleted) über Graph v1.0 | `nhi_longlived_credential`-Drift-Findings, CA-/Risky-Agent-/Governance-/Sponsorless-Posture-Findings und opt-in beobachtete Agent-Zugriffskanten aus Beta-`auditLogs/signIns` — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `agentcore` | AWS Bedrock AgentCore Identity (Workload-Identities, Token-Vault-Credential-Provider) + AgentCore Policy Engines/Cedar-Policies als Collections | `nhi_longlived_credential`-Drift-Findings (statische API-Key-Provider) — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `google-agent` | Google Agent Identity (Agent-Runtime-Reasoning-Engines; SPIFFE-basierte Agent-Identitäten) sowie Agent-Registry-/Agent-Gateway-Posture. Rows verwenden die **vollständige SPIFFE-ID** als Ref und konvergieren mit dem `spiffe`-Roster; Gather erkennt nicht attribuierte Registry-Agents, Shadow-Reasoning-Engines außerhalb einer lesbaren Registry, riskante MCP-Tool-Annotationen und Gateway-Registry-Posture | Registry-/Gateway-Posture-Findings und Shadow-Agent-Erkennung — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `agent365` | Microsoft Agent 365 Registry (Package-Level-Inventar einschließlich Agents *ohne* Entra-Identität) über Graph v1.0, Client-Credentials mit App-Berechtigung oder delegiertes Token, optionale Package-Details | Registry-Hygiene-Findings (blockierte deployte Packages; externe/geteilte Packages für alle Benutzer deployt) — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `foundry-agents` | Microsoft-Foundry-Projekte, Agent Applications/Deployments und aktuelle Agent-Service-Agents über ARM + Foundry Agent Service v1; korreliert App-Identity-Links mit `entra-agent` | Von ARM abgeleitete Application-Posture-Findings (fehlende Entra-Agent-Identität; fehlgeschlagenes Deployment einer aktivierten App) — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `ai-control-tower` | ServiceNow AI Control Tower Digital-Asset-Inventar (Table API, read-only) | No-op (nur Roster) |
| `oasf` | AGNTCY/OASF-Agent-Descriptors + Agent-Badge-Verifikation — **EXPERIMENTAL** bis die Identity-Spec VCDM-2.0-konform ist | Badge-Findings — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |
| `onepassword` | 1Password-Account als `secret_store`-Custodian | Item-Usage-Secret-Access-Kanten — füge einen `sources`-Eintrag mit `poll_seconds` hinzu |

Für die sieben Kinds mit einem re-pollbaren Gather (`entra-agent`, `agent365`, `agentcore`,
`foundry-agents`, `google-agent`, `oasf`, `onepassword`) verdrahte die **Roster**-Hälfte als `identity`-Eintrag *ohne*
`as_source` und die **Edges/Findings**-Hälfte als separaten `sources`-Eintrag mit
`poll_seconds` — nicht beides via `as_source: true`, was den Scan nur einmal pro
Boot ausführt (und eine doppelte Registrierung desselben Kinds wird abgelehnt).

Vom Registry deklarierte **Owner/Sponsor** landen während des Roster-Syncs auf den NHI-Lifecycle-
Records (dieselbe Semantik wie `PUT /nhi/{ref}/ownership`), und ein vom Registry behaupteter
**Orphan** (ein Entra-Agent, dessen Blueprint verschwunden ist) landet auf dem `registry_orphaned`-
Flag desselben Records — der Lifecycle-Sweep OR-t ihn in `orphaned` und emittiert das
`nhi_orphaned`-Finding, sodass die Orphan-Erkennung föderierte Agents mit null zusätzlicher
Verdrahtung überwacht. Die `vault-audit`-*Quelle* (unter `sources`, nicht `identity`) tailt das Vault-
File-Audit-Device und emittiert das OBSERVED-Gegenstück zu `vault`s permitted Grants
für dieselben `entity:<name>`-Refs.

## Knowledge-Document-Quellen (keine Access-Map-Coverage)

Diese speisen das **knowledge**-Modul (Modul VIII), **nicht** die Access Map: sie nehmen
*Dokumentinhalt* für governed Retrieval auf, emittieren **keine** R/RW-Kante und erzeugen **keine**
Beobachtung auf dem Bus. Das Modul *zieht* sie (List → Fetch) bei einer Ingest-Anfrage
(`POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"}`), sodass sie in dieses
Modul verdrahtet sind — benenne sie unter `documents` in `OLIVARES_SOURCES_CONFIG`, nicht `sources`. Jede ist
read-only und minimal-data: sie trägt die ACL und Provenienz der Quelle (niemals eine persönliche
E-Mail; das Modul maskiert den Body vor dem Persistieren).

| Kind | Nimmt auf |
|---|---|
| `gdrive` | Google Drive Dokumente (Docs/Sheets/Slides/Dateien) |
| `confluence` | Atlassian Confluence Spaces & Pages |
| `notion` | Notion Workspaces, Databases & Pages |
| `sharepoint` | Microsoft SharePoint / OneDrive Sites & Dokumente |
| `s3content` | Object-Storage-Content (S3 / R2 / GCS Objekte) |
| `sap_odata` | Entitäten eines SAP-OData-Service als governierte Dokumente |
| `salesforce` | Salesforce-Objekte/-Records als governierte Dokumente |
| `snowflake` | Snowflake-Tabellen/-Zeilen als governierte Dokumente (verschieden vom R/RW-Observer `snowflake-audit`) |
| `azure_ai_search` | Dokumente in Azure-AI-Search-Indizes |
| `postgres` | PostgreSQL-Zeilen als governierte Dokumente — per Konstruktion read-only, deklarierte ACL pro Zeile, Klassifizierung pro Spalte (verschieden vom R/RW-Observer `pgaudit`; kein NL-to-SQL). Siehe [Postgres als governierte Kontextquelle](/de/how-to/govern-postgres-content/). |
| `filesystem` | File-Server-Inhalte (lokal / NFS / SMB) — Lesen per Konstruktion auf das Root-Verzeichnis begrenzt, POSIX-Owner/-Group/-ACL auf Dokument-ACLs gemappt, xattr-Klassifizierung (verschieden vom `filelog`-Log-Sink). Siehe [File-Server governieren](/de/how-to/govern-your-file-server/). |

```jsonc
// OLIVARES_SOURCES_CONFIG — Document-Quellen liegen unter "documents", niemals "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## Ausgabeziele (keine Coverage)

Output-Connectors **liefern** Findings und Benachrichtigungen aus; sie beobachten nichts und haben
keine Coverage-Stufe. Sie werden separat von Quellen verdrahtet.

In-process-Ziel-Kinds: `slack`, `teams`, `pagerduty`, `opsgenie`, `webhook`, `siem`,
`splunkhec`, `syslog`, `servicenow`, `jira`, `email`, `twilio`, `chronicle`, `datadog`,
`elastic`, `snmp`, `filelog`, `otlplog` (OTLP/HTTP-Logs) und `s3archive` (der S3-Object-
Lock-WORM-Sink — ein unveränderliches, auf Lock geprüftes Objekt pro Benachrichtigung).

Drei Broker-Egress-Kinds laufen **out-of-process** als eingebettete Plugins (ihre Wire-Protocol-
Abhängigkeitsbäume werden wie bei den Plugin-Quellen niemals in die Engine gelinkt): `kafka`,
`amqp` und `cloudqueue` — dieselben Kind-Namen wie ihre Source-Zwillinge; als Ziel liefert jeder
die Benachrichtigung als CloudEvent an den konfigurierten Broker/die Queue. Ein einfacher Dev-Build
ohne `task build:connectors` überspringt ein solches Ziel mit einer ehrlichen Boot-Warnung, statt
vorzutäuschen, dass es vorhanden sei.

:::note[Der ausgehende Webhook ist ein Ziel, kein API-Webhook]
`webhook` ist ein Output-Channel, auf den die Control Plane pusht, kein Callback, den du
gegen die REST-API des Produkts registrierst — das OpenAPI-Dokument definiert keine `webhooks`. Siehe
[Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

## Deployment-Anforderungen und ehrliche Attribution

Die R/RW-Differential-Connectors sind in das Standard-Binary verdrahtet, aber zwei bringen eine
**Deployment-Anforderung** mit, die der Rest nicht hat — der Connector-Code ist host-agnostisch, die
*Daten*, die er konsumiert, sind es nicht:

- **`ebpf`** konsumiert [Tetragon](https://tetragon.io/)s Kernel-Event-Export. **Der
  Connector benötigt keine Kernel-Capability** — er liest eine `0600`-Datei/-FIFO/`stdin`, die
  Tetragon besitzt (`events_path`, Standard `-`). Tetragon selbst ist ein **separater, gehärteter
  DaemonSet**, der die minimalen `CAP_BPF` + `CAP_PERFMON` hält, non-root mit
  seccomp/AppArmor und ohne Inbound-Listener läuft. Das Deployment ist also: Tetragon privilegiert laufen lassen
  (seine gebündelten File-Access- + TCP-Connect-TracingPolicies), dann `ebpf` auf seinen Export richten.
  Minimum Tetragon: v1.0.
- **`runtime`** liest das procfs des Hosts (`proc_root`, Standard `/proc`), den Docker-Daemon-
  Socket (`docker_socket`, **standardmäßig aus** — Lesezugriff auf `docker.sock` ist
  root-äquivalent; bewusst opt-in, idealerweise via GET-allowlisteten Socket-Proxy) und/oder
  die Kubernetes-API (standardmäßig in-cluster ServiceAccount). Mounte nur, was du aktivierst.
- **`gcp-audit`** authentifiziert sich als GCP-Service-Account (Key-JSON oder ein WIF/ADC-ausgestelltes
  `access_token`) und benötigt nur **read-only-Management**-Rollen:
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` +
  `roles/logging.viewer` — das Lesen von **Data-Access**-Einträgen benötigt zusätzlich
  `roles/logging.privateLogViewer`. Scope `organization_id` (Org-Walk + Org-scoped-Audit)
  und/oder `projects`. Data-Access-Audit-Logs sind **standardmäßig aus in GCP**: aktiviere sie gemäß
  der IAM/Data-Access-Config, sonst under-reportet der Activity-Feed ehrlich.
- **`azure-activity`** authentifiziert sich als Entra-Service-Principal (Client-Credentials) oder
  ein Managed-Identity-`access_token` und benötigt nur die **Reader**-Rolle am Tenant-Root
  (oder pro Subscription) — diese eine Rolle deckt Resource Graph, Subscription-Listing und
  das Activity Log ab. Subscriptions werden automatisch gelistet, wenn `subscriptions` nicht gesetzt ist.

Beide laufen weiterhin **in-process** (Transport A); die
`cmd/{pg-audit,s3-cloudtrail,ebpf-source}`-go-plugin-Binarys existieren für ein out-of-process-
**Collector**-Deployment nahe dem Host, falls du sie dort isolieren möchtest.

Jede Quelle ist **opt-in, deny-closed**: ein fehlendes `log_path`/`path`/`events_path` ist ein
Konfigurationsfehler beim Start (die Quelle wird nicht verdrahtet), niemals ein stilles No-op. Das Demo-
Estate ([Quickstart](/de/start/quickstart/)) säet äquivalente synthetische Beobachtungen durch
den realen Bus, sodass du das Clean-Tier-Signal end-to-end sehen kannst, bevor du eine Live-Quelle verdrahtest.

:::caution[Ehrliche Grenzen über jede Stufe hinweg]
- **Eine fehlende Kante ist kein Beweis für keinen Zugriff**, wo Coverage lossy, impossible oder eine
  Quelle nicht verdrahtet ist. Die Access Map ist ehrlich über ihre eigene Reichweite.
- **Per-Agent-Identität ist die harte Abhängigkeit.** Ein geteilter Service-Account hinter einem
  Connection-Pool lässt die Attribution selbst bei einem Clean-Tier-Store auf `approximate` zusammenbrechen —
  siehe [Governance und Freigabe](/de/how-to/govern-and-approve/).
- **MCP-Tool-Annotationen sind untrusted** gemäß der MCP-Spezifikation: ein deklarierter Capability-
  Hinweis, bestätigt gegen eine beobachtete Quelle, niemals allein vertraut.
:::

## Verwandtes

- [Eine Quelle anbinden](/de/how-to/connect-a-source/) — das Connector-Modell und wie man eine verdrahtet.
- [Claude Code anbinden](/de/how-to/connect-claude-code/) — der kooperative Pfad end-to-end.
- [Modul III — die Access Map](/de/reference/modules/iii-access-map/) — was aus den Kanten wird.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der produktweite ehrliche Contract.
