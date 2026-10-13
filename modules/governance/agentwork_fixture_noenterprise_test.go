//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Community must still evaluate lifecycle protections stored by a Business build.
// The fixture writes the historical rows directly because authoring them is paid.
func registerWorkAgent(t *testing.T, h *harness, tenant model.TenantID, _ string, agentRef, sponsorRef string) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.nhi_lifecycle"))
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"identity_ref": agentRef, "source": "spiffe", "kind": "agent",
			"sponsor_ref": sponsorRef, "criticality": "high", "max_age_seconds": int64(0),
			"staleness_status": "unknown", "enforcement": "monitor", "orphaned": false,
			"offboard_state": "none",
		})
		return err
	}); err != nil {
		t.Fatalf("seed stored agent %s: %v", agentRef, err)
	}
}

func materializeWorkOrphan(t *testing.T, h *harness, tenant model.TenantID, _ string, ref string) {
	setStoredWorkLifecycleFields(t, h, tenant, ref, model.Record{"orphaned": true})
}

func blockWorkAgent(t *testing.T, h *harness, tenant model.TenantID, _ string, ref string) {
	setStoredWorkLifecycleFields(t, h, tenant, ref, model.Record{"enforcement": "blocked"})
}

func offboardWorkAgent(t *testing.T, h *harness, tenant model.TenantID, _ string, ref string) {
	setStoredWorkLifecycleFields(t, h, tenant, ref, model.Record{"offboard_state": "soft_deleted"})
}

func setStoredWorkLifecycleFields(t *testing.T, h *harness, tenant model.TenantID, ref string, fields model.Record) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.nhi_lifecycle"))
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{Column: "identity_ref", Op: model.OpEq, Value: ref}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("stored agent %s not found", ref)
		}
		row := rows[0]
		for key, value := range fields {
			row[key] = value
		}
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatalf("update stored agent %s: %v", ref, err)
	}
}
