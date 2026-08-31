---
title: "OpenTelemetry GenAI (any instrumented agent)"
description: >-
  Feed the access map and FinOps from ANY OTel-instrumented agent — LangChain,
  LangGraph, CrewAI, AutoGen, Google ADK and peers — via the vendor-neutral
  gen_ai.* ingest profile: opt-in, pinned to semconv v1.41.1, normalizing the
  three GenAI dialects that coexist in real fleets.
sidebar:
  order: 4
---

Claude Code is the canonical cooperative source, but it is not the only
cooperative agent you run. The same connector that receives Claude Code's
telemetry (`kind: claude`) carries an **opt-in, vendor-neutral OpenTelemetry
GenAI profile**: point any OTel-instrumented agent or framework at the same
OTLP receiver, and its `gen_ai.*` telemetry feeds the access-map and cost
pipeline — LangChain, LangGraph, CrewAI, AutoGen, Google ADK and anything
else that emits the GenAI semantic conventions on spans or log events.

## Why it is opt-in

The OpenTelemetry GenAI conventions are **Development status** (pre-stable)
upstream, and three dialects genuinely coexist in 2026 fleets. So the profile
is off by default and gated exactly like the OTel SDKs gate it — by the
opt-in token:

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` mirrors `OTEL_SEMCONV_STABILITY_OPT_IN`: a comma-separated
list that must contain `gen_ai_latest_experimental`. With the profile **off**,
a `gen_ai.*` record still feeds the session-liveness watchdog but is **not
mapped** — honest absence, not silent ingestion.

## What the normalizer accepts

The profile is pinned to **semconv v1.41.1** and normalizes the three GenAI
dialects that coexist in real estates, stamping every normalized event with
the dialect's semconv pin so provenance survives:

| Dialect | Shape |
|---|---|
| Legacy OpenLLMetry | indexed `gen_ai.prompt.{i}.*` attributes |
| v1.36 and prior | the deprecated per-message events |
| v1.37+ | the `messages` generation |

On top of the message shapes it maps the **`mcp.*` conventions (v1.39)** and
the **`invoke_agent` client/internal split plus `invoke_workflow` (v1.41)** —
so framework-orchestrated agent and workflow invocations land as structured
topology, not noise. Span-based emission (how LangGraph, LangChain, CrewAI,
AutoGen and Google ADK instrument) and log-based emission are both ingested.

Cost samples are de-duplicated by W3C span id, so an agent whose telemetry
arrives on both the span and log paths is never double-billed.

## Wiring an agent to it

The receiver is the connector's own OTLP endpoint (gRPC `127.0.0.1:4317`,
HTTP `127.0.0.1:4318` by default). On the agent side, standard OTel SDK
configuration applies — exporter endpoint to the loopback receiver, and the
GenAI opt-in if your instrumentation gates on it:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[The same loopback rule as Claude Code]
The cooperative ingest is **unauthenticated** and binds loopback by default.
Anything that can reach the socket can forge telemetry — keep it on loopback
(`allow_public_bind` exists and is deliberately marked DANGEROUS). Off-host
agents are the kernel backstop's job, not a public OTLP port's.
:::

## What you'll see in the console

Instrumented sessions appear in **Sessions** as live activity, attributed to
the emitting agent; their model calls feed **Cost & FinOps**; MCP and tool
spans contribute edges to the **Access map** like any cooperative source:

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="The Sessions view showing live agent session activity from cooperative telemetry." />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="The Sessions view showing live agent session activity from cooperative telemetry." />

## Honest limits

- **Pre-stable conventions, pinned ingest.** The profile is pinned to
  v1.41.1; when upstream moves, the pin moves by a deliberate update, not by
  silent drift. Instrumentation that emits a fourth dialect is not guessed
  at.
- **Cooperative means cooperative.** An agent that does not emit is invisible
  to this path — that is what [eBPF/Tetragon](/how-to/connectors/ebpf-tetragon/)
  and store-native audit are for.
- **Framework span-kind quirks are real.** Some frameworks emit spans whose
  kind does not match the v1.41 client/internal rules; the normalizer maps
  what it can prove and leaves the rest unmapped rather than misattributed.

## Related

- [Connect Claude Code](/how-to/connect-claude-code/) — the same receiver,
  Claude-specific surface.
- [Enterprise OTel for Claude Code](/how-to/claude-code-enterprise-otel/) —
  fleet-wide telemetry posture.
- [Events reference](/reference/events/) — the normalized observations this
  produces.
