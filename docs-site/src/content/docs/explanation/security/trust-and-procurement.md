---
title: Trust & procurement
description: >-
  What a buyer's security team can verify today: certification readiness (not
  claims), the pen-test program, support response-target model, accessibility conformance,
  and machine-readable compliance evidence — and what is honestly not there yet.
---

This page is the entry point for security, compliance and procurement teams
evaluating Olivares AI. The product's compliance posture follows one rule,
enforced in code as much as in prose: **state what is built and verifiable; never
claim an attestation that does not exist.** The compliance module reports a
control backed only by design evidence as `by_design` — never `satisfied` — and
every framework entry in the catalog carries its own "not a certification"
disclaimer.

:::note[Current status, no surprises]
Olivares AI holds **no SOC 2 report, no ISO/IEC 27001 or 42001 certificate**, has
**not yet undergone a third-party penetration test**, and is **not** listed in the
CSA STAR Registry. What exists instead — and is arguably more useful pre-contract —
is a verifiable readiness package: control-by-control mappings to evidence you can
pull from a running deployment yourself, plus the explicit list of decisions
(certification engagements, pen-test contracting, commercial support activation)
that remain open. FedRAMP/ATO is explicitly out of scope for the self-hosted
product.
:::

## The trust package

The full buyer-facing package lives in the repository under `docs/trust/`:

- **Certification readiness** — SOC 2 Type II, ISO/IEC 27001:2022 and ISO/IEC
  42001:2023 mappings from each control to the product capability and the live
  evidence endpoint that backs it, including the AI-specific evidence a 2026
  auditor asks about (prompt/interaction logging, model versioning, lineage,
  LLM sub-processor inventory).
- **Questionnaire answer bank** — pre-verified vendor answers aligned to the
  Shared Assessments SIG 2026 domains and ready to transcribe into a CSA AI-CAIQ
  for STAR for AI Level 1.
- **Penetration-testing program** — committed cadence (scoped third-party test at
  first commercial GA, annual thereafter, event-driven re-tests), scope, and a
  remediation workflow wired to the published CVE remediation targets in `SECURITY.md`.
- **Reference architecture** — deployment topologies (single-node, HA
  active-passive, multi-region, air-gapped), trust zones, measured sizing
  baselines, RPO/RTO tiers, and the IdP/SIEM/ITSM/KMS integration surface.
- **EU procurement artifacts** — an EU AI Act Annex IV technical-documentation
  template populated from live evidence, and a clause-by-clause crosswalk to the
  Commission's MCC-AI model contractual clauses (High-Risk and Light variants).
- **Agent safety case** — a forward-looking, CAE-style structured argument
  template with honest residual-risk columns.
- **Single-vendor risk** — the viability objection answered structurally: the
  AGPL core is the complete governance platform with nothing internally
  feature-capped to upsell (a small additive commercial line is built separately
  and distributed privately, absent from the open binary — it adds capability on
  top, never subtracting from the open core); in that open binary the
  license key is attestation-only and offline — it enables nothing — and builds
  are reproducible and provenance-attested, so continuity does not depend on the
  vendor's existence.

## What you can verify without trusting us

Self-hosting inverts the usual attestation relationship: most controls a SOC 2
report would attest, you can verify directly in your own deployment.

- **Releases:** cosign signatures, SBOM, SLSA Build L3 (SLSA v1.2) provenance, OpenVEX — see
  [Verify a release](/how-to/verify-a-release/).
- **Security contact & disclosure:** the reporting channel, the coordinated-disclosure
  timeline and the CVE remediation targets are published in `SECURITY.md` and advertised
  machine-readably at [`/.well-known/security.txt`](https://olivares.ai/.well-known/security.txt)
  (RFC 9116), so a scanner or researcher discovers the channel without asking.
- **Tamper-evidence:** the append-only, hash-chained, per-event-signed audit
  ledger verifies offline — see the [security model](/explanation/security/security-model/).
- **Live compliance evidence:** framework status, gap analysis, sealed evidence
  packages (JSON/CSV/OSCAL), model AIBOMs (CycloneDX 1.6 / SPDX 3.0.1 AI
  profile), model cards, and the regulatory calendar are all API responses, not
  PDFs — the product treats compliance dates and mappings as version-pinned data.
- **Operational claims:** SLOs, sizing and RPO/RTO numbers in the reference
  architecture trace to measured baselines committed in the repository.

## Support and accessibility

- The support model (tiers, severity-based response targets, escalation) is
  published in `SUPPORT.md` — including the honest disclosure that commercial
  support is defined but not yet purchasable, and that the escalation chain is
  one person deep today.
- The accessibility conformance report is a completed **VPAT 2.5Rev INT** edition
  ACR (WCAG 2.1/2.2 AA + Revised Section 508 + EN 301 549 V3.2.1) at
  `docs/accessibility/VPAT-olivares-admin.md`, with the formal
  assistive-technology pass still pending and disclosed as such. The console ships
  in English and Spanish; the i18n roadmap beyond EN/ES is demand-gated and
  documented in the trust package.

## Public Trust Center

The [Trust Center](https://olivares.ai/trust) on the product website surfaces
the same supply-chain artifacts described above in a public, standalone page:
SLSA Build L3 attestations, cosign signatures, SBOM downloads, OpenVEX
advisories, and the verification script. Commercial license holders can access
per-version compliance artifacts through the
[customer portal](https://licenses.olivares.ai/portal).

## Where to go next

- [Security model](/explanation/security/security-model/) — how the platform
  defends itself.
- [Threat model](/explanation/security/threat-model/) — adversaries and trust
  boundaries.
- [Honesty and limits](/start/honesty-and-limits/) — what runs today versus what
  is planned, product-wide.
- [Trust Center](https://olivares.ai/trust) — public supply-chain verification
  and compliance status.
