---
title: Console reference — every screen and the permission it needs
description: >-
  Every route the Olivares AI console publishes, indexed by historical hub
  categories, with the RBAC permission each one requires and the reference page
  its in-product help link opens. Generated from the console's own route census.
---

This page is the map of the console. It lists **every route the application mounts** —
not a selection, not the ones somebody remembered to write up — with the permission a
principal needs to enter it and where to read more.

The live sidebar is **nine areas with sections**, not five hubs. After Overview it
renders the areas this principal may actually open, each with a directory page and
expandable modules, then the pinned Settings utility. A navigation filter replaces
that grouped tree with a ranked list of authorized matches; the command palette uses
the same index, ranking and permission projection. An area directory is a page of
links — the entries your permissions allow — not a live reading of capability,
availability or readiness.

It is **generated**. The roster comes from `web/src/features/route-census.json`, the
append-only census that `registry.route-conservation.test.ts` pins against the built
router, so a screen cannot be added, moved or lost without this page changing with it.
Each screen's name and one-line description are the **console's own strings**, taken from
the same translation catalog the sidebar renders, so what you read here is what you see in
the product. The tables below still group those rows by the five historical hub
categories (Operate, Automate, Connect, Govern, Prove) plus sign-in, setup and account.
That grouping is an index, not the current sidebar order. The nine area directory
pages currently sit with the routes mounted outside the feature registry.

:::note[Permissions are enforced by the engine, not by this table]
The `Requires` column names the permission the console checks before offering a route,
using the effective permissions returned by the engine. The engine authorizes API
requests independently, including requests made outside the console. Navigation
visibility does not establish that a module is configured or ready to run.
See [Roles and permissions](/reference/modules/vi-governance/).
:::

## How to read this page

- **Screen** — the name the sidebar, the area directories and the command palette use.
- **Path** — the URL under your deployment's console origin. It is a published contract:
  a bookmark, a runbook deep link and a docs cross-reference are all this string.
- **Requires** — the RBAC permission. `any signed-in user` means the route is open to
  every authenticated principal; **no sign-in** means it is served before there is a
  session at all.
- **Reference** — the page the console's own help link opens for that screen.

The headings below are those historical hub categories, in the generator's index
order — not the order the sidebar renders. The current sidebar lists areas as
Infrastructure, AI, Data & context, Work & communications, Automation, Security &
identity, Deployment, Observability & evidence, then System & settings.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

The console publishes **75 routes**. Every one of them is in the tables below, with the
permission it requires and the reference page its in-product help link opens.

### Operate

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Overview | `/` | Estate overview and health at a glance | any signed-in user | [docs home](/) |
| Operate sessions | `/agentops` | Create, attach to and govern Claude Code sessions — no SSH; shares its screen with Observe sessions | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/how-to/run-claude-code-with-olivares/) |
| Backups | `/backups` | Encrypted disaster-recovery snapshots of the control plane: create, schedule, download and restore | `system:admin` | [how-to/backup-and-restore](/how-to/backup-and-restore/) |
| Communications | `/communications` | Channels, direct notices and the personal inbox of the selected workspace | `sessions:channel:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Channel administration | `/communications/administration` | Administer channels: configuration and grant history, each act under the channel's current ETag | `sessions:channel:admin` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Handoffs | `/communications/handoffs` | Offers of work responsibility addressed to you: read the context and accept or reject | `sessions:delivery:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Communications inbox | `/communications/inbox` | Your exact mailbox: deliveries addressed to you, opened fresh and acknowledged explicitly | `sessions:delivery:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| New channel | `/communications/new` | Create a channel with explicit initial grants | `sessions:channel:write` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Health & SLA | `/health` | Agent and MCP uptime and SLAs | `health:status:read` | [reference/modules/xxii-health](/reference/modules/xxii-health/) |
| Kill switch | `/killswitch` | Emergency stop, dual-control recovery and guardian containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/how-to/cookbook/kill-switch-drill/) |
| Logs | `/logs` | The live engine log stream, with level and module filters, search and pause | `system:admin` | [how-to/troubleshooting](/how-to/troubleshooting/) |
| Observability | `/observability` | Ingestion health by standard and trace drill-down | `health:status:read` | [reference/modules/observability](/reference/modules/observability/) |
| Source bindings | `/provider-bindings` | Dedicate configured sources, at the revision this node applied, to provider profiles | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Provider profiles | `/provider-profiles` | Register and administer the provider homes sessions launch under, and read their configuration on demand | `sessions:profile:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Sandbox | `/sandbox` | Isolated agent testing and replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/reference/modules/xvii-sandbox/) |
| Observe sessions | `/sessions` | Observe live and discovered sessions with their timelines; shares its screen with Operate sessions | `sessions:live:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Tenants | `/tenants` | Withdraw or restore a tenant's service | `system:admin` | [how-to/troubleshooting](/how-to/troubleshooting/) |
| Voice | `/voice` | Voice and realtime sessions | `voice:session:read` | [reference/modules/xvi-voice](/reference/modules/xvi-voice/) |
| Work | `/work` | The durable cross-session backlog: items, dependencies, acceptance and decisions | `sessions:work:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Workspace | `/workspace` | Agents, sessions, resources and activity scoped to one workspace | `tenant:read` | [reference/modules/xx-multi-tenancy](/reference/modules/xx-multi-tenancy/) |
| Workspace templates | `/workspace-templates` | Reusable snapshots of hooks, settings, connectors and policies for workspace sessions | `sessions:template:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |

### Automate

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Alerting | `/alerting` | Route findings to destinations and inspect deliveries | `notify:route:read` | [reference/modules/xv-notify](/reference/modules/xv-notify/) |
| Automations | `/automations` | All three automation rails and their trigger catalog | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/reference/modules/iv-orchestration/) |
| Webhooks & events | `/eventing` | Outbound webhook subscriptions, the captured event log, deliveries and the dead-letter queue | `eventing:subscription:read` | [reference/modules/eventing](/reference/modules/eventing/) |
| Orchestration | `/orchestration` | Agent-to-agent coordination and schedules | `orchestration:graph:read` | [reference/modules/iv-orchestration](/reference/modules/iv-orchestration/) |

### Connect

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| API Playground | `/api-playground` | Interactively explore and test the control-plane API | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/reference/modules/xix-api-manage-as-code/) |
| MCP & skills | `/capabilities` | Govern MCP servers, skills and tools | `capabilities:catalog:read` | [reference/modules/v-capabilities](/reference/modules/v-capabilities/) |
| Catalog | `/catalog` | Curated, approved agents and capabilities | `catalog:entry:read` | [reference/modules/xiv-catalog](/reference/modules/xiv-catalog/) |
| Protocol bindings | `/communications/protocol-bindings` | Compose and reconcile governed A2A and MCP bindings | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Deployment | `/deploy` | Provision and wire agents to infrastructure | `deploy:deployment:read` | [reference/modules/vii-deploy](/reference/modules/vii-deploy/) |
| Inventory | `/inventory` | Discover and catalog every agent, MCP and model | `inventory:catalog:read` | [reference/modules/i-inventory](/reference/modules/i-inventory/) |
| Knowledge | `/knowledge` | Knowledge bases, RAG and data lineage | `knowledge:kb:read` | [reference/modules/viii-knowledge](/reference/modules/viii-knowledge/) |
| Model Operations | `/model-operations` | Owned models, admission and deployments | `models:registry:read` | [reference/modules/xxiii-model-operations](/reference/modules/xxiii-model-operations/) |
| Models | `/models` | Models, routing and provider keys | `models:catalog:read` | [reference/modules/x-models](/reference/modules/x-models/) |
| Setup wizard | `/onboarding` | Step-by-step deployment configuration | `system:admin` | [start/quickstart](/start/quickstart/) |
| Platforms | `/platforms` | Provider platform surfaces, compliance matrix and per-platform model lifecycle — reference, not your servers | `models:platforms:read` | [reference/modules/x-models](/reference/modules/x-models/) |

### Govern

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Access map | `/access-map` | What each agent reads and writes (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/reference/modules/iii-access-map/) |
| AgentCore export | `/agentcore-export` | Plan, review and apply the projection of this tenant's governance rules onto AWS AgentCore as Cedar policies; planning writes nothing | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Claude Code governance | `/claude-policy` | Managed policy, hooks, MCP, sandbox and policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/how-to/connectors/claude-code-hooks-pep/) |
| Administration | `/console` | Users, SSO/IdP, workspaces, agent groups, roles, secrets, connectors, API keys and this installation's licence | `tenant:admin` | [reference/modules/xx-multi-tenancy](/reference/modules/xx-multi-tenancy/) |
| Identity & NHI | `/identity` | SSO, SCIM, the NHI roster and the WIF graph | `governance:identity:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Inference proxy | `/inference-proxy` | Proxy gates, egress DLP rules and device approvals | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/reference/modules/inferenceproxy/) |
| Permissions | `/permissions` | Identity, roles and approvals | `governance:identity:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Rate limits | `/rate-limits` | Anthropic rate-limit inventory and its availability (read-only reference) | `models:ratelimits:read` | [reference/modules/x-models](/reference/modules/x-models/) |
| Data residency | `/residency` | Pin each org to a region, or leave it unpinned | `system:admin` | [reference/modules/xiii-compliance](/reference/modules/xiii-compliance/) |
| Routine policies | `/routine-policies` | Cadence floors, concurrency caps, approval requirements, cron allowlists and blocked environments for Claude Code routines | `governance:routine:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |

### Prove

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Claude Code Adoption | `/adoption` | Productivity, acceptance & model mix | `adoption:metrics:read` | [reference/modules/claudeadoption](/reference/modules/claudeadoption/) |
| Agent Artifacts | `/agent-artifacts` | Skills, MCP extensions and instruction files — registry, posture and supply-chain BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/reference/modules/xxiii-model-operations/) |
| Supply chain | `/attestation` | Release attestation — SLSA, SBOM, VEX and Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/how-to/verify-a-release/) |
| Audit ledger | `/audit` | Tamper-evident evidence ledger | `audit:read` | [reference/modules/ix-security](/reference/modules/ix-security/) |
| Compliance | `/compliance` | Frameworks, controls and evidence | `compliance:framework:read` | [reference/modules/xiii-compliance](/reference/modules/xiii-compliance/) |
| Dashboards | `/dashboards` | Executive KPIs and reporting | any signed-in user | [reference/modules/xxi-executive-dashboards](/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Quality, evals and regression | `evals:run:read` | [reference/modules/xii-evals](/reference/modules/xii-evals/) |
| Cost & FinOps | `/finops` | Token cost, budgets and spend | `finops:spend:read` | [reference/modules/xi-finops](/reference/modules/xi-finops/) |
| Posture export | `/posture-export` | Export ground-truth posture for a control tower | `posture:export:read` | [reference/modules/posture-export](/reference/modules/posture-export/) |
| Recordings | `/recordings` | Privileged session recording and replay | `recording:session:admin` | [reference/modules/recording](/reference/modules/recording/) |
| Red-teaming | `/red-team` | Adversarial testing of your agents | `redteam:target:read` | [reference/modules/xviii-redteam](/reference/modules/xviii-redteam/) |
| Reports | `/reporting` | Generate and download governance reports | `reporting:report:read` | [reference/modules/reporting](/reference/modules/reporting/) |
| Security | `/security` | Guardrails, forensics and anomalies | `security:finding:read` | [reference/modules/ix-security](/reference/modules/ix-security/) |
| Session viewer | `/session-viewer/$id` (deep link only) | The full timeline of one recorded session, reached from a row in Recordings rather than from the sidebar. | `recording:session:admin` | [reference/modules/recording](/reference/modules/recording/) |
| Team costs | `/team-costs` | Cost attribution by team: sessions, tokens and spend, with a daily trend and a per-project and per-model breakdown | `finops:spend:read` | [reference/modules/xi-finops](/reference/modules/xi-finops/) |

### Sign-in, setup and account

These are mounted outside the feature registry. The ones marked **no sign-in** are
served before there is a session — they are the only console routes that are.

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Accept an invitation | `/accept-invite` | Where an emailed invitation link lands: the invitee sets a password and joins the workspace, with no prior session. | **no sign-in** | — |
| AI | `/areas/ai` | Directory of the AI area: observe and operate sessions, provider profiles and environments, models, specialized execution and provider reference. Lists the entries your permissions allow. | any signed-in user | — |
| Automation | `/areas/automation` | Directory of the Automation area: flows and orchestration, events and notifications. Lists the entries your permissions allow. | any signed-in user | — |
| Data & context | `/areas/data-context` | Directory of the Data & context area: capabilities, knowledge and artifacts. Lists the entries your permissions allow. | any signed-in user | — |
| Deployment | `/areas/deployment` | Directory of the Deployment area: how deployments are prepared and controlled. Lists the entries your permissions allow. | any signed-in user | — |
| Infrastructure | `/areas/infrastructure` | Directory of the Infrastructure area: the inventory and workspaces of the estate. Lists the entries your permissions allow. | any signed-in user | — |
| Observability & evidence | `/areas/observation` | Directory of the Observability & evidence area: status and activity, costs and adoption, audit and recordings, evaluation and evidence. Lists the entries your permissions allow. | any signed-in user | — |
| Security & identity | `/areas/security-identity` | Directory of the Security & identity area: identity and access, policies, protection and response, governance boundaries. Lists the entries your permissions allow. | any signed-in user | — |
| System & settings | `/areas/system` | Directory of the System & settings area: administration, installation and maintenance, developer tools and personal preferences. Lists the entries your permissions allow. | any signed-in user | — |
| Work & communications | `/areas/work-communications` | Directory of the Work & communications area: the durable cross-session work backlog and governed communications. Lists the entries your permissions allow. | any signed-in user | — |
| Sign in | `/login` | The credential and token sign-in page for an already provisioned account. | **no sign-in** | — |
| Settings | `/settings` | Workspace and account settings | any signed-in user | — |
| First-run setup | `/setup` | The one-time page that turns a fresh deployment into a usable one: it consumes the setup token and creates the first owner account. | **no sign-in** | — |
| Public status | `/status-page` | Component health for people who are not signed in, refreshed on its own while the page is open. | **no sign-in** | — |

<!-- END GENERATED olivares-console-routes -->

## What this page does not tell you

It is a map, not a manual. It says which screens exist, where they live and who may open
them; it does not walk you through a task. For those, start at
[Paths by role](/start/paths-by-role/) or the [how-to guides](/how-to/self-hosting/).

Screens whose backend is deny-closed until an operator provisions it appear here like any
other — the route exists and the permission is real. Which module actuates and which is
gated is recorded in the [modules overview](/reference/modules/overview/), and the
[honesty and limits](/start/honesty-and-limits/) page states the general rule. An area
directory listing is not a live capability or readiness reading.
