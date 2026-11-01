// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOpenApprovalRecordUsesRequester(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "requester-test", Slug: "requester-test", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner := model.NewID()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		out, err := m.openApprovalRecord(ctx, sc, "engine:orchestration-cron", model.ActorSystem, "", ApprovalRequest{
			Action: "orchestration.schedule.fire", SubjectKind: "schedule",
			RequestedBy: "user:" + owner.String(), RequestedByUser: owner,
		}, 0, model.NewTimestamp(time.Now()))
		if err != nil {
			return err
		}
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(out.ID))
		if err != nil {
			return err
		}
		if rec.String(colRequestedBy) != "user:"+owner.String() || rec.String(colRequestedByUser) != owner.String() {
			t.Fatalf("requester = %q/%q, want owner", rec.String(colRequestedBy), rec.String(colRequestedByUser))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
