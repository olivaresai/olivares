---
title: Market context & sources
description: >-
  The market signals behind Olivares AI — agent sprawl, failing pilots, missing
  access controls — each with its verified primary source, its exact figure, and
  an honest caveat. The single place every other page cites its numbers from.
sidebar:
  order: 1
---

This page is the **single source of truth for every market statistic** used
across the Olivares AI website, README and docs. It exists because the AI
governance market is awash in numbers whose attribution has been mangled in the
retelling — and a buyer's analyst will check. We would rather lose a punchy line
than cite a figure we cannot stand behind.

:::note[The attribution rule]
We cite **primary sources only**, name them exactly, and quote the figure as the
source states it. We do **not** launder a number through a blog that dropped the
attribution, and we do **not** stack aggregator stats ("70% of the Fortune 100…")
that no buyer can trace. Where a finding is **preliminary or not peer-reviewed**,
we say so on the same line. This mirrors how the product itself treats evidence:
[attribution confidence](/reference/glossary/#attribution-confidence) is a
first-class field, and a control with only design-stage evidence reports
`by_design`, never `satisfied`.
:::

## The figures we use, and where they come from

| Claim | Figure (as the source states it) | Primary source | Caveat / how we use it |
|---|---|---|---|
| Breached AI orgs lacked access controls | **97%** of organizations that suffered an AI-related security incident lacked proper AI access controls; **13%** of organizations reported a breach of their AI models or applications | **IBM, *Cost of a Data Breach Report 2025*** (research conducted by **Ponemon Institute**), IBM Newsroom | Attribution is **IBM / Ponemon — not Forrester**, a misattribution that circulates widely. We use it for the *access-control gap*, which is exactly what the [R/RW access map](/explanation/#the-access-map-read-first-minimal-data-permitted-vs-observed) and Permitted-vs-Observed diff address. |
| Agentic projects will be scrapped | **Over 40%** of agentic AI projects will be **canceled by the end of 2027**, due to escalating costs, unclear business value or inadequate risk controls | **Gartner**, press release (2025) | We use it for the *governance-debt* point — projects die for want of controls and provable value, not model quality. |
| Guardian agents become a market | **Guardian agent** technologies will account for **10–15% of the agentic AI market by 2030** | **Gartner**, press release (2025) | Establishes "guardian agents" as an analyst-recognized category. We are explicit that we are *not* a runtime agent that guards other agents — see [Analyst vocabulary](/explanation/positioning/analyst-vocabulary/). |
| Most pilots show no P&L impact | **~95%** of generative-AI pilots are delivering **no measurable P&L impact**; externally **purchased/partnered** tools succeed roughly **twice as often** as internally built ones | **MIT Media Lab, Project NANDA — *The GenAI Divide: State of AI in Business 2025*** (reported via *Fortune*, Aug 2025) | **Preliminary, not peer-reviewed.** We always label it as such. We use the *buy-vs-build* finding to support the "adopt a maintained control plane rather than hand-roll governance" argument — never as a settled statistic. |
| Higher-ed uses AI faster than it governs it | A large majority (**~80%**) of higher-education staff use AI tools, while **fewer than a quarter (<25%)** are familiar with their institution's AI policies | **EDUCAUSE** AI Landscape / community surveys (2025–2026) | Survey estimates; verify the exact study/year before external citation. We use the *policy-awareness gap* in the [higher-ed page](/explanation/positioning/higher-education-and-research/). |

## Qualitative evidence we rely on

These are not percentages; they are positions from named, citable sources that
frame *why the category exists*.

- **Bessemer Venture Partners** (*Atlas — "Securing AI Agents: the defining
  cybersecurity challenge of 2026"*): in-flight, surgical intervention on agent
  behavior is **"where the market is most underdeveloped and where the clearest
  infrastructure opportunity lies,"** and **"most enterprises do not have a
  precise inventory of the agents operating in their environment."** This is the
  external statement of the gap our [access map](/explanation/) closes.
- **Anthropic** (engineering posts on Claude Code sandboxing and Managed Agents):
  self-hosted sandboxes move execution into infrastructure the customer controls,
  but Anthropic **assigns audit logging, policy/RBAC, multi-host orchestration and
  traffic inspection to the customer**. That delegated responsibility is the seam
  Olivares AI fills — see [vs control towers](/explanation/positioning/vs-control-towers/).

## Survey signals (directional — verify before external citation)

Independent and community surveys consistently report the same shape: agents are
proliferating faster than organizations can inventory or attribute them. We treat
the specific percentages below as **directional context** synthesized from named
surveys; they are **not** part of our verified-primary set above and should be
re-checked against the original instrument before any external use.

- Cloud Security Alliance / Token Security (n≈418), Protiviti, and Optro surveys
  report, variously: a large share of organizations have **unknown/unmanaged
  agents** in their environment, only a minority keep a **real-time inventory**, a
  majority experienced an **agent-related incident** in the prior year, and only a
  minority can **trace an agent action back to a human or system**.

The point those surveys make in aggregate is the only thing we assert publicly:
**organizations are losing track of their agents, and cannot attribute what those
agents do.** That is a claim our product is built to make false for its users —
and it is the honest core of every positioning page here.

## Things we deliberately do **not** claim

- No customer counts, logo walls, or "trusted by N enterprises" — the product is
  pre-1.0 and pre-launch (see [Honesty & limits](/start/honesty-and-limits/)).
- No certification or attestation we do not hold (SOC 2, ISO 27001/42001 are
  **readiness**, not certificates — see the trust & procurement package that ships
  with the source).
- No invented benchmarks, throughput claims, or accuracy numbers. Capacity figures
  come only from the reproducible benchmark harness, with hardware provenance.
