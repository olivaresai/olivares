---
title: Where Olivares fits vs your AI gateway & Guardrails
description: >-
  You already run an AI gateway (LiteLLM, Portkey, Cloudflare) or hyperscaler
  Guardrails (Bedrock, Azure). Good — keep them. Olivares AI is not a gateway and
  does not compete on routing or caching. It is the governance and evidence plane
  that sits beside them and closes the gap they leave open.
sidebar:
  order: 7
---

If you have already invested in an **AI gateway** or in a hyperscaler's
**Guardrails**, the honest first thing to say is: **keep them, and Olivares AI is
not trying to replace them.** A gateway's job is the model call — route it, cache
it, balance it, budget it. Guardrails' job is content safety on that call. Both
are real, both are good at what they do, and neither is the thing Olivares is.

:::tip[The short version]
**Olivares AI is not an AI gateway.** It does not route, cache, load-balance, or
sit on the hot path of your model traffic, and it never will. It sits **beside and
behind** your gateway as the *governance and evidence plane*: in-process
enforcement inside the agent runtime, a tamper-evident evidence ledger,
non-human-identity lifecycle, and human-in-the-loop / break-glass / kill-switch
over **live sessions**. Your gateway governs the *request*; Olivares governs the
*agent and everything it touches*, and proves it to an auditor.
:::

## What a gateway and Guardrails do well (use them for this)

These are commodity, well-understood capabilities, and the vendors describe them
plainly:

- **AI gateways** are request-path managers for model calls. LiteLLM is an
  *"OpenAI Proxy Server (LLM Gateway) to call 100+ LLMs in a unified interface &
  track spend, set budgets per virtual key/user"*
  ([LiteLLM](https://docs.litellm.ai/docs/simple_proxy)); Cloudflare AI Gateway
  lets you *"Connect to any model, dynamically route requests, and manage usage,
  billing, and logs from one unified gateway"*
  ([Cloudflare](https://www.cloudflare.com/products/ai-gateway/)); Portkey
  *"records real-time API requests, including cost"*
  ([Portkey](https://portkey.ai/features/ai-gateway)). Routing, fallbacks,
  caching, virtual keys, per-key budgets, request logging — this is their lane.
- **Hyperscaler Guardrails** are content-safety filters. Bedrock Guardrails
  *"provides configurable safeguards to help you build safe generative AI
  applications"* that *"detect and filter undesirable content and protect
  sensitive information that might be present in user inputs or model responses"* —
  content filters, denied topics, word filters, PII redaction, contextual-grounding
  and automated-reasoning checks
  ([AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)).

If your problem is *"give my apps one endpoint to many models, with budgets,
caching and content filtering,"* that stack solves it, and you do not need a
control plane to do it. We integrate with that pattern; we do not reimplement it.

## The governance gap they leave open

A gateway sees a **request**. Guardrails see **content**. Neither sees the
**agent** — its identity over time, what it reached across your data plane, who
approved a risky action, and whether any of it can be proven later. That is the
gap Olivares fills.

| Gap the gateway / Guardrails leave | Why it matters | What Olivares AI provides |
|---|---|---|
| **Enforcement at the agent runtime** | A gateway enforces at the *request boundary*; it cannot stop a local Claude Code tool-call that never traverses it | A deny-closed [in-process PEP](/how-to/connectors/claude-code-hooks-pep/) at the agent: firm-identity gate, policy disposition, live-policy overlay, all before the tool runs |
| **Tamper-evident evidence** | Gateway and Guardrails emit *logs* — mutable request records; an auditor wants immutable proof | Append-only, hash-chained, [Ed25519-signed ledger](/reference/glossary/#audit-ledger), off-box verifiable, exportable as OSCAL evidence |
| **Non-human-identity lifecycle** | A gateway's "virtual key" is a budget bucket, not an identity that is provisioned, attributed, rotated and offboarded | [NHI lifecycle](/reference/glossary/#identity--nhi): staleness → block, offboarding cascade, dual-control on rotation, bound to the access map |
| **Live-session intervention** | Logs and budgets are after-the-fact; none of these surveyed tools stop a session mid-flight | [HITL approvals](/reference/glossary/#approval-hitl), [break-glass](/reference/glossary/#break-glass), and a [kill switch](/reference/glossary/#kill-switch) that denies all governed actuation until a dual-control re-enable |
| **Ground truth across the estate** | A gateway sees only the calls that pass through it; agents also touch DBs, object stores, MCP, files directly | The read-first [R/RW access map](/explanation/#the-access-map-read-first-minimal-data-permitted-vs-observed) and Permitted-vs-Observed drift, corroborated against native audit |
| **Sovereignty** | SaaS gateways and cloud Guardrails process that traffic in their cloud | Self-hosted / air-gapped; the data plane never leaves your boundary |

None of these are routing features. That is the point: the gap is not *better
routing*, it is **governance the request path was never designed to provide.**

## On Guardrails specifically: content safety is a hook, not a competitor

Bedrock Guardrails can be applied two ways — inline during a Bedrock inference
call, or *"directly through the `ApplyGuardrail` API without invoking the
foundation models"*, which works *"with any foundation model whether hosted on
Amazon Bedrock or self-hosted models"*
([AWS](https://aws.amazon.com/bedrock/guardrails/)). That is genuinely useful, and
Olivares treats content safety as a **detector you plug in**, never a wall we ask
you to choose *instead* of Guardrails. Two honest, distinct facts:

- The inline inference proxy exposes a **content-inspection seam** — a pluggable
  point where a content / DLP detector returns a verdict the deny-closed decider
  acts on. Content safety belongs *there*, in the pipeline, rather than being
  re-implemented as a competing filter.
- Olivares reads your Guardrails' **own decisions** read-first. The AWS connector
  ingests Bedrock guardrail decisions from their CloudWatch / S3 logs as posture
  and evidence; it deliberately does **not** call the paid `ApplyGuardrail`
  runtime itself. Your content verdicts become part of the tamper-evident record.

So content safety composes with what you already run. What Guardrails do *not*
document — and where the governance gap stays open — is the rest of the agent's
life: the Bedrock pages document no agent identity, no session management, no human
approvals, and no cost governance (not documented on those pages, checked 2026-06-21).
Olivares is exactly that complement: it carries the identity, the session controls,
the approvals and the evidence; the content filter stays where it already lives.

## How they compose

A healthy arrangement keeps every tool in its lane:

- **Keep your gateway** (LiteLLM / Portkey / Kong / Cloudflare) as the model-call
  plane — routing, caching, virtual keys, budgets on the request.
- **Keep your Guardrails** (Bedrock / Azure Content Safety) as your content-safety
  detector — the Olivares PEP runs a pluggable detector at its content-inspection
  seam and reads your Guardrails' own decisions read-first as evidence; it does not
  invoke `ApplyGuardrail` itself.
- **Add Olivares beside them** as the governance and evidence plane: the
  in-process PEP on the agents that never hit your gateway, the access map across
  the whole estate, the tamper-evident ledger, and the live HITL/break-glass/kill
  controls.

The one place Olivares does touch inference is narrow and explicit — an
**API-key-only** gateway path for raw SDK/`curl` callers, described in
[Governing subscription-authed agents](/explanation/positioning/governing-subscription-authed-agents/).
It exists to govern traffic your other tools cannot reach, never to compete with
them on routing, and it **never** carries a subscription credential.

## When your gateway is enough

Honesty cuts both ways. If your agents only ever call models **through** your
gateway, your content-safety needs are met by Guardrails, you have **no
self-hosted or laptop-resident agents** reaching databases / object stores / MCP
directly, and you have **no sovereignty or tamper-evident-evidence requirement** —
then your gateway plus its logs and Guardrails may be all you need, and you should
not add a control plane for its own sake.

Olivares earns its place when the questions become *estate-wide and adversarial*:
which agents exist and what each actually reached, can I stop a bad action
deny-closed **at the agent**, who approved the risky one, and can I hand an auditor
**immutable proof** — all without sending that picture to someone else's cloud.
For the deeper treatment of two adjacent comparisons, see
[vs AI control towers](/explanation/positioning/vs-control-towers/) and
[vs LLM gateways & observability](/explanation/positioning/vs-llm-observability/).
