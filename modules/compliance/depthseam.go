// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import "context"

// This file is the OPEN-CORE half of compliance-depth seam: the unified interface
// the module consumes so a commercial add-on can provide US state AI law compliance packs,
// sector-overlay compliance packs (HIPAA/PCI/FINRA), continuous controls monitoring (CCM),
// and FedRAMP 20x Key Security Indicator (KSI) documents. The VALUE — the named-regulation
// structuring, the CCM drift-detection intelligence, the FedRAMP authorization-package
// structuring — lives in the commercial add-on enterprise/compliancedepth, wired ONLY under
// -tags enterprise (the RegulatoryPackager / AIMSPackager pattern). The open binary
// never links it.
//
// No rug-pull (LICENSING.md): the open framework catalog (frameworks.go — 4 US state laws + 3
// sector overlays), the regulatory calendar (calendar.go — milestones with source + verified_on),
// the on-demand live assessment (assess.go), the evidence engine (evidence.go) and the risk
// classifier (risk.go) are ALL unchanged and stay open. Without a wired depth packager the new
// endpoints answer 501; the default binary is byte-identical.
//
// Honesty (docs/SECURITY-HARDENING.md): the add-on automates evidence gathering, obligation mapping and
// reporting against named regulations; it does NOT make the operator compliant with any law
// and is NOT a certification. Every emitted verdict (obligation satisfied, control monitored,
// FedRAMP KSI met) is PROVISIONAL and requires human attestation. An honest gap is exported
// as a gap, never as satisfied; satisfied never rests on architectural evidence alone
// (assess.go:15). Cero over-claiming of dates: every regulatory date lives exclusively in
// calendar.go as a datum with its primary source and the date this repo last verified it.

// ComplianceDepthPackager is the closed seam for compliance depth on top of the open
// compliance substrate. The default is nil — without a wired depth packager the
// endpoints answer 501 and the open catalog/calendar/evidence/risk surfaces keep their
// behavior. The real implementation is enterprise/compliancedepth, wired only under -tags
// enterprise.
type ComplianceDepthPackager interface {
	// BuildUSStatePack structures operator-supplied jurisdiction context + the live
	// assessment of US state AI law frameworks (tx_traiga, ca_sb53, il_hb3773, co_sb26_189)
	// into a per-jurisdiction compliance pack: obligation mapping, notice/disclosure
	// templates, recordkeeping inventory, impact assessment structure, and the NIST AI RMF
	// affirmative-defense crosswalk. It MUST be deny-closed: invalid input or a pack with
	// no jurisdiction identifier is an error — never a silent partial. The returned pack is
	// a DRAFT: it never asserts the operator complies with the law.
	BuildUSStatePack(ctx context.Context, in USStateInput, assessments map[string]FrameworkAssessment) (*USStatePack, error)

	// BuildSectorPack structures operator-supplied sector context + the live assessment of
	// sector-overlay frameworks (hipaa_clinical_ai, pci_dss_401_ai, finra_genai) into a
	// per-sector compliance pack: obligation mapping, control-to-requirement crosswalk,
	// recordkeeping inventory, and gap analysis against the sector's specific requirements.
	// Deny-closed on invalid input. The returned pack is a DRAFT.
	BuildSectorPack(ctx context.Context, in SectorInput, assessments map[string]FrameworkAssessment) (*SectorPack, error)

	// RunCCMSnapshot re-evaluates the assessment engine for a set of frameworks at a point
	// in time and returns a timestamped snapshot of control statuses. The open binary already
	// does this on-demand (GET /frameworks/{id}/status); this method persists the result and
	// feeds the drift engine. Deny-closed on invalid input.
	RunCCMSnapshot(ctx context.Context, in CCMSnapshotInput, assessments map[string]FrameworkAssessment) (*CCMSnapshot, error)

	// DetectDrift compares two CCM snapshots (previous and current) and detects per-control
	// status changes (regressions, improvements, new gaps). Returns drift findings that feed
	// alerting and remediation tracking. Deny-closed on invalid input.
	DetectDrift(ctx context.Context, prev *CCMSnapshot, curr *CCMSnapshot) ([]DriftFinding, error)

	// BuildFedRAMPKSIs structures the live assessment into FedRAMP 20x Key Security
	// Indicators (KSIs) expressed in OSCAL v1.1.3 machine-readable format, with
	// DoD impact-level framing (IL2/IL4/IL5). Deny-closed on invalid input.
	// The returned document is a DRAFT.
	BuildFedRAMPKSIs(ctx context.Context, in FedRAMPKSIInput, assessments map[string]FrameworkAssessment) (*FedRAMPKSIDocument, error)
}

// --- US State Law Pack ---------------------------------------------------------------

// USStateInput is the operator-supplied jurisdiction context for a US state AI law
// compliance pack. The compliance substrate (the live assessment per-framework, the
// calendar milestones, the capability evidence map) is passed alongside.
type USStateInput struct {
	// Document is the raw operator-supplied jurisdiction context (JSON: deployer/developer
	// role, high-risk AI systems inventory, notice recipients, impact assessment scope,
	// NIST AI RMF implementation evidence). The packager parses, validates and structures
	// it; the operator's bytes are hashed for the minimal-data anchor (SHA-256).
	Document []byte
	// Jurisdictions lists the state frameworks to include (e.g. ["tx_traiga", "co_sb26_189"]).
	// Empty ⇒ all four.
	Jurisdictions []string
	// ScopeNote is an operator-supplied free-text note.
	ScopeNote string
}

// USStatePack is the structured US state AI law compliance pack the closed add-on returns.
type USStatePack struct {
	// Jurisdictions lists the per-jurisdiction results.
	Jurisdictions []JurisdictionResult
	// CrosswalkNIST maps each obligation across jurisdictions to NIST AI RMF subcategories,
	// building the affirmative-defense bridge where the statute provides one.
	CrosswalkNIST map[string]any
	// Validation are the packager's honest validation findings.
	Validation []DepthIssue
	// Note is an honest coverage caveat; empty when clean.
	Note string
}

// JurisdictionResult is one state law's compliance assessment.
type JurisdictionResult struct {
	// FrameworkID is the catalog framework (e.g. "tx_traiga").
	FrameworkID string
	// LawName is the human label (e.g. "Texas Responsible AI Governance Act (HB 1709)").
	LawName string
	// ObligationMap is the per-obligation mapping: obligation → status/evidence/gap.
	ObligationMap map[string]any
	// NoticeTemplates are draft notice/disclosure templates the law requires, populated as
	// far as the operator's input allows. The operator MUST review and customize them.
	NoticeTemplates map[string]any
	// RecordkeepingInventory lists the records the operator must maintain under this law,
	// mapped to control-plane evidence that covers them (or an honest gap).
	RecordkeepingInventory map[string]any
	// ImpactAssessment is the structured impact-assessment template the law requires (where
	// applicable), pre-populated from the risk classifier's existing classifications.
	ImpactAssessment map[string]any
	// AffirmativeDefense describes the NIST AI RMF affirmative-defense provision (where the
	// statute provides one) and the evidence the control plane can supply toward it.
	AffirmativeDefense map[string]any
}

// --- Sector Overlay Pack -------------------------------------------------------------

// SectorInput is the operator-supplied sector context for a sector-overlay compliance pack.
type SectorInput struct {
	// Document is the raw operator-supplied sector context (JSON: sector identity, regulated
	// systems, data classifications, specific compliance requirements).
	Document []byte
	// Sectors lists the sector frameworks to include (e.g. ["hipaa_clinical_ai", "pci_dss_401_ai"]).
	// Empty ⇒ all three.
	Sectors []string
	// ScopeNote is an operator-supplied free-text note.
	ScopeNote string
}

// SectorPack is the structured sector-overlay compliance pack the closed add-on returns.
type SectorPack struct {
	// Sectors lists the per-sector results.
	Sectors []SectorResult
	// Validation are the packager's honest validation findings.
	Validation []DepthIssue
	// Note is an honest coverage caveat; empty when clean.
	Note string
}

// SectorResult is one sector overlay's compliance assessment.
type SectorResult struct {
	// FrameworkID is the catalog framework (e.g. "hipaa_clinical_ai").
	FrameworkID string
	// SectorName is the human label (e.g. "HIPAA Clinical AI Overlay").
	SectorName string
	// ControlMapping maps each sector-specific requirement to the control-plane control(s)
	// that evidence it (or an honest gap).
	ControlMapping map[string]any
	// RecordkeepingInventory lists sector-specific records the operator must maintain.
	RecordkeepingInventory map[string]any
	// GapAnalysis lists the sector-specific requirements the control plane cannot evidence.
	GapAnalysis map[string]any
}

// --- CCM (Continuous Controls Monitoring) --------------------------------------------

// CCMSnapshotInput requests a point-in-time controls snapshot.
type CCMSnapshotInput struct {
	// Frameworks lists the framework IDs to snapshot (e.g. ["eu_ai_act", "soc2_tsc"]).
	// Empty ⇒ all catalog frameworks.
	Frameworks []string
	// ScopeNote is an operator-supplied free-text note.
	ScopeNote string
}

// CCMSnapshot is a timestamped point-in-time assessment snapshot.
type CCMSnapshot struct {
	// SnapshotAt is the timestamp (ISO 8601) when the snapshot was taken.
	SnapshotAt string
	// Frameworks lists the per-framework assessment results at snapshot time.
	Frameworks []CCMFrameworkSnapshot
	// Summary is the cross-framework aggregate.
	Summary CCMSummary
	// Note is an honest coverage caveat; empty when clean.
	Note string
}

// CCMFrameworkSnapshot is one framework's state in a CCM snapshot.
type CCMFrameworkSnapshot struct {
	FrameworkID string
	Name        string
	Controls    []CCMControlState
	Summary     AssessmentSummary
}

// CCMControlState is one control's status at snapshot time.
type CCMControlState struct {
	ControlID string
	Title     string
	Status    string // satisfied | by_design | partial | gap | unmapped
}

// CCMSummary is a cross-framework aggregate.
type CCMSummary struct {
	FrameworksMonitored int
	TotalControls       int
	Satisfied           int
	ByDesign            int
	Partial             int
	Gap                 int
	Unmapped            int
}

// DriftFinding is a detected status change between two CCM snapshots.
type DriftFinding struct {
	FrameworkID string
	ControlID   string
	Title       string
	PrevStatus  string
	CurrStatus  string
	// Direction is "regression" (satisfied/by_design → partial/gap), "improvement"
	// (gap/partial → satisfied), or "change" (lateral movement).
	Direction string
	// Detail describes the specific capability changes that caused the drift.
	Detail string
}

// --- FedRAMP 20x KSI -----------------------------------------------------------------

// FedRAMPKSIInput requests a FedRAMP 20x Key Security Indicators document.
type FedRAMPKSIInput struct {
	// Document is the raw operator-supplied authorization context (JSON: system name,
	// system owner, authorizing official, impact level, boundary description).
	Document []byte
	// ImpactLevel is the DoD impact level framing (IL2, IL4, IL5). Empty ⇒ IL2 default.
	ImpactLevel string
	// ScopeNote is an operator-supplied free-text note.
	ScopeNote string
}

// FedRAMPKSIDocument is the structured FedRAMP 20x KSI document the closed add-on returns.
type FedRAMPKSIDocument struct {
	// SystemName identifies the system under authorization.
	SystemName string
	// ImpactLevel is the applied DoD impact level (IL2/IL4/IL5).
	ImpactLevel string
	// KSIs are the Key Security Indicators expressed as machine-readable OSCAL v1.1.3
	// assessment-results observations.
	KSIs map[string]any
	// OscalVersion is the OSCAL version used (v1.1.3).
	OscalVersion string
	// AuthorizationPackage is the machine-readable authorization package structure
	// (system-security-plan + assessment-results + assessment-plan references).
	AuthorizationPackage map[string]any
	// Validation are the packager's honest validation findings.
	Validation []DepthIssue
	// Note is an honest coverage caveat; empty when clean.
	Note string
}

// --- Shared types --------------------------------------------------------------------

// DepthIssue is one validation or gap finding on a depth artifact. Bounded, non-sensitive.
type DepthIssue struct {
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Section  string `json:"section,omitempty"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

// --- persisted DTOs ------------------------------------------------------------------

// depthPackDTO is a stored depth pack (US law or sector) as returned to a caller.
type depthPackDTO struct {
	ID           string         `json:"id"`
	PackType     string         `json:"pack_type"` // "us_state_law" | "sector_overlay"
	Regulation   string         `json:"regulation,omitempty"`
	Sections     map[string]any `json:"sections,omitempty"`
	Validation   []DepthIssue   `json:"validation,omitempty"`
	ErrorCount   int            `json:"error_count"`
	Note         string         `json:"note,omitempty"`
	DocSHA256    string         `json:"doc_sha256"`
	ScopeNote    string         `json:"scope_note,omitempty"`
	GeneratedBy  string         `json:"generated_by"`
	GeneratedAt  string         `json:"generated_at"`
	LedgerAnchor map[string]any `json:"ledger_anchor,omitempty"`
	Disclaimer   string         `json:"disclaimer"`
}

// ccmSnapshotDTO is a stored CCM snapshot as returned to a caller.
type ccmSnapshotDTO struct {
	ID          string         `json:"id"`
	SnapshotAt  string         `json:"snapshot_at"`
	Frameworks  map[string]any `json:"frameworks,omitempty"`
	Summary     map[string]any `json:"summary,omitempty"`
	Note        string         `json:"note,omitempty"`
	GeneratedBy string         `json:"generated_by"`
	GeneratedAt string         `json:"generated_at"`
	Disclaimer  string         `json:"disclaimer"`
}

// driftFindingDTO is a stored drift finding as returned to a caller.
type driftFindingDTO struct {
	ID          string `json:"id"`
	SnapshotRef string `json:"snapshot_ref"`
	FrameworkID string `json:"framework_id"`
	ControlID   string `json:"control_id"`
	Title       string `json:"title"`
	PrevStatus  string `json:"prev_status"`
	CurrStatus  string `json:"curr_status"`
	Direction   string `json:"direction"`
	Detail      string `json:"detail,omitempty"`
	DetectedAt  string `json:"detected_at"`
}

// fedRAMPKSIDTO is a stored FedRAMP KSI document as returned to a caller.
type fedRAMPKSIDTO struct {
	ID           string         `json:"id"`
	SystemName   string         `json:"system_name"`
	ImpactLevel  string         `json:"impact_level"`
	KSIs         map[string]any `json:"ksis,omitempty"`
	OscalVersion string         `json:"oscal_version"`
	AuthPkg      map[string]any `json:"authorization_package,omitempty"`
	Validation   []DepthIssue   `json:"validation,omitempty"`
	ErrorCount   int            `json:"error_count"`
	Note         string         `json:"note,omitempty"`
	DocSHA256    string         `json:"doc_sha256"`
	ScopeNote    string         `json:"scope_note,omitempty"`
	GeneratedBy  string         `json:"generated_by"`
	GeneratedAt  string         `json:"generated_at"`
	LedgerAnchor map[string]any `json:"ledger_anchor,omitempty"`
	Disclaimer   string         `json:"disclaimer"`
}

// Disclaimers (docs/SECURITY-HARDENING.md — helps comply, never certifies).
const (
	usStateLawDisclaimer = "US state AI law compliance pack drafted from the control plane's " +
		"live assessment and operator-supplied context, mapped to the obligations of the named " +
		"state law(s). The control plane automates evidence gathering and obligation mapping; it " +
		"does NOT make the operator compliant with any state law and this is NOT legal advice or " +
		"a certification. Notice templates are DRAFTS an attorney must review. The " +
		"NIST AI RMF affirmative-defense crosswalk evidences the operator's risk-management " +
		"framework alignment; it does not itself constitute the affirmative defense. All verdicts " +
		"are provisional and require human attestation."

	sectorOverlayDisclaimer = "Sector-overlay compliance pack drafted from the control plane's " +
		"live assessment and operator-supplied sector context, mapped to the requirements of the " +
		"named sector regulation(s)/guidance. The control plane automates evidence gathering and " +
		"requirement mapping; it does NOT make the operator compliant with any sector regulation " +
		"and this is NOT legal advice or a certification. All verdicts are " +
		"provisional and require human attestation."

	ccmDisclaimer = "Continuous controls monitoring (CCM) snapshot taken from the live assessment " +
		"engine at the recorded timestamp. Control statuses are derived from the same capability " +
		"evidence as the on-demand assessment (assess.go); a control without operational evidence " +
		"is never satisfied. Drift findings compare successive snapshots " +
		"control-by-control; they are ADVISORY signals, not compliance verdicts."

	fedRAMPKSIDisclaimer = "FedRAMP 20x Key Security Indicators (KSIs) structured from the " +
		"control plane's live assessment in OSCAL v1.1.3 machine-readable format. The control " +
		"plane automates KSI evidence gathering; it does NOT constitute a FedRAMP authorization " +
		"and this is NOT legal advice or a certification. The authorization " +
		"package is a DRAFT for a 3PAO/authorizing-official review. DoD impact-level (IL2/IL4/IL5) " +
		"framing is a mapping/message aid, not an authorization determination. All verdicts are " +
		"provisional and require human attestation."
)
