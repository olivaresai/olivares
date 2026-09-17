// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestConfinedPolicyRepoDeniesState proves the confined policy repository
// refuses the state capability with the EXISTING workspace-lineage denial
// rather than being absent from the type. The distinction matters to a caller:
// an absent capability means "this store cannot do it", while this denial means
// "you may not do it here", and conflating them would report the wrong cause.
//
// Satisfying the interface is never support: both methods always refuse.
func TestConfinedPolicyRepoDeniesState(t *testing.T) {
	repo := deniedPolicyRepo{deniedRepo[model.Policy]{what: "policies"}}
	var asRepository Repository[model.Policy] = repo
	writer, ok := asRepository.(PolicyStateWriter)
	if !ok {
		t.Fatal("confined policy repository does not expose PolicyStateWriter")
	}

	ctx := context.Background()
	if _, err := writer.GetPolicyState(ctx, model.NewID()); !errors.Is(err, ErrWorkspaceLineageRequired) {
		t.Fatalf("GetPolicyState err = %v, want ErrWorkspaceLineageRequired", err)
	}
	if _, err := writer.SetPolicyEnabled(ctx, model.NewID(), "recovery", 1, true); !errors.Is(err, ErrWorkspaceLineageRequired) {
		t.Fatalf("SetPolicyEnabled err = %v, want ErrWorkspaceLineageRequired", err)
	}
	// The ordinary denials are unchanged.
	if _, err := asRepository.Get(ctx, model.NewID()); !errors.Is(err, ErrWorkspaceLineageRequired) {
		t.Fatalf("Get err = %v, want the unchanged denial", err)
	}
	if _, err := asRepository.Update(ctx, model.Policy{}); !errors.Is(err, ErrWorkspaceLineageRequired) {
		t.Fatalf("Update err = %v, want the unchanged denial", err)
	}

	// The generic denied repository must NOT have gained the capability: it backs
	// every other confined entity, and widening it would make each of them
	// satisfy a policy interface they have nothing to do with.
	var generic Repository[model.Policy] = deniedRepo[model.Policy]{what: "policies"}
	if _, widened := generic.(PolicyStateWriter); widened {
		t.Fatal("generic deniedRepo gained PolicyStateWriter; keep it on the policy-specific type")
	}
}
