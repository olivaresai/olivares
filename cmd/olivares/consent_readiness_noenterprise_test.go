// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// An edition that refuses personal-data erasure must prove the refusal at its
// authorized routes. Community retains its complete-census requirement.
func assertEditionAbsenceProofReadiness(t *testing.T, _ *consentEstate, r store.Readiness) {
	t.Helper()
	t.Errorf("the Community composition opened unready: %s", r.Cause())
}
