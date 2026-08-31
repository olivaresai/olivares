// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// This file defines the module's own vocabulary: the in-repo catalog types
// (Framework/Control/Capability), the evidence/assessment value objects, and the
// risk-tier scheme. None of it is persisted as-is — it is the deterministic source
// of truth the assessment engine evaluates against live tenant evidence.

// CapabilityKey is a stable identifier for a control-plane capability a control can
// map to. The set is fixed (capabilities.go); a control maps to a subset.
type CapabilityKey string

// CapabilityClass says HOW a capability is evidenced — the line that keeps the
// module honest (docs/SECURITY-HARDENING.md).
type CapabilityClass string

const (
	// ClassOperational is evidenced ONLY by real tenant data; absent data is a gap.
	ClassOperational CapabilityClass = "operational"
	// ClassArchitectural is a platform-design guarantee cited to the design docs —
	// present by construction, and clearly labeled as design evidence, not runtime
	// telemetry.
	ClassArchitectural CapabilityClass = "architectural"
)

// Capability describes one control-plane capability a control can be backed by.
type Capability struct {
	Key   CapabilityKey   `json:"key"`
	Class CapabilityClass `json:"class"`
	Name  string          `json:"name"`
	Desc  string          `json:"description"`
	// Cite is the design-doc/contract citation for an architectural capability (the
	// proof for "present by construction"); empty for operational capabilities.
	Cite string `json:"cite,omitempty"`
}

// FrameworkPin is the VERSION PIN of a catalog framework as verifiable data:
// the exact document, its publication date, the canonical primary-source URL and the
// date this repo last verified the pin against that source. A framework whose pin
// drifts from its source is a product defect, not a prose problem — the pin makes
// the drift testable.
type FrameworkPin struct {
	// Document is the canonical document identifier (e.g. "Regulation (EU) 2024/1689",
	// "NIST AI 100-1").
	Document string `json:"document"`
	// PublishedOn is the document's publication date (ISO 8601), when the source
	// states one; empty for documents still in development.
	PublishedOn string `json:"published_on,omitempty"`
	// SourceURL is the canonical primary-source URL the pin was verified against.
	SourceURL string `json:"source_url"`
	// VerifiedOn is the ISO date this repo last verified Document/PublishedOn against
	// SourceURL. Every pin MUST carry one (tested).
	VerifiedOn string `json:"verified_on"`
	// Status is the document's lifecycle status: "in_force" (binding law applying or
	// partially applying), "final" (published standard/guidance), "guidance"
	// (non-binding advisory), "in_development" (draft/beta — explicitly not final).
	Status string `json:"status"`
}

// Framework pin statuses.
const (
	PinInForce       = "in_force"
	PinFinal         = "final"
	PinGuidance      = "guidance"
	PinInDevelopment = "in_development"
)

// Framework is a versioned regulatory framework or standard (the in-repo catalog).
type Framework struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Version    string       `json:"version"`
	Authority  string       `json:"authority"`
	Disclaimer string       `json:"disclaimer"`
	Pin        FrameworkPin `json:"pin"`
	Controls   []Control    `json:"controls"`
}

// Control is one control/article/clause and the capabilities that honestly evidence
// it. A control with an empty Capabilities is an HONEST gap by construction — the
// module surfaces it as not-yet-mappable, never as satisfied.
type Control struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Requirement  string          `json:"requirement"`
	Criterion    string          `json:"criterion"`
	Capabilities []CapabilityKey `json:"capabilities"`
	// Note is an honest coverage caveat carried into every assessment: the part of
	// the control's obligation the control plane CANNOT evidence (e.g. dataset bias
	// examination, watermarking, key management). It keeps partial coverage from
	// reading as full coverage (docs/SECURITY-HARDENING.md).
	Note string `json:"note,omitempty"`
	// MilestoneIDs link the control to the regulatory-calendar milestones that govern
	// WHEN its obligation applies (calendar.go). Dates live ONLY in the calendar
	// — as data with source + verified_on — never as prose in the control text, so a
	// date change is one data edit, not a silent prose decay. Every referenced ID must
	// resolve (tested).
	MilestoneIDs []string `json:"milestone_ids,omitempty"`
}

// EvidenceState is the outcome of probing one capability for a tenant.
type EvidenceState string

const (
	// EvidencePresent means the capability is backed (real data or a cited design
	// guarantee).
	EvidencePresent EvidenceState = "present"
	// EvidenceAbsent means no evidence backs the capability — an honest gap.
	EvidenceAbsent EvidenceState = "absent"
	// EvidenceUnknown means the capability could not be evaluated (the evidence
	// source is unreachable); treated as absent for status but reported distinctly.
	EvidenceUnknown EvidenceState = "unknown"
)

// CapabilityEvidence is the result of evaluating one capability against live tenant
// evidence. It is minimal-data: counts, a non-sensitive reason and references — never
// a payload (docs/SECURITY-HARDENING.md).
type CapabilityEvidence struct {
	Key    CapabilityKey   `json:"key"`
	Class  CapabilityClass `json:"class"`
	State  EvidenceState   `json:"state"`
	Detail string          `json:"detail"`
	// Count is the operational row count backing the capability (0 for architectural
	// or absent). More reports whether the count was truncated at the page cap.
	Count int64 `json:"count,omitempty"`
	More  bool  `json:"more,omitempty"`
	// Refs point at the underlying evidence (a ledger range, an entity kind, a design
	// citation, an attestation) so an auditor can follow the trail.
	Refs []EvidenceRef `json:"refs,omitempty"`
}

// EvidenceRef points at the underlying evidence without copying it.
type EvidenceRef struct {
	// Kind is the reference class: "audit_chain" | "entity" | "design" | "attestation".
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// ControlStatus is a control's assessed status. It is deliberately NOT "compliant":
// the module reports control STATUS + EVIDENCE, never certification (docs/SECURITY-HARDENING.md).
type ControlStatus string

const (
	// StatusSatisfied means every mapped capability is present AND at least one of
	// them is OPERATIONAL — i.e. real live evidence backs the control, not design
	// alone. "Satisfied" never rests on architectural evidence only (docs/SECURITY-HARDENING.md).
	StatusSatisfied ControlStatus = "satisfied"
	// StatusByDesign means every mapped capability is present but they are ALL
	// architectural — the platform provides the control by construction, yet there is
	// no operational telemetry proving it is exercised for this tenant. It is honest
	// "design-ready", deliberately distinct from "satisfied".
	StatusByDesign ControlStatus = "by_design"
	// StatusPartial means some, but not all, mapped capabilities are present.
	StatusPartial ControlStatus = "partial"
	// StatusGap means no mapped capability is present.
	StatusGap ControlStatus = "gap"
	// StatusUnmapped means the control has no mapped capability yet — an honest gap
	// surfaced distinctly so a real auditor sees what the platform cannot yet evidence.
	StatusUnmapped ControlStatus = "unmapped"
)

// ControlAssessment is one control evaluated against live tenant evidence.
type ControlAssessment struct {
	ControlID    string               `json:"control_id"`
	Title        string               `json:"title"`
	Requirement  string               `json:"requirement"`
	Criterion    string               `json:"criterion"`
	Status       ControlStatus        `json:"status"`
	Note         string               `json:"note,omitempty"`
	Capabilities []CapabilityEvidence `json:"capabilities"`
}

// FrameworkAssessment is a framework evaluated against live tenant evidence.
type FrameworkAssessment struct {
	Framework  string              `json:"framework"`
	Name       string              `json:"name"`
	Version    string              `json:"version"`
	Disclaimer string              `json:"disclaimer"`
	Summary    AssessmentSummary   `json:"summary"`
	Controls   []ControlAssessment `json:"controls"`
}

// AssessmentSummary counts control statuses for a framework.
type AssessmentSummary struct {
	Total     int `json:"total"`
	Satisfied int `json:"satisfied"`
	ByDesign  int `json:"by_design"`
	Partial   int `json:"partial"`
	Gap       int `json:"gap"`
	Unmapped  int `json:"unmapped"`
}

// RiskTier is the EU AI Act risk classification (Art. 5/6/50 + minimal), the base
// scheme cross-mapped to the NIST AI RMF functions.
type RiskTier string

const (
	// TierUnacceptable is the EU AI Act Art. 5 prohibited tier. A heuristic NEVER
	// asserts it — only a human reviewer may set it (it is a legal determination).
	TierUnacceptable RiskTier = "unacceptable"
	// TierHigh is the Art. 6 + Annex III high-risk tier.
	TierHigh RiskTier = "high"
	// TierLimited is the Art. 50 limited/transparency tier.
	TierLimited RiskTier = "limited"
	// TierMinimal is the minimal-risk tier (the conservative default).
	TierMinimal RiskTier = "minimal"
)

// RiskState is the governance state of a classification.
type RiskState string

const (
	// RiskSuggested is the heuristic suggestion, awaiting human review.
	RiskSuggested RiskState = "suggested"
	// RiskApproved means a reviewer accepted the suggested tier.
	RiskApproved RiskState = "approved"
	// RiskOverridden means a reviewer set a different tier than suggested.
	RiskOverridden RiskState = "overridden"
)

// nistFunctionsForTier returns the NIST AI RMF functions a tier emphasizes (the
// cross-walk EU AI Act tier -> NIST AI RMF GOVERN/MAP/MEASURE/MANAGE).
func nistFunctionsForTier(t RiskTier) []string {
	switch t {
	case TierUnacceptable:
		// Prohibited: screen out before deployment — GOVERN decides, MAP surfaces it.
		return []string{"GOVERN", "MAP"}
	case TierHigh:
		return []string{"GOVERN", "MAP", "MEASURE", "MANAGE"}
	case TierLimited:
		return []string{"GOVERN", "MAP", "MEASURE"}
	default:
		return []string{"GOVERN", "MAP"}
	}
}
