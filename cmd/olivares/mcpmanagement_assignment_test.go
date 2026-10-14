// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An MCP server assigned with `olivares mcp assign` reaches only the sessions
// whose run is under its target in the skills assignment tree: a folder, a
// workspace (department). New sessions elsewhere do not list its tools, and
// unassigning the last target serves every session again.
func TestManagedMCPAssignmentReachesOnlyItsTargets(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	folderA, folderB, home, install := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// The stub lives in the profile home, which sessions in both folders may read.
	agent := filepath.Join(home, "agent.py")
	if err := os.WriteFile(agent, []byte("#!"+python+"\n"+productMCPCatalogueAgent), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)
	script := filepath.Join(install, "hu_echo_mcp.py")
	if err := os.WriteFile(script, []byte(productMCPEchoServer), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	pep, err := buildClaudeHookPEPServer(eng, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pep.Close() })
	if err := eng.hookCredentials().bindEndpoint(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = pep.Serve(listener) }()
	var admin, tenant string
	do := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		if code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, raw)
		}
		return result
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	created := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "mcp-assign@olivares.ai", "password": "fixture-password-2026!", "organization": "MCP assignment test"}, http.StatusCreated)
	login := do("POST", "/v1/auth/login", map[string]any{"email": "mcp-assign@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ = login["token"].(string)
	organization, _ := created["organization"].(map[string]any)
	tenant, _ = organization["tenant_id"].(string)
	if admin == "" || tenant == "" {
		t.Fatal("setup did not give an administrator and an organization")
	}
	srv := httptest.NewServer(eng.api.Handler())
	t.Cleanup(srv.Close)
	cli := func(args ...string) string {
		t.Helper()
		full := append([]string{"mcp", args[0], "--server", srv.URL, "--token", admin, "--tenant", tenant}, args[1:]...)
		out, errb, err := execSessionCLI(t, nil, full...)
		if err != nil {
			t.Fatalf("olivares mcp %s: %v\n%s%s", strings.Join(args, " "), err, out, errb)
		}
		return out
	}

	refA := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folderA, "name": "Folder A"}, http.StatusCreated)["workspace_ref"].(string)
	refB := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folderB, "name": "Folder B"}, http.StatusCreated)["workspace_ref"].(string)
	profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "session_tools": []string{}, "session_permission_mode": "default", "display_name": "Faithful protocol stub"}, http.StatusCreated)
	departments, _ := do("GET", "/v1/workspaces", nil, http.StatusOK)["items"].([]any)
	if len(departments) != 1 {
		t.Fatalf("a new organization has %d workspaces, want its default one", len(departments))
	}
	home0 := departments[0].(map[string]any)["id"].(string)
	other := do("POST", "/v1/workspaces", map[string]any{"name": "Elsewhere", "slug": "elsewhere"}, http.StatusCreated)["id"].(string)

	cli("add", "echo", "--", python, "-u", script)
	cli("enable", "echo", "--allow", "hu_echo")

	type catalogue struct {
		Tools        []string `json:"tools"`
		Error        string   `json:"error"`
		AliasCall    string   `json:"alias_call"`
		DirectStatus int      `json:"direct_status"`
	}
	session := func(folder, ref, message string) catalogue {
		t.Helper()
		result := filepath.Join(folder, "catalogue-result.json")
		_ = os.Remove(result)
		run := do("POST", "/v1/m/sessions/runs", map[string]any{"name": "Assignment session", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ref, "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
		runRef := run["run_ref"].(string)
		defer do("POST", "/v1/m/sessions/runs/"+runRef+"/stop", map[string]any{}, http.StatusOK)
		do("POST", "/v1/m/sessions/runs/"+runRef+"/input", map[string]any{"message": map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": message}}}, http.StatusAccepted)
		var raw []byte
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if raw, err = os.ReadFile(result); err == nil {
				break
			}
		}
		var out catalogue
		if err != nil || json.Unmarshal(raw, &out) != nil || out.Error != "" {
			t.Fatalf("the session in %s did not return its catalogue: %v (%s)", folder, err, out.Error)
		}
		return out
	}
	alias := ""
	listed := func(folder, ref string) bool {
		t.Helper()
		for _, name := range session(folder, ref, "List the catalogue.").Tools {
			if strings.HasSuffix(name, "_hu_echo") {
				alias = name
				return true
			}
		}
		return false
	}
	expect := func(step string, inA, inB bool) {
		t.Helper()
		if a, b := listed(folderA, refA), listed(folderB, refB); a != inA || b != inB {
			t.Fatalf("%s: hu_echo listed in folder A=%v B=%v, want A=%v B=%v", step, a, b, inA, inB)
		}
	}

	expect("unassigned", true, true)
	if out := cli("assign", "echo", "--folder", refA); !strings.Contains(out, "echo serves the sessions under: folder "+refA) {
		t.Fatalf("assign = %q", out)
	}
	expect("assigned to folder A", true, false)
	// Both call doors refuse the other folder: the alias a session may hold from
	// before, and the server's own ?server= endpoint.
	serverID := do("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)["servers"].([]any)[0].(map[string]any)["id"].(string)
	probe := "Call managed " + alias + " " + serverID
	if got := session(folderA, refA, probe); got.AliasCall != "hu-echo says: assigned" || got.DirectStatus != http.StatusOK {
		t.Fatalf("folder A could not call its server: %+v", got)
	}
	if got := session(folderB, refB, probe); got.AliasCall != "-32602" || got.DirectStatus != http.StatusNotFound {
		t.Fatalf("folder B reached a server assigned to folder A: %+v", got)
	}
	cli("unassign", "echo", "--folder", refA)
	cli("assign", "echo", "--workspace", other)
	expect("assigned to another workspace", false, false)
	cli("assign", "echo", "--workspace", home0)
	expect("assigned to their workspace too", true, true)
	cli("unassign", "echo", "--workspace", other)
	if out := cli("unassign", "echo", "--workspace", home0); out != "echo serves every session of the organization.\n" {
		t.Fatalf("last unassign = %q", out)
	}
	expect("unassigned again", true, true)
}
