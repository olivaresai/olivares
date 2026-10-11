// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A memory entry the user writes for a workspace reaches the sessions of that
// workspace through the session MCP and no session of another workspace. The
// tools are the knowledge module's own memory routes with the launcher's rights;
// the workspace is pinned by the server, so a session cannot name another one.
func TestSessionMemoryToolsStayInTheSessionWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	if err := saveNodeModuleSelection(dataDir, append(standardModuleSelection(), "knowledge"), time.Now()); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: dataDir, Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ctx := t.Context()
	console := func(token, tenant, method, path string, body any) (int, map[string]any) {
		t.Helper()
		code, out, _ := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant, body)
		return code, out
	}
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, setup := console("", "", "POST", "/v1/setup", map[string]any{"token": setupToken, "email": "memory@olivares.ai", "password": "fixture-password-2026!", "organization": "Memory tools"})
	organization, _ := setup["organization"].(map[string]any)
	tenantID, _ := organization["tenant_id"].(string)
	tenant, err := model.ParseTenantID(tenantID)
	if code != http.StatusCreated || err != nil {
		t.Fatalf("setup = %d %v", code, setup)
	}
	code, login := console("", "", "POST", "/v1/auth/login", map[string]any{"email": "memory@olivares.ai", "password": "fixture-password-2026!"})
	adminToken, _ := login["token"].(string)
	if code != http.StatusOK || adminToken == "" {
		t.Fatalf("login = %d", code)
	}
	admin, err := eng.authr.Authenticate(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := eng.authr.IssueToken(ctx, admin, auth.TokenSpec{Name: "memory viewer", BoundTenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := eng.authr.Authenticate(ctx, viewerToken)
	if err != nil {
		t.Fatal(err)
	}
	var home, other model.Workspace
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) (err error) {
		if home, err = sc.DefaultWorkspace(ctx); err != nil {
			return err
		}
		other, err = sc.Workspaces().Create(ctx, model.Workspace{Name: "Other", Slug: "other", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The user writes one fact for each workspace, as the console does.
	for ws, content := range map[string]string{home.Slug: "Build with task build:go", other.Slug: "Other workspace fact"} {
		code, body := console(adminToken, tenantID, "POST", "/v1/m/knowledge/memory", map[string]any{"agent_ref": "workspace:" + ws, "key": "build", "content": content})
		if code != http.StatusOK {
			t.Fatalf("console memory write for %s = %d %v", ws, code, body)
		}
	}
	// An entry the user scoped to one person stays out of the shared workspace memory.
	if code, body := console(adminToken, tenantID, "POST", "/v1/m/knowledge/memory", map[string]any{"agent_ref": "workspace:" + home.Slug, "key": "build", "content": "Alice private fact", "user_ref": "alice"}); code != http.StatusOK {
		t.Fatalf("console scoped memory write = %d %v", code, body)
	}

	issuer := auth.NewSessionCredentials(eng.authr, func(context.Context, auth.SessionScope) error { return nil })
	mint := func(launcher auth.Principal, ws model.Workspace) string {
		t.Helper()
		bearer, err := issuer.Mint(ctx, launcher, auth.SessionScope{TenantID: tenant, WorkspaceID: ws.ID, FolderRef: "fixture",
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), Fence: 1})
		if err != nil {
			t.Fatal(err)
		}
		return bearer
	}
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: eng.admits(), configurer: turnlessConfigurer{eng}}
	rpc := func(bearer, method string, params any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "memory", "method": method, "params": params})
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s = %d %s", method, w.Code, w.Body.String())
		}
		return out
	}
	tool := func(bearer, name string, args any) (int, string) {
		t.Helper()
		out := rpc(bearer, "tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := out["result"].(map[string]any)
		structured, _ := result["structuredContent"].(map[string]any)
		status, ok := structured["http_status"].(float64)
		if !ok {
			t.Fatalf("%s returned no API answer: %v", name, out)
		}
		body, _ := json.Marshal(structured["body"])
		return int(status), string(body)
	}
	homeSession, otherSession, viewerSession := mint(admin, home), mint(admin, other), mint(viewer, home)

	// The inputs exclude what the server pins and the namespaces a session never names.
	var catalogue struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	raw, _ := json.Marshal(rpc(homeSession, "tools/list", map[string]any{}))
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"olivares_memory_list": nil, "olivares_memory_add": {"key"}}
	for _, listed := range catalogue.Result.Tools {
		required, ok := want[listed.Name]
		if !ok {
			continue
		}
		delete(want, listed.Name)
		if !slices.Equal(listed.InputSchema.Required, required) {
			t.Errorf("%s requires %v, want %v", listed.Name, listed.InputSchema.Required, required)
		}
		for _, field := range []string{"agent_ref", "user_ref", "session_ref"} {
			if listed.InputSchema.Properties[field] != nil {
				t.Errorf("%s lists %s as an input", listed.Name, field)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("tools/list lacks %v: %s", want, raw)
	}

	// Each session reads its own workspace's fact and never the other's.
	for _, tc := range []struct {
		name, bearer, sees, never string
	}{
		{"home session", homeSession, "Build with task build:go", "Other workspace fact"},
		{"other session", otherSession, "Other workspace fact", "Build with task build:go"},
		{"home viewer session", viewerSession, "Build with task build:go", "Other workspace fact"},
	} {
		code, body := tool(tc.bearer, "olivares_memory_list", nil)
		if code != http.StatusOK || !strings.Contains(body, tc.sees) || strings.Contains(body, tc.never) || strings.Contains(body, "Alice private fact") {
			t.Errorf("%s memory list = %d %s; want %q and neither %q nor the user-scoped entry", tc.name, code, body, tc.sees, tc.never)
		}
	}
	// A session cannot name another workspace or namespace, in any spelling the
	// route's case-folding decoder would accept.
	for _, field := range []string{"agent_ref", "Agent_Ref", "user_ref", "session_ref"} {
		for name, args := range map[string]map[string]any{
			"olivares_memory_list": {field: "workspace:" + other.Slug},
			"olivares_memory_add":  {"key": "leak", "content": "x", field: "workspace:" + other.Slug},
		} {
			out := rpc(homeSession, "tools/call", map[string]any{"name": name, "arguments": args})
			rpcErr, _ := out["error"].(map[string]any)
			if message, _ := rpcErr["message"].(string); !strings.HasPrefix(message, field+" is not an argument") {
				t.Errorf("%s with %s was not refused: %v", name, field, out)
			}
		}
	}

	// A session adds to its own workspace's memory where its user may write: the
	// console lists the entry under that workspace and the other workspace never sees it.
	if code, body := tool(homeSession, "olivares_memory_add", map[string]any{"key": "lint", "content": "Lint with task lint:spdx"}); code != http.StatusOK || !strings.Contains(body, `"agent_ref":"workspace:`+home.Slug+`"`) {
		t.Fatalf("admin session memory add = %d %s", code, body)
	}
	code, rows := console(adminToken, tenantID, "GET", "/v1/m/knowledge/memory?agent_ref=workspace:"+home.Slug, nil)
	if listedRows, _ := json.Marshal(rows); code != http.StatusOK || !strings.Contains(string(listedRows), "Lint with task lint:spdx") {
		t.Errorf("the console does not list the session's entry: %d %s", code, listedRows)
	}
	if _, body := tool(otherSession, "olivares_memory_list", nil); strings.Contains(body, "Lint with task lint:spdx") {
		t.Errorf("another workspace's session reads the entry: %s", body)
	}
	consoleCode, consoleBody := console(viewerToken, tenantID, "POST", "/v1/m/knowledge/memory", map[string]any{"agent_ref": "workspace:" + home.Slug, "key": "viewer", "content": "x"})
	toolCode, toolBody := tool(viewerSession, "olivares_memory_add", map[string]any{"key": "viewer", "content": "x"})
	consoleError, _ := json.Marshal(consoleBody["error"])
	if consoleCode != http.StatusForbidden || toolCode != consoleCode || !strings.Contains(toolBody, string(consoleError)) {
		t.Errorf("viewer memory add: session %d %s, console %d %s", toolCode, toolBody, consoleCode, consoleError)
	}
}

// A node that does not run a module answers its routes module_not_enabled, so its
// session MCP offers no tool on them: here knowledge, skills and sourcescope.
func TestSessionToolsSkipModulesTheNodeDoesNotRun(t *testing.T) {
	eng, _, adminToken, tenantID := bootWithModuleProfile(t, standardModuleSelection())
	ctx := t.Context()
	admin, err := eng.authr.Authenticate(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := model.ParseTenantID(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.Workspace
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) (err error) {
		workspace, err = sc.DefaultWorkspace(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(eng.authr, func(context.Context, auth.SessionScope) error { return nil })
	bearer, err := issuer.Mint(ctx, admin, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace.ID, FolderRef: "fixture",
		SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), Fence: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionMCPHandler{authr: issuer, issuedSessionOnly: true, admits: eng.admits(), configurer: turnlessConfigurer{eng}}
	r := httptest.NewRequest(http.MethodPost, "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("tools/list = %d %s", w.Code, w.Body.String())
	}
	var names []string
	for _, tool := range out.Result.Tools {
		names = append(names, tool.Name)
	}
	for _, absent := range []string{"olivares_memory_list", "olivares_memory_add", "olivares_skill_assign", "olivares_connector_add"} {
		if slices.Contains(names, absent) {
			t.Errorf("tools/list offers %s on a node without its module: %v", absent, names)
		}
	}
	if !slices.Contains(names, "olivares_folder_add") {
		t.Errorf("tools/list lacks olivares_folder_add, whose sessions module always runs: %v", names)
	}
}
