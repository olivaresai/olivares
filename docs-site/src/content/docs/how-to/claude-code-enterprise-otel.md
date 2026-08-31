---
title: "Configure enterprise OpenTelemetry for Claude Code"
description: >-
  The recommended enterprise telemetry posture for a Claude Code fleet:
  managed-settings env that turns the sanctioned OTel export on, operator labels
  via OTEL_RESOURCE_ATTRIBUTES that become FinOps dimensions, the tracing beta
  for subagent hierarchy, and the privacy knobs — with their duties — spelled out.
---

Claude Code's OpenTelemetry export is the **sanctioned observation path** for a
governed fleet: it is not plan-gated, it carries session-attributed telemetry,
and the managed settings tier can turn it on for every developer — without
proxying anything. This page is the *enterprise* configuration on top of
[Connect Claude Code](/how-to/connect-claude-code/): what to set fleet-wide, what
each knob buys you, and what duty it creates. Key names and semantics below were
verified against Claude Code's own documentation on 2026-06-10 (client 2.1.17x);
re-check them there before encoding new ones — they evolve quickly.

:::note[Managed env governs Claude Code only]
The managed `env` block configures the **Claude Code process**. OTEL_* variables
are **not** propagated to subprocesses (Bash commands, hooks, MCP servers); only
`TRACEPARENT` is inherited by shell subprocesses while tracing is active. Plan
subprocess observability separately (the kernel/eBPF backstop).
:::

## What you get

| Knob | What it buys | Duty it creates |
|---|---|---|
| Managed telemetry `env` | Every session exports OTLP to your collector — observation that survives a developer's own config | None — structural telemetry by default |
| `OTEL_RESOURCE_ATTRIBUTES` | Org-defined labels (team, project, cost center) on **every metric datapoint and event record**; the control plane routes them into FinOps spend dimensions | Keep label values non-sensitive; the connector allowlists and scrubs them |
| Tracing beta | `claude_code.llm_request` / `claude_code.tool` spans carry `agent_id` / `parent_agent_id` — the **per-instance subagent hierarchy** in the access graph | Beta surface: verify on upgrade |
| `OTEL_LOG_TOOL_DETAILS=1` | `tool_parameters` on tool events — including **which command was rejected** on a denied tool decision | Tool inputs leave the host: a residency/redaction duty you must own |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint` (cli / sdk-ts / claude-vscode …) — which surface launched each session | None (low-cardinality label) |

## Step 1 — turn the export on from the managed tier

Author the telemetry `env` in your managed settings policy (the
`managed-settings` connector's `TelemetryEnv` helper renders exactly this
posture): enable telemetry, point the OTLP exporter at the control-plane
collector, and export both metrics and logs. Defer the full variable reference to
Claude Code's own monitoring documentation — do not hand-copy values from here.

:::caution[Never inline collector credentials]
A managed-settings file is plaintext on every host. The authoring layer rejects
`OTEL_EXPORTER_OTLP_HEADERS` with a value for exactly this reason — authenticate
the collector with mTLS or a secret-manager reference, never an inline token.
:::

Content capture (prompts, tool bodies) stays **off** unless you opt in — and the
control-plane connector independently retains structural data only, whatever the
client emits.

## Step 2 — label the fleet for FinOps

Set `OTEL_RESOURCE_ATTRIBUTES` in the same managed env, using strict W3C Baggage
formatting (percent-encode values; no spaces or quotes):

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

Since client 2.1.161 these values ride **every metric datapoint and event
record**, not just the OTLP resource block — and custom keys never override the
standard attributes. On the control plane, list the keys you honor in the claude
connector's `resource_labels` allowlist; the connector scrubs the values and
attaches them as labels on the session's identity edges and on every cost
sample. FinOps promotes `team` and `project` to first-class spend dimensions, so
"slice Claude Code spend by team" works end to end. Keys not on the allowlist
are dropped — minimal data by default.

## Step 3 — subagent hierarchy (tracing beta)

Enable the enhanced-telemetry beta plus a traces exporter in the managed env to
get spans. The subagent identity attributes (`agent_id`, `parent_agent_id`) are
**span-only** — they appear on no metric and no log event — and live on the
`claude_code.llm_request` (since 2.1.139) and `claude_code.tool` (since 2.1.145)
spans. The connector maps them into the access graph as:

- `session → identity.subagent` — the subagent **instance** that acted, and
- `parent agent → identity.subagent` — **who spawned it** (absent for agents the
  main session spawned directly).

This is what makes two concurrent subagents of the same type distinguishable —
the `Agent` tool's `subagent_type` alone is a type label, not an instance.

## Step 4 — optional fidelity knobs

- `OTEL_LOG_TOOL_DETAILS=1` adds `tool_parameters` to tool events — on denied
  tool decisions too (since 2.1.157), so a rejection finding can name the
  sanitized command that was blocked. The connector reduces inputs to redacted
  resource references at ingest and never stores them raw; but the values DO
  leave the developer's host, so enabling this is a deliberate residency
  decision.
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` adds `app.entrypoint` to all metrics
  and events (default off). The connector records it as session topology — an
  SDK-embedded fleet has a different risk posture than interactive CLI use.

## Honest limits of this path

- **Unauthenticated loopback ingest.** The cooperative receiver binds loopback
  by default and must stay there; anything reachable can forge telemetry (see
  [Connect Claude Code](/how-to/connect-claude-code/)).
- **Subprocesses are not covered.** OTEL_* does not reach Bash/hook/MCP
  subprocesses; only `TRACEPARENT` is inherited under tracing.
- **The admin-plane feed cannot see third-party providers.** The Claude Code
  Analytics API only tracks usage on the Claude API — Claude Platform on AWS,
  Microsoft Foundry, Amazon Bedrock and Google Gemini Enterprise Agent
  Platform (formerly Vertex AI) are not included. For a fleet
  on those surfaces, **this OTel path is the only observation you have**, and
  the shadow-auth detector on the admin feed cannot clear them.
- **Cost figures here are estimates.** Per-request cost telemetry is
  reconciled against the authoritative cost reports; one source of cost per
  session, never both.

## Next steps

- [Connect Claude Code](/how-to/connect-claude-code/) — the base wiring this
  page builds on.
- [Govern and approve](/how-to/govern-and-approve/) — the enforcement half
  (managed settings, hooks, the PEP).
- [Forward audit to Splunk](/how-to/forward-audit-to-splunk/) — ship the
  findings this telemetry produces to your SIEM.
