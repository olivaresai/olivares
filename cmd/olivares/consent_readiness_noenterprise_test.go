// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// An edition that refuses personal-data erasure must prove the refusal at its
// authorized routes. Community retains its complete-census requirement.
func assertEditionAbsenceProofReadiness(t *testing.T, _ *consentEstate, r store.Readiness) {
	t.Helper()
	t.Errorf("the Community composition opened unready: %s", r.Cause())
}

// wantEditionRetirementRefusal is the seam a retirement test calls once the pump
// has run on the case s, whose seeded row is row. An edition that cannot prove
// an account's history absent on this engine refuses the retirement; there it
// checks the account's record shows the refusal and what the steps ahead of
// the refusing one found, and returns true. Community retires on every engine.
func (e *consentEstate) wantEditionRetirementRefusal(*testing.T, model.ID, model.TenantID, retirementSeeder, model.ID) bool {
	return false
}
