---
title: Analyst vocabulary, mapped honestly
description: >-
  The 2026 analyst vocabulary for AI governance — agent sprawl, guardian agents,
  AI TRiSM, discover/observe/govern/secure — defined, attributed where it has a
  source, and mapped to what Olivares AI actually does and does not do.
sidebar:
  order: 2
---

If you evaluate AI tooling, you have met these words: **agent sprawl**, **guardian
agents**, **AI TRiSM**, **discover / observe / govern / secure**. They are useful
shorthand, and a 2026 buyer expects a vendor to speak them. They are also easy to
abuse — to imply a product *is* a category when it merely sits near one.

This page does three things: it **defines** each term, it **attributes** it where
it has a real owner, and it **says plainly** which ones describe Olivares AI and
which ones we only relate to. For the numbers that back the underlying market, see
[Market context & sources](/explanation/positioning/market-context-and-sources/).

## Agent sprawl

**What it means.** The uncontrolled proliferation of AI agents, copilots, MCP
servers and automations across an organization — created by different teams, with
different credentials, touching different systems, faster than anyone keeps an
inventory. The result is unknown agents with unknown access.

**Does it describe us?** It describes the *problem we exist for*. Olivares AI's
first job is to make sprawl visible: it **discovers** the agents, models, MCP
servers and tools in your estate and builds a
[read/write access map](/explanation/#the-access-map-read-first-minimal-data-permitted-vs-observed)
of what each can reach — read-first, minimal-data, on **your** infrastructure. The
[Permitted-vs-Observed diff](/reference/glossary/#observed--permitted) then turns
"we have a lot of agents" into "here are the ones using access nobody granted."
Sprawl is the disease; an accurate, attributed inventory is the first treatment.

## Guardian agents

**What it means.** **Gartner's** term for AI capabilities that monitor, oversee or
intervene on *other* AI agents. Gartner projects guardian-agent technologies will
account for **10–15% of the agentic AI market by 2030** (Gartner press release,
2025; see [sources](/explanation/positioning/market-context-and-sources/)).

**Does it describe us? Carefully.** Olivares AI delivers the *governance and
oversight outcome* the category is about — observing agent behaviour, diffing
permitted against observed, gating actions deny-closed, and recording everything
in a tamper-evident ledger. But we are **not** an autonomous runtime agent that
reasons about other agents in the request path. We are a **read-first control
plane** that sits *outside* the data path: we observe through telemetry, native
audit logs and an eBPF kernel backstop, and we enforce at well-defined gates
(approvals, the
[Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/), kill switches)
— not by inserting an AI proxy into every call. If "guardian agent" means
*supervisory governance over your agent estate*, yes. If it means *an LLM standing
guard inline*, that is a different architecture, and we will not claim it.

## AI TRiSM

**What it means.** **AI TRiSM** — *AI Trust, Risk and Security Management* — is a
**framework coined and owned by Gartner** for managing the trust, risk and
security of AI across its lifecycle. As commonly summarised, it spans **governance**
and **runtime inspection & enforcement** of AI, alongside information governance
and infrastructure security.

:::caution[Attribution note]
The AI TRiSM framework, its layer taxonomy and any definitions are **Gartner
proprietary research**. Public restatements (including layer names and diagrams)
typically originate from **licensed reprints**. We describe AI TRiSM at the
*theme* level and map our capabilities to those themes; we do **not** reproduce
Gartner's exact model, claim conformance to it, or imply a Gartner endorsement.
:::

**How we map to it (theme level).**

- **Governance** — policy authoring, risk classification (EU tier × NIST
  function), approvals/HITL, manage-as-code, and the
  [compliance module](/reference/modules/xiii-compliance/)'s framework catalog.
- **Runtime inspection** — the access map and Permitted-vs-Observed drift,
  guardrail/anomaly findings, session timelines — all read-first and out-of-band.
- **Runtime enforcement** — deny-closed gates where we *do* sit in a decision
  path: approvals, the Claude Code hooks PEP, MCP tool gating, kill switches.
- **Information governance** — PII/sensitivity discovery over governed knowledge
  bases, data-residency attestation, retention and legal-hold.

We use AI TRiSM as a *map of the problem space a buyer already knows*, to show
coverage — not as a badge.

## Discover / observe / govern / secure

**What it means.** The verb sequence analysts and vendors use to describe the AI-
governance lifecycle: first **discover** what exists, then **observe** what it
does, then **govern** what it is allowed to do, then **secure** the whole estate.

**Does it describe us?** Yes — it is close to our own product narrative, which is
worth stating in our exact terms so the mapping is honest:

| Analyst verb | What Olivares AI actually does |
|---|---|
| **Discover** | Inventory of agents, models, MCP servers and tools across the estate. |
| **Observe** | The R/RW access map — read-first, minimal-data, with per-edge attribution confidence; cooperative paths (OTel, hooks) corroborated by native audit (pgAudit, CloudTrail) and an eBPF backstop. |
| **Govern** | Permitted-vs-Observed drift, policy + approvals/HITL, deny-closed actuation gates, manage-as-code. |
| **Secure** | Guardrails, the tamper-evident audit ledger, kill switches, compliance evidence — **and** the posture that the whole thing runs self-hosted, with no mandatory telemetry and no control-plane egress by default: what crosses your perimeter is what you configure to cross it (model APIs, the SIEM/webhook outputs you wire, an external embedding provider if you provision one). |

The honest caveat that runs through all four: **fidelity is tiered**. Observation
is clean for SQL databases, object stores and warehouses; lossy for document and
vector stores; and not achievable passively for some systems. The map
[shows its confidence](/reference/glossary/#attribution-confidence) rather than
inventing attribution it does not have.

## The three lanes this vocabulary points at

Strip the labels away and the same three differentiators remain — the lanes the
market has left open and the copy should keep returning to:

1. **Ground truth from the data plane.** We do not take an agent's word for what
   it touched. We **correlate** the cooperative signal (OTel, MCP, hooks) against
   the system's own ledger — pgAudit classifying reads vs writes, CloudTrail
   exposing object-store access — and an eBPF kernel backstop for the
   non-cooperative case. That correlation is what makes Permitted-vs-Observed a
   *fact*, not a self-report.
2. **Deny-closed enforcement on the local dev agent.** Most tools only *observe*
   Claude Code. Olivares AI also **governs** it: the hooks PEP turns policy into a
   deny-closed decision at the agent, not an after-the-fact log line.
3. **Sovereignty.** Self-hosted, source-available **AGPL** — the data plane never
   leaves your boundary and there is no SaaS control plane in your compliance path.

Every term above is in service of those three. When a page here uses an analyst
word, it is to meet the buyer where they are — then to point back at one of these
three things the product genuinely does.
