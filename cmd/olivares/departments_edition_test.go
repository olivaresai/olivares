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
	"github.com/olivaresai/olivares/core/model"
)

// The Community binary wires no department service (wire_noenterprise.go): placing
// a department, filing a group in a workspace and nesting a group under another
// answer 501 departments_unavailable from the booted engine, and `olivares
// workspaces` has no tree verbs. The Business build serves them.
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
	tenant := createOrganizationFixture(t, eng.store, "departments", "departments").TenantID.String()
	sales := do("POST", "/v1/workspaces", root, tenant, map[string]any{"name": "Sales", "slug": "sales"}, 201)["id"].(string)
	emea := do("POST", "/v1/workspaces", root, tenant, map[string]any{"name": "EMEA", "slug": "emea"}, 201)["id"].(string)
	group := func(name string) string {
		g, err := eng.authr.SCIMCreateGroup(context.Background(), principalOfToken(t, eng.authr, root), model.TenantID(tenant), auth.SCIMGroupInput{DisplayName: name})
		if err != nil {
			t.Fatalf("create group %s: %v", name, err)
		}
		return g.Group.ID.String()
	}
	staff, team := group("Staff"), group("Team")

	for _, c := range []struct{ path, field, value string }{
		{"/v1/workspaces/" + emea + "/parent", "parent_id", sales},
		{"/v1/groups/" + team + "/workspace", "workspace_id", sales},
		{"/v1/groups/" + team + "/parent", "parent_id", staff},
	} {
		out := do("PUT", c.path, root, tenant, map[string]any{c.field: c.value}, http.StatusNotImplemented)
		if e, _ := out["error"].(map[string]any); e["code"] != "departments_unavailable" {
			t.Errorf("PUT %s = %v, want departments_unavailable", c.path, out)
		}
	}
	if got := do("GET", "/v1/workspaces/"+emea, root, tenant, nil, 200)["parent_id"]; got != nil {
		t.Errorf("a refused move set parent_id %v", got)
	}
	listed := false
	for _, g := range do("GET", "/v1/groups", root, tenant, nil, 200)["groups"].([]any) {
		if g := g.(map[string]any); g["id"] == team {
			listed = true
			if g["parent_group_id"] != "" || g["workspace_id"] != "" {
				t.Errorf("group %s = %v, want a refused nesting and a refused placement not stored", team, g)
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
