// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/reporting"
)

// reportingComplianceAdapter bridges the compliance module's exported AssessAll
// into the reporting module's ComplianceSource interface. Neither module imports
// the other; the composition root owns this glue.
type reportingComplianceAdapter struct {
	comp *compliance.Module
}

var _ reporting.ComplianceSource = reportingComplianceAdapter{}

func (a reportingComplianceAdapter) GatherComplianceData(ctx context.Context, tenant model.TenantID, framework string) (reporting.ComplianceData, error) {
	assessments, err := a.comp.AssessAll(ctx, tenant)
	if err != nil {
		return reporting.ComplianceData{}, err
	}
	var frameworks []reporting.FrameworkReport
	for _, fa := range assessments {
		if framework != "" && fa.Framework != framework {
			continue
		}
		fr := reporting.FrameworkReport{
			ID:      fa.Framework,
			Name:    fa.Name,
			Version: fa.Version,
			Summary: reporting.AssessmentSummary{
				Total:     fa.Summary.Total,
				Satisfied: fa.Summary.Satisfied,
				ByDesign:  fa.Summary.ByDesign,
				Partial:   fa.Summary.Partial,
				Gap:       fa.Summary.Gap,
				Unmapped:  fa.Summary.Unmapped,
			},
		}
		for _, ca := range fa.Controls {
			fr.Controls = append(fr.Controls, reporting.ControlReport{
				ID:          ca.ControlID,
				Title:       ca.Title,
				Requirement: ca.Requirement,
				Status:      string(ca.Status),
				Note:        ca.Note,
			})
		}
		frameworks = append(frameworks, fr)
	}
	return reporting.ComplianceData{
		Frameworks: frameworks,
		Generated:  time.Now().UTC(),
		Disclaimer: compliance.ReportDisclaimer(),
	}, nil
}
