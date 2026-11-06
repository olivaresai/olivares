// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// T2: the decision itself refuses the principal Ask acts under, even past its ceiling. The
// handler is called directly, as a route would be if a later change ever let the decision
// permission into Ask's ceiling, and the request stays pending.
func TestDecisionRefusesAskPrincipalPastItsCeiling(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := approvalTenantData{data: api.NewModuleData(st), tenant: tenant}
	var opened approvalDTO
	if err := data.Mutate(ctx, func(sc store.Scope) (err error) {
		opened, err = m.openApprovalRecord(ctx, sc, "user:requester", model.ActorUser, "requester",
			createApprovalRequest{SubjectKind: "ask_action", SubjectRef: "digest", Action: "ask.run.start"}, 0, m.clock.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ask, err := auth.Principal{Kind: auth.KindUser, UserID: "decider", Superadmin: true}.ForAsk(tenant, permApprovalAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.decideApproval(ctx, data, api.ModuleContext{Tenant: tenant, Principal: ask}, model.ID(opened.ID), ApprovalDecisionRequest{Decision: "approve"})
	var refusal approvalDecisionError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusForbidden {
		t.Fatalf("Ask's decision = %v, want a 403 refusal", err)
	}
}
