// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// N2 J8 on 09: on the default PostgreSQL install (the application and owner roles,
// no BYPASSRLS admin pool, no tenant inventory) the AI tools sign-in status and the
// provider-profile resolve answered 503, so no session could start: the tools'
// login home and the local Ollama's registration enumerated every tenant
// (ListOrgs), which that install refuses. They now ask whether this node serves
// the ONE tenant the request names (servesTenant). The same refusal stays for a
// tenant this node does not serve and for the system tenant; SQLite answers the same.
func TestToolLoginHomeAndLocalOllamaServeTheCallersTenant(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	for _, engineName := range []string{"postgres", "sqlite"} {
		t.Run(engineName, func(t *testing.T) {
			folder, home, dir := t.TempDir(), t.TempDir(), t.TempDir()
			for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
				t.Setenv(name, "")
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			// A Claude Code stub that is signed in only in a product login home,
			// <data>/tool-logins/<tenant>/claude, and names that tenant as its account.
			claude := filepath.Join(folder, "claude.py")
			stub := "#!" + python + "\nimport json, os, sys\n" +
				"parts = os.environ.get('HOME', '').rstrip('/').split('/')\n" +
				"ok = len(parts) >= 3 and parts[-3] == 'tool-logins' and parts[-1] == 'claude'\n" +
				"if sys.argv[1:3] == ['auth', 'status']:\n" +
				"    print(json.dumps({'loggedIn': ok, 'authMethod': 'claude.ai', 'email': parts[-2] if ok else ''}))\n"
			if err := os.WriteFile(claude, []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(envSessionClaudeBin, claude)

			cfg := bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true}
			if engineName == "postgres" {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.Engine, cfg.DSN, cfg.OwnerDSN = "postgres", pg.App, pg.Owner
			}
			eng, err := boot(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })

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
			setupResult := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "pg@olivares.ai", "password": "fixture-password-2026!"}, http.StatusCreated)
			login := do("POST", "/v1/auth/login", map[string]any{"email": "pg@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
			admin, _ = login["token"].(string)
			org, _ := setupResult["organization"].(map[string]any)
			tenant, _ = org["tenant_id"].(string)

			// AI tools: the caller's tenant's own login, read in its own home.
			status := do("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id="+url.QueryEscape(tenant), nil, http.StatusOK)
			if status["installed"] != true || status["signed_in"] != true || status["account"] != tenant {
				t.Fatalf("sign-in status = %v, want installed and signed in, in the home of tenant %s", status, tenant)
			}
			loginHome := filepath.Join(dir, toolLoginsDir, tenant, "claude")
			if info, err := os.Stat(loginHome); err != nil || !info.IsDir() {
				t.Fatalf("the tool login home %s = %v, want a directory created for the caller's tenant", loginHome, err)
			}
			// The resolve rule reads the same answer: preview and resolve both say own login.
			if got := do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=claude", nil, http.StatusOK); got["reason"] != "own_login" {
				t.Fatalf("GET resolve = %v, want own_login", got)
			}
			resolved := do("POST", "/v1/m/sessions/provider-profiles/resolve", map[string]any{"driver": "claude"}, http.StatusOK)
			profile, _ := resolved["profile"].(map[string]any)
			if resolved["reason"] != "own_login" || profile["profile_ref"] == nil || profile["driver"] != "claude" {
				t.Fatalf("POST resolve = %v, want a claude profile on its own login", resolved)
			}

			// A tenant this node does not serve, and the system tenant, are refused as before.
			unserved := model.NewTenantID().String()
			do("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id="+url.QueryEscape(unserved), nil, http.StatusServiceUnavailable)
			if _, err := os.Stat(filepath.Join(dir, toolLoginsDir, unserved)); !os.IsNotExist(err) {
				t.Fatalf("an unserved tenant got a login home: %v", err)
			}
			do("GET", "/v1/m/agenttools/sign-in?driver=claude&tenant_id="+url.QueryEscape(model.SystemTenantID.String()), nil, http.StatusBadRequest)

			// The local Ollama is registered in the caller's tenant only.
			p, err := eng.authr.Authenticate(context.Background(), admin)
			if err != nil {
				t.Fatal(err)
			}
			register := registerLocalOllama(eng.sessionsMod, eng.store)
			if err := register(context.Background(), p, model.TenantID(tenant), "http://127.0.0.1:11434"); err != nil {
				t.Fatalf("register Ollama in the caller's tenant: %v", err)
			}
			records, _, err := eng.sessionsMod.ListProviderRecords(context.Background(), model.TenantID(tenant), "active", sessions.ProviderKindOllama, model.Query{Limit: 10})
			if err != nil || len(records) != 1 || records[0].DisplayName != localOllamaName {
				t.Fatalf("Ollama records in the caller's tenant = %+v (%v), want one %q", records, err, localOllamaName)
			}
			for _, other := range []model.TenantID{model.TenantID(unserved), model.SystemTenantID} {
				if err := register(context.Background(), p, other, "http://127.0.0.1:11434"); err == nil ||
					!strings.Contains(err.Error(), "add its endpoint in Providers") {
					t.Fatalf("register Ollama in %s = %v, want the refusal", other, err)
				}
			}
		})
	}
}
