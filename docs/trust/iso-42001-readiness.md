<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ISO/IEC 42001:2023 — AIMS readiness (NOT a certification)

> **Status: Olivares.AI is not ISO/IEC 42001 certified and no certification audit
> has been engaged.** This maps ISO/IEC 42001:2023 Annex A (AI management system
> controls) to evidence the product produces. Machine-readable in the product:
> `GET /v1/m/compliance/frameworks/iso_42001` and `…/status` / `…/gaps`.
> Catalog honesty note carried in the data itself: the standard's normative text is
> paywalled, so control titles were verified via secondary sources — disclosed,
> not hidden (`modules/compliance/frameworks.go`, control A.4.5 note).

## Why ISO 42001 is the differentiator here

For most vendors, ISO 42001 is a management-system wrapper around *their own* use
of AI. For an **AI-governance control plane**, it is closer to the product's core
competence: the platform's job is to *produce* exactly the operational evidence an
AIMS requires — event logs of AI systems, oversight gates, impact/risk
classifications, supplier posture, provenance. Two consequences:

- A buyer running Olivares gets a head start on **their own** ISO 42001 program:
  the rows below are evidence feeds for the buyer's AIMS, regardless of our
  certification status.
- CSA **STAR for AI Level 2** is defined as ISO/IEC 42001 certification combined
  with a Valid-AI-ted AI-CAIQ (cloudsecurityalliance.org/star/ai, verified
  2026-06-12) — so the 42001 decision also gates the highest STAR tier. See
  [csa-star-ai-readiness.md](./csa-star-ai-readiness.md).

## Annex A control mapping

Transcription verified against `modules/compliance/frameworks.go` (2026-06-12;
**re-verified 2026-08-17** — the catalog still carries these same 13 Annex A IDs in this
order, and still exactly two of them with no capability).
The two `nil`-capability rows are **deliberately shipped as honest gaps** in the
product catalog — the platform cannot evidence them, and says so.

| Annex A | Control | Product capability | Verifiable evidence |
|---|---|---|---|
| A.6.2.8 | AI system event logs | Per-tenant append-only audit of agent/AI activity; live hash-chain verify; WORM/SIEM export | Ledger + export; 7-year default audit retention + Object-Lock archival |
| A.6.2.6 | AI operation & monitoring | Guardrail/anomaly findings, least-privilege drift, eval results, residency attestation | Findings; `GET /v1/m/accessmap/drift`; evals; `GET /v1/m/compliance/residency` |
| A.6.2.5 | AI system deployment | Deployment/change ledger; secure-by-default posture | Module VII records; `docs/SECURITY-HARDENING.md` |
| A.6.2.4 | Verification & validation | Eval results + adversarial/red-team findings as auditable V&V evidence | Evals module XII; red-team module XVIII (probes run only in the isolated sandbox) |
| A.5.2 | AI impact assessment process | Per-agent EU-AI-Act/NIST risk classifications feeding a documented impact/risk view | `GET /v1/m/compliance/risk`; classify/review is dual-controlled and audited |
| A.7.5 | Data provenance | Sealed, ledger-anchored CycloneDX AIBOM (model + dataset lineage); SPDX 3.0.1 AI-Profile export | `GET /v1/m/models/owned-models/{id}/aibom` (`?format=spdx`); catalog note: evidences lineage, **not** dataset statistical quality — absent until a first AIBOM is sealed |
| A.4.5 | System & computing resources | Per-inference token/compute/cost accounting; RBAC + tenant isolation; governed NHIs | FinOps cost samples; identity governance |
| A.9.2 | Responsible-use processes | Deny-by-default HITL/approval gates; observed access; drift | Approvals (quorum/SoD); access map |
| A.10.3 | Suppliers | Operator-**verified** GPAI posture per brokered model provider (tech docs / training-data summary / copyright policy / downstream info / Code of Practice) | `GET /v1/m/models/gpai-posture` (verified by the operator, never self-reported by the vendor) |
| A.8.4 | Communication of incidents | Threat findings + reconstructable, integrity-verified incident timeline | Ledger forensics; `docs/STATUS-AND-INCIDENT-COMMS.md` |
| A.8.2 | System documentation for users | System/agent inventory + transparency record | Inventory module I; posture export |
| A.3.2 | AI roles & responsibilities | **Honest gap** — organizational allocation of AI roles cannot be evidenced by the platform | `Capabilities: nil` in the catalog; reported as a gap, never `satisfied` |
| A.5.4 | Impact on individuals/groups | **Honest gap** — documented harms assessment (fairness/privacy/rights) is human work | Same honest-gap treatment; risk classifications are an *input*, not the assessment |

## AIMS gaps (the management-system distance)

1. **AI policy, AIMS scope, SoA** (clauses 4–6) — the formal artifacts, founder work.
2. **AI impact assessments** (A.5.4) actually performed and recorded for the
   product's own AI usage — note the product itself embeds limited AI (LLM-as-judge
   evals); the assessment is small but must exist.
3. **Roles & competence records** (A.3.2, clause 7) — trivial solo, must be written.
4. **Internal audit / management review** cadence — same outsourcing pattern as
   ISO 27001 (paid decision).

> **Founder decision recorded (2026-07-18), not pending:** certification is **not being
> pursued now** and is **demand-gated like the rest** — that third option is the one the
> decision took, so this page no longer presents it as a live three-way question.
>
> **Still the founder's:** if a buyer's demand does open an engagement, whether 42001 goes
> *first* (as the AI-governance differentiator, ahead of 27001/SOC 2 — unusual but
> defensible for this product category) or bundled with ISO 27001 (shared ISMS/AIMS
> machinery, one certification body), plus the body and the timing. Cost envelope is in the
> same recurring range as the other certifications. Consequence a buyer should know:
> CSA STAR for AI **Level 2** requires 42001 certification, so it is deferred with it.
