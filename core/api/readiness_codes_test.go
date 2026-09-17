// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"strings"
	"testing"
)

// TestReadinessAdminPoolCodeMatchesTheSetupRefusal ties the readiness code to the
// one the setup ceremony already answers with. They are two literals in two files;
// an operator who greps their probe body for the code their POST /v1/setup returned
// must find the same string, and nothing in the compiler makes that true.
//
// statusFor is asked with the PRODUCTION-SHAPED error (the sentinel wrapped in the
// store's own remedy, productionShapedEnumerationError in errors_adminpool_test.go),
// not a bare sentinel, so this cannot pass against an error the engine never sends.
func TestReadinessAdminPoolCodeMatchesTheSetupRefusal(t *testing.T) {
	_, code := statusFor(productionShapedEnumerationError())
	if code != readinessAdminPoolCode {
		t.Fatalf("statusFor answers %q and readiness reports %q: the two halves of one condition drifted",
			code, readinessAdminPoolCode)
	}
}

// TestReadinessRemedyIsTheCuratedSentence pins the remedy readiness serves. It is
// the curated constant keyed on the CODE — the one that cannot echo a wrapped
// error's text — and it must still name what the operator has to do. A lookup miss
// would serve an EMPTY remedy, which is the mute first boot this work exists to
// remove, reintroduced through a map key.
func TestReadinessRemedyIsTheCuratedSentence(t *testing.T) {
	remedy, ok := honestSeamMessage[readinessAdminPoolCode]
	if !ok {
		t.Fatalf("no curated remedy is keyed on %q: readiness would answer an empty string", readinessAdminPoolCode)
	}
	for _, want := range []string{"BYPASSRLS", "olivares db init", "--admin-role", "--admin-dsn"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("the curated remedy no longer names %q: %q", want, remedy)
		}
	}
	// The two codes readiness invents are its own: they must NOT collide with a
	// curated seam message, or a probe failure would inherit a provisioning remedy
	// that does not apply to it.
	for _, own := range []string{readinessProbeUnavailableCode, readinessSetupStateCode} {
		if _, taken := honestSeamMessage[own]; taken {
			t.Errorf("readiness code %q collides with a curated seam message", own)
		}
	}
}
