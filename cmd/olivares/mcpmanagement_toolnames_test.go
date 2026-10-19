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

// Two enabled servers that expose the same tool name reach a new session as two
// distinct, server-qualified tools; each alias runs on its own server. A second
// server with a held name is refused (409) and never replaces the first. Every
// driver reaches the tools through the one session endpoint, so what this test
// reads from a real session is what each driver sees.
func TestManagedMCPSameToolNameOnTwoServersStaysQualified(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	folder, home, install := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	agent := filepath.Join(home, "agent.py")
	if err := os.WriteFile(agent, []byte("#!"+python+"\n"+productMCPCatalogueAgent), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)
	// Both servers expose hu_echo; only the answer tells them apart.
	scripts := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		scripts[name] = filepath.Join(install, name+"_mcp.py")
		body := strings.ReplaceAll(productMCPEchoServer, "hu-echo says: ", name+" says: ")
		if err := os.WriteFile(scripts[name], []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
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
	created := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "mcp-names@olivares.ai", "password": "fixture-password-2026!", "organization": "MCP tool names test"}, http.StatusCreated)
	login := do("POST", "/v1/auth/login", map[string]any{"email": "mcp-names@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ = login["token"].(string)
	organization, _ := created["organization"].(map[string]any)
	tenant, _ = organization["tenant_id"].(string)
	if admin == "" || tenant == "" {
		t.Fatal("setup did not give an administrator and an organization")
	}
	srv := httptest.NewServer(eng.api.Handler())
	t.Cleanup(srv.Close)
	cliRun := func(args ...string) (string, error) {
		t.Helper()
		full := append([]string{"mcp", args[0], "--server", srv.URL, "--token", admin, "--tenant", tenant}, args[1:]...)
		out, errb, err := execSessionCLI(t, nil, full...)
		return out + errb, err
	}
	cli := func(args ...string) string {
		t.Helper()
		out, err := cliRun(args...)
		if err != nil {
			t.Fatalf("olivares mcp %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	registered := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "Names folder"}, http.StatusCreated)
	profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "session_tools": []string{}, "session_permission_mode": "default", "display_name": "Faithful protocol stub"}, http.StatusCreated)

	cli("add", "alpha", "--", python, "-u", scripts["alpha"])
	cli("add", "beta", "--", python, "-u", scripts["beta"])
	roster := func() []any { return do("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)["servers"].([]any) }
	ids := map[string]string{}
	for _, s := range roster() {
		row := s.(map[string]any)
		ids[row["name"].(string)] = row["id"].(string)
	}
	// A held name is refused whatever its case. "ALPHA" passes the CLI's own
	// exact-name pre-check, so the engine refuses it and names the holder.
	for name, want := range map[string]string{"alpha": "alpha", "ALPHA": ids["alpha"]} {
		out, err := cliRun("add", name, "--", python, "-u", scripts["beta"])
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("add %q again = %v %q, want a refusal that names %q", name, err, out, want)
		}
	}
	after := roster()
	if len(after) != 2 || len(ids) != 2 || ids["alpha"] == "" || ids["beta"] == "" {
		t.Fatalf("roster after the refused duplicates = %v", after)
	}
	for _, s := range after {
		if row := s.(map[string]any); row["name"] == "alpha" && row["args"].([]any)[1] != scripts["alpha"] {
			t.Fatal("a refused duplicate replaced the first server")
		}
	}
	cli("enable", "alpha", "--allow", "hu_echo")
	cli("enable", "beta", "--allow", "hu_echo")

	type catalogue struct {
		Tools     []string `json:"tools"`
		Error     string   `json:"error"`
		AliasCall string   `json:"alias_call"`
	}
	session := func(message string) catalogue {
		t.Helper()
		result := filepath.Join(folder, "catalogue-result.json")
		_ = os.Remove(result)
		run := do("POST", "/v1/m/sessions/runs", map[string]any{"name": "Names session", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": registered["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
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
			t.Fatalf("the session did not return its catalogue: %v (%s)", err, out.Error)
		}
		return out
	}
	aliases := map[string]string{}
	for _, name := range session("List the catalogue.").Tools {
		if !strings.HasSuffix(name, "_hu_echo") {
			continue
		}
		for server, id := range ids {
			if name == managedToolAlias(id, "hu_echo") {
				aliases[server] = name
			}
		}
	}
	if len(aliases) != 2 || aliases["alpha"] == aliases["beta"] {
		t.Fatalf("the session lists %v for hu_echo; want one alias per server, each qualified by its own server", aliases)
	}
	for server, alias := range aliases {
		if got := session("Call managed " + alias + " " + ids[server]).AliasCall; got != server+" says: assigned" {
			t.Fatalf("%s's alias ran %q; an alias must reach its own server", server, got)
		}
	}
}
