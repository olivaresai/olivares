---
title: "Connect Claude Code (the cooperative path)"
description: "Point Claude Code's OpenTelemetry exporter at the engine and wire it as a source so its tool telemetry — plus untrusted MCP introspection — feeds the R/RW access map."
---

Claude Code is the **canonical cooperative source** for Olivares AI. It emits
OpenTelemetry (OTLP) telemetry about the tools it runs, and the MCP servers it
talks to expose introspection hints (`readOnlyHint` / `destructiveHint`) about
whether a tool reads or writes. Together these feed **module III — the R/RW access
map** with high-fidelity, agent-attributed edges, the cooperative half of the
permitted-vs-observed picture.

This page wires that path: point Claude Code's OTLP exporter at the engine's
receiver, then declare the source so its telemetry becomes access edges. For the
general source-wiring mechanism and where this fits, see
[Connect a source](/how-to/connect-a-source/) and the
[architecture overview](/explanation/architecture/overview/). For the shape of the
normalized events this produces, see the [events reference](/reference/events/).

:::note[Cooperative, not authoritative]
The cooperative path is **high-fidelity but trust-tiered**. OTLP tool telemetry is
attributed to a concrete agent session; MCP annotations are a useful R/RW *signal*
but are **untrusted by the MCP spec** and are corroborated, never trusted alone
(see [Honesty and limits](/start/honesty-and-limits/)). For activity outside the
agent's cooperation — or to catch an agent that stops emitting — pair this with a
non-cooperative backstop (kernel/eBPF) and store-native audit (pgAudit,
CloudTrail). This page is the cooperative source only.
:::

## What you get from this source

Once wired, Claude Code's telemetry is normalized into the engine's data model and
fed to module III:

| Output | Provenance | Notes |
|---|---|---|
| **Access edge** `agent session → resource (read/write)` | signal source `otel` | confidence `attributed` — the origin is a concrete session, not a shared service account |
| **MCP server edge** `session → MCP server` | signal source `otel` | mode `unknown` (a connection is not itself an access; this is topology/inventory) |
| **R/RW hint from MCP introspection** | signal source `mcp_annotation` | **untrusted** — a corroborating signal, never an edge on its own |
| **Cost sample** (per-request model usage) | the api-request telemetry | feeds FinOps, not the access map |
| **Finding** (anti-evasion) | telemetry gaps / denied tools | a session that stops emitting while still active is flagged |

The connector is **read-first and minimal-data**: it records the *relationship*
(which session touched which resource, read or write), never the payload. A raw
tool input or shell command — which can carry a secret or PII — is reduced to a
redacted resource reference before it ever becomes an observation. That posture is
the default; retaining any content is an explicit, category-scoped opt-in.

## How the wiring works

There are two halves, and they meet at a loopback socket on the host where Claude
Code runs.

1. **The engine exposes an OTLP receiver as core ingest.** The cooperative
   connector runs an OTLP receiver (gRPC and HTTP) for Claude Code's own
   OpenTelemetry output, plus an endpoint for its tool hooks. It **binds to
   loopback by default** — the cooperative ingest is unauthenticated, so it must
   not be reachable off-host. Keep it on loopback; the off-host backstop is the
   kernel collector, not a public OTLP port.
2. **You point Claude Code's OTLP exporter at that receiver**, and you **declare
   the source** so the engine knows to run it for your tenant.

```
  Claude Code (agent host)                 Olivares AI engine
  ┌──────────────────────────┐             ┌─────────────────────────────┐
  │ OTLP exporter            │── loopback ─▶│ cooperative OTLP receiver   │
  │ (OTEL_* env on the CLI)  │   (4317/4318)│ → normalize → access edges  │
  │ MCP servers (R/RW hints) │             │ → module III (R/RW map)     │
  └──────────────────────────┘             └─────────────────────────────┘
```

:::caution[The receiver is unauthenticated and loopback-only by default]
Because the cooperative ingest accepts telemetry without authenticating the
sender, anyone who can reach the socket can forge edges. The receiver defaults to
a loopback bind for exactly this reason. Binding it to a non-loopback address is a
dangerous, explicit opt-in; do not expose it on a shared network. Off-host agents
should be observed with the non-cooperative backstop instead.
:::

## Step 1 — Point Claude Code at the receiver

Claude Code is configured through its own OpenTelemetry environment variables. On
the agent host, enable its OTLP export and direct it at the engine's loopback
receiver. The engine's receiver follows the standard OpenTelemetry ports (gRPC and
HTTP); set Claude Code's exporter endpoint to the matching loopback address and
protocol.

:::note[Exact OTEL variable names belong to Claude Code, not to this product]
The exporter is configured with Claude Code's / OpenTelemetry's own settings
(enable telemetry, choose the OTLP protocol, set the endpoint). Those names are
defined by Claude Code and the OTel SDK — consult Claude Code's telemetry
documentation for the current variable names rather than copying a list here. What
this product owns is the **receiver** they point at and the **source declaration**
below.
:::

By default the connector retains only **structural** telemetry — session and
identity attributes, tool names, R/RW mode, timing — and never prompt text, tool
bodies or raw API bodies, even if Claude Code is configured to emit them. Leave it
that way unless you have a specific, audited reason to retain a content category.

## Step 2 — Declare the source

Real (non-demo) sources are wired from a single operator-owned configuration file,
named by the `OLIVARES_SOURCES_CONFIG` environment variable, that the engine reads
**before it starts**. Secrets live by value in that operator file, never in the
store. Each entry names the source, its `kind`, the tenant it belongs to, and a
per-source `config` block:

```json
{
  "sources": [
    {
      "name": "claude",
      "kind": "claude",
      "tenant": "<tenant-ref>",
      "config": {
        "grpc_addr": "127.0.0.1:4317"
      }
    }
  ]
}
```

- **`name`** is your label for this source instance.
- **`kind`** selects the cooperative Claude Code connector.
- **`tenant`** scopes every edge it produces to one tenant (module III reads are
  tenant-scoped and privileged).
- **`config`** holds the connector's own settings — for example the loopback
  address the OTLP receiver binds. The connector binds its receiver itself rather
  than borrowing the agent's, so disabling a Claude Code OTEL variable cannot
  silently turn the collector off.

:::caution[Confirm the connector's config keys against the shipped descriptor]
The connector publishes its own configuration schema (its descriptor lists every
key, type, default and description). The `config` block above shows the
representative receiver-address key; **do not invent additional keys** from this
page. Read the descriptor the connector reports — or
[the configuration reference](/reference/configuration/) — for the authoritative,
versioned list (receiver addresses, the hook path, correlation/silence windows,
the content-capture allowlist, and the opt-in governance fields). One value at a
time, verified against what your build actually ships.
:::

An **unconfigured or empty source warns honestly** rather than failing: a `kind`
that is unknown, not embedded, or fails to load is reported at startup, never
silently dropped to a no-op. After editing the file, restart the engine so the
composition root re-reads it.

## Step 3 — Verify edges are arriving

With Claude Code exporting and the source declared, run a Claude Code session that
touches a resource (read a file, run a command, call an MCP tool), then look at the
access map. Viewing the access graph is a **privileged, tenant-scoped, audited
action** (editor role and up — never the lowest viewer), so use a token with the
right role:

- The access graph is served at the module route `/v1/m/accessmap/graph`.
- The permitted-vs-observed result — the least-privilege **drift** — is at
  `/v1/m/accessmap/drift`.

These module routes are reachable but are deliberately **not** in the served
OpenAPI document; their contracts live in the product's typed Go/TS interfaces.
For the end-to-end walkthrough from a fresh engine to a populated graph, follow the
[Zero to graph tutorial](/tutorials/zero-to-graph/).

You should see edges whose signal source is `otel`, attributed to the Claude Code
session. If MCP introspection contributed an R/RW hint, that arrives as a separate
`mcp_annotation` signal that corroborates — but does not by itself establish — the
edge's mode.

## Honest limits of this path

- **MCP annotations are untrusted.** `readOnlyHint` / `destructiveHint` are
  advisory hints a server declares about itself; the MCP spec says clients must
  treat them as untrusted. The product surfaces them as a corroborating signal and
  shows confidence honestly — it never upgrades an edge to "read-only" on a hint
  alone.
- **Attribution depends on per-agent identity.** Edges are attributed to a session
  identity. A pool of agents sharing one service account collapses attribution;
  resolving that is a governance concern (issue and enforce per-agent identity),
  not something this connector can manufacture.
- **It is cooperative.** It sees what the agent reports. An agent that never emits,
  or activity that happens off the agent's path, is invisible to this source by
  construction — which is exactly why the non-cooperative kernel backstop and
  store-native audit exist alongside it.
- **Design-stage depth.** Much of the platform is pre-1.0. Treat capabilities here
  as the verified cooperative ingest path; where a downstream module or field is
  not yet built, the product says so rather than implying coverage.

## Next steps

- [Connect a source](/how-to/connect-a-source/) — the general source-wiring model
  (cooperative and non-cooperative).
- [Govern and approve](/how-to/govern-and-approve/) — turn observed drift into a
  least-privilege decision.
- [Events reference](/reference/events/) — the normalized observations this source
  emits.
- [Architecture overview](/explanation/architecture/overview/) — where the
  cooperative path sits in the platform.
