// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// This file is the OPEN-CORE half of OSCAL reinforcement: the seam that lets a
// commercial add-on emit a FedRAMP-adjacent OSCAL plan-of-action-and-milestones (POA&M)
// alongside the Business evidence export's three models (component-definition +
// assessment-results + control-mapping, oscal.go). The VALUE — the named-regulation POA&M
// model (the FedRAMP package) — lives in enterprise/oscalingest, wired ONLY under -tags
// enterprise. Without a wired builder Business export emits its three models with
// no POA&M. Community keeps stored JSON/CSV export and answers 501 for OSCAL.
//
// Honesty (docs/SECURITY-HARDENING.md): a POA&M lists the OPEN findings (the not-satisfied controls of the
// sealed package) as planned remediation items — it NEVER asserts a control is satisfied
// (oscal.go keeps that invariant: only live operational evidence → OSCAL satisfied). It is a
// remediation-tracking aid, not a conformance claim.

// POAMBuilder renders an OSCAL plan-of-action-and-milestones from a sealed evidence package's
// control results. The default is nil — Business OSCAL export then emits its three models
// unchanged. The real implementation is enterprise/oscalingest, wired only under -tags
// enterprise.
type POAMBuilder interface {
	// BuildPOAM renders the POA&M model as a JSON object to attach to the OSCAL bundle, from
	// the package's NOT-satisfied controls. It returns (nil, nil) when there is nothing to
	// plan (every control satisfied) so the export simply omits the model. An entitlement or
	// rendering error is disclosed as a fixed omission reason, without the error's contents;
	// it never fails the stored evidence export.
	BuildPOAM(in POAMInput) (map[string]any, error)
}

// POAMInput is the export-facing, EXPORTED view of a sealed package the closed POA&M builder
// consumes (the open controlResultDTO is unexported — the boundary crosses through this typed
// value, the ProfileRef pattern). It is minimal: the package's identity + ledger anchor
// + the per-control status, never raw evidence payloads.
type POAMInput struct {
	PackageID     string
	Framework     string
	FrameworkName string
	GeneratedAt   string
	OscalVersion  string
	ManifestHash  string
	LedgerSeq     int64
	LedgerHash    string
	IntegrityOK   bool
	// Items are the package's control results (already scoped to any registered profile, the
	// same set the assessment-results carry). The builder selects the not-satisfied ones.
	Items []POAMItem
	// Profile is the optional registered OSCAL profile/SSP scoping back-reference; nil
	// when the export is include-all.
	Profile *ProfileRef
	// Source is the canonical framework-catalog href (the private exporter's single source of truth,
	// oscalSourcePrefix + framework) the POA&M's import-ssp/source anchors to — flowed through
	// the seam so the closed builder never re-spells the prefix (drift-proof, like OscalVersion).
	Source string
}

// POAMItem is one control's status in the sealed package, the unit a POA&M item derives from.
type POAMItem struct {
	ControlID string
	// Status is the precise product status (satisfied | by_design | partial | gap | unmapped).
	Status string
	// Satisfied mirrors the OSCAL finding-status mapping (oscal.go: ONLY StatusSatisfied →
	// satisfied), so the builder never has to re-derive the honesty invariant.
	Satisfied bool
	Title     string
	Summary   string
}
