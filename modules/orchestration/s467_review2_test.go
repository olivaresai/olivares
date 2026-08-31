// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import "testing"

// s467_review2_test.go — regression tests for the second adversarial-review round
// (Codex): non-canonical serialization (substitution WITHOUT a race).

// A "|"-joined preimage let subject_ref="a|b",cadence="c" collide with
// subject_ref="a",cadence="b|c". The length-prefixed encoding must not.
func TestS467CanonicalNoDelimiterCollision(t *testing.T) {
	if canonicalFields("a|b", "c") == canonicalFields("a", "b|c") {
		t.Fatal("canonicalFields collided across a moved '|' delimiter")
	}
	if canonicalFields("a", "bc") == canonicalFields("ab", "c") {
		t.Fatal("canonicalFields collided across a moved boundary")
	}
	// The effect digest (full-binding) must not collide either.
	h, m := newHarness(t)
	_ = h
	d1 := m.effectDigest(operationSpec{tenant: "t", surface: "s", approvalRef: "a|b", targetFp: "c"})
	d2 := m.effectDigest(operationSpec{tenant: "t", surface: "s", approvalRef: "a", targetFp: "b|c"})
	if d1 == d2 {
		t.Fatal("effect digest collided across a moved delimiter (substitution without a race)")
	}
}
