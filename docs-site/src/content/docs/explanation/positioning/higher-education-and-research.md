---
title: Higher education & research
description: >-
  Why a self-hosted control plane fits universities and research institutions —
  enforcing acceptable-use policy across a federated estate, isolating risky work
  in sandboxes, and producing attribution reports, without sending student or
  research data to a vendor cloud.
sidebar:
  order: 5
---

Universities and research institutions adopted AI faster than they governed it.
**EDUCAUSE** surveys report that a large majority (**~80%**) of higher-education
staff now use AI tools, while **fewer than a quarter (<25%)** are familiar with
their institution's AI policies (EDUCAUSE AI Landscape / community surveys,
2025–2026 — survey estimates; see
[Market context & sources](/explanation/positioning/market-context-and-sources/)).
That gap — pervasive use, thin policy awareness — is the higher-ed governance
problem in one line.

The sector also has constraints that make a **US SaaS control plane a hard sell**:
research data under grant or IRB conditions, student records under privacy law
(FERPA in the US, GDPR in the EU), and a culture of decentralized, federated IT
where every department runs its own stack. A self-hosted, source-available control
plane is a natural fit precisely because of those constraints.

## Three jobs the control plane does for higher ed

### 1. Enforce acceptable-use policy across a federated estate

Acceptable-use policies (AUPs) for AI are usually a PDF nobody reads. The control
plane turns the parts that are *technical* into something observable and
enforceable:

- **Discover** the agents, copilots and MCP servers actually in use across
  departments — including the shadow ones the policy never anticipated.
- **Map** what each can read or write, and **diff Permitted vs Observed** so a
  research group's agent reaching a system it was never granted shows up as drift.
- **Enforce** the technical lines deny-closed where the platform sits in a decision
  path — approvals/HITL, the [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/),
  MCP tool gating — rather than relying on everyone having read the AUP.

The honest scope: the platform enforces what is *expressible as policy over agent
actions and access*. It does not adjudicate academic-integrity questions or read
intent — it makes the technical guardrails real and the rest auditable.

### 2. Isolate risky work in sandboxes

Research and coursework routinely involve untrusted code, adversarial prompts and
experimental agents. The platform's **agent simulation/testing sandbox** and
**red-teaming** modules let risky behaviour be exercised in isolation, away from
production systems, with the results recorded.

:::caution[What the sandbox is, and is not]
The execution-isolation guarantee is the **sandbox module** — red-team probes
execute only there, never against the live control plane or production agents. The
platform **detects** code-execution and exfiltration patterns and **tests
refusal**; it is not a general-purpose OS sandbox wrapped around every student's
laptop. Match the claim to the capability.
:::

### 3. Produce attribution reports

When something goes wrong — a data-handling complaint, a grant-compliance review,
a misuse report — the question is always *who did what, with which system, when*.
The control plane answers it from the **append-only, hash-chained,
Ed25519-signed** ledger, with per-edge
[attribution confidence](/reference/glossary/#attribution-confidence) and off-box
verification. Attribution reports are derived from real recorded activity, and the
report itself is tamper-evident — which matters when the finding has consequences
for a person.

## Why self-hosted is the deciding factor here

- **No vendor cloud in the path.** Collectors run on the institution's own
  infrastructure; the access map stores only the *relation* (agent → resource,
  read/write) with a source and confidence — **no payloads, no PII, no student or
  research content**. Nothing has to traverse a vendor cloud to be governed: there is
  no mandatory telemetry and no control-plane egress by default, and what crosses the
  campus perimeter is what the institution configures to cross it — calls to its model
  APIs, the SIEM/webhook outputs it wires, an external embedding provider if it
  provisions one.
- **Federated by nature.** A control plane that is multi-tenant, self-hosted and
  identity-federated mirrors how universities already run IT — per-department
  autonomy, central visibility — instead of forcing everything through one SaaS
  tenant.
- **Air-gap and sovereignty options** suit secure research enclaves and
  EU-resident data, with residency attestation
  (`GET /v1/m/compliance/residency`).
- **AGPL, source-available, no cost floor to start.** A platform engineer or
  research-computing team can stand it up and read every line — the bottom-up
  adoption path the sector actually uses, not a procurement-gated SaaS contract.

## Related

- [EU AI Act evidence from runtime data](/explanation/eu-ai-act-evidence/) — for
  EU institutions under the Act.
- [Where Olivares AI fits with your IdP](/explanation/architecture/where-it-fits-with-your-idp/)
  — federating campus identity and agent identity.
- [Self-host the control plane](/how-to/self-hosting/) — get started.
