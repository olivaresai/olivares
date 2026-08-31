// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestStoreFinalizeDerivesActorFromClaim pins FIX B: the store's FinalizeDecisionClaim
// must attribute the delegation.finalize audit to the claim's OWN immutable
// PEPServiceID, never to a caller-supplied actor. A raw caller could otherwise sign a
// finalize as any service (or ""), forging audit attribution for a security-critical
// decision.
func TestStoreFinalizeDerivesActorFromClaim(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	issued := model.NewTimestamp(time.Now())

	var service model.PEPService
	var claim model.PDPDecisionClaim
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		service, err = as.PEPServices().Create(ctx, model.PEPService{Name: "actor-pep"})
		if err != nil {
			return err
		}
		claim, _, err = as.ClaimDecision(ctx, model.PDPDecisionClaim{
			HandleJTI:          model.NewID(),
			PEPServiceID:       service.ID,
			NonceHash:          "actor-nonce",
			RequestFingerprint: "actor-fp",
			RequestIssuedAt:    issued,
		}, nil)
		return err
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	// The store op takes no actor: it derives the audit actor from the claim's own
	// immutable PEPServiceID, so there is no caller-controlled field to forge.
	good := []byte(`{"decision":"allow"}`)
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, good, sha256HexBytes(good), "p1")
		return e
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	wantActor := "pep_service:" + service.ID.String()
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		found := false
		if werr := as.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == "delegation.finalize" && ev.TargetID == claim.ID {
				found = true
				if ev.Actor != wantActor {
					t.Errorf("finalize audit actor = %q, want %q (derived from the claim's PEPServiceID, not caller input)", ev.Actor, wantActor)
				}
			}
			return nil
		}); werr != nil {
			return werr
		}
		if !found {
			t.Error("no delegation.finalize audit event found")
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
}
