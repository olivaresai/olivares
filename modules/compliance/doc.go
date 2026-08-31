// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package compliance is module XIII — compliance and regulatory: it OPENS
// enterprise doors by MAPPING what the control plane already observes and audits
// onto regulatory frameworks, and by producing auditor-consumable EVIDENCE derived
// from the append-only/hash-chained ledger. It is an intelligence/control layer: it
// captures NOTHING new — it AGGREGATES and TRANSFORMS what the core and the other
// modules already record.
//
// # What it is
//
//   - A versioned, in-repo CONTROL CATALOG (frameworks.go): EU AI Act, NIST AI RMF
//     (+ GenAI Profile), ISO/IEC 42001, SOC 2 / ISO 27001, GDPR, the revised EU PLD
//     (defense-evidence framing), and the agentic-security crosswalks — OWASP Top 10
//     for Agentic Applications 2026, OWASP Agentic Threats & Mitigations, Five Eyes
//     'Careful adoption of agentic AI services', CISA/NSA 'AI Data Security', CSA
//     MAESTRO, NIST COSAiS — modeled as sets of versioned CONTROLS, each with its
//     requirement and its satisfaction criterion, every framework carrying a
//     structured VERSION PIN (document + primary source + verified_on). The
//     catalog is the deterministic source of truth (the frameworks change slowly
//     and the trace must be reproducible); it is a TECHNICAL mapping, never legal
//     advice, and the module never claims certification (docs/SECURITY-HARDENING.md).
//
//   - A REGULATORY CALENDAR as data (calendar.go): every regulatory date the
//     product relies on — the EU AI Act staggered application incl. the Digital
//     Omnibus deferrals (adopted, pending OJ publication), PLD transposition, the
//     AILD withdrawal, DORA, Colorado ADMT, the FIPS 140-2 sunset, CNSA 2.0 — as
//     milestones with primary source + verified_on, plus a watchlist of
//     in-development instruments. Controls link milestones via MilestoneIDs; no
//     application date lives as prose in the catalog (tested).
//
//   - A DORA EXPORT MODE (dora.go): the tenant's AI risk register, incident
//     timeline and third-party AI-provider register rendered ICT-risk-compatible
//     for a financial entity's DORA program, anchored to the ledger and citing the
//     verified provisions (Art 6(1)/(5), 8(1), 10(1), 17(1)-(2), 28(3)).
//
//   - A declarative CONTROL -> EVIDENCE map (capabilities.go, assess.go): every
//     control maps to control-plane CAPABILITIES that honestly evidence it. A
//     capability is either OPERATIONAL — present only when REAL tenant data exists
//     (an audit chain that verifies, observed access edges, security findings, eval
//     results, deployments, risk classifications, a residency attestation) — or
//     ARCHITECTURAL — a platform-design guarantee cited to the design docs (the
//     ledger's append-only/hash-chain, RBAC + multi-tenant isolation, mTLS, minimal
//     data, secure defaults, signed supply chain). A control is SATISFIED only when
//     every mapped capability is present, PARTIAL when some are, and a GAP when none
//     are — a control with no backing evidence is a GAP, never a faked pass.
//
//   - EXPORTABLE AUDIT EVIDENCE (evidence.go): a sealed, append-only evidence
//     PACKAGE derived from the ledger — it records the chain head (seq+hash) and the
//     live hash-chain VERIFY result, so the package proves the evidence it references
//     was not altered (docs/SECURITY-HARDENING.md). The auditor consumes a structured report + a
//     JSON/CSV dataset + a manifest; the continuous CEF/syslog/OTLP feed to SIEM/WORM
//     stays the core's — this module references it, never reimplements it.
//
//   - AGENT RISK CLASSIFICATION (risk.go): each agent is classified into an EU AI Act
//     risk tier (unacceptable/high/limited/minimal) cross-mapped to the NIST AI RMF
//     functions, from its OBSERVED attributes — which resources it touches (the R/RW
//     edges of module III), its security findings (module IX/XVIII). The suggested
//     tier is GOVERNED: it is only a suggestion until a human reviews/approves it
//     (the unacceptable tier is a legal determination a heuristic must never assert),
//     and every classification and review is audited.
//
//   - DATA RESIDENCY (residency.go): the self-hosted structural advantage made
//     legible — a per-region attestation that data stays inside the customer's
//     perimeter (the GDPR/EU/air-gap argument), plus a scan that turns EXISTING
//     egress signals (data lineage, findings) into a residency-violation Finding. It
//     consumes inventory/topology; it captures nothing new.
//
//   - REPORTING (report.go): per-framework control status, GAP ANALYSIS, an
//     org/tenant summary across frameworks — all exportable as JSON/CSV. The
//     executive PDF/dashboards are XXI; this module produces the DATA, not the
//     visual.
//
//   - RECORDS MANAGEMENT (dataclass.go, retention.go, holds.go —): a
//     fixed in-code DATA-CLASS registry, per-class retention schedules (documented
//     basis + window + disposition; INERT until a tenant authors one — the
//     recommendations are advisory), LEGAL HOLDS with an append-only,
//     ledger-anchored chain of custody, the hold-gate (CheckHold + GET
//     /holds/check) that and the knowledge module's deletes consult, and the
//     hold-checked sweep that disposes in bounded batches under the approved
//     schedules. This is the ONE deliberate exception to "aggregates and
//     transforms only": the module OWNS a destruction path. It does not break the
//     evidence invariants, because destruction here is ITSELF evidence-producing
//     and maximally constrained: (a) enabling a purge disposition passes the
//     approval gate (deny-closed, no break-glass) and the run executes only that
//     approved schedule; (b) an active legal hold VETOES every purge — tenant and
//     class holds skip the whole class, mapped subject holds exclude rows, and an
//     unmapped related subject hold skips the whole class (over-preservation is
//     the safe direction; releasing a hold is CRITICAL, dual-control, re-verified
//     in-module); (c) every pass with activity seals an APPEND-ONLY retention_run
//     certificate anchored to the ledger head plus a self-audit — the destruction
//     log can never be silently rewritten; and (d) the append-only evidence
//     tables and the audit ledger are NOT purgeable in v1 (the ledger's
//     multi-year story is WORM archival, not deletion). The §7 Covered-Models
//     floor is ANNOTATED (provider_floor_days / effective_disclosure_days), never
//     fabricated: un-wired ⇒ provider_floor_known=false.
//
// # The honesty invariant (docs/SECURITY-HARDENING.md, non-negotiable)
//
// The module DESIGNS-FOR-AUDIT; it does not CERTIFY. No path marks a control
// satisfied without linked evidence; an absent operational capability is an honest
// gap; opt-in guarantees (at-rest encryption) default to absent until attested. The
// output speaks of control STATUS + EVIDENCE, never "compliant" or "certified".
//
// # Minimal data & self-audit (docs/SECURITY-HARDENING.md, §4)
//
// Evidence is metadata/controls — counts, statuses, hashes, ledger sequence
// references — never payloads or PII. Consulting or exporting an evidence package,
// sealing one, classifying or reviewing risk, and attesting residency are PRIVILEGED
// actions that self-audit to the ledger in the caller's own transaction.
//
// # Ports, fail-closed
//
// The seams XIII declares are its own (ports.go): an AutonomySource for an agent's
// scheduling/autonomy signal (module IV), a LineageSource for perimeter-egress
// signals (module VIII), the ApprovalGate for the two dangerous verbs
// (enabling a purge disposition; releasing a legal hold — both DENY-closed until
// the composition root wires the bridge, and neither admits break-glass) and
// the ProviderRetention floor source (un-wired ⇒ provider_floor_known=false). An
// un-wired evidence seam yields LESS evidence (a gap), never a fabricated pass; an
// un-wired destruction seam destroys NOTHING. XIII never imports a sibling module;
// the composition root injects the real adapters.
package compliance
