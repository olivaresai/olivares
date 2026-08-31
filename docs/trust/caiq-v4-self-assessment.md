<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Cloud Controls Matrix v4 — Self-assessment

> **Status:** Self-assessment as of 2026-07-06. NOT submitted to CSA STAR Registry (submission decision pending). This document contains Olivares AI's self-assessment in its own words — the CSA questionnaire is proprietary and not reproduced here.

This is a buyer due-diligence artifact for a self-hosted product. It is not a
certification, audit opinion, or external attestation.

## A&A — Audit & Assurance

Scope note: Olivares AI supplies the audit-evidence substrate inside customer-operated deployments; independent assurance programs and buyer audit scheduling remain organizational activities.

| Control area | Posture | Evidence |
|---|---|---|
| Tamper-evident audit evidence substrate | Implemented | `modules/compliance/frameworks.go:1305-1310`; `docs/SECURITY-HARDENING.md:74-83`; `docs/trust/machine-readable-evidence.md:31-34` |
| Machine-readable control status and evidence packages | Implemented | `docs/trust/machine-readable-evidence.md:24-45`; `docs/trust/machine-readable-evidence.md:51-58` |
| External assurance / CSA STAR submission | Planned | `docs/trust/README.md:28-40`; `docs/trust/csa-star-ai-readiness.md:8-11`; `docs/trust/csa-star-ai-readiness.md:53-56` |

## AIS — Application & Interface Security

Scope note: The engine and embedded console are self-hosted; customers operate the ingress, WAF, TLS termination choices beyond the engine, and any edge rate limits.

| Control area | Posture | Evidence |
|---|---|---|
| Secure application/API defaults | Implemented | `docs/SECURITY-HARDENING.md:47-54`; `docs/SECURITY-HARDENING.md:85-93`; `docs/SECURITY-HARDENING.md:196-198` |
| Agent/application guardrail detection and red-team coverage | Partial | `modules/compliance/frameworks.go:1313-1318`; `modules/compliance/frameworks.go:1462-1480`; `docs/trust/penetration-testing.md:8-12` |
| Anonymous login/setup edge protection | Partial | `docs/SECURITY-HARDENING.md:156-192` |

## BCR — Business Continuity & Resilience

Scope note: Olivares ships backup/restore tooling and DR verification guidance; customers schedule backups, store bundles off-site, run drills, and operate HA infrastructure.

| Control area | Posture | Evidence |
|---|---|---|
| Verified DR bundle and continuity checks | Implemented | `docs/DR-RUNBOOK.md:40-45`; `docs/DR-RUNBOOK.md:152-162`; `docs/DR-RUNBOOK.md:174-184` |
| RPO/RTO tiers and restore procedure | Partial | `docs/DR-RUNBOOK.md:102-113`; `docs/trust/reference-architecture.md:83-94` |
| Customer backup cadence and crisis-management process | N-A (customer responsibility) | Customer responsibility in self-hosted deployments; `docs/DR-RUNBOOK.md:110-113`; `modules/compliance/frameworks.go:1320-1325` |

## CCC — Change Control & Configuration

Scope note: Olivares controls its source, build, release, and product change features; customers control deployment configuration, rollout approval, and environment drift.

| Control area | Posture | Evidence |
|---|---|---|
| Secure default configuration | Implemented | `docs/SECURITY-HARDENING.md:196-198`; `modules/compliance/frameworks.go:1328-1333` |
| Change/deploy ledger and online migration posture | Partial | `modules/compliance/frameworks.go:600-604`; `docs/trust/soc2-readiness.md:58-59` |
| Signed release and artifact verification contract | Partial | `docs/RELEASE-VERIFICATION.md:18-31`; `docs/RELEASE-VERIFICATION.md:35-53`; `docs/RELEASE-VERIFICATION.md:121-128` |

## CEK — Cryptography, Encryption & Key Management

Scope note: The product provides transport encryption, signing, and key-custody seams; at-rest encryption and many key-management procedures depend on the customer deployment.

| Control area | Posture | Evidence |
|---|---|---|
| TLS/mTLS and authenticated local plugin channel posture | Implemented | `docs/SECURITY-HARDENING.md:47-54`; `docs/trust/reference-architecture.md:124-137` |
| At-rest encryption | Partial | `docs/SECURITY-HARDENING.md:71-72`; `docs/SECURITY-HARDENING.md:285`; `docs/trust/reference-architecture.md:161-162` |
| Offline license-signing and DR key custody model | Partial | `LICENSING.md`; `docs/DR-RUNBOOK.md:68-81` |

## DSP — Data Security & Privacy

Scope note: The control plane minimizes what it stores and produces privacy evidence; customers remain controllers/operators for production data, legal bases, notices, and data-subject workflows.

| Control area | Posture | Evidence |
|---|---|---|
| Data minimization and redaction | Implemented | `docs/trust/reference-architecture.md:45-47`; `docs/trust/questionnaire-answer-bank.md:42-46`; `docs/SECURITY-HARDENING.md:63-72` |
| PII/sensitivity discovery and DLP posture | Implemented | `modules/compliance/frameworks.go:1351-1356`; `modules/compliance/frameworks.go:564-568`; `README.md:89-90` |
| Residency, retention, legal hold, and erasure support | Partial | `docs/trust/questionnaire-answer-bank.md:44-46`; `cmd/olivares/wire_noenterprise.go:250-256`; `docs/trust/reference-architecture.md:77-81` |

## GRC — Governance, Risk & Compliance

Scope note: Olivares provides framework catalogs, risk classifications, and evidence exports; certification decisions, statements of applicability, and buyer governance ownership remain organizational.

| Control area | Posture | Evidence |
|---|---|---|
| Versioned framework catalog and honesty rule | Implemented | `modules/compliance/frameworks.go:6-17`; `docs/trust/README.md:19-27`; `docs/trust/machine-readable-evidence.md:51-58` |
| Compliance APIs, gaps, and evidence exports | Implemented | `docs/trust/machine-readable-evidence.md:29-45`; `README.md:73-75` |
| Formal certifications and attestations | Planned | `docs/trust/README.md:28-40`; `docs/trust/README.md:77-88` |

## HRS — Human Resources

Scope note: For customer deployments, personnel screening, onboarding, offboarding, and training are customer controls; Olivares.AI also discloses its current single-maintainer vendor posture.

| Control area | Posture | Evidence |
|---|---|---|
| Customer HR security controls for operators and users | N-A (customer responsibility) | Self-hosted software; `modules/compliance/frameworks.go:1365-1370`; `docs/trust/soc2-readiness.md:87-91` |
| Olivares.AI vendor personnel maturity | Partial | `docs/trust/questionnaire-answer-bank.md:28-33`; `docs/trust/vendor-viability.md:8-12`; `docs/trust/vendor-viability.md:71-83` |
| Future formal people-control evidence | Planned | `docs/trust/soc2-readiness.md:73-91`; `docs/trust/iso-27001-readiness.md:49-64` |

## IAM — Identity & Access Management

Scope note: Olivares enforces product RBAC and tenant isolation and integrates with customer IdPs; customers own identity source accuracy, MFA policy, and endpoint login posture.

| Control area | Posture | Evidence |
|---|---|---|
| RBAC, tenant isolation, and access observability | Implemented | `docs/SECURITY-HARDENING.md:108-117`; `modules/compliance/frameworks.go:1373-1377`; `README.md:85-89` |
| Single-IdP SSO and enterprise multi-IdP boundary | Partial | `LICENSING.md`; `cmd/olivares/wire_noenterprise.go:46-70` |
| User accounts (no cap in any edition) | Implemented | `LICENSING.md`; `core/auth/seatcap.go:48-78`; `core/auth/seatcap.go:154-174`; `cmd/olivares/wire_noenterprise.go:87-98` |

## IPY — Interoperability & Portability

Scope note: Olivares exports evidence and operational data in open formats; portability of customer AI workloads, cloud infrastructure, or model providers is a deployment architecture responsibility.

| Control area | Posture | Evidence |
|---|---|---|
| Open evidence and telemetry export formats | Implemented | `docs/trust/machine-readable-evidence.md:29-49`; `docs/trust/vendor-viability.md:52-56` |
| Product source and rebuild portability | Implemented | `docs/trust/vendor-viability.md:40-48`; `README.md:171-186` |
| Customer AI workload/provider portability | N-A (customer responsibility) | Self-hosted deployment architecture; `modules/compliance/frameworks.go:1380-1385` |

## IVS — Infrastructure & Virtualization Security

Scope note: Olivares secures the control plane process, tenant boundary, and deployment defaults; host, hypervisor, Kubernetes, and network-layer hardening belong to the customer environment.

| Control area | Posture | Evidence |
|---|---|---|
| Control-plane trust zones and tenant isolation | Implemented | `docs/trust/reference-architecture.md:15-42`; `docs/SECURITY-HARDENING.md:108-117` |
| Secure factory posture | Implemented | `docs/SECURITY-HARDENING.md:196-198`; `docs/trust/reference-architecture.md:124-137` |
| Host, hypervisor, and cluster hardening | N-A (customer responsibility) | Self-hosted infrastructure; `modules/compliance/frameworks.go:1388-1393` |

## LOG — Logging & Monitoring

Scope note: Olivares produces and protects product and AI-estate logs inside the customer deployment; customers operate SIEM retention, alert routing, and SOC workflows.

| Control area | Posture | Evidence |
|---|---|---|
| Append-only signed audit ledger | Implemented | `docs/SECURITY-HARDENING.md:74-83`; `modules/compliance/frameworks.go:1395-1400`; `docs/trust/reference-architecture.md:31-35` |
| WORM/SIEM export and posture export | Implemented | `docs/trust/reference-architecture.md:139-151`; `docs/trust/questionnaire-answer-bank.md:63-65` |
| Evidence-read auditing and continuous validation | Implemented | `docs/trust/machine-readable-evidence.md:24-27`; `docs/trust/machine-readable-evidence.md:51-58` |

## SEF — Security Incident Management & Forensics

Scope note: The product supplies vulnerability disclosure, forensic reconstruction, and evidence export; customer incident command, notification decisions, and regulator reporting are customer-owned.

| Control area | Posture | Evidence |
|---|---|---|
| Vulnerability disclosure and remediation process | Partial | `SECURITY.md:5-28`; `SECURITY.md:58-78`; `SECURITY.md:81-94` |
| Forensic timeline and incident evidence | Implemented | `modules/compliance/frameworks.go:1411-1416`; `docs/SECURITY-HARDENING.md:78-83`; `docs/trust/questionnaire-answer-bank.md:63-67` |
| Third-party penetration test program | Planned | `docs/trust/penetration-testing.md:8-21`; `docs/trust/penetration-testing.md:76-82` |

## STA — Supply Chain Management & Transparency

Scope note: Olivares controls its release, license, and component transparency; customers control admission policy, registry mirroring, and approval of deployed artifacts.

| Control area | Posture | Evidence |
|---|---|---|
| Release artifacts, SBOM, VEX, SLSA, and verification | Partial | `docs/RELEASE-VERIFICATION.md:18-31`; `docs/RELEASE-VERIFICATION.md:35-53`; `docs/RELEASE-VERIFICATION.md:81-100`; `docs/RELEASE-VERIFICATION.md:121-128` |
| Open-core licensing and connector boundary transparency | Implemented | `README.md:171-186`; `LICENSING.md`; `cmd/olivares/wire_noenterprise.go:30-44` |
| Model/provider supply-chain evidence hooks | Partial | `modules/compliance/frameworks.go:1418-1423`; `docs/trust/questionnaire-answer-bank.md:75-80` |

## TVM — Threat & Vulnerability Management

Scope note: Olivares maintains its vulnerability program and threat model; customers remain responsible for scanning and patching their operating systems, clusters, databases, endpoints, and ingress.

| Control area | Posture | Evidence |
|---|---|---|
| Threat model and hardening verification | Implemented | `docs/SECURITY-HARDENING.md:31-134`; `docs/SECURITY-HARDENING.md:257-267` |
| Dependency and release vulnerability handling | Partial | `SECURITY.md:58-78`; `docs/SECURITY-HARDENING.md:202-215`; `modules/compliance/frameworks.go:1425-1430` |
| External penetration testing | Planned | `docs/trust/penetration-testing.md:8-21`; `docs/trust/penetration-testing.md:76-82` |

## UEM — Universal Endpoint Management

Scope note: Olivares is not an endpoint-management platform and does not manage customer laptops, mobile devices, EDR posture, or MDM enrollment.

| Control area | Posture | Evidence |
|---|---|---|
| Endpoint/device fleet management | N-A (customer responsibility) | Customer responsibility — self-hosted software. `modules/compliance/frameworks.go:1433-1438`; `docs/trust/reference-architecture.md:17-42` |
| Device posture enforcement for operators | N-A (customer responsibility) | Customer responsibility — self-hosted software. Product IAM integrates with customer identity controls; `README.md:88-89` |

## DCS — Datacenter Security

Scope note: Olivares does not operate the datacenter or cloud region for self-hosted deployments; physical and environmental controls belong to the customer or their infrastructure provider.

| Control area | Posture | Evidence |
|---|---|---|
| Physical datacenter security | N-A (customer responsibility) | Customer responsibility — self-hosted software. `modules/compliance/frameworks.go:1343-1348`; `docs/trust/README.md:42-55` |
| Environmental controls and physical media handling | N-A (customer responsibility) | Customer responsibility — self-hosted software. The product runs inside the customer perimeter; `docs/trust/reference-architecture.md:15-19` |
