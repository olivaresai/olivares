<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# MCC-AI crosswalk — EU model contractual clauses → platform capability

> **What MCC-AI is (verified against the official documents, 2026-06-12).** The
> European Commission's Community of Practice on Public Procurement of AI publishes
> model contractual clauses for buying AI: **MCC-AI-High-Risk** (21 articles,
> sections A–F, annexes A–H) and **MCC-AI-Light** (15 articles; the cover sheet
> says "MCC-AI-High-Light", the body uses "MCC-AI-Light") plus a Commentary —
> version **February 2025**, announced 2025-03-05, available in 24 EU languages
> (public-buyers-community.ec.europa.eu). They are **voluntary templates** aligned
> to the AI Act, not a full contract and not an official Commission position.
>
> **Dual reading of this crosswalk.** (1) If a public buyer procures an AI system
> *governed by* Olivares, the platform is how the buyer **operates and evidences**
> the supplier's MCC-AI obligations at runtime. (2) Where a buyer attaches MCC-AI
> clauses to the Olivares contract itself: the control plane is governance tooling,
> not a high-risk AI system in typical use — most Section B obligations apply to
> the AI systems it governs, not to it; we answer honestly per clause below.

## MCC-AI-High-Risk — clause → capability → evidence

| Art. | Clause | Platform support | Evidence |
|---|---|---|---|
| 2 | Risk management system | Per-agent risk classification (EU tier × NIST), governed dual-control review; continuous findings | `GET /v1/m/compliance/risk`; findings API |
| 3 | Data and data governance | Dataset registry + sealed AIBOM lineage; PII discovery/DLP labels; residency attestation + egress scan | AIBOM/SPDX exports; discovery/DLP labels; `GET /v1/m/compliance/residency` |
| 4 | Technical documentation & instructions for use | Annex-IV-shaped doc pack generated from live evidence | [eu-ai-act-annex-iv.md](./eu-ai-act-annex-iv.md) |
| 5 | Record-keeping | Append-only, hash-chained, signed audit ledger; session recording; 7-year retention + WORM archival | Ledger verify; session recording; `docs/RECORDS-MANAGEMENT.md` |
| 6 | Transparency of the AI system | Inventory/transparency record; model cards; honest gap: Art-50-style content marking is not provided by the platform | Inventory; model cards |
| 7 | Human oversight | Deny-by-default HITL approvals, quorum/SoD, AAL3 step-up, audited break-glass, graduated kill switch | Approvals; step-up; break-glass; kill switch |
| 8 | Accuracy, robustness and cybersecurity | Evals + regression gates; adversarial red-team; guardrails (ASI01–10); hardened supply chain; published SLOs | Evals; red-team; `scripts/verify-release.sh`; [`docs/17-PRODUCTION-READINESS-SLO.md`](../17-PRODUCTION-READINESS-SLO.md) |
| 9 | Compliance with Section B | Live control status + gap analysis per framework | `GET /v1/m/compliance/frameworks/eu_ai_act/status` / `…/gaps` |
| 10 | Quality management system | Partial: engineering QMS evidence (CI gates, change ledger, signed releases); the organizational QMS narrative is provider work | [soc2-readiness.md](./soc2-readiness.md) §gaps |
| 11 | Conformity assessment | Sealed, ledger-anchored evidence packages exportable for assessment (JSON/CSV/OSCAL) | `GET /v1/m/compliance/evidence/{id}/export` |
| 12 | ⟨Optional⟩ Fundamental rights impact assessment | **Honest gap** — FRIA is human/legal work; the platform supplies inputs (risk classes, access/impact surface) but cannot evidence the assessment (mirrors ISO 42001 A.5.4 nil mapping) | [iso-42001-readiness.md](./iso-42001-readiness.md) |
| 13 | Corrective actions | Findings→remediation under the published SLA; model retire/deprecate; kill switch; signed patch releases | `SECURITY.md`; kill switch |
| 14 | ⟨Optional⟩ Explanation of individual decision-making | Partial: reconstructable per-session timeline (who/what/when, tools, approvals) — **not** model-internal explanations | Session timeline; ledger forensics |
| 15–18 | Rights to data sets, handover, indemnification | Structural answer: **self-hosted — no mandatory telemetry and no control-plane egress by default**, so what crosses the buyer's perimeter is what the buyer configures to cross it — model API calls, the SIEM/webhook outputs they wire, an external embedding provider if they provision one; exports are open formats (JSON/CSV/OSCAL/CycloneDX/SPDX); handover is a non-event | [reference-architecture.md](./reference-architecture.md) |
| 19 | ⟨Optional⟩ AI register | Inventory + posture export feed the buyer's register directly (machine-readable) | `GET /v1/m/posture/export` |
| 20 | Compliance and audit | Read-only auditor access via RBAC; offline ledger verification (`--pubkey`); evidence exports; full audit trail of the audit itself (evidence reads are audited) | Compliance module routes |
| 21 | Costs | Contractual matter; the platform adds per-identity/per-outcome cost accounting for transparency | FinOps cost accounting |

**Annexes A–H** (system description, data sets, technical documentation,
instructions, transparency, oversight, accuracy, robustness/cybersecurity): each
maps to the corresponding row above; the Annex-IV packaging workflow produces the
technical annexes from live evidence.

## MCC-AI-Light (non-high-risk procurement)

The Light variant keeps the Section B themes (risk management, data governance,
documentation, record-keeping, transparency, oversight, accuracy/robustness/
cybersecurity, compliance, FRIA, individual-decision explanation) without QMS/
conformity-assessment/audit machinery. Rows 2–9, 12, 14 above apply unchanged.
This is the realistic variant for procuring **Olivares itself** (governance
tooling): our per-clause answers are the same, including the two honest
partials/gaps (FRIA, model-internal explainability).

## Buyer quick-start

A public buyer evaluating against MCC-AI can replay this crosswalk in under an
hour: deploy single-node ([reference-architecture.md](./reference-architecture.md)
§T1), then call the endpoints in the Evidence column — every claim above is a
live API response or a repo artifact, not prose.
