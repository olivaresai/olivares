// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

func assertScopedLineageKinds(t *testing.T, decision auth.ScopedEvidenceDecision, policy store.AuthorizationFactRef, kinds ...model.Kind) {
	t.Helper()
	want := map[model.Kind]bool{model.AuthorizationEpochKind: true}
	for _, k := range kinds {
		epoch, _ := model.LineageEpochKind(k)
		want[epoch] = true
	}
	if len(decision.Facts) != len(want) {
		t.Fatalf("facts %#v, wanted kinds %#v", decision.Facts, want)
	}
	for _, f := range decision.Facts {
		if !want[f.Kind] || f.ID != policy.ID || f.Version < 1 {
			t.Fatalf("unexpected lineage fact %#v", f)
		}
		if f.Kind == model.AuthorizationEpochKind && f != policy {
			t.Fatalf("policy fact changed")
		}
		delete(want, f.Kind)
	}
}

type lineageHiddenScope struct {
	store.Scope
	store.TransactionClock
	store.AuthorizationEpochReader
}

func TestScopedLineageRequiresActualEpochCapability(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
		return lineageHiddenScope{sc, sc.(store.TransactionClock), sc.(store.AuthorizationEpochReader)}
	}}
	scoped, _ := typedEvidenceScopedEngine(t, f, data, `permit(principal, action, resource);`, 0, FreshnessRecord{})
	req := typedEvidenceRequest(f.tenant)
	req.Resource = auth.ResourceAttrs{Kind: "session", ID: model.NewID().String()}
	got, err := scoped.ScopedEvidence(typedEvidenceContext(t, time.Now().Add(time.Minute)), req)
	if err != nil || got.ResourceGuard.Verdict != auth.CheckUnknown || got.Effect != auth.EffectAbstain || len(got.Facts) != 0 {
		t.Fatalf("uncovered lineage = %#v, %v", got, err)
	}
}

func TestScopedLineageCapturesConsultedAbsenceAndOptInOnly(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	data := f.data(typedEvidenceNow, nil)
	var ws model.Workspace
	var session model.Session
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		ws, err = sc.Workspaces().Create(context.Background(), model.Workspace{Name: "lineage", Slug: "lineage"})
		if err != nil {
			return err
		}
		session, err = sc.Sessions().Create(context.Background(), model.Session{ExternalID: "lineage-s", WorkspaceID: ws.ID, AgentID: model.NewID()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scoped, policy := typedEvidenceScopedEngine(t, f, data, `permit(principal, action, resource) when { resource in Workspace::"lineage" };`, 0, FreshnessRecord{})
	for _, inherit := range []bool{false, true} {
		req := typedEvidenceRequest(f.tenant)
		req.Resource = auth.ResourceAttrs{Kind: "session", ID: session.ID.String()}
		req.Route.SessionInheritsAgentGroups = inherit
		got, err := scoped.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req)
		if err != nil || got.Effect != auth.EffectGrant || got.ResourceGuard.Verdict != auth.CheckClean {
			t.Fatalf("decision=%#v err=%v", got, err)
		}
		kinds := []model.Kind{"core.session", "core.workspace"}
		if inherit {
			kinds = append(kinds, "core.agent_group_member")
		}
		assertScopedLineageKinds(t, got, policy, kinds...)
	}
}

func TestScopedLineageS399ResolvesStoredDefaultWorkspace(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	var ws model.Workspace
	var session model.Session
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		ws, err = sc.DefaultWorkspace(context.Background())
		if err != nil {
			return err
		}
		session, err = sc.Sessions().Create(context.Background(), model.Session{ExternalID: "default-lineage"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !session.WorkspaceID.IsZero() {
		t.Fatal("fixture did not preserve legacy default representation")
	}
	req := typedEvidenceRequest(f.tenant)
	req.Principal = f.confinedPrincipal(t, ws.ID)
	req.Resource = auth.ResourceAttrs{Kind: "session", ID: session.ID.String()}
	scoped, policy := typedEvidenceScopedEngine(t, f, f.data(typedEvidenceNow, nil), "", 0, FreshnessRecord{})
	got, err := scoped.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req)
	if err != nil || got.ResourceGuard.Verdict != auth.CheckClean || got.Effect == auth.EffectForbid {
		t.Fatalf("default workspace denied: %#v %v", got, err)
	}
	assertScopedLineageKinds(t, got, policy, "core.session", "core.workspace")
}
