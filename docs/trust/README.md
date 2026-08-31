<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares AI — Trust & procurement package

**Audience:** the security, compliance, procurement and architecture teams evaluating
Olivares AI (self-hosted AI-agent control plane).
**Maintained by:** Olivares.AI — `enterprise@olivares.ai` (commercial), `security@olivares.ai` (security).
**Last revised:** 2026-08-17 (attestation status reconciled with the founder decision of
2026-07-18; see below).

This directory is the vendor-side package a buyer's security team consumes during
procurement: certification **readiness** (not certification claims), the third-party
penetration-testing program, the buyer-facing reference architecture, questionnaire
answer banks, EU-procurement crosswalks, machine-readable evidence, and the
accessibility/i18n posture.

## The honesty rule (read this first)

Every claim in this package follows the product's design-to-audit posture
(`docs/SECURITY-HARDENING.md`): **we state what is built and verifiable,
and we do not claim attestations we do not hold.** The same rule is enforced *inside*
the product: the compliance module's framework catalog carries per-framework
disclaimers ("not a certification"), and a control whose evidence is architectural
is reported `by_design` — never `satisfied` (`modules/compliance/frameworks.go`).

## Current attestation status (no surprises)

The table below carried the header "Status today (2026-06-12)" for nine weeks, which is the one
thing a status table must never say: *today* with a date that is not. The rows themselves did not
change in those nine weeks, and there is a reason on the record rather than an oversight — the
external engagements were **deferred by a founder decision on 2026-07-18**, which is why nothing
was attested, certified, submitted or contracted meanwhile. That decision is stated in each row
and in [what this package does not do](#what-this-package-does-not-do); it is a *deferral*, not a
pending question, and it does not commit to a future date.

| Item | Status (reviewed 2026-08-17) |
|---|---|
| SOC 2 Type I / Type II | **Not attested. Not engaged — deferred by founder decision (2026-07-18), demand-gated.** Readiness mapping: [soc2-readiness.md](./soc2-readiness.md) |
| ISO/IEC 27001:2022 | **Not certified. Not engaged — deferred by the same decision, demand-gated.** Readiness mapping: [iso-27001-readiness.md](./iso-27001-readiness.md) |
| ISO/IEC 42001:2023 | **Not certified. Not engaged — deferred by the same decision, demand-gated.** Readiness mapping: [iso-42001-readiness.md](./iso-42001-readiness.md) |
| CSA STAR for AI (Level 1, AI-CAIQ) | **Not submitted.** Self-assessments prepared: [caiq-v4-self-assessment.md](./caiq-v4-self-assessment.md), [ai-caiq-self-assessment.md](./ai-caiq-self-assessment.md), [csa-star-ai-readiness.md](./csa-star-ai-readiness.md) |
| Third-party penetration test | **None performed. No external test contracted — deferred by the 2026-07-18 decision, which instead directs internal automated adversarial campaigns before the public release.** Program & cadence commitment: [penetration-testing.md](./penetration-testing.md) |
| FIPS 140-3 | The optional FIPS build links the CMVP-**validated** Go module (cert **#5247**, active). That validates the *cryptographic module*, not the product or any deployment — see `docs/SCP-09-FIPS-STIG.md`. |
| FedRAMP / DoD ATO | **Explicitly out of scope** — see below. |
| Accessibility (VPAT/ACR) | Self-assessed ACR published on **VPAT 2.5Rev INT** (WCAG 2.1/2.2 + Revised Section 508 + EN 301 549 V3.2.1): `docs/accessibility/VPAT-olivares-admin.md`. The automated platform-accessibility-tree pass is complete for the report's recorded surfaces; the human NVDA/JAWS/VoiceOver walkthrough remains pending and is disclosed in the report. |
| OpenSSF Best Practices badge | Not yet registered (requires the public repository + first tagged release); criteria tracking: `docs/openssf-badge.md`. |

## FedRAMP / ATO: out of scope, and why

FedRAMP authorization is **not** on the near-term roadmap (deferred posture, post-v1).
Two facts make this honest rather than evasive:

1. FedRAMP's own *Minimum Assessment Scope* states that software
   *"delivered separately for installation on agency systems and not operated in a
   shared responsibility model … is not a cloud computing product or service and is
   entirely outside the scope of FedRAMP"*
   (fedramp.gov/docs/20x/minimum-assessment-scope/, verified 2026-06-12). A
   self-hosted Olivares deployment operated by the customer is exactly that case.
2. What FedRAMP 20x *does* get right for every buyer — machine-readable,
   continuously-checkable evidence — we adopt directly:
   [machine-readable-evidence.md](./machine-readable-evidence.md).

## Package contents

| Artifact | File |
|---|---|
| SOC 2 Type II readiness (TSC → evidence), incl. the **SOC 2 + AI evidence annex** | [soc2-readiness.md](./soc2-readiness.md) |
| ISO/IEC 27001:2022 readiness (Annex A → evidence) | [iso-27001-readiness.md](./iso-27001-readiness.md) |
| ISO/IEC 42001:2023 readiness (AIMS — the AI-governance differentiator) | [iso-42001-readiness.md](./iso-42001-readiness.md) |
| CSA STAR for AI / AI-CAIQ self-assessment readiness | [csa-star-ai-readiness.md](./csa-star-ai-readiness.md) |
| Security-questionnaire answer bank (SIG-2026-aligned, AI-CAIQ-ready) | [questionnaire-answer-bank.md](./questionnaire-answer-bank.md) |
| Third-party pen-test program: cadence, scope, remediation | [penetration-testing.md](./penetration-testing.md) |
| Reference architecture (topologies, trust zones, HA/DR, sizing, integrations) | [reference-architecture.md](./reference-architecture.md) |
| Support & response-target model (best-effort, non-binding) | [`SUPPORT.md`](../../SUPPORT.md) §Commercial support tiers |
| Accessibility conformance report (VPAT 2.5Rev INT) | `docs/accessibility/VPAT-olivares-admin.md` |
| i18n posture & roadmap (7-locale console, docs-site in EN + 6 locales) | [i18n-roadmap.md](./i18n-roadmap.md) |
| EU AI Act Annex IV technical-documentation template | [eu-ai-act-annex-iv.md](./eu-ai-act-annex-iv.md) |
| MCC-AI (EU model contractual clauses) crosswalk | [mcc-ai-crosswalk.md](./mcc-ai-crosswalk.md) |
| Machine-readable evidence (FedRAMP-20x-style, from the product API) | [machine-readable-evidence.md](./machine-readable-evidence.md) |
| IPv6 parity — audited dual-stack/IPv6-only declaration (M-21-07) | [ipv6-parity.md](./ipv6-parity.md) |
| DoD Zero Trust capability mapping — 45 capabilities, per-row evidence (AI estate) | [dod-zero-trust-mapping.md](./dod-zero-trust-mapping.md) |
| FedRAMP posture — 20x re-verified, MAS scope, KSI evidence map (no authorization claimed) | [fedramp-ready-posture.md](./fedramp-ready-posture.md) |
| DoD Impact Levels IL2/IL4/IL5/IL6 — product vs environment, FIPS/STIG real state, out-of-scope | [impact-levels.md](./impact-levels.md) |
| Agent safety case (forward-looking bundle) | [agent-safety-case.md](./agent-safety-case.md) |
| Single-vendor risk mitigation (the viability objection, answered) | [vendor-viability.md](./vendor-viability.md) |
| CCM v4 self-assessment (17 domains, evidence per control area) | [caiq-v4-self-assessment.md](./caiq-v4-self-assessment.md) |
| AI-CAIQ / AICM v1.1 self-assessment (18 domains incl. Model Security) | [ai-caiq-self-assessment.md](./ai-caiq-self-assessment.md) |
| Evaluation kit (ordered proof-of-value package) | [evaluation-kit.md](./evaluation-kit.md) |
| Operational adopter checklist (install to verified evidence) | [adopter-checklist.md](./adopter-checklist.md) |
| Buyer evaluation guide (10-day POV with pass/fail criteria) | [evaluation-guide.md](./evaluation-guide.md) |
| Evaluation report template (criteria, evidence, findings and verdict) | [evaluation-report-template.md](./evaluation-report-template.md) |
| Feature matrix — open core (AGPL) vs commercial add-ons | [feature-matrix.md](./feature-matrix.md) |
| Why the commercial add-ons? (value proposition + 3 promises) | [why-enterprise.md](./why-enterprise.md) |
| Commercial one-pager (for enterprise@ responses) | [one-pager.md](./one-pager.md) |

## What this package does NOT do

- It does **not** assert any certification, attestation, audit opinion or
  authorization that has not been issued. Readiness ≠ certificate.
- It does **not** replace the buyer's own assessment. It is built so that
  assessment is cheap: every mapping row points at a verifiable artifact
  (an API endpoint, a signed release asset, a repo file).
- Formal certification engagements (SOC 2 examination, ISO 27001/42001
  certification audits), the first third-party pen-test contract, and commercial
  support activation carry **real recurring cost**, and each artifact marks the
  decision point explicitly instead of implying it is done. Two states, kept apart
  because a reader deserves to know which one they are looking at:
  - **Decided and recorded (2026-07-18):** no external pen-test and no SOC 2
    examination for now — internal, automated adversarial campaigns run before the
    public release instead — and every certification is **demand-gated**, i.e.
    engaged when a concrete buyer requires it. This is a decision taken, not a
    question waiting: nobody needs to re-ask it, and it names no future date.
  - **Still the founder's to make, and unchanged by that decision:** which audit
    firm or certification body, SOC 2 Type I-first versus straight Type II, the
    observation window, and each paid item (a SIG subscription, CSA Valid-AI-ted
    scoring, escrow terms). Those are named where they arise, not settled here.
