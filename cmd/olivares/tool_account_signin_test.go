// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// Two account homes must remain independent all the way through the real engine.
// This stub only reaches the vendor URL/code step; it authenticates no vendor.
func TestToolSignInUsesTheSelectedProviderAccount(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
				t.Setenv(name, "")
			}
			dir, bin := t.TempDir(), filepath.Join(t.TempDir(), "claude.py")
			stub := "#!" + python + "\nimport json, os, sys\n" +
				"home = os.environ['HOME']; config = os.environ['CLAUDE_CONFIG_DIR']\n" +
				"assert os.getcwd() == home\n" +
				"if sys.argv[1:3] == ['auth', 'status']:\n" +
				" print(json.dumps({'loggedIn': False, 'authMethod': 'none'}))\n" +
				"elif sys.argv[1:3] == ['auth', 'login']:\n" +
				" open(os.path.join(home, 'login-observed.txt'), 'w').write(home+'\\n'+config)\n" +
				" print('https://claude.com/cai/oauth/authorize?state=account-fixture', flush=True)\n" +
				" sys.stdin.readline()\n"
			if err := os.WriteFile(bin, []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(envSessionClaudeBin, bin)
			cfg := bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.Engine, cfg.DSN, cfg.OwnerDSN = "postgres", pg.App, pg.Owner
			}
			eng, err := boot(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			var token, tenant string
			call := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant, body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
				}
				return result
			}
			setup, _, err := eng.setupTok.Ensure()
			if err != nil {
				t.Fatal(err)
			}
			created := call("POST", "/v1/setup", map[string]any{"token": setup, "email": "acct@olivares.ai", "password": "fixture-password-2026!"}, 201)
			login := call("POST", "/v1/auth/login", map[string]any{"email": "acct@olivares.ai", "password": "fixture-password-2026!"}, 200)
			token = login["token"].(string)
			tenant = created["organization"].(map[string]any)["tenant_id"].(string)
			var refs, ids []string
			for _, name := range []string{"claude-1", "claude-b"} {
				account := call("POST", "/v1/m/sessions/provider-accounts", map[string]any{"driver": "claude", "name": name}, 201)
				ref := account["account_ref"].(string)
				refs = append(refs, ref)
				statusPath := "/v1/m/agenttools/sign-in?driver=claude&tenant_id=" + url.QueryEscape(tenant) + "&account_ref=" + url.QueryEscape(ref)
				call("GET", statusPath, nil, 200)
				flow := call("POST", "/v1/m/agenttools/sign-in", map[string]any{"driver": "claude", "tenant_id": tenant, "account_ref": ref}, 202)
				if flow["state"] != "needs_code" || flow["account_ref"] != ref {
					t.Fatalf("flow = %v", flow)
				}
				ids = append(ids, flow["id"].(string))
				profile := call("GET", "/v1/m/sessions/provider-profiles/"+url.PathEscape(ref)+"/configuration", nil, 200)
				home, config := profile["user_home"].(string), profile["config_home"].(string)
				raw, err := os.ReadFile(filepath.Join(home, "login-observed.txt"))
				if err != nil || string(raw) != home+"\n"+config {
					t.Fatalf("login used another home: %s (%v)", raw, err)
				}
			}
			for i, id := range ids {
				flow := call("GET", "/v1/m/agenttools/sign-in/"+id, nil, 200)
				if flow["account_ref"] != refs[i] || flow["state"] != "needs_code" {
					t.Fatalf("second account cancelled the first: %v", flow)
				}
			}
			call("GET", "/v1/m/agenttools/sign-in?driver=codex&tenant_id="+tenant+"&account_ref="+refs[0], nil, 409)
			call("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id="+tenant+"&account_ref=ppf_missing", nil, 404)
			for _, id := range ids {
				call(http.MethodDelete, "/v1/m/agenttools/sign-in/"+id, nil, 200)
			}
		})
	}
}
