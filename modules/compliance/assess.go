// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import "sort"

// This file is the assessment ENGINE: pure functions that fold capability evidence
// into control status. The honesty rule (docs/SECURITY-HARDENING.md) is encoded in controlStatus:
//   - no mapped capability                  → unmapped (an honest gap)
//   - every mapped present, ≥1 operational  → satisfied (live evidence backs it)
//   - every mapped present, all architectural → by_design (design-ready, NOT satisfied)
//   - some present                          → partial
//   - none present                          → gap
// "Satisfied" can NEVER rest on architectural (design) evidence alone.

// controlStatus computes a control's status from its mapped capabilities' evidence.
func controlStatus(mapped []CapabilityEvidence) ControlStatus {
	if len(mapped) == 0 {
		return StatusUnmapped
	}
	present, operationalPresent := 0, 0
	for _, ev := range mapped {
		if ev.State == EvidencePresent {
			present++
			if ev.Class == ClassOperational {
				operationalPresent++
			}
		}
	}
	switch {
	case present == 0:
		return StatusGap
	case present < len(mapped):
		return StatusPartial
	case operationalPresent > 0:
		return StatusSatisfied
	default:
		return StatusByDesign
	}
}

// assessControl evaluates one control against the tenant's capability evidence.
func assessControl(c Control, caps map[CapabilityKey]CapabilityEvidence) ControlAssessment {
	mapped := make([]CapabilityEvidence, 0, len(c.Capabilities))
	for _, key := range c.Capabilities {
		if ev, ok := caps[key]; ok {
			mapped = append(mapped, ev)
		} else {
			// A control referencing an unknown capability key is a catalog bug; surface
			// it as unknown rather than silently dropping it.
			mapped = append(mapped, CapabilityEvidence{Key: key, State: EvidenceUnknown, Detail: "unknown capability"})
		}
	}
	return ControlAssessment{
		ControlID:    c.ID,
		Title:        c.Title,
		Requirement:  c.Requirement,
		Criterion:    c.Criterion,
		Status:       controlStatus(mapped),
		Note:         c.Note,
		Capabilities: mapped,
	}
}

// assessFramework evaluates a whole framework against the tenant's capability
// evidence, in catalog order, with a status summary.
func assessFramework(fw Framework, caps map[CapabilityKey]CapabilityEvidence) FrameworkAssessment {
	fa := FrameworkAssessment{
		Framework:  fw.ID,
		Name:       fw.Name,
		Version:    fw.Version,
		Disclaimer: fw.Disclaimer,
		Controls:   make([]ControlAssessment, 0, len(fw.Controls)),
	}
	for _, c := range fw.Controls {
		ca := assessControl(c, caps)
		fa.Controls = append(fa.Controls, ca)
		fa.Summary.Total++
		switch ca.Status {
		case StatusSatisfied:
			fa.Summary.Satisfied++
		case StatusByDesign:
			fa.Summary.ByDesign++
		case StatusPartial:
			fa.Summary.Partial++
		case StatusGap:
			fa.Summary.Gap++
		case StatusUnmapped:
			fa.Summary.Unmapped++
		}
	}
	return fa
}

// gapControls returns only the controls that are not fully backed — partial, gap or
// unmapped — the gap-analysis view. by_design is included as a (design-only) caveat.
func gapControls(fa FrameworkAssessment) []ControlAssessment {
	out := make([]ControlAssessment, 0)
	for _, ca := range fa.Controls {
		switch ca.Status {
		case StatusPartial, StatusGap, StatusUnmapped, StatusByDesign:
			out = append(out, ca)
		}
	}
	return out
}

// missingCapabilities returns, for a control assessment, the keys of capabilities
// that are not present — what an operator must turn on to close the gap.
func missingCapabilities(ca ControlAssessment) []CapabilityKey {
	var out []CapabilityKey
	for _, ev := range ca.Capabilities {
		if ev.State != EvidencePresent {
			out = append(out, ev.Key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
