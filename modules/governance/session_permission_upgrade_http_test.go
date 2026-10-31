//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"fmt"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/sessions"
	"net/http"
	"testing"
)

// Old roles and forbids must keep their effect without editing stored source.
func TestStoredSessionPermissionsUpgradeHTTP(t *testing.T) {
	for _, onlyStop := range []bool{false, true} {
		for _, condition := range []string{`context.permission == "sessions:run:read" || context.permission == "sessions:run:write"`, `action == Action::"sessions:run:read" || action == Action::"sessions:run:write"`} {
			t.Run(fmt.Sprintf("stop-only=%v/%s", onlyStop, condition), func(t *testing.T) {
				m := sessions.New(sessions.WithSessionWorkspaceRoot(t.TempDir()))
				f := newHarnessWith(t, harnessOpts{registerExtra: m.RegisterSchema, extraModules: []api.Module{m}, dataConsumers: []api.DataConsumer{m}})
				if err := m.Init(t.Context(), f.host); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = m.Stop(context.Background()) })
				root := f.adminLogin()
				tenant := f.createOrg(root, "session-upgrade")
				_, viewer := f.roleUser(root, tenant, "old-role@upgrade.test", auth.RoleViewer)
				p, err := f.authr.Authenticate(t.Context(), viewer)
				if err != nil {
					t.Fatal(err)
				}
				records := map[model.Kind][]model.Record{
					"governance.permission_group": {{"name": "old-reads", "permissions": `["sessions:run:read"]`, "created_by": "upgrade-fixture"}},
					"governance.custom_role":      {{"name": "old-operator", "permissions": `["sessions:run:write"]`, "groups": `["old-reads"]`, "created_by": "upgrade-fixture"}},
					"governance.scoped_grant":     {{"subject_kind": "user", "subject_ref": p.UserID.String(), "grant_role": "old-operator", "role_custom": true, "scope_tree": "tenant", "scope_ref": "", "scope_class": "", "created_by": "upgrade-fixture"}},
				}
				if onlyStop {
					records["governance.custom_role"][0]["permissions"] = `["sessions:stop:write"]`
					records["governance.custom_role"][0]["groups"] = `[]`
				}
				if err := governance.SeedStoredAuthorizationForTest(t.Context(), f.st, tenant, records); err != nil {
					t.Fatal(err)
				}
				if err := f.gov.ReloadActivePDP(t.Context(), tenant); err != nil {
					t.Fatal(err)
				}
				ref := "old-session"
				if err := f.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(model.Kind("sessions.run"))
					if err != nil {
						return err
					}
					_, err = repo.Create(t.Context(), model.Record{"run_ref": ref, "name": "old session", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "state": "stopped", "last_event_seq": int64(0)})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				base := "/v1/m/sessions/runs/" + ref
				for _, actor := range []string{viewer, root} {
					for _, route := range []struct{ method, path string }{{"GET", "/events"}, {"POST", "/stop"}} {
						r := f.do(route.method, base+route.path, actor, nil, tenantHdr(tenant))
						want := http.StatusOK
						if onlyStop && actor == viewer && route.path == "/events" {
							want = http.StatusForbidden
						}
						if r.code != want {
							t.Errorf("retained role %s: %d %s", route.path, r.code, r.raw)
						}
					}
				}
				if onlyStop {
					for _, path := range []string{"input", "resume", "git/commit"} {
						if r := f.do("POST", base+"/"+path, viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
							t.Errorf("stop-only %s: %d %s", path, r.code, r.raw)
						}
					}
				}
				testsupport.SeedCedar(t, f.st, tenant, "forbid(principal, action, resource) when {"+condition+"};", f.gov)
				for _, actor := range []string{root} {
					for _, route := range []struct{ method, path string }{{"GET", "/events"}, {"POST", "/stop"}} {
						r := f.do(route.method, base+route.path, actor, nil, tenantHdr(tenant))
						// The old read forbid also hides metadata/discovery, so denial is concealed.
						if r.code != http.StatusNotFound {
							t.Errorf("retained forbid %s: %d %s", route.path, r.code, r.raw)
						}
					}
				}
			})
		}
	}
}

func TestScopedViewerListsWithoutSessionOutputHTTP(t *testing.T) {
	for _, role := range []string{auth.RoleViewer, "list-only", "trimmed"} {
		t.Run(role, func(t *testing.T) {
			m := sessions.New(sessions.WithSessionWorkspaceRoot(t.TempDir()))
			h := newHarnessWith(t, harnessOpts{registerExtra: m.RegisterSchema, extraModules: []api.Module{m}, dataConsumers: []api.DataConsumer{m}})
			if err := m.Init(t.Context(), h.host); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Stop(context.Background()) })
			root := h.adminLogin()
			tenant := h.createOrg(root, "scoped-viewer")
			_, token := h.roleUser(root, tenant, "viewer@split.test", auth.RoleViewer)
			p := h.principalOf(token)
			records := map[model.Kind][]model.Record{
				"governance.custom_role":  {{"name": role, "permissions": `["sessions:run:read"]`, "created_by": "test", "session_permissions_exact": true}},
				"governance.scoped_grant": {{"subject_kind": "user", "subject_ref": p.UserID.String(), "grant_role": role, "role_custom": role != auth.RoleViewer, "scope_tree": "tenant", "scope_ref": "", "scope_class": "", "created_by": "test"}},
			}
			if role == "trimmed" {
				records["governance.custom_role"][0]["base_role"] = auth.RoleEditor
				records["governance.custom_role"][0]["excludes"] = `["sessions:output:read"]`
			}
			if err := governance.SeedStoredAuthorizationForTest(t.Context(), h.st, tenant, records); err != nil {
				t.Fatal(err)
			}
			if err := h.gov.ReloadActivePDP(t.Context(), tenant); err != nil {
				t.Fatal(err)
			}
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.run")
				if err != nil {
					return err
				}
				_, err = repo.Create(t.Context(), model.Record{"run_ref": "listed", "name": "listed", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "state": "stopped", "last_event_seq": int64(0)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"events", "attach"} {
				r := h.do("GET", "/v1/m/sessions/runs/listed/"+path, token, nil, tenantHdr(tenant))
				if r.code != http.StatusForbidden {
					t.Errorf("%s=%d %s", path, r.code, r.raw)
				}
			}
			if r := h.do("GET", "/v1/m/sessions/runs", token, nil, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("list=%d %s", r.code, r.raw)
			}
		})
	}
}

func TestStoredABACSessionRestrictionsHTTP(t *testing.T) {
	for _, rule := range []map[string]any{
		{"deny": true, "permission": "sessions:run:read"},
		{"deny": true, "resource": "run", "principal_kind": "token"},
		{"deny": true, "permission": "sessions:run:write"},
		{"deny": true, "permission": "sessions:run:write", "min_aal": 3},
	} {
		t.Run(fmt.Sprint(rule), func(t *testing.T) {
			m := sessions.New(sessions.WithSessionWorkspaceRoot(t.TempDir()))
			h := newHarnessWith(t, harnessOpts{registerExtra: m.RegisterSchema, extraModules: []api.Module{m}, dataConsumers: []api.DataConsumer{m}})
			if err := m.Init(t.Context(), h.host); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Stop(context.Background()) })
			root := h.adminLogin()
			tenant := h.createOrg(root, "abac-http")
			issued := h.do("POST", "/v1/tokens", root, map[string]any{"name": "operator", "tenant": tenant.String(), "role": auth.RoleEditor}, nil)
			if issued.code != http.StatusCreated {
				t.Fatalf("issue=%d %s", issued.code, issued.raw)
			}
			token := issued.body["token"].(string)
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.run")
				if err != nil {
					return err
				}
				_, err = repo.Create(t.Context(), model.Record{"run_ref": "denied", "name": "denied", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "state": "stopped", "last_event_seq": int64(0)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			base := "/v1/m/sessions/runs/denied"
			path, method := "/events", "GET"
			if rule["permission"] == "sessions:run:write" {
				path, method = "/stop", "POST"
			}
			if r := h.do(method, base+path, token, nil, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("before=%d %s", r.code, r.raw)
			}
			h.authorPolicy(root, tenant, "legacy-deny", map[string]any{"rules": []any{rule}})
			r := h.do(method, base+path, token, nil, tenantHdr(tenant))
			if r.code != http.StatusForbidden && r.code != http.StatusNotFound {
				t.Fatalf("after=%d %s", r.code, r.raw)
			}
		})
	}
}
