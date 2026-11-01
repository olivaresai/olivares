---
title: Console reference — every screen and the permission it needs
description: >-
  Every route the Olivares AI console publishes, grouped by the console's areas,
  with the RBAC permission each one requires and the reference page its in-product
  help link opens. Generated from the console's own route census.
---

:::note[Edition]
Directory roster sources (LDAP, Okta/Entra and agent/workload IdPs), managed identity lifecycle and the inbound SCIM server require the Business/Enterprise add-ons. Community keeps generic source ingestion, the resource catalog, security findings, the access map, the identity inventory, single-IdP SSO, local roles and bindings, policies and approvals. Community hides the paid console panels and CLI commands; their published API routes return HTTP `501`. Selecting a module does not add its Business implementation.
:::

This page is the map of the console. It lists **every route the application mounts** —
not a selection, not the ones somebody remembered to write up — with the permission a
principal needs to enter it and where to read more.

The console has **one navigation: the left sidebar**. It lists the places in four groups —
**Work** (Now, Sessions, Files & workspaces, Work & communications), **Tools** (AI tools,
Models, MCP & skills, Sources), **Control** (Rules & approvals, Usage & cost, Activity &
audit, Access map) and **Admin** (People & groups, Security, Operations, License) — with
Settings at the bottom. Every screen sits in one place, and the row of links above a screen
lists the other screens of that place. A place appears only when your permissions allow at
least one of its screens, and a Business screen only on the Business edition. The sidebar
folds to its icons and opens again; on a narrow screen the same sidebar opens as a drawer
from the menu button. The top bar holds the page title, search (⌘K or Ctrl+K), notifications
and the account menu. The command palette searches the same screens with the same
permissions. The former area directory addresses (`/areas/…`) open the place that now holds
their screens, keeping the query and the fragment.

A new installation lists the first job only: Now, Sessions, AI tools,
Approvals, the setup wizard and Settings. Every other screen is a preview there: its
address, API and CLI keep working, and navigation, the command palette and the shortcuts
do not list it. An installation that existed before the upgrade lists every screen it did,
and so does one whose administrator has chosen its modules, from its next start, or that
runs the seeded demo.

It is **generated**. The roster comes from `web/src/features/route-census.json`, the
append-only census that `registry.route-conservation.test.ts` pins against the built
router, so a screen cannot be added, moved or lost without this page changing with it.
Each screen's name and one-line description are the **console's own strings**, taken from
the same translation catalog the console renders, so what you read here is what you see in
the product. The tables below group those rows by area, after Now (the overview) and
before sign-in, setup and account. The nine former area directory addresses sit with the
routes mounted outside the feature registry; each now redirects to its place.

:::note[Permissions are enforced by the engine, not by this table]
The `Requires` column names the permission the console checks before offering a route,
using the effective permissions returned by the engine. The engine authorizes API
requests independently, including requests made outside the console. Navigation
visibility does not establish that a module is configured or ready to run.
See [Roles and permissions](/reference/modules/vi-governance/).
:::

## How to read this page

- **Screen** — the name the row above a screen and the command palette use.
- **Path** — the URL under your deployment's console origin. It is a published contract:
  a bookmark, a runbook deep link and a docs cross-reference are all this string.
- **Requires** — the RBAC permission. `any signed-in user` means the route is open to
  every authenticated principal; **no sign-in** means it is served before there is a
  session at all.
- **Reference** — the page the console's own help link opens for that screen.

The headings below are the registry areas the screens are grouped by in the route census:
Infrastructure, AI, Data & context, Work & communications, Automation, Security & identity,
Delivery, Oversight, then System & settings.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

The console publishes **66 routes**. Every one of them is in the tables below, with the
permission it requires and the reference page its in-product help link opens.

### Now

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Now | `/` | Estate overview and health at a glance | any signed-in user | [docs home](/) |

### Infrastructure

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Estate | `/estate` | Inspect configured resources and their relationships, including work item dependencies. | `sessions:run:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Inventory | `/inventory` | Discover and catalog the agents, MCP servers and models that connectors observed. | `inventory:catalog:read` | [reference/modules/i-inventory](/reference/modules/i-inventory/) |
| Workspace | `/workspace` | Agents, sessions, resources and activity scoped to one workspace | `tenant:read` | [reference/modules/xx-multi-tenancy](/reference/modules/xx-multi-tenancy/) |

### AI

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| AI tools | `/agent-tools` | See the accounts and API keys sessions run as; installing and signing in the tools is for deployment administrators | `sessions:account:read` | [how-to/add-a-provider](/how-to/add-a-provider/) |
| Operate sessions | `/agentops` | Start sessions and follow what each one does, including the ones Olivares finds | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/how-to/run-claude-code-with-olivares/) |
| Model Operations | `/model-operations` | Owned models, admission and deployments | `models:registry:read` | [reference/modules/xxiii-model-operations](/reference/modules/xxiii-model-operations/) |
| Models | `/models` | Models, routing and governance | `models:catalog:read` | [reference/modules/x-models](/reference/modules/x-models/) |
| Platforms | `/platforms` | Provider platform surfaces, compliance matrix and per-platform model lifecycle — reference, not your servers | `models:platforms:read` | [reference/modules/x-models](/reference/modules/x-models/) |
| Provider accounts | `/provider-accounts` | List named provider accounts and adopt an existing provider profile as an account | `sessions:account:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Source bindings | `/provider-bindings` | Link configured sources to provider profiles | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Provider profiles | `/provider-profiles` | Register and administer the provider homes sessions launch under, and read their configuration on demand | `sessions:profile:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Providers | `/providers` | Register the API keys and endpoints sessions launch with; test, rotate and revoke them | `sessions:provider:read` | [how-to/add-a-provider](/how-to/add-a-provider/) |
| Rate limits | `/rate-limits` | Anthropic rate-limit inventory and its availability (read-only reference) | `models:ratelimits:read` | [reference/modules/x-models](/reference/modules/x-models/) |
| Reach | `/reach` | See which destinations each tool can reach, and which are waiting for approval. | `accessmap:graph:read` | [reference/modules/iii-access-map](/reference/modules/iii-access-map/) |
| Sandbox | `/sandbox` | Isolated agent testing and replay | `sandbox:run:read` | [reference/modules/xvii-sandbox](/reference/modules/xvii-sandbox/) |
| Sessions | `/sessions` | Start sessions and follow what each one does, including the ones Olivares finds | `sessions:live:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Voice | `/voice` | Voice and realtime sessions | `voice:session:read` | [reference/modules/xvi-voice](/reference/modules/xvi-voice/) |
| Workspace templates | `/workspace-templates` | Reusable snapshots of hooks, settings, connectors and policies for workspace sessions | `sessions:template:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |

### Data & context

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Agent Artifacts | `/agent-artifacts` | Skills, MCP extensions and instruction files — registry, posture and supply-chain BOM | `models:registry:read` | [reference/modules/xxiii-model-operations](/reference/modules/xxiii-model-operations/) |
| MCP & skills | `/capabilities` | Govern MCP servers, skills and tools | `capabilities:catalog:read` | [reference/modules/v-capabilities](/reference/modules/v-capabilities/) |
| Catalog | `/catalog` | Curated, approved agents and capabilities | `catalog:entry:read` | [reference/modules/xiv-catalog](/reference/modules/xiv-catalog/) |
| Knowledge | `/knowledge` | Knowledge bases, RAG and data lineage | `knowledge:kb:read` | [reference/modules/viii-knowledge](/reference/modules/viii-knowledge/) |

### Work & communications

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Communications | `/communications` | Channels, direct notices and the personal inbox of the selected workspace | `sessions:channel:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Channel administration | `/communications/administration` | Administer channels: configuration and grant history | `sessions:channel:admin` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Handoffs | `/communications/handoffs` | Offers of work responsibility addressed to you: read the context and accept or reject | `sessions:delivery:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Communications inbox | `/communications/inbox` | Messages addressed to you: open and acknowledge each one | `sessions:delivery:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| New channel | `/communications/new` | Create a channel with explicit initial grants | `sessions:channel:write` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Protocol bindings | `/communications/protocol-bindings` | Compose and reconcile governed A2A and MCP bindings | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |
| Work | `/work` | Shared work: items, dependencies, acceptance and decisions | `sessions:work:read` | [reference/modules/ii-sessions](/reference/modules/ii-sessions/) |

### Automation

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Alerting | `/alerting` | Route findings to destinations and inspect deliveries | `notify:route:read` | [reference/modules/xv-notify](/reference/modules/xv-notify/) |
| Webhooks & events | `/eventing` | Outbound webhook subscriptions, the captured event log, deliveries and the dead-letter queue | `eventing:subscription:read` | [reference/modules/eventing](/reference/modules/eventing/) |

### Security & identity

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Access map | `/access-map` | What each agent reads and writes (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/reference/modules/iii-access-map/) |
| Policy export | `/agentcore-export` | Plan, review and apply the projection of this tenant's governance rules onto AWS AgentCore as Cedar policies; planning writes nothing | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Managed policy | `/claude-policy` | Managed policy, hooks, MCP, sandbox and policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/how-to/connectors/claude-code-hooks-pep/) |
| Identity & access | `/identity` | SSO, SCIM and the identities your agents use | `governance:identity:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Inference proxy | `/inference-proxy` | Proxy gates, egress DLP rules and device approvals | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/reference/modules/inferenceproxy/) |
| Kill switch | `/killswitch` | Emergency stop, dual-control recovery and guardian containment | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/how-to/cookbook/kill-switch-drill/) |
| Permissions | `/permissions` | Identity, roles and approvals | `governance:identity:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Routine policies | `/routine-policies` | Cadence floors, concurrency caps, approval requirements, cron allowlists and blocked environments for Claude Code routines | `governance:routine:read` | [reference/modules/vi-governance](/reference/modules/vi-governance/) |
| Security | `/security` | Guardrails, forensics and anomalies | `security:finding:read` | [reference/modules/ix-security](/reference/modules/ix-security/) |
| Vault | `/vault` | Your passwords, notes and authenticator codes | `tenant:read` | [reference/console](/reference/console/) |

### Delivery

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Git publication | `/git-publication` | Push commits, open pull requests and merge through approved Git targets | `gitpublish:target:read` | [reference/modules/gitpublish](/reference/modules/gitpublish/) |

### Oversight

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Adoption | `/adoption` | Productivity, acceptance & model mix | `adoption:metrics:read` | [reference/modules/claudeadoption](/reference/modules/claudeadoption/) |
| Supply chain | `/attestation` | Release attestation — SLSA, SBOM, VEX and Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/how-to/verify-a-release/) |
| Audit ledger | `/audit` | Tamper-evident evidence ledger | `audit:read` | [reference/modules/ix-security](/reference/modules/ix-security/) |
| Dashboards | `/dashboards` | Executive KPIs and reporting | any signed-in user | [reference/modules/xxi-executive-dashboards](/reference/modules/xxi-executive-dashboards/) |
| Evals | `/evals` | Quality, evals and regression | `evals:run:read` | [reference/modules/xii-evals](/reference/modules/xii-evals/) |
| Health & SLA | `/health` | Agent and MCP uptime and SLAs | `health:status:read` | [reference/modules/xxii-health](/reference/modules/xxii-health/) |
| Observability | `/observability` | Ingestion health by standard and trace drill-down | `health:status:read` | [reference/modules/observability](/reference/modules/observability/) |
| Retention | `/retention` | How long each class of record is kept, the sweep that applies it and its sealed certificates | `compliance:retention:read` | [reference/modules/xiii-compliance](/reference/modules/xiii-compliance/) |
| Budgets | `/stored-budgets` | Consumption against each stored limit. Reserved capacity counts toward the limit. | `finops:budget:read` | [reference/modules/xi-finops](/reference/modules/xi-finops/) |
| Usage | `/usage` | Each account's official 5-hour and weekly limits, nearest first, and today's tokens and cost. | `sessions:run:read` | [reference/console](/reference/console/) |

### System & settings

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| API Playground | `/api-playground` | Interactively explore and test the control-plane API | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/reference/modules/xix-api-manage-as-code/) |
| Backups | `/backups` | Encrypted disaster-recovery snapshots of the control plane: create, schedule, download and restore | `system:admin` | [how-to/backup-and-restore](/how-to/backup-and-restore/) |
| Administration | `/console` | Users, SSO/IdP, workspaces, agent groups, roles, secrets, connectors, API keys and this installation's license | `tenant:admin` | [reference/modules/xx-multi-tenancy](/reference/modules/xx-multi-tenancy/) |
| Source diff | `/console/sources/diff` | Compare a base and a head revision of a connected Git repository, file by file | `system:admin` | [reference/console](/reference/console/) |
| Logs | `/logs` | The live engine log stream, with level and module filters, search and pause | `system:admin` | [how-to/troubleshooting](/how-to/troubleshooting/) |
| Setup wizard | `/onboarding` | Step-by-step deployment configuration | `system:admin` | [start/quickstart](/start/quickstart/) |
| Updates | `/updates` | The version this engine runs, the newest signed release in its channel, and one action to install it | `system:admin` | [how-to/upgrade-and-rollback](/how-to/upgrade-and-rollback/) |

### Sign-in, setup and account

These are mounted outside the feature registry. The ones marked **no sign-in** are
served before there is a session — they are the only console routes that are.

| Screen | Path | What it is | Requires | Reference |
|---|---|---|---|---|
| Accept an invitation | `/accept-invite` | Where an emailed invitation link lands: the invitee sets a password and joins the workspace, with no prior session. | **no sign-in** | — |
| Sign in | `/login` | The credential and token sign-in page for an already provisioned account. | **no sign-in** | — |
| Pair a device | `/pair` | Where a one-time device-pairing link lands: the new device signs in with no prior session. | **no sign-in** | — |
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
