// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "testing"

// Probe failures must not inherit a provisioning remedy for an optional pool.
func TestReadinessFailureCodesHaveNoProvisioningRemedy(t *testing.T) {
	// The two codes readiness invents are its own: they must NOT collide with a
	// curated seam message, or a probe failure would inherit a provisioning remedy
	// that does not apply to it.
	for _, own := range []string{readinessProbeUnavailableCode, readinessSetupStateCode} {
		if _, taken := honestSeamMessage[own]; taken {
			t.Errorf("readiness code %q collides with a curated seam message", own)
		}
	}
}
