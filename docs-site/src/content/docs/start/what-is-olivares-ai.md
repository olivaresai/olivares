---
title: What is Olivares AI?
description: >-
  Integrate, manage and secure the AI you run, from a single machine to a whole estate —
  one ground truth: Claude Code at the deepest level, Codex and Grok Build alongside. A
  single self-hosted binary that gives your AI context, resource access and
  managed sessions, and gives you the permissions, policies, budgets and audit evidence
  to run it across your infrastructure — with no mandatory telemetry and no
  control-plane egress by default: what crosses your perimeter is what you configure to
  cross it, from calls to your model APIs to the SIEM/webhook outputs you wire.
---

Olivares AI **integrates, manages and secures the AI you run** — on one machine or across
a whole estate, one ground truth: Claude Code at the deepest level, Codex and Grok Build
alongside, complementing those agents rather than competing with them. As you put more
models, agents, MCP servers and tools to work across real, heterogeneous infrastructure,
two things get hard at once: making AI genuinely useful, and keeping it under control.
That is as true of one self-hosted box as it is of a regulated estate; the difference is
scale, not kind.

Olivares AI does both. On one side it gives your AI what it needs to work — context,
access to the right resources, managed sessions. On the other it gives you the **granular
permissions, policies, budgets and audit evidence** to run all of it: which model and
agent can reach what, the data they touch, what they are allowed to execute, what they
spend, and the proof you can hand a regulator.

Everything runs as a **single self-hosted binary** on your own hosts. There is no
mandatory telemetry and no control-plane egress by default: what crosses your perimeter
is what **you** configure to cross it — calls to your model APIs, the SIEM/webhook outputs
you wire, an external embedding provider if you provision one. That is a property of the
architecture and of your configuration; it is a description, **not a guarantee**.

## One capability: the read/write access map

Among those capabilities is the **R/RW access map**. For every origin (an agent, a
non-human identity, a session) it builds an edge to
each resource it touches, classified **read**, **write**, **read-write** or
**unknown**, and tagged with:

- **where the signal came from** (`SignalSource`) — OpenTelemetry from a
  cooperative agent, a Postgres pgAudit READ/WRITE classification, an AWS
  CloudTrail record, a kernel-level eBPF/Tetragon backstop, an MCP annotation
  (treated as **untrusted** and corroborated, never trusted alone), a declared
  policy grant, or an agent-to-agent (A2A) signal; and
- **how much to trust the attribution** (`Confidence`) — `attributed` when it is
  firmly tied to a per-agent identity, `approximate` when it is inferred (a shared
  service account, or a lossy store).

At its centre is the diff: **Permitted vs Observed**. Permitted edges come
from declared grants; observed edges come from real telemetry and audit. Comparing
them surfaces *unexpected accesses* (an agent reading a table it was never granted),
*unused grants* (a permission no agent ever exercised), and *reconciliation-pending*
edges (an access the system cannot yet firmly attribute).

The product is **honest about fidelity**. Coverage is **tiered**: clean on stores
with native audit (SQL, object storage, warehouses), lossy on some stores
(document/vector), and impossible to reconstruct passively on others (e.g. Redis,
SQLite, D1). Where the read/write nature cannot be determined, the mode is
`unknown` — the product never fabricates a classification.

## A platform, not a single feature

The access map is one capability among many. The product is a **modular platform** (in
the spirit of Grafana or Backstage): one engine plus modules plus connectors, designed so
any module attaches without re-architecting the rest. It ships **30 modules** — inventory
and live sessions, the R/RW map, agent orchestration (A2A, in development), MCP and skill management,
identity and non-human identity, deployment, knowledge and context, security and
guardrails, model and provider management, cost/FinOps, evals and a testing sandbox,
red-teaming, compliance and evidence, an internal catalog, output integrations and SIEM
push, voice/realtime, and health/SLA — plus platform capabilities not counted among the
30 (its own API and manage-as-code, multi-tenancy, executive dashboards) — across
**158 integrations** (a count measured from code by
`scripts/check-public-counts.sh`). A few capabilities are pre-v1 or
deny-closed seams until provisioned; the docs are explicit about which.

See the [modules catalog](/reference/modules/overview/) for the full list, and the
[architecture overview](/explanation/architecture/overview/) for how the engine and
modules fit together.

## How it observes: read-first, minimal-data

Olivares AI is **read-first**: the engine observes through logs, OpenTelemetry and
eBPF; it does **not** sit in the agent's data path, so a collector failure never
breaks your production traffic. And it is **minimal-data by design**: the access
graph stores **relations** — origin → resource, read/write, source, confidence,
timestamp — **never payloads, SQL bodies, secrets or PII**. What is not stored
cannot leak.

This is also why it is self-hostable and air-gap friendly: there is no mandatory
telemetry and no control-plane egress by default, so what crosses your perimeter is
what **you** configure to cross it — calls to your model APIs, the SIEM/webhook
outputs you wire, an external embedding provider if you provision one. Olivares AI
is not on that list: the vendor is never in the data path. It is reached only when you ask
it for something — `olivares upgrade`, or a subscription download of commercial add-ons and
their updates — never as a side effect of running. And `olivares upgrade --endpoint`
points even that at your own mirror, so the one command that reaches out does not have
to. That is a strong argument
for data residency, GDPR and air-gapped environments.

## Where to go next

- **Try it:** the [zero-to-graph tutorial](/tutorials/zero-to-graph/) boots the
  single binary and reaches a populated Permitted-vs-Observed graph.
- **Understand it:** the [architecture overview](/explanation/architecture/overview/)
  and the [security & threat model](/explanation/security/threat-model/).
- **Operate it:** [self-hosting](/how-to/self-hosting/) and
  [air-gapped installation](/how-to/air-gap-install/).

:::note[Status]
Olivares AI is **pre-1.0**. The single binary builds, boots and reaches a populated
access graph today (this is exercised end-to-end by the test suite), but several
capabilities are design-stage or post-v1. The documentation is explicit about what
runs now versus what is planned — see [Honesty & limits](/start/honesty-and-limits/).
:::
