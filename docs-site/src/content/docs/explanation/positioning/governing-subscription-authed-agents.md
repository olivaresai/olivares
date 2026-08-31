---
title: Governing subscription-authed Claude Code & Codex
description: >-
  How Olivares AI governs coding agents that authenticate with a subscription —
  Claude Code on Pro/Max, Codex on ChatGPT — without ever sitting in the middle
  of that subscription. Three mechanisms (observe, managed-settings + hooks, an
  API-key gateway), one red line: we never route your subscription credential.
sidebar:
  order: 6
---

The hardest agent to govern is the one a developer logged into with a personal
or company **subscription**: Claude Code signed in with Pro/Max, or Codex signed
in with ChatGPT. The same shape applies to Grok Build, and to any CLI agent that
authenticates the person rather than the workload — the mechanisms below are about the
*shape* of that login, not about one vendor. It runs on a laptop, it authenticates with
an OAuth credential,
and it is exactly the surface a cloud-provider guardrail in the inference path
never sees (see
[the wedge](/explanation/positioning/where-olivares-fits-vs-your-gateway/)). The
tempting "solution" — put a service in front of it that holds the subscription
and routes its traffic — is one Olivares AI **will not** build, because the model
providers prohibit it and because it would make our control plane a single point
of credential compromise.

This page is the honest account of how we govern these agents **without ever
brokering the subscription**: what we observe, where we enforce, and the one
narrow path where a gateway is appropriate (and it is never the subscription's).

:::danger[The red line: we never route your subscription]
Olivares AI **never holds, proxies, or routes a third-party subscription
credential.** Anthropic's own policy states: *"Anthropic does not permit
third-party developers to offer Claude.ai login or to route requests through
Free, Pro, or Max plan credentials on behalf of their users"*
([Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance),
fetched 2026-06-21 — the prohibition names the three consumer plans **Free, Pro,
Max**). OpenAI's terms work the same way for a consumer ChatGPT/Codex login. Our
posture is stricter than the line itself: we route **no** subscription OAuth, of
**any** plan. Governance happens *around* the agent, never *inside* its
credential.
:::

## Why brokering the subscription is off the table

It is worth being precise about the rule, because a buyer's counsel will check
it. Anthropic's policy draws two lists that must not be conflated:

- **Who may use OAuth at all** — five plans: *"OAuth authentication is intended
  exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise
  subscription plans and is designed to support ordinary use of Claude Code and
  other native Anthropic applications."*
- **What a third party may not do** — route on behalf of users: *"Anthropic does
  not permit third-party developers to offer Claude.ai login or to route requests
  through Free, Pro, or Max plan credentials on behalf of their users."*

The prohibition explicitly names the **consumer** plans (Free, Pro, Max). The
page does not, conversely, grant anyone permission to route Team or Enterprise
seats — it is silent on that, and we do not read silence as a licence. For
*developers building tooling*, Anthropic's own guidance points away from
subscription OAuth entirely: *"Developers building products or services that
interact with Claude's capabilities, including those using the Agent SDK, should
use API key authentication through Claude Console or a supported cloud provider."*
([source](https://code.claude.com/docs/en/legal-and-compliance); plan-by-terms
split: Team/Enterprise/API under Commercial Terms, Free/Pro/Max under Consumer
Terms.)

Our Codex connector encodes the identical discipline in code, by design: the
automation credential is an OpenAI **API key** or a **workspace access token**,
never a personal ChatGPT subscription — *"proxying it for third-party/programmatic
use violates OpenAI's terms exactly as a consumer Claude subscription does for
Anthropic. There is no subscription config field by design"*
(`connectors/codex/codex.go`). So the red line is not a marketing promise bolted
on afterward; it is the shape of the product.

## Three mechanisms, none of them the subscription

We govern a subscription-authed agent through three independent channels. The
first two never touch inference at all; the third touches it only for traffic
that authenticates with an **API key**, never a subscription.

### 1. Observe — telemetry, usage, and posture

Claude Code emits OpenTelemetry, and an administrator can turn it on for the
fleet from the managed tier: *"Administrators can configure OpenTelemetry settings
for all users through the managed settings file"*
([Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)). We
ingest that **gen-ai signal** — sessions, tokens, cost, tool activity — and turn
it into the access map and posture findings. Crucially, this is **minimal-data by
construction on Claude Code's side too**: prompt content is *"redacted by
default"* and tool details, tool content, and raw API bodies are each *"(default:
disabled)"* (same source). We consume usage and metadata, not conversations.

For Codex, the same observe channel is the connector's ingest of the Analytics
and Compliance/Audit APIs — usage, adoption, and immutable audit records turned
into cost samples and tamper-evident evidence, carrying *"never prompt/diff
content or key values"* (`connectors/codex/codex.go`).

→ [Ingest OpenTelemetry GenAI](/how-to/connectors/otel-genai/) ·
[Enterprise OTel for Claude Code](/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + hooks — the in-process PEP

Observation is not enforcement. The enforcement channel for Claude Code is its
**managed settings** file at the OS-policy tier, which carries a non-overridable
`PreToolUse` hook that calls back to the Olivares decision point before every tool
runs. Anthropic documents the property we rely on: *"Environment variables defined
in the managed settings file have high precedence and cannot be overridden by
users"*, and managed settings *"can be distributed via MDM"*
([monitoring](https://code.claude.com/docs/en/monitoring-usage)).

Olivares renders that file (`olivares agent managed-settings`) with
`allowManagedHooksOnly` so a developer's own hook can never precede or undercut
the governed one, and the per-session endpoint and bearer are injected at launch
— not written into the static file. The decision itself is **deny-closed at every
edge**: a tool-call is allowed only when a firm identity resolves, the policy
disposition is not `deny`, the live policy engine does not forbid it, and — for an
`ask` — a human approval is bound to the exact plan hash. An emergency stop
([kill switch](/reference/glossary/#kill-switch)) outranks everything, including an
active break-glass grant.

This is the mechanism the
[Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/) page documents
operationally, and it is what makes us able to *govern* the local dev agent, not
merely watch it — the second of the
[three lanes](/explanation/positioning/analyst-vocabulary/#the-three-lanes-this-vocabulary-points-at).

### 3. Gateway for an API key — never for OAuth

There is exactly one path where Olivares sits in the inference request line, and
it exists only for callers that **do not** use Claude Code's managed-settings
channel: raw SDK or `curl` traffic authenticated with an **API key** (or a
Bedrock/Vertex-equivalent). Claude Code routes such requests with
`ANTHROPIC_BASE_URL` — *"To route requests through a custom API endpoint, set the
`ANTHROPIC_BASE_URL` environment variable instead"* — and authenticates a gateway with a
bearer via `ANTHROPIC_AUTH_TOKEN`, *"when routing through an LLM gateway or proxy
that authenticates with bearer tokens rather than Anthropic API keys"*
([Claude Code IAM](https://code.claude.com/docs/en/iam)). Pointed at the Olivares
inline inference proxy, that traffic gets a governed pipeline — residency, model
access, context-window, DLP, budget, recording — before it is forwarded.

The boundary is absolute: **this path carries API-key / bearer traffic, never a
subscription's OAuth credential.** It is the enforcement seam for the SDK/`curl`
callers that managed settings cannot reach, and nothing more.

## The honesty box: verified-deployed, not unbypassable

:::caution[Enforcement we can prove is *deployed*, not enforcement that *cannot* be evaded]
The managed-settings + hook PEP is **deny-closed** and **non-overridable by the
user through settings** — but it is not magic. A developer who points
`ANTHROPIC_BASE_URL` at their own endpoint sends inference somewhere else
entirely; our own engineering note says so plainly: *"a custom
`ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
(`modules/inferenceproxy/doc.go`). So we never claim the PEP is impossible to
escape. Instead we claim two things we can stand behind:

1. **It is verified-deployed.** Olivares attests that the managed settings and
   the PEP hook are actually present on the host — an un-provisioned host runs
   ungoverned-but-observed, and that is visible, not hidden.
2. **The bypass is itself a finding.** A non-default `ANTHROPIC_BASE_URL` on a
   host surfaces as a posture finding, and a managed environment that pins a base
   URL diverging from the authorized Olivares gateway raises a **drift** finding
   (`connectors/claude-config`, `connectors/managedsettings`). The evasion does
   not go quiet; it lights up.

"Verified-deployed, evasion-as-finding" is the honest enforcement story for any
agent that runs on a machine the developer controls. We will not sell you
"unbypassable."
:::

## The Codex asymmetry, stated honestly

Claude Code and Codex are not symmetric, and the difference matters. For Codex
authenticated by ChatGPT there is **no documented equivalent of
`ANTHROPIC_BASE_URL`** — OpenAI's
[managed-configuration page](https://developers.openai.com/codex/enterprise/managed-configuration)
documents no setting or environment variable to route inference through a custom
base URL or gateway (verified by fetch, 2026-06-21; an absence on that page, not
a proof none exists elsewhere). So we **do not** govern Codex by intercepting its
inference.

Instead we govern it where OpenAI *does* give administrators enforced controls.
Codex managed configuration lets an enterprise set *"Requirements: admin-enforced
constraints that users can't override"* that *"constrain security-sensitive
settings (approval policy, approvers reviewer, automatic review policy, sandbox
mode, permission profiles, web search mode, managed hooks, and optionally which
MCP servers users can enable)"* (same source). Olivares authors and attests those
requirements (`connectors/codex-managed-config`) — approval policy, sandbox mode,
the MCP allowlist, redacted telemetry (`log_user_prompt = false`) — and ingests
Codex's Analytics and Compliance evidence. Governance through configuration and
evidence, not through a man-in-the-middle on the model call.

## In one table

| Channel | What it does | Touches inference? | The credential |
|---|---|---|---|
| **Observe** | Usage, cost, tool activity → access map + posture; Codex Analytics/Compliance → ledger | No | None — telemetry only, content redacted by default |
| **Managed settings + hooks** | Deny-closed `PreToolUse` PEP on Claude Code, non-overridable via settings | No | The agent's own; we never see it |
| **Gateway (API key only)** | Governed pipeline for raw SDK/`curl` callers via `ANTHROPIC_BASE_URL` | Yes | **API key / bearer — never subscription OAuth** |
| **Codex managed-config** | Admin-enforced requirements (approval/sandbox/MCP) + evidence ingest | No | The org's; configuration, not interception |

## Related

- [Where Olivares fits vs your gateway / Guardrails](/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — why none of this competes with your AI gateway.
- [Olivares AI vs WitnessAI](/explanation/positioning/vs-witnessai/) — the
  head-to-head on governing agents in IDEs.
- [Claude Code hooks & the PEP](/how-to/connectors/claude-code-hooks-pep/) and
  [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/) — the
  operational how-to.
- [Honesty & limits](/start/honesty-and-limits/) — the standing commitment this
  page is written under.
