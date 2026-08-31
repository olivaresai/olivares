// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestRevokeDelegationAuditsOncePerRealRevoke pins FIX C at the auth layer: the
// Authenticator.RevokeDelegationHandle wrapper audits delegation.revoke ONLY when the
// store op reports it actually flipped a row (changed=true). A revoke of an
// already-revoked handle changes nothing and must not emit a second, misleading
// delegation.revoke event for a revocation that did not happen.
func TestRevokeDelegationAuditsOncePerRealRevoke(t *testing.T) {
	f := newDelegFixture(t)
	_, stored := f.mint(t, f.serviceA)

	if err := f.a.RevokeDelegationHandle(f.ctx, f.admin, stored.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	// Second revoke of the now-revoked handle: idempotent, and it must NOT audit again.
	if err := f.a.RevokeDelegationHandle(f.ctx, f.admin, stored.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}

	count := 0
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(f.ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == "delegation.revoke" && ev.TargetID == stored.ID {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("delegation.revoke audit count = %d, want exactly 1 (no duplicate for a no-op revoke)", count)
	}
}
