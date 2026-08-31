<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# FedRAMP posture — what applies, what we meet, what we do not claim

> **Status: no FedRAMP authorization exists, none is pending, and — decisive fact first — for
> the product as actually sold today (self-hosted), FedRAMP does not apply at all.** The FedRAMP
> Minimum Assessment Scope (FRR-MAS; re-verified against fedramp.gov, including the Consolidated
> Rules for 2026 scope page, on **2026-07-09**) states that software *"delivered separately for
> installation on agency systems and not operated in a shared responsibility model … is not a
> cloud computing product or service and is **entirely outside the scope of FedRAMP** under the
> FedRAMP Authorization Act."* A self-hosted Olivares deployment — the only offering that exists
> — is exactly that case. An agency buys it like other installed software (via its own ATO
> process, where [impact-levels.md](./impact-levels.md) and the evidence API below do the work),
> not through the FedRAMP Marketplace.

## The live 20x picture (re-verified 2026-07-09, fedramp.gov)

This page updates the 2026-06-12 snapshot in
[machine-readable-evidence.md](./machine-readable-evidence.md); the program moved:

- **FedRAMP 20x is in Phase 3**; pilots concluded. Consolidated Rules for 2026 were slated for
  completion by June 2026 with the submission pipeline opening in Q4 FY26. Certification classes
  A (mature programs), B (small-scale/light-use) and C (common enterprise services) are defined;
  Class D (High) is estimated FY27.
- **An agency sponsor is no longer the gate it was.** Phase 1's own findings state: *"Program
  authorization without an agency sponsor opened the door to offerings such as GRC tools."*
  The earlier framing "FedRAMP authorization is unreachable without an agency sponsor" is
  therefore **obsolete** and we retire it here. What remains true: authorization applies to a
  **cloud service offering** operated by the provider, with real assessment cost and continuous
  machine-readable reporting.
- **Key Security Indicators (KSI)** are the 20x evidence unit — themes including `KSI-IAM`,
  `KSI-MLA` (Monitoring, Logging & Auditing), `KSI-CNA` (Cybersecurity & Network Architecture),
  `KSI-SVC` (Service Hardening), `KSI-CMT` (Change Management), `KSI-INR` (Incident Response),
  `KSI-RPL` (Resilience & Recoverability), `KSI-TPR` (Third-Party Risk), `KSI-CED`, `KSI-PIY`
  (RFC-0006 Phase 1; RFC-0014 Phase 2). Phase 2 language: *"FedRAMP will expect truly automated
  and opinionated validation of Key Security Indicators for a Moderate authorization."*

## So what does "FedRAMP-ready posture" honestly mean for Olivares?

Two distinct, real things — neither of which is an authorization claim:

**1. For the agency's own ATO of a self-hosted deployment** (the case that exists): the product
ships the control evidence an assessor asks for, machine-readable and regenerable — the same
model 20x mandates for cloud offerings:

| 20x evidence idea (KSI theme) | Product counterpart | Evidence anchor |
|---|---|---|
| KSI-IAM — identity, MFA, least privilege | Federation (SAML/OIDC, PIV path), AAL step-up, deny-closed scoping | `core/auth/federation.go`, `core/auth/piv.go`, `core/auth/assurance.go:21-31`, `modules/sourcescope` |
| KSI-MLA — monitoring/logging/auditing | Append-only hash-chained ledger with **live chain verify exposed as evidence**; SIEM push; WORM archive + legal hold | `core/store/audit.go:12`, `modules/compliance/evidence.go:23`, `modules/siemforward/forwarder.go`, `core/audit/archivecaps.go` |
| KSI-CNA — architecture, segmentation, encryption in transit | TLS-on-by-default single binary; deny-by-default egress gate for isolated runs; documented network posture | `cmd/olivares/cmd_serve.go`, `core/secure/tls.go`, `core/runtime/sandboxrt/proxy.go:17-34`, [ipv6-parity.md](./ipv6-parity.md) |
| KSI-SVC — hardening, FIPS-validated crypto | FIPS 140-3 build variant on the CMVP-validated Go module (cert #5247, ACTIVE, re-verified 2026-07-09); STIG-hardened image with OpenSCAP pipeline | `Dockerfile.fips`, `Dockerfile.stig`, `oscap/`, `docs/SCP-09-FIPS-STIG.md` |
| KSI-CMT — change management | Signed releases, SLSA Build L3 provenance, schema-parity upgrade gate, OTA signed manifests | `docs/CRA-READINESS.md`, `cmd/olivares/cmd_upgrade.go` |
| KSI-INR — incident response | Findings→notification→HITL decision→enforcement loop; kill switch; forensics timeline | `modules/eventing`, `modules/sessions/runtime_killswitch.go`, `modules/security/forensic.go` |
| KSI-RPL — resilience/recovery | DR backup/restore with drills (`olivares dr`), documented RTO/RPO | `core/dr/dr.go`, `docs/DR-RUNBOOK.md` |
| KSI-TPR — third-party risk | Deny-closed signed admission for models/connectors/MCP; SBOM (CISA 2025 fields) per release | `core/secure/modelsign/`, `cmd/olivares/externalplugins.go`, release SBOMs |
| Machine-readable, regenerable | The evidence API — statuses, gaps, OSCAL export — pullable per tenant, itself audited | [machine-readable-evidence.md](./machine-readable-evidence.md), `modules/posture-export/postureexport.go`, `modules/compliance` |

**2. For a hypothetical future managed (SaaS) offering**: the scope analysis flips — a cloud
offering IS FedRAMP's subject, and the honest statement becomes "the evidence plumbing above is
built to 20x's model (automated KSI validation, programmatic access), but authorization is a
program decision with recurring cost that has NOT been taken." That decision point is recorded,
not implied done — the same treatment every other engagement gets in
[README.md](./README.md#what-this-package-does-not-do), which keeps what has been *decided*
(the 2026-07-18 deferral of external assurance) apart from what is still the founder's.

## What we explicitly do NOT claim

- No FedRAMP authorization, no "In Process" status, no Marketplace listing, no 3PAO engagement.
- "FedRAMP-ready" here never means "authorized-equivalent"; it names the two postures above.
- We do not claim the KSI mapping is an assessment: it shows where each theme's evidence lives so
  an assessor (agency ATO or future 3PAO) starts from artifacts, not prose.

*Sources (primary, all re-read 2026-07-09): fedramp.gov/20x (Phase 3, classes, sponsor finding);
fedramp.gov RFC-0006 and RFC-0014 (KSI themes and Phase-2 validation language);
FRR-MAS / Consolidated Rules 2026 scope (self-hosted exclusion).*
