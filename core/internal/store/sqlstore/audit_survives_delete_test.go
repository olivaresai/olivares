// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAuditChainSurvivesAgentAndWorkspaceDeletion pins that deleting an agent
// (soft delete) or a workspace (hard row delete) keeps every ledger entry that
// names it, and that the tenant's chain still verifies end to end. A cascade
// from the entity to its audit rows, or a delete path that rewrites the chain,
// would erase the record of what the deleted entity did. It drives the store, the
// layer every delete path goes through, on both engines: the foreign keys that
// could cascade are the engine's own.
func TestAuditChainSurvivesAgentAndWorkspaceDeletion(t *testing.T) {
	forEachCustodyEngine(t, func(t *testing.T, e custodyEngine) {
		cfg, _ := e.prepare(t)
		testAuditChainSurvivesDeletion(t, openCustodyStore(t, cfg, nil))
	})
}

func testAuditChainSurvivesDeletion(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "acme-"+uniqueSuffix())
	agent := mustCreateAgent(t, st, tenant, "bot")

	var ws model.Workspace
	audit := func(sc store.Scope, action string, kind model.Kind, id model.ID) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:1", ActorKind: model.ActorUser, Action: action,
			TargetKind: kind, TargetID: id,
		})
		return err
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		w, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Team A", Slug: "team-a", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		ws = w
		if err := audit(sc, "agent.update", "core.agent", agent.ID); err != nil {
			return err
		}
		return audit(sc, "workspace.update", "core.workspace", ws.ID)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delete both entities the way the product does: the delete and its own
	// audit entry commit together.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := sc.Agents().Delete(ctx, agent.ID); err != nil {
			return err
		}
		if err := audit(sc, "agent.delete", "core.agent", agent.ID); err != nil {
			return err
		}
		if err := sc.Workspaces().Delete(ctx, ws.ID); err != nil {
			return err
		}
		return audit(sc, "workspace.delete", "core.workspace", ws.ID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Agents().Get(ctx, agent.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("agent read after delete: err = %v, want ErrNotFound", err)
		}
		if _, err := sc.Workspaces().Get(ctx, ws.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("workspace read after delete: err = %v, want ErrNotFound", err)
		}
		var total int64
		seen := map[string]bool{}
		if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			total++
			if ev.TargetID == agent.ID || ev.TargetID == ws.ID {
				seen[ev.Action] = true
			}
			return nil
		}); err != nil {
			return err
		}
		for _, action := range []string{"agent.update", "agent.delete", "workspace.update", "workspace.delete"} {
			if !seen[action] {
				t.Errorf("ledger entry %q for a deleted entity is gone", action)
			}
		}
		rep, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !rep.OK || rep.BreakAt != 0 || rep.Checked != total {
			t.Errorf("verify after delete = %+v, want OK over all %d events", rep, total)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}
