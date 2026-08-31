// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and tables. All tables are "compliance_"-prefixed and
// within the 40-char cap (the longest, compliance_evidence_package and
// compliance_retention_policy, are 27). An EVIDENCE_PACKAGE and a CONTROL_RESULT are
// APPEND-ONLY (immutable sealed auditor evidence — a re-run is a new package, never an
// edit); a RISK_CLASS and a RESIDENCY attestation are MUTABLE (re-classifiable /
// re-attestable, the change self-audited). Adds the records-management plane: a
// RETENTION_POLICY (mutable schedule document) and a LEGAL_HOLD (mutable lifecycle
// active → released), plus their immutable evidence — the HOLD_EVENT custody trail and
// the RETENTION_RUN disposition certificates, both APPEND-ONLY (docs/SECURITY-HARDENING.md).
const (
	packageKind         model.Kind = "compliance.evidence_package"
	packageTable                   = "compliance_evidence_package"
	resultKind          model.Kind = "compliance.control_result"
	resultTable                    = "compliance_control_result"
	riskKind            model.Kind = "compliance.risk_class"
	riskTable                      = "compliance_risk_class"
	residencyKind       model.Kind = "compliance.residency"
	residencyTable                 = "compliance_residency"
	retentionPolicyKind model.Kind = "compliance.retention_policy"
	retentionPolicyTbl             = "compliance_retention_policy" // 27 chars
	legalHoldKind       model.Kind = "compliance.legal_hold"
	legalHoldTable                 = "compliance_legal_hold" // 21 chars
	holdEventKind       model.Kind = "compliance.hold_event"
	holdEventTable                 = "compliance_hold_event" // 21 chars
	retentionRunKind    model.Kind = "compliance.retention_run"
	retentionRunTable              = "compliance_retention_run" // 24 chars
	// the RTBF/right-to-erasure plane. A SUBJECT_KEY is the crypto-shredding
	// key-ring row (mutable, HARD-deleted on shred — the one deliberate mapping of a
	// data subject's plaintext identifiers, destroyed together with its DEK). An
	// ERASURE_REQUEST is the mutable DSR lifecycle; its custody trail (ERASURE_EVENT)
	// and final certificate (ERASURE_RECEIPT) are APPEND-ONLY evidence that, by
	// construction, reference the subject ONLY through a shreddable token.
	subjectKeyKind     model.Kind = "compliance.subject_key"
	subjectKeyTable               = "compliance_subject_key" // 22 chars
	erasureRequestKind model.Kind = "compliance.erasure_request"
	erasureRequestTbl             = "compliance_erasure_request" // 26 chars
	erasureEventKind   model.Kind = "compliance.erasure_event"
	erasureEventTable             = "compliance_erasure_event" // 24 chars
	erasureReceiptKind model.Kind = "compliance.erasure_receipt"
	erasureReceiptTbl             = "compliance_erasure_receipt" // 26 chars
	// a registered OSCAL profile/SSP selection. MUTABLE (re-registering replaces
	// the active selection per framework); register/unregister self-audit semantically.
	// The row holds the resolved control-id selection + back-references + the ingested
	// document's SHA-256 (the document itself is REFERENCED by hash, never copied; docs/SECURITY-HARDENING.md
	// §3,§5). One active selection per (tenant, framework) scopes the OSCAL
	// assessment-results export.
	oscalProfileKind model.Kind = "compliance.oscal_profile"
	oscalProfileTbl             = "compliance_oscal_profile" // 24 chars
	// the DORA named-regulation DEPTH plane (enterprise/doraregister seam). A
	// DORA_REGISTER is the maintained Register of Information (Commission Implementing
	// Regulation (EU) 2024/2956), MUTABLE (re-generate replaces the active register per
	// maintaining entity); a DORA_INCIDENT is a major-incident classification + report draft
	// (RTS (EU) 2024/1772 / 2025/301), MUTABLE (the report evolves initial → intermediate →
	// final). Both are populated ONLY by the enterprise packager (the open generate/classify
	// endpoints answer 501 without it) and self-audit semantically. These tables live in the
	// OPEN module so the community and enterprise builds register IDENTICAL schema (the
	// schema-parity gate); the enterprise add-on adds NO tables of its own.
	doraRegisterKind model.Kind = "compliance.dora_register"
	doraRegisterTbl             = "compliance_dora_register" // 24 chars
	doraIncidentKind model.Kind = "compliance.dora_incident"
	doraIncidentTbl             = "compliance_dora_incident" // 24 chars
	// the ISO/IEC 42001 AIMS certification-readiness pack (enterprise/iso42001 seam).
	// A MUTABLE artifact (re-generate replaces the active pack per tenant). Populated ONLY
	// by the enterprise packager (the open generate endpoint answers 501 without it) and
	// self-audits semantically. This table lives in the OPEN module so the community and
	// enterprise builds register IDENTICAL schema (the schema-parity gate); the enterprise
	// add-on adds NO tables of its own.
	aimsPackKind model.Kind = "compliance.aims_pack"
	aimsPackTbl             = "compliance_aims_pack" // 20 chars
	// the compliance-depth plane (enterprise/compliancedepth seam). These tables
	// persist the US state law packs, sector overlay packs, CCM snapshots, CCM drift
	// findings, and FedRAMP KSI documents. All populated ONLY by the enterprise depth
	// packager (the open endpoints answer 501 without it) and self-audit semantically.
	// These tables live in the OPEN module so the community and enterprise builds
	// register IDENTICAL schema (the schema-parity gate); the enterprise add-on adds
	// NO tables of its own.
	usStateLawPackKind model.Kind = "compliance.us_law_pack"
	usStateLawPackTbl             = "compliance_us_law_pack" // 22 chars
	sectorPackKind     model.Kind = "compliance.sector_pack"
	sectorPackTbl                 = "compliance_sector_pack" // 22 chars
	ccmSnapshotKind    model.Kind = "compliance.ccm_snapshot"
	ccmSnapshotTbl                = "compliance_ccm_snapshot" // 23 chars
	ccmDriftKind       model.Kind = "compliance.ccm_drift"
	ccmDriftTbl                   = "compliance_ccm_drift" // 20 chars
	fedRAMPKSIKind     model.Kind = "compliance.fedramp_ksi"
	fedRAMPKSITbl                 = "compliance_fedramp_ksi" // 22 chars
	// the NIS 2 Directive significant-incident classification + tiered report
	// drafting (enterprise/nis2incident seam). A MUTABLE artifact (the report evolves
	// through phases: early_warning → notification → intermediate → final). Populated
	// ONLY by the enterprise packager (the open classify endpoint answers 501 without it)
	// and self-audits semantically. This table lives in the OPEN module so the community
	// and enterprise builds register IDENTICAL schema (the schema-parity gate); the
	// enterprise add-on adds NO tables of its own.
	nis2IncidentKind model.Kind = "compliance.nis2_incident"
	nis2IncidentTbl             = "compliance_nis2_incident" // 25 chars
)

// compliance_evidence_package columns — a sealed, immutable evidence package derived
// from the ledger (append-only). It records the chain head and the live verify
// result so the package PROVES the evidence it references was not altered (docs/SECURITY-HARDENING.md).
const (
	colFramework    = "framework"
	colFrameworkVer = "framework_version"
	colGeneratedAt  = "generated_at"
	colGeneratedBy  = "generated_by"
	colLedgerSeq    = "ledger_seq"  // chain head seq at seal time
	colLedgerHash   = "ledger_hash" // chain head hash (hex) at seal time
	colIntegrityOK  = "integrity_ok"
	colIntegrityN   = "integrity_checked"
	colIntegrityWhy = "integrity_reason" // nullable: the first break, if any
	colCtrlTotal    = "controls_total"
	colSatisfied    = "satisfied"
	colPartial      = "partial"
	colGap          = "gap"
	colUnmapped     = "unmapped"
	colManifestHash = "manifest_hash" // hash of the canonical assessment (tamper-evidence of the package body)
	colScopeNote    = "scope_note"    // nullable
)

// compliance_control_result columns — one control's status inside a package
// (append-only). capabilities holds the minimal-data CapabilityEvidence list (JSON).
const (
	colPackageRef = "package_ref"
	// colFramework reused
	colControlID  = "control_id"
	colTitle      = "title"
	colStatus     = "status"
	colEvSummary  = "evidence_summary"
	colCaps       = "capabilities" // JSON: []CapabilityEvidence
	colOccurredAt = "occurred_at"
)

// compliance_risk_class columns — an agent/use-case risk classification (mutable,
// audited). suggested_tier is the heuristic output; tier is the effective (possibly
// reviewer-overridden) tier; state governs it.
const (
	colSubjectKind  = "subject_kind" // "agent" | "use_case"
	colSubjectRef   = "subject_ref"
	colAgentID      = "agent_id" // nullable; set when the subject resolves to a core Agent
	colTier         = "tier"
	colSuggested    = "suggested_tier"
	colRiskState    = "state" // suggested | approved | overridden
	colRationale    = "rationale"
	colNistFns      = "nist_functions" // JSON: []string
	colSignals      = "signals"        // JSON: the observed signals that drove the tier
	colReviewedBy   = "reviewed_by"    // nullable
	colClassifiedAt = "classified_at"
)

// compliance_residency columns — a per-region residency attestation (mutable,
// audited). self_hosted + encryption_at_rest are operator attestations; the scan
// bumps violations_observed when an existing egress signal contradicts the perimeter.
const (
	colRegion        = "region"
	colPerimeter     = "perimeter" // e.g. "self-hosted", "eu-west", "air-gapped"
	colSelfHosted    = "self_hosted"
	colEncAtRest     = "encryption_at_rest"
	colDataClasses   = "data_classes" // JSON: []string, nullable
	colAttestedBy    = "attested_by"
	colAttestedAt    = "attested_at"
	colViolations    = "violations_observed"
	colLastChecked   = "last_checked" // nullable
	colResidencyNote = "note"         // nullable
)

// compliance_retention_policy columns — one per-class retention schedule (mutable,
// self-audited). data_class is a registry id (dataclass.go); basis is the bounded
// legal/business basis prose; approval_ref is the approval that ENABLED a purge
// disposition ("" for retain — enabling destruction is the gated act, §6).
const (
	colDataClass     = "data_class" // shared spelling: policy, hold and run rows
	colRPDays        = "retention_days"
	colRPDisposition = "disposition" // retain | purge
	colRPEnabled     = "enabled"
	colRPBasis       = "basis"
	colApprovalRef   = "approval_ref" // shared spelling: policy, hold-event and run rows
)

// compliance_legal_hold columns — one preservation order (mutable LIFECYCLE only:
// active → released; every transition leaves an append-only hold_event). matter_ref
// is the case/matter id; subject_kind/subject_ref reuse the risk-class spellings.
const (
	colLHMatterRef  = "matter_ref"
	colLHScopeKind  = "scope_kind" // tenant | data_class | subject
	colLHReason     = "reason"
	colLHCreatedBy  = "created_by"
	colLHReleasedBy = "released_by"
	colLHReleasedAt = "released_at"
	colLHReleaseRef = "release_approval_ref"
)

// compliance_hold_event columns — the append-only chain-of-custody trail: each event carries the real actor (+ optional delegation), the approval
// evidence (ref + distinct approver principals, JSON) and the ledger-head anchor
// (seq + hash) at the moment of the event, the evidence-package anchoring pattern.
const (
	colHEHoldID    = "hold_id"
	colHEEvent     = "event" // set | release_requested | released
	colHEActor     = "actor"
	colHEActorKind = "actor_kind"
	colHEOnBehalf  = "on_behalf_of" // optional delegation chain
	colHENote      = "note"
	colHEApprovers = "approvers" // JSON: []string distinct approving principals
)

// compliance_retention_run columns — one append-only disposition CERTIFICATE per
// class with activity (the Sedona "repeatable, documented destruction" evidence):
// the applied age cutoff, the counts, the hold outcomes, the policy/approval refs,
// the ledger-head anchor and the canonical run-summary hash.
const (
	colRRTrigger   = "trigger" // sweep | manual
	colRRCutoff    = "cutoff"
	colRRExamined  = "examined"
	colRRPurged    = "purged"
	colRRExcluded  = "excluded_held"      // rows excluded by a mapped subject-hold
	colRRSkipped   = "skipped_class_hold" // whole class skipped by a tenant/class hold
	colRRTruncated = "truncated"          // batch cap reached; the next run continues
	colRRPolicyID  = "policy_id"
)

// compliance_subject_key columns — the crypto-shredding key-ring
// (mutable, NOT audited by descriptor: every lifecycle step self-audits semantically
// with ids only). New rows store only a deterministic lookup digest in subject_ref
// and the subject identifiers encrypted in subject_payload under the per-subject
// AES-256-GCM DEK. Crypto-shredding HARD-deletes the row, which destroys the
// encrypted data-plane payload and its DEK in one atomic act — every token in
// append-only/WORM media becomes permanently unintelligible without touching a
// hashed byte (docs/SECURITY-HARDENING.md). Legacy rows with plaintext subject_ref/aliases remain
// readable for in-place upgrades and are destroyed by the same hard-delete.
const (
	colSKSubjectKind = "subject_kind"
	colSKSubjectRef  = "subject_ref" // lookup digest for new rows; legacy plaintext for old rows
	colSKAliases     = "aliases"     // legacy JSON []string; new rows keep aliases in subject_payload
	colSKPayload     = "subject_payload"
	colSKDEK         = "dek" // 32-byte AES-256-GCM data-encryption key
	colSKCreatedBy   = "created_by"
)

// compliance_erasure_request columns — one DSR (data-subject request) lifecycle
// (mutable status only; everything identifying rides the token). subject_token is the
// pii-sealed subject reference; key_id points at the subject_key row whose deletion
// anonymizes this request forever. data_classes is the affected §2 class set; case_ref
// is the operator's DSR case/ticket id (an identity reference: rejected over-length,
// never clamped).
const (
	colERSubjectKind   = "subject_kind"
	colERToken         = "subject_token"
	colERSubjectLookup = "subject_lookup"
	colERKeyID         = "key_id"
	colERClasses       = "data_classes" // JSON []string of §2 registry ids
	colCaseRef         = "case_ref"     // shared spelling: request and receipt rows
	colERReason        = "reason"
	colERRequestedBy   = "requested_by"
	colERStatus        = "status"
	colERPlanHash      = "plan_hash" // bound at first execute; anti-TOCTOU across polls
	// provider_user_ids persists the provider-side subject ids ACROSS executes: a
	// re-execute (after provider_pending or a failure) must not silently skip the
	// provider leg because the operator omitted the ids the first call carried.
	colERProviderIDs = "provider_user_ids"
)

// compliance_erasure_event columns — the append-only RTBF chain of custody
// (hold_event-aligned): every workflow transition with the real actor, the approval
// evidence and the ledger-head anchor. NO column can carry a subject identifier —
// notes are bounded prose with counts and target labels only.
const (
	colEEErasureID = "erasure_id"
	colEEEvent     = "event" // received | hold_blocked | approval_requested | executed | account | provider | key_shredded | sealed | failed
	colEEActor     = "actor"
	colEEActorKind = "actor_kind"
	colEENote      = "note"
	colEEApprovers = "approvers" // JSON []string distinct approving principals
)

// compliance_erasure_receipt columns — the append-only erasure CERTIFICATE (the
// "recibo" of docs/RECORDS-MANAGEMENT.md §6.2): per-target outcomes (counts only),
// the provider-floor disclosure, the key-shred fact, the post-erasure live chain
// verification, the ledger anchor and the canonical manifest hash. The subject is
// referenced ONLY by the (now shredded) token — the receipt outlives the erasure
// without re-identifying the person.
const (
	colRCErasureID  = "erasure_id"
	colRCSubject    = "subject_kind"
	colRCToken      = "subject_token"
	colRCTargets    = "targets" // JSON []targetOutcome: label, examined, erased, scrubbed, excluded_held, status, detail
	colRCAccount    = "account_outcome"
	colRCProvider   = "provider_outcome"
	colRCFloorDays  = "provider_floor_days"
	colRCFloorKnown = "provider_floor_known"
	colRCFloorSrc   = "provider_floor_source"
	colRCShredded   = "key_shredded"
	colRCVerifyOK   = "verify_ok"
	colRCVerifyN    = "verify_checked"
	colRCVerifyWhy  = "verify_reason"
	colRCRetained   = "retained" // JSON []retainedRecord: what stays + the documented legal basis
)

// compliance_oscal_profile columns — a registered OSCAL profile/catalog/SSP
// selection. framework is the resolved KNOWN framework id (colFramework, reused); the
// selection rides selected_ids (JSON []string), the document's identity rides its
// SHA-256 (reference, not copy) and the OSCAL back-references (profile/ssp uuid,
// import-profile href, source href) anchor the assessment-results export.
const (
	colOPDocKind      = "doc_kind"            // profile | catalog | system-security-plan
	colOPProfileUUID  = "profile_uuid"        // nullable
	colOPSSPUUID      = "ssp_uuid"            // nullable
	colOPImportHref   = "import_profile_href" // nullable: ssp.import-profile.href
	colOPSourceHref   = "source_href"         // nullable: profile import/catalog source
	colOPSelected     = "selected_ids"        // JSON []string: the resolved selection
	colOPDropped      = "dropped_ids"         // JSON []string, nullable: selected-but-unmappable ids
	colOPOscalVer     = "oscal_version"       // nullable: metadata.oscal-version of the ingested doc
	colOPDocSHA       = "doc_sha256"          // SHA-256 of the ingested document (reference, not copy)
	colOPNote         = "oscal_note"          // nullable: honest coverage caveat
	colOPRegisteredBy = "registered_by"
	colOPRegisteredAt = "registered_at"
)

// compliance_dora_register columns — the maintained DORA Register of Information,
// structured to Commission Implementing Regulation (EU) 2024/2956. entity_lei is the
// maintaining entity (B_01.01.0010) and the register's identity key. register holds the
// structured B_xx.xx templates (the operator's OWN register artifact — the deliverable they
// maintain, deliberately stored so the plane is the re-exportable system of record, NOT
// third-party telemetry); validation/reconciliation are the packager's honest findings;
// doc_sha256 anchors the operator's submitted bytes; the ledger seq+hash give tamper-evidence.
const (
	colDREntityLEI   = "entity_lei"
	colDREntityName  = "entity_name"    // nullable
	colDRRefDate     = "reference_date" // nullable: B_01.01.0060
	colDRRegulation  = "regulation"
	colDRRegister    = "register"       // JSON: the structured B_xx.xx templates
	colDRValidation  = "validation"     // JSON []RegisterIssue, nullable
	colDRReconcile   = "reconciliation" // JSON []RegisterIssue, nullable
	colDRCounts      = "counts"         // JSON map[string]int, nullable
	colDRNote        = "register_note"  // nullable: honest caveat
	colDRDocSHA      = "doc_sha256"
	colDRGeneratedBy = "generated_by" // shared spelling reused by the incident plane
	colDRGeneratedAt = "generated_at" // reuses the spelling of the evidence package
)

// compliance_dora_incident columns — a provisional major-incident classification +
// report draft. reference is the operator's incident reference (identity key). classification
// holds the report draft + deadlines + basis (JSON); the indexed scalars (major,
// critical_services, criteria_met) drive filtering. doc_sha256 anchors the operator's impact
// input; the ledger seq+hash give tamper-evidence.
const (
	colDIReference    = "reference"
	colDIFindingID    = "finding_id" // nullable: optional link to a governed finding
	colDIMajor        = "major"
	colDICritical     = "critical_services" // the Art 6 gating precondition
	colDICriteria     = "criteria_met"      // JSON []string, nullable: Art 9 thresholds met
	colDIClassif      = "classification"    // JSON: report draft + deadlines + basis
	colDIRationale    = "rationale"         // nullable
	colDINote         = "incident_note"     // nullable
	colDIDocSHA       = "doc_sha256"
	colDIClassifiedBy = "classified_by"
	colDIClassifiedAt = "classified_at"
)

// compliance_nis2_incident columns — a provisional NIS 2 Art 23 significant-incident
// classification + tiered report drafts (early warning / notification / final). reference is
// the operator's incident reference (identity key). classification holds the report drafts +
// deadlines + basis (JSON); the indexed scalars (significant, cross_border, suspected_crime)
// drive filtering. phase tracks the current reporting phase (forward-only:
// early_warning → notification → intermediate → final). doc_sha256 anchors the operator's
// impact input; the ledger seq+hash give tamper-evidence.
const (
	colNIReference    = "reference"
	colNIFindingID    = "finding_id"      // nullable: optional link to a governed finding
	colNISignificant  = "significant"     // Art 23(3) verdict
	colNICrossBorder  = "cross_border"    // cross-border impact indicator
	colNICrime        = "suspected_crime" // suspected unlawful/malicious action (Art 23(4)(a))
	colNICriteria     = "criteria_met"    // JSON []string, nullable: Art 23(3) criteria
	colNIClassif      = "classification"  // JSON: report drafts + deadlines + basis
	colNIRationale    = "rationale"       // nullable
	colNINote         = "incident_note"   // nullable
	colNIPhase        = "phase"           // current reporting phase
	colNIDocSHA       = "doc_sha256"
	colNIClassifiedBy = "classified_by"
	colNIClassifiedAt = "classified_at"
)

// compliance_aims_pack columns — the maintained ISO/IEC 42001 AIMS certification-
// readiness pack. organization_name is the entity this pack is prepared for (identity key).
// The sections (soa, policy, risk_register, impact_assessments, lifecycle_controls,
// supplier_governance) are the structured deliverables the enterprise add-on produces from
// the live assessment + operator-supplied context; validation carries the packager's honest
// findings; doc_sha256 anchors the operator's submitted bytes; the ledger seq+hash give
// tamper-evidence. One active pack per tenant (replace-on-regenerate).
const (
	colAPStandard    = "standard"
	colAPOrgName     = "organisation_name"
	colAPSoA         = "soa"                 // JSON: Statement of Applicability (Annex A)
	colAPPolicy      = "policy"              // JSON: AI policy (clauses 4–10)
	colAPRiskReg     = "risk_register"       // JSON: AI risk register (clause 6.1 + Annex A.5.2)
	colAPImpact      = "impact_assessments"  // JSON: impact assessments (A.5.2/A.5.4)
	colAPLifecycle   = "lifecycle_controls"  // JSON: lifecycle control mapping (A.6.2.x)
	colAPSupplier    = "supplier_governance" // JSON: supplier/AI-component governance (A.10.3/A.7.5)
	colAPValidation  = "aims_validation"     // JSON []AIMSIssue, nullable
	colAPScopeNote   = "scope_note"          // nullable
	colAPDocSHA      = "doc_sha256"
	colAPGeneratedBy = "generated_by"
	colAPGeneratedAt = "generated_at"
)

// compliance_us_law_pack / compliance_sector_pack columns — shared by both
// depth pack types. pack_type distinguishes them ("us_state_law" | "sector_overlay").
// sections holds the structured deliverable (jurisdiction results or sector results);
// validation carries the packager's honest findings; doc_sha256 anchors the operator's
// submitted bytes; the ledger seq+hash give tamper-evidence.
const (
	colDPPackType   = "pack_type"
	colDPRegulation = "regulation"
	colDPSections   = "sections"         // JSON: the structured deliverable
	colDPValidation = "depth_validation" // JSON []DepthIssue, nullable
	colDPNote       = "depth_note"       // nullable
	colDPDocSHA     = "doc_sha256"
	colDPScopeNote  = "scope_note" // nullable — reuses the spelling from evidence packages
)

// compliance_ccm_snapshot columns — a point-in-time controls monitoring snapshot.
// frameworks holds the per-framework assessment results at snapshot time (JSON); summary
// is the cross-framework aggregate (JSON).
const (
	colCSSnapshotAt = "snapshot_at"
	colCSFrameworks = "frameworks"    // JSON
	colCSSummary    = "summary"       // JSON
	colCSNote       = "snapshot_note" // nullable
)

// compliance_ccm_drift columns — a detected status change between two CCM
// snapshots. One row per control that changed. snapshot_ref links to the current
// (newer) snapshot.
const (
	colCDSnapshotRef = "snapshot_ref"
	colCDFrameworkID = "framework_id"
	colCDControlID   = "control_id"
	colCDTitle       = "title" // reuses the existing spelling
	colCDPrevStatus  = "prev_status"
	colCDCurrStatus  = "curr_status"
	colCDDirection   = "direction"
	colCDDetail      = "detail" // nullable
	colCDDetectedAt  = "detected_at"
)

// compliance_fedramp_ksi columns — a FedRAMP 20x Key Security Indicators
// document. system_name identifies the system under authorization; impact_level is
// IL2/IL4/IL5; ksis holds the machine-readable KSIs (OSCAL v1.1.3); auth_package
// holds the authorization package structure.
const (
	colFKSystemName  = "system_name"
	colFKImpactLevel = "impact_level"
	colFKKSIs        = "ksis" // JSON
	colFKOscalVer    = "oscal_version"
	colFKAuthPkg     = "auth_package"       // JSON
	colFKValidation  = "fedramp_validation" // JSON []DepthIssue, nullable
	colFKNote        = "fedramp_note"       // nullable
	colFKDocSHA      = "doc_sha256"
	colFKScopeNote   = "scope_note"
)

// RegisterSchema declares the module's sixteen owned entities (the SchemaProvider
// seam, S02 §7). The engine creates the tables, injects the base columns and attaches
// the tenant/audit/append-only guards; a module cannot opt out of isolation.
//
// Minimal data (docs/SECURITY-HARDENING.md): every column is a control/evidence metadatum — a status,
// a count, a hash, a ledger sequence reference, a non-sensitive label — never a
// payload or PII. The candidate evidence (the ledger) is REFERENCED (seq+hash), never
// copied. ONE deliberate, documented exception: compliance_subject_key
// holds the per-subject DEK and, for new rows, an AEAD-sealed subject payload so that
// ONE hard delete destroys both the data-plane ciphertext opener and the key — it is
// the erasure mechanism, not a data sink, and nothing else may copy its plaintext.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:       packageKind,
		Table:      packageTable,
		AppendOnly: true, // a sealed evidence package is immutable; a re-run is a new package
		Fields: []model.FieldSpec{
			{Name: colFramework, Kind: model.KindText, Indexed: true},
			{Name: colFrameworkVer, Kind: model.KindText},
			{Name: colGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colGeneratedBy, Kind: model.KindText},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
			{Name: colIntegrityOK, Kind: model.KindBool},
			{Name: colIntegrityN, Kind: model.KindInt},
			{Name: colIntegrityWhy, Kind: model.KindText, Nullable: true},
			{Name: colCtrlTotal, Kind: model.KindInt},
			{Name: colSatisfied, Kind: model.KindInt},
			{Name: colPartial, Kind: model.KindInt},
			{Name: colGap, Kind: model.KindInt},
			{Name: colUnmapped, Kind: model.KindInt},
			{Name: colManifestHash, Kind: model.KindText},
			{Name: colScopeNote, Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:       resultKind,
		Table:      resultTable,
		AppendOnly: true, // immutable per-control evidence within a sealed package
		Fields: []model.FieldSpec{
			{Name: colPackageRef, Kind: model.KindUUID, Indexed: true},
			{Name: colFramework, Kind: model.KindText, Indexed: true},
			{Name: colControlID, Kind: model.KindText, Indexed: true},
			{Name: colTitle, Kind: model.KindText},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colEvSummary, Kind: model.KindText, Nullable: true},
			{Name: colCaps, Kind: model.KindJSON, Nullable: true},
			{Name: colOccurredAt, Kind: model.KindTimestamp},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:    riskKind,
		Table:   riskTable,
		Audited: true, // a classification and every review is auditable evidence
		Fields: []model.FieldSpec{
			{Name: colSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colAgentID, Kind: model.KindUUID, Nullable: true},
			{Name: colTier, Kind: model.KindText, Indexed: true},
			{Name: colSuggested, Kind: model.KindText},
			{Name: colRiskState, Kind: model.KindText, Indexed: true},
			{Name: colRationale, Kind: model.KindText, Nullable: true},
			{Name: colNistFns, Kind: model.KindJSON, Nullable: true},
			{Name: colSignals, Kind: model.KindJSON, Nullable: true},
			{Name: colReviewedBy, Kind: model.KindText, Nullable: true},
			{Name: colClassifiedAt, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			// One active classification per (tenant, subject). Unique index leads with tenant_id.
			Name:    "compliance_risk_uniq",
			Columns: []string{model.ColTenantID, colSubjectKind, colSubjectRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:    residencyKind,
		Table:   residencyTable,
		Audited: true, // a residency attestation and a scan are auditable evidence
		Fields: []model.FieldSpec{
			{Name: colRegion, Kind: model.KindText, Indexed: true},
			{Name: colPerimeter, Kind: model.KindText},
			{Name: colSelfHosted, Kind: model.KindBool},
			{Name: colEncAtRest, Kind: model.KindBool},
			{Name: colDataClasses, Kind: model.KindJSON, Nullable: true},
			{Name: colAttestedBy, Kind: model.KindText},
			{Name: colAttestedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colViolations, Kind: model.KindInt},
			{Name: colLastChecked, Kind: model.KindTimestamp, Nullable: true},
			{Name: colResidencyNote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One attestation per (tenant, region). Unique index leads with tenant_id.
			Name:    "compliance_residency_uniq",
			Columns: []string{model.ColTenantID, colRegion},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the per-class retention schedule. Mutable and deliberately NOT
	// descriptor-Audited: the privileged mutations (put/delete/enable) each append a
	// SEMANTIC self-audit attributed to the real principal in their own transaction
	// (the governance pattern, modules/governance/schema.go). The destruction record
	// is NOT this row — it is the append-only retention_run certificate below.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  retentionPolicyKind,
		Table: retentionPolicyTbl,
		Fields: []model.FieldSpec{
			{Name: colDataClass, Kind: model.KindText, Indexed: true},
			{Name: colRPDays, Kind: model.KindInt},
			{Name: colRPDisposition, Kind: model.KindText},
			{Name: colRPEnabled, Kind: model.KindBool},
			{Name: colRPBasis, Kind: model.KindText, Nullable: true},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One policy per (tenant, class). Unique index leads with tenant_id.
			Name:    "compliance_retention_policy_uniq",
			Columns: []string{model.ColTenantID, colDataClass},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the legal hold — a preservation order whose ONLY mutation is the governed
	// lifecycle transition active → released (CRITICAL, dual-control, no break-glass).
	// Not descriptor-Audited: set/release-request/release each self-audit semantically
	// AND leave an append-only hold_event in the same transaction.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  legalHoldKind,
		Table: legalHoldTable,
		Fields: []model.FieldSpec{
			{Name: colLHMatterRef, Kind: model.KindText, Indexed: true},
			{Name: colTitle, Kind: model.KindText, Nullable: true},
			{Name: colLHScopeKind, Kind: model.KindText, Indexed: true},
			{Name: colDataClass, Kind: model.KindText, Nullable: true},
			{Name: colSubjectKind, Kind: model.KindText, Nullable: true},
			{Name: colSubjectRef, Kind: model.KindText, Nullable: true},
			{Name: colLHReason, Kind: model.KindText},
			{Name: colStatus, Kind: model.KindText, Indexed: true},
			{Name: colLHCreatedBy, Kind: model.KindText},
			{Name: colLHReleasedBy, Kind: model.KindText, Nullable: true},
			{Name: colLHReleasedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colLHReleaseRef, Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}

	// the hold chain-of-custody trail. APPEND-ONLY: who set, who requested the
	// release, who released under WHICH approval with WHICH distinct approvers, each
	// event anchored to the ledger head (seq + hash) at that moment — custody that can
	// never be silently rewritten (docs/SECURITY-HARDENING.md). No column can hold customer content.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       holdEventKind,
		Table:      holdEventTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colHEHoldID, Kind: model.KindUUID, Indexed: true},
			{Name: colHEEvent, Kind: model.KindText, Indexed: true},
			{Name: colHEActor, Kind: model.KindText},
			{Name: colHEActorKind, Kind: model.KindText},
			{Name: colHEOnBehalf, Kind: model.KindText, Nullable: true},
			{Name: colHENote, Kind: model.KindText, Nullable: true},
			// approval_ref is plain text, "" for an ungated event ("set") — never
			// NULL, so the dedupe index below compares with plain equality and has
			// no NULLs-are-distinct hole.
			{Name: colApprovalRef, Kind: model.KindText},
			{Name: colHEApprovers, Kind: model.KindJSON, Nullable: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// Custody dedupe backstop: handleReleaseHold's pending branch is
			// read-then-insert, which cannot exclude a concurrent twin — two polls
			// of the same pending approval can both pass the findOne guard. The
			// engine therefore enforces at most ONE custody row per (tenant, hold,
			// event, approval_ref); the loser's Create surfaces the unique
			// violation as store.ErrConflict, which the handler treats as
			// already-sealed. The key cannot collide with legitimate rows: a hold
			// has exactly one "set" (approval_ref "", written in the same tx that
			// creates the hold), at most one "released" (under the single approval
			// that lifted it; the active-status guard forbids a second release),
			// and one "release_requested" PER approval_ref (a re-request after
			// expiry opens a NEW approval ⇒ a new ref). Unique index leads with
			// tenant_id.
			Name:    "compliance_hold_event_uniq",
			Columns: []string{model.ColTenantID, colHEHoldID, colHEEvent, colApprovalRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the disposition certificate. APPEND-ONLY: every sweep pass with activity
	// (rows examined, or a whole class skipped by a hold) seals an immutable record of
	// WHAT was destroyed (counts only), under WHICH policy/approval, with WHICH cutoff
	// — the defensible-deletion log of destruction (Sedona pillar 3). The certificate
	// is anchored to the ledger head and carries the canonical run-summary hash.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       retentionRunKind,
		Table:      retentionRunTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colDataClass, Kind: model.KindText, Indexed: true},
			{Name: colRRTrigger, Kind: model.KindText},
			{Name: colRRCutoff, Kind: model.KindTimestamp},
			{Name: colRRExamined, Kind: model.KindInt},
			{Name: colRRPurged, Kind: model.KindInt},
			{Name: colRRExcluded, Kind: model.KindInt},
			{Name: colRRSkipped, Kind: model.KindBool},
			{Name: colRRTruncated, Kind: model.KindBool},
			{Name: colRRPolicyID, Kind: model.KindUUID},
			{Name: colApprovalRef, Kind: model.KindText, Nullable: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
			{Name: colManifestHash, Kind: model.KindText},
		},
	}); err != nil {
		return err
	}

	// the crypto-shredding key-ring. MUTABLE with a HARD delete (no
	// SoftDelete — a tombstoned key would defeat the shred; no AppendOnly — deletion
	// IS the feature). The DEK is plain KindBytes, deliberately NOT FieldSpec.Redact
	// (Redact one-way-hashes on write; the key must open tokens/payload until it is
	// destroyed). subject_payload is AEAD ciphertext under that DEK; subject_ref is
	// only the lookup digest for NEW rows, retaining legacy plaintext compatibility.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  subjectKeyKind,
		Table: subjectKeyTable,
		Fields: []model.FieldSpec{
			{Name: colSKSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colSKSubjectRef, Kind: model.KindText, Indexed: true},
			{Name: colSKAliases, Kind: model.KindJSON, Nullable: true},
			{Name: colSKPayload, Kind: model.KindText, Nullable: true},
			{Name: colSKDEK, Kind: model.KindBytes},
			{Name: colSKCreatedBy, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// One live key per (tenant, subject). Unique index leads with tenant_id
			//. A NEW request for the SAME subject after a shred mints a new
			// row — the old tokens stay dead (their key id no longer resolves).
			Name:    "compliance_subject_key_uniq",
			Columns: []string{model.ColTenantID, colSKSubjectKind, colSKSubjectRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the DSR lifecycle row. Mutable STATUS only (there is no update endpoint:
	// subject, classes and case are immutable after creation, so the plan the
	// approvers see cannot drift — anti-TOCTOU at the data layer). Not descriptor-
	// Audited: every transition self-audits semantically with ids only.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  erasureRequestKind,
		Table: erasureRequestTbl,
		Fields: []model.FieldSpec{
			{Name: colERSubjectKind, Kind: model.KindText, Indexed: true},
			{Name: colERToken, Kind: model.KindText},
			{Name: colERSubjectLookup, Kind: model.KindText, Indexed: true, Nullable: true},
			{Name: colERKeyID, Kind: model.KindUUID, Indexed: true},
			// data_classes is nullable: a subject kind whose targets carry no §2
			// class (e.g. "identity") legitimately affects none — encodeJSON encodes
			// an empty list as NULL.
			{Name: colERClasses, Kind: model.KindJSON, Nullable: true},
			{Name: colCaseRef, Kind: model.KindText, Indexed: true},
			{Name: colERReason, Kind: model.KindText, Nullable: true},
			{Name: colERRequestedBy, Kind: model.KindText},
			{Name: colERStatus, Kind: model.KindText, Indexed: true},
			{Name: colERPlanHash, Kind: model.KindText, Nullable: true},
			{Name: colERProviderIDs, Kind: model.KindJSON, Nullable: true},
		},
	}); err != nil {
		return err
	}

	// the RTBF chain of custody. APPEND-ONLY, ledger-anchored, dedupe-indexed
	// exactly like the hold_event trail; approval_ref is plain text "" when ungated so
	// the unique index compares plain equality (no NULLs-are-distinct hole).
	if err := reg.Register(model.EntityDescriptor{
		Kind:       erasureEventKind,
		Table:      erasureEventTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colEEErasureID, Kind: model.KindUUID, Indexed: true},
			{Name: colEEEvent, Kind: model.KindText, Indexed: true},
			{Name: colEEActor, Kind: model.KindText},
			{Name: colEEActorKind, Kind: model.KindText},
			{Name: colEENote, Kind: model.KindText, Nullable: true},
			{Name: colApprovalRef, Kind: model.KindText},
			{Name: colEEApprovers, Kind: model.KindJSON, Nullable: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// Same backstop as compliance_hold_event_uniq: at most ONE custody row per
			// (tenant, request, event, approval_ref) — a poll while pending must not
			// multiply custody; the loser's Create surfaces store.ErrConflict and the
			// handler answers like the winner. "executed"/"failed" events carry the
			// EXECUTING approval_ref, so a re-execute after a failure (same approval
			// within its time-box) dedupes too — by design: custody records the
			// attempt under that approval once; the receipt carries final counts.
			Name:    "compliance_erasure_event_uniq",
			Columns: []string{model.ColTenantID, colEEErasureID, colEEEvent, colApprovalRef},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the erasure certificate. APPEND-ONLY: counts, outcomes, the provider-floor
	// disclosure (§7: deleting our copy does not delete the provider's), the key-shred
	// fact, the post-erasure LIVE chain verification (the evidence-package integrity
	// pattern) and the documented retained-records reconciliation. Anchored to the
	// ledger head; manifest-hashed for tamper evidence of the body.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       erasureReceiptKind,
		Table:      erasureReceiptTbl,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colRCErasureID, Kind: model.KindUUID, Indexed: true},
			{Name: colRCSubject, Kind: model.KindText},
			{Name: colRCToken, Kind: model.KindText},
			{Name: colRCTargets, Kind: model.KindJSON},
			{Name: colRCAccount, Kind: model.KindText},
			{Name: colRCProvider, Kind: model.KindText},
			{Name: colRCFloorDays, Kind: model.KindInt},
			{Name: colRCFloorKnown, Kind: model.KindBool},
			{Name: colRCFloorSrc, Kind: model.KindText, Nullable: true},
			{Name: colRCShredded, Kind: model.KindBool},
			{Name: colRCVerifyOK, Kind: model.KindBool},
			{Name: colRCVerifyN, Kind: model.KindInt},
			{Name: colRCVerifyWhy, Kind: model.KindText, Nullable: true},
			{Name: colRCRetained, Kind: model.KindJSON},
			{Name: colCaseRef, Kind: model.KindText, Indexed: true},
			{Name: colApprovalRef, Kind: model.KindText},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
			{Name: colManifestHash, Kind: model.KindText},
		},
	}); err != nil {
		return err
	}

	// the registered OSCAL profile/SSP selection. MUTABLE (re-register replaces).
	// Deliberately NOT descriptor-Audited: register and unregister each append a SEMANTIC
	// self-audit attributed to the real principal in their own transaction (the governance
	// pattern, like the retention/hold planes) — one rich audit per act (framework,
	// selection size, document SHA-256), not a generic mutation row. One active selection
	// per (tenant, framework). Minimal data: the resolved control-id selection +
	// back-references + the ingested document's SHA-256 (the document is referenced by
	// hash, never copied; docs/SECURITY-HARDENING.md,§5).
	if err := reg.Register(model.EntityDescriptor{
		Kind:  oscalProfileKind,
		Table: oscalProfileTbl,
		Fields: []model.FieldSpec{
			{Name: colFramework, Kind: model.KindText, Indexed: true},
			{Name: colOPDocKind, Kind: model.KindText},
			{Name: colOPProfileUUID, Kind: model.KindText, Nullable: true},
			{Name: colOPSSPUUID, Kind: model.KindText, Nullable: true},
			{Name: colOPImportHref, Kind: model.KindText, Nullable: true},
			{Name: colOPSourceHref, Kind: model.KindText, Nullable: true},
			{Name: colOPSelected, Kind: model.KindJSON},
			{Name: colOPDropped, Kind: model.KindJSON, Nullable: true},
			{Name: colOPOscalVer, Kind: model.KindText, Nullable: true},
			{Name: colOPDocSHA, Kind: model.KindText},
			{Name: colTitle, Kind: model.KindText, Nullable: true},
			{Name: colOPNote, Kind: model.KindText, Nullable: true},
			{Name: colScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colOPRegisteredBy, Kind: model.KindText},
			{Name: colOPRegisteredAt, Kind: model.KindTimestamp, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			// One active selection per (tenant, framework). Unique index leads with
			// tenant_id; re-register Updates the row, never a second one.
			Name:    "compliance_oscal_profile_uniq",
			Columns: []string{model.ColTenantID, colFramework},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the maintained DORA Register of Information. MUTABLE (re-generate replaces the
	// active register per maintaining entity); NOT descriptor-Audited — generate/delete
	// self-audit semantically with rich meta. One active register per (tenant, entity_lei).
	// register holds the operator's structured RoI body (the deliverable they maintain),
	// anchored to the ledger head (seq + hash) for tamper-evidence.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  doraRegisterKind,
		Table: doraRegisterTbl,
		Fields: []model.FieldSpec{
			{Name: colDREntityLEI, Kind: model.KindText, Indexed: true},
			{Name: colDREntityName, Kind: model.KindText, Nullable: true},
			{Name: colDRRefDate, Kind: model.KindText, Nullable: true},
			{Name: colDRRegulation, Kind: model.KindText},
			{Name: colDRRegister, Kind: model.KindJSON},
			{Name: colDRValidation, Kind: model.KindJSON, Nullable: true},
			{Name: colDRReconcile, Kind: model.KindJSON, Nullable: true},
			{Name: colDRCounts, Kind: model.KindJSON, Nullable: true},
			{Name: colDRNote, Kind: model.KindText, Nullable: true},
			{Name: colDRDocSHA, Kind: model.KindText},
			{Name: colDRGeneratedBy, Kind: model.KindText},
			{Name: colDRGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active register per (tenant, maintaining entity). Unique index leads with
			// tenant_id; re-generate Updates the row, never a second one.
			Name:    "compliance_dora_register_uniq",
			Columns: []string{model.ColTenantID, colDREntityLEI},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the DORA major-incident classification + report draft. MUTABLE (the report
	// evolves initial → intermediate → final, re-classify replaces); NOT descriptor-Audited —
	// classify/delete self-audit semantically. One active classification per (tenant,
	// reference). classification holds the report/deadlines/basis; the ledger seq+hash anchor
	// it for tamper-evidence.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  doraIncidentKind,
		Table: doraIncidentTbl,
		Fields: []model.FieldSpec{
			{Name: colDIReference, Kind: model.KindText, Indexed: true},
			{Name: colDIFindingID, Kind: model.KindText, Nullable: true},
			{Name: colDIMajor, Kind: model.KindBool, Indexed: true},
			{Name: colDICritical, Kind: model.KindBool},
			{Name: colDICriteria, Kind: model.KindJSON, Nullable: true},
			{Name: colDIClassif, Kind: model.KindJSON},
			{Name: colDIRationale, Kind: model.KindText, Nullable: true},
			{Name: colDINote, Kind: model.KindText, Nullable: true},
			{Name: colDIDocSHA, Kind: model.KindText},
			{Name: colDIClassifiedBy, Kind: model.KindText},
			{Name: colDIClassifiedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active classification per (tenant, reference). Unique index leads with
			// tenant_id; re-classify Updates the row, never a second one.
			Name:    "compliance_dora_incident_uniq",
			Columns: []string{model.ColTenantID, colDIReference},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the ISO/IEC 42001 AIMS certification-readiness pack. MUTABLE (re-generate
	// replaces); NOT descriptor-Audited — generate/export/delete self-audit semantically
	// with rich meta. One active pack per tenant. The pack holds the structured
	// deliverables (SoA, AI policy, risk register, impact assessments, lifecycle controls,
	// supplier governance) anchored to the ledger head for tamper-evidence.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  aimsPackKind,
		Table: aimsPackTbl,
		Fields: []model.FieldSpec{
			{Name: colAPStandard, Kind: model.KindText},
			{Name: colAPOrgName, Kind: model.KindText, Indexed: true},
			{Name: colAPSoA, Kind: model.KindJSON},
			{Name: colAPPolicy, Kind: model.KindJSON, Nullable: true},
			{Name: colAPRiskReg, Kind: model.KindJSON, Nullable: true},
			{Name: colAPImpact, Kind: model.KindJSON, Nullable: true},
			{Name: colAPLifecycle, Kind: model.KindJSON, Nullable: true},
			{Name: colAPSupplier, Kind: model.KindJSON, Nullable: true},
			{Name: colAPValidation, Kind: model.KindJSON, Nullable: true},
			{Name: colAPScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colAPDocSHA, Kind: model.KindText},
			{Name: colAPGeneratedBy, Kind: model.KindText},
			{Name: colAPGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active pack per tenant. Unique index leads with tenant_id;
			// re-generate Updates the row, never a second one.
			Name:    "compliance_aims_pack_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the NIS 2 Directive significant-incident classification + tiered report
	// drafts. MUTABLE (the report evolves through phases: early_warning → notification
	// → intermediate → final, re-classify replaces); NOT descriptor-Audited —
	// classify/update/delete self-audit semantically. One active classification per
	// (tenant, reference). classification holds the report drafts/deadlines/basis;
	// the ledger seq+hash anchor it for tamper-evidence.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  nis2IncidentKind,
		Table: nis2IncidentTbl,
		Fields: []model.FieldSpec{
			{Name: colNIReference, Kind: model.KindText, Indexed: true},
			{Name: colNIFindingID, Kind: model.KindText, Nullable: true},
			{Name: colNISignificant, Kind: model.KindBool, Indexed: true},
			{Name: colNICrossBorder, Kind: model.KindBool},
			{Name: colNICrime, Kind: model.KindBool},
			{Name: colNICriteria, Kind: model.KindJSON, Nullable: true},
			{Name: colNIClassif, Kind: model.KindJSON},
			{Name: colNIRationale, Kind: model.KindText, Nullable: true},
			{Name: colNINote, Kind: model.KindText, Nullable: true},
			{Name: colNIPhase, Kind: model.KindText, Indexed: true},
			{Name: colNIDocSHA, Kind: model.KindText},
			{Name: colNIClassifiedBy, Kind: model.KindText},
			{Name: colNIClassifiedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "compliance_nis2_incident_uniq",
			Columns: []string{model.ColTenantID, colNIReference},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the US state AI law compliance-depth pack. MUTABLE (re-generate replaces the
	// active pack per tenant); NOT descriptor-Audited — generate/delete self-audit
	// semantically with rich meta. One active pack per tenant. The pack holds the
	// structured jurisdiction results anchored to the ledger head for tamper-evidence.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  usStateLawPackKind,
		Table: usStateLawPackTbl,
		Fields: []model.FieldSpec{
			{Name: colDPPackType, Kind: model.KindText, Indexed: true},
			{Name: colDPRegulation, Kind: model.KindText},
			{Name: colDPSections, Kind: model.KindJSON},
			{Name: colDPValidation, Kind: model.KindJSON, Nullable: true},
			{Name: colDPNote, Kind: model.KindText, Nullable: true},
			{Name: colDPDocSHA, Kind: model.KindText},
			{Name: colDPScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colGeneratedBy, Kind: model.KindText},
			{Name: colGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active pack per tenant. Unique index leads with tenant_id;
			// re-generate Updates the row, never a second one.
			Name:    "compliance_us_law_pack_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the sector overlay compliance-depth pack. MUTABLE (re-generate replaces the
	// active pack per tenant); NOT descriptor-Audited — generate/delete self-audit
	// semantically with rich meta. One active pack per tenant. Same column structure as the
	// US state law pack; pack_type distinguishes them.
	if err := reg.Register(model.EntityDescriptor{
		Kind:  sectorPackKind,
		Table: sectorPackTbl,
		Fields: []model.FieldSpec{
			{Name: colDPPackType, Kind: model.KindText, Indexed: true},
			{Name: colDPRegulation, Kind: model.KindText},
			{Name: colDPSections, Kind: model.KindJSON},
			{Name: colDPValidation, Kind: model.KindJSON, Nullable: true},
			{Name: colDPNote, Kind: model.KindText, Nullable: true},
			{Name: colDPDocSHA, Kind: model.KindText},
			{Name: colDPScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colGeneratedBy, Kind: model.KindText},
			{Name: colGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active pack per tenant. Unique index leads with tenant_id;
			// re-generate Updates the row, never a second one.
			Name:    "compliance_sector_pack_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}

	// the CCM (Continuous Controls Monitoring) snapshot. APPEND-ONLY (a snapshot is
	// immutable once taken — a new assessment creates a new snapshot row, never edits an
	// existing one). Multiple snapshots allowed per tenant (historical series).
	if err := reg.Register(model.EntityDescriptor{
		Kind:       ccmSnapshotKind,
		Table:      ccmSnapshotTbl,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colCSSnapshotAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colCSFrameworks, Kind: model.KindJSON},
			{Name: colCSSummary, Kind: model.KindJSON},
			{Name: colCSNote, Kind: model.KindText, Nullable: true},
			{Name: colGeneratedBy, Kind: model.KindText},
			{Name: colGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	// the CCM drift finding. APPEND-ONLY (a drift finding is immutable — it records
	// the detected change between two snapshots). One row per control that changed.
	// snapshot_ref links to the current (newer) snapshot.
	if err := reg.Register(model.EntityDescriptor{
		Kind:       ccmDriftKind,
		Table:      ccmDriftTbl,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colCDSnapshotRef, Kind: model.KindUUID, Indexed: true},
			{Name: colCDFrameworkID, Kind: model.KindText, Indexed: true},
			{Name: colCDControlID, Kind: model.KindText, Indexed: true},
			{Name: colCDTitle, Kind: model.KindText},
			{Name: colCDPrevStatus, Kind: model.KindText},
			{Name: colCDCurrStatus, Kind: model.KindText},
			{Name: colCDDirection, Kind: model.KindText, Indexed: true},
			{Name: colCDDetail, Kind: model.KindText, Nullable: true},
			{Name: colCDDetectedAt, Kind: model.KindTimestamp, Indexed: true},
		},
	}); err != nil {
		return err
	}

	// the FedRAMP 20x Key Security Indicators document. MUTABLE (re-generate
	// replaces the active KSI doc per tenant); NOT descriptor-Audited — generate/delete
	// self-audit semantically with rich meta. One active KSI doc per tenant. The document
	// holds the machine-readable KSIs (OSCAL v1.1.3) and authorization package structure,
	// anchored to the ledger head for tamper-evidence.
	return reg.Register(model.EntityDescriptor{
		Kind:  fedRAMPKSIKind,
		Table: fedRAMPKSITbl,
		Fields: []model.FieldSpec{
			{Name: colFKSystemName, Kind: model.KindText, Indexed: true},
			{Name: colFKImpactLevel, Kind: model.KindText, Indexed: true},
			{Name: colFKKSIs, Kind: model.KindJSON},
			{Name: colFKOscalVer, Kind: model.KindText},
			{Name: colFKAuthPkg, Kind: model.KindJSON, Nullable: true},
			{Name: colFKValidation, Kind: model.KindJSON, Nullable: true},
			{Name: colFKNote, Kind: model.KindText, Nullable: true},
			{Name: colFKDocSHA, Kind: model.KindText},
			{Name: colFKScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colGeneratedBy, Kind: model.KindText},
			{Name: colGeneratedAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colLedgerSeq, Kind: model.KindInt},
			{Name: colLedgerHash, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One active KSI doc per tenant. Unique index leads with tenant_id;
			// re-generate Updates the row, never a second one.
			Name:    "compliance_fedramp_ksi_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	})
}
