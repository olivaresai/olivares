// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type stopOrderStore struct {
	store.Store
	locks *int
}

func (s stopOrderStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&stopOrderScope{Scope: sc, locks: s.locks})
	})
}

func TestKillSwitchReenableSpendFencesApprovalReads(t *testing.T) {
	st, tenant := openRevisionStore(t, store.EngineSQLite)
	ctx := context.Background()
	m := New(WithClock(&intClock{t: intBase}))
	var stopID, approvalID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := engageForGeneration(ctx, m, sc, "fence-agent"); err != nil {
			return err
		}
		stops, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rows, err := listAll(ctx, stops)
		if err != nil || len(rows) != 1 {
			t.Fatalf("stops = %d, %v", len(rows), err)
		}
		stopID = model.ID(rows[0].String(model.ColID))
		dto, err := m.openApprovalRecord(ctx, sc, model.ActorSystem, model.ActorSystem, "", createApprovalRequest{
			Action: ksReenableAction, SubjectKind: "killswitch", SubjectRef: stopID.String(), Reason: "fence probe",
		}, ksReenableFloor, m.clock.Now())
		if err != nil {
			return err
		}
		approvalID = model.ID(dto.ID)
		rows[0][colKSReenableAppr] = dto.ID
		if _, err := stops.Update(ctx, rows[0]); err != nil {
			return err
		}
		approvals, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		approval, err := approvals.Get(ctx, approvalID)
		if err != nil {
			return err
		}
		approval[colStatus] = statusApproved
		approval[colApproveCount] = int64(2)
		if _, err := approvals.Update(ctx, approval); err != nil {
			return err
		}
		decisions, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		for _, user := range []string{"user:a", "user:b"} {
			if _, err := decisions.Create(ctx, model.Record{
				colApprovalID: approvalID.String(), colDecision: decisionApprove,
				colDecider: user, colDeciderUser: user, colDecidedAt: model.NewTimestamp(intBase).String(),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	locks := 0
	mc := api.ModuleContext{
		Tenant:    tenant,
		Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()},
		Data:      api.NewScopedData(stopOrderStore{Store: st, locks: &locks}, tenant),
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", stopID.String())
	req := httptest.NewRequest(http.MethodPost, "/killswitch/"+stopID.String()+"/reenable", strings.NewReader("{}"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec := httptest.NewRecorder()
	m.handleReenableKillSwitch(rec, req, mc)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-enable with the repository fence = %d %s", rec.Code, rec.Body.String())
	}
	if locks < 2 {
		t.Fatalf("re-enable took %d fences, want one for discovery and one for the spend", locks)
	}
}
