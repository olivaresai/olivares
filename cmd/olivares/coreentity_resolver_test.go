// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
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
func TestCoreWorkspaceEntityScopedAuthorizationHTTP(t *testing.T) {
	e := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng := e.eng
	srv, err := api.New(api.Options{
		Store: eng.store, Authenticator: eng.authr, Authorizer: eng.authz, Signer: eng.signer,
		SetupToken: eng.setupTok, PrincipalEvidenceProducer: eng.authr,
		CoreEntityResolver: coreEntityResolver{st: eng.store}, Modules: []api.Module{workspaceEntityProbe{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string, tenant model.TenantID, workspace string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/m/workspaceprobe/workspaces/"+workspace+"/probe", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("tenant=%s workspace=%s: status=%d, want %d: %s", tenant, workspace, rec.Code, want, rec.Body.String())
		}
		if want == http.StatusOK {
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["workspace"] != workspace ||
				got["id"] != workspace || got["tenant"] != tenant.String() {
				t.Fatalf("handler did not receive exact stored workspace lineage and valid witness: %v, %v", got, err)
			}
		}
	}
	// An owner with AAL3 still cannot bypass RequireScopedGrant.
	call(e.owner.token, e.tenant, e.workspace.String(), http.StatusNotFound)
	capabilityPublishAuthored(t, eng, e, fmt.Sprintf(
		`permit(principal in User::%q, action == Action::%q, resource) when { resource in Workspace::"k3-admin-ws" };`+
			`permit(principal in User::%q, action == Action::%q, resource);`,
		e.viewer.id.String(), string(workspaceEntityProbeAction), e.steward.id.String(), string(workspaceEntityProbeAction)))
	// A real scoped grant does not bypass the route's assurance requirement.
	call(e.viewer.token, e.tenant, e.workspace.String(), http.StatusForbidden)
	stepUpCommunicationHTTPTestUser(t, eng, e.viewer.token)
	stepUpCommunicationHTTPTestUser(t, eng, e.steward.token)
	call(e.viewer.token, e.tenant, e.workspace.String(), http.StatusOK)
	call(e.viewer.token, e.tenant, e.sideWorkspace.String(), http.StatusNotFound)
	call(e.viewer.token, e.tenant, e.foreignWorkspace, http.StatusNotFound)
	// The principal producer refuses a tenant the session has no direct
	// membership in; it cannot supply that tenant's sealed authority evidence.
	call(e.viewer.token, e.other, e.foreignWorkspace, http.StatusServiceUnavailable)
	call(e.viewer.token, e.tenant, model.NewID().String(), http.StatusNotFound)
	// A tenant-wide authored grant remains valid for each real workspace.
	call(e.steward.token, e.tenant, e.workspace.String(), http.StatusOK)
	call(e.steward.token, e.tenant, e.sideWorkspace.String(), http.StatusOK)
	call(e.steward.token, e.tenant, model.NewID().String(), http.StatusNotFound)

	resolver := coreEntityResolver{st: eng.store}
	facts, err := resolver.ResolveCoreEntity(context.Background(), e.tenant, api.CoreKindWorkspace, e.workspace)
	if err != nil || !facts.Exists || facts.ID != e.workspace || facts.Tenant != e.tenant ||
		facts.WorkspaceID != facts.ID || !facts.AgentID.IsZero() {
		t.Fatalf("workspace facts = %+v, err %v", facts, err)
	}
	foreign, err := resolver.ResolveCoreEntity(context.Background(), e.other, api.CoreKindWorkspace, e.workspace)
	if err != nil || foreign.Exists {
		t.Fatalf("cross-tenant resolver exposed workspace: %+v, err %v", foreign, err)
	}
}
