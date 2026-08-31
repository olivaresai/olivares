---
title: Module IX — security, guardrails & audit
description: "The defensive control plane: deterministic guardrails that produce
  minimal-data findings, prioritized anomalies, and hash-chain-verified incident
  timelines — detective by default, with inline enforcement an opt-in, governed,
  off-by-default seam."
slug: 2026-06/reference/modules/ix-security
---

Module IX is the **defensive, cross-cutting layer** of Olivares AI. It turns the
estate's events and the tamper-evident evidence ledger into **findings**, **prioritized
anomalies** and **reconstructible incident timelines**, so a defender can *see* and
*prove* what every agent did. It is **detective by default**: it observes and hands over
evidence, and never sits in the agent's data-path.

## What it is

The module spans three bounded responsibilities:

* **Guardrails** — a chain of deterministic, explainable detectors inspects agent
  text on the `input`, `output` and `tool_args` surfaces for secrets/PII,
  prompt-injection, jailbreak, disallowed content, output-schema violations and the
  OWASP Agentic Top 10. Detections carry framework references (OWASP LLM Top 10 2025,
  OWASP Agentic Top 10 2026, MITRE ATLAS) verbatim from primary sources, never invented.
  An optional, pluggable classifier (a hosted guardrail-LLM) runs *behind* the
  deterministic detectors: it can only **add** detections, never suppress one, and its
  failure is logged and ignored.
* **Anomaly detection** — it correlates the Permitted-vs-Observed drift that
  [module III](/2026-06/reference/modules/iii-access-map/) computes with high-severity findings,
  and joins kernel-side and cooperative-side anti-evasion signals: an agent that silences
  its own telemetry is treated as a signal, not a blind spot.
* **Forensics / IR** — it groups evidence into a **case** and reconstructs its
  **timeline** from the append-only, hash-chained ledger, *verifying* the chain and its
  signed checkpoints rather than trusting them. A tampered ledger is reported, not hidden.
* **Privileged-session recording** — an immutable, replayable record of what a
  privileged operator session actually did on the product's most sensitive module
  surfaces: one append-only frame per recorded action (who, when, route shape,
  permission, targets, outcome, request digest), hash-chained per session and
  anchored into the evidence ledger (open → periodic anchors → seal), so rewriting
  any frame breaks both the session chain and its signed ledger anchors. The gate
  runs *before* the action and is deny-closed: on a recorded surface, no appendable
  evidence trail means no privileged action.

## Its contract & entities

Module IX is the **first producer of the core `Finding` entity**; it owns no ledger and
no capture, it consumes them. On top of `Finding` it owns three entities: a mutable
**case** (lifecycle `open` → `investigating` → `contained` → `closed`, with an integrity
snapshot taken at open time), an **append-only case link** that forms the chain of
custody (the evidence set of an incident is itself evidence and cannot be rewritten), and
a per-class **enforcement policy** — where the absence of a row means *detective*.

Its routes are mounted under the module API and wrapped with authn + tenant + authz, with
namespaced read/write/admin permissions. Reading findings is plain (a finding is the alert
itself); the **recon-sensitive** reads — the verified timeline, the SIEM export, the
anomaly view and the standalone integrity verification — are **privileged and
self-audited**: the act of looking is recorded in the same chain it inspects. Every
mutation (triage, case lifecycle, enforcement posture) is self-audited too. Exports to
WORM/SIEM (CEF, syslog, OTLP) carry per-line integrity fields so the chain can be
re-verified **offline** by an external immutable store.

## What it consumes & produces on the bus

Module IX reacts to [`finding.reported`](/2026-06/reference/events/) (persisting other modules'
high-severity findings into the tenant's security view) and to
[`guardrail.observed`](/2026-06/reference/events/), the detective-input channel of already-redacted
observed text. It produces one `FindingReport` per detection on namespaced
`security_*` routing keys, which downstream delivery routes to SIEM/Slack/PagerDuty and
which compliance maps to controls. The live `guardrail.observed` feed comes from the
runtime-ingestion layer described in the [event bus reference](/2026-06/reference/events/): it is
**deny-closed and opt-in** (off unless an operator enables it), and the inspected text is
the connector's *already-redacted resource reference* of a `tool_args` edge — never the
raw argument.

:::caution[Honest limits]
* **Detective by default; enforcement is an opt-in seam.** The module observes and gives
  evidence. Inline enforcement (blocking an output or action) is **off by default**,
  admin-tier, and — where a HITL approval gate is wired — governed. Enabling it is the only
  capability that touches production; disabling (the safe default) is always allowed. A
  guardrail that fails must never break production.
* **The live feed has a real coverage boundary.** On the live `guardrail.observed`
  surface, only **PII or a secret embedded in a resource reference** (and anomalous/sensitive
  resource patterns) is detectable. Prompt-injection and jailbreak need the *content* of
  the argument, which is discarded at the cooperative source and never reaches the bus; the
  `input` / `output` / `tool_result` surfaces require an in-process content source that this
  build does not provide. This is declared, not faked.
* **Integrity verification can be unavailable, never faked.** The hash chain is always
  verified for internal consistency, but attestation of *signed checkpoints* needs the
  ledger's public key wired in; without it, checkpoint verification is reported
  **unavailable** rather than pretended. A forged checkpoint is detected, not trusted.
* **Coverage inherits the access map's tiers.** Anomalies built on drift are bounded by
  module III's tiered audit coverage; the content catalog (disallowed content) is a
  conservative, non-exhaustive starter set, shown as such.
:::

## Related

* [Event bus reference](/2026-06/reference/events/) — `finding.reported`, `guardrail.observed` and the runtime-ingestion channel.
* [Live-ingest — the in-process observe producer](/2026-06/reference/modules/live-ingest/) — the deny-closed module that publishes the live `guardrail.observed` feed this module consumes.
* [Module III — the read/write access map](/2026-06/reference/modules/iii-access-map/) — the drift this module correlates.
* [Modules catalog](/2026-06/reference/modules/overview/) — module IX's layer and actuation status.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — acting on findings and enforcement.
* [Forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/) — exporting verifiable evidence to a SIEM/WORM store.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — what is built, observed and actuated today.
