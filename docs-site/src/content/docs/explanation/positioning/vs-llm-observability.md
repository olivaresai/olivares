---
title: Olivares AI vs LLM gateways & observability (LiteLLM, Langfuse)
description: >-
  An honest comparison with the popular self-hosted LLM-ops stack — a gateway
  (LiteLLM) plus an observability platform (Langfuse). What each does well, where
  Olivares AI is different, and why it is "and", not "or".
sidebar:
  order: 3
---

A common, sensible self-hosted stack pairs an **LLM gateway** (for example
**LiteLLM**) with an **LLM observability platform** (for example **Langfuse**). If
you have one, you might reasonably ask whether you need a control plane at all.
This page answers that honestly — including the cases where the answer is **no**.

:::tip[The short version]
LiteLLM and Langfuse are about **the model calls your application makes**: route
them, trace them, manage prompts, track cost per call. Olivares AI is about
**every agent in your estate and everything it reads or writes** — databases,
object stores, MCP servers, tools, files — and whether that matches what policy
permits. Different altitude. They compose; we **ingest the same OpenTelemetry
gen-ai signal** they emit.
:::

## What that stack does well (use it for this)

- **LiteLLM** — a unified, OpenAI-compatible gateway in front of many providers:
  routing, fallbacks, retries, virtual keys, per-key budgets and rate limits, and
  cost accounting on the model calls that pass through it.
- **Langfuse** — LLM engineering and observability: request/response **traces**,
  prompt management and versioning, evaluations, datasets, and a developer-facing
  UI for debugging chains.

If your problem is *"instrument my app's LLM calls, debug prompts, and manage
model access from one endpoint,"* this stack is excellent and self-hostable. You
do not need a control plane to do that, and we will not pretend otherwise.

## Where Olivares AI is structurally different

| Dimension | LLM gateway + observability | Olivares AI |
|---|---|---|
| **Unit of concern** | A model call (prompt → completion) | An agent and every resource it reads/writes — DBs, object stores, MCP, tools, files |
| **Vantage point** | **In the request path** (proxy/SDK); sees what the app sends | **Out of band, read-first**; observes telemetry, native audit and a kernel backstop — never in the data path |
| **Source of truth** | What the app/proxy **reports** | Self-reported telemetry **corroborated against the system's own ledger** — pgAudit (read vs write), CloudTrail (object access), eBPF backstop |
| **The key question** | "What did this prompt do, and what did it cost?" | "Is this agent using access **nobody granted**?" — [Permitted-vs-Observed drift](/explanation/#the-access-map-read-first-minimal-data-permitted-vs-observed) |
| **Enforcement** | Gateway can gate **model calls** (keys, budgets) | Deny-closed gates on **actions and resource access**: approvals, the [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/), MCP tool gating, kill switches |
| **Audit artifact** | Traces / logs for debugging | Append-only, hash-chained, **Ed25519-signed** ledger, **off-box verifiable**, exportable as **OSCAL** evidence packages |
| **Deployment posture** | Self-hostable | Self-hosted **or air-gapped**; data plane never leaves your boundary; **AGPL**, source-available |

The load-bearing difference is **ground truth**. An observability trace tells you
what the application *said* it did. It cannot tell you that an agent reached a
table the trace never mentioned. Olivares AI cross-checks the cooperative signal
against the data plane, so "what the agent touched" is a corroborated fact, not a
self-report. See [Analyst vocabulary](/explanation/positioning/analyst-vocabulary/)
for why that is the first of our three lanes.

## It's "and", not "or" — we ingest your telemetry

Olivares AI is **not** a replacement for your gateway or your tracing tool, and it
does not want to be in the request path they occupy. It **consumes the same
signal**: the control plane ingests **OpenTelemetry GenAI** semantic-convention
spans, the same gen-ai telemetry these tools emit and consume. So a healthy
arrangement is:

- Keep **LiteLLM** as your model gateway and **Langfuse** for developer-facing
  tracing and prompt work.
- Point the **OTel gen-ai** stream at Olivares AI as one corroborating source, and
  let the access map, drift detection and the ledger do the estate-wide governance
  layer on top.

→ [Ingest OpenTelemetry GenAI](/how-to/connectors/otel-genai/) ·
[Enterprise OTel for Claude Code](/how-to/claude-code-enterprise-otel/)

## When you should *not* reach for Olivares AI

Honesty cuts both ways. You probably do **not** need this control plane if:

- Your only goal is **tracing and debugging LLM calls** in one or two apps, with a
  prompt playground — Langfuse alone is a better fit.
- You just need a **multi-provider gateway** with budgets and failover — that is
  LiteLLM's job, and we integrate with that pattern rather than reimplement it.
- You have **no estate to govern**: a single service, a single model, no agents
  touching databases/object stores/MCP, and no audit or regulatory obligation.

Olivares AI earns its place when the questions become *estate-wide and
adversarial*: **which agents exist, what can each actually reach, where is access
drifting from policy, can I prove it to an auditor, and can I stop a bad action
deny-closed** — all without sending that picture to someone else's cloud.
