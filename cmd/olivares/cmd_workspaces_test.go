// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// workspacesFixtureServer answers the three reads `olivares workspaces` makes:
// the tenant's workspace list, one workspace, and its summary. It records which
// paths were called and asserts the caller's credential and tenant header
// arrive — the verbs are thin clients, so the test's job is to prove the wire,
// the rendering and the exit codes, not the engine's decisions.
func workspacesFixtureServer(t *testing.T, bearer string, called *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != bearer {
			t.Errorf("Authorization bearer = %q, want the caller's credential", got)
		}
		*called = append(*called, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/workspaces":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "ws-1", "tenant_id": "tenant-a", "name": "Default", "slug": "default",
						"status": "active", "is_default": true, "created_at": "2026-10-01T00:00:00Z",
						"updated_at": "2026-10-01T00:00:00Z", "version": 1},
					{"id": "ws-2", "tenant_id": "tenant-a", "name": "Research", "slug": "research",
						"status": "active", "is_default": false, "created_at": "2026-10-02T00:00:00Z",
						"updated_at": "2026-10-02T00:00:00Z", "version": 3},
				},
				"has_more": false,
			})
		case "/v1/workspaces/ws-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "ws-2", "tenant_id": "tenant-a", "name": "Research", "slug": "research",
				"status": "active", "is_default": false, "created_at": "2026-10-02T00:00:00Z",
				"updated_at": "2026-10-03T00:00:00Z", "version": 3,
			})
		case "/v1/workspaces/ws-2/summary":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspace_id": "ws-2", "name": "Research", "slug": "research", "is_default": false,
				"agent_count": 2, "session_count": 5, "resource_count": 0, "group_count": 1,
				"agent_count_capped": false, "session_count_capped": true,
				"resource_count_capped": false, "group_count_capped": false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestWorkspacesListRendersRows(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	out, stderr, err := execRoot(t, "workspaces", "ls", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("workspaces ls: %v (stderr: %s)", err, stderr)
	}
	if len(called) != 1 || called[0] != "GET /v1/workspaces" {
		t.Fatalf("calls = %v, want exactly GET /v1/workspaces", called)
	}
	for _, want := range []string{"ws-1", "Default", "default", "ws-2", "Research", "research", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output misses %q:\n%s", want, out)
		}
	}
}

func TestWorkspacesListJSONPassesThePageThrough(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	out, _, err := execRoot(t, "workspaces", "list", "--server", srv.URL, "--tenant", "tenant-a", "-o", "json")
	if err != nil {
		t.Fatalf("workspaces list -o json: %v", err)
	}
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("list -o json is not the list page: %v\n%s", err, out)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "ws-1" || page.Items[1].Slug != "research" {
		t.Fatalf("list -o json items = %+v, want the two fixture rows", page.Items)
	}
}

func TestWorkspacesListDeclaresTruncation(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One page of a longer list: a truncated listing that looks complete is
		// how an operator concludes a workspace does not exist (cmd_tokens.go:405).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "ws-1", "tenant_id": "tenant-a", "name": "Default", "slug": "default",
					"status": "active", "is_default": true, "created_at": "2026-10-01T00:00:00Z",
					"updated_at": "2026-10-01T00:00:00Z", "version": 1},
			},
			"cursor": "cur-9", "has_more": true,
		})
	}))
	defer srv.Close()

	out, _, err := execRoot(t, "workspaces", "ls", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("workspaces ls on a truncated page: %v", err)
	}
	if !strings.Contains(out, "more rows remain") || !strings.Contains(out, "cur-9") {
		t.Fatalf("a truncated page must say so and hand the cursor:\n%s", out)
	}
}

func TestWorkspacesGetReadsTheContentsRead(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	out, stderr, err := execRoot(t, "workspaces", "get", "ws-2", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("workspaces get: %v (stderr: %s)", err, stderr)
	}
	// The show verb's contents come from the workspace's one contents read,
	// published as the summary route — not from a client-side stitch.
	wantCalls := []string{"GET /v1/workspaces/ws-2", "GET /v1/workspaces/ws-2/summary"}
	if strings.Join(called, ";") != strings.Join(wantCalls, ";") {
		t.Fatalf("calls = %v, want %v", called, wantCalls)
	}
	for _, want := range []string{"ws-2", "Research", "research", "active", "UPDATED", "VERSION"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output misses %q:\n%s", want, out)
		}
	}
	// The four published counts, each anchored to its own rendered row — a bare
	// digit would be satisfied by the id or the timestamps — with the capped one
	// marked as a floor and the legend explaining the mark.
	for _, want := range []string{`(?m)^AGENTS\s+2$`, `(?m)^SESSIONS\s+5\+$`, `(?m)^RESOURCES\s+0$`, `(?m)^AGENT GROUPS\s+1$`} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("get output misses row %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "+ marks a floor") {
		t.Errorf("a capped count must carry its legend:\n%s", out)
	}
}

func TestWorkspacesShowAliasReadsTheContentsRead(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	_, _, err := execRoot(t, "workspace", "show", "ws-2", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("workspace show alias: %v", err)
	}
	if len(called) != 2 {
		t.Fatalf("calls = %v, want the workspace and summary reads", called)
	}
}

func TestWorkspacesGetJSONCarriesBothReads(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	out, _, err := execRoot(t, "workspaces", "get", "ws-2", "--server", srv.URL, "--tenant", "tenant-a", "-o", "json")
	if err != nil {
		t.Fatalf("workspaces get -o json: %v", err)
	}
	var doc struct {
		Workspace struct {
			ID      string `json:"id"`
			Slug    string `json:"slug"`
			Version int64  `json:"version"`
		} `json:"workspace"`
		Summary struct {
			WorkspaceID         string `json:"workspace_id"`
			SessionCount        int    `json:"session_count"`
			SessionCountCapped  bool   `json:"session_count_capped"`
			AgentCount          int    `json:"agent_count"`
			ResourceCountCapped bool   `json:"resource_count_capped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("get -o json does not decode: %v\n%s", err, out)
	}
	if doc.Workspace.ID != "ws-2" || doc.Workspace.Version != 3 {
		t.Fatalf("workspace half = %+v, want the fixture workspace", doc.Workspace)
	}
	if doc.Summary.WorkspaceID != "ws-2" || doc.Summary.AgentCount != 2 ||
		doc.Summary.SessionCount != 5 || !doc.Summary.SessionCountCapped || doc.Summary.ResourceCountCapped {
		t.Fatalf("summary half = %+v, want the fixture contents counts", doc.Summary)
	}
}

func TestWorkspacesGetUnknownIsExit4(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var called []string
	srv := workspacesFixtureServer(t, bearer, &called)
	defer srv.Close()

	_, _, err := execRoot(t, "workspaces", "get", "ws-nope", "--server", srv.URL, "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("unknown workspace exited %d, want %d (not found): %v", exitcode.From(err), exitcode.NotFound, err)
	}
}

func TestWorkspacesGetNeverPrintsTheBearer(t *testing.T) {
	const bearer = "fixture-workspaces-secret-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A broken plane reflecting the request's credential into its refusal,
		// the case redactCoded exists for (bootstrapclient.go).
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "refused for " + bearer}})
	}))
	defer srv.Close()

	out, stderr, err := execRoot(t, "workspaces", "get", "ws-2", "--server", srv.URL, "--tenant", "tenant-a")
	if exitcode.From(err) != exitcode.Auth {
		t.Fatalf("HTTP 403 exited %d, want %d (auth): %v", exitcode.From(err), exitcode.Auth, err)
	}
	if strings.Contains(out+stderr+err.Error(), bearer) {
		t.Fatal("a reflected bearer reached CLI output")
	}
}

func TestWorkspacesListCarriesTheTenantHeader(t *testing.T) {
	const bearer = "fixture-workspaces-bearer"
	t.Setenv("OLIVARES_TOKEN", bearer)
	var tenantHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantHeader = r.Header.Get("X-Olivares-Tenant")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}, "has_more": false})
	}))
	defer srv.Close()

	out, _, err := execRoot(t, "workspaces", "ls", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("workspaces ls on an empty page: %v", err)
	}
	if tenantHeader != "tenant-a" {
		t.Fatalf("X-Olivares-Tenant = %q, want tenant-a", tenantHeader)
	}
	if !strings.Contains(out, "no workspaces exist yet") {
		t.Fatalf("empty page must say so:\n%s", out)
	}
}

// A department stored under another shows its parent in `get`, in every build.
func TestWorkspacesGetShowsTheParent(t *testing.T) {
	t.Setenv("OLIVARES_TOKEN", "fixture-tree-bearer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/summary") {
			_, _ = io.WriteString(w, `{"workspace_id":"ws-emea"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"ws-emea","name":"EMEA","slug":"emea","status":"active","parent_id":"ws-sales"}`)
	}))
	defer srv.Close()
	out, _, err := execRoot(t, "workspaces", "get", "ws-emea", "--server", srv.URL, "--tenant", "tenant-a")
	if err != nil || !regexpParentRow.MatchString(out) {
		t.Fatalf("workspaces get = %v:\n%s", err, out)
	}
}

var regexpParentRow = regexp.MustCompile(`(?m)^\s*PARENT\s+ws-sales\s*$`)
