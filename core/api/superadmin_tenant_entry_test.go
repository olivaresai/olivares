// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const superadminEntryPermission auth.Permission = "superadminentry:thing:write"

type superadminEntryModule struct {
	calls int
	meta  auth.RouteMetadata
	data  api.ModuleData
}

func (*superadminEntryModule) APINamespace() string { return "superadminentry" }
func (*superadminEntryModule) Permissions() []auth.Permission {
	return []auth.Permission{superadminEntryPermission, "superadminentry:agent:read"}
}
func (m *superadminEntryModule) UseData(data api.ModuleData) { m.data = data }
func (*superadminEntryModule) Actions() []auth.CedarAction {
	return []auth.CedarAction{"directory:admin"}
}
func (m *superadminEntryModule) APIRoutes(reg api.RouteRegistrar) {
	reg.(interface {
		HandlePolicy(string, string, auth.Permission, api.RouteMetadata, api.ModuleHandler)
	}).HandlePolicy("POST", "/enter", superadminEntryPermission,
		api.RouteMetadata{RouteMetadata: m.meta}, m.enter)
	reg.(interface {
		HandlePolicy(string, string, auth.Permission, api.RouteMetadata, api.ModuleHandler)
	}).HandlePolicy("GET", "/agents", "superadminentry:agent:read",
		api.RouteMetadata{RouteMetadata: auth.RouteMetadata{RBACMinimumRole: auth.RoleOwner}}, m.agents)
	reg.(api.CollectionScopeRouteRegistrar).WithCollectionScope(api.CollectionScopeRef{WorkspaceQueryParam: "workspace_id"}).(interface {
		HandlePolicy(string, string, auth.Permission, api.RouteMetadata, api.ModuleHandler)
	}).HandlePolicy("GET", "/scoped-agents", "superadminentry:agent:read",
		api.RouteMetadata{RouteMetadata: auth.RouteMetadata{RBACMinimumRole: auth.RoleOwner}}, m.agents)
}
func (m *superadminEntryModule) enter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	role, member := mc.Principal.RoleIn(mc.Tenant)
	if !member || role != auth.RoleOwner || mc.Principal.Superadmin && mc.Principal.SessionScope() != mc.Tenant ||
		!mc.Authorization.VerifyFor(time.Now(), auth.Request{Principal: mc.Principal,
			Permission: superadminEntryPermission, Tenant: mc.Tenant, Resource: mc.Resource,
			Route: m.meta}) {
		http.Error(w, "tenant owner authority missing", http.StatusInternalServerError)
		return
	}
	m.calls++
	w.WriteHeader(http.StatusNoContent)
}

func (m *superadminEntryModule) agents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	counts := make(map[string]int)
	query := model.Query{}
	if !mc.Resource.WorkspaceID.IsZero() {
		query.Filters = []model.Filter{{Column: "workspace_id", Op: model.OpEq, Value: mc.Resource.WorkspaceID.String()}}
	}
	for _, source := range []string{"request", "boot"} {
		read := func(sc store.Scope) error {
			agents, _, err := sc.Agents().List(r.Context(), query)
			counts[source] = len(agents)
			return err
		}
		var err error
		if source == "request" {
			err = mc.Data.View(r.Context(), read)
		} else {
			err = m.data.View(r.Context(), mc.Tenant, read)
		}
		if err != nil {
			http.Error(w, "view unavailable", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(counts)
}

func TestSuperadminTenantOwnerKeepsConfinedHTTPViews(t *testing.T) {
	m := &superadminEntryModule{meta: auth.RouteMetadata{RBACMinimumRole: auth.RoleOwner}}
	policy := &decisionHorizonPolicy{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
		policy.store = o.Store
		o.Authorizer = auth.NewAuthorizer(policy)
	}, m)
	m.UseData(api.NewModuleData(h.st))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "confined-superadmin-entry")
	var own, foreign model.ID
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		for _, name := range []string{"own", "foreign"} {
			workspace, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: name, Slug: name, Status: model.StatusActive})
			if err != nil {
				return err
			}
			if _, err := sc.Agents().Create(t.Context(), model.Agent{Name: name, Kind: "test", ExternalID: name, WorkspaceID: workspace.ID}); err != nil {
				return err
			}
			if name == "own" {
				own = workspace.ID
			} else {
				foreign = workspace.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actor, err := h.authr.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	user, err := h.authr.CreateUser(t.Context(), actor, auth.NewUser{Email: "confined-superadmin@entry.test",
		Password: "confined-entry1", Tenant: tenant, Role: auth.RoleViewer, WorkspaceID: own})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthMutate(t.Context(), func(sc store.AuthScope) error {
		row, err := sc.Users().Get(t.Context(), user.ID)
		if err != nil {
			return err
		}
		row.IsSuperadmin = true
		_, err = sc.Users().Update(t.Context(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, _, err := h.authr.Login(t.Context(), "confined-superadmin@entry.test", "confined-entry1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/m/superadminentry/agents", "/v1/m/superadminentry/scoped-agents?workspace_id=" + own.String()} {
		reply := h.do("GET", path, token, nil, tenantHdr(tenant))
		if reply.code != http.StatusOK || reply.body["request"] != float64(1) || reply.body["boot"] != float64(1) {
			t.Fatalf("confined module views: status=%d counts=%v", reply.code, reply.body)
		}
	}
	// Collection admission uses one generic refusal for absent or crossed scope.
	foreignReply := h.do("GET", "/v1/m/superadminentry/scoped-agents?workspace_id="+foreign.String(), token, nil, tenantHdr(tenant))
	absentReply := h.do("GET", "/v1/m/superadminentry/scoped-agents?workspace_id="+model.NewID().String(), token, nil, tenantHdr(tenant))
	if foreignReply.code != http.StatusForbidden || absentReply.code != http.StatusForbidden || foreignReply.raw != absentReply.raw {
		t.Fatalf("collection admission exposed a foreign or absent scope: foreign=%d absent=%d", foreignReply.code, absentReply.code)
	}
	if reply := h.do("GET", "/v1/workspaces", token, nil, tenantHdr(tenant)); reply.code != http.StatusOK || len(reply.body["items"].([]any)) != 1 {
		t.Fatalf("workspace list escaped confinement: status=%d", reply.code)
	}
	for _, suffix := range []string{"", "/summary"} {
		if reply := h.do("GET", "/v1/workspaces/"+own.String()+suffix, token, nil, tenantHdr(tenant)); reply.code != http.StatusOK {
			t.Fatalf("own workspace metadata: status=%d, want 200", reply.code)
		}
		if reply := h.do("GET", "/v1/workspaces/"+foreign.String()+suffix, token, nil, tenantHdr(tenant)); reply.code != http.StatusNotFound {
			t.Fatalf("foreign workspace metadata: status=%d, want 404", reply.code)
		}
	}
	reply := h.do("POST", "/v1/auth/capabilities", token, map[string]any{"schema_version": 2,
		"questions": []map[string]any{
			{"id": "own", "kind": "surface", "operation": "GET /v1/m/superadminentry/scoped-agents", "workspace_id": own.String()},
			{"id": "foreign", "kind": "surface", "operation": "GET /v1/m/superadminentry/scoped-agents", "workspace_id": foreign.String()},
		}}, tenantHdr(tenant))
	if reply.code != http.StatusOK {
		t.Fatalf("workspace capability projection: status=%d", reply.code)
	}
	for _, result := range reply.body["results"].([]any) {
		row := result.(map[string]any)
		want := "reachable"
		if row["id"] == "foreign" {
			want = "not_reachable"
		}
		if row["state"] != want {
			t.Fatalf("workspace capability %v state=%v, want %s", row["id"], row["state"], want)
		}
	}
}

func TestSuperadminExplicitTenantReachesGovernedOwnerRoute(t *testing.T) {
	m := &superadminEntryModule{meta: auth.RouteMetadata{RBACMinimumRole: auth.RoleOwner}}
	policy := &decisionHorizonPolicy{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
		policy.store = o.Store
		o.Authorizer = auth.NewAuthorizer(policy)
	}, m)
	admin := h.adminLogin()
	tenants := []model.TenantID{h.createOrg(admin, "superadmin-entry-one"), h.createOrg(admin, "superadmin-entry-two")}
	principal, err := h.authr.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		r := h.do("POST", "/v1/m/superadminentry/enter", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusNoContent {
			t.Fatalf("governed explicit tenant entry: %d %s", r.code, r.raw)
		}
		found := false
		if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
			return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 0, func(event model.AuditEvent, canonical string, _ []byte) error {
				if event.Action == "auth.superadmin.tenant_entry" || event.Action == "auth.superadmin.tenant_entry.test" {
					var meta map[string]any
					if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
						return err
					}
					t.Logf("entry audit: action=%s actorMatches=%t kind=%s role=%v superadmin=%v", event.Action, event.Actor == principal.Actor(), event.ActorKind, meta["role"], meta["superadmin"])
					if event.Action != "auth.superadmin.tenant_entry" {
						return nil
					}
					found = event.Actor == principal.Actor() && event.ActorKind == model.ActorUser &&
						meta["superadmin"] == true && meta["role"] == auth.RoleOwner
				}
				return nil
			})
		}); err != nil || !found {
			t.Fatalf("superadmin attribution was lost: found=%t err=%v", found, err)
		}
	}
	for _, header := range []map[string]string{nil, tenantHdr(model.SystemTenantID)} {
		if r := h.do("POST", "/v1/m/superadminentry/enter", admin, nil, header); r.code != http.StatusBadRequest {
			t.Fatalf("missing/system tenant: %d %s", r.code, r.raw)
		}
	}
	if m.calls != 2 {
		t.Fatalf("invalid tenant reached the handler: calls=%d", m.calls)
	}
}

func TestTenantOwnerImplicitScopedGrantIsAuditedOnGovernedRoute(t *testing.T) {
	m := &superadminEntryModule{meta: auth.RouteMetadata{RequireScopedGrant: true, CedarAction: "directory:admin"}}
	policy := &decisionHorizonPolicy{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
		policy.store = o.Store
		o.Authorizer = auth.NewAuthorizer(policy)
	}, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "owner-scoped-entry")
	for _, role := range []string{auth.RoleOwner, auth.RoleAdmin} {
		created := h.do("POST", "/v1/users", admin, map[string]any{
			"email": role + "@owner-entry.test", "password": "owner-entry1", "tenant": tenant.String(), "role": role,
		}, nil)
		if created.code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", role, created.code, created.raw)
		}
		login := h.do("POST", "/v1/auth/login", "", map[string]any{
			"email": role + "@owner-entry.test", "password": "owner-entry1",
		}, nil)
		if login.code != http.StatusOK {
			t.Fatalf("login %s: %d %s", role, login.code, login.raw)
		}
		token := login.body["token"].(string)
		reply := h.do("POST", "/v1/m/superadminentry/enter", token, nil, tenantHdr(tenant))
		want := http.StatusNoContent
		if role != auth.RoleOwner {
			want = http.StatusForbidden
		}
		if reply.code != want {
			t.Fatalf("governed %s: %d %s, want %d", role, reply.code, reply.raw, want)
		}
	}
	if reply := h.do("POST", "/v1/m/superadminentry/enter", admin, nil, tenantHdr(tenant)); reply.code != http.StatusNoContent {
		t.Fatalf("superadmin implicit scoped grant: %d %s", reply.code, reply.raw)
	}
	var owner, superadmin int
	err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 0, func(event model.AuditEvent, canonical string, _ []byte) error {
			if event.Action != "auth.owner_scoped_grant" {
				return nil
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			if event.ActorKind != model.ActorUser || meta["permission"] != string(superadminEntryPermission) || meta["cedar_action"] != "directory:admin" || meta["grant_source"] != "tenant_owner" {
				t.Fatalf("implicit grant audit: event=%+v meta=%+v", event, meta)
			}
			if meta["superadmin"] == true {
				superadmin++
			} else {
				owner++
			}
			return nil
		})
	})
	if err != nil || owner != 1 || superadmin != 1 || m.calls != 2 {
		t.Fatalf("implicit grant audits owner=%d superadmin=%d calls=%d err=%v", owner, superadmin, m.calls, err)
	}
}

type ownerAuditFailureStore struct {
	store.Store
	refuse bool
}

func (s *ownerAuditFailureStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if s.refuse {
		return errors.New("fixture audit unavailable")
	}
	return s.Store.Mutate(ctx, tenant, fn)
}

func TestTenantOwnerImplicitGrantAuditFailureRefusesBeforeHandler(t *testing.T) {
	m := &superadminEntryModule{meta: auth.RouteMetadata{RequireScopedGrant: true, CedarAction: "directory:admin"}}
	policy := &decisionHorizonPolicy{}
	var failing *ownerAuditFailureStore
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
		policy.store = o.Store
		o.Authorizer = auth.NewAuthorizer(policy)
		failing = &ownerAuditFailureStore{Store: o.Store}
		o.Store = failing
	}, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "owner-audit-failure")
	failing.refuse = true
	reply := h.do("POST", "/v1/m/superadminentry/enter", admin, nil, tenantHdr(tenant))
	if reply.code != http.StatusServiceUnavailable || m.calls != 0 {
		t.Fatalf("unaudited implicit grant reached handler: %d %s calls=%d", reply.code, reply.raw, m.calls)
	}
}
