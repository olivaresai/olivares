// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAgentCoreItemsFromGrantsGolden(t *testing.T) {
	ctx := context.Background()
	gov := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, gov.RegisterSchema)
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
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		roleRepo, err := sc.Ext(customRoleKind)
		if err != nil {
			return err
		}
		if _, err := roleRepo.Create(ctx, roleRecord("agentcore-export", "", "", "", []string{"agent:read"}, []string{"models"}, nil, "test")); err != nil {
			return err
		}
		groupRepo, err := sc.Ext(permGroupKind)
		if err != nil {
			return err
		}
		if _, err := groupRepo.Create(ctx, groupRecord("models", "", "", []string{"model:write"}, "test")); err != nil {
			return err
		}
		grantRepo, err := sc.Ext(scopedGrantKind)
		if err != nil {
			return err
		}
		grants := []scopedGrant{
			{SubjectKind: subjectUser, SubjectRef: "u1", Role: "agentcore-export", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "payments"}},
			{SubjectKind: subjectRole, SubjectRef: "viewer", Role: "agentcore-export", RoleCustom: true, Scope: scopeSpec{Tree: scopeAgentGroup, Ref: "bots"}},
			{SubjectKind: subjectGroup, SubjectRef: "g1", Role: "agentcore-export", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
		}
		for _, g := range grants {
			if _, err := grantRepo.Create(ctx, grantRecord(g, "test")); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []agentcore.ExportItem
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		got, err = agentCoreItemsFromGrants(ctx, tenant, sc)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	perms := []string{"agent:read", "model:write"}
	want := []agentcore.ExportItem{
		{Kind: "grant", Tenant: tenant.String(), SubjectKind: "group", SubjectRef: "g1", ScopeKind: "tenant", Effect: "permit", Perms: perms},
		{Kind: "grant", Tenant: tenant.String(), SubjectKind: "role", SubjectRef: "viewer", ScopeKind: "agent_group", Workspace: "bots", Effect: "permit", Perms: perms},
		{Kind: "grant", Tenant: tenant.String(), SubjectKind: "user", SubjectRef: "u1", ScopeKind: "workspace", Workspace: "payments", Effect: "permit", Perms: perms},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items mismatch\n got: %#v\nwant: %#v", got, want)
	}
}
