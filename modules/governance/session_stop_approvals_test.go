// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionStopCancellationKeepsOtherSessionsAndGovernance(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "session-stop")
	ctx := context.Background()
	open := func(action, session string) string {
		r := h.do("POST", "/v1/m/governance/approvals", admin, map[string]any{"action": action, "session_ref": session, "subject_kind": "repository", "subject_ref": "proof"}, tenantHdr(tenant))
		if r.code != 201 {
			t.Fatalf("create=%d %s", r.code, r.raw)
		}
		return r.body["id"].(string)
	}

	otherTenant := h.createOrg(admin, "other-session-stop")
	other := h.do("POST", "/v1/m/governance/approvals", admin, map[string]any{"action": "github.write", "session_ref": "session-own", "subject_kind": "repository", "subject_ref": "other-tenant"}, tenantHdr(otherTenant))
	if other.code != http.StatusCreated {
		t.Fatalf("other tenant request=%d %s", other.code, other.raw)
	}
	otherID := other.body["id"].(string)
	canceled := open("github.write", "session-own")
	kept := []string{open("github.write", "session-other"), open("nhi.rotate", "session-own"), open("security.posture.change", "session-own"), open("compliance.attest", "session-own"), open("evidence.export", "")}
	if err := h.gov.CancelSessionApprovals(ctx, tenant, []string{"session-own"}, "operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/m/governance/approvals/"+canceled, admin, nil, tenantHdr(tenant))
	if r.body["status"] != "canceled" {
		t.Fatalf("cancellation=%s", r.raw)
	}
	for _, id := range kept {
		r = h.do("GET", "/v1/m/governance/approvals/"+id, admin, nil, tenantHdr(tenant))
		if r.body["status"] != "pending" {
			t.Fatalf("unrelated request changed: %s", r.raw)
		}
	}

	otherRead := h.do("GET", "/v1/m/governance/approvals/"+otherID, admin, nil, tenantHdr(otherTenant))
	if otherRead.body["status"] != "pending" {
		t.Fatalf("another tenant changed: %s", otherRead.raw)
	}
	if r = h.decide(admin, tenant, canceled, "approve"); r.code != http.StatusConflict {
		t.Fatalf("decision=%d %s", r.code, r.raw)
	}
	if got := resolvedPayloads(t, h); len(got) != 1 || got[0].Outcome != "canceled" {
		t.Fatalf("resolution events=%+v", got)
	}
	if err := h.gov.CancelSessionApprovals(ctx, tenant, []string{"session-own"}, "operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if len(resolvedPayloads(t, h)) != 1 {
		t.Fatal("repeated stop republished cancellation")
	}
}

// SQLite serializes writers. Preserve the pending read across a real committed
// decision to reproduce the read/update interleaving possible on PostgreSQL.
type sessionStopDecisionRaceData struct {
	api.ModuleData
	afterRead  func()
	conflicted bool
}

func (d *sessionStopDecisionRaceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if d.afterRead == nil {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	var pending []model.Record
	if err := d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		pending, _, err = repo.List(ctx, model.Query{})
		return err
	}); err != nil {
		return err
	}
	hook := d.afterRead
	d.afterRead = nil
	hook()
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&sessionStopDecisionRaceScope{Scope: sc, pending: pending, conflicted: &d.conflicted})
	})
}

type sessionStopDecisionRaceScope struct {
	store.Scope
	pending    []model.Record
	conflicted *bool
}

func (s *sessionStopDecisionRaceScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if kind == "governance.approval" && err == nil {
		return &sessionStopDecisionRaceRepo{GenericRepo: repo, pending: s.pending, conflicted: s.conflicted}, nil
	}
	return repo, err
}

type sessionStopDecisionRaceRepo struct {
	store.GenericRepo
	pending    []model.Record
	conflicted *bool
}

func (r *sessionStopDecisionRaceRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return r.pending, model.Page{}, nil
}

func (r *sessionStopDecisionRaceRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	got, err := r.GenericRepo.Update(ctx, rec)
	if errors.Is(err, store.ErrConflict) {
		*r.conflicted = true
	}
	return got, err
}

func TestSessionStopCancellationSurvivesCommittedDecisionRace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "session-stop-decision-race")
	_, approver := h.roleUser(admin, tenant, "stop-approver@example.test", "admin")
	var ids []string
	for _, subject := range []string{"decision-race-one", "decision-race-two", "decision-race-three"} {
		r := h.do("POST", "/v1/m/governance/approvals", admin, map[string]any{"action": "github.write", "session_ref": "racing-session", "subject_kind": "repository", "subject_ref": subject}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create=%d %s", r.code, r.raw)
		}
		ids = append(ids, r.body["id"].(string))
	}
	raced := ids[1]
	data := &sessionStopDecisionRaceData{ModuleData: api.NewModuleData(h.st), afterRead: func() {
		if r := h.decide(approver, tenant, raced, "approve"); r.code != http.StatusOK {
			t.Fatalf("racing decision=%d %s", r.code, r.raw)
		}
	}}
	h.gov.UseData(data)
	if err := h.gov.CancelSessionApprovals(t.Context(), tenant, []string{"racing-session"}, "operator", model.ActorUser); err != nil {
		t.Errorf("stop cancellation after committed decision=%v", err)
	}
	if !data.conflicted {
		t.Fatal("test never exercised the real version conflict")
	}
	for _, id := range ids {
		want := "canceled"
		if id == raced {
			want = "approved"
		}
		r := h.do("GET", "/v1/m/governance/approvals/"+id, admin, nil, tenantHdr(tenant))
		if r.body["status"] != want {
			t.Errorf("approval after decision race=%s, want %s", r.raw, want)
		}
	}
}
