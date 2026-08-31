<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# CSA STAR for AI / AI-CAIQ — self-assessment readiness (NOT submitted)

> **Status: prepared, not submitted.** Olivares.AI does not yet appear in the CSA
> STAR Registry. This document records what the program is (facts verified against
> cloudsecurityalliance.org on 2026-06-12), how ready the self-assessment is, and
> the exact submission decision pending.

## The program, verified

- **AI Controls Matrix (AICM) v1.1** — CSA's AI control framework: **18 security
  domains, 247 control objectives** (17 domains derived from the Cloud Controls
  Matrix v4 plus a new **Model Security** domain). v1.0 released 2025-07-09
  (243 objectives); the **v1.1 bundle (2026-06)** is what the in-repo catalog pins
  as the maintained source of truth (`modules/compliance/frameworks.go`). Official mappings: ISO/IEC 42001:2023, ISO 27001,
  NIST AI RMF 1.0, NIST AI 600-1, BSI AIC4, AIUC-1, and a dedicated
  AICM↔EU-AI-Act mapping.
- **AI-CAIQ v1.1** — the questionnaire form of the AICM, shipped with the v1.1
  bundle (supersedes v1.0.2 of 2025-10-16). Re-confirm the exact workbook version
  the STAR portal accepts at submission time.
- **STAR for AI** (launched 2025-10-23): **Level 1** = publish a completed AI-CAIQ
  self-assessment to the STAR Registry; **Level 1 Valid-AI-ted** = automated
  scoring of that submission (US$595, up to 10 scoring attempts/year; free for CSA
  corporate members); **Level 2** = third-party ISO/IEC 42001 certification +
  Valid-AI-ted AI-CAIQ.
- **Licensing constraint (why the questionnaire is not reproduced here):** CSA's
  terms allow personal, non-commercial use and fair-use quotation with attribution,
  but **prohibit redistribution or modification** of the matrix/questionnaire. The
  permitted publication path for a vendor is precisely the STAR Registry submission
  (and handing a completed AI-CAIQ to a customer). A "CCM & AICM Licensing FAQ"
  (2026-03-13) governs commercial/derivative use. We therefore keep **our answers**
  in our own structure and transcribe into the official workbook at submission time.

## Readiness position

The substance of the 18 domains is already answered by this trust package — the
self-assessment is a transcription exercise, not a discovery exercise:

| AICM area (18 domains: 17 CCM-derived + Model Security) | Where our answers live |
|---|---|
| Governance/GRC, audit & assurance | [README.md](./README.md) (status, no-claims), compliance module catalog + status endpoints, `GOVERNANCE.md` |
| Identity & access, logging & monitoring | [iso-27001-readiness.md](./iso-27001-readiness.md) rows A.5.15–A.5.18, A.8.15–A.8.16; access map drift |
| Cryptography & key management | BYOK/CMEK envelope, FIPS build, PQC posture (`docs/SCP-09-FIPS-STIG.md`, `docs/SEC-G3-CRYPTO-AGILITY-PQC.md`) |
| Data security & privacy lifecycle | PII discovery/DLP, RTBF (`docs/RIGHT-TO-ERASURE.md`), retention/legal hold (`docs/RECORDS-MANAGEMENT.md`), residency |
| Change control, interoperability, infrastructure | Reference architecture, change ledger, expand-contract migrations, SIEM/ITSM interop |
| Incident management & forensics | Ledger forensics, `docs/STATUS-AND-INCIDENT-COMMS.md`, `SECURITY.md` |
| Supply chain, transparency & accountability | `scripts/verify-release.sh` artifacts (cosign/SBOM/SLSA/OpenVEX), [vendor-viability.md](./vendor-viability.md) |
| Threat & vulnerability management | CVE SLA + reachability/VEX process (`SECURITY.md`), red-team module, [penetration-testing.md](./penetration-testing.md) |
| Business continuity & resilience | HA/DR with measured RPO/RTO (`docs/DR-RUNBOOK.md`), SLOs (`docs/17-PRODUCTION-READINESS-SLO.md`) |
| **Model Security** (the new domain) | Signed model admission, sealed AIBOM + SPDX 3.0.1 AI Profile, model cards, GPAI supplier posture, guardrails (OWASP Agentic/ASI catalog), kill switch |

Honest-answer policy for the workbook: where the true answer is "No" or "Partial"
(e.g. *no third-party pen-test yet*, *no certification*, *single-maintainer
organization*), the submission will say so — the same rule as everything else in
this package. A self-assessment that overclaims is worse than none.

## Submission path (pending decision)

1. Create/confirm the CSA organization account; download the current AI-CAIQ
   workbook (v1.1 with the AICM v1.1 bundle — confirm on the portal).
2. Transcribe answers from this package; mark N/A domains honestly (e.g.
   datacenter-physical domains for a self-hosted software vendor) with rationale.
3. Submit via cloudsecurityalliance.org/star/submit → Organization Submission
   (self-assessment).
4. Optional: Valid-AI-ted scoring (US$595) — recommended only once the answers are
   stable post-first-release.

> **Founder decision recorded (2026-07-18), not pending:** (a) the free Level 1
> self-assessment submission goes **at the first tagged release**, not now — the
> recommendation this page used to offer is the decision that was taken, and a registry
> entry reading "pre-release" would have aged poorly; (c) **Level 2 is deferred**, because
> it requires ISO/IEC 42001 certification and that certification is demand-gated by the
> same decision ([iso-42001-readiness.md](./iso-42001-readiness.md)).
>
> **Still the founder's:** (b) whether to spend on Valid-AI-ted scoring (a paid item, and
> only worth it once the answers are stable post-release).
