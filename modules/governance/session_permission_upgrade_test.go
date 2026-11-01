// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSessionPermissionCompatibility(t *testing.T) {
	for _, pair := range [][2]string{{"sessions:output:read", "sessions:run:read"}, {"sessions:stop:write", "sessions:run:write"}} {
		for _, tc := range []struct {
			name, source string
			allow        bool
		}{
			{"new negative permit", `permit(principal, action, resource) when {context.permission != "` + pair[0] + `"};`, false},
			{"new wildcard forbid", `permit(principal, action, resource); forbid(principal, action, resource) when {context.permission like "` + pair[0][:len(pair[0])-4] + `*"};`, false},
			{"old permit", `permit(principal, action == Action::"` + pair[1] + `", resource);`, true},
			{"old negative permit", `permit(principal, action, resource) unless {context.permission == "` + pair[1] + `"};`, false},
			{"unrelated narrow literal must not widen a permit", `permit(principal, action, resource) unless {context.permission == "` + pair[1] + `" && resource.kind != "` + pair[0] + `"};`, false},
			{"unrelated narrow literal must not weaken a forbid", `permit(principal, action, resource); forbid(principal, action, resource) when {context.permission == "` + pair[1] + `" && resource.kind != "` + pair[0] + `"};`, false},
			{"wildcard permit", `permit(principal, action, resource) when {context.permission like "sessions:run:*"};`, true},
			{"wildcard forbid", `permit(principal, action, resource); forbid(principal, action, resource) when {context.permission like "sessions:run:*"};`, false},
			{"wildcard negative permit", `permit(principal, action, resource) unless {context.permission like "sessions:run:*"};`, false},
			{"old forbid wins", `permit(principal, action == Action::"` + pair[0] + `", resource); forbid(principal, action, resource) when {context.permission == "` + pair[1] + `"};`, false},
			{"new forbid wins", `permit(principal, action == Action::"` + pair[1] + `", resource); forbid(principal, action == Action::"` + pair[0] + `", resource);`, false},
			{"new permit", `permit(principal, action == Action::"` + pair[0] + `", resource);`, true},
			{"old forbid error", `permit(principal, action, resource); forbid(principal, action == Action::"` + pair[1] + `", resource) when {resource.missing};`, false},
		} {
			t.Run(pair[0]+"/"+tc.name, func(t *testing.T) {
				set, err := compileGrantSet(tc.source)
				if err != nil {
					t.Fatal(err)
				}
				req := authUpgradeRequest(pair[0])
				effect, _ := evalGrantBasic(set, req, time.Now())
				if got := effect == auth.EffectGrant; got != tc.allow {
					t.Fatalf("effect=%v, want allow=%v", effect, tc.allow)
				}
			})
		}
	}
}

func authUpgradeRequest(permission string) auth.Request {
	return auth.Request{Permission: auth.Permission(permission), Resource: auth.ResourceAttrs{Kind: "run", ID: "run-id"}}
}

func TestSessionSplitRetainedOverlayReplay(t *testing.T) {
	tenant := model.TenantID("tenant")
	p := auth.ScopedPrincipal("cred", "user", tenant, auth.RoleEditor)
	req := auth.Request{Principal: p, Tenant: tenant, Permission: "sessions:output:read", Resource: auth.ResourceAttrs{Kind: "run", ID: "run-id"}}
	evaluator, err := NewCedarEvaluator(`forbid(principal, action, resource) when {context.permission == "sessions:run:read"};`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var record auth.AuthorizationRecord
	ctx := auth.WithAuthorizationRecording(t.Context(), func(r auth.AuthorizationRecord) { record = r })
	if auth.NewAuthorizer(evaluator).Authorize(ctx, req).Allow {
		t.Fatal("old forbid did not restrict output")
	}
	if len(record.Snapshot.Policy) != 1 || record.Snapshot.Policy[0].Engine != "cedar-overlay-v2" {
		t.Fatalf("unversioned capture: %+v", record.Snapshot.Policy)
	}
	content, err := json.Marshal(record.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := evaluateRetainedAuthorization(string(content), tenant, retainedAuthorizationQuestion(record.Snapshot))
	if err != nil || got != auth.EvidenceDeny {
		t.Fatalf("replay=%v err=%v", got, err)
	}
}

func TestManagedStopPermissionCompatibility(t *testing.T) {
	set, err := compileGrantSet(`permit(principal, action, resource); forbid(principal, action, resource) when {context.permission == "sessions:run:write"};`)
	if err != nil {
		t.Fatal(err)
	}
	req := authUpgradeRequest("sessions:stop:write")
	req.Route.CedarAction = "run:stop"
	if effect, _ := evalGrantBasic(set, req, time.Now()); effect == auth.EffectGrant {
		t.Fatal("managed stop ignored legacy forbid")
	}
}

func TestManagedSessionPermissionsAreExact(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)
	if err := auth.RegisterModulePermissions([]auth.Permission{"sessions:run:read", "sessions:run:write", "sessions:output:read", "sessions:stop:write"}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{auth.RoleViewer, "list-only", "trimmed"} {
		roles := map[string]customRole{"list-only": {Perms: []string{"sessions:run:read"}}, "trimmed": {Base: auth.RoleEditor, Excludes: []string{"sessions:output:read"}}}
		source := projectManagedCedar([]scopedGrant{{SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: role, RoleCustom: role != auth.RoleViewer, Scope: scopeSpec{Tree: scopeTenant}}}, roles, nil)
		set, err := compileGrantSet(source)
		if err != nil {
			t.Fatal(err)
		}
		req := authUpgradeRequest("sessions:output:read")
		req.Tenant = "tenant"
		req.Principal = auth.ScopedPrincipal("cred", "user", req.Tenant, auth.RoleViewer)
		if effect, _ := evalGrantBasic(set, req, time.Now()); effect == auth.EffectGrant {
			t.Errorf("%s grants output", role)
		}
		req.Permission = "sessions:run:read"
		if effect, _ := evalGrantBasic(set, req, time.Now()); effect != auth.EffectGrant {
			t.Errorf("%s cannot list", role)
		}
	}
}

func TestStoredManagedSessionProjectionUpgrade(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)
	if err := auth.RegisterModulePermissions([]auth.Permission{"sessions:run:read", "sessions:run:write", "sessions:output:read", "sessions:stop:write"}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{auth.RoleViewer, "stored", "stored-class"} {
		roles := map[string]customRole{"stored": recToCustomRole(model.Record{"name": "stored", "permissions": `["sessions:run:read","sessions:run:write"]`})}
		roles["stored-class"] = roles["stored"]
		class := ""
		if role == "stored-class" {
			class = "sessions:run"
		}
		grants := []scopedGrant{{ID: "grant-id", SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: role, RoleCustom: role != auth.RoleViewer, Scope: scopeSpec{Tree: scopeTenant, Class: class}}}
		old := projectManagedCedarVersion(grants, roles, nil, false)
		source := managedProjectionWithGrantIDs(old, managedProjectionState{roles: roles, grants: grants})
		set, err := compileGrantSet(source)
		if err != nil {
			t.Fatal(err)
		}
		request := authUpgradeRequest("sessions:output:read")
		request.Tenant = "tenant"
		request.Principal = auth.ScopedPrincipal("cred", "test", request.Tenant, auth.RoleViewer)
		if effect, _ := evalGrantBasic(set, request, time.Now()); (effect == auth.EffectGrant) != (role != auth.RoleViewer) {
			t.Errorf("%s effect=%v", role, effect)
		}
		tampered := strings.ReplaceAll(old, "grant-id", "different-id")
		if got := managedProjectionWithGrantIDs(tampered, managedProjectionState{roles: roles, grants: grants}); got != tampered {
			t.Fatal("rewrote a mismatched stored grant ID")
		}
		// Stored policy text and revisions are immutable; divergent source is not reprojected.
		divergent := old + `forbid(principal, action, resource);`
		if got := managedProjectionWithGrantIDs(divergent, managedProjectionState{roles: roles, grants: grants}); got != divergent {
			t.Fatal("replaced a divergent managed revision")
		}
	}
}

func TestStoredSessionPermissionBundleVersions(t *testing.T) {
	for _, exact := range []bool{false, true} {
		row := roleRecord("list", "", "", "", []string{"sessions:run:read"}, nil, nil, "test")
		row[colRBACSessionExact] = exact
		role := recToCustomRole(row)
		hasOutput := false
		for _, p := range role.Perms {
			hasOutput = hasOutput || p == "sessions:output:read"
		}
		if hasOutput == exact {
			t.Fatalf("exact=%v permissions=%v", exact, role.Perms)
		}
		group := recToPermGroup(row)
		if len(group.Perms) != len(role.Perms) {
			t.Fatal("group and role upgrade differ")
		}
	}
}
