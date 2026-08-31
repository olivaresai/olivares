<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Why Enterprise?

The open (AGPL) build is the complete governance platform — inventory, access map,
Cedar authorization engine, four deny-closed enforcement points, sealed audit
ledger, FinOps, compliance evidence, SIEM push, and everything else in the
observe-map-govern-audit loop. The commercial add-ons do not take any of that
away. They add capabilities for organizations that operate at scale, face specific
regulatory obligations, or need risk-mitigation depth that goes beyond what a
general-purpose governance loop covers.

---

## The three promises

These are structural, not contractual:

1. **What is open never moves to a paid tier.** Every capability in the AGPL
   build today stays in the AGPL build. The commercial add-ons are additive new
   code, never features removed from the open product.

2. **The core AGPL has no artificial performance, size or user limits.** There
   are no throttles, synthetic delays, seat caps or feature flags in the open
   binary. User accounts are unlimited (the 3-account community cap was removed
   on 2026-07-27); the edition boundary is the additive `enterprise/` line
   itself, never a limit imposed on the open one.

3. **The criterion is published.** Security-core and the complete
   observe-map-govern-audit loop are free; commercial is scale-operation +
   legal exception. The open-core cut and its reasoning are documented in
   `LICENSING.md`.

---

## What enterprise adds, by theme

### Identity and access

Scale from one IdP to many: the open build handles single-IdP OIDC and SAML
login end to end; enterprise adds per-tenant **multi-IdP federation** (routing by
tenant or domain), **SSO enforcement** (require-SSO, block password login, IP
allow-list), a **SAML SP-metadata endpoint** for IdP configuration, integration
with **CyberArk Conjur** for secrets-backed identity and NHI lifecycle
actuation. **User accounts are unlimited in both** — there is no seat cap in any
edition, and no licence state can impose one.

### Content and data security

Deep content inspection across every governed channel — not just text scanning.
The open build runs deterministic PII/injection/jailbreak guardrails and a
deny-closed DLP egress gate. Enterprise adds a **content firewall** that inspects
messages, retrieval results and MCP renders for prompt-injection, exfiltration
and unsafe-action patterns; a **hook content inspector** that applies the same
depth to Claude Code hook `tool_input`; an **RTBF crypto-shred coordinator**
with policy-readiness checks and WORM coordination; and a **computer-use gate**
with OCR, timeline and deep-DLP governance for computer-use tool declarations.

### Threat and incident

A **threat-intel catalog** for the detection engine: a base catalog compiled into the
add-on, plus optional signed, versioned feed artifacts the operator pins a publisher key
for and applies (rollback refused, last-known-good kept, an expired artifact ignored),
where the open build relies on its own signals. Olivares operates no curated feed
distribution and publishes no release cadence. Automatic **circuit-breaker** suspension when an agent
crosses configurable thresholds (with auto-reset, cooldown and kill-switch
escalation). Access-path queries over the access graph ship in the open build.
A continuous attack-graph scanner is planned post-release
(`enterprise/attackgraph` does not exist in this cut). Bidirectional
**incident close-loop** sync
between the governance plane and incident-management tools, including a Teams bot
connector.

### Compliance and regulatory

The open build maps 26 framework catalogs and exports sealed OSCAL evidence.
Enterprise adds the named-regulation depth that regulated industries need:
**OSCAL SSP ingestion** and a FedRAMP-adjacent **POA&M builder**; a **DORA
Register-of-Information** generator structured to the templates of Commission
Implementing Regulation (EU) 2024/2956, with major-incident classification and
report drafting; an **ISO 42001 AIMS** certification-readiness pack (Statement of
Applicability, AI policy, risk register, impact assessments, lifecycle controls,
supplier governance); and **compliance-depth overlays** for sector-specific
requirements (TX TRAIGA, CA SB 53, IL HB 3773, CO SB 26-189, HIPAA, PCI DSS
4.0.1, FINRA GenAI, CCM, FedRAMP 20x KSIs).

These add-ons automate evidence gathering and report structuring. They do not
certify the organization or guarantee compliance — the certification itself comes
from an accredited body (ISO/IEC 42006:2025 for ISO 42001) or the relevant
authority (FedRAMP, ESAs for DORA). Every produced artifact carries that
disclaimer.

### Operations and resilience

**WORM retention governor** with named regulatory floors (SEC 17a-4, FINRA 4511,
CFTC 1.31) and a compliance-mode lock. Long-horizon **legal-hold
orchestration** that places object-lock holds on archived segments.
Examiner-grade **evidence bundles** (native records + verification verdict +
chain-of-custody). **Azure immutable-LOCKED** and **GCS Bucket-Lock** WORM
sinks alongside the open S3 Object Lock sink. An **HA durable event bus** over
NATS JetStream that upgrades enforcement-event delivery from at-most-once to
at-least-once with dedup. **Scheduled reports** with custom branding and
operator-uploaded templates. **Server-tool egress control** that enforces
`req.Tools` in the inference PEP.

### Integration

**CAEP/SSF SET transmitter** to emit agent-risk events to external SSF receivers
(signed SETs, RFC 8935). **Upstream credential provider** using RFC 8693
token-exchange for short-lived, audience-bound tokens instead of static
credentials. **Tool-pin verifier** that detects tool-definition changes and
rug-pulls (deny-closed on fingerprint mismatch). **MCP elicitation mediator**
for runtime governance of elicitation prompts, user responses and sampling
injection.

---

## What the commercial offering includes

1. **Legal exception to AGPL obligations.** Use the product commercially without
   copyleft — modify privately, embed, do not publish changes. The commercial
   license is a private contract; see `LICENSING.md`.

2. **The additive commercial add-ons — each an optional, separately-entitled
   capability.** Everything listed above: multi-IdP,
   SSO enforcement, content firewall, hook hardening, RTBF coordinator,
   computer-use gate, threat-intel, incident close-loop, circuit-breaker,
   OSCAL POA&M, DORA register, ISO 42001 AIMS, compliance-depth,
   WORM governor + holds + bundle + sinks, durable bus, report scheduler +
   branding + templates, server-tool egress, CAEP transmitter, token-exchange,
   tool-pin, MCP elicitation mediator, CyberArk Conjur, SAML SP-metadata.
   No single license grants the whole list; packaging and pricing on request.

3. **Support with published response targets.** Best-effort first-response
   targets (non-binding) documented in `SUPPORT.md`.

4. **No seat maths, ever.** Users are unlimited in every edition — add-ons are
   term-based entitlements, never a per-seat charge.

---

## What needs no commercial entitlement at all

Everything in the AGPL build, which includes:

- Full inventory, access map with permitted-vs-observed drift, sessions,
  orchestration graph, health/SLA
- Cedar authorization engine (RBAC + deny-overlay + scoped grants) with four
  deny-closed enforcement points
- Two-person approvals, break-glass with dual control, estate kill switch
- Claude Code governed operation (console launch/attach/govern/stop,
  managed-settings delivery)
- Single-IdP SSO (OIDC + SAML 2.0), WebAuthn/FIDO2, PIV/CAC, AAL step-up
- Non-human identity lifecycle, agent-identity federation
- Inline guardrails (PII, prompt-injection, jailbreak), DLP egress, BYOK/CMEK
- Hash-chained Ed25519-signed audit ledger, sealed append-only evidence
- 26 framework compliance catalogs, SIEM push (CEF/LEEF/syslog/OTLP/OCSF)
- FinOps budgets that deny or throttle spend
- Evals with blocking CI gate, red-team sandboxes (gVisor/Firecracker)
- Single static binary, SQLite or Postgres, Docker/K8s/Helm/air-gap,
  Terraform provider, SDKs
- S3 Object Lock WORM archival, backup/restore with chain verification

The complete feature matrix is in [feature-matrix.md](./feature-matrix.md). The
evaluation procedure is in [evaluation-guide.md](./evaluation-guide.md).

---

Pricing on request — enterprise@olivares.ai
