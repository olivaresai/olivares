// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AssessAll runs the assessment engine against every cataloged framework for
// the given tenant and returns the results. It is the programmatic API for the
// data that GET /v1/m/compliance/summary serves over HTTP, intended for the
// report-generation module.
func (m *Module) AssessAll(ctx context.Context, tenant model.TenantID) ([]FrameworkAssessment, error) {
	var results []FrameworkAssessment
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		s, e := gatherEvidence(ctx, sc)
		if e != nil {
			return e
		}
		caps := evaluateCapabilities(s)
		results = make([]FrameworkAssessment, 0, len(catalog))
		for _, fw := range catalog {
			results = append(results, assessFramework(fw, caps))
		}
		return nil
	})
	return results, err
}

// ReportDisclaimer returns the standard compliance report disclaimer text.
func ReportDisclaimer() string { return reportDisclaimer }
