// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestMCPGatewaySessionToolsDefaultOnPreservesExplicitOff(t *testing.T) {
	ctx := t.Context()
	st := testStore(t)
	svc := auth.NewMCPGatewayStore(st)
	a, b := model.TenantID(model.NewID()), model.TenantID(model.NewID())
	for _, tenant := range []model.TenantID{a, b} {
		got, err := svc.Get(ctx, tenant)
		if err != nil || !got.SessionTools || got.Version != 0 || len(got.Servers) != 0 {
			t.Fatalf("new tenant must expose session tools without a write: %+v %v", got, err)
		}
	}
	off, err := svc.SetSessionTools(ctx, adminActor(), a, 0, false)
	if err != nil || off.SessionTools || off.Version != 1 {
		t.Fatalf("operator switch off: %+v %v", off, err)
	}
	reopened := auth.NewMCPGatewayStore(st)
	got, err := reopened.Get(ctx, a)
	if err != nil || got.SessionTools || got.Version != 1 {
		t.Fatalf("stored off must survive reopening: %+v %v", got, err)
	}
	created, err := reopened.PutServer(ctx, adminActor(), a, got.Version, "", mcpGatewayInput())
	if err != nil || created.SessionTools || len(created.Servers) != 1 || created.Servers[0].Enabled {
		t.Fatalf("adding a disabled server must preserve operator off: %+v %v", created, err)
	}
	if untouched, err := reopened.Get(ctx, b); err != nil || !untouched.SessionTools || untouched.Version != 0 {
		t.Fatalf("another tenant keeps its unwritten default: %+v %v", untouched, err)
	}
	on, err := reopened.SetSessionTools(ctx, adminActor(), a, created.Version, true)
	if err != nil || !on.SessionTools {
		t.Fatalf("operator switch on: %+v %v", on, err)
	}
	if persisted, err := auth.NewMCPGatewayStore(st).Get(ctx, a); err != nil || !persisted.SessionTools || persisted.Version != on.Version {
		t.Fatalf("stored on must survive reopening: %+v %v", persisted, err)
	}

	// A roster written by an older version is existing state, even when its
	// switch is absent. The new-tenant default must not widen that state.
	legacy := model.TenantID(model.NewID())
	_, err = auth.NewSourceStore(st).Put(ctx, adminActor(), model.SourceDef{
		Name: "mcp-gateway", Scope: legacy, Tenant: legacy.String(), Kind: "olivares.mcp-gateway",
		Config: map[string]string{"gateway_json": `{"servers":[]}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if old, err := reopened.Get(ctx, legacy); err != nil || old.SessionTools || old.Version != 1 {
		t.Fatalf("legacy stored configuration must retain off: %+v %v", old, err)
	}
}
