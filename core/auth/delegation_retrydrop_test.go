// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"testing"
)

// TestRetryDroppedCapabilitiesConsistentWithStoredDecision pins FIX 4: on an
// idempotent retry the reported dropped-capabilities set must be recomputed
// against the STORED decision, not the current registration. Otherwise a
// capability that was effective at claim time but is de-registered before the
// retry is reported BOTH as stored-effective and freshly dropped — a contradiction.
func TestRetryDroppedCapabilitiesConsistentWithStoredDecision(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	// serviceA registers buffer_request + streaming; declare both so both are
	// effective at the ORIGINAL claim (nothing dropped).
	pr := freshPresented("retry-caps", "messages")
	pr.DeclaredCapabilities = map[string]bool{"buffer_request": true, "streaming": true}

	first, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first.DroppedCapabilities()) != 0 {
		t.Fatalf("first-claim dropped = %v, want empty", first.DroppedCapabilities())
	}

	// De-register streaming AFTER the claim was stored.
	if err := f.a.UpdatePEPServiceCapabilities(f.ctx, f.admin, f.serviceA.ID, map[string]bool{
		"buffer_request": true,
	}); err != nil {
		t.Fatalf("de-register streaming: %v", err)
	}

	// Re-present the SAME request → idempotent retry (surfaces the stored decision).
	retry, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if !retry.Retried() {
		t.Fatal("second verify was not classified as a retry")
	}

	eff := retry.EffectiveCapabilities()
	dropped := retry.DroppedCapabilities()
	// The stored decision kept streaming effective; the retry must reflect that and
	// must NOT also report streaming (or anything) as freshly dropped.
	if !eff["streaming"] || !eff["buffer_request"] {
		t.Fatalf("retry effective = %v, want the stored {buffer_request, streaming}", eff)
	}
	for k := range dropped {
		if eff[k] {
			t.Errorf("capability %q reported as BOTH stored-effective and dropped on retry", k)
		}
	}
}
