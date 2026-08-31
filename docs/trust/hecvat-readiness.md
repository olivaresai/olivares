<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# HECVAT — pre-filled response guide (higher-education vendor assessment)

> **Status.** This maps the **HECVAT** (Higher Education Community Vendor Assessment Toolkit,
> stewarded by **EDUCAUSE** with the REN-ISAC/Internet2 community — current major line **HECVAT 4**,
> *Full* and *Lite*) to the evidence Olivares AI already produces. It is an honest readiness guide,
> not a certification and not a completed submission. **Confirm the exact minor release and question
> identifiers against the workbook you download from EDUCAUSE** and transcribe these answers into its
> cells; the domains below are stable across HECVAT 4, the individual row numbers are not.

## The framing that changes most answers: Olivares is **self-hosted**

HECVAT assesses a vendor that *hosts* institutional data. Olivares AI runs in the **institution's own
estate** — production operation, data storage and network boundaries are the institution's, under its
controls. So a large fraction of HECVAT questions about the *vendor's* hosting, datacenter, backups
and staff access are answered **"not applicable — self-hosted; the institution operates the runtime
and can verify each control in its own deployment."** That is buyer-favorable: you are not asked to
trust an attestation for the runtime; most rows below end in an API call or a file you can inspect.

## Domain mapping — HECVAT area → product control → verifiable evidence

| HECVAT domain | Answer | Verifiable evidence (file / API) |
|---|---|---|
| **Documentation / third-party assessments** | No SOC 2 / ISO 27001 report held today; readiness mappings published, and the product serves a live, machine-readable control catalog. | `docs/trust/soc2-readiness.md`, `iso-27001-readiness.md`, `iso-42001-readiness.md`; `GET /v1/m/compliance/frameworks` |
| **Company / product** | Self-hosted control plane; single binary, source-available (AGPL core). | `README.md`; `ARCHITECTURE.md` |
| **Data — types & sensitivity** | Minimal-data by construction: the control plane persists relations and metadata, not payloads. Governed CONTENT (knowledge bases, prompt templates, agent memory) is persisted, and is minimized on the write path against the product's deterministic sensitivity catalog — credentials and key material, email, IBAN, Luhn-valid cards, US SSN, ES NIF/NIE, IPv4, colon-form MAC and Bitcoin-like wallet addresses are removed before chunking, embedding or storage. Values the catalog does not recognize (names, postal addresses, free-text phone numbers, non-US national ids) are NOT removed. The same catalog labels what it finds. | `RedactSensitive` + `ClassifySensitivity` (`modules/security/sensitivity.go`), wired in `cmd/olivares/knowledgeclassifier.go`; `modules/knowledge/ingest.go` |
| **FERPA / education records** | FERPA overlay in the compliance catalog (education-records access, directory-info scoping, consent-gated disclosure, §99.32 disclosure recordkeeping, minimization, transmission). | `GET /v1/m/compliance/frameworks/ferpa`; `modules/compliance/frameworks.go` |
| **Controlled-access research data** | NIH NOT-OD-25-081 policy pack: classify at a restricted clearance + DLP egress deny of the controlled-access class (deny-closed). | `docs/edu-research/nih-nsf-policy-pack.md`; `modules/knowledge/dlp.go`, `vector.go` |
| **Authentication / AAA** | SSO via OIDC/SAML 2.0; eduGAIN/InCommon aggregate consumption (signature-verified) with eduPerson mapping; RBAC + fail-closed multi-tenant isolation. | `core/auth/federation/`; `connectors/edugain/`; RBAC (engine) |
| **Access control / least privilege** | Permitted-vs-observed access drift; dual-control approvals (quorum, anti-self-approval); scoped admin. | `GET /v1/m/accessmap/drift`; `modules/governance/approvals.go` |
| **Encryption** | TLS ≥1.2 (PQC-hybrid key establishment by default) on every listener; mutual TLS on the connector channel; at-rest encryption attestable. | `docs/SECURITY-HARDENING.md`; `docs/SEC-G3-CRYPTO-AGILITY-PQC.md` |
| **Change management** | Governed change/deploy ledger, HITL approval gates, conventional commits + DCO + CI gates; online-safe migrations. | `scripts/check-migrations.sh`; deploy records (module VII) |
| **Vulnerability / systems management** | `govulncheck` blocking CI; weekly rebuild/rescan + VEX refresh; signed releases + SBOM + SLSA Build L3 provenance. | `SECURITY.md`; `scripts/verify-release.sh` |
| **Incident handling** | Immutable, hash-chained audit ledger; reconstructable forensic timeline; continuous WORM/SIEM export; published SEV model. | `docs/STATUS-AND-INCIDENT-COMMS.md`; SIEM export (CEF/LEEF/syslog/OTLP/OCSF) |
| **Business continuity / DR** | Self-hosted: the institution owns backups/DR of its estate; the plane ships backup/restore of its own stores + integrity re-verification. | backup/restore UI + audit re-verify |
| **Datacenter / physical / network** | **Not applicable — self-hosted.** The institution's datacenter/cloud controls apply; the plane binds localhost by default, push-only collectors, no inbound listener. | `ARCHITECTURE.md` (topology) |
| **Privacy / data subject rights** | RTBF erasure workflow (hold-gate, dual-control, crypto-shredding) with ledger-anchored receipts; retention/legal-hold. | `GET /v1/m/compliance/…erasure`; `modules/compliance/` |
| **Accessibility (procurement often bundles)** | VPAT 2.5Rev / EN 301 549 / WCAG 2.1 AA (+2.2 tested) self-assessment with an automated AT-verification gate. | `docs/accessibility/VPAT-olivares-admin.md` |

## Honest gaps (the same organizational distance as SOC 2)

These are **not product gaps** — they are management-system items for Olivares.AI as a company, most
requiring a founder decision. They mirror `docs/trust/soc2-readiness.md` §Organizational gaps: a dated
policy corpus, a business-level risk register, third-party certifications (SOC 2 / ISO 27001) engaged
when a customer requires them, and formal HR/personnel controls (single founder today). HECVAT rows
asking for a current SOC 2 or ISO certificate are answered honestly: **not held yet; readiness
mapping published; engaged when a concrete institution requires it.**

## No false claims

Olivares AI is **not** listed on Internet2 NET+, holds **no** "NET+ approved" status, and displays no
community-program logos it has not earned. The self-hosted, source-available product is complete and
free (Community edition); this guide exists so an institution's security review starts from evidence,
not archaeology.
