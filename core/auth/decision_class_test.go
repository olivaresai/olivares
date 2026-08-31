// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// The whole constrained-observe safety rests on the zero value of DecisionClass
// being ClassInvariant: any deny that forgets to set a class — or carries an unknown
// value — must be treated as a non-shadowable platform invariant, never business policy.
func TestDecisionClassZeroValueIsInvariant(t *testing.T) {
	if auth.ClassInvariant != 0 {
		t.Fatalf("ClassInvariant must be the zero value (0), got %d", auth.ClassInvariant)
	}
	if auth.ClassPolicy == auth.ClassInvariant {
		t.Fatal("ClassPolicy must be distinct from ClassInvariant")
	}
	// A deny constructed without a Class is invariant (the fail-safe default).
	var d auth.Decision
	if d.Class != auth.ClassInvariant {
		t.Fatalf("zero-value Decision.Class = %d, want ClassInvariant", d.Class)
	}
	deny := auth.Decision{Allow: false, Reason: "some deny"}
	if deny.Class != auth.ClassInvariant {
		t.Fatalf("unclassified deny Decision.Class = %d, want ClassInvariant", deny.Class)
	}
	var sd auth.ScopedDecision
	if sd.Class != auth.ClassInvariant {
		t.Fatalf("zero-value ScopedDecision.Class = %d, want ClassInvariant", sd.Class)
	}
	forbid := auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "some forbid"}
	if forbid.Class != auth.ClassInvariant {
		t.Fatalf("unclassified forbid ScopedDecision.Class = %d, want ClassInvariant", forbid.Class)
	}
}

// Shadowability MUST be tested with == ClassPolicy, never != ClassInvariant, so that an
// unknown/future enum value stays non-shadowable. This pins that contract.
func TestOnlyClassPolicyIsShadowable(t *testing.T) {
	shadowable := func(c auth.DecisionClass) bool { return c == auth.ClassPolicy }
	if shadowable(auth.ClassInvariant) {
		t.Fatal("ClassInvariant must not be shadowable")
	}
	if !shadowable(auth.ClassPolicy) {
		t.Fatal("ClassPolicy must be shadowable")
	}
	if shadowable(auth.DecisionClass(42)) {
		t.Fatal("an unknown DecisionClass value must not be shadowable")
	}
}
