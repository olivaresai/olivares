// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import "context"

// This file is the OPEN-CORE half of NIS 2 incident-reporting depth seam: the
// interface the module consumes so a commercial add-on can apply the Directive (EU)
// 2022/2555 Art 23(3) significant-incident criteria to operator-supplied impact data and
// draft the tiered reports (early warning, incident notification, final report). The VALUE
// (the criteria application, the deadline computation, the report drafting) lives in the
// commercial add-on enterprise/nis2incident, wired ONLY under -tags enterprise (the
// RegulatoryPackager / AIMSPackager pattern). The open binary never links it.
//
// No rug-pull (LICENSING.md): the open framework catalog nis2 (frameworks.go), the
// regulatory calendar (calendar.go — NIS 2 milestones with source + verified_on), the
// on-demand live assessment (assess.go) and the evidence engine (evidence.go) are ALL
// unchanged and stay open. Without a wired NIS2 incident packager the new endpoints answer
// 501; the default binary is byte-identical.
//
// Honesty (docs/SECURITY-HARDENING.md): the control plane cannot MEASURE the significant-incident criteria
// (operational disruption severity, financial loss, persons affected) — so it never derives
// a "significant" verdict from its own telemetry. The operator supplies the impact data; the
// packager APPLIES the Art 23(3) criteria and drafts the reports. Every verdict and computed
// deadline is PROVISIONAL and requires human attestation; the legal classification and the
// duty to notify the CSIRT or competent authority rest with the entity.

// NIS2IncidentPackager is the closed seam for NIS 2 Directive significant-incident
// classification and tiered report drafting. The default is nil — without a wired packager
// the NIS2 incident endpoints answer 501 and the open nis2 catalog/calendar surfaces keep
// their behavior. The real implementation is enterprise/nis2incident, wired only under
// -tags enterprise.
type NIS2IncidentPackager interface {
	// ClassifySignificantIncident applies the Art 23(3) criteria to operator-supplied
	// impact data and returns a structured classification with report drafts for each
	// reporting phase (early warning, notification, final). It MUST be deny-closed:
	// invalid input or missing required fields is an error — never a silent partial.
	// Every verdict is PROVISIONAL: the legal classification and the duty to notify rest
	// with the entity, not the platform (docs/SECURITY-HARDENING.md).
	ClassifySignificantIncident(ctx context.Context, in NIS2IncidentInput) (*NIS2IncidentResult, error)
}

// NIS2IncidentInput is the operator-supplied impact data for a NIS 2 significant-incident
// classification. The compliance substrate (the nis2 framework assessment, the calendar)
// is available to the classifier through the assessment engine; the operator provides the
// impact facts the platform cannot observe.
type NIS2IncidentInput struct {
	// Reference is the operator's incident reference (ticket ID, never content).
	Reference string

	// FindingID optionally links the incident to a governed security/threat finding.
	FindingID string

	// Impact is the raw operator-supplied impact document (JSON: awareness_at, affected
	// services, users affected, financial loss estimate, operational disruption severity,
	// cross-border indicators, suspected criminal activity, root cause, mitigations).
	// The packager parses, validates and structures it; the operator's bytes are hashed
	// for the minimal-data anchor (SHA-256), never re-published elsewhere.
	Impact []byte
}

// NIS2IncidentResult is the structured significant-incident classification the closed
// add-on returns.
type NIS2IncidentResult struct {
	// Significant is the Art 23(3) verdict: true when either criterion (a) or (b) is met.
	Significant bool

	// CrossBorder is true when the incident has or could have cross-border impact.
	CrossBorder bool

	// SuspectedCrime is true when the incident is suspected to be caused by unlawful
	// or malicious action (Art 23(4)(a)).
	SuspectedCrime bool

	// CriteriaMet lists which Art 23(3) criteria the impact data satisfies.
	CriteriaMet []string

	// Rationale is the human-readable explanation of the classification decision.
	Rationale string

	// Deadlines maps each reporting phase to its computed deadline (from awareness_at).
	Deadlines map[string]any

	// ReportDrafts maps each phase (early_warning, notification, final) to a structured
	// report template pre-populated from the impact data.
	ReportDrafts map[string]any

	// Basis cites the Art 23 provisions each section rests on.
	Basis []map[string]string

	// Note is an optional caveat or honest gap.
	Note string
}

// nis2IncidentPhases is the ordered vocabulary of NIS 2 Art 23(4) reporting phases.
// Phase transitions are forward-only: early_warning → notification → intermediate → final.
var nis2IncidentPhases = []string{
	"early_warning", "notification", "intermediate", "final",
}

// validNIS2Phase returns true if the phase is in the vocabulary.
func validNIS2Phase(p string) bool {
	for _, v := range nis2IncidentPhases {
		if v == p {
			return true
		}
	}
	return false
}

// nis2PhaseIndex returns the ordinal of a phase (-1 if unknown).
func nis2PhaseIndex(p string) int {
	for i, v := range nis2IncidentPhases {
		if v == p {
			return i
		}
	}
	return -1
}
