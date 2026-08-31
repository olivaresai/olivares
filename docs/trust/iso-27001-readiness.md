<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ISO/IEC 27001:2022 — readiness mapping (NOT a certification)

> **Status: Olivares.AI is not ISO/IEC 27001 certified and no certification audit
> has been engaged.** This document maps ISO/IEC 27001:2022 Annex A controls (aligned
> to the ISO/IEC 27002:2022 control set) to evidence the product and process already
> produce. Annex A is applied via a Statement of Applicability (SoA) against a risk
> assessment — the mapping below is **evidence input to a future SoA**, not
> conformity. The machine-readable version lives in the product:
> `GET /v1/m/compliance/frameworks/iso_27001_2022` and `…/status` / `…/gaps`.

## Scope framing

Same self-hosted asymmetry as SOC 2 (see [soc2-readiness.md](./soc2-readiness.md)):
the customer operates the runtime, so the Annex A controls in the table are
**controls the customer's deployment gets from the product and can verify
directly**. Olivares.AI's own ISMS scope, when certified, would cover engineering,
release and support operations.

## Annex A control mapping (the 16 controls the platform evidences)

Transcription verified against `modules/compliance/frameworks.go` (2026-06-12;
**re-verified 2026-08-17** — the catalog still carries these same 16 Annex A IDs, in this
order). Controls not listed are organizational/physical (clauses 4–10 of the management
system, A.6 people, A.7 physical) — see "ISMS gaps" below.

| Annex A | Control | Product capability | Verifiable evidence |
|---|---|---|---|
| A.5.12 | Classification of information | Deterministic PII/sensitivity discovery: explainable classes (named rule + count, never the matched value), per-document recommendation | PII scan results, `knowledge.pii_scan` evidence |
| A.5.13 | Labelling of information | Persistent sensitivity labels (classes, max severity, detector version, content hash), incl. explicit *clean* labels | Stored labels per document |
| A.5.15 | Access control | Engine-enforced RBAC + multi-tenant isolation; observed R/RW access edges | RLS `FORCE` + app-layer tenant guard (`docs/SECURITY-HARDENING.md`); access map |
| A.5.16 | Identity management | Full lifecycle of non-human/agent identities; live identity sources (LDAP/IdP/SCIM) | NHI lifecycle; identity-source connectors; agent-identity federation |
| A.5.18 | Access rights | Permitted-vs-observed comparison surfaces over-provisioned rights for review/removal | `GET /v1/m/accessmap/drift` |
| A.5.23 | Cloud services security | Lineage evidences which crossings the operator configured, and the residency gate refuses the rest; residency attestation + egress scan | `GET /v1/m/compliance/residency`; residency scan findings |
| A.5.24 | Incident management planning | Forensic, integrity-verified reconstruction + escalation/approval gates, prepared before incidents | Ledger timeline; approvals; `docs/STATUS-AND-INCIDENT-COMMS.md` |
| A.5.26 | Response to incidents | Live threat detection + forensic timeline for response | Findings; ledger verify |
| A.5.28 | Collection of evidence | Append-only hash-chained ledger, Ed25519 per-event signatures, continuous WORM/SIEM export | Offline verification (`--pubkey`); S3 Object Lock COMPLIANCE-mode archival with chained manifests |
| A.5.31 | Legal/regulatory requirements | Per-agent regulatory risk classification (EU AI Act / NIST); regulatory calendar as data (source + verified-on per date) | `GET /v1/m/compliance/risk`; `GET /v1/m/compliance/calendar` |
| A.8.12 | Data leakage prevention | Deny-closed DLP egress gate keyed on sensitivity classes; unscanned content withheld; append-only enforcement events | `knowledge.dlp_event` |
| A.8.15 | Logging | Append-only, hash-chained, signed audit ledger; export to external WORM/SIEM | Ledger + export (CEF/LEEF/syslog/OTLP/OCSF) + push forwarder |
| A.8.16 | Monitoring activities | Continuous observation of agent behavior/access; anomaly findings | Access observability + findings |
| A.8.24 | Use of cryptography | mTLS/TLS by default (TLS 1.3, hybrid PQC key establishment X25519MLKEM768 default); at-rest encryption attested per tenant; BYOK/CMEK envelope (AWS/GCP/Azure KMS); optional FIPS 140-3 build (CMVP cert #5247) | `docs/SEC-G3-CRYPTO-AGILITY-PQC.md`; `docs/SCP-09-FIPS-STIG.md`; honest catalog note: at-rest is opt-in/operator-provided and is a gap until attested |
| A.8.25 | Secure development life cycle | Evals + adversarial testing + signed/SBOM-backed pinned supply chain; blocking `govulncheck`/secret scanning | CI gates; `scripts/verify-release.sh`; `SECURITY.md` (CVE SLA, OpenVEX, weekly rebuild) |
| A.8.32 | Change management | Change/deploy ledger + human approval gates; online expand-contract migrations | Deploy records; `scripts/check-migrations.sh` |

## ISMS gaps (clauses 4–10 + organizational Annex A)

ISO 27001 certifies a **management system**, not a feature list. What does not
exist yet, and is founder work, not engineering work:

1. **ISMS scope statement, information-security policy, SoA** — the formal trio
   the certification body audits first.
2. **Risk assessment & treatment methodology** with records of runs (the threat
   model is the seed: `docs/SECURITY-HARDENING.md`).
3. **Internal audit + management review** on a cadence, with records — structurally
   awkward solo; typically outsourced (paid decision).
4. **People & physical controls** (A.6, A.7) — minimal for a solo remote founder
   but must be stated, not implied.
5. **Supplier security (A.5.19–A.5.22)** as an organizational process — the
   *product's* supplier evidence (GPAI posture, SBOM of dependencies) helps, but the
   vendor-management procedure itself must be written.

> **Founder decision recorded (2026-07-18), not pending:** certification is **not being
> pursued now** and is **demand-gated**, the same position that decision took for SOC 2 —
> they share most of the ISMS work, and the standard guidance is to run both off one
> control set. The compliance module's catalog is exactly that shared control set,
> expressed as data.
>
> **Still the founder's, and untouched by that decision:** the certification body,
> readiness support, the ISMS scope statement and the timing — plus the same cost
> envelope as SOC 2. A buyer reading this should not conclude that *whether* to certify
> is undecided; it is deferred, on the record, with no date attached.
