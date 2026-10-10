// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const workspaceEntityProbePermission auth.Permission = "workspaceprobe:target:write"
const workspaceEntityProbeAction auth.CedarAction = "workspaceprobe:prepare"

type workspaceEntityProbe struct{}

func (workspaceEntityProbe) APINamespace() string { return "workspaceprobe" }
func (workspaceEntityProbe) Permissions() []auth.Permission {
	return []auth.Permission{workspaceEntityProbePermission}
}
func (workspaceEntityProbe) Actions() []auth.CedarAction {
	return []auth.CedarAction{workspaceEntityProbeAction}
}
func workspaceEntityProbeMetadata() api.RouteMetadata {
	return api.RouteMetadata{
		RouteMetadata: auth.RouteMetadata{
			CedarAction: string(workspaceEntityProbeAction), RequireScopedGrant: true, MinimumAAL: auth.AAL3,
		},
		CoreKind: api.CoreKindWorkspace, IDParam: "workspace_id", ResourceKind: "workspace",
		ConcealDeniedAsNotFound: true,
	}
}
func (workspaceEntityProbe) APIRoutes(reg api.RouteRegistrar) {
	governed, ok := reg.(interface {
		HandlePolicy(string, string, auth.Permission, api.RouteMetadata, api.ModuleHandler)
	})
	if !ok {
		panic("workspace probe requires governed route registration")
	}
	governed.HandlePolicy(http.MethodPost, "/workspaces/{workspace_id}/probe", workspaceEntityProbePermission,
		workspaceEntityProbeMetadata(), func(w http.ResponseWriter, _ *http.Request, mc api.ModuleContext) {
			question := auth.Request{
				Principal: mc.Principal, Tenant: mc.Tenant, Permission: workspaceEntityProbePermission,
				Resource: mc.Resource, Route: workspaceEntityProbeMetadata().RouteMetadata,
			}
			if !mc.Authorization.VerifyFor(time.Now(), question) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"tenant": mc.Tenant.String(), "workspace": mc.Resource.WorkspaceID.String(), "id": mc.Resource.ID,
			})
		})
}

// The resolver under test is the engine's production resolver. The route's
// principal, AAL3 elevation, scoped grants and witness all come from the real
// authenticated HTTP estate; no authority producer is replaced.

// requirePasskeyStepUpForTest turns on the strictest administrative step-up
// policy (passkey) for an estate, the behavior before the policy existed.
func requirePasskeyStepUpForTest(t *testing.T, st store.Store, a *auth.Authenticator) {
	t.Helper()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.AuthPolicy().Create(ctx, model.AuthPolicy{AdminStepUp: auth.StepUpPasskey})
		return err
	}); err != nil {
		t.Fatalf("require passkey step-up: %v", err)
	}
	a.ReloadStepUp()
}
