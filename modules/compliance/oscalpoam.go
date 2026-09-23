// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"errors"

	"github.com/olivaresai/olivares/core/license"
)

// This file is the OPEN-CORE half of OSCAL reinforcement: the seam that lets a
// commercial add-on emit a FedRAMP-adjacent OSCAL plan-of-action-and-milestones (POA&M)
// alongside the evidence export's three open models (component-definition +
// assessment-results + control-mapping, oscal.go). The VALUE — the named-regulation POA&M
// model (the FedRAMP package) — lives in enterprise/oscalingest, wired ONLY under -tags
// enterprise. The open OSCAL plumbing stays open and BYTE-IDENTICAL: without a wired builder
// the export emits its three models with no POA&M (no rug-pull).
//
// Honesty (docs/SECURITY-HARDENING.md): a POA&M lists the OPEN findings (the not-satisfied controls of the
// sealed package) as planned remediation items — it NEVER asserts a control is satisfied
// (oscal.go keeps that invariant: only live operational evidence → OSCAL satisfied). It is a
// remediation-tracking aid, not a conformance claim.

// POAMBuilder renders an OSCAL plan-of-action-and-milestones from a sealed evidence package's
// control results. The default is nil — the evidence OSCAL export then emits its three models
// unchanged. The real implementation is enterprise/oscalingest, wired only under -tags
// enterprise.
type POAMBuilder interface {
	// BuildPOAM renders the POA&M model as a JSON object to attach to the OSCAL bundle, from
	// the package's NOT-satisfied controls. It returns (nil, nil) when there is nothing to
	// plan (every control satisfied) so the export simply omits the model. An entitlement or
	// rendering error is disclosed as a fixed omission reason, without the error's contents;
	// it never fails the open evidence export.
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
	// Source is the canonical framework-catalog href (the open module's single source of truth,
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

// poamInputFrom builds a POAMInput from a sealed package DTO + its (already profile-scoped)
// control results + the optional profile reference. It applies oscalStatusState so Satisfied
// is consistent with the assessment-results export.
func poamInputFrom(dto evidencePackageDTO, results []controlResultDTO, fwName string, ref *ProfileRef) POAMInput {
	items := make([]POAMItem, 0, len(results))
	for _, c := range results {
		items = append(items, POAMItem{
			ControlID: c.ControlID,
			Status:    c.Status,
			Satisfied: oscalStatusState(c.Status) == "satisfied",
			Title:     c.Title,
			Summary:   c.Summary,
		})
	}
	return POAMInput{
		PackageID:     dto.ID,
		Framework:     dto.Framework,
		FrameworkName: fwName,
		GeneratedAt:   dto.GeneratedAt,
		OscalVersion:  oscalVersion,
		ManifestHash:  dto.ManifestHash,
		LedgerSeq:     dto.LedgerSeq,
		LedgerHash:    dto.LedgerHash,
		IntegrityOK:   dto.IntegrityOK,
		Items:         items,
		Profile:       ref,
		Source:        oscalSourcePrefix + dto.Framework,
	}
}

type poamAttachmentOutcome struct {
	attached       bool
	omissionReason string
}

// attachPOAM preserves the open models and sealed manifest. A builder error adds a safe
// omission notice to the export disclaimer and returns its fixed reason for the audit.
// An absent builder or nil model leaves the export unchanged.
func (m *Module) attachPOAM(doc map[string]any, dto evidencePackageDTO, results []controlResultDTO, fwName string, ref *ProfileRef) poamAttachmentOutcome {
	if m.poamBuilder == nil {
		return poamAttachmentOutcome{}
	}
	poam, err := m.poamBuilder.BuildPOAM(poamInputFrom(dto, results, fwName, ref))
	if err != nil {
		reason := "render_failed"
		notice := " POA&M section omitted: rendering failed (render_failed)."
		if errors.Is(err, license.ErrAddonRequiresLicense) {
			reason = "addon_requires_license"
			notice = " POA&M section omitted: the required add-on license is unavailable (addon_requires_license)."
		}
		disclaimer, _ := doc["disclaimer"].(string)
		doc["disclaimer"] = disclaimer + notice
		if m.log != nil {
			m.log.Warn("compliance: OSCAL POA&M omitted",
				"package_id", dto.ID, "framework", dto.Framework, "reason", reason)
		}
		return poamAttachmentOutcome{omissionReason: reason}
	}
	if poam == nil {
		return poamAttachmentOutcome{}
	}
	doc["plan-of-action-and-milestones"] = poam
	return poamAttachmentOutcome{attached: true}
}
