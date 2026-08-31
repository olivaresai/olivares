<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Security questionnaire answer bank (SIG-2026-aligned)

> Reusable, pre-verified vendor answers for third-party-risk questionnaires —
> Shared Assessments **SIG 2026**, CSA **AI-CAIQ**, and ad-hoc buyer
> questionnaires. Every answer is the honest state **as of the date it carries** — that is
> **2026-06-12** unless a row states a later one — with a pointer to the verifiable
> artifact. Answers that will change at first release are flagged `[pre-release]`.
>
> Read that literally, because it is the honest version of what this line used to say
> ("the honest current state on 2026-06-12"): the bank is not re-verified end to end
> every time one row changes, so an undated answer is a **dated** answer, not a live one.
> The external-assurance answers were reconciled on **2026-08-17** with the founder
> decision of **2026-07-18** (no external pen-test or SOC 2 examination for now;
> certifications demand-gated) — see [README.md](./README.md#current-attestation-status-no-surprises).
>
> **SIG licensing note (verified 2026-06-12):** the SIG questionnaire is paid,
> licensed content of Shared Assessments (corporate license ~US$7,000/yr); a
> completed SIG may be delivered to specific authorized recipients but the
> questionnaire itself may not be republished. This bank therefore contains **our
> answers in our own words**, organized so they transcribe directly into the SIG
> 2026 workbook (21 risk domains in 4 control areas, including the *Artificial
> Intelligence* domain; the 2026 release added an ISO/IEC 42001 mapping).
> Completing an actual SIG workbook requires the subscription — a founder decision,
> demand-gated.

## A. Company & product profile

| Question theme | Answer |
|---|---|
| Legal entity | Olivares.AI — sole-proprietor (autónomo) operating under a registered trade name, Spain (EU). `[pre-release: incorporate-on-demand is a deferred decision]` |
| Product | Self-hosted control plane for governing AI agents: inventory, R/RW access map (permitted-vs-observed drift), governance/HITL, audit ledger, FinOps, evals, guardrails, compliance evidence. |
| Delivery model | **Self-hosted software** (single Go binary / container / Helm / air-gap bundle). No SaaS today; a managed offering is design-stage only. We never operate customer production. |
| Data we receive from customers | **None in production.** The product runs in the customer estate; telemetry stays there. Support interactions may include customer-redacted diagnostics only. |
| Sub-processors of customer data | **None** (no hosted service). LLM providers are the *customer's* vendors; the product inventories them for the customer's TPRM (`GET /v1/m/models/data-governance`). |
| Personnel | Single maintainer (disclosed, with mitigations): see [vendor-viability.md](./vendor-viability.md). |

## B. Information protection

| Theme | Answer |
|---|---|
| Encryption in transit | TLS 1.3; mTLS collector↔core; hybrid post-quantum key establishment (X25519MLKEM768) is the **default**. `docs/SEC-G3-CRYPTO-AGILITY-PQC.md` |
| Encryption at rest | Operator-provided (LUKS/filesystem/Postgres TDE) and attested per tenant in the compliance module; BYOK/CMEK envelope encryption with AWS/GCP/Azure KMS for key custody; honest catalog note: a gap until attested in a given deployment. |
| FIPS | Optional FIPS 140-3 build linking the CMVP-validated Go module (cert **#5247**, Level 1, active, sunset 2031-04-26). The validation covers the crypto module, not the product. `docs/SCP-09-FIPS-STIG.md` |
| Data minimization | By construction for the CONTROL PLANE: it stores **relations and hashes, not payloads**; fields declared `Redact` are SHA-256-hashed at write. Governed CONTENT is stored (that is the point of a knowledge base) and is **minimized, not omitted**: the product's deterministic sensitivity catalog removes credentials and key material, email, IBAN, Luhn-valid cards, US SSN, ES NIF/NIE, IPv4, colon-form MAC and Bitcoin-like wallet addresses before the text is chunked, embedded or persisted. It does **not** remove what it cannot recognize — a name, a postal address, a free-text phone number or a national id outside the US/ES shapes will persist. |
| Data classification / DLP | Deterministic PII/sensitivity scanning with persistent labels; deny-closed DLP egress gate; append-only enforcement events. |
| Data residency | Region-pinned tenants with deny-closed cross-region guard (403 `residency_violation`); residency attestation + egress scan. `docs/MULTI-REGION-RESIDENCY.md` |
| Privacy / data subject rights | RTBF runbook with dual-control, legal-hold veto, sealed erasure receipts, honest limits documented (`docs/RIGHT-TO-ERASURE.md`); retention + legal hold + WORM archival (`docs/RECORDS-MANAGEMENT.md`). |

## C. IT operations & business resilience

| Theme | Answer |
|---|---|
| Availability SLO | 99.5%/28d single-node (the honest current tier); 99.9%/28d target for the HA tier. `docs/17-PRODUCTION-READINESS-SLO.md` |
| HA | Active-passive leader election (Postgres advisory lock), automatic failover via `/readyz` drain, shared audit-signing key custody, online expand-contract migrations. |
| Backup/DR | Encrypted `.drbundle` backups (operator KEK), verify-on-restore (fails closed on chain/signature mismatch); RPO 15min–6h (cron tiers) down to ≈seconds (Postgres PITR); RTO <15–30min; drill cadence documented. `docs/DR-RUNBOOK.md` |
| Capacity | Published measured baseline (≈1.5k durable writes/s single-writer; Postgres 5.2k/7.8k ev/s at 4/16 writers; SQLite→Postgres crossover at 1–2 concurrent writers). `docs/SIZING-AND-CAPACITY.md` |
| Change management | Conventional commits + DCO; CI gates (lint/SPDX/boundary/build/test, `govulncheck`, secret scanning); change/deploy ledger in-product; signed releases. |
| Vulnerability management | Published CVE remediation targets (Critical 7d / High 14d / Medium 30d / Low next release; KEV→Critical); reachability-first via `govulncheck`; OpenVEX for non-reachable; weekly rebuild+rescan. `SECURITY.md` |
| Penetration testing | **None performed, and no external test is contracted** `[pre-release]`. That is a recorded founder decision of **2026-07-18**, not an omission: it defers the third-party engagement and directs **internal, automated adversarial campaigns** before the public release instead. Committed program + cadence for when an external test is engaged: [penetration-testing.md](./penetration-testing.md). |
| Commercial entitlement lifecycle & revocation | Commercial add-ons are licensed per **paid term** (Ed25519-signed, **validated locally** — no mandatory outbound call to validate an installed licence; issuance, renewal and CRL distribution do reach the vendor at `licenses.olivares.ai`), with **one signed grace extension of 168 hours**, granted only for involuntary renewal failure and at most once per 365 days; there is no trial profile and no perpetual fallback. A **license CRL is carried in the OTA-signed channel manifest** (serials, holders, key-compromise epoch) — the OTA key domain is independent from the license key, so a compromised license key cannot un-revoke itself. **Honest limits, stated to buyers:** the CRL reaches only deployments that pull updates or import offline bundles — there are no mandatory outbound calls that would push it; expiry and grace are evaluated against the LOCAL clock, and the anti-rollback treatment is being redesigned (do not read the current behaviour as a commitment). **What revocation or expiry costs you, precisely, today:** nothing in the open (AGPL) binary — it is never gated, and that is a standing commitment, not a current-state note (license checks cannot become an authorization bypass; ADR-0010). It costs **no user accounts either, in any edition**: self-hosted users are unlimited and no license state can cap, disable or delete an account (licensing decision of 2026-07-27 — the community seat cap and the enterprise seat lift were both removed). What a lapse ends is the per-add-on entitlement to the additive commercial modules; **the per-module enforcement of that entitlement is not implemented yet** (planned entitlement-enforcement work, gated before first sale), so today an expired license in a commercial build reports its status honestly rather than switching modules off. Precedent-aligned: Teleport (grace, core keeps running) and HashiCorp (never against a running node); we reject GitLab-style day-one lockout. `LICENSING.md` |

## D. Security incident & threat management

| Theme | Answer |
|---|---|
| Logging & audit | Append-only, hash-chained, Ed25519 per-event-signed ledger; offline verification; 7-year default audit retention; S3 Object-Lock COMPLIANCE-mode archival with chained manifests. |
| SIEM integration | Pull export (CEF/LEEF/syslog/OTLP/OCSF) + push forwarder to SIEM/ITSM sinks + posture export for control towers. |
| Incident response | Severity model SEV1/2/3 with comms cadence (SEV1 first update ≤15 min); postmortem triggers tied to error budget. `docs/STATUS-AND-INCIDENT-COMMS.md` |
| Vulnerability disclosure | `SECURITY.md`: private reporting, safe harbor, acknowledgement ≤3 business days, triage ≤10, coordinated disclosure ≤90; GHSA→OSV advisories; machine-discoverable via RFC 9116 `/.well-known/security.txt`. |
| Threat detection | Guardrail/anomaly findings over agent surfaces (OWASP Agentic ASI01–ASI10 catalog); estate kill switch with two-person re-enable. |

## E. Artificial intelligence (the SIG 2026 AI domain / AI-CAIQ Model Security)

| Theme | Answer |
|---|---|
| Does the vendor use AI in the product? | Yes, narrowly: LLM-as-judge in the evals module (customer-configured providers). The control plane's own logic is deterministic; no customer data is sent to any vendor-operated AI. |
| AI governance framework | The product *is* an AI-governance layer; the catalog maps 26 frameworks (EU AI Act, NIST AI RMF + GenAI profile, ISO 42001, SOC 2, ISO 27001, GDPR, OWASP Agentic, CISA/Five-Eyes guidance, …) as data with per-framework pins and disclaimers. |
| Model inventory & versioning | Model registry + versions; deny-closed signed model admission (Sigstore); deprecation/lifecycle tracking. |
| Model/data lineage | Sealed CycloneDX 1.6 AIBOM per model; SPDX 3.0.1 AI-Profile export; model cards (JSON/Markdown) with explicit `not_recorded` for unknown fields. |
| Human oversight | Deny-by-default HITL approvals, quorum/SoD dual-control, break-glass that is itself audited and floor-gated, AAL3 step-up for privileged actions. |
| AI incident response | Graduated estate/agent kill switch (one-click stop, structural two-person re-enable); guardian loops with anti-spiral dedup + HITL. |
| ISO 42001 | Not certified; readiness mapping with two disclosed honest gaps: [iso-42001-readiness.md](./iso-42001-readiness.md). |
| Governing Claude in US-government deployments | The product is deployment-surface-aware for the governed provider (verified 2026-07-03 against Anthropic primary docs): FedRAMP-High paths exist via Claude for Government, Amazon Bedrock GovCloud, and Vertex AI Assured Workloads; ITAR only via Bedrock (IL5; Secret-region IL6). Honest limits we surface rather than hide: Claude Code and Cowork are NOT yet in Claude for Government ("on the roadmap this year" per Anthropic), and the Anthropic Compliance API does not cover Bedrock/Vertex-governed deployments — our per-surface catalog (`connectors/claude-api/surfaces.go`) and coverage findings state this instead of implying parity. |

> **Founder decision recorded (2026-07-18), not pending:** the SIG subscription is
> **demand-gated** — bought only if/when a buyer demands the official workbook, and this
> bank answers the substance either way. The **spend itself** (~US$7,000/yr) stays the
> founder's call at that moment; what is settled is that nobody re-opens the question
> beforehand.
