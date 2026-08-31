<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# SOC 2 Type II — readiness mapping (NOT an attestation)

> **Status: Olivares.AI does not hold a SOC 2 report of any type, and no CPA
> examination has been engaged.** This document maps the AICPA 2017 Trust Services
> Criteria (with Revised Points of Focus — 2022), Security/Common Criteria, to the
> evidence the product and the engineering process already produce, so that a buyer
> can see exactly how far the gap to a Type II examination is — and so the
> examination, when engaged, starts from evidence instead of archaeology.
> The authoritative, machine-readable version of this mapping ships **inside the
> product**: `GET /v1/m/compliance/frameworks/soc2_tsc` (catalog, criteria,
> per-control disclaimer) and `…/soc2_tsc/status` (live evaluation against real
> tenant evidence). The product itself refuses to overclaim: design-only evidence
> reports `by_design`, never `satisfied`.

## Scope framing for a self-hosted product

SOC 2 examines the **service organization**. Olivares AI is self-hosted: production
operation of the control plane happens in the *customer's* estate, under the
customer's controls. The realistic SOC 2 scope for Olivares.AI as an organization is
therefore: the software supply chain (build, sign, release), the engineering change
process, vulnerability handling and support operations — not the runtime your team
operates. Two consequences, both buyer-favorable:

- The product's runtime controls (the left column below) are **yours to operate and
  audit directly** — you do not need to trust our attestation for them, you can
  verify them in your own deployment (most rows below end in an API call you can run).
- Our attestation surface is small and largely already evidenced in public,
  verifiable artifacts (signed releases, SBOM, SLSA provenance, public CVE SLA).

## Control mapping — Common Criteria → product capability → evidence

The 13 criteria below are the ones the platform itself evidences (this is the same
mapping the product serves as data; transcription verified against
`modules/compliance/frameworks.go` on 2026-06-12 and **re-verified 2026-08-17** — the
catalog still carries these same 13 criterion IDs, in this order). Criteria not listed (CC1.x
Control Environment, CC2.x Communication, CC3.x Risk Assessment, CC9.x Risk
Mitigation, and the A/PI/C/P series) are **organizational-process criteria** — see
"Organizational gaps" below.

| TSC | Criterion (short) | Product capability | Verifiable evidence |
|---|---|---|---|
| CC4.1 | Ongoing/separate evaluations of controls | Continuous control telemetry: observed access, guardrail/anomaly findings, eval runs, red-team results, all ledger-anchored | `GET /v1/m/compliance/frameworks/soc2_tsc/status`; access map `GET /v1/m/accessmap/drift`; findings API; audit ledger verify |
| CC5.2 | General controls over technology | RBAC, governed change/deployment process, secure-by-default config (no default creds, TLS on, localhost bind, setup token) | RBAC model (engine); deploy/change ledger (module VII); `docs/SECURITY-HARDENING.md` (point-by-point STRIDE verification) |
| CC6.1 | Logical access security | RBAC + multi-tenant isolation (Postgres RLS `FORCE`), access edges recorded, transit encryption | `docs/SECURITY-HARDENING.md` (RLS, app-layer guard); mTLS collector↔core; access-edge records |
| CC6.2 | Registration, authorization, deprovisioning | Governed identities incl. non-human/agent identities (lifecycle: provision→rotate→finalize), approval gates | NHI lifecycle; identity sources LDAP/IdP/SCIM; approvals with deny-by-default |
| CC6.3 | Least privilege & segregation of duties | **Permitted-vs-observed drift** (the product's differentiator), role-based authz, dual-control approvals (quorum, anti-self-approval) | `GET /v1/m/accessmap/drift`; `modules/governance/approvals.go` (quorum/SoD); break-glass is itself audited |
| CC6.6 | Protection against external threats | Guardrail/anomaly detection, mTLS/TLS boundaries, push-only collectors with no inbound listener | Threat findings; `ARCHITECTURE.md` topology; TLS 1.3 with hybrid PQC key establishment by default (`docs/SEC-G3-CRYPTO-AGILITY-PQC.md`) |
| CC6.7 | Restrict/protect information in movement | Minimal-data by construction for the control plane (relations, not payloads); governed content minimized on the write path against the product's deterministic sensitivity catalog (credentials and key material, email, IBAN, Luhn-valid cards, US SSN, ES NIF/NIE, IPv4, colon-form MAC, Bitcoin-like wallets — values the catalog does not recognize, such as names or postal addresses, are not removed); deny-closed DLP gate; residency attestation + egress scan | DLP enforcement events; `GET /v1/m/compliance/residency`; `RedactSensitive` on the ingest write path; fields declared `Redact` stored as SHA-256 |
| CC6.8 | Prevent/detect unauthorized software | Signed releases, SBOM, pinned deps, governed change process; signed **model** admission for AI artifacts | `scripts/verify-release.sh` (cosign, SBOM attestation, SLSA Build L3 (SLSA v1.2) provenance, OpenVEX); model admission deny-closed |
| CC7.1 | Detect config changes & new vulnerabilities | Change ledger + anomaly detection; reachability-gated vulnerability process | `govulncheck` blocking CI; weekly rebuild/rescan + VEX refresh (`SECURITY.md` §Remediation SLA) |
| CC7.2 | Monitor for anomalies | Continuous monitoring of components and agent behavior into a tamper-evident ledger | Findings + live hash-chain verification; Prometheus metrics + SLO burn-rate alerts (`docs/17-PRODUCTION-READINESS-SLO.md`) |
| CC7.3 | Evaluate security events | Reconstructable, integrity-verified incident timeline | Forensic timeline from the ledger; per-event Ed25519 signatures verify offline (`--pubkey`) |
| CC7.4 | Incident response execution | IR supported by immutable ledger + continuous WORM/SIEM export; severity model + comms cadence published | SIEM export (CEF/LEEF/syslog/OTLP/OCSF) + push forwarder; `docs/STATUS-AND-INCIDENT-COMMS.md` (SEV1 ≤15 min first update) |
| CC8.1 | Authorized, tested, documented changes | Change/deploy ledger, HITL approval gates, signed supply chain; expand-contract online migrations | Deploy records; approvals; `scripts/check-migrations.sh` (online-safety lint); conventional commits + DCO + CI gates |

## SOC 2 + AI evidence annex (what an AI-aware auditor will ask in 2026)

Buyers increasingly probe SOC 2 scope for AI-specific operations. These are the
product's answers, mapped to the criteria where an auditor will look:

| AI evidence ask | What exists | Maps to |
|---|---|---|
| **Prompt/interaction logs** | OTel GenAI ingest (`gen_ai.*` semconv) feeds session timelines and the audit ledger; privileged-session recording with payload-hash anchoring. Payloads are minimized by design — relations and hashes, not bodies. | CC7.2 (monitoring), CC4.1 |
| **Model versioning** | Model registry with versions + **signed model admission** (OpenSSF Model Signing / Sigstore, deny-closed) — an unsigned or tampered model does not enter service. | CC8.1 (change mgmt), CC6.8 |
| **Lineage** | Sealed, ledger-anchored CycloneDX 1.6 AIBOM per model (datasets + artifact lineage); SPDX 3.0.1 AI-Profile serialization of the same inventory. | CC8.1; ISO 42001 A.7.5 |
| **LLM sub-processor inventory** | Provider/model catalog with per-provider **data-governance posture** (`GET /v1/m/models/data-governance`) and operator-verified GPAI posture (`GET /v1/m/models/gpai-posture`) — note: *self-hosted Olivares has no sub-processors of customer data; the LLM providers are the **customer's** vendors, and the product inventories them for the customer's own TPRM.* | CC6.7, CC9-adjacent |
| **AI change control** | Routing policies, admission policy, model lifecycle (deprecation/retirement tracking) under RBAC + audit. | CC8.1 |

## Organizational gaps (the honest distance to Type II)

These are **not product gaps**; they are the management-system work a Type II
examination requires of Olivares.AI as a company, and most require founder
decisions:

1. **Policy corpus** — formal, dated, approved information-security policies
   (much of the content exists in `SECURITY.md`/`docs/SECURITY-HARDENING.md`/`GOVERNANCE.md`
   but is not yet organized as a policy set with review cadence).
2. **Formal risk assessment** with treatment decisions, refreshed on a cadence
   (the STRIDE threat model in `docs/SECURITY-HARDENING.md` is the
   technical core of it; the business-level register does not exist yet).
3. **Type II observation window** — operating-effectiveness evidence over a 3–12
   month period, which can only start once the policies and the period are declared.
4. **People controls** — background-check, onboarding/offboarding, security
   training records (single-founder today; trivial but must be documented).
5. **Independent monitoring cadence** — internal-audit-like review distinct from
   the doer (a structural challenge for a solo founder; commonly solved with an
   external vCISO/auditor-readiness firm — a paid decision).

> **Founder decision recorded (2026-07-18), not pending:** **no SOC 2 examination for
> now**; internal, automated adversarial campaigns run before the public release
> instead, and the examination is **demand-gated** — engaged when a concrete customer
> requires it. Self-hosting reduces the pressure, because the buyer can audit the
> runtime directly. Nothing about that is an open question, and it commits to no date.
>
> **What that decision did NOT settle, and remains the founder's:** engaging a CPA firm
> (typical SaaS market cost US$20–60k/yr recurring, internal planning estimate),
> Type I-first vs straight Type II, and the observation window. Read this block as
> three reserved items inside a recorded deferral — not as a fourth open question about
> whether to pursue SOC 2 at all.
