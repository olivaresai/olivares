// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// The Community binary wires no department service (wire_noenterprise.go): placing
// a department and filing a group in a workspace answer 501 departments_unavailable
// from the booted engine, and `olivares workspaces` has no tree verbs. The Business
// build serves them. Group nesting is published Community API and keeps working.
func TestCommunityBuildAnswers501ForDepartments(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{Engine: "sqlite", DSN: filepath.Join(dir, "core.db"), DataDir: dir, Version: "departments-test", Logger: discardLogger(), NoIngest: true, ServeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()
	do := func(method, path, token, tenant string, body any, want int) map[string]any {
		t.Helper()
		code, out, raw := doDemoViewJSON(t, h, method, path, token, tenant, body)
		if code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
		}
		return out
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	do("POST", "/v1/setup", "", "", map[string]any{"token": setup, "email": "root@departments.test", "password": "departments-root-password"}, 201)
	root := do("POST", "/v1/auth/login", "", "", map[string]any{"email": "root@departments.test", "password": "departments-root-password"}, 200)["token"].(string)
	tenant := do("POST", "/v1/system/orgs", root, "", map[string]any{"name": "departments", "slug": "departments"}, 201)["tenant_id"].(string)
	sales := do("POST", "/v1/workspaces", root, tenant, map[string]any{"name": "Sales", "slug": "sales"}, 201)["id"].(string)
	emea := do("POST", "/v1/workspaces", root, tenant, map[string]any{"name": "EMEA", "slug": "emea"}, 201)["id"].(string)
	group := func(name string) string {
		return do("POST", "/v1/scim/v2/Groups", root, tenant, map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": name}, 201)["id"].(string)
	}
	staff, team := group("Staff"), group("Team")

	for _, c := range []struct{ path, field, value string }{
		{"/v1/workspaces/" + emea + "/parent", "parent_id", sales},
		{"/v1/groups/" + team + "/workspace", "workspace_id", sales},
	} {
		out := do("PUT", c.path, root, tenant, map[string]any{c.field: c.value}, http.StatusNotImplemented)
		if e, _ := out["error"].(map[string]any); e["code"] != "departments_unavailable" {
			t.Errorf("PUT %s = %v, want departments_unavailable", c.path, out)
		}
	}
	if got := do("GET", "/v1/workspaces/"+emea, root, tenant, nil, 200)["parent_id"]; got != nil {
		t.Errorf("a refused move set parent_id %v", got)
	}
	if got := do("PUT", "/v1/groups/"+team+"/parent", root, tenant, map[string]any{"parent_id": staff}, 200)["parent_group_id"]; got != staff {
		t.Errorf("Community nesting answered parent_group_id %v, want %s", got, staff)
	}
	listed := false
	for _, g := range do("GET", "/v1/groups", root, tenant, nil, 200)["groups"].([]any) {
		if g := g.(map[string]any); g["id"] == team {
			listed = true
			if g["parent_group_id"] != staff || g["workspace_id"] != "" {
				t.Errorf("group %s = %v, want nested under %s and a refused placement not stored", team, g, staff)
			}
		}
	}
	if !listed {
		t.Errorf("GET /v1/groups does not list the group %s", team)
	}

	out, _, err := execRoot(t, "workspaces", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"tree", "set-parent", "place"} {
		if strings.Contains(out, "\n  "+verb+" ") {
			t.Errorf("`olivares workspaces` lists the Business verb %q:\n%s", verb, out)
		}
	}
}
