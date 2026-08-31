<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# AI Controls Matrix v1.1 / AI-CAIQ — Self-assessment

> **Status:** Self-assessment as of 2026-07-06. NOT submitted to CSA STAR Registry (submission decision pending). This document contains Olivares AI's self-assessment in its own words — the CSA AI-CAIQ questionnaire is proprietary and not reproduced here.

This is a domain-level self-assessment for enterprise due diligence, aligned to
**CSA AICM v1.1** — the version the in-repo catalog pins as the maintained source
of truth (`modules/compliance/frameworks.go`; AICM v1.1 ships with AI-CAIQ v1.1
and keeps the same 18-domain structure). Answers are in Olivares AI's own words,
without reproducing CSA proprietary questionnaire text.

## A&A — Audit & Assurance

Scope note: Olivares supplies audit evidence for AI-agent governance inside customer-operated deployments; independent audit execution remains outside the product.

| Control area | Posture | Evidence |
|---|---|---|
| AI governance evidence substrate | Implemented | `modules/compliance/frameworks.go:1281-1302`; `modules/compliance/frameworks.go:1305-1310`; `docs/trust/machine-readable-evidence.md:29-45` |
| Evidence package sealing and export | Implemented | `docs/trust/eu-ai-act-annex-iv.md:45-55`; `docs/trust/machine-readable-evidence.md:31-45` |
| STAR for AI / AI-CAIQ submission | Planned | `docs/trust/csa-star-ai-readiness.md:58-72`; `docs/trust/README.md:28-40` |

## AIS — Application & Interface Security

Scope note: Olivares secures its API, console, and governed agent interfaces; customers operate network ingress and any external application security tooling.

| Control area | Posture | Evidence |
|---|---|---|
| Secure defaults for API and console | Implemented | `docs/SECURITY-HARDENING.md:47-54`; `docs/SECURITY-HARDENING.md:85-93`; `README.md:96-113` |
| AI application guardrails and adversarial testing | Partial | `modules/compliance/frameworks.go:1061-1132`; `modules/compliance/frameworks.go:1462-1480`; `docs/trust/agent-safety-case.md:42-43` |
| Public edge protections | Partial | `docs/SECURITY-HARDENING.md:156-192` |

## BCR — Business Continuity & Resilience

Scope note: The product ships verifiable DR mechanics; customers are responsible for scheduling, off-site custody, restore exercises, and infrastructure failover.

| Control area | Posture | Evidence |
|---|---|---|
| Ledger-safe backup and restore | Implemented | `docs/DR-RUNBOOK.md:11-16`; `docs/DR-RUNBOOK.md:40-45`; `docs/DR-RUNBOOK.md:152-162` |
| AI evidence continuity after restore | Implemented | `docs/DR-RUNBOOK.md:188-201`; `docs/DR-RUNBOOK.md:203-215` |
| Customer continuity program | N-A (customer responsibility) | Customer-operated deployment; `docs/DR-RUNBOOK.md:174-184`; `modules/compliance/frameworks.go:1320-1325` |

## CCC — Change Control & Configuration

Scope note: Olivares records and governs product and AI-system changes where it is integrated; customer release boards and environment change windows remain customer processes.

| Control area | Posture | Evidence |
|---|---|---|
| AI deployment/change records | Partial | `docs/trust/eu-ai-act-annex-iv.md:40-41`; `modules/compliance/frameworks.go:1328-1333` |
| AI model/change lifecycle hooks | Partial | `docs/trust/soc2-readiness.md:68-72`; `docs/trust/questionnaire-answer-bank.md:75-78` |
| Release verification for product changes | Partial | `docs/RELEASE-VERIFICATION.md:18-31`; `docs/RELEASE-VERIFICATION.md:102-141` |

## CEK — Cryptography, Encryption & Key Management

Scope note: Product-controlled channels and signatures are covered; customer storage encryption, KMS policy, and cloud key lifecycle remain deployment controls.

| Control area | Posture | Evidence |
|---|---|---|
| Transport encryption and mTLS posture | Implemented | `docs/SECURITY-HARDENING.md:47-54`; `docs/trust/reference-architecture.md:124-137` |
| AI/model evidence signing | Implemented | `docs/trust/questionnaire-answer-bank.md:75-76`; `modules/compliance/frameworks.go:1403-1408` |
| Storage encryption and key-management operations | Partial | `docs/SECURITY-HARDENING.md:71-72`; `modules/compliance/frameworks.go:1335-1340`; `docs/DR-RUNBOOK.md:68-81` |

## DSP — Data Security & Privacy

Scope note: The product minimizes and labels governed data paths; customers remain responsible for lawful processing, notices, data-source stewardship, and legal decisions.

| Control area | Posture | Evidence |
|---|---|---|
| AI data minimization and lineage | Implemented | `docs/trust/reference-architecture.md:45-47`; `modules/compliance/frameworks.go:1351-1356`; `docs/trust/eu-ai-act-annex-iv.md:35-40` |
| PII/DLP and residency controls | Partial | `docs/trust/questionnaire-answer-bank.md:43-45`; `modules/compliance/frameworks.go:1469-1480`; `docs/trust/reference-architecture.md:77-81` |
| Privacy rights and erasure evidence | Partial | `docs/trust/questionnaire-answer-bank.md:45-46`; `modules/compliance/frameworks.go:700-710`; `cmd/olivares/wire_noenterprise.go:250-256` |

## GRC — Governance, Risk & Compliance

Scope note: Olivares provides AI governance evidence and catalogs; customers decide applicability, risk acceptance, and compliance filings.

| Control area | Posture | Evidence |
|---|---|---|
| AI framework catalog and risk classification | Implemented | `modules/compliance/frameworks.go:6-24`; `modules/compliance/frameworks.go:1358-1362`; `docs/trust/machine-readable-evidence.md:31-45` |
| EU AI Act / Annex IV evidence packaging | Partial | `docs/trust/eu-ai-act-annex-iv.md:14-21`; `docs/trust/eu-ai-act-annex-iv.md:31-44`; `docs/trust/eu-ai-act-annex-iv.md:56-61` |
| Formal AI governance certifications | Planned | `docs/trust/iso-42001-readiness.md:8-15`; `docs/trust/iso-42001-readiness.md:54-69` |

## HRS — Human Resources

Scope note: AI operator training, employee screening, and HR lifecycle controls are not provided by self-hosted software; Olivares discloses its vendor maturity gaps.

| Control area | Posture | Evidence |
|---|---|---|
| Customer AI user training and HR controls | N-A (customer responsibility) | Self-hosted customer process; `modules/compliance/frameworks.go:1365-1370`; `modules/compliance/frameworks.go:788-795` |
| Olivares vendor personnel maturity | Partial | `docs/trust/questionnaire-answer-bank.md:28-33`; `docs/trust/vendor-viability.md:8-12`; `docs/trust/soc2-readiness.md:87-91` |

## IAM — Identity & Access Management

Scope note: Product RBAC, tenant isolation, and non-human identity governance are built into the control plane; customer IdP policy and MFA enforcement remain external.

| Control area | Posture | Evidence |
|---|---|---|
| Agent/non-human identity governance | Implemented | `README.md:72-73`; `modules/compliance/frameworks.go:1373-1377`; `modules/compliance/frameworks.go:1075-1081` |
| RBAC and tenant isolation | Implemented | `docs/SECURITY-HARDENING.md:108-117`; `README.md:85-89` |
| Customer IdP/MFA policy enforcement | Partial | `LICENSING.md`; `cmd/olivares/wire_noenterprise.go:46-70`; `modules/compliance/frameworks.go:816-822` |

## IPY — Interoperability & Portability

Scope note: Olivares supports evidence and artifact portability; moving customer AI workloads or models between providers is outside the product boundary.

| Control area | Posture | Evidence |
|---|---|---|
| AI evidence export formats | Implemented | `docs/trust/machine-readable-evidence.md:29-45`; `docs/trust/eu-ai-act-annex-iv.md:45-55` |
| AIBOM/SPDX AI evidence portability | Partial | `docs/trust/machine-readable-evidence.md:39-42`; `docs/trust/questionnaire-answer-bank.md:75-76`; `modules/compliance/frameworks.go:1380-1385` |
| AI workload/provider portability | N-A (customer responsibility) | Customer deployment architecture; `modules/compliance/frameworks.go:1380-1385` |

## IVS — Infrastructure & Virtualization Security

Scope note: Olivares secures its own deployment posture and tenant boundary; customer hosts, clusters, hypervisors, and network controls are customer responsibilities.

| Control area | Posture | Evidence |
|---|---|---|
| Secure deployment posture | Implemented | `docs/SECURITY-HARDENING.md:196-198`; `docs/trust/reference-architecture.md:124-137` |
| Multi-tenant isolation and RLS backstop | Implemented | `docs/SECURITY-HARDENING.md:108-117`; `docs/trust/reference-architecture.md:31-35` |
| Host and virtualization hardening | N-A (customer responsibility) | Customer-operated infrastructure; `modules/compliance/frameworks.go:1388-1393` |

## LOG — Logging & Monitoring

Scope note: Olivares logs and monitors governed AI activity; customers operate downstream SIEM, SOC, retention, and alert handling.

| Control area | Posture | Evidence |
|---|---|---|
| AI action audit ledger | Implemented | `docs/SECURITY-HARDENING.md:74-83`; `docs/trust/questionnaire-answer-bank.md:63-65`; `modules/compliance/frameworks.go:1395-1400` |
| AI access and drift monitoring | Implemented | `README.md:69-75`; `docs/trust/machine-readable-evidence.md:43-45` |
| Monitoring operations and alert response | Partial | `docs/trust/reference-architecture.md:139-151`; `docs/trust/questionnaire-answer-bank.md:65-67` |

## SEF — Security Incident Management & Forensics

Scope note: Product evidence supports incident analysis; customers decide incident classification, notification, and regulatory reporting.

| Control area | Posture | Evidence |
|---|---|---|
| AI forensics and timeline reconstruction | Implemented | `modules/compliance/frameworks.go:1411-1416`; `docs/trust/agent-safety-case.md:41-43`; `docs/SECURITY-HARDENING.md:78-83` |
| Security disclosure and remediation | Partial | `SECURITY.md:5-28`; `SECURITY.md:58-78`; `SECURITY.md:81-94` |
| Independent red-team / penetration engagement | Planned | `docs/trust/agent-safety-case.md:47-55`; `docs/trust/penetration-testing.md:8-21` |

## STA — Supply Chain Management & Transparency

Scope note: Olivares covers product, connector, model-admission, and evidence-supply-chain transparency; customers enforce local admission policy and provider contracts.

| Control area | Posture | Evidence |
|---|---|---|
| Product release supply chain | Partial | `docs/RELEASE-VERIFICATION.md:18-31`; `docs/RELEASE-VERIFICATION.md:35-53`; `docs/trust/vendor-viability.md:40-48` |
| AI model/data supply-chain evidence | Partial | `modules/compliance/frameworks.go:1418-1423`; `docs/trust/questionnaire-answer-bank.md:75-76`; `docs/trust/eu-ai-act-annex-iv.md:35-41` |
| Open-core and connector transparency | Implemented | `README.md:171-186`; `LICENSING.md`; `docs/trust/vendor-viability.md:14-31` |

## TVM — Threat & Vulnerability Management

Scope note: Olivares handles its own vulnerabilities and AI threat detections; customers handle vulnerability management for their underlying estate.

| Control area | Posture | Evidence |
|---|---|---|
| Agentic threat catalog mapping | Implemented | `modules/compliance/frameworks.go:1017-1045`; `modules/compliance/frameworks.go:1061-1132`; `docs/SECURITY-HARDENING.md:31-134` |
| Product vulnerability SLA and VEX process | Partial | `SECURITY.md:58-78`; `docs/SECURITY-HARDENING.md:202-215`; `docs/trust/penetration-testing.md:51-74` |
| Third-party penetration testing | Planned | `docs/trust/penetration-testing.md:8-21`; `docs/trust/penetration-testing.md:76-82` |

## UEM — Universal Endpoint Management

Scope note: Olivares does not enroll, patch, or posture-check customer endpoints; it can govern agent activity observed from those endpoints.

| Control area | Posture | Evidence |
|---|---|---|
| Endpoint fleet management | N-A (customer responsibility) | Customer responsibility — self-hosted software. `modules/compliance/frameworks.go:1433-1438` |
| Operator device posture | N-A (customer responsibility) | Customer responsibility — self-hosted software. Product relies on customer IdP/device controls; `README.md:88-89` |

## DCS — Datacenter Security

Scope note: Physical datacenter and environmental controls are outside the product because Olivares is self-hosted software, not a hosted cloud service.

| Control area | Posture | Evidence |
|---|---|---|
| Datacenter physical security | N-A (customer responsibility) | Customer responsibility — self-hosted software. `modules/compliance/frameworks.go:1343-1348`; `docs/trust/README.md:42-55` |
| Facility operations and hardware disposal | N-A (customer responsibility) | Customer responsibility — self-hosted software. `docs/trust/reference-architecture.md:15-19` |

## MDS — Model Security

Scope note: Olivares governs models and model artifacts where the customer has artifacts or provider evidence to admit; closed-weight brokered models cannot have weights signed by Olivares and are tracked through supplier posture instead.

| Control area | Posture | Evidence |
|---|---|---|
| Model inventory and versioning | Implemented | `docs/trust/questionnaire-answer-bank.md:75-76`; `docs/trust/eu-ai-act-annex-iv.md:35-41`; `docs/trust/machine-readable-evidence.md:39-42` |
| Signed model admission with Sigstore/cosign | Implemented | `modules/compliance/frameworks.go:1403-1408`; `modules/compliance/frameworks.go:1220-1222`; `docs/RELEASE-VERIFICATION.md:14-30`; `docs/trust/soc2-readiness.md:67-72`; `docs/trust/questionnaire-answer-bank.md:75-76` |
| AIBOM / SPDX 3.0.1 AI Profile | Partial | `docs/trust/machine-readable-evidence.md:39-40`; `docs/trust/eu-ai-act-annex-iv.md:35-37`; `modules/compliance/frameworks.go:313-319` |
| Model cards and lifecycle records | Partial | `docs/trust/machine-readable-evidence.md:39-42`; `docs/trust/eu-ai-act-annex-iv.md:35-41`; `docs/trust/soc2-readiness.md:67-72` |
| Guardrails for prompt injection, jailbreak, PII, exfiltration | Partial | `modules/compliance/frameworks.go:1462-1480`; `modules/compliance/frameworks.go:1572-1597`; `docs/trust/questionnaire-answer-bank.md:67-78` |
| Human oversight, HITL, and kill switch | Implemented | `docs/trust/agent-safety-case.md:36-43`; `docs/trust/questionnaire-answer-bank.md:77-78`; `README.md:86-91` |
| Agentic safety case | Partial | `docs/trust/agent-safety-case.md:8-23`; `docs/trust/agent-safety-case.md:45-59` |
